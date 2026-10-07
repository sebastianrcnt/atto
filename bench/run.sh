#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
bin=$(mktemp "${TMPDIR:-/tmp}/atto2-bench.XXXXXX")
trap 'rm -f "$bin"' EXIT
go build -o "$bin" ./cmd/atto2
python3 bench/run.py "$bin" "$@"
