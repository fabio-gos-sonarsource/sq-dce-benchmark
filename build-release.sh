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
  # Always start from an empty folder. The tool writes its outputs next to the binary, so
  # running a benchmark inside dist/ leaves a bench.yaml (with real tokens), results.json
  # and a PDF behind — mkdir -p would keep them and the next build would ship them.
  rm -rf "$dir"
  mkdir -p "$dir"
  echo "building ${os}/${arch} ..."
  # CGO_ENABLED=0 -> fully static; -trimpath/-s/-w -> reproducible + smaller
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$dir/$bin" .
  cp README.md bench.example.yaml "$dir/"
  # Double-click launcher, per platform (the sample seeds are embedded in the binary).
  # Linux gets none: .command is a macOS Finder convention and .bat is Windows-only.
  case "$os" in
    darwin)  cp packaging/run.command "$dir/"; chmod +x "$dir/run.command" ;;
    windows) cp packaging/run.bat "$dir/" ;;
  esac
  # zip updates an existing archive rather than replacing it, so drop it first.
  ( cd "$OUT" && rm -f "$(basename "$dir").zip" \
      && zip -qr "$(basename "$dir").zip" "$(basename "$dir")" -x '*.DS_Store' )
done

echo "done -> $OUT"
ls -1 "$OUT"/*.zip
