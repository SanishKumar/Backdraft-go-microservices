// Package review evaluates a proposed policy against a frozen service model
// and a versioned recovery contract. It never sends traffic to live services.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/analysis"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

const Version = 1
const Engine = "paired-ingress-v1"

type Evidence struct {
	Source        string                    `json:"source"`
	CapturedAt    time.Time                 `json:"capturedAt"`
	SelfSamples   map[string]int            `json:"selfSamples"`
	ShapeSamples  map[string]int            `json:"shapeSamples"`
	MethodSamples map[string]map[string]int `json:"methodSamples"`
	OpsObserved   bool                      `json:"opsObserved"`
}

type Contract struct {
	Services        []string  `json:"services"`
	Loads           []float64 `json:"loads"`
	Durations       []float64 `json:"durations"`
	Seeds           []uint64  `json:"seeds"`
	LatencyMs       float64   `json:"latencyMs"`
	ErrorRate       float64   `json:"errorRate"`
	WarmS           float64   `json:"warmS"`
	ObserveS        float64   `json:"observeS"`
	MinSuccessRatio float64   `json:"minSuccessRatio"`
	MaxRecoveryS    float64   `json:"maxRecoveryS"`
	MaxQueue        float64   `json:"maxQueue"`
}

type Bundle struct {
	Version   int                    `json:"version"`
	Engine    string                 `json:"engine"`
	Name      string                 `json:"name"`
	Model     *sim.Model             `json:"model"`
	Candidate backdraft.SystemConfig `json:"candidate"`
	Contract  Contract               `json:"contract"`
	Evidence  Evidence               `json:"evidence"`
}

