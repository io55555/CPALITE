#!/usr/bin/env bash
set -euo pipefail

VERSION="${VERSION:-1.3.7}"
# GOARCH: amd64 (default) or arm64
ARCH="${GOARCH:-$(go env GOARCH 2>/dev/null || echo amd64)}"
mkdir -p dist
export CGO_ENABLED=1
export GOOS="${GOOS:-linux}"
export GOARCH="$ARCH"

# Optional cross-compile: ARCH=arm64 CC=aarch64-linux-gnu-gcc ./build.sh
OUT="dist/grok-manager-linux-${ARCH}.so"

go build \
  -buildvcs=false \
  -buildmode=c-shared \
  -trimpath \
  -ldflags="-s -w" \
  -o "$OUT" \
  ./plugin

if [[ "$ARCH" == "amd64" ]]; then
  cp -f "$OUT" dist/grok-manager.so
  cp -f "$OUT" "dist/grok-manager-v${VERSION}.so"
else
  cp -f "$OUT" "dist/grok-manager-v${VERSION}-linux-${ARCH}.so"
fi

echo "Built $OUT (v${VERSION} ${GOOS}/${ARCH})"
ls -lh dist/grok-manager*.so
file "$OUT" || true
