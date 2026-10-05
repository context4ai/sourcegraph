#!/usr/bin/env bash
# Pinned build dependency only; Bun is not installed on runtime hosts.
set -euo pipefail
if command -v bun >/dev/null && [[ "$(bun --version)" == 1.3.9 ]]; then
  exit 0
fi
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] || {
  echo 'Install Bun 1.3.9 for the local build platform.' >&2
  exit 1
}
mkdir -p .tmp/build-tools
archive="$(mktemp "$PWD/.tmp/build-tools/bun.XXXXXX.zip")"
trap 'rm -f "$archive"' EXIT
curl --fail --location --retry 2 --connect-timeout 15 --max-time 180 \
  https://github.com/oven-sh/bun/releases/download/bun-v1.3.9/bun-linux-x64-baseline.zip -o "$archive"
python3 - "$archive" <<'PY'
import hashlib
import pathlib
import sys
import zipfile

archive = pathlib.Path(sys.argv[1])
expected = '104d4d037f4b35e10215c0507e1779691f39c57bd91ddeefe11cad781e3fc4b9'
if hashlib.sha256(archive.read_bytes()).hexdigest() != expected:
    raise SystemExit('Bun archive SHA256 mismatch')
target = pathlib.Path('.tmp/build-tools/bun')
with zipfile.ZipFile(archive) as bundle:
    target.write_bytes(bundle.read('bun-linux-x64-baseline/bun'))
target.chmod(0o755)
PY
[[ "$(.tmp/build-tools/bun --version)" == 1.3.9 ]]
