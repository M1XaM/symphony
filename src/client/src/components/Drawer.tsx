import type { ComponentType } from 'react'
import { useSymphonyStore, type DrawerTab } from '../store/useSymphonyStore'
import { LogView } from './LogView'
import { LogsView } from './LogsView'
import { PlanView } from './PlanView'
import { StatsView } from './StatsView'
import { TestsView } from './TestsView'

const VIEWS: Record<DrawerTab, ComponentType> = {
  plan: PlanView,
  stats: StatsView,
  logs: LogsView,
  log: LogView,
  tests: TestsView,
}

const TABS: { id: DrawerTab; label: string }[] = [
  { id: 'plan', label: 'Plan' },
  { id: 'stats', label: 'Stats' },
  { id: 'logs', label: 'Containers' },
  { id: 'log', label: 'Log' },
  { id: 'tests', label: 'Tests' },
]

export function Drawer() {
  const open = useSymphonyStore((s) => s.drawerOpen)
  const tab = useSymphonyStore((s) => s.drawerTab)
  const setDrawer = useSymphonyStore((s) => s.setDrawer)
  const opCount = useSymphonyStore((s) => s.plan?.ops.length ?? 0)
  const logCount = useSymphonyStore((s) => s.log.length)
  const testRunning = useSymphonyStore((s) => s.activeTest !== null)
  const View = VIEWS[tab]
  const counts: Partial<Record<DrawerTab, number>> = { plan: opCount, log: logCount }

  return (
    <div className={`drawer${open ? ' is-open' : ''}`}>
      <div className="drawer-bar">
        {TABS.map((t) => (
          <button key={t.id} type="button" className={`tab${tab === t.id ? ' is-active' : ''}`} onClick={() => setDrawer(true, t.id)}>
            {t.label} {counts[t.id] ? <span className="tab-count">{counts[t.id]}</span> : null}
            {t.id === 'tests' && testRunning ? <span className="live-dot live-dot-running tab-live" aria-label="test running" /> : null}
          </button>
        ))}
        <div className="toolbar-spacer" />
        <button type="button" className="btn btn-ghost" onClick={() => setDrawer(!open)}>
          {open ? 'hide' : 'show'}
        </button>
      </div>
      {open ? (
        <div className="drawer-body">
          <View />
        </div>
      ) : null}
    </div>
  )
}
