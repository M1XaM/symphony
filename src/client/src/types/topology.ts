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

/** Container limits; 0 = unlimited. Hot: applied to a running container in place. */
export interface Resources {
  cpus?: number
  memory_mb?: number
  pids_max?: number
}

export interface TopologyNode {
  id: string
  kind: NodeKind
  image: string
  replicas: number
  position: Position
  config: Config
  resources: Resources
  /** behaviour program (blocks/common/behaviour); absent = derived from links */
  behaviour?: unknown
}

export interface TopologyEdge {
  id: string
  from: string
  to: string
  kind: EdgeKind
  topic?: string
  via?: string
  timeout_ms?: number
  retries?: number
}

export interface Topology {
  version: number
  name: string
  nodes: TopologyNode[]
  edges: TopologyEdge[]
}

export const SCHEMA_VERSION = 2
export const EMPTY_TOPOLOGY: Topology = { version: SCHEMA_VERSION, name: 'untitled', nodes: [], edges: [] }

/** Mirrors spec.DefaultImage / DefaultConfig / DefaultResources so a node
 *  dropped on the canvas is exactly what the engine would build. */
export const DEFAULT_IMAGE: Record<NodeKind, string> = {
  service: 'symphony/service:dev',
  broker: 'symphony/broker:dev',
  db: 'symphony/database:dev',
  proxy: 'symphony/proxy:dev',
  infra: 'symphony/gateway:dev',
}

export const DEFAULT_RESOURCES: Record<NodeKind, Resources> = {
  service: { cpus: 0.5, memory_mb: 256 },
  broker: { cpus: 1, memory_mb: 512 },
  db: { cpus: 1, memory_mb: 512 },
  proxy: { cpus: 0.25, memory_mb: 128 },
  infra: { cpus: 0.5, memory_mb: 128 },
}

export function defaultConfig(kind: NodeKind): Config {
  switch (kind) {
    case 'service':
      return {
        hot: { timeout_ms: 800, retry_max: 3, breaker_threshold: 5, cpu_ms: 2, io_ms: 0, log_level: 'info' },
        cold: { workers: 32, queue: 64 },
      }
    case 'broker':
      return { hot: { log_retention_ms: 600000, max_poll_records: 500, delivery_delay_ms: 0 }, cold: { num_partitions: 3 } }
    case 'db':
      return {
        hot: { statement_timeout_ms: 5000, log_min_duration_ms: 250, read_ms: 1, write_ms: 2, fsync_ms: 3 },
        cold: { max_connections: 100, max_rows: 1000000 },
      }
    case 'proxy':
      return { hot: { timeout_ms: 2000, log_level: 'info' }, cold: {} }
    case 'infra':
      return { hot: { rate_limit_rps: 0, saga_timeout_ms: 10000, log_level: 'warn' }, cold: {} }
  }
}

// --- runtime state ---------------------------------------------------------

export type NodeState = 'pending' | 'starting' | 'healthy' | 'degraded' | 'failed' | 'stopped' | 'paused' | 'removed'

/** One line of a block's deep health document. */
export interface HealthCheck {
  name: string
  status: 'healthy' | 'degraded' | 'failing'
  detail?: Record<string, unknown>
}

/** Mirrors core.NodeStatus (also the payload of the `node:status` event). */
export interface NodeStatus {
  node_id: string
  state: NodeState
  replicas: number
  ready: number
  message?: string
  updated_at: number
  checks?: HealthCheck[]
}

export interface RuntimeInfo {
  kind: 'docker' | 'fake'
  detail: string
}

/** A /control command and its result (blocks/common/control). */
export interface ControlCommand {
  id?: string
  op: string
  target?: { edge?: string; direction?: 'in' | 'out'; peer?: string; topic?: string; table?: string }
  params?: Record<string, unknown>
  duration_ms?: number
}

export interface ControlResult {
  id?: string
  op: string
  ok: boolean
  error?: string
  state?: unknown
}

export interface LogLine {
  ts: number
  level: string
  msg: string
  attrs?: Record<string, unknown>
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

export type FaultKind =
  | 'kill'
  | 'pause'
  | 'partition'
  | 'cpu_stress'
  | 'memory_pressure'
  | 'stall'
  | 'crash'
  | 'latency'
  | 'drop'
  | 'error'
  | 'timeout'
  | 'slow'
  | 'corrupt'
  | 'duplicate'
  | 'reject'

export interface FaultSpec {
  kind: FaultKind
  duration_ms: number
  params: Record<string, unknown>
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
  rps?: number
  p99_ms?: number
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
  deployment: string
  ts: number
  latency_ms: number
  p99_ms: number
  consumer_lag: number
  sagas_per_sec: number
  error_rate: number
  cpu_pct: number
  active_faults: number
  sagas_ok: number
  sagas_failed: number
  source: 'blocks' | 'mock'
}

export interface ContainerStatsEvent {
  deployment: string
  node_id: string
  container: string
  ts: number
  cpu_pct: number
  mem_used_bytes: number
  mem_limit_bytes: number
  net_rx_bps: number
  net_tx_bps: number
  throttled_periods: number
}

export interface TestProgressEvent {
  run_id: string
  scenario: string
  template: string
  phase: string
  elapsed_ms: number
  total_ms: number
  step?: string
  live?: Record<string, number>
}

export interface TestDoneEvent {
  run_id: string
  scenario: string
  template: string
  passed: boolean
  aborted: boolean
  error?: string
}

// --- tests -----------------------------------------------------------------

export interface ScenarioStep {
  at_s: unknown
  do: string
  phase?: string
  node?: string
  kind?: string
  edge?: string
  args?: Record<string, unknown>
}

export interface Scenario {
  id: string
  name: string
  description: string
  reveals?: string
  tags?: string[]
  requires: { kinds?: string[]; needs_proxy?: boolean; needs_broker?: boolean }
  params: Record<string, unknown>
  timeline: ScenarioStep[]
  collect?: string[]
  assert?: { metric: string; op: string; value: number; phase?: string; note?: string }[]
}

export interface RunSummary {
  id: string
  scenario: string
  name: string
  template: string
  started_at: number
  ended_at: number
  passed: boolean
  aborted: boolean
  error?: string
  summary?: Record<string, number>
  running?: boolean
}

export interface RunPoint {
  t: number
  v: number
}

export interface Run extends RunSummary {
  params: Record<string, unknown>
  runtime: string
  phases: { name: string; start_s: number; end_s: number }[]
  series: Record<string, RunPoint[]>
  summary: Record<string, number>
  assertions: { metric: string; op: string; value: number; phase?: string; note?: string; actual: number; passed: boolean }[]
  notes: string[]
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
    n.resources = n.resources ?? {}
  }
  return plain
}

export function toWireFault(f: FaultSpec): spec.FaultSpec {
  return spec.FaultSpec.createFrom(f)
}
