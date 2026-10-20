package backdraft

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPanickingHandlerReleasesWorker(t *testing.T) {
	a := New("api", ServiceConfig{Server: ServerPolicy{Workers: 1}})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected handler panic")
			}
		}()
		a.serve(context.Background(), inbound{}, func(context.Context) error { panic("application failure") })
	}()
	if inUse, queued, _ := a.limiter.Stats(); inUse != 0 || queued != 0 {
		t.Fatalf("panic leaked admission: %d workers, %d queued", inUse, queued)
	}
}

func TestCodeNames(t *testing.T) {
	for c, want := range map[codes.Code]string{
		codes.OK:                "OK",
		codes.DeadlineExceeded:  "DEADLINE_EXCEEDED",
		codes.ResourceExhausted: "RESOURCE_EXHAUSTED",
	} {
		if got := CodeName(c); got != want {
			t.Errorf("CodeName(%v) = %q, want %q", c, got, want)
		}
		if got, ok := ParseCode(want); !ok || got != c {
			t.Errorf("ParseCode(%q) = %v, %v", want, got, ok)
		}
	}
	if c, ok := ParseCode("Unavailable"); !ok || c != codes.Unavailable {
		t.Errorf("ParseCode accepts Go names")
	}
}

func TestTraceparentRoundTrip(t *testing.T) {
	tr, sp := newTraceID(), newSpanID()
	gotT, gotS, ok := parseTraceparent(formatTraceparent(tr, sp))
	if !ok || gotT != tr || gotS != sp {
		t.Fatalf("round trip failed")
	}
	for _, bad := range []string{"", "00-xyz", "00-00000000000000000000000000000000-0000000000000000-01"} {
		if _, _, ok := parseTraceparent(bad); ok {
			t.Errorf("parseTraceparent(%q) accepted", bad)
		}
	}
}

func TestBackoffIsCappedAndJittered(t *testing.T) {
	p := ClientPolicy{MaxAttempts: 5, BackoffMs: 100, BackoffMaxMs: 300}
	if got := p.Backoff(1, 1); got != 100*time.Millisecond {
		t.Errorf("first backoff = %v", got)
	}
	if got := p.Backoff(4, 1); got != 300*time.Millisecond {
		t.Errorf("capped backoff = %v", got)
	}
	if got := p.Backoff(2, 0.5); got != 100*time.Millisecond {
		t.Errorf("jittered backoff = %v", got)
	}
	if got := p.WorstCaseBackoff(); got != (100+200+300+300)*time.Millisecond {
		t.Errorf("worst case backoff = %v", got)
	}
}

func TestIntervalsUnion(t *testing.T) {
	var iv intervals
	base := time.Unix(0, 0)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	iv.add(at(0), at(10))
	iv.add(at(5), at(15)) // overlaps
	iv.add(at(20), at(30))
	if got := iv.union(); got != 25*time.Millisecond {
		t.Fatalf("union = %v, want 25ms", got)
	}
}

func TestBudgetBoundsRetries(t *testing.T) {
	now := time.Unix(1_000, 0)
	b := NewBudget(BudgetPolicy{Ratio: 0.2, MinPerSec: 0.5})
	b.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		b.RecordRequest()
	}
	allowed := 0
	for i := 0; i < 1000; i++ {
		if b.AllowRetry() {
			allowed++
		}
	}
	// 0.2 * 100 requests + 0.5/s * 10s window
	if allowed != 25 {
		t.Fatalf("allowed %d retries, want 25", allowed)
	}
	// Once the window slides past, the budget is replenished only by new
	// traffic and the floor.
	now = now.Add(11 * time.Second)
	allowed = 0
	for i := 0; i < 1000; i++ {
		if b.AllowRetry() {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("after window: allowed %d retries, want 5", allowed)
	}
}

func TestLimiterQueuesFIFOAndRejectsWhenFull(t *testing.T) {
	l := NewLimiter(1, 2)
	release, err := l.Acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var order []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := l.Acquire(context.Background(), false)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			r()
		}(i)
		waitFor(t, func() bool { _, q, _ := l.Stats(); return q == i })
	}
	if _, err := l.Acquire(context.Background(), false); err != ErrQueueFull {
		t.Fatalf("third waiter: err = %v, want ErrQueueFull", err)
	}
	release()
	wg.Wait()
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("grant order = %v, want [1 2]", order)
	}
	if inUse, queued, _ := l.Stats(); inUse != 0 || queued != 0 {
		t.Fatalf("leaked: inUse=%d queued=%d", inUse, queued)
	}
}

func TestLimiterAbandonLeavesQueue(t *testing.T) {
	l := NewLimiter(1, 0)
	release, _ := l.Acquire(context.Background(), false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx, true); err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if _, queued, _ := l.Stats(); queued != 0 {
		t.Fatalf("abandoned waiter still queued")
	}
	release()
	if inUse, _, _ := l.Stats(); inUse != 0 {
		t.Fatalf("inUse = %d after release", inUse)
	}
}

