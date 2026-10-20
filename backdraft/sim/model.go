// Package sim is a discrete-event simulator of a service graph under the
// same policies the Backdraft agent enforces: per-attempt timeouts,
// retries with jittered backoff, retry budgets, deadline propagation, and
// worker pools with FIFO queues that hold a worker for the whole request,
// downstream calls included.
//
// Calibrated from live traffic, it answers the question a dashboard
// cannot: if this slowdown happens, does the system come back by itself
// when it ends, or do retries keep it down? It simulates a minute of
// traffic in milliseconds, so it can sweep load and incident size to map
// where a system stops being able to heal.
package sim

import (
	"fmt"
	"math"

	"github.com/SanishKumar/go-microservices/backdraft"
)

// Model describes a system well enough to replay it.
type Model struct {
	// Entry is the pseudo-service that originates traffic. Its client
	// policies model how users time out and retry.
	Entry    string                  `json:"entry"`
	Services map[string]ServiceModel `json:"services"`
	// Ops are the requests the entry makes, mixed by weight.
	Ops []Op `json:"ops"`
}

// ServiceModel is one service: its policies and how it spends time.
type ServiceModel struct {
	Config backdraft.ServiceConfig `json:"config"`
	// SelfMs are samples of the time a request spends in the service
	// itself, excluding downstream calls. The simulator draws from them,
	// so the latency distribution is the observed one, tail included.
	SelfMs []float64 `json:"selfMs,omitempty"`
	// MeanSelfMs is used, with log-normal spread, when there are no
	// samples.
	MeanSelfMs float64 `json:"meanSelfMs"`
	// Shapes are the downstream calls each method makes, keyed by method.
	// A request picks one observed shape at random.
	Shapes map[string][]Shape `json:"shapes,omitempty"`
}

// Shape is a request's downstream calls: groups run one after another,
// calls within a group run in parallel.
type Shape [][]Call

// Call is one downstream call.
type Call struct {
	Peer   string `json:"peer"`
	Method string `json:"method"`
}

// Op is one kind of request the entry sends.
type Op struct {
	Name    string  `json:"name"`
	Service string  `json:"service"`
	Weight  float64 `json:"weight"`
}

// Validate checks that every reference in the model resolves.
func (m *Model) Validate() error {
	if m == nil {
		return fmt.Errorf("model is nil")
	}
	if err := m.System().Validate(); err != nil {
		return err
	}
	if _, ok := m.Services[m.Entry]; !ok {
		return fmt.Errorf("entry %q is not a service", m.Entry)
	}
	if len(m.Ops) == 0 {
		return fmt.Errorf("model has no ops")
	}
	total := 0.0
	for _, op := range m.Ops {
		if _, ok := m.Services[op.Service]; !ok {
			return fmt.Errorf("op %s targets unknown service %q", op.Name, op.Service)
		}
		if !validTime(op.Weight) {
			return fmt.Errorf("op %s has invalid weight", op.Name)
		}
		total += op.Weight
	}
	if !validTime(total) || total <= 0 {
		return fmt.Errorf("ops have no weight")
	}
	for name, s := range m.Services {
		if err := s.Config.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !validTime(s.MeanSelfMs) {
			return fmt.Errorf("%s has invalid mean self time", name)
		}
		for _, ms := range s.SelfMs {
			if !validTime(ms) {
				return fmt.Errorf("%s has invalid self time sample", name)
			}
		}
		for method, shapes := range s.Shapes {
			for _, sh := range shapes {
				for _, g := range sh {
					for _, c := range g {
						if _, ok := m.Services[c.Peer]; !ok {
							return fmt.Errorf("%s.%s calls unknown service %q", name, method, c.Peer)
						}
					}
				}
			}
		}
	}
	return nil
}

func validTime(n float64) bool { return n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }

// WithConfigs returns a copy of m with the given service configurations
// swapped in, to ask "what if the policy were this instead".
func (m *Model) WithConfigs(sys map[string]backdraft.ServiceConfig) *Model {
	out := &Model{Entry: m.Entry, Ops: m.Ops, Services: map[string]ServiceModel{}}
	for name, s := range m.Services {
		if cfg, ok := sys[name]; ok {
			s.Config = cfg.Clone()
		}
		out.Services[name] = s
	}
	return out
}

// System returns the model's policies as a SystemConfig.
func (m *Model) System() backdraft.SystemConfig {
	sys := backdraft.SystemConfig{Entry: m.Entry, Services: map[string]backdraft.ServiceConfig{}}
	for name, s := range m.Services {
		sys.Services[name] = s.Config.Clone()
	}
	return sys
}
