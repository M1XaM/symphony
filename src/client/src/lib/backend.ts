// Thin wrapper over the generated Wails bindings so components never import
// wailsjs directly and the class<->interface conversion happens in one place.
import * as API from '../../wailsjs/go/api/API'
import { control, spec } from '../../wailsjs/go/models'
import {
  fromWireTopology,
  normalizeRun,
  toWireFault,
  toWireTopology,
  type ContainerRow,
  type ControlCommand,
  type ControlResult,
  type DeploymentInfo,
  type ExportResult,
  type FaultSpec,
  type HostStatsEvent,
  type LogEvent,
  type Plan,
  type Resources,
  type Run,
  type RunSummary,
  type ReportFormat,
  type RuntimeStatus,
  type Scenario,
  type Topology,
  type ValidationError,
} from '../types/topology'

declare global {
  interface Window {
    go?: unknown
    runtime?: unknown
  }
}

/** True when running inside the Wails webview (bindings injected). */
export function hasBackend(): boolean {
  return typeof window !== 'undefined' && window.go !== undefined
}

export const backend = {
  getRuntimeStatus(): Promise<RuntimeStatus> {
    return API.GetRuntimeStatus() as unknown as Promise<RuntimeStatus>
  },
  async getDeployments(): Promise<DeploymentInfo[]> {
    const list = (await API.GetDeployments()) as unknown as DeploymentInfo[]
    return list.map((d) => ({
      name: d.name,
      phase: d.phase,
      network: d.network,
      topology: fromWireTopology(d.topology),
      statuses: d.statuses ?? {},
    }))
  },
  async getPresets(): Promise<Topology[]> {
    return (await API.GetPresets()).map(fromWireTopology)
  },
  async getUserTemplates(): Promise<Topology[]> {
    return (await API.GetUserTemplates()).map(fromWireTopology)
  },
  async saveUserTemplate(t: Topology): Promise<Topology[]> {
    return (await API.SaveUserTemplate(toWireTopology(t))).map(fromWireTopology)
  },
  async deleteUserTemplate(name: string): Promise<Topology[]> {
    return (await API.DeleteUserTemplate(name)).map(fromWireTopology)
  },
  /** Ask where to save a template, then write it there as JSON. */
  async exportTemplate(t: Topology): Promise<ExportResult> {
    const res = await API.ExportTemplate(toWireTopology(t))
    return { path: res.path, cancelled: res.cancelled }
  },
  async getLogHistory(): Promise<LogEvent[]> {
    const lines = await API.GetLogHistory()
    return lines.map((l) => ({
      level: l.level as LogEvent['level'],
      source: l.source,
      message: l.message,
      ts: l.ts,
    }))
  },
  validate(t: Topology): Promise<ValidationError[]> {
    return API.ValidateTopology(toWireTopology(t))
  },
  plan(t: Topology): Promise<Plan> {
    return API.PlanApply(toWireTopology(t))
  },
  apply(t: Topology): Promise<void> {
    return API.Apply(toWireTopology(t))
  },
  teardown(deployment: string): Promise<void> {
    return API.Teardown(deployment)
  },
  setHotConfig(deployment: string, nodeId: string, patch: Record<string, unknown>): Promise<void> {
    return API.SetHotConfig(deployment, nodeId, patch)
  },
  injectFault(deployment: string, nodeId: string, fault: FaultSpec): Promise<void> {
    return API.InjectFault(deployment, nodeId, toWireFault(fault))
  },
  getHostStats(): Promise<HostStatsEvent[]> {
    return API.GetHostStats() as unknown as Promise<HostStatsEvent[]>
  },
  setResources(deployment: string, nodeId: string, r: Resources): Promise<void> {
    return API.SetResources(deployment, nodeId, spec.Resources.createFrom(r))
  },
  clearFaults(deployment: string, nodeId: string): Promise<void> {
    return API.ClearFaults(deployment, nodeId)
  },
  async control(deployment: string, nodeId: string, cmd: ControlCommand): Promise<ControlResult> {
    const res = await API.ControlNode(deployment, nodeId, control.Command.createFrom(cmd))
    return JSON.parse(JSON.stringify(res)) as ControlResult
  },
  /** `docker ps`: every container on the host (all = `docker ps -a`). */
  async listContainers(all: boolean): Promise<ContainerRow[]> {
    const rows = await API.ListContainers(all)
    return JSON.parse(JSON.stringify(rows)) as ContainerRow[]
  },
  /** `docker logs`: what one of Symphony's containers has written. */
  async getContainerLogs(name: string): Promise<LogEvent[]> {
    const lines = await API.GetContainerLogs(name, 0)
    return lines.map((l) => ({ level: l.level as LogEvent['level'], source: l.source, message: l.message, ts: l.ts }))
  },
  async listScenarios(): Promise<Scenario[]> {
    return JSON.parse(JSON.stringify(await API.ListScenarios())) as Scenario[]
  },
  startTest(scenario: string, t: Topology, params: Record<string, unknown>): Promise<string> {
    return API.StartTest(scenario, toWireTopology(t), params)
  },
  abortTest(runId: string): Promise<void> {
    return API.AbortTest(runId)
  },
  async listRuns(): Promise<RunSummary[]> {
    return JSON.parse(JSON.stringify(await API.ListRuns())) as RunSummary[]
  },
  async getRun(runId: string): Promise<Run> {
    return normalizeRun(JSON.parse(JSON.stringify(await API.GetRun(runId))) as Run)
  },
  deleteRun(runId: string): Promise<void> {
    return API.DeleteRun(runId)
  },
  /** Render a run's report, then ask where to save it. */
  async exportRunReport(runId: string, format: ReportFormat): Promise<ExportResult> {
    const res = await API.ExportRunReport(runId, format)
    return { path: res.path, cancelled: res.cancelled }
  },
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}
