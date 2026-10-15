// Package analysis finds resilience problems that only exist between
// services: retry amplification across layers, timeouts that outlive their
// callers, work orphaned by lost deadlines, retries of operations that are
// not safe to repeat, and retry load that exceeds a service's capacity to
// recover.
//
// It works on the declared or live SystemConfig, optionally sharpened by
// observed traffic, and every finding a policy change can fix carries that
// change as a Patch.
package analysis

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
)

type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
)

var severityRank = map[Severity]int{Critical: 0, High: 1, Medium: 2, Low: 3}

// Edge is a caller -> callee dependency.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (e Edge) String() string { return e.From + ">" + e.To }

// Observed is live traffic that sharpens the static analysis. Every field
// is optional.
type Observed struct {
	// EdgeRPS is first-attempt calls per second on each edge.
	EdgeRPS map[Edge]float64
	// HoldMs is the mean time a request holds a worker at each service,
	// including time blocked on its own downstream calls.
	HoldMs map[string]float64
	// Sequences lists, per service, the distinct downstream call sequences
	// its requests make (peer names in call order).
	Sequences map[string][][]string
	// Methods lists the callee methods actually called on each edge.
	Methods map[Edge][]string
}

// Input is what Analyze works on.
type Input struct {
	System   backdraft.SystemConfig
	Observed *Observed
}

// Patch replaces the configuration of the services it names.
type Patch map[string]backdraft.ServiceConfig

// Finding is one problem, explained with the numbers behind it.
type Finding struct {
	ID       string             `json:"id"`
	Rule     string             `json:"rule"`
	Severity Severity           `json:"severity"`
	Title    string             `json:"title"`
	Detail   string             `json:"detail"`
	Fix      string             `json:"fix"`
	Path     []string           `json:"path,omitempty"`
	Edges    []Edge             `json:"edges,omitempty"`
	Metrics  map[string]float64 `json:"metrics,omitempty"`
	Patch    Patch              `json:"patch,omitempty"`
}

// Path is one chain of calls from the entry service.
type Path struct {
	Services []string `json:"services"`
	// Attempts per edge along the path.
	Attempts []int `json:"attempts"`
	// Worst is the attempts reaching the last service for one entry
	// request when every attempt fails.
	Worst float64 `json:"worst"`
	// Sustained is the amplification the path can keep up across many
	// requests during an outage: a retry budget caps a layer at 1+ratio
	// whatever its attempt count.
	Sustained float64 `json:"sustained"`
	// Budgeted is Sustained if every edge the system controls had a retry
	// budget.
	Budgeted float64 `json:"budgeted"`
}

// Report is the result of Analyze.
type Report struct {
	Findings []Finding `json:"findings"`
	Paths    []Path    `json:"paths"`
	// WorstAttempts is, per service, the most attempts one entry request
	// can sustain against it.
	WorstAttempts map[string]float64 `json:"worstAttempts"`
	Counts        map[Severity]int   `json:"counts"`
}

type analyzer struct {
	sys      backdraft.SystemConfig
	obs      *Observed
	paths    []Path
	findings []Finding
}

// Analyze runs every rule.
func Analyze(in Input) Report {
	a := &analyzer{sys: in.System, obs: in.Observed}
	if a.obs == nil {
		a.obs = &Observed{}
	}
	a.paths = a.enumeratePaths()
	a.amplification()
	a.inversions()
	a.orphanedWork()
	a.unsafeRetries()
	a.unbudgetedRetries()
	a.admission()
	a.retryCliff()

	sort.SliceStable(a.findings, func(i, j int) bool {
		fi, fj := a.findings[i], a.findings[j]
		if severityRank[fi.Severity] != severityRank[fj.Severity] {
			return severityRank[fi.Severity] < severityRank[fj.Severity]
		}
		return fi.ID < fj.ID
	})
	r := Report{
		Findings:      a.findings,
		Paths:         a.paths,
		WorstAttempts: map[string]float64{},
		Counts:        map[Severity]int{},
	}
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	for _, p := range a.paths {
		last := p.Services[len(p.Services)-1]
		r.WorstAttempts[last] = math.Max(r.WorstAttempts[last], p.Sustained)
	}
	for _, f := range a.findings {
		r.Counts[f.Severity]++
	}
	return r
}

