package analysis

import "github.com/SanishKumar/go-microservices/backdraft"

// Mitigations are the system-wide fixes for retry storms, each of which
// can be switched on independently to see what it buys.
type Mitigations struct {
	// RetryBudgets caps every retrying edge at 10% of its requests.
	RetryBudgets bool `json:"retryBudgets"`
	// SingleLayerRetries keeps retries only on calls made by the services
	// the entry talks to, and turns them off deeper in.
	SingleLayerRetries bool `json:"singleLayerRetries"`
	// PropagateDeadlines makes every call inherit its caller's deadline.
	PropagateDeadlines bool `json:"propagateDeadlines"`
	// ShedExpired drops queued requests whose callers have given up.
	ShedExpired bool `json:"shedExpired"`
	// BoundedQueues limits each queue to four times its worker count.
	BoundedQueues bool `json:"boundedQueues"`
}

// All returns every mitigation switched on.
func All() Mitigations {
	return Mitigations{true, true, true, true, true}
}

// Mitigate returns sys with the selected mitigations applied. Mitigations
// that are off leave the corresponding settings untouched, and the entry
// service (which models users) is never changed.
func Mitigate(sys backdraft.SystemConfig, m Mitigations) backdraft.SystemConfig {
	out := backdraft.SystemConfig{Entry: sys.Entry, Services: map[string]backdraft.ServiceConfig{}}
	edgeOfEntry := map[string]bool{}
	for _, peer := range sys.Services[sys.Entry].Peers() {
		edgeOfEntry[peer] = true
	}
	for name, cfg := range sys.Services {
		cfg = cfg.Clone()
		if name == sys.Entry {
			out.Services[name] = cfg
			continue
		}
		for peer, p := range cfg.Clients {
			if m.SingleLayerRetries && !edgeOfEntry[name] {
				p.MaxAttempts = 1
			}
			if m.RetryBudgets && p.MaxAttempts > 1 && p.Budget == nil {
				p.Budget = defaultBudget(nil)
			}
			if m.PropagateDeadlines {
				p.PropagateDeadline = true
			}
			cfg.Clients[peer] = p
		}
		if cfg.Server.Workers > 0 {
			if m.ShedExpired {
				cfg.Server.ShedExpired = true
			}
			if m.BoundedQueues && cfg.Server.QueueLimit == 0 {
				cfg.Server.QueueLimit = 4 * cfg.Server.Workers
			}
		}
		out.Services[name] = cfg
	}
	return out
}

// Detect reports which mitigations are fully in effect in sys.
func Detect(sys backdraft.SystemConfig) Mitigations {
	m := All()
	edgeOfEntry := map[string]bool{}
	for _, peer := range sys.Services[sys.Entry].Peers() {
		edgeOfEntry[peer] = true
	}
	for name, cfg := range sys.Services {
		if name == sys.Entry {
			continue
		}
		for _, p := range cfg.Clients {
			if p.MaxAttempts > 1 && p.Budget == nil {
				m.RetryBudgets = false
			}
			if p.MaxAttempts > 1 && !edgeOfEntry[name] {
				m.SingleLayerRetries = false
			}
			if !p.PropagateDeadline {
				m.PropagateDeadlines = false
			}
		}
		if cfg.Server.Workers > 0 {
			if !cfg.Server.ShedExpired {
				m.ShedExpired = false
			}
			if cfg.Server.QueueLimit == 0 {
				m.BoundedQueues = false
			}
		}
	}
	return m
}
