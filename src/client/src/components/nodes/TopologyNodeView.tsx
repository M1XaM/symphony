import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'
import { KIND_LABEL } from '../../lib/format'
import type { NodeKind, NodeStatus, TopologyNode } from '../../types/topology'
import { StatusDot } from '../StatusDot'

export type RFNodeData = { node: TopologyNode; status?: NodeStatus }
export type RFNode = Node<RFNodeData, NodeKind>

/** One component renders every kind; the kind only changes the header colour. */
export function TopologyNodeView({ data, selected }: NodeProps<RFNode>) {
  const { node, status } = data
  const replicaText = status ? `${status.ready}/${status.replicas} ready` : `×${node.replicas}`
  return (
    <div className={`tnode tnode-${node.kind}${selected ? ' is-selected' : ''}`}>
      <Handle type="target" position={Position.Left} className="tnode-handle" />
      <div className="tnode-head">
        <span className="tnode-kind">{KIND_LABEL[node.kind] ?? node.kind}</span>
        <StatusDot state={status?.state} title={status?.message || status?.state} />
      </div>
      <div className="tnode-body">
        <div className="tnode-id">{node.id}</div>
        <div className="tnode-meta" title={node.image}>
          {node.image}
        </div>
        <div className="tnode-meta tnode-replicas">
          {replicaText}
          {status?.message ? <span className="tnode-msg"> · {status.message}</span> : null}
        </div>
      </div>
      <Handle type="source" position={Position.Right} className="tnode-handle" />
    </div>
  )
}
