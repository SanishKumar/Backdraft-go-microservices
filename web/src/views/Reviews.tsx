import { useEffect, useRef, useState } from 'react'
import { TimeChart } from '../components/charts/TimeChart'
import { ReviewPolicyEditor } from '../components/ReviewPolicyEditor'
import { useStore } from '../store'
import {
  downloadJSON,
  policyDiff,
  reviews,
  type ReviewBundle,
  type ReviewContract,
  type ReviewDraft,
  type ReviewRecord,
  type ReviewSummary,
  type SystemPolicy,
} from '../reviews'
import '../reviews.css'

const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error))
const percentage = (value: number) => `${(value * 100).toFixed(1)}%`
const recovery = (value: number) => (value < 0 ? 'Not recovered' : `${value.toFixed(1)}s`)
function baseline(bundle: ReviewBundle): SystemPolicy {
  return {
    entry: bundle.model.entry,
    services: Object.fromEntries(Object.entries(bundle.model.services).map(([name, value]) => [name, value.config])),
  }
}
function parsedNumbers(value: string): number[] {
  if (!value.trim()) throw new Error('Enter at least one value for each envelope axis.')
  const values = value.split(',').map((v) => (v.trim() ? Number(v) : NaN))
  if (values.some((v) => !Number.isFinite(v)))
    throw new Error('Use comma-separated numbers for loads, durations, and seeds.')
  return values
}

