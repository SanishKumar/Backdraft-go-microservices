import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import { latest, useStore } from '../store'
import type { Mitigations } from '../types'
import { fmtPct, fmtRate } from '../format'
import { toast } from './Toast'

function Slider({
  value,
  min,
  max,
  step,
  onChange,
  onCommit,
  fill = 'var(--text-2)',
  label,
}: {
  value: number
  min: number
  max: number
  step: number
  onChange: (v: number) => void
  onCommit?: (v: number) => void
  fill?: string
  label: string
}) {
  const pct = ((value - min) / (max - min)) * 100
  return (
    <input
      type="range"
      aria-label={label}
      min={min}
      max={max}
      step={step}
      value={value}
      style={{ ['--pct' as string]: `${pct}%`, ['--fill' as string]: fill }}
      onChange={(e) => onChange(Number(e.target.value))}
      onPointerUp={(e) => onCommit?.(Number((e.target as HTMLInputElement).value))}
      onKeyUp={(e) => onCommit?.(Number((e.target as HTMLInputElement).value))}
    />
  )
}

function Traffic() {
  const load = useStore((s) => s.state?.load)
  const ready = useStore((s) => s.state?.loadReady)
  const capacity = useStore((s) => s.state?.capacity ?? 0)
  const bottleneck = useStore((s) => s.state?.bottleneck)
  const offered = useStore((s) => latest(s.ticks)?.entry.offered)
  const [rps, setRps] = useState(load?.rps ?? 0)
  const editing = useRef(false)
  useEffect(() => {
    if (!editing.current && load) setRps(load.rps)
  }, [load])

  const commit = (v: number) => {
    editing.current = false
    api.setLoad(v).catch((e) => toast(e.message, true))
  }
  const max = Math.max(400, Math.round((capacity * 0.6) / 50) * 50)

  if (!load) {
    return (
      <div className="section">
        <div className="label">Traffic</div>
        <div className="hint">No load generator attached. Point real traffic at the gateway.</div>
      </div>
    )
  }
  return (
    <div className="section">
      <div className="label">
        Traffic
        <span className="aside">{ready ? `${fmtRate(offered)} req/s live` : 'seeding…'}</span>
      </div>
      <div className="row">
        <span className="field-label">Users send</span>
        <span className="value">{rps} req/s</span>
      </div>
      <Slider
        label="Request rate"
        value={rps}
        min={0}
        max={max}
        step={10}
        onChange={(v) => {
          editing.current = true
          setRps(v)
        }}
        onCommit={commit}
      />
      {capacity > 0 && (
        <div className="hint">
          {fmtPct(rps / capacity)} of the ~{fmtRate(capacity)} req/s the system can serve
          {bottleneck ? ` (${bottleneck} is the limit)` : ''}.
        </div>
      )}
    </div>
  )
}

