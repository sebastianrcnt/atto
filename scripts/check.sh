#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."

case "${1:-}" in
    '') tests=true ;;
    --no-tests) tests=false ;;
    *) printf 'usage: scripts/check.sh [--no-tests]\n' >&2; exit 2 ;;
esac

check() {
    local name=$1
    shift
    printf 'check: %s\n' "$name"
    if ! "$@"; then
        printf 'check: FAILED: %s\n' "$name" >&2
        exit 1
    fi
}

check_format() {
    local file unformatted
    local files=()
    while IFS= read -r -d '' file; do
        files+=("$file")
    done < <(find . -name .git -prune -o -type f -name '*.go' -print0)
    if [ "${#files[@]}" -eq 0 ]; then
        return
    fi
    unformatted=$(gofmt -l "${files[@]}") || return
    if [ -n "$unformatted" ]; then
        printf 'Not gofmt-formatted (run gofmt -w):\n%s\n' "$unformatted" >&2
        return 1
    fi
}

check 'gofmt -l' check_format
check 'go vet ./...' go vet ./...
check 'modernize@v0.51.0' go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@v0.51.0 ./...
check 'go mod tidy -diff' go mod tidy -diff
check 'staticcheck@v0.8.1' go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
if "$tests"; then
    check 'go test -race -count=1 ./...' go test -race -count=1 ./...
fi
