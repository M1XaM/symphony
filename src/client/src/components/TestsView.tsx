import { useMemo, useState } from 'react'
import { fmtNum } from '../lib/format'
import { selectDockerUp, selectIsApplied, useSymphonyStore } from '../store/useSymphonyStore'
import type { Scenario } from '../types/topology'
import { NumberInput } from './ui/NumberInput'

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
              <NumberInput className="input-num" value={Number(v)} onValue={(n) => onChange({ ...values, [k]: Number.isNaN(n) ? 0 : n })} />
            ) : (
              <input className="input" value={String(v)} onChange={(e) => onChange({ ...values, [k]: e.target.value })} />
            )}
          </label>
        )
      })}
    </div>
  )
}

/** The scenario catalogue and the one being run; what a run measured is in
 *  the Runs tab. */
export function TestsView() {
  const scenarios = useSymphonyStore((s) => s.scenarios)
  const active = useSymphonyStore((s) => s.activeTest)
  const name = useSymphonyStore((s) => s.topology.name)
  // the kinds on the canvas as one sorted string, so dragging a node (a new
  // topology object per frame) does not re-render the tab
  const kindKey = useSymphonyStore((s) => [...new Set(s.topology.nodes.map((n) => n.kind))].sort().join(','))
  const applied = useSymphonyStore(selectIsApplied)
  const dockerUp = useSymphonyStore(selectDockerUp)
  const startTest = useSymphonyStore((s) => s.startTest)
  const abortTest = useSymphonyStore((s) => s.abortTest)
  const [selected, setSelected] = useState<string>(scenarios[0]?.id ?? '')
  const [params, setParams] = useState<Record<string, unknown>>({})

  const kinds = useMemo(() => new Set<string>(kindKey ? kindKey.split(',') : []), [kindKey])
  const scenario = scenarios.find((s) => s.id === selected) ?? scenarios[0]
  const blocked = scenario ? blockedReason(scenario, kinds) : 'no scenarios'
  const pct = active && active.total_ms > 0 ? Math.min(100, (active.elapsed_ms / active.total_ms) * 100) : 0

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
      </div>
    </div>
  )
}
