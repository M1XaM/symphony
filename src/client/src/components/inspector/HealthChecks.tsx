import { useState } from 'react'
import { fmtNum } from '../../lib/format'
import type { HealthCheck } from '../../types/topology'
import { StatusDot } from '../StatusDot'

function detailText(d?: Record<string, unknown>): string {
  if (!d) return ''
  return Object.entries(d)
    .filter(([, v]) => typeof v !== 'object' || v === null)
    .map(([k, v]) => `${k}=${typeof v === 'number' ? fmtNum(v, Number.isInteger(v) ? 0 : 2) : String(v)}`)
    .join(' · ')
}

/** A node's deep health: the checks that are not healthy, or all on request. */
export function HealthChecks({ checks }: { checks?: HealthCheck[] }) {
  const [open, setOpen] = useState(false)
  if (!checks || checks.length === 0) return <div className="muted small">no deep health yet</div>
  const bad = checks.filter((c) => c.status !== 'healthy')
  const shown = open ? checks : bad.length ? bad : checks.slice(0, 4)
  return (
    <div className="checks">
      {shown.map((c) => (
        <div key={c.name} className={`check check-${c.status}`} title={JSON.stringify(c.detail ?? {}, null, 1)}>
          <StatusDot state={c.status === 'failing' ? 'failed' : c.status} />
          <span className="check-name mono">{c.name}</span>
          <span className="check-detail muted">{detailText(c.detail)}</span>
        </div>
      ))}
      {checks.length > shown.length || open ? (
        <button type="button" className="btn btn-ghost btn-small" onClick={() => setOpen(!open)}>
          {open ? 'fewer' : `all ${checks.length} checks`}
        </button>
      ) : null}
    </div>
  )
}
