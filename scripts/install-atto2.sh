#!/bin/sh
# install-atto2.sh builds this tree as atto2: an atto kept apart from the
# installed one, with its own state (~/.atto2: settings, sessions, daemon)
# and binary (~/.atto2/bin/atto). The atto2 command is a wrapper that sets
# ATTO_DIR; the binary's directory goes first on the agent's PATH, so the
# "atto ..." commands the model runs reach this build too.
#
#   scripts/install-atto2.sh [wrapper dir]   (default: $(go env GOPATH)/bin)
#
# A first install copies settings.json (with update checks off and
# /remote on port 7880), models.json and extensions from ~/.atto, but not
# auth.json: log in with "atto2 login".
set -eu
cd "$(dirname "$0")/.."
dir="$HOME/.atto2"
bindir="${1:-$(go env GOPATH)/bin}"

mkdir -p "$dir/bin"
chmod 700 "$dir"
go build -o "$dir/bin/atto.new" ./cmd/atto
mv "$dir/bin/atto.new" "$dir/bin/atto"

if [ ! -f "$dir/settings.json" ] && [ -f "$HOME/.atto/settings.json" ]; then
	if command -v jq >/dev/null; then
		jq '. + {updateCheck: false, remote: ((.remote // {}) + {port: 7880})}' "$HOME/.atto/settings.json" >"$dir/settings.json"
	else
		echo '{"updateCheck": false, "remote": {"port": 7880}}' >"$dir/settings.json"
	fi
	[ -f "$HOME/.atto/models.json" ] && cp "$HOME/.atto/models.json" "$dir/"
	[ -d "$HOME/.atto/extensions" ] && cp -R "$HOME/.atto/extensions" "$dir/"
fi

mkdir -p "$bindir"
cat >"$bindir/atto2" <<'EOF'
#!/bin/sh
# atto2: atto kept apart from atto: its own ~/.atto2 (settings, sessions,
# daemon) and binary (~/.atto2/bin/atto). See docs/atto2.md.
export ATTO_DIR="$HOME/.atto2"
exec "$ATTO_DIR/bin/atto" "$@"
EOF
chmod 755 "$bindir/atto2"
"$bindir/atto2" --version
