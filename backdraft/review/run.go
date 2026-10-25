package review

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

type Scenario struct {
	Service  string  `json:"service"`
	Load     float64 `json:"load"`
	Duration float64 `json:"duration"`
	Seed     uint64  `json:"seed"`
}
type Outcome struct {
	Status        string   `json:"status"`
	FinalRatio    float64  `json:"finalRatio"`
	BaselineRatio float64  `json:"baselineRatio"`
	RecoveryS     float64  `json:"recoveryS"`
	PeakQueue     float64  `json:"peakQueue"`
	Bottleneck    string   `json:"bottleneck"`
	Reasons       []string `json:"reasons"`
}
type Case struct {
	Scenario   Scenario `json:"scenario"`
	Baseline   Outcome  `json:"baseline"`
	Candidate  Outcome  `json:"candidate"`
	Regression bool     `json:"regression"`
}
type Series struct {
	T       []float64 `json:"t"`
	Offered []float64 `json:"offered"`
	Goodput []float64 `json:"goodput"`
	Queue   []float64 `json:"queue"`
}
type Witness struct {
	CaseIndex int    `json:"caseIndex"`
	Side      string `json:"side"`
	Baseline  Series `json:"baseline"`
	Candidate Series `json:"candidate"`
}
type Report struct {
	Runtime         string    `json:"runtime"`
	Status          string    `json:"status"`
	BundleDigest    string    `json:"bundleDigest"`
	ModelDigest     string    `json:"modelDigest"`
	CreatedAt       time.Time `json:"createdAt"`
	Cases           []Case    `json:"cases"`
	BaselinePassed  int       `json:"baselinePassed"`
	CandidatePassed int       `json:"candidatePassed"`
	Regressions     int       `json:"regressions"`
	Warnings        []string  `json:"warnings"`
	Changes         []Change  `json:"changes"`
	Witness         *Witness  `json:"witness,omitempty"`
}

// Run evaluates the complete finite envelope. Both policies receive identical
// ingress timing and operation choices for each seed; no live policies change.
// Cases are sorted by duration, load, service and seed. The witness is the
// smallest *tested* failure, never a claim of a global minimum.
func Run(ctx context.Context, b Bundle) (*Report, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := &Report{Runtime: runtime.Version() + "/" + runtime.GOOS + "/" + runtime.GOARCH, Status: "pass", BundleDigest: Digest(b), ModelDigest: Digest(b.Model), CreatedAt: time.Now().UTC(), Cases: []Case{}, Warnings: b.Warnings(), Changes: b.Changes()}
	for _, service := range b.Contract.Services {
		for _, load := range b.Contract.Loads {
			for _, duration := range b.Contract.Durations {
				for _, seed := range b.Contract.Seeds {
					r.Cases = append(r.Cases, Case{Scenario: Scenario{service, load, duration, seed}})
				}
			}
		}
	}
	sort.Slice(r.Cases, func(i, j int) bool {
		a, z := r.Cases[i].Scenario, r.Cases[j].Scenario
		if a.Duration != z.Duration {
			return a.Duration < z.Duration
		}
		if a.Load != z.Load {
			return a.Load < z.Load
		}
		if a.Service != z.Service {
			return a.Service < z.Service
		}
		return a.Seed < z.Seed
	})
	candidate := b.Model.WithConfigs(b.Candidate.Services)
	work := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var runErr error
	witnessRank := 3
	for w := 0; w < min(2, len(r.Cases)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range work {
				c := &r.Cases[index]
				s := c.Scenario
				sc := sim.Brownout(s.Load, s.Service, b.Contract.LatencyMs, b.Contract.WarmS, s.Duration, b.Contract.ObserveS, s.Seed)
				sc.StepS = 0.5
				sc.Faults[0].ErrorRate = b.Contract.ErrorRate
				base, err := sim.RunContext(ctx, b.Model, sc)
				if err != nil {
					mu.Lock()
					if runErr == nil {
						runErr = err
					}
					mu.Unlock()
					continue
				}
				next, err := sim.RunContext(ctx, candidate, sc)
				if err != nil {
					mu.Lock()
					if runErr == nil {
						runErr = err
					}
					mu.Unlock()
					continue
				}
				c.Baseline, c.Candidate = judge(base, b.Contract), judge(next, b.Contract)
				c.Regression = c.Baseline.Status == "pass" && c.Candidate.Status == "fail"
				rank, side := 2, "representative"
				if c.Candidate.Status != "pass" {
					rank, side = 0, "candidate"
				} else if c.Baseline.Status != "pass" {
					rank, side = 1, "baseline"
				}
				mu.Lock()
				if rank < witnessRank || rank == witnessRank && (r.Witness == nil || index < r.Witness.CaseIndex) {
					witnessRank = rank
					r.Witness = &Witness{index, side, series(base), series(next)}
				}
				mu.Unlock()
			}
		}()
	}
	for i := range r.Cases {
		select {
		case work <- i:
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return nil, ctx.Err()
		}
	}
	close(work)
	wg.Wait()
	if runErr != nil {
		return nil, runErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, c := range r.Cases {
		if c.Baseline.Status == "pass" {
			r.BaselinePassed++
		}
		if c.Candidate.Status == "pass" {
			r.CandidatePassed++
		}
		if c.Regression {
			r.Regressions++
		}
		if c.Candidate.Status == "fail" {
			r.Status = "fail"
		} else if c.Candidate.Status == "inconclusive" && r.Status == "pass" {
			r.Status = "inconclusive"
		}
	}
	if b.Evidence.Source == "capture" && len(r.Warnings) > 1 && r.Status == "pass" {
		r.Status = "inconclusive"
	}
	return r, nil
}

