package sim

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"

	"github.com/SanishKumar/go-microservices/backdraft"
	"google.golang.org/grpc/codes"
)

// Scenario is what happens to the model over time.
type Scenario struct {
	DurationS float64 `json:"durationS"`
	// Load is the offered request rate, interpolated linearly between
	// points and held flat past the last one. Arrivals are Poisson: users
	// do not wait for each other, which is what lets overload build.
	Load   []LoadPoint   `json:"load"`
	Faults []FaultWindow `json:"faults,omitempty"`
	Seed   uint64        `json:"seed"`
	// StepS is the resolution of the result series. Default 0.25s.
	StepS float64 `json:"stepS,omitempty"`
	// StopWhenStuck ends the run once, well after the last fault, goodput
	// is near zero and the backlog is still growing: the outcome is no
	// longer in doubt. Series are only meaningful up to the stop.
	StopWhenStuck bool `json:"stopWhenStuck,omitempty"`
}

type LoadPoint struct {
	T   float64 `json:"t"`
	RPS float64 `json:"rps"`
}

// FaultWindow adds latency and errors to a service for a while: the
// trigger of an incident.
type FaultWindow struct {
	Service   string  `json:"service"`
	StartS    float64 `json:"startS"`
	EndS      float64 `json:"endS"`
	LatencyMs float64 `json:"latencyMs"`
	ErrorRate float64 `json:"errorRate,omitempty"`
}

// Result holds per-step series; rates are per second.
type Result struct {
	StepS    float64                   `json:"stepS"`
	T        []float64                 `json:"t"`
	Offered  []float64                 `json:"offered"`
	Goodput  []float64                 `json:"goodput"`
	Failed   []float64                 `json:"failed"`
	Services map[string]*ServiceSeries `json:"services"`
	Edges    map[string]*EdgeSeries    `json:"edges"`
	Verdict  Verdict                   `json:"verdict"`
	// Events processed, and whether the run stopped early because queues
	// grew past any plausible recovery.
	Events    int  `json:"events"`
	Truncated bool `json:"truncated"`
	// StoppedAt is when a StopWhenStuck run ended, 0 if it ran to the end.
	StoppedAt float64 `json:"stoppedAt,omitempty"`
}

type ServiceSeries struct {
	Arrivals  []float64 `json:"arrivals"`
	Completed []float64 `json:"completed"`
	Late      []float64 `json:"late"`
	Expired   []float64 `json:"expired"`
	Shed      []float64 `json:"shed"`
	Queue     []float64 `json:"queue"`
	Busy      []float64 `json:"busy"`
	Workers   int       `json:"workers"`
	// busySeconds accumulates worker-time, for capacity estimates.
	busySeconds float64
}

type EdgeSeries struct {
	Attempts     []float64 `json:"attempts"`
	Retries      []float64 `json:"retries"`
	Timeouts     []float64 `json:"timeouts"`
	BudgetDenied []float64 `json:"budgetDenied"`
}

const (
	netDelay   = 0.0002 // one-way network latency, seconds
	maxPending = 150_000
	maxEvents  = 40_000_000
)

// The engine's events are a tagged union rather than closures: a run is
// hundreds of thousands of events, and allocating a closure for each one
// made the garbage collector the simulator's main cost.
type evKind uint8

const (
	evSample        evKind = iota // record queue gauges for step k
	evArrival                     // a user sends a request
	evDeliver                     // job reaches srv
	evWorkDone                    // job's own work at srv is done
	evExpire                      // queued job's deadline passed (deadline-aware server)
	evReply                       // srv's response to job reaches the caller
	evAttemptExpiry               // the caller's per-attempt deadline for job
	evRetry                       // backoff over: call makes its next attempt
	evNextArrival                 // poll for arrivals while the load is zero
)

type event struct {
	t    float64
	seq  uint64
	kind evKind
	code codes.Code
	k    int
	srv  *server
	job  *job
	call *call
}

// heap is a binary min-heap of events ordered by time, then insertion.
type heap []event

