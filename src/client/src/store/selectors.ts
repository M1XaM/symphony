import type { ContainerStatsEvent, LinkHealthEntry, MetricsEvent, NodeStatus, Phase, TopologyEdge, TopologyNode } from '../types/topology'
import type { Deployment, SimSample, SymphonyState } from './types'

/** Shared empty maps, so selectors on a non-applied topology keep a stable reference. */
const NO_STATUSES: Record<string, NodeStatus> = {}
const NO_LINKS: Record<string, LinkHealthEntry> = {}
const NO_CONTAINERS: Record<string, ContainerStatsEvent> = {}
const NO_SAMPLES: SimSample[] = []

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
