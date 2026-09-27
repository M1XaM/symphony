import { create } from 'zustand'
import { backend, errorMessage, hasBackend } from '../lib/backend'
import { onEvent } from '../lib/events'
import {
  DEFAULT_IMAGE,
  DEFAULT_RESOURCES,
  EMPTY_TOPOLOGY,
  SCHEMA_VERSION,
  defaultConfig,
  type ConfigValue,
  type ContainerRow,
  type ContainerStatsEvent,
  type ControlCommand,
  type ControlResult,
  type DeploymentInfo,
  type FaultSpec,
  type HostStatsEvent,
  type LinkHealthEntry,
  type LinkHealthEvent,
  type LogEvent,
  type LogLine,
  type LogStreamState,
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
  type Resources,
  type Run,
  type RunSummary,
  type RuntimeStatus,
  type Scenario,
  type TestDoneEvent,
  type TestProgressEvent,
  type Topology,
  type TopologyEdge,
  type TopologyNode,
  type ValidationError,
} from '../types/topology'

export interface LogLineEntry {
  id: number
  ts: number
  level: LogEvent['level']
  source: string
  message: string
}

export type DrawerTab = 'plan' | 'stats' | 'containers' | 'logs' | 'log' | 'tests'

/** The two floating panels that can be folded away to clear the map. */
export type PanelSide = 'left' | 'right'

/** The two halves of the left panel's slider. */
type LeftTab = 'elements' | 'templates'

/** Set while the left panel is in its "build a template" state. */
interface TemplateDraft {
  /** Topology to put back on the canvas if the build is cancelled. */
  restore: Topology
}

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
  /** Last `metrics:tick` for this deployment. */
  metrics?: MetricsEvent
  /** Last `container:stats` per node (replicas summed). */
  containers: Record<string, ContainerStatsEvent>
}

/** One second of a deployment's container totals, for the Stats tab. */
export interface SimSample {
  ts: number
  cpu_pct: number
  mem_mb: number
}

const LOG_CAP = 2000
/** The engine talks in single events: a status per node, a stats sample per
 *  container, a line per line of container output. Handing each one to React
 *  on its own re-renders the app dozens of times a second, so they are queued
 *  and applied together in one update per interval. */
const EVENT_FLUSH_MS = 100
/** Host samples kept for the Stats tab: one per second. */
const HOST_STATS_CAP = 180
const HOT_DEBOUNCE_MS = 150
const TEMPLATES_KEY = 'symphony.templates'

export interface SymphonyState {
  initialized: boolean
  /** Whether the Docker daemon answers; null until the backend has said. */
  runtime: RuntimeStatus | null
  topology: Topology
  /** bumps whenever a whole topology is swapped in, so the canvas re-fits */
  loadSeq: number
  presets: Topology[]
  selectedNodeId: string | null
  selectedEdgeId: string | null
  /** Everything the engine has applied, keyed by topology name. */
  deployments: Record<string, Deployment>
  plan: Plan | null
  opStatus: Record<string, OpStatus>
  validation: ValidationError[]
  log: LogLineEntry[]
  /** metrics over every deployment (the collector's "" sample) */
  metrics: MetricsEvent | null
  hostStats: HostStatsEvent[]
  simHistory: Record<string, SimSample[]>
  drawerOpen: boolean
  drawerTab: DrawerTab
  /** Folded-away side panels; only their reopen arrow stays on screen. */
  panelOpen: Record<PanelSide, boolean>
  leftTab: LeftTab
  /** Templates the user built, stacked on top of the built-in `presets`. */
  userTemplates: Topology[]
  draft: TemplateDraft | null
  /** Logs tab: which node and what it said. */
  logsNode: string | null
  nodeLogs: LogLine[]
  /** Containers tab: the last `docker ps` sweep. */
  containers: ContainerRow[]
  /** How many of them are running, kept with the rows so the tab badge is a
   *  field read rather than a scan on every store update. */
  containersRunning: number
  containersAll: boolean
  containersError: string | null
  /** Whether container output is mirrored into the Log tab. */
  logStream: LogStreamState
  /** Tests tab. */
  scenarios: Scenario[]
  runs: RunSummary[]
  activeTest: TestProgressEvent | null
  openRun: Run | null
  compareIds: string[]
  compareRuns: Run[]