func series(r *sim.Result) Series {
	s := Series{T: r.T, Offered: r.Offered, Goodput: r.Goodput, Queue: make([]float64, len(r.T))}
	// Sorted addition keeps the exported evidence deterministic.
	var names []string
	for name := range r.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for i, q := range r.Services[name].Queue {
			s.Queue[i] += q
		}
	}
	return s
}

func judge(r *sim.Result, c Contract) Outcome {
	v := r.Verdict
	o := Outcome{Status: "pass", FinalRatio: v.FinalRatio, BaselineRatio: v.BaselineRatio, RecoveryS: v.RecoveryS, Bottleneck: v.Bottleneck, Reasons: []string{}}
	o.PeakQueue = float64(r.PeakQueue)
	if r.Truncated || r.StoppedAt > 0 {
		o.Status = "inconclusive"
		o.Reasons = append(o.Reasons, "Simulation resource limit reached; no passing result can be claimed.")
		return o
	}
	if v.BaselineRatio < 0.9 {
		o.Reasons = append(o.Reasons, fmt.Sprintf("Warmup success %.1f%% is below 90%%; this policy is already overloaded.", 100*v.BaselineRatio))
	}
	if v.FinalRatio < c.MinSuccessRatio {
		o.Reasons = append(o.Reasons, fmt.Sprintf("Final success %.1f%% is below %.1f%%.", 100*v.FinalRatio, 100*c.MinSuccessRatio))
	}
	if v.RecoveryS < 0 {
		o.Reasons = append(o.Reasons, "Did not recover within the observation window.")
	} else if v.RecoveryS > c.MaxRecoveryS {
		o.Reasons = append(o.Reasons, fmt.Sprintf("Recovery %.1fs exceeds %.1fs.", v.RecoveryS, c.MaxRecoveryS))
	}
	if o.PeakQueue > c.MaxQueue {
		o.Reasons = append(o.Reasons, fmt.Sprintf("Peak backlog %.0f exceeds %.0f.", o.PeakQueue, c.MaxQueue))
	}
	if len(o.Reasons) > 0 {
		o.Status = "fail"
	}
	return o
}
