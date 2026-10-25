package tower

import (
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/analysis"
	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

const (
	selfSamples  = 2000
	shapeSamples = 200
	minSamples   = 50
)

// reservoir keeps a uniform sample of a stream.
type reservoir[T any] struct {
	items []T
	seen  int
}

func (r *reservoir[T]) add(rng *rand.Rand, x T, size int) {
	r.seen++
	if len(r.items) < size {
		r.items = append(r.items, x)
	} else if j := rng.IntN(r.seen); j < size {
		r.items[j] = x
	}
}

type child struct {
	peer, method string
	start, end   int64
}

type pending struct {
	children []child
	at       time.Time
}

// calibrator learns, from spans, what the simulator needs to replay the
// system: how long each service works per request (self time, which
// excludes downstream calls), which downstream calls each method makes and
// in what order, and the mix of requests users send.
type calibrator struct {
	mu          sync.Mutex
	rng         *rand.Rand
	entry       string
	serverEntry bool
	self        map[string]*reservoir[float64]
	shapes      map[string]map[string]*reservoir[sim.Shape]
	pending     map[[8]byte]*pending
	ops         map[string]map[string]float64 // op -> target service -> count
	methods     map[analysis.Edge]map[string]bool
}

func newCalibrator(entry string) *calibrator {
	return &calibrator{
		rng:     rand.New(rand.NewPCG(3, 4)),
		entry:   entry,
		self:    map[string]*reservoir[float64]{},
		shapes:  map[string]map[string]*reservoir[sim.Shape]{},
		pending: map[[8]byte]*pending{},
		ops:     map[string]map[string]float64{},
		methods: map[analysis.Edge]map[string]bool{},
	}
}

// ingest relies on agents emitting a request's client spans before its
// server span, which holds because a server span ends after every call
// its handler made.
func (c *calibrator) ingest(spans []*pb.Span) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for _, s := range spans {
		switch s.Kind {
		case pb.Kind_CLIENT:
			if s.Outcome == pb.Outcome_BUDGET {
				continue
			}
			e := analysis.Edge{From: s.Service, To: s.Peer}
			if c.methods[e] == nil {
				c.methods[e] = map[string]bool{}
			}
			c.methods[e][s.Method] = true
			if s.Attempt != 1 {
				continue
			}
			if s.Service == c.entry && !c.serverEntry {
				if c.ops[s.Method] == nil {
					c.ops[s.Method] = map[string]float64{}
				}
				c.ops[s.Method][s.Peer]++
				continue
			}
			if len(s.ParentId) != 8 {
				continue
			}
			var pid [8]byte
			copy(pid[:], s.ParentId)
			p := c.pending[pid]
			if p == nil {
				p = &pending{at: now}
				c.pending[pid] = p
			}
			p.children = append(p.children, child{s.Peer, s.Method, s.StartUnixNano, s.StartUnixNano + s.DurationNanos})
		case pb.Kind_SERVER:
			if c.serverEntry && s.Service == c.entry {
				if c.ops[s.Method] == nil {
					c.ops[s.Method] = map[string]float64{}
				}
				c.ops[s.Method][s.Service]++
			}
			var sid [8]byte
			copy(sid[:], s.SpanId)
			p := c.pending[sid]
			delete(c.pending, sid)
			if s.Outcome != pb.Outcome_OK || s.Faulted {
				continue // calibrate on healthy behaviour only
			}
			r := c.self[s.Service]
			if r == nil {
				r = &reservoir[float64]{}
				c.self[s.Service] = r
			}
			r.add(c.rng, float64(s.SelfNanos)/1e6, selfSamples)
			var shape sim.Shape
			if p != nil {
				shape = toShape(p.children)
			}
			if c.shapes[s.Service] == nil {
				c.shapes[s.Service] = map[string]*reservoir[sim.Shape]{}
			}
			sr := c.shapes[s.Service][s.Method]
			if sr == nil {
				sr = &reservoir[sim.Shape]{}
				c.shapes[s.Service][s.Method] = sr
			}
			sr.add(c.rng, shape, shapeSamples)
		}
	}
	// Children whose parent never reported (a request that was shed or
	// whose server span was dropped) would otherwise leak.
	if len(c.pending) > 10_000 {
		for k, p := range c.pending {
			if now.Sub(p.at) > 30*time.Second {
				delete(c.pending, k)
			}
		}
	}
}

