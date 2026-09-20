import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  useReactFlow,
  type Connection,
  type Edge,
  type EdgeChange,
  type NodeChange,
  type NodeTypes,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { useCallback, useEffect, useMemo, useRef, type DragEvent } from 'react'
import { selectIsApplied, selectLinks, selectStatuses, useSymphonyStore } from '../store/useSymphonyStore'
import type { LinkLevel, NodeKind } from '../types/topology'
import { TopologyNodeView, type RFNode } from './nodes/TopologyNodeView'

export const DRAG_MIME = 'application/symphony-node'

const MIN_ZOOM = 0.3
const MAX_ZOOM = 2

/** Edge colour by link health. Not applied: grey. Applied: green while the
 *  link loses fewer requests than the warn threshold, yellow above it, red
 *  above the critical one (thresholds live in engine/metrics). The arrow
 *  head takes the same colour; the stroke itself comes from CSS. */
type EdgeLevel = LinkLevel | 'idle'
const MARKER_COLOR: Record<EdgeLevel, string> = {
  idle: '#4a4f5a',
  ok: '#3fb950',
  warn: '#d29922',
  crit: '#f85149',
}

/** How much of the pane each floating panel covers, measured from the live
 *  layout so the CSS stays the single source of truth. */
function panelInsets() {
  const gap = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--gap')) || 10
  const box = (sel: string) => document.querySelector(sel)?.getBoundingClientRect()
  return {
    left: gap * 2 + (box('.panel-left')?.width ?? 0),
    right: gap * 2 + (box('.panel-right')?.width ?? 0),
    top: gap * 2 + (box('.panel-top')?.height ?? 0),
    bottom: gap * 2 + (box('.panel-bottom')?.height ?? 0),
  }
}

const nodeTypes: NodeTypes = {
  service: TopologyNodeView,
  broker: TopologyNodeView,
  db: TopologyNodeView,
  proxy: TopologyNodeView,
  infra: TopologyNodeView,
}

