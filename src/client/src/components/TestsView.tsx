import { useEffect, useMemo, useRef, useState } from 'react'
import { fmtNum } from '../lib/format'
import { selectDockerUp, selectIsApplied, useSymphonyStore } from '../store/useSymphonyStore'
import type { ReportFormat, Run, RunSummary, Scenario } from '../types/topology'
import { Sparkline } from './Sparkline'

const SERIES_CHOICES = ['sagas_per_sec', 'latency_p99_ms', 'error_rate', 'consumer_lag', 'cpu_pct']
const SERIES_CLASSES = ['series-1', 'series-2', 'series-3', 'series-4']

/** Why a scenario cannot run on the canvas topology, mirroring tests.Check. */
function blockedReason(s: Scenario, kinds: Set<string>): string {
  for (const k of s.requires.kinds ?? []) if (!kinds.has(k)) return `needs a ${k} node`
  if (s.requires.needs_proxy && !kinds.has('proxy')) return 'needs a proxy'
  if (s.requires.needs_broker && !kinds.has('broker')) return 'needs a broker'
  if (!kinds.has('infra')) return 'needs a gateway'
  return ''
}

function ParamsForm({ scenario, values, onChange }: { scenario: Scenario; values: Record<string, unknown>; onChange(v: Record<string, unknown>): void }) {
  const keys = Object.keys(scenario.params).sort()
  return (
    <div className="test-params">
      {keys.map((k) => {
        const def = scenario.params[k]
        const v = values[k] ?? def
        return (
          <label key={k} className="test-param">
            <span className="mono">{k}</span>
            {typeof def === 'number' ? (
              <input className="input input-num" type="number" value={Number(v)} onChange={(e) => onChange({ ...values, [k]: Number(e.target.value) })} />
            ) : (
              <input className="input" value={String(v)} onChange={(e) => onChange({ ...values, [k]: e.target.value })} />
            )}
          </label>
        )
      })}
    </div>
  )
}

function RunDetail({ run, onClose }: { run: Run; onClose(): void }) {
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
        <span className="toolbar-spacer" />
        <button type="button" className="btn btn-ghost btn-small" onClick={onClose}>
          close
        </button>
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
            <select className="input input-inline" value={series} onChange={(e) => setSeries(e.target.value)}>
              {keys.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
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
            <select className="input input-inline" value={series} onChange={(e) => setSeries(e.target.value)}>
              {SERIES_CHOICES.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </div>
          <Sparkline title={series} series={lines} cap={cap} format={(v) => fmtNum(v, 2)} />
        </div>
      </div>
    </div>
  )
}

/** The three dots at the end of a finished run's row: its report, as
 *  Markdown or as PDF. While the report is being written the dots give way
 *  to a spinner; the save dialog follows. */
function RunMenu({ runId, busy, disabled, onExport }: { runId: string; busy: boolean; disabled: boolean; onExport(format: ReportFormat): void }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLSpanElement>(null)
  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false)
    }
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', key)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', key)
    }
  }, [open])
  const pick = (format: ReportFormat) => {
    setOpen(false)
    onExport(format)
  }
  if (busy) {
    return (
      <span className="run-menu" title="writing the report…">
        <span className="spinner" role="status" aria-label="writing the report" />
      </span>
    )
  }
  return (
    <span className="run-menu" ref={ref}>
      <button
        type="button"
        className="btn btn-ghost btn-small"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`more for run ${runId}`}
        title="export the report"
        disabled={disabled}
        onClick={() => setOpen(!open)}
      >
        ⋯
      </button>
      {open ? (
        <span className="run-menu-list" role="menu">
          <button type="button" role="menuitem" className="run-menu-item" onClick={() => pick('md')}>
            Export MD
          </button>
          <button type="button" role="menuitem" className="run-menu-item" onClick={() => pick('pdf')}>
            Export PDF
          </button>
        </span>
      ) : null}
    </span>
  )
}

