import { create } from 'zustand'
import { backend, errorMessage, hasBackend } from '../lib/backend'
import { onEvent } from '../lib/events'
import {
  EMPTY_TOPOLOGY,
  type ConfigValue,
  type EdgeKind,
  type FaultSpec,
  type LogEvent,
  type MetricsEvent,
  type NodeKind,
  type NodeStatus,
  type OpStatus,
  type Phase,
  type PhaseEvent,
  type Plan,
  type Position,
  type ProgressEvent,
  type Topology,
  type TopologyEdge,
  type TopologyNode,
  type ValidationError,
} from '../types/topology'

export interface LogLine {
  id: number
  ts: number
  level: LogEvent['level']
  source: string
  message: string
}

export type DrawerTab = 'plan' | 'log'

const LOG_CAP = 500
const LATENCY_CAP = 120
const HOT_DEBOUNCE_MS = 150

interface SymphonyState {
  initialized: boolean
  topology: Topology
  /** bumps whenever a whole topology is swapped in, so the canvas re-fits */
  loadSeq: number
  presets: Topology[]
  selectedNodeId: string | null
  statuses: Record<string, NodeStatus>
  phase: Phase
  busy: boolean
  plan: Plan | null
  opStatus: Record<string, OpStatus>
  validation: ValidationError[]
  log: LogLine[]
  metrics: MetricsEvent | null
  latency: number[]
  drawerOpen: boolean
  drawerTab: DrawerTab

  init(): Promise<void>
  loadPreset(name: string): void
  setName(name: string): void
  addNode(kind: NodeKind, position: Position): void
  moveNode(id: string, position: Position): void
  removeNodes(ids: string[]): void
  updateNode(id: string, patch: Partial<Pick<TopologyNode, 'image' | 'replicas'>>): void
  setColdConfig(id: string, key: string, value: ConfigValue): void
  setHotConfig(id: string, key: string, value: ConfigValue): void
  addEdge(from: string, to: string): void
  removeEdges(ids: string[]): void
  select(id: string | null): void
  validate(): Promise<void>
  planApply(): Promise<void>
  apply(): Promise<void>
  teardown(): Promise<void>
  injectFault(id: string, fault: FaultSpec): Promise<void>
  appendLog(line: Omit<LogLine, 'id'>): void
  clearLog(): void
  setDrawer(open: boolean, tab?: DrawerTab): void
}

let logSeq = 0
const hotTimers = new Map<string, ReturnType<typeof setTimeout>>()
const hotPending = new Map<string, Record<string, ConfigValue>>()

function inferEdgeKind(from: TopologyNode, to: TopologyNode): EdgeKind {
  if (from.kind === 'broker') return 'subscribes'
  if (to.kind === 'broker') return 'publishes'
  if (to.kind === 'db') return 'uses'
  return 'calls'
}

function uniqueId(base: string, taken: (id: string) => boolean): string {
  if (!taken(base)) return base
  for (let i = 2; ; i++) {
    const id = `${base}-${i}`
    if (!taken(id)) return id
  }
}

function defaultsFor(kind: NodeKind): Pick<TopologyNode, 'image' | 'config'> {
  switch (kind) {
    case 'service':
      return {
        image: 'symphony/service:dev',
        config: {
          hot: { timeout_ms: 800, retry_max: 3, rate_limit_rps: 200, log_level: 'info' },
          cold: { port: 9000, db_pool_size: 10, heap_mb: 256 },
        },
      }
    case 'broker':
      return {
        image: 'apache/kafka:3.8.0',
        config: {
          hot: { log_retention_ms: 600000, max_poll_records: 500 },
          cold: { num_partitions: 3, replication_factor: 1, heap_mb: 512 },
        },
      }
    case 'db':
      return {
        image: 'postgres:16-alpine',
        config: {
          hot: { statement_timeout_ms: 5000, log_min_duration_ms: 250 },
          cold: { max_connections: 100, shared_buffers_mb: 128 },
        },
      }
    case 'proxy':
      return {
        image: 'ghcr.io/shopify/toxiproxy:2.9.0',
        config: { hot: { latency_ms: 0, jitter_ms: 0, drop_rate: 0 }, cold: { api_port: 8474 } },
      }
    case 'infra':
      return {
        image: 'nginx:1.27-alpine',
        config: { hot: { rate_limit_rps: 500, log_level: 'warn' }, cold: { port: 8080, worker_connections: 1024 } },
      }
  }
}

function progressLine(p: ProgressEvent): Omit<LogLine, 'id'> {
  const n = `${p.index + 1}/${p.count}`
  const base = `[${n}] ${p.kind} ${p.target}`
  switch (p.status) {
    case 'started':
      return { ts: Date.now(), level: 'debug', source: 'apply', message: `${base} ...` }
    case 'done':
      return { ts: Date.now(), level: 'info', source: 'apply', message: `${base} ok (${p.elapsed_ms}ms) [${p.class}]` }
    case 'failed':
      return { ts: Date.now(), level: 'error', source: 'apply', message: `${base} FAILED: ${p.message ?? ''}` }
  }
}

