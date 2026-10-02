import { backend, errorMessage, hasBackend } from '../../lib/backend'
import { DEFAULT_IMAGE, DEFAULT_RESOURCES, EMPTY_TOPOLOGY, defaultConfig, type ConfigValue, type TopologyEdge, type TopologyNode } from '../../types/topology'
import { warn } from '../batch'
import { inferEdge, uniqueId } from '../editing'
import { selectIsApplied } from '../selectors'
import type { EditorSlice, Slice } from '../types'

/** A slider or a typed number changes many times a second; the running
 *  container is told once it settles. */
const HOT_DEBOUNCE_MS = 150
const RESOURCES_DEBOUNCE_MS = 400

const hotTimers = new Map<string, ReturnType<typeof setTimeout>>()
const hotPending = new Map<string, Record<string, ConfigValue>>()
const resTimers = new Map<string, ReturnType<typeof setTimeout>>()

export const createEditorSlice: Slice<EditorSlice> = (set, get) => ({
  topology: EMPTY_TOPOLOGY,
  loadSeq: 0,
  selectedNodeId: null,
  selectedEdgeId: null,

  setName(name) {
    if (selectIsApplied(get())) return
    set((s) => ({ topology: { ...s.topology, name } }))
  },

  addNode(kind, position) {
    set((s) => {
      const taken = (id: string) => s.topology.nodes.some((n) => n.id === id)
      const id = uniqueId(kind === 'infra' ? 'gateway' : kind, taken)
      const node: TopologyNode = {
        id,
        kind,
        image: DEFAULT_IMAGE[kind],
        replicas: 1,
        position: { x: Math.round(position.x), y: Math.round(position.y) },
        config: defaultConfig(kind),
        resources: { ...DEFAULT_RESOURCES[kind] },
      }
      return { topology: { ...s.topology, nodes: [...s.topology.nodes, node] }, selectedNodeId: id, selectedEdgeId: null }
    })
  },

  moveNode(id, position) {
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.map((n) => (n.id === id ? { ...n, position } : n)),
      },
    }))
  },

  removeNodes(ids) {
    if (ids.length === 0) return
    const gone = new Set(ids)
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.filter((n) => !gone.has(n.id)),
        edges: s.topology.edges.filter((e) => !gone.has(e.from) && !gone.has(e.to) && !(e.via && gone.has(e.via))),
      },
      selectedNodeId: s.selectedNodeId && gone.has(s.selectedNodeId) ? null : s.selectedNodeId,
    }))
  },

  updateNode(id, patch) {
    set((s) => ({
      topology: { ...s.topology, nodes: s.topology.nodes.map((n) => (n.id === id ? { ...n, ...patch } : n)) },
    }))
  },

  setColdConfig(id, key, value) {
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.map((n) =>
          n.id === id ? { ...n, config: { ...n.config, cold: { ...n.config.cold, [key]: value } } } : n,
        ),
      },
    }))
  },

  setHotConfig(id, key, value) {
    set((s) => ({
      topology: {
        ...s.topology,
        nodes: s.topology.nodes.map((n) =>
          n.id === id ? { ...n, config: { ...n.config, hot: { ...n.config.hot, [key]: value } } } : n,
        ),
      },
    }))
    const pending = hotPending.get(id) ?? {}
    pending[key] = value
    hotPending.set(id, pending)
    const existing = hotTimers.get(id)
    if (existing) clearTimeout(existing)
    hotTimers.set(
      id,
      setTimeout(async () => {
        hotTimers.delete(id)
        const patch = hotPending.get(id)
        hotPending.delete(id)
        if (!patch || !hasBackend() || !selectIsApplied(get())) return
        try {
          await backend.setHotConfig(get().topology.name, id, patch)
        } catch (err) {
          get().appendLog({
            ts: Date.now(),
            level: 'warn',
            source: 'control',
            message: `${id}: ${errorMessage(err)} — change kept locally, applied on next Apply`,
          })
        }
      }, HOT_DEBOUNCE_MS),
    )
  },

  setResources(id, r) {
    set((s) => ({
      topology: { ...s.topology, nodes: s.topology.nodes.map((n) => (n.id === id ? { ...n, resources: { ...n.resources, ...r } } : n)) },
    }))
    const existing = resTimers.get(id)
    if (existing) clearTimeout(existing)
    resTimers.set(
      id,
      setTimeout(async () => {
        resTimers.delete(id)
        if (!hasBackend() || !selectIsApplied(get())) return
        const node = get().topology.nodes.find((n) => n.id === id)
        if (!node) return
        try {
          await backend.setResources(get().topology.name, id, node.resources)
        } catch (err) {
          warn(get, 'runtime', err)
        }
      }, RESOURCES_DEBOUNCE_MS),
    )
  },

  addEdge(from, to) {
    set((s) => {
      const a = s.topology.nodes.find((n) => n.id === from)
      const b = s.topology.nodes.find((n) => n.id === to)
      if (!a || !b || from === to) return {}
      if (s.topology.edges.some((e) => e.from === from && e.to === to)) return {}
      const { kind, topic } = inferEdge(s.topology, a, b)
      const taken = (id: string) => s.topology.edges.some((e) => e.id === id)
      const edge: TopologyEdge = { id: uniqueId(`e-${from}-${to}`, taken), from, to, kind }
      if (topic) edge.topic = topic
      return { topology: { ...s.topology, edges: [...s.topology.edges, edge] } }
    })
  },

  updateEdge(id, patch) {
    set((s) => ({
      topology: { ...s.topology, edges: s.topology.edges.map((e) => (e.id === id ? { ...e, ...patch } : e)) },
    }))
  },

  removeEdges(ids) {
    if (ids.length === 0) return
    const gone = new Set(ids)
    set((s) => ({
      topology: { ...s.topology, edges: s.topology.edges.filter((e) => !gone.has(e.id)) },
      selectedEdgeId: s.selectedEdgeId && gone.has(s.selectedEdgeId) ? null : s.selectedEdgeId,
    }))
  },

  select(id) {
    if (get().selectedNodeId !== id) set({ selectedNodeId: id, selectedEdgeId: id ? null : get().selectedEdgeId })
  },

  selectEdge(id) {
    if (get().selectedEdgeId !== id) set({ selectedEdgeId: id, selectedNodeId: id ? null : get().selectedNodeId })
  },
})