// toShape orders calls by start time and groups calls that overlap, which
// ran in parallel.
func toShape(children []child) sim.Shape {
	sort.Slice(children, func(i, j int) bool { return children[i].start < children[j].start })
	var shape sim.Shape
	var groupEnd int64
	for _, ch := range children {
		call := sim.Call{Peer: ch.peer, Method: ch.method}
		if len(shape) > 0 && ch.start < groupEnd {
			shape[len(shape)-1] = append(shape[len(shape)-1], call)
			groupEnd = max(groupEnd, ch.end)
			continue
		}
		shape = append(shape, []sim.Call{call})
		groupEnd = ch.end
	}
	return shape
}

// Calibration says how much of a model came from observation.
type Calibration struct {
	SelfSamples   map[string]int            `json:"selfSamples"`
	ShapeSamples  map[string]int            `json:"shapeSamples"`
	MethodSamples map[string]map[string]int `json:"methodSamples"`
	OpsObserved   bool                      `json:"opsObserved"`
}

// model builds a simulation model from observations, falling back to
// fallback for anything not yet observed, with the given policies.
func (c *calibrator) model(sys backdraft.SystemConfig, fallback *sim.Model) (*sim.Model, Calibration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cal := Calibration{SelfSamples: map[string]int{}, ShapeSamples: map[string]int{}, MethodSamples: map[string]map[string]int{}}
	m := &sim.Model{Entry: sys.Entry, Services: map[string]sim.ServiceModel{}}
	for name, cfg := range sys.Services {
		sm := sim.ServiceModel{Config: cfg.Clone(), MeanSelfMs: cfg.Server.WorkMs}
		if fallback != nil {
			if fb, ok := fallback.Services[name]; ok {
				sm.MeanSelfMs, sm.SelfMs, sm.Shapes = fb.MeanSelfMs, fb.SelfMs, fb.Shapes
			}
		}
		if r := c.self[name]; r != nil && len(r.items) >= minSamples {
			sm.SelfMs = append([]float64(nil), r.items...)
			cal.SelfSamples[name] = len(r.items)
		}
		if methods := c.shapes[name]; len(methods) > 0 {
			cal.MethodSamples[name] = map[string]int{}
			shapes := map[string][]sim.Shape{}
			for method, samples := range sm.Shapes {
				shapes[method] = samples
			}
			sm.Shapes = shapes
			for method, r := range methods {
				if len(r.items) >= minSamples {
					sm.Shapes[method] = append([]sim.Shape(nil), r.items...)
					cal.ShapeSamples[name] += len(r.items)
					cal.MethodSamples[name][method] = len(r.items)
				}
			}
		}
		m.Services[name] = sm
	}
	var total float64
	for _, targets := range c.ops {
		for _, n := range targets {
			total += n
		}
	}
	if total >= minSamples {
		cal.OpsObserved = true
		for _, op := range sortedKeys(c.ops) {
			for _, target := range sortedKeys(c.ops[op]) {
				m.Ops = append(m.Ops, sim.Op{Name: op, Service: target, Weight: c.ops[op][target] / total})
			}
		}
	} else if fallback != nil {
		m.Ops = fallback.Ops
	}
	// Policies or ops can name services that have not connected yet.
	for _, op := range m.Ops {
		if _, ok := m.Services[op.Service]; !ok {
			m.Services[op.Service] = sim.ServiceModel{}
		}
	}
	for _, s := range m.Services {
		for _, peer := range s.Config.Peers() {
			if _, ok := m.Services[peer]; !ok {
				m.Services[peer] = sim.ServiceModel{}
			}
		}
	}
	return m, cal
}

// observed returns what live traffic says about call sequences and the
// methods each edge actually uses, for the analyzer.
func (c *calibrator) observed() (seqs map[string][][]string, methods map[analysis.Edge][]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seqs = map[string][][]string{}
	for svc, byMethod := range c.shapes {
		seen := map[string]bool{}
		for _, r := range byMethod {
			for _, shape := range r.items {
				var seq []string
				for _, g := range shape {
					// Parallel calls to one peer (an N+1 fan-out) cost one
					// call's worth of time. Distinct peers in a parallel
					// group are treated as sequential, which overstates
					// the worst case rather than understating it.
					seenInGroup := map[string]bool{}
					for _, call := range g {
						if !seenInGroup[call.Peer] {
							seq = append(seq, call.Peer)
							seenInGroup[call.Peer] = true
						}
					}
				}
				key := strings.Join(seq, ">")
				if !seen[key] && len(seq) > 0 {
					seen[key] = true
					seqs[svc] = append(seqs[svc], seq)
				}
			}
		}
	}
	methods = map[analysis.Edge][]string{}
	for e, ms := range c.methods {
		methods[e] = sortedKeys(ms)
	}
	return
}
