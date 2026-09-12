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
import { useCallback, useEffect, useMemo, type DragEvent } from 'react'
import { useSymphonyStore } from '../store/useSymphonyStore'
import type { NodeKind } from '../types/topology'
import { TopologyNodeView, type RFNode } from './nodes/TopologyNodeView'

export const DRAG_MIME = 'application/symphony-node'

const nodeTypes: NodeTypes = {
  service: TopologyNodeView,
  broker: TopologyNodeView,
  db: TopologyNodeView,
  proxy: TopologyNodeView,
  infra: TopologyNodeView,
}

export function Canvas() {
  const topology = useSymphonyStore((s) => s.topology)
  const statuses = useSymphonyStore((s) => s.statuses)
  const selectedNodeId = useSymphonyStore((s) => s.selectedNodeId)
  const loadSeq = useSymphonyStore((s) => s.loadSeq)
  const moveNode = useSymphonyStore((s) => s.moveNode)
  const removeNodes = useSymphonyStore((s) => s.removeNodes)
  const removeEdges = useSymphonyStore((s) => s.removeEdges)
  const addEdge = useSymphonyStore((s) => s.addEdge)
  const addNode = useSymphonyStore((s) => s.addNode)
  const select = useSymphonyStore((s) => s.select)
  const { screenToFlowPosition, fitView } = useReactFlow()

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

  useEffect(() => {
    const id = window.setTimeout(() => fitView({ padding: 0.15, duration: 0 }), 30)
    return () => window.clearTimeout(id)
  }, [loadSeq, fitView])

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
    <div className="canvas" onDragOver={onDragOver} onDrop={onDrop}>
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
        minZoom={0.3}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
        defaultEdgeOptions={{ type: 'smoothstep' }}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
        <Controls showInteractive={false} position="bottom-right" />
      </ReactFlow>
    </div>
  )
}
