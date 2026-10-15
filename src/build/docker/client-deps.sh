#!/usr/bin/env bash
# Runs INSIDE the builder container, from /work. Brings the client's
# node_modules (a named volume, see the Makefile) in line with
# client/package-lock.json.
#
# The volume outlives dependency changes, and Wails only runs `npm install`
# when package.json no longer matches the committed package.json.md5, so a
# volume filled before a dependency was added would be used as is. A stamp of
# the lockfile the volume was installed from catches that: when it differs,
# reinstall with `npm ci`.
set -euo pipefail

cd client
stamp=node_modules/.package-lock.md5
want=$(md5sum package-lock.json | cut -d' ' -f1)

if [ "$(cat "$stamp" 2>/dev/null)" != "$want" ]; then
  echo "==> npm ci (client dependencies changed)"
  npm ci
  echo "$want" > "$stamp"
fi
