import { useEffect, useState } from 'react'
import { connect, latest, useStore } from './store'
import { TopBar, Mark } from './components/TopBar'
import { Controls } from './components/Controls'
import { FindingsRail, StatusCard } from './components/Findings'
import { Timeline } from './components/Timeline'
import { TracesDrawer } from './components/Traces'
import { DemoCaption, useDemo } from './components/Demo'
import { Toaster } from './components/Toast'
import { Constellation } from './components/Constellation'
import { SystemGraph } from './components/SystemGraph'
import { PredictView } from './views/Predict'
import { PolicyView } from './views/Policy'
import { fmtRate } from './format'

function LiveView() {
  const demo = useDemo()
  const state = useStore((s) => s.state)
  const offered = useStore((s) => latest(s.ticks)?.entry.offered)
  const setView = useStore((s) => s.setView)
  const [paused, setPaused] = useState(false)
  const hasCaption = !!demo.caption
  useEffect(() => {
    if (hasCaption) document.getElementById('incident-progress')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, [hasCaption])
  return (
    <main className="live-view" id="main">
      <section className="hero" aria-labelledby="hero-title">
        <div className="hero-copy">
          <div className="eyebrow"><span className="spark">✳</span> A resilience workbench</div>
          <h1 id="hero-title">Small fault.<br />Long shadow.</h1>
          <p>A slowdown lasts seconds. The retries can last much longer. See how failure spreads through your system—and what brings it back.</p>
          <div className="hero-actions">
            {state?.load ? (
              <button className="btn primary" onClick={demo.play} disabled={!state.loadReady || !!demo.caption}>
                <span className="play-icon" aria-hidden>▷</span>
                {demo.caption ? 'Incident running' : 'Play the incident'}
                <span aria-hidden>↗</span>
              </button>
            ) : <a href="#observe" className="btn primary">Explore your system <span aria-hidden>↗</span></a>}
            <button className="btn ghost" onClick={() => setView('predict')}>Explore a what-if <span aria-hidden>↗</span></button>
          </div>
          <div className="hero-note">{state?.load ? 'Real requests. Real failures. A safe place to learn.' : 'Your live traffic, connected end to end.'}</div>
        </div>
        <div className="hero-art">
          <Constellation paused={paused} />
          <div className="constellation-caption">
            <span><span className="dot live" /> A system of consequences</span>
            <button className="btn ghost small" aria-label={paused ? 'Resume constellation animation' : 'Pause constellation animation'} onClick={() => setPaused(!paused)}>{paused ? 'Play motion' : 'Pause motion'} <span aria-hidden>{paused ? '▷' : 'Ⅱ'}</span></button>
          </div>
        </div>
      </section>
      <div className="hero-bottom">
        <a href="#observe" className="scroll-link">Follow the signal <span aria-hidden>↓</span></a>
        <div className="system-meta">
          <span><i className="dot live" /> {state?.services.length ?? 0} connected services</span>
          <span>{fmtRate(offered)} requests / sec</span><span>gRPC + HTTP</span>
        </div>
      </div>
      <section id="observe" className="chapter observe" aria-labelledby="observe-title">
        <div className="section-heading">
          <div><div className="eyebrow">01 / Observe</div><h2 id="observe-title">Watch the<br />ripple effect.</h2></div>
          <div className="section-intro"><StatusCard /><p>Follow each hop, then inspect the service behind it. The last 90 seconds show what the system is doing now.</p></div>
        </div>
        {demo.caption && <DemoCaption caption={demo.caption} progress={demo.progress} onStop={demo.stop} />}
        <Timeline />
        <SystemGraph />
      </section>
      <Controls />
      <FindingsRail />
      <footer className="footer">
        <div className="brand"><Mark /><span className="brand-name">backdraft</span></div>
        <span>Understand the failure. Change the outcome.</span>
        <button className="btn ghost" onClick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}>Back to top ↑</button>
      </footer>
    </main>
  )
}

export function App() {
  const view = useStore((s) => s.view)
  const connected = useStore((s) => s.connected)
  const hasState = useStore((s) => !!s.state)
  useEffect(() => connect(), [])
  useEffect(() => { window.scrollTo({ top: 0 }) }, [view])
  return (
    <div className="app">
      <a href="#main" className="skip-link">Skip to workspace</a>
      <TopBar />
      {hasState && !connected && <p className="connection-notice" role="status">Connection lost. Showing the last received values while reconnecting.</p>}
      {!hasState ? (
        <main className="loading-state" id="main">
          <div className="eyebrow">Backdraft / Live connection</div>
          <h1>{connected ? 'Finding the signal.' : 'Waiting for your system.'}</h1>
          <p>{connected ? 'Loading services and their policies…' : 'Start the local lab to connect your services.'}</p>
          {!connected && <code>go run ./cmd/lab</code>}
        </main>
      ) : view === 'live' ? <LiveView /> : <main id="main">{view === 'predict' ? <PredictView /> : <PolicyView />}</main>}
      <TracesDrawer />
      <Toaster />
    </div>
  )
}
