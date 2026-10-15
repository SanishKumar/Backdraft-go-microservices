import * as THREE from 'three'

export interface NodeLayout {
  name: string
  layer: number
  pos: THREE.Vector3
}

export interface EdgeLayout {
  key: string
  from: string
  to: string
  curve: THREE.QuadraticBezierCurve3
  // Points sampled evenly along the curve, for cheap per-frame lookups.
  lut: Float32Array
  length: number
}

export const COLUMN_HEIGHT = 2
const LAYER_GAP = 4.8
const ROW_GAP = 4.4
const LUT_STEPS = 64

/**
 * Lays the call graph out left to right by call depth (longest path from
 * the entry), so every edge points away from the users, and orders each
 * layer by its parents' positions to keep edges from crossing.
 */
export function layoutGraph(topology: Record<string, string[]>, entry: string) {
  const names = new Set<string>([entry, ...Object.keys(topology)])
  for (const peers of Object.values(topology)) peers.forEach((p) => names.add(p))

  const layer = new Map<string, number>([[entry, 0]])
  const visit = (n: string, depth: number, stack: Set<string>) => {
    for (const next of topology[n] ?? []) {
      if (stack.has(next)) continue
      if ((layer.get(next) ?? -1) < depth + 1) {
        layer.set(next, depth + 1)
        stack.add(next)
        visit(next, depth + 1, stack)
        stack.delete(next)
      }
    }
  }
  visit(entry, 0, new Set([entry]))
  let maxLayer = Math.max(...layer.values())
  for (const n of names) {
    if (!layer.has(n)) layer.set(n, ++maxLayer)
  }

  const byLayer: string[][] = []
  for (const [n, l] of layer) (byLayer[l] ??= []).push(n)

  const parents = new Map<string, string[]>()
  for (const [from, peers] of Object.entries(topology)) {
    for (const p of peers) parents.set(p, [...(parents.get(p) ?? []), from])
  }

  const z = new Map<string, number>()
  byLayer.forEach((nodes) => {
    if (!nodes) return
    const bary = (n: string) => {
      const ps = (parents.get(n) ?? []).filter((p) => z.has(p))
      return ps.length ? ps.reduce((s, p) => s + z.get(p)!, 0) / ps.length : 0
    }
    nodes.sort((a, b) => bary(a) - bary(b) || a.localeCompare(b))
    nodes.forEach((n, i) => z.set(n, (i - (nodes.length - 1) / 2) * ROW_GAP))
  })

  const layers = byLayer.length
  const nodes = new Map<string, NodeLayout>()
  for (const n of names) {
    const l = layer.get(n)!
    nodes.set(n, { name: n, layer: l, pos: new THREE.Vector3((l - (layers - 1) / 2) * LAYER_GAP, 0, z.get(n)!) })
  }

  const edges: EdgeLayout[] = []
  for (const [from, peers] of Object.entries(topology)) {
    for (const to of peers) {
      const a = nodes.get(from)!.pos.clone().setY(COLUMN_HEIGHT + 0.25)
      const b = nodes.get(to)!.pos.clone().setY(COLUMN_HEIGHT + 0.25)
      const dist = a.distanceTo(b)
      const mid = a.clone().lerp(b, 0.5)
      mid.y += 1.1 + dist * 0.2
      const curve = new THREE.QuadraticBezierCurve3(a, mid, b)
      const lut = new Float32Array((LUT_STEPS + 1) * 3)
      curve.getSpacedPoints(LUT_STEPS).forEach((p, i) => p.toArray(lut, i * 3))
      edges.push({ key: `${from}>${to}`, from, to, curve, lut, length: curve.getLength() })
    }
  }
  let halfWidth = 0
  let halfDepth = 0
  for (const n of nodes.values()) {
    halfWidth = Math.max(halfWidth, Math.abs(n.pos.x))
    halfDepth = Math.max(halfDepth, Math.abs(n.pos.z))
  }
  return { nodes, edges, halfWidth, halfDepth }
}

/** Writes the point at fraction u along e into out (u in [0,1]). */
export function pointAt(e: EdgeLayout, u: number, out: Float32Array, offset: number) {
  const f = Math.min(Math.max(u, 0), 1) * LUT_STEPS
  const i = Math.min(Math.floor(f), LUT_STEPS - 1)
  const t = f - i
  const a = i * 3
  const b = a + 3
  out[offset] = e.lut[a] + (e.lut[b] - e.lut[a]) * t
  out[offset + 1] = e.lut[a + 1] + (e.lut[b + 1] - e.lut[a + 1]) * t
  out[offset + 2] = e.lut[a + 2] + (e.lut[b + 2] - e.lut[a + 2]) * t
}
