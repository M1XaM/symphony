#!/usr/bin/env bash
# Runs INSIDE the builder container (see Dockerfile). cwd is /work, which is
# the bind-mounted src/ directory. Invoked by `make build`.
#
#   PLATFORM   wails -platform value (default linux/amd64; windows/amd64 also
#              cross-compiles from here, darwin does not)
#   HOST_UID / HOST_GID
#              owner to give the files the build writes into the bind mount
#   Extra arguments are passed through to `wails build`.
set -euo pipefail

PLATFORM="${PLATFORM:-linux/amd64}"

tags=()
case "$PLATFORM" in
  linux/*) tags=(-tags webkit2_41) ;;  # Debian bookworm ships webkit2gtk 4.1
esac

# node_modules is a named volume so the host copy (possibly built for another
# OS) is never touched. Wails skips `npm install` when package.json.md5 matches
# and node_modules exists, which is true for a fresh empty volume, so drop the
# checksum until the volume has actually been populated.
if [ ! -d client/node_modules/.bin ]; then
  rm -f client/package.json.md5
fi

echo "==> wails build -platform $PLATFORM ${tags[*]:-}"
wails build -platform "$PLATFORM" "${tags[@]}" "$@"

# Hand ownership of everything written into the bind mount back to the host
# user (only matters on Linux hosts, where bind mounts keep container uids).
if [ -n "${HOST_UID:-}" ]; then
  chown -R "$HOST_UID:${HOST_GID:-$HOST_UID}" \
    build/bin client/dist client/wailsjs client/package.json.md5 go.sum 2>/dev/null || true
fi
