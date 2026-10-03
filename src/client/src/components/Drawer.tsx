import type { ComponentType } from 'react'
import { useSymphonyStore, type DrawerTab, type SymphonyState } from '../store/useSymphonyStore'
import { ContainersView } from './ContainersView'
import { LogView } from './LogView'
import { PlanView } from './PlanView'
import { RunsView } from './RunsView'
import { StatsView } from './StatsView'
import { TestsView } from './TestsView'

const VIEWS: Record<DrawerTab, ComponentType> = {
  plan: PlanView,
  stats: StatsView,
  containers: ContainersView,
  log: LogView,
  tests: TestsView,
  runs: RunsView,
}

const TABS: { id: DrawerTab; label: string }[] = [
  { id: 'plan', label: 'Plan' },
  { id: 'stats', label: 'Stats' },
  { id: 'containers', label: 'Containers' },
  { id: 'log', label: 'Log' },
  { id: 'tests', label: 'Tests' },
  { id: 'runs', label: 'Runs' },
]

const COUNTS: Partial<Record<DrawerTab, (s: SymphonyState) => number>> = {
  plan: (s) => s.plan?.ops.length ?? 0,
  log: (s) => s.log.length,
  containers: (s) => s.containersRunning,
  runs: (s) => s.runs.length,
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

/** The bottom panel's tabs. Folding it away is the arrow on its top edge and
 *  its height the edge itself; both live in App, next to the side panels'. */
export function Drawer() {
  const tab = useSymphonyStore((s) => s.drawerTab)
  const setDrawer = useSymphonyStore((s) => s.setDrawer)
  const View = VIEWS[tab]

  return (
    <div className="drawer">
      <div className="drawer-bar">
        {TABS.map((t) => (
          <button key={t.id} type="button" className={`tab${tab === t.id ? ' is-active' : ''}`} onClick={() => setDrawer(true, t.id)}>
            {t.label} <TabBadge tab={t.id} />
          </button>
        ))}
      </div>
      <div className="drawer-body">
        <View />
      </div>
    </div>
  )
}
