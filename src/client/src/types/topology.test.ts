import { describe, expect, it } from 'vitest'
import { normalizeRun, type Run } from './topology'

describe('normalizeRun', () => {
  it('gives a run that is still going the lists it does not have yet', () => {
    // what the engine sends for a run in progress, as older builds sent it
    const wire = { id: 'r1', scenario: 't01', name: 'Baseline', template: 'T', started_at: 1, running: true, assertions: null, notes: null, phases: null } as unknown as Run
    const run = normalizeRun(wire)
    expect(run.assertions).toEqual([])
    expect(run.notes).toEqual([])
    expect(run.phases).toEqual([])
    expect(run.series).toEqual({})
    expect(run.summary).toEqual({})
    expect(run.params).toEqual({})
    expect(run.running).toBe(true)
  })

  it('leaves a complete run as it is', () => {
    const full = { id: 'r2', phases: [{ name: 'steady', start_s: 0, end_s: 10 }], series: { x: [{ t: 0, v: 1 }] }, summary: { a: 1 }, assertions: [], notes: ['n'], params: { rps: 1 } } as unknown as Run
    expect(normalizeRun(full)).toEqual(full)
  })
})
