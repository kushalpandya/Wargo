#!/bin/sh
# Simulated `brew upgrade` for WARGO_BREW_FAKE (development only). It prints
# output shaped like Homebrew's and asks SUDO_ASKPASS for the password the
# way sudo would, without ever running sudo.
s() { sleep "${WARGO_BREW_FAKE_DELAY:-0.4}"; }
echo "==> Upgrading 4 outdated packages:"
echo "git 2.51.0 -> 2.52.1"
echo "node 24.9.0 -> 24.11.1"
echo "openssl@3 3.5.3 -> 3.6.0"
echo "ripgrep 14.1.1 -> 15.1.0"
echo "Warning: Not upgrading 1 pinned package:"
echo "python@3.13 3.13.7"
for p in git node openssl@3 ripgrep; do
	echo "==> Fetching $p"; s
	echo "==> Downloading https://ghcr.io/v2/homebrew/core/$p/blobs/sha256:0123456789abcdef"; s
done
for p in openssl@3 git node ripgrep; do
	echo "==> Upgrading $p"; s
	echo "==> Pouring $p--1.0.arm64_tahoe.bottle.tar.gz"; s
	echo "==> Summary"
	echo "/opt/homebrew/Cellar/$p/1.0: 1,024 files, 32.1MB"
	echo "==> Running \`brew cleanup $p\`..."
	echo "Removing: /opt/homebrew/Cellar/$p/0.9... (1,000 files, 31MB)"; s
done
echo "==> Upgrading 2 outdated casks:"
echo "iterm2 3.7.1 -> 3.7.7"
echo "nordvpn 8.38.1 -> 10.15.1"
echo "==> Upgrading iterm2"; s
echo "==> Downloading https://iterm2.com/downloads/stable/iTerm2-3_7_7.zip"; s
echo "==> Moving App 'iTerm.app' to '/Applications/iTerm.app'"
echo "iterm2 was successfully upgraded!"
echo "==> Upgrading nordvpn"; s
echo "==> Downloading https://downloads.nordcdn.com/apps/macos/generic/NordVPN-OpenVPN/latest/NordVPN.pkg"; s
echo "==> Running installer for nordvpn with sudo; the password may be necessary."
if [ -z "${SUDO_ASKPASS:-}" ]; then
	echo "sudo: a terminal is required to read the password" >&2
	exit 1
fi
pw="$("$SUDO_ASKPASS" "Password:")" || { echo "sudo: no password was provided" >&2; exit 1; }
[ -n "$pw" ] || { echo "sudo: no password was provided" >&2; exit 1; }
echo "installer: Package name is NordVPN"; s
echo "installer: Upgrading at base path /"; s
echo "installer: The upgrade was successful."
echo "nordvpn was successfully upgraded!"
echo "==> Checking for dependents of upgraded formulae..."
echo "==> No broken dependents found!"
