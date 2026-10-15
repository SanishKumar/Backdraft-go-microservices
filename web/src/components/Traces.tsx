import { useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import { useStore } from '../store'
import type { SpanView, TraceSummary } from '../types'
import { ago, fmtMs } from '../format'

const outcomeColor: Record<string, string> = {
  ok: 'var(--ok)',
  error: 'var(--bad)',
  timeout: 'var(--bad)',
  shed: 'var(--retry)',
  expired: 'var(--waste)',
  budget: 'var(--quiet)',
}

interface Row {
  span: SpanView
  depth: number
}

/** Orders spans depth-first under their parents, attempts in order. */
function tree(spans: SpanView[]): Row[] {
  const byParent = new Map<string, SpanView[]>()
  const ids = new Set(spans.map((s) => s.id))
  for (const s of spans) {
    const p = s.parent && ids.has(s.parent) ? s.parent : ''
    byParent.set(p, [...(byParent.get(p) ?? []), s])
  }
  const rows: Row[] = []
  const walk = (parent: string, depth: number) => {
    const kids = (byParent.get(parent) ?? []).sort((a, b) => a.startUs - b.startUs)
    for (const k of kids) {
      rows.push({ span: k, depth })
      walk(k.id, depth + 1)
    }
  }
  walk('', 0)
  return rows
}

function Waterfall({ spans }: { spans: SpanView[] }) {
  const rows = useMemo(() => tree(spans), [spans])
  const end = Math.max(...spans.map((s) => Math.max(s.startUs + s.durUs, s.deadlineUs ?? 0)), 1)
  const pct = (us: number) => `${(us / end) * 100}%`
  return (
    <div className="waterfall">
      <div className="legend" style={{ marginBottom: 12 }}>
        <span><i style={{ background: 'var(--ok)' }} />ok</span>
        <span><i style={{ background: 'var(--retry)' }} />retry</span>
        <span><i style={{ background: 'var(--bad)' }} />failed / timed out</span>
        <span><i style={{ background: 'var(--line-2)' }} />queued</span>
        <span><i style={{ background: 'var(--waste)' }} />deadline · finished after it</span>
      </div>
      {rows.map(({ span: s, depth }) => {
        const color = s.outcome === 'ok' && s.attempt > 1 ? 'var(--retry)' : outcomeColor[s.outcome] ?? 'var(--text-3)'
        const label = s.kind === 'client' ? `${s.service} → ${s.peer}` : s.service
        return (
          <div key={s.id} className="wf-row">
            <div className="wf-name" style={{ paddingLeft: depth * 12 }} title={`${label} ${s.method}`}>
              <span style={{ color: s.kind === 'client' ? 'var(--text-3)' : 'var(--text)' }}>{label}</span>{' '}
              <span className="mono" style={{ color: 'var(--text-3)', fontSize: 11 }}>
                {s.method}
                {s.attempt > 1 && <span style={{ color: 'var(--retry)' }}> #{s.attempt}</span>}
              </span>
            </div>
            <div className="wf-track">
              {s.queueUs ? (
                <div className="wf-bar" style={{ left: pct(s.startUs), width: pct(s.queueUs), background: 'var(--line-2)' }} />
              ) : null}
              <div
                className="wf-bar"
                title={`${fmtMs(s.durUs / 1000)} · ${s.code}${s.late ? ' · finished after the caller gave up' : ''}${s.replayed ? ' · idempotent replay' : ''}`}
                style={{
                  left: pct(s.startUs + (s.queueUs ?? 0)),
                  width: pct(Math.max(0, s.durUs - (s.queueUs ?? 0))),
                  background: color,
                  opacity: s.kind === 'client' ? 0.55 : 0.9,
                  boxShadow: s.late ? '0 0 0 1px var(--waste)' : undefined,
                }}
              />
              {s.deadlineUs ? <div className="wf-deadline" style={{ left: pct(s.deadlineUs) }} /> : null}
            </div>
          </div>
        )
      })}
    </div>
  )
}

export function TracesDrawer() {
  const open = useStore((s) => s.tracesOpen)
  const setOpen = useStore((s) => s.setTracesOpen)
  const [filter, setFilter] = useState<'interesting' | 'all'>('interesting')
  const [list, setList] = useState<TraceSummary[]>([])
  const [sel, setSel] = useState<string | null>(null)
  const [spans, setSpans] = useState<SpanView[] | null>(null)

  useEffect(() => {
    if (!open) return
    const load = () => api.traces(filter).then(setList).catch(() => {})
    load()
    const id = window.setInterval(load, 3000)
    return () => window.clearInterval(id)
  }, [open, filter])

  useEffect(() => {
    if (!sel) return setSpans(null)
    api
      .trace(sel)
      .then((t) => setSpans(t.spans))
      .catch(() => setSpans([]))
  }, [sel])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [setOpen])

  if (!open) return null
  return (
    <div className="drawer panel" role="dialog" aria-label="Traces">
      <div className="trace-list">
        <div className="section" style={{ position: 'sticky', top: 0, background: 'var(--panel-solid)', zIndex: 1 }}>
          <div className="label">
            Traces
            <button className="btn ghost small" onClick={() => setOpen(false)} aria-label="Close traces">
              ✕
            </button>
          </div>
          <div className="seg">
            <button aria-pressed={filter === 'interesting'} onClick={() => setFilter('interesting')}>
              retries & failures
            </button>
            <button aria-pressed={filter === 'all'} onClick={() => setFilter('all')}>
              all
            </button>
          </div>
        </div>
        {list.length === 0 && <div className="empty">No traces yet.</div>}
        {list.map((t) => (
          <div key={t.id} className="trace-row" aria-selected={t.id === sel} onClick={() => setSel(t.id)}>
            <span style={{ fontWeight: 500 }}>
              <span className="dot" style={{ display: 'inline-block', marginRight: 7, background: t.ok ? 'var(--ok)' : 'var(--bad)' }} />
              {t.op || 'internal'}
            </span>
            <span className="num" style={{ color: 'var(--text-2)' }}>
              {fmtMs(t.durationMs)}
            </span>
            <span className="hint">
              {t.attempts} attempts{t.retries > 0 && <span style={{ color: 'var(--retry)' }}> · {t.retries} retries</span>}
              {t.late > 0 && <span style={{ color: 'var(--waste)' }}> · {t.late} late</span>}
            </span>
            <span className="hint">{ago(t.start)}</span>
          </div>
        ))}
      </div>
      {spans ? (
        spans.length ? <Waterfall spans={spans} /> : <div className="empty">That trace has been evicted.</div>
      ) : (
        <div className="empty" style={{ alignSelf: 'center' }}>
          Pick a trace. During a storm, look for server work that keeps running after the attempt that asked for it timed
          out, and for bars that end to the right of their deadline line.
        </div>
      )}
    </div>
  )
}