export function Canvas() {
  const topology = useSymphonyStore((s) => s.topology)
  const statuses = useSymphonyStore(selectStatuses)
  const links = useSymphonyStore(selectLinks)
  const applied = useSymphonyStore(selectIsApplied)
  const selectedNodeId = useSymphonyStore((s) => s.selectedNodeId)
  const selectedEdgeId = useSymphonyStore((s) => s.selectedEdgeId)
  const selectEdge = useSymphonyStore((s) => s.selectEdge)
  const loadSeq = useSymphonyStore((s) => s.loadSeq)
  const moveNode = useSymphonyStore((s) => s.moveNode)
  const removeNodes = useSymphonyStore((s) => s.removeNodes)
  const removeEdges = useSymphonyStore((s) => s.removeEdges)
  const addEdge = useSymphonyStore((s) => s.addEdge)
  const addNode = useSymphonyStore((s) => s.addNode)
  const select = useSymphonyStore((s) => s.select)
  const { screenToFlowPosition, fitView, setViewport } = useReactFlow()
  const paneRef = useRef<HTMLDivElement>(null)

  // Mark the whole app while the map is being moved, so the expensive chrome
  // (panel backdrop blur, the live-edge dash) steps aside for the duration.
  // Toggled straight on the DOM: a re-render per gesture is exactly what the
  // frames being dropped cannot afford.
  const moving = useRef(0)
  const setMoving = useCallback((on: boolean) => {
    moving.current = Math.max(0, moving.current + (on ? 1 : -1))
    paneRef.current?.closest('.app')?.classList.toggle('is-moving', moving.current > 0)
  }, [])
  const onMoveStart = useCallback(() => setMoving(true), [setMoving])
  const onMoveEnd = useCallback(() => setMoving(false), [setMoving])
  useEffect(() => () => paneRef.current?.closest('.app')?.classList.remove('is-moving'), [])

  // React Flow re-renders a node whenever its object changes, so hand back the
  // very same object when nothing about that node did. Without this, one
  // status tick — or one frame of a drag — re-rendered every node on the map.
  const nodeCache = useRef(new Map<string, RFNode>())
  const nodes = useMemo<RFNode[]>(() => {
    const prev = nodeCache.current
    const next = new Map<string, RFNode>()
    const out = topology.nodes.map((n) => {
      const status = statuses[n.id]
      const selected = n.id === selectedNodeId
      const cached = prev.get(n.id)
      const node =
        cached && cached.data.node === n && cached.data.status === status && cached.selected === selected
          ? cached
          : { id: n.id, type: n.kind, position: n.position, selected, data: { node: n, status } }
      next.set(n.id, node)
      return node
    })
    nodeCache.current = next
    return out
  }, [topology.nodes, statuses, selectedNodeId])

  // Same for the edges, which otherwise churn twice a second on link health.
  const edgeCache = useRef(new Map<string, Edge>())
  const edges = useMemo<Edge[]>(() => {
    const prev = edgeCache.current
    const next = new Map<string, Edge>()
    const out = topology.edges.map((e) => {
      const health = applied ? links[e.id] : undefined
      const level: EdgeLevel = health?.level ?? 'idle'
      let label = e.via ? `${e.topic ?? e.kind} via ${e.via}` : e.topic
      if (health && level !== 'ok') {
        const err = `${Math.round(health.error_rate * 100)}% lost`
        label = label ? `${label} · ${err}` : err
      }
      const selected = e.id === selectedEdgeId
      const className = `tedge tedge-${e.kind} tedge-${level}${e.via ? ' tedge-via' : ''}${health && health.rps ? ' tedge-live' : ''}`
      const cached = prev.get(e.id)
      const edge: Edge =
        cached && cached.data?.edge === e && cached.className === className && cached.label === label && cached.selected === selected
          ? cached
          : {
              id: e.id,
              source: e.from,
              target: e.to,
              type: 'smoothstep',
              label,
              selected,
              className,
              markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: MARKER_COLOR[level] },
              data: { kind: e.kind, via: e.via, level, edge: e },
            }
      next.set(e.id, edge)
      return edge
    })
    edgeCache.current = next
    return out
  }, [topology.edges, links, applied, selectedEdgeId])

  /** Fit the graph into the band the floating panels leave clear. Used on load
   *  and by the controls' fit button, which would otherwise centre on the whole
   *  pane and tuck half the graph under a panel. */
  const fitToClearArea = useCallback(() => {
    const pane = paneRef.current
    if (!pane) return
    // Never let the insets eat the whole viewport on a small window.
    const inset = panelInsets()
    const maxX = pane.clientWidth * 0.4
    const maxY = pane.clientHeight * 0.4
    const pad = {
      left: Math.min(inset.left + 16, maxX),
      right: Math.min(inset.right + 16, maxX),
      top: Math.min(inset.top + 16, maxY),
      bottom: Math.min(inset.bottom + 16, maxY),
    }

    if (useSymphonyStore.getState().topology.nodes.length === 0) {
      // Nothing to fit (a fresh template): park the origin in the clear area so
      // dropped nodes land in view with sane coordinates.
      setViewport({ x: pad.left + 32, y: pad.top + 32, zoom: 1 })
      return
    }

    fitView({
      padding: {
        left: `${pad.left}px`,
        right: `${pad.right}px`,
        top: `${pad.top}px`,
        bottom: `${pad.bottom}px`,
      },
      duration: 0,
    })
  }, [fitView, setViewport])

  useEffect(() => {
    const id = window.setTimeout(fitToClearArea, 30)
    return () => window.clearTimeout(id)
  }, [loadSeq, fitToClearArea])

  const onNodesChange = useCallback(
    (changes: NodeChange<RFNode>[]) => {
      const removed: string[] = []
      for (const c of changes) {
        switch (c.type) {
          case 'position':
            if (c.position) moveNode(c.id, c.position)
            break
          case 'select':
            if (c.selected) select(c.id)
            else if (useSymphonyStore.getState().selectedNodeId === c.id) select(null)
            break
          case 'remove':
            removed.push(c.id)
            break
          default:
            break
        }
      }
      if (removed.length) removeNodes(removed)
    },
    [moveNode, removeNodes, select],
  )

  const onEdgesChange = useCallback(
    (changes: EdgeChange<Edge>[]) => {
      const removed: string[] = []
      for (const c of changes) {
        if (c.type === 'remove') removed.push(c.id)
        else if (c.type === 'select') {
          if (c.selected) selectEdge(c.id)
          else if (useSymphonyStore.getState().selectedEdgeId === c.id) selectEdge(null)
        }
      }
      if (removed.length) removeEdges(removed)
    },
    [removeEdges, selectEdge],
  )

  const onConnect = useCallback(
    (c: Connection) => {
      if (c.source && c.target) addEdge(c.source, c.target)
    },
    [addEdge],
  )

  const onDragOver = useCallback((e: DragEvent) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
  }, [])

  const onDrop = useCallback(
    (e: DragEvent) => {
      e.preventDefault()
      const kind = e.dataTransfer.getData(DRAG_MIME) as NodeKind
      if (!kind) return
      addNode(kind, screenToFlowPosition({ x: e.clientX, y: e.clientY }))
    },
    [addNode, screenToFlowPosition],
  )

  return (
    <div className="canvas" ref={paneRef} onDragOver={onDragOver} onDrop={onDrop}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onConnect={onConnect}
        onMoveStart={onMoveStart}
        onMoveEnd={onMoveEnd}
        onNodeDragStart={onMoveStart}
        onNodeDragStop={onMoveEnd}
        onPaneClick={() => {
          select(null)
          selectEdge(null)
        }}
        deleteKeyCode={['Backspace', 'Delete']}
        colorMode="dark"
        minZoom={MIN_ZOOM}
        maxZoom={MAX_ZOOM}
        proOptions={{ hideAttribution: true }}
        defaultEdgeOptions={{ type: 'smoothstep' }}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
        <Controls showInteractive={false} position="top-left" onFitView={fitToClearArea} />
      </ReactFlow>
    </div>
  )
}
