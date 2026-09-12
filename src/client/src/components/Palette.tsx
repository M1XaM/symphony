import type { DragEvent } from 'react'
import { KIND_LABEL } from '../lib/format'
import type { NodeKind } from '../types/topology'
import { DRAG_MIME } from './Canvas'

const ELEMENTS: { kind: NodeKind; label: string; hint: string }[] = [
  { kind: 'service', label: KIND_LABEL.service, hint: 'stateless worker' },
  { kind: 'broker', label: KIND_LABEL.broker, hint: 'kafka / rabbit' },
  { kind: 'db', label: KIND_LABEL.db, hint: 'postgres' },
  { kind: 'proxy', label: KIND_LABEL.proxy, hint: 'toxiproxy, fault point' },
  { kind: 'infra', label: KIND_LABEL.infra, hint: 'ingress / nginx' },
]

/** The draggable node kinds. Shown by the left panel's Elements tab and by the
 *  template builder, which needs the same source of nodes. */
export function Palette() {
  const onDragStart = (e: DragEvent, kind: NodeKind) => {
    e.dataTransfer.setData(DRAG_MIME, kind)
    e.dataTransfer.effectAllowed = 'move'
  }

  return (
    <div className="palette-list">
      {ELEMENTS.map((p) => (
        <div key={p.kind} className={`palette-item kind-${p.kind}`} draggable onDragStart={(e) => onDragStart(e, p.kind)}>
          <span className="palette-swatch" />
          <span className="palette-label">{p.label}</span>
          <span className="palette-hint">{p.hint}</span>
        </div>
      ))}
    </div>
  )
}
