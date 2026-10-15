package sim

import (
	"math"
	"runtime"
	"runtime/debug"
	"sync"
)

// Parallel simulations allocate quickly against a small live heap, so at
// the default GC target they spend most of their time collecting. Bursts
// of them raise the target and put it back when the last one finishes.
var (
	gcMu      sync.Mutex
	gcBursts  int
	gcRestore int
)

func burst() func() {
	gcMu.Lock()
	if gcBursts == 0 {
		gcRestore = debug.SetGCPercent(-1)
		debug.SetGCPercent(max(gcRestore, 400))
	}
	gcBursts++
	gcMu.Unlock()
	return func() {
		gcMu.Lock()
		gcBursts--
		if gcBursts == 0 {
			debug.SetGCPercent(gcRestore)
		}
		gcMu.Unlock()
	}
}

// Brownout is the canonical incident: steady load, a service slows down
// for faultS seconds, then the run continues for observeS seconds to see
// whether the system comes back.
func Brownout(rps float64, service string, latencyMs, warmS, faultS, observeS float64, seed uint64) Scenario {
	return Scenario{
		DurationS: warmS + faultS + observeS,
		Load:      []LoadPoint{{T: 0, RPS: rps}},
		Faults:    []FaultWindow{{Service: service, StartS: warmS, EndS: warmS + faultS, LatencyMs: latencyMs}},
		Seed:      seed,
	}
}

// Capacity estimates the highest entry rate the model can sustain when
// healthy, using the utilization law: run light load, measure each
// service's demand (worker-seconds per entry request), and divide its
// workers by that demand. The smallest quotient is the bottleneck.
func Capacity(m *Model) (rps float64, bottleneck string) {
	if m.Validate() != nil {
		return 0, ""
	}
	const load, dur = 5.0, 40.0
	r := Run(m, Scenario{DurationS: dur, Load: []LoadPoint{{RPS: load}}, Seed: 7})
	entries := 0.0
	for _, x := range r.Offered {
		entries += x * r.StepS
	}
	if entries == 0 {
		return 0, ""
	}
	rps = math.Inf(1)
	for _, name := range sortedNames(r.Services) {
		s := r.Services[name]
		if s.Workers == 0 || s.busySeconds == 0 {
			continue
		}
		demand := s.busySeconds / entries
		if c := float64(s.Workers) / demand; c < rps {
			rps, bottleneck = c, name
		}
	}
	if math.IsInf(rps, 1) {
		return 0, ""
	}
	return rps, bottleneck
}

// MapSpec describes a stability map: a brownout of Service by LatencyMs,
// repeated for every load and incident duration.
type MapSpec struct {
	Service   string    `json:"service"`
	LatencyMs float64   `json:"latencyMs"`
	Loads     []float64 `json:"loads"`
	Durations []float64 `json:"durations"`
	WarmS     float64   `json:"warmS"`
	ObserveS  float64   `json:"observeS"`
	Seed      uint64    `json:"seed"`
}

// MapCell is one brownout's outcome.
type MapCell struct {
	Load       float64 `json:"load"`
	Duration   float64 `json:"duration"`
	Kind       string  `json:"kind"`
	RecoveryS  float64 `json:"recoveryS"`
	FinalRatio float64 `json:"finalRatio"`
	MinRatio   float64 `json:"minRatio"`
}

// StabilityMap runs every cell of spec in parallel. The boundary between
// recovered and metastable cells is where the system stops healing on
// its own: below it an incident ends when its trigger does, above it the
// incident needs an operator.
func StabilityMap(m *Model, spec MapSpec) []MapCell {
	if spec.WarmS <= 0 {
		spec.WarmS = 6
	}
	if spec.ObserveS <= 0 {
		spec.ObserveS = 25
	}
	defer burst()()
	cells := make([]MapCell, 0, len(spec.Loads)*len(spec.Durations))
	for _, d := range spec.Durations {
		for _, l := range spec.Loads {
			cells = append(cells, MapCell{Load: l, Duration: d})
		}
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.GOMAXPROCS(0); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				c := &cells[i]
				sc := Brownout(c.Load, spec.Service, spec.LatencyMs, spec.WarmS, c.Duration, spec.ObserveS, spec.Seed+uint64(i))
				sc.StopWhenStuck = true
				v := Run(m, sc).Verdict
				c.Kind, c.RecoveryS, c.FinalRatio, c.MinRatio = v.Kind, v.RecoveryS, v.FinalRatio, v.MinRatio
			}
		}()
	}
	for i := range cells {
		work <- i
	}
	close(work)
	wg.Wait()
	return cells
}

// Hysteresis ramps load from lo to hi and back over periodS with no fault.
// Plotting goodput against offered load for both halves shows whether the
// system returns along the path it came: a gap between the two curves is
// the metastable region, loads at which the system is healthy if it gets
// there from below and stuck if it gets there from above.
func Hysteresis(m *Model, lo, hi, periodS float64, seed uint64) *Result {
	return Run(m, Scenario{
		DurationS: periodS,
		Load:      []LoadPoint{{T: 0, RPS: lo}, {T: periodS / 2, RPS: hi}, {T: periodS, RPS: lo}},
		Seed:      seed,
		StepS:     0.5,
	})
}
