import { useEffect, useRef } from 'react'
import { fmtTime } from '../lib/format'
import { selectIsApplied, useSymphonyStore } from '../store/useSymphonyStore'

/** Container logs of one node of the canvas topology, refreshed while open. */
export function LogsView() {
  const nodes = useSymphonyStore((s) => s.topology.nodes)
  const applied = useSymphonyStore(selectIsApplied)
  const logsNode = useSymphonyStore((s) => s.logsNode)
  const lines = useSymphonyStore((s) => s.nodeLogs)
  const setLogsNode = useSymphonyStore((s) => s.setLogsNode)
  const refreshLogs = useSymphonyStore((s) => s.refreshLogs)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!applied || !logsNode) return
    const id = window.setInterval(() => void refreshLogs(), 2000)
    return () => window.clearInterval(id)
  }, [applied, logsNode, refreshLogs])

  useEffect(() => {
    const el = ref.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])

  useEffect(() => {
    if (!logsNode && nodes.length > 0 && applied) setLogsNode(nodes[0].id)
  }, [logsNode, nodes, applied, setLogsNode])

  return (
    <div className="log">
      <div className="log-actions">
        <span className="muted">
          container logs ·{' '}
          <select className="input input-inline" value={logsNode ?? ''} onChange={(e) => setLogsNode(e.target.value || null)} disabled={!applied}>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.id}
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
