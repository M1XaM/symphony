import { useEffect, useState } from 'react'
import { useSymphonyStore } from '../store/useSymphonyStore'
import type { ContainerRow } from '../types/topology'

/** How often the sweep repeats — `watch` itself defaults to two seconds. */
const REFRESH_MS = 1000

/** "3 minutes ago", the way the CLI's CREATED column reads. */
function since(ts: number): string {
  const s = Math.max(0, Math.round((Date.now() - ts) / 1000))
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

function Row({ c }: { c: ContainerRow }) {
  return (
    <tr className={`ps-row ps-${c.state}${c.ours ? ' is-ours' : ''}`}>
      <td className="ps-id">{c.id}</td>
      <td className="ps-name" title={c.name}>
        {c.name}
        {c.ours ? <span className="ps-mine" title="started by Symphony">●</span> : null}
      </td>
      <td className="ps-image" title={c.image}>
        {c.image}
      </td>
      <td className="ps-created">{c.created_at ? since(c.created_at) : '—'}</td>
      <td className="ps-status" title={c.command ? `${c.status} · ${c.command}` : c.status}>
        <span className={`ps-state ps-state-${c.state}`}>{c.state}</span> {c.status}
      </td>
      <td className="ps-ports" title={c.ports.join(', ')}>
        {c.ports.length ? c.ports.join(', ') : '—'}
      </td>
    </tr>
  )
}

/** Live `docker ps`: the daemon's own view, swept on a timer while the tab
 *  is open. Containers this app started are marked, everything else on the
 *  machine is listed just as the terminal would list it. */
export function ContainersView() {
  const containers = useSymphonyStore((s) => s.containers)
  const running = useSymphonyStore((s) => s.containersRunning)
  const all = useSymphonyStore((s) => s.containersAll)
  const error = useSymphonyStore((s) => s.containersError)
  const runtime = useSymphonyStore((s) => s.runtime)
  const refresh = useSymphonyStore((s) => s.refreshContainers)
  const setAll = useSymphonyStore((s) => s.setContainersAll)
  const [paused, setPaused] = useState(false)

  // A hidden window has nobody reading the table, so it does not ask the
  // daemon; coming back to it refreshes at once rather than up to a tick late.
  useEffect(() => {
    if (paused) return
    const tick = () => {
      if (!document.hidden) void refresh()
    }
    tick()
    const id = window.setInterval(tick, REFRESH_MS)
    document.addEventListener('visibilitychange', tick)
    return () => {
      window.clearInterval(id)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [refresh, paused, all])

  return (
    <div className="ps">
      <div className="log-actions">
        <span className="muted">
          {runtime?.kind === 'docker' ? 'docker ps' : 'simulated containers'} · {running} running
          {all ? ` · ${containers.length} total` : ''}
          {paused ? ' · paused' : ` · every ${REFRESH_MS / 1000}s`}
        </span>
        <span className="ps-actions">
          <label className="ps-check">
            <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> show stopped
          </label>
          <button type="button" className="btn btn-ghost" onClick={() => setPaused((p) => !p)}>
            {paused ? 'resume' : 'pause'}
          </button>
          <button type="button" className="btn btn-ghost" onClick={() => void refresh()}>
            refresh
          </button>
        </span>
      </div>
      <div className="ps-scroll">
        {error ? <div className="ps-empty log-error">{error}</div> : null}
        {!error && containers.length === 0 ? (
          <div className="ps-empty muted">no containers{all ? '' : ' running'}</div>
        ) : null}
        {containers.length > 0 ? (
          <table className="ps-table">
            <thead>
              <tr>
                <th>container id</th>
                <th>name</th>
                <th>image</th>
                <th>created</th>
                <th>status</th>
                <th>ports</th>
              </tr>
            </thead>
            <tbody>
              {containers.map((c) => (
                <Row key={c.id || c.name} c={c} />
              ))}
            </tbody>
          </table>
        ) : null}
      </div>
    </div>
  )
}
