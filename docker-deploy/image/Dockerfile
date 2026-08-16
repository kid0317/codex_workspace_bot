# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/appctl ./cmd/appctl \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/codex-bridge ./cmd/codex-bridge \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/codex-remote ./cmd/codex-remote \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/secure-appctl ./cmd/secure-appctl \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/provider-proxy ./cmd/provider-proxy

FROM --platform=$TARGETPLATFORM node:22-bookworm-slim AS codex
ARG CODEX_VERSION=0.147.0
RUN npm install --global "@openai/codex@${CODEX_VERSION}" \
    && npm cache clean --force

FROM debian:bookworm-slim

ARG VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="Codex Workspace Bot" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.licenses="NOASSERTION"

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl git jq openssh-client ripgrep tini unzip \
    && rm -rf /var/lib/apt/lists/*
COPY --from=codex /usr/local/lib/node_modules/@openai/codex/node_modules/@openai/ /opt/codex-packages/
RUN codex_binary="$(find /opt/codex-packages -type f -path '*/bin/codex' | head -n 1)" \
    && test -n "$codex_binary" \
    && ln -s "$codex_binary" /usr/local/bin/codex \
    && groupadd --gid 10001 app \
    && useradd --uid 10001 --gid 10001 --create-home --shell /bin/bash app \
    && codex --version

WORKDIR /opt/codex-workspace-bot
COPY --from=builder /out/server /usr/local/bin/codex-workspace-bot-server
COPY --from=builder /out/appctl /usr/local/bin/appctl
COPY --from=builder /out/codex-bridge /usr/local/bin/codex-bridge
COPY --from=builder /out/codex-remote /usr/local/bin/codex-remote
COPY --from=builder /out/secure-appctl /usr/local/bin/secure-appctl
COPY --from=builder /out/provider-proxy /usr/local/bin/provider-proxy
COPY migrations ./migrations

USER 10001:10001
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/codex-workspace-bot-server", "--config", "/space/config/bot.yaml"]
