import { selectBusy, selectDockerUp, selectIsApplied, selectPhase, selectUnsaved, useSymphonyStore } from '../store/useSymphonyStore'

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
  const phase = useSymphonyStore(selectPhase)
  const applied = useSymphonyStore(selectIsApplied)
  const busy = useSymphonyStore(selectBusy)
  const dockerUp = useSymphonyStore(selectDockerUp)
  const validation = useSymphonyStore((s) => s.validation)
  const validate = useSymphonyStore((s) => s.validate)
  const planApply = useSymphonyStore((s) => s.planApply)
  const apply = useSymphonyStore((s) => s.apply)
  const teardown = useSymphonyStore((s) => s.teardown)
  const unsaved = useSymphonyStore(selectUnsaved)
  const builtIn = useSymphonyStore((s) => s.presets.some((p) => p.name === s.topology.name))
  const saveChanges = useSymphonyStore((s) => s.saveChanges)

  const errors = validation.filter((v) => v.severity === 'error').length
  const warnings = validation.length - errors

  return (
    <div className="toolbar">
      <input
        className="toolbar-name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        readOnly={applied}
        title={applied ? 'a running topology keeps its name until it is torn down' : undefined}
        spellCheck={false}
        aria-label="topology name"
      />
      <div className="toolbar-spacer" />
      {unsaved ? (
        <button
          type="button"
          className="btn btn-accent"
          onClick={() => void saveChanges()}
          title={builtIn ? 'a built-in template cannot be changed: saves what is on the map as a new template' : `save the changes to "${name}"`}
        >
          Save
        </button>
      ) : null}
      <button type="button" className="btn" onClick={validate} disabled={busy}>
        Validate
        {validation.length > 0 ? (
          <span className={`badge ${errors ? 'badge-error' : 'badge-warn'}`}>{errors ? `${errors} err` : `${warnings} warn`}</span>
        ) : null}
      </button>
      <button type="button" className="btn" onClick={planApply} disabled={busy}>
        Plan
      </button>
      <button type="button" className="btn btn-accent" onClick={apply} disabled={busy || !dockerUp} title={dockerUp ? undefined : 'needs Docker'}>
        Apply
      </button>
      <button
        type="button"
        className="btn"
        onClick={teardown}
        disabled={busy || !applied || !dockerUp}
        title={!dockerUp ? 'needs Docker' : applied ? `tear down "${name}"` : 'nothing running under this name'}
      >
        Teardown
      </button>
      <span className={`pill pill-${phase}`}>{PHASE_LABEL[phase] ?? phase}</span>
    </div>
  )
}
