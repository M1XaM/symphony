import { errorMessage } from '../lib/backend'
import type { Get, LogLineEntry, SymphonyState } from './types'

/** Log lines kept in the Log tab. */
const LOG_CAP = 2000
/** The engine talks in single events: a status per node, a stats sample per
 *  container, a line per line of container output. Handing each one to React
 *  on its own re-renders the app dozens of times a second, so they are queued
 *  and applied together in one update per interval. */
const EVENT_FLUSH_MS = 100

type Update = (s: SymphonyState) => Partial<SymphonyState>
type SetState = (fn: (s: SymphonyState) => SymphonyState) => void

let setState: SetState | null = null
let logSeq = 0
/** Log lines and event updates waiting for the next flush; see EVENT_FLUSH_MS. */
let pendingLines: LogLineEntry[] = []
let pendingUpdates: Update[] = []
let flushTimer: ReturnType<typeof setTimeout> | null = null

/** Tell the batcher which store it flushes into (the store calls this once,
 *  as it is created). */
export function bindStore(set: SetState): void {
  setState = set
}

function scheduleFlush(): void {
  if (flushTimer === null) flushTimer = setTimeout(flush, EVENT_FLUSH_MS)
}

/** Queue a state change driven by a backend event. */
export function queue(update: Update): void {
  pendingUpdates.push(update)
  scheduleFlush()
}

/** Queue a line for the Log tab. */
export function queueLine(line: Omit<LogLineEntry, 'id'>): void {
  pendingLines.push({ id: ++logSeq, ...line })
  scheduleFlush()
}

/** Forget the lines that have not reached the Log tab yet. */
export function dropQueuedLines(): void {
  pendingLines.length = 0
}

function flush(): void {
  flushTimer = null
  const updates = pendingUpdates
  const lines = pendingLines.length > LOG_CAP ? pendingLines.slice(-LOG_CAP) : pendingLines
  pendingUpdates = []
  pendingLines = []
  setState?.((s) => {
    let next = s
    for (const u of updates) next = { ...next, ...u(next) }
    if (lines.length > 0) {
      const keep = Math.max(0, next.log.length + lines.length - LOG_CAP)
      const log = keep > 0 ? next.log.slice(keep) : next.log.slice()
      for (const l of lines) log.push(l)
      next = { ...next, log }
    }
    return next
  })
}

/** Put a failed backend call in the Log tab as a warning. */
export function warn(get: Get, source: string, err: unknown): void {
  get().appendLog({ ts: Date.now(), level: 'warn', source, message: errorMessage(err) })
}
