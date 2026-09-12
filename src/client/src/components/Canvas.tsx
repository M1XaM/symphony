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
import { selectStatuses, useSymphonyStore } from '../store/useSymphonyStore'
import type { NodeKind } from '../types/topology'
import { TopologyNodeView, type RFNode } from './nodes/TopologyNodeView'

export const DRAG_MIME = 'application/symphony-node'

const MIN_ZOOM = 0.3
const MAX_ZOOM = 2

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
  const selectedNodeId = useSymphonyStore((s) => s.selectedNodeId)
  const loadSeq = useSymphonyStore((s) => s.loadSeq)
  const moveNode = useSymphonyStore((s) => s.moveNode)
  const removeNodes = useSymphonyStore((s) => s.removeNodes)
  const removeEdges = useSymphonyStore((s) => s.removeEdges)
  const addEdge = useSymphonyStore((s) => s.addEdge)
  const addNode = useSymphonyStore((s) => s.addNode)
  const select = useSymphonyStore((s) => s.select)
  const { screenToFlowPosition, fitView, setViewport } = useReactFlow()
  const paneRef = useRef<HTMLDivElement>(null)

  const nodes = useMemo<RFNode[]>(
    () =>
      topology.nodes.map((n) => ({
        id: n.id,
        type: n.kind,
        position: n.position,
        selected: n.id === selectedNodeId,
        data: { node: n, status: statuses[n.id] },
      })),
    [topology.nodes, statuses, selectedNodeId],
  )

  const edges = useMemo<Edge[]>(
    () =>
      topology.edges.map((e) => {
        const label = e.via ? `${e.topic ?? e.kind} via ${e.via}` : e.topic
        return {
          id: e.id,
          source: e.from,
          target: e.to,
          type: 'smoothstep',
          label,
          className: `tedge tedge-${e.kind}${e.via ? ' tedge-via' : ''}`,
          markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
          data: { kind: e.kind, via: e.via },
        }
      }),
    [topology.edges],
  )

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
      const removed = changes.filter((c) => c.type === 'remove').map((c) => c.id)
      if (removed.length) removeEdges(removed)
    },
    [removeEdges],
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
        onPaneClick={() => select(null)}
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
