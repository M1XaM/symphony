import { useSymphonyStore } from '../store/useSymphonyStore'
import { LogView } from './LogView'
import { PlanView } from './PlanView'

export function Drawer() {
  const open = useSymphonyStore((s) => s.drawerOpen)
  const tab = useSymphonyStore((s) => s.drawerTab)
  const setDrawer = useSymphonyStore((s) => s.setDrawer)
  const opCount = useSymphonyStore((s) => s.plan?.ops.length ?? 0)
  const logCount = useSymphonyStore((s) => s.log.length)

  return (
    <div className={`drawer${open ? ' is-open' : ''}`}>
      <div className="drawer-bar">
        <button type="button" className={`tab${tab === 'plan' ? ' is-active' : ''}`} onClick={() => setDrawer(true, 'plan')}>
          Plan {opCount ? <span className="tab-count">{opCount}</span> : null}
        </button>
        <button type="button" className={`tab${tab === 'log' ? ' is-active' : ''}`} onClick={() => setDrawer(true, 'log')}>
          Log {logCount ? <span className="tab-count">{logCount}</span> : null}
        </button>
        <div className="toolbar-spacer" />
        <button type="button" className="btn btn-ghost" onClick={() => setDrawer(!open)}>
          {open ? 'hide' : 'show'}
        </button>
      </div>
      {open ? <div className="drawer-body">{tab === 'plan' ? <PlanView /> : <LogView />}</div> : null}
    </div>
  )
}
