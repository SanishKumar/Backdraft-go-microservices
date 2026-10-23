import { useMemo, useState } from 'react'
import { api } from '../api'
import { latest, useStore } from '../store'
import type { Finding, Tick } from '../types'
import { fmtPct } from '../format'
import { toast } from './Toast'

type Status = { tone: 'ok' | 'warn' | 'bad'; title: string; text: string }

/**
 * Reads the live system the way an on-call engineer would want it read:
 * not "error rate 100%" but "the trigger is gone and it is still down".
 */
function useStatus(): Status | null {
  const ticks = useStore((s) => s.ticks)
  const events = useStore((s) => s.events)
  const faults = useStore((s) => s.state?.faults ?? {})
  return useMemo(() => {
    const t = latest(ticks)
    if (!t || t.entry.offered < 1) return { tone: 'ok', title: 'Idle', text: 'No traffic yet.' }
    const ratioOf = (w: Tick[]) => {
      const sum = (f: (t: Tick) => number) => w.reduce((s, x) => s + f(x), 0)
      return Math.min(1, sum((x) => x.entry.goodput) / Math.max(1, sum((x) => x.entry.offered)))
    }
    const ratio = ratioOf(ticks.slice(-8))
    const before = ticks.length >= 32 ? ratioOf(ticks.slice(-32, -24)) : ratio
    const queuedNow = Object.values(t.services).reduce((s, x) => s + x.queued, 0)
    const queuedBefore = ticks.length >= 16 ? Object.values(ticks[ticks.length - 16].services).reduce((s, x) => s + x.queued, 0) : queuedNow
    const draining = queuedNow > 50 && queuedNow < queuedBefore * 0.85
    const rising = ratio - before > 0.08 || (draining && ratio > 0.05)
    const faulted = Object.entries(faults).filter(([, on]) => on).map(([n]) => n)
    const lastClear = [...events].reverse().find((e) => e.kind === 'fault' && e.text.includes('cleared'))
    const sinceClear = lastClear ? (t.t - lastClear.t) / 1000 : Infinity

    // The service doing the most work for nobody.
    let worst = ''
    let worstWaste = 0
    for (const [name, s] of Object.entries(t.services)) {
      const w = s.arrivals > 5 ? s.late / s.arrivals : 0
      if (w > worstWaste) [worst, worstWaste] = [name, w]
    }

    if (faulted.length > 0) {
      return {
        tone: ratio > 0.9 ? 'warn' : 'bad',
        title: `Fault active on ${faulted.join(', ')}`,
        text: `${fmtPct(ratio)} of requests succeed while ${faulted.join(', ')} ${faulted.length > 1 ? 'are' : 'is'} slowed down.`,
      }
    }
    if (ratio >= 0.97) {
      return {
        tone: 'ok',
        title: 'Healthy',
        text: Number.isFinite(sinceClear) && sinceClear < 60 ? `Recovered after the last fault.` : `${fmtPct(ratio)} of requests succeed.`,
      }
    }
    if (rising || (draining && ratio < 0.97)) {
      let deepest = ''
      let depth = 0
      for (const [name, s] of Object.entries(t.services)) if (s.queued > depth) [deepest, depth] = [name, s.queued]
      return {
        tone: 'warn',
        title: 'Recovering',
        text:
          `${fmtPct(ratio)} of requests succeed and rising.` +
          (depth > 50 ? ` ${deepest} is still draining a backlog of ${Math.round(depth).toLocaleString('en-US')}.` : ''),
      }
    }
    if (sinceClear >= 4 && sinceClear < 600 && ratio < 0.5) {
      const busy = worst && t.services[worst].workers > 0 ? fmtPct(t.services[worst].inFlight / t.services[worst].workers) : null
      return {
        tone: 'bad',
        title: 'Metastable: still down after the trigger cleared',
        text:
          `The slowdown ended ${Math.round(sinceClear)}s ago and only ${fmtPct(ratio)} of requests succeed. ` +
          (worst
            ? `${worst} is ${busy ? `${busy} busy and ` : ''}${fmtPct(worstWaste)} of its work finishes after the caller gave up. The current traffic and backlog are consistent with retry-sustained overload.`
            : 'Retries are sustaining the overload.'),
      }
    }
    return {
      tone: 'warn',
      title: ratio > 0.5 ? 'Recovering' : 'Degraded',
      text: `${fmtPct(ratio)} of requests succeed.`,
    }
  }, [ticks, events, faults])
}