func (a *analyzer) add(f Finding) { a.findings = append(a.findings, f) }

func (a *analyzer) svc(name string) backdraft.ServiceConfig { return a.sys.Services[name] }

func (a *analyzer) policy(e Edge) backdraft.ClientPolicy { return a.svc(e.From).Client(e.To) }

// edges returns every configured edge in a stable order.
func (a *analyzer) edges() []Edge {
	var out []Edge
	for _, from := range sortedKeys(a.sys.Services) {
		for _, to := range a.svc(from).Peers() {
			out = append(out, Edge{from, to})
		}
	}
	return out
}

// enumeratePaths lists every acyclic call chain from the entry service.
func (a *analyzer) enumeratePaths() []Path {
	var out []Path
	var walk func(stack []string)
	walk = func(stack []string) {
		cur := stack[len(stack)-1]
		for _, next := range a.svc(cur).Peers() {
			if contains(stack, next) {
				continue
			}
			s := append(append([]string(nil), stack...), next)
			p := Path{Services: s, Worst: 1, Sustained: 1, Budgeted: 1}
			for i := 0; i+1 < len(s); i++ {
				e := Edge{s[i], s[i+1]}
				ep := a.policy(e)
				p.Attempts = append(p.Attempts, ep.MaxAttempts)
				p.Worst *= float64(ep.MaxAttempts)
				p.Sustained *= sustainedFactor(ep)
				p.Budgeted *= a.budgetedFactor(e)
			}
			out = append(out, p)
			walk(s)
		}
	}
	if a.sys.Entry != "" {
		walk([]string{a.sys.Entry})
	}
	return out
}

// sustainedFactor is how many attempts per request an edge can keep
// producing through an outage: its attempt count, or 1+ratio under a
// retry budget.
func sustainedFactor(p backdraft.ClientPolicy) float64 {
	n := float64(p.MaxAttempts)
	if p.Budget != nil {
		n = math.Min(n, 1+p.Budget.Ratio)
	}
	return n
}

// budgetedFactor is sustainedFactor with a default budget on any edge the
// system controls (everything but the entry, which models users).
func (a *analyzer) budgetedFactor(e Edge) float64 {
	p := a.policy(e)
	if e.From != a.sys.Entry {
		p.Budget = defaultBudget(p.Budget)
	}
	return sustainedFactor(p)
}

// upstreamAmp is the most retries above service s can multiply the
// requests arriving at s, per entry request.
func (a *analyzer) upstreamAmp(s string, budgeted bool) float64 {
	if s == a.sys.Entry {
		return 1
	}
	best := 0.0
	for _, p := range a.paths {
		if p.Services[len(p.Services)-1] != s {
			continue
		}
		v := p.Sustained
		if budgeted {
			v = p.Budgeted
		}
		best = math.Max(best, v)
	}
	return best
}

func (a *analyzer) multiplyingLayers(p Path) int {
	n := 0
	for i := 0; i+1 < len(p.Services); i++ {
		if sustainedFactor(a.policy(Edge{p.Services[i], p.Services[i+1]})) >= 1.5 {
			n++
		}
	}
	return n
}

