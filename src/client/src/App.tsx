import { ReactFlowProvider } from '@xyflow/react'
import { useEffect } from 'react'
import { Canvas } from './components/Canvas'
import { Drawer } from './components/Drawer'
import { Inspector } from './components/Inspector'
import { LeftPanel } from './components/LeftPanel'
import { Toolbar } from './components/Toolbar'
import { useSymphonyStore } from './store/useSymphonyStore'

export default function App() {
  const init = useSymphonyStore((s) => s.init)
  useEffect(() => {
    void init()
  }, [init])

  // The map fills the window; the panels float above it on their own layer.
  return (
    <div className="app">
      <div className="map-layer">
        <ReactFlowProvider>
          <Canvas />
        </ReactFlowProvider>
      </div>
      <div className="chrome">
        <header className="panel panel-top">
          <Toolbar />
        </header>
        <aside className="panel panel-left">
          <LeftPanel />
        </aside>
        <aside className="panel panel-right">
          <Inspector />
        </aside>
        <section className="panel panel-bottom">
          <Drawer />
        </section>
      </div>
    </div>
  )
}