func (h heap) less(i, j int) bool {
	return h[i].t < h[j].t || (h[i].t == h[j].t && h[i].seq < h[j].seq)
}

func (h *heap) push(e event) {
	*h = append(*h, e)
	s := *h
	for i := len(s) - 1; i > 0; {
		p := (i - 1) / 2
		if !s.less(i, p) {
			break
		}
		s[i], s[p] = s[p], s[i]
		i = p
	}
}

func (h *heap) pop() event {
	s := *h
	top := s[0]
	n := len(s) - 1
	s[0] = s[n]
	s[n] = event{}
	s = s[:n]
	for i := 0; ; {
		l, r, m := 2*i+1, 2*i+2, i
		if l < n && s.less(l, m) {
			m = l
		}
		if r < n && s.less(r, m) {
			m = r
		}
		if m == i {
			break
		}
		s[i], s[m] = s[m], s[i]
		i = m
	}
	*h = s
	return top
}

// edge is one caller -> callee dependency with its policy resolved once.
type edge struct {
	to          *server
	pol         backdraft.ClientPolicy
	retryOn     uint32 // bit per status code
	noRetry     map[string]bool
	timeout     float64
	series      *EdgeSeries
	budget      *simBudget
	propagate   bool
	maxAttempts int
}

func (e *edge) retries(c codes.Code) bool { return e.retryOn&(1<<uint(c)) != 0 }

// call is one logical call, across all its attempts.
type call struct {
	edge           *edge
	from           *server
	method         string
	maxAttempts    int
	attempt        int
	parentDeadline float64
	// parent is the job whose shape made this call; nil for a user's call.
	parent *job
}

// job is one attempt as the callee sees it: queued, then worked on.
type job struct {
	method   string
	deadline float64
	start    float64
	srv      *server
	call     *call
	started  bool
	dead     bool // left the queue on expiry
	resolved bool // the caller has stopped waiting (reply or timeout)
	faultErr bool
	// Executing the shape of downstream calls.
	shape   Shape
	group   int
	pending int
	failed  codes.Code
}

type server struct {
	name    string
	model   ServiceModel
	cfg     backdraft.ServiceConfig
	workers int
	busy    int
	queue   []*job
	head    int
	live    int // queued jobs not yet expired
	series  *ServiceSeries
	edges   map[string]*edge
}

func (s *server) push(j *job) {
	if s.head > 1024 && s.head*2 > len(s.queue) {
		s.queue = append(s.queue[:0], s.queue[s.head:]...)
		s.head = 0
	}
	s.queue = append(s.queue, j)
	s.live++
}

func (s *server) pop() *job {
	for s.head < len(s.queue) {
		j := s.queue[s.head]
		s.queue[s.head] = nil
		s.head++
		if !j.dead {
			s.live--
			return j
		}
	}
	return nil
}

type simBudget struct {
	ratio, floor float64
	sec          [10]int64
	req, retry   [10]float64
}

func (b *simBudget) slot(now float64) int {
	sec := int64(now)
	i := int(sec % 10)
	if b.sec[i] != sec {
		b.sec[i], b.req[i], b.retry[i] = sec, 0, 0
	}
	return i
}

func (b *simBudget) request(now float64) { b.req[b.slot(now)]++ }

func (b *simBudget) allow(now float64) bool {
	i := b.slot(now)
	sec := int64(now)
	var req, retry float64
	for k := range b.sec {
		if sec-b.sec[k] < 10 {
			req += b.req[k]
			retry += b.retry[k]
		}
	}
	if retry+1 > b.ratio*req+b.floor*10 {
		return false
	}
	b.retry[i]++
	return true
}

// slab hands out values from large chunks: one allocation per chunk
// instead of one per value. Nothing is freed until the run ends.
type slab[T any] struct {
	chunk []T
	i     int
}

func (s *slab[T]) alloc() *T {
	if s.i == len(s.chunk) {
		s.chunk = make([]T, 2048)
		s.i = 0
	}
	p := &s.chunk[s.i]
	s.i++
	return p
}