func (a *analyzer) amplification() {
	// Paths where more than one layer multiplies attempts, grouped by the
	// chain of callers so leaves behind the same chain share a finding.
	type group struct {
		path   Path // representative
		leaves []string
	}
	groups := map[string]*group{}
	for _, p := range a.paths {
		if a.multiplyingLayers(p) < 2 || p.Sustained < 4 {
			continue
		}
		prefix := strings.Join(p.Services[:len(p.Services)-1], ">") + fmt.Sprintf("|%g", p.Sustained)
		g, ok := groups[prefix]
		if !ok {
			g = &group{path: p}
			groups[prefix] = g
		}
		g.leaves = append(g.leaves, p.Services[len(p.Services)-1])
	}
	// A leaf reached by several chains is reported on its worst one.
	best := map[string]float64{}
	for _, g := range groups {
		for _, l := range g.leaves {
			best[l] = math.Max(best[l], g.path.Sustained)
		}
	}
	for _, key := range sortedKeys(groups) {
		g := groups[key]
		var leaves []string
		for _, l := range g.leaves {
			if best[l] == g.path.Sustained {
				leaves = append(leaves, l)
			}
		}
		if len(leaves) == 0 {
			continue
		}
		sort.Strings(leaves)
		p := g.path
		sev := Medium
		switch {
		case p.Sustained >= 16:
			sev = Critical
		case p.Sustained >= 8:
			sev = High
		}
		chain := p.Services[:len(p.Services)-1]
		var factors []string
		var edges []Edge
		for i := 0; i+1 < len(chain); i++ {
			e := Edge{chain[i], chain[i+1]}
			factors = append(factors, fmt.Sprintf("%s x%s", e.From, num(sustainedFactor(a.policy(e)))))
			edges = append(edges, e)
		}
		last := Edge{chain[len(chain)-1], leaves[0]}
		factors = append(factors, fmt.Sprintf("%s x%s", last.From, num(sustainedFactor(a.policy(last)))))
		for _, l := range leaves {
			edges = append(edges, Edge{last.From, l})
		}
		var into []Path
		for _, q := range a.paths {
			if contains(leaves, q.Services[len(q.Services)-1]) {
				into = append(into, q)
			}
		}
		target := joinAnd(leaves)
		a.add(Finding{
			ID:       "amplification:" + strings.Join(leaves, ","),
			Rule:     "retry-amplification",
			Severity: sev,
			Title:    fmt.Sprintf("One request from %s can become %s attempts on %s", a.sys.Entry, num(p.Sustained), target),
			Detail: fmt.Sprintf(
				"Along %s -> %s, %d layers retry (%s). Each layer retries the failures of the layer below it, so when %s %s slow every timeout fires at once and the attempts multiply: load grows %sx at exactly the moment it can least be absorbed. With retry budgets this chain tops out at %sx.",
				strings.Join(chain, " -> "), target, a.multiplyingLayers(p), strings.Join(factors, ", "), target, plural(len(leaves) == 1, "is", "are"), num(p.Sustained), num(p.Budgeted)),
			Fix:     "Retry at one layer only, the one closest to the user, and cap it with a retry budget. Inner layers should fail fast and let the outer layer decide.",
			Path:    p.Services,
			Edges:   edges,
			Metrics: map[string]float64{"worst": p.Worst, "sustained": p.Sustained, "budgeted": p.Budgeted, "layers": float64(a.multiplyingLayers(p))},
			Patch:   a.singleLayerPatch(into),
		})
	}
}

// singleLayerPatch keeps retries, budgeted, on the outermost edge the
// system controls along each path, and turns them off further in.
func (a *analyzer) singleLayerPatch(paths []Path) Patch {
	patch := Patch{}
	for _, path := range paths {
		kept := false
		for i := 0; i+1 < len(path.Services); i++ {
			e := Edge{path.Services[i], path.Services[i+1]}
			if e.From == a.sys.Entry {
				continue // the entry models users; their behaviour is not ours to set
			}
			cfg := a.patched(patch, e.From)
			p := cfg.Client(e.To)
			if p.MaxAttempts <= 1 {
				continue
			}
			if !kept {
				p.Budget = defaultBudget(p.Budget)
				kept = true
			} else {
				p.MaxAttempts = 1
			}
			cfg.Clients[e.To] = p
			patch[e.From] = cfg
		}
	}
	return nilIfEmpty(patch)
}

// worstCall is the longest a call on edge e can take with every attempt
// timing out. ok is false when it is unbounded.
func (a *analyzer) worstCall(e Edge) (d time.Duration, ok bool) {
	p := a.policy(e)
	if p.TimeoutMs == 0 {
		return 0, false
	}
	return time.Duration(p.MaxAttempts)*p.Timeout() + p.WorstCaseBackoff(), true
}

// sequences are the downstream call sequences of service s: observed
// ones, or each dependency on its own when nothing has been observed
// (every request touching that dependency can hit its worst case).
func (a *analyzer) sequences(s string) [][]string {
	if seqs := a.obs.Sequences[s]; len(seqs) > 0 {
		return seqs
	}
	var seqs [][]string
	for _, peer := range a.svc(s).Peers() {
		seqs = append(seqs, []string{peer})
	}
	return seqs
}

