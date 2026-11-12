#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Wargo installer — native install on any systemd-based Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/kushalpandya/wargo/main/install.sh | sh
#
# Run it as the user who will sign in to Wargo (an admin in the wheel/sudo
# group); it asks for your sudo password once. Re-running upgrades Wargo
# in place and keeps your configuration.
#
# Options (or the matching environment variables):
#   --version vX.Y.Z   install a specific release   (WARGO_VERSION)
#   --user NAME        account that signs in          (WARGO_USER)
#   --port N           listen port, default 2207       (WARGO_PORT)
#   --archive FILE     install from a downloaded wargo_*.tar.gz (offline)
#   --dry-run          show what would be done without changing anything
#   --no-start         install but don't start the service
#   --uninstall        remove Wargo (keeps /var/lib/wargo and /etc/wargo)
#   --purge            with --uninstall, also delete data and config
set -eu

REPO="kushalpandya/wargo"
VERSION="${WARGO_VERSION:-}"
TARGET_USER="${WARGO_USER:-}"
PORT="${WARGO_PORT:-2207}"
START=1
ARCHIVE=""
DRY=0
ACTION=install
PURGE=0

BIN_DIR=/usr/local/bin
CONF_DIR=/etc/wargo
DATA_DIR=/var/lib/wargo
UNIT=/etc/systemd/system/wargo@.service
PAM_FILE=/etc/pam.d/wargo

if [ -t 1 ]; then B="$(printf '\033[1m')"; D="$(printf '\033[2m')"; R="$(printf '\033[31m')"; G="$(printf '\033[32m')"; N="$(printf '\033[0m')"; else B= D= R= G= N=; fi
say() { printf '%s==>%s %s\n' "$B" "$N" "$*"; }
note() { printf '    %s%s%s\n' "$D" "$*" "$N"; }
die() { printf '%serror:%s %s\n' "$R" "$N" "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?}"; shift 2 ;;
    --user) TARGET_USER="${2:?}"; shift 2 ;;
    --port) PORT="${2:?}"; shift 2 ;;
    --archive) ARCHIVE="${2:?}"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    --no-start) START=0; shift ;;
    --uninstall) ACTION=uninstall; shift ;;
    --purge) PURGE=1; shift ;;
    -h|--help) sed -n '3,20p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

# --- privileges -------------------------------------------------------------
if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
  [ -n "$TARGET_USER" ] || TARGET_USER="${SUDO_USER:-}"
else
  command -v sudo >/dev/null 2>&1 || die "sudo is required (or run as root with --user NAME)"
  SUDO="sudo"
  [ -n "$TARGET_USER" ] || TARGET_USER="$(id -un)"
fi
[ -n "$TARGET_USER" ] || die "could not tell which account signs in; pass --user NAME"
[ "$TARGET_USER" != root ] || die "Wargo signs in with a normal admin account, not root; pass --user NAME"
id "$TARGET_USER" >/dev/null 2>&1 || die "no such user: $TARGET_USER"
TARGET_GROUP="$(id -gn "$TARGET_USER")"
TARGET_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6)"

as_root() {
  if [ "$DRY" -eq 1 ]; then printf '    %swould run:%s %s\n' "$D" "$N" "$*"; return 0; fi
  if [ -n "$SUDO" ]; then $SUDO "$@"; else "$@"; fi
}

# --- uninstall ---------------------------------------------------------------
if [ "$ACTION" = uninstall ]; then
  say "Removing Wargo"
  as_root systemctl disable --now "wargo@$TARGET_USER" 2>/dev/null || true
  as_root systemctl disable --now wargo-privd.socket wargo-privd.service 2>/dev/null || true
  as_root rm -f "$BIN_DIR/wargo" "$BIN_DIR/wargo-privd" "$UNIT" \
    /etc/systemd/system/wargo-privd.service /etc/systemd/system/wargo-privd.socket "$PAM_FILE"
  as_root systemctl daemon-reload
  if [ "$PURGE" -eq 1 ]; then
    as_root rm -rf "$CONF_DIR" "$DATA_DIR" /var/lib/wargo-privd /var/log/wargo
    note "Deleted configuration and data."
  else
    note "Kept $CONF_DIR and $DATA_DIR (use --uninstall --purge to delete them)."
  fi
  printf '%sWargo removed.%s\n' "$G" "$N"
  exit 0
