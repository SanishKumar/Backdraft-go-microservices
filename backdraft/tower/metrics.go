package tower

import (
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/pb"
)

const (
	tickInterval  = 250 * time.Millisecond
	rateBuckets   = 4  // 1s of ticks for displayed rates
	windowBuckets = 20 // 5s of ticks for analysis inputs
	maxParticles  = 160
)

// hist is a log-bucketed latency histogram: 1.2x per bucket from 50us.
type hist [80]uint32

func histIndex(d time.Duration) int {
	ms := float64(d) / float64(time.Millisecond)
	if ms <= 0.05 {
		return 0
	}
	i := int(math.Log(ms/0.05) / math.Log(1.2))
	return min(max(i, 0), len(hist{})-1)
}

func (h *hist) add(d time.Duration) { h[histIndex(d)]++ }

func (h *hist) merge(o *hist) {
	for i := range h {
		h[i] += o[i]
	}
}

// quantile returns the upper bound, in ms, of the bucket holding q.
func (h *hist) quantile(q float64) float64 {
	var total uint32
	for _, c := range h {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := uint32(math.Ceil(q * float64(total)))
	var cum uint32
	for i, c := range h {
		cum += c
		if cum >= target {
			return 0.05 * math.Pow(1.2, float64(i+1))
		}
	}
	return 0
}

type svcCounters struct {
	arrivals, ok, errors, shed, expired, late float64
	holdSum, holdN                            float64
	dur                                       hist
}

type edgeCounters struct {
	requests, attempts, retries, timeouts, errors, budget, replayed float64
	dur                                                             hist
}

type entryCounters struct {
	offered, goodput, failed float64
	dur                      hist
}

type bucket struct {
	svc   map[string]*svcCounters
	edge  map[string]*edgeCounters
	entry entryCounters

	clientSpans float64
	particles   []Particle
}

func newBucket() *bucket {
	return &bucket{svc: map[string]*svcCounters{}, edge: map[string]*edgeCounters{}}
}

func (b *bucket) service(name string) *svcCounters {
	c := b.svc[name]
	if c == nil {
		c = &svcCounters{}
		b.svc[name] = c
	}
	return c
}

func (b *bucket) edgeOf(key string) *edgeCounters {
	c := b.edge[key]
	if c == nil {
		c = &edgeCounters{}
		b.edge[key] = c
	}
	return c
}

// Particle is one sampled attempt for the UI to animate along its edge.
type Particle struct {
	Edge     string `json:"e"`
	Attempt  uint32 `json:"a"`
	Outcome  string `json:"o"`
	Duration uint32 `json:"d"` // ms
	Replayed bool   `json:"r,omitempty"`
}

// metrics turns spans into per-tick rates. Spans are counted when they
// arrive, which is when the work they describe finished.
type metrics struct {
	entryService string // real ingress when there is no built-in users/load generator
	mu           sync.Mutex
	cur          *bucket
	ring         [windowBuckets]*bucket
	next         int
	gauges       map[string]map[string]*pb.Gauges // service -> instance
	rng          *rand.Rand
}

func newMetrics() *metrics {
	m := &metrics{cur: newBucket(), gauges: map[string]map[string]*pb.Gauges{}, rng: rand.New(rand.NewPCG(1, 2))}
	for i := range m.ring {
		m.ring[i] = newBucket()
	}
	return m
}

var outcomeNames = map[pb.Outcome]string{
	pb.Outcome_OK: "ok", pb.Outcome_ERROR: "error", pb.Outcome_TIMEOUT: "timeout",
	pb.Outcome_SHED: "shed", pb.Outcome_EXPIRED: "expired", pb.Outcome_BUDGET: "budget",
}

func (m *metrics) ingest(spans []*pb.Span) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.cur
	for _, s := range spans {
		d := time.Duration(s.DurationNanos)
		switch s.Kind {
		case pb.Kind_SERVER:
			if m.entryService != "" && s.Service == m.entryService {
				b.entry.offered++
				if s.Outcome == pb.Outcome_OK && !s.Late {
					b.entry.goodput++
				} else {
					b.entry.failed++
				}
				b.entry.dur.add(d)
			}
			c := b.service(s.Service)
			c.arrivals++
			switch s.Outcome {
			case pb.Outcome_OK:
				c.ok++
				c.holdSum += float64(s.DurationNanos-s.QueueNanos) / 1e6
				c.holdN++
			case pb.Outcome_SHED:
				c.shed++
			case pb.Outcome_EXPIRED:
				c.expired++
			default:
				c.errors++
			}
			if s.Late {
				c.late++
			}
			c.dur.add(d)
		case pb.Kind_CLIENT:
			c := b.edgeOf(s.Service + ">" + s.Peer)
			if s.Outcome == pb.Outcome_BUDGET {
				c.budget++
				continue
			}
			c.attempts++
			if s.Attempt == 1 {
				c.requests++
			} else {
				c.retries++
			}
			switch s.Outcome {
			case pb.Outcome_TIMEOUT:
				c.timeouts++
			case pb.Outcome_ERROR:
				c.errors++
			}
			if s.Replayed {
				c.replayed++
			}
			c.dur.add(d)
			// Reservoir-sample attempts so the particle stream keeps the
			// true mix of first tries, retries and failures.
			b.clientSpans++
			p := Particle{Edge: s.Service + ">" + s.Peer, Attempt: s.Attempt, Outcome: outcomeNames[s.Outcome],
				Duration: uint32(min(d.Milliseconds(), 60_000)), Replayed: s.Replayed}
			if len(b.particles) < maxParticles {
				b.particles = append(b.particles, p)
			} else if j := m.rng.IntN(int(b.clientSpans)); j < maxParticles {
				b.particles[j] = p
			}
		}
	}
}

