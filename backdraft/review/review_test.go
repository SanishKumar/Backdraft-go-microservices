package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/review"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
	"github.com/SanishKumar/go-microservices/internal/reference"
)

func bundle() review.Bundle {
	sys := reference.System()
	b := review.Bundle{Version: review.Version, Engine: review.Engine, Name: "Checkout retry policy", Model: reference.SimModel(sys), Candidate: review.ProposedPolicy(reference.SimModel(sys)), Contract: review.DefaultContract(reference.Catalog, 200), Evidence: review.Evidence{Source: "declared"}}
	b.Contract.Seeds = []uint64{2, 1}
	b.Contract.Loads = []float64{200}
	b.Contract.Durations = []float64{8, 2}
	return b
}

func TestProposalHardensRealEntryAndKeepsOnlyOneRetryLayer(t *testing.T) {
	p := backdraft.ClientPolicy{MaxAttempts: 3, TimeoutMs: 200, RetryOn: []string{"UNAVAILABLE"}}
	m := &sim.Model{Entry: "checkout", Ops: []sim.Op{{Name: "Read", Service: "checkout", Weight: 1}}, Services: map[string]sim.ServiceModel{
		"checkout": {Config: backdraft.ServiceConfig{Server: backdraft.ServerPolicy{Workers: 8}, Clients: map[string]backdraft.ClientPolicy{"catalog": p}}},
		"catalog":  {Config: backdraft.ServiceConfig{Server: backdraft.ServerPolicy{Workers: 4}, Clients: map[string]backdraft.ClientPolicy{"db": p}}},
		"db":       {Config: backdraft.ServiceConfig{Server: backdraft.ServerPolicy{Workers: 2}}},
	}}
	candidate := review.ProposedPolicy(m)
	entry := candidate.Services["checkout"]
	if candidate.Entry != "checkout" || len(candidate.Services) != 3 {
		t.Fatal("proposal leaked a synthetic ingress")
	}
	if !entry.Server.ShedExpired || entry.Server.QueueLimit != 32 || !entry.Clients["catalog"].PropagateDeadline || entry.Clients["catalog"].Budget == nil {
		t.Fatal("real entry was not protected")
	}
	if candidate.Services["catalog"].Clients["db"].MaxAttempts != 1 {
		t.Fatal("nested retries survived")
	}
	if m.Services["checkout"].Config.Server.ShedExpired {
		t.Fatal("proposal mutated frozen model")
	}
}

func TestReviewRetainsEverySeedAndDeterministicSmallestWitness(t *testing.T) {
	b := bundle()
	a, err := review.Run(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "pass" || a.CandidatePassed != 4 {
		t.Fatalf("candidate did not recover: %+v", a.Cases)
	}
	if a.BaselinePassed == 4 || a.Witness == nil || a.Witness.Side != "baseline" {
		t.Fatalf("missing baseline failure: %+v", a)
	}
	for i, c := range a.Cases {
		if c.Baseline.Status != "pass" {
			if a.Witness.CaseIndex != i {
				t.Fatal("did not select smallest tested failure")
			}
			break
		}
	}
	if a.Cases[0].Scenario.Duration != 2 || a.Cases[0].Scenario.Seed != 1 {
		t.Fatal("case order depends on input or worker scheduling")
	}
	if !reflect.DeepEqual(a.Witness.Baseline.Offered, a.Witness.Candidate.Offered) {
		t.Fatal("comparison used different ingress")
	}
	z, err := review.Run(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	a.CreatedAt = time.Time{}
	z.CreatedAt = time.Time{}
	if !reflect.DeepEqual(a, z) {
		t.Fatal("same bundle produced different evidence")
	}
	// Deliberately regress a passing policy back to the unbounded baseline.
	b.Model = b.Model.WithConfigs(b.Candidate.Services)
	b.Candidate = reference.System()
	r, err := review.Run(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "fail" || r.Regressions == 0 || r.Witness.Side != "candidate" {
		t.Fatalf("regression escaped the gate: %+v", r)
	}
}

func TestIncompleteCaptureCannotPassAndCancellationCannotReturnEvidence(t *testing.T) {
	b := bundle()
	b.Contract.Seeds = []uint64{1}
	b.Contract.Durations = []float64{2}
	b.Contract.Loads = []float64{30}
	b.Evidence.Source = "capture"
	r, err := review.Run(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if r.CandidatePassed != 1 || r.Status != "inconclusive" {
		t.Fatalf("partial calibration claimed a pass: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := review.Run(ctx, b); r != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled review returned evidence: %+v %v", r, err)
	}
}

func TestBundleRejectsChangedSemanticsAndUnboundedEnvelopes(t *testing.T) {
	for name, mutate := range map[string]func(*review.Bundle){
		"version":   func(b *review.Bundle) { b.Version++ },
		"nil model": func(b *review.Bundle) { b.Model = nil },
		"graph":     func(b *review.Bundle) { delete(b.Candidate.Services, "account") },
		"work": func(b *review.Bundle) {
			cfg := b.Candidate.Services["catalog"]
			cfg.Server.WorkMs++
			b.Candidate.Services["catalog"] = cfg
		},
		"methods": func(b *review.Bundle) {
			cfg := b.Candidate.Services["catalog"]
			cfg.Methods = nil
			b.Candidate.Services["catalog"] = cfg
		},
		"duplicate seed":   func(b *review.Bundle) { b.Contract.Seeds = []uint64{1, 1} },
		"unsafe JSON seed": func(b *review.Bundle) { b.Contract.Seeds = []uint64{1 << 63} },
		"missing target":   func(b *review.Bundle) { b.Contract.Services = []string{"unknown"} },
		"too many": func(b *review.Bundle) {
			b.Contract.Loads = []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
			b.Contract.Durations = []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := bundle()
			mutate(&b)
			if b.Validate() == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

func TestStoreSurvivesRestartAndDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := review.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := bundle()
	b.Contract.Seeds = []uint64{1}
	b.Contract.Durations = []float64{2}
	r, err := review.Run(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Save(b, *r)
	if err != nil {
		t.Fatal(err)
	}
	s, err = review.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Digest(got) != review.Digest(saved) {
		t.Fatal("persisted record changed")
	}
	if _, err = s.Get("../../policy"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe record path accepted")
	}
	got.Report.Status = "tampered"
	raw, _ := json.Marshal(got)
	if err = os.WriteFile(filepath.Join(dir, saved.ID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(saved.ID); err == nil {
		t.Fatal("tampered report accepted")
	}
	// Memory mode must be immutable too; callers retain mutable maps/slices.
	memory, _ := review.Open("")
	record, err := memory.Save(b, *r)
	if err != nil {
		t.Fatal(err)
	}
	record.Bundle.Contract.Seeds[0] = 999
	got, err = memory.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bundle.Contract.Seeds[0] != 1 {
		t.Fatal("caller changed a saved record")
	}
}
