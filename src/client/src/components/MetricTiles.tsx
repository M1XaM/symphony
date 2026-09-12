import { fmtNum } from '../lib/format'
import { useSymphonyStore } from '../store/useSymphonyStore'

export function MetricTiles() {
  const m = useSymphonyStore((s) => s.metrics)
  const statuses = useSymphonyStore((s) => s.statuses)
  const phase = useSymphonyStore((s) => s.phase)

  const total = Object.keys(statuses).length
  const healthy = Object.values(statuses).filter((s) => s.state === 'healthy').length

  const tiles = [
    { label: 'E2E p99', value: fmtNum(m?.p99_ms, 1), unit: 'ms' },
    { label: 'Consumer lag', value: fmtNum(m?.consumer_lag), unit: 'msgs' },
    { label: 'Sagas / sec', value: fmtNum(m?.sagas_per_sec, 1), unit: '' },
    { label: 'CPU', value: fmtNum(m?.cpu_pct, 0), unit: '%' },
  ]

  return (
    <div className="summary">
      <div className="pane-title">Run summary</div>
      <div className="summary-row">
        <span className="muted">phase</span>
        <span className={`pill pill-${phase}`}>{phase.replace('_', ' ')}</span>
      </div>
      <div className="summary-row">
        <span className="muted">nodes healthy</span>
        <span className="mono">
          {healthy}/{total}
        </span>
      </div>
      <div className="summary-row">
        <span className="muted">active faults</span>
        <span className="mono">{m?.active_faults ?? 0}</span>
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