func TestLimiterResizeGrantsWaiters(t *testing.T) {
	l := NewLimiter(1, 0)
	r1, _ := l.Acquire(context.Background(), false)
	got := make(chan func(), 1)
	go func() {
		r, _ := l.Acquire(context.Background(), false)
		got <- r
	}()
	waitFor(t, func() bool { _, q, _ := l.Stats(); return q == 1 })
	l.Resize(2, 0)
	select {
	case r2 := <-got:
		r2()
	case <-time.After(time.Second):
		t.Fatal("waiter not granted after resize")
	}
	r1()
}

// echo is a one-method gRPC service whose handler sleeps for whatever the
// test says and can call a downstream echo.
type echo struct {
	delay  atomic.Int64 // nanoseconds
	calls  atomic.Int64
	next   func(ctx context.Context) error
	failed atomic.Int64
}

var echoDesc = grpc.ServiceDesc{
	ServiceName: "test.Echo",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Do",
		Handler: func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
			in := new(timestamppb.Timestamp)
			if err := dec(in); err != nil {
				return nil, err
			}
			h := func(ctx context.Context, req any) (any, error) {
				return srv.(*echo).do(ctx, req.(*timestamppb.Timestamp))
			}
			if ic == nil {
				return h(ctx, in)
			}
			return ic(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/test.Echo/Do"}, h)
		},
	}},
}

func (e *echo) do(ctx context.Context, in *timestamppb.Timestamp) (*timestamppb.Timestamp, error) {
	e.calls.Add(1)
	time.Sleep(time.Duration(e.delay.Load()))
	if e.next != nil {
		if err := e.next(ctx); err != nil {
			e.failed.Add(1)
			return nil, err
		}
	}
	return in, nil
}

func startEcho(t *testing.T, a *Agent, e *echo) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(a.ServerOptions()...)
	gs.RegisterService(&echoDesc, e)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

func dialEcho(t *testing.T, a *Agent, peer, addr string) func(ctx context.Context) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, append(a.DialOptions(peer), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return func(ctx context.Context) error {
		return conn.Invoke(ctx, "/test.Echo/Do", timestamppb.Now(), new(timestamppb.Timestamp))
	}
}

func drain(a *Agent) []*pb.Span {
	var out []*pb.Span
	for {
		select {
		case s := <-a.spans:
			out = append(out, s)
		default:
			return out
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClientRetriesTimeoutsAndReportsEachAttempt(t *testing.T) {
	server := New("db", ServiceConfig{})
	e := &echo{}
	e.delay.Store(int64(60 * time.Millisecond))
	addr := startEcho(t, server, e)

	client := New("api", ServiceConfig{Clients: map[string]ClientPolicy{
		"db": {TimeoutMs: 20, MaxAttempts: 3, RetryOn: []string{"DEADLINE_EXCEEDED"}, PropagateDeadline: true},
	}})
	call := dialEcho(t, client, "db", addr)

	err := call(context.Background())
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	spans := drain(client)
	if len(spans) != 3 {
		t.Fatalf("got %d client spans, want 3", len(spans))
	}
	for i, s := range spans {
		if s.Attempt != uint32(i+1) || s.Outcome != pb.Outcome_TIMEOUT || s.Peer != "db" || s.Method != "Do" {
			t.Errorf("span %d = attempt %d outcome %v peer %q method %q", i, s.Attempt, s.Outcome, s.Peer, s.Method)
		}
	}
	// The server kept working on every attempt the client abandoned.
	waitFor(t, func() bool { return len(server.spans) >= 3 })
	for _, s := range drain(server) {
		if !s.Late {
			t.Errorf("server span for attempt %d not marked late", s.Attempt)
		}
		if s.Peer != "api" {
			t.Errorf("server span caller = %q, want api", s.Peer)
		}
	}
}

func TestNonRetryableCodesAreNotRetried(t *testing.T) {
	server := New("db", ServiceConfig{})
	addr := startEcho(t, server, &echo{next: func(context.Context) error {
		return status.Error(codes.InvalidArgument, "nope")
	}})
	client := New("api", ServiceConfig{Clients: map[string]ClientPolicy{
		"db": {MaxAttempts: 3, RetryOn: []string{"UNAVAILABLE"}, PropagateDeadline: true},
	}})
	if err := dialEcho(t, client, "db", addr)(context.Background()); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v", err)
	}
	if n := len(drain(client)); n != 1 {
		t.Fatalf("%d attempts, want 1", n)
	}
}

func TestRetryBudgetSuppressesRetries(t *testing.T) {
	server := New("db", ServiceConfig{Fault: Fault{ErrorRate: 1}})
	addr := startEcho(t, server, &echo{})
	client := New("api", ServiceConfig{Clients: map[string]ClientPolicy{
		"db": {MaxAttempts: 3, RetryOn: []string{"UNAVAILABLE"}, PropagateDeadline: true, Budget: &BudgetPolicy{Ratio: 0.1}},
	}})
	call := dialEcho(t, client, "db", addr)
	for i := 0; i < 50; i++ {
		_ = call(context.Background())
	}
	var attempts, suppressed int
	for _, s := range drain(client) {
		if s.Outcome == pb.Outcome_BUDGET {
			suppressed++
		} else {
			attempts++
		}
	}
	// 50 requests earn 5 retries; without a budget this would be 150
	// attempts. Every request ends with exactly one suppressed retry: 45 at
	// the second attempt, and the 5 that got a retry at their third.
	if attempts != 55 || suppressed != 50 {
		t.Fatalf("attempts=%d suppressed=%d, want 55 and 50", attempts, suppressed)
	}
}

func TestShedExpiredDropsQueuedWork(t *testing.T) {
	server := New("db", ServiceConfig{Server: ServerPolicy{Workers: 1, ShedExpired: true}})
	e := &echo{}
	e.delay.Store(int64(50 * time.Millisecond))
	addr := startEcho(t, server, e)
	client := New("api", ServiceConfig{Clients: map[string]ClientPolicy{
		"db": {TimeoutMs: 30, MaxAttempts: 1, PropagateDeadline: true},
	}})
	call := dialEcho(t, client, "db", addr)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = call(context.Background()) }()
	}
	wg.Wait()
	waitFor(t, func() bool { return len(server.spans) >= 5 })
	expired := 0
	for _, s := range drain(server) {
		if s.Outcome == pb.Outcome_EXPIRED {
			expired++
		}
	}
	if e.calls.Load() != 1 || expired != 4 {
		t.Fatalf("handler ran %d times with %d expired, want 1 and 4", e.calls.Load(), expired)
	}
}

