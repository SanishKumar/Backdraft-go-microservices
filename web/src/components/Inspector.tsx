import { api } from '../api'
import { latest, useStore } from '../store'
import { fmtCount, fmtMs, fmtPct, fmtRate } from '../format'
import { toast } from './Toast'

export function Inspector() {
  const name = useStore((s) => s.selected)
  const select = useStore((s) => s.select)
  const setView = useStore((s) => s.setView)
  const view = useStore((s) => s.state?.services.find((v) => v.name === name))
  const tick = useStore((s) => latest(s.ticks))
  if (!name || !view) return null
  const st = tick?.services[name]
  const cfg = view.effective
  const peers = Object.entries(cfg.clients ?? {}).sort(([a], [b]) => a.localeCompare(b))
  const wasted = st && st.arrivals > 0 ? st.late / st.arrivals : 0

  return (
    <div className="inspector panel" style={{ top: 'var(--gap)', left: 'calc(var(--rail-left) + 2 * var(--gap))' }}>
      <div className="section">
        <div className="label" style={{ marginBottom: 8 }}>
          <span style={{ color: 'var(--text)', fontSize: 13, letterSpacing: 0, textTransform: 'none', fontWeight: 600 }}>{name}</span>
          <button className="btn ghost small" onClick={() => select(null)} aria-label="Close">
            ✕
          </button>
        </div>
        <dl className="kv">
          <dt>Requests</dt>
          <dd>{fmtRate(st?.arrivals)}/s</dd>
          <dt>Latency p50 / p99</dt>
          <dd>
            {fmtMs(st?.p50)} / {fmtMs(st?.p99)}
          </dd>
          <dt>Workers busy</dt>
          <dd>{cfg.server.workers > 0 ? `${st?.inFlight ?? 0} / ${cfg.server.workers}` : `${st?.inFlight ?? 0} (unbounded)`}</dd>
          <dt>Queued</dt>
          <dd style={{ color: (st?.queued ?? 0) > 0 ? 'var(--retry)' : undefined }}>{fmtCount(st?.queued)}</dd>
          <dt>Finished too late</dt>
          <dd style={{ color: wasted > 0.05 ? 'var(--waste)' : undefined }}>
            {fmtRate(st?.late)}/s ({fmtPct(wasted)})
          </dd>
          <dt>Shed / expired</dt>
          <dd>
            {fmtRate(st?.shed)} / {fmtRate(st?.expired)}
          </dd>
        </dl>
      </div>
      <div className="section">
        <div className="label">Server policy</div>
        <div className="hint" style={{ color: 'var(--text-2)' }}>
          {cfg.server.workers > 0 ? `${cfg.server.workers} workers` : 'Unbounded concurrency'}
          {cfg.server.workers > 0 && `, queue ${cfg.server.queueLimit > 0 ? `≤ ${cfg.server.queueLimit}` : 'unbounded'}`}
          {cfg.server.shedExpired ? ', sheds expired work' : ', keeps expired work'}
          {cfg.server.workMs ? `, ~${cfg.server.workMs}ms of work per request` : ''}.
        </div>
      </div>
      {peers.length > 0 && (
        <div className="section">
          <div className="label">Calls out</div>
          {peers.map(([peer, p]) => {
            const e = tick?.edges[`${name}>${peer}`]
            return (
              <div key={peer} style={{ marginBottom: 8 }}>
                <div className="row" style={{ fontWeight: 500 }}>
                  → {peer}
                  <span className="num" style={{ color: 'var(--text-3)', fontWeight: 400 }}>
                    {fmtRate(e?.attempts)}/s
                    {(e?.retries ?? 0) > 0 && <span style={{ color: 'var(--retry)' }}> · {fmtRate(e?.retries)} retries</span>}
                  </span>
                </div>
                <div className="hint">
                  {p.timeoutMs ? `${p.timeoutMs}ms` : 'no'} timeout × {p.maxAttempts} attempt{p.maxAttempts > 1 ? 's' : ''}
                  {p.budget ? `, budget ${Math.round(p.budget.ratio * 100)}%` : p.maxAttempts > 1 ? ', no budget' : ''}
                  {p.propagateDeadline ? '' : ', drops deadline'}
                </div>
              </div>
            )
          })}
        </div>
      )}
      <div className="section" style={{ display: 'flex', gap: 8 }}>
        {cfg.server.workers > 0 && (
          <button className="btn small" onClick={() => api.fault(name, 300, 8).catch((e) => toast(e.message, true))}>
            Slow down 8s
          </button>
        )}
        <button className="btn ghost small" onClick={() => setView('policy')}>
          Edit policy
        </button>
      </div>
    </div>
  )
}
