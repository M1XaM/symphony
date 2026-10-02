import { selectHasGateway, selectSelectedEdge, selectSelectedNode, useSymphonyStore } from '../store/useSymphonyStore'
import { MetricTiles } from './MetricTiles'
import { EdgeInspector } from './inspector/EdgeInspector'
import { NodeInspector } from './inspector/NodeInspector'

/** The right panel: the selected node or edge, else the run summary. */
export function Inspector() {
  const node = useSymphonyStore(selectSelectedNode)
  const edge = useSymphonyStore(selectSelectedEdge)
  const hasGateway = useSymphonyStore(selectHasGateway)
  return (
    <div className="inspector">
      <div className="inspector-main">
        {node ? <NodeInspector node={node} /> : edge ? <EdgeInspector edge={edge} /> : <MetricTiles />}
        {!node && !edge && hasGateway ? (
          <div className="summary-hint muted small">select the gateway to drive load, an edge to break a link</div>
        ) : null}
      </div>
    </div>
  )
}
