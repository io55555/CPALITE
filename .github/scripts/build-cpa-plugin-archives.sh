#!/usr/bin/env bash
set -euo pipefail

# Build CPA Linux/Windows archives for plugin support.
# Variants:
#   linux default       : CGO=1 glibc  -> can dlopen plugins (Ubuntu/Debian)
#   linux _no-plugin    : CGO=0 static -> Alpine/OpenWrt portable, NO plugins
#   linux _musl         : CGO=1 musl   -> Alpine plugin-capable
#   windows             : CGO=0 host   -> plugins via LoadDLL

ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
ROOT="$(cd "$ROOT" && pwd)"

# Accept relative or absolute BACKEND_DIR/DIST_DIR; never double-prefix GITHUB_WORKSPACE.
if [[ -z "${BACKEND_DIR:-}" ]]; then
  BACKEND_DIR="$ROOT/src/CLIProxyAPI"
elif [[ "$BACKEND_DIR" != /* ]]; then
  BACKEND_DIR="$ROOT/$BACKEND_DIR"
fi
BACKEND_DIR="$(cd "$BACKEND_DIR" && pwd)"

if [[ -z "${DIST_DIR:-}" ]]; then
  DIST_DIR="$BACKEND_DIR/dist"
elif [[ "$DIST_DIR" != /* ]]; then
  DIST_DIR="$ROOT/$DIST_DIR"
fi
mkdir -p "$DIST_DIR"
DIST_DIR="$(cd "$DIST_DIR" && pwd)"

if [[ -z "${PLUGIN_ROOT:-}" ]]; then
  PLUGIN_ROOT="$ROOT/dist/plugins"
elif [[ "$PLUGIN_ROOT" != /* ]]; then
  PLUGIN_ROOT="$ROOT/$PLUGIN_ROOT"
fi

VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-unknown}"
BUILD_DATE="${BUILD_DATE:-unknown}"
VER_NUM="${VERSION#v}"
GO_ALPINE_IMAGE="${GO_ALPINE_IMAGE:-}"

# Path of backend inside the docker mount at /workspace
if [[ "$BACKEND_DIR" == "$ROOT" ]]; then
  BACKEND_REL="."
elif [[ "$BACKEND_DIR" == "$ROOT"/* ]]; then
  BACKEND_REL="${BACKEND_DIR#"$ROOT"/}"
else
  echo "BACKEND_DIR ($BACKEND_DIR) must be inside ROOT ($ROOT) for musl docker builds" >&2
  exit 1
fi

echo "ROOT=$ROOT"
echo "BACKEND_DIR=$BACKEND_DIR"
echo "BACKEND_REL=$BACKEND_REL"
echo "DIST_DIR=$DIST_DIR"
echo "PLUGIN_ROOT=$PLUGIN_ROOT"

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

  local out_dir work_dir image docker_out host_out
  out_dir="$DIST_DIR/build/linux_${goarch}_musl"
  work_dir="$out_dir/archive"
  rm -rf "$out_dir"
  mkdir -p "$work_dir"
  image="$(detect_go_alpine_image)"

  # Write into the same host work_dir via the /workspace mount.
  # Prefer DIST_DIR if it is under ROOT; otherwise fall back to backend-relative dist.
  if [[ "$DIST_DIR" == "$ROOT"/* ]]; then
    docker_out="/workspace/${DIST_DIR#"$ROOT"/}/build/linux_${goarch}_musl/archive/CPA"
  else
    docker_out="/workspace/${BACKEND_REL}/dist/build/linux_${goarch}_musl/archive/CPA"
  fi
  host_out="$work_dir/CPA"

  echo "Building linux/${goarch} musl via docker image=$image platform=$platform"
  echo "docker output path: $docker_out"
  echo "host expect path:   $host_out"

  docker run --rm --platform "$platform" \
    -v "$ROOT:/workspace" \
    -w "/workspace/${BACKEND_REL}" \
    -e VER_NUM="$VER_NUM" \
    -e COMMIT="$COMMIT" \
    -e BUILD_DATE="$BUILD_DATE" \
    -e GOARCH="$goarch" \
    -e DOCKER_OUT="$docker_out" \
    "$image" \
    sh -ec '
      set -euo pipefail
      apk add --no-cache build-base binutils >/dev/null
      mkdir -p "$(dirname "$DOCKER_OUT")"
      CGO_ENABLED=1 GOOS=linux GOARCH="${GOARCH}" go build -buildvcs=false \
        -ldflags="-s -w -X main.Version=${VER_NUM} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
        -o "$DOCKER_OUT" ./cmd/server/
      if command -v readelf >/dev/null 2>&1; then
        readelf -l "$DOCKER_OUT" | grep -E "program interpreter|Requesting program interpreter" || true
      fi
      ls -lh "$DOCKER_OUT"
    '

  if [[ ! -s "$host_out" ]]; then
    echo "musl CPA binary missing for $goarch at $host_out" >&2
    echo "listing dist build tree:" >&2
    find "$DIST_DIR/build" -maxdepth 4 -type f -print >&2 || true
    exit 1
  fi
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
    shopt -s nullglob
    files=(CPA_*.tar.gz CPA_*.zip)
    if [[ ${#files[@]} -gt 0 ]]; then
      sha256sum "${files[@]}" | sort -k2 > checksums.txt
    fi
  fi
  ls -lh CPA_* checksums.txt 2>/dev/null || true
)

echo "CPA linux/windows archives ready in $DIST_DIR"