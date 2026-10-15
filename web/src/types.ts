// Mirrors of the control plane's JSON (backdraft/tower, backdraft/analysis,
// backdraft/sim). Field names follow the Go json tags.

export interface ClientPolicy {
  timeoutMs: number
  maxAttempts: number
  backoffMs: number
  backoffMaxMs: number
  retryOn?: string[]
  propagateDeadline: boolean
  budget?: { ratio: number; minPerSec: number }
  noRetry?: string[]
}

export interface ServerPolicy {
  workers: number
  queueLimit: number
  shedExpired: boolean
  workMs?: number
  requestTimeoutMs?: number
}

export interface ServiceConfig {
  server: ServerPolicy
  clients?: Record<string, ClientPolicy>
  methods?: Record<string, { idempotent: boolean; dedupes?: boolean }>
  fault: { latencyMs: number; errorRate: number }
}

export interface ServiceView {
  name: string
  declared: ServiceConfig
  effective: ServiceConfig
  instances: number
}

export interface Mitigations {
  retryBudgets: boolean
  singleLayerRetries: boolean
  propagateDeadlines: boolean
  shedExpired: boolean
  boundedQueues: boolean
}

export interface ControlEvent {
  t: number
  kind: 'info' | 'config' | 'fault' | 'load'
  service?: string
  text: string
}

export interface PathReport {
  services: string[]
  attempts: number[]
  worst: number
  sustained: number
  budgeted: number
}

export interface State {
  entry: string
  services: ServiceView[]
  load?: { rps: number; mix: Record<string, number> }
  loadReady: boolean
  mitigations: Mitigations
  events: ControlEvent[]
  calibration: { selfSamples: Record<string, number>; shapeSamples: Record<string, number>; opsObserved: boolean }
  capacity: number
  bottleneck: string
  faults: Record<string, boolean>
  paths: PathReport[]
  worstAttempts: Record<string, number>
  topology: Record<string, string[]>
}

export interface ServiceStats {
  arrivals: number
  ok: number
  errors: number
  shed: number
  expired: number
  late: number
  inFlight: number
  queued: number
  workers: number
  p50: number
  p99: number
}

export interface EdgeStats {
  requests: number
  attempts: number
  retries: number
  timeouts: number
  errors: number
  budgetDenied: number
  replayed: number
  p50: number
  p99: number
}

export type Outcome = 'ok' | 'error' | 'timeout' | 'shed' | 'expired' | 'budget'

export interface Particle {
  e: string
  a: number
  o: Outcome
  d: number
  r?: boolean
}

export interface Tick {
  t: number
  entry: { offered: number; goodput: number; failed: number; p50: number; p99: number }
  services: Record<string, ServiceStats>
  edges: Record<string, EdgeStats>
  particles?: Particle[]
  scale: number
}

export type Severity = 'critical' | 'high' | 'medium' | 'low'

export interface Edge {
  from: string
  to: string
}

export interface Finding {
  id: string
  rule: string
  severity: Severity
  title: string
  detail: string
  fix: string
  path?: string[]
  edges?: Edge[]
  metrics?: Record<string, number>
  patch?: Record<string, ServiceConfig>
}

export interface Report {
  findings: Finding[]
  paths: PathReport[]
  worstAttempts: Record<string, number>
  counts: Partial<Record<Severity, number>>
}

export interface TraceSummary {
  id: string
  op: string
  start: number
  durationMs: number
  spans: number
  attempts: number
  retries: number
  late: number
  ok: boolean
}

export interface SpanView {
  id: string
  parent?: string
  kind: 'client' | 'server'
  service: string
  peer?: string
  method: string
  attempt: number
  startUs: number
  durUs: number
  queueUs?: number
  selfUs?: number
  deadlineUs?: number
  code: string
  outcome: Outcome
  late?: boolean
  replayed?: boolean
  faulted?: boolean
}

export interface Verdict {
  kind: 'steady' | 'overloaded' | 'recovered' | 'recovering' | 'metastable' | 'inconclusive'
  drainS?: number
  baselineRatio: number
  finalRatio: number
  minRatio: number
  recoveryS: number
  bottleneck?: string
  bottleneckLoad?: number
  bottleneckCapacity?: number
  saturated?: string[]
  peakQueue: Record<string, number>
  summary: string
}

export interface SimResult {
  stepS: number
  t: number[]
  offered: number[]
  goodput: number[]
  failed: number[]
  services: Record<
    string,
    { arrivals: number[]; completed: number[]; late: number[]; expired: number[]; shed: number[]; queue: number[]; busy: number[]; workers: number }
  >
  edges: Record<string, { attempts: number[]; retries: number[]; timeouts: number[]; budgetDenied: number[] }>
  verdict: Verdict
  events: number
  truncated: boolean
}

export interface Variant {
  name: string
  base?: 'current' | 'declared' | 'mitigated'
  mitigations?: Mitigations
  patch?: Record<string, ServiceConfig>
}

export interface VariantResult {
  name: string
  result: SimResult | null
  wallMs: number
}

export interface SimRunResponse {
  scenario: { durationS: number; faults?: { service: string; startS: number; endS: number; latencyMs: number }[] }
  variants: VariantResult[]
}

export interface MapCell {
  load: number
  duration: number
  kind: Verdict['kind']
  recoveryS: number
  finalRatio: number
  minRatio: number
}

export interface MapResponse {
  cells: MapCell[]
  loads: number[]
  durations: number[]
  service: string
  latencyMs: number
  capacity: number
  bottleneck: string
  wallMs: number
}

export interface HysteresisResponse {
  lo: number
  hi: number
  periodS: number
  capacity: number
  variants: VariantResult[]
}
