#!/usr/bin/env bash
# Cross-compile sq-benchmark to single static binaries for every platform.
# Run:  ./build-release.sh v1.0.0
set -euo pipefail

VERSION="${1:-dev}"
OUT="dist/${VERSION}"
mkdir -p "$OUT"

PLATFORMS=("darwin/arm64" "darwin/amd64" "linux/arm64" "linux/amd64" "windows/amd64" "windows/arm64")

for p in "${PLATFORMS[@]}"; do
  os="${p%/*}"; arch="${p#*/}"
  bin="sq-benchmark"; [ "$os" = "windows" ] && bin="sq-benchmark.exe"
  dir="$OUT/sq-benchmark_${VERSION}_${os}_${arch}"
  mkdir -p "$dir"
  echo "building ${os}/${arch} ..."
  # CGO_ENABLED=0 -> fully static; -trimpath/-s/-w -> reproducible + smaller
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$dir/$bin" .
  cp README.md bench.example.yaml "$dir/" 2>/dev/null || true
  # double-click launchers (sample seeds are embedded in the binary)
  if [ "$os" = "windows" ]; then
    cp run.bat "$dir/" 2>/dev/null || true
  else
    cp run.command "$dir/" 2>/dev/null || true
    chmod +x "$dir/run.command" 2>/dev/null || true
  fi
  ( cd "$OUT" && zip -qr "$(basename "$dir").zip" "$(basename "$dir")" )
done

echo "done -> $OUT"
ls -1 "$OUT"/*.zip
