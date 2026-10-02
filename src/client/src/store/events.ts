import { onEvent } from '../lib/events'
import type {
  ContainerStatsEvent,
  HostStatsEvent,
  LinkHealthEvent,
  LogEvent,
  MetricsEvent,
  NodeStatusEvent,
  Phase,
  PhaseEvent,
  ProgressEvent,
  RuntimeStatus,
  TestDoneEvent,
  TestProgressEvent,
} from '../types/topology'
import { queue } from './batch'
import { HOST_STATS_CAP, dropDeployment, dropHistory, foldContainerStats, patchDeployment } from './deployments'
import type { Get, LogLineEntry, Set } from './types'

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

/** Subscribe the store to everything the engine emits. The frequent events
 *  go through the batch queue; see batch.ts. */
export function wireEvents(set: Set, get: Get): void {
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
  onEvent<ContainerStatsEvent>('container:stats', (c) => queue((s) => foldContainerStats(s, c)))
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
}
