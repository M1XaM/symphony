import { describe, expect, it } from 'vitest'
import type { Topology } from '../types/topology'
import { generateTopology } from './generate'

/** A small seeded generator, so a failing topology can be reproduced. */
function seeded(seed: number): () => number {
  let s = seed >>> 0
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

/** The rules spec.Validate applies that a generated topology could break. */
function problems(t: Topology): string[] {
  const out: string[] = []
  const kind = new Map(t.nodes.map((n) => [n.id, n.kind]))
  if (kind.size !== t.nodes.length) out.push('duplicate node id')
  const ids = new Set<string>()
  const produced = new Set<string>()
  const consumed = new Set<string>()
  for (const e of t.edges) {
    if (ids.has(e.id)) out.push(`duplicate edge id ${e.id}`)
    ids.add(e.id)
    const from = kind.get(e.from)
    const to = kind.get(e.to)
    if (!from || !to) out.push(`${e.id}: unknown node`)
    if (e.from === e.to) out.push(`${e.id}: self edge`)
    if (e.kind === 'publishes') {
      if (to !== 'broker' && to !== 'proxy') out.push(`${e.id}: publishes to ${to}`)
      if (!e.topic) out.push(`${e.id}: no topic`)
      produced.add(e.topic ?? '')
    }
    if (e.kind === 'subscribes') {
      if (from !== 'broker' && from !== 'proxy') out.push(`${e.id}: subscribes from ${from}`)
      if (!e.topic) out.push(`${e.id}: no topic`)
      consumed.add(e.topic ?? '')
    }
    if (e.kind === 'calls' && (to === 'db' || to === 'broker')) out.push(`${e.id}: calls a ${to}`)
    if (e.kind === 'uses' && to !== 'db') out.push(`${e.id}: uses a ${to}`)
  }
  for (const topic of produced) if (!consumed.has(topic)) out.push(`${topic}: no subscriber`)
  for (const topic of consumed) if (!produced.has(topic)) out.push(`${topic}: no producer`)
  // every node but the gateway is reached by something
  for (const n of t.nodes) if (n.kind !== 'infra' && !t.edges.some((e) => e.to === n.id)) out.push(`${n.id}: nothing reaches it`)
  return out
}

describe('generateTopology', () => {
  it('is valid at every step of the scale', () => {
    for (let step = 0; step <= 50; step++) {
      for (let seed = 1; seed <= 40; seed++) {
        const t = generateTopology(step / 50, 'x', seeded(step * 1000 + seed))
        expect(problems(t), `step ${step} seed ${seed}`).toEqual([])
        expect(t.nodes.filter((n) => n.kind === 'infra')).toHaveLength(1)
      }
    }
  })

  it('is all calls at one end and all events at the other', () => {
    for (let seed = 1; seed <= 40; seed++) {
      const orch = generateTopology(0, 'x', seeded(seed))
      expect(orch.nodes.some((n) => n.kind === 'broker')).toBe(false)
      expect(orch.nodes.some((n) => n.id === 'orchestrator')).toBe(true)

      const chor = generateTopology(1, 'x', seeded(seed))
      expect(chor.nodes.some((n) => n.kind === 'broker')).toBe(true)
      // only the gateway calls: every step is fed by the broker
      expect(chor.edges.filter((e) => e.kind === 'calls').map((e) => e.from)).toEqual(['gateway'])
    }
  })

  it('keeps the name and gives no two nodes the same place', () => {
    const t = generateTopology(0.5, 'My saga', seeded(7))
    expect(t.name).toBe('My saga')
    const places = new Set(t.nodes.map((n) => `${n.position.x},${n.position.y}`))
    expect(places.size).toBe(t.nodes.length)
  })
})
