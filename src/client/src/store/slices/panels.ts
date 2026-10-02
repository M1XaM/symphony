import { backend, errorMessage, hasBackend } from '../../lib/backend'
import { dropQueuedLines, queueLine, warn } from '../batch'
import { selectIsApplied } from '../selectors'
import type { PanelsSlice, Slice } from '../types'

export const createPanelsSlice: Slice<PanelsSlice> = (set, get) => ({
  log: [],
  drawerOpen: true,
  drawerTab: 'plan',
  panelOpen: { left: true, right: true },
  leftTab: 'elements',
  logsNode: null,
  nodeLogs: [],
  containers: [],
  containersRunning: 0,
  containersAll: false,
  containersError: null,
  logStream: { enabled: false },

  setLogsNode(id) {
    set({ logsNode: id, nodeLogs: [] })
    if (id) void get().refreshLogs()
  },

  async refreshLogs() {
    const id = get().logsNode
    if (!id || !hasBackend() || !selectIsApplied(get())) return
    try {
      const lines = await backend.getNodeLogs(get().topology.name, id, 300)
      if (get().logsNode === id) set({ nodeLogs: lines })
    } catch (err) {
      warn(get, 'logs', err)
    }
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

  async setLogStream(on) {
    if (!hasBackend()) return
    try {
      set({ logStream: await backend.setContainerLogStream(on) })
    } catch (err) {
      warn(get, 'logs', err)
    }
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

  togglePanel(side) {
    set((s) => ({ panelOpen: { ...s.panelOpen, [side]: !s.panelOpen[side] } }))
  },

  setLeftTab(tab) {
    set({ leftTab: tab })
  },
})
