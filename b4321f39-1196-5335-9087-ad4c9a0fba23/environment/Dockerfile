# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Agent image: nats-server at commit 6619d7f7d7211f37479d13a8a8cd104a6acbaed1, the first parent of
# the merge of pull request 8765 (the tree before that change), without git metadata, with every
# module the repository needs already in the module cache (no network needed).
#
# The source is fetched in a build stage that checks the commit id, the commit time and the sha256
# of the whole fetched tree. The runtime stage adds the pinned codex and claude command-line tools,
# creates the user dev (uid 1000, gid 1000, home /home/dev) that owns /app, and warms that user's Go
# build cache for server/stree.

FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS fetch
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG BASE_REPO=https://github.com/nats-io/nats-server.git
ARG BASE_REV=6619d7f7d7211f37479d13a8a8cd104a6acbaed1
ARG SOURCE_DATE_EPOCH=1793366398
ARG TREE_SHA256=03cca4de0f0d77c004c81df9abfcd705604221860b70a8ff812113555e865ec2

WORKDIR /src/app
RUN set -eu; \
    git init -q .; \
    git fetch --quiet --depth 1 --no-tags "$BASE_REPO" "$BASE_REV"; \
    got="$(git rev-parse 'FETCH_HEAD^{commit}')"; \
    if [ "$got" != "$BASE_REV" ]; then echo "fetched $got, expected $BASE_REV" >&2; exit 1; fi; \
    when="$(git log -1 --format=%ct FETCH_HEAD)"; \
    if [ "$when" != "$SOURCE_DATE_EPOCH" ]; then echo "commit time $when is not $SOURCE_DATE_EPOCH" >&2; exit 1; fi; \
    git -c advice.detachedHead=false checkout -q FETCH_HEAD; \
    rm -rf /src/app/.git; \
    sum="$(find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"; \
    if [ "$sum" != "$TREE_SHA256" ]; then echo "tree digest $sum is not $TREE_SHA256" >&2; exit 1; fi

# @openai/codex 0.166.0 and Claude Code 2.1.311. Only the two packages are copied into the runtime image, not
# Node: codex is linked to its native binary rather than the codex.js launcher, and claude is native.
FROM node:22.23.4-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d AS cli
RUN npm install -g --no-fund --no-audit @openai/codex@0.166.0 @anthropic-ai/claude-code@2.1.311 \
    && npm cache clean --force

FROM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS runtime

LABEL org.opencontainers.image.title="nats-server server/stree test-generation workspace" \
      org.opencontainers.image.description="nats-server at the first parent of the merge of pull request 8765 (the tree before that change), with its modules, for writing server/stree tests." \
      org.opencontainers.image.source="https://github.com/nats-io/nats-server" \
      org.opencontainers.image.revision="6619d7f7d7211f37479d13a8a8cd104a6acbaed1" \
      org.opencontainers.image.version="2.1.2" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/golang:1.26.10-bookworm" \
      org.opencontainers.image.base.digest="sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb"

ENV GOTOOLCHAIN=local CGO_ENABLED=1 GOFLAGS=-mod=mod

COPY --from=fetch /src/app /app
RUN set -eu; \
    case "$(go version)" in *'go1.26.10 '*) ;; *) echo "unexpected Go: $(go version)" >&2; exit 1 ;; esac; \
    env -C /app go mod download

ENV GOPROXY=off GOCACHE=/home/dev/.cache/go-build
# Debian's /etc/profile resets PATH for non-root login shells, which drops /usr/local/go/bin;
# agent harnesses run commands through such shells, so put the toolchain on the default PATH.
RUN ln -s /usr/local/go/bin/go /usr/local/bin/go && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
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

RUN useradd --create-home --uid 1000 --shell /bin/bash dev && chown -R dev:dev /app

# Warm the build cache for the package under test, as the user dev who will use it.
USER 1000:1000
WORKDIR /app
RUN go test -count=1 -race ./server/stree/ > /dev/null

# The image ends as root (the Smoldata runtime rule); task.toml runs the agent as uid 1000 (dev).
USER root
CMD ["bash"]
