import { Component, type ErrorInfo, type ReactNode } from 'react'

interface State {
  error: Error | null
}

/** Without this, an error thrown while rendering unmounts the whole app and
 *  leaves an empty window. The engine and its containers are not affected
 *  by a view failing, so say what happened and offer the window back. */
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error('render failed', error, info.componentStack)
  }

  render(): ReactNode {
    const { error } = this.state
    if (!error) return this.props.children
    return (
      <div className="crash">
        <div className="crash-title">The window hit an error and stopped drawing.</div>
        <div className="muted small">Nothing was lost: the deployments keep running and their state is stored.</div>
        <pre className="crash-detail mono small">{error.message}</pre>
        <button type="button" className="btn btn-accent" onClick={() => window.location.reload()}>
          reload the window
        </button>
      </div>
    )
  }
}
