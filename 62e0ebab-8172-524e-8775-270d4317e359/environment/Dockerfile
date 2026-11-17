# syntax=docker/dockerfile:1.20@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d
# Development environment: nats-server source (no .git) with warm, offline Go module and build caches.
# Both stages use the same pinned multi-platform index digest of the Go release, for reproducible builds.

ARG SOURCE_DATE_EPOCH=1792960721

# ---- Stage 1: warm the caches on the build platform, cross-compiled for the target ----
FROM --platform=$BUILDPLATFORM golang:1.26.10@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS warm

ARG TARGETOS
ARG TARGETARCH
ENV GOTOOLCHAIN=local \
    GOFLAGS=-mod=mod \
    GOMODCACHE=/warm/mod \
    GOCACHE=/warm/build

WORKDIR /warm/src
COPY nats-server/ /warm/src/

# Download every module this tree declares, then cross-compile the whole tree
# for the target platform so the build cache the solver inherits is the target's
# own. The compiled output is discarded: only the caches are kept.
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN set -e; \
    go version 2>&1 | tee /warm/go-version.txt; \
    go mod download; \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -o /warm/discard/ ./... 2>&1 | tail -5; \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" go vet ./server 2>&1 | tail -5 || true; \
    rm -rf /warm/discard
SHELL ["/bin/sh", "-c"]

# ---- Stage 2: coding-assistant CLIs, preinstalled because the environment has no network ----
# ([environment] network_mode = "no-network"), so nothing can be installed at run time. Pinned versions:
# @openai/codex 0.166.0 and Claude Code 2.1.311. Only the two packages are copied into the runtime image, not
# Node: codex is linked to its native binary rather than the codex.js launcher, and claude is native.
FROM node:22.23.4-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d AS cli
RUN npm install -g --no-fund --no-audit @openai/codex@0.166.0 @anthropic-ai/claude-code@2.1.311 \
    && npm cache clean --force

# ---- Stage 3: runtime image, resolved for the target platform from the same digest ----
FROM golang:1.26.10@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS runtime

ARG SOURCE_DATE_EPOCH=1792960721

LABEL org.opencontainers.image.title="ethara/62e0ebab-8172-524e-8775-270d4317e359 environment" \
      org.opencontainers.image.description="Offline nats-server source tree with a pinned Go toolchain and warm module cache." \
      org.opencontainers.image.source="https://github.com/nats-io/nats-server" \
      org.opencontainers.image.revision="011cda33cec116deb81bd0a79ad5308e993f6b62" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/golang:1.26.10" \
      org.opencontainers.image.base.digest="sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c" \
      org.opencontainers.image.created="2026-10-25T20:38:41Z"

# Offline for good: no proxy to reach, no toolchain to fetch, modules resolved
# from the warm cache alone.
ENV GOFLAGS=-mod=mod \
    GOPROXY=off \
    GOTOOLCHAIN=local \
    GOMODCACHE=/app/.gomodcache \
    GOCACHE=/app/.gocache \
    GOBIN=/app/.gobin \
    HOME=/app

WORKDIR /app

COPY --from=warm --chown=65532:65532 /warm/src/ /app/nats-server/
COPY --from=warm --chown=65532:65532 /warm/mod/ /app/.gomodcache/
COPY --from=warm --chown=65532:65532 /warm/build/ /app/.gocache/

# A numeric non-root identity equal to task.toml's [agent] user. The workdir is
# the only writable place; nothing is setuid and no sudo is installed.
RUN set -e; \
    mkdir -p /app/.gobin; \
    chown -R 65532:65532 /app; \
    chmod -R u+rwX,go-w /app; \
    find /usr/bin /usr/sbin /usr/lib -xdev -perm /6000 -type f -print -delete; \
    find /app -newermt "@${SOURCE_DATE_EPOCH}" -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

# A login shell resets PATH for every non-root user in Debian's /etc/profile;
# put the Go toolchain back on it so `bash -l` finds go as the solver.
RUN set -e; \
    printf '%s\n' 'export PATH=/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' > /etc/profile.d/go-path.sh; \
    chown root:root /etc/profile.d/go-path.sh; \
    chmod 0644 /etc/profile.d/go-path.sh

# The CLIs on every user's PATH in /usr/local/bin, so `command -v codex` and `command -v claude` find them as
# the non-root user. The binaries are checked to be present and executable; none is run at build time.
COPY --from=cli /usr/local/lib/node_modules/@openai/codex /usr/local/lib/node_modules/@openai/codex
COPY --from=cli /usr/local/lib/node_modules/@anthropic-ai/claude-code /usr/local/lib/node_modules/@anthropic-ai/claude-code
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN set -e; \
    codex_bin="$(find /usr/local/lib/node_modules/@openai/codex -type f -path '*/vendor/*/bin/codex' -print -quit)"; \
    rg_bin="$(find /usr/local/lib/node_modules/@openai/codex -type f -path '*/codex-path/rg' -print -quit)"; \
    claude_bin=/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe; \
    test -n "$codex_bin"; test -n "$rg_bin"; \
    test -x "$codex_bin"; test -x "$rg_bin"; test -x "$claude_bin"; \
    ln -sf "$codex_bin" /usr/local/bin/codex; \
    ln -sf "$claude_bin" /usr/local/bin/claude; \
    ln -sf "$rg_bin" /usr/local/bin/rg; \
    test -x /usr/local/bin/codex; test -x /usr/local/bin/claude; test -x /usr/local/bin/rg; \
    printf 'cli: %s\n' "$codex_bin" "$claude_bin" "$rg_bin"; \
    mkdir -p /etc/codex /etc/claude-code; \
    chmod 0755 /etc/codex /etc/claude-code; \
    printf 'web_search = "disabled"\n' > /etc/codex/managed_config.toml; \
    printf '{"permissions": {"deny": ["WebSearch", "WebFetch"]}}\n' > /etc/claude-code/managed-settings.json; \
    chmod 0444 /etc/codex/managed_config.toml /etc/claude-code/managed-settings.json; \
    grep -qx 'web_search = "disabled"' /etc/codex/managed_config.toml; \
    test "$(cat /etc/claude-code/managed-settings.json)" = '{"permissions": {"deny": ["WebSearch", "WebFetch"]}}'; \
    printf 'managed config: %s\n' "$(cat /etc/codex/managed_config.toml)" "$(cat /etc/claude-code/managed-settings.json)"
SHELL ["/bin/sh", "-c"]

# Work in the container runs as the numeric non-root uid 65532:65532 (task.toml); the image itself ends as root.
USER root
