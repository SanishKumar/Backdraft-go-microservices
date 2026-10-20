package backdraft

import (
	"math"
	"testing"
)

func TestRejectInvalidPolicies(t *testing.T) {
	for name, cfg := range map[string]ServiceConfig{
		"negative work":            {Server: ServerPolicy{WorkMs: -1}},
		"nonfinite work":           {Server: ServerPolicy{WorkMs: math.NaN()}},
		"negative inbound timeout": {Server: ServerPolicy{RequestTimeoutMs: -1}},
		"negative latency":         {Fault: Fault{LatencyMs: -1}},
		"nonfinite errors":         {Fault: Fault{ErrorRate: math.Inf(1)}},
		"negative attempts":        {Clients: map[string]ClientPolicy{"db": {MaxAttempts: -1}}},
		"nonfinite budget":         {Clients: map[string]ClientPolicy{"db": {Budget: &BudgetPolicy{Ratio: math.NaN()}}}},
		"overflowed timeout":       {Clients: map[string]ClientPolicy{"db": {TimeoutMs: math.MaxInt64}}},
	} {
		t.Run(name, func(t *testing.T) {
			if cfg.Validate() == nil {
				t.Fatal("accepted an unenforceable policy")
			}
		})
	}
}

func TestSystemRejectsUnknownDependencies(t *testing.T) {
	sys := SystemConfig{Entry: "api", Services: map[string]ServiceConfig{
		"api": {Clients: map[string]ClientPolicy{"typo": {MaxAttempts: 1}}},
	}}
	if sys.Validate() == nil {
		t.Fatal("accepted an unknown dependency")
	}
}
