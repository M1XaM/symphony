// Subscribe to backend events forwarded through Wails' runtime.
import { EventsOff, EventsOn } from '../../wailsjs/runtime/runtime'
import { hasBackend } from './backend'

export function onEvent<T>(name: string, handler: (payload: T) => void): () => void {
  if (!hasBackend()) return () => {}
  const cancel = EventsOn(name, (payload: T) => handler(payload))
  return () => {
    if (typeof cancel === 'function') cancel()
    else EventsOff(name)
  }
}
