#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Wargo installer for macOS (a per-user LaunchAgent). Build first, then run:
#
#   make install-macos        (or: make web build && sh deploy/launchd/install.sh)
#
# Run it as the admin user who will sign in to Wargo, not as root; it asks for
# your password with sudo where it needs to. Re-running upgrades in place and
# keeps your configuration.
#
# Options:
#   --port N       listen port, default 2207
#   --no-start     install but don't start the agent
#   --dry-run      show what would be done without changing anything
#   --uninstall    remove Wargo (keeps its config, data and logs)
#   --purge        with --uninstall, also delete config, data and logs
set -eu

LABEL=io.github.kushalpandya.wargo
PORT=2207
PORT_SET=0
START=1
DRY=0
ACTION=install
PURGE=0

BIN_DIR=/usr/local/bin
BIN="$BIN_DIR/wargo"
PAM_FILE=/etc/pam.d/wargo

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

if [ -t 1 ]; then B="$(printf '\033[1m')"; D="$(printf '\033[2m')"; R="$(printf '\033[31m')"; G="$(printf '\033[32m')"; N="$(printf '\033[0m')"; else B= D= R= G= N=; fi
say() { printf '%s==>%s %s\n' "$B" "$N" "$*"; }
note() { printf '    %s%s%s\n' "$D" "$*" "$N"; }
die() { printf '%serror:%s %s\n' "$R" "$N" "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT="${2:?}"; PORT_SET=1; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    --no-start) START=0; shift ;;
    --uninstall) ACTION=uninstall; shift ;;
    --purge) PURGE=1; shift ;;
    -h|--help) sed -n '3,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
case "$PORT" in ''|*[!0-9]*|??????*) die "invalid port: $PORT (use 1-65535)" ;; esac
{ [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ]; } || die "invalid port: $PORT (use 1-65535)"
[ "$PURGE" -eq 0 ] || [ "$ACTION" = uninstall ] || die "--purge only works together with --uninstall"

# --- preflight ---------------------------------------------------------------
[ "$(uname -s)" = Darwin ] || die "this installer is for macOS; on Linux use install.sh"
[ "$(id -u)" -ne 0 ] || die "run this as your normal admin user, not root (it uses sudo where needed)"
id -Gn | tr ' ' '\n' | grep -qx admin || die "$(id -un) must be an administrator (the admin group) to use Wargo"

