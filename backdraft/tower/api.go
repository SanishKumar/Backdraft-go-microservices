package tower

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/analysis"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
	"github.com/SanishKumar/go-microservices/internal/jsonutil"
)

// Handler serves the HTTP API, the live WebSocket and, if configured,
// the UI.
func (t *Tower) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, t.state()) })
	mux.HandleFunc("GET /api/findings", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, t.currentFindings()) })
	mux.HandleFunc("GET /api/live", t.handleLive)
	mux.HandleFunc("GET /ws", t.serveWS)

	mux.HandleFunc("PUT /api/services/{name}/config", t.handleSetConfig)
	mux.HandleFunc("POST /api/patch", t.handlePatch)
	mux.HandleFunc("POST /api/mitigations", t.handleMitigations)
	mux.HandleFunc("POST /api/reset", t.handleReset)
	mux.HandleFunc("POST /api/fault", t.handleFault)
	mux.HandleFunc("GET /api/load", t.handleGetLoad)
	mux.HandleFunc("PUT /api/load", t.handleSetLoad)

	mux.HandleFunc("GET /api/traces", t.handleTraces)
	mux.HandleFunc("GET /api/traces/{id}", t.handleTrace)

	mux.HandleFunc("GET /api/model", t.handleModel)
	mux.HandleFunc("POST /api/sim/run", t.simulation(t.handleSimRun))
	mux.HandleFunc("POST /api/sim/map", t.simulation(t.handleSimMap))
	mux.HandleFunc("POST /api/sim/hysteresis", t.simulation(t.handleSimHysteresis))

	if dir := t.opts.UIDir; dir != "" {
		mux.Handle("/", spa(dir))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !upgrader.CheckOrigin(r) {
			httpError(w, http.StatusForbidden, "cross-origin requests are not allowed")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf(format, args...)})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := jsonutil.DecodeObject(r.Body, v); err != nil {
		httpError(w, http.StatusBadRequest, "bad request body: %v", err)
		return false
	}
	return true
}

func (t *Tower) simulation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case t.simSlots <- struct{}{}:
			defer func() { <-t.simSlots }()
		default:
			httpError(w, http.StatusTooManyRequests, "two simulations are already running; retry shortly")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}

func simulationError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, context.DeadlineExceeded) {
		code = http.StatusGatewayTimeout
	} else if errors.Is(err, context.Canceled) {
		code = http.StatusRequestTimeout
	}
	httpError(w, code, "%v", err)
}

func (t *Tower) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var cfg backdraft.ServiceConfig
	if !readJSON(w, r, &cfg) {
		return
	}
	name := r.PathValue("name")
	if err := t.setConfigs(map[string]backdraft.ServiceConfig{name: cfg}, "Edited "+name+" policy"); err != nil {
		httpError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, t.state())
}

func (t *Tower) handlePatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Patch  analysis.Patch `json:"patch"`
		Reason string         `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Patch) == 0 {
		httpError(w, http.StatusBadRequest, "empty patch")
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = "Applied fix"
	}
	if err := t.setConfigs(req.Patch, reason); err != nil {
		httpError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, t.state())
}

// handleMitigations rebuilds every policy from the declared baseline with
// the selected mitigations, so switching one off really removes it.
func (t *Tower) handleMitigations(w http.ResponseWriter, r *http.Request) {
	var m analysis.Mitigations
	if !readJSON(w, r, &m) {
		return
	}
	before := analysis.Detect(t.system(false))
	sys := analysis.Mitigate(t.system(true), m)
	if err := t.setConfigs(sys.Services, describeMitigations(before, m)); err != nil {
		httpError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, t.state())
}

func describeMitigations(before, after analysis.Mitigations) string {
	var on, off []string
	names := []struct {
		was, is bool
		name    string
	}{
		{before.RetryBudgets, after.RetryBudgets, "retry budgets"},
		{before.SingleLayerRetries, after.SingleLayerRetries, "single-layer retries"},
		{before.PropagateDeadlines, after.PropagateDeadlines, "deadline propagation"},
		{before.ShedExpired, after.ShedExpired, "shedding expired work"},
		{before.BoundedQueues, after.BoundedQueues, "bounded queues"},
	}
	for _, n := range names {
		switch {
		case n.is && !n.was:
			on = append(on, n.name)
		case !n.is && n.was:
			off = append(off, n.name)
		}
	}
	var parts []string
	if len(on) > 0 {
		parts = append(parts, "On: "+strings.Join(on, ", "))
	}
	if len(off) > 0 {
		parts = append(parts, "Off: "+strings.Join(off, ", "))
	}
	if len(parts) == 0 {
		return "Mitigations reapplied"
	}
	return strings.Join(parts, ". ")
}

func (t *Tower) handleReset(w http.ResponseWriter, r *http.Request) {
	t.resetPolicies()
	writeJSON(w, t.state())
}

func (t *Tower) handleFault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service   string  `json:"service"`
		LatencyMs int     `json:"latencyMs"`
		ErrorRate float64 `json:"errorRate"`
		DurationS float64 `json:"durationS"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.LatencyMs < 0 || req.LatencyMs > 30_000 || req.ErrorRate < 0 || req.ErrorRate > 1 || req.DurationS < 0 || req.DurationS > 86400 {
		httpError(w, http.StatusBadRequest, "latencyMs must be 0-30000, errorRate 0-1, durationS 0-86400")
		return
	}
	f := backdraft.Fault{LatencyMs: req.LatencyMs, ErrorRate: req.ErrorRate}
	if err := t.setFault(req.Service, f, time.Duration(req.DurationS*float64(time.Second))); err != nil {
		httpError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, t.state())
}

