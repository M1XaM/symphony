// Thin wrapper over the generated Wails bindings so components never import
// wailsjs directly and the class<->interface conversion happens in one place.
import * as API from '../../wailsjs/go/api/API'
import {
  fromWireTopology,
  toWireFault,
  toWireTopology,
  type FaultSpec,
  type NodeStatus,
  type Phase,
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
  async getTopology(): Promise<Topology> {
    return fromWireTopology(await API.GetTopology())
  },
  async getPresets(): Promise<Topology[]> {
    return (await API.GetPresets()).map(fromWireTopology)
  },
  getPhase(): Promise<Phase> {
    return API.GetPhase() as Promise<Phase>
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
  teardown(): Promise<void> {
    return API.Teardown()
  },
  setHotConfig(nodeId: string, patch: Record<string, unknown>): Promise<void> {
    return API.SetHotConfig(nodeId, patch)
  },
  injectFault(nodeId: string, fault: FaultSpec): Promise<void> {
    return API.InjectFault(nodeId, toWireFault(fault))
  },
  getNodeStatus(): Promise<Record<string, NodeStatus>> {
    return API.GetNodeStatus() as Promise<Record<string, NodeStatus>>
  },
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}
