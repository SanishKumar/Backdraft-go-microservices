import { create } from 'zustand'
import type { ControlEvent, Report, State, Tick } from './types'

// About two minutes of history at four ticks a second.
const HISTORY = 480

export type View = 'live' | 'predict' | 'policy'

interface Store {
  connected: boolean
  state: State | null
  report: Report | null
  ticks: Tick[]
  events: ControlEvent[]
  view: View
  selected: string | null
  tracesOpen: boolean
  hoveredEdges: string[]
  setView: (v: View) => void
  select: (s: string | null) => void
  setTracesOpen: (open: boolean) => void
  highlight: (edges: string[]) => void
}

export const useStore = create<Store>((set) => ({
  connected: false,
  state: null,
  report: null,
  ticks: [],
  events: [],
  view: 'live',
  selected: null,
  tracesOpen: false,
  hoveredEdges: [],
  setView: (view) => set({ view }),
  select: (selected) => set({ selected }),
  setTracesOpen: (tracesOpen) => set({ tracesOpen }),
  highlight: (hoveredEdges) => set({ hoveredEdges }),
}))

type Message =
  | { type: 'hello'; state: State; findings: Report; history?: Tick[] }
  | { type: 'tick'; tick: Tick; events?: ControlEvent[] }
  | { type: 'state'; state: State }
  | { type: 'findings'; findings: Report }

export function connect(): () => void {
  let ws: WebSocket | null = null
  let stopped = false
  let delay = 500
  let timer: number | undefined

  const open = () => {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    ws = new WebSocket(`${proto}://${location.host}/ws`)
    ws.onopen = () => {
      delay = 500
      useStore.setState({ connected: true })
    }
    ws.onmessage = (ev) => {
      const m = JSON.parse(ev.data) as Message
      switch (m.type) {
        case 'hello':
          useStore.setState({
            state: m.state,
            report: m.findings,
            ticks: (m.history ?? []).slice(-HISTORY),
            events: m.state.events ?? [],
          })
          break
        case 'tick': {
          const { ticks, events } = useStore.getState()
          const next = ticks.length >= HISTORY ? ticks.slice(ticks.length - HISTORY + 1) : ticks.slice()
          const { particles, ...light } = m.tick
          next.push(light)
          useStore.setState({
            ticks: next,
            events: m.events?.length ? [...events, ...m.events].slice(-200) : events,
          })
          break
        }
        case 'state':
          useStore.setState({ state: m.state })
          break
        case 'findings':
          useStore.setState({ report: m.findings })
          break
      }
    }
    ws.onclose = () => {
      useStore.setState({ connected: false })
      if (!stopped) {
        timer = window.setTimeout(open, delay)
        delay = Math.min(delay * 2, 5000)
      }
    }
  }
  open()
  return () => {
    stopped = true
    window.clearTimeout(timer)
    ws?.close()
  }
}

export const latest = (ticks: Tick[]): Tick | undefined => ticks[ticks.length - 1]
