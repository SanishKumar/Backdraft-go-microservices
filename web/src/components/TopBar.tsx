import { useStore, type View } from '../store'
import { fmtRate } from '../format'

const TABS: { id: View; label: string }[] = [
  { id: 'live', label: 'Live' },
  { id: 'predict', label: 'Predict' },
  { id: 'policy', label: 'Policy' },
]

export function Mark() {
  return (
    <svg className="brand-mark" viewBox="0 0 32 32" aria-hidden>
      <path
        d="M9 23c0-5 4-6.5 4-11 3 2 4 4.5 3.5 7 1.5-.8 2.3-2.4 2.5-4 2.6 2.6 4 5.3 4 8a7 7 0 0 1-14 0Z"
        fill="none"
        stroke="var(--ember)"
        strokeWidth="2"
        strokeLinejoin="round"
      />
    </svg>
  )
}

export function TopBar() {
  const view = useStore((s) => s.view)
  const setView = useStore((s) => s.setView)
  const connected = useStore((s) => s.connected)
  const capacity = useStore((s) => s.state?.capacity)
  const bottleneck = useStore((s) => s.state?.bottleneck)
  const setTracesOpen = useStore((s) => s.setTracesOpen)
  return (
    <header className="topbar">
      <div className="brand">
        <Mark />
        <span className="brand-name">backdraft</span>
        <span className="brand-tag">finds the retry storm before production does</span>
      </div>
      <nav className="tabs" role="tablist" aria-label="Views">
        {TABS.map((t) => (
          <button key={t.id} className="tab" role="tab" aria-selected={view === t.id} onClick={() => setView(t.id)}>
            {t.label}
          </button>
        ))}
      </nav>
      <div className="top-right">
        {capacity ? (
          <span className="num" title="Healthy capacity estimated from the calibrated model (utilization law)">
            capacity ≈ {fmtRate(capacity)} req/s{bottleneck ? ` · ${bottleneck}-bound` : ''}
          </span>
        ) : null}
        <button
          className="btn small"
          onClick={() => {
            setView('live')
            setTracesOpen(true)
          }}
        >
          Traces
        </button>
        <span className="conn">
          <span className={`dot ${connected ? 'live' : 'bad'}`} />
          {connected ? 'live' : 'reconnecting'}
        </span>
      </div>
    </header>
  )
}
