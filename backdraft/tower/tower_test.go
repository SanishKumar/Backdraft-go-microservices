package tower

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
	"google.golang.org/grpc"
)

func TestHistogramQuantiles(t *testing.T) {
	var h hist
	for i := 1; i <= 100; i++ {
		h.add(time.Duration(i) * time.Millisecond)
	}
	// Buckets are 20% wide, so a quantile is accurate to within 20%.
	for q, want := range map[float64]float64{0.5: 50, 0.99: 99} {
		got := h.quantile(q)
		if got < want || got > want*1.2 {
			t.Errorf("q%.2f = %.1fms, want within 20%% above %.0fms", q, got, want)
		}
	}
	if (&hist{}).quantile(0.5) != 0 {
		t.Errorf("empty histogram quantile not 0")
	}
}

func TestShapeGroupsOverlappingCalls(t *testing.T) {
	shape := toShape([]child{
		{peer: "c", method: "Third", start: 300, end: 400},
		{peer: "a", method: "First", start: 0, end: 100},
		{peer: "b", method: "Second", start: 120, end: 250},
		{peer: "b", method: "Second", start: 130, end: 260}, // parallel with the one above
	})
	if len(shape) != 3 {
		t.Fatalf("got %d groups, want 3: %+v", len(shape), shape)
	}
	if len(shape[1]) != 2 || shape[0][0].Peer != "a" || shape[2][0].Peer != "c" {
		t.Fatalf("unexpected shape %+v", shape)
	}
}

func span(trace byte, attempt uint32, outcome pb.Outcome) *pb.Span {
	id := make([]byte, 16)
	id[0], id[1] = trace, byte(attempt)
	return &pb.Span{TraceId: id, SpanId: []byte{trace, 1, 2, 3, 4, 5, 6, 7}, Kind: pb.Kind_CLIENT,
		Service: "users", Peer: "gateway", Method: "Browse", Attempt: attempt, Outcome: outcome}
}

func TestTraceStoreKeepsInterestingTracesThroughAFlood(t *testing.T) {
	ts := newTraceStore()
	ts.add([]*pb.Span{span(1, 2, pb.Outcome_TIMEOUT)}) // a retry: interesting
	for i := 0; i < recentTraces+50; i++ {
		id := make([]byte, 16)
		id[2], id[3] = byte(i), byte(i>>8)
		ts.add([]*pb.Span{{TraceId: id, Kind: pb.Kind_CLIENT, Service: "users", Outcome: pb.Outcome_OK, Attempt: 1}})
	}
	got := ts.list(true, 10, "users")
	if len(got) != 1 || got[0].Retries != 1 {
		t.Fatalf("interesting traces = %+v, want the one retry", got)
	}
	if n := len(ts.list(false, recentTraces*2, "users")); n != recentTraces {
		t.Fatalf("recent ring holds %d traces, want %d", n, recentTraces)
	}
	if len(ts.traces) > recentTraces+interestingTraces {
		t.Fatalf("store leaks: %d traces", len(ts.traces))
	}
}

func TestCalibratorLearnsShapesSelfTimeAndMix(t *testing.T) {
	c := newCalibrator("users")
	parent := []byte{9, 9, 9, 9, 9, 9, 9, 9}
	var spans []*pb.Span
	for i := 0; i < minSamples; i++ {
		sid := []byte{byte(i), 1, 1, 1, 1, 1, 1, 1}
		spans = append(spans,
			&pb.Span{Kind: pb.Kind_CLIENT, Service: "users", Peer: "api", Method: "Get", Attempt: 1, Outcome: pb.Outcome_OK},
			&pb.Span{Kind: pb.Kind_CLIENT, Service: "api", Peer: "db", Method: "Read", Attempt: 1, ParentId: sid, StartUnixNano: 10, DurationNanos: 5},
			&pb.Span{Kind: pb.Kind_SERVER, Service: "api", Method: "Get", SpanId: sid, ParentId: parent, Outcome: pb.Outcome_OK, SelfNanos: 2e6},
		)
	}
	// A faulted request must not skew the model of healthy behaviour.
	spans = append(spans, &pb.Span{Kind: pb.Kind_SERVER, Service: "api", Method: "Get", SpanId: []byte{200, 0, 0, 0, 0, 0, 0, 0}, Outcome: pb.Outcome_OK, SelfNanos: 9e9, Faulted: true})
	c.ingest(spans)

	sys := backdraft.SystemConfig{Entry: "users", Services: map[string]backdraft.ServiceConfig{
		"users": {Clients: map[string]backdraft.ClientPolicy{"api": backdraft.DefaultClientPolicy()}},
		"api":   {Clients: map[string]backdraft.ClientPolicy{"db": backdraft.DefaultClientPolicy()}},
		"db":    {},
	}}
	m, cal := c.model(sys, nil)
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cal.OpsObserved || len(m.Ops) != 1 || m.Ops[0].Service != "api" {
		t.Fatalf("ops = %+v", m.Ops)
	}
	api := m.Services["api"]
	if len(api.SelfMs) != minSamples || api.SelfMs[0] != 2 {
		t.Fatalf("self samples = %d (first %v), want %d of 2ms", len(api.SelfMs), api.SelfMs[:1], minSamples)
	}
	if sh := api.Shapes["Get"]; len(sh) == 0 || sh[0][0][0] != (sim.Call{Peer: "db", Method: "Read"}) {
		t.Fatalf("shapes = %+v", api.Shapes)
	}
	seqs, methods := c.observed()
	if len(seqs["api"]) != 1 || len(methods) != 2 {
		t.Fatalf("observed sequences %v, methods %v", seqs, methods)
	}
}

// The control loop end to end: an agent connects, declares its policy,
// and enforces what the control plane pushes back.
func TestSessionPushesConfigToAgents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tw := New(Options{})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	pb.RegisterControlPlaneServer(gs, tw)
	go gs.Serve(lis)
	defer gs.Stop()

	declared := backdraft.ServiceConfig{Server: backdraft.ServerPolicy{Workers: 4}}
	agent := backdraft.New("db", declared)
	go agent.Run(ctx, lis.Addr().String())

	waitUntil(t, func() bool {
		_, ok := tw.system(true).Services["db"]
		return ok
	})
	if got := tw.system(true).Services["db"].Server.Workers; got != 4 {
		t.Fatalf("declared workers = %d", got)
	}

	want := declared.Clone()
	want.Server.Workers = 9
	want.Server.ShedExpired = true
	if err := tw.setConfigs(map[string]backdraft.ServiceConfig{"db": want}, "test"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return agent.Config().Server.Workers == 9 && agent.Config().Server.ShedExpired })

	if err := tw.setConfigs(map[string]backdraft.ServiceConfig{"db": {Server: backdraft.ServerPolicy{Workers: -1}}}, ""); err == nil {
		t.Fatal("accepted an invalid config")
	}
	if err := tw.setConfigs(map[string]backdraft.ServiceConfig{"nope": declared}, ""); err == nil {
		t.Fatal("accepted a config for an unknown service")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
