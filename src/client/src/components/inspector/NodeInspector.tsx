import { memo } from 'react'
import { KIND_LABEL, fmtBytes, fmtNum } from '../../lib/format'
import { selectContainers, selectDockerUp, selectIsApplied, selectStatuses, useSymphonyStore } from '../../store/useSymphonyStore'
import type { TopologyNode } from '../../types/topology'
import { ConfigField } from '../HotField'
import { StatusDot } from '../StatusDot'
import { NumberInput } from '../ui/NumberInput'
import { HealthChecks } from './HealthChecks'
import { LoadPanel } from './LoadPanel'
import { NODE_FAULTS } from './faults'

function ResourceSlider({
  label,
  value,
  min,
  max,
  step,
  unit,
  onChange,
}: {
  label: string
  value: number
  min: number
  max: number
  step: number
  unit: string
  onChange(v: number): void
}) {
  return (
    <div className="field">
      <label className="field-label">
        <span className="mono">{label}</span>
        <span className="tag tag-hot">live</span>
        <span className="toolbar-spacer" />
        <span className="muted">{value === 0 ? 'unlimited' : `${value} ${unit}`}</span>
      </label>
      <div className="field-num">
        <input type="range" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} />
        <NumberInput
          className="input-num"
          min={0}
          step={step}
          value={value}
          onValue={(n) => {
            if (!Number.isNaN(n) && n >= 0) onChange(n)
          }}
        />
      </div>
    </div>
  )
}

/** Everything the inspector shows of a node except where it sits: dragging
 *  the selected node replaces its object on every frame, and the form does
 *  not need to be redrawn for that. */
function sameExceptPosition(a: { node: TopologyNode }, b: { node: TopologyNode }): boolean {
  const x = a.node
  const y = b.node
  return (
    x === y ||
    (x.id === y.id &&
      x.kind === y.kind &&
      x.image === y.image &&
      x.replicas === y.replicas &&
      x.config === y.config &&
      x.resources === y.resources)
  )
}

export const NodeInspector = memo(function NodeInspector({ node }: { node: TopologyNode }) {
  const status = useSymphonyStore((s) => selectStatuses(s)[node.id])
  const stats = useSymphonyStore((s) => selectContainers(s)[node.id])
  const applied = useSymphonyStore(selectIsApplied)
  const dockerUp = useSymphonyStore(selectDockerUp)
  const updateNode = useSymphonyStore((s) => s.updateNode)
  const setHotConfig = useSymphonyStore((s) => s.setHotConfig)
  const setColdConfig = useSymphonyStore((s) => s.setColdConfig)
  const setResources = useSymphonyStore((s) => s.setResources)
  const removeNodes = useSymphonyStore((s) => s.removeNodes)
  const injectFault = useSymphonyStore((s) => s.injectFault)
  const clearFaults = useSymphonyStore((s) => s.clearFaults)
  const showNodeLogs = useSymphonyStore((s) => s.showNodeLogs)

  const hotKeys = Object.keys(node.config.hot).sort()
  const coldKeys = Object.keys(node.config.cold).sort()
  const running = applied && status !== undefined && status.state !== 'removed'
  const canControl = running && dockerUp

  return (
    <div className="inspector-node">
      <div className="pane-title">
        <span className={`kind-chip kind-${node.kind}`}>{KIND_LABEL[node.kind]}</span>
        <StatusDot state={status?.state} />
        <span className="muted small">{status ? `${status.state} ${status.ready}/${status.replicas}` : 'not running'}</span>
        <span className="toolbar-spacer" />
        {running ? (
          <button
            type="button"
            className="btn btn-ghost btn-small"
            onClick={() => void showNodeLogs(node.id)}
          >
            logs
          </button>
        ) : null}
      </div>
      {status?.message ? <div className="field muted small">{status.message}</div> : null}

      <div className="field">
        <label className="field-label">id</label>
        <div className="mono field-static">{node.id}</div>
      </div>
      <div className="field">
        <label className="field-label">
          image <span className="tag tag-cold">requires restart</span>
        </label>
        <input className="input mono" value={node.image} spellCheck={false} onChange={(e) => updateNode(node.id, { image: e.target.value })} />
      </div>
      <div className="field">
        <label className="field-label">
          replicas <span className="tag tag-cold">requires restart</span>
        </label>
        <NumberInput className="input-num" min={1} max={16} value={node.replicas} onValue={(n) => updateNode(node.id, { replicas: Math.max(1, n || 1) })} />
      </div>

      {running ? (
        <>
          <div className="section-title">Health</div>
          <HealthChecks checks={status?.checks} />
        </>
      ) : null}

      <div className="section-title">Resources</div>
      <ResourceSlider label="cpus" value={node.resources.cpus ?? 0} min={0} max={4} step={0.05} unit="cores" onChange={(v) => setResources(node.id, { cpus: v })} />
      <ResourceSlider label="memory_mb" value={node.resources.memory_mb ?? 0} min={0} max={2048} step={16} unit="MB" onChange={(v) => setResources(node.id, { memory_mb: v })} />
      {stats ? (
        <div className="field muted small">
          using {fmtNum(stats.cpu_pct, 1)}% cpu · {fmtBytes(stats.mem_used_bytes)}
          {stats.mem_limit_bytes ? ` of ${fmtBytes(stats.mem_limit_bytes)}` : ''} · ↓{fmtBytes(stats.net_rx_bps)}/s ↑{fmtBytes(stats.net_tx_bps)}/s
          {stats.throttled_periods ? ` · throttled ${stats.throttled_periods}×` : ''}
        </div>
      ) : null}

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

      {node.kind === 'infra' ? <LoadPanel node={node} /> : null}

      <div className="section-title">Faults</div>
      {NODE_FAULTS.map((g) => (
        <div key={g.group} className="fault-group">
          <div className="muted small">{g.group}</div>
          <div className="fault-grid">
            {g.items.map((f) => (
              <button
                key={f.label}
                type="button"
                className="btn btn-small"
                disabled={!canControl}
                title={running ? f.title : 'apply the topology first'}
                onClick={() => injectFault(node.id, f.fault)}
              >
                {f.label}
              </button>
            ))}
          </div>
        </div>
      ))}
      <div className="fault-grid">
        <button type="button" className="btn btn-small" disabled={!canControl} onClick={() => clearFaults(node.id)}>
          clear faults
        </button>
      </div>

      <div className="inspector-actions">
        <button type="button" className="btn btn-danger btn-small" onClick={() => removeNodes([node.id])}>
          remove node
        </button>
      </div>
    </div>
  )
}, sameExceptPosition)
