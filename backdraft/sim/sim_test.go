package sim

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
)

func TestCanceledSimulationsReleaseWorkers(t *testing.T) {
	m := chain(threeTries, patientUser, fourWorkers)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunContext(ctx, m, Brownout(200, "back", 300, 8, 8, 30, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run: %v", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer stop()
	_, err := StabilityMapContext(ctx, m, MapSpec{Service: "back", LatencyMs: 300,
		Loads: []float64{1000, 2000, 5000}, Durations: []float64{100, 200}, ObserveS: 300})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("map ignored deadline: %v", err)
	}
}

// chain is users -> front -> back, with back as the only constrained
// service: 4 workers at 10ms, so about 400 req/s.
func chain(front, users backdraft.ClientPolicy, back backdraft.ServerPolicy) *Model {
	return &Model{
		Entry: "users",
		Ops:   []Op{{Name: "Get", Service: "front", Weight: 1}},
		Services: map[string]ServiceModel{
			"users": {Config: backdraft.ServiceConfig{Clients: map[string]backdraft.ClientPolicy{"front": users}}},
			"front": {
				Config:     backdraft.ServiceConfig{Clients: map[string]backdraft.ClientPolicy{"back": front}},
				MeanSelfMs: 1,
				Shapes:     map[string][]Shape{"Get": {{{{Peer: "back", Method: "Read"}}}}},
			},
			"back": {Config: backdraft.ServiceConfig{Server: back}, MeanSelfMs: 10},
		},
	}
}

var (
	patientUser = backdraft.ClientPolicy{TimeoutMs: 1000, MaxAttempts: 1, PropagateDeadline: true}
	oneTry      = backdraft.ClientPolicy{TimeoutMs: 200, MaxAttempts: 1, PropagateDeadline: true}
	threeTries  = backdraft.ClientPolicy{TimeoutMs: 200, MaxAttempts: 3, BackoffMs: 10, BackoffMaxMs: 40,
		RetryOn: []string{"DEADLINE_EXCEEDED", "UNAVAILABLE"}, PropagateDeadline: false}
	fourWorkers = backdraft.ServerPolicy{Workers: 4}
)

func TestDeterministic(t *testing.T) {
	m := chain(threeTries, patientUser, fourWorkers)
	sc := Brownout(250, "back", 100, 3, 3, 6, 42)
	a, b := Run(m, sc), Run(m, sc)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different results")
	}
}

func TestPolicyChangesDoNotChangeOfferedWorkload(t *testing.T) {
	sc := Brownout(250, "back", 100, 3, 3, 6, 42)
	a := Run(chain(threeTries, patientUser, fourWorkers), sc)
	b := Run(chain(oneTry, patientUser, fourWorkers), sc)
	if a.Truncated || b.Truncated || !reflect.DeepEqual(a.Offered, b.Offered) {
		t.Fatal("paired comparison did not preserve incoming traffic")
	}
	if reflect.DeepEqual(a.Goodput, b.Goodput) {
		t.Fatal("test must exercise policies with different outcomes")
	}
}

func TestLightLoadIsHealthyAndMatchesLittlesLaw(t *testing.T) {
	m := chain(oneTry, patientUser, fourWorkers)
	// Utilization law: busy workers = arrival rate x service time
	// = 100/s x 10ms = 1.
	var busy float64
	var n int
	// Gauge snapshots have sampling variance; use multiple independent
	// ingress seeds instead of relying on a lucky single trajectory.
	for seed := uint64(1); seed <= 10; seed++ {
		r := Run(m, Scenario{DurationS: 30, Load: []LoadPoint{{RPS: 100}}, Seed: seed})
		if r.Verdict.Kind != "steady" || r.Verdict.FinalRatio < 0.99 {
			t.Fatalf("seed %d: %+v", seed, r.Verdict)
		}
		for i, at := range r.T {
			if at > 5 {
				busy += r.Services["back"].Busy[i]
				n++
			}
		}
	}
	if mean := busy / float64(n); math.Abs(mean-1) > 0.15 {
		t.Fatalf("mean busy workers = %.2f, want about 1", mean)
	}
}

func TestFinalDipCannotBeReportedAsZeroSecondRecovery(t *testing.T) {
	r := &Result{StepS: 1, T: make([]float64, 40), Offered: make([]float64, 40), Goodput: make([]float64, 40), Services: map[string]*ServiceSeries{}}
	for i := range r.T {
		r.T[i] = float64(i)
		r.Offered[i] = 100
		r.Goodput[i] = 100
	}
	r.Goodput[39] = 79
	v := judge(r, Brownout(100, "back", 100, 8, 2, 30, 1), chain(oneTry, patientUser, fourWorkers))
	if v.RecoveryS >= 0 {
		t.Fatalf("last rolling window was below recovery threshold: %+v", v)
	}
}

func TestCapacityFindsBottleneck(t *testing.T) {
	rps, b := Capacity(chain(oneTry, patientUser, fourWorkers))
	if b != "back" || math.Abs(rps-400) > 40 {
		t.Fatalf("capacity = %.0f at %q, want about 400 at back", rps, b)
	}
}

