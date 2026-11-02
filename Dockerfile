# syntax=docker/dockerfile:1
# SPDX-License-Identifier: AGPL-3.0-only
#
# Wargo in container mode. See docs/DOCKER.md for the required run options
# (privileged, pid/network/uts host, / mounted at /host).

# --- frontend (architecture independent: always built on the build host) ---
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# --- Go binary (cgo for PAM and libxcrypt) ---
FROM golang:1.26-bookworm AS build
RUN apt-get update \
 && apt-get install -y --no-install-recommends libpam0g-dev libcrypt-dev \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
# -tags wargo_container links libxcrypt (host /etc/shadow verification),
# which only container mode needs; native release builds leave it out.
RUN CGO_ENABLED=1 go build -trimpath -tags wargo_container \
      -ldflags "-s -w -X github.com/kushalpandya/wargo/internal/server.Version=${VERSION}" \
      -o /out/wargo ./cmd/wargo

# --- runtime ---
# Wargo must run as root in this image (container mode: nsenter into the
# host namespaces, per-thread identity switching, reading the host's
# /etc/shadow); see docs/SECURITY.md#container-mode. Base images are pinned
# by Debian release; rebuild regularly for security updates.
FROM debian:bookworm-slim
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Wargo" \
      org.opencontainers.image.description="Single-binary homelab dashboard: system, processes, containers, terminal, files, OS updates" \
      org.opencontainers.image.source="https://github.com/kushalpandya/wargo" \
      org.opencontainers.image.url="https://github.com/kushalpandya/wargo" \
      org.opencontainers.image.documentation="https://github.com/kushalpandya/wargo/blob/main/docs/DOCKER.md" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
# util-linux: nsenter (host namespaces) and setpriv; tini reaps children.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      util-linux libpam0g libcrypt1 ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* /usr/share/doc/* /usr/share/man/*
COPY --from=build /out/wargo /usr/local/bin/wargo
ENV WARGO_HOST_ROOT=/host \
    WARGO_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 2207
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/wargo", "health"]
# -s: tini is not PID 1 with pid: host, so register as a subreaper.
ENTRYPOINT ["tini", "-s", "--", "/usr/local/bin/wargo"]
CMD ["serve"]
