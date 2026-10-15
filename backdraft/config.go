// Package backdraft is a gRPC (and HTTP) agent that owns a service's
// resilience behaviour - per-attempt timeouts, retries, retry budgets,
// deadline propagation, admission control - and reports what actually
// happened on every attempt to the Backdraft control plane.
//
// The point is to make the cross-service consequences of local policy
// visible. Three attempts per hop is a reasonable local choice; three hops
// of it is 27 attempts against the deepest service during an incident.
package backdraft

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
)

// ServiceConfig is everything the agent enforces for one service.
type ServiceConfig struct {
	Server ServerPolicy `json:"server"`
	// Clients holds the policy for calls to each downstream service, keyed
	// by the callee's service name.
	Clients map[string]ClientPolicy `json:"clients,omitempty"`
	// Methods describes this service's own methods, keyed by short method
	// name (for example "PostOrder").
	Methods map[string]MethodInfo `json:"methods,omitempty"`
	// Fault is injected into requests this service handles. Lab use only.
	Fault Fault `json:"fault"`
}

// ClientPolicy governs calls from one service to one downstream service.
type ClientPolicy struct {
	// TimeoutMs bounds each attempt. 0 means no per-attempt timeout.
	TimeoutMs int `json:"timeoutMs"`
	// MaxAttempts counts the first attempt, so 1 means no retries.
	MaxAttempts int `json:"maxAttempts"`
	// BackoffMs is the delay before the first retry; it doubles per retry
	// up to BackoffMaxMs, with full jitter.
	BackoffMs    int `json:"backoffMs"`
	BackoffMaxMs int `json:"backoffMaxMs"`
	// RetryOn lists the status codes worth retrying, as canonical names
	// ("UNAVAILABLE", "DEADLINE_EXCEEDED", ...).
	RetryOn []string `json:"retryOn,omitempty"`
	// PropagateDeadline bounds downstream calls by the caller's remaining
	// deadline and cancels them when the caller goes away. Without it, each
	// hop starts a fresh clock and work outlives the request that wanted it.
	PropagateDeadline bool `json:"propagateDeadline"`
	// Budget caps retries as a fraction of recent requests.
	Budget *BudgetPolicy `json:"budget,omitempty"`
	// NoRetry names methods that get one attempt regardless of
	// MaxAttempts, for writes that are not safe to repeat.
	NoRetry []string `json:"noRetry,omitempty"`
}

// BudgetPolicy allows retries while retries <= Ratio x requests + MinPerSec x
// window, measured over a sliding 10 second window.
type BudgetPolicy struct {
	Ratio     float64 `json:"ratio"`
	MinPerSec float64 `json:"minPerSec"`
}

// ServerPolicy governs how a service admits work.
type ServerPolicy struct {
	// Workers is the number of requests handled concurrently. 0 means
	// unlimited. A worker is held for the whole handler, including time
	// blocked on downstream calls, as in a thread-per-request server.
	Workers int `json:"workers"`
	// QueueLimit bounds requests waiting for a worker; excess requests are
	// rejected with RESOURCE_EXHAUSTED. 0 means unbounded.
	QueueLimit int `json:"queueLimit"`
	// ShedExpired drops queued requests whose caller has already given up
	// instead of doing work nobody will read.
	ShedExpired bool `json:"shedExpired"`
	// WorkMs is synthetic per-request CPU time, drawn from a log-normal
	// with this mean. It lets a laptop reproduce production saturation.
	WorkMs float64 `json:"workMs,omitempty"`
	// RequestTimeoutMs is applied to inbound requests that arrive without a
	// deadline (HTTP edges). 0 means none.
	RequestTimeoutMs int `json:"requestTimeoutMs,omitempty"`
}

// MethodInfo describes a method's retry safety.
type MethodInfo struct {
	// Idempotent methods can be repeated without changing the outcome.
	Idempotent bool `json:"idempotent"`
	// Dedupes means the method is not idempotent by nature but recognises
	// repeated requests (for example by idempotency key).
	Dedupes bool `json:"dedupes,omitempty"`
}

// Fault is a brownout injected into a service.
type Fault struct {
	LatencyMs int     `json:"latencyMs"`
	ErrorRate float64 `json:"errorRate"`
}

// Active reports whether the fault changes behaviour.
func (f Fault) Active() bool { return f.LatencyMs > 0 || f.ErrorRate > 0 }

// SystemConfig is the policy of a whole system, as checked in CI and shown
// in the control plane.
type SystemConfig struct {
	// Entry is the service that receives external traffic (often the
	// pseudo-service "users", whose client policy models user retries).
	Entry    string                   `json:"entry"`
	Services map[string]ServiceConfig `json:"services"`
}

// DefaultClientPolicy is what plain gRPC does: one attempt, no per-attempt
// timeout, deadline propagated.
func DefaultClientPolicy() ClientPolicy {
	return ClientPolicy{MaxAttempts: 1, PropagateDeadline: true}
}

