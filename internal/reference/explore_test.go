package reference

import (
	"testing"

	"github.com/SanishKumar/go-microservices/backdraft/analysis"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

// The reference system's headline behaviour, which the lab demo and the
// README rely on: at about a fifth of its capacity, a few seconds of
// catalog slowness takes it down for good, and retry budgets alone are
// enough to make the same incident heal by itself.
func TestBrownoutIsMetastableUntilMitigated(t *testing.T) {
	declared := SimModel(System())
	capacity, bottleneck := sim.Capacity(declared)
	if bottleneck != Catalog || capacity < 700 || capacity > 1100 {
		t.Fatalf("capacity %.0f req/s at %s, want roughly 900 at catalog", capacity, bottleneck)
	}

	sc := sim.Brownout(200, Catalog, 300, 8, 8, 30, 1)
	if v := sim.Run(declared, sc).Verdict; v.Kind != "metastable" || v.Bottleneck != Catalog {
		t.Errorf("declared policy: %s at %q (%s)", v.Kind, v.Bottleneck, v.Summary)
	}
	for _, tc := range []struct {
		name        string
		m           analysis.Mitigations
		maxRecovery float64
	}{
		// Budgets cut the retry loop; the backlog still has to drain.
		{"retry budgets", analysis.Mitigations{RetryBudgets: true}, 10},
		// Shedding expired work drains it almost at once.
		{"all mitigations", analysis.All(), 3},
	} {
		v := sim.Run(SimModel(analysis.Mitigate(System(), tc.m)), sc).Verdict
		if v.Kind != "recovered" || v.RecoveryS > tc.maxRecovery {
			t.Errorf("%s: %s after %.1fs (%s)", tc.name, v.Kind, v.RecoveryS, v.Summary)
		}
	}

	// Light load rides the same incident out.
	if v := sim.Run(declared, sim.Brownout(100, Catalog, 300, 8, 8, 30, 1)).Verdict; v.Kind != "recovered" {
		t.Errorf("100 req/s: %s (%s)", v.Kind, v.Summary)
	}
}
