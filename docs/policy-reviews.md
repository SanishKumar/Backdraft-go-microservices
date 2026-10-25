# Policy reviews

A review answers a specific question: does this policy meet our recovery
contract under this set of faults? It compares a candidate against a frozen
baseline. Neither capturing nor running a review changes live policies.

Backdraft is useful when a timeout, retry budget, worker pool, or queue change
affects several services and a unit test cannot show the combined behavior.
Use a staging or local environment to calibrate the model, then retain that
model beside the policy change. Validate consequential changes with real
traffic as well.

## Capture

Instrument each unary gRPC client and server with the Backdraft agent as
shown in the [README](../README.md#connect-a-service). These interceptors
enforce the configured resilience policies; they are not passive tracers.
Start `backdraft serve --entry checkout`, then send representative, healthy
traffic to that entry service.

```sh
backdraft capture --name checkout-retries --out checkout.review.json
```

The UI's **Capture your system** action creates the same kind of bundle.
Capture refuses disconnected services and active injected faults. For a
captured review to pass, the model needs:

- An observed ingress mix, with at least 50 first attempts or completed entry
  server requests, depending on the entry mode.
- At least 50 healthy self-time samples for every service that executes work.
- At least 50 healthy request-shape samples for every operation referenced by
  ingress or downstream shapes. Leaf methods count too: their empty shape is
  evidence that no downstream calls occurred.

Missing coverage is listed in the result. A candidate that meets every
numerical limit with incomplete capture evidence is **inconclusive**, not
passed. Collect more traffic and create a fresh capture. Editing the old
bundle does not collect new evidence.

Service samples are reservoirs of up to 2,000 timings and 200 shapes per
method. The captured sample counts describe the retained model, not a claim
that every production path was observed. Timings and shapes contain method
and service names, but not request payloads.

To try the workflow without services:

```sh
backdraft capture --example --out review.json
backdraft review review.json
```

This is explicitly a **declared** reference model. A declared model can pass
its mathematical contract, but is never presented as a live calibration.
Use `--require-capture` when CI must reject declared models.

## Candidate and contract

A bundle is ordinary JSON with these top-level fields:

| Field | Meaning |
| --- | --- |
| `version`, `engine` | Serialization and simulation semantics; unsupported values are rejected. |
| `name` | A name for the decision being reviewed. |
| `model` | Frozen baseline policies, ingress operations, service-time samples, and downstream call shapes. |
| `candidate` | Complete proposed `SystemConfig`. |
| `contract` | Finite fault envelope and acceptance thresholds. |
| `evidence` | Declared/captured source, capture time, and calibration coverage. |

The initial proposal enables retry budgets, one retry layer, propagated
deadlines, expired-work shedding, and bounded queues. It also excludes
declared non-idempotent, non-deduplicated methods from retries. It is a
starting point to test. It does not infer the safety of undeclared methods.

The candidate can change resilience policies, including worker counts. It
cannot change service names, dependencies, synthetic work time, or method
idempotency semantics. Those changes require a new baseline model.

The envelope is the Cartesian product of fault targets, loads, durations,
and seeds, limited to 128 paired cases. Each case injects latency and/or an
error probability into one service. Warmup and observation windows are
explicit. Increasing either changes what the result can establish.

The gate requires every candidate case to satisfy:

1. **Healthy warmup:** at least 90% of offered requests complete successfully
   before the fault. A system overloaded before the fault fails.
2. **Final success:** at least `minSuccessRatio` in the last five seconds or
   final 20% of the run, whichever interval is shorter.
3. **Recovery:** sustained return to 90% of the baseline goodput ratio within
   `maxRecoveryS` seconds after the fault ends, using trailing two-second
   windows. A final-window dip cannot count as zero-second recovery.
4. **Backlog:** no more than `maxQueue` queued requests across all services.
   This maximum is updated on every enqueue, including between chart samples.

The final-success threshold and recovery threshold measure different things.
For example, a policy can recover to 90% of its baseline quickly but still
fail a 99% final-success requirement. Queue counts exclude requests already
occupying workers. Chart series are sampled every 500ms.

For each seed, baseline and candidate receive the same Poisson arrival times
and operation mix. The ingress random stream is independent of retries,
backoffs, and service execution. Internal execution draws can diverge when
the policies take different paths; this is not replay of individual live
requests. Test multiple seeds and loads rather than relying on one trajectory.

## Evidence and history

The report retains every case's verdict, success, recovery time, peak queue,
and reasons. A regression is a baseline pass paired with a candidate failure.
It keeps full time series for one witness:

- First preference: a failing or inconclusive candidate case.
- Otherwise: a failing or inconclusive baseline case.
- If both policies pass everything: a representative case.

Within each category, selection is ordered by duration, load, service name,
then seed. “Smallest tested failure” refers to that ordering, not a global
minimum or a search over untested conditions.

Reviews are stored as immutable JSON records in `.backdraft/reviews` by
default. Writes use a temporary file, sync, and rename. Record and bundle
SHA-256 hashes are checked on load. These hashes identify content and detect
accidental corruption; they are not signatures or independent attestations.
The UI lists the latest 100 records. Disk records are retained until removed
by the workspace owner; back up the directory if the evidence matters.
`--data ""` selects temporary memory storage, limited to 200 reviews.

Download a bundle to reproduce a decision elsewhere. Download the Markdown
summary for code review, or JSON evidence for your own reporting. The
candidate export is a policy artifact to deploy through your normal change
process. Passing a simulation never automatically publishes a policy.

## CI

Build and pin a Backdraft executable in your tooling image, or check out a
specific repository revision and build `./cmd/backdraft`. The review command
needs no frontend, control plane, network, or running services.

```sh
backdraft check --fail-on high candidate.policy.json
backdraft review --require-capture --max-model-age 168h --candidate candidate.policy.json --out report.json checkout.review.json
```

Keep the baseline bundle under review too. Changing its contract or model is
a change to the test itself. A stale model is rejected when `--max-model-age`
is set; timestamps are supplied by the capture environment, not a trusted
timestamp authority. Pin the same engine build and runtime for comparisons;
the report records the Go runtime and platform.

```yaml
- name: Review resilience policy
  run: backdraft review --require-capture --max-model-age 168h --candidate policy.json --out report.json checkout.review.json
- name: Keep review evidence
  if: always()
  uses: actions/upload-artifact@v4
  with:
    name: resilience-review
    path: report.json
    if-no-files-found: ignore
```

| Exit | Meaning |
| --- | --- |
| `0` | Candidate passed every case and evidence requirements. |
| `1` | At least one candidate case failed the contract. |
| `2` | Invalid input, stale/incomplete evidence, resource limit, or timeout. |

Static policy analysis and simulated recovery are separate checks. A policy
can recover in the model while still retrying an unsafe write, so run both.

The HTTP runner allows two concurrent simulation jobs and a 90-second budget.
Each review evaluates at most two pairs concurrently. Cancellation stops
evaluation and does not save a partial result. If cancellation races with an
already completed save, the finished review remains in history. Resource
limits never turn an unfinished simulation into a pass.

## Model limits

The model represents one worker pool per service and unary request shapes.
It does not represent individual replicas, connection pools, autoscaling,
garbage collection, database lock contention, correlated fault patterns, or
an arbitrary production workload. Observed shapes can also miss calls when
telemetry is dropped. Re-capture after topology, traffic mix, or service code
changes, and check important results with a real staging experiment.

Backdraft's live experiment and trace views are useful for that validation.
The reference shop is a reproducible example; the review format and CLI also
work with models captured from your own instrumented services.
