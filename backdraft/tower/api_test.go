package tower

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

func apiTower() *Tower {
	return New(Options{Entry: "users", LoadTarget: "http://127.0.0.1:1/graphql", Fallback: &sim.Model{
		Entry: "users", Ops: []sim.Op{{Name: "Read", Service: "db", Weight: 1}},
		Services: map[string]sim.ServiceModel{
			"users": {Config: backdraft.ServiceConfig{Clients: map[string]backdraft.ClientPolicy{"db": {MaxAttempts: 1}}}},
			"db":    {Config: backdraft.ServiceConfig{Server: backdraft.ServerPolicy{Workers: 1}}, MeanSelfMs: 10},
		},
	}})
}

func TestAPIRejectsInvalidExperimentsAndPolicies(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/api/sim/run", `null`},
		{"/api/sim/run", `{} {}`},
		{"/api/sim/run", `{"typo":1}`},
		{"/api/sim/run", `{"faultS":-100}`},
		{"/api/sim/run", `{"service":"missing","faultS":8}`},
		{"/api/sim/run", `{"latencyMs":-1}`},
		{"/api/sim/run", `{"errorRate":2}`},
		{"/api/sim/run", `{"variants":[{},{},{},{},{}]}`},
		{"/api/sim/run", `{"variants":[{"base":"typo"}]}`},
		{"/api/sim/run", `{"variants":[{"patch":{"db":{"server":{"workers":-1}}}}]}`},
		{"/api/sim/map", `{"loads":[-1],"durations":[1]}`},
		{"/api/sim/map", `{"loads":[20001],"durations":[1]}`},
		{"/api/sim/map", `{"loads":[1],"durations":[1000000000]}`},
		{"/api/sim/map", `{"latencyMs":-1}`},
		{"/api/sim/hysteresis", `{"lo":200,"hi":100}`},
		{"/api/sim/hysteresis", `{"variants":[{}, {"patch":{"missing":{}}}]}`},
		{"/api/fault", `{"service":"db","durationS":1e99}`},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			apiTower().Handler().ServeHTTP(rr, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("HTTP %d: %s", rr.Code, rr.Body)
			}
		})
	}
}

func TestLoadMixValidationAndCrossOriginProtection(t *testing.T) {
	tw := apiTower()
	for _, body := range []string{`{"rps":-1}`, `{"mix":{}}`, `{"mix":{"Unknown":1}}`, `{"mix":{"Browse":-1}}`} {
		rr := httptest.NewRecorder()
		tw.Handler().ServeHTTP(rr, httptest.NewRequest("PUT", "/api/load", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %s", body, rr.Body)
		}
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/mitigations", strings.NewReader(`{"retryBudgets":true}`))
	req.Header.Set("Origin", "https://unrelated.example")
	tw.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write returned %d", rr.Code)
	}
}

func TestSimulationReadinessCancellationAndConcurrency(t *testing.T) {
	rr := httptest.NewRecorder()
	New(Options{}).Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/sim/hysteresis", strings.NewReader(`{}`)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("unready model returned %d", rr.Code)
	}
	tw := apiTower()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr = httptest.NewRecorder()
	tw.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/sim/run", strings.NewReader(`{"rps":5,"warmS":1,"observeS":1}`)).WithContext(ctx))
	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled simulation returned %d: %s", rr.Code, rr.Body)
	}
	for i := 0; i < cap(tw.simSlots); i++ {
		tw.simSlots <- struct{}{}
	}
	rr = httptest.NewRecorder()
	tw.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/sim/run", strings.NewReader(`{}`)))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("accepted work beyond concurrency limit: %d", rr.Code)
	}
}

