import { useCallback, useEffect, useMemo, useState } from 'react'
import { scaleLinear } from 'd3-scale'
import { line } from 'd3-shape'
import { api } from '../api'
import { latest, useStore } from '../store'
import type { HysteresisResponse, MapCell, MapResponse, SimResult, Variant, VariantResult } from '../types'
import { fmtCount, fmtPct, fmtRate, fmtSecs } from '../format'
import { TimeChart } from '../components/charts/TimeChart'
import { toast } from '../components/Toast'

const MITIGATED: Variant = { name: 'With all mitigations', base: 'mitigated' }
const CURRENT: Variant = { name: 'Current policy' }

const verdictLabel: Record<string, string> = {
  metastable: 'Metastable',
  recovered: 'Recovers',
  recovering: 'Recovers slowly',
  steady: 'Steady',
  overloaded: 'Overloaded',
  inconclusive: 'Inconclusive',
}

function Verdict({ kind }: { kind: string }) {
  return <span className={`verdict ${kind}`}>{verdictLabel[kind] ?? kind}</span>
}

function storedFix(): Variant | null {
  try {
    const raw = sessionStorage.getItem('bd-sim-patch')
    return raw ? (JSON.parse(raw) as Variant) : null
  } catch {
    return null
  }
}

interface Scenario {
  service: string
  latencyMs: number
  faultS: number
  rps: number
}

function ResultCard({ v, scenario, warmS }: { v: VariantResult; scenario: Scenario; warmS: number }) {
  const r = v.result
  if (!r) return <div className="card section">The model is not ready for this variant yet.</div>
  const queued = r.t.map((_, i) => Object.values(r.services).reduce((s, x) => s + x.queue[i], 0))
  const band = [{ from: warmS, to: warmS + scenario.faultS, color: 'rgba(242,193,78,0.08)' }]
  const fmtT = (t: number) => `t = ${t.toFixed(1)}s`
  return (
    <div className="card" style={{ display: 'flex', flexDirection: 'column' }}>
      <div className="section">
        <div className="row" style={{ alignItems: 'flex-start' }}>
          <div>
            <h2>{v.name}</h2>
            <div className="hint">
              {fmtSecs(r.t[r.t.length - 1])} of traffic, {fmtCount(r.events)} events, simulated in {v.wallMs} ms
            </div>
          </div>
          <Verdict kind={r.verdict.kind} />
        </div>
        <p style={{ margin: '10px 0 0', color: 'var(--text-2)', lineHeight: 1.55 }}>{r.verdict.summary}</p>
      </div>
      <div className="section" style={{ height: 170, display: 'flex', flexDirection: 'column' }}>
        <div className="label">
          Requests that succeed
          <span className="legend">
            <span><i style={{ background: 'var(--text-3)' }} />offered</span>
            <span><i style={{ background: 'var(--ok)' }} />succeeded</span>
          </span>
        </div>
        <TimeChart
          x={r.t}
          bands={band}
          xFormat={fmtT}
          series={[
            { key: 'o', label: 'offered', values: r.offered, color: 'var(--text-3)', dashed: true, width: 1 },
            { key: 'g', label: 'succeeded', values: r.goodput, color: 'var(--ok)', area: true },
          ]}
        />
      </div>
      <div className="section" style={{ height: 150, display: 'flex', flexDirection: 'column' }}>
        <div className="label">
          Backlog
          <span className="aside">peak {fmtCount(Math.max(...queued))}</span>
        </div>
        <TimeChart
          x={r.t}
          bands={band}
          log
          xFormat={fmtT}
          format={fmtCount}
          series={[{ key: 'q', label: 'queued', values: queued, color: 'var(--retry)', area: true }]}
        />
      </div>
    </div>
  )
}

