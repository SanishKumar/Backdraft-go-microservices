# Architecture

Backdraft consists of an instrumentation library, a control plane, a policy
analyzer, a discrete-event simulator, and a browser UI.

```text
Service interceptors ── spans / gauges ──> Control plane ── WebSocket ──> UI
                     <── policy updates ──       │
                                                ├── Analyzer
                                                └── Calibrator ──> Simulator
```

## Instrumentation

The `backdraft` package wraps unary gRPC calls and HTTP handlers. Client
interceptors enforce attempt timeouts, jittered backoff, per-method retry
exclusions, aggregate retry budgets, and deadline propagation. Server
interceptors manage worker admission, FIFO queues, and expired requests.

Each attempt produces a span containing its status, attempt number, deadline,
queue time, service time, and trace context. Self time excludes the union of
intervals spent waiting on downstream calls. A bounded telemetry channel drops
spans instead of blocking application traffic. Reporting uses a bidirectional
gRPC session; configuration updates use the same session.

Retry budgets count requests and retries over a sliding ten-second window.
The allowed retries are `ratio × requests + minPerSec × 10`. This aggregate
limit is distinct from the attempt cap on an individual request.

## Analysis

`backdraft/analysis` walks the declared dependency graph. It calculates
per-request attempt multiplication separately from sustained amplification
under budgets. Rules also check deadline propagation, timeout fit, method
retry safety, admission bounds, and whether retry traffic can exceed observed
service capacity. Static checks work from a policy file; live observations
can narrow which methods and downstream sequences a rule considers.

## Simulation

`backdraft/sim` models Poisson arrivals, deadlines, jittered retries, retry
budgets, worker pools, FIFO queues, and fault windows. A worker remains occupied
while a handler waits for downstream calls. Stable seeds make scenarios
repeatable for a given model.

The calibrator learns healthy self-time samples, downstream call groups, and
the operation mix. The reference lab supplies a fallback model before enough
observations arrive. Predictions distinguish recovery, a draining backlog,
continued overload, and insufficient evidence. Stability-map cells can stop
early when the configured failure criteria are met.

Standalone mode uses `--entry` to identify the real ingress service. Its
server spans provide the operation mix; its client spans still describe
downstream calls. The reference topology and synthetic users are loaded only
when the reference traffic generator is enabled. Ingress rates in standalone
mode count completed server spans, rather than independently observed arrivals.

These are model predictions within a finite observation window. Validate an
important result against the real system and traffic mix before using it to
set production policy.

## Reference system

The lab runs Account, Catalog, and Order as separate gRPC listeners behind a
GraphQL gateway. A fifth instrumentation session represents users. All calls
use the same network interceptors as independently deployed services. The
load generator is open-loop: requests continue to arrive while previous
requests are still pending.

The in-memory mode bounds retained order history for long-running demos.
Compose uses Postgres for accounts and orders and Elasticsearch for products.
The latter uses a legacy client/server pair and is confined to the development
network.
