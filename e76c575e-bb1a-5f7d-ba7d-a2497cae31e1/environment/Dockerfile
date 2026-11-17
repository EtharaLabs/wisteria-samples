# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Task image: openraft source at the pinned base revision under /app/openraft, a Cargo.lock that
# pins every dependency, the nightly-2026-03-01 toolchain, and all crates vendored for offline builds.

FROM --platform=$BUILDPLATFORM rust:1.99.0-slim-trixie@sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581 AS build
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG BASE_REPO=https://github.com/databendlabs/openraft.git
ARG BASE_REV=3c0144e34d156ed1f8e7c6f6940ef1dbb063cfb3
ARG SOURCE_DATE_EPOCH=1793326612
ARG LOCK_SHA256=463a0e212f04367fb7eeeebf29988017dd92b1939c6e99a38e5162b75d4d338a

COPY .build/debian.sources /etc/apt/sources.list.d/debian.sources
RUN rm -f /etc/apt/sources.list \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      git=1:2.47.3-0+deb13u1 \
 && rm -rf /var/lib/apt/lists/*

# Fetch only the pinned revision, refuse any other commit or commit time, then remove git metadata.
RUN set -eu; \
    mkdir -p /opt/statea; \
    git -C /opt/statea init -q; \
    git -C /opt/statea fetch --quiet --depth 1 --no-tags "$BASE_REPO" "$BASE_REV"; \
    got="$(git -C /opt/statea rev-parse 'FETCH_HEAD^{commit}')"; \
    if [ "$got" != "$BASE_REV" ]; then echo "fetched $got, expected the pinned base revision" >&2; exit 1; fi; \
    when="$(git -C /opt/statea log -1 --format=%ct FETCH_HEAD)"; \
    if [ "$when" != "$SOURCE_DATE_EPOCH" ]; then echo "commit time $when is not SOURCE_DATE_EPOCH" >&2; exit 1; fi; \
    git -C /opt/statea -c advice.detachedHead=false checkout -q FETCH_HEAD; \
    rm -rf /opt/statea/.git

# Upstream ships no lock file; this one pins the resolution used for the task, and every crate is
# vendored and checked against its checksum.
COPY .build/Cargo.lock /opt/statea/Cargo.lock
RUN set -eu; \
    echo "$LOCK_SHA256  /opt/statea/Cargo.lock" | sha256sum -c -; \
    cp -a /opt/statea /tmp/vend; rm -f /tmp/vend/rust-toolchain; \
    env -C /tmp/vend PWD=/tmp/vend OLDPWD=/ CARGO_HOME=/tmp/cargo-home cargo vendor --locked /opt/cargo-vendor >/dev/null; \
    cmp /tmp/vend/Cargo.lock /opt/statea/Cargo.lock; \
    chmod -R a+rX,go-w /opt/cargo-vendor /opt/statea; \
    rm -rf /tmp/vend /tmp/cargo-home

# Coding-assistant CLIs, preinstalled because the task image has no network to install them at run
# time: @openai/codex 0.166.0 and Claude Code 2.1.311. Only the two packages are copied into the
# runtime image, not Node: codex is linked to its native binary and claude is native.
FROM node:22.23.4-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d AS cli
RUN npm install -g --no-fund --no-audit @openai/codex@0.166.0 @anthropic-ai/claude-code@2.1.311 \
 && npm cache clean --force

FROM rust:1.99.0-slim-trixie@sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581 AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG SOURCE_DATE_EPOCH=1793326612
ARG TOOLCHAIN=nightly-2026-03-01

LABEL org.opencontainers.image.source="https://github.com/databendlabs/openraft" \
      org.opencontainers.image.revision="3c0144e34d156ed1f8e7c6f6940ef1dbb063cfb3" \
      org.opencontainers.image.licenses="MIT OR Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/rust:1.99.0-slim-trixie" \
      org.opencontainers.image.base.digest="sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581"

# GNU patch, for applying diffs to a tree that has no git metadata.
COPY .build/debian.sources /etc/apt/sources.list.d/debian.sources
RUN rm -f /etc/apt/sources.list \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      patch=2.8-2 \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/*.bin /var/log/apt /var/log/dpkg.log \
      /var/log/alternatives.log /var/cache/debconf/*-old /var/lib/dpkg/*-old \
      /var/cache/ldconfig/aux-cache

# Install the pinned nightly (components checked against the dist manifest) and assert the exact build.
RUN rustup toolchain install "$TOOLCHAIN" --profile minimal \
 && rustup default "$TOOLCHAIN" \
 && rustc -V | grep -q '^rustc 1\.96\.0-nightly (38c0de8dc 2026-02-28)$' \
 && cargo -V | grep -q '^cargo 1\.96\.0-nightly (f298b8c82 2026-02-24)$' \
 && rm -rf /usr/local/rustup/downloads /usr/local/rustup/tmp \
 && chmod -R a+rX,go-w /usr/local/rustup /usr/local/cargo

# Unprivileged user dev (uid 10001); owns the source tree, the build directory and /app.
RUN groupadd --gid 10001 dev \
 && useradd --uid 10001 --gid 10001 --no-log-init --create-home --home-dir /home/dev \
      --shell /bin/bash dev \
 && chage --lastday "$(( SOURCE_DATE_EPOCH / 86400 ))" dev \
 && install -d -o 10001 -g 10001 -m 0755 /var/tmp/openraft-target /app

COPY --from=build --chown=10001:10001 /opt/statea /app/openraft
COPY --from=build /opt/cargo-vendor /opt/cargo-vendor

# Offline cargo against the vendored crates, with a dev-owned CARGO_HOME and build output outside the tree.
ENV CARGO_HOME=/home/dev/.cargo
RUN printf '%s\n' \
      '[source.crates-io]' \
      'replace-with = "vendored-sources"' \
      '' \
      '[source.vendored-sources]' \
      'directory = "/opt/cargo-vendor"' \
      '' \
      '[net]' \
      'offline = true' \
      '' \
      '[build]' \
      'target-dir = "/var/tmp/openraft-target"' \
      > /tmp/config.toml \
 && install -d -o 10001 -g 10001 -m 0755 /home/dev/.cargo \
 && install -o 10001 -g 10001 -m 0644 /tmp/config.toml /home/dev/.cargo/config.toml \
 && rm /tmp/config.toml \
 && printf '%s\n' \
      'export CARGO_HOME=/home/dev/.cargo' \
      'export RUSTUP_HOME=/usr/local/rustup' \
      "export PATH=\"/usr/local/cargo/bin:\$PATH\"" \
      > /etc/profile.d/rust-toolchain.sh \
 && chmod 0644 /etc/profile.d/rust-toolchain.sh

# Prove the offline build: compile the tests of the openraft and tests packages.
USER 10001:10001
RUN env -C /app/openraft PWD=/app/openraft OLDPWD=/ \
      cargo test --locked -p openraft -p tests --tests --no-run 2>&1 | tail -n 3

ENTRYPOINT []
CMD ["bash"]
WORKDIR /app/openraft
# Work in the container runs as the task's [agent].user (10001:10001, user dev); the image itself
# ends as root.
USER root

# The CLIs on the default PATH, where `command -v codex` / `command -v claude` find them for user
# dev. The build checks that the files exist and that uid 10001 can execute them; it does not run
# them.
COPY --from=cli /usr/local/lib/node_modules/@openai/codex /usr/local/lib/node_modules/@openai/codex
COPY --from=cli /usr/local/lib/node_modules/@anthropic-ai/claude-code /usr/local/lib/node_modules/@anthropic-ai/claude-code
RUN codex_bin="$(find /usr/local/lib/node_modules/@openai/codex -type f -path '*/vendor/*/bin/codex' -print -quit)" \
 && rg_bin="$(find /usr/local/lib/node_modules/@openai/codex -type f -path '*/codex-path/rg' -print -quit)" \
 && claude_bin=/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe \
 && test -n "$codex_bin" && test -n "$rg_bin" && test -f "$claude_bin" \
 && ln -sf "$codex_bin" /usr/local/bin/codex \
 && ln -sf "$claude_bin" /usr/local/bin/claude \
 && ln -sf "$rg_bin" /usr/local/bin/rg \
 && for b in codex claude rg; do \
      setpriv --reuid=10001 --regid=10001 --clear-groups -- test -x "/usr/local/bin/$b" || exit 1; \
      echo "cli: /usr/local/bin/$b -> $(readlink "/usr/local/bin/$b") executable by 10001"; \
    done \
 && ! command -v node

# Web tools off: Codex's web search and Claude Code's WebSearch and WebFetch both go through the
# model API hosts the container may reach. The managed (system-level) configs override user config
# and flags; the build fails unless both files hold exactly this.
RUN mkdir -p /etc/codex /etc/claude-code \
 && chmod 0755 /etc/codex /etc/claude-code \
 && printf 'web_search = "disabled"\n' > /etc/codex/managed_config.toml \
 && printf '{"permissions": {"deny": ["WebSearch", "WebFetch"]}}\n' > /etc/claude-code/managed-settings.json \
 && chmod 0444 /etc/codex/managed_config.toml /etc/claude-code/managed-settings.json \
 && grep -qx 'web_search = "disabled"' /etc/codex/managed_config.toml \
 && grep -qxF '{"permissions": {"deny": ["WebSearch", "WebFetch"]}}' /etc/claude-code/managed-settings.json \
 && echo "cli: managed configs in place"
