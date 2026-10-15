import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import { useStore } from '../store'
import type { Mitigations } from '../types'
import { fmtPct } from '../format'

const NONE: Mitigations = {
  retryBudgets: false,
  singleLayerRetries: false,
  propagateDeadlines: false,
  shedExpired: false,
  boundedQueues: false,
}

interface Caption {
  step: string
  text: string
  live?: () => string | null
  final?: boolean
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const id = window.setTimeout(resolve, ms)
    signal.addEventListener('abort', () => {
      window.clearTimeout(id)
      reject(new DOMException('stopped', 'AbortError'))
    })
  })

/** Success ratio over the last two seconds of ticks. */
function successRatio(): number {
  const ticks = useStore.getState().ticks.slice(-8)
  const offered = ticks.reduce((s, t) => s + t.entry.offered, 0)
  const good = ticks.reduce((s, t) => s + t.entry.goodput, 0)
  return offered > 0 ? Math.min(1, good / offered) : 1
}

async function waitFor(cond: () => boolean, maxMs: number, signal: AbortSignal) {
  const until = Date.now() + maxMs
  while (Date.now() < until) {
    if (cond()) return true
    await sleep(500, signal)
  }
  return false
}

export function useDemo() {
  const [caption, setCaption] = useState<Caption | null>(null)
  const [progress, setProgress] = useState(0)
  const ctrl = useRef<AbortController | null>(null)

  const stop = useCallback(() => {
    ctrl.current?.abort()
    ctrl.current = null
    setCaption(null)
  }, [])

  const play = useCallback(async () => {
    ctrl.current?.abort()
    const c = new AbortController()
    ctrl.current = c
    const s = c.signal
    const at = (p: number, cap: Caption) => {
      setProgress(p)
      setCaption(cap)
    }
    try {
      useStore.getState().setView('live')
      await api.reset()
      await api.setLoad(200)
      const capacity = useStore.getState().state?.capacity ?? 0
      at(0.05, {
        step: '1 / 5 · Steady state',
        text: `Users send 200 requests a second to a system that can serve about ${Math.round(capacity / 10) * 10 || 'nine hundred'}. Every hop retries failed calls up to three times, which is a sensible choice when you look at one hop at a time.`,
        live: () => `${fmtPct(successRatio())} of requests succeed`,
      })
      await waitFor(() => successRatio() > 0.97, 12000, s)
      await sleep(5000, s)

      await api.fault('catalog', 300, 8)
      at(0.25, {
        step: '2 / 5 · The trigger',
        text: 'Catalog slows down by 300 ms for 8 seconds: a GC pause, a noisy neighbour, a slow disk. Timeouts fire, and every layer above it retries. Watch the amber.',
        live: () => `${fmtPct(successRatio())} succeed`,
      })
      await sleep(8500, s)

      at(0.45, {
        step: '3 / 5 · The trigger is gone',
        text: 'Catalog is fast again, and every one of its workers is busy. But almost all of that work finishes after the caller has already given up, so it is thrown away.',
        live: () => `${fmtPct(successRatio())} succeed · fault cleared`,
      })
      await sleep(10000, s)

      at(0.6, {
        step: '4 / 5 · Metastable',
        text: 'Nothing is broken and nothing gets through. The retries the slowdown set off now keep catalog saturated by themselves: three attempts at each of three layers. Left alone, this does not heal.',
        live: () => `${fmtPct(successRatio())} succeed`,
      })
      await sleep(9000, s)

      await api.mitigations({ ...NONE, retryBudgets: true })
      at(0.75, {
        step: '5 / 5 · One policy change',
        text: 'Retry budgets on: each client may retry at most 10% of its requests. Same code, same incident. Watch the attempts fall away and the backlog drain.',
        live: () => `${fmtPct(successRatio())} succeed`,
      })
      const t0 = Date.now()
      const healed = await waitFor(() => successRatio() > 0.97, 45000, s)
      const took = Math.round((Date.now() - t0) / 1000)
      at(1, {
        step: 'Recovered',
        text: healed
          ? `Back to full health ${took} seconds after the change, with the incident long over. The Predict tab maps every load and incident size this policy survives, before production finds out.`
          : 'Still draining. Try switching on "Shed expired work" as well, then open the Predict tab to see why it helps.',
        final: true,
      })
    } catch (e) {
      if ((e as Error).name !== 'AbortError') setCaption({ step: 'Error', text: (e as Error).message, final: true })
    }
  }, [])

  useEffect(() => () => ctrl.current?.abort(), [])
  return { caption, progress, play, stop }
}

export function DemoCaption({ caption, progress, onStop }: { caption: Caption; progress: number; onStop: () => void }) {
  const setView = useStore((s) => s.setView)
  const [, tick] = useState(0)
  useEffect(() => {
    if (!caption.live) return
    const id = window.setInterval(() => tick((x) => x + 1), 500)
    return () => window.clearInterval(id)
  }, [caption])
  return (
    <div className="caption panel" role="status" aria-live="polite">
      <div className="step">
        <span>{caption.step}</span>
        {caption.live && <span style={{ color: 'var(--text-2)', letterSpacing: 0, textTransform: 'none' }}>{caption.live()}</span>}
      </div>
      <div>{caption.text}</div>
      <div className="demo-bar">
        <div style={{ width: `${progress * 100}%` }} />
      </div>
      <div style={{ display: 'flex', gap: 8, marginTop: 12, justifyContent: 'flex-end' }}>
        {caption.final ? (
          <>
            <button className="btn ghost small" onClick={() => (api.reset(), onStop())}>
              Reset the lab
            </button>
            <button
              className="btn primary small"
              onClick={() => {
                onStop()
                setView('predict')
              }}
            >
              Open Predict
            </button>
          </>
        ) : (
          <button className="btn ghost small" onClick={() => (api.clearFault('catalog'), onStop())}>
            Stop
          </button>
        )}
      </div>
    </div>
  )
}
