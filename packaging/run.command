#!/usr/bin/env bash
# Double-click this on macOS to run the benchmark using the bench.yaml in this folder.
cd "$(dirname "$0")" || exit 1
./sq-benchmark run
echo
read -r -p "Done. Press Enter to close this window..."
