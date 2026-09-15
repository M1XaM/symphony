import { create } from 'zustand'
import { backend, errorMessage, hasBackend } from '../lib/backend'
import { onEvent } from '../lib/events'
import {
  EMPTY_TOPOLOGY,
  type ConfigValue,
  type FaultSpec,
  type HostStatsEvent,
  type LinkHealthEntry,
  type LinkHealthEvent,
  type LogEvent,
  type MetricsEvent,
  type NodeKind,
  type NodeStatus,
  type NodeStatusEvent,
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

export type DrawerTab = 'plan' | 'stats' | 'log' | 'tests'

/** One applied topology as the webview sees it. Several can run at once; the
 *  engine keys them by topology name, and so does the store. */
export interface Deployment {
  name: string
  phase: Phase
  statuses: Record<string, NodeStatus>
  /** Per-edge health from the last `link:health` tick, keyed by edge id. */
  links: Record<string, LinkHealthEntry>
  /** An apply or teardown is in flight for this deployment. */
  busy: boolean
}

/** The two halves of the left panel's slider. */
export type LeftTab = 'elements' | 'templates'

/** Set while the left panel is in its "build a template" state. */
export interface TemplateDraft {
  /** Topology to put back on the canvas if the build is cancelled. */
  restore: Topology
}

const LOG_CAP = 500
/** Host samples kept for the Stats tab: one per second. */
const HOST_STATS_CAP = 180
const HOT_DEBOUNCE_MS = 150
const TEMPLATES_KEY = 'symphony.templates'

interface SymphonyState {
  initialized: boolean
  topology: Topology
  /** bumps whenever a whole topology is swapped in, so the canvas re-fits */
  loadSeq: number
  presets: Topology[]
  selectedNodeId: string | null
  /** Everything the engine has applied, keyed by topology name. The canvas can
   *  show a template that is not among them, which then has no runtime state
   *  of its own — see selectIsApplied. */
  deployments: Record<string, Deployment>
  plan: Plan | null
  opStatus: Record<string, OpStatus>
  validation: ValidationError[]
  log: LogLine[]
  metrics: MetricsEvent | null
  hostStats: HostStatsEvent[]
  drawerOpen: boolean
  drawerTab: DrawerTab
  leftTab: LeftTab
  /** Templates the user built, stacked on top of the built-in `presets`. */
  userTemplates: Topology[]
  draft: TemplateDraft | null

  init(): Promise<void>
  loadTemplate(name: string): void
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
  setLeftTab(tab: LeftTab): void
  startTemplate(): void
  saveTemplate(): Promise<void>
  cancelTemplate(): void
  deleteTemplate(name: string): Promise<void>
}

let logSeq = 0
/** Shared empty maps, so selectors on a non-applied topology keep a stable reference. */
const NO_STATUSES: Record<string, NodeStatus> = {}
const NO_LINKS: Record<string, LinkHealthEntry> = {}

function newDeployment(name: string, phase: Phase = 'idle'): Deployment {
  return { name, phase, statuses: {}, links: {}, busy: false }
}

/** Return a copy of the deployments map with one entry patched, creating it
 *  when the engine mentions a deployment the webview has not seen. */
function patchDeployment(
  deployments: Record<string, Deployment>,
  name: string,
  patch: (d: Deployment) => Partial<Deployment>,
): Record<string, Deployment> {
  const cur = deployments[name] ?? newDeployment(name)
  return { ...deployments, [name]: { ...cur, ...patch(cur) } }
}

function dropDeployment(deployments: Record<string, Deployment>, name: string): Record<string, Deployment> {
  if (!(name in deployments)) return deployments
  const next = { ...deployments }
  delete next[name]
  return next
}
const hotTimers = new Map<string, ReturnType<typeof setTimeout>>()
const hotPending = new Map<string, Record<string, ConfigValue>>()

function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** The templates the webview itself remembers. The backend's file is the real
 *  library; this mirrors it so a plain browser — or a machine where the config
 *  directory cannot be written — still remembers across restarts. */
function readTemplates(): Topology[] {
  try {
    const raw = window.localStorage.getItem(TEMPLATES_KEY)
    const parsed = raw ? (JSON.parse(raw) as unknown) : null
    return Array.isArray(parsed) ? (parsed as Topology[]) : []
  } catch {
    return []
  }
}

function writeTemplates(list: Topology[]): void {
  try {
    window.localStorage.setItem(TEMPLATES_KEY, JSON.stringify(list))
  } catch {
    // storage unavailable — the templates still live for this session
  }
}

/** Send a library change to the backend and keep whatever comes back, falling
 *  back to the webview's own copy when there is no backend or the file cannot
 *  be written. Callers update the state optimistically first, so the panel
 *  never waits on the disk. */
async function persistTemplates(list: Topology[], call: () => Promise<Topology[]>): Promise<void> {
  if (!hasBackend()) {
    writeTemplates(list)
    return
  }
  try {
    const stored = await call()
    useSymphonyStore.setState({ userTemplates: stored })
    writeTemplates(stored)
  } catch (err) {
    writeTemplates(list)
    useSymphonyStore.getState().appendLog({
      ts: Date.now(),
      level: 'warn',
      source: 'library',
      message: `${errorMessage(err)} — template kept in this webview instead`,
    })
  }
}

/** Guess what a new edge means from its endpoints. A proxy passes traffic
 *  through, so an edge leaving one continues whatever feeds it: a proxy fed
 *  by a subscription forwards that subscription, with its topic. */
function inferEdge(t: Topology, from: TopologyNode, to: TopologyNode): Pick<TopologyEdge, 'kind' | 'topic'> {
  if (from.kind === 'broker') return { kind: 'subscribes', topic: `${to.id}.events` }
  if (from.kind === 'proxy') {
    const feed = t.edges.find((e) => e.to === from.id && e.kind === 'subscribes')
    if (feed) return { kind: 'subscribes', topic: feed.topic }
  }
  if (to.kind === 'broker') return { kind: 'publishes', topic: `${from.id}.events` }
  if (to.kind === 'proxy') {
    const feed = t.edges.find((e) => e.from === to.id && e.kind === 'publishes')
    if (feed) return { kind: 'publishes', topic: feed.topic }
  }
  if (to.kind === 'db') return { kind: 'uses' }
  return { kind: 'calls' }
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
  const base = `[${p.deployment}] ${n} ${p.kind} ${p.target}`
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
  deployments: {},
  plan: null,
  opStatus: {},
  validation: [],
  log: [],
  metrics: null,
  hostStats: [],
  drawerOpen: true,
  drawerTab: 'plan',
  leftTab: 'elements',
  userTemplates: readTemplates(),
  draft: null,

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

    // The engine and the framework both log while the window is still coming
    // up, long before any of this is listening. Take what they already wrote,
    // then subscribe: nothing is emitted in between, since the engine sits idle
    // until the user acts.
    try {
      for (const l of await backend.getLogHistory()) {
        get().appendLog({ ts: l.ts, level: l.level, source: l.source, message: l.message })
      }
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'warn', source: 'ui', message: `log history: ${errorMessage(err)}` })
    }

    onEvent<ProgressEvent>('apply:progress', (p) => {
      // The Plan tab shows the canvas topology's plan; op ids repeat across
      // deployments, so only its own progress may light the rows up.
      if (p.deployment === get().topology.name) {
        set((s) => ({ opStatus: { ...s.opStatus, [p.op_id]: p.status } }))
      }
      get().appendLog(progressLine(p))
    })
    onEvent<LogEvent>('engine:log', (l) => {
      get().appendLog({ ts: l.ts || Date.now(), level: l.level, source: l.source, message: l.message })
    })
    onEvent<PhaseEvent>('engine:phase', (p) => {
      set((s) => ({
        deployments:
          p.phase === 'gone'
            ? dropDeployment(s.deployments, p.deployment)
            : patchDeployment(s.deployments, p.deployment, () => ({ phase: p.phase as Phase })),
      }))
    })
    onEvent<NodeStatusEvent>('node:status', (st) => {
      set((s) => ({
        deployments: patchDeployment(s.deployments, st.deployment, (d) => {
          const statuses = { ...d.statuses }
          const { deployment: _dep, ...status } = st
          if (status.state === 'removed') delete statuses[status.node_id]
          else statuses[status.node_id] = status
          return { statuses }
        }),
      }))
    })
    onEvent<LinkHealthEvent>('link:health', (h) => {
      set((s) => {
        if (!(h.deployment in s.deployments)) return {}
        return { deployments: patchDeployment(s.deployments, h.deployment, () => ({ links: h.links })) }
      })
    })
    onEvent<MetricsEvent>('metrics:tick', (m) => set({ metrics: m }))
    onEvent<HostStatsEvent>('host:stats', (h) => {
      set((s) => {
        const hostStats = s.hostStats.length >= HOST_STATS_CAP ? s.hostStats.slice(1) : s.hostStats.slice()
        hostStats.push(h)
        return { hostStats }
      })
    })

    try {
      const [presets, running, hostStats] = await Promise.all([
        backend.getPresets(),
        backend.getDeployments(),
        backend.getHostStats(),
      ])
      const deployments: Record<string, Deployment> = {}
      for (const d of running) {
        deployments[d.name] = { name: d.name, phase: d.phase, statuses: d.statuses, links: {}, busy: false }
      }
      set({ presets, deployments, hostStats: hostStats.slice(-HOST_STATS_CAP) })
      if (running.length > 0) {
        // Come back to what the engine is running rather than a blank canvas.
        set((s) => ({ topology: running[0].topology, loadSeq: s.loadSeq + 1 }))
      } else if (presets.length > 0) {
        get().loadTemplate(presets[0].name)
      }
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'ui', message: `init failed: ${errorMessage(err)}` })
    }

    try {
      let stored = await backend.getUserTemplates()
      const mirrored = get().userTemplates
      if (stored.length === 0 && mirrored.length > 0) {
        // First run of a build that stores templates on disk: lift the ones
        // the webview was holding into the file.
        for (const t of mirrored) stored = await backend.saveUserTemplate(t)
      }
      set({ userTemplates: stored })
      writeTemplates(stored)
    } catch (err) {
      get().appendLog({
        ts: Date.now(),
        level: 'warn',
        source: 'library',
        message: `${errorMessage(err)} — using the templates kept in this webview`,
      })
    }
  },

  loadTemplate(name) {
    const { presets, userTemplates } = get()
    const template = presets.find((p) => p.name === name) ?? userTemplates.find((t) => t.name === name)
    if (!template) return
    set((s) => ({
      topology: clone(template),
      loadSeq: s.loadSeq + 1,
      selectedNodeId: null,
      plan: null,
      opStatus: {},
      validation: [],
      draft: null,
    }))
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `loaded template "${name}"` })
  },

  setName(name) {
    // The engine keys deployments by name, so what is applied keeps its name
    // until it is torn down; the toolbar makes the field read-only then.
    if (selectIsApplied(get())) return
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
        // Only a running node has anything to push to; otherwise the change
        // simply rides along with the next Apply.
        if (!patch || !hasBackend() || !selectIsApplied(get())) return
        try {
          await backend.setHotConfig(get().topology.name, id, patch)
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
      const { kind, topic } = inferEdge(s.topology, a, b)
      const taken = (id: string) => s.topology.edges.some((e) => e.id === id)
      const edge: TopologyEdge = { id: uniqueId(`e-${from}-${to}`, taken), from, to, kind }
      if (topic) edge.topic = topic
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
    }
  },

  async apply() {
    if (!hasBackend()) return
    const name = get().topology.name
    if (get().deployments[name]?.busy) return
    // From here on this topology is one of the engine's deployments, next to
    // whatever else is already running; its statuses and phase are its own.
    set((s) => ({
      drawerOpen: true,
      drawerTab: 'log',
      deployments: patchDeployment(s.deployments, name, () => ({ busy: true })),
    }))
    try {
      // Refresh the plan so the Plan tab shows what is about to run.
      const plan = await backend.plan(get().topology)
      set({ plan, opStatus: {} })
      await backend.apply(get().topology)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'apply', message: `[${name}] ${errorMessage(err)}` })
    } finally {
      set((s) => ({
        // an apply that ended with nothing running has already been dropped
        deployments: name in s.deployments ? patchDeployment(s.deployments, name, () => ({ busy: false })) : s.deployments,
      }))
    }
  },

  async teardown() {
    if (!hasBackend()) return
    const name = get().topology.name
    if (!(name in get().deployments) || get().deployments[name].busy) return
    set((s) => ({
      drawerOpen: true,
      drawerTab: 'log',
      deployments: patchDeployment(s.deployments, name, () => ({ busy: true })),
    }))
    try {
      await backend.teardown(name)
      set((s) => ({ plan: null, opStatus: {}, deployments: dropDeployment(s.deployments, name) }))
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'teardown', message: `[${name}] ${errorMessage(err)}` })
      set((s) => ({
        deployments: name in s.deployments ? patchDeployment(s.deployments, name, () => ({ busy: false })) : s.deployments,
      }))
    }
  },

  async injectFault(id, fault) {
    if (!hasBackend() || !selectIsApplied(get())) return
    try {
      await backend.injectFault(get().topology.name, id, fault)
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

  setLeftTab(tab) {
    set({ leftTab: tab })
  },

  startTemplate() {
    set((s) => ({
      draft: { restore: s.topology },
      topology: { version: s.topology.version, name: '', nodes: [], edges: [] },
      loadSeq: s.loadSeq + 1,
      selectedNodeId: null,
      plan: null,
      opStatus: {},
      validation: [],
      leftTab: 'elements',
    }))
  },

  async saveTemplate() {
    const s = get()
    if (!s.draft) return
    const name = s.topology.name.trim()
    if (!name || s.topology.nodes.length === 0) return
    const taken = (n: string) => s.presets.some((p) => p.name === n) || s.userTemplates.some((t) => t.name === n)
    const unique = uniqueId(name, taken)
    const saved = clone({ ...s.topology, name: unique })
    const userTemplates = [...s.userTemplates, saved]
    set({ userTemplates, draft: null, topology: { ...s.topology, name: unique }, leftTab: 'templates' })
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `saved template "${unique}"` })
    await persistTemplates(userTemplates, () => backend.saveUserTemplate(saved))
  },

  cancelTemplate() {
    const draft = get().draft
    if (!draft) return
    set((s) => ({ draft: null, topology: draft.restore, loadSeq: s.loadSeq + 1, selectedNodeId: null }))
  },

  async deleteTemplate(name) {
    const userTemplates = get().userTemplates.filter((t) => t.name !== name)
    if (userTemplates.length === get().userTemplates.length) return
    set({ userTemplates })
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `deleted template "${name}"` })
    await persistTemplates(userTemplates, () => backend.deleteUserTemplate(name))
  },
}))

