import { memo, useEffect, useMemo, useRef, useState } from 'react'
import { fmtTime } from '../lib/format'
import { useSymphonyStore, type LogLineEntry } from '../store/useSymphonyStore'

/** How many lines are put in the DOM at once. The store keeps LOG_CAP of them
 *  and the filter searches all of them; only the tail is rendered, because a
 *  few thousand live rows cost more to diff than they are worth. */
const RENDER_CAP = 500

/** Lines never change once logged, so a row renders once and every later
 *  flush only has to add the new ones. */
const LogRow = memo(function LogRow({ line }: { line: LogLineEntry }) {
  return (
    <div className={`log-line log-${line.level}`}>
      <span className="log-ts">{fmtTime(line.ts)}</span>
      <span className="log-src" title={line.source}>
        {line.source}
      </span>
      <span className="log-msg">{line.message}</span>
    </div>
  )
})

/** Everything the engine says, plus — while the mirror is on — every line the
 *  containers write to stdout/stderr, which is what `docker logs -f` would
 *  print for them. The filter matches source and message alike. */
export function LogView() {
  const log = useSymphonyStore((s) => s.log)
  const clearLog = useSymphonyStore((s) => s.clearLog)
  const stream = useSymphonyStore((s) => s.logStream)
  const setLogStream = useSymphonyStore((s) => s.setLogStream)
  const [filter, setFilter] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  const stickRef = useRef(true)

  const needle = filter.trim().toLowerCase()
  const matched = useMemo(
    () => (needle ? log.filter((l) => l.source.toLowerCase().includes(needle) || l.message.toLowerCase().includes(needle)) : log),
    [log, needle],
  )
  const lines = useMemo(() => (matched.length > RENDER_CAP ? matched.slice(-RENDER_CAP) : matched), [matched])
  const hidden = matched.length - lines.length

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
          <input
            className="input input-inline log-filter"
            value={filter}
            placeholder="filter"
            spellCheck={false}
            onChange={(e) => setFilter(e.target.value)}
            aria-label="filter log lines"
          />
          <label className="ps-check" title="mirror every container’s stdout/stderr into this tab">
            <input
              type="checkbox"
              checked={stream.enabled}
              onChange={(e) => void setLogStream(e.target.checked)}
            />{' '}
            docker logs
          </label>
          <button type="button" className="btn btn-ghost" onClick={clearLog}>
            clear
          </button>
        </span>
      </div>
      <div className="log-lines" ref={ref} onScroll={onScroll}>
        {lines.map((l) => (
          <LogRow key={l.id} line={l} />
        ))}
        {log.length === 0 ? <div className="muted">waiting for events…</div> : null}
        {log.length > 0 && matched.length === 0 ? <div className="muted">nothing matches "{filter}"</div> : null}
      </div>
    </div>
  )
}
