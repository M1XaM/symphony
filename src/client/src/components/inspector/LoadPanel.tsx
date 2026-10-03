import { useEffect, useState } from 'react'
import { fmtNum } from '../../lib/format'
import { selectIsApplied, useSymphonyStore } from '../../store/useSymphonyStore'
import type { TopologyNode } from '../../types/topology'
import { NumberInput } from '../ui/NumberInput'
import { Select } from '../ui/Select'

const PATTERNS = ['constant', 'ramp', 'burst', 'sine', 'step']

/** Load generator controls, shown on the gateway node. */
export function LoadPanel({ node }: { node: TopologyNode }) {
  const control = useSymphonyStore((s) => s.control)
  const applied = useSymphonyStore(selectIsApplied)
  const [rps, setRps] = useState(50)
  const [pattern, setPattern] = useState('constant')
  const [duration, setDuration] = useState(60)
  const [status, setStatus] = useState<Record<string, unknown> | null>(null)

  useEffect(() => {
    if (!applied) return
    let stop = false
    const tick = async () => {
      const res = await control(node.id, { op: 'load.status' })
      if (!stop && res?.state && typeof res.state === 'object') setStatus(res.state as Record<string, unknown>)
    }
    void tick()
    const id = window.setInterval(tick, 2000)
    return () => {
      stop = true
      window.clearInterval(id)
    }
  }, [applied, control, node.id])

  const running = status?.running === true
  return (
    <>
      <div className="section-title">Load generator</div>
      <div className="field">
        <div className="load-row">
          <label className="field-label">rps</label>
          <NumberInput className="input-num" min={1} value={rps} onValue={(n) => setRps(Math.max(1, n || 1))} />
          <label className="field-label">pattern</label>
          <Select value={pattern} options={PATTERNS} onChange={setPattern} aria-label="pattern" />
          <label className="field-label">seconds</label>
          <NumberInput className="input-num" min={0} value={duration} onValue={(n) => setDuration(Math.max(0, n || 0))} />
        </div>
        <div className="load-actions">
          <button
            type="button"
            className="btn btn-accent btn-small"
            disabled={!applied}
            onClick={() =>
              void control(node.id, {
                op: 'load.set',
                params: { rps, pattern, to_rps: rps * 8, factor: 4, duration_ms: duration * 1000, concurrency: 128 },
              })
            }
          >
            {running ? 'restart' : 'start'}
          </button>
          <button type="button" className="btn btn-small" disabled={!applied || !running} onClick={() => void control(node.id, { op: 'load.stop' })}>
            stop
          </button>
          <span className="muted small">
            {running
              ? `running · target ${fmtNum(status?.target_rps as number, 0)} rps · achieved ${fmtNum(status?.achieved_rps as number, 0)} · sent ${String(status?.sent ?? 0)}`
              : applied
                ? 'idle'
                : 'apply the topology first'}
          </span>
        </div>
      </div>
    </>
  )
}
