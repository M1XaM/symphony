import { DEFAULT_IMAGE, DEFAULT_RESOURCES, SCHEMA_VERSION, defaultConfig, type NodeKind, type Topology, type TopologyEdge, type TopologyNode } from '../types/topology'

/** What the saga's steps may be called. */
const STEP_NAMES = ['payment', 'inventory', 'shipping', 'billing', 'pricing', 'fraud', 'loyalty', 'notification']

const COL = 250
const ROW = 140

/** A random saga topology for the template builder. `bias` runs from 0 (every
 *  step is called by one orchestrator) to 1 (every step reacts to the events
 *  of the one before it, through a broker); in between each step is the one
 *  or the other with that probability, so the middle of the scale gives
 *  hybrids. How many steps, what they are called, whether there is a
 *  database, a proxy in front of a step or a second replica is drawn anew on
 *  every call.
 *
 *  The edges follow what the blocks derive their behaviour from (see
 *  blocks/common/behaviour): the entry service calls its orchestrated steps
 *  in order and publishes to the chain of choreographed ones, which ends in a
 *  sink or, when nothing is orchestrated, may come back to the entry. */
export function generateTopology(bias: number, name: string, rand: () => number = Math.random): Topology {
  const p = Math.min(1, Math.max(0, bias))
  const chance = (q: number) => rand() < q
  const pick = (n: number) => Math.min(n - 1, Math.floor(rand() * n))

  const pool = [...STEP_NAMES]
  const count = 2 + pick(4)
  const called: string[] = []
  const chained: string[] = []
  for (let i = 0; i < count; i++) {
    const step = pool.splice(pick(pool.length), 1)[0]
    if (chance(p)) chained.push(step)
    else called.push(step)
  }
  const steps = [...called, ...chained]
  const entry = chained.length === 0 ? 'orchestrator' : 'order'
  const broker = chained.length > 0 ? 'kafka' : null
  const db = chance(0.75) ? 'postgres' : null
  const proxied = chance(0.5) ? steps[pick(steps.length)] : null
  const doubled = chance(0.5) ? steps[pick(steps.length)] : null
  // with nothing orchestrated the last event may close the saga at the entry
  const loops = called.length === 0 && chance(0.5)

  // columns, left to right: gateway, entry, broker, proxy, steps
  let col = 2
  const brokerX = 40 + COL * col
  if (broker) col++
  const proxyX = 40 + COL * col
  if (proxied) col++
  const stepX = 40 + COL * col
  const stepY = (s: string) => 40 + ROW * steps.indexOf(s)
  const midY = 40 + (ROW * (steps.length - 1)) / 2

  const nodes: TopologyNode[] = []
  const add = (id: string, kind: NodeKind, x: number, y: number, replicas = 1) => {
    nodes.push({
      id,
      kind,
      image: DEFAULT_IMAGE[kind],
      replicas,
      position: { x: Math.round(x), y: Math.round(y) },
      config: defaultConfig(kind),
      resources: { ...DEFAULT_RESOURCES[kind] },
    })
  }
  add('gateway', 'infra', 40, midY)
  add(entry, 'service', 40 + COL, midY)
  if (broker) add(broker, 'broker', brokerX, 40 + ROW * (called.length + (chained.length - 1) / 2))
  if (proxied) add('toxiproxy', 'proxy', proxyX, stepY(proxied) - 50)
  for (const s of steps) add(s, 'service', stepX, stepY(s), s === doubled ? 2 : 1)
  if (db) add(db, 'db', 40 + COL, midY + ROW + 60)

  const edges: TopologyEdge[] = []
  const link = (from: string, to: string, kind: TopologyEdge['kind'], topic?: string) => {
    const edge: TopologyEdge = { id: `e-${from}-${to}${kind === 'publishes' || kind === 'subscribes' ? `-${kind.slice(0, 3)}` : ''}`, from, to, kind }
    if (topic) edge.topic = topic
    edges.push(edge)
  }
  /** An edge into a step, through the proxy when the step has one. */
  const into = (from: string, step: string, kind: 'calls' | 'subscribes', topic?: string) => {
    if (step === proxied) {
      link(from, 'toxiproxy', kind, topic)
      link('toxiproxy', step, kind, topic)
    } else {
      link(from, step, kind, topic)
    }
  }

  link('gateway', entry, 'calls')
  for (const s of called) into(entry, s, 'calls')
  if (broker) {
    let topic = `${entry}.created`
    link(entry, broker, 'publishes', topic)
    chained.forEach((s, i) => {
      into(broker, s, 'subscribes', topic)
      const last = i === chained.length - 1
      if (last && !loops) return
      topic = `${s}.done`
      link(s, broker, 'publishes', topic)
      if (last) link(broker, entry, 'subscribes', topic)
    })
  }
  if (db) {
    link(entry, db, 'uses')
    // a called step may keep its own state there too
    for (const s of called) if (chance(0.25)) link(s, db, 'uses')
  }

  return { version: SCHEMA_VERSION, name, nodes, edges }
}
