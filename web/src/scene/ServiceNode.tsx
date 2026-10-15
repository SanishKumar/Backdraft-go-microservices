import { useMemo, useRef } from 'react'
import { useFrame } from '@react-three/fiber'
import { Html } from '@react-three/drei'
import * as THREE from 'three'
import { latest, useStore } from '../store'
import { fmtCount, fmtMs, fmtPct, fmtRate } from '../format'
import { C, heat } from './colors'
import { COLUMN_HEIGHT as H, type NodeLayout } from './layout'

const R = 0.62
const MAX_DISCS = 22
const DISC_GAP = 0.15

/** Discs for a queue length: logarithmic, so 10 and 20,000 both read. */
const discsFor = (q: number) => (q < 1 ? 0 : Math.min(MAX_DISCS, 1 + Math.floor(Math.log2(q) * 1.5)))

const ease = (dt: number, rate = 6) => 1 - Math.exp(-dt * rate)

export function ServiceNode({ node, selected, onSelect }: { node: NodeLayout; selected: boolean; onSelect: () => void }) {
  const name = node.name
  const fill = useRef<THREE.Mesh>(null)
  const fillMat = useRef<THREE.MeshStandardMaterial>(null)
  const discs = useRef<THREE.InstancedMesh>(null)
  const halo = useRef<THREE.Mesh>(null)
  const haloMat = useRef<THREE.MeshBasicMaterial>(null)
  const hazard = useRef<THREE.Mesh>(null)
  const padMat = useRef<THREE.MeshBasicMaterial>(null)
  const anim = useRef({ util: 0, discs: 0, waste: 0 })
  const tmp = useMemo(() => ({ m: new THREE.Matrix4(), c: new THREE.Color(), q: new THREE.Quaternion(), s: new THREE.Vector3(), p: new THREE.Vector3() }), [])

  const stats = useStore((s) => latest(s.ticks)?.services[name])
  const cfg = useStore((s) => s.state?.services.find((v) => v.name === name)?.effective)
  const fault = cfg?.fault
  const faulted = !!fault && (fault.latencyMs > 0 || fault.errorRate > 0)

  useFrame((state, dt) => {
    const st = latest(useStore.getState().ticks)?.services[name]
    const a = anim.current
    const workers = st?.workers ?? 0
    const util = st ? (workers > 0 ? st.inFlight / workers : Math.min(1, st.inFlight / 48)) : 0
    const waste = st && st.arrivals > 1 ? Math.min(1, st.late / st.arrivals) : 0
    a.util += (util - a.util) * ease(dt)
    a.discs += (discsFor(st?.queued ?? 0) - a.discs) * ease(dt, 4)
    a.waste += (waste - a.waste) * ease(dt, 3)

    if (fill.current && fillMat.current) {
      const u = Math.max(0.015, Math.min(1, a.util))
      fill.current.scale.y = u
      fill.current.position.y = (u * H) / 2
      heat(a.util, fillMat.current.color)
      fillMat.current.emissive.copy(fillMat.current.color).multiplyScalar(0.35 + 0.4 * a.util)
    }
    if (discs.current) {
      for (let i = 0; i < MAX_DISCS; i++) {
        const show = Math.min(1, Math.max(0, a.discs - i))
        const r = 1 - i * 0.012
        tmp.s.set(r * show, show, r * show)
        tmp.p.set(0, H + 0.2 + i * DISC_GAP, 0)
        tmp.m.compose(tmp.p, tmp.q, tmp.s)
        discs.current.setMatrixAt(i, tmp.m)
        tmp.c.copy(C.retry).lerp(C.bad, i / MAX_DISCS)
        discs.current.setColorAt(i, tmp.c)
      }
      discs.current.instanceMatrix.needsUpdate = true
      if (discs.current.instanceColor) discs.current.instanceColor.needsUpdate = true
    }
    if (halo.current && haloMat.current) {
      const pulse = 0.75 + 0.25 * Math.sin(state.clock.elapsedTime * 5)
      haloMat.current.opacity = a.waste * 0.85 * pulse
      halo.current.scale.setScalar(1 + a.waste * 0.25 * pulse)
    }
    if (hazard.current) hazard.current.rotation.z += dt * 1.6
    if (padMat.current) padMat.current.opacity = selected ? 0.55 : 0.22
  })

  const wastePct = stats && stats.arrivals > 1 ? stats.late / stats.arrivals : 0
  const unbounded = (cfg?.server.workers ?? 0) === 0

  return (
    <group position={node.pos}>
      {/* pad */}
      <mesh rotation-x={-Math.PI / 2} position-y={0.005} onClick={(e) => (e.stopPropagation(), onSelect())}>
        <ringGeometry args={[R + 0.12, R + 0.32, 64]} />
        <meshBasicMaterial ref={padMat} color={selected ? C.ember : C.line} transparent opacity={0.22} />
      </mesh>
      {/* glass column */}
      <mesh position-y={H / 2} onClick={(e) => (e.stopPropagation(), onSelect())}>
        <cylinderGeometry args={[R, R, H, 48, 1, true]} />
        <meshStandardMaterial color="#9fb2c8" transparent opacity={0.07} side={THREE.DoubleSide} depthWrite={false} />
      </mesh>
      {[0, H].map((y) => (
        <mesh key={y} position-y={y} rotation-x={Math.PI / 2}>
          <torusGeometry args={[R, 0.012, 8, 64]} />
          <meshBasicMaterial color={unbounded ? C.quiet : C.line} />
        </mesh>
      ))}
      {/* utilization */}
      <mesh ref={fill}>
        <cylinderGeometry args={[R - 0.1, R - 0.1, H, 48]} />
        <meshStandardMaterial ref={fillMat} color={C.ok} roughness={0.4} metalness={0.1} transparent opacity={0.92} />
      </mesh>
      {/* queue */}
      <instancedMesh ref={discs} args={[undefined, undefined, MAX_DISCS]} frustumCulled={false}>
        <cylinderGeometry args={[R - 0.04, R - 0.04, 0.07, 40]} />
        <meshStandardMaterial roughness={0.5} emissive="#5a3a10" emissiveIntensity={0.6} />
      </instancedMesh>
      {/* wasted work: finished after the caller gave up */}
      <mesh ref={halo} position-y={H * 0.5} rotation-x={Math.PI / 2}>
        <torusGeometry args={[R + 0.22, 0.05, 12, 64]} />
        <meshBasicMaterial ref={haloMat} color={C.waste} transparent opacity={0} depthWrite={false} blending={THREE.AdditiveBlending} />
      </mesh>
      {faulted && (
        <mesh ref={hazard} rotation-x={-Math.PI / 2} position-y={0.01}>
          <ringGeometry args={[R + 0.42, R + 0.56, 48, 1, 0, Math.PI * 1.6]} />
          <meshBasicMaterial color={C.retry} transparent opacity={0.85} />
        </mesh>
      )}

      <Html position={[0, 0, R + 1.05]} center zIndexRange={[4, 0]} style={{ pointerEvents: 'none' }}>
        <div className={`node-label${selected ? ' selected' : ''}`}>
          <div className="nl-name">{name}</div>
          <div className="nl-row num">
            {fmtRate(stats?.arrivals)}/s <span className="nl-sep">·</span> p99 {fmtMs(stats?.p99)}
          </div>
          <div className="nl-row num">
            {unbounded ? (
              <span className="nl-dim">unbounded</span>
            ) : (
              <>
                {stats?.inFlight ?? 0}/{stats?.workers ?? cfg?.server.workers ?? 0} busy
                {(stats?.queued ?? 0) > 0 && (
                  <>
                    <span className="nl-sep">·</span>
                    <span className="nl-queue">q {fmtCount(stats?.queued)}</span>
                  </>
                )}
              </>
            )}
          </div>
          {(faulted || wastePct > 0.05) && (
            <div className="nl-chips">
              {faulted && <span className="chip fault">+{fault!.latencyMs}ms</span>}
              {wastePct > 0.05 && <span className="chip waste">{fmtPct(wastePct)} wasted</span>}
            </div>
          )}
        </div>
      </Html>
    </group>
  )
}

