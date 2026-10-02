import type { Topology, TopologyEdge, TopologyNode } from '../types/topology'

export function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** Guess what a new edge means from its endpoints. A proxy passes traffic
 *  through, so an edge leaving one continues whatever feeds it. */
export function inferEdge(t: Topology, from: TopologyNode, to: TopologyNode): Pick<TopologyEdge, 'kind' | 'topic'> {
  if (from.kind === 'broker') return { kind: 'subscribes', topic: `${to.id}.events` }
  if (from.kind === 'proxy') {
    const feed = t.edges.find((e) => e.to === from.id && e.kind === 'subscribes')
    if (feed) return { kind: 'subscribes', topic: feed.topic }
  }
  if (to.kind === 'broker') return { kind: 'publishes', topic: `${from.id}.events` }
  if (to.kind === 'proxy') {
    const feed = t.edges.find((e) => e.from === to.id && e.kind === 'publishes')
    if (feed) return { kind: 'publishes', topic: feed.topic }
  }
  if (to.kind === 'db') return { kind: 'uses' }
  return { kind: 'calls' }
}

/** `base`, or `base-2`, `base-3`, ... : the first that is not taken. */
export function uniqueId(base: string, taken: (id: string) => boolean): string {
  if (!taken(base)) return base
  for (let i = 2; ; i++) {
    const id = `${base}-${i}`
    if (!taken(id)) return id
  }
}
