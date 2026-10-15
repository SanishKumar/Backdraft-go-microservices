import { useMemo } from 'react'
import { useStore } from '../store'
import type { ControlEvent, Tick } from '../types'
import { fmtCount, fmtPct, fmtRate } from '../format'
import { TimeChart, type Band } from './charts/TimeChart'

const WINDOW_MS = 90_000

/** Shaded spans where a fault was active, reconstructed from events. */
export function faultBands(events: ControlEvent[], now: number): Band[] {
  const open = new Map<string, number>()
  const bands: Band[] = []
  for (const ev of events) {
    if (ev.kind !== 'fault' || !ev.service) continue
    if (ev.text.includes('cleared')) {
      const start = open.get(ev.service)
      if (start !== undefined) bands.push({ from: start, to: ev.t, color: 'rgba(242,193,78,0.07)' })
      open.delete(ev.service)
    } else {
      open.set(ev.service, ev.t)
    }
  }
  for (const start of open.values()) bands.push({ from: start, to: now, color: 'rgba(242,193,78,0.07)' })
  return bands
}

function sumOf<T>(rec: Record<string, T>, pick: (v: T) => number) {
  let s = 0
  for (const v of Object.values(rec)) s += pick(v)
  return s
}

export function Timeline() {
  const ticks = useStore((s) => s.ticks)
  const events = useStore((s) => s.events)
  const entry = useStore((s) => s.state?.entry ?? 'users')
  const now = ticks.length ? ticks[ticks.length - 1].t : Date.now()
  const domain: [number, number] = [now - WINDOW_MS, now]

  const data = useMemo(() => {
    const vis = ticks.filter((t) => t.t >= now - WINDOW_MS - 1000)
    const x = vis.map((t) => t.t)
    const internal = (t: Tick) =>
      Object.fromEntries(Object.entries(t.edges).filter(([k]) => !k.startsWith(entry + '>'))) as Tick['edges']
    return {
      x,
      offered: vis.map((t) => t.entry.offered),
      goodput: vis.map((t) => t.entry.goodput),
      failed: vis.map((t) => t.entry.failed),
      requests: vis.map((t) => sumOf(internal(t), (e) => e.requests)),
      attempts: vis.map((t) => sumOf(internal(t), (e) => e.attempts)),
      denied: vis.map((t) => sumOf(internal(t), (e) => e.budgetDenied)),
      queued: vis.map((t) => sumOf(t.services, (s) => s.queued)),
      late: vis.map((t) => sumOf(t.services, (s) => s.late)),
      arrivals: vis.map((t) => sumOf(t.services, (s) => s.arrivals)),
    }
  }, [ticks, now, entry])

  const last = data.x.length - 1
  const goodRatio = last >= 0 && data.offered[last] > 0 ? Math.min(1, data.goodput[last] / data.offered[last]) : undefined
  const amp = last >= 0 && data.requests[last] > 0 ? data.attempts[last] / data.requests[last] : undefined
  const waste = last >= 0 && data.arrivals[last] > 0 ? data.late[last] / data.arrivals[last] : 0
  const bands = useMemo(() => faultBands(events, now), [events, now])
  const tone = (ok: boolean, warn: boolean) => (ok ? 'var(--ok)' : warn ? 'var(--retry)' : 'var(--bad)')

  return (
    <div className="dock panel" id="dock">
      <div className="chart">
        <div className="label">
          Requests that succeed
          <span className="legend">
            <span><i style={{ background: 'var(--text-3)' }} />offered</span>
            <span><i style={{ background: 'var(--ok)' }} />succeeded</span>
            <span><i style={{ background: 'var(--bad)' }} />failed</span>
          </span>
        </div>
        <div className="headline">
          <span className="big" style={{ color: goodRatio === undefined ? undefined : tone(goodRatio > 0.97, goodRatio > 0.6) }}>
            {fmtPct(goodRatio)}
          </span>
          <span className="unit">of {fmtRate(data.offered[last])} req/s</span>
        </div>
        <TimeChart
          x={data.x}
          domain={domain}
          events={events}
          bands={bands}
          series={[
            { key: 'offered', label: 'offered', values: data.offered, color: 'var(--text-3)', dashed: true, width: 1 },
            { key: 'goodput', label: 'succeeded', values: data.goodput, color: 'var(--ok)', area: true },
            { key: 'failed', label: 'failed', values: data.failed, color: 'var(--bad)' },
          ]}
        />
      </div>
      <div className="chart">
        <div className="label">
          Retry amplification
          <span className="legend">
            <span><i style={{ background: 'var(--text-3)' }} />calls</span>
            <span><i style={{ background: 'var(--retry)' }} />attempts</span>
          </span>
        </div>
        <div className="headline">
          <span className="big" style={{ color: amp === undefined ? undefined : tone(amp < 1.15, amp < 1.6) }}>
            ×{amp === undefined ? '–' : amp.toFixed(2)}
          </span>
          <span className="unit">attempts per call{data.denied[last] > 0 ? ` · ${fmtRate(data.denied[last])}/s held back by budget` : ''}</span>
        </div>
        <TimeChart
          x={data.x}
          domain={domain}
          events={events}
          bands={bands}
          series={[
            { key: 'requests', label: 'calls', values: data.requests, color: 'var(--text-3)', width: 1.2 },
            { key: 'attempts', label: 'attempts', values: data.attempts, color: 'var(--retry)', area: true },
          ]}
        />
      </div>
      <div className="chart">
        <div className="label">
          Backlog
          {waste > 0.05 && <span className="chip waste">{fmtPct(waste)} of work wasted</span>}
        </div>
        <div className="headline">
          <span className="big" style={{ color: (data.queued[last] ?? 0) > 20 ? 'var(--retry)' : undefined }}>
            {fmtCount(data.queued[last])}
          </span>
          <span className="unit">requests queued</span>
        </div>
        <TimeChart
          x={data.x}
          domain={domain}
          events={events}
          bands={bands}
          log
          format={fmtCount}
          series={[{ key: 'queued', label: 'queued', values: data.queued, color: 'var(--retry)', area: true }]}
        />
      </div>
    </div>
  )
}
