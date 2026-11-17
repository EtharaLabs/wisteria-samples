# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b
# check=error=true

ARG GO_VERSION=1.25.14
ARG GO_SHA256_AMD64=a21ae5633a269bcd7e90cf767e48225633795e99d831742cbf3397064fee7712
ARG GO_SHA256_ARM64=9bf234ea70ffec9347fdf6b22ce4add51717d3386a38a441e8c8743fceb5eaee

FROM --platform=$BUILDPLATFORM python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f AS fetch
SHELL ["/bin/bash", "-o", "pipefail", "-euc"]
ARG TARGETOS
ARG TARGETARCH
ARG GO_VERSION
ARG GO_SHA256_AMD64
ARG GO_SHA256_ARM64
WORKDIR /fetch
# Builders without BuildKit's automatic platform args (Kaniko) fall back to this stage's own platform.
RUN target_os="${TARGETOS:-linux}"; \
    target_arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "${target_arch}" in \
      amd64) checksum="${GO_SHA256_AMD64}" ;; \
      arm64) checksum="${GO_SHA256_ARM64}" ;; \
      *) echo "unsupported TARGETARCH ${target_arch}" >&2; exit 1 ;; \
    esac; \
    archive="go${GO_VERSION}.${target_os}-${target_arch}.tar.gz"; \
    curl -fsSL -o "${archive}" "https://go.dev/dl/${archive}"; \
    printf '%s  %s\n' "${checksum}" "${archive}" | sha256sum -c -; \
    mkdir -p /opt; \
    tar -C /opt -xzf "${archive}"; \
    rm -f "${archive}"
COPY base.tar.gz /fetch/base.tar.gz
RUN mkdir -p /src; \
    tar -C /src -xzf /fetch/base.tar.gz; \
    test -d /src/opentelemetry-collector-contrib/processor/k8sattributesprocessor/vendor; \
    test -z "$(find /src -name .git -o -name AGENTS.md -o -name AGENT.md -o -name CLAUDE.md)"

FROM python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f AS prewarm
SHELL ["/bin/bash", "-o", "pipefail", "-euc"]
COPY --from=fetch /opt/go /usr/local/go
COPY --from=fetch /src/opentelemetry-collector-contrib /app/opentelemetry-collector-contrib
ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
    GOPROXY=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOTELEMETRY=off \
    GOCACHE=/opt/gocache \
    GOPATH=/prewarm/gopath
WORKDIR /app/opentelemetry-collector-contrib/processor/k8sattributesprocessor
RUN go test -count=1 -run '^$' ./...; \
    go vet ./internal/kube/

# Command-line coding assistants, preinstalled on PATH because the no-network environment cannot
# install them at run time: @openai/codex 0.166.0 and Claude Code 2.1.311. Only the two
# packages are copied into the runtime stage, not Node: codex and rg are linked to their native
# binaries and claude is native. The build checks the binaries without executing them.
FROM node:22.23.4-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d AS cli
RUN npm install -g --no-fund --no-audit @openai/codex@0.166.0 @anthropic-ai/claude-code@2.1.311 \
    && npm cache clean --force

FROM python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-euc"]
LABEL org.opencontainers.image.title="opentelemetry-collector-contrib workspace at the k8sattributes issue" \
      org.opencontainers.image.source="https://github.com/open-telemetry/opentelemetry-collector-contrib" \
      org.opencontainers.image.revision="bd71eee5197f6cf5869d9958ea9ff67782296a69" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/python:3.12-trixie" \
      org.opencontainers.image.base.digest="sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f"
# The repository lives under /app, outside Kaniko's default build-context directory, where a Kaniko build
# either cannot write or leaves what it wrote out of the image.
RUN groupadd --gid 10001 dev; \
    useradd --uid 10001 --gid 10001 --no-log-init --create-home --home-dir /home/dev --shell /bin/bash dev; \
    install -d -o 10001 -g 10001 -m 0755 /app /home/dev/.cache
COPY --from=fetch --chown=0:0 /opt/go /usr/local/go
COPY --from=fetch --chown=10001:10001 /src/opentelemetry-collector-contrib /app/opentelemetry-collector-contrib
COPY --from=prewarm --chown=10001:10001 /opt/gocache /home/dev/.cache/go-build
COPY --from=cli --chown=0:0 /usr/local/lib/node_modules/@openai/codex /usr/local/lib/node_modules/@openai/codex
COPY --from=cli --chown=0:0 /usr/local/lib/node_modules/@anthropic-ai/claude-code /usr/local/lib/node_modules/@anthropic-ai/claude-code
RUN codex_dir=/usr/local/lib/node_modules/@openai/codex; \
    codex_bin="$(find "${codex_dir}" -type f -path '*/vendor/*/bin/codex' -print -quit)"; \
    rg_bin="$(find "${codex_dir}" -type f -path '*/codex-path/rg' -print -quit)"; \
    claude_bin=/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe; \
    for f in "${codex_bin}" "${rg_bin}" "${claude_bin}"; do \
      test -n "$f"; test -f "$f"; test -x "$f"; \
      python3 -c 'import sys; sys.exit(0 if open(sys.argv[1], "rb").read(4) == b"\x7fELF" else sys.argv[1] + ": not an ELF file")' "$f"; \
    done; \
    ln -sf "${codex_bin}" /usr/local/bin/codex; \
    ln -sf "${claude_bin}" /usr/local/bin/claude; \
    ln -sf "${rg_bin}" /usr/local/bin/rg; \
    test -x /usr/local/bin/codex; test -x /usr/local/bin/claude; test -x /usr/local/bin/rg; \
    if command -v node; then echo "node must not be in the image" >&2; exit 1; fi; \
    echo "CLIs linked: ${codex_bin} ${claude_bin} ${rg_bin}"
# Web tools off: Codex's web search and Claude Code's WebSearch and WebFetch would reach the open
# web through the model API hosts that stay reachable. System-level (managed) configs override user config.
RUN mkdir -p /etc/codex /etc/claude-code; \
    chmod 0755 /etc/codex /etc/claude-code; \
    printf 'web_search = "disabled"\n' > /etc/codex/managed_config.toml; \
    printf '{"permissions": {"deny": ["WebSearch", "WebFetch"]}}\n' > /etc/claude-code/managed-settings.json; \
    chmod 0444 /etc/codex/managed_config.toml /etc/claude-code/managed-settings.json; \
    grep -qx 'web_search = "disabled"' /etc/codex/managed_config.toml; \
    python3 -c 'import json; d = set(json.load(open("/etc/claude-code/managed-settings.json"))["permissions"]["deny"]); raise SystemExit(0 if {"WebSearch", "WebFetch"} <= d else 1)'; \
    echo "managed configs checked: codex web_search disabled; claude-code denies WebSearch and WebFetch"
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME=/home/dev \
    GOPROXY=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOTELEMETRY=off \
    GOCACHE=/home/dev/.cache/go-build
WORKDIR /app/opentelemetry-collector-contrib
# Work runs as uid:gid 10001:10001 (user dev), as task.toml sets; the image itself ends as root.
USER root
CMD ["/bin/bash"]
