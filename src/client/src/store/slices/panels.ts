import { backend, errorMessage, hasBackend } from '../../lib/backend'
import { dropQueuedLines, queueLine } from '../batch'
import { selectContainers } from '../selectors'
import type { PanelsSlice, Slice } from '../types'

/** The drawer opens this high, never gets lower than its bar and a few rows,
 *  and never takes more than half the window. */
const DRAWER_DEFAULT = 230
const DRAWER_MIN = 110

export function clampDrawerHeight(px: number): number {
  const max = typeof window === 'undefined' ? Infinity : Math.floor(window.innerHeight / 2)
  return Math.round(Math.max(DRAWER_MIN, Math.min(px, max)))
}

export const createPanelsSlice: Slice<PanelsSlice> = (set, get) => ({
  log: [],
  drawerOpen: true,
  drawerTab: 'plan',
  drawerHeight: DRAWER_DEFAULT,
  panelOpen: { left: true, right: true },
  leftTab: 'elements',
  logSource: '',
  logContainers: [],
  containerLog: [],
  containerLogError: null,
  containers: [],
  containersRunning: 0,
  containersAll: false,
  containersError: null,

  setLogSource(name) {
    if (get().logSource === name) return
    set({ logSource: name, containerLog: [], containerLogError: null })
    if (name) void get().refreshContainerLog()
  },

  /** The containers Symphony runs, for the Log tab's source list. */
  async refreshLogContainers() {
    if (!hasBackend()) return
    try {
      const rows = (await backend.listContainers(false)).filter((r) => r.ours).sort((x, y) => x.name.localeCompare(y.name))
      const was = get().logContainers
      if (rows.length !== was.length || rows.some((r, i) => r.name !== was[i].name)) set({ logContainers: rows })
    } catch {
      // no Docker: the list stays as it was, and the tab says why it is empty
    }
  },

  async refreshContainerLog() {
    const name = get().logSource
    if (!name || !hasBackend()) return
    try {
      const lines = await backend.getContainerLogs(name)
      if (get().logSource !== name) return
      const was = get().containerLog
      const last = lines[lines.length - 1]
      const wasLast = was[was.length - 1]
      // an unchanged tail is left alone, so its rows are not rendered again
      if (lines.length === was.length && last?.ts === wasLast?.ts && last?.message === wasLast?.message) {
        if (get().containerLogError) set({ containerLogError: null })
        return
      }
      set({ containerLog: lines.map((l, i) => ({ id: i, ...l })), containerLogError: null })
    } catch (err) {
      if (get().logSource === name) set({ containerLogError: errorMessage(err) })
    }
  },

  async showNodeLogs(nodeId) {
    set({ drawerOpen: true, drawerTab: 'log' })
    await get().refreshLogContainers()
    // the node's last stats sample names one of its containers; the sweep's
    // rows only know the node id, which two deployments may share
    const name = selectContainers(get())[nodeId]?.container ?? get().logContainers.find((c) => c.node === nodeId)?.name
    if (name) get().setLogSource(name)
  },

  /** One `docker ps` sweep. The Containers tab calls this on a timer, which
   *  is the `watch docker ps` it stands in for. */
  async refreshContainers() {
    if (!hasBackend()) return
    try {
      const rows = await backend.listContainers(get().containersAll)
      let running = 0
      for (const r of rows) if (r.state === 'running') running++
      set({ containers: rows, containersRunning: running, containersError: null })
    } catch (err) {
      set({ containersError: errorMessage(err) })
    }
  },

  setContainersAll(all) {
    set({ containersAll: all })
    void get().refreshContainers()
  },

  appendLog(line) {
    queueLine(line)
  },

  clearLog() {
    dropQueuedLines()
    set({ log: [] })
  },

  setDrawer(open, tab) {
    set((s) => ({ drawerOpen: open, drawerTab: tab ?? s.drawerTab }))
  },

  setDrawerHeight(px) {
    set({ drawerHeight: clampDrawerHeight(px) })
  },

  togglePanel(side) {
    set((s) => ({ panelOpen: { ...s.panelOpen, [side]: !s.panelOpen[side] } }))
  },

  setLeftTab(tab) {
    set({ leftTab: tab })
  },
})
