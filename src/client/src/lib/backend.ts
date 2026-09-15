// Thin wrapper over the generated Wails bindings so components never import
// wailsjs directly and the class<->interface conversion happens in one place.
import * as API from '../../wailsjs/go/api/API'
import {
  fromWireTopology,
  toWireFault,
  toWireTopology,
  type DeploymentInfo,
  type FaultSpec,
  type HostStatsEvent,
  type LogEvent,
  type Plan,
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
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}