/** Selector helper: the currently selected node, or null. */
export function selectSelectedNode(s: SymphonyState): TopologyNode | null {
  if (!s.selectedNodeId) return null
  return s.topology.nodes.find((n) => n.id === s.selectedNodeId) ?? null
}

/** The deployment of the topology on the canvas, if the engine is running it. */
export function selectDeployment(s: SymphonyState): Deployment | undefined {
  return s.deployments[s.topology.name]
}

/** True when the canvas is showing a topology the engine has applied. */
export function selectIsApplied(s: SymphonyState): boolean {
  return s.topology.name in s.deployments
}

/** Statuses describe the canvas topology's own deployment only: another
 *  template is not running, whatever node ids it happens to share. */
export function selectStatuses(s: SymphonyState): Record<string, NodeStatus> {
  return selectDeployment(s)?.statuses ?? NO_STATUSES
}

/** Per-edge health of the canvas topology's deployment; empty when not applied. */
export function selectLinks(s: SymphonyState): Record<string, LinkHealthEntry> {
  return selectDeployment(s)?.links ?? NO_LINKS
}

/** The phase of what is on the canvas, idle when it is not applied. */
export function selectPhase(s: SymphonyState): Phase {
  return selectDeployment(s)?.phase ?? 'idle'
}

/** True while the canvas topology is being applied or torn down. */
export function selectBusy(s: SymphonyState): boolean {
  return selectDeployment(s)?.busy ?? false
}
