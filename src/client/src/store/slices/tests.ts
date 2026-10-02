import { backend, errorMessage, hasBackend } from '../../lib/backend'
import { warn } from '../batch'
import type { Slice, TestsSlice } from '../types'

/** Runs shown side by side at most. */
const COMPARE_MAX = 4

export const createTestsSlice: Slice<TestsSlice> = (set, get) => ({
  scenarios: [],
  runs: [],
  activeTest: null,
  openRun: null,
  compareIds: [],
  compareRuns: [],
  exportingRun: null,

  async refreshRuns() {
    if (!hasBackend()) return
    try {
      set({ runs: await backend.listRuns() })
    } catch (err) {
      warn(get, 'tests', err)
    }
  },

  async startTest(scenario, params) {
    if (!hasBackend()) return
    try {
      const id = await backend.startTest(scenario, get().topology, params)
      set({ activeTest: { run_id: id, scenario, template: get().topology.name, phase: 'starting', elapsed_ms: 0, total_ms: 0 } })
      get().appendLog({ ts: Date.now(), level: 'info', source: 'test', message: `started ${scenario} on "${get().topology.name}" (${id})` })
      await get().refreshRuns()
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'test', message: errorMessage(err) })
    }
  },

  async abortTest() {
    const a = get().activeTest
    if (!a || !hasBackend()) return
    try {
      await backend.abortTest(a.run_id)
    } catch (err) {
      warn(get, 'test', err)
    }
  },

  async viewRun(id) {
    if (!id) {
      set({ openRun: null })
      return
    }
    try {
      set({ openRun: await backend.getRun(id) })
    } catch (err) {
      warn(get, 'tests', err)
    }
  },

  async deleteRun(id) {
    try {
      await backend.deleteRun(id)
      set((s) => ({
        openRun: s.openRun?.id === id ? null : s.openRun,
        compareIds: s.compareIds.filter((c) => c !== id),
        compareRuns: s.compareRuns.filter((r) => r.id !== id),
      }))
      await get().refreshRuns()
    } catch (err) {
      warn(get, 'tests', err)
    }
  },

  async exportRun(id, format) {
    if (!hasBackend() || get().exportingRun) return
    set({ exportingRun: id })
    try {
      const res = await backend.exportRunReport(id, format)
      if (!res.cancelled) {
        get().appendLog({ ts: Date.now(), level: 'info', source: 'test', message: `report of ${id} saved to ${res.path}` })
      }
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'test', message: `report of ${id}: ${errorMessage(err)}` })
    } finally {
      set({ exportingRun: null })
    }
  },

  async toggleCompare(id) {
    const s = get()
    if (s.compareIds.includes(id)) {
      set({ compareIds: s.compareIds.filter((c) => c !== id), compareRuns: s.compareRuns.filter((r) => r.id !== id) })
      return
    }
    try {
      const run = await backend.getRun(id)
      set((st) => ({ compareIds: [...st.compareIds, id].slice(-COMPARE_MAX), compareRuns: [...st.compareRuns, run].slice(-COMPARE_MAX) }))
    } catch (err) {
      warn(get, 'tests', err)
    }
  },
})
