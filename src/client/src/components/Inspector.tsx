import { memo, useEffect, useState } from 'react'
import { KIND_LABEL, fmtBytes, fmtNum } from '../lib/format'
import {
  selectContainers,
  selectHasGateway,
  selectIsApplied,
  selectLinks,
  selectSelectedEdge,
  selectSelectedNode,
  selectStatuses,
  useSymphonyStore,
} from '../store/useSymphonyStore'
import type { FaultSpec, HealthCheck, TopologyEdge, TopologyNode } from '../types/topology'
import { ConfigField } from './HotField'
import { MetricTiles } from './MetricTiles'
import { StatusDot } from './StatusDot'

interface FaultButton {
  label: string
  title: string
  fault: FaultSpec
}

/** Faults that hit the whole node. Container-level ones go through Docker,
 *  the rest through the block's /control. */
const NODE_FAULTS: { group: string; items: FaultButton[] }[] = [
  {
    group: 'container',
    items: [
      { label: 'kill 5s', title: 'SIGKILL the container, start it again after 5 s', fault: { kind: 'kill', duration_ms: 5000, params: {} } },
      { label: 'pause 5s', title: 'freeze the process for 5 s', fault: { kind: 'pause', duration_ms: 5000, params: {} } },
      { label: 'partition 10s', title: 'disconnect the container from the network for 10 s', fault: { kind: 'partition', duration_ms: 10000, params: {} } },
    ],
  },
  {
    group: 'inside',
    items: [
      { label: 'burn 1 cpu 10s', title: 'busy-loop one core inside the container', fault: { kind: 'cpu_stress', duration_ms: 10000, params: { cores: 1 } } },
      { label: '+200 MB 10s', title: 'allocate and touch 200 MB inside the container', fault: { kind: 'memory_pressure', duration_ms: 10000, params: { mb: 200 } } },
      { label: 'stall 5s', title: 'freeze the data path; the admin port keeps answering', fault: { kind: 'stall', duration_ms: 5000, params: {} } },
      { label: 'crash', title: 'the process exits (restart it with kill 0s or re-apply)', fault: { kind: 'crash', duration_ms: 0, params: { exit_code: 137 } } },
    ],
  },
  {
    group: 'all links',
    items: [
      { label: '+250 ms 10s', title: 'delay every request in and out', fault: { kind: 'latency', duration_ms: 10000, params: { ms: 250, jitter_ms: 50 } } },
      { label: 'drop 30% 10s', title: 'swallow 30 % of requests', fault: { kind: 'drop', duration_ms: 10000, params: { rate: 0.3 } } },
      { label: 'error 30% 10s', title: 'answer 30 % of requests with 503', fault: { kind: 'error', duration_ms: 10000, params: { rate: 0.3, status: 503 } } },
      { label: 'timeout 30% 10s', title: 'hang on 30 % of requests until the caller gives up', fault: { kind: 'timeout', duration_ms: 10000, params: { rate: 0.3 } } },
    ],
  },
]

/** Faults scoped to one edge, injected on the sender. */
const EDGE_FAULTS: FaultButton[] = [
  { label: '+300 ms', title: 'delay on this link for 10 s', fault: { kind: 'latency', duration_ms: 10000, params: { ms: 300, jitter_ms: 50 } } },
  { label: 'drop 50%', title: 'drop half the requests on this link for 10 s', fault: { kind: 'drop', duration_ms: 10000, params: { rate: 0.5 } } },
  { label: 'drop 100%', title: 'cut the link for 10 s', fault: { kind: 'drop', duration_ms: 10000, params: { rate: 1 } } },
  { label: 'error 50%', title: '503 on half the requests for 10 s', fault: { kind: 'error', duration_ms: 10000, params: { rate: 0.5 } } },
  { label: 'timeout 30%', title: 'hang 30 % of requests for 10 s', fault: { kind: 'timeout', duration_ms: 10000, params: { rate: 0.3 } } },
  { label: 'duplicate 30%', title: 'deliver 30 % of requests twice for 10 s', fault: { kind: 'duplicate', duration_ms: 10000, params: { rate: 0.3 } } },
  { label: 'slow 2 KB/s', title: 'throttle responses for 10 s', fault: { kind: 'slow', duration_ms: 10000, params: { bytes_per_sec: 2048 } } },
]

function detailText(d?: Record<string, unknown>): string {
  if (!d) return ''
  return Object.entries(d)
    .filter(([, v]) => typeof v !== 'object' || v === null)
    .map(([k, v]) => `${k}=${typeof v === 'number' ? fmtNum(v, Number.isInteger(v) ? 0 : 2) : String(v)}`)
    .join(' · ')
}

function HealthChecks({ checks }: { checks?: HealthCheck[] }) {
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
        <input
          type="number"
          className="input input-num"
          min={0}
          step={step}
          value={value}
          onChange={(e) => {
            const n = Number(e.target.value)
            if (!Number.isNaN(n) && n >= 0) onChange(n)
          }}
        />
      </div>
    </div>
  )
}