type engine struct {
	jobs    slab[job]
	calls   slab[call]
	m       *Model
	sc      Scenario
	now     float64
	seq     uint64
	q       heap
	rng     *rand.Rand
	servers map[string]*server
	ordered []*server
	entry   *server
	res     *Result
	n       int
	pending int
	opCum   []float64
	opEdges []*edge
}

// Run simulates sc against m.
func Run(m *Model, sc Scenario) *Result {
	r, _ := RunContext(context.Background(), m, sc)
	return r
}

// RunContext stops processing when the caller cancels or its deadline expires.
func RunContext(ctx context.Context, m *Model, sc Scenario) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sc.StepS <= 0 {
		sc.StepS = 0.25
	}
	n := int(math.Ceil(sc.DurationS/sc.StepS - 1e-9))
	if n < 1 {
		n = 1
	}
	e := &engine{
		m:       m,
		sc:      sc,
		rng:     rand.New(rand.NewPCG(sc.Seed, sc.Seed^0x9e3779b97f4a7c15)),
		servers: map[string]*server{},
		n:       n,
		q:       make(heap, 0, 4096),
		res: &Result{
			StepS:    sc.StepS,
			T:        make([]float64, n),
			Offered:  make([]float64, n),
			Goodput:  make([]float64, n),
			Failed:   make([]float64, n),
			Services: map[string]*ServiceSeries{},
			Edges:    map[string]*EdgeSeries{},
		},
	}
	for i := range e.res.T {
		e.res.T[i] = float64(i) * sc.StepS
	}
	for _, name := range sortedNames(m.Services) {
		sm := m.Services[name]
		ss := &ServiceSeries{
			Arrivals: make([]float64, n), Completed: make([]float64, n), Late: make([]float64, n),
			Expired: make([]float64, n), Shed: make([]float64, n), Queue: make([]float64, n), Busy: make([]float64, n),
			Workers: sm.Config.Server.Workers,
		}
		e.res.Services[name] = ss
		s := &server{name: name, model: sm, cfg: sm.Config, workers: sm.Config.Server.Workers, series: ss, edges: map[string]*edge{}}
		e.servers[name] = s
		e.ordered = append(e.ordered, s)
	}
	for _, s := range e.ordered {
		for _, peer := range s.cfg.Peers() {
			pol := s.cfg.Client(peer)
			ed := &edge{
				to:          e.servers[peer],
				pol:         pol,
				noRetry:     map[string]bool{},
				timeout:     float64(pol.TimeoutMs) / 1000,
				propagate:   pol.PropagateDeadline,
				maxAttempts: pol.MaxAttempts,
				series: &EdgeSeries{
					Attempts: make([]float64, n), Retries: make([]float64, n),
					Timeouts: make([]float64, n), BudgetDenied: make([]float64, n),
				},
			}
			for c := codes.OK; c <= codes.Unauthenticated; c++ {
				if pol.RetriesOn(c) {
					ed.retryOn |= 1 << uint(c)
				}
			}
			for _, method := range pol.NoRetry {
				ed.noRetry[method] = true
			}
			if pol.Budget != nil {
				ed.budget = &simBudget{ratio: pol.Budget.Ratio, floor: pol.Budget.MinPerSec}
			}
			e.res.Edges[s.name+">"+peer] = ed.series
			s.edges[peer] = ed
		}
	}
	e.entry = e.servers[m.Entry]
	total := 0.0
	for _, op := range m.Ops {
		total += op.Weight
		e.opCum = append(e.opCum, total)
		e.opEdges = append(e.opEdges, e.edgeTo(e.entry, op.Service))
	}

	for k := 0; k < n; k++ {
		e.at(event{t: float64(k) * sc.StepS, kind: evSample, k: k})
	}
	if len(e.opCum) > 0 && e.opCum[len(e.opCum)-1] > 0 && e.entry != nil {
		e.scheduleArrival()
	}

	lastFault := 0.0
	for _, f := range sc.Faults {
		lastFault = math.Max(lastFault, f.EndS)
	}
	nextCheck := lastFault + 10
	events := 0
	for len(e.q) > 0 {
		if events%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		ev := e.q.pop()
		if ev.t > sc.DurationS {
			break
		}
		e.now = ev.t
		e.dispatch(ev)
		events++
		if events > maxEvents || e.pending > maxPending {
			e.res.Truncated = true
			break
		}
		if sc.StopWhenStuck && len(sc.Faults) > 0 && e.now >= nextCheck {
			nextCheck = e.now + 1
			if e.stuck() {
				e.res.StoppedAt = e.now
				break
			}
		}
	}
	e.res.Events = events

	inv := 1 / sc.StepS
	scale := func(xs []float64) {
		for i := range xs {
			xs[i] *= inv
		}
	}
	scale(e.res.Offered)
	scale(e.res.Goodput)
	scale(e.res.Failed)
	for _, s := range e.res.Services {
		scale(s.Arrivals)
		scale(s.Completed)
		scale(s.Late)
		scale(s.Expired)
		scale(s.Shed)
	}
	for _, ed := range e.res.Edges {
		scale(ed.Attempts)
		scale(ed.Retries)
		scale(ed.Timeouts)
		scale(ed.BudgetDenied)
	}
	e.res.Verdict = judge(e.res, sc, m)
	return e.res, nil
}

