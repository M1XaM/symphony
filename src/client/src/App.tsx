import { ReactFlowProvider } from '@xyflow/react'
import { useEffect, useRef, type CSSProperties, type PointerEvent } from 'react'
import { Canvas } from './components/Canvas'
import { DockerNotice } from './components/DockerNotice'
import { Drawer } from './components/Drawer'
import { Inspector } from './components/Inspector'
import { LeftPanel } from './components/LeftPanel'
import { Toolbar } from './components/Toolbar'
import { clampDrawerHeight } from './store/slices/panels'
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

/** The bottom panel's fold handle: the same arrow as the side panels', in the
 *  middle of its top edge. */
function DrawerToggle() {
  const open = useSymphonyStore((s) => s.drawerOpen)
  const setDrawer = useSymphonyStore((s) => s.setDrawer)
  return (
    <button
      type="button"
      className="panel-toggle"
      onClick={() => setDrawer(!open)}
      title={`${open ? 'hide' : 'show'} the bottom panel`}
      aria-label={`${open ? 'hide' : 'show'} the bottom panel`}
      aria-expanded={open}
    >
      <span className="panel-toggle-turned">{open ? '›' : '‹'}</span>
    </button>
  )
}

/** The bottom panel's top edge, dragged to make it higher or lower. While it
 *  is dragged the height is written straight to the layout, so only the grid
 *  moves; the store hears of it when the edge is let go. */
function DrawerResizer() {
  const setDrawerHeight = useSymphonyStore((s) => s.setDrawerHeight)
  const drag = useRef<{ y: number; h: number; now: number } | null>(null)
  const app = () => document.querySelector<HTMLElement>('.app')

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.currentTarget.setPointerCapture(e.pointerId)
    const h = useSymphonyStore.getState().drawerHeight
    drag.current = { y: e.clientY, h, now: h }
    app()?.classList.add('is-resizing')
  }
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (!d) return
    d.now = clampDrawerHeight(d.h + d.y - e.clientY)
    app()?.style.setProperty('--drawer-h', `${d.now}px`)
  }
  const onPointerUp = () => {
    const d = drag.current
    if (!d) return
    drag.current = null
    app()?.classList.remove('is-resizing')
    setDrawerHeight(d.now)
  }
  return (
    <div
      className="drawer-resize"
      role="separator"
      aria-orientation="horizontal"
      aria-label="resize the bottom panel"
      title="drag to resize"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
    />
  )
}

export default function App() {
  const init = useSymphonyStore((s) => s.init)
  const leftOpen = useSymphonyStore((s) => s.panelOpen.left)
  const rightOpen = useSymphonyStore((s) => s.panelOpen.right)
  const drawerOpen = useSymphonyStore((s) => s.drawerOpen)
  const drawerHeight = useSymphonyStore((s) => s.drawerHeight)
  useEffect(() => {
    void init()
  }, [init])

  // The map fills the window; the panels float above it on their own layer.
  // Folding one sets its column to zero width, which the map's fit and the
  // zoom controls both read off the same custom properties.
  const folded = `${leftOpen ? '' : ' left-folded'}${rightOpen ? '' : ' right-folded'}`
  return (
    <div className={`app${folded}`} style={{ '--drawer-h': `${drawerHeight}px` } as CSSProperties}>
      <div className="map-layer">
        <ReactFlowProvider>
          <Canvas />
        </ReactFlowProvider>
      </div>
      <div className="chrome">
        <header className="panel panel-top">
          <DockerNotice />
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
        <div className="slot slot-bottom">
          {drawerOpen ? (
            <>
              <section className="panel panel-bottom">
                <Drawer />
              </section>
              <DrawerResizer />
            </>
          ) : null}
          <DrawerToggle />
        </div>
      </div>
    </div>
  )
}
