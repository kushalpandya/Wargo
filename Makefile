# SPDX-License-Identifier: AGPL-3.0-only
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-alpha)
# _GNU_SOURCE: the PAM bindings need RTLD_NEXT, which glibc < 2.34 only
# declares with it (building on older distributions).
export CGO_CFLAGS ?= -O2 -g -D_GNU_SOURCE
LDFLAGS := -s -w -X github.com/kushalpandya/wargo/internal/server.Version=$(VERSION)

.PHONY: all web build privd install-macos test lint dev clean

# wargo-privd is Linux only (systemd socket activation).
ifeq ($(shell uname -s),Darwin)
all: web build
else
all: web build privd
endif

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build:
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/wargo ./cmd/wargo

privd:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/wargo-privd ./cmd/wargo-privd

# Install the per-user LaunchAgent on macOS (asks for sudo for /usr/local/bin).
install-macos: web build
	sh deploy/launchd/install.sh

test:
	go test ./...
	cd web && npm test --if-present

lint:
	go vet ./...
	cd web && npm run lint --if-present

# Run the backend and the live-reload UI together; Ctrl+C stops both.
# ARGS=--lan also serves the UI on the LAN (sign-in required).
dev:
	sh scripts/dev.sh $(ARGS)

clean:
	rm -rf bin web/dist/assets web/dist/index.html