// edgeTo returns s's edge to peer, creating a default one (plain gRPC
// behaviour) for calls the policy does not mention.
func (e *engine) edgeTo(s *server, peer string) *edge {
	if ed, ok := s.edges[peer]; ok {
		return ed
	}
	pol := backdraft.DefaultClientPolicy()
	ed := &edge{to: e.servers[peer], pol: pol, propagate: true, maxAttempts: 1, noRetry: map[string]bool{}}
	s.edges[peer] = ed
	return ed
}

func (e *engine) at(ev event) {
	e.seq++
	ev.seq = e.seq
	e.q.push(ev)
}

func (e *engine) dispatch(ev event) {
	switch ev.kind {
	case evSample:
		for _, s := range e.ordered {
			s.series.Queue[ev.k] = float64(s.live)
			s.series.Busy[ev.k] = float64(s.busy)
		}
	case evArrival:
		e.arrival()
		e.scheduleArrival()
	case evNextArrival:
		e.scheduleArrival()
	case evDeliver:
		e.arrive(ev.srv, ev.job)
	case evWorkDone:
		e.workDone(ev.srv, ev.job)
	case evExpire:
		j := ev.job
		if j.dead || j.started {
			return
		}
		j.dead = true
		ev.srv.live--
		e.pending--
		ev.srv.series.Expired[e.bucket()]++
	case evReply:
		j := ev.job
		if j.resolved {
			return // the caller gave up already; the work was wasted
		}
		j.resolved = true
		e.attemptResult(j.call, ev.code)
	case evAttemptExpiry:
		j := ev.job
		if j.resolved {
			return
		}
		j.resolved = true
		if s := j.call.edge.series; s != nil {
			s.Timeouts[e.bucket()]++
		}
		e.attemptResult(j.call, codes.DeadlineExceeded)
	case evRetry:
		e.try(ev.call)
	}
}

func (e *engine) bucket() int {
	b := int(e.now / e.sc.StepS)
	if b >= e.n {
		b = e.n - 1
	}
	return b
}

// stuck reports near-zero goodput over the last 4s with a backlog that
// is large and still growing.
func (e *engine) stuck() bool {
	b := e.bucket()
	w := int(4 / e.sc.StepS)
	if b < 2*w {
		return false
	}
	var good, offered float64
	for i := b - w; i < b; i++ {
		good += e.res.Goodput[i]
		offered += e.res.Offered[i]
	}
	queue := func(i int) (q float64) {
		for _, s := range e.ordered {
			q += s.series.Queue[i]
		}
		return
	}
	return offered > 0 && good/offered < 0.02 && queue(b-1) > 500 && queue(b-1) > queue(b-1-w)
}