fi

# --- preflight ---------------------------------------------------------------
say "Checking this system"
[ "$(uname -s)" = Linux ] || die "Wargo runs on Linux only"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU architecture $(uname -m); try the Docker install (docs/DOCKER.md)" ;;
esac
[ -d /run/systemd/system ] || die "systemd is required for the native install; use the Docker install instead (docs/DOCKER.md)"
if ! getconf GNU_LIBC_VERSION >/dev/null 2>&1; then
  die "this system doesn't use glibc (e.g. Alpine); use the Docker install instead (docs/DOCKER.md)"
fi
HAVE_PAM=0
for f in /usr/lib*/libpam.so.0 /lib*/libpam.so.0 /usr/lib/*/libpam.so.0 /lib/*/libpam.so.0; do
  [ -e "$f" ] && HAVE_PAM=1 && break
done
[ "$HAVE_PAM" -eq 1 ] || die "PAM (libpam.so.0) was not found; install your distribution's pam package"
for t in curl tar sha256sum; do command -v "$t" >/dev/null 2>&1 || die "'$t' is required"; done
ADMIN_GROUP=""
for g in wheel sudo admin; do
  if id -nG "$TARGET_USER" | tr ' ' '\n' | grep -qx "$g"; then ADMIN_GROUP=$g; break; fi
done
[ -n "$ADMIN_GROUP" ] || die "$TARGET_USER must be in the wheel, sudo or admin group to use Wargo"
note "user $TARGET_USER ($ADMIN_GROUP), $ARCH, $(getconf GNU_LIBC_VERSION)"

# --- download ----------------------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
if [ -n "$ARCHIVE" ]; then
  [ -f "$ARCHIVE" ] || die "no such file: $ARCHIVE"
  ASSET="$(basename "$ARCHIVE")"
  cp "$ARCHIVE" "$TMP/$ASSET"
  VERSION="$(printf '%s' "$ASSET" | sed -n 's/^wargo_\(.*\)_linux_.*\.tar\.gz$/v\1/p')"
  [ -n "$VERSION" ] || VERSION="(local archive)"
  say "Using $ARCHIVE"
elif [ -z "$VERSION" ]; then
  # Newest stable release (pre-releases only with --version).
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || die "could not find the latest release; pass --version vX.Y.Z"
fi
if [ -z "$ARCHIVE" ]; then
  case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
  ASSET="wargo_${VERSION#v}_linux_${ARCH}.tar.gz"
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

if [ -z "$ARCHIVE" ]; then
  say "Downloading Wargo $VERSION"
  curl -fSL --progress-bar -o "$TMP/$ASSET" "$BASE/$ASSET" || die "download failed: $BASE/$ASSET"
  curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" || die "download failed: $BASE/SHA256SUMS"
  (cd "$TMP" && grep " $ASSET\$" SHA256SUMS | sha256sum -c - >/dev/null) || die "checksum mismatch for $ASSET"
  note "checksum verified"
fi
mkdir "$TMP/x"
tar -xzf "$TMP/$ASSET" -C "$TMP/x"
SRC="$TMP/x"
[ -x "$SRC/wargo" ] || SRC="$(dirname "$(find "$TMP/x" -name wargo -type f | head -n1)")"
[ -x "$SRC/wargo" ] || die "the archive doesn't contain the wargo binary"
"$SRC/wargo" version >/dev/null 2>&1 || die "the wargo binary doesn't run on this system (glibc too old?); use the Docker install"

# --- install -----------------------------------------------------------------
UPGRADE=0
[ -x "$BIN_DIR/wargo" ] && UPGRADE=1
if [ "$UPGRADE" -eq 1 ]; then say "Upgrading $("$BIN_DIR/wargo" version 2>/dev/null || echo Wargo) → $VERSION"; else say "Installing"; fi
note "you may be asked for your sudo password"

as_root install -m 0755 "$SRC/wargo" "$BIN_DIR/wargo"
[ -x "$SRC/wargo-privd" ] && as_root install -m 0755 "$SRC/wargo-privd" "$BIN_DIR/wargo-privd"

