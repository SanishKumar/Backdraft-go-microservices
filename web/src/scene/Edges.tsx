import { useMemo, useRef } from 'react'
import { useFrame } from '@react-three/fiber'
import { Html, Line } from '@react-three/drei'
import * as THREE from 'three'
import { latest, useStore } from '../store'
import { C } from './colors'
import type { EdgeLayout } from './layout'

type LineRef = { material: { color: THREE.Color; opacity: number; linewidth: number } }

function EdgeLine({ edge, dimmed }: { edge: EdgeLayout; dimmed: boolean }) {
  const ref = useRef<LineRef>(null)
  const points = useMemo(() => edge.curve.getPoints(56), [edge])
  const target = useMemo(() => new THREE.Color(), [])
  // Attempts per call over the last two seconds. A first attempt and its
  // retries finish in different ticks, so one tick's ratio is noise on a
  // quiet edge.
  const amp = useStore((s) => {
    let req = 0
    let att = 0
    for (const t of s.ticks.slice(-8)) {
      const e = t.edges[edge.key]
      if (e) {
        req += e.requests
        att += e.attempts
      }
    }
    return req >= 40 ? Math.round((att / req) * 10) / 10 : 1
  })

  useFrame((_, dt) => {
    const st = latest(useStore.getState().ticks)?.edges[edge.key]
    const m = ref.current?.material
    if (!m) return
    const att = st?.attempts ?? 0
    const retryShare = att > 0 ? (st!.retries + st!.timeouts * 0.5) / att : 0
    // Lines stay quiet so the traffic on them reads; they only take on
    // colour when retries dominate.
    target.copy(C.line).lerp(C.ok, Math.min(1, att / 200) * 0.25)
    if (retryShare > 0.05) target.copy(C.line).lerp(C.retry, Math.min(1, retryShare * 1.6) * 0.75)
    if (retryShare > 0.4) target.lerp(C.bad, Math.min(1, (retryShare - 0.4) * 1.5))
    m.color.lerp(target, 1 - Math.exp(-dt * 4))
    const want = dimmed ? 0.08 : 0.28 + Math.min(0.35, att / 600) + Math.min(0.3, retryShare)
    m.opacity += (want - m.opacity) * (1 - Math.exp(-dt * 5))
    m.linewidth = 1 + Math.min(1.6, Math.log10(1 + att) * 0.6)
  })

  const mid = useMemo(() => edge.curve.getPoint(0.5), [edge])

  return (
    <>
      <Line ref={ref as never} points={points} color={C.line} lineWidth={1.2} transparent opacity={0.35} depthWrite={false} />
      {amp >= 1.15 && !dimmed && (
        <Html position={mid} center zIndexRange={[3, 0]} style={{ pointerEvents: 'none' }}>
          <div className={`amp num${amp >= 2 ? ' hot' : ''}`}>×{amp.toFixed(1)}</div>
        </Html>
      )}
    </>
  )
}

export function Edges({ edges, highlighted }: { edges: EdgeLayout[]; highlighted: string[] }) {
  const hl = new Set(highlighted)
  return (
    <>
      {edges.map((e) => (
        <EdgeLine key={e.key} edge={e} dimmed={hl.size > 0 && !hl.has(e.key)} />
      ))}
    </>
  )
}
