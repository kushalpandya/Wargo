#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Builds the Wargo demo site: the web UI of a Wargo checkout plus the demo
# sources in this branch (an in-browser fake backend), served under /Wargo/.
#
#   sh build.sh <wargo-checkout> <output-dir>
#
# Used by .github/workflows/pages.yml on main. The Wargo checkout is
# modified in place (demo sources copied in, index.html pointed at the demo
# entry), so pass a throwaway checkout.
set -eu

APP="$(cd "$1" && pwd)"
mkdir -p "$2"
OUT="$(cd "$2" && pwd)"
DEMO="$(cd "$(dirname "$0")" && pwd)"
BASE="${WARGO_DEMO_BASE:-/Wargo/}"
WEB="$APP/web"

echo "==> Installing the web UI's dependencies"
(cd "$WEB" && npm ci --no-audit --no-fund)
# MSW (the request interceptor) is only needed by the demo; install it
# without touching Wargo's package.json or lock file.
MSW="$(sed -n 's/.*"msw": *"\([^"]*\)".*/\1/p' "$DEMO/package.json")"
(cd "$WEB" && npm install --no-save --no-audit --no-fund "msw@$MSW")

echo "==> Adding the demo sources"
rm -rf "$WEB/src/demo"
cp -R "$DEMO/src" "$WEB/src/demo"
cp "$DEMO/vite.demo.config.ts" "$WEB/vite.demo.config.ts"
sed -i 's#src="/src/main.tsx"#src="/src/demo/entry.ts"#' "$WEB/index.html"
grep -q 'src="/src/demo/entry.ts"' "$WEB/index.html" || { echo "index.html entry not found" >&2; exit 1; }
(cd "$WEB" && npx msw init public --no-save)

echo "==> Type checking"
(cd "$WEB" && npx tsc -b)

echo "==> Building for $BASE"
(cd "$WEB" && WARGO_DEMO_BASE="$BASE" WARGO_REF="${WARGO_REF:-}" npx vite build --config vite.demo.config.ts --outDir "$OUT" --emptyOutDir)

# GitHub Pages serves 404.html for unknown paths: the same page lets deep
# links such as /Wargo/system/events open the app at that route.
cp "$OUT/index.html" "$OUT/404.html"
# The app manifest assumes it lives at /.
sed -i "s#\"start_url\": \"/\"#\"start_url\": \"$BASE\"#; s#\"src\": \"/favicon.svg\"#\"src\": \"${BASE}favicon.svg\"#" "$OUT/manifest.webmanifest"
touch "$OUT/.nojekyll"
echo "==> Demo site in $OUT"
