// Package tower is the Backdraft control plane. Agents open a session to
// it, report every attempt they make and every request they serve, and
// receive configuration. From that stream it keeps live metrics and
// traces, calibrates a simulation model, runs the analyzer, and serves
// the API and UI that tie them together.
package tower

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/analysis"
	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

// Options configures a Tower.
type Options struct {
	// Entry is the pseudo-service whose client policy models users.
	Entry string
	// LoadTarget is the GraphQL endpoint the load generator drives. Empty
	// disables the generator.
	LoadTarget string
	// EntryPolicy is the users' declared policy, used when the generator
	// is enabled.
	EntryPolicy backdraft.ServiceConfig
	// Fallback describes the system before there is traffic to calibrate
	// from: call shapes, service times and the request mix.
	Fallback *sim.Model
	// InitialRPS is the load generator's starting rate.
	InitialRPS float64
	// UIDir, when set, is served at /.
	UIDir string
}

// Tower is the control plane.
type Tower struct {
	pb.UnimplementedControlPlaneServer

	opts    Options
	metrics *metrics
	traces  *traceStore
	calib   *calibrator
	events  *eventLog
	hub     *hub
	load    *loadGen

	mu       sync.Mutex
	services map[string]*serviceState
	faults   map[string]*time.Timer

	findingsMu  sync.Mutex
	findings    analysis.Report
	findingsKey string

	historyMu sync.Mutex
	history   []Snapshot
}

type serviceState struct {
	declared  backdraft.ServiceConfig
	effective backdraft.ServiceConfig
	sessions  map[*session]struct{}
	seen      bool
}

type session struct {
	instance string
	out      chan []byte
}

// push replaces any undelivered config with the latest one.
func (s *session) push(cfg []byte) {
	select {
	case <-s.out:
	default:
	}
	select {
	case s.out <- cfg:
	default:
	}
}

// New creates a control plane. Call Run to start its loops.
func New(opts Options) *Tower {
	if opts.Entry == "" {
		opts.Entry = "users"
	}
	t := &Tower{
		opts:     opts,
		metrics:  newMetrics(),
		traces:   newTraceStore(),
		calib:    newCalibrator(opts.Entry),
		events:   newEventLog(),
		hub:      newHub(),
		services: map[string]*serviceState{},
		faults:   map[string]*time.Timer{},
	}
	if opts.Fallback != nil {
		// Known services show up (as not yet connected) before they do.
		for name, s := range opts.Fallback.Services {
			t.services[name] = &serviceState{declared: s.Config.Clone(), effective: s.Config.Clone(), sessions: map[*session]struct{}{}}
		}
	}
	return t
}

// Run starts the tick loop and, if configured, the load generator, whose
// users agent reports to the control plane at grpcAddr. It blocks until
// ctx ends.
func (t *Tower) Run(ctx context.Context, grpcAddr string) {
	if t.opts.LoadTarget != "" {
		users := backdraft.New(t.opts.Entry, t.opts.EntryPolicy)
		go users.Run(ctx, grpcAddr)
		t.load = newLoadGen(t.opts.LoadTarget, users, t.metrics)
		t.load.set(LoadConfig{RPS: t.opts.InitialRPS})
		go t.load.run(ctx)
	}
	t.events.add("info", "", "Control plane started")

	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	analyze := time.NewTicker(2 * time.Second)
	defer analyze.Stop()
	t.refreshFindings()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			snap := t.metrics.roll(now)
			t.historyMu.Lock()
			light := snap
			light.Particles = nil
			t.history = append(t.history, light)
			if len(t.history) > 720 {
				t.history = t.history[len(t.history)-600:]
			}
			t.historyMu.Unlock()
			t.hub.broadcast(message{Type: "tick", Tick: &snap, Events: t.events.since(now.Add(-tickInterval))})
		case <-analyze.C:
			t.refreshFindings()
		}
	}
}