export const useSymphonyStore = create<SymphonyState>()((set, get) => ({
  initialized: false,
  topology: EMPTY_TOPOLOGY,
  loadSeq: 0,
  presets: [],
  selectedNodeId: null,
  statuses: {},
  phase: 'idle',
  busy: false,
  plan: null,
  opStatus: {},
  validation: [],
  log: [],
  metrics: null,
  latency: [],
  drawerOpen: true,
  drawerTab: 'plan',

  async init() {
    if (get().initialized) return
    set({ initialized: true })

    if (!hasBackend()) {
      get().appendLog({
        ts: Date.now(),
        level: 'warn',
        source: 'ui',
        message: 'no Wails backend detected (running in a plain browser?) — bindings are inert',
      })
      return
    }

    onEvent<ProgressEvent>('apply:progress', (p) => {
      set((s) => ({ opStatus: { ...s.opStatus, [p.op_id]: p.status } }))
      get().appendLog(progressLine(p))
    })
    onEvent<LogEvent>('engine:log', (l) => {
      get().appendLog({ ts: l.ts || Date.now(), level: l.level, source: l.source, message: l.message })
    })
    onEvent<PhaseEvent>('engine:phase', (p) => set({ phase: p.phase }))
    onEvent<NodeStatus>('node:status', (st) => {
      set((s) => {
        const statuses = { ...s.statuses }
        if (st.state === 'removed') delete statuses[st.node_id]
        else statuses[st.node_id] = st
        return { statuses }
      })
    })
    onEvent<MetricsEvent>('metrics:tick', (m) => {
      set((s) => {
        const latency = s.latency.length >= LATENCY_CAP ? s.latency.slice(1) : s.latency.slice()
        latency.push(m.latency_ms)
        return { metrics: m, latency }
      })
    })

    try {
      const [presets, current, phase, statuses] = await Promise.all([
        backend.getPresets(),
        backend.getTopology(),
        backend.getPhase(),
        backend.getNodeStatus(),
      ])
      set({ presets, phase, statuses })
      if (current.nodes.length > 0) {
        set((s) => ({ topology: current, loadSeq: s.loadSeq + 1 }))
      } else if (presets.length > 0) {
        get().loadPreset(presets[0].name)
      }
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'ui', message: `init failed: ${errorMessage(err)}` })
    }
  },

  loadPreset(name) {
    const preset = get().presets.find((p) => p.name === name)
    if (!preset) return
    set((s) => ({
      topology: JSON.parse(JSON.stringify(preset)) as Topology,
      loadSeq: s.loadSeq + 1,
      selectedNodeId: null,
      plan: null,
      opStatus: {},
      validation: [],
    }))
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `loaded preset "${name}"` })
  },

  setName(name) {
    set((s) => ({ topology: { ...s.topology, name } }))
  },

  addNode(kind, position) {
    set((s) => {
      const taken = (id: string) => s.topology.nodes.some((n) => n.id === id)
      const id = uniqueId(kind === 'infra' ? 'gateway' : kind, taken)
      const node: TopologyNode = {
        id,
        kind,
        replicas: 1,
        position: { x: Math.round(position.x), y: Math.round(position.y) },
        ...defaultsFor(kind),
      }
      return { topology: { ...s.topology, nodes: [...s.topology.nodes, node] }, selectedNodeId: id }
    })
  },

  moveNode(id, position) {
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.map((n) => (n.id === id ? { ...n, position } : n)),
      },
    }))
  },

  removeNodes(ids) {
    if (ids.length === 0) return
    const gone = new Set(ids)
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.filter((n) => !gone.has(n.id)),
        edges: s.topology.edges.filter((e) => !gone.has(e.from) && !gone.has(e.to) && !(e.via && gone.has(e.via))),
      },
      selectedNodeId: s.selectedNodeId && gone.has(s.selectedNodeId) ? null : s.selectedNodeId,
    }))
  },

  updateNode(id, patch) {
    set((s) => ({
      topology: { ...s.topology, nodes: s.topology.nodes.map((n) => (n.id === id ? { ...n, ...patch } : n)) },
    }))
  },

  setColdConfig(id, key, value) {
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.map((n) =>
          n.id === id ? { ...n, config: { ...n.config, cold: { ...n.config.cold, [key]: value } } } : n,
        ),
      },
    }))
  },

  setHotConfig(id, key, value) {
    // Update the local topology immediately so the control feels live, then
    // coalesce backend calls per node.
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.map((n) =>
          n.id === id ? { ...n, config: { ...n.config, hot: { ...n.config.hot, [key]: value } } } : n,
        ),
      },
    }))
    const pending = hotPending.get(id) ?? {}
    pending[key] = value
    hotPending.set(id, pending)
    const existing = hotTimers.get(id)
    if (existing) clearTimeout(existing)
    hotTimers.set(
      id,
      setTimeout(async () => {
        hotTimers.delete(id)
        const patch = hotPending.get(id)
        hotPending.delete(id)
        if (!patch || !hasBackend()) return
        try {
          await backend.setHotConfig(id, patch)
        } catch (err) {
          get().appendLog({
            ts: Date.now(),
            level: 'warn',
            source: 'control',
            message: `${id}: ${errorMessage(err)} — change kept locally, applied on next Apply`,
          })
        }
      }, HOT_DEBOUNCE_MS),
    )
  },

  addEdge(from, to) {
    set((s) => {
      const a = s.topology.nodes.find((n) => n.id === from)
      const b = s.topology.nodes.find((n) => n.id === to)
      if (!a || !b || from === to) return {}
      if (s.topology.edges.some((e) => e.from === from && e.to === to)) return {}
      const kind = inferEdgeKind(a, b)
      const taken = (id: string) => s.topology.edges.some((e) => e.id === id)
      const edge: TopologyEdge = { id: uniqueId(`e-${from}-${to}`, taken), from, to, kind }
      if (kind === 'publishes') edge.topic = `${from}.events`
      if (kind === 'subscribes') edge.topic = `${to}.events`
      return { topology: { ...s.topology, edges: [...s.topology.edges, edge] } }
    })
  },

  removeEdges(ids) {
    if (ids.length === 0) return
    const gone = new Set(ids)
    set((s) => ({ topology: { ...s.topology, edges: s.topology.edges.filter((e) => !gone.has(e.id)) } }))
  },

  select(id) {
    if (get().selectedNodeId !== id) set({ selectedNodeId: id })
  },

  async validate() {
    if (!hasBackend()) return
    try {
      const findings = await backend.validate(get().topology)
      set({ validation: findings, drawerOpen: true, drawerTab: 'plan' })
      const errors = findings.filter((f) => f.severity === 'error').length
      get().appendLog({
        ts: Date.now(),
        level: errors ? 'warn' : 'info',
        source: 'validate',
        message: `${findings.length} finding(s), ${errors} error(s)`,
      })
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'validate', message: errorMessage(err) })
    }
  },

  async planApply() {
    if (!hasBackend()) return
    set({ busy: true })
    try {
      const [findings, plan] = await Promise.all([backend.validate(get().topology), backend.plan(get().topology)])
      set({ validation: findings, plan, opStatus: {}, drawerOpen: true, drawerTab: 'plan' })
      get().appendLog({
        ts: Date.now(),
        level: 'info',
        source: 'planner',
        message: `plan: ${plan.ops.length} ops (${plan.summary.hot} hot, ${plan.summary.cold} cold, ${plan.summary.structural} structural)`,
      })
    } catch (err) {
      set({ plan: null })
      get().appendLog({ ts: Date.now(), level: 'error', source: 'planner', message: errorMessage(err) })
    } finally {
      set({ busy: false })
    }
  },

  async apply() {
    if (!hasBackend()) return
    set({ busy: true, drawerOpen: true, drawerTab: 'log' })
    try {
      // Refresh the plan so the Plan tab shows what is about to run.
      const plan = await backend.plan(get().topology)
      set({ plan, opStatus: {} })
      await backend.apply(get().topology)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'apply', message: errorMessage(err) })
    } finally {
      set({ busy: false })
    }
  },

  async teardown() {
    if (!hasBackend()) return
    set({ busy: true, drawerOpen: true, drawerTab: 'log' })
    try {
      await backend.teardown()
      set({ plan: null, opStatus: {} })
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'teardown', message: errorMessage(err) })
    } finally {
      set({ busy: false })
    }
  },

  async injectFault(id, fault) {
    if (!hasBackend()) return
    try {
      await backend.injectFault(id, fault)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'warn', source: 'fault', message: `${id}: ${errorMessage(err)}` })
    }
  },

  appendLog(line) {
    set((s) => {
      const next = s.log.length >= LOG_CAP ? s.log.slice(s.log.length - LOG_CAP + 1) : s.log.slice()
      next.push({ id: ++logSeq, ...line })
      return { log: next }
    })
  },

  clearLog() {
    set({ log: [] })
  },

  setDrawer(open, tab) {
    set((s) => ({ drawerOpen: open, drawerTab: tab ?? s.drawerTab }))
  },
}))

/** Selector helper: the currently selected node, or null. */
export function selectSelectedNode(s: SymphonyState): TopologyNode | null {
  if (!s.selectedNodeId) return null
  return s.topology.nodes.find((n) => n.id === s.selectedNodeId) ?? null
}
