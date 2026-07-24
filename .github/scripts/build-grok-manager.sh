#!/usr/bin/env bash
set -euo pipefail

# Build grok-manager plugin shared libraries for release packaging.
# Outputs under dist/plugins/<os>/<arch>/grok-manager.<ext>

ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
PLUGIN_SRC="${PLUGIN_SRC:-$ROOT/src/grok-manager}"
OUT_ROOT="${OUT_ROOT:-$ROOT/dist/plugins}"
VERSION="${GROK_MANAGER_VERSION:-1.3.7}"

if [[ ! -d "$PLUGIN_SRC" ]]; then
  echo "plugin source not found: $PLUGIN_SRC" >&2
  exit 1
fi

mkdir -p "$OUT_ROOT"
export CGO_ENABLED=1

build_one() {
  local goos="$1" goarch="$2" cc="$3" ext="$4"
  local out_dir="$OUT_ROOT/${goos}/${goarch}"
  local out_file="$out_dir/grok-manager.${ext}"
  mkdir -p "$out_dir"
  echo "Building grok-manager ${goos}/${goarch} -> $out_file"
  (
    cd "$PLUGIN_SRC"
    GOOS="$goos" GOARCH="$goarch" CC="$cc" \
      go build -buildvcs=false -buildmode=c-shared -trimpath -ldflags="-s -w" \
      -o "$out_file" .
  )
  # also keep versioned copy next to unversioned
  cp -f "$out_file" "$out_dir/grok-manager-v${VERSION}.${ext}"
  # remove c-shared header noise if generated beside output
  rm -f "${out_file%.${ext}}.h" "$out_dir/grok-manager.h" || true
  ls -lh "$out_file"
}

# native linux amd64
build_one linux amd64 gcc so
# linux arm64 cross
build_one linux arm64 aarch64-linux-gnu-gcc so
# windows amd64 cross
build_one windows amd64 x86_64-w64-mingw32-gcc dll

echo "Plugin artifacts:"
find "$OUT_ROOT" -type f \( -name '*.so' -o -name '*.dll' \) -print