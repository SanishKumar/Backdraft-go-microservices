import { useMemo, useState } from 'react'
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
  const [metric, setMetric] = useState(0)
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

  const metrics = [
    { name: 'Successful requests', value: fmtPct(goodRatio), unit: `of ${fmtRate(data.offered[last])} req/s`, color: goodRatio === undefined ? 'var(--text)' : tone(goodRatio > .97, goodRatio > .6) },
    { name: 'Retry amplification', value: amp === undefined ? '–' : `×${amp.toFixed(2)}`, unit: 'attempts per call', color: amp === undefined ? 'var(--text)' : tone(amp < 1.15, amp < 1.6) },
    { name: 'Backlog', value: fmtCount(data.queued[last]), unit: waste > .05 ? `${fmtPct(waste)} of work wasted` : 'requests queued', color: (data.queued[last] ?? 0) > 20 ? 'var(--retry)' : 'var(--text)' },
  ]
  const series = [
    [
      { key: 'offered', label: 'offered', values: data.offered, color: 'var(--text-3)', dashed: true, width: 1 },
      { key: 'goodput', label: 'succeeded', values: data.goodput, color: 'var(--ok)', area: true },
      { key: 'failed', label: 'failed', values: data.failed, color: 'var(--bad)' },
    ],
    [
      { key: 'requests', label: 'calls', values: data.requests, color: 'var(--text-3)', width: 1.2 },
      { key: 'attempts', label: 'attempts', values: data.attempts, color: 'var(--retry)', area: true },
    ],
    [{ key: 'queued', label: 'queued', values: data.queued, color: 'var(--retry)', area: true }],
  ]
  return (
    <div className="timeline" id="dock">
      <div className="metric-options" role="group" aria-label="Live chart metric">
        {metrics.map((m, i) => <button key={m.name} className="metric-option" aria-pressed={metric === i} onClick={() => setMetric(i)}>
          <span className="label">{m.name}<span aria-hidden>{metric === i ? '↘' : '↗'}</span></span>
          <span className="metric-value" style={{ color: m.color }}>{m.value}</span>
          <span className="hint">{m.unit}</span>
        </button>)}
      </div>
      <div className="chart-focus" role="img" aria-label={`${metrics[metric].name}, last 90 seconds. Current value: ${metrics[metric].value}`}>
        <div className="legend">
          {series[metric].map((s) => <span key={s.key}><i style={{ background: s.color }} />{s.label}</span>)}
          <span className="chart-window">Last 90 seconds · live</span>
        </div>
        <TimeChart x={data.x} domain={domain} events={events} bands={bands} series={series[metric]} log={metric === 2} format={metric === 2 ? fmtCount : fmtRate} />
        <div className="chart-axis"><span>90s ago</span><span>60s</span><span>30s</span><span>Now</span></div>
      </div>
    </div>
  )
}
