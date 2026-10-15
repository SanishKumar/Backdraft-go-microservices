export const fmtRate = (x: number | undefined): string => {
  if (x === undefined || !Number.isFinite(x)) return '–'
  if (x >= 10000) return `${(x / 1000).toFixed(0)}k`
  if (x >= 1000) return `${(x / 1000).toFixed(1)}k`
  if (x >= 10) return x.toFixed(0)
  if (x >= 1) return x.toFixed(1)
  if (x > 0) return x.toFixed(2)
  return '0'
}

export const fmtCount = (x: number | undefined): string => {
  if (x === undefined || !Number.isFinite(x)) return '–'
  return Math.round(x).toLocaleString('en-US')
}

export const fmtMs = (ms: number | undefined): string => {
  if (ms === undefined || !Number.isFinite(ms) || ms <= 0) return '–'
  if (ms >= 10000) return `${(ms / 1000).toFixed(0)}s`
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`
  if (ms >= 10) return `${ms.toFixed(0)}ms`
  return `${ms.toFixed(1)}ms`
}

export const fmtPct = (x: number | undefined, digits = 0): string => {
  if (x === undefined || !Number.isFinite(x)) return '–'
  return `${(100 * x).toFixed(digits)}%`
}

export const fmtSecs = (s: number): string => (s < 10 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`)

export const ago = (t: number, now = Date.now()): string => {
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 60) return `${s}s ago`
  return `${Math.floor(s / 60)}m ${s % 60}s ago`
}
