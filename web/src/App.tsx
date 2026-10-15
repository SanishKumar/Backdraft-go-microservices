import { lazy, Suspense, useEffect } from 'react'
import { connect, useStore } from './store'
import { TopBar } from './components/TopBar'
import { Controls } from './components/Controls'
import { FindingsRail } from './components/Findings'
import { Timeline } from './components/Timeline'
import { Inspector } from './components/Inspector'
import { TracesDrawer } from './components/Traces'
import { DemoCaption, useDemo } from './components/Demo'
import { Toaster } from './components/Toast'
import { PredictView } from './views/Predict'
import { PolicyView } from './views/Policy'

const Scene = lazy(() => import('./scene/Scene').then((m) => ({ default: m.Scene })))

function LiveView() {
  const demo = useDemo()
  return (
    <div className="view live-view">
      <div className="stage" id="stage">
        <Suspense fallback={<div className="empty">Loading the scene…</div>}>
          <Scene />
        </Suspense>
      </div>
      <Controls onPlay={demo.play} />
      <FindingsRail />
      <Timeline />
      <Inspector />
      <TracesDrawer />
      {demo.caption && <DemoCaption caption={demo.caption} progress={demo.progress} onStop={demo.stop} />}
    </div>
  )
}

export function App() {
  const view = useStore((s) => s.view)
  const connected = useStore((s) => s.connected)
  const hasState = useStore((s) => !!s.state)
  useEffect(() => connect(), [])

  return (
    <div className="app">
      <TopBar />
      {!hasState ? (
        <div className="view">
          <div className="empty" style={{ marginTop: '18vh' }}>
            {connected ? 'Loading…' : 'Connecting to the control plane…'}
            <div className="hint" style={{ marginTop: 8 }}>
              Start it with <span className="mono">go run ./cmd/lab</span>
            </div>
          </div>
        </div>
      ) : view === 'live' ? (
        <LiveView />
      ) : view === 'predict' ? (
        <div className="view">
          <PredictView />
        </div>
      ) : (
        <div className="view">
          <PolicyView />
        </div>
      )}
      <Toaster />
    </div>
  )
}