func (t *Tower) handleGetLoad(w http.ResponseWriter, r *http.Request) {
	if t.load == nil {
		httpError(w, http.StatusNotFound, "load generator disabled")
		return
	}
	writeJSON(w, t.load.config())
}

func (t *Tower) handleSetLoad(w http.ResponseWriter, r *http.Request) {
	if t.load == nil {
		httpError(w, http.StatusNotFound, "load generator disabled")
		return
	}
	var c LoadConfig
	if !readJSON(w, r, &c) {
		return
	}
	before := t.load.config()
	if err := c.Validate(); err != nil {
		httpError(w, http.StatusBadRequest, "%v", err)
		return
	}
	t.load.set(c)
	if after := t.load.config(); math.Round(after.RPS) != math.Round(before.RPS) {
		t.events.add("load", "", fmt.Sprintf("Load %.0f -> %.0f req/s", before.RPS, after.RPS))
	}
	t.broadcastState()
	writeJSON(w, t.load.config())
}

// handleLive returns the latest one-second snapshot of the system.
func (t *Tower) handleLive(w http.ResponseWriter, r *http.Request) {
	t.historyMu.Lock()
	defer t.historyMu.Unlock()
	if len(t.history) == 0 {
		httpError(w, http.StatusServiceUnavailable, "no data yet")
		return
	}
	writeJSON(w, t.history[len(t.history)-1])
}

func (t *Tower) handleTraces(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 60
	}
	writeJSON(w, t.traces.list(r.URL.Query().Get("filter") != "all", limit, t.opts.Entry))
}

func (t *Tower) handleTrace(w http.ResponseWriter, r *http.Request) {
	spans, ok := t.traces.get(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusNotFound, "trace not found (it may have been evicted)")
		return
	}
	writeJSON(w, map[string]any{"id": r.PathValue("id"), "spans": spans})
}

func (t *Tower) handleModel(w http.ResponseWriter, r *http.Request) {
	m, cal := t.model(t.system(false))
	writeJSON(w, map[string]any{"model": m, "calibration": cal})
}

// variant names a policy set to simulate.
type variant struct {
	Name string `json:"name"`
	// Base is "current" (default), "declared" or "mitigated".
	Base string `json:"base,omitempty"`
	// Mitigations, if set, are applied to the declared policy instead.
	Mitigations *analysis.Mitigations `json:"mitigations,omitempty"`
	// Patch is applied on top of the base.
	Patch analysis.Patch `json:"patch,omitempty"`
}

func (t *Tower) variantModel(v variant) *sim.Model {
	var sys backdraft.SystemConfig
	switch {
	case v.Mitigations != nil:
		sys = analysis.Mitigate(t.system(true), *v.Mitigations)
	case v.Base == "declared":
		sys = t.system(true)
	case v.Base == "mitigated":
		sys = analysis.Mitigate(t.system(false), analysis.All())
	default:
		sys = t.system(false)
	}
	for name, cfg := range v.Patch {
		sys.Services[name] = cfg.Clone()
	}
	m, _ := t.model(sys)
	return m
}

