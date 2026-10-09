#!/bin/sh
# deploy.sh [--slim] HOST... builds atto from this checkout for each ssh HOST and
# installs it there, without GitHub: for development machines. Windows
# hosts get %LOCALAPPDATA%\Programs\atto\atto.exe, others ~/.local/bin/atto
# (the install scripts' defaults). The build is an edge build, so
# "atto update" on the host keeps following the edge channel and variant.
# --slim or ATTO_VARIANT=slim omits the extension engine.
set -eu
variant=${ATTO_VARIANT:-full}
if [ "${1:-}" = --slim ]; then variant=slim; shift; fi
case $variant in full) tags= ;; slim) tags=noext ;; *) echo "unknown ATTO_VARIANT '$variant'; use slim or full" >&2; exit 2 ;; esac
[ $# -gt 0 ] || { echo "usage: scripts/deploy.sh [--slim] HOST..." >&2; exit 2; }
cd "$(dirname "$0")/.."

# The version CI would give this commit (see .github/workflows/ci.yml),
# marked as a local build.
desc=$(git describe --tags --long --abbrev=7 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude 'v*-*' 2>/dev/null ||
	echo "v0.0.0-$(git rev-list --count HEAD)-g$(git rev-parse --short=7 HEAD)")
sha=${desc##*-g}; rest=${desc%-g*}; n=${rest##*-}; tag=${rest%-*}
IFS=. read -r major minor patch <<EOV
${tag#v}
EOV
version="v$major.$minor.$((patch + 1))-dev.$n+$sha.local"
git diff --quiet HEAD || version="$version.dirty"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

build() { # os arch -> path
	out="$tmp/atto_$1_$2"
	[ "$1" = windows ] && out="$out.exe"
	CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -tags "$tags" \
		-ldflags "-s -w -X github.com/sebastianrcnt/atto/update.Version=$version -X github.com/sebastianrcnt/atto/update.Channel=edge" \
		-o "$out" ./cmd/atto
	echo "$out"
}

arch_of() {
	case $1 in
	AMD64 | x86_64) echo amd64 ;;
	ARM64 | aarch64 | arm64) echo arm64 ;;
	*) echo "unknown architecture: $1" >&2; return 1 ;;
	esac
}

for host; do
	if ssh "$host" ver 2>/dev/null | grep -q Windows; then
		arch=$(arch_of "$(ssh "$host" 'echo %PROCESSOR_ARCHITECTURE%' | tr -d '\r ')")
		bin=$(build windows "$arch")
		ssh "$host" 'if not exist "%LOCALAPPDATA%\Programs\atto" mkdir "%LOCALAPPDATA%\Programs\atto"'
		scp -q "$bin" "$host:AppData/Local/Programs/atto/atto.exe.new"
		# A running atto.exe can be renamed but not overwritten, and an
		# atto.exe.old may still be running too: move it aside under a new
		# name then (atto removes those at a later start).
		cat >"$tmp/swap.ps1" <<'PS'
$ErrorActionPreference = 'Stop'
$d = Join-Path $env:LOCALAPPDATA 'Programs\atto'; $exe = Join-Path $d 'atto.exe'
if (Test-Path $exe) {
	$old = "$exe.old"
	Remove-Item $old -ErrorAction SilentlyContinue
	if (Test-Path $old) { $old = "$exe.old-$([DateTime]::Now.Ticks)" }
	Move-Item $exe $old
}
Move-Item "$exe.new" $exe
& $exe -version
PS
		scp -q "$tmp/swap.ps1" "$host:AppData/Local/Temp/atto-swap.ps1"
		printf '%s: ' "$host"
		ssh "$host" 'powershell -NoProfile -ExecutionPolicy Bypass -File "%TEMP%\atto-swap.ps1" & del "%TEMP%\atto-swap.ps1"' | tr -d '\r'
	else
		read -r kernel machine <<EOU
$(ssh "$host" uname -sm)
EOU
		os=$(echo "$kernel" | tr '[:upper:]' '[:lower:]')
		bin=$(build "$os" "$(arch_of "$machine")")
		ssh "$host" 'mkdir -p ~/.local/bin'
		scp -q "$bin" "$host:.local/bin/atto.new"
		printf '%s: ' "$host"
		ssh "$host" 'chmod +x ~/.local/bin/atto.new && mv -f ~/.local/bin/atto.new ~/.local/bin/atto && ~/.local/bin/atto -version'
	fi
done