func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (b Bundle) Validate() error {
	if b.Version != Version || b.Engine != Engine {
		return fmt.Errorf("unsupported bundle version or engine; expected version %d, engine %s", Version, Engine)
	}
	if strings.TrimSpace(b.Name) == "" || len(b.Name) > 120 {
		return fmt.Errorf("name must contain 1-120 characters")
	}
	if err := b.Model.Validate(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if len(b.Model.Services) > 64 || len(b.Model.Ops) > 128 {
		return fmt.Errorf("model exceeds 64 services or 128 operations")
	}
	if err := b.Candidate.Validate(); err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	if b.Candidate.Entry != b.Model.Entry || len(b.Candidate.Services) != len(b.Model.Services) {
		return fmt.Errorf("candidate must preserve the captured entry and service graph")
	}
	for name, svc := range b.Model.Services {
		cfg, ok := b.Candidate.Services[name]
		if !ok || !reflect.DeepEqual(cfg.Peers(), svc.Config.Peers()) {
			return fmt.Errorf("candidate changes dependencies of %s; capture a new model for topology changes", name)
		}
		if cfg.Fault.Active() || svc.Config.Fault.Active() {
			return fmt.Errorf("clear injected faults before capturing or reviewing policies")
		}
		if cfg.Server.WorkMs != svc.Config.Server.WorkMs || !reflect.DeepEqual(cfg.Methods, svc.Config.Methods) {
			return fmt.Errorf("candidate must preserve %s service times and method semantics", name)
		}
		if len(svc.SelfMs) > 2000 || len(svc.Shapes) > 128 {
			return fmt.Errorf("%s has too many samples or methods", name)
		}
		for _, shapes := range svc.Shapes {
			if len(shapes) > 200 {
				return fmt.Errorf("%s has too many shape samples", name)
			}
			for _, shape := range shapes {
				if len(shape) > 64 {
					return fmt.Errorf("%s has too many call groups", name)
				}
				for _, group := range shape {
					if len(group) > 64 {
						return fmt.Errorf("%s has too many parallel calls", name)
					}
				}
			}
		}
	}
	c := b.Contract
	if len(c.Services) == 0 || len(c.Services) > 16 || len(c.Loads) == 0 || len(c.Loads) > 12 || len(c.Durations) == 0 || len(c.Durations) > 12 || len(c.Seeds) == 0 || len(c.Seeds) > 8 {
		return fmt.Errorf("specify 1-16 services, 1-12 loads, 1-12 durations and 1-8 seeds")
	}
	if len(c.Services)*len(c.Loads)*len(c.Durations)*len(c.Seeds) > 128 {
		return fmt.Errorf("contract exceeds 128 paired cases; narrow the search envelope")
	}
	if !unique(c.Services) || !unique(c.Loads) || !unique(c.Durations) || !unique(c.Seeds) {
		return fmt.Errorf("contract axes must not contain duplicate values")
	}
	for _, seed := range c.Seeds {
		if seed > 9007199254740991 {
			return fmt.Errorf("seeds must fit a JSON safe integer (0-9007199254740991)")
		}
	}
	for _, name := range c.Services {
		if _, ok := b.Model.Services[name]; !ok || name == b.Model.Entry {
			return fmt.Errorf("unknown or ingress fault target %q", name)
		}
	}
	for _, rps := range c.Loads {
		if !within(rps, 1, 5000) {
			return fmt.Errorf("loads must be within 1-5000 requests/sec")
		}
	}
	for _, d := range c.Durations {
		if !within(d, 0.5, 60) {
			return fmt.Errorf("fault durations must be within 0.5-60 seconds")
		}
	}
	if !within(c.LatencyMs, 0, 30000) || !within(c.ErrorRate, 0, 1) || c.LatencyMs == 0 && c.ErrorRate == 0 {
		return fmt.Errorf("specify latency (0-30000 ms) or an error rate (0-1)")
	}
	if !within(c.WarmS, 4, 30) || !within(c.ObserveS, 10, 120) {
		return fmt.Errorf("warmup must be 4-30s and observation 10-120s")
	}
	if !within(c.MinSuccessRatio, 0.5, 1) || !within(c.MaxRecoveryS, 0, c.ObserveS-2) || !within(c.MaxQueue, 0, 100000) {
		return fmt.Errorf("invalid contract limits: success 0.5-1, recovery at least 2s shorter than observation, queue 0-100000")
	}
	if b.Evidence.Source != "capture" && b.Evidence.Source != "declared" {
		return fmt.Errorf("evidence source must be capture or declared")
	}
	return nil
}

func within(v, lo, hi float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi }
func unique[T comparable](xs []T) bool {
	seen := map[T]bool{}
	for _, x := range xs {
		if seen[x] {
			return false
		}
		seen[x] = true
	}
	return true
}

// Warnings travel with the exported evidence; a sampled model is not a
// certificate of production reliability, even when every case passes.
func (b Bundle) Warnings() []string {
	out := []string{"Simulation evidence only. Validate important changes against real traffic before deployment."}
	if b.Evidence.Source == "declared" {
		return append(out, "Declared model: no live calibration was supplied.")
	}
	if !b.Evidence.OpsObserved {
		out = append(out, "The request mix has not been observed sufficiently.")
	}
	var missing []string
	for name, svc := range b.Model.Services {
		if name == b.Model.Entry && len(svc.SelfMs) == 0 {
			continue
		}
		if b.Evidence.SelfSamples[name] < 50 || len(svc.SelfMs) < 50 {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		out = append(out, "Insufficient healthy service-time samples: "+strings.Join(missing, ", "))
	}
	// Include methods reachable from both ingress and observed call shapes.
	// Aggregate sample counts alone can hide an unobserved, expensive path.
	required := map[string]map[string]bool{}
	add := func(service, method string) {
		if required[service] == nil {
			required[service] = map[string]bool{}
		}
		required[service][method] = true
	}
	for _, op := range b.Model.Ops {
		if op.Weight > 0 {
			add(op.Service, op.Name)
		}
	}
	for _, svc := range b.Model.Services {
		for _, shapes := range svc.Shapes {
			for _, shape := range shapes {
				for _, group := range shape {
					for _, call := range group {
						add(call.Peer, call.Method)
					}
				}
			}
		}
	}
	missing = nil
	for service, methods := range required {
		for method := range methods {
			if b.Evidence.MethodSamples[service][method] < 50 {
				missing = append(missing, service+"."+method)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		out = append(out, "Insufficient request-shape samples: "+strings.Join(missing, ", "))
	}
	return out
}

// ProposedPolicy is a starting point to review, not a recommendation to deploy.
// It bounds queues and retries and excludes declared unsafe writes from retries.
func ProposedPolicy(model *sim.Model) backdraft.SystemConfig {
	sys := model.System()
	originalEntry := sys.Entry
	serverEntry := false
	for _, op := range model.Ops {
		serverEntry = serverEntry || op.Service == sys.Entry
	}
	if serverEntry {
		// The generic mitigator treats Entry as external callers. A captured
		// server entry must receive admission controls and own the retry layer.
		pseudo := "__review_ingress"
		for {
			if _, exists := sys.Services[pseudo]; !exists {
				break
			}
			pseudo += "_"
		}
		sys.Services[pseudo] = backdraft.ServiceConfig{Clients: map[string]backdraft.ClientPolicy{originalEntry: {MaxAttempts: 1}}}
		sys.Entry = pseudo
	}
	out := analysis.Mitigate(sys, analysis.All())
	if serverEntry {
		delete(out.Services, sys.Entry)
		out.Entry = originalEntry
	}
	for name, cfg := range out.Services {
		for peer, p := range cfg.Clients {
			if p.MaxAttempts <= 1 {
				continue
			}
			seen := map[string]bool{}
			for _, method := range p.NoRetry {
				seen[method] = true
			}
			for method, info := range out.Services[peer].Methods {
				if !info.Idempotent && !info.Dedupes && !seen[method] {
					p.NoRetry = append(p.NoRetry, method)
				}
			}
			sort.Strings(p.NoRetry)
			cfg.Clients[peer] = p
		}
		out.Services[name] = cfg
	}
	return out
}

func DefaultContract(service string, load float64) Contract {
	load = math.Max(2, math.Min(5000, math.Round(load)))
	return Contract{Services: []string{service}, Loads: []float64{math.Round(load * 0.6), load}, Durations: []float64{2, 8}, Seeds: []uint64{1, 2, 3}, LatencyMs: 300, WarmS: 8, ObserveS: 30, MinSuccessRatio: 0.95, MaxRecoveryS: 10, MaxQueue: 1000}
}

type Change struct {
	Service string                  `json:"service"`
	Before  backdraft.ServiceConfig `json:"before"`
	After   backdraft.ServiceConfig `json:"after"`
}

func (b Bundle) Changes() []Change {
	out := []Change{}
	var names []string
	for name := range b.Model.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		before, after := b.Model.Services[name].Config, b.Candidate.Services[name]
		if Digest(before) != Digest(after) {
			out = append(out, Change{name, before, after})
		}
	}
	return out
}
