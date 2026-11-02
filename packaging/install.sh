#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Installs a built Wargo onto this machine. Run from the repository root:
#   sudo ./packaging/install.sh [user]
set -eu

[ "$(id -u)" -eq 0 ] || { echo "run with sudo" >&2; exit 1; }
TARGET_USER="${1:-${SUDO_USER:-}}"
[ -n "$TARGET_USER" ] || { echo "usage: sudo $0 <user>" >&2; exit 1; }
id "$TARGET_USER" >/dev/null 2>&1 || { echo "no such user: $TARGET_USER" >&2; exit 1; }
TARGET_GROUP="$(id -gn "$TARGET_USER")"
BIN="${BIN:-bin/wargo}"
[ -x "$BIN" ] || { echo "missing $BIN — run 'make' first" >&2; exit 1; }

echo "Installing Wargo for user $TARGET_USER"
install -m 0755 "$BIN" /usr/local/bin/wargo
[ -x bin/wargo-privd ] && install -m 0755 bin/wargo-privd /usr/local/bin/wargo-privd

if [ -e /etc/pam.d/wargo ]; then
  echo "  /etc/pam.d/wargo exists, leaving it"
else
  install -m 0644 deploy/pam/wargo /etc/pam.d/wargo
fi

install -m 0644 deploy/systemd/wargo@.service /etc/systemd/system/wargo@.service
[ -f deploy/systemd/wargo-privd.service ] && install -m 0644 deploy/systemd/wargo-privd.service /etc/systemd/system/
[ -f deploy/systemd/wargo-privd.socket ] && install -m 0644 deploy/systemd/wargo-privd.socket /etc/systemd/system/

install -d -m 0755 /etc/wargo
if [ ! -e /etc/wargo/config.toml ]; then
  install -m 0640 -g "$TARGET_GROUP" config.example.toml /etc/wargo/config.toml
  echo "  wrote /etc/wargo/config.toml — review roots and origins"
fi
install -d -m 0700 -o "$TARGET_USER" -g "$TARGET_GROUP" /var/lib/wargo

systemctl daemon-reload
echo
echo "Done. Start it with:  systemctl enable --now wargo@$TARGET_USER"
echo "Then open http://127.0.0.1:2207 and sign in as $TARGET_USER."
