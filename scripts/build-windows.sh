#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
app_version=$(sed -n 's/^const appVersion = "\([^"]*\)"/\1/p' version.go)
if [[ -z "$app_version" ]]; then
  echo 'Could not read appVersion from version.go' >&2
  exit 1
fi
output_dir=${1:-bin}
mkdir -p "$output_dir"
# Use a pinned build-only resource compiler; it does not become a runtime dependency.
go run scripts/build-resources.go
GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags '-H=windowsgui' -o "$output_dir/soundstreamer-$app_version.exe" .
printf 'Built %s/soundstreamer-%s.exe\n' "$output_dir" "$app_version"
