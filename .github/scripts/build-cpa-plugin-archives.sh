#!/usr/bin/env bash
set -euo pipefail

# Build CPA Linux/Windows archives for plugin support.
# Variants:
#   linux default       : CGO=1 glibc  -> can dlopen plugins (Ubuntu/Debian)
#   linux _no-plugin    : CGO=0 static -> Alpine/OpenWrt portable, NO plugins
#   linux _musl         : CGO=1 musl   -> Alpine plugin-capable
#   windows             : CGO=0 host   -> plugins via LoadDLL

ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
BACKEND_DIR="${BACKEND_DIR:-$ROOT/src/CLIProxyAPI}"
DIST_DIR="${DIST_DIR:-$BACKEND_DIR/dist}"
VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-unknown}"
BUILD_DATE="${BUILD_DATE:-unknown}"
PLUGIN_ROOT="${PLUGIN_ROOT:-$ROOT/dist/plugins}"
VER_NUM="${VERSION#v}"
GO_ALPINE_IMAGE="${GO_ALPINE_IMAGE:-}"

mkdir -p "$DIST_DIR"
cd "$BACKEND_DIR"

detect_go_alpine_image() {
  if [[ -n "$GO_ALPINE_IMAGE" ]]; then
    echo "$GO_ALPINE_IMAGE"
    return
  fi
  local ver="1.26"
  if [[ -f go.mod ]]; then
    ver="$(awk '/^go / {print $2; exit}' go.mod)"
  fi
  ver="$(printf '%s\n' "$ver" | awk -F. '{print $1"."$2}')"
  echo "golang:${ver}-alpine"
}

embed_plugin() {
  local work_dir="$1" goos="$2" goarch="$3" kind="$4"
  local pext="so"
  [[ "$goos" == "windows" ]] && pext="dll"

  local plugin_src=""
  case "$kind" in
    musl)
      plugin_src="$PLUGIN_ROOT/linux-musl/${goarch}/grok-manager.${pext}"
      ;;
    *)
      plugin_src="$PLUGIN_ROOT/${goos}/${goarch}/grok-manager.${pext}"
      ;;
  esac

  if [[ -f "$plugin_src" ]]; then
    mkdir -p "$work_dir/plugins/${goos}/${goarch}"
    cp -f "$plugin_src" "$work_dir/plugins/${goos}/${goarch}/grok-manager.${pext}"
    if [[ "$kind" == "musl" ]]; then
      cp -f "$plugin_src" "$work_dir/plugins/${goos}/${goarch}/grok-manager-linux-${goarch}-musl.${pext}"
    else
      cp -f "$plugin_src" "$work_dir/plugins/${goos}/${goarch}/grok-manager-${goos}-${goarch}.${pext}"
    fi
    echo "embedded plugin ($kind): plugins/${goos}/${goarch}/grok-manager.${pext}"
  else
    echo "warning: plugin missing for ${goos}/${goarch} kind=${kind}: $plugin_src" >&2
  fi
}

package_archive() {
  local work_dir="$1" goos="$2" goarch="$3" suffix="${4:-}"
  local archive_name
  if [[ "$goos" == "windows" ]]; then
    archive_name="CPA_${VER_NUM}_${goos}_${goarch}${suffix}.zip"
    (
      cd "$work_dir"
      zip -qr "$DIST_DIR/$archive_name" .
    )
  else
    archive_name="CPA_${VER_NUM}_${goos}_${goarch}${suffix}.tar.gz"
    tar -C "$work_dir" -czf "$DIST_DIR/$archive_name" .
  fi
  echo "built $DIST_DIR/$archive_name"
}

copy_docs() {
  local work_dir="$1"
  for f in LICENSE README.md README_CN.md config.example.yaml; do
    [[ -f "$f" ]] && cp -f "$f" "$work_dir/" || true
  done
}

