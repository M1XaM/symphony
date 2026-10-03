import { fmtNum } from '../../lib/format'
import { selectDockerUp, selectIsApplied, selectLinks, useSymphonyStore } from '../../store/useSymphonyStore'
import type { FaultSpec, TopologyEdge } from '../../types/topology'
import { StatusDot } from '../StatusDot'
import { NumberInput } from '../ui/NumberInput'
import { EDGE_FAULTS } from './faults'

export function EdgeInspector({ edge }: { edge: TopologyEdge }) {
  const health = useSymphonyStore((s) => selectLinks(s)[edge.id])
  const applied = useSymphonyStore(selectIsApplied)
  const dockerUp = useSymphonyStore(selectDockerUp)
  const updateEdge = useSymphonyStore((s) => s.updateEdge)
  const removeEdges = useSymphonyStore((s) => s.removeEdges)
  const injectFault = useSymphonyStore((s) => s.injectFault)
  const clearFaults = useSymphonyStore((s) => s.clearFaults)
  const canControl = applied && dockerUp

  const scoped = (f: FaultSpec): FaultSpec => ({ ...f, params: { ...f.params, edge: edge.id, direction: 'out' } })

  return (
    <div className="inspector-node">
      <div className="pane-title">
        <span className="kind-chip kind-edge">{edge.kind}</span>
        {health ? <StatusDot state={health.level === 'ok' ? 'healthy' : health.level === 'warn' ? 'degraded' : 'failed'} /> : <StatusDot />}
        <span className="muted small">{health ? `${fmtNum(health.error_rate * 100, 1)}% lost` : 'not running'}</span>
      </div>
      <div className="field">
        <label className="field-label">link</label>
        <div className="mono field-static">
          {edge.from} → {edge.to}
        </div>
        <div className="muted small mono">{edge.id}</div>
      </div>
      {health ? (
        <div className="field muted small">
          {fmtNum(health.rps, 1)} req/s · p99 {fmtNum(health.p99_ms, 0)} ms · {fmtNum(health.error_rate * 100, 1)}% failed or lost
        </div>
      ) : null}
      {edge.kind === 'publishes' || edge.kind === 'subscribes' ? (
        <div className="field">
          <label className="field-label">
            topic <span className="tag tag-cold">requires restart</span>
          </label>
          <input className="input mono" value={edge.topic ?? ''} spellCheck={false} onChange={(e) => updateEdge(edge.id, { topic: e.target.value })} />
        </div>
      ) : null}
      <div className="field">
        <label className="field-label">
          timeout_ms <span className="tag tag-cold">requires restart</span>
        </label>
        <NumberInput
          className="input-num"
          min={0}
          placeholder="node default"
          value={edge.timeout_ms ?? ''}
          onValue={(n) => updateEdge(edge.id, { timeout_ms: Math.max(0, n || 0) })}
        />
      </div>
      <div className="field">
        <label className="field-label">
          retries <span className="tag tag-cold">requires restart</span>
        </label>
        <NumberInput
          className="input-num"
          min={0}
          placeholder="node default"
          value={edge.retries ?? ''}
          onValue={(n) => updateEdge(edge.id, { retries: Math.max(0, n || 0) })}
        />
      </div>

      <div className="section-title">Faults on this link</div>
      <div className="muted small">injected on the sender ({edge.from}), 10 s each</div>
      <div className="fault-grid">
        {EDGE_FAULTS.map((f) => (
          <button key={f.label} type="button" className="btn btn-small" disabled={!canControl} title={f.title} onClick={() => injectFault(edge.from, scoped(f.fault))}>
            {f.label}
          </button>
        ))}
        <button
          type="button"
          className="btn btn-small"
          disabled={!canControl}
          title="refuse traffic to the receiver only"
          onClick={() => injectFault(edge.from, { kind: 'partition', duration_ms: 10000, params: { peers: [edge.to] } })}
        >
          partition to {edge.to}
        </button>
        <button type="button" className="btn btn-small" disabled={!canControl} onClick={() => clearFaults(edge.from)}>
          clear on {edge.from}
        </button>
      </div>

      <div className="inspector-actions">
        <button type="button" className="btn btn-danger btn-small" onClick={() => removeEdges([edge.id])}>
          remove edge
        </button>
      </div>
    </div>
  )
}