export function ReviewsView() {
  const state = useStore((s) => s.state)
  const [draft, setDraft] = useState<ReviewDraft | null>(null)
  const [record, setRecord] = useState<ReviewRecord | null>(null)
  const [items, setItems] = useState<ReviewSummary[]>([])
  const [persistent, setPersistent] = useState(true)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [seconds, setSeconds] = useState(0)
  const abort = useRef<AbortController | null>(null)
  const upload = useRef<HTMLInputElement>(null)
  const request = useRef(0)
  const refresh = () =>
    reviews.list().then((r) => {
      setItems(r.items)
      setPersistent(r.persistent)
    })
  useEffect(() => {
    let alive = true
    reviews
      .list()
      .then((r) => {
        if (alive) {
          setItems(r.items)
          setPersistent(r.persistent)
        }
      })
      .catch((e) => {
        if (alive) setError(errorText(e))
      })
    const id = new URLSearchParams(location.search).get('review')
    if (id)
      reviews
        .get(id)
        .then((r) => {
          if (alive) setRecord(r)
        })
        .catch((e) => {
          if (alive) setError(errorText(e))
        })
    return () => {
      alive = false
      request.current++
      abort.current?.abort()
    }
  }, [])
  useEffect(() => {
    if (busy !== 'run') return
    setSeconds(0)
    const timer = setInterval(() => setSeconds((s) => s + 1), 1000)
    return () => clearInterval(timer)
  }, [busy])
  const clearLink = () => history.replaceState(null, '', location.pathname)
  async function capture() {
    setError('')
    setBusy('capture')
    try {
      setDraft(await reviews.capture())
      setRecord(null)
      clearLink()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy('')
    }
  }
  async function importBundle(file?: File) {
    if (!file) return
    setError('')
    setBusy('import')
    try {
      if (file.size > 8 * 1024 * 1024) throw new Error('Bundle exceeds the 8 MiB limit.')
      setDraft(await reviews.validate(JSON.parse(await file.text())))
      setRecord(null)
      clearLink()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy('')
    }
  }
  async function run(bundle: ReviewBundle) {
    setError('')
    setBusy('run')
    const controller = new AbortController()
    abort.current = controller
    const token = ++request.current
    try {
      const result = await reviews.run(bundle, controller.signal)
      if (token !== request.current) return
      setRecord(result)
      setDraft(null)
      history.replaceState(null, '', `?review=${result.id}`)
      await refresh()
      document.getElementById('review-result')?.scrollIntoView({ behavior: 'smooth' })
    } catch (e) {
      if (token === request.current)
        setError(
          controller.signal.aborted
            ? 'Review canceled. Check history if it finished before cancellation reached the server.'
            : errorText(e),
        )
    } finally {
      if (token === request.current) {
        setBusy('')
        abort.current = null
      }
    }
  }
  async function open(id: string) {
    setError('')
    setBusy('open')
    try {
      setRecord(await reviews.get(id))
      setDraft(null)
      history.replaceState(null, '', `?review=${id}`)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy('')
    }
  }
  return (
    <div className="page reviews-page">
      <header className="page-header">
        <div>
          <div className="eyebrow">Backdraft / Policy reviews</div>
          <h1>
            Before you
            <br />
            change a policy.
          </h1>
        </div>
        <div>
          <p className="lede">
            Will a new retry or timeout policy help your system recover? Compare it against the current policy, keep the
            failing cases, and take the same check into CI.
          </p>
          <p className="review-caption">Frozen model. Paired traffic. A recovery contract you define.</p>
        </div>
      </header>
      {error && (
        <p className="review-error" role="alert">
          {error}
        </p>
      )}
      {!draft && !record && (
        <section className="review-start" aria-label="Start a review">
          <div>
            <h2>Bring your system.</h2>
            <p className="lede">
              Capture healthy traffic from connected services, or open a bundle a teammate has shared. Reviews run
              locally without injecting faults into those services.
            </p>
            <div className="review-actions">
              <button className="btn primary" onClick={capture} disabled={!!busy || !state?.services?.length}>
                {busy === 'capture' ? 'Capturing…' : 'Capture your system'} ↗
              </button>
              <button className="btn ghost" disabled={!!busy} onClick={() => upload.current?.click()}>
                Open a bundle
              </button>
            </div>
            <p className="review-caption">
              {state?.services?.length
                ? `${state.services.filter((s) => s.instances > 0).length} connected services. Capture freezes policies, request shapes and service-time samples.`
                : 'No services connected. Start the lab to try a review, or connect your gRPC services to the control plane.'}
            </p>
          </div>
          <div className="review-onboarding">
            <span className="eyebrow">Try it without a running system</span>
            <p>Generate a reference bundle, then open it here.</p>
            <pre className="code">backdraft capture --example --out review.json</pre>
            <p className="review-caption">
              The reference model is declared from source and is labeled as such in every result.
            </p>
            <a
              className="btn ghost"
              href="https://github.com/SanishKumar/Backdraft-go-microservices/blob/main/docs/policy-reviews.md"
              target="_blank"
              rel="noreferrer"
            >
              Connect a service & read the workflow ↗
            </a>
          </div>
        </section>
      )}
      <input
        ref={upload}
        type="file"
        accept=".json,application/json"
        hidden
        aria-label="Import review bundle"
        onChange={(e) => {
          void importBundle(e.target.files?.[0])
          e.target.value = ''
        }}
      />
      {draft && (
        <ReviewEditor
          key={JSON.stringify(draft.bundle)}
          draft={draft}
          busy={!!busy}
          onRun={run}
          onClose={() => setDraft(null)}
          onError={setError}
        />
      )}
      {busy === 'run' && (
        <div className="review-running" role="status">
          <span className="spinner" /> Evaluating the complete envelope · {seconds}s{' '}
          <button className="btn ghost" onClick={() => abort.current?.abort()}>
            Cancel
          </button>
          <p>
            Both policies receive identical ingress for each seed. A timeout or incomplete calibration cannot produce a
            passing captured review.
          </p>
        </div>
      )}
      {record && (
        <ReviewResult
          record={record}
          busy={!!busy}
          onRevise={() => {
            setDraft({ bundle: record.bundle, warnings: record.report.warnings })
            setRecord(null)
            clearLink()
          }}
          onNew={() => {
            setRecord(null)
            clearLink()
          }}
          onRerun={() => run(record.bundle)}
        />
      )}
      <section className="page-section" aria-labelledby="review-history">
        <div className="row">
          <div>
            <div className="eyebrow">Your workspace</div>
            <h2 id="review-history">Review history</h2>
          </div>
          <span className="review-caption">
            {persistent ? 'Saved on this machine · latest 100' : 'Temporary workspace · lost on restart'}
          </span>
        </div>
        {items.length ? (
          <div className="review-history">
            {items.map((item) => (
              <button
                key={item.id}
                className="review-history-row"
                disabled={!!busy}
                onClick={() => open(item.id)}
                aria-current={record?.id === item.id ? 'true' : undefined}
              >
                <span className={`review-status ${item.report.status}`}>{item.report.status}</span>
                <span>
                  {item.name}
                  <small>
                    {new Date(item.report.createdAt).toLocaleString()} · {item.id.slice(0, 10)}
                  </small>
                </span>
                <span>
                  {item.report.candidatePassed} cases passed
                  <br />
                  <small>{item.report.regressions} regressions</small>
                </span>
                <span aria-hidden>↗</span>
              </button>
            ))}
          </div>
        ) : (
          <p className="lede">Completed reviews will appear here with their model, policy changes, and results.</p>
        )}
      </section>
    </div>
  )
}

