import { describe, expect, it } from 'vitest'
import type { Topology, TopologyNode } from '../types/topology'
import { asciiName, clone, inferEdge, uniqueId } from './editing'

function node(id: string, kind: TopologyNode['kind']): TopologyNode {
  return { id, kind, image: '', replicas: 1, position: { x: 0, y: 0 }, config: { hot: {}, cold: {} }, resources: {} }
}

describe('asciiName', () => {
  it('keeps English letters, digits, spaces and special characters', () => {
    expect(asciiName('Fan-out saga (v2) #1!')).toBe('Fan-out saga (v2) #1!')
  })

  it('drops everything else', () => {
    expect(asciiName('Сага café\t→ 2')).toBe(' caf 2')
  })
})

describe('uniqueId', () => {
  it('keeps a free name and numbers a taken one from 2', () => {
    const taken = new Set(['service', 'service-2'])
    expect(uniqueId('gateway', (id) => taken.has(id))).toBe('gateway')
    expect(uniqueId('service', (id) => taken.has(id))).toBe('service-3')
  })
})

describe('inferEdge', () => {
  const svc = node('order', 'service')
  const broker = node('kafka', 'broker')
  const db = node('postgres', 'db')
  const proxy = node('toxiproxy', 'proxy')
  const empty: Topology = { version: 2, name: 't', nodes: [svc, broker, db, proxy], edges: [] }

  it('reads the kind off the endpoints', () => {
    expect(inferEdge(empty, svc, node('payment', 'service'))).toEqual({ kind: 'calls' })
    expect(inferEdge(empty, svc, db)).toEqual({ kind: 'uses' })
    expect(inferEdge(empty, svc, broker)).toEqual({ kind: 'publishes', topic: 'order.events' })
    expect(inferEdge(empty, broker, svc)).toEqual({ kind: 'subscribes', topic: 'order.events' })
  })

  it('continues through a proxy whatever feeds it', () => {
    const fed: Topology = { ...empty, edges: [{ id: 'e1', from: 'kafka', to: 'toxiproxy', kind: 'subscribes', topic: 'order.created' }] }
    expect(inferEdge(fed, proxy, svc)).toEqual({ kind: 'subscribes', topic: 'order.created' })
    // nothing feeds it: a plain call
    expect(inferEdge(empty, proxy, svc)).toEqual({ kind: 'calls' })
  })
})

describe('clone', () => {
  it('copies deeply', () => {
    const a = { nodes: [{ id: 'x' }] }
    const b = clone(a)
    b.nodes[0].id = 'y'
    expect(a.nodes[0].id).toBe('x')
  })
})