func TestQueueLimitSheds(t *testing.T) {
	server := New("db", ServiceConfig{Server: ServerPolicy{Workers: 1, QueueLimit: 1}})
	e := &echo{}
	e.delay.Store(int64(40 * time.Millisecond))
	addr := startEcho(t, server, e)
	client := New("api", ServiceConfig{})
	call := dialEcho(t, client, "db", addr)

	var wg sync.WaitGroup
	var shed atomic.Int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if status.Code(call(context.Background())) == codes.ResourceExhausted {
				shed.Add(1)
			}
		}()
	}
	wg.Wait()
	if shed.Load() != 2 {
		t.Fatalf("shed %d requests, want 2", shed.Load())
	}
}

// Without deadline propagation, a downstream call outlives the request
// that asked for it: the caller is gone but the leaf still does the work.
func TestDeadlinePropagation(t *testing.T) {
	for _, propagate := range []bool{true, false} {
		leafAgent := New("leaf", ServiceConfig{})
		leaf := &echo{}
		leaf.delay.Store(int64(80 * time.Millisecond))
		leafAddr := startEcho(t, leafAgent, leaf)

		midAgent := New("mid", ServiceConfig{Clients: map[string]ClientPolicy{
			"leaf": {MaxAttempts: 1, PropagateDeadline: propagate},
		}})
		mid := &echo{next: dialEcho(t, midAgent, "leaf", leafAddr)}
		midAddr := startEcho(t, midAgent, mid)

		mid.delay.Store(int64(30 * time.Millisecond))
		edge := New("edge", ServiceConfig{Clients: map[string]ClientPolicy{
			"mid": {TimeoutMs: 20, MaxAttempts: 1, PropagateDeadline: true},
		}})
		_ = dialEcho(t, edge, "mid", midAddr)(context.Background())

		time.Sleep(150 * time.Millisecond)
		got := leaf.calls.Load()
		if propagate && got != 0 {
			t.Errorf("propagate=true: leaf did %d calls of orphaned work, want 0", got)
		}
		if !propagate && got != 1 {
			t.Errorf("propagate=false: leaf did %d calls, want 1 (orphaned)", got)
		}
	}
}

func TestSelfTimeExcludesDownstreamCalls(t *testing.T) {
	leafAgent := New("leaf", ServiceConfig{})
	leaf := &echo{}
	leaf.delay.Store(int64(40 * time.Millisecond))
	leafAddr := startEcho(t, leafAgent, leaf)

	midAgent := New("mid", ServiceConfig{})
	mid := &echo{next: dialEcho(t, midAgent, "leaf", leafAddr)}
	mid.delay.Store(int64(10 * time.Millisecond))
	midAddr := startEcho(t, midAgent, mid)

	edge := New("edge", ServiceConfig{})
	if err := dialEcho(t, edge, "mid", midAddr)(context.Background()); err != nil {
		t.Fatal(err)
	}
	var server *pb.Span
	for _, s := range drain(midAgent) {
		if s.Kind == pb.Kind_SERVER {
			server = s
		}
	}
	if server == nil {
		t.Fatal("no server span")
	}
	self := time.Duration(server.SelfNanos)
	if self < 10*time.Millisecond || self > 30*time.Millisecond {
		t.Fatalf("self time %v, want about 10ms (total %v)", self, time.Duration(server.DurationNanos))
	}
}
