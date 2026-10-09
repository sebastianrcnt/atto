#!/bin/sh
# wintest.sh [HOST] [go test args...] runs vet and the tests of this checkout's
# HEAD on a Windows ssh HOST (default win), like the nightly CI's Windows job
# but without waiting for it. Uncommitted changes are not included. Each run
# uses a fresh directory under %TEMP%\atto-wintest; the host's own atto install
# and ~\.atto are not touched (tests use temporary homes). WINTEST_SHELL, if
# set, is the path of the shell atto uses there (ATTO_SHELL), for a host whose
# default shell is unusable.
set -eu
host=${1:-win}
[ $# -gt 0 ] && shift
cd "$(dirname "$0")/.."
sha=$(git rev-parse --short=12 HEAD)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git archive --format=tar -o "$tmp/src.tar" HEAD

dir="atto-wintest\\$sha"
ssh "$host" "(if exist %TEMP%\\$dir rmdir /s /q %TEMP%\\$dir) & mkdir %TEMP%\\$dir" >/dev/null
scp -q "$tmp/src.tar" "$host:AppData/Local/Temp/atto-wintest/$sha/src.tar"
args=${*:-./...}
shellenv=
[ -n "${WINTEST_SHELL:-}" ] && shellenv="set ATTO_SHELL=$WINTEST_SHELL& "
# cmd: && stops on the first failing step; ATTO_* from the ssh session must not
# leak into tests.
status=0
ssh "$host" "cd /d %TEMP%\\$dir && tar -xf src.tar && del src.tar && set ATTO_SESSION_ID=& set ATTO_AGENT=& set ATTO_TOOL_CALL_ID=& set GOFLAGS=-count=1& ${shellenv}go vet ./... && go vet -tags noext ./... && go test $args && go test -tags noext $args" || status=$?
ssh "$host" "cd /d %TEMP% && rmdir /s /q $dir" >/dev/null 2>&1 || true
exit $status
