package backdraft

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
)

// Propagation uses the W3C traceparent header so traces line up with
// OpenTelemetry-instrumented hops; the two bd- headers carry what
// traceparent cannot.
const (
	headerTraceparent = "traceparent"
	headerCaller      = "bd-caller"
	headerAttempt     = "bd-attempt"
	// ReplayedHeader is the response header a server sets when it answered
	// a request from an earlier one with the same idempotency key.
	ReplayedHeader = "idempotent-replayed"
)

type (
	TraceID [16]byte
	SpanID  [8]byte
)

func newTraceID() (t TraceID) {
	putUint64(t[:8], rand.Uint64())
	putUint64(t[8:], rand.Uint64())
	return
}

func newSpanID() (s SpanID) {
	putUint64(s[:], rand.Uint64()|1) // never all-zero, which W3C forbids
	return
}

func putUint64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (56 - 8*i))
	}
}

func formatTraceparent(t TraceID, s SpanID) string {
	return "00-" + hex.EncodeToString(t[:]) + "-" + hex.EncodeToString(s[:]) + "-01"
}

func parseTraceparent(v string) (t TraceID, s SpanID, ok bool) {
	if len(v) != 55 || v[2] != '-' || v[35] != '-' || v[52] != '-' {
		return t, s, false
	}
	if _, err := hex.Decode(t[:], []byte(v[3:35])); err != nil {
		return t, s, false
	}
	if _, err := hex.Decode(s[:], []byte(v[36:52])); err != nil {
		return t, s, false
	}
	return t, s, t != TraceID{} && s != SpanID{}
}

// spanState is the server span handling the current inbound request.
type spanState struct {
	trace TraceID
	id    SpanID
	down  intervals

	mu     sync.Mutex
	op     string
	failed codes.Code
}

type spanKey struct{}

func withSpan(ctx context.Context, s *spanState) context.Context {
	return context.WithValue(ctx, spanKey{}, s)
}

func spanFrom(ctx context.Context) *spanState {
	s, _ := ctx.Value(spanKey{}).(*spanState)
	return s
}

// SetOperation names the operation the current inbound request performs,
// for transports where the route does not (a GraphQL endpoint serves every
// operation from one path).
func SetOperation(ctx context.Context, name string) {
	if s := spanFrom(ctx); s != nil && name != "" {
		s.mu.Lock()
		s.op = name
		s.mu.Unlock()
	}
}

// SetFailed records that the current inbound request failed with code c
// even though the transport reported success, as GraphQL does with errors
// in a 200 response.
func SetFailed(ctx context.Context, c codes.Code) {
	if s := spanFrom(ctx); s != nil && c != codes.OK {
		s.mu.Lock()
		if s.failed == codes.OK {
			s.failed = c
		}
		s.mu.Unlock()
	}
}

// intervals accumulates the wall-clock time a handler spends blocked on
// downstream calls. Calls may overlap, so it keeps the intervals and
// measures their union.
type intervals struct {
	mu    sync.Mutex
	spans [][2]int64
}

func (iv *intervals) add(start, end time.Time) {
	iv.mu.Lock()
	iv.spans = append(iv.spans, [2]int64{start.UnixNano(), end.UnixNano()})
	iv.mu.Unlock()
}

func (iv *intervals) union() time.Duration {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	if len(iv.spans) == 0 {
		return 0
	}
	sort.Slice(iv.spans, func(i, j int) bool { return iv.spans[i][0] < iv.spans[j][0] })
	var total int64
	cur := iv.spans[0]
	for _, s := range iv.spans[1:] {
		if s[0] > cur[1] {
			total += cur[1] - cur[0]
			cur = s
		} else if s[1] > cur[1] {
			cur[1] = s[1]
		}
	}
	total += cur[1] - cur[0]
	return time.Duration(total)
}