// The central behaviour: the same brownout heals without retries and
// becomes self-sustaining with them. A retry budget breaks the loop;
// shedding expired work alone does not when retries triple arrivals past
// capacity, because a FIFO queue at its deadline serves only requests that
// will finish late.
func TestRetriesTurnABrownoutMetastable(t *testing.T) {
	sc := Brownout(300, "back", 150, 5, 5, 25, 3)
	shed := fourWorkers
	shed.ShedExpired = true
	budgeted := threeTries
	budgeted.Budget = &backdraft.BudgetPolicy{Ratio: 0.1, MinPerSec: 1}

	for _, tc := range []struct {
		name   string
		client backdraft.ClientPolicy
		server backdraft.ServerPolicy
		want   []string
	}{
		{"no retries", oneTry, fourWorkers, []string{"recovered"}},
		{"three tries", threeTries, fourWorkers, []string{"metastable"}},
		{"three tries, shed expired", threeTries, shed, []string{"metastable"}},
		{"budgeted", budgeted, fourWorkers, []string{"recovered", "recovering"}},
		{"budgeted, shed expired", budgeted, shed, []string{"recovered"}},
	} {
		v := Run(chain(tc.client, patientUser, tc.server), sc).Verdict
		ok := false
		for _, w := range tc.want {
			ok = ok || v.Kind == w
		}
		if !ok {
			t.Errorf("%s: %s, want %v (%s)", tc.name, v.Kind, tc.want, v.Summary)
		}
		if v.Kind == "metastable" && v.Bottleneck != "back" {
			t.Errorf("%s: bottleneck %q, want back", tc.name, v.Bottleneck)
		}
	}
}

func TestRetryBudgetBoundsAttempts(t *testing.T) {
	budgeted := threeTries
	budgeted.Budget = &backdraft.BudgetPolicy{Ratio: 0.1}
	// back always fails, so every request wants all three attempts.
	m := chain(budgeted, patientUser, fourWorkers)
	r := Run(m, Scenario{DurationS: 30, Load: []LoadPoint{{RPS: 50}},
		Faults: []FaultWindow{{Service: "back", StartS: 0, EndS: 30, ErrorRate: 1}}, Seed: 5})
	var attempts, offered float64
	for i, t := range r.T {
		if t > 12 {
			attempts += r.Edges["front>back"].Attempts[i]
			offered += r.Offered[i]
		}
	}
	if amp := attempts / offered; amp > 1.15 {
		t.Fatalf("amplification under budget = %.2f, want <= 1.1", amp)
	}
}

func TestQueueLimitSheds(t *testing.T) {
	bounded := fourWorkers
	bounded.QueueLimit = 8
	r := Run(chain(oneTry, patientUser, bounded), Scenario{DurationS: 10, Load: []LoadPoint{{RPS: 800}}, Seed: 2})
	var shed, peak float64
	for i := range r.T {
		shed += r.Services["back"].Shed[i]
		peak = math.Max(peak, r.Services["back"].Queue[i])
	}
	if shed == 0 || peak > 8 {
		t.Fatalf("shed=%.0f peak queue=%.0f, want shedding and queue <= 8", shed, peak)
	}
}

// Ramping load past capacity and back: without retries the system comes
// back down the way it went up; with them it stays failed at loads it
// handled fine on the way up.
func TestHysteresis(t *testing.T) {
	shed := fourWorkers
	shed.ShedExpired = true
	endRatio := func(r *Result) float64 {
		var g, o float64
		for i := len(r.T) - 10; i < len(r.T); i++ {
			g += r.Goodput[i]
			o += r.Offered[i]
		}
		return g / o
	}
	if got := endRatio(Hysteresis(chain(oneTry, patientUser, shed), 50, 600, 60, 9)); got < 0.95 {
		t.Errorf("no retries: goodput ratio back at low load = %.2f, want >= 0.95", got)
	}
	if got := endRatio(Hysteresis(chain(threeTries, patientUser, fourWorkers), 100, 600, 60, 9)); got > 0.5 {
		t.Errorf("three tries: goodput ratio back at low load = %.2f, want the system stuck", got)
	}
}

func TestStabilityMapShape(t *testing.T) {
	cells := StabilityMap(chain(threeTries, patientUser, fourWorkers), MapSpec{
		Service: "back", LatencyMs: 150, Loads: []float64{50, 300}, Durations: []float64{1, 6}, ObserveS: 20,
	})
	if len(cells) != 4 {
		t.Fatalf("%d cells", len(cells))
	}
	get := func(load, dur float64) MapCell {
		for _, c := range cells {
			if c.Load == load && c.Duration == dur {
				return c
			}
		}
		t.Fatalf("no cell %v/%v", load, dur)
		return MapCell{}
	}
	if c := get(50, 6); c.Kind != "recovered" {
		t.Errorf("light load, long fault: %s", c.Kind)
	}
	if c := get(300, 6); c.Kind != "metastable" {
		t.Errorf("heavy load, long fault: %s", c.Kind)
	}
}

// Stopping a hopeless run early must not change its verdict.
func TestStopWhenStuckAgreesWithFullRun(t *testing.T) {
	m := chain(threeTries, patientUser, fourWorkers)
	for _, rps := range []float64{50, 300} {
		sc := Brownout(rps, "back", 150, 5, 5, 30, 3)
		full := Run(m, sc).Verdict.Kind
		sc.StopWhenStuck = true
		early := Run(m, sc)
		if early.Verdict.Kind != full {
			t.Errorf("%v req/s: early stop says %s, full run %s", rps, early.Verdict.Kind, full)
		}
		if rps == 300 && early.StoppedAt == 0 {
			t.Errorf("stuck run did not stop early")
		}
	}
}

// A model with no traffic yet (no ops) must simulate as idle, not panic.
func TestEmptyModel(t *testing.T) {
	m := &Model{Entry: "users", Services: map[string]ServiceModel{"users": {}}}
	if r := Run(m, Scenario{DurationS: 2, Load: []LoadPoint{{RPS: 100}}}); r.Events == 0 {
		t.Fatal("no sampling events")
	}
	if c, b := Capacity(m); c != 0 || b != "" {
		t.Fatalf("capacity of an empty model = %v at %q", c, b)
	}
}
