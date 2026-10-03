import { useEffect, useMemo, useState } from 'react'
import { fmtNum } from '../lib/format'
import { useSymphonyStore } from '../store/useSymphonyStore'
import type { Run, RunSummary } from '../types/topology'
import { Sparkline } from './Sparkline'
import { DotsMenu, type MenuItem } from './ui/DotsMenu'
import { Select } from './ui/Select'

const SERIES_CHOICES = ['sagas_per_sec', 'latency_p99_ms', 'error_rate', 'consumer_lag', 'cpu_pct']
const SERIES_CLASSES = ['series-1', 'series-2', 'series-3', 'series-4']

function RunDetail({ run }: { run: Run }) {
  const [series, setSeries] = useState(SERIES_CHOICES[0])
  const data = useMemo(() => (run.series[series] ?? []).map((p) => p.v), [run, series])
  const keys = Object.keys(run.series).sort()
  return (
    <div className="run-detail">
      <div className="run-head">
        <span className={`pill ${run.running ? 'pill-applying' : run.aborted ? '' : run.passed ? 'pill-running' : 'pill-failed'}`}>
          {run.running ? 'running' : run.aborted ? 'aborted' : run.passed ? 'passed' : 'failed'}
        </span>
        <span className="run-title">
          {run.name} on <b>{run.template}</b>
        </span>
        <span className="muted small">{new Date(run.started_at).toLocaleString()}</span>
      </div>
      {run.error ? <div className="finding finding-error">{run.error}</div> : null}
      <div className="run-grid">
        <div>
          <div className="pane-title">Summary</div>
          <table className="plan-table">
            <tbody>
              {Object.entries(run.summary)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([k, v]) => (
                  <tr key={k}>
                    <td className="mono">{k}</td>
                    <td className="mono">{fmtNum(v, 2)}</td>
                  </tr>
                ))}
            </tbody>
          </table>
          <div className="pane-title">Assertions</div>
          {run.assertions.length === 0 ? <div className="muted small">{run.running ? 'evaluated when the run ends' : 'none'}</div> : null}
          {run.assertions.map((a, i) => (
            <div key={i} className={`finding finding-${a.passed ? 'ok' : 'error'}`}>
              <span className="finding-sev">{a.passed ? 'pass' : 'fail'}</span>
              <span className="finding-path">
                {a.metric}
                {a.phase ? `@${a.phase}` : ''} {a.op} {a.value}
              </span>
              <span className="finding-msg">
                actual {fmtNum(a.actual, 2)}
                {a.note ? ` · ${a.note}` : ''}
              </span>
            </div>
          ))}
        </div>
        <div>
          <div className="chart-pick">
            <span className="pane-title">Series</span>
            <Select className="select-inline" value={series} options={keys} onChange={setSeries} aria-label="series" />
          </div>
          <Sparkline
            title={series}
            series={[{ name: run.template, data, className: 'series-1' }]}
            cap={Math.max(data.length, 60)}
            format={(v) => fmtNum(v, 2)}
            footer={run.phases.map((p) => `${p.name} ${fmtNum(p.start_s, 0)}–${fmtNum(p.end_s, 0)}s`).join(' · ')}
          />
          <div className="pane-title">Timeline</div>
          <div className="run-notes mono small">
            {run.notes.map((n, i) => (
              <div key={i}>{n}</div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

function CompareView({ runs }: { runs: Run[] }) {
  const [series, setSeries] = useState(SERIES_CHOICES[0])
  const keys = useMemo(() => {
    const set = new Set<string>()
    for (const r of runs) for (const k of Object.keys(r.summary)) set.add(k)
    return [...set].sort()
  }, [runs])
  const lines = runs.map((r, i) => ({ name: r.template, data: (r.series[series] ?? []).map((p) => p.v), className: SERIES_CLASSES[i % SERIES_CLASSES.length] }))
  const cap = Math.max(60, ...lines.map((l) => l.data.length))
  return (
    <div className="run-detail">
      <div className="run-head">
        <span className="run-title">Compare {runs.length} runs</span>
      </div>
      <div className="run-grid">
        <table className="plan-table">
          <thead>
            <tr>
              <th>metric</th>
              {runs.map((r) => (
                <th key={r.id}>
                  {r.template}
                  <div className="muted small">{r.name}</div>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k}>
                <td className="mono">{k}</td>
                {runs.map((r) => (
                  <td key={r.id} className="mono">
                    {r.summary[k] === undefined ? '—' : fmtNum(r.summary[k], 2)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        <div>
          <div className="chart-pick">
            <span className="pane-title">Series</span>
            <Select className="select-inline" value={series} options={SERIES_CHOICES} onChange={setSeries} aria-label="series" />
          </div>
          <Sparkline title={series} series={lines} cap={cap} format={(v) => fmtNum(v, 2)} />
        </div>
      </div>
    </div>
  )
}

/** The stored results of the Tests tab's scenarios: every run, the one that
 *  is open, and up to four ticked ones side by side. What can be done with a
 *  run is behind the three dots at the end of its row. */
export function RunsView() {
  const runs = useSymphonyStore((s) => s.runs)
  const openRun = useSymphonyStore((s) => s.openRun)
  const compareIds = useSymphonyStore((s) => s.compareIds)
  const compareRuns = useSymphonyStore((s) => s.compareRuns)
  const viewRun = useSymphonyStore((s) => s.viewRun)
  const deleteRun = useSymphonyStore((s) => s.deleteRun)
  const toggleCompare = useSymphonyStore((s) => s.toggleCompare)
  const exportingRun = useSymphonyStore((s) => s.exportingRun)
  const exportRun = useSymphonyStore((s) => s.exportRun)

  // a run opened while it is still going is read again as it goes
  const watching = openRun?.running ? openRun.id : null
  useEffect(() => {
    if (!watching) return
    const id = window.setInterval(() => void viewRun(watching), 2000)
    return () => window.clearInterval(id)
  }, [watching, viewRun])

  const menu = (r: RunSummary): MenuItem[] => {
    const isOpen = openRun?.id === r.id
    const items: MenuItem[] = [isOpen ? { label: 'Close', onSelect: () => void viewRun(null) } : { label: 'Open', onSelect: () => void viewRun(r.id) }]
    if (r.running) return items
    const busy = exportingRun !== null
    return [
      ...items,
      { label: 'Export MD', disabled: busy, onSelect: () => void exportRun(r.id, 'md') },
      { label: 'Export PDF', disabled: busy, onSelect: () => void exportRun(r.id, 'pdf') },
      { label: 'Delete', danger: true, onSelect: () => void deleteRun(r.id) },
    ]
  }

  const runRow = (r: RunSummary) => (
    <tr key={r.id} className={openRun?.id === r.id ? 'op-started' : ''}>
      <td>
        <input type="checkbox" checked={compareIds.includes(r.id)} disabled={r.running} onChange={() => void toggleCompare(r.id)} title="compare" />
      </td>
      <td>
        <span className={`pill ${r.running ? 'pill-applying' : r.aborted ? '' : r.passed ? 'pill-running' : 'pill-failed'}`}>
          {r.running ? 'running' : r.aborted ? 'aborted' : r.passed ? 'pass' : 'fail'}
        </span>
      </td>
      <td>{r.name}</td>
      <td>{r.template}</td>
      <td className="mono muted">{new Date(r.started_at).toLocaleTimeString()}</td>
      <td className="mono">{r.summary?.['sagas_per_sec.mean'] !== undefined ? fmtNum(r.summary['sagas_per_sec.mean'], 1) : '—'}</td>
      <td className="mono">{r.summary?.['latency_p99_ms.max'] !== undefined ? fmtNum(r.summary['latency_p99_ms.max'], 0) : '—'}</td>
      <td className="mono">{r.summary?.['error_rate.mean'] !== undefined ? `${fmtNum(r.summary['error_rate.mean'] * 100, 1)}%` : '—'}</td>
      <td className="mono">{r.summary?.recovery_time_s !== undefined ? `${fmtNum(r.summary.recovery_time_s, 1)}s` : '—'}</td>
      <td className="run-more">
        {exportingRun === r.id ? (
          <span className="spinner" role="status" aria-label="writing the report" title="writing the report…" />
        ) : (
          <DotsMenu label={`more for run ${r.id}`} items={menu(r)} />
        )}
      </td>
    </tr>
  )

  return (
    <div className="runs">
      {compareRuns.length >= 2 ? <CompareView runs={compareRuns} /> : openRun ? <RunDetail run={openRun} /> : null}
      {runs.length === 0 ? (
        <div className="muted small runs-empty">no runs yet — pick a scenario in the Tests tab and run it on the canvas template, then on the other one, and compare</div>
      ) : (
        <table className="plan-table">
          <thead>
            <tr>
              <th />
              <th>result</th>
              <th>scenario</th>
              <th>template</th>
              <th>started</th>
              <th>sagas/s</th>
              <th>p99 max</th>
              <th>failed</th>
              <th>recovery</th>
              <th />
            </tr>
          </thead>
          <tbody>{runs.map(runRow)}</tbody>
        </table>
      )}
    </div>
  )
}
