#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs the demo with live reload against a local Wargo checkout:
#
#   sh dev.sh ../Wargo [port]      # then open http://127.0.0.1:<port>/Wargo/
#
# Copies Wargo's web/ (minus node_modules) to a scratch folder, links this
# branch's src/ in as src/demo, and starts Vite. Wargo's node_modules gets
# msw added without saving it to package.json.
set -eu
APP="$(cd "$1" && pwd)"
PORT="${2:-5300}"
DEMO="$(cd "$(dirname "$0")" && pwd)"
WEB="$APP/web"
RUN="${WARGO_DEMO_RUN:-/tmp/opencode/wargo-demo-run}"

MSW="$(sed -n 's/.*"msw": *"\([^"]*\)".*/\1/p' "$DEMO/package.json")"
[ -d "$WEB/node_modules/msw" ] || (cd "$WEB" && npm install --no-save --no-audit --no-fund "msw@$MSW")

rm -rf "$RUN"
mkdir -p "$RUN"
tar -C "$WEB" --exclude=node_modules --exclude=dist -cf - . | tar -C "$RUN" -xf -
ln -s "$WEB/node_modules" "$RUN/node_modules"
rm -rf "$RUN/src/demo"
ln -s "$DEMO/src" "$RUN/src/demo"
cp "$DEMO/vite.demo.config.ts" "$RUN/vite.demo.config.ts"
sed -i 's#src="/src/main.tsx"#src="/src/demo/entry.ts"#' "$RUN/index.html"
(cd "$RUN" && npx msw init public --no-save >/dev/null)
cd "$RUN"
exec npx vite --config vite.demo.config.ts --port "$PORT" --strictPort --host 127.0.0.1
