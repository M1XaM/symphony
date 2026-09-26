import type { ComponentType } from 'react'
import { useSymphonyStore, type DrawerTab, type SymphonyState } from '../store/useSymphonyStore'
import { ContainersView } from './ContainersView'
import { LogView } from './LogView'
import { LogsView } from './LogsView'
import { PlanView } from './PlanView'
import { StatsView } from './StatsView'
import { TestsView } from './TestsView'

const VIEWS: Record<DrawerTab, ComponentType> = {
  plan: PlanView,
  stats: StatsView,
  containers: ContainersView,
  logs: LogsView,
  log: LogView,
  tests: TestsView,
}

const TABS: { id: DrawerTab; label: string }[] = [
  { id: 'plan', label: 'Plan' },
  { id: 'stats', label: 'Stats' },
  { id: 'containers', label: 'Containers' },
  { id: 'logs', label: 'Node logs' },
  { id: 'log', label: 'Log' },
  { id: 'tests', label: 'Tests' },
]

const COUNTS: Partial<Record<DrawerTab, (s: SymphonyState) => number>> = {
  plan: (s) => s.plan?.ops.length ?? 0,
  log: (s) => s.log.length,
  containers: (s) => s.containersRunning,
}

const noCount = () => 0

/** A tab's badge subscribes on its own: the log count changes with every
 *  flush, and that must not re-render the drawer and the open tab with it. */
function TabBadge({ tab }: { tab: DrawerTab }) {
  const count = useSymphonyStore(COUNTS[tab] ?? noCount)
  const testRunning = useSymphonyStore((s) => tab === 'tests' && s.activeTest !== null)
  return (
    <>
      {count ? <span className="tab-count">{count}</span> : null}
      {testRunning ? <span className="live-dot live-dot-running tab-live" aria-label="test running" /> : null}
    </>
  )
}

export function Drawer() {
  const open = useSymphonyStore((s) => s.drawerOpen)
  const tab = useSymphonyStore((s) => s.drawerTab)
  const setDrawer = useSymphonyStore((s) => s.setDrawer)
  const View = VIEWS[tab]

  return (
    <div className={`drawer${open ? ' is-open' : ''}`}>
      <div className="drawer-bar">
        {TABS.map((t) => (
          <button key={t.id} type="button" className={`tab${tab === t.id ? ' is-active' : ''}`} onClick={() => setDrawer(true, t.id)}>
            {t.label} <TabBadge tab={t.id} />
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
