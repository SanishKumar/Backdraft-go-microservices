package tower

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/review"
	"github.com/SanishKumar/go-microservices/internal/jsonutil"
)

func (t *Tower) handleCaptureReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	sys := t.system(false)
	// Refuse to freeze an incomplete topology or a live fault into the baseline.
	t.mu.Lock()
	var unavailable string
	for name, st := range t.services {
		if len(st.sessions) == 0 || st.effective.Fault.Active() {
			unavailable = name
			break
		}
	}
	t.mu.Unlock()
	if unavailable != "" {
		httpError(w, 409, "%s is disconnected or has an active fault; connect all services and clear faults before capture", unavailable)
		return
	}
	for _, cfg := range sys.Services {
		if cfg.Fault.Active() {
			httpError(w, 409, "clear active faults before capture")
			return
		}
	}
	m, cal := t.model(sys)
	if err := m.Validate(); err != nil {
		httpError(w, 409, "model not ready: %v; connect services and send healthy traffic", err)
		return
	}
	capacity, target := capacityOf(m)
	load := capacity * 0.7
	// Prefer the actual offered load when the generator is enabled. Capacity
	// is only a suggested starting point for externally driven graphs.
	if t.load != nil && t.load.config().RPS > 0 {
		load = t.load.config().RPS
	}
	if target == "" || target == m.Entry {
		for _, name := range sortedKeys(m.Services) {
			if name != m.Entry {
				target = name
				break
			}
		}
	}
	if req.Name == "" {
		req.Name = "Policy review"
	}
	b := review.Bundle{Version: review.Version, Engine: review.Engine, Name: req.Name, Model: m, Candidate: review.ProposedPolicy(m), Contract: review.DefaultContract(target, load), Evidence: review.Evidence{
		Source: "capture", CapturedAt: time.Now().UTC(), SelfSamples: cal.SelfSamples, ShapeSamples: cal.ShapeSamples, MethodSamples: cal.MethodSamples, OpsObserved: cal.OpsObserved,
	}}
	if err := b.Validate(); err != nil {
		httpError(w, 409, "cannot capture: %v", err)
		return
	}
	writeJSON(w, map[string]any{"bundle": b, "warnings": b.Warnings()})
}

func (t *Tower) handleRunReview(w http.ResponseWriter, r *http.Request) {
	var b review.Bundle
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := jsonutil.DecodeObject(r.Body, &b); err != nil {
		httpError(w, 400, "invalid bundle: %v", err)
		return
	}
	if err := b.Validate(); err != nil {
		httpError(w, 400, "%v", err)
		return
	}
	select {
	case t.simSlots <- struct{}{}:
		defer func() { <-t.simSlots }()
	default:
		httpError(w, 429, "simulation workers are busy; retry when the running review finishes")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	report, err := review.Run(ctx, b)
	if err != nil {
		httpError(w, 408, "review stopped without a result: %v", err)
		return
	}
	record, err := t.opts.Reviews.Save(b, *report)
	if err != nil {
		httpError(w, 500, "could not save review: %v", err)
		return
	}
	writeJSON(w, record)
}

func (t *Tower) handleValidateReview(w http.ResponseWriter, r *http.Request) {
	var b review.Bundle
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := jsonutil.DecodeObject(r.Body, &b); err != nil {
		httpError(w, 400, "invalid bundle: %v", err)
		return
	}
	if err := b.Validate(); err != nil {
		httpError(w, 400, "%v", err)
		return
	}
	writeJSON(w, map[string]any{"bundle": b, "warnings": b.Warnings()})
}

func (t *Tower) handleReviewList(w http.ResponseWriter, r *http.Request) {
	items, err := t.opts.Reviews.List()
	if err != nil {
		httpError(w, 500, "could not read review history: %v", err)
		return
	}
	writeJSON(w, map[string]any{"items": items, "persistent": t.opts.Reviews.Persistent()})
}

func (t *Tower) handleReviewGet(w http.ResponseWriter, r *http.Request) {
	record, err := t.opts.Reviews.Get(r.PathValue("id"))
	if errors.Is(err, os.ErrNotExist) {
		httpError(w, 404, "review not found")
		return
	}
	if err != nil {
		httpError(w, 500, "could not read review: %v", err)
		return
	}
	if r.URL.Query().Get("download") == "bundle" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="review-%s.json"`, record.ID[:12]))
		writeJSON(w, record.Bundle)
		return
	}
	if r.URL.Query().Get("download") == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="policy-review.md"`)
		review.WriteMarkdown(w, record.Bundle, record.Report)
		return
	}
	writeJSON(w, record)
}
