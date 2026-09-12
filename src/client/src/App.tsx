import { ReactFlowProvider } from '@xyflow/react'
import { useEffect } from 'react'
import { Canvas } from './components/Canvas'
import { Drawer } from './components/Drawer'
import { Inspector } from './components/Inspector'
import { Palette } from './components/Palette'
import { Toolbar } from './components/Toolbar'
import { useSymphonyStore } from './store/useSymphonyStore'

export default function App() {
  const init = useSymphonyStore((s) => s.init)
  useEffect(() => {
    void init()
  }, [init])

  return (
    <div className="app">
      <aside className="pane pane-left">
        <Palette />
      </aside>
      <main className="pane pane-center">
        <Toolbar />
        <div className="canvas-wrap">
          <ReactFlowProvider>
            <Canvas />
          </ReactFlowProvider>
        </div>
        <Drawer />
      </main>
      <aside className="pane pane-right">
        <Inspector />
      </aside>
    </div>
  )
}
