// The webview's one store, assembled from slices: the topology being edited
// (editor), what the engine runs and says (engine), the panels around the map
// (panels), the templates (library) and the Tests tab (tests). Components
// read it through the selectors in ./selectors.
import { create } from 'zustand'
import { bindStore } from './batch'
import { createEditorSlice } from './slices/editor'
import { createEngineSlice } from './slices/engine'
import { createLibrarySlice } from './slices/library'
import { createPanelsSlice } from './slices/panels'
import { createTestsSlice } from './slices/tests'
import type { SymphonyState } from './types'

export const useSymphonyStore = create<SymphonyState>()((...a) => ({
  ...createEditorSlice(...a),
  ...createEngineSlice(...a),
  ...createPanelsSlice(...a),
  ...createLibrarySlice(...a),
  ...createTestsSlice(...a),
}))

// backend events are applied in batches; this is where they land
bindStore((fn) => useSymphonyStore.setState(fn))

export * from './selectors'
export type { Deployment, DrawerTab, LogLineEntry, PanelSide, SimSample, SymphonyState } from './types'