// worstSequence is the longest seq can take with every attempt timing
// out; bounded is false if a call in it has no timeout.
func (a *analyzer) worstSequence(s string, seq []string) (d time.Duration, bounded bool) {
	for _, peer := range seq {
		w, ok := a.worstCall(Edge{s, peer})
		if !ok {
			return 0, false
		}
		d += w
	}
	return d, true
}

// worstResponse is the slowest call sequence of service s.
func (a *analyzer) worstResponse(s string) (d time.Duration, chain []string, bounded bool) {
	for _, seq := range a.sequences(s) {
		w, ok := a.worstSequence(s, seq)
		if !ok {
			return 0, seq, false
		}
		if w > d {
			d, chain = w, seq
		}
	}
	return d, chain, true
}

// fitAttempts is the most attempts of p that fit in budget.
func fitAttempts(p backdraft.ClientPolicy, budget time.Duration) int {
	n := 1
	for k := 2; k <= p.MaxAttempts; k++ {
		q := p
		q.MaxAttempts = k
		if time.Duration(k)*q.Timeout()+q.WorstCaseBackoff() > budget {
			break
		}
		n = k
	}
	return n
}

func (a *analyzer) inversions() {
	for _, e := range a.edges() {
		p := a.policy(e)
		if p.TimeoutMs == 0 || len(a.svc(e.To).Clients) == 0 {
			continue
		}
		worst, chain, bounded := a.worstResponse(e.To)
		if bounded && worst <= p.Timeout() {
			continue
		}
		orphans := false
		for _, peer := range chain {
			if !a.svc(e.To).Client(peer).PropagateDeadline {
				orphans = true
			}
		}
		sev := Medium
		consequence := fmt.Sprintf("%s later attempts are pure load: they cannot finish before %s has stopped waiting.", possessive(e.To), e.From)
		if orphans {
			sev = High
			consequence = fmt.Sprintf("%s does not propagate the deadline, so it keeps calling %s after %s has given up and retried - every retry starts another copy of the work.", e.To, joinAnd(chain), e.From)
		}
		spend := "without bound (a call has no timeout)"
		if bounded {
			spend = fmt.Sprintf("up to %s on %s", ms(worst), strings.Join(chain, " then "))
			if len(a.obs.Sequences[e.To]) == 0 {
				spend += " alone"
			}
		}

		// Fit every sequence that overruns inside the caller's attempt:
		// split the caller's timeout between the calls in it, keep as many
		// attempts as fit, and propagate the deadline.
		share := map[string]time.Duration{}
		for _, seq := range a.sequences(e.To) {
			if w, ok := a.worstSequence(e.To, seq); ok && w <= p.Timeout() {
				continue
			}
			s := p.Timeout() / time.Duration(len(seq))
			for _, peer := range seq {
				if cur, ok := share[peer]; !ok || s < cur {
					share[peer] = s
				}
			}
		}
		patch := Patch{}
		cfg := a.patched(patch, e.To)
		for _, peer := range sortedKeys(share) {
			cp := cfg.Client(peer)
			cp.PropagateDeadline = true
			if cp.TimeoutMs == 0 || cp.Timeout() > share[peer] {
				cp.TimeoutMs = int(share[peer].Milliseconds())
			}
			cp.MaxAttempts = fitAttempts(cp, share[peer])
			cfg.Clients[peer] = cp
		}
		patch[e.To] = cfg

		a.add(Finding{
			ID:       "inversion:" + e.String(),
			Rule:     "timeout-inversion",
			Severity: sev,
			Title:    fmt.Sprintf("%s can outlast %s %s timeout", e.To, possessive(e.From), ms(p.Timeout())),
			Detail:   fmt.Sprintf("%s waits %s per attempt for %s, but %s can spend %s. %s", e.From, ms(p.Timeout()), e.To, e.To, spend, consequence),
			Fix:      fmt.Sprintf("Make %s downstream calls fit inside %s timeout: propagate the deadline and keep only the attempts that can finish in time.", possessive(e.To), possessive(e.From)),
			Path:     append([]string{e.From, e.To}, chain...),
			Edges:    []Edge{e},
			Metrics:  map[string]float64{"timeoutMs": float64(p.TimeoutMs), "worstMs": float64(worst.Milliseconds())},
			Patch:    patch,
		})
	}
}

