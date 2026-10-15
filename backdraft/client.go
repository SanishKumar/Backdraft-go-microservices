package backdraft

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Attempt identifies one try of a call, for transports that propagate
// trace context themselves.
type Attempt struct {
	N           uint32
	Traceparent string
	Caller      string
	// Replayed is set by the transport when the response was an
	// idempotent replay of an earlier request.
	Replayed bool
}

// Call runs fn under the client policy for peer: per-attempt timeout,
// retries with backoff, retry budget and deadline propagation. Each attempt
// is reported as a CLIENT span. fn must return a status error (or nil) so
// the result can be classified.
func (a *Agent) Call(ctx context.Context, peer, method string, fn func(ctx context.Context, at *Attempt) error) error {
	pol := a.cfg.Load().Client(peer).ForMethod(method)
	parent := spanFrom(ctx)
	var trace TraceID
	var parentID SpanID
	if parent != nil {
		trace, parentID = parent.trace, parent.id
	} else {
		trace = newTraceID()
	}

	// Without propagation the downstream call keeps running on its own
	// clock after the caller has given up.
	base := ctx
	if !pol.PropagateDeadline {
		base = context.WithoutCancel(ctx)
	}
	var budget *Budget
	if pol.Budget != nil {
		budget = a.budget(peer, *pol.Budget)
	}

	callStart := time.Now()
	if parent != nil {
		defer func() { parent.down.add(callStart, time.Now()) }()
	}

	var err error
	for n := uint32(1); ; n++ {
		if n > 1 {
			if int(n) > pol.MaxAttempts || !pol.RetriesOn(status.Code(err)) || base.Err() != nil {
				return err
			}
			if budget != nil && !budget.AllowRetry() {
				a.emit(&pb.Span{
					TraceId: trace[:], SpanId: idBytes(newSpanID()), ParentId: idBytes(parentID),
					Kind: pb.Kind_CLIENT, Service: a.service, Peer: peer, Method: method, Attempt: n,
					StartUnixNano: time.Now().UnixNano(), Code: uint32(status.Code(err)), Outcome: pb.Outcome_BUDGET,
				})
				return err
			}
			if d := pol.Backoff(int(n-1), rand.Float64()); d > 0 {
				t := time.NewTimer(d)
				select {
				case <-t.C:
				case <-base.Done():
					t.Stop()
					return err
				}
			}
		} else if budget != nil {
			budget.RecordRequest()
		}

		actx, cancel := base, context.CancelFunc(func() {})
		if pol.TimeoutMs > 0 {
			actx, cancel = context.WithTimeout(base, pol.Timeout())
		}
		sid := newSpanID()
		at := &Attempt{N: n, Traceparent: formatTraceparent(trace, sid), Caller: a.service}
		start := time.Now()
		var deadline int64
		if d, ok := actx.Deadline(); ok {
			deadline = d.UnixNano()
		}
		err = toStatusErr(fn(actx, at))
		cancel()

		code := status.Code(err)
		outcome := pb.Outcome_OK
		switch {
		case code == codes.DeadlineExceeded:
			outcome = pb.Outcome_TIMEOUT
		case err != nil:
			outcome = pb.Outcome_ERROR
		}
		a.emit(&pb.Span{
			TraceId: trace[:], SpanId: sid[:], ParentId: idBytes(parentID),
			Kind: pb.Kind_CLIENT, Service: a.service, Peer: peer, Method: method, Attempt: n,
			StartUnixNano: start.UnixNano(), DurationNanos: int64(time.Since(start)),
			DeadlineUnixNano: deadline, Code: uint32(code), Outcome: outcome, Replayed: at.Replayed,
		})
		if err == nil {
			return nil
		}
	}
}

// toStatusErr gives context errors their gRPC codes so non-gRPC transports
// classify the same way.
func toStatusErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	}
	return status.Error(codes.Unknown, err.Error())
}

func idBytes(s SpanID) []byte {
	if s == (SpanID{}) {
		return nil
	}
	return s[:]
}

// UnaryClientInterceptor applies the policy for peer to every call on a
// connection. Install it with DialOptions.
func (a *Agent) UnaryClientInterceptor(peer string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return a.Call(ctx, peer, shortMethod(method), func(ctx context.Context, at *Attempt) error {
			ctx = metadata.AppendToOutgoingContext(ctx,
				headerTraceparent, at.Traceparent,
				headerCaller, at.Caller,
				headerAttempt, strconv.Itoa(int(at.N)),
			)
			var header metadata.MD
			err := invoker(ctx, method, req, reply, cc, append(opts, grpc.Header(&header))...)
			at.Replayed = len(header.Get(ReplayedHeader)) > 0
			return err
		})
	}
}

// DialOptions returns the options that route a client connection to peer
// through the agent.
func (a *Agent) DialOptions(peer string) []grpc.DialOption {
	return []grpc.DialOption{grpc.WithChainUnaryInterceptor(a.UnaryClientInterceptor(peer))}
}

// shortMethod turns "/order.v1.OrderService/PostOrder" into "PostOrder".
func shortMethod(full string) string {
	if i := strings.LastIndexByte(full, '/'); i >= 0 {
		return full[i+1:]
	}
	return full
}
