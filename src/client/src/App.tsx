import { ReactFlowProvider } from '@xyflow/react'
import { useEffect } from 'react'
import { Canvas } from './components/Canvas'
import { Drawer } from './components/Drawer'
import { Inspector } from './components/Inspector'
import { LeftPanel } from './components/LeftPanel'
import { Toolbar } from './components/Toolbar'
import { useSymphonyStore, type PanelSide } from './store/useSymphonyStore'

const LABEL: Record<PanelSide, string> = { left: 'elements and templates', right: 'inspector' }
/** The arrow always points the way the panel would travel. */
const ARROW: Record<PanelSide, { open: string; closed: string }> = {
  left: { open: '‹', closed: '›' },
  right: { open: '›', closed: '‹' },
}

/** The fold/unfold handle, pinned to the middle of the panel's inner edge.
 *  When the panel is folded away it is all that is left of it. */
function PanelToggle({ side }: { side: PanelSide }) {
  const open = useSymphonyStore((s) => s.panelOpen[side])
  const toggle = useSymphonyStore((s) => s.togglePanel)
  return (
    <button
      type="button"
      className="panel-toggle"
      onClick={() => toggle(side)}
      title={`${open ? 'hide' : 'show'} ${LABEL[side]}`}
      aria-label={`${open ? 'hide' : 'show'} ${LABEL[side]} panel`}
      aria-expanded={open}
    >
      {open ? ARROW[side].open : ARROW[side].closed}
    </button>
  )
}

export default function App() {
  const init = useSymphonyStore((s) => s.init)
  const leftOpen = useSymphonyStore((s) => s.panelOpen.left)
  const rightOpen = useSymphonyStore((s) => s.panelOpen.right)
  useEffect(() => {
    void init()
  }, [init])

  // The map fills the window; the panels float above it on their own layer.
  // Folding one sets its column to zero width, which the map's fit and the
  // zoom controls both read off the same custom properties.
  const folded = `${leftOpen ? '' : ' left-folded'}${rightOpen ? '' : ' right-folded'}`
  return (
    <div className={`app${folded}`}>
      <div className="map-layer">
        <ReactFlowProvider>
          <Canvas />
        </ReactFlowProvider>
      </div>
      <div className="chrome">
        <header className="panel panel-top">
          <Toolbar />
        </header>
        <div className="slot slot-left">
          {leftOpen ? (
            <aside className="panel panel-left">
              <LeftPanel />
            </aside>
          ) : null}
          <PanelToggle side="left" />
        </div>
        <div className="slot slot-right">
          {rightOpen ? (
            <aside className="panel panel-right">
              <Inspector />
            </aside>
          ) : null}
          <PanelToggle side="right" />
        </div>
        <section className="panel panel-bottom">
          <Drawer />
        </section>
      </div>
    </div>
  )
}