# sha256 of the previous deploy/pam/wargo (without nodelay). An installed file
# with this hash is our unmodified one and is safe to replace.
OLD_PAM_SHA=220703a0e9c5286c9347d52dbe9f55c7b323a83fbc369c9c712af7be08bf11a3
if [ ! -e "$PAM_FILE" ]; then
  as_root install -m 0644 "$SRC/deploy/pam/wargo" "$PAM_FILE"
  note "installed $PAM_FILE (password checks without account lockout)"
elif [ "$(as_root sha256sum "$PAM_FILE" | cut -d' ' -f1)" = "$OLD_PAM_SHA" ]; then
  as_root install -m 0644 "$SRC/deploy/pam/wargo" "$PAM_FILE"
  note "updated $PAM_FILE (adds nodelay to the pam_unix.so auth line)"
elif ! grep -q 'pam_unix.so.*nodelay' "$PAM_FILE" 2>/dev/null; then
  note "$PAM_FILE was changed by you, so it was left alone; add nodelay to its pam_unix.so auth line yourself"
fi

as_root install -m 0644 "$SRC/deploy/systemd/wargo@.service" "$UNIT"
[ -f "$SRC/deploy/systemd/wargo-privd.service" ] && as_root install -m 0644 "$SRC/deploy/systemd/wargo-privd.service" /etc/systemd/system/wargo-privd.service
[ -f "$SRC/deploy/systemd/wargo-privd.socket" ] && as_root install -m 0644 "$SRC/deploy/systemd/wargo-privd.socket" /etc/systemd/system/wargo-privd.socket

as_root install -d -m 0755 "$CONF_DIR"
if [ ! -e "$CONF_DIR/config.toml" ]; then
  cat > "$TMP/config.toml" <<EOF
# Wargo configuration. Every option is documented in config.example.toml:
# https://github.com/kushalpandya/wargo/blob/main/config.example.toml
listen = "127.0.0.1:$PORT"

[auth]
user = "$TARGET_USER"
# Public URL(s) you reach Wargo from, needed for passkeys and WebSockets
# behind a reverse proxy, e.g. ["https://wargo.example.com"].
origins = []
trusted_proxies = ["127.0.0.1/32", "::1/128"]

[[files.roots]]
name = "Home"
path = "$TARGET_HOME"
EOF
  as_root install -m 0640 -g "$TARGET_GROUP" "$TMP/config.toml" "$CONF_DIR/config.toml"
  note "wrote $CONF_DIR/config.toml"
fi
as_root install -d -m 0700 -o "$TARGET_USER" -g "$TARGET_GROUP" "$DATA_DIR"

as_root systemctl daemon-reload
if [ "$DRY" -eq 1 ]; then
  printf '\n%sDry run finished; nothing was changed.%s\n' "$G" "$N"
  exit 0
fi
if [ "$START" -eq 1 ]; then
  if [ "$UPGRADE" -eq 1 ]; then
    as_root systemctl restart "wargo@$TARGET_USER"
  else
    as_root systemctl enable --now "wargo@$TARGET_USER" >/dev/null 2>&1
  fi
  i=0
  until "$BIN_DIR/wargo" health -addr "127.0.0.1:$PORT" >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -lt 20 ] || { as_root systemctl status --no-pager "wargo@$TARGET_USER" || true; die "Wargo didn't start; see: journalctl -u wargo@$TARGET_USER"; }
    sleep 0.5
  done
fi

printf '\n%sWargo %s is %s.%s\n\n' "$G$B" "$VERSION" "$([ "$UPGRADE" -eq 1 ] && echo upgraded || echo installed)" "$N"
if [ "$UPGRADE" -eq 0 ]; then
  cat <<EOF
  Open      http://127.0.0.1:$PORT   (on this machine)
  Sign in   as $TARGET_USER with your usual password, then set up two-factor.

  To reach it from other devices, put it behind HTTPS (Cloudflare Tunnel,
  Caddy or nginx) and add that URL to auth.origins in $CONF_DIR/config.toml:
  https://github.com/$REPO/blob/main/docs/INSTALL.md#access-from-anywhere

  Manage it with: systemctl status|restart|stop wargo@$TARGET_USER
  Logs:           journalctl -u wargo@$TARGET_USER -f
EOF
fi