export function StatusCard() {
  const status = useStatus()
  if (!status) return null
  const color = { ok: 'var(--ok)', warn: 'var(--retry)', bad: 'var(--bad)' }[status.tone]
  return (
    <div className="panel status-card" data-tone={status.tone}>
      <div className="section">
        <div className="status-title" style={{ color }}>
          <span className={`dot${status.tone === 'ok' ? ' live' : ''}`} style={{ background: color }} />
          {status.title}
        </div>
        <div className="hint" style={{ marginTop: 6, color: 'var(--text-2)' }}>
          {status.text}
        </div>
      </div>
    </div>
  )
}

function FindingItem({ f, open, onToggle }: { f: Finding; open: boolean; onToggle: () => void }) {
  const highlight = useStore((s) => s.highlight)
  const setView = useStore((s) => s.setView)
  const [busy, setBusy] = useState(false)
  const edges = (f.edges ?? []).map((e) => `${e.from}>${e.to}`)

  const apply = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!f.patch) return
    setBusy(true)
    try {
      await api.patch(f.patch, `Fix: ${f.title}`)
      toast(`Applied: ${Object.keys(f.patch).join(', ')} updated live`)
    } catch (err) {
      toast((err as Error).message, true)
    } finally {
      setBusy(false)
    }
  }
  const simulate = (e: React.MouseEvent) => {
    e.stopPropagation()
    sessionStorage.setItem('bd-sim-patch', JSON.stringify({ name: 'With this fix', patch: f.patch ?? {} }))
    setView('predict')
  }

  return (
    <div
      className="finding"
      data-sev={f.severity}
      onMouseEnter={() => highlight(edges)}
      onMouseLeave={() => highlight([])}
    >
      <button className="finding-trigger" onClick={onToggle} aria-expanded={open}>
        <span className="f-sev">{f.severity}</span>
        <span className="f-title">{f.title}</span>
        <span className="f-expand" aria-hidden>{open ? '−' : '+'}</span>
      </button>
      {open && (
        <>
          <div className="f-detail">{f.detail}</div>
          <div className="f-fix">{f.fix}</div>
          <div className="f-actions">
            {f.patch && (
              <button className="btn small" disabled={busy} onClick={apply}>
                Apply fix live
              </button>
            )}
            {f.patch && (
              <button className="btn ghost small" onClick={simulate}>
                Simulate first
              </button>
            )}
          </div>
        </>
      )}
    </div>
  )
}

export function FindingsRail() {
  const report = useStore((s) => s.report)
  const [open, setOpen] = useState<string | null>(null)
  const counts = report?.counts ?? {}
  return (
    <section className="chapter findings-layout" id="findings" aria-labelledby="findings-title">
      <div className="findings-intro">
        <div className="eyebrow">03 / Understand</div>
        <h2 id="findings-title">The fault is<br />only the start.</h2>
        <p>Retries multiply. Deadlines drift. Queues keep work that nobody needs. Find the policies that let a small failure become a lasting one.</p>
      </div>
      <div className="findings-list">
        <div className="section" style={{ paddingBottom: 6 }}>
          <div className="label" style={{ marginBottom: 6 }}>
            Findings
            <span className="counts">
              {(['critical', 'high', 'medium'] as const).map((s) =>
                counts[s] ? (
                  <span key={s} className="count" style={{ color: s === 'critical' ? 'var(--bad)' : s === 'high' ? 'var(--ember)' : 'var(--retry)' }}>
                    {counts[s]} {s}
                  </span>
                ) : null,
              )}
            </span>
          </div>
          <div className="hint">From the live policy and observed traffic. Open a finding to inspect the evidence and compare a fix.</div>
        </div>
        {report?.findings.length === 0 && <div className="empty">Nothing to report. Every path is bounded.</div>}
        {report?.findings.map((f) => (
          <FindingItem key={f.id} f={f} open={open === f.id} onToggle={() => setOpen(open === f.id ? null : f.id)} />
        ))}
      </div>
    </section>
  )
}