function ScenarioRunner({ scenario, setScenario, targets, run, results, busy }: {
  scenario: Scenario
  setScenario: (s: Scenario) => void
  targets: string[]
  run: () => void
  results: VariantResult[] | null
  busy: boolean
}) {
  const capacity = useStore((s) => s.state?.capacity ?? 0)
  const num = (k: keyof Scenario, label: string, unit: string, min: number, max: number, step: number) => (
    <label className="field">
      <span className="field-label">{label}</span>
      <span className="row" style={{ gap: 8 }}>
        <input
          className="cell-input"
          style={{ width: 84 }}
          type="number"
          min={min}
          max={max}
          step={step}
          value={scenario[k] as number}
          onChange={(e) => setScenario({ ...scenario, [k]: Number(e.target.value) })}
        />
        <span className="hint">{unit}</span>
      </span>
    </label>
  )
  return (
    <section style={{ marginBottom: 36 }}>
      <div className="card">
        <div className="scenario">
          <label className="field">
            <span className="field-label">Slow down</span>
            <div className="seg">
              {targets.map((t) => (
                <button key={t} aria-pressed={t === scenario.service} onClick={() => setScenario({ ...scenario, service: t })} style={{ padding: '6px 12px' }}>
                  {t}
                </button>
              ))}
            </div>
          </label>
          {num('latencyMs', 'by', 'ms', 10, 5000, 50)}
          {num('faultS', 'for', 'seconds', 1, 120, 1)}
          {num('rps', 'at a load of', capacity ? `req/s (${fmtPct(scenario.rps / capacity)} of capacity)` : 'req/s', 1, 20000, 10)}
          <button className="btn primary" onClick={run} disabled={busy} style={{ marginLeft: 'auto' }}>
            {busy ? <span className="spinner" /> : null}
            Simulate
          </button>
        </div>
      </div>
      {results && (
        <div className="grid-2" style={{ marginTop: 16, gridTemplateColumns: `repeat(${Math.min(results.length, 3)}, 1fr)` }}>
          {results.map((v) => (
            <ResultCard key={v.name} v={v} scenario={scenario} warmS={8} />
          ))}
        </div>
      )}
    </section>
  )
}

const cellColor = (c: MapCell) => {
  switch (c.kind) {
    case 'recovered':
      // Faster recovery reads brighter; even slow recovery stays green.
      return `rgba(127, 212, 189, ${c.recoveryS <= 2 ? 0.9 : c.recoveryS <= 8 ? 0.62 : 0.42})`
    case 'recovering':
      return 'rgba(242, 193, 78, 0.75)'
    case 'metastable':
      return 'rgba(240, 84, 92, 0.85)'
    default:
      return 'var(--line-2)'
  }
}

