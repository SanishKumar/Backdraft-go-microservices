import { call } from './api'
import type { ServiceConfig } from './types'

export interface SystemPolicy {
  entry: string
  services: Record<string, ServiceConfig>
}
export interface ReviewContract {
  services: string[]
  loads: number[]
  durations: number[]
  seeds: number[]
  latencyMs: number
  errorRate: number
  warmS: number
  observeS: number
  minSuccessRatio: number
  maxRecoveryS: number
  maxQueue: number
}
export interface ReviewBundle {
  version: number
  engine: string
  name: string
  model: { entry: string; services: Record<string, { config: ServiceConfig }>; ops: unknown[] }
  candidate: SystemPolicy
  contract: ReviewContract
  evidence: {
    source: 'capture' | 'declared'
    capturedAt: string
    selfSamples: Record<string, number> | null
    methodSamples: Record<string, Record<string, number>> | null
  }
}
export interface ReviewOutcome {
  status: string
  finalRatio: number
  baselineRatio: number
  recoveryS: number
  peakQueue: number
  bottleneck: string
  reasons: string[]
}
export interface ReviewCase {
  scenario: { service: string; load: number; duration: number; seed: number }
  baseline: ReviewOutcome
  candidate: ReviewOutcome
  regression: boolean
}
export interface ReviewSeries {
  t: number[]
  offered: number[]
  goodput: number[]
  queue: number[]
}
export interface ReviewReport {
  status: string
  createdAt: string
  bundleDigest: string
  modelDigest: string
  cases: ReviewCase[]
  baselinePassed: number
  candidatePassed: number
  regressions: number
  warnings: string[]
  changes: { service: string; before: ServiceConfig; after: ServiceConfig }[]
  witness?: { caseIndex: number; side: string; baseline: ReviewSeries; candidate: ReviewSeries }
}
export interface ReviewRecord {
  id: string
  bundle: ReviewBundle
  report: ReviewReport
}
export interface ReviewSummary {
  id: string
  name: string
  report: ReviewReport
}
export interface ReviewDraft {
  bundle: ReviewBundle
  warnings: string[]
}
export const reviews = {
  list: () => call<{ items: ReviewSummary[]; persistent: boolean }>('GET', '/api/reviews'),
  get: (id: string) => call<ReviewRecord>('GET', `/api/reviews/${encodeURIComponent(id)}`),
  capture: () => call<ReviewDraft>('POST', '/api/reviews/capture', { name: 'Policy review' }),
  validate: (bundle: unknown) => call<ReviewDraft>('POST', '/api/reviews/validate', bundle),
  run: (bundle: ReviewBundle, signal: AbortSignal) => call<ReviewRecord>('POST', '/api/reviews', bundle, signal),
}

export function downloadJSON(name: string, value: unknown) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(value, null, 2) + '\n'], { type: 'application/json' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function policyDiff(
  before: SystemPolicy,
  after: SystemPolicy,
): { path: string; before: string; after: string }[] {
  const flatten = (value: unknown, path = ''): Record<string, string> => {
    if (value && typeof value === 'object' && !Array.isArray(value))
      return Object.assign(
        {},
        ...Object.entries(value).map(([key, item]) => flatten(item, path ? `${path}.${key}` : key)),
      )
    return { [path]: JSON.stringify(value) ?? 'unset' }
  }
  const a = flatten(before.services),
    b = flatten(after.services)
  return [...new Set([...Object.keys(a), ...Object.keys(b)])]
    .sort()
    .filter((key) => a[key] !== b[key])
    .map((path) => ({ path, before: a[path] ?? 'unset', after: b[path] ?? 'unset' }))
}
