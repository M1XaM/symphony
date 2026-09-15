import { fmtNum } from '../lib/format'
import { selectIsApplied, selectMetrics, selectPhase, selectStatuses, useSymphonyStore } from '../store/useSymphonyStore'

export function MetricTiles() {
  const m = useSymphonyStore(selectMetrics)
  const statuses = useSymphonyStore(selectStatuses)
  const phase = useSymphonyStore(selectPhase)
  const applied = useSymphonyStore(selectIsApplied)
  const runtime = useSymphonyStore((s) => s.runtime)

  const total = Object.keys(statuses).length
  const healthy = Object.values(statuses).filter((s) => s.state === 'healthy' || s.state === 'degraded').length

  const tiles = [
    { label: 'Sagas / sec', value: fmtNum(m?.sagas_per_sec, 1), unit: '' },
    { label: 'E2E p99', value: fmtNum(m?.p99_ms, 1), unit: 'ms' },
    { label: 'Failed', value: fmtNum((m?.error_rate ?? 0) * 100, 1), unit: '%' },
    { label: 'Consumer lag', value: fmtNum(m?.consumer_lag), unit: 'msgs' },
    { label: 'CPU', value: fmtNum(m?.cpu_pct, 0), unit: '%' },
    { label: 'Faults', value: String(m?.active_faults ?? 0), unit: '' },
  ]

  return (
    <div className="summary">
      <div className="pane-title">Run summary</div>
      <div className="summary-row">
        <span className="muted">phase</span>
        <span className={`pill pill-${phase}`}>{phase.replace('_', ' ')}</span>
      </div>
      <div className="summary-row">
        <span className="muted">nodes up</span>
        <span className="mono">
          {healthy}/{total}
        </span>
      </div>
      <div className="summary-row">
        <span className="muted">numbers from</span>
        <span className="mono small">{m?.source === 'blocks' ? 'block counters' : runtime?.kind === 'fake' ? 'mock (no docker)' : applied ? 'waiting…' : 'idle'}</span>
      </div>
      <div className="tiles">
        {tiles.map((t) => (
          <div key={t.label} className="tile">
            <div className="tile-label">{t.label}</div>
            <div className="tile-value">
              {t.value}
              {t.unit ? <span className="tile-unit"> {t.unit}</span> : null}
            </div>
          </div>
        ))}
      </div>
      <div className="muted small">select a node to inspect and tune it</div>
    </div>
  )
}