  init(): Promise<void>
  /** Re-read what the engine runs; `changed` names a deployment whose
   *  topology another process changed, which the map then shows as it is. */
  refreshDeployments(changed?: string): Promise<void>
  loadTemplate(name: string): void
  setName(name: string): void
  addNode(kind: NodeKind, position: Position): void
  moveNode(id: string, position: Position): void
  removeNodes(ids: string[]): void
  updateNode(id: string, patch: Partial<Pick<TopologyNode, 'image' | 'replicas'>>): void
  setColdConfig(id: string, key: string, value: ConfigValue): void
  setHotConfig(id: string, key: string, value: ConfigValue): void
  setResources(id: string, r: Resources): void
  addEdge(from: string, to: string): void
  updateEdge(id: string, patch: Partial<Pick<TopologyEdge, 'topic' | 'timeout_ms' | 'retries'>>): void
  removeEdges(ids: string[]): void
  select(id: string | null): void
  selectEdge(id: string | null): void
  validate(): Promise<void>
  planApply(): Promise<void>
  apply(): Promise<void>
  teardown(): Promise<void>
  injectFault(id: string, fault: FaultSpec): Promise<void>
  clearFaults(id: string): Promise<void>
  control(id: string, cmd: ControlCommand): Promise<ControlResult | null>
  setLogsNode(id: string | null): void
  refreshLogs(): Promise<void>
  refreshContainers(): Promise<void>
  setContainersAll(all: boolean): void
  setLogStream(on: boolean): Promise<void>
  appendLog(line: Omit<LogLineEntry, 'id'>): void
  clearLog(): void
  setDrawer(open: boolean, tab?: DrawerTab): void
  togglePanel(side: PanelSide): void
  setLeftTab(tab: LeftTab): void
  startTemplate(): void
  saveTemplate(): Promise<void>
  cancelTemplate(): void
  deleteTemplate(name: string): Promise<void>
  refreshRuns(): Promise<void>
  startTest(scenario: string, params: Record<string, unknown>): Promise<void>
  abortTest(): Promise<void>
  viewRun(id: string | null): Promise<void>
  deleteRun(id: string): Promise<void>
  toggleCompare(id: string): Promise<void>
}

type Update = (s: SymphonyState) => Partial<SymphonyState>

let logSeq = 0
/** Log lines and event updates waiting for the next flush; see EVENT_FLUSH_MS. */
let pendingLines: LogLineEntry[] = []
let pendingUpdates: Update[] = []
let flushTimer: ReturnType<typeof setTimeout> | null = null

function scheduleFlush(): void {
  if (flushTimer === null) flushTimer = setTimeout(flush, EVENT_FLUSH_MS)
}

/** Queue a state change driven by a backend event. */
function queue(update: Update): void {
  pendingUpdates.push(update)
  scheduleFlush()
}

function flush(): void {
  flushTimer = null
  const updates = pendingUpdates
  const lines = pendingLines.length > LOG_CAP ? pendingLines.slice(-LOG_CAP) : pendingLines
  pendingUpdates = []
  pendingLines = []
  useSymphonyStore.setState((s) => {
    let next = s
    for (const u of updates) next = { ...next, ...u(next) }
    if (lines.length > 0) {
      const keep = Math.max(0, next.log.length + lines.length - LOG_CAP)
      const log = keep > 0 ? next.log.slice(keep) : next.log.slice()
      for (const l of lines) log.push(l)
      next = { ...next, log }
    }
    return next
  })
}

/** Shared empty maps, so selectors on a non-applied topology keep a stable reference. */
const NO_STATUSES: Record<string, NodeStatus> = {}
const NO_LINKS: Record<string, LinkHealthEntry> = {}
const NO_CONTAINERS: Record<string, ContainerStatsEvent> = {}
const NO_SAMPLES: SimSample[] = []
const hotTimers = new Map<string, ReturnType<typeof setTimeout>>()
const hotPending = new Map<string, Record<string, ConfigValue>>()
const resTimers = new Map<string, ReturnType<typeof setTimeout>>()

