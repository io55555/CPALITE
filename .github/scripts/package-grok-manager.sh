#!/usr/bin/env bash
set -euo pipefail

# Inject grok-manager plugin binaries into CPA release archives produced by GoReleaser.
# Inside archive path keeps CPA convention:
#   plugins/<os>/<arch>/grok-manager.<ext>

ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
BACKEND_DIR="${BACKEND_DIR:-$ROOT/src/CLIProxyAPI}"
PLUGIN_ROOT="${PLUGIN_ROOT:-$ROOT/dist/plugins}"
DIST_DIR="${DIST_DIR:-$BACKEND_DIR/dist}"

if [[ ! -d "$PLUGIN_ROOT" ]]; then
  echo "plugin root missing: $PLUGIN_ROOT" >&2
  exit 1
fi
if [[ ! -d "$DIST_DIR" ]]; then
  echo "dist dir missing: $DIST_DIR" >&2
  exit 1
fi

map_os_arch_from_name() {
  # CPA_vX_linux_amd64.tar.gz / CPA_vX_windows_arm64.zip
  local base="$1"
  if [[ "$base" =~ _([^_]+)_([^_.]+) ]]; then
    echo "${BASH_REMATCH[1]} ${BASH_REMATCH[2]}"
  else
    echo ""
  fi
}

inject_tar() {
  local archive="$1" goos="$2" goarch="$3" ext="$4"
  local plugin="$PLUGIN_ROOT/${goos}/${goarch}/grok-manager.${ext}"
  [[ -f "$plugin" ]] || { echo "skip missing plugin $plugin"; return 0; }
  local tmp
  tmp="$(mktemp -d)"
  tar -xzf "$archive" -C "$tmp"
  mkdir -p "$tmp/plugins/${goos}/${goarch}"
  cp -f "$plugin" "$tmp/plugins/${goos}/${goarch}/grok-manager.${ext}"
  # also drop a clearly named copy for operators inspecting the archive
  cp -f "$plugin" "$tmp/plugins/${goos}/${goarch}/grok-manager-${goos}-${goarch}.${ext}"
  local abs
  abs="$(cd "$(dirname "$archive")" && pwd)/$(basename "$archive")"
  rm -f "$abs"
  tar -czf "$abs" -C "$tmp" .
  rm -rf "$tmp"
  echo "injected plugin into $archive"
}

inject_zip() {
  local archive="$1" goos="$2" goarch="$3" ext="$4"
  local plugin="$PLUGIN_ROOT/${goos}/${goarch}/grok-manager.${ext}"
  [[ -f "$plugin" ]] || { echo "skip missing plugin $plugin"; return 0; }
  local tmp
  tmp="$(mktemp -d)"
  unzip -q "$archive" -d "$tmp"
  mkdir -p "$tmp/plugins/${goos}/${goarch}"
  cp -f "$plugin" "$tmp/plugins/${goos}/${goarch}/grok-manager.${ext}"
  cp -f "$plugin" "$tmp/plugins/${goos}/${goarch}/grok-manager-${goos}-${goarch}.${ext}"
  local abs
  abs="$(cd "$(dirname "$archive")" && pwd)/$(basename "$archive")"
  rm -f "$abs"
  (
    cd "$tmp"
    zip -qr "$abs" .
  )
  rm -rf "$tmp"
  echo "injected plugin into $archive"
}

shopt -s nullglob
for archive in "$DIST_DIR"/CPA_*.tar.gz "$DIST_DIR"/CPA_*.zip; do
  base="$(basename "$archive")"
  read -r goos goarch <<<"$(map_os_arch_from_name "$base")"
  if [[ -z "${goos:-}" || -z "${goarch:-}" ]]; then
    echo "cannot parse os/arch from $base"
    continue
  fi
  ext="so"
  if [[ "$goos" == "windows" ]]; then
    ext="dll"
  fi
  if [[ ! -f "$PLUGIN_ROOT/${goos}/${goarch}/grok-manager.${ext}" ]]; then
    echo "no plugin for ${goos}/${goarch}, leave archive unchanged: $base"
    continue
  fi
  if [[ "$archive" == *.tar.gz ]]; then
    inject_tar "$archive" "$goos" "$goarch" "$ext"
  else
    inject_zip "$archive" "$goos" "$goarch" "$ext"
  fi
done

if compgen -G "$DIST_DIR/CPA_*" > /dev/null; then
  (
    cd "$DIST_DIR"
    rm -f checksums.txt
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum CPA_*.tar.gz CPA_*.zip 2>/dev/null > checksums.txt || true
    fi
  )
  echo "updated checksums.txt"
fi