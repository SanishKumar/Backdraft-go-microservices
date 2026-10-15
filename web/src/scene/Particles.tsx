import { useEffect, useMemo, useRef } from 'react'
import { useFrame, useThree } from '@react-three/fiber'
import * as THREE from 'three'
import { particleBus } from '../store'
import type { Particle } from '../types'
import { C } from './colors'
import { pointAt, type EdgeLayout } from './layout'

const SLOTS = 3000
const TRAIL = 4 // points per comet: head plus fading tail
const SPEED = 9 // world units per second along an edge

const vertex = /* glsl */ `
  attribute float aSize;
  attribute float aAlpha;
  attribute vec3 aColor;
  uniform float uScale;
  varying float vAlpha;
  varying vec3 vColor;
  void main() {
    vAlpha = aAlpha;
    vColor = aColor;
    vec4 mv = modelViewMatrix * vec4(position, 1.0);
    // World-sized points, attenuated like THREE.PointsMaterial.
    gl_PointSize = aSize * (uScale / -mv.z);
    gl_Position = projectionMatrix * mv;
  }
`

const fragment = /* glsl */ `
  varying float vAlpha;
  varying vec3 vColor;
  void main() {
    float d = length(gl_PointCoord - 0.5);
    if (d > 0.5) discard;
    float core = smoothstep(0.5, 0.0, d);
    float glow = core * core;
    // A hot white core inside a coloured halo.
    vec3 c = mix(vColor, vec3(1.0), glow * glow * 0.55);
    gl_FragColor = vec4(c * (0.6 + 1.1 * glow), vAlpha * core);
  }
`

const KIND_OK = 0
const KIND_RETRY = 1
const KIND_TIMEOUT = 2
const KIND_ERROR = 3
const KIND_REPLAY = 4

/**
 * Animates sampled attempts along their edges. Particles arrive from the
 * control plane a tick (250ms) after the attempts finished, so the scene
 * runs a quarter second behind real time and spreads each tick's sample
 * across that window.
 */
export function Particles({ edges, highlighted }: { edges: EdgeLayout[]; highlighted: string[] }) {
  const index = useMemo(() => new Map(edges.map((e, i) => [e.key, i])), [edges])
  const state = useRef({
    edge: new Int16Array(SLOTS).fill(-1),
    t0: new Float64Array(SLOTS),
    dur: new Float32Array(SLOTS),
    kind: new Uint8Array(SLOTS),
    next: 0,
  })

  const { geometry, material } = useMemo(() => {
    const g = new THREE.BufferGeometry()
    const n = SLOTS * TRAIL
    g.setAttribute('position', new THREE.BufferAttribute(new Float32Array(n * 3), 3).setUsage(THREE.DynamicDrawUsage))
    g.setAttribute('aColor', new THREE.BufferAttribute(new Float32Array(n * 3), 3).setUsage(THREE.DynamicDrawUsage))
    g.setAttribute('aSize', new THREE.BufferAttribute(new Float32Array(n), 1).setUsage(THREE.DynamicDrawUsage))
    g.setAttribute('aAlpha', new THREE.BufferAttribute(new Float32Array(n), 1).setUsage(THREE.DynamicDrawUsage))
    g.boundingSphere = new THREE.Sphere(new THREE.Vector3(), 60)
    const m = new THREE.ShaderMaterial({
      vertexShader: vertex,
      fragmentShader: fragment,
      uniforms: { uScale: { value: 400 } },
      transparent: true,
      depthWrite: false,
      blending: THREE.AdditiveBlending,
    })
    return { geometry: g, material: m }
  }, [])

  useEffect(() => {
    return particleBus.subscribe((ps: Particle[]) => {
      const s = state.current
      const now = performance.now() / 1000
      for (const p of ps) {
        if (p.o === 'budget') continue
        const ei = index.get(p.e)
        if (ei === undefined) continue
        const slot = s.next
        s.next = (s.next + 1) % SLOTS
        s.edge[slot] = ei
        s.t0[slot] = now + Math.random() * 0.25
        s.dur[slot] = Math.max(0.55, edges[ei].length / SPEED)
        s.kind[slot] = p.r
          ? KIND_REPLAY
          : p.o === 'timeout'
            ? KIND_TIMEOUT
            : p.o !== 'ok'
              ? KIND_ERROR
              : p.a > 1
                ? KIND_RETRY
                : KIND_OK
      }
    })
  }, [edges, index])

  const hl = useMemo(() => new Set(highlighted), [highlighted])
  const color = useMemo(() => new THREE.Color(), [])
  const { size, viewport } = useThree()
  useEffect(() => {
    material.uniforms.uScale.value = (size.height / 2) * viewport.dpr
  }, [material, size.height, viewport.dpr])

  useFrame(() => {
    const s = state.current
    const now = performance.now() / 1000
    const pos = geometry.attributes.position.array as Float32Array
    const col = geometry.attributes.aColor.array as Float32Array
    const size = geometry.attributes.aSize.array as Float32Array
    const alpha = geometry.attributes.aAlpha.array as Float32Array
    const dim = hl.size > 0

    for (let i = 0; i < SLOTS; i++) {
      const ei = s.edge[i]
      const base = i * TRAIL
      if (ei < 0) {
        for (let k = 0; k < TRAIL; k++) alpha[base + k] = 0
        continue
      }
      const u = (now - s.t0[i]) / s.dur[i]
      const kind = s.kind[i]
      // A timed-out attempt dies partway: its caller stopped waiting.
      const end = kind === KIND_TIMEOUT ? 0.62 : 1
      if (u > end + 0.25) {
        s.edge[i] = -1
        for (let k = 0; k < TRAIL; k++) alpha[base + k] = 0
        continue
      }
      const e = edges[ei]
      switch (kind) {
        case KIND_OK:
          color.copy(C.ok)
          break
        case KIND_RETRY:
          color.copy(C.retry)
          break
        case KIND_REPLAY:
          color.copy(C.waste)
          break
        default:
          color.copy(u > 0.5 ? C.bad : C.retry).lerp(C.bad, Math.min(1, u * 1.4))
      }
      let fade = 1
      if (u > end) fade = Math.max(0, 1 - (u - end) / 0.25)
      if (dim && !hl.has(e.key)) fade *= 0.18
      const big = kind === KIND_OK ? 1 : 1.25
      // Failures flare where they die.
      const burst = kind >= KIND_TIMEOUT && kind !== KIND_REPLAY && u > end ? 1 + (u - end) * 6 : 1
      for (let k = 0; k < TRAIL; k++) {
        const j = base + k
        const uk = Math.min(u, end) - k * 0.028
        if (u < 0 || uk < 0) {
          alpha[j] = 0
          continue
        }
        pointAt(e, uk, pos, j * 3)
        col[j * 3] = color.r
        col[j * 3 + 1] = color.g
        col[j * 3 + 2] = color.b
        const tail = 1 - k / TRAIL
        size[j] = 1.15 * big * (0.35 + 0.65 * tail) * (k === 0 ? burst : 1)
        alpha[j] = fade * tail * (k === 0 ? 1 : 0.6)
      }
    }
    geometry.attributes.position.needsUpdate = true
    geometry.attributes.aColor.needsUpdate = true
    geometry.attributes.aSize.needsUpdate = true
    geometry.attributes.aAlpha.needsUpdate = true
  })

  return <points geometry={geometry} material={material} frustumCulled={false} />
}
