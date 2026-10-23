import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { latest, useStore } from '../store'
import type { ClientPolicy, ServiceConfig } from '../types'
import { fmtRate } from '../format'
import { toast } from '../components/Toast'

const CODES = ['UNAVAILABLE', 'DEADLINE_EXCEEDED', 'RESOURCE_EXHAUSTED', 'INTERNAL']
const clone = <T,>(x: T): T => JSON.parse(JSON.stringify(x))
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

function NumCell({ value, declared, onChange, min = 0, max = 100000, placeholder, label }: {
  label: string
  value: number | undefined
  declared: number | undefined
  onChange: (v: number | undefined) => void
  min?: number
  max?: number
  placeholder?: string
}) {
  return (
    <input
      className={`cell-input${value !== declared ? ' changed' : ''}`}
      aria-label={label}
      type="number"
      min={min}
      max={max}
      placeholder={placeholder}
      value={value ?? ''}
      title={value !== declared ? `declared: ${declared ?? 'none'}` : undefined}
      onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))}
    />
  )
}

function Switch({ on, onChange, label }: { on: boolean; onChange: (v: boolean) => void; label: string }) {
  return <button type="button" className="switch" role="switch" aria-checked={on} aria-label={label} onClick={() => onChange(!on)} />
}

export function PolicyView() {
  const state = useStore((s) => s.state)
  const tick = useStore((s) => latest(s.ticks))
  const report = useStore((s) => s.report)
  const services = state?.services ?? []
  const [draft, setDraft] = useState<Record<string, ServiceConfig>>({})
  const [busy, setBusy] = useState(false)

  const previous = useRef<Record<string, ServiceConfig>>({})

  // Keep the draft in step with live config for services not being edited.
  useEffect(() => {
    const baseline = previous.current
    previous.current = Object.fromEntries(services.map((s) => [s.name, s.effective]))
    setDraft((d) => {
      const next = { ...d }
      for (const s of services) if (!d[s.name] || same(d[s.name], baseline[s.name])) next[s.name] = clone(s.effective)
      return next
    })
  }, [services])

  const effective = useMemo(() => Object.fromEntries(services.map((s) => [s.name, s.effective])), [services])
  const declared = useMemo(() => Object.fromEntries(services.map((s) => [s.name, s.declared])), [services])
  const pending = Object.keys(draft).filter((n) => effective[n] && !same(draft[n], effective[n]))

  const edit = (svc: string, f: (c: ServiceConfig) => void) =>
    setDraft((d) => {
      const c = clone(d[svc])
      f(c)
      return { ...d, [svc]: c }
    })
  const editClient = (svc: string, peer: string, f: (p: ClientPolicy) => void) => edit(svc, (c) => f(c.clients![peer]))

  const apply = async () => {
    setBusy(true)
    try {
      await api.patch(Object.fromEntries(pending.map((n) => [n, draft[n]])), `Edited ${pending.join(', ')} policy`)
      toast(`Applied live to ${pending.join(', ')}`)
    } catch (e) {
      toast((e as Error).message, true)
    } finally {
      setBusy(false)
    }
  }

  const exportPolicy = () => {
    const sys = { entry: state?.entry, services: Object.fromEntries(services.map((s) => [s.name, { ...s.effective, fault: { latencyMs: 0, errorRate: 0 } }])) }
    const blob = new Blob([JSON.stringify(sys, null, 2) + '\n'], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'policy.json'
    a.click()
    URL.revokeObjectURL(a.href)
  }

  const edges = services
    .flatMap((s) => Object.keys(draft[s.name]?.clients ?? {}).map((peer) => ({ from: s.name, to: peer })))
    .sort((a, b) => (a.from === state?.entry ? -1 : b.from === state?.entry ? 1 : a.from.localeCompare(b.from) || a.to.localeCompare(b.to)))
  const servers = services.filter((s) => !state?.load || s.name !== state.entry)
  const paths = [...(report?.paths ?? [])].sort((a, b) => b.sustained - a.sustained)

  return (
    <div className="page">
      <div className="page-inner">
        <div className="page-header">
          <div><div className="eyebrow">Policy / Change the outcome</div><h1>Decide when<br />to stop trying.</h1></div>
          <p className="lede">Tune the timeouts, retries, and queues your services enforce. Changes apply to the next request. Amber values differ from the declared policy.</p>
        </div>
        <div className="policy-actions">
            {pending.length > 0 && (
              <button className="btn ghost" onClick={() => setDraft(clone(effective))}>
                Discard
              </button>
            )}
            <button className="btn primary" disabled={!pending.length || busy} onClick={apply}>
              {pending.length ? `Apply to ${pending.join(', ')}` : 'No changes'}
            </button>
        </div>

        <section className="policy-table-wrap" tabIndex={0} role="region" aria-label="Client policies">
          <div className="section" style={{ paddingBottom: 0 }}>
            <h2>Calls</h2>
            <div className="hint">Per-attempt timeout, attempts including the first, base backoff (doubling, full jitter), retry budget as a share of requests.</div>
          </div>
          <table className="policy">
            <thead>
              <tr>
                <th>Call</th>
                <th>Timeout ms</th>
                <th>Attempts</th>
                <th>Backoff ms</th>
                <th>Retry on</th>
                <th>Budget %</th>
                <th>Deadline</th>
                <th>Never retry</th>
                <th style={{ textAlign: 'right' }}>Live</th>
              </tr>
            </thead>
            <tbody>
              {edges.map(({ from, to }) => {
                const p = draft[from].clients![to]
                const d = declared[from]?.clients?.[to]
                const live = tick?.edges[`${from}>${to}`]
                const methods = Object.entries(draft[to]?.methods ?? {}).filter(([, m]) => !m.idempotent && !m.dedupes).map(([m]) => m)
                const isUsers = from === state?.entry
                return (
                  <tr key={`${from}>${to}`} className={same(p, d) ? '' : 'changed'}>
                    <td style={{ whiteSpace: 'nowrap' }}>
                      <span style={{ color: isUsers ? 'var(--text-3)' : undefined }}>{from}</span> → {to}
                      {isUsers && <div className="hint">how people behave</div>}
                    </td>
                    <td>
                      <NumCell label={`${from} to ${to} timeout in milliseconds`} value={p.timeoutMs} declared={d?.timeoutMs} onChange={(v) => editClient(from, to, (x) => (x.timeoutMs = v ?? 0))} />
                    </td>
                    <td>
                      <NumCell label={`${from} to ${to} maximum attempts`} value={p.maxAttempts} declared={d?.maxAttempts} min={1} max={10} onChange={(v) => editClient(from, to, (x) => (x.maxAttempts = Math.max(1, v ?? 1)))} />
                    </td>
                    <td>
                      <NumCell label={`${from} to ${to} backoff in milliseconds`} value={p.backoffMs} declared={d?.backoffMs} onChange={(v) => editClient(from, to, (x) => (x.backoffMs = v ?? 0))} />
                    </td>
                    <td>
                      <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', maxWidth: 260 }}>
                        {CODES.map((c) => {
                          const on = p.retryOn?.includes(c) ?? false
                          return (
                            <button
                              key={c}
                              aria-pressed={on}
                              className={`chip${on ? ' fault' : ''}`}
                              style={{ cursor: 'pointer', background: on ? undefined : 'transparent' }}
                              onClick={() => editClient(from, to, (x) => (x.retryOn = on ? (x.retryOn ?? []).filter((y) => y !== c) : [...(x.retryOn ?? []), c]))}
                            >
                              {c.toLowerCase().replace('_', ' ')}
                            </button>
                          )
                        })}
                      </div>
                    </td>
                    <td>
                      <NumCell
                        label={`${from} to ${to} retry budget percent`}
                        max={100}
                        value={p.budget ? Math.round(p.budget.ratio * 100) : undefined}
                        declared={d?.budget ? Math.round(d.budget.ratio * 100) : undefined}
                        placeholder="none"
                        onChange={(v) => editClient(from, to, (x) => (x.budget = v === undefined ? undefined : { ratio: v / 100, minPerSec: x.budget?.minPerSec ?? 1 }))}
                      />
                    </td>
                    <td>
                      <span className="row" style={{ justifyContent: 'flex-start', gap: 8 }}>
                        <Switch on={p.propagateDeadline} label={`${from} to ${to} propagate deadline`} onChange={(v) => editClient(from, to, (x) => (x.propagateDeadline = v))} />
                        <span className="hint">{p.propagateDeadline ? 'inherits' : 'drops'}</span>
                      </span>
                    </td>
                    <td>
                      <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                        {methods.length === 0 && <span className="hint">–</span>}
                        {methods.map((m) => {
                          const on = p.noRetry?.includes(m) ?? false
                          return (
                            <button
                              key={m}
                              aria-pressed={on}
                              className={`chip${on ? ' ok' : ' bad'}`}
                              style={{ cursor: 'pointer' }}
                              title={on ? 'Never retried' : 'Not idempotent, and retried'}
                              onClick={() => editClient(from, to, (x) => (x.noRetry = on ? (x.noRetry ?? []).filter((y) => y !== m) : [...(x.noRetry ?? []), m]))}
                            >
                              {m}
                            </button>
                          )
                        })}
                      </div>
                    </td>
                    <td className="num" style={{ textAlign: 'right', whiteSpace: 'nowrap', color: 'var(--text-2)' }}>
                      {fmtRate(live?.attempts)}/s
                      {(live?.retries ?? 0) > 0 && <div style={{ color: 'var(--retry)' }}>{fmtRate(live?.retries)} retries</div>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </section>

        <div className="grid-2 policy-pair">
          <section className="policy-table-wrap" tabIndex={0}>
            <div className="section" style={{ paddingBottom: 0 }}>
              <h2>Servers</h2>
              <div className="hint">A worker is held for the whole request, downstream calls included.</div>
            </div>
            <table className="policy">
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Workers</th>
                  <th>Queue limit</th>
                  <th>Shed expired</th>
                  <th>Work ms</th>
                </tr>
              </thead>
              <tbody>
                {servers.map((s) => {
                  const c = draft[s.name]?.server
                  const d = s.declared.server
                  if (!c) return null
                  return (
                    <tr key={s.name} className={same(c, d) ? '' : 'changed'}>
                      <td>{s.name}</td>
                      <td>
                        <NumCell label={`${s.name} workers`} value={c.workers || undefined} declared={d.workers || undefined} placeholder="∞" onChange={(v) => edit(s.name, (x) => (x.server.workers = v ?? 0))} />
                      </td>
                      <td>
                        <NumCell label={`${s.name} queue limit`} value={c.queueLimit || undefined} declared={d.queueLimit || undefined} placeholder="∞" onChange={(v) => edit(s.name, (x) => (x.server.queueLimit = v ?? 0))} />
                      </td>
                      <td>
                        <Switch on={c.shedExpired} label={`${s.name} shed expired work`} onChange={(v) => edit(s.name, (x) => (x.server.shedExpired = v))} />
                      </td>
                      <td>
                        <NumCell label={`${s.name} work in milliseconds`} value={c.workMs} declared={d.workMs} onChange={(v) => edit(s.name, (x) => (x.server.workMs = v))} />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </section>

          <section className="policy-table-wrap" tabIndex={0}>
            <div className="section" style={{ paddingBottom: 0 }}>
              <h2>Every path from {state?.entry}</h2>
              <div className="hint">Attempts one request can become at the end of each path when every attempt fails, sustained through an outage.</div>
            </div>
            <table className="policy">
              <thead>
                <tr>
                  <th>Path</th>
                  <th style={{ textAlign: 'right' }}>Now</th>
                  <th style={{ textAlign: 'right' }}>With budgets</th>
                </tr>
              </thead>
              <tbody>
                {paths.map((p) => (
                  <tr key={p.services.join('>')}>
                    <td className="mono" style={{ fontSize: 12 }}>
                      {p.services.map((s, i) => (
                        <span key={i}>
                          {i > 0 && <span style={{ color: p.attempts[i - 1] > 1 ? 'var(--retry)' : 'var(--text-4)' }}> ×{p.attempts[i - 1]} → </span>}
                          {s}
                        </span>
                      ))}
                    </td>
                    <td className="num" style={{ textAlign: 'right', color: p.sustained >= 8 ? 'var(--bad)' : p.sustained >= 3 ? 'var(--retry)' : 'var(--ok)' }}>
                      ×{p.sustained.toFixed(p.sustained % 1 ? 1 : 0)}
                    </td>
                    <td className="num" style={{ textAlign: 'right', color: 'var(--text-2)' }}>×{p.budgeted.toFixed(1)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
        </div>

        <section className="card section">
          <div className="row" style={{ alignItems: 'flex-start', gap: 24 }}>
            <div style={{ flex: 1 }}>
              <h2>Check it in CI</h2>
              <p className="hint" style={{ fontSize: 13, color: 'var(--text-2)', maxWidth: 620 }}>
                The same analysis runs without a control plane. Commit the policy next to your services and fail the build
                when a change would add a retry layer, outlive a caller's timeout, or retry a write.
              </p>
              <pre className="code">{`$ go run ./cmd/backdraft check --fail-on high policy.json
CRITICAL  One request from users can become 18 attempts on account and catalog
HIGH      order can outlast gateway's 1.5s timeout
...
3 critical, 4 high, 3 medium, 0 low
exit status 1`}</pre>
            </div>
            <button className="btn" onClick={exportPolicy}>
              Download policy.json
            </button>
          </div>
        </section>
      </div>
    </div>
  )
}
