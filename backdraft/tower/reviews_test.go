package tower

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/SanishKumar/go-microservices/backdraft/review"
)

func TestReviewAPIImportRunPersistenceAndCancellation(t *testing.T) {
	tw := apiTower()
	b := review.Bundle{Version: review.Version, Engine: review.Engine, Name: "Imported service", Model: tw.opts.Fallback, Candidate: review.ProposedPolicy(tw.opts.Fallback), Contract: review.DefaultContract("db", 10), Evidence: review.Evidence{Source: "declared"}}
	b.Contract.Seeds = []uint64{1}
	b.Contract.Durations = []float64{1}
	b.Contract.LatencyMs = 1
	raw, _ := json.Marshal(b)
	call := func(method, path string, body []byte) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		tw.Handler().ServeHTTP(rr, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return rr
	}
	for _, body := range []string{"null", `{"typo":true}`, string(raw) + " {}"} {
		if rr := call("POST", "/api/reviews/validate", []byte(body)); rr.Code != 400 {
			t.Fatalf("invalid import accepted: %d", rr.Code)
		}
	}
	if rr := call("POST", "/api/reviews/validate", raw); rr.Code != 200 {
		t.Fatal(rr.Body)
	}
	rr := call("POST", "/api/reviews", raw)
	if rr.Code != 200 {
		t.Fatalf("run %d: %s", rr.Code, rr.Body)
	}
	var record review.Record
	if err := json.Unmarshal(rr.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Report.Status != "pass" {
		t.Fatalf("unexpected result: %+v", record.Report)
	}
	// Imported reviews work with no connected agents and never alter live policy.
	if review.Digest(tw.system(false)) != review.Digest(b.Model.System()) {
		t.Fatal("review altered live policies")
	}
	if rr := call("GET", "/api/reviews/"+record.ID+"?download=bundle", nil); rr.Code != 200 || rr.Header().Get("Content-Disposition") == "" {
		t.Fatal("bundle export unavailable")
	}
	if rr := call("GET", "/api/reviews/"+record.ID+"?download=markdown", nil); rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte(record.Report.BundleDigest)) {
		t.Fatal("report export omitted identity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr = httptest.NewRecorder()
	tw.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/reviews", bytes.NewReader(raw)).WithContext(ctx))
	if rr.Code != 408 {
		t.Fatalf("cancellation: %d %s", rr.Code, rr.Body)
	}
	items, err := tw.opts.Reviews.List()
	if err != nil || len(items) != 1 {
		t.Fatal("canceled review was saved", err)
	}
	for i := 0; i < cap(tw.simSlots); i++ {
		tw.simSlots <- struct{}{}
	}
	if rr := call("POST", "/api/reviews", raw); rr.Code != 429 {
		t.Fatal("review bypassed worker limit")
	}
}

func TestCaptureRequiresConnectedFaultFreeServices(t *testing.T) {
	tw := apiTower()
	call := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		tw.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/reviews/capture", bytes.NewBufferString(`{"name":"Captured checkout"}`)))
		return rr
	}
	if rr := call(); rr.Code != 409 {
		t.Fatal("disconnected model was captured")
	}
	for _, st := range tw.services {
		st.sessions[&session{out: make(chan []byte, 1)}] = struct{}{}
	}
	rr := call()
	if rr.Code != 200 {
		t.Fatalf("capture %d: %s", rr.Code, rr.Body)
	}
	var response struct {
		Bundle   review.Bundle `json:"bundle"`
		Warnings []string      `json:"warnings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Bundle.Evidence.Source != "capture" || len(response.Warnings) < 2 {
		t.Fatal("unobserved capture hid evidence gaps")
	}
	cfg := tw.services["db"].effective
	cfg.Fault.LatencyMs = 100
	tw.services["db"].effective = cfg
	if rr := call(); rr.Code != 409 {
		t.Fatal("active fault was captured")
	}
}