/** The entry: a crowd of users rather than a server. */
export function EntryNode({ node }: { node: NodeLayout }) {
  const group = useRef<THREE.Group>(null)
  const dots = useMemo(() => {
    const out: [number, number][] = []
    for (let i = 0; i < 46; i++) {
      const r = 0.25 + Math.sqrt(i / 46) * 1.05
      const a = i * 2.39996
      out.push([Math.cos(a) * r, Math.sin(a) * r])
    }
    return out
  }, [])
  const entry = useStore((s) => latest(s.ticks)?.entry)
  const good = entry && entry.offered > 0 ? Math.min(1, entry.goodput / entry.offered) : 1

  useFrame((state) => {
    if (!group.current) return
    group.current.children.forEach((c, i) => {
      c.position.y = 0.09 + 0.05 * Math.sin(state.clock.elapsedTime * 2.2 + i)
    })
  })

  return (
    <group position={node.pos}>
      <group ref={group}>
        {dots.map(([x, z], i) => (
          <mesh key={i} position={[x, 0.09, z]}>
            <sphereGeometry args={[0.07, 10, 10]} />
            <meshBasicMaterial color={i / dots.length < good ? C.ok : C.bad} />
          </mesh>
        ))}
      </group>
      <mesh rotation-x={-Math.PI / 2} position-y={0.005}>
        <ringGeometry args={[1.45, 1.52, 64]} />
        <meshBasicMaterial color={C.line} transparent opacity={0.5} />
      </mesh>
      <Html position={[0, 0, 2.1]} center zIndexRange={[4, 0]} style={{ pointerEvents: 'none' }}>
        <div className="node-label">
          <div className="nl-name">{node.name}</div>
          <div className="nl-row num">
            {fmtRate(entry?.offered)}/s offered
          </div>
          <div className="nl-row num">
            <span style={{ color: good > 0.95 ? 'var(--ok)' : good > 0.5 ? 'var(--retry)' : 'var(--bad)' }}>
              {fmtPct(good)} succeed
            </span>
          </div>
        </div>
      </Html>
    </group>
  )
}
