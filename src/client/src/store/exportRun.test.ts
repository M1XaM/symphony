import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// the store talks to the window's backend through this module
vi.mock('../lib/backend', () => ({
  hasBackend: () => true,
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
  backend: { exportRunReport: vi.fn() },
}))

import { backend } from '../lib/backend'
import { useSymphonyStore } from './useSymphonyStore'

const exportRunReport = vi.mocked(backend.exportRunReport)
const initial = useSymphonyStore.getState()

beforeEach(() => {
  vi.useFakeTimers()
  exportRunReport.mockReset()
  useSymphonyStore.setState(initial, true)
})

afterEach(() => {
  vi.runOnlyPendingTimers()
  vi.useRealTimers()
})

function logged(): string[] {
  vi.advanceTimersByTime(100)
  return useSymphonyStore.getState().log.map((l) => `${l.level}: ${l.message}`)
}

describe('exporting a run report', () => {
  it('marks the run while the report is written and says where it went', async () => {
    let finish: (r: { path: string; cancelled: boolean }) => void = () => {}
    exportRunReport.mockReturnValue(new Promise((resolve) => (finish = resolve)))

    const done = useSymphonyStore.getState().exportRun('run-1', 'pdf')
    expect(useSymphonyStore.getState().exportingRun).toBe('run-1')
    expect(exportRunReport).toHaveBeenCalledWith('run-1', 'pdf')

    // one report at a time: a second request while the first is open is dropped
    await useSymphonyStore.getState().exportRun('run-2', 'md')
    expect(exportRunReport).toHaveBeenCalledTimes(1)

    finish({ path: '/home/me/run-1.pdf', cancelled: false })
    await done
    expect(useSymphonyStore.getState().exportingRun).toBeNull()
    expect(logged()).toEqual(['info: report of run-1 saved to /home/me/run-1.pdf'])
  })

  it('says nothing when the save dialog is cancelled', async () => {
    exportRunReport.mockResolvedValue({ path: '', cancelled: true })
    await useSymphonyStore.getState().exportRun('run-1', 'md')
    expect(useSymphonyStore.getState().exportingRun).toBeNull()
    expect(logged()).toEqual([])
  })

  it('reports a failure and lets go of the run', async () => {
    exportRunReport.mockRejectedValue(new Error('disk full'))
    await useSymphonyStore.getState().exportRun('run-1', 'pdf')
    expect(useSymphonyStore.getState().exportingRun).toBeNull()
    expect(logged()).toEqual(['error: report of run-1: disk full'])
  })
})
