import { backend, errorMessage, hasBackend } from '../../lib/backend'
import { SCHEMA_VERSION } from '../../types/topology'
import { clone, uniqueId } from '../editing'
import { generateTopology } from '../generate'
import { persistTemplates, readTemplates } from '../templates'
import type { LibrarySlice, Slice } from '../types'

export const createLibrarySlice: Slice<LibrarySlice> = (set, get) => ({
  presets: [],
  userTemplates: readTemplates(),
  draft: null,

  loadTemplate(name) {
    const { presets, userTemplates } = get()
    const template = presets.find((p) => p.name === name) ?? userTemplates.find((t) => t.name === name)
    if (!template) return
    set((s) => ({
      topology: clone(template),
      loadSeq: s.loadSeq + 1,
      selectedNodeId: null,
      selectedEdgeId: null,
      plan: null,
      opStatus: {},
      validation: [],
      draft: null,
    }))
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `loaded template "${name}"` })
  },

  startTemplate() {
    set((s) => ({
      draft: { restore: s.topology },
      topology: { version: SCHEMA_VERSION, name: '', nodes: [], edges: [] },
      loadSeq: s.loadSeq + 1,
      selectedNodeId: null,
      selectedEdgeId: null,
      plan: null,
      opStatus: {},
      validation: [],
      leftTab: 'elements',
    }))
  },

  async saveTemplate() {
    const s = get()
    if (!s.draft) return
    const name = s.topology.name.trim()
    if (!name || s.topology.nodes.length === 0) return
    const taken = (n: string) => s.presets.some((p) => p.name === n) || s.userTemplates.some((t) => t.name === n)
    const unique = uniqueId(name, taken)
    const saved = clone({ ...s.topology, name: unique })
    const userTemplates = [...s.userTemplates, saved]
    set({ userTemplates, draft: null, topology: { ...s.topology, name: unique }, leftTab: 'templates' })
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `saved template "${unique}"` })
    await persistTemplates(set, get, userTemplates, () => backend.saveUserTemplate(saved))
  },

  cancelTemplate() {
    const draft = get().draft
    if (!draft) return
    set((s) => ({ draft: null, topology: draft.restore, loadSeq: s.loadSeq + 1, selectedNodeId: null, selectedEdgeId: null }))
  },

  async deleteTemplate(name) {
    const userTemplates = get().userTemplates.filter((t) => t.name !== name)
    if (userTemplates.length === get().userTemplates.length) return
    set({ userTemplates })
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `deleted template "${name}"` })
    await persistTemplates(set, get, userTemplates, () => backend.deleteUserTemplate(name))
  },

  async exportTemplate(name) {
    const { presets, userTemplates } = get()
    const template = presets.find((p) => p.name === name) ?? userTemplates.find((t) => t.name === name)
    if (!template || !hasBackend()) return
    try {
      await backend.exportTemplate(template)
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'library', message: `export of "${name}": ${errorMessage(err)}` })
    }
  },

  generateDraft(bias) {
    if (!get().draft) return
    set((s) => ({
      topology: generateTopology(bias, s.topology.name),
      loadSeq: s.loadSeq + 1,
      selectedNodeId: null,
      selectedEdgeId: null,
      plan: null,
      opStatus: {},
      validation: [],
    }))
  },
})