USER_NAME="$(id -un)"
UID_NUM="$(id -u)"
HOME_DIR="$HOME"
# These values end up in a plist, a TOML file and sed; refuse anything odd.
case "$USER_NAME" in ""|*[!A-Za-z0-9_.-]*) die "unsupported characters in the user name: $USER_NAME" ;; esac
case "$HOME_DIR" in /*) ;; *) die "HOME must be an absolute path: $HOME_DIR" ;; esac
case "$HOME_DIR" in *[!A-Za-z0-9\ _./-]*) die "unsupported characters in HOME (letters, digits, space and _ . / - only)" ;; esac
SUPPORT_DIR="$HOME_DIR/Library/Application Support/wargo"
CONFIG="$SUPPORT_DIR/config.toml"
LOG_DIR="$HOME_DIR/Library/Logs/wargo"
PLIST="$HOME_DIR/Library/LaunchAgents/$LABEL.plist"
TEMPLATE="$ROOT/deploy/launchd/$LABEL.plist"
DOMAIN="gui/$UID_NUM"

# run: execute, or only print under --dry-run.
run() {
  if [ "$DRY" -eq 1 ]; then printf '    %swould run:%s %s\n' "$D" "$N" "$*"; return 0; fi
  "$@"
}
as_root() { run sudo "$@"; }

# --- uninstall ---------------------------------------------------------------
if [ "$ACTION" = uninstall ]; then
  say "Removing Wargo"
  run launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  run rm -f "$PLIST"
  as_root rm -f "$BIN"
  if [ ! -e "$PAM_FILE" ]; then
    :
  elif cmp -s "$ROOT/deploy/pam/wargo-macos" "$PAM_FILE"; then
    as_root rm -f "$PAM_FILE"
  else
    note "left $PAM_FILE alone because it differs from Wargo's file; remove it yourself if unused"
  fi
  if [ "$PURGE" -eq 1 ]; then
    run rm -rf "$SUPPORT_DIR" "$LOG_DIR"
    note "Deleted configuration, data and logs."
  else
    note "Kept $SUPPORT_DIR and $LOG_DIR (use --uninstall --purge to delete them)."
  fi
  [ "$DRY" -eq 1 ] || printf '%sWargo removed.%s\n' "$G" "$N"
  exit 0
fi

SRC_BIN="$ROOT/bin/wargo"
if [ ! -x "$SRC_BIN" ]; then
  if [ "$DRY" -eq 1 ]; then note "bin/wargo is missing; a real run needs it (make web build)"; else die "bin/wargo not found; run: make web build"; fi
fi
[ -f "$TEMPLATE" ] || die "missing $TEMPLATE"

# --- install -----------------------------------------------------------------
UPGRADE=0
[ -e "$PLIST" ] && UPGRADE=1
if [ "$UPGRADE" -eq 1 ]; then say "Upgrading Wargo"; else say "Installing Wargo"; fi
note "you may be asked for your password (sudo)"

if [ -e "$CONFIG" ] && [ "$PORT_SET" -eq 1 ]; then
  die "$CONFIG already exists; --port would not change it. Edit its listen value instead."
fi

# The port to probe is whatever an existing config says. Read it before any
# sudo step; if it can't be read, fall back to the default with a note.
HEALTH_ADDR="127.0.0.1:$PORT"
if [ -f "$CONFIG" ]; then
  LISTEN="$(sed -n -e 's/^listen *= *"\(.*\)" *$/\1/p' -e "s/^listen *= *'\\(.*\\)' *\$/\\1/p" "$CONFIG" | head -n 1)"
  LPORT="${LISTEN##*:}"
  LHOST="${LISTEN%:*}"
  case "$LPORT" in
    ''|*[!0-9]*|??????*) note "could not read the port from listen in $CONFIG; checking $HEALTH_ADDR instead" ;;
    *)
      case "$LHOST" in ""|0.0.0.0|"[::]") LHOST=127.0.0.1 ;; esac
      PORT="$LPORT"
      HEALTH_ADDR="$LHOST:$LPORT"
      ;;
  esac
fi

[ -d "$BIN_DIR" ] || as_root install -d -m 0755 "$BIN_DIR"
as_root install -m 0755 "$SRC_BIN" "$BIN"
if [ ! -e "$PAM_FILE" ]; then
  as_root install -m 0644 "$ROOT/deploy/pam/wargo-macos" "$PAM_FILE"
  [ "$DRY" -eq 1 ] || note "installed $PAM_FILE (password checks against your Mac account)"
fi

# Escapers: TOML basic strings, XML text, and sed replacements.
toml_esc() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
xml_esc() { printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }
sed_esc() { printf '%s' "$1" | sed -e 's/[\\|&]/\\&/g'; }

if [ ! -e "$CONFIG" ]; then
  if [ "$DRY" -eq 1 ]; then
    printf '    %swould write:%s %s (mode 0600, listen 127.0.0.1:%s)\n' "$D" "$N" "$CONFIG" "$PORT"
  else
    mkdir -p "$SUPPORT_DIR"
    umask 077
    cat > "$CONFIG" <<CFG
# Wargo configuration. Every option is documented in config.example.toml:
# https://github.com/kushalpandya/wargo/blob/main/config.example.toml
listen = "127.0.0.1:$PORT"
data_dir = "$(toml_esc "$SUPPORT_DIR/data")"

[auth]
user = "$(toml_esc "$USER_NAME")"
# Public URL(s) you reach Wargo from, needed for passkeys and WebSockets
# behind a reverse proxy, e.g. ["https://wargo.example.com"].
origins = []
trusted_proxies = ["127.0.0.1/32", "::1/128"]

[[files.roots]]
name = "Home"
path = "$(toml_esc "$HOME_DIR")"
CFG
    chmod 0600 "$CONFIG"
    note "wrote $CONFIG"
  fi
fi

run mkdir -p "$LOG_DIR" "$HOME_DIR/Library/LaunchAgents"
if [ "$DRY" -eq 1 ]; then
  printf '    %swould write:%s %s (from %s)\n' "$D" "$N" "$PLIST" "$TEMPLATE"
else
  sed -e "s|@BIN@|$(sed_esc "$(xml_esc "$BIN")")|g" \
      -e "s|@CONFIG@|$(sed_esc "$(xml_esc "$CONFIG")")|g" \
      -e "s|@LOGDIR@|$(sed_esc "$(xml_esc "$LOG_DIR")")|g" "$TEMPLATE" > "$PLIST"
  chmod 0644 "$PLIST"
fi

if [ "$START" -eq 1 ]; then
  # Always bootout then bootstrap: it also loads a changed plist, and works
  # whether or not the agent is currently loaded.
  run launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  run launchctl bootstrap "$DOMAIN" "$PLIST"
  if [ "$DRY" -eq 0 ]; then
    i=0
    until "$BIN" health -addr "$HEALTH_ADDR" >/dev/null 2>&1; do
      i=$((i + 1))
      [ "$i" -lt 20 ] || die "Wargo didn't start; see the log: $LOG_DIR/wargo.log"
      sleep 0.5
    done
  fi
fi

if [ "$DRY" -eq 1 ]; then
  printf '\n%sDry run finished; nothing was changed.%s\n' "$G" "$N"
  exit 0
fi

printf '\n%sWargo is %s.%s\n\n' "$G$B" "$([ "$UPGRADE" -eq 1 ] && echo upgraded || echo installed)" "$N"
cat <<EOF
  Open      http://127.0.0.1:$PORT   (on this Mac)
  Sign in   as $USER_NAME with your Mac password, then set up two-factor.

  Wargo runs while you are logged in. Manage it with:
    launchctl print $DOMAIN/$LABEL | grep state
    launchctl kickstart -k $DOMAIN/$LABEL      (restart)
  Logs:     $LOG_DIR/wargo.log

  Optional: Full Disk Access. Without it Wargo can't open Desktop, Documents,
  Downloads, iCloud Drive or external drives. Your organisation's device
  management may not allow it. To grant it:
    1. open "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"
    2. System Settings > Privacy & Security > Full Disk Access > add $BIN
    3. restart Wargo: launchctl kickstart -k $DOMAIN/$LABEL
  Reinstalling or upgrading replaces the binary, so grant access again after.
EOF