// entry records one logical user request.
func (m *metrics) entry(ok bool, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := &m.cur.entry
	if ok {
		e.goodput++
		e.dur.add(d)
	} else {
		e.failed++
	}
}

func (m *metrics) offered() {
	m.mu.Lock()
	m.cur.entry.offered++
	m.mu.Unlock()
}

func (m *metrics) gauge(service, instance string, g *pb.Gauges) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gauges[service] == nil {
		m.gauges[service] = map[string]*pb.Gauges{}
	}
	m.gauges[service][instance] = g
}

func (m *metrics) dropInstance(service, instance string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.gauges[service], instance)
}

// Snapshot is one tick of live state, as streamed to the UI.
type Snapshot struct {
	T         int64                   `json:"t"`
	Entry     EntryStats              `json:"entry"`
	Services  map[string]ServiceStats `json:"services"`
	Edges     map[string]EdgeStats    `json:"edges"`
	Particles []Particle              `json:"particles,omitempty"`
	// Scale is how many attempts each particle stands for.
	Scale float64 `json:"scale"`
}

type EntryStats struct {
	Offered float64 `json:"offered"`
	Goodput float64 `json:"goodput"`
	Failed  float64 `json:"failed"`
	P50     float64 `json:"p50"`
	P99     float64 `json:"p99"`
}

type ServiceStats struct {
	Arrivals float64 `json:"arrivals"`
	OK       float64 `json:"ok"`
	Errors   float64 `json:"errors"`
	Shed     float64 `json:"shed"`
	Expired  float64 `json:"expired"`
	Late     float64 `json:"late"`
	InFlight int     `json:"inFlight"`
	Queued   int     `json:"queued"`
	Workers  int     `json:"workers"`
	P50      float64 `json:"p50"`
	P99      float64 `json:"p99"`
}

type EdgeStats struct {
	Requests     float64 `json:"requests"`
	Attempts     float64 `json:"attempts"`
	Retries      float64 `json:"retries"`
	Timeouts     float64 `json:"timeouts"`
	Errors       float64 `json:"errors"`
	BudgetDenied float64 `json:"budgetDenied"`
	Replayed     float64 `json:"replayed"`
	P50          float64 `json:"p50"`
	P99          float64 `json:"p99"`
}