function Heatmap({ title, data, liveRps, onPick, busy }: { title: string; data: MapResponse | null; liveRps?: number; onPick: (c: MapCell) => void; busy: boolean }) {
  const [hover, setHover] = useState<MapCell | null>(null)
  if (!data) {
    return (
      <div className="card section" style={{ minHeight: 320, display: 'grid', placeItems: 'center' }}>
        {busy ? <span className="spinner" /> : <span className="hint">Run a stability map to compare this policy.</span>}
      </div>
    )
  }
  const durations = [...data.durations].sort((a, b) => b - a)
  const cell = (l: number, d: number) => data.cells.find((c) => c.load === l && c.duration === d)!
  const stuck = data.cells.filter((c) => c.kind === 'metastable').length
  // The lightest load at which some incident in the map is not survived.
  const firstFail = data.loads.find((l) => data.durations.some((d) => cell(l, d).kind === 'metastable'))
  const liveCol = liveRps ? data.loads.findIndex((l) => l >= liveRps) : -1
  return (
    <div className="card section">
      <div className="row" style={{ marginBottom: 6 }}>
        <h2>{title}</h2>
        <span className="hint">{data.wallMs} ms for {data.cells.length} simulations</span>
      </div>
      <div className="hint" style={{ marginBottom: 14, minHeight: 36 }}>
        {stuck === 0
          ? `Survives every incident in this range: a ${data.latencyMs}ms slowdown of ${data.service} for up to ${Math.max(...data.durations)}s, at up to ${fmtRate(Math.max(...data.loads))} req/s.`
          : `From ${fmtRate(firstFail)} req/s (${fmtPct((firstFail ?? 0) / data.capacity)} of capacity), some ${data.latencyMs}ms slowdowns of ${data.service} do not recover within the observation window. ${stuck} of ${data.cells.length} scenarios remain overloaded.`}
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '44px 1fr', gap: 8 }}>
        <div style={{ display: 'grid', gridTemplateRows: `repeat(${durations.length}, 1fr)`, gap: 3 }}>
          {durations.map((d) => (
            <div key={d} className="num hint" style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end' }}>
              {d}s
            </div>
          ))}
        </div>
        <div style={{ position: 'relative' }}>
          <div className="heat" style={{ gridTemplateColumns: `repeat(${data.loads.length}, 1fr)` }} onMouseLeave={() => setHover(null)}>
            {durations.map((d) =>
              data.loads.map((l) => {
                const c = cell(l, d)
                return (
                  <button
                    key={`${l}-${d}`}
                    style={{ background: cellColor(c) }}
                    onMouseEnter={() => setHover(c)}
                    onFocus={() => setHover(c)}
                    onClick={() => onPick(c)}
                    aria-label={`${l} req/s, ${d}s slowdown: ${c.kind}`}
                  />
                )
              }),
            )}
          </div>
          {liveCol >= 0 && (
            <div
              title="Current live load"
              style={{
                position: 'absolute',
                top: -6,
                bottom: -6,
                left: `calc(${(liveCol / data.loads.length) * 100}% - 1px)`,
                width: 2,
                background: 'var(--text)',
                opacity: 0.6,
                borderRadius: 1,
                pointerEvents: 'none',
              }}
            />
          )}
        </div>
        <div />
        <div style={{ display: 'grid', gridTemplateColumns: `repeat(${data.loads.length}, 1fr)`, gap: 3 }}>
          {data.loads.map((l) => (
            <div key={l} className="num hint" style={{ textAlign: 'center' }}>
              {fmtRate(l)}
            </div>
          ))}
        </div>
      </div>
      <div className="row" style={{ marginTop: 10 }}>
        <span className="hint">↑ slowdown duration · → load (req/s)</span>
        <span className="hint num" style={{ minHeight: 18 }}>
          {hover
            ? `${fmtRate(hover.load)} req/s, ${hover.duration}s: ${
                hover.kind === 'recovered'
                  ? `recovers in ${fmtSecs(hover.recoveryS)}`
                  : hover.kind === 'metastable'
                    ? `stuck at ${fmtPct(hover.finalRatio)} success`
                    : verdictLabel[hover.kind]
              } — click to simulate`
            : 'hover a cell, click to replay it'}
        </span>
      </div>
    </div>
  )
}

function HysteresisPlot({ v, lo, hi }: { v: VariantResult; lo: number; hi: number }) {
  const r = v.result as SimResult
  const w = 520
  const h = 260
  const pad = { l: 42, r: 12, t: 12, b: 30 }
  const sx = scaleLinear().domain([0, hi * 1.05]).range([pad.l, w - pad.r])
  const sy = scaleLinear().domain([0, hi * 1.05]).range([h - pad.b, pad.t])
  // Smooth over 4s so the loop reads through the Poisson noise.
  const smooth = (xs: number[]) => xs.map((_, i) => {
    const a = Math.max(0, i - 4)
    const b = Math.min(xs.length, i + 5)
    let s = 0
    for (let k = a; k < b; k++) s += xs[k]
    return s / (b - a)
  })
  const off = smooth(r.offered)
  const good = smooth(r.goodput)
  const mid = Math.floor(r.t.length / 2)
  const pts = (from: number, to: number) => off.slice(from, to).map((o, i) => [o, good[from + i]] as [number, number])
  const path = line<[number, number]>().x((d) => sx(d[0])).y((d) => sy(d[1]))
  const ticks = sx.ticks(5)
  return (
    <div className="card section">
      <div className="row" style={{ marginBottom: 8 }}>
        <h2>{v.name}</h2>
        <span className="legend">
          <span><i style={{ background: 'var(--ok)' }} />load rising</span>
          <span><i style={{ background: 'var(--retry)' }} />load falling</span>
        </span>
      </div>
      <svg viewBox={`0 0 ${w} ${h}`} style={{ width: '100%', height: 'auto' }}>
        {ticks.map((t) => (
          <g key={t}>
            <line x1={sx(t)} x2={sx(t)} y1={pad.t} y2={h - pad.b} stroke="var(--line)" strokeDasharray="2 4" />
            <text x={sx(t)} y={h - pad.b + 16} textAnchor="middle" fill="var(--text-4)" fontSize={10} fontFamily="var(--mono)">
              {fmtRate(t)}
            </text>
            <line x1={pad.l} x2={w - pad.r} y1={sy(t)} y2={sy(t)} stroke="var(--line)" strokeDasharray="2 4" />
            <text x={pad.l - 6} y={sy(t) + 3} textAnchor="end" fill="var(--text-4)" fontSize={10} fontFamily="var(--mono)">
              {fmtRate(t)}
            </text>
          </g>
        ))}
        <line x1={sx(0)} y1={sy(0)} x2={sx(hi)} y2={sy(hi)} stroke="var(--text-4)" strokeDasharray="4 4" />
        <path d={path(pts(0, mid + 1)) ?? undefined} fill="none" stroke="var(--ok)" strokeWidth={2} />
        <path d={path(pts(mid, r.t.length)) ?? undefined} fill="none" stroke="var(--retry)" strokeWidth={2} strokeDasharray="5 3" />
        <text x={w - pad.r} y={h - 4} textAnchor="end" fill="var(--text-3)" fontSize={10.5} fontFamily="var(--mono)">
          offered req/s →
        </text>
        <text x={pad.l + 4} y={pad.t + 10} fill="var(--text-3)" fontSize={10.5} fontFamily="var(--mono)">
          ↑ succeeded req/s
        </text>
      </svg>
      <div className="hint" style={{ marginTop: 6 }}>
        Ramped {fmtRate(lo)} → {fmtRate(hi)} → {fmtRate(lo)} req/s with no fault. {r.verdict.summary}
      </div>
    </div>
  )
}

