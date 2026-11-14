#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
release_version=${1:-dev}
case "$release_version" in ''|*[!A-Za-z0-9._-]*) printf '%s\n' 'Invalid release version' >&2; exit 2;; esac
mkdir -p dist
release_stage=$(mktemp -d "${TMPDIR:-/tmp}/agentview-release.XXXXXX")
trap 'rm -rf "$release_stage"' EXIT HUP INT TERM
for release_os in darwin linux; do
  for release_arch in amd64 arm64; do
    release_name="agentview_${release_version}_${release_os}_${release_arch}"
    mkdir -p "$release_stage/$release_name"
    CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build -trimpath -ldflags "-s -w -X main.version=$release_version" -o "$release_stage/$release_name/agentview" ./cmd/agentview
    cp LICENSE README.md "$release_stage/$release_name/"
    COPYFILE_DISABLE=1 tar -czf "dist/$release_name.tar.gz" -C "$release_stage" "$release_name"
  done
done
python3 - "$release_version" <<'PY'
import hashlib, pathlib, sys
version = sys.argv[1]
archives = sorted(pathlib.Path('dist').glob(f'agentview_{version}_*.tar.gz'))
with pathlib.Path(f'dist/agentview_{version}_checksums.txt').open('w') as output:
    for archive in archives:
        output.write(f'{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n')
PY
printf '%s\n' "Release packages prepared in dist/ for $release_version"
