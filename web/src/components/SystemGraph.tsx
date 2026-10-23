import { useMemo } from 'react'
import { latest, useStore } from '../store'
import { fmtCount, fmtRate } from '../format'
import { Inspector } from './Inspector'

function layout(topology: Record<string, string[]>, entry: string) {
  const names = [...new Set([entry, ...Object.keys(topology), ...Object.values(topology).flat()])]
  const incoming = new Map(names.map((name) => [name, 0]))
  for (const peers of Object.values(topology)) for (const peer of peers) incoming.set(peer, incoming.get(peer)! + 1)
  const depth = new Map(names.map((name) => [name, 0]))
  const queue = names.filter((name) => incoming.get(name) === 0)
  // Longest dependency depth keeps direct and indirect calls distinct.
  for (let i = 0; i < queue.length; i++) {
    for (const to of topology[queue[i]] ?? []) {
      depth.set(to, Math.max(depth.get(to)!, depth.get(queue[i])! + 1))
      incoming.set(to, incoming.get(to)! - 1)
      if (incoming.get(to) === 0) queue.push(to)
    }
  }
  // Cycles remain inspectable, without repeatedly visiting their nodes.
  for (const name of names) if (incoming.get(name)! > 0) depth.set(name, name === entry ? 0 : 1)
  const maxDepth = Math.max(1, ...depth.values())
  const width = Math.max(850, (maxDepth + 1) * 220)
  const columns = Array.from({ length: maxDepth + 1 }, (_, d) => names.filter((n) => depth.get(n) === d).sort())
  const height = Math.max(320, Math.max(...columns.map((c) => c.length)) * 140)
  const nodes = columns.flatMap((col, d) => col.map((name, i) => ({
    name, x: 90 + d / maxDepth * (width - 180), y: (i + .5) / col.length * height,
  })))
  return { nodes, width, height }
}

export function SystemGraph() {
  const topology = useStore((s) => s.state?.topology)
  const entry = useStore((s) => s.state?.entry ?? 'users')
  const syntheticEntry = useStore((s) => !!s.state?.load)
  const tick = useStore((s) => latest(s.ticks))
  const selected = useStore((s) => s.selected)
  const select = useStore((s) => s.select)
  const faults = useStore((s) => s.state?.faults)
  const highlighted = useStore((s) => s.hoveredEdges)
  const graph = useMemo(() => layout(topology ?? {}, entry), [topology, entry])
  return (
    <div className="system-graph">
      <div className="row graph-heading"><span className="label">The live call graph</span><span className="hint">Select a service to inspect it <span aria-hidden>↗</span></span></div>
      <div className="graph-scroll" tabIndex={0} role="region" aria-label="Live service call graph">
        <div className="graph-canvas" style={{ minWidth: graph.width, height: graph.height }}>
          <svg viewBox={`0 0 ${graph.width} ${graph.height}`} preserveAspectRatio="none" className="graph-links" aria-hidden="true">
            <defs><marker id="direction" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="6" markerHeight="6" orient="auto"><path d="m1 1 5 3-5 3" fill="none" stroke="context-stroke" /></marker></defs>
            {Object.entries(topology ?? {}).flatMap(([from, peers]) => peers.map((to) => {
              const a = graph.nodes.find((n) => n.name === from), b = graph.nodes.find((n) => n.name === to)
              if (!a || !b) return null
              const stats = tick?.edges[`${from}>${to}`]
              const color = highlighted.includes(`${from}>${to}`) ? 'var(--iris)' : (stats?.retries ?? 0) > 1 ? 'var(--retry)' : 'var(--verdant)'
              const bow = b.x - a.x > 300 ? (b.y < a.y ? -65 : 65) : 0
              const path = `M ${a.x + 25} ${a.y - 22} C ${(a.x + b.x) / 2} ${a.y - 22 + bow}, ${(a.x + b.x) / 2} ${b.y - 22 + bow}, ${b.x - 32} ${b.y - 22}`
              return <g key={`${from}>${to}`}>
                <path d={path} fill="none" stroke={color} strokeOpacity=".3" markerEnd="url(#direction)" />
                {(stats?.attempts ?? 0) > 0 && <path d={path} fill="none" stroke={color} strokeDasharray="2 24" className="flow-line" />}
                <text x={(a.x + b.x) / 2} y={(a.y + b.y) / 2 - 35 + bow * .75} textAnchor="middle" fill="var(--text-3)" fontSize="11">{fmtRate(stats?.attempts)}/s{(stats?.retries ?? 0) > 0 ? ` · ${fmtRate(stats?.retries)} retries` : ''}</text>
              </g>
            }))}
          </svg>
          {graph.nodes.map((node, i) => {
            const stats = tick?.services[node.name]
            return <button key={node.name} className="service-node" aria-label={`Inspect ${node.name}`} aria-pressed={selected === node.name} onClick={() => select(selected === node.name ? null : node.name)} style={{ left: `${node.x / graph.width * 100}%`, top: node.y - 53 }}>
              <svg viewBox="0 0 70 60" aria-hidden="true">
                {Array.from({ length: 28 }, (_, n) => {
                  const a = n * 2.39996, r = 3 + Math.sqrt(n) * 4.5
                  const x = 35 + Math.cos(a) * r, y = 28 + Math.sin(a) * r * .7
                  return <path key={n} d={`m${x} ${y - 2.5} 2.5 4.5h-5z`} fill="none" stroke={faults?.[node.name] ? 'var(--retry)' : ['#8052ff', '#ffb829', '#43c6ad', '#ad8fff'][(n + i) % 4]} strokeWidth=".8" />
                })}
              </svg>
              <span className="service-name">{node.name}</span>
              <span className="hint">{faults?.[node.name] ? 'Slowdown active' : `${fmtRate(node.name === entry && syntheticEntry ? tick?.entry.offered : stats?.arrivals)} req/s`}</span>
              {(stats?.queued ?? 0) > 0 && <span className="queue-label">{fmtCount(stats?.queued)} queued</span>}
            </button>
          })}
        </div>
      </div>
      <Inspector />
    </div>
  )
}
