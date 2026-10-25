import { useId } from 'react'
import { useStore, type View } from '../store'

const TABS: { id: View; label: string }[] = [
  { id: 'reviews', label: 'Reviews' },
  { id: 'live', label: 'Live system' },
  { id: 'predict', label: 'Predict' },
  { id: 'policy', label: 'Policy' },
]

export function Mark() {
  const id = useId()
  return (
    <svg className="brand-mark" viewBox="0 0 32 36" aria-hidden="true">
      <defs><linearGradient id={id} x1="0" y1="0" x2="1" y2="1"><stop stopColor="#8052ff" /><stop offset="1" stopColor="#15846e" /></linearGradient></defs>
      <path d="M17 1 1 28l16-7 14 14L17 1Z" fill={`url(#${id})`} />
      <path d="m17 1 0 20L1 28" fill="none" stroke="#ad8fff" strokeWidth=".75" />
    </svg>
  )
}

export function TopBar() {
  const view = useStore((s) => s.view)
  const setView = useStore((s) => s.setView)
  const connected = useStore((s) => s.connected)
  const setTracesOpen = useStore((s) => s.setTracesOpen)
  return (
    <header className="topbar">
      <button className="brand" onClick={() => { setView('reviews'); window.scrollTo({ top: 0, behavior: 'smooth' }) }} aria-label="Backdraft home">
        <Mark /><span className="brand-name">backdraft</span>
      </button>
      <nav className="tabs" aria-label="Workspace views">
        {TABS.map((t) => <button key={t.id} className="tab" aria-current={view === t.id ? 'page' : undefined} onClick={() => setView(t.id)}>{t.label}</button>)}
      </nav>
      <div className="top-right">
        <span className="conn" role="status"><span className={`dot ${connected ? 'live' : 'bad'}`} />{connected ? 'Connected' : 'Reconnecting'}</span>
        <button className="btn ghost trace-link" onClick={() => setTracesOpen(true)}>Traces <span aria-hidden>↗</span></button>
      </div>
    </header>
  )
}
