import { KIND_LABEL } from '../lib/format'
import { selectSelectedNode, selectStatuses, useSymphonyStore } from '../store/useSymphonyStore'
import type { FaultSpec, TopologyNode } from '../types/topology'
import { ConfigField } from './HotField'
import { LatencyChart } from './LatencyChart'
import { MetricTiles } from './MetricTiles'
import { StatusDot } from './StatusDot'

const FAULTS: { label: string; fault: FaultSpec }[] = [
  { label: 'latency +250ms / 5s', fault: { kind: 'latency', duration_ms: 5000, params: { ms: 250, jitter_ms: 50 } } },
  { label: 'drop 10% / 5s', fault: { kind: 'drop', duration_ms: 5000, params: { rate: 0.1 } } },
  { label: 'pause 3s', fault: { kind: 'pause', duration_ms: 3000, params: {} } },
  { label: 'kill 3s', fault: { kind: 'kill', duration_ms: 3000, params: {} } },
]

function NodeInspector({ node }: { node: TopologyNode }) {
  const status = useSymphonyStore((s) => selectStatuses(s)[node.id])
  const updateNode = useSymphonyStore((s) => s.updateNode)
  const setHotConfig = useSymphonyStore((s) => s.setHotConfig)
  const setColdConfig = useSymphonyStore((s) => s.setColdConfig)
  const removeNodes = useSymphonyStore((s) => s.removeNodes)
  const injectFault = useSymphonyStore((s) => s.injectFault)

  const hotKeys = Object.keys(node.config.hot).sort()
  const coldKeys = Object.keys(node.config.cold).sort()
  const running = status !== undefined && status.state !== 'removed'

  return (
    <div className="inspector-node">
      <div className="pane-title">
        <span className={`kind-chip kind-${node.kind}`}>{KIND_LABEL[node.kind]}</span>
        <StatusDot state={status?.state} />
        <span className="muted small">{status ? `${status.state} ${status.ready}/${status.replicas}` : 'not running'}</span>
      </div>

      <div className="field">
        <label className="field-label">id</label>
        <div className="mono field-static">{node.id}</div>
      </div>
      <div className="field">
        <label className="field-label">
          image <span className="tag tag-cold">requires restart</span>
        </label>
        <input
          className="input mono"
          value={node.image}
          spellCheck={false}
          onChange={(e) => updateNode(node.id, { image: e.target.value })}
        />
      </div>
      <div className="field">
        <label className="field-label">
          replicas <span className="tag tag-cold">requires restart</span>
        </label>
        <input
          className="input input-num"
          type="number"
          min={1}
          max={16}
          value={node.replicas}
          onChange={(e) => updateNode(node.id, { replicas: Math.max(1, Number(e.target.value) || 1) })}
        />
      </div>

      <div className="section-title">Hot config</div>
      {hotKeys.length === 0 ? <div className="muted small">none</div> : null}
      {hotKeys.map((k) => (
        <ConfigField key={k} name={k} value={node.config.hot[k]} mode="hot" onChange={(v) => setHotConfig(node.id, k, v)} />
      ))}

      <div className="section-title">Cold config</div>
      {coldKeys.length === 0 ? <div className="muted small">none</div> : null}
      {coldKeys.map((k) => (
        <ConfigField key={k} name={k} value={node.config.cold[k]} mode="cold" onChange={(v) => setColdConfig(node.id, k, v)} />
      ))}

      <div className="section-title">Faults</div>
      <div className="fault-grid">
        {FAULTS.map((f) => (
          <button
            key={f.label}
            type="button"
            className="btn btn-small"
            disabled={!running}
            title={running ? `InjectFault(${node.id}, ${f.fault.kind})` : 'apply the topology first'}
            onClick={() => injectFault(node.id, f.fault)}
          >
            {f.label}
          </button>
        ))}
      </div>

      <div className="inspector-actions">
        <button type="button" className="btn btn-danger btn-small" onClick={() => removeNodes([node.id])}>
          remove node
        </button>
      </div>
    </div>
  )
}

export function Inspector() {
  const node = useSymphonyStore(selectSelectedNode)
  return (
    <div className="inspector">
      <div className="inspector-main">{node ? <NodeInspector node={node} /> : <MetricTiles />}</div>
      <div className="inspector-chart">
        <LatencyChart />
      </div>
    </div>
  )
}