/** Load generator controls, shown on the gateway node. */
function LoadPanel({ node }: { node: TopologyNode }) {
  const control = useSymphonyStore((s) => s.control)
  const applied = useSymphonyStore(selectIsApplied)
  const [rps, setRps] = useState(50)
  const [pattern, setPattern] = useState('constant')
  const [duration, setDuration] = useState(60)
  const [status, setStatus] = useState<Record<string, unknown> | null>(null)

  useEffect(() => {
    if (!applied) return
    let stop = false
    const tick = async () => {
      const res = await control(node.id, { op: 'load.status' })
      if (!stop && res?.state && typeof res.state === 'object') setStatus(res.state as Record<string, unknown>)
    }
    void tick()
    const id = window.setInterval(tick, 2000)
    return () => {
      stop = true
      window.clearInterval(id)
    }
  }, [applied, control, node.id])

  const running = status?.running === true
  return (
    <>
      <div className="section-title">Load generator</div>
      <div className="field">
        <div className="load-row">
          <label className="field-label">rps</label>
          <input className="input input-num" type="number" min={1} value={rps} onChange={(e) => setRps(Math.max(1, Number(e.target.value) || 1))} />
          <label className="field-label">pattern</label>
          <select className="input" value={pattern} onChange={(e) => setPattern(e.target.value)}>
            {['constant', 'ramp', 'burst', 'sine', 'step'].map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
          <label className="field-label">seconds</label>
          <input className="input input-num" type="number" min={0} value={duration} onChange={(e) => setDuration(Math.max(0, Number(e.target.value) || 0))} />
        </div>
        <div className="load-actions">
          <button
            type="button"
            className="btn btn-accent btn-small"
            disabled={!applied}
            onClick={() =>
              void control(node.id, {
                op: 'load.set',
                params: { rps, pattern, to_rps: rps * 8, factor: 4, duration_ms: duration * 1000, concurrency: 128 },
              })
            }
          >
            {running ? 'restart' : 'start'}
          </button>
          <button type="button" className="btn btn-small" disabled={!applied || !running} onClick={() => void control(node.id, { op: 'load.stop' })}>
            stop
          </button>
          <span className="muted small">
            {running
              ? `running · target ${fmtNum(status?.target_rps as number, 0)} rps · achieved ${fmtNum(status?.achieved_rps as number, 0)} · sent ${String(status?.sent ?? 0)}`
              : applied
                ? 'idle'
                : 'apply the topology first'}
          </span>
        </div>
      </div>
    </>
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

const NodeInspector = memo(function NodeInspector({ node }: { node: TopologyNode }) {
  const status = useSymphonyStore((s) => selectStatuses(s)[node.id])
  const stats = useSymphonyStore((s) => selectContainers(s)[node.id])
  const applied = useSymphonyStore(selectIsApplied)
  const runtime = useSymphonyStore((s) => s.runtime)
  const updateNode = useSymphonyStore((s) => s.updateNode)
  const setHotConfig = useSymphonyStore((s) => s.setHotConfig)
  const setColdConfig = useSymphonyStore((s) => s.setColdConfig)
  const setResources = useSymphonyStore((s) => s.setResources)
  const removeNodes = useSymphonyStore((s) => s.removeNodes)
  const injectFault = useSymphonyStore((s) => s.injectFault)
  const clearFaults = useSymphonyStore((s) => s.clearFaults)
  const setLogsNode = useSymphonyStore((s) => s.setLogsNode)
  const setDrawer = useSymphonyStore((s) => s.setDrawer)

  const hotKeys = Object.keys(node.config.hot).sort()
  const coldKeys = Object.keys(node.config.cold).sort()
  const running = applied && status !== undefined && status.state !== 'removed'
  const canControl = running && runtime?.kind === 'docker'

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
            onClick={() => {
              setLogsNode(node.id)
              setDrawer(true, 'logs')
            }}
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
        <input
          className="input input-num"
          type="number"
          min={1}
          max={16}
          value={node.replicas}
          onChange={(e) => updateNode(node.id, { replicas: Math.max(1, Number(e.target.value) || 1) })}
        />
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
                disabled={!running || (g.group !== 'container' && !canControl)}
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
        <button type="button" className="btn btn-small" disabled={!running} onClick={() => clearFaults(node.id)}>
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

function EdgeInspector({ edge }: { edge: TopologyEdge }) {
  const health = useSymphonyStore((s) => selectLinks(s)[edge.id])
  const applied = useSymphonyStore(selectIsApplied)
  const runtime = useSymphonyStore((s) => s.runtime)
  const updateEdge = useSymphonyStore((s) => s.updateEdge)
  const removeEdges = useSymphonyStore((s) => s.removeEdges)
  const injectFault = useSymphonyStore((s) => s.injectFault)
  const clearFaults = useSymphonyStore((s) => s.clearFaults)
  const canControl = applied && runtime?.kind === 'docker'

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
        <input
          className="input input-num"
          type="number"
          min={0}
          placeholder="node default"
          value={edge.timeout_ms ?? ''}
          onChange={(e) => updateEdge(edge.id, { timeout_ms: Math.max(0, Number(e.target.value) || 0) })}
        />
      </div>
      <div className="field">
        <label className="field-label">
          retries <span className="tag tag-cold">requires restart</span>
        </label>
        <input
          className="input input-num"
          type="number"
          min={0}
          placeholder="node default"
          value={edge.retries ?? ''}
          onChange={(e) => updateEdge(edge.id, { retries: Math.max(0, Number(e.target.value) || 0) })}
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
        <button type="button" className="btn btn-small" disabled={!applied} onClick={() => clearFaults(edge.from)}>
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

export function Inspector() {
  const node = useSymphonyStore(selectSelectedNode)
  const edge = useSymphonyStore(selectSelectedEdge)
  const hasGateway = useSymphonyStore(selectHasGateway)
  return (
    <div className="inspector">
      <div className="inspector-main">
        {node ? <NodeInspector node={node} /> : edge ? <EdgeInspector edge={edge} /> : <MetricTiles />}
        {!node && !edge && hasGateway ? (
          <div className="summary-hint muted small">select the gateway to drive load, an edge to break a link</div>
        ) : null}
      </div>
    </div>
  )
}
