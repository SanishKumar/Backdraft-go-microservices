import type {
  HysteresisResponse,
  MapResponse,
  Mitigations,
  ServiceConfig,
  SimRunResponse,
  SpanView,
  State,
  TraceSummary,
  Variant,
} from './types'

export async function call<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as { error?: string }).error ?? `${res.status} ${res.statusText}`)
  return data as T
}

export const api = {
  setLoad: (rps: number, signal?: AbortSignal) => call<{ rps: number }>('PUT', '/api/load', { rps }, signal),
  fault: (service: string, latencyMs: number, durationS: number, errorRate = 0, signal?: AbortSignal) =>
    call<State>('POST', '/api/fault', { service, latencyMs, durationS, errorRate }, signal),
  clearFault: (service: string) => call<State>('POST', '/api/fault', { service, latencyMs: 0, durationS: 0 }),
  mitigations: (m: Mitigations, signal?: AbortSignal) => call<State>('POST', '/api/mitigations', m, signal),
  reset: (signal?: AbortSignal) => call<State>('POST', '/api/reset', undefined, signal),
  patch: (patch: Record<string, ServiceConfig>, reason: string) => call<State>('POST', '/api/patch', { patch, reason }),
  setConfig: (service: string, cfg: ServiceConfig) => call<State>('PUT', `/api/services/${service}/config`, cfg),
  traces: (filter: 'interesting' | 'all', limit = 80) =>
    call<TraceSummary[]>('GET', `/api/traces?filter=${filter}&limit=${limit}`),
  trace: (id: string) => call<{ id: string; spans: SpanView[] }>('GET', `/api/traces/${id}`),
  model: () => call<{ model: unknown; calibration: unknown }>('GET', '/api/model'),
  simRun: (req: {
    rps?: number
    service: string
    latencyMs: number
    faultS: number
    warmS?: number
    observeS?: number
    variants?: Variant[]
  }) => call<SimRunResponse>('POST', '/api/sim/run', req),
  simMap: (req: { service?: string; latencyMs?: number; loads?: number[]; durations?: number[]; variant?: Variant }) =>
    call<MapResponse>('POST', '/api/sim/map', req),
  hysteresis: (req: { variants?: Variant[] }) => call<HysteresisResponse>('POST', '/api/sim/hysteresis', req),
}
