import { useEffect, useMemo, useRef } from 'react'
import { fmtTime } from '../lib/format'
import { selectIsApplied, useSymphonyStore } from '../store/useSymphonyStore'

/** Container logs of one node of the canvas topology, refreshed while open. */
export function LogsView() {
  // joined into one string so a node being dragged does not re-render the tab
  const ids = useSymphonyStore((s) => s.topology.nodes.map((n) => n.id).join('\n'))
  const nodeIds = useMemo(() => (ids ? ids.split('\n') : []), [ids])
  const applied = useSymphonyStore(selectIsApplied)
  const logsNode = useSymphonyStore((s) => s.logsNode)
  const lines = useSymphonyStore((s) => s.nodeLogs)
  const setLogsNode = useSymphonyStore((s) => s.setLogsNode)
  const refreshLogs = useSymphonyStore((s) => s.refreshLogs)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!applied || !logsNode) return
    // idem: no polling behind a hidden window
    const tick = () => {
      if (!document.hidden) void refreshLogs()
    }
    const id = window.setInterval(tick, 2000)
    document.addEventListener('visibilitychange', tick)
    return () => {
      window.clearInterval(id)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [applied, logsNode, refreshLogs])

  useEffect(() => {
    const el = ref.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])

  useEffect(() => {
    if (!logsNode && nodeIds.length > 0 && applied) setLogsNode(nodeIds[0])
  }, [logsNode, nodeIds, applied, setLogsNode])

  return (
    <div className="log">
      <div className="log-actions">
        <span className="muted">
          container logs ·{' '}
          <select className="input input-inline" value={logsNode ?? ''} onChange={(e) => setLogsNode(e.target.value || null)} disabled={!applied}>
            {nodeIds.map((id) => (
              <option key={id} value={id}>
                {id}
              </option>
            ))}
          </select>
        </span>
        <button type="button" className="btn btn-ghost" onClick={() => void refreshLogs()} disabled={!applied}>
          refresh
        </button>
      </div>
      <div className="log-lines" ref={ref}>
        {!applied ? <div className="muted">apply the topology to read its containers' logs</div> : null}
        {applied && lines.length === 0 ? <div className="muted">nothing logged yet</div> : null}
        {lines.map((l, i) => (
          <div key={i} className={`log-line log-${l.level.toLowerCase()}`}>
            <span className="log-ts">{fmtTime(l.ts)}</span>
            <span className="log-src">{String(l.attrs?.container ?? '')}</span>
            <span className="log-msg">
              {l.msg}
              {l.attrs
                ? Object.entries(l.attrs)
                    .filter(([k]) => !['container', 'node', 'kind', 'replica'].includes(k))
                    .map(([k, v]) => ` ${k}=${typeof v === 'object' ? JSON.stringify(v) : String(v)}`)
                    .join('')
                : ''}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}