// Session implements pb.ControlPlaneServer.
func (t *Tower) Session(stream pb.ControlPlane_SessionServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil || hello.Service == "" {
		return errors.New("first message must be a Hello naming the service")
	}
	var declared backdraft.ServiceConfig
	if err := json.Unmarshal(hello.DeclaredConfig, &declared); err != nil {
		return fmt.Errorf("declared config: %w", err)
	}
	name := hello.Service
	sess := &session{instance: hello.Instance, out: make(chan []byte, 1)}

	t.mu.Lock()
	st := t.services[name]
	if st == nil {
		st = &serviceState{declared: declared, effective: declared.Clone(), sessions: map[*session]struct{}{}}
		t.services[name] = st
	} else {
		overridden := !reflect.DeepEqual(st.effective, st.declared)
		st.declared = declared
		if !overridden {
			st.effective = declared.Clone()
		}
	}
	firstSeen := !st.seen
	st.seen = true
	st.sessions[sess] = struct{}{}
	if !reflect.DeepEqual(st.effective, st.declared) {
		cfg, _ := json.Marshal(st.effective)
		sess.push(cfg)
	}
	t.mu.Unlock()
	if firstSeen {
		t.events.add("info", name, name+" connected")
	}
	t.broadcastState()
	defer func() {
		t.mu.Lock()
		delete(st.sessions, sess)
		t.mu.Unlock()
		t.metrics.dropInstance(name, sess.instance)
		t.broadcastState()
	}()

	ctx := stream.Context()
	sendErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case cfg := <-sess.out:
				if err := stream.Send(&pb.ControlMessage{Config: cfg}); err != nil {
					sendErr <- err
					return
				}
			}
		}
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		select {
		case err := <-sendErr:
			return err
		default:
		}
		switch m := msg.Msg.(type) {
		case *pb.AgentMessage_Spans:
			t.metrics.ingest(m.Spans.Spans)
			t.traces.add(m.Spans.Spans)
			t.calib.ingest(m.Spans.Spans)
		case *pb.AgentMessage_Gauges:
			t.metrics.gauge(name, sess.instance, m.Gauges)
		}
	}
}

// system returns the effective (or declared) configuration of every
// known service.
func (t *Tower) system(declared bool) backdraft.SystemConfig {
	t.mu.Lock()
	defer t.mu.Unlock()
	sys := backdraft.SystemConfig{Entry: t.opts.Entry, Services: map[string]backdraft.ServiceConfig{}}
	for name, st := range t.services {
		if declared {
			sys.Services[name] = st.declared.Clone()
		} else {
			sys.Services[name] = st.effective.Clone()
		}
	}
	return sys
}

