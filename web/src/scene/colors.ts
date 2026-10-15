import * as THREE from 'three'

// Scene colours match the CSS semantics: mint is healthy flow, amber is a
// retry, red is a failure, magenta is work done for nobody.
export const C = {
  ok: new THREE.Color('#7fd4bd'),
  retry: new THREE.Color('#f2c14e'),
  bad: new THREE.Color('#f0545c'),
  waste: new THREE.Color('#d86ba6'),
  ember: new THREE.Color('#ff7847'),
  quiet: new THREE.Color('#5d6878'),
  line: new THREE.Color('#3a4350'),
  text: new THREE.Color('#e8eaee'),
}

const tmp = new THREE.Color()

/** Utilization colour: mint when idle, amber when busy, red when full. */
export function heat(u: number, out = tmp): THREE.Color {
  const x = Math.min(Math.max(u, 0), 1)
  if (x < 0.6) return out.copy(C.ok).lerp(C.retry, x / 0.6 * 0.35)
  if (x < 0.9) return out.copy(C.ok).lerp(C.retry, 0.35 + ((x - 0.6) / 0.3) * 0.65)
  return out.copy(C.retry).lerp(C.bad, (x - 0.9) / 0.1)
}
