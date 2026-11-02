#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Packages install the binary to /usr/bin; point the unit there.
if [ -f /usr/lib/systemd/system/wargo@.service ]; then
  sed -i 's#/usr/local/bin/wargo#/usr/bin/wargo#' /usr/lib/systemd/system/wargo@.service
fi
install -d -m 0755 /var/lib/wargo
command -v systemctl >/dev/null && systemctl daemon-reload || true
echo "Wargo installed. Enable it for your admin user:"
echo "  sudo chown \$USER: /var/lib/wargo && sudo chmod 700 /var/lib/wargo"
echo "  sudo systemctl enable --now wargo@\$USER"
