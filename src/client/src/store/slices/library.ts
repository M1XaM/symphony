import { backend, errorMessage, hasBackend } from '../../lib/backend'
import { SCHEMA_VERSION, type Topology } from '../../types/topology'
import { warn } from '../batch'
import { asciiName, clone, uniqueId } from '../editing'
import { persistTemplates, readTemplates } from '../templates'
import type { Get, LibrarySlice, Set, Slice } from '../types'

let generateSeq = 0

/** Swap a whole topology onto the canvas. */
function putOnCanvas(set: Set, topology: Topology): void {
  set((s) => ({ topology, loadSeq: s.loadSeq + 1, selectedNodeId: null, selectedEdgeId: null, plan: null, opStatus: {}, validation: [] }))
}

/** After a save the canvas takes the template as the library stored it (the
 *  engine fills in defaults), so the two are the same until the next edit. */
function adoptStored(set: Set, get: Get, name: string): void {
  const stored = get().userTemplates.find((t) => t.name === name)
  if (stored && get().topology.name === name) set({ topology: clone(stored) })
}

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
    adoptStored(set, get, unique)
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

  async generateDraft(bias) {
    if (!get().draft || !hasBackend()) return
    // the slider moves faster than the answers come: only the last one counts
    const seq = ++generateSeq
    try {
      const topology = await backend.generateTopology(bias, get().topology.name)
      if (seq === generateSeq && get().draft) putOnCanvas(set, { ...topology, name: get().topology.name })
    } catch (err) {
      warn(get, 'library', err)
    }
  },

  async importDraft() {
    if (!get().draft || !hasBackend()) return
    try {
      const topology = await backend.importTemplate()
      if (!topology || !get().draft) return
      putOnCanvas(set, { ...topology, name: asciiName(topology.name) })
      get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: `imported "${topology.name}": ${topology.nodes.length} nodes, ${topology.edges.length} edges` })
    } catch (err) {
      get().appendLog({ ts: Date.now(), level: 'error', source: 'library', message: `import: ${errorMessage(err)}` })
    }
  },

  async saveChanges() {
    const s = get()
    if (s.draft) return
    let name = s.topology.name
    const own = s.userTemplates.some((t) => t.name === name)
    if (!own) {
      if (!s.presets.some((p) => p.name === name)) return
      // a built-in template stays as it is; the changed one is saved beside it
      name = uniqueId(name, (n) => s.presets.some((p) => p.name === n) || s.userTemplates.some((t) => t.name === n))
    }
    const saved = clone({ ...s.topology, name })
    const userTemplates = own ? s.userTemplates.map((t) => (t.name === name ? saved : t)) : [...s.userTemplates, saved]
    set({ userTemplates, topology: saved })
    get().appendLog({ ts: Date.now(), level: 'info', source: 'ui', message: own ? `saved template "${name}"` : `saved as a new template "${name}"` })
    await persistTemplates(set, get, userTemplates, () => backend.saveUserTemplate(saved))
    adoptStored(set, get, name)
  },
})