func TestOldFaultTimerCannotClearNewExperimentOrRevertPolicy(t *testing.T) {
	tw := apiTower()
	if err := tw.setFault("db", backdraft.Fault{LatencyMs: 100}, time.Hour); err != nil {
		t.Fatal(err)
	}
	old := tw.faults["db"]
	if err := tw.setFault("db", backdraft.Fault{LatencyMs: 200}, time.Hour); err != nil {
		t.Fatal(err)
	}
	current := tw.faults["db"]
	t.Cleanup(func() {
		tw.mu.Lock()
		for _, f := range tw.faults {
			f.timer.Stop()
		}
		tw.mu.Unlock()
	})
	tw.expireFault("db", old)
	if tw.system(false).Services["db"].Fault.LatencyMs != 200 {
		t.Fatal("stale timer cleared a new fault")
	}
	cfg := tw.system(false).Services["db"]
	cfg.Server.Workers = 7
	cfg.Fault = backdraft.Fault{}
	if err := tw.setConfigs(map[string]backdraft.ServiceConfig{"db": cfg}, "test"); err != nil {
		t.Fatal(err)
	}
	if tw.system(false).Services["db"].Fault.LatencyMs != 200 {
		t.Fatal("policy update cleared an active fault")
	}
	tw.expireFault("db", current)
	if got := tw.system(false).Services["db"]; got.Server.Workers != 7 || got.Fault.Active() {
		t.Fatalf("expiry reverted policy: %+v", got)
	}
}

func TestCalibrationRetainsUnobservedMethodShapes(t *testing.T) {
	c := newCalibrator("users")
	sys := backdraft.SystemConfig{Entry: "users", Services: map[string]backdraft.ServiceConfig{"users": {}, "db": {}}}
	fallback := &sim.Model{Services: map[string]sim.ServiceModel{"db": {Shapes: map[string][]sim.Shape{
		"Read": {nil}, "Checkout": {{{{Peer: "db", Method: "Read"}}}},
	}}}}
	for i := 0; i < minSamples; i++ {
		c.ingest([]*pb.Span{{Kind: pb.Kind_SERVER, Service: "db", Method: "Read", Outcome: pb.Outcome_OK, SpanId: []byte{byte(i), 1, 2, 3, 4, 5, 6, 7}}})
	}
	m, _ := c.model(sys, fallback)
	if len(m.Services["db"].Shapes["Checkout"]) != 1 {
		t.Fatal("observing Read erased the known Checkout call shape")
	}
}

func TestStandaloneLearnsRealIngressWithoutReferenceServices(t *testing.T) {
	tw := New(Options{Entry: "api"})
	if len(tw.system(false).Services) != 0 {
		t.Fatal("standalone topology contains reference services")
	}
	tw.services["api"] = &serviceState{effective: backdraft.ServiceConfig{
		Server:  backdraft.ServerPolicy{Workers: 4},
		Clients: map[string]backdraft.ClientPolicy{"db": {MaxAttempts: 1}},
	}}
	tw.services["db"] = &serviceState{effective: backdraft.ServiceConfig{Server: backdraft.ServerPolicy{Workers: 2, WorkMs: 10}}}
	var spans []*pb.Span
	for i := 0; i < minSamples; i++ {
		sid := []byte{byte(i), 1, 2, 3, 4, 5, 6, 7}
		spans = append(spans,
			&pb.Span{Kind: pb.Kind_CLIENT, Service: "api", Peer: "db", Method: "Read", Attempt: 1, ParentId: sid, StartUnixNano: 1, DurationNanos: 1e6},
			&pb.Span{Kind: pb.Kind_SERVER, Service: "api", Method: "Checkout", SpanId: sid, Outcome: pb.Outcome_OK, SelfNanos: 1e6, DurationNanos: 2e6},
		)
	}
	tw.calib.ingest(spans)
	tw.metrics.ingest(spans)
	m, cal := tw.model(tw.system(false))
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cal.OpsObserved || len(m.Ops) != 1 || m.Ops[0].Service != "api" || m.Ops[0].Name != "Checkout" {
		t.Fatalf("ingress mix was confused with its downstream calls: %+v", m.Ops)
	}
	if shape := m.Services["api"].Shapes["Checkout"]; len(shape) != minSamples || shape[0][0][0].Peer != "db" {
		t.Fatalf("lost ingress call shape: %+v", shape)
	}
	if capacity, _ := sim.Capacity(m); capacity <= 0 {
		t.Fatal("real ingress model has no capacity")
	}
	if snapshot := tw.metrics.roll(time.Now()); snapshot.Entry.Goodput != minSamples {
		t.Fatalf("ingress traffic is missing: %+v", snapshot.Entry)
	}
}
