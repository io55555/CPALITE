#!/usr/bin/env bash
set -euo pipefail

# Build grok-manager plugin shared libraries for release packaging.
# Layout:
#   dist/plugins/linux/<arch>/grok-manager.so                 # glibc inject
#   dist/plugins/linux-musl/<arch>/grok-manager.so            # musl/Alpine inject
#   dist/plugins/windows/<arch>/grok-manager.dll
#   dist/plugins/release/grok-manager-<os>-<arch>[.musl].<ext>
#   dist/plugins/release/grok-manager-v<ver>-<os>-<arch>[.musl].<ext>

ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
ROOT="$(cd "$ROOT" && pwd)"

if [[ -z "${PLUGIN_SRC:-}" ]]; then
  PLUGIN_SRC="$ROOT/src/grok-manager"
elif [[ "$PLUGIN_SRC" != /* ]]; then
  PLUGIN_SRC="$ROOT/$PLUGIN_SRC"
fi

if [[ -z "${OUT_ROOT:-}" ]]; then
  OUT_ROOT="$ROOT/dist/plugins"
elif [[ "$OUT_ROOT" != /* ]]; then
  OUT_ROOT="$ROOT/$OUT_ROOT"
fi

VERSION="${GROK_MANAGER_VERSION:-1.3.7}"
RELEASE_DIR="${OUT_ROOT}/release"
GO_ALPINE_IMAGE="${GO_ALPINE_IMAGE:-}"
BACKEND_GO_MOD="${BACKEND_GO_MOD:-$ROOT/src/CLIProxyAPI/go.mod}"

if [[ ! -d "$PLUGIN_SRC" ]]; then
  echo "plugin source not found: $PLUGIN_SRC" >&2
  exit 1
fi

mkdir -p "$OUT_ROOT" "$RELEASE_DIR"
export CGO_ENABLED=1

detect_go_alpine_image() {
  if [[ -n "$GO_ALPINE_IMAGE" ]]; then
    echo "$GO_ALPINE_IMAGE"
    return
  fi
  local ver="1.26"
  if [[ -f "$BACKEND_GO_MOD" ]]; then
    ver="$(awk '/^go / {print $2; exit}' "$BACKEND_GO_MOD")"
  fi
  # golang official tags accept major.minor and major.minor.patch
  ver="$(printf '%s\n' "$ver" | awk -F. '{print $1"."$2}')"
  echo "golang:${ver}-alpine"
}

publish_release_copies() {
  local src="$1" plain="$2" versioned="$3"
  cp -f "$src" "$plain"
  cp -f "$src" "$versioned"
  ls -lh "$src" "$plain" "$versioned"
}

build_glibc_one() {
  local goos="$1" goarch="$2" cc="$3" ext="$4"
  local out_dir="$OUT_ROOT/${goos}/${goarch}"
  local out_file="$out_dir/grok-manager.${ext}"
  local release_plain="$RELEASE_DIR/grok-manager-${goos}-${goarch}.${ext}"
  local release_ver="$RELEASE_DIR/grok-manager-v${VERSION}-${goos}-${goarch}.${ext}"

  mkdir -p "$out_dir"
  echo "Building grok-manager ${goos}/${goarch} (glibc/host-cc) -> $out_file"
  (
    cd "$PLUGIN_SRC"
    GOOS="$goos" GOARCH="$goarch" CC="$cc" \
      go build -buildvcs=false -buildmode=c-shared -trimpath -ldflags="-s -w" \
      -o "$out_file" .
  )
  rm -f "${out_file%.${ext}}.h" "$out_dir/grok-manager.h" || true
  publish_release_copies "$out_file" "$release_plain" "$release_ver"
}

build_musl_one() {
  local goarch="$1"
  local platform="linux/amd64"
  [[ "$goarch" == "arm64" ]] && platform="linux/arm64"

  local out_dir="$OUT_ROOT/linux-musl/${goarch}"
  local out_file="$out_dir/grok-manager.so"
  local release_plain="$RELEASE_DIR/grok-manager-linux-${goarch}-musl.so"
  local release_ver="$RELEASE_DIR/grok-manager-v${VERSION}-linux-${goarch}-musl.so"
  local image
  image="$(detect_go_alpine_image)"

  mkdir -p "$out_dir"
  local docker_out host_out plugin_rel
  if [[ "$OUT_ROOT" != "$ROOT"/* ]]; then
    echo "OUT_ROOT ($OUT_ROOT) must be inside ROOT ($ROOT) for musl docker builds" >&2
    exit 1
  fi
  plugin_rel="${OUT_ROOT#"$ROOT"/}/linux-musl/${goarch}/grok-manager.so"
  docker_out="/workspace/${plugin_rel}"
  host_out="$out_file"

  echo "Building grok-manager linux/${goarch} (musl/alpine docker: $image, platform=$platform)"
  echo "docker output path: $docker_out"
  docker run --rm --platform "$platform" \
    -v "$ROOT:/workspace" \
    -w /workspace/src/grok-manager \
    -e DOCKER_OUT="$docker_out" \
    -e GOARCH_BUILD="$goarch" \
    "$image" \
    sh -ec '
      set -euo pipefail
      apk add --no-cache build-base >/dev/null
      mkdir -p "$(dirname "$DOCKER_OUT")"
      CGO_ENABLED=1 GOOS=linux GOARCH="${GOARCH_BUILD}" \
        go build -buildvcs=false -buildmode=c-shared -trimpath -ldflags="-s -w" \
        -o "$DOCKER_OUT" .
      ls -lh "$DOCKER_OUT"
    '
  rm -f "${out_file%.so}.h" "$out_dir/grok-manager.h" || true
  [[ -s "$out_file" ]] || { echo "musl plugin missing: $out_file" >&2; exit 1; }
  publish_release_copies "$out_file" "$release_plain" "$release_ver"
}

# glibc / host-toolchain builds (plugin-capable Ubuntu/Debian hosts)
build_glibc_one linux amd64 gcc so
build_glibc_one linux arm64 aarch64-linux-gnu-gcc so
build_glibc_one windows amd64 x86_64-w64-mingw32-gcc dll

# musl builds for Alpine (same path style under linux-musl/)
if command -v docker >/dev/null 2>&1; then
  build_musl_one amd64
  build_musl_one arm64
else
  echo "warning: docker not available; skip musl plugin builds" >&2
fi

echo "Plugin inject layout:"
find "$OUT_ROOT" -type f \( -name 'grok-manager.so' -o -name 'grok-manager.dll' \) -print | sort
echo "Plugin release assets:"
find "$RELEASE_DIR" -type f \( -name '*.so' -o -name '*.dll' \) -print | sort
