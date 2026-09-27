import { useSymphonyStore } from '../store/useSymphonyStore'

/** Shown above the toolbar while the Docker daemon does not answer: every
 *  topology runs as Docker containers, so nothing can be applied until it
 *  does. The engine keeps asking and the notice goes away by itself. */
export function DockerNotice() {
  const runtime = useSymphonyStore((s) => s.runtime)
  if (!runtime || runtime.available) return null
  return (
    <div className="docker-notice" role="alert">
      <div className="docker-notice-title">Docker is not available</div>
      <div>
        Symphony runs every topology as Docker containers. Install Docker Engine (Linux) or Docker Desktop (Windows, macOS) and
        start it; Symphony connects by itself as soon as the daemon answers. Until then topologies can be drawn, validated and
        planned, but not applied.
      </div>
      <div className="mono small docker-notice-detail">{runtime.detail}</div>
    </div>
  )
}
