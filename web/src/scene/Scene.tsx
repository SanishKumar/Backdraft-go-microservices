import { useEffect, useMemo } from 'react'
import { Canvas, useThree } from '@react-three/fiber'
import { OrbitControls } from '@react-three/drei'
import * as THREE from 'three'
import { useStore } from '../store'
import { Edges } from './Edges'
import { Floor } from './Floor'
import { layoutGraph } from './layout'
import { Particles } from './Particles'
import { EntryNode, ServiceNode } from './ServiceNode'

const TARGET = new THREE.Vector3(0, 0.6, 0)

/**
 * Frames the graph in the area the floating panels leave free: shifts the
 * projection so the graph centres there, and pulls the camera back far
 * enough that the graph's bounds fit it. Runs on mount and on resize only,
 * so it never fights the user's own orbiting.
 */
function FrameFreeArea({ halfWidth, halfDepth }: { halfWidth: number; halfDepth: number }) {
  const { camera, size, controls } = useThree()
  useEffect(() => {
    const cam = camera as THREE.PerspectiveCamera
    const update = () => {
      const stage = document.getElementById('stage')?.getBoundingClientRect()
      const left = document.getElementById('rail-left')
      const right = document.getElementById('rail-right')
      const dock = document.getElementById('dock')
      let fw = 1
      let fh = 1
      if (!stage || !left || !right || !dock || getComputedStyle(left).position !== 'absolute') {
        cam.clearViewOffset()
      } else {
        const l = left.getBoundingClientRect().right - stage.left
        const r = right.getBoundingClientRect().left - stage.left
        const b = dock.getBoundingClientRect().top - stage.top
        cam.setViewOffset(size.width, size.height, size.width / 2 - (l + r) / 2, size.height / 2 - b / 2, size.width, size.height)
        fw = Math.max(0.2, (r - l) / size.width)
        fh = Math.max(0.2, b / size.height)
      }
      // Labels are DOM; scale them with the room the scene has.
      const free = fw * size.width
      document.getElementById('stage')?.style.setProperty('--label-scale', String(THREE.MathUtils.clamp(free / 720, 0.56, 1.08)))
      const tanV = Math.tan(THREE.MathUtils.degToRad(cam.fov / 2))
      const tanH = tanV * cam.aspect
      // Labels sit in front of and beside the nodes, and arcs rise above.
      const distW = (halfWidth + 3.4) / (tanH * fw)
      const distH = (halfDepth + 4.5) / (tanV * fh)
      const dist = THREE.MathUtils.clamp(Math.max(distW, distH) * 0.92, 14, 60)
      const dir = cam.position.clone().sub(TARGET).normalize()
      cam.position.copy(TARGET).addScaledVector(dir, dist)
      cam.updateProjectionMatrix()
      ;(controls as unknown as { update?: () => void } | null)?.update?.()
    }
    update()
    const ro = new ResizeObserver(update)
    ;['rail-left', 'rail-right', 'dock'].forEach((id) => {
      const el = document.getElementById(id)
      if (el) ro.observe(el)
    })
    return () => ro.disconnect()
  }, [camera, size, controls, halfWidth, halfDepth])
  return null
}

export function Scene() {
  const syntheticEntry = useStore((s) => !!s.state?.load)
  const topology = useStore((s) => s.state?.topology)
  const entry = useStore((s) => s.state?.entry ?? 'users')
  const selected = useStore((s) => s.selected)
  const select = useStore((s) => s.select)
  const highlighted = useStore((s) => s.hoveredEdges)
  const key = topology ? JSON.stringify(topology) : ''
  const graph = useMemo(() => (topology ? layoutGraph(topology, entry) : null), [key, entry])

  return (
    <Canvas
      fallback={<div className="empty">3D is unavailable in this browser. Metrics, traces, and policy controls remain available.</div>}
      dpr={[1, 2]}
      camera={{ position: [-3, 15.5, 24], fov: 34, near: 0.5, far: 160 }}
      gl={{ antialias: true, powerPreference: 'high-performance' }}
      onPointerMissed={() => select(null)}
    >
      <color attach="background" args={['#08090c']} />
      <fog attach="fog" args={['#08090c', 32, 64]} />
      <ambientLight intensity={0.45} />
      <directionalLight position={[6, 14, 8]} intensity={1.1} />
      <directionalLight position={[-8, 6, -6]} intensity={0.35} color="#8fb4ff" />
      {graph && <FrameFreeArea halfWidth={graph.halfWidth} halfDepth={graph.halfDepth} />}
      <Floor />
      {graph && (
        <group position-y={-0.6}>
          <Edges edges={graph.edges} highlighted={highlighted} />
          {[...graph.nodes.values()].map((n) =>
            n.name === entry && syntheticEntry ? (
              <EntryNode key={n.name} node={n} />
            ) : (
              <ServiceNode key={n.name} node={n} selected={selected === n.name} onSelect={() => select(n.name)} />
            ),
          )}
          <Particles edges={graph.edges} highlighted={highlighted} />
        </group>
      )}
      <OrbitControls
        makeDefault
        enablePan={false}
        enableDamping
        dampingFactor={0.08}
        minDistance={14}
        maxDistance={64}
        minPolarAngle={0.3}
        maxPolarAngle={1.25}
        target={[0, 0.6, 0]}
      />
    </Canvas>
  )
}