function newDeployment(name: string, phase: Phase = 'idle'): Deployment {
  return { name, phase, statuses: {}, links: {}, busy: false, containers: {} }
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

function freshDeployment(d: DeploymentInfo): Deployment {
  return { ...newDeployment(d.name, d.phase), statuses: d.statuses }
}

function dropDeployment(deployments: Record<string, Deployment>, name: string): Record<string, Deployment> {
  if (!(name in deployments)) return deployments
  const next = { ...deployments }
  delete next[name]
  return next
}

/** Drop a gone deployment's chart samples with it, so re-applying the same
 *  template starts from an empty chart instead of the last run's tail. */
function dropHistory(history: Record<string, SimSample[]>, name: string): Record<string, SimSample[]> {
  if (!(name in history)) return history
  const next = { ...history }
  delete next[name]
  return next
}

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
 *  through, so an edge leaving one continues whatever feeds it. */
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

function progressLine(p: ProgressEvent): Omit<LogLineEntry, 'id'> {
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

function warn(source: string, err: unknown): void {
  useSymphonyStore.getState().appendLog({ ts: Date.now(), level: 'warn', source, message: errorMessage(err) })
}

export const useSymphonyStore = create<SymphonyState>()((set, get) => ({
  initialized: false,
  runtime: null,
  topology: EMPTY_TOPOLOGY,
  loadSeq: 0,
  presets: [],
  selectedNodeId: null,
  selectedEdgeId: null,
  deployments: {},
  plan: null,
  opStatus: {},
  validation: [],
  log: [],
  metrics: null,
  hostStats: [],
  simHistory: {},
  drawerOpen: true,
  drawerTab: 'plan',
  panelOpen: { left: true, right: true },
  leftTab: 'elements',
  userTemplates: readTemplates(),
  draft: null,
  logsNode: null,
  nodeLogs: [],
  containers: [],
  containersRunning: 0,
  containersAll: false,
  containersError: null,
  logStream: { available: false, enabled: false },
  scenarios: [],
  runs: [],
  activeTest: null,
  openRun: null,
  compareIds: [],
  compareRuns: [],

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

    try {
      for (const l of await backend.getLogHistory()) {
        get().appendLog({ ts: l.ts, level: l.level, source: l.source, message: l.message })
      }
    } catch (err) {
      warn('ui', err)
    }

    onEvent<RuntimeStatus>('runtime:status', (r) => {
      const was = get().runtime?.available
      set({ runtime: r })
      // the engine has just adopted whatever the daemon still runs
      if (r.available && !was) void get().refreshDeployments()
    })
    onEvent<PhaseEvent>('deployment:sync', (p) => void get().refreshDeployments(p.deployment))
    onEvent<ProgressEvent>('apply:progress', (p) => {
      queue((s) => (p.deployment === s.topology.name ? { opStatus: { ...s.opStatus, [p.op_id]: p.status } } : {}))
      get().appendLog(progressLine(p))
    })
    onEvent<LogEvent>('engine:log', (l) => {
      get().appendLog({ ts: l.ts || Date.now(), level: l.level, source: l.source, message: l.message })
    })
    onEvent<PhaseEvent>('engine:phase', (p) => {
      queue((s) =>
        p.phase === 'gone'
          ? { deployments: dropDeployment(s.deployments, p.deployment), simHistory: dropHistory(s.simHistory, p.deployment) }
          : { deployments: patchDeployment(s.deployments, p.deployment, () => ({ phase: p.phase as Phase })) },
      )
    })
    onEvent<NodeStatusEvent>('node:status', (st) => {
      queue((s) => ({
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
      queue((s) => {
        if (!(h.deployment in s.deployments)) return {}
        return { deployments: patchDeployment(s.deployments, h.deployment, () => ({ links: h.links })) }
      })
    })
    onEvent<MetricsEvent>('metrics:tick', (m) => {
      if (!m.deployment) {
        queue(() => ({ metrics: m }))
        return
      }
      queue((s) => {
        if (!(m.deployment in s.deployments)) return {}
        return { deployments: patchDeployment(s.deployments, m.deployment, () => ({ metrics: m })) }
      })
    })
    onEvent<ContainerStatsEvent>('container:stats', (c) => {
      queue((s) => {
        if (!(c.deployment in s.deployments)) return {}
        const d = s.deployments[c.deployment]
        const prev = d.containers[c.node_id]
        // replicas report separately; fold them into the node within the same second
        const merged: ContainerStatsEvent =
          prev && prev.container !== c.container && Math.abs(prev.ts - c.ts) < 900
            ? { ...c, cpu_pct: prev.cpu_pct + c.cpu_pct, mem_used_bytes: prev.mem_used_bytes + c.mem_used_bytes, net_rx_bps: prev.net_rx_bps + c.net_rx_bps, net_tx_bps: prev.net_tx_bps + c.net_tx_bps }
            : c
        const containers = { ...d.containers, [c.node_id]: merged }
        let cpu = 0
        let mem = 0
        for (const v of Object.values(containers)) {
          cpu += v.cpu_pct
          mem += v.mem_used_bytes
        }
        const hist = s.simHistory[c.deployment] ?? []
        const last = hist[hist.length - 1]
        let next = hist
        if (!last || c.ts - last.ts >= 900) {
          next = hist.length >= HOST_STATS_CAP ? hist.slice(1) : hist.slice()
          next.push({ ts: c.ts, cpu_pct: Math.round(cpu * 10) / 10, mem_mb: Math.round(mem / 1048576) })
        } else {
          next = hist.slice(0, -1)
          next.push({ ts: last.ts, cpu_pct: Math.round(cpu * 10) / 10, mem_mb: Math.round(mem / 1048576) })
        }
        return {
          deployments: patchDeployment(s.deployments, c.deployment, () => ({ containers })),
          simHistory: { ...s.simHistory, [c.deployment]: next },
        }
      })
    })
    onEvent<HostStatsEvent>('host:stats', (h) => {
      queue((s) => {
        const hostStats = s.hostStats.length >= HOST_STATS_CAP ? s.hostStats.slice(1) : s.hostStats.slice()
        hostStats.push(h)
        return { hostStats }
      })
    })
    onEvent<TestProgressEvent>('test:progress', (p) => queue(() => ({ activeTest: p })))
    onEvent<TestDoneEvent>('test:done', (d) => {
      // queued too, so a progress tick still waiting in the queue cannot
      // bring the finished run back
      queue(() => ({ activeTest: null }))
      get().appendLog({
        ts: Date.now(),
        level: d.passed ? 'info' : 'warn',
        source: 'test',
        message: `${d.scenario} on "${d.template}": ${d.aborted ? 'aborted' : d.passed ? 'passed' : 'failed'}${d.error ? ` (${d.error})` : ''}`,
      })
      void get().refreshRuns()
      if (get().openRun?.id === d.run_id) void get().viewRun(d.run_id)
    })

    try {
      const [runtime, presets, running, hostStats] = await Promise.all([
        backend.getRuntimeStatus(),
        backend.getPresets(),
        backend.getDeployments(),
        backend.getHostStats(),
      ])
      const deployments: Record<string, Deployment> = {}
      for (const d of running) deployments[d.name] = freshDeployment(d)
      set({ runtime, presets, deployments, hostStats: hostStats.slice(-HOST_STATS_CAP) })
      if (running.length > 0) {
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
        for (const t of mirrored) stored = await backend.saveUserTemplate(t)
      }
      set({ userTemplates: stored })
      writeTemplates(stored)
    } catch (err) {
      warn('library', err)
    }

    try {
      set({ logStream: await backend.containerLogStream() })
    } catch (err) {
      warn('logs', err)
    }

    try {
      set({ scenarios: await backend.listScenarios() })
      await get().refreshRuns()
    } catch (err) {
      warn('tests', err)
    }
  },

  async refreshDeployments(changed) {
    try {
      const running = await backend.getDeployments()
      set((s) => {
        const deployments: Record<string, Deployment> = {}
        for (const d of running) {
          const prev = s.deployments[d.name]
          deployments[d.name] = prev ? { ...prev, phase: d.phase, statuses: d.statuses } : freshDeployment(d)
        }
        const shown = changed === s.topology.name ? running.find((d) => d.name === changed) : undefined
        return shown ? { deployments, topology: shown.topology, plan: null, opStatus: {} } : { deployments }
      })
    } catch (err) {
      warn('engine', err)
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
      selectedEdgeId: null,
      plan: null,
      opStatus: {},
      validation: [],
      draft: null,
    }))
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `loaded template "${name}"` })
  },

  setName(name) {
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
        image: DEFAULT_IMAGE[kind],
        replicas: 1,
        position: { x: Math.round(position.x), y: Math.round(position.y) },
        config: defaultConfig(kind),
        resources: { ...DEFAULT_RESOURCES[kind] },
      }
      return { topology: { ...s.topology, nodes: [...s.topology.nodes, node] }, selectedNodeId: id, selectedEdgeId: null }
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

  setResources(id, r) {
    set((s) => ({
      topology: { ...s.topology, nodes: s.topology.nodes.map((n) => (n.id === id ? { ...n, resources: { ...n.resources, ...r } } : n)) },
    }))
    const existing = resTimers.get(id)
    if (existing) clearTimeout(existing)
    resTimers.set(
      id,
      setTimeout(async () => {
        resTimers.delete(id)
        if (!hasBackend() || !selectIsApplied(get())) return
        const node = get().topology.nodes.find((n) => n.id === id)
        if (!node) return
        try {
          await backend.setResources(get().topology.name, id, node.resources)
        } catch (err) {
          warn('runtime', err)
        }
      }, 400),
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

  updateEdge(id, patch) {
    set((s) => ({
      topology: { ...s.topology, edges: s.topology.edges.map((e) => (e.id === id ? { ...e, ...patch } : e)) },
    }))
  },

  removeEdges(ids) {
    if (ids.length === 0) return
    const gone = new Set(ids)
    set((s) => ({
      topology: { ...s.topology, edges: s.topology.edges.filter((e) => !gone.has(e.id)) },
      selectedEdgeId: s.selectedEdgeId && gone.has(s.selectedEdgeId) ? null : s.selectedEdgeId,
    }))
  },

  select(id) {
    if (get().selectedNodeId !== id) set({ selectedNodeId: id, selectedEdgeId: id ? null : get().selectedEdgeId })
  },

  selectEdge(id) {
    if (get().selectedEdgeId !== id) set({ selectedEdgeId: id, selectedNodeId: id ? null : get().selectedNodeId })
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
    set((s) => ({
      drawerOpen: true,
      drawerTab: 'log',
      deployments: patchDeployment(s.deployments, name, () => ({ busy: true })),
    }))
    try {
      const plan = await backend.plan(get().topology)
      set({ plan, opStatus: {} })
      await backend.apply(get().topology)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'apply', message: `[${name}] ${errorMessage(err)}` })
    } finally {
      set((s) => ({
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
      set((s) => ({
        plan: null,
        opStatus: {},
        deployments: dropDeployment(s.deployments, name),
        simHistory: dropHistory(s.simHistory, name),
      }))
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

  async clearFaults(id) {
    if (!hasBackend() || !selectIsApplied(get())) return
    try {
      await backend.clearFaults(get().topology.name, id)
    } catch (err) {
      warn('fault', err)
    }
  },

  async control(id, cmd) {
    if (!hasBackend() || !selectIsApplied(get())) return null
    try {
      return await backend.control(get().topology.name, id, cmd)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'warn', source: 'control', message: `${id} ${cmd.op}: ${errorMessage(err)}` })
      return null
    }
  },

  setLogsNode(id) {
    set({ logsNode: id, nodeLogs: [] })
    if (id) void get().refreshLogs()
  },

  async refreshLogs() {
    const id = get().logsNode
    if (!id || !hasBackend() || !selectIsApplied(get())) return
    try {
      const lines = await backend.getNodeLogs(get().topology.name, id, 300)
      if (get().logsNode === id) set({ nodeLogs: lines })
    } catch (err) {
      warn('logs', err)
    }
  },

  /** One `docker ps` sweep. The Containers tab calls this on a timer, which
   *  is the `watch docker ps` it stands in for. */
  async refreshContainers() {
    if (!hasBackend()) return
    try {
      const rows = await backend.listContainers(get().containersAll)
      let running = 0
      for (const r of rows) if (r.state === 'running') running++
      set({ containers: rows, containersRunning: running, containersError: null })
    } catch (err) {
      set({ containersError: errorMessage(err) })
    }
  },

  setContainersAll(all) {
    set({ containersAll: all })
    void get().refreshContainers()
  },

  async setLogStream(on) {
    if (!hasBackend()) return
    try {
      set({ logStream: await backend.setContainerLogStream(on) })
    } catch (err) {
      warn('logs', err)
    }
  },

  appendLog(line) {
    pendingLines.push({ id: ++logSeq, ...line })
    scheduleFlush()
  },

  clearLog() {
    pendingLines.length = 0
    set({ log: [] })
  },

  setDrawer(open, tab) {
    set((s) => ({ drawerOpen: open, drawerTab: tab ?? s.drawerTab }))
  },

  togglePanel(side) {
    set((s) => ({ panelOpen: { ...s.panelOpen, [side]: !s.panelOpen[side] } }))
  },

  setLeftTab(tab) {
    set({ leftTab: tab })
  },

  startTemplate() {
    set((s) => ({
      draft: { restore: s.topology },
      topology: { version: SCHEMA_VERSION, name: '', nodes: [], edges: [] },
      loadSeq: s.loadSeq + 1,
      selectedNodeId: null,
      selectedEdgeId: null,
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
    set((s) => ({ draft: null, topology: draft.restore, loadSeq: s.loadSeq + 1, selectedNodeId: null, selectedEdgeId: null }))
  },

  async deleteTemplate(name) {
    const userTemplates = get().userTemplates.filter((t) => t.name !== name)
    if (userTemplates.length === get().userTemplates.length) return
    set({ userTemplates })
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `deleted template "${name}"` })
    await persistTemplates(userTemplates, () => backend.deleteUserTemplate(name))
  },

  async refreshRuns() {
    if (!hasBackend()) return
    try {
      set({ runs: await backend.listRuns() })
    } catch (err) {
      warn('tests', err)
    }
  },

  async startTest(scenario, params) {
    if (!hasBackend()) return
    try {
      const id = await backend.startTest(scenario, get().topology, params)
      set({ activeTest: { run_id: id, scenario, template: get().topology.name, phase: 'starting', elapsed_ms: 0, total_ms: 0 } })
      get().appendLog({ ts: Date.now(), level: 'info', source: 'test', message: `started ${scenario} on "${get().topology.name}" (${id})` })
      await get().refreshRuns()
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'test', message: errorMessage(err) })
    }
  },

  async abortTest() {
    const a = get().activeTest
    if (!a || !hasBackend()) return
    try {
      await backend.abortTest(a.run_id)
    } catch (err) {
      warn('test', err)
    }
  },

  async viewRun(id) {
    if (!id) {
      set({ openRun: null })
      return
    }
    try {
      set({ openRun: await backend.getRun(id) })
    } catch (err) {
      warn('tests', err)
    }
  },

  async deleteRun(id) {
    try {
      await backend.deleteRun(id)
      set((s) => ({
        openRun: s.openRun?.id === id ? null : s.openRun,
        compareIds: s.compareIds.filter((c) => c !== id),
        compareRuns: s.compareRuns.filter((r) => r.id !== id),
      }))
      await get().refreshRuns()
    } catch (err) {
      warn('tests', err)
    }
  },

  async toggleCompare(id) {
    const s = get()
    if (s.compareIds.includes(id)) {
      set({ compareIds: s.compareIds.filter((c) => c !== id), compareRuns: s.compareRuns.filter((r) => r.id !== id) })
      return
    }
    try {
      const run = await backend.getRun(id)
      set((st) => ({ compareIds: [...st.compareIds, id].slice(-4), compareRuns: [...st.compareRuns, run].slice(-4) }))
    } catch (err) {
      warn('tests', err)
    }
  },
}))

/** Selector helper: the currently selected node, or null. */
export function selectSelectedNode(s: SymphonyState): TopologyNode | null {
  if (!s.selectedNodeId) return null
  return s.topology.nodes.find((n) => n.id === s.selectedNodeId) ?? null
}

/** The currently selected edge, or null. */
export function selectSelectedEdge(s: SymphonyState): TopologyEdge | null {
  if (!s.selectedEdgeId) return null
  return s.topology.edges.find((e) => e.id === s.selectedEdgeId) ?? null
}

/** The deployment of the topology on the canvas, if the engine is running it. */
function selectDeployment(s: SymphonyState): Deployment | undefined {
  return s.deployments[s.topology.name]
}

/** True when the canvas is showing a topology the engine has applied. */
export function selectIsApplied(s: SymphonyState): boolean {
  return s.topology.name in s.deployments
}

export function selectStatuses(s: SymphonyState): Record<string, NodeStatus> {
  return selectDeployment(s)?.statuses ?? NO_STATUSES
}

export function selectLinks(s: SymphonyState): Record<string, LinkHealthEntry> {
  return selectDeployment(s)?.links ?? NO_LINKS
}

export function selectContainers(s: SymphonyState): Record<string, ContainerStatsEvent> {
  return selectDeployment(s)?.containers ?? NO_CONTAINERS
}

export function selectSimHistory(s: SymphonyState): SimSample[] {
  return s.simHistory[s.topology.name] ?? NO_SAMPLES
}

/** Metrics of the canvas topology when applied, else the all-deployments sample. */
export function selectMetrics(s: SymphonyState): MetricsEvent | null {
  return selectDeployment(s)?.metrics ?? (selectIsApplied(s) ? null : s.metrics)
}

export function selectPhase(s: SymphonyState): Phase {
  return selectDeployment(s)?.phase ?? 'idle'
}

export function selectDockerUp(s: SymphonyState): boolean {
  return s.runtime?.available === true
}

export function selectBusy(s: SymphonyState): boolean {
  return selectDeployment(s)?.busy ?? false
}

/** Whether the canvas topology has a gateway node. */
export function selectHasGateway(s: SymphonyState): boolean {
  return s.topology.nodes.some((n) => n.kind === 'infra')
}
