#!/usr/bin/env bash
set -euo pipefail

# Build grok-manager plugin shared libraries for release packaging.
# Layout:
#   dist/plugins/<os>/<arch>/grok-manager.<ext>                 # for CPA archive inject
#   dist/plugins/release/grok-manager-<os>-<arch>.<ext>         # GitHub release flat assets
#   dist/plugins/release/grok-manager-v<ver>-<os>-<arch>.<ext>  # versioned flat assets

ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
PLUGIN_SRC="${PLUGIN_SRC:-$ROOT/src/grok-manager}"
OUT_ROOT="${OUT_ROOT:-$ROOT/dist/plugins}"
VERSION="${GROK_MANAGER_VERSION:-1.3.7}"
RELEASE_DIR="${OUT_ROOT}/release"

if [[ ! -d "$PLUGIN_SRC" ]]; then
  echo "plugin source not found: $PLUGIN_SRC" >&2
  exit 1
fi

mkdir -p "$OUT_ROOT" "$RELEASE_DIR"
export CGO_ENABLED=1

build_one() {
  local goos="$1" goarch="$2" cc="$3" ext="$4"
  local out_dir="$OUT_ROOT/${goos}/${goarch}"
  local out_file="$out_dir/grok-manager.${ext}"
  local release_plain="$RELEASE_DIR/grok-manager-${goos}-${goarch}.${ext}"
  local release_ver="$RELEASE_DIR/grok-manager-v${VERSION}-${goos}-${goarch}.${ext}"

  mkdir -p "$out_dir"
  echo "Building grok-manager ${goos}/${goarch} -> $out_file"
  (
    cd "$PLUGIN_SRC"
    GOOS="$goos" GOARCH="$goarch" CC="$cc" \
      go build -buildvcs=false -buildmode=c-shared -trimpath -ldflags="-s -w" \
      -o "$out_file" .
  )
  # strip cgo generated headers
  rm -f "${out_file%.${ext}}.h" "$out_dir/grok-manager.h" || true

  # Flat release names must include os+arch (GitHub assets lose directories).
  cp -f "$out_file" "$release_plain"
  cp -f "$out_file" "$release_ver"
  ls -lh "$out_file" "$release_plain"
}

# native linux amd64
build_one linux amd64 gcc so
# linux arm64 cross
build_one linux arm64 aarch64-linux-gnu-gcc so
# windows amd64 cross
build_one windows amd64 x86_64-w64-mingw32-gcc dll

echo "Plugin inject layout:"
find "$OUT_ROOT" -type f \( -name 'grok-manager.so' -o -name 'grok-manager.dll' \) -print | sort
echo "Plugin release assets:"
find "$RELEASE_DIR" -type f \( -name '*.so' -o -name '*.dll' \) -print | sort