import { errorMessage, hasBackend } from '../lib/backend'
import type { Topology } from '../types/topology'
import type { Get, Set } from './types'

const TEMPLATES_KEY = 'symphony.templates'

/** The templates the webview itself remembers. The backend's file is the real
 *  library; this mirrors it so a plain browser — or a machine where the config
 *  directory cannot be written — still remembers across restarts. */
export function readTemplates(): Topology[] {
  try {
    const raw = window.localStorage.getItem(TEMPLATES_KEY)
    const parsed = raw ? (JSON.parse(raw) as unknown) : null
    return Array.isArray(parsed) ? (parsed as Topology[]) : []
  } catch {
    return []
  }
}

export function writeTemplates(list: Topology[]): void {
  try {
    window.localStorage.setItem(TEMPLATES_KEY, JSON.stringify(list))
  } catch {
    // storage unavailable — the templates still live for this session
  }
}

/** Store the user's templates: in the backend's library through `call`, and
 *  mirrored in the webview. */
export async function persistTemplates(set: Set, get: Get, list: Topology[], call: () => Promise<Topology[]>): Promise<void> {
  if (!hasBackend()) {
    writeTemplates(list)
    return
  }
  try {
    const stored = await call()
    set({ userTemplates: stored })
    writeTemplates(stored)
  } catch (err) {
    writeTemplates(list)
    get().appendLog({
      ts: Date.now(),
      level: 'warn',
      source: 'library',
      message: `${errorMessage(err)} — template kept in this webview instead`,
    })
  }
}