function ReviewEditor({
  draft,
  busy,
  onRun,
  onClose,
  onError,
}: {
  draft: ReviewDraft
  busy: boolean
  onRun: (b: ReviewBundle) => void
  onClose: () => void
  onError: (s: string) => void
}) {
  const b = draft.bundle
  const [name, setName] = useState(b.name)
  const [contract, setContract] = useState<ReviewContract>(b.contract)
  const [axes, setAxes] = useState({
    loads: b.contract.loads.join(', '),
    durations: b.contract.durations.join(', '),
    seeds: b.contract.seeds.join(', '),
  })
  const [candidate, setCandidate] = useState(JSON.stringify(b.candidate, null, 2))
  const file = useRef<HTMLInputElement>(null)
  let policy: SystemPolicy | null = null
  try {
    const value: unknown = JSON.parse(candidate)
    if (
      value &&
      typeof value === 'object' &&
      'services' in value &&
      'entry' in value &&
      value.services &&
      typeof value.services === 'object'
    )
      policy = value as SystemPolicy
  } catch {
    /* Invalid text stays editable. */
  }
  const diff = policy ? policyDiff(baseline(b), policy) : []
  const update = (key: keyof ReviewContract, value: number) => setContract((c) => ({ ...c, [key]: value }))
  function submit() {
    onError('')
    try {
      if (!policy) throw new Error('Candidate must be a valid SystemConfig JSON object with entry and services.')
      const seeds = parsedNumbers(axes.seeds)
      if (seeds.some((s) => !Number.isSafeInteger(s) || s < 0))
        throw new Error('Seeds must be nonnegative safe integers.')
      onRun({
        ...b,
        name,
        candidate: policy,
        contract: { ...contract, loads: parsedNumbers(axes.loads), durations: parsedNumbers(axes.durations), seeds },
      })
    } catch (e) {
      onError(errorText(e))
    }
  }
  return (
    <section className="review-editor" aria-label="Review setup">
      <fieldset disabled={busy}>
        <div className="review-actions">
          <button className="btn ghost" onClick={onClose}>
            ← Close draft
          </button>
          <span className="review-caption">
            {b.evidence.source === 'capture'
              ? `Captured ${new Date(b.evidence.capturedAt).toLocaleString()}`
              : 'Declared reference model'}{' '}
            · {Object.keys(b.model.services).length} services · {b.engine}
          </span>
        </div>
        <label className="review-field review-name">
          Review name
          <input value={name} maxLength={120} onChange={(e) => setName(e.target.value)} />
        </label>
        <div className="review-step">
          <div>
            <div className="eyebrow">01 / Envelope</div>
            <h2>Define the failure.</h2>
            <p className="lede">
              Every combination is tested against both policies. Use several seeds to expose sensitivity to traffic
              timing.
            </p>
          </div>
          <div className="review-fields">
            <div className="review-field review-wide">
              <span>Fault targets</span>
              <div className="review-targets">
                {Object.keys(b.model.services)
                  .filter((s) => s !== b.model.entry)
                  .sort()
                  .map((service) => (
                    <label key={service}>
                      <input
                        type="checkbox"
                        checked={contract.services.includes(service)}
                        onChange={(e) =>
                          setContract({
                            ...contract,
                            services: e.target.checked
                              ? [...contract.services, service]
                              : contract.services.filter((s) => s !== service),
                          })
                        }
                      />
                      {service}
                    </label>
                  ))}
              </div>
            </div>
            {(['loads', 'durations', 'seeds'] as const).map((key) => (
              <label key={key} className="review-field">
                {{ loads: 'Loads · req/s', durations: 'Fault durations · seconds', seeds: 'Random seeds' }[key]}
                <input value={axes[key]} onChange={(e) => setAxes({ ...axes, [key]: e.target.value })} />
              </label>
            ))}
            {(
              [
                { key: 'latencyMs', label: 'Added latency · ms', min: 0, max: 30000 },
                { key: 'errorRate', label: 'Injected error rate · 0–1', min: 0, max: 1, step: 0.05 },
                { key: 'warmS', label: 'Warmup · seconds', min: 4, max: 30 },
                { key: 'observeS', label: 'Observation · seconds', min: 10, max: 120 },
              ] as const
            ).map((f) => (
              <label className="review-field" key={f.key}>
                {f.label}
                <input
                  type="number"
                  min={f.min}
                  max={f.max}
                  step={'step' in f ? f.step : 1}
                  value={contract[f.key]}
                  onChange={(e) => update(f.key, e.target.valueAsNumber)}
                />
              </label>
            ))}
            <p className="review-caption review-wide">
              Up to 128 paired cases. Faults are simulated one service at a time.
            </p>
          </div>
        </div>
        <div className="review-step">
          <div>
            <div className="eyebrow">02 / Contract</div>
            <h2>Set the bar.</h2>
            <p className="lede">
              The candidate must meet every limit in every case. A healthy warmup is required before recovery is
              meaningful.
            </p>
          </div>
          <div className="review-fields">
            <label className="review-field">
              Minimum final success · %
              <input
                type="number"
                min="50"
                max="100"
                step="0.1"
                value={+(contract.minSuccessRatio * 100).toFixed(2)}
                onChange={(e) => update('minSuccessRatio', e.target.valueAsNumber / 100)}
              />
            </label>
            <label className="review-field">
              Maximum recovery · seconds
              <input
                type="number"
                min="0"
                max={contract.observeS - 2}
                value={contract.maxRecoveryS}
                onChange={(e) => update('maxRecoveryS', e.target.valueAsNumber)}
              />
            </label>
            <label className="review-field">
              Maximum total backlog
              <input
                type="number"
                min="0"
                max="100000"
                value={contract.maxQueue}
                onChange={(e) => update('maxQueue', e.target.valueAsNumber)}
              />
            </label>
            <p className="review-caption review-wide">
              Recovery means sustained return to 90% of pre-fault goodput. Queue limits apply across the whole run.
            </p>
          </div>
        </div>
        <div className="review-step">
          <div>
            <div className="eyebrow">03 / Candidate</div>
            <h2>Make the change.</h2>
            <p className="lede">
              The initial proposal bounds retries and queues, propagates deadlines, and excludes declared unsafe writes.
              Review it or import your own policy.
            </p>
            <div className="review-actions">
              <button className="btn ghost" onClick={() => file.current?.click()}>
                Import policy JSON ↗
              </button>
              <button className="btn ghost" onClick={() => setCandidate(JSON.stringify(b.candidate, null, 2))}>
                Reset candidate
              </button>
            </div>
            <input
              ref={file}
              type="file"
              accept=".json,application/json"
              hidden
              aria-label="Import candidate policy"
              onChange={async (e) => {
                const selected = e.target.files?.[0]
                e.target.value = ''
                if (!selected) return
                if (selected.size > 1024 * 1024) {
                  onError('Policy exceeds 1 MiB.')
                  return
                }
                try {
                  setCandidate(await selected.text())
                } catch (error) {
                  onError(errorText(error))
                }
              }}
            />
          </div>
          <div>
            <PolicyChanges changes={diff} />
            {policy && (
              <ReviewPolicyEditor policy={policy} onChange={(p) => setCandidate(JSON.stringify(p, null, 2))} />
            )}
            <details className="review-json">
              <summary>Edit candidate JSON</summary>
              <textarea
                aria-label="Candidate policy JSON"
                value={candidate}
                onChange={(e) => setCandidate(e.target.value)}
                spellCheck={false}
              />
            </details>
          </div>
        </div>
        <div className="review-evidence">
          <span className="eyebrow">Evidence quality</span>
          {draft.warnings.map((warning) => (
            <p key={warning}>{warning}</p>
          ))}
        </div>
        <div className="review-actions">
          <button className="btn primary" onClick={submit}>
            Run policy review ↗
          </button>
          <span className="review-caption">
            Live policies remain unchanged. Results are saved with the exact inputs.
          </span>
        </div>
      </fieldset>
    </section>
  )
}