func (t *Tower) variantModels(w http.ResponseWriter, variants []variant) ([]*sim.Model, bool) {
	if len(variants) > 4 {
		httpError(w, http.StatusBadRequest, "at most four policy variants")
		return nil, false
	}
	known := t.system(false).Services
	models := make([]*sim.Model, len(variants))
	for i, v := range variants {
		if v.Base != "" && v.Base != "current" && v.Base != "declared" && v.Base != "mitigated" {
			httpError(w, http.StatusBadRequest, "unknown policy base %q", v.Base)
			return nil, false
		}
		for name, cfg := range v.Patch {
			if _, ok := known[name]; !ok {
				httpError(w, http.StatusBadRequest, "unknown service %q", name)
				return nil, false
			}
			if err := cfg.Validate(); err != nil {
				httpError(w, http.StatusBadRequest, "invalid %s policy: %v", name, err)
				return nil, false
			}
			for peer := range cfg.Clients {
				if _, ok := known[peer]; !ok {
					httpError(w, http.StatusBadRequest, "unknown dependency %q", peer)
					return nil, false
				}
			}
		}
		m := t.variantModel(v)
		if err := m.Validate(); err != nil {
			httpError(w, http.StatusConflict, "model not ready: %v", err)
			return nil, false
		}
		models[i] = m
	}
	return models, true
}

func (t *Tower) validFaultService(w http.ResponseWriter, service string) bool {
	if service != "" {
		if _, ok := t.system(false).Services[service]; !ok {
			httpError(w, http.StatusBadRequest, "unknown fault service %q", service)
			return false
		}
	}
	return true
}

func (t *Tower) defaultLoad() float64 {
	if t.load != nil {
		if rps := t.load.config().RPS; rps > 0 {
			return rps
		}
	}
	return 150
}

type variantResult struct {
	Name   string      `json:"name"`
	Result *sim.Result `json:"result"`
	WallMs int64       `json:"wallMs"`
}