func (e *engine) loadAt(t float64) float64 {
	pts := e.sc.Load
	if len(pts) == 0 {
		return 0
	}
	if t <= pts[0].T {
		return pts[0].RPS
	}
	for i := 1; i < len(pts); i++ {
		if t <= pts[i].T {
			a, b := pts[i-1], pts[i]
			if b.T == a.T {
				return b.RPS
			}
			return a.RPS + (b.RPS-a.RPS)*(t-a.T)/(b.T-a.T)
		}
	}
	return pts[len(pts)-1].RPS
}

func (e *engine) scheduleArrival() {
	rate := e.loadAt(e.now)
	if rate <= 0 {
		e.at(event{t: e.now + 0.05, kind: evNextArrival})
		return
	}
	e.at(event{t: e.now + e.rng.ExpFloat64()/rate, kind: evArrival})
}

func (e *engine) arrival() {
	e.res.Offered[e.bucket()]++
	u := e.rng.Float64() * e.opCum[len(e.opCum)-1]
	i := sort.SearchFloat64s(e.opCum, u)
	e.startCall(e.entry, e.opEdges[i], e.m.Ops[i].Name, math.Inf(1), nil)
}

func (e *engine) fault(service string) (latency float64, errRate float64) {
	for _, f := range e.sc.Faults {
		if f.Service == service && e.now >= f.StartS && e.now < f.EndS {
			latency += f.LatencyMs / 1000
			errRate = math.Max(errRate, f.ErrorRate)
		}
	}
	return
}

func (e *engine) selfTime(s *server) float64 {
	if xs := s.model.SelfMs; len(xs) > 0 {
		return xs[e.rng.IntN(len(xs))] / 1000
	}
	mean := s.model.MeanSelfMs
	if mean <= 0 {
		return 0
	}
	const sigma = 0.35
	mu := math.Log(mean) - sigma*sigma/2
	return math.Exp(mu+sigma*e.rng.NormFloat64()) / 1000
}

func (e *engine) arrive(s *server, j *job) {
	b := e.bucket()
	s.series.Arrivals[b]++
	if ms := s.cfg.Server.RequestTimeoutMs; ms > 0 {
		j.deadline = math.Min(j.deadline, e.now+float64(ms)/1000)
	}
	if s.workers == 0 || s.busy < s.workers {
		e.start(s, j)
		return
	}
	if lim := s.cfg.Server.QueueLimit; lim > 0 && s.live >= lim {
		s.series.Shed[b]++
		e.reply(j, codes.ResourceExhausted)
		return
	}
	s.push(j)
	e.pending++
	if s.cfg.Server.ShedExpired && !math.IsInf(j.deadline, 1) {
		// A deadline-aware server lets a waiter go the moment its caller
		// does, as the agent's limiter does.
		e.at(event{t: j.deadline, kind: evExpire, srv: s, job: j})
	}
}

func (e *engine) start(s *server, j *job) {
	s.busy++
	j.start, j.started = e.now, true
	if s.cfg.Server.ShedExpired && e.now >= j.deadline {
		s.busy--
		s.series.Expired[e.bucket()]++
		e.reply(j, codes.DeadlineExceeded)
		e.next(s)
		return
	}
	lat, errRate := e.fault(s.name)
	j.faultErr = errRate > 0 && e.rng.Float64() < errRate
	e.at(event{t: e.now + e.selfTime(s) + lat, kind: evWorkDone, srv: s, job: j})
}

func (e *engine) workDone(s *server, j *job) {
	if j.faultErr {
		e.finish(s, j, codes.Unavailable)
		return
	}
	if shapes := s.model.Shapes[j.method]; len(shapes) > 0 {
		j.shape = shapes[e.rng.IntN(len(shapes))]
	}
	e.runGroup(j)
}

// runGroup starts the next group of j's downstream calls, or finishes j.
func (e *engine) runGroup(j *job) {
	for j.group < len(j.shape) && len(j.shape[j.group]) == 0 {
		j.group++
	}
	if j.group == len(j.shape) {
		e.finish(j.srv, j, codes.OK)
		return
	}
	g := j.shape[j.group]
	j.pending = len(g)
	j.failed = codes.OK
	for _, c := range g {
		e.startCall(j.srv, e.edgeTo(j.srv, c.Peer), c.Method, j.deadline, j)
	}
}

