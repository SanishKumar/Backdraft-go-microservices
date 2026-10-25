import { useState } from 'react'
import type { SystemPolicy } from '../reviews'
import type { ClientPolicy, ServerPolicy } from '../types'

export function ReviewPolicyEditor({
  policy,
  onChange,
}: {
  policy: SystemPolicy
  onChange: (policy: SystemPolicy) => void
}) {
  const names = Object.keys(policy.services).sort()
  const [selected, setSelected] = useState(names.find((n) => n !== policy.entry) ?? names[0] ?? '')
  const service = names.includes(selected) ? selected : names[0]
  const cfg = policy.services[service]
  if (!cfg || typeof cfg !== 'object' || !cfg.server || typeof cfg.server !== 'object') return null
  const server = (key: keyof ServerPolicy, value: number | boolean) =>
    onChange({
      ...policy,
      services: { ...policy.services, [service]: { ...cfg, server: { ...cfg.server, [key]: value } } },
    })
  const client = (peer: string, changes: Partial<ClientPolicy>) =>
    onChange({
      ...policy,
      services: {
        ...policy.services,
        [service]: { ...cfg, clients: { ...cfg.clients, [peer]: { ...cfg.clients![peer], ...changes } } },
      },
    })
  return (
    <details className="review-edit">
      <summary>Tune candidate controls</summary>
      <label className="review-field">
        Service
        <select value={service} onChange={(e) => setSelected(e.target.value)}>
          {names.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
      </label>
      <div className="review-fields">
        <label className="review-field">
          Workers · 0 is unlimited
          <input
            type="number"
            min="0"
            value={cfg.server.workers ?? 0}
            onChange={(e) => server('workers', e.target.valueAsNumber)}
          />
        </label>
        <label className="review-field">
          Queue limit · 0 is unlimited
          <input
            type="number"
            min="0"
            value={cfg.server.queueLimit ?? 0}
            onChange={(e) => server('queueLimit', e.target.valueAsNumber)}
          />
        </label>
        <label className="review-toggle">
          <input
            type="checkbox"
            checked={!!cfg.server.shedExpired}
            onChange={(e) => server('shedExpired', e.target.checked)}
          />{' '}
          Shed expired queued work
        </label>
      </div>
      {Object.entries(cfg.clients ?? {})
        .filter(([, p]) => p && typeof p === 'object')
        .map(([peer, p]) => (
          <div className="review-edit-hop" key={peer}>
            <h4>
              {service} → {peer}
            </h4>
            <div className="review-fields">
              <label className="review-field">
                Timeout · ms
                <input
                  type="number"
                  min="0"
                  value={p.timeoutMs ?? 0}
                  onChange={(e) => client(peer, { timeoutMs: e.target.valueAsNumber })}
                />
              </label>
              <label className="review-field">
                Maximum attempts
                <input
                  type="number"
                  min="1"
                  max="100"
                  value={p.maxAttempts ?? 1}
                  onChange={(e) => client(peer, { maxAttempts: e.target.valueAsNumber })}
                />
              </label>
              <label className="review-field">
                Base backoff · ms
                <input
                  type="number"
                  min="0"
                  value={p.backoffMs ?? 0}
                  onChange={(e) => client(peer, { backoffMs: e.target.valueAsNumber })}
                />
              </label>
              <label className="review-field">
                Maximum backoff · ms
                <input
                  type="number"
                  min="0"
                  value={p.backoffMaxMs ?? 0}
                  onChange={(e) => client(peer, { backoffMaxMs: e.target.valueAsNumber })}
                />
              </label>
              <label className="review-toggle">
                <input
                  type="checkbox"
                  checked={!!p.propagateDeadline}
                  onChange={(e) => client(peer, { propagateDeadline: e.target.checked })}
                />{' '}
                Propagate deadline
              </label>
              <label className="review-toggle">
                <input
                  type="checkbox"
                  checked={!!p.budget}
                  onChange={(e) =>
                    client(peer, { budget: e.target.checked ? { ratio: 0.1, minPerSec: 1 } : undefined })
                  }
                />{' '}
                Retry budget
              </label>
              {p.budget && (
                <label className="review-field">
                  Retry budget · fraction
                  <input
                    type="number"
                    min="0"
                    max="1"
                    step="0.05"
                    value={p.budget.ratio}
                    onChange={(e) => client(peer, { budget: { ...p.budget!, ratio: e.target.valueAsNumber } })}
                  />
                </label>
              )}
            </div>
          </div>
        ))}
      <p className="review-caption">
        These controls edit the candidate only. Retryable status codes and method exclusions are available in the JSON
        editor.
      </p>
    </details>
  )
}
