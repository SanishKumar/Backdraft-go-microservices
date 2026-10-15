package backdraft

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Agent enforces a service's ServiceConfig and reports spans for every
// inbound request and outbound attempt. One per service process.
type Agent struct {
	service  string
	instance string
	declared ServiceConfig

	cfg     atomic.Pointer[ServiceConfig]
	limiter *Limiter
	budgets sync.Map // peer -> *Budget

	spans   chan *pb.Span
	dropped atomic.Uint64
}

// New creates an agent for service with the policies compiled into it.
// The control plane may override them at runtime.
func New(service string, declared ServiceConfig) *Agent {
	host, _ := os.Hostname()
	a := &Agent{
		service:  service,
		instance: fmt.Sprintf("%s-%d", host, os.Getpid()),
		declared: declared.Clone(),
		limiter:  NewLimiter(declared.Server.Workers, declared.Server.QueueLimit),
		spans:    make(chan *pb.Span, 1<<14),
	}
	cfg := declared.Clone()
	a.cfg.Store(&cfg)
	return a
}

// Service returns the service name.
func (a *Agent) Service() string { return a.service }

// Config returns the configuration currently enforced.
func (a *Agent) Config() ServiceConfig { return a.cfg.Load().Clone() }

// Apply replaces the enforced configuration.
func (a *Agent) Apply(cfg ServiceConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg = cfg.Clone()
	a.cfg.Store(&cfg)
	a.limiter.Resize(cfg.Server.Workers, cfg.Server.QueueLimit)
	for peer, p := range cfg.Clients {
		if p.Budget != nil {
			if b, ok := a.budgets.Load(peer); ok {
				b.(*Budget).Set(*p.Budget)
			}
		}
	}
	return nil
}

func (a *Agent) budget(peer string, p BudgetPolicy) *Budget {
	if b, ok := a.budgets.Load(peer); ok {
		return b.(*Budget)
	}
	b, _ := a.budgets.LoadOrStore(peer, NewBudget(p))
	return b.(*Budget)
}

// emit hands a span to the reporter without ever blocking a request.
func (a *Agent) emit(s *pb.Span) {
	select {
	case a.spans <- s:
	default:
		a.dropped.Add(1)
	}
}

// work sleeps for a synthetic log-normal service time with the given mean.
// Sleeping (rather than spinning) keeps a laptop lab honest: what matters
// for queueing is that a worker is held, not that a core is busy.
func work(meanMs float64) time.Duration {
	if meanMs <= 0 {
		return 0
	}
	const sigma = 0.35
	mu := math.Log(meanMs) - sigma*sigma/2
	return time.Duration(math.Exp(mu+sigma*rand.NormFloat64()) * float64(time.Millisecond))
}

// Run maintains a session with the control plane at addr until ctx ends,
// reconnecting as needed. Without a session the agent keeps enforcing its
// last configuration and discards spans.
func (a *Agent) Run(ctx context.Context, addr string) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("backdraft: %s: control plane address %q: %v", a.service, addr, err)
		return
	}
	defer conn.Close()
	client := pb.NewControlPlaneClient(conn)
	delay := 250 * time.Millisecond
	for ctx.Err() == nil {
		start := time.Now()
		err := a.session(ctx, client)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 10*time.Second {
			delay = 250 * time.Millisecond
		}
		log.Printf("backdraft: %s: control plane session ended: %v (retrying in %s)", a.service, err, delay)
		a.discard(delay)
		delay = min(delay*2, 5*time.Second)
	}
}

// discard drains spans for d so a reconnect does not replay stale traffic.
func (a *Agent) discard(d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case <-a.spans:
			a.dropped.Add(1)
		case <-deadline:
			return
		}
	}
}

func (a *Agent) session(ctx context.Context, client pb.ControlPlaneClient) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		return err
	}
	declared, _ := json.Marshal(a.declared)
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{
		Service:        a.service,
		Instance:       a.instance,
		DeclaredConfig: declared,
	}}}); err != nil {
		return err
	}

	recvErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			var cfg ServiceConfig
			if err := json.Unmarshal(msg.Config, &cfg); err != nil {
				log.Printf("backdraft: %s: bad config from control plane: %v", a.service, err)
				continue
			}
			if err := a.Apply(cfg); err != nil {
				log.Printf("backdraft: %s: rejected config from control plane: %v", a.service, err)
			}
		}
	}()

	flush := time.NewTicker(100 * time.Millisecond)
	defer flush.Stop()
	gauge := time.NewTicker(200 * time.Millisecond)
	defer gauge.Stop()
	batch := make([]*pb.Span, 0, 512)
	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Spans{Spans: &pb.SpanBatch{
			Spans:   batch,
			Dropped: a.dropped.Swap(0),
		}}})
		batch = make([]*pb.Span, 0, 512)
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-recvErr:
			return err
		case s := <-a.spans:
			batch = append(batch, s)
			if len(batch) == cap(batch) {
				if err := send(); err != nil {
					return err
				}
			}
		case <-flush.C:
			if err := send(); err != nil {
				return err
			}
		case <-gauge.C:
			inUse, queued, capacity := a.limiter.Stats()
			if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Gauges{Gauges: &pb.Gauges{
				UnixNano: time.Now().UnixNano(),
				InFlight: int32(inUse),
				Queued:   int32(queued),
				Workers:  int32(capacity),
			}}}); err != nil {
				return err
			}
		}
	}
}
