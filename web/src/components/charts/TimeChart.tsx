import { useEffect, useMemo, useRef, useState } from 'react'
import { scaleLinear, scaleSymlog } from 'd3-scale'
import { area, curveMonotoneX, line } from 'd3-shape'
import type { ControlEvent } from '../../types'
import { fmtRate } from '../../format'

export interface Series {
  key: string
  label: string
  values: number[]
  color: string
  area?: boolean
  width?: number
  dashed?: boolean
}

export interface Band {
  from: number
  to: number
  color: string
}

interface Props {
  x: number[]
  series: Series[]
  domain?: [number, number]
  log?: boolean
  events?: ControlEvent[]
  bands?: Band[]
  format?: (v: number) => string
  xFormat?: (x: number) => string
  minY?: number
}

const eventColor: Record<string, string> = {
  fault: 'var(--retry)',
  config: 'var(--ok)',
  load: 'var(--text-3)',
  info: 'var(--text-4)',
}

function useSize<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [size, setSize] = useState({ w: 0, h: 0 })
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => setSize({ w: e.contentRect.width, h: e.contentRect.height }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, size] as const
}

/**
 * A small multi-series time chart: lines or areas over a shared x, with
 * event markers so cause and effect line up, and a hover readout.
 */
export function TimeChart({ x, series, domain, log, events, bands, format = fmtRate, xFormat, minY = 1 }: Props) {
  const [ref, { w, h }] = useSize<HTMLDivElement>()
  const [hover, setHover] = useState<number | null>(null)
  const pad = { l: 2, r: 34, t: 8, b: 4 }

  const geo = useMemo(() => {
    if (!w || !h || x.length < 2) return null
    const x0 = domain ? domain[0] : x[0]
    const x1 = domain ? domain[1] : x[x.length - 1]
    const sx = scaleLinear().domain([x0, x1]).range([pad.l, w - pad.r])
    let max = minY
    for (const s of series) for (const v of s.values) if (Number.isFinite(v) && v > max) max = v
    const sy = (log ? scaleSymlog().constant(1) : scaleLinear()).domain([0, max * 1.12]).range([h - pad.b, pad.t])
    const ticks = log ? [0, ...[1, 10, 100, 1000, 10000, 100000].filter((t) => t <= max * 1.1)] : sy.ticks(3)
    const paths = series.map((s) => {
      const pts = x.map((xi, i) => [xi, s.values[i]] as [number, number]).filter(([, v]) => Number.isFinite(v))
      const l = line<[number, number]>()
        .x((d) => sx(d[0]))
        .y((d) => sy(d[1]))
        .curve(curveMonotoneX)(pts)
      const a = s.area
        ? area<[number, number]>()
            .x((d) => sx(d[0]))
            .y0(sy(0))
            .y1((d) => sy(d[1]))
            .curve(curveMonotoneX)(pts)
        : null
      return { s, l, a }
    })
    return { sx, sy, ticks, paths, x0, x1 }
  }, [w, h, x, series, domain, log, minY])

  const onMove = (e: React.PointerEvent) => {
    if (!geo) return
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
    const xv = geo.sx.invert(e.clientX - r.left)
    let best = 0
    for (let i = 0; i < x.length; i++) if (Math.abs(x[i] - xv) < Math.abs(x[best] - xv)) best = i
    setHover(best)
  }

  return (
    <div ref={ref} style={{ position: 'relative', flex: 1, minHeight: 0 }} onPointerMove={onMove} onPointerLeave={() => setHover(null)}>
      {geo && (
        <svg width={w} height={h} style={{ position: 'absolute', inset: 0 }}>
          {bands?.map((b, i) => (
            <rect
              key={i}
              x={geo.sx(Math.max(b.from, geo.x0))}
              width={Math.max(0, geo.sx(Math.min(b.to, geo.x1)) - geo.sx(Math.max(b.from, geo.x0)))}
              y={pad.t}
              height={h - pad.t - pad.b}
              fill={b.color}
            />
          ))}
          {geo.ticks.map((t) => (
            <g key={t}>
              <line x1={pad.l} x2={w - pad.r} y1={geo.sy(t)} y2={geo.sy(t)} stroke="var(--line)" strokeDasharray={t === 0 ? undefined : '2 4'} />
              <text x={w - pad.r + 6} y={geo.sy(t) + 3.5} fill="var(--text-4)" fontSize={10} fontFamily="var(--mono)">
                {format(t)}
              </text>
            </g>
          ))}
          {events
            ?.filter((ev) => ev.t >= geo.x0 && ev.t <= geo.x1 && ev.kind !== 'info')
            .map((ev, i) => (
              <g key={`${ev.t}-${i}`}>
                <line x1={geo.sx(ev.t)} x2={geo.sx(ev.t)} y1={pad.t - 4} y2={h - pad.b} stroke={eventColor[ev.kind]} strokeOpacity={0.55} strokeDasharray="3 3" />
                <path d={`M${geo.sx(ev.t) - 4},${pad.t - 7} h8 l-4,5z`} fill={eventColor[ev.kind]} />
              </g>
            ))}
          {geo.paths.map(({ s, a }) =>
            a ? <path key={`${s.key}-a`} d={a} fill={s.color} fillOpacity={0.14} /> : null,
          )}
          {geo.paths.map(({ s, l }) => (
            <path
              key={s.key}
              d={l ?? undefined}
              fill="none"
              stroke={s.color}
              strokeWidth={s.width ?? 1.5}
              strokeDasharray={s.dashed ? '3 3' : undefined}
              strokeLinejoin="round"
            />
          ))}
          {hover !== null && (
            <line x1={geo.sx(x[hover])} x2={geo.sx(x[hover])} y1={pad.t} y2={h - pad.b} stroke="var(--text-3)" strokeWidth={1} />
          )}
        </svg>
      )}
      {geo && hover !== null && (
        <div
          className="crosshair-tip"
          style={{
            left: Math.min(geo.sx(x[hover]) + 10, w - 150),
            top: 6,
          }}
        >
          {xFormat && <div style={{ color: 'var(--text-3)' }}>{xFormat(x[hover])}</div>}
          {series.map((s) => (
            <div key={s.key}>
              <span style={{ color: s.color }}>■</span> {s.label} <span style={{ color: 'var(--text)' }}>{format(s.values[hover] ?? 0)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
