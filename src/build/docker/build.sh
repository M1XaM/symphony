#!/usr/bin/env bash
# Runs INSIDE the builder container (see Dockerfile). cwd is /work, which is
# the bind-mounted src/ directory. Invoked by `make build`: builds the window
# (wails) and symphony-cli (the same package with -tags cli) into build/bin.
#
#   PLATFORM   wails -platform value (default linux/amd64; windows/amd64 also
#              cross-compiles from here, darwin does not)
#   HOST_UID / HOST_GID
#              owner to give the files the build writes into the bind mount
#   ONLY=cli   build symphony-cli alone (`make cli`)
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

if [ "${ONLY:-}" != cli ]; then
  echo "==> wails build -platform $PLATFORM ${tags[*]:-}"
  wails build -platform "$PLATFORM" "${tags[@]}" "$@"
fi

# No webview and no cgo, so it cross-compiles like any Go program.
cli=build/bin/symphony-cli
case "$PLATFORM" in windows/*) cli+=.exe ;; esac
echo "==> go build -tags cli ($PLATFORM) -> $cli"
CGO_ENABLED=0 GOOS="${PLATFORM%%/*}" GOARCH="${PLATFORM##*/}" \
  go build -tags cli -trimpath -ldflags "-s -w" -o "$cli" .

# Hand ownership of everything written into the bind mount back to the host
# user (only matters on Linux hosts, where bind mounts keep container uids).
if [ -n "${HOST_UID:-}" ]; then
  chown -R "$HOST_UID:${HOST_GID:-$HOST_UID}" \
    build/bin client/dist client/wailsjs client/package.json.md5 go.sum 2>/dev/null || true
fi