export function PredictView() {
  const state = useStore((s) => s.state)
  const liveRps = useStore((s) => latest(s.ticks)?.entry.offered)
  const targets = useMemo(() => (state?.services ?? []).filter((s) => s.effective.server.workers > 0).map((s) => s.name), [state?.services])
  const [scenario, setScenario] = useState<Scenario>({
    service: state?.bottleneck || 'catalog',
    latencyMs: 300,
    faultS: 8,
    rps: Math.round(state?.load?.rps || 200),
  })
  const [results, setResults] = useState<VariantResult[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [maps, setMaps] = useState<[MapResponse | null, MapResponse | null]>([null, null])
  const [mapBusy, setMapBusy] = useState(false)
  const [hyst, setHyst] = useState<HysteresisResponse | null>(null)
  const [hystBusy, setHystBusy] = useState(false)
  const fix = useMemo(storedFix, [])

  const variants = useMemo(() => (fix ? [CURRENT, fix, MITIGATED] : [CURRENT, MITIGATED]), [fix])

  const run = useCallback(
    async (s: Scenario = scenario) => {
      setBusy(true)
      try {
        const res = await api.simRun({ ...s, warmS: 8, observeS: 30, variants })
        setResults(res.variants)
      } catch (e) {
        toast((e as Error).message, true)
      } finally {
        setBusy(false)
      }
    },
    [scenario, variants],
  )

  const runMaps = useCallback(async (service: string, latencyMs: number) => {
    setMapBusy(true)
    setMaps([null, null])
    try {
      const [a, b] = await Promise.all([
        api.simMap({ service, latencyMs, variant: CURRENT }),
        api.simMap({ service, latencyMs, variant: MITIGATED }),
      ])
      setMaps([a, b])
    } catch (e) {
      toast((e as Error).message, true)
    } finally {
      setMapBusy(false)
    }
  }, [])

  useEffect(() => {
    run()
    if (fix) sessionStorage.removeItem('bd-sim-patch')
  }, []) // once on entry, with the defaults

  const pick = (c: MapCell) => {
    const s = { ...scenario, rps: c.load, faultS: c.duration, latencyMs: maps[0]?.latencyMs ?? scenario.latencyMs, service: maps[0]?.service ?? scenario.service }
    setScenario(s)
    run(s)
    document.querySelector('.page')?.scrollTo({ top: 0, behavior: 'smooth' })
  }

  const runHyst = async () => {
    setHystBusy(true)
    try {
      setHyst(await api.hysteresis({ variants: [CURRENT, MITIGATED] }))
    } catch (e) {
      toast((e as Error).message, true)
    } finally {
      setHystBusy(false)
    }
  }

  const cal = state?.calibration
  const selfTotal = cal ? Object.values(cal.selfSamples).reduce((a, b) => a + b, 0) : 0
  const shapeTotal = cal ? Object.values(cal.shapeSamples).reduce((a, b) => a + b, 0) : 0

  return (
    <div className="page">
      <div className="page-inner">
        <h1>What happens if…</h1>
        <p className="lede">
          Backdraft replays this system in a discrete-event simulator calibrated from its live traffic: the observed
          distribution of each service's own work, the calls each operation actually makes, the real request mix, and the
          policies in force right now. A minute of traffic simulates in milliseconds, so it can answer what a dashboard
          cannot: if this slowdown happens, does the system come back when it ends?
        </p>

        <ScenarioRunner scenario={scenario} setScenario={setScenario} targets={targets} run={() => run()} results={results} busy={busy} />

        <section style={{ marginBottom: 36 }}>
          <div className="row" style={{ marginBottom: 12, alignItems: 'flex-end' }}>
            <div>
              <h1 style={{ fontSize: 19 }}>Stability map</h1>
              <p className="lede" style={{ margin: 0 }}>
                Each cell is a full simulation: a {maps[0]?.latencyMs ?? scenario.latencyMs}ms slowdown of{' '}
                {maps[0]?.service ?? scenario.service}, at a given load, for a given time. Red cells remain overloaded
                through the model's observation window.
              </p>
            </div>
            <button className="btn" disabled={mapBusy || busy} onClick={() => runMaps(scenario.service, scenario.latencyMs)}>
              Map {scenario.service} +{scenario.latencyMs}ms
            </button>
          </div>
          <div className="grid-2">
            <Heatmap title="Current policy" data={maps[0]} liveRps={liveRps} onPick={pick} busy={mapBusy} />
            <Heatmap title="With all mitigations" data={maps[1]} liveRps={liveRps} onPick={pick} busy={mapBusy} />
          </div>
          <div className="legend" style={{ marginTop: 10 }}>
            <span><i style={{ background: 'var(--ok)', height: 8 }} />recovers (brighter is faster)</span>
            <span><i style={{ background: 'var(--retry)', height: 8 }} />recovers slowly</span>
            <span><i style={{ background: 'var(--bad)', height: 8 }} />metastable</span>
            <span><i style={{ background: 'var(--text)', height: 8, width: 2 }} />live load</span>
          </div>
        </section>

        <section style={{ marginBottom: 36 }}>
          <div className="row" style={{ marginBottom: 12, alignItems: 'flex-end' }}>
            <div>
              <h1 style={{ fontSize: 19 }}>Hysteresis</h1>
              <p className="lede" style={{ margin: 0 }}>
                Load ramps past capacity and back down, with no fault at all. A healthy system retraces its path. One that
                retries without limit comes back down a different, lower path: at the same load, it is healthy if it got
                there from below and down if it got there from above.
              </p>
            </div>
            <button className="btn" onClick={runHyst} disabled={hystBusy}>
              {hystBusy ? <span className="spinner" /> : null}
              Run the sweep
            </button>
          </div>
          {hyst && (
            <div className="grid-2">
              {hyst.variants.map((v) => (
                <HysteresisPlot key={v.name} v={v} lo={hyst.lo} hi={hyst.hi} />
              ))}
            </div>
          )}
        </section>

        <section>
          <h1 style={{ fontSize: 19 }}>What the model knows</h1>
          <div className="card section" style={{ display: 'flex', gap: 28, flexWrap: 'wrap', alignItems: 'center' }}>
            <div>
              <div className="big num" style={{ fontSize: 20 }}>{fmtCount(selfTotal)}</div>
              <div className="hint">service-time samples</div>
            </div>
            <div>
              <div className="big num" style={{ fontSize: 20 }}>{fmtCount(shapeTotal)}</div>
              <div className="hint">observed call shapes</div>
            </div>
            <div>
              <div className="big num" style={{ fontSize: 20 }}>{cal?.opsObserved ? 'live' : 'declared'}</div>
              <div className="hint">request mix</div>
            </div>
            <div className="hint" style={{ maxWidth: 520 }}>
              Samples are taken from healthy requests only, so the model describes the system, not the incident.
              Anything not yet observed falls back to what the services declare.
            </div>
            <a className="btn" style={{ marginLeft: 'auto', textDecoration: 'none' }} href="/api/model" download="backdraft-model.json">
              Download model
            </a>
          </div>
        </section>
      </div>
    </div>
  )
}