function PolicyChanges({ changes }: { changes: ReturnType<typeof policyDiff> }) {
  return (
    <details className="review-diff" open>
      <summary>{changes.length} changed policy fields</summary>
      {changes.length ? (
        <div className="review-diff-rows">
          {changes.map((change) => (
            <div key={change.path}>
              <code>{change.path}</code>
              <span>
                <del>{change.before}</del> → {change.after}
              </span>
            </div>
          ))}
        </div>
      ) : (
        <p className="review-caption">
          No policy difference. You can still check whether the baseline meets this contract.
        </p>
      )}
    </details>
  )
}

function ReviewResult({
  record,
  busy,
  onRevise,
  onNew,
  onRerun,
}: {
  record: ReviewRecord
  busy: boolean
  onRevise: () => void
  onNew: () => void
  onRerun: () => void
}) {
  const { bundle: b, report: r } = record
  const [metric, setMetric] = useState<'goodput' | 'queue'>('goodput')
  const witness = r.witness
  const selected = witness ? r.cases[witness.caseIndex] : null
  return (
    <section id="review-result" className="review-result" aria-label="Review result">
      <div className="review-actions">
        <button className="btn ghost" onClick={onNew} disabled={busy}>
          ← New review
        </button>
        <span className="review-caption">
          Saved {new Date(r.createdAt).toLocaleString()} ·{' '}
          {b.evidence.source === 'declared' ? 'Declared model' : 'Captured model'}
        </span>
      </div>
      <div className="review-result-heading">
        <div>
          <div className={`review-status ${r.status}`}>
            {r.status === 'pass' ? 'Contract passed' : r.status === 'fail' ? 'Contract failed' : 'Inconclusive'}
          </div>
          <h2>{b.name}</h2>
          <p className="lede">
            {r.status === 'pass'
              ? 'The candidate met every limit in this tested envelope.'
              : r.status === 'fail'
                ? 'At least one candidate case missed the contract. Inspect the evidence before changing your services.'
                : 'The evidence is incomplete. Improve calibration or narrow the envelope, then review again.'}
          </p>
        </div>
        <div className="review-score">
          <span>
            {r.candidatePassed}
            <small> / {r.cases.length}</small>
          </span>
          <p>candidate cases passed</p>
          <p>
            Baseline {r.baselinePassed}/{r.cases.length} · {r.regressions} regressions
          </p>
        </div>
      </div>
      <div className="review-actions">
        <button className="btn primary" onClick={onRevise} disabled={busy}>
          Revise the candidate ↗
        </button>
        <button className="btn ghost" onClick={onRerun} disabled={busy}>
          Rerun exact bundle
        </button>
        <a className="btn ghost" href={`/api/reviews/${record.id}?download=bundle`}>
          Download bundle ↓
        </a>
        <a className="btn ghost" href={`/api/reviews/${record.id}?download=markdown`}>
          Review summary ↓
        </a>
        <button className="btn ghost" onClick={() => downloadJSON('candidate.policy.json', b.candidate)}>
          Candidate policy ↓
        </button>
        <button className="btn ghost" onClick={() => downloadJSON('report.json', r)}>
          JSON evidence ↓
        </button>
      </div>
      {witness && selected && (
        <section className="page-section" aria-labelledby="witness-title">
          <div className="row">
            <div>
              <div className="eyebrow">
                {witness.side === 'representative' ? 'Representative case' : `Smallest tested ${witness.side} failure`}
              </div>
              <h2 id="witness-title">
                {selected.scenario.service} · {selected.scenario.duration}s slowdown
              </h2>
              <p className="review-caption">
                {selected.scenario.load} requests/sec · seed {selected.scenario.seed} · +{b.contract.latencyMs}ms ·{' '}
                {percentage(b.contract.errorRate)} injected errors
              </p>
            </div>
            <div className="review-actions">
              <button className="btn ghost" aria-pressed={metric === 'goodput'} onClick={() => setMetric('goodput')}>
                Goodput
              </button>
              <button className="btn ghost" aria-pressed={metric === 'queue'} onClick={() => setMetric('queue')}>
                Backlog
              </button>
            </div>
          </div>
          <div className="review-chart-labels">
            <span style={{ color: 'var(--text-3)' }}>— Baseline</span>
            <span style={{ color: 'var(--ok)' }}>— Candidate</span>
            <span style={{ color: 'var(--retry)' }}>▴ Fault interval</span>
          </div>
          <div className="review-chart">
            <TimeChart
              x={witness.baseline.t}
              series={[
                {
                  key: 'baseline',
                  label: 'Baseline',
                  values: witness.baseline[metric],
                  color: 'var(--text-3)',
                  dashed: true,
                },
                { key: 'candidate', label: 'Candidate', values: witness.candidate[metric], color: 'var(--ok)' },
              ]}
              bands={[
                { from: b.contract.warmS, to: b.contract.warmS + selected.scenario.duration, color: '#ffb82918' },
              ]}
              xFormat={(t) => `${t}s`}
            />
          </div>
          <div className="review-chart-axis">
            <span>0s</span>
            <span>
              Fault: {b.contract.warmS}–{b.contract.warmS + selected.scenario.duration}s
            </span>
            <span>{witness.baseline.t.at(-1)}s</span>
          </div>
          <div className="review-comparison">
            {(['baseline', 'candidate'] as const).map((side) => (
              <div key={side}>
                <h3>
                  {side === 'baseline' ? 'Baseline' : 'Candidate'}{' '}
                  <span className={`review-status ${selected[side].status}`}>{selected[side].status}</span>
                </h3>
                <p>
                  {percentage(selected[side].finalRatio)} final success · {recovery(selected[side].recoveryS)} recovery
                  · {Math.ceil(selected[side].peakQueue).toLocaleString()} peak backlog
                </p>
                {selected[side].reasons.map((reason) => (
                  <p className="review-reason" key={reason}>
                    {reason}
                  </p>
                ))}
              </div>
            ))}
          </div>
          <p className="review-caption">
            Selected by fault duration, then load, service, and seed. This is the smallest failure tested, not a global
            minimum.
          </p>
        </section>
      )}
      <section className="page-section">
        <h2>Every case, accounted for.</h2>
        <p className="review-caption">
          Contract: ≥{percentage(b.contract.minSuccessRatio)} final success · ≤{b.contract.maxRecoveryS}s recovery · ≤
          {b.contract.maxQueue.toLocaleString()} queued
        </p>
        <div className="policy-table-wrap">
          <table className="policy review-cases">
            <caption className="sr-only">Paired baseline and candidate results for every scenario</caption>
            <thead>
              <tr>
                {[
                  'Target',
                  'Load / sec',
                  'Fault',
                  'Seed',
                  'Baseline',
                  'Candidate',
                  'Final success',
                  'Recovery',
                  'Peak queue',
                ].map((label) => (
                  <th key={label} scope="col">
                    {label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {r.cases.map((c, i) => (
                <tr key={i}>
                  <td>{c.scenario.service}</td>
                  <td>{c.scenario.load}</td>
                  <td>{c.scenario.duration}s</td>
                  <td>{c.scenario.seed}</td>
                  <td className={`review-status ${c.baseline.status}`}>{c.baseline.status}</td>
                  <td>
                    <span className={`review-status ${c.candidate.status}`}>{c.candidate.status}</span>
                    {c.regression && <span className="review-regression">regression</span>}
                    {c.candidate.reasons.length > 0 && (
                      <details>
                        <summary>Why</summary>
                        {c.candidate.reasons.map((reason) => (
                          <p key={reason}>{reason}</p>
                        ))}
                      </details>
                    )}
                  </td>
                  <td>{percentage(c.candidate.finalRatio)}</td>
                  <td>{recovery(c.candidate.recoveryS)}</td>
                  <td>{Math.ceil(c.candidate.peakQueue).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      <section className="page-section review-step">
        <div>
          <div className="eyebrow">Reproduce & share</div>
          <h2>Take it into CI.</h2>
          <p className="lede">
            Download the bundle as <code>review.json</code>. Run the same engine offline, or substitute the policy from
            your change.
          </p>
          <pre className="code">backdraft review --candidate candidate.policy.json --out report.json review.json</pre>
          <p className="review-caption">
            Exit 0: passed · 1: failed · 2: invalid or inconclusive. Pin the Backdraft build in CI. Run{' '}
            <code>backdraft check</code> for static policy hazards too.
          </p>
        </div>
        <div>
          <PolicyChanges changes={policyDiff(baseline(b), b.candidate)} />
          <dl className="review-digests">
            <dt>Bundle SHA-256</dt>
            <dd>{r.bundleDigest}</dd>
            <dt>Model SHA-256</dt>
            <dd>{r.modelDigest}</dd>
            <dt>Engine</dt>
            <dd>{b.engine}</dd>
          </dl>
        </div>
      </section>
      <div className="review-evidence">
        <span className="eyebrow">What this evidence covers</span>
        {r.warnings.map((warning) => (
          <p key={warning}>{warning}</p>
        ))}
      </div>
    </section>
  )
}
