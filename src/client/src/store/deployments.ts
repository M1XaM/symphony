import type { ContainerStatsEvent, DeploymentInfo, Phase } from '../types/topology'
import type { Deployment, SimSample, SymphonyState } from './types'

/** Host samples kept for the Stats tab: one per second. */
export const HOST_STATS_CAP = 180

export function newDeployment(name: string, phase: Phase = 'idle'): Deployment {
  return { name, phase, statuses: {}, links: {}, busy: false, containers: {} }
}

/** Return a copy of the deployments map with one entry patched, creating it
 *  when the engine mentions a deployment the webview has not seen. */
export function patchDeployment(
  deployments: Record<string, Deployment>,
  name: string,
  patch: (d: Deployment) => Partial<Deployment>,
): Record<string, Deployment> {
  const cur = deployments[name] ?? newDeployment(name)
  return { ...deployments, [name]: { ...cur, ...patch(cur) } }
}

export function freshDeployment(d: DeploymentInfo): Deployment {
  return { ...newDeployment(d.name, d.phase), statuses: d.statuses }
}

export function dropDeployment(deployments: Record<string, Deployment>, name: string): Record<string, Deployment> {
  if (!(name in deployments)) return deployments
  const next = { ...deployments }
  delete next[name]
  return next
}

/** Drop a gone deployment's chart samples with it, so re-applying the same
 *  template starts from an empty chart instead of the last run's tail. */
export function dropHistory(history: Record<string, SimSample[]>, name: string): Record<string, SimSample[]> {
  if (!(name in history)) return history
  const next = { ...history }
  delete next[name]
  return next
}

/** Take one container's stats sample into its deployment: the node's latest
 *  sample (replicas summed) and the deployment's per-second totals. */
export function foldContainerStats(
  s: Pick<SymphonyState, 'deployments' | 'simHistory'>,
  c: ContainerStatsEvent,
): Partial<SymphonyState> {
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
}