export function TestsView() {
  const scenarios = useSymphonyStore((s) => s.scenarios)
  const runs = useSymphonyStore((s) => s.runs)
  const active = useSymphonyStore((s) => s.activeTest)
  const openRun = useSymphonyStore((s) => s.openRun)
  const compareIds = useSymphonyStore((s) => s.compareIds)
  const compareRuns = useSymphonyStore((s) => s.compareRuns)
  const name = useSymphonyStore((s) => s.topology.name)
  // the kinds on the canvas as one sorted string, so dragging a node (a new
  // topology object per frame) does not re-render the tab
  const kindKey = useSymphonyStore((s) => [...new Set(s.topology.nodes.map((n) => n.kind))].sort().join(','))
  const applied = useSymphonyStore(selectIsApplied)
  const dockerUp = useSymphonyStore(selectDockerUp)
  const startTest = useSymphonyStore((s) => s.startTest)
  const abortTest = useSymphonyStore((s) => s.abortTest)
  const viewRun = useSymphonyStore((s) => s.viewRun)
  const deleteRun = useSymphonyStore((s) => s.deleteRun)
  const toggleCompare = useSymphonyStore((s) => s.toggleCompare)
  const exportingRun = useSymphonyStore((s) => s.exportingRun)
  const exportRun = useSymphonyStore((s) => s.exportRun)
  const [selected, setSelected] = useState<string>(scenarios[0]?.id ?? '')
  const [params, setParams] = useState<Record<string, unknown>>({})

  // a run opened while it is still going is read again as it goes
  const watching = openRun?.running ? openRun.id : null
  useEffect(() => {
    if (!watching) return
    const id = window.setInterval(() => void viewRun(watching), 2000)
    return () => window.clearInterval(id)
  }, [watching, viewRun])

  const kinds = useMemo(() => new Set<string>(kindKey ? kindKey.split(',') : []), [kindKey])
  const scenario = scenarios.find((s) => s.id === selected) ?? scenarios[0]
  const blocked = scenario ? blockedReason(scenario, kinds) : 'no scenarios'
  const pct = active && active.total_ms > 0 ? Math.min(100, (active.elapsed_ms / active.total_ms) * 100) : 0

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
      <td>
        <button type="button" className="btn btn-ghost btn-small" onClick={() => void viewRun(r.id)}>
          open
        </button>
        {!r.running ? (
          <button type="button" className="btn btn-ghost btn-small" onClick={() => void deleteRun(r.id)}>
            ×
          </button>
        ) : null}
        {!r.running ? <RunMenu runId={r.id} busy={exportingRun === r.id} disabled={exportingRun !== null} onExport={(f) => void exportRun(r.id, f)} /> : null}
      </td>
    </tr>
  )

  return (
    <div className="tests">
      <div className="tests-left">
        <div className="pane-title">Scenarios</div>
        <div className="scenario-list">
          {scenarios.map((s) => {
            const why = blockedReason(s, kinds)
            return (
              <button
                key={s.id}
                type="button"
                className={`scenario${s.id === scenario?.id ? ' is-active' : ''}${why ? ' is-blocked' : ''}`}
                title={why || s.description}
                onClick={() => {
                  setSelected(s.id)
                  setParams({})
                }}
              >
                <span className="scenario-id mono">{s.id.slice(0, 3)}</span>
                <span className="scenario-name">{s.name}</span>
              </button>
            )
          })}
        </div>
      </div>
      <div className="tests-main">
        {scenario ? (
          <div className="scenario-detail">
            <div className="run-head">
              <span className="run-title">{scenario.name}</span>
              {scenario.tags?.map((t) => (
                <span key={t} className="cls cls-structural">
                  {t}
                </span>
              ))}
              <span className="toolbar-spacer" />
              {active ? (
                <button type="button" className="btn btn-danger btn-small" onClick={() => void abortTest()}>
                  abort
                </button>
              ) : (
                <button
                  type="button"
                  className="btn btn-accent btn-small"
                  disabled={!!blocked || !dockerUp}
                  title={blocked || (applied ? `run on "${name}"` : `applies "${name}" first, then runs`)}
                  onClick={() => void startTest(scenario.id, params)}
                >
                  run on {name}
                </button>
              )}
            </div>
            <div className="muted small">{scenario.description}</div>
            {scenario.reveals ? <div className="muted small">reveals: {scenario.reveals}</div> : null}
            {blocked ? <div className="finding finding-warning">{blocked}</div> : null}
            <ParamsForm scenario={scenario} values={params} onChange={setParams} />
            {active ? (
              <div className="test-progress">
                <div className="progress">
                  <div className="progress-bar" style={{ width: `${pct}%` }} />
                </div>
                <span className="muted small">
                  {active.phase} · {fmtNum(active.elapsed_ms / 1000, 0)}s / {fmtNum(active.total_ms / 1000, 0)}s
                  {active.live
                    ? ` · ${fmtNum(active.live.sagas_per_sec, 1)} sagas/s · p99 ${fmtNum(active.live.latency_p99_ms, 0)} ms · ${fmtNum((active.live.error_rate ?? 0) * 100, 1)}% failed`
                    : ''}
                </span>
              </div>
            ) : null}
          </div>
        ) : null}
        {compareRuns.length >= 2 ? <CompareView runs={compareRuns} /> : openRun ? <RunDetail run={openRun} onClose={() => void viewRun(null)} /> : null}
        <div className="pane-title">Runs</div>
        {runs.length === 0 ? <div className="muted small">no runs yet — pick a scenario and run it on the canvas template, then on the other one, and compare</div> : null}
        {runs.length > 0 ? (
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
        ) : null}
      </div>
    </div>
  )
}
