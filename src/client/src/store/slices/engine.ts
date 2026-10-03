import { backend, errorMessage, hasBackend } from '../../lib/backend'
import { warn } from '../batch'
import { HOST_STATS_CAP, dropDeployment, dropHistory, freshDeployment, patchDeployment } from '../deployments'
import { wireEvents } from '../events'
import { selectIsApplied } from '../selectors'
import { writeTemplates } from '../templates'
import type { Deployment, EngineSlice, Slice } from '../types'

export const createEngineSlice: Slice<EngineSlice> = (set, get) => ({
  initialized: false,
  runtime: null,
  deployments: {},
  plan: null,
  opStatus: {},
  validation: [],
  metrics: null,
  hostStats: [],
  simHistory: {},

  async init() {
    if (get().initialized) return
    set({ initialized: true })

    if (!hasBackend()) {
      get().appendLog({
        ts: Date.now(),
        level: 'warn',
        source: 'ui',
        message: 'no Wails backend detected (running in a plain browser?) — bindings are inert',
      })
      return
    }

    try {
      for (const l of await backend.getLogHistory()) {
        get().appendLog({ ts: l.ts, level: l.level, source: l.source, message: l.message })
      }
    } catch (err) {
      warn(get, 'ui', err)
    }

    wireEvents(set, get)

    try {
      const [runtime, presets, running, hostStats] = await Promise.all([
        backend.getRuntimeStatus(),
        backend.getPresets(),
        backend.getDeployments(),
        backend.getHostStats(),
      ])
      const deployments: Record<string, Deployment> = {}
      for (const d of running) deployments[d.name] = freshDeployment(d)
      set({ runtime, presets, deployments, hostStats: hostStats.slice(-HOST_STATS_CAP) })
      if (running.length > 0) {
        set((s) => ({ topology: running[0].topology, loadSeq: s.loadSeq + 1 }))
      } else if (presets.length > 0) {
        get().loadTemplate(presets[0].name)
      }
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'ui', message: `init failed: ${errorMessage(err)}` })
    }

    try {
      let stored = await backend.getUserTemplates()
      const mirrored = get().userTemplates
      if (stored.length === 0 && mirrored.length > 0) {
        for (const t of mirrored) stored = await backend.saveUserTemplate(t)
      }
      set({ userTemplates: stored })
      writeTemplates(stored)
    } catch (err) {
      warn(get, 'library', err)
    }

    try {
      set({ scenarios: await backend.listScenarios() })
      await get().refreshRuns()
    } catch (err) {
      warn(get, 'tests', err)
    }
  },

  async refreshDeployments(changed) {
    try {
      const running = await backend.getDeployments()
      set((s) => {
        const deployments: Record<string, Deployment> = {}
        for (const d of running) {
          const prev = s.deployments[d.name]
          deployments[d.name] = prev ? { ...prev, phase: d.phase, statuses: d.statuses } : freshDeployment(d)
        }
        const shown = changed === s.topology.name ? running.find((d) => d.name === changed) : undefined
        return shown ? { deployments, topology: shown.topology, plan: null, opStatus: {} } : { deployments }
      })
    } catch (err) {
      warn(get, 'engine', err)
    }
  },

  async validate() {
    if (!hasBackend()) return
    try {
      const findings = await backend.validate(get().topology)
      set({ validation: findings, drawerOpen: true, drawerTab: 'plan' })
      const errors = findings.filter((f) => f.severity === 'error').length
      get().appendLog({
        ts: Date.now(),
        level: errors ? 'warn' : 'info',
        source: 'validate',
        message: `${findings.length} finding(s), ${errors} error(s)`,
      })
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'validate', message: errorMessage(err) })
    }
  },

  async planApply() {
    if (!hasBackend()) return
    try {
      const [findings, plan] = await Promise.all([backend.validate(get().topology), backend.plan(get().topology)])
      set({ validation: findings, plan, opStatus: {}, drawerOpen: true, drawerTab: 'plan' })
      get().appendLog({
        ts: Date.now(),
        level: 'info',
        source: 'planner',
        message: `plan: ${plan.ops.length} ops (${plan.summary.hot} hot, ${plan.summary.cold} cold, ${plan.summary.structural} structural)`,
      })
    } catch (err) {
      set({ plan: null })
      get().appendLog({ ts: Date.now(), level: 'error', source: 'planner', message: errorMessage(err) })
    }
  },

  async apply() {
    if (!hasBackend()) return
    const name = get().topology.name
    if (get().deployments[name]?.busy) return
    set((s) => ({
      drawerOpen: true,
      drawerTab: 'log',
      deployments: patchDeployment(s.deployments, name, () => ({ busy: true })),
    }))
    try {
      const plan = await backend.plan(get().topology)
      set({ plan, opStatus: {} })
      await backend.apply(get().topology)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'apply', message: `[${name}] ${errorMessage(err)}` })
    } finally {
      set((s) => ({
        deployments: name in s.deployments ? patchDeployment(s.deployments, name, () => ({ busy: false })) : s.deployments,
      }))
    }
  },

  async teardown() {
    if (!hasBackend()) return
    const name = get().topology.name
    if (!(name in get().deployments) || get().deployments[name].busy) return
    set((s) => ({
      drawerOpen: true,
      drawerTab: 'log',
      deployments: patchDeployment(s.deployments, name, () => ({ busy: true })),
    }))
    try {
      await backend.teardown(name)
      set((s) => ({
        plan: null,
        opStatus: {},
        deployments: dropDeployment(s.deployments, name),
        simHistory: dropHistory(s.simHistory, name),
      }))
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'teardown', message: `[${name}] ${errorMessage(err)}` })
      set((s) => ({
        deployments: name in s.deployments ? patchDeployment(s.deployments, name, () => ({ busy: false })) : s.deployments,
      }))
    }
  },

  async injectFault(id, fault) {
    if (!hasBackend() || !selectIsApplied(get())) return
    try {
      await backend.injectFault(get().topology.name, id, fault)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'warn', source: 'fault', message: `${id}: ${errorMessage(err)}` })
    }
  },

  async clearFaults(id) {
    if (!hasBackend() || !selectIsApplied(get())) return
    try {
      await backend.clearFaults(get().topology.name, id)
    } catch (err) {
      warn(get, 'fault', err)
    }
  },

  async control(id, cmd) {
    if (!hasBackend() || !selectIsApplied(get())) return null
    try {
      return await backend.control(get().topology.name, id, cmd)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'warn', source: 'control', message: `${id} ${cmd.op}: ${errorMessage(err)}` })
      return null
    }
  },
})
