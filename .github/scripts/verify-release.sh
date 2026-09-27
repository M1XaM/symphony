#!/usr/bin/env bash
# Release gate: checks that neither binary brings a terminal or console
# window up with it. Run by .github/workflows/release.yml before anything is
# published; needs file, gio (libglib2.0-bin), shared-mime-info, xvfb-run,
# python3 and the WebKitGTK 4.1 runtime.
#
#   verify-release.sh <linux binary> <windows .exe>
set -euo pipefail

linux_bin="$1"
windows_exe="$2"
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== linux: $linux_bin"

# A plain executable, not a PIE that the desktop would call a shared library.
file -b "$linux_bin" | grep -q '^ELF 64-bit LSB executable' ||
  fail "not an ELF executable: $(file -b "$linux_bin")"

# What a file manager decides from: a name like "install" makes the binary
# text/x-install, which is opened as a script (in an editor or a terminal).
type=$(gio info -a standard::content-type "$linux_bin" | awk '/content-type/ {print $2}')
[ "$type" = application/x-executable ] || fail "detected as $type, not application/x-executable"
echo "content type: $type"

# Start it the way a launcher does: no terminal, no stdin, its own session.
# It has to stay up, and everything it starts has to be WebKit's own helpers.
# DOCKER_HOST points nowhere, so the run is the same whether or not the
# runner has Docker: the app opens and says Docker is missing.
home=$(mktemp -d)
log="$home/run.log"
chmod +x "$linux_bin"
HOME="$home" XDG_CONFIG_HOME="$home/config" XDG_DATA_HOME="$home/data" DOCKER_HOST="unix://$home/no-docker.sock" \
  setsid xvfb-run -a "$linux_bin" </dev/null >"$log" 2>&1 &
launcher=$!
disown
trap 'pkill -KILL -s "$launcher" 2>/dev/null || true' EXIT

app=""
for _ in $(seq 1 30); do
  app=$(pgrep -s "$launcher" -x "$(basename "$linux_bin" | cut -c1-15)" || true)
  [ -n "$app" ] && break
  sleep 1
done
[ -n "$app" ] || { cat "$log"; fail "the app never started"; }
sleep 10
kill -0 "$app" 2>/dev/null || { cat "$log"; fail "the app exited within 10 s"; }

descendants() { for c in $(pgrep -P "$1"); do echo "$c"; descendants "$c"; done; }
children=$(descendants "$app")
[ -n "$children" ] || fail "no WebKit processes: the webview did not start"
for pid in $children; do
  name=$(ps -o comm= -p "$pid" || true)
  [ -z "$name" ] && continue # already gone
  case "$name" in
    WebKit*) ;;
    *) fail "the app started '$name' (pid $pid); only WebKit helpers are expected" ;;
  esac
done
echo "running headless without a terminal; children: $(for p in $children; do ps -o comm= -p "$p"; done | sort | uniq -c | xargs)"

echo "== windows: $windows_exe"

# Subsystem 2 is a GUI program; 3 would open a console window next to it.
python3 - "$windows_exe" <<'EOF'
import struct, sys
data = open(sys.argv[1], 'rb').read()
pe = struct.unpack_from('<I', data, 0x3C)[0]
if data[pe:pe + 4] != b'PE\0\0':
    sys.exit('FAIL: not a PE file')
subsystem = struct.unpack_from('<H', data, pe + 24 + 68)[0]
if subsystem != 2:
    sys.exit(f'FAIL: PE subsystem {subsystem}, expected 2 (GUI)')
print('PE subsystem: 2 (GUI, no console window)')
EOF

echo "OK"
