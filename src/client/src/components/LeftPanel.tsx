import { useSymphonyStore } from '../store/useSymphonyStore'
import type { Topology } from '../types/topology'
import { Palette } from './Palette'

function TemplateRow({ template, custom }: { template: Topology; custom: boolean }) {
  const isActive = useSymphonyStore((s) => s.topology.name === template.name)
  // Live against the engine, which is a different thing from being on the
  // canvas: you can be looking at one template while another one runs.
  const isLive = useSymphonyStore((s) => s.appliedName === template.name)
  const phase = useSymphonyStore((s) => s.phase)
  const loadTemplate = useSymphonyStore((s) => s.loadTemplate)
  const deleteTemplate = useSymphonyStore((s) => s.deleteTemplate)

  return (
    <div className={`template-row${isActive ? ' is-active' : ''}`}>
      <button type="button" className="template-item" onClick={() => loadTemplate(template.name)}>
        <span className="template-head">
          <span className="template-name">{template.name}</span>
          {isLive ? <span className={`live-dot live-dot-${phase}`} title={phase.replace('_', ' ')} aria-label={phase} /> : null}
        </span>
        <span className="template-meta">
          {template.nodes.length} nodes · {template.edges.length} edges{custom ? ' · custom' : ''}
        </span>
      </button>
      {custom ? (
        <button
          type="button"
          className="template-del"
          title={`delete "${template.name}"`}
          aria-label={`delete ${template.name}`}
          onClick={() => deleteTemplate(template.name)}
        >
          ×
        </button>
      ) : null}
    </div>
  )
}

/** The preconfigured architectures, then the user's own, then — always last —
 *  the way to build another one. */
function Templates() {
  const presets = useSymphonyStore((s) => s.presets)
  const userTemplates = useSymphonyStore((s) => s.userTemplates)
  const startTemplate = useSymphonyStore((s) => s.startTemplate)

  return (
    <div className="template-list">
      {presets.map((t) => (
        <TemplateRow key={t.name} template={t} custom={false} />
      ))}
      {userTemplates.map((t) => (
        <TemplateRow key={t.name} template={t} custom />
      ))}
      <button type="button" className="btn btn-add" onClick={startTemplate}>
        <span className="btn-plus">+</span> Add template
      </button>
    </div>
  )
}

/** Default state: a two-way slider over the elements and the templates, with
 *  "add template" pinned underneath both. */
function Browser() {
  const tab = useSymphonyStore((s) => s.leftTab)
  const setLeftTab = useSymphonyStore((s) => s.setLeftTab)

  return (
    <>
      <div className="seg-wrap">
        <div className={`seg seg-${tab}`} role="tablist" aria-label="left panel">
          <span className="seg-thumb" aria-hidden="true" />
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'elements'}
            className={`seg-btn${tab === 'elements' ? ' is-active' : ''}`}
            onClick={() => setLeftTab('elements')}
          >
            Elements
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'templates'}
            className={`seg-btn${tab === 'templates' ? ' is-active' : ''}`}
            onClick={() => setLeftTab('templates')}
          >
            Templates
          </button>
        </div>
      </div>

      <div className="left-scroll">
        {tab === 'elements' ? (
          <>
            <Palette />
            <div className="palette-help">drag onto the canvas · drag handle to handle to connect · Del removes</div>
          </>
        ) : (
          <Templates />
        )}
      </div>
    </>
  )
}

/** Build state: name it, drag it together on the cleared canvas, save it. */
function Builder() {
  const name = useSymphonyStore((s) => s.topology.name)
  const setName = useSymphonyStore((s) => s.setName)
  const nodeCount = useSymphonyStore((s) => s.topology.nodes.length)
  const saveTemplate = useSymphonyStore((s) => s.saveTemplate)
  const cancelTemplate = useSymphonyStore((s) => s.cancelTemplate)

  const canSave = name.trim().length > 0 && nodeCount > 0

  return (
    <>
      <div className="panel-head">
        <span>New template</span>
        <span className="toolbar-spacer" />
        <span className="panel-head-note">{nodeCount} placed</span>
      </div>

      <div className="left-scroll">
        <div className="field">
          <label className="field-label" htmlFor="template-name">
            name
          </label>
          <input
            id="template-name"
            className="input"
            value={name}
            placeholder="e.g. Fan-out saga"
            spellCheck={false}
            autoFocus
            onChange={(e) => setName(e.target.value)}
          />
        </div>

        <div className="section-title">Elements</div>
        <Palette />
        <div className="palette-help">the canvas is cleared — drag elements onto it and connect their handles</div>
      </div>

      <div className="left-foot left-foot-split">
        <button type="button" className="btn btn-small" onClick={cancelTemplate}>
          Cancel
        </button>
        <button
          type="button"
          className="btn btn-accent btn-small"
          disabled={!canSave}
          title={canSave ? 'save as a new template' : 'name it and place at least one element'}
          onClick={saveTemplate}
        >
          Save
        </button>
      </div>
    </>
  )
}

export function LeftPanel() {
  const building = useSymphonyStore((s) => s.draft !== null)
  return <div className="left-panel">{building ? <Builder /> : <Browser />}</div>
}
