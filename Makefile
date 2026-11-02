# SPDX-License-Identifier: AGPL-3.0-only
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-alpha)
# _GNU_SOURCE: the PAM bindings need RTLD_NEXT, which glibc < 2.34 only
# declares with it (building on older distributions).
export CGO_CFLAGS ?= -O2 -g -D_GNU_SOURCE
LDFLAGS := -s -w -X github.com/kushalpandya/wargo/internal/server.Version=$(VERSION)

.PHONY: all web build privd test lint dev clean

all: web build privd

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build:
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/wargo ./cmd/wargo

privd:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/wargo-privd ./cmd/wargo-privd

test:
	go test ./...
	cd web && npm test --if-present

lint:
	go vet ./...
	cd web && npm run lint --if-present

# Run the backend with a local dev config (see config.example.toml).
dev:
	go run ./cmd/wargo -config dev.local.toml

clean:
	rm -rf bin web/dist/assets web/dist/index.html
