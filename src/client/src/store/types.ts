import type { StateCreator } from 'zustand'
import type {
  ConfigValue,
  ContainerRow,
  ContainerStatsEvent,
  ControlCommand,
  ControlResult,
  FaultSpec,
  HostStatsEvent,
  LinkHealthEntry,
  LogEvent,
  MetricsEvent,
  NodeKind,
  NodeStatus,
  OpStatus,
  Phase,
  Plan,
  Position,
  Resources,
  Run,
  RunSummary,
  ReportFormat,
  RuntimeStatus,
  Scenario,
  TestProgressEvent,
  Topology,
  TopologyEdge,
  TopologyNode,
  ValidationError,
} from '../types/topology'

export interface LogLineEntry {
  id: number
  ts: number
  level: LogEvent['level']
  source: string
  message: string
}

export type DrawerTab = 'plan' | 'stats' | 'containers' | 'log' | 'tests' | 'runs'

/** The two floating panels that can be folded away to clear the map. */
export type PanelSide = 'left' | 'right'

/** The two halves of the left panel's slider. */
export type LeftTab = 'elements' | 'templates'

/** Set while the left panel is in its "build a template" state. */
export interface TemplateDraft {
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

/** The topology on the canvas and what is selected on it. */
export interface EditorSlice {
  topology: Topology
  /** bumps whenever a whole topology is swapped in, so the canvas re-fits */
  loadSeq: number
  selectedNodeId: string | null
  selectedEdgeId: string | null

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
}

/** What the engine runs and says, and the calls that drive it. */
export interface EngineSlice {
  initialized: boolean
  /** Whether the Docker daemon answers; null until the backend has said. */
  runtime: RuntimeStatus | null
  /** Everything the engine has applied, keyed by topology name. */
  deployments: Record<string, Deployment>
  plan: Plan | null
  opStatus: Record<string, OpStatus>
  validation: ValidationError[]
  /** metrics over every deployment (the collector's "" sample) */
  metrics: MetricsEvent | null
  hostStats: HostStatsEvent[]
  simHistory: Record<string, SimSample[]>

  init(): Promise<void>
  /** Re-read what the engine runs; `changed` names a deployment whose
   *  topology another process changed, which the map then shows as it is. */
  refreshDeployments(changed?: string): Promise<void>
  validate(): Promise<void>
  planApply(): Promise<void>
  apply(): Promise<void>
  teardown(): Promise<void>
  injectFault(id: string, fault: FaultSpec): Promise<void>
  clearFaults(id: string): Promise<void>
  control(id: string, cmd: ControlCommand): Promise<ControlResult | null>
}

/** The panels around the map and what their tabs show. */
export interface PanelsSlice {
  log: LogLineEntry[]
  drawerOpen: boolean
  drawerTab: DrawerTab
  /** Height of the open drawer in px; dragged by its top edge. */
  drawerHeight: number
  /** Folded-away side panels; only their reopen arrow stays on screen. */
  panelOpen: Record<PanelSide, boolean>
  leftTab: LeftTab
  /** Log tab: whose lines it shows. '' is the engine's own log ("System"),
   *  anything else the name of one of Symphony's containers. */
  logSource: string
  /** The containers the Log tab offers, from its own sweep. */
  logContainers: ContainerRow[]
  /** What the picked container has written, as `docker logs` has it. */
  containerLog: LogLineEntry[]
  containerLogError: string | null
  /** Containers tab: the last `docker ps` sweep. */
  containers: ContainerRow[]
  /** How many of them are running, kept with the rows so the tab badge is a
   *  field read rather than a scan on every store update. */
  containersRunning: number
  containersAll: boolean
  containersError: string | null

  setLogSource(name: string): void
  refreshLogContainers(): Promise<void>
  refreshContainerLog(): Promise<void>
  /** Open the Log tab on the first container of a node of the canvas topology. */
  showNodeLogs(nodeId: string): Promise<void>
  refreshContainers(): Promise<void>
  setContainersAll(all: boolean): void
  appendLog(line: Omit<LogLineEntry, 'id'>): void
  clearLog(): void
  setDrawer(open: boolean, tab?: DrawerTab): void
  setDrawerHeight(px: number): void
  togglePanel(side: PanelSide): void
  setLeftTab(tab: LeftTab): void
}

/** The templates: the built-in ones and the ones the user saved. */
export interface LibrarySlice {
  presets: Topology[]
  /** Templates the user built, stacked on top of the built-in `presets`. */
  userTemplates: Topology[]
  draft: TemplateDraft | null

  loadTemplate(name: string): void
  startTemplate(): void
  saveTemplate(): Promise<void>
  cancelTemplate(): void
  deleteTemplate(name: string): Promise<void>
  /** Write a template to a JSON file the user picks. */
  exportTemplate(name: string): Promise<void>
  /** While building a template: replace the canvas with a random topology
   *  that leans towards orchestration (0) or choreography (1). */
  generateDraft(bias: number): Promise<void>
  /** While building a template: put an exported template on the canvas. */
  importDraft(): Promise<void>
  /** Save what was changed on the canvas into the template it came from; a
   *  built-in one cannot be changed, so it is saved as a copy. */
  saveChanges(): Promise<void>
}

/** The Tests and Runs tabs. */
export interface TestsSlice {
  scenarios: Scenario[]
  runs: RunSummary[]
  activeTest: TestProgressEvent | null
  openRun: Run | null
  compareIds: string[]
  compareRuns: Run[]
  /** The run whose report is being written (rendered, then the save
   *  dialog), so its row can say so; null when none is. */
  exportingRun: string | null

  refreshRuns(): Promise<void>
  startTest(scenario: string, params: Record<string, unknown>): Promise<void>
  abortTest(): Promise<void>
  viewRun(id: string | null): Promise<void>
  deleteRun(id: string): Promise<void>
  toggleCompare(id: string): Promise<void>
  /** Write a run's report: render it, then ask where to save it. */
  exportRun(id: string, format: ReportFormat): Promise<void>
}

export type SymphonyState = EditorSlice & EngineSlice & PanelsSlice & LibrarySlice & TestsSlice

/** One part of the store: its fields and the actions over them. Every slice
 *  sees the whole state, so an action may touch what another slice owns. */
export type Slice<T> = StateCreator<SymphonyState, [], [], T>

export type Get = () => SymphonyState
export type Set = (partial: Partial<SymphonyState> | ((s: SymphonyState) => Partial<SymphonyState>)) => void