func (a *analyzer) orphanedWork() {
	var edges []Edge
	for _, e := range a.edges() {
		if e.From != a.sys.Entry && !a.policy(e).PropagateDeadline {
			edges = append(edges, e)
		}
	}
	if len(edges) == 0 {
		return
	}
	patch := Patch{}
	for _, e := range edges {
		cfg := a.patched(patch, e.From)
		p := cfg.Client(e.To)
		p.PropagateDeadline = true
		cfg.Clients[e.To] = p
		patch[e.From] = cfg
	}
	a.add(Finding{
		ID:       "orphaned-work",
		Rule:     "deadline-not-propagated",
		Severity: High,
		Title:    fmt.Sprintf("%s drop%s the caller's deadline", edgeList(edges), plural(len(edges) == 1, "s", "")),
		Detail:   "These calls start a fresh clock instead of inheriting the deadline of the request that caused them. When the caller gives up, the downstream work carries on to completion and its result is thrown away; when the caller retries, the abandoned work and the retry run side by side.",
		Fix:      "Derive downstream contexts from the incoming request context so its deadline and cancellation flow through.",
		Edges:    edges,
		Patch:    patch,
	})
}

func (a *analyzer) unsafeRetries() {
	// callee.method -> edges that retry it
	retriedBy := map[string][]Edge{}
	overload := map[string]bool{}
	for _, e := range a.edges() {
		p := a.policy(e)
		methods := a.svc(e.To).Methods
		called := a.obs.Methods[e]
		for _, m := range sortedKeys(methods) {
			info := methods[m]
			if info.Idempotent || info.Dedupes || !p.RetriesMethod(m) {
				continue
			}
			if called != nil && !contains(called, m) {
				continue // observed traffic shows this edge never calls it
			}
			key := e.To + "." + m
			retriedBy[key] = append(retriedBy[key], e)
			if p.RetriesOn(codeDeadline) {
				overload[key] = true
			}
		}
	}
	for _, key := range sortedKeys(retriedBy) {
		edges := retriedBy[key]
		method := key[strings.IndexByte(key, '.')+1:]
		var callers []string
		patch := Patch{}
		for _, e := range edges {
			callers = append(callers, e.From)
			cfg := a.patched(patch, e.From)
			cp := cfg.Client(e.To)
			cp.NoRetry = append(append([]string(nil), cp.NoRetry...), method)
			cfg.Clients[e.To] = cp
			patch[e.From] = cfg
		}
		sev := High
		why := "A retry after an UNAVAILABLE error can still repeat a request that was processed."
		if overload[key] {
			sev = Critical
			why = "Retries fire on DEADLINE_EXCEEDED, and a request that timed out has often succeeded: the retry performs it a second time."
		}
		qualifier := ""
		if len(a.obs.Methods) == 0 {
			qualifier = " (assuming they call it; live traffic narrows this down)"
		}
		a.add(Finding{
			ID:       "unsafe-retry:" + key,
			Rule:     "unsafe-retry",
			Severity: sev,
			Title:    fmt.Sprintf("%s is retried but not safe to repeat", key),
			Detail:   fmt.Sprintf("%s is neither idempotent nor deduplicated, and %s retr%s it%s. %s", key, joinAnd(callers), plural(len(callers) == 1, "ies", "y"), qualifier, why),
			Fix:      fmt.Sprintf("Deduplicate %s on an idempotency key, as order.PostOrder does, and mark it dedupes. Until then, exclude it from retries (the patch adds it to noRetry).", method),
			Edges:    edges,
			Patch:    patch,
		})
	}
}