func (t *Tower) handleSimRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RPS       float64   `json:"rps"`
		Service   string    `json:"service"`
		LatencyMs float64   `json:"latencyMs"`
		ErrorRate float64   `json:"errorRate"`
		WarmS     float64   `json:"warmS"`
		FaultS    float64   `json:"faultS"`
		ObserveS  float64   `json:"observeS"`
		Seed      uint64    `json:"seed"`
		Variants  []variant `json:"variants"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.RPS < 0 || req.WarmS < 0 || req.FaultS < 0 || req.ObserveS < 0 || req.LatencyMs < 0 || req.LatencyMs > 30_000 || req.ErrorRate < 0 || req.ErrorRate > 1 {
		httpError(w, http.StatusBadRequest, "rates and durations must be non-negative, latencyMs 0-30000, errorRate 0-1")
		return
	}
	if !t.validFaultService(w, req.Service) {
		return
	}
	if req.RPS <= 0 {
		req.RPS = t.defaultLoad()
	}
	if req.WarmS <= 0 {
		req.WarmS = 8
	}
	if req.ObserveS <= 0 {
		req.ObserveS = 30
	}
	if req.WarmS+req.FaultS+req.ObserveS > 600 || req.RPS > 20_000 {
		httpError(w, http.StatusBadRequest, "scenario too large: at most 600 simulated seconds and 20000 req/s")
		return
	}
	if len(req.Variants) == 0 {
		req.Variants = []variant{{Name: "Current policy"}, {Name: "With all mitigations", Base: "mitigated"}}
	}
	models, ok := t.variantModels(w, req.Variants)
	if !ok {
		return
	}
	sc := sim.Brownout(req.RPS, req.Service, req.LatencyMs, req.WarmS, req.FaultS, req.ObserveS, req.Seed)
	sc.Faults[0].ErrorRate = req.ErrorRate
	sc.StepS = 0.5
	if req.Service == "" || req.FaultS <= 0 {
		sc.Faults = nil
	}
	out := make([]variantResult, len(req.Variants))
	for i, v := range req.Variants {
		start := time.Now()
		res, err := sim.RunContext(r.Context(), models[i], sc)
		if err != nil {
			simulationError(w, err)
			return
		}
		out[i] = variantResult{Name: v.Name, Result: res, WallMs: time.Since(start).Milliseconds()}
	}
	writeJSON(w, map[string]any{"scenario": sc, "variants": out})
}

func (t *Tower) handleSimMap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		sim.MapSpec
		Variant variant `json:"variant"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.WarmS < 0 || req.ObserveS < 0 || req.LatencyMs < 0 || req.LatencyMs > 30_000 {
		httpError(w, http.StatusBadRequest, "durations must be non-negative and latencyMs 0-30000")
		return
	}
	if req.WarmS == 0 {
		req.WarmS = 6
	}
	if req.ObserveS == 0 {
		req.ObserveS = 25
	}
	models, ok := t.variantModels(w, []variant{req.Variant})
	if !ok {
		return
	}
	m := models[0]
	capacity, bottleneck := capacityOf(m)
	if len(req.Loads) == 0 {
		// From light load to half of capacity: well inside the range any
		// capacity plan calls safe.
		for f := 0.05; f <= 0.501; f += 0.05 {
			req.Loads = append(req.Loads, math.Round(capacity*f))
		}
	}
	if len(req.Durations) == 0 {
		req.Durations = []float64{1, 2, 4, 6, 8, 12, 16, 20}
	}
	if req.LatencyMs <= 0 {
		req.LatencyMs = 300
	}
	if len(req.Loads)*len(req.Durations) > 400 {
		httpError(w, http.StatusBadRequest, "at most 400 cells")
		return
	}
	if req.Service == "" {
		req.Service = bottleneck
	}
	if !t.validFaultService(w, req.Service) {
		return
	}
	for _, load := range req.Loads {
		if load < 0 || load > 20_000 {
			httpError(w, http.StatusBadRequest, "map load must be within 0-20000 req/s")
			return
		}
	}
	for _, duration := range req.Durations {
		if duration < 0 || req.WarmS+duration+req.ObserveS > 600 {
			httpError(w, http.StatusBadRequest, "each map scenario must be within 600 simulated seconds with non-negative durations")
			return
		}
	}
	start := time.Now()
	cells, err := sim.StabilityMapContext(r.Context(), m, req.MapSpec)
	if err != nil {
		simulationError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"cells": cells, "loads": req.Loads, "durations": req.Durations,
		"service": req.Service, "latencyMs": req.LatencyMs,
		"capacity": capacity, "bottleneck": bottleneck, "wallMs": time.Since(start).Milliseconds(),
	})
}

func (t *Tower) handleSimHysteresis(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lo, Hi, PeriodS float64
		Seed            uint64
		Variants        []variant
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Lo < 0 || req.Hi < 0 || req.PeriodS < 0 {
		httpError(w, http.StatusBadRequest, "load and period must be non-negative")
		return
	}
	if len(req.Variants) == 0 {
		req.Variants = []variant{{Name: "Current policy"}, {Name: "With all mitigations", Base: "mitigated"}}
	}
	models, ok := t.variantModels(w, req.Variants)
	if !ok {
		return
	}
	capacity, _ := capacityOf(models[0])
	if req.Hi <= 0 {
		req.Hi = math.Round(1.2 * capacity)
	}
	if req.Lo <= 0 {
		req.Lo = math.Round(0.04 * capacity)
	}
	if req.PeriodS <= 0 {
		req.PeriodS = 160
	}
	if req.PeriodS > 600 || req.Hi > 20_000 || req.Lo > req.Hi {
		httpError(w, http.StatusBadRequest, "at most 600 simulated seconds and 20000 req/s")
		return
	}
	out := make([]variantResult, len(req.Variants))
	for i, v := range req.Variants {
		start := time.Now()
		res, err := sim.HysteresisContext(r.Context(), models[i], req.Lo, req.Hi, req.PeriodS, req.Seed)
		if err != nil {
			simulationError(w, err)
			return
		}
		out[i] = variantResult{Name: v.Name, Result: res, WallMs: time.Since(start).Milliseconds()}
	}
	writeJSON(w, map[string]any{"lo": req.Lo, "hi": req.Hi, "periodS": req.PeriodS, "capacity": capacity, "variants": out})
}

// spa serves a built single-page app, falling back to index.html for
// client-side routes.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
		if st, err := os.Stat(p); err != nil || st.IsDir() && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}
