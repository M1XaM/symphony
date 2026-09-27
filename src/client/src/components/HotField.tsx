import type { ReactElement } from 'react'
import type { ConfigValue } from '../types/topology'

interface Bounds {
  min: number
  max: number
  step: number
}

/** Slider range guessed from the key's unit suffix; good enough for the block configs. */
function boundsFor(key: string, value: number): Bounds {
  if (key.endsWith('_rate') || key.endsWith('_ratio')) return { min: 0, max: 1, step: 0.01 }
  if (key.endsWith('_ms')) return { min: 0, max: Math.max(5000, value * 2), step: 10 }
  if (key.endsWith('_rps')) return { min: 0, max: Math.max(1000, value * 2), step: 10 }
  if (key.endsWith('_max') || key.startsWith('retry')) return { min: 0, max: 10, step: 1 }
  if (key.endsWith('_records')) return { min: 1, max: Math.max(2000, value * 2), step: 10 }
  return { min: 0, max: Math.max(100, value * 2), step: 1 }
}

const LOG_LEVELS = ['debug', 'info', 'warn', 'error']

interface Props {
  name: string
  value: ConfigValue
  mode: 'hot' | 'cold'
  onChange(value: ConfigValue): void
}

/** Renders one config entry. Hot numbers get a live slider; cold ones a plain input. */
export function ConfigField({ name, value, mode, onChange }: Props) {
  const hot = mode === 'hot'
  let control: ReactElement

  if (typeof value === 'boolean') {
    control = <input type="checkbox" checked={value} onChange={(e) => onChange(e.target.checked)} />
  } else if (typeof value === 'number') {
    const b = boundsFor(name, value)
    control = (
      <div className="field-num">
        {hot ? (
          <input
            type="range"
            min={b.min}
            max={b.max}
            step={b.step}
            value={value}
            onChange={(e) => onChange(Number(e.target.value))}
          />
        ) : null}
        <input
          type="number"
          className="input input-num"
          step={b.step}
          value={value}
          onChange={(e) => {
            const n = Number(e.target.value)
            if (!Number.isNaN(n)) onChange(n)
          }}
        />
      </div>
    )
  } else if (name === 'log_level') {
    control = (
      <select className="input" value={value} onChange={(e) => onChange(e.target.value)}>
        {LOG_LEVELS.map((l) => (
          <option key={l} value={l}>
            {l}
          </option>
        ))}
      </select>
    )
  } else {
    control = <input className="input" value={value} onChange={(e) => onChange(e.target.value)} spellCheck={false} />
  }

  return (
    <div className={`field field-${mode}`}>
      <label className="field-label">
        <span className="mono">{name}</span>
        {hot ? <span className="tag tag-hot">live</span> : <span className="tag tag-cold">requires restart</span>}
      </label>
      {control}
    </div>
  )
}
