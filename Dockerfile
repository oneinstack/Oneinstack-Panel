# syntax=docker/dockerfile:1.7

FROM golang:1.24.4-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETARCH
ARG VERSION=docker-local
ARG BUILD_TIME=unknown
ARG COMMIT_HASH=unknown
ARG WEB_VERSION=1.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
    go build -trimpath \
      -ldflags="-s -w \
        -X oneinstack/internal/buildinfo.Version=${VERSION} \
        -X oneinstack/internal/buildinfo.BuildTime=${BUILD_TIME} \
        -X oneinstack/internal/buildinfo.CommitHash=${COMMIT_HASH} \
        -X oneinstack/internal/buildinfo.WebVersion=${WEB_VERSION}" \
      -o /out/one ./cmd

FROM debian:bookworm-slim AS runtime-base

ARG VERSION=docker-local
ARG BUILD_TIME=unknown
ARG COMMIT_HASH=unknown

LABEL org.opencontainers.image.title="OneinStack Panel" \
      org.opencontainers.image.description="OneinStack-compatible server management panel" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.revision="${COMMIT_HASH}"

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      bash \
      ca-certificates \
      curl \
      gzip \
      iproute2 \
      openssh-client \
      procps \
      tar \
      unzip \
      xz-utils \
      zip \
    && rm -rf /var/lib/apt/lists/*

COPY docker/entrypoint.sh /usr/local/bin/one-docker-entrypoint
COPY script-registry/bundled /opt/oneinstack/script-registry/bundled

RUN chmod 0755 /usr/local/bin/one-docker-entrypoint \
    && mkdir -p /var/lib/oneinstack-panel /data

ENV ONEINSTACK_BASE_PATH=/var/lib/oneinstack-panel \
    ONEINSTACK_CONFIG_PATH=/var/lib/oneinstack-panel/config.yaml \
    ONEINSTACK_SYSTEM_BIND_ADDRESS=0.0.0.0 \
    ONEINSTACK_SYSTEM_PORT=8089 \
    ONEINSTACK_SYSTEM_HTTPS_ENABLED=false \
    ONEINSTACK_SYSTEM_CERTIFICATE_PATH=/var/lib/oneinstack-panel/certificates \
    ONEINSTACK_SYSTEM_ACME_CHALLENGE_PATH=/var/lib/oneinstack-panel/acme-webroot \
    ONEINSTACK_SCRIPT_CENTER_CACHE_PATH=/var/lib/oneinstack-panel/script-registry/cache \
    ONEINSTACK_SCRIPT_CENTER_BUNDLED_PATH=/opt/oneinstack/script-registry/bundled

WORKDIR /var/lib/oneinstack-panel

EXPOSE 8089

VOLUME ["/var/lib/oneinstack-panel", "/data"]

HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=6 \
  CMD curl --fail --silent --show-error http://127.0.0.1:8089/health/ready >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/one-docker-entrypoint"]
CMD ["server", "start"]

# Local deployment can reuse the already verified release binary and avoid a
# second dependency download. The default final stage below remains a complete
# source build for CI and clean checkouts.
FROM runtime-base AS runtime-prebuilt

ARG TARGETARCH
RUN --mount=type=bind,source=dist,target=/prebuilt,ro \
    install -m 0755 "/prebuilt/one-linux-${TARGETARCH}" /usr/local/bin/one

FROM runtime-base AS runtime-source

COPY --from=builder /out/one /usr/local/bin/one
