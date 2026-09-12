import { useEffect, useRef } from 'react'
import { fmtTime } from '../lib/format'
import { useSymphonyStore } from '../store/useSymphonyStore'

export function LogView() {
  const log = useSymphonyStore((s) => s.log)
  const clearLog = useSymphonyStore((s) => s.clearLog)
  const ref = useRef<HTMLDivElement>(null)
  const stickRef = useRef(true)

  useEffect(() => {
    const el = ref.current
    if (el && stickRef.current) el.scrollTop = el.scrollHeight
  }, [log])

  const onScroll = () => {
    const el = ref.current
    if (!el) return
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8
  }

  return (
    <div className="log">
      <div className="log-actions">
        <span className="muted">{log.length} lines</span>
        <button type="button" className="btn btn-ghost" onClick={clearLog}>
          clear
        </button>
      </div>
      <div className="log-lines" ref={ref} onScroll={onScroll}>
        {log.map((l) => (
          <div key={l.id} className={`log-line log-${l.level}`}>
            <span className="log-ts">{fmtTime(l.ts)}</span>
            <span className="log-src">{l.source}</span>
            <span className="log-msg">{l.message}</span>
          </div>
        ))}
        {log.length === 0 ? <div className="muted">waiting for events…</div> : null}
      </div>
    </div>
  )
}
