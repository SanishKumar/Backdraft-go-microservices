package backdraft

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type inbound struct {
	method  string
	caller  string
	attempt uint32
	trace   TraceID
	parent  SpanID
	traced  bool
}

// errNotHandled marks admission failures, where the handler never ran.
type errNotHandled struct{ error }

func (e errNotHandled) Unwrap() error { return e.error }

// serve admits one inbound request, runs handle while holding a worker,
// and reports a SERVER span. It is shared by the gRPC and HTTP paths.
func (a *Agent) serve(ctx context.Context, in inbound, handle func(context.Context) error) error {
	arrive := time.Now()
	cfg := a.cfg.Load()

	st := &spanState{id: newSpanID()}
	if in.traced {
		st.trace = in.trace
	} else {
		st.trace = newTraceID()
	}
	span := &pb.Span{
		TraceId: st.trace[:], SpanId: st.id[:], ParentId: idBytes(in.parent),
		Kind: pb.Kind_SERVER, Service: a.service, Peer: in.caller, Method: in.method,
		Attempt: in.attempt, StartUnixNano: arrive.UnixNano(),
	}
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		span.DeadlineUnixNano = deadline.UnixNano()
	}
	finish := func(err error, outcome pb.Outcome) error {
		end := time.Now()
		span.DurationNanos = int64(end.Sub(arrive))
		span.Code = uint32(status.Code(err))
		span.Outcome = outcome
		span.Late = hasDeadline && end.After(deadline)
		a.emit(span)
		return err
	}

	release, err := a.limiter.Acquire(ctx, cfg.Server.ShedExpired)
	if errors.Is(err, ErrQueueFull) {
		return finish(errNotHandled{status.Error(codes.ResourceExhausted, "backdraft: queue full")}, pb.Outcome_SHED)
	}
	if err != nil {
		span.QueueNanos = int64(time.Since(arrive))
		return finish(errNotHandled{status.Error(codes.DeadlineExceeded, "backdraft: caller gave up while queued")}, pb.Outcome_EXPIRED)
	}
	// Release admission even if an application handler panics. HTTP's outer
	// recovery must not leave the worker pool permanently exhausted.
	defer release()
	span.QueueNanos = int64(time.Since(arrive))
	// The request may have queued across a config change; what applies
	// now is what governs the work. A brownout slows the work done while
	// it lasts, not the work that happened to arrive during it.
	cfg = a.cfg.Load()
	if cfg.Server.ShedExpired && ctx.Err() != nil {
		return finish(errNotHandled{status.Error(codes.DeadlineExceeded, "backdraft: caller gave up while queued")}, pb.Outcome_EXPIRED)
	}

	// Work the client cannot cancel: synthetic service time plus any
	// injected brownout latency, both holding the worker.
	workStart := time.Now()
	fault := cfg.Fault
	span.Faulted = fault.Active()
	if d := work(cfg.Server.WorkMs) + time.Duration(fault.LatencyMs)*time.Millisecond; d > 0 {
		time.Sleep(d)
	}
	if fault.ErrorRate > 0 && rand.Float64() < fault.ErrorRate {
		err = status.Error(codes.Unavailable, "backdraft: injected fault")
	} else {
		err = handle(withSpan(ctx, st))
	}
	span.SelfNanos = int64(time.Since(workStart) - st.down.union())

	st.mu.Lock()
	if st.op != "" {
		span.Method = st.op
	}
	if err == nil && st.failed != codes.OK {
		err = status.Error(st.failed, "request failed")
	}
	st.mu.Unlock()

	if err != nil {
		return finish(err, pb.Outcome_ERROR)
	}
	return finish(nil, pb.Outcome_OK)
}

func inboundFromMD(md metadata.MD, method string) inbound {
	in := inbound{method: method, attempt: 1}
	if v := md.Get(headerTraceparent); len(v) > 0 {
		in.trace, in.parent, in.traced = parseTraceparent(v[0])
	}
	if v := md.Get(headerCaller); len(v) > 0 {
		in.caller = v[0]
	}
	if v := md.Get(headerAttempt); len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			in.attempt = uint32(n)
		}
	}
	return in
}

// UnaryServerInterceptor admits and measures every inbound gRPC request.
func (a *Agent) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		var resp any
		err := a.serve(ctx, inboundFromMD(md, shortMethod(info.FullMethod)), func(ctx context.Context) error {
			var err error
			resp, err = handler(ctx, req)
			return err
		})
		var nh errNotHandled
		if errors.As(err, &nh) {
			return nil, nh.error
		}
		return resp, err
	}
}

// ServerOptions returns the options that route a gRPC server through the
// agent.
func (a *Agent) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(a.UnaryServerInterceptor())}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Middleware admits and measures inbound HTTP requests, for edge services
// such as an API gateway. Requests get the server's RequestTimeoutMs as a
// deadline, which downstream gRPC calls inherit.
func (a *Agent) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		md := metadata.MD{}
		for _, k := range []string{headerTraceparent, headerCaller, headerAttempt} {
			if v := r.Header.Get(k); v != "" {
				md.Set(k, v)
			}
		}
		ctx := r.Context()
		if ms := a.cfg.Load().Server.RequestTimeoutMs; ms > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
			defer cancel()
		}
		err := a.serve(ctx, inboundFromMD(md, r.URL.Path), func(ctx context.Context) error {
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r.WithContext(ctx))
			if rec.status >= 500 {
				return status.Error(codes.Unavailable, http.StatusText(rec.status))
			}
			return nil
		})
		var nh errNotHandled
		if errors.As(err, &nh) {
			code := http.StatusServiceUnavailable
			if status.Code(nh.error) == codes.DeadlineExceeded {
				code = http.StatusGatewayTimeout
			}
			http.Error(w, nh.Error(), code)
		}
	})
}
