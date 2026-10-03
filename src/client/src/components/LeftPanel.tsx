import { useState } from 'react'
import { useSymphonyStore } from '../store/useSymphonyStore'
import type { Topology } from '../types/topology'
import { Palette } from './Palette'
import { DotsMenu, type MenuItem } from './ui/DotsMenu'

function TemplateRow({ template, custom }: { template: Topology; custom: boolean }) {
  const isActive = useSymphonyStore((s) => s.topology.name === template.name)
  // Live against the engine, which is a different thing from being on the
  // canvas: any number of templates can run while you look at another one.
  const phase = useSymphonyStore((s) => s.deployments[template.name]?.phase)
  const loadTemplate = useSymphonyStore((s) => s.loadTemplate)
  const deleteTemplate = useSymphonyStore((s) => s.deleteTemplate)
  const exportTemplate = useSymphonyStore((s) => s.exportTemplate)

  // the built-in templates cannot be deleted
  const items: MenuItem[] = [{ label: 'Export template', onSelect: () => void exportTemplate(template.name) }]
  if (custom) items.push({ label: 'Delete', danger: true, onSelect: () => void deleteTemplate(template.name) })

  return (
    <div className={`template-row${isActive ? ' is-active' : ''}`}>
      <button type="button" className="template-item" onClick={() => loadTemplate(template.name)}>
        <span className="template-name">{template.name}</span>
        <span className="template-meta">
          {template.nodes.length} nodes · {template.edges.length} edges{custom ? ' · custom' : ''}
        </span>
      </button>
      {/* always there, lit or not, so the dot sits at the same place on every card */}
      <span className="template-live">
        {phase !== undefined ? <span className={`live-dot live-dot-${phase}`} title={phase.replace('_', ' ')} aria-label={phase} /> : null}
      </span>
      <DotsMenu label={`more for ${template.name}`} items={items} />
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

/** How many places the generator's slider has between its two ends. */
const STYLE_STEPS = 50

/** Build state: name it, drag it together on the cleared canvas, save it. */
function Builder() {
  const name = useSymphonyStore((s) => s.topology.name)
  const setName = useSymphonyStore((s) => s.setName)
  const nodeCount = useSymphonyStore((s) => s.topology.nodes.length)
  const saveTemplate = useSymphonyStore((s) => s.saveTemplate)
  const cancelTemplate = useSymphonyStore((s) => s.cancelTemplate)
  const generateDraft = useSymphonyStore((s) => s.generateDraft)
  const importDraft = useSymphonyStore((s) => s.importDraft)
  // the middle of the scale until it is moved; moving it draws a topology
  const [style, setStyle] = useState(STYLE_STEPS / 2)

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
          <div className="field-note">English letters, digits and special characters</div>
        </div>

        <div className="section-title">Elements</div>
        <Palette />
        <div className="palette-help">the canvas is cleared — drag elements onto it and connect their handles</div>

        <div className="section-title">Generate</div>
        <div className="field">
          <input
            type="range"
            className="style-slider"
            min={0}
            max={STYLE_STEPS}
            step={1}
            value={style}
            aria-label="generate a topology, from orchestration to choreography"
            onChange={(e) => {
              const v = Number(e.target.value)
              setStyle(v)
              void generateDraft(v / STYLE_STEPS)
            }}
          />
          <div className="style-ends">
            <span>orchestration</span>
            <span>choreography</span>
          </div>
        </div>
        <div className="palette-help">every move of the slider draws a new random topology, leaning to the side it is nearer; add to it or change it afterwards</div>

        <div className="section-title">Import</div>
        <div className="field">
          <button type="button" className="btn btn-import" onClick={() => void importDraft()}>
            Import from a file…
          </button>
        </div>
        <div className="palette-help">puts a template exported earlier (a JSON file) on the map, to change and save as a new one</div>
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