func (a *analyzer) unbudgetedRetries() {
	var overload, other []Edge
	for _, e := range a.edges() {
		p := a.policy(e)
		if e.From == a.sys.Entry || p.MaxAttempts <= 1 || p.Budget != nil {
			continue
		}
		if p.RetriesOn(codeDeadline) || p.RetriesOn(codeExhausted) {
			overload = append(overload, e)
		} else {
			other = append(other, e)
		}
	}
	all := append(append([]Edge(nil), overload...), other...)
	if len(all) == 0 {
		return
	}
	patch := Patch{}
	for _, e := range all {
		cfg := a.patched(patch, e.From)
		p := cfg.Client(e.To)
		p.Budget = defaultBudget(nil)
		cfg.Clients[e.To] = p
		patch[e.From] = cfg
	}
	sev, detail := Medium, "Retries are capped per request but not in aggregate, so a failing dependency receives a full set of retries for every request."
	if len(overload) > 0 {
		sev = High
		detail = fmt.Sprintf("%s retr%s on DEADLINE_EXCEEDED or RESOURCE_EXHAUSTED with no retry budget. Those codes usually mean the callee is overloaded, and an unbudgeted retry answers overload with more load.", edgeList(overload), plural(len(overload) == 1, "ies", "y"))
	}
	a.add(Finding{
		ID:       "unbudgeted-retries",
		Rule:     "no-retry-budget",
		Severity: sev,
		Title:    fmt.Sprintf("%d edge%s retry without a budget", len(all), plural(len(all) == 1, "", "s")),
		Detail:   detail,
		Fix:      "Add a retry budget (10% of requests plus a small floor). Retries still smooth over isolated failures, but during an outage they add at most 10% load instead of multiplying it.",
		Edges:    all,
		Patch:    patch,
	})
}

func (a *analyzer) admission() {
	var noShed, unbounded []string
	for _, s := range sortedKeys(a.sys.Services) {
		srv := a.svc(s).Server
		if srv.Workers == 0 {
			continue
		}
		if !srv.ShedExpired {
			noShed = append(noShed, s)
		}
		if srv.QueueLimit == 0 {
			unbounded = append(unbounded, s)
		}
	}
	if len(noShed) > 0 {
		patch := Patch{}
		for _, s := range noShed {
			cfg := a.patched(patch, s)
			cfg.Server.ShedExpired = true
			patch[s] = cfg
		}
		a.add(Finding{
			ID:       "no-shedding",
			Rule:     "expired-work",
			Severity: High,
			Title:    fmt.Sprintf("%s work on requests whose callers already left", joinAnd(noShed)),
			Detail:   "Requests wait in the queue past their deadline and are processed anyway. After a slowdown the backlog is mostly requests nobody is waiting for, so a recovered service spends its capacity on them while fresh requests time out behind them, and the backlog never shrinks.",
			Fix:      "Drop queued requests whose deadline has passed, before doing any work on them.",
			Path:     noShed,
			Patch:    patch,
		})
	}
	if len(unbounded) > 0 {
		patch := Patch{}
		for _, s := range unbounded {
			cfg := a.patched(patch, s)
			cfg.Server.QueueLimit = 4 * cfg.Server.Workers
			patch[s] = cfg
		}
		a.add(Finding{
			ID:       "unbounded-queue",
			Rule:     "unbounded-queue",
			Severity: Medium,
			Title:    fmt.Sprintf("%s queue%s without limit", joinAnd(unbounded), plural(len(unbounded) == 1, "s", "")),
			Detail:   "An unbounded queue turns overload into latency for everyone instead of fast rejections for some. Callers time out and retry, and the queue keeps growing.",
			Fix:      "Bound each queue to a few times its worker count and reject the rest with RESOURCE_EXHAUSTED, which clients can back off from.",
			Path:     unbounded,
			Patch:    patch,
		})
	}
}

