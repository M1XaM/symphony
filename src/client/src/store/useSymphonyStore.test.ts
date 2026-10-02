import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { EMPTY_TOPOLOGY } from '../types/topology'
import { newDeployment } from './deployments'
import { selectIsApplied, selectPhase, selectSelectedNode, selectStatuses, useSymphonyStore } from './useSymphonyStore'

const initial = useSymphonyStore.getState()

beforeEach(() => {
  vi.useFakeTimers()
  useSymphonyStore.setState({ ...initial, topology: { ...EMPTY_TOPOLOGY, name: 'demo' } }, true)
})

afterEach(() => {
  vi.runOnlyPendingTimers()
  vi.useRealTimers()
})

describe('editing the topology', () => {
  it('names nodes, infers edges and selects what was added', () => {
    const s = useSymphonyStore.getState()
    s.addNode('infra', { x: 10.4, y: 20.6 })
    s.addNode('service', { x: 0, y: 0 })
    s.addNode('service', { x: 0, y: 0 })
    s.addNode('db', { x: 0, y: 0 })
    s.addEdge('gateway', 'service')
    s.addEdge('service', 'db')
    s.addEdge('service', 'db') // a second edge between the same two is refused
    s.addEdge('service', 'service')

    const t = useSymphonyStore.getState().topology
    expect(t.nodes.map((n) => n.id)).toEqual(['gateway', 'service', 'service-2', 'db'])
    expect(t.nodes[0].position).toEqual({ x: 10, y: 21 })
    expect(t.edges).toEqual([
      { id: 'e-gateway-service', from: 'gateway', to: 'service', kind: 'calls' },
      { id: 'e-service-db', from: 'service', to: 'db', kind: 'uses' },
    ])
    expect(selectSelectedNode(useSymphonyStore.getState())?.id).toBe('db')
  })

  it('removes a node with its edges and its selection', () => {
    const s = useSymphonyStore.getState()
    s.addNode('service', { x: 0, y: 0 })
    s.addNode('db', { x: 0, y: 0 })
    s.addEdge('service', 'db')
    s.select('db')
    s.removeNodes(['db'])
    const after = useSymphonyStore.getState()
    expect(after.topology.nodes.map((n) => n.id)).toEqual(['service'])
    expect(after.topology.edges).toEqual([])
    expect(after.selectedNodeId).toBeNull()
  })

  it('selects a node or an edge, never both', () => {
    const s = useSymphonyStore.getState()
    s.select('a')
    s.selectEdge('e1')
    expect(useSymphonyStore.getState()).toMatchObject({ selectedNodeId: null, selectedEdgeId: 'e1' })
    s.select('a')
    expect(useSymphonyStore.getState()).toMatchObject({ selectedNodeId: 'a', selectedEdgeId: null })
  })

  it('does not rename a topology the engine is running', () => {
    useSymphonyStore.setState({ deployments: { demo: newDeployment('demo', 'running') } })
    useSymphonyStore.getState().setName('other')
    expect(useSymphonyStore.getState().topology.name).toBe('demo')
  })
})

describe('templates', () => {
  it('loads a copy of a preset and clears what belonged to the last one', () => {
    const preset = { ...EMPTY_TOPOLOGY, name: 'Preset', nodes: [], edges: [] }
    useSymphonyStore.setState({ presets: [preset], selectedNodeId: 'x', validation: [{ path: 'p', message: 'm', severity: 'error' }] })
    useSymphonyStore.getState().loadTemplate('Preset')
    const s = useSymphonyStore.getState()
    expect(s.topology).toEqual(preset)
    expect(s.topology).not.toBe(preset)
    expect(s).toMatchObject({ selectedNodeId: null, validation: [], plan: null, loadSeq: initial.loadSeq + 1 })
  })

  it('builds a template and puts the canvas back when cancelled', () => {
    const before = useSymphonyStore.getState().topology
    useSymphonyStore.getState().startTemplate()
    expect(useSymphonyStore.getState().topology.name).toBe('')
    useSymphonyStore.getState().cancelTemplate()
    expect(useSymphonyStore.getState().topology).toBe(before)
    expect(useSymphonyStore.getState().draft).toBeNull()
  })
})

describe('the log', () => {
  it('collects lines and shows them together on the next flush', () => {
    const s = useSymphonyStore.getState()
    s.appendLog({ ts: 1, level: 'info', source: 'ui', message: 'one' })
    s.appendLog({ ts: 2, level: 'warn', source: 'ui', message: 'two' })
    expect(useSymphonyStore.getState().log).toEqual([])
    vi.advanceTimersByTime(100)
    const log = useSymphonyStore.getState().log
    expect(log.map((l) => l.message)).toEqual(['one', 'two'])
    expect(log[1].id).toBeGreaterThan(log[0].id)
    useSymphonyStore.getState().clearLog()
    expect(useSymphonyStore.getState().log).toEqual([])
  })
})

describe('selectors', () => {
  it('describe the deployment of the topology on the canvas', () => {
    expect(selectIsApplied(useSymphonyStore.getState())).toBe(false)
    expect(selectPhase(useSymphonyStore.getState())).toBe('idle')
    const empty = selectStatuses(useSymphonyStore.getState())
    expect(selectStatuses(useSymphonyStore.getState())).toBe(empty) // a stable reference

    useSymphonyStore.setState({ deployments: { demo: newDeployment('demo', 'running') } })
    expect(selectIsApplied(useSymphonyStore.getState())).toBe(true)
    expect(selectPhase(useSymphonyStore.getState())).toBe('running')
  })
})
