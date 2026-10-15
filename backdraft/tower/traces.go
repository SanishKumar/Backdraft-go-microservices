package tower

import (
	"encoding/hex"
	"sort"
	"sync"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"google.golang.org/grpc/codes"
)

const (
	recentTraces      = 3000
	interestingTraces = 800
	maxSpansPerTrace  = 600
)

type traceID [16]byte

type trace struct {
	spans       []*pb.Span
	interesting bool
}

// traceStore keeps recent traces, with a separate, longer-lived ring for
// traces where something went wrong so a storm's healthy traffic does not
// evict the evidence.
type traceStore struct {
	mu          sync.Mutex
	traces      map[traceID]*trace
	recent      []traceID
	interesting []traceID
	ri, ii      int
}

func newTraceStore() *traceStore {
	return &traceStore{
		traces:      map[traceID]*trace{},
		recent:      make([]traceID, 0, recentTraces),
		interesting: make([]traceID, 0, interestingTraces),
	}
}

func (ts *traceStore) add(spans []*pb.Span) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, s := range spans {
		if len(s.TraceId) != 16 {
			continue
		}
		var id traceID
		copy(id[:], s.TraceId)
		t := ts.traces[id]
		if t == nil {
			t = &trace{}
			ts.traces[id] = t
			ts.evict(&ts.recent, &ts.ri, recentTraces, id)
		}
		if len(t.spans) < maxSpansPerTrace {
			t.spans = append(t.spans, s)
		}
		if !t.interesting && (s.Attempt > 1 || (s.Outcome != pb.Outcome_OK && s.Outcome != pb.Outcome_OUTCOME_UNSPECIFIED) || s.Late) {
			t.interesting = true
			ts.evict(&ts.interesting, &ts.ii, interestingTraces, id)
		}
	}
}

// evict appends id to a fixed-size ring, dropping the trace that falls
// off unless the other ring still references it.
func (ts *traceStore) evict(ring *[]traceID, pos *int, size int, id traceID) {
	if len(*ring) < size {
		*ring = append(*ring, id)
		return
	}
	old := (*ring)[*pos]
	(*ring)[*pos] = id
	*pos = (*pos + 1) % size
	if old == id {
		return
	}
	if t := ts.traces[old]; t != nil && !ts.referenced(old, ring) {
		delete(ts.traces, old)
	}
}

func (ts *traceStore) referenced(id traceID, except *[]traceID) bool {
	for _, ring := range []*[]traceID{&ts.recent, &ts.interesting} {
		if ring == except {
			continue
		}
		for _, x := range *ring {
			if x == id {
				return true
			}
		}
	}
	return false
}

// TraceSummary is a row in the trace list.
type TraceSummary struct {
	ID         string `json:"id"`
	Op         string `json:"op"`
	Start      int64  `json:"start"` // unix ms
	DurationMs int64  `json:"durationMs"`
	Spans      int    `json:"spans"`
	Attempts   int    `json:"attempts"`
	Retries    int    `json:"retries"`
	Late       int    `json:"late"`
	OK         bool   `json:"ok"`
}

func summarize(id traceID, t *trace, entry string) TraceSummary {
	s := TraceSummary{ID: hex.EncodeToString(id[:]), Spans: len(t.spans)}
	var start, end int64
	for i, sp := range t.spans {
		st, en := sp.StartUnixNano, sp.StartUnixNano+sp.DurationNanos
		if i == 0 || st < start {
			start = st
		}
		if en > end {
			end = en
		}
		if sp.Kind == pb.Kind_CLIENT && sp.Outcome != pb.Outcome_BUDGET {
			s.Attempts++
			if sp.Attempt > 1 {
				s.Retries++
			}
		}
		if sp.Late {
			s.Late++
		}
		if sp.Kind == pb.Kind_CLIENT && sp.Service == entry {
			s.Op = sp.Method
			if sp.Outcome == pb.Outcome_OK {
				s.OK = true
			}
		}
	}
	s.Start = start / 1e6
	s.DurationMs = (end - start) / 1e6
	return s
}

// list returns summaries, newest first.
func (ts *traceStore) list(interestingOnly bool, limit int, entry string) []TraceSummary {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ring, pos := ts.recent, ts.ri
	if interestingOnly {
		ring, pos = ts.interesting, ts.ii
	}
	out := []TraceSummary{}
	n := len(ring)
	for i := 1; i <= n && len(out) < limit; i++ {
		id := ring[(pos-i+n)%n]
		if t := ts.traces[id]; t != nil {
			out = append(out, summarize(id, t, entry))
		}
	}
	return out
}

// SpanView is one span as the UI's waterfall needs it.
type SpanView struct {
	ID       string `json:"id"`
	Parent   string `json:"parent,omitempty"`
	Kind     string `json:"kind"`
	Service  string `json:"service"`
	Peer     string `json:"peer,omitempty"`
	Method   string `json:"method"`
	Attempt  uint32 `json:"attempt"`
	StartUs  int64  `json:"startUs"` // relative to trace start
	DurUs    int64  `json:"durUs"`
	QueueUs  int64  `json:"queueUs,omitempty"`
	SelfUs   int64  `json:"selfUs,omitempty"`
	Deadline int64  `json:"deadlineUs,omitempty"` // relative to trace start
	Code     string `json:"code"`
	Outcome  string `json:"outcome"`
	Late     bool   `json:"late,omitempty"`
	Replayed bool   `json:"replayed,omitempty"`
	Faulted  bool   `json:"faulted,omitempty"`
}

func (ts *traceStore) get(id string) ([]SpanView, bool) {
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 16 {
		return nil, false
	}
	var tid traceID
	copy(tid[:], raw)
	ts.mu.Lock()
	t := ts.traces[tid]
	var spans []*pb.Span
	if t != nil {
		spans = append(spans, t.spans...)
	}
	ts.mu.Unlock()
	if t == nil {
		return nil, false
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].StartUnixNano < spans[j].StartUnixNano })
	origin := spans[0].StartUnixNano
	out := make([]SpanView, 0, len(spans))
	for _, s := range spans {
		v := SpanView{
			ID:       hex.EncodeToString(s.SpanId),
			Kind:     map[pb.Kind]string{pb.Kind_CLIENT: "client", pb.Kind_SERVER: "server"}[s.Kind],
			Service:  s.Service,
			Peer:     s.Peer,
			Method:   s.Method,
			Attempt:  s.Attempt,
			StartUs:  (s.StartUnixNano - origin) / 1e3,
			DurUs:    s.DurationNanos / 1e3,
			QueueUs:  s.QueueNanos / 1e3,
			SelfUs:   s.SelfNanos / 1e3,
			Code:     backdraft.CodeName(codes.Code(s.Code)),
			Outcome:  outcomeNames[s.Outcome],
			Late:     s.Late,
			Replayed: s.Replayed,
			Faulted:  s.Faulted,
		}
		if len(s.ParentId) > 0 {
			v.Parent = hex.EncodeToString(s.ParentId)
		}
		if s.DeadlineUnixNano > 0 {
			v.Deadline = (s.DeadlineUnixNano - origin) / 1e3
		}
		out = append(out, v)
	}
	return out, true
}
