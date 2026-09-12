import type { DragEvent } from 'react'
import { KIND_LABEL } from '../lib/format'
import { useSymphonyStore } from '../store/useSymphonyStore'
import type { NodeKind } from '../types/topology'
import { DRAG_MIME } from './Canvas'

const PALETTE: { kind: NodeKind; label: string; hint: string }[] = [
  { kind: 'service', label: KIND_LABEL.service, hint: 'stateless worker' },
  { kind: 'broker', label: KIND_LABEL.broker, hint: 'kafka / rabbit' },
  { kind: 'db', label: KIND_LABEL.db, hint: 'postgres' },
  { kind: 'proxy', label: KIND_LABEL.proxy, hint: 'toxiproxy, fault point' },
  { kind: 'infra', label: KIND_LABEL.infra, hint: 'ingress / nginx' },
]

export function Palette() {
  const presets = useSymphonyStore((s) => s.presets)
  const currentName = useSymphonyStore((s) => s.topology.name)
  const loadPreset = useSymphonyStore((s) => s.loadPreset)

  const onDragStart = (e: DragEvent, kind: NodeKind) => {
    e.dataTransfer.setData(DRAG_MIME, kind)
    e.dataTransfer.effectAllowed = 'move'
  }

  return (
    <div className="palette">
      <div className="pane-title">Palette</div>
      <div className="palette-list">
        {PALETTE.map((p) => (
          <div key={p.kind} className={`palette-item kind-${p.kind}`} draggable onDragStart={(e) => onDragStart(e, p.kind)}>
            <span className="palette-swatch" />
            <span className="palette-label">{p.label}</span>
            <span className="palette-hint">{p.hint}</span>
          </div>
        ))}
      </div>
      <div className="palette-help">drag onto the canvas · drag handle to handle to connect · Del removes</div>

      <div className="pane-title">Topologies</div>
      <div className="preset-list">
        {presets.length === 0 ? <div className="muted">no presets loaded</div> : null}
        {presets.map((p) => (
          <button
            key={p.name}
            type="button"
            className={`preset-item${p.name === currentName ? ' is-active' : ''}`}
            onClick={() => loadPreset(p.name)}
          >
            <span className="preset-name">{p.name}</span>
            <span className="preset-meta">
              {p.nodes.length} nodes · {p.edges.length} edges
            </span>
          </button>
        ))}
      </div>
    </div>
  )
}
