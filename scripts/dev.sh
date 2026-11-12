#!/bin/sh
# Runs the Wargo backend and the Vite UI together for development.
#   scripts/dev.sh            UI on http://127.0.0.1:5173, this machine only
#   scripts/dev.sh --lan      UI also on this machine's LAN addresses
# (or: make dev, make dev ARGS=--lan). The UI has live reload; Ctrl+C stops
# both servers.
#
# The backend always listens on loopback; Vite proxies the API to it. With
# --lan the proxy marks requests as forwarded, so the backend refuses the
# sign-in bypass for all of them and everyone (you included) signs in with
# a real account password.
#
# Settings come from dev.local.toml (git-ignored), which is created on the
# first run with sign-in bypassed on 127.0.0.1:2207. Environment overrides:
#   WARGO_DEV_CONFIG   config file to use (default dev.local.toml)
#   GO                 go command (default: go, or mise with the go.mod toolchain)
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

die() { echo "dev: $*" >&2; exit 1; }

LAN=
case "${1:-}" in
	"") ;;
	--lan) LAN=1 ;;
	-h | --help) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown option: $1 (try --help)" ;;
esac

# Go: plain `go` when it works, else the toolchain from go.mod through mise.
if [ -z "${GO:-}" ]; then
	if go version >/dev/null 2>&1; then
		GO=go
	elif command -v mise >/dev/null 2>&1; then
		v="$(sed -n 's/^toolchain go//p' go.mod)"
		[ -n "$v" ] || v="$(sed -n 's/^go //p' go.mod)"
		GO="mise exec go@$v -- go"
	else
		die "Go not found; install Go $(sed -n 's/^go //p' go.mod) or newer"
	fi
fi
command -v npm >/dev/null 2>&1 || die "npm not found; install Node.js 22 or newer"

CFG="${WARGO_DEV_CONFIG:-dev.local.toml}"
if [ ! -f "$CFG" ]; then
	cat >"$CFG" <<'EOF'
# Local dev config (git-ignored). Every option is in config.example.toml.
listen = "127.0.0.1:2207"
data_dir = "./data"

[auth]
# Vite's proxy on this machine forwards the visitor's address.
trusted_proxies = ["127.0.0.1/32", "::1/128"]

# Skips sign-in; only honoured on loopback, and never for --lan visitors.
[dev]
auth_bypass = true
EOF
	echo "dev: created $CFG (sign-in bypassed, port 2207)"
fi

listen="$(sed -n 's/^listen[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' "$CFG" | head -n 1)"
listen="${listen:-127.0.0.1:2207}"
case "$listen" in :*) listen="127.0.0.1$listen" ;; esac
API="http://$listen"
case "$listen" in
	127.* | localhost:* | "[::1]:"*) ;;
	*) die "$CFG listens on $listen; keep the backend on loopback (127.0.0.1) and use --lan for LAN access" ;;
esac

if [ -n "$LAN" ]; then
	HOST=0.0.0.0
	# Sign-in on macOS needs the PAM service file the installer adds.
	if [ "$(uname -s)" = Darwin ] && [ ! -f /etc/pam.d/wargo ]; then
		echo "dev: /etc/pam.d/wargo is missing, so sign-in will fail. Add it with:" >&2
		echo "dev:   sudo install -m 0644 deploy/pam/wargo-macos /etc/pam.d/wargo" >&2
	fi
else
	HOST=127.0.0.1
fi

if [ ! -d web/node_modules ]; then
	echo "dev: installing web dependencies"
	(cd web && npm ci --no-audit --no-fund)
fi

# Colour the prefixes only on a terminal.
if [ -t 1 ]; then c_api="$(printf '\033[36m')" c_web="$(printf '\033[35m')" c_off="$(printf '\033[0m')"; else c_api= c_web= c_off=; fi
prefix() { awk -v p="$1" '{ print p $0; fflush() }'; }

# Each server runs in its own process group, so stopping it stops the whole
# pipeline (go run and the binary it builds, npm and vite). Stdin is
# /dev/null so neither waits on the terminal.
set -m
($GO run ./cmd/wargo -config "$CFG" </dev/null 2>&1 | prefix "${c_api}[api]${c_off} ") &
api=$!
(cd web && WARGO_API="$API" WARGO_DEV_LAN="$LAN" npm run dev -- --host "$HOST" --strictPort </dev/null 2>&1 | prefix "${c_web}[web]${c_off} ") &
web=$!
set +m

stopping=
stop() {
	[ -n "$stopping" ] && return
	stopping=1
	echo
	echo "dev: stopping"
	kill -TERM -"$api" -"$web" 2>/dev/null || true
	i=0
	while { kill -0 "$api" || kill -0 "$web"; } 2>/dev/null && [ "$i" -lt 50 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	kill -KILL -"$api" -"$web" 2>/dev/null || true
	wait 2>/dev/null || true
}
trap 'stop; exit 130' INT
trap 'stop; exit 143' TERM
trap stop EXIT

if [ -n "$LAN" ]; then
	echo "dev: UI on port 5173 of this machine's LAN addresses (listed by vite below), sign-in required. Ctrl+C to stop."
else
	echo "dev: UI on http://127.0.0.1:5173 (this machine only, sign-in bypassed). Ctrl+C to stop."
fi

# If either server exits on its own, stop the other and exit.
while kill -0 "$api" 2>/dev/null && kill -0 "$web" 2>/dev/null; do
	sleep 1
done
kill -0 "$api" 2>/dev/null && die_name=web || die_name=api
echo "dev: the $die_name server exited" >&2
exit 1
