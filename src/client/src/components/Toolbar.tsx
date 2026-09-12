import { useSymphonyStore } from '../store/useSymphonyStore'

const PHASE_LABEL: Record<string, string> = {
  idle: 'idle',
  validating: 'validating',
  planning: 'planning',
  applying: 'applying',
  running: 'running',
  tearing_down: 'tearing down',
  failed: 'failed',
}

export function Toolbar() {
  const name = useSymphonyStore((s) => s.topology.name)
  const setName = useSymphonyStore((s) => s.setName)
  const phase = useSymphonyStore((s) => s.phase)
  const busy = useSymphonyStore((s) => s.busy)
  const validation = useSymphonyStore((s) => s.validation)
  const validate = useSymphonyStore((s) => s.validate)
  const planApply = useSymphonyStore((s) => s.planApply)
  const apply = useSymphonyStore((s) => s.apply)
  const teardown = useSymphonyStore((s) => s.teardown)
  const nodeCount = useSymphonyStore((s) => s.topology.nodes.length)

  const errors = validation.filter((v) => v.severity === 'error').length
  const warnings = validation.length - errors

  return (
    <div className="toolbar">
      <input
        className="toolbar-name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        spellCheck={false}
        aria-label="topology name"
      />
      <span className="toolbar-count muted">{nodeCount} nodes</span>
      <div className="toolbar-spacer" />
      <button type="button" className="btn" onClick={validate} disabled={busy}>
        Validate
        {validation.length > 0 ? (
          <span className={`badge ${errors ? 'badge-error' : 'badge-warn'}`}>
            {errors ? `${errors} err` : `${warnings} warn`}
          </span>
        ) : null}
      </button>
      <button type="button" className="btn" onClick={planApply} disabled={busy}>
        Plan
      </button>
      <button type="button" className="btn btn-accent" onClick={apply} disabled={busy}>
        Apply
      </button>
      <button type="button" className="btn" onClick={teardown} disabled={busy}>
        Teardown
      </button>
      <span className={`pill pill-${phase}`}>{PHASE_LABEL[phase] ?? phase}</span>
    </div>
  )
}
