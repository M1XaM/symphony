import { useSymphonyStore } from '../store/useSymphonyStore'

export function PlanView() {
  const plan = useSymphonyStore((s) => s.plan)
  const opStatus = useSymphonyStore((s) => s.opStatus)
  const validation = useSymphonyStore((s) => s.validation)

  return (
    <div className="plan">
      {validation.length > 0 ? (
        <ul className="findings">
          {validation.map((f, i) => (
            <li key={i} className={`finding finding-${f.severity}`}>
              <span className="finding-sev">{f.severity}</span>
              <span className="finding-path">{f.path}</span>
              <span className="finding-msg">{f.message}</span>
            </li>
          ))}
        </ul>
      ) : null}

      {!plan ? (
        <div className="muted plan-empty">No plan yet. Click Plan to preview the operations Apply would run.</div>
      ) : plan.ops.length === 0 ? (
        <div className="muted plan-empty">Plan is empty: the running topology already matches.</div>
      ) : (
        <>
          <div className="plan-summary">
            <span>{plan.ops.length} ops</span>
            <span className="cls cls-hot">{plan.summary.hot} hot</span>
            <span className="cls cls-cold">{plan.summary.cold} cold</span>
            <span className="cls cls-structural">{plan.summary.structural} structural</span>
            <span className="muted">network {plan.network}</span>
          </div>
          <table className="plan-table">
            <thead>
              <tr>
                <th>#</th>
                <th>op</th>
                <th>target</th>
                <th>class</th>
                <th>reason</th>
              </tr>
            </thead>
            <tbody>
              {plan.ops.map((op, i) => {
                const st = opStatus[op.id]
                return (
                  <tr key={op.id} className={st ? `op-${st}` : ''}>
                    <td className="muted">{i + 1}</td>
                    <td className="mono">{op.kind}</td>
                    <td className="mono">{op.target}</td>
                    <td>
                      <span className={`cls cls-${op.class}`}>{op.class}</span>
                    </td>
                    <td className="plan-reason">
                      {op.reason}
                      {st ? <span className={`op-state op-state-${st}`}> {st}</span> : null}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}
