#!/bin/sh
# Dev loop for macOS using a dedicated Colima profile.
#   scripts/colima-dev.sh test [go test args]   vet + go test -race on Linux (default ./...)
#   scripts/colima-dev.sh deploy                build wargo:dev in the VM and recreate the container
#   scripts/colima-dev.sh logs                  follow the container logs
# All Docker calls run inside the VM via `colima ssh`, so the Mac's Docker
# context is never changed.
set -eu

P="${WARGO_COLIMA_PROFILE:-wargo}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"

vm() { colima ssh -p "$P" -- "$@"; }

if ! vm test -d "$REPO/internal" >/dev/null 2>&1; then
	echo "The repo ($REPO) is not mounted in the Colima profile '$P'." >&2
	echo "Start it with: colima start -p $P --cpu 4 --memory 4 --disk 40 --mount \"$REPO\"" >&2
	exit 1
fi

cmd="${1:-}"
[ $# -gt 0 ] && shift

case "$cmd" in
test)
	[ $# -gt 0 ] || set -- ./...
	args=""
	for a in "$@"; do args="$args '$a'"; done
	inner="export PATH=\$PATH:/usr/local/go/bin GOCACHE=/cache/build GOMODCACHE=/cache/mod CGO_CFLAGS='-O2 -g -D_GNU_SOURCE'; \
rm -rf /tmp/w && mkdir /tmp/w && cd /src && tar --exclude=./web/node_modules --exclude=./.git -cf - . | tar -xf - -C /tmp/w && cd /tmp/w && \
go vet ./... && go test -race $args"
	outer="apt-get update -qq >/dev/null && apt-get install -y -qq libpam0g-dev >/dev/null && \
useradd -m tester && mkdir -p /cache && chown tester /cache && su tester -c \"\$INNER\""
	vm docker run --rm -e "INNER=$inner" -v "$REPO:/src:ro" -v wargo-gocache:/cache \
		golang:1.26-bookworm sh -c "$outer"
	;;
deploy)
	sha="$(git -C "$REPO" rev-parse --short HEAD)"
	vm sh -c "cd '$REPO' && docker buildx build -t wargo:dev --build-arg VERSION=dev-$sha --build-arg REVISION=$sha --load ."
	vm docker rm -f wargo >/dev/null 2>&1 || true
	vm docker run -d --name wargo --restart unless-stopped --privileged \
		--pid host --network host --uts host \
		-v /:/host -v /var/run/docker.sock:/var/run/docker.sock -v wargo-data:/data \
		-e WARGO_HOST_ROOT=/host -e WARGO_USER=tester wargo:dev
	;;
logs)
	vm docker logs -f wargo
	;;
*)
	echo "usage: $0 test [go test args] | deploy | logs" >&2
	exit 2
	;;
esac