function Brownout() {
  const services = useStore((s) => s.state?.services ?? [])
  const faults = useStore((s) => s.state?.faults ?? {})
  const events = useStore((s) => s.events)
  const targets = services.filter((s) => s.effective.server.workers > 0).map((s) => s.name)
  const [svc, setSvc] = useState('catalog')
  const [latency, setLatency] = useState(300)
  const [duration, setDuration] = useState(8)
  const [, force] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => force((x) => x + 1), 250)
    return () => window.clearInterval(id)
  }, [])

  const target = targets.includes(svc) ? svc : targets[0]
  const active = Object.entries(faults).filter(([, on]) => on)
  const remaining = (service: string) => {
    const ev = [...events].reverse().find((e) => e.kind === 'fault' && e.service === service && !e.text.includes('cleared'))
    const m = ev?.text.match(/for (\d+(?:\.\d+)?)s/)
    if (!ev || !m) return null
    return Math.max(0, Number(m[1]) - (Date.now() - ev.t) / 1000)
  }

  return (
    <div className="section">
      <div className="label">
        Inject a brownout
        <span className="aside">the trigger</span>
      </div>
      <div className="seg" role="group" aria-label="Service to slow down">
        {targets.map((t) => (
          <button key={t} aria-pressed={t === target} onClick={() => setSvc(t)}>
            {t}
          </button>
        ))}
      </div>
      <div className="row" style={{ marginTop: 12 }}>
        <span className="field-label">Extra latency</span>
        <span className="value">+{latency} ms</span>
      </div>
      <Slider label="Extra latency" value={latency} min={50} max={1500} step={50} onChange={setLatency} fill="var(--retry)" />
      <div className="row">
        <span className="field-label">For</span>
        <span className="value">{duration} s</span>
      </div>
      <Slider label="Duration" value={duration} min={1} max={30} step={1} onChange={setDuration} fill="var(--retry)" />
      <button
        className="btn primary block"
        style={{ marginTop: 10 }}
        disabled={!target}
        onClick={() => api.fault(target, latency, duration).catch((e) => toast(e.message, true))}
      >
        Slow {target} down
      </button>
      {active.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 10 }}>
          {active.map(([name]) => {
            const left = remaining(name)
            return (
              <div key={name} className="row">
                <span className="chip fault">
                  {name} slowed{left !== null ? ` · ${left.toFixed(0)}s left` : ''}
                </span>
                <button className="btn ghost small" onClick={() => api.clearFault(name).catch((e) => toast(e.message, true))}>
                  Clear
                </button>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

const MITIGATIONS: { key: keyof Mitigations; name: string; desc: string }[] = [
  { key: 'retryBudgets', name: 'Retry budgets', desc: 'Retries capped at 10% of requests, per client.' },
  { key: 'singleLayerRetries', name: 'Retry at one layer', desc: 'Only the gateway retries; inner services fail fast.' },
  { key: 'propagateDeadlines', name: 'Propagate deadlines', desc: 'Downstream calls inherit the caller’s deadline.' },
  { key: 'shedExpired', name: 'Shed expired work', desc: 'Drop queued requests whose caller already left.' },
  { key: 'boundedQueues', name: 'Bounded queues', desc: 'Reject past 4× workers instead of queueing forever.' },
]

export function MitigationToggles() {
  const m = useStore((s) => s.state?.mitigations)
  const [busy, setBusy] = useState(false)
  if (!m) return null
  const set = async (next: Mitigations) => {
    setBusy(true)
    try {
      await api.mitigations(next)
    } catch (e) {
      toast((e as Error).message, true)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="section">
      <div className="label">
        Mitigations
        <button className="btn ghost small" disabled={busy} onClick={() => api.reset().catch((e) => toast(e.message, true))}>
          Reset
        </button>
      </div>
      {MITIGATIONS.map((x) => (
        <label key={x.key} className="toggle" onClick={(e) => (e.preventDefault(), !busy && set({ ...m, [x.key]: !m[x.key] }))}>
          <span className="t-name">{x.name}</span>
          <button type="button" className="switch" role="switch" aria-checked={m[x.key]} aria-label={x.name} disabled={busy} />
          <span className="t-desc">{x.desc}</span>
        </label>
      ))}
    </div>
  )
}

export function Controls({ onPlay }: { onPlay: () => void }) {
  const hasLoad = useStore((s) => !!s.state?.load)
  return (
    <div className="rail left" id="rail-left">
      {hasLoad && <div className="panel play-card">
        <div className="section">
          <div className="play-title">See a metastable failure happen</div>
          <div className="hint">
            A 60-second walkthrough: a short slowdown takes this system down, it stays down after the slowdown ends, and one
            policy change brings it back.
          </div>
          <button className="btn block" style={{ marginTop: 10 }} onClick={onPlay}>
            <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden>
              <path d="M2 1l7 4-7 4z" fill="currentColor" />
            </svg>
            Play the incident
          </button>
        </div>
      </div>}
      <div className="panel scroll grow">
        {hasLoad && <Traffic />}
        <Brownout />
        <MitigationToggles />
      </div>
    </div>
  )
}