func (e *engine) finish(s *server, j *job, c codes.Code) {
	b := e.bucket()
	s.busy--
	s.series.busySeconds += e.now - j.start
	if c == codes.OK {
		s.series.Completed[b]++
	}
	if e.now > j.deadline {
		s.series.Late[b]++
	}
	e.reply(j, c)
	e.next(s)
}

func (e *engine) next(s *server) {
	for s.workers == 0 || s.busy < s.workers {
		j := s.pop()
		if j == nil {
			return
		}
		e.pending--
		if s.cfg.Server.ShedExpired && e.now >= j.deadline {
			s.series.Expired[e.bucket()]++
			e.reply(j, codes.DeadlineExceeded)
			continue
		}
		e.start(s, j)
		return
	}
}

func (e *engine) reply(j *job, c codes.Code) {
	e.at(event{t: e.now + netDelay, kind: evReply, job: j, code: c})
}

// startCall begins one logical call from s over ed, mirroring
// backdraft.Agent.Call.
func (e *engine) startCall(s *server, ed *edge, method string, parentDeadline float64, parent *job) {
	c := e.calls.alloc()
	*c = call{edge: ed, from: s, method: method, maxAttempts: ed.maxAttempts, parentDeadline: parentDeadline, parent: parent}
	if c.maxAttempts < 1 {
		c.maxAttempts = 1
	}
	if ed.noRetry[method] {
		c.maxAttempts = 1
	}
	if ed.budget != nil {
		ed.budget.request(e.now)
	}
	e.try(c)
}

func (e *engine) try(c *call) {
	c.attempt++
	ed := c.edge
	b := e.bucket()
	if ed.series != nil {
		ed.series.Attempts[b]++
		if c.attempt > 1 {
			ed.series.Retries[b]++
		}
	}
	if ed.propagate && e.now >= c.parentDeadline {
		// gRPC fails a call on an expired context without sending it.
		if ed.series != nil {
			ed.series.Timeouts[b]++
		}
		e.attemptResult(c, codes.DeadlineExceeded)
		return
	}
	deadline := math.Inf(1)
	if ed.timeout > 0 {
		deadline = e.now + ed.timeout
	}
	if ed.propagate && c.parentDeadline < deadline {
		deadline = c.parentDeadline
	}
	j := e.jobs.alloc()
	*j = job{method: c.method, deadline: deadline, srv: ed.to, call: c}
	if !math.IsInf(deadline, 1) {
		e.at(event{t: deadline, kind: evAttemptExpiry, job: j})
	}
	e.at(event{t: e.now + netDelay, kind: evDeliver, srv: ed.to, job: j})
}

// attemptResult decides, after an attempt resolves, whether to retry.
func (e *engine) attemptResult(c *call, code codes.Code) {
	ed := c.edge
	if code == codes.OK || c.attempt >= c.maxAttempts || !ed.retries(code) ||
		(ed.propagate && e.now >= c.parentDeadline) {
		e.callDone(c, code)
		return
	}
	if ed.budget != nil && !ed.budget.allow(e.now) {
		if ed.series != nil {
			ed.series.BudgetDenied[e.bucket()]++
		}
		e.callDone(c, code)
		return
	}
	e.at(event{t: e.now + ed.pol.Backoff(c.attempt, e.rng.Float64()).Seconds(), kind: evRetry, call: c})
}

// callDone hands a finished logical call back to whoever made it.
func (e *engine) callDone(c *call, code codes.Code) {
	p := c.parent
	if p == nil {
		b := e.bucket()
		if code == codes.OK {
			e.res.Goodput[b]++
		} else {
			e.res.Failed[b]++
		}
		return
	}
	p.pending--
	if code != codes.OK && p.failed == codes.OK {
		p.failed = code
	}
	if p.pending > 0 {
		return
	}
	if p.failed != codes.OK {
		e.finish(p.srv, p, p.failed)
		return
	}
	p.group++
	e.runGroup(p)
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
