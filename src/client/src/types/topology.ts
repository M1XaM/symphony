// TypeScript mirror of internal/spec, internal/planner and internal/engine.
//
// The Wails generator emits classes in ../../wailsjs/go/models.ts; those are
// used at the binding boundary (see lib/backend.ts). UI state uses the plain
// interfaces below so it can be spread, diffed and stored without class
// instances. Keep field names identical to the Go json tags.
import { planner, spec } from '../../wailsjs/go/models'

export type NodeKind = 'service' | 'broker' | 'db' | 'proxy' | 'infra'
export const NODE_KINDS: readonly NodeKind[] = ['service', 'broker', 'db', 'proxy', 'infra']

export type EdgeKind = 'publishes' | 'subscribes' | 'calls' | 'uses'

export type ConfigValue = number | string | boolean

export interface Position {
  x: number
  y: number
}

export interface Config {
  hot: Record<string, ConfigValue>
  cold: Record<string, ConfigValue>
}

export interface TopologyNode {
  id: string
  kind: NodeKind
  image: string
  replicas: number
  position: Position
  config: Config
}

export interface TopologyEdge {
  id: string
  from: string
  to: string
  kind: EdgeKind
  topic?: string
  via?: string
}

export interface Topology {
  version: number
  name: string
  nodes: TopologyNode[]
  edges: TopologyEdge[]
}

export const EMPTY_TOPOLOGY: Topology = { version: 1, name: 'untitled', nodes: [], edges: [] }

// --- runtime state ---------------------------------------------------------

export type NodeState = 'pending' | 'starting' | 'healthy' | 'failed' | 'stopped' | 'paused' | 'removed'

/** Mirrors engine.NodeStatus (also the payload of the `node:status` event). */
export interface NodeStatus {
  node_id: string
  state: NodeState
  replicas: number
  ready: number
  message?: string
  updated_at: number
}

export type Phase = 'idle' | 'validating' | 'planning' | 'applying' | 'running' | 'tearing_down' | 'failed'

/** Mirrors core.DeploymentInfo: one applied topology, keyed by its name. */
export interface DeploymentInfo {
  name: string
  phase: Phase
  network: string
  topology: Topology
  statuses: Record<string, NodeStatus>
}

// --- planner ---------------------------------------------------------------

export type OpKind = 'ensure_network' | 'create' | 'wait_healthy' | 'ensure_topic' | 'control' | 'destroy'
export type ChangeClass = 'hot' | 'cold' | 'structural'
export type Plan = planner.Plan
export type PlanOperation = planner.Operation
export type OpStatus = 'started' | 'done' | 'failed'

// --- validation / faults ---------------------------------------------------

export type ValidationError = spec.ValidationError

export type FaultKind = 'latency' | 'drop' | 'kill' | 'pause' | 'partition' | 'cpu_stress'
export interface FaultSpec {
  kind: FaultKind
  duration_ms: number
  params: Record<string, ConfigValue>
}

// --- event payloads (mirror internal/events) --------------------------------

export interface ProgressEvent {
  deployment: string
  op_id: string
  index: number
  count: number
  kind: OpKind
  target: string
  class: ChangeClass
  status: OpStatus
  message?: string
  elapsed_ms: number
}

export interface LogEvent {
  level: 'debug' | 'info' | 'warn' | 'error'
  source: string
  message: string
  ts: number
}

/** `gone` is only ever received: the deployment was torn down. */
export interface PhaseEvent {
  deployment: string
  phase: Phase | 'gone'
  message?: string
}

/** Payload of `node:status`: a NodeStatus tagged with its deployment. */
export interface NodeStatusEvent extends NodeStatus {
  deployment: string
}

export type LinkLevel = 'ok' | 'warn' | 'crit'

export interface LinkHealthEntry {
  error_rate: number
  level: LinkLevel
}

export interface LinkHealthEvent {
  deployment: string
  ts: number
  links: Record<string, LinkHealthEntry>
}

export interface HostStatsEvent {
  ts: number
  cpu_pct: number
  mem_pct: number
  mem_used_bytes: number
  mem_total_bytes: number
  net_rx_bps: number
  net_tx_bps: number
}

export interface MetricsEvent {
  ts: number
  latency_ms: number
  p99_ms: number
  consumer_lag: number
  sagas_per_sec: number
  cpu_pct: number
  active_faults: number
}

export interface FaultEvent {
  deployment: string
  node_id: string
  kind: FaultKind
  duration_ms: number
  active: boolean
}

// --- boundary conversions --------------------------------------------------

/** Build the generated class instance the binding expects. */
export function toWireTopology(t: Topology): spec.Topology {
  return spec.Topology.createFrom(JSON.parse(JSON.stringify(t)))
}

/** Strip class instances coming back from the binding into plain data. */
export function fromWireTopology(t: spec.Topology | Topology): Topology {
  const plain = JSON.parse(JSON.stringify(t)) as Topology
  plain.nodes = plain.nodes ?? []
  plain.edges = plain.edges ?? []
  for (const n of plain.nodes) {
    n.config = n.config ?? { hot: {}, cold: {} }
    n.config.hot = n.config.hot ?? {}
    n.config.cold = n.config.cold ?? {}
  }
  return plain
}

export function toWireFault(f: FaultSpec): spec.FaultSpec {
  return spec.FaultSpec.createFrom(f)
}
