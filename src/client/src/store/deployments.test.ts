import { describe, expect, it } from 'vitest'
import type { ContainerStatsEvent } from '../types/topology'
import type { SymphonyState } from './types'
import { dropDeployment, foldContainerStats, newDeployment, patchDeployment } from './deployments'

function sample(container: string, node: string, ts: number, cpu: number, memMB: number): ContainerStatsEvent {
  return { deployment: 'demo', node_id: node, container, ts, cpu_pct: cpu, mem_used_bytes: memMB * 1048576, mem_limit_bytes: 0, net_rx_bps: 0, net_tx_bps: 0, throttled_periods: 0 }
}

describe('patchDeployment', () => {
  it('creates a deployment the webview has not seen and leaves the others alone', () => {
    const before = { a: newDeployment('a', 'running') }
    const after = patchDeployment(before, 'b', () => ({ busy: true }))
    expect(after.b).toMatchObject({ name: 'b', phase: 'idle', busy: true })
    expect(after.a).toBe(before.a)
    expect(before).not.toHaveProperty('b')
  })

  it('dropDeployment returns the same map when there is nothing to drop', () => {
    const map = { a: newDeployment('a') }
    expect(dropDeployment(map, 'nope')).toBe(map)
    expect(dropDeployment(map, 'a')).toEqual({})
  })
})

describe('foldContainerStats', () => {
  const start: Pick<SymphonyState, 'deployments' | 'simHistory'> = { deployments: { demo: newDeployment('demo', 'running') }, simHistory: {} }

  it('ignores a deployment that is gone', () => {
    expect(foldContainerStats({ deployments: {}, simHistory: {} }, sample('demo-a', 'a', 1000, 10, 100))).toEqual({})
  })

  it('sums the replicas of a node and the nodes of a deployment within a second', () => {
    let s = { ...start, ...foldContainerStats(start, sample('demo-pay-1', 'pay', 1000, 10, 100)) }
    s = { ...s, ...foldContainerStats(s, sample('demo-pay-2', 'pay', 1100, 20, 50)) }
    s = { ...s, ...foldContainerStats(s, sample('demo-inv', 'inv', 1200, 5, 30)) }
    expect(s.deployments.demo.containers.pay.cpu_pct).toBe(30)
    expect(s.deployments.demo.containers.pay.mem_used_bytes).toBe(150 * 1048576)
    // one point for the second, holding the totals so far
    expect(s.simHistory.demo).toEqual([{ ts: 1000, cpu_pct: 35, mem_mb: 180 }])
  })

  it('starts a new point each second', () => {
    let s = { ...start, ...foldContainerStats(start, sample('demo-a', 'a', 1000, 10, 100)) }
    s = { ...s, ...foldContainerStats(s, sample('demo-a', 'a', 2000, 40, 120)) }
    expect(s.simHistory.demo).toEqual([
      { ts: 1000, cpu_pct: 10, mem_mb: 100 },
      { ts: 2000, cpu_pct: 40, mem_mb: 120 },
    ])
  })
})
