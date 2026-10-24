# Backdraft

Backdraft is a local workbench for testing how gRPC services fail and recover.
Inject a short slowdown, inspect the retries and queues it leaves behind,
then compare or apply a policy change.

[![CI](https://github.com/SanishKumar/Backdraft-go-microservices/actions/workflows/ci.yml/badge.svg)](https://github.com/SanishKumar/Backdraft-go-microservices/actions/workflows/ci.yml)

![Backdraft demo: a Catalog slowdown causes a retry storm, then a policy change restores service](docs/images/backdraft-demo.gif)

A 30-second tour recorded in the local lab, with pauses shortened. Catalog
slows for eight seconds; retries keep the system overloaded after the fault
clears. A retry budget restores service. The final views compare policy
predictions and map the failure boundary across loads and fault durations.

[View the static screenshot](docs/images/backdraft.png).

## Quick start

Requires Go 1.24.1+ and Node.js 22.12+.

```sh
git clone https://github.com/SanishKumar/Backdraft-go-microservices.git
cd Backdraft-go-microservices
cd web
npm ci
npm run build
cd ..
go run ./cmd/lab
```

Open [localhost:7070](http://localhost:7070). Choose **Play the incident** to
run the included experiment. The lab starts a GraphQL gateway, three gRPC
services, a traffic generator, and the control plane. Storage is in memory;
restarting the lab clears its data.

The experiment slows Catalog for eight seconds at 200 requests per second,
then enables retry budgets. The charts show whether the system recovers and
how long it takes. Timing and capacity vary by machine.

## Workspace

| View | Use |
| --- | --- |
| **Live** | Follow request attempts, queues, success rate, and wasted work. Inject latency or errors, inspect traces, and apply a finding's policy patch. |
| **Predict** | Compare a slowdown under the current and mitigated policies. Sweep load and incident duration in a stability map, or ramp traffic up and down. |
| **Policy** | Edit per-hop timeouts, attempts, backoff, retry budgets, deadline propagation, worker counts, and queue limits. Export a policy for CI. |

The live workspace moves from request metrics and the call graph to fault
experiments and policy findings. Select a service to inspect its queues and
deadlines, or open Traces for individual request waterfalls. The animated
constellation can be paused and respects reduced-motion preferences.

The reference policy is intentionally unsafe so the incident can be reproduced.
`examples/hardened.policy.json` provides a separate configuration with bounded
queues, propagated deadlines, and retries restricted to safe operations.

## Policy checks

Run the analyzer without starting any services:

```sh
go run ./cmd/backdraft check examples/hardened.policy.json
go run ./cmd/backdraft check --format json examples/reference.policy.json
```

The checker detects retry amplification, timeout inversions, dropped deadlines,
unsafe write retries, missing retry budgets, and unbounded queues. Findings
include an explanation and, where available, a configuration patch.

Exit codes: `0` passes, `1` exceeds the severity threshold, and `2` indicates
invalid input. The default threshold is `high`; set `--fail-on` to `critical`,
`high`, `medium`, `low`, or `none`.

## Connect a service

Install a Backdraft interceptor on each unary gRPC client and server, then
start a reporting session:

```go
policy := backdraft.ServiceConfig{
    Server: backdraft.ServerPolicy{
        Workers: 32, QueueLimit: 128, ShedExpired: true,
    },
    Clients: map[string]backdraft.ClientPolicy{
        "catalog": {
            TimeoutMs: 500, MaxAttempts: 2,
            BackoffMs: 25, BackoffMaxMs: 100,
            RetryOn: []string{"UNAVAILABLE"},
            PropagateDeadline: true,
            Budget: &backdraft.BudgetPolicy{Ratio: 0.1},
        },
    },
}
agent := backdraft.New("checkout", policy)
go agent.Run(ctx, "127.0.0.1:7071")

server := grpc.NewServer(agent.ServerOptions()...)
conn, err := grpc.NewClient(catalogAddress, agent.DialOptions("catalog")...)
```

Import `github.com/SanishKumar/go-microservices/backdraft`. Use stable service
names on both ends of an edge. The client policy owns retries; account for
any retries already configured in another transport layer. Declare method
idempotency in `ServiceConfig.Methods` and exclude unsafe operations with
`ClientPolicy.NoRetry`.

Build the UI, then run the standalone control plane:

```sh
go run ./cmd/backdraft serve --entry checkout --ui web/dist
```

HTTP listens on `127.0.0.1:7070`; reporting gRPC listens on
`127.0.0.1:7071`. The built-in traffic generator is disabled unless
`--load-target` is supplied. An instrumented service retains its last policy
when the control plane disconnects.

Send traffic to the entry service to populate its model. Predictions become
available after enough request samples arrive. The standalone control plane
uses the services that connect to it; it does not add the reference shop to
your topology.

## Docker

```sh
docker compose up --build
```

This runs the reference services with Postgres and Elasticsearch. The UI is
at [localhost:7070](http://localhost:7070); the GraphQL playground is at
[localhost:8000/playground](http://localhost:8000/playground).

The Compose configuration is for local development. Its database credentials
and resilience policies are examples. Existing databases from the original
shop schema require migration before use with the current order schema.

## Development

Start the lab, then run `npm run dev` in `web/`. Vite proxies API requests
and the live stream to port 7070.

```sh
go test -short ./...          # unit and integration tests
go test ./internal/lab        # live failure and recovery experiment
go vet ./...
python -B -m unittest discover -s scripts -p "test_*.py"
cd web
npm run typecheck
npm run build
```

`python scripts/smoke.py` checks a running lab. For Compose, add
`--gateway http://127.0.0.1:8000/graphql --wait 180`.
The smoke check pauses traffic, clears faults and policy edits, and drains
work left by earlier experiments before testing checkout. Avoid changing
controls while it runs. On success it restores the original traffic settings
with the reference policy; on failure it leaves traffic paused for diagnosis.
[CI](.github/workflows/ci.yml) also runs Linux race detection and the
Compose smoke check against real databases.

## Scope

The control plane is intended for local experiments and service integration.
It is not a hosted management service.

- Unary RPCs are instrumented. Streaming RPCs pass through unchanged.
- Traces, configuration overrides, and calibration are held in memory.
- In standalone mode, ingress rates are derived from completed server spans;
  they do not measure arrivals that have not finished or telemetry that was dropped.
- The control plane has no authentication or TLS. Keep its ports private.
- The simulator models each service as one worker pool. It does not model
  per-instance routing, connection pools, garbage collection, or memory pressure.
- Predictions depend on observed traffic and request shapes. A reported
  failure means the model did not recover within the observation window;
  it is not proof of an indefinite outage.
- The lab uses synthetic service times to make overload reproducible without
  consuming a laptop's CPU. Capacity figures are specific to that model.

See [architecture](docs/architecture.md) for the model and instrumentation,
and [service changes](docs/review.md) for the reference system's data and API
corrections.