// setConfigs replaces the effective configuration of the named services
// and pushes it to their agents.
func (t *Tower) setConfigs(cfgs map[string]backdraft.ServiceConfig, reason string) error {
	for name, cfg := range cfgs {
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	t.mu.Lock()
	var changed []string
	for name, cfg := range cfgs {
		st := t.services[name]
		if st == nil {
			t.mu.Unlock()
			return fmt.Errorf("unknown service %q", name)
		}
		if reflect.DeepEqual(st.effective, cfg) {
			continue
		}
		st.effective = cfg.Clone()
		raw, _ := json.Marshal(st.effective)
		for sess := range st.sessions {
			sess.push(raw)
		}
		changed = append(changed, name)
	}
	t.mu.Unlock()
	if len(changed) > 0 {
		sort.Strings(changed)
		if reason != "" {
			t.events.add("config", strings.Join(changed, ","), reason)
		}
		t.broadcastState()
		go t.refreshFindings()
	}
	return nil
}

// setFault injects a brownout into service, cleared after d if d > 0.
func (t *Tower) setFault(service string, f backdraft.Fault, d time.Duration) error {
	sys := t.system(false)
	cfg, ok := sys.Services[service]
	if !ok {
		return fmt.Errorf("unknown service %q", service)
	}
	cfg.Fault = f
	reason := fmt.Sprintf("Fault on %s: +%dms latency", service, f.LatencyMs)
	if f.ErrorRate > 0 {
		reason += fmt.Sprintf(", %.0f%% errors", 100*f.ErrorRate)
	}
	if d > 0 {
		reason += fmt.Sprintf(" for %s", d)
	}
	if !f.Active() {
		reason = "Fault on " + service + " cleared"
	}
	if err := t.setConfigs(map[string]backdraft.ServiceConfig{service: cfg}, ""); err != nil {
		return err
	}
	t.events.add("fault", service, reason)
	t.mu.Lock()
	if old := t.faults[service]; old != nil {
		old.Stop()
		delete(t.faults, service)
	}
	if d > 0 && f.Active() {
		t.faults[service] = time.AfterFunc(d, func() { t.setFault(service, backdraft.Fault{}, 0) })
	}
	t.mu.Unlock()
	return nil
}

// observed gathers what live traffic says, for the analyzer.
func (t *Tower) observed() *analysis.Observed {
	edgeRPS, hold := t.metrics.observedRates()
	seqs, methods := t.calib.observed()
	obs := &analysis.Observed{EdgeRPS: map[analysis.Edge]float64{}, HoldMs: hold, Sequences: seqs, Methods: methods}
	for k, v := range edgeRPS {
		if i := strings.IndexByte(k, '>'); i > 0 {
			obs.EdgeRPS[analysis.Edge{From: k[:i], To: k[i+1:]}] = v
		}
	}
	return obs
}

func (t *Tower) refreshFindings() {
	sys := t.system(false)
	// Faults are an experiment, not policy: analyze without them.
	for name, cfg := range sys.Services {
		cfg.Fault = backdraft.Fault{}
		sys.Services[name] = cfg
	}
	report := analysis.Analyze(analysis.Input{System: sys, Observed: t.observed()})
	var key strings.Builder
	for _, f := range report.Findings {
		key.WriteString(f.ID + f.Title + "\n")
	}
	t.findingsMu.Lock()
	changed := key.String() != t.findingsKey
	t.findings, t.findingsKey = report, key.String()
	t.findingsMu.Unlock()
	if changed {
		t.hub.broadcast(message{Type: "findings", Findings: &report})
	}
}

func (t *Tower) currentFindings() analysis.Report {
	t.findingsMu.Lock()
	defer t.findingsMu.Unlock()
	return t.findings
}

// ServiceView is a service as the UI sees it.
type ServiceView struct {
	Name      string                  `json:"name"`
	Declared  backdraft.ServiceConfig `json:"declared"`
	Effective backdraft.ServiceConfig `json:"effective"`
	Instances int                     `json:"instances"`
}

// State is the slow-changing part of the control plane's view.
type State struct {
	Entry       string               `json:"entry"`
	Services    []ServiceView        `json:"services"`
	Load        *LoadConfig          `json:"load,omitempty"`
	LoadReady   bool                 `json:"loadReady"`
	Mitigations analysis.Mitigations `json:"mitigations"`
	Events      []Event              `json:"events"`
	Calibration Calibration          `json:"calibration"`
	Capacity    float64              `json:"capacity"`
	Bottleneck  string               `json:"bottleneck"`
	Faults      map[string]bool      `json:"faults"`
	Paths       []analysis.Path      `json:"paths"`
	Worst       map[string]float64   `json:"worstAttempts"`
	Topology    map[string][]string  `json:"topology"`
}

var (
	capacityMu    sync.Mutex
	capacityCache = map[string][2]any{}
)

// capacity estimates the system's healthy capacity from the current
// model, cached per policy set.
func capacityOf(m *sim.Model) (float64, string) {
	raw, _ := json.Marshal(m.System())
	key := string(raw)
	capacityMu.Lock()
	if v, ok := capacityCache[key]; ok {
		capacityMu.Unlock()
		return v[0].(float64), v[1].(string)
	}
	capacityMu.Unlock()
	c, b := sim.Capacity(m)
	capacityMu.Lock()
	if len(capacityCache) > 64 {
		capacityCache = map[string][2]any{}
	}
	capacityCache[key] = [2]any{c, b}
	capacityMu.Unlock()
	return c, b
}

func (t *Tower) state() State {
	effective := t.system(false)
	st := State{Entry: t.opts.Entry, Faults: map[string]bool{}, Topology: map[string][]string{}}
	t.mu.Lock()
	for _, name := range sortedKeys(t.services) {
		s := t.services[name]
		st.Services = append(st.Services, ServiceView{
			Name: name, Declared: s.declared, Effective: s.effective, Instances: len(s.sessions),
		})
		st.Faults[name] = s.effective.Fault.Active()
		st.Topology[name] = s.effective.Peers()
	}
	t.mu.Unlock()
	if t.load != nil {
		cfg := t.load.config()
		st.Load = &cfg
		st.LoadReady = t.load.ready.Load()
	}
	st.Mitigations = analysis.Detect(effective)
	st.Events = t.events.recent(60)
	m, cal := t.model(effective)
	st.Calibration = cal
	st.Capacity, st.Bottleneck = capacityOf(m)
	report := t.currentFindings()
	st.Paths, st.Worst = report.Paths, report.WorstAttempts
	return st
}

func (t *Tower) broadcastState() {
	st := t.state()
	t.hub.broadcast(message{Type: "state", State: &st})
}

// model returns the calibrated simulation model under sys's policies.
func (t *Tower) model(sys backdraft.SystemConfig) (*sim.Model, Calibration) {
	for name, cfg := range sys.Services {
		cfg.Fault = backdraft.Fault{}
		sys.Services[name] = cfg
	}
	return t.calib.model(sys, t.opts.Fallback)
}
