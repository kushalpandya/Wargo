#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Packages install the binary to /usr/bin; point the unit there.
for unit in wargo@.service wargo-privd.service; do
  if [ -f "/usr/lib/systemd/system/$unit" ]; then
    sed -i 's#/usr/local/bin/wargo#/usr/bin/wargo#' "/usr/lib/systemd/system/$unit"
  fi
done
install -d -m 0755 /var/lib/wargo
command -v systemctl >/dev/null && systemctl daemon-reload || true
echo "Wargo installed. Enable it for your admin user:"
echo "  (for wargo-privd set wargo_binary = \"/usr/bin/wargo\" in /etc/wargo/privd.toml)"
echo "  sudo chown \$USER: /var/lib/wargo && sudo chmod 700 /var/lib/wargo"
echo "  sudo systemctl enable --now wargo@\$USER"
