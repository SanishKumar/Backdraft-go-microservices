import { useEffect, useRef } from 'react'
import { latest, useStore } from '../store'

const COLORS = ['#8052ff', '#a983ff', '#ffb829', '#15846e', '#43c6ad', '#df72cf', '#5688f5']

// A deterministic folded volume. This is an abstract system portrait;
// individual triangles are not spans.
function makeParticles() {
  let seed = 3817
  const random = () => { seed = (seed * 1664525 + 1013904223) >>> 0; return seed / 4294967296 }
  return Array.from({ length: 4600 }, (_, i) => {
    const theta = random() * Math.PI * 2
    const phi = Math.acos(2 * random() - 1)
    const side = i % 2 ? 1 : -1
    const fold = 1 + .11 * Math.sin(theta * 7 + phi * 6) + .07 * Math.cos(phi * 11)
    const radius = (i % 5 ? .84 + random() * .16 : Math.cbrt(random())) * fold
    const ambient = i > 4410
    const x = side * (.045 + .84 * Math.abs(Math.sin(phi) * Math.cos(theta)) * radius)
    return {
      x: ambient ? (random() - .5) * 2.7 : x,
      y: ambient ? (random() - .5) * 2.5 : Math.cos(phi) * .83 * radius + .1 * Math.sin(theta * 2),
      z: ambient ? (random() - .5) * 1.8 : Math.sin(phi) * Math.sin(theta) * .62 * radius,
      size: ambient ? 1.5 + random() * 2 : .85 + random() * 1.55,
      phase: random() * Math.PI * 2,
      color: COLORS[Math.floor(random() * COLORS.length)],
      alpha: ambient ? .15 + random() * .3 : .35 + random() * .65,
      ambient,
    }
  })
}

export function Constellation({ paused }: { paused: boolean }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const pausedRef = useRef(paused)
  useEffect(() => { pausedRef.current = paused }, [paused])
  useEffect(() => {
    const el = canvas.current
    const ctx = el?.getContext('2d')
    if (!el || !ctx) return
    const points = makeParticles()
    const motion = matchMedia('(prefers-reduced-motion: reduce)')
    let width = 0, height = 0, frame = 0, time = 0, last = 0, visible = true, reduced = motion.matches
    let dirty = true
    const pointer = { x: 0, y: 0 }
    const resize = new ResizeObserver(([entry]) => {
      width = entry.contentRect.width
      height = entry.contentRect.height
      const dpr = Math.min(devicePixelRatio || 1, 2)
      el.width = Math.round(width * dpr)
      el.height = Math.round(height * dpr)
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      dirty = true
    })
    resize.observe(el)
    const observer = new IntersectionObserver(([e]) => { visible = e.isIntersecting })
    observer.observe(el)
    const onMotion = () => { reduced = motion.matches; dirty = true }
    const onMove = (event: PointerEvent) => {
      if (reduced || pausedRef.current) return
      const rect = el.getBoundingClientRect()
      pointer.x = (event.clientX - rect.left) / rect.width - .5
      pointer.y = (event.clientY - rect.top) / rect.height - .5
    }
    const onLeave = () => { pointer.x = 0; pointer.y = 0 }
    motion.addEventListener('change', onMotion)
    el.addEventListener('pointermove', onMove)
    el.addEventListener('pointerleave', onLeave)
    const draw = (stamp: number) => {
      frame = requestAnimationFrame(draw)
      const delta = Math.min((stamp - last) / 1000, .06)
      if (stamp - last < 32) return
      last = stamp
      if (!visible || document.hidden) return
      const moving = !pausedRef.current && !reduced
      if (!moving && !dirty) return
      dirty = false
      if (moving) time += delta
      const tick = latest(useStore.getState().ticks)
      const stress = tick && tick.entry.offered > 1 ? Math.min(1, tick.entry.failed / tick.entry.offered) : 0
      const yaw = -.24 + Math.sin(time * .12) * .18 + pointer.x * .13
      const tilt = -.24 + pointer.y * .08
      const scale = Math.min(width * .48, height * .48)
      const breath = 1 + Math.sin(time * .6) * .018
      ctx.clearRect(0, 0, width, height)
      for (let i = 0; i < points.length; i++) {
        const p = points[i]
        const drift = Math.sin(time * .3 + p.phase) * (p.ambient ? .035 : .007)
        const x = p.x * Math.cos(yaw) + p.z * Math.sin(yaw)
        const z = p.z * Math.cos(yaw) - p.x * Math.sin(yaw)
        const y = p.y * Math.cos(tilt) - z * Math.sin(tilt)
        const perspective = 2.8 / (2.8 - z)
        const px = width * .51 + (x + drift) * scale * perspective * breath
        const py = height * .47 + y * scale * perspective * breath
        const size = p.size * perspective * (width < 450 ? .8 : 1)
        const angle = p.phase + time * .045
        ctx.globalAlpha = Math.min(1, p.alpha * (.72 + (z + .8) * .18))
        ctx.strokeStyle = stress > .2 && i % 4 === 0 ? '#ffb829' : p.color
        ctx.lineWidth = .9
        ctx.beginPath()
        for (let k = 0; k < 3; k++) {
          const a = angle + k * Math.PI * 2 / 3
          const tx = px + Math.cos(a) * size, ty = py + Math.sin(a) * size
          if (k === 0) ctx.moveTo(tx, ty)
          else ctx.lineTo(tx, ty)
        }
        ctx.closePath()
        ctx.stroke()
      }
      ctx.globalAlpha = 1
    }
    frame = requestAnimationFrame(draw)
    return () => {
      cancelAnimationFrame(frame)
      resize.disconnect()
      observer.disconnect()
      motion.removeEventListener('change', onMotion)
      el.removeEventListener('pointermove', onMove)
      el.removeEventListener('pointerleave', onLeave)
    }
  }, [])
  return <canvas ref={canvas} className="constellation" role="img" aria-label="An abstract system of thousands of colored triangular particles. Amber accents increase as live request failures rise." />
}