// Client returns the policy for calls to peer.
func (c ServiceConfig) Client(peer string) ClientPolicy {
	if p, ok := c.Clients[peer]; ok {
		return p.normalized()
	}
	return DefaultClientPolicy()
}

// Clone returns a deep copy.
func (c ServiceConfig) Clone() ServiceConfig {
	b, _ := json.Marshal(c)
	var out ServiceConfig
	_ = json.Unmarshal(b, &out)
	return out
}

// Peers returns the downstream services in a stable order.
func (c ServiceConfig) Peers() []string {
	peers := make([]string, 0, len(c.Clients))
	for p := range c.Clients {
		peers = append(peers, p)
	}
	sort.Strings(peers)
	return peers
}

func (p ClientPolicy) normalized() ClientPolicy {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.BackoffMaxMs < p.BackoffMs {
		p.BackoffMaxMs = p.BackoffMs
	}
	return p
}

// Retries returns how many retries the policy allows.
func (p ClientPolicy) Retries() int { return p.normalized().MaxAttempts - 1 }

// ForMethod returns the policy as it applies to one method.
func (p ClientPolicy) ForMethod(method string) ClientPolicy {
	if p.RetriesMethod(method) {
		return p
	}
	p.MaxAttempts = 1
	return p
}

// RetriesMethod reports whether calls to method may be retried at all.
func (p ClientPolicy) RetriesMethod(method string) bool {
	for _, m := range p.NoRetry {
		if m == method {
			return false
		}
	}
	return p.normalized().MaxAttempts > 1
}

// Timeout returns the per-attempt timeout, 0 if none.
func (p ClientPolicy) Timeout() time.Duration { return time.Duration(p.TimeoutMs) * time.Millisecond }

// RetriesOn reports whether code c is retryable under p.
func (p ClientPolicy) RetriesOn(c codes.Code) bool {
	for _, name := range p.RetryOn {
		if rc, ok := ParseCode(name); ok && rc == c {
			return true
		}
	}
	return false
}

// Backoff returns the delay before retry number n (1-based), with full
// jitter applied using u in [0,1).
func (p ClientPolicy) Backoff(n int, u float64) time.Duration {
	if p.BackoffMs <= 0 {
		return 0
	}
	ceil := float64(p.BackoffMs) * math.Pow(2, float64(n-1))
	if max := float64(p.normalized().BackoffMaxMs); max > 0 && ceil > max {
		ceil = max
	}
	return time.Duration(u * ceil * float64(time.Millisecond))
}

// WorstCaseBackoff returns the longest total backoff across all retries.
func (p ClientPolicy) WorstCaseBackoff() time.Duration {
	var d time.Duration
	for n := 1; n <= p.Retries(); n++ {
		d += p.Backoff(n, 1)
	}
	return d
}

// codeNames maps canonical gRPC status code names to codes.
var codeNames = func() map[string]codes.Code {
	m := map[string]codes.Code{}
	for c := codes.OK; c <= codes.Unauthenticated; c++ {
		m[CodeName(c)] = c
	}
	return m
}()

// CodeName returns the canonical name of c, e.g. "DEADLINE_EXCEEDED".
func CodeName(c codes.Code) string {
	s := c.String()
	var b strings.Builder
	var prev rune
	for _, r := range s {
		if r >= 'A' && r <= 'Z' && prev >= 'a' && prev <= 'z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
		prev = r
	}
	return strings.ToUpper(b.String())
}

// ParseCode accepts canonical names ("UNAVAILABLE") or Go names
// ("Unavailable").
func ParseCode(name string) (codes.Code, bool) {
	if c, ok := codeNames[strings.ToUpper(name)]; ok {
		return c, true
	}
	for c := codes.OK; c <= codes.Unauthenticated; c++ {
		if strings.EqualFold(c.String(), name) {
			return c, true
		}
	}
	return 0, false
}

// Validate reports policy values that cannot be enforced.
func (c ServiceConfig) Validate() error {
	if c.Server.Workers < 0 || c.Server.QueueLimit < 0 {
		return fmt.Errorf("workers and queueLimit must be >= 0")
	}
	if c.Fault.ErrorRate < 0 || c.Fault.ErrorRate > 1 {
		return fmt.Errorf("fault errorRate must be within [0, 1]")
	}
	for peer, p := range c.Clients {
		if p.TimeoutMs < 0 || p.BackoffMs < 0 || p.BackoffMaxMs < 0 {
			return fmt.Errorf("client %s: durations must be >= 0", peer)
		}
		if p.MaxAttempts > 10 {
			return fmt.Errorf("client %s: maxAttempts above 10 is never what you want", peer)
		}
		for _, name := range p.RetryOn {
			if _, ok := ParseCode(name); !ok {
				return fmt.Errorf("client %s: unknown status code %q", peer, name)
			}
		}
		if b := p.Budget; b != nil && (b.Ratio < 0 || b.MinPerSec < 0) {
			return fmt.Errorf("client %s: budget values must be >= 0", peer)
		}
	}
	return nil
}