build_one() {
  local goos="$1" goarch="$2" cgo="$3" cc="${4:-}" suffix="${5:-}" kind="${6:-glibc}"
  local out_dir work_dir binary_name
  out_dir="$DIST_DIR/build/${goos}_${goarch}${suffix}"
  work_dir="$out_dir/archive"
  rm -rf "$out_dir"
  mkdir -p "$work_dir"

  binary_name="CPA"
  if [[ "$goos" == "windows" ]]; then
    binary_name="CPA.exe"
  fi

  echo "Building ${goos}/${goarch} cgo=${cgo} kind=${kind} suffix=${suffix:-none}"
  (
    export GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="$cgo"
    if [[ -n "$cc" ]]; then
      export CC="$cc"
      if [[ "$cc" == *gcc ]]; then
        export CXX="${cc%gcc}g++"
      fi
    fi
    go build -buildvcs=false \
      -ldflags="-s -w -X main.Version=${VER_NUM} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
      -o "$work_dir/${binary_name}" ./cmd/server/
  )

  if [[ "$cgo" == "0" && "$goos" == "linux" ]]; then
    if command -v readelf >/dev/null 2>&1; then
      if readelf -l "$work_dir/${binary_name}" | grep -q 'Requesting program interpreter'; then
        echo "no-plugin binary unexpectedly requires dynamic interpreter" >&2
        readelf -l "$work_dir/${binary_name}" >&2 || true
        exit 1
      fi
    fi
  fi

  if [[ "$cgo" == "1" || "$goos" == "windows" ]]; then
    embed_plugin "$work_dir" "$goos" "$goarch" "$kind"
  fi

  copy_docs "$work_dir"
  package_archive "$work_dir" "$goos" "$goarch" "$suffix"
}

build_musl_one() {
  local goarch="$1"
  local platform="linux/amd64"
  [[ "$goarch" == "arm64" ]] && platform="linux/arm64"

  local out_dir work_dir image
  out_dir="$DIST_DIR/build/linux_${goarch}_musl"
  work_dir="$out_dir/archive"
  rm -rf "$out_dir"
  mkdir -p "$work_dir"
  image="$(detect_go_alpine_image)"

  echo "Building linux/${goarch} musl via docker image=$image platform=$platform"
  docker run --rm --platform "$platform" \
    -v "$ROOT:/workspace" \
    -w /workspace/src/CLIProxyAPI \
    -e VER_NUM="$VER_NUM" \
    -e COMMIT="$COMMIT" \
    -e BUILD_DATE="$BUILD_DATE" \
    -e GOARCH="$goarch" \
    "$image" \
    sh -ec '
      set -euo pipefail
      apk add --no-cache build-base binutils >/dev/null
      CGO_ENABLED=1 GOOS=linux GOARCH="${GOARCH}" go build -buildvcs=false \
        -ldflags="-s -w -X main.Version=${VER_NUM} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
        -o "/workspace/src/CLIProxyAPI/dist/build/linux_${GOARCH}_musl/archive/CPA" ./cmd/server/
      # musl dynamic binary should request ld-musl interpreter
      if command -v readelf >/dev/null 2>&1; then
        readelf -l "/workspace/src/CLIProxyAPI/dist/build/linux_${GOARCH}_musl/archive/CPA" | grep -E "program interpreter|Requesting program interpreter" || true
      fi
    '

  [[ -s "$work_dir/CPA" ]] || { echo "musl CPA binary missing for $goarch" >&2; exit 1; }
  embed_plugin "$work_dir" linux "$goarch" musl
  copy_docs "$work_dir"
  package_archive "$work_dir" linux "$goarch" "_musl"
}

# Linux plugin-capable (glibc)
build_one linux amd64 1 gcc "" glibc
build_one linux arm64 1 aarch64-linux-gnu-gcc "" glibc

# Linux portable no-plugin (static / any libc when plugins are not needed)
build_one linux amd64 0 "" "_no-plugin" none
build_one linux arm64 0 "" "_no-plugin" none

# Linux Alpine/musl plugin-capable
if command -v docker >/dev/null 2>&1; then
  build_musl_one amd64
  build_musl_one arm64
else
  echo "warning: docker not available; skip musl CPA archives" >&2
fi

# Windows (plugins via LoadDLL; no CGO needed on host)
build_one windows amd64 0 x86_64-w64-mingw32-gcc "" windows

(
  cd "$DIST_DIR"
  rm -f checksums.txt
  if command -v sha256sum >/dev/null 2>&1; then
    # include all CPA archives (goreleaser darwin/freebsd + custom linux/windows)
    shopt -s nullglob
    files=(CPA_*.tar.gz CPA_*.zip)
    if [[ ${#files[@]} -gt 0 ]]; then
      sha256sum "${files[@]}" | sort -k2 > checksums.txt
    fi
  fi
  ls -lh CPA_* checksums.txt 2>/dev/null || true
)

echo "CPA linux/windows archives ready in $DIST_DIR"