// roll closes the current bucket and returns a snapshot of the last
// second.
func (m *metrics) roll(now time.Time) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	closed := m.cur
	m.ring[m.next] = closed
	m.next = (m.next + 1) % windowBuckets
	m.cur = newBucket()

	recent := m.last(rateBuckets)
	perSec := 1 / (float64(len(recent)) * tickInterval.Seconds())
	snap := Snapshot{
		T:        now.UnixMilli(),
		Services: map[string]ServiceStats{},
		Edges:    map[string]EdgeStats{},
	}

	var eh hist
	for _, b := range recent {
		snap.Entry.Offered += b.entry.offered
		snap.Entry.Goodput += b.entry.goodput
		snap.Entry.Failed += b.entry.failed
		eh.merge(&b.entry.dur)
	}
	snap.Entry.Offered *= perSec
	snap.Entry.Goodput *= perSec
	snap.Entry.Failed *= perSec
	snap.Entry.P50, snap.Entry.P99 = eh.quantile(0.5), eh.quantile(0.99)

	svcNames := map[string]bool{}
	for name := range m.gauges {
		svcNames[name] = true
	}
	for _, b := range recent {
		for name := range b.svc {
			svcNames[name] = true
		}
	}
	for name := range svcNames {
		var st ServiceStats
		var h hist
		for _, b := range recent {
			if c := b.svc[name]; c != nil {
				st.Arrivals += c.arrivals
				st.OK += c.ok
				st.Errors += c.errors
				st.Shed += c.shed
				st.Expired += c.expired
				st.Late += c.late
				h.merge(&c.dur)
			}
		}
		st.Arrivals *= perSec
		st.OK *= perSec
		st.Errors *= perSec
		st.Shed *= perSec
		st.Expired *= perSec
		st.Late *= perSec
		st.P50, st.P99 = h.quantile(0.5), h.quantile(0.99)
		for _, g := range m.gauges[name] {
			st.InFlight += int(g.InFlight)
			st.Queued += int(g.Queued)
			st.Workers += int(g.Workers)
		}
		snap.Services[name] = st
	}

	edgeKeys := map[string]bool{}
	for _, b := range recent {
		for k := range b.edge {
			edgeKeys[k] = true
		}
	}
	for k := range edgeKeys {
		var st EdgeStats
		var h hist
		for _, b := range recent {
			if c := b.edge[k]; c != nil {
				st.Requests += c.requests
				st.Attempts += c.attempts
				st.Retries += c.retries
				st.Timeouts += c.timeouts
				st.Errors += c.errors
				st.BudgetDenied += c.budget
				st.Replayed += c.replayed
				h.merge(&c.dur)
			}
		}
		st.Requests *= perSec
		st.Attempts *= perSec
		st.Retries *= perSec
		st.Timeouts *= perSec
		st.Errors *= perSec
		st.BudgetDenied *= perSec
		st.Replayed *= perSec
		st.P50, st.P99 = h.quantile(0.5), h.quantile(0.99)
		snap.Edges[k] = st
	}

	snap.Particles = closed.particles
	snap.Scale = 1
	if n := len(closed.particles); n > 0 {
		snap.Scale = closed.clientSpans / float64(n)
	}
	return snap
}

// last returns the n most recently closed buckets. Callers hold mu.
func (m *metrics) last(n int) []*bucket {
	out := make([]*bucket, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, m.ring[(m.next-i+windowBuckets)%windowBuckets])
	}
	return out
}

// observedRates returns first-attempt calls per second on each edge and
// mean worker hold time per service over the analysis window.
func (m *metrics) observedRates() (edgeRPS map[string]float64, holdMs map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	edgeRPS, holdMs = map[string]float64{}, map[string]float64{}
	holdN := map[string]float64{}
	window := m.last(windowBuckets)
	secs := float64(len(window)) * tickInterval.Seconds()
	for _, b := range window {
		for k, c := range b.edge {
			edgeRPS[k] += c.requests / secs
		}
		for name, c := range b.svc {
			holdMs[name] += c.holdSum
			holdN[name] += c.holdN
		}
	}
	for name := range holdMs {
		if holdN[name] > 0 {
			holdMs[name] /= holdN[name]
		} else {
			delete(holdMs, name)
		}
	}
	return
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
