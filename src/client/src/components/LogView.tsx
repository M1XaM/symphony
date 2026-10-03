import { memo, useEffect, useMemo, useRef, useState } from 'react'
import { fmtTime } from '../lib/format'
import { selectDockerUp, useSymphonyStore, type LogLineEntry } from '../store/useSymphonyStore'
import { Select } from './ui/Select'

/** How many lines are put in the DOM at once. The store keeps LOG_CAP of them
 *  and the filter searches all of them; only the tail is rendered, because a
 *  few thousand live rows cost more to diff than they are worth. */
const RENDER_CAP = 500

/** How often the source list and the picked container's log are read again. */
const REFRESH_MS = 2000

/** The source that is always there: the engine's own log. */
const SYSTEM = { value: '', label: 'System' }

/** Lines never change once logged, so a row renders once and every later
 *  flush only has to add the new ones. */
const LogRow = memo(function LogRow({ line }: { line: LogLineEntry }) {
  return (
    <div className={`log-line log-${line.level}`}>
      <span className="log-ts">{line.ts ? fmtTime(line.ts) : ''}</span>
      <span className="log-src" title={line.source}>
        {line.source}
      </span>
      <span className="log-msg">{line.message}</span>
    </div>
  )
})

/** One tab for every log there is. "System" is what the engine says; each of
 *  the other sources is one of Symphony's containers, and picking it shows
 *  what that container has written, which is what `docker logs` would print
 *  for it. The filter matches source and message alike. */
export function LogView() {
  const system = useSymphonyStore((s) => s.log)
  const clearLog = useSymphonyStore((s) => s.clearLog)
  const source = useSymphonyStore((s) => s.logSource)
  const setLogSource = useSymphonyStore((s) => s.setLogSource)
  const containers = useSymphonyStore((s) => s.logContainers)
  const containerLog = useSymphonyStore((s) => s.containerLog)
  const error = useSymphonyStore((s) => s.containerLogError)
  const refreshLogContainers = useSymphonyStore((s) => s.refreshLogContainers)
  const refreshContainerLog = useSymphonyStore((s) => s.refreshContainerLog)
  const dockerUp = useSymphonyStore(selectDockerUp)
  const [filter, setFilter] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  const stickRef = useRef(true)

  // While the tab is open the list of containers follows the daemon, and so
  // does the log of the picked one; nothing is asked behind a hidden window.
  useEffect(() => {
    if (!dockerUp) return
    const tick = () => {
      if (document.hidden) return
      void refreshLogContainers()
      void refreshContainerLog()
    }
    tick()
    const id = window.setInterval(tick, REFRESH_MS)
    document.addEventListener('visibilitychange', tick)
    return () => {
      window.clearInterval(id)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [dockerUp, source, refreshLogContainers, refreshContainerLog])

  // a container that is gone stays in the list while it is the one shown
  const options = useMemo(() => {
    const out = [SYSTEM, ...containers.map((c) => ({ value: c.name, label: c.name }))]
    if (source && !containers.some((c) => c.name === source)) out.push({ value: source, label: source })
    return out
  }, [containers, source])

  const log = source ? containerLog : system
  const needle = filter.trim().toLowerCase()
  const matched = useMemo(
    () => (needle ? log.filter((l) => l.source.toLowerCase().includes(needle) || l.message.toLowerCase().includes(needle)) : log),
    [log, needle],
  )
  const lines = useMemo(() => (matched.length > RENDER_CAP ? matched.slice(-RENDER_CAP) : matched), [matched])
  const hidden = matched.length - lines.length

  // a newly picked source starts at its end
  useEffect(() => {
    stickRef.current = true
  }, [source])

  useEffect(() => {
    const el = ref.current
    if (el && stickRef.current) el.scrollTop = el.scrollHeight
  }, [lines])

  const onScroll = () => {
    const el = ref.current
    if (!el) return
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8
  }

  return (
    <div className="log">
      <div className="log-actions">
        <span className="muted">
          {needle ? `${matched.length} of ${log.length}` : `${log.length}`} lines
          {hidden > 0 ? ` · showing the last ${RENDER_CAP}` : ''}
        </span>
        <span className="log-tools">
          <Select className="select-inline log-source" value={source} options={options} onChange={setLogSource} aria-label="log source" />
          <input
            className="input input-inline log-filter"
            value={filter}
            placeholder="filter"
            spellCheck={false}
            onChange={(e) => setFilter(e.target.value)}
            aria-label="filter log lines"
          />
          {source ? null : (
            <button type="button" className="btn btn-ghost" onClick={clearLog}>
              clear
            </button>
          )}
        </span>
      </div>
      <div className="log-lines" ref={ref} onScroll={onScroll}>
        {lines.map((l) => (
          <LogRow key={l.id} line={l} />
        ))}
        {source && error ? (
          <div className="log-line log-error">
            <span className="log-msg">{error}</span>
          </div>
        ) : null}
        {log.length === 0 && !(source && error) ? <div className="muted">{source ? 'nothing logged yet' : 'waiting for events…'}</div> : null}
        {log.length > 0 && matched.length === 0 ? <div className="muted">nothing matches "{filter}"</div> : null}
      </div>
    </div>
  )
}