// retryCliff compares the load retries alone can put on each service with
// what the service can serve once healthy. When retries exceed capacity, a
// service pushed into overload stays there after the trigger is gone: the
// sustaining effect of a metastable failure.
func (a *analyzer) retryCliff() {
	if len(a.obs.EdgeRPS) == 0 || len(a.obs.HoldMs) == 0 {
		return
	}
	for _, s := range sortedKeys(a.sys.Services) {
		workers := a.svc(s).Server.Workers
		hold := a.obs.HoldMs[s]
		if workers == 0 || hold <= 0 {
			continue
		}
		capacity := float64(workers) / (hold / 1000)
		var normal, worst, budgeted float64
		var edges []Edge
		for _, e := range a.edges() {
			if e.To != s {
				continue
			}
			rps := a.obs.EdgeRPS[e]
			if rps == 0 {
				continue
			}
			edges = append(edges, e)
			normal += rps
			worst += rps * sustainedFactor(a.policy(e)) * a.upstreamAmp(e.From, false)
			budgeted += rps * a.budgetedFactor(e) * a.upstreamAmp(e.From, true)
		}
		if worst <= capacity {
			continue
		}
		a.add(Finding{
			ID:       "retry-cliff:" + s,
			Rule:     "metastable-risk",
			Severity: Critical,
			Title:    fmt.Sprintf("Once %s saturates, retries alone keep it saturated", s),
			Detail: fmt.Sprintf(
				"%s serves %s req/s now and can serve about %s req/s (%d workers / %s per request). If a slowdown makes its callers time out, retries can drive %s req/s at it - more than it can serve even after the slowdown ends. That is the sustaining effect behind metastable failures: the outage outlives its trigger. With retry budgets the worst case is %s req/s.",
				s, num(normal), num(capacity), workers, ms(time.Duration(hold*float64(time.Millisecond))), num(worst), num(budgeted)),
			Fix:     "Budget retries so worst-case retry load stays under capacity, and shed expired work so a backlog drains instead of compounding. The stability map shows the load and incident size at which this tips over.",
			Path:    []string{s},
			Edges:   edges,
			Metrics: map[string]float64{"normal": normal, "capacity": capacity, "worst": worst, "budgeted": budgeted},
			Patch:   a.budgetPatch(s),
		})
	}
}

// budgetPatch budgets every retrying edge on any path into s.
func (a *analyzer) budgetPatch(s string) Patch {
	patch := Patch{}
	for _, p := range a.paths {
		if p.Services[len(p.Services)-1] != s {
			continue
		}
		for i := 0; i+1 < len(p.Services); i++ {
			e := Edge{p.Services[i], p.Services[i+1]}
			pol := a.policy(e)
			if e.From == a.sys.Entry || pol.MaxAttempts <= 1 || pol.Budget != nil {
				continue
			}
			cfg := a.patched(patch, e.From)
			cp := cfg.Client(e.To)
			cp.Budget = defaultBudget(nil)
			cfg.Clients[e.To] = cp
			patch[e.From] = cfg
		}
	}
	return nilIfEmpty(patch)
}

// patched returns the in-progress patched config for s.
func (a *analyzer) patched(patch Patch, s string) backdraft.ServiceConfig {
	cfg, ok := patch[s]
	if !ok {
		cfg = a.svc(s).Clone()
	}
	if cfg.Clients == nil {
		cfg.Clients = map[string]backdraft.ClientPolicy{}
	}
	return cfg
}

func defaultBudget(b *backdraft.BudgetPolicy) *backdraft.BudgetPolicy {
	if b != nil {
		return b
	}
	return &backdraft.BudgetPolicy{Ratio: 0.1, MinPerSec: 1}
}

func nilIfEmpty(p Patch) Patch {
	if len(p) == 0 {
		return nil
	}
	return p
}

var (
	codeDeadline, _  = backdraft.ParseCode("DEADLINE_EXCEEDED")
	codeExhausted, _ = backdraft.ParseCode("RESOURCE_EXHAUSTED")
)

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func edgeList(edges []Edge) string {
	parts := make([]string, len(edges))
	for i, e := range edges {
		parts[i] = e.From + " -> " + e.To
	}
	return strings.Join(parts, ", ")
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func possessive(name string) string {
	if strings.HasSuffix(name, "s") {
		return name + "'"
	}
	return name + "'s"
}

func plural(one bool, singular, many string) string {
	if one {
		return singular
	}
	return many
}

func num(v float64) string {
	if v >= 100 || v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func ms(d time.Duration) string {
	if d >= time.Second {
		s := fmt.Sprintf("%.2f", d.Seconds())
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		return s + "s"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
