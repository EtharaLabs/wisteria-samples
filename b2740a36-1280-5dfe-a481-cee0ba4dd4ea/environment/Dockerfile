# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Environment image: fontconfig source at the pinned base revision under /app/fontconfig, with a
# prebuilt out-of-tree meson build under /app/fontconfig-build.

FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS build
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG BASE_REPO=https://gitlab.freedesktop.org/fontconfig/fontconfig.git
ARG BASE_REV=c8b9654feda3612f65eb70c4f8db8910d1a7daf1
ARG SOURCE_DATE_EPOCH=1791169496

COPY .build/debian.sources /etc/apt/sources.list.d/debian.sources
RUN rm -f /etc/apt/sources.list \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      ca-certificates=20250419+deb13u1 \
      git=1:2.47.3-0+deb13u1 \
 && rm -rf /var/lib/apt/lists/*

# Fetch only the pinned revision, verified against its commit id, then remove all git metadata.
RUN set -eu; \
    mkdir -p /opt/statea; \
    git -C /opt/statea init -q; \
    git -C /opt/statea fetch --quiet --depth 1 --no-tags "$BASE_REPO" "$BASE_REV"; \
    got="$(git -C /opt/statea rev-parse 'FETCH_HEAD^{commit}')"; \
    if [ "$got" != "$BASE_REV" ]; then echo "fetched $got, expected the pinned base revision" >&2; exit 1; fi; \
    when="$(git -C /opt/statea log -1 --format=%ct FETCH_HEAD)"; \
    if [ "$when" != "$SOURCE_DATE_EPOCH" ]; then echo "commit time $when is not SOURCE_DATE_EPOCH" >&2; exit 1; fi; \
    git -C /opt/statea -c advice.detachedHead=false checkout -q FETCH_HEAD; \
    rm -rf /opt/statea/.git; \
    chmod -R a+rX,go-w /opt/statea

# meson's pure-Python wheel, checked against the manifest's hash, from its own build-platform stage.
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS wheels
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

COPY .build/debian.sources /etc/apt/sources.list.d/debian.sources
RUN rm -f /etc/apt/sources.list \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      python3=3.13.5-1 \
      python3.13=3.13.5-2+deb13u5 \
 && rm -rf /var/lib/apt/lists/*

# The installer, checked against its digest here (Kaniko ignores ADD --checksum).
ADD https://bootstrap.pypa.io/pip/zipapp/pip-26.3.pyz /opt/pip/pip.pyz
RUN echo "be510f6d23d345eeabdd3eec2599a3ca2df3cbc98c4afbe2631561ca12e744d7  /opt/pip/pip.pyz" \
      | sha256sum --check --strict -
COPY .build/meson.lock /opt/pip/
RUN python3 /opt/pip/pip.pyz install --no-cache-dir --disable-pip-version-check --no-deps \
      --require-hashes --only-binary=:all: --target /opt/meson/site -r /opt/pip/meson.lock \
 && rm -rf /opt/meson/site/bin /opt/meson/site/share

# The codex and claude CLIs, preinstalled so the runner's install check (`command -v codex`,
# `command -v claude`, run as the task's [agent].user) finds them and skips its run-time install under
# network_mode = "no-network". Only the packages are copied into the runtime, not Node: codex is
# linked to its native binary and claude is native.
FROM node:22.23.4-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d AS cli
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN npm install -g --no-fund --no-audit @openai/codex@0.166.0 @anthropic-ai/claude-code@2.1.311 \
 && npm cache clean --force

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG SOURCE_DATE_EPOCH=1791169496
ARG MESON_OPTIONS="-Ddoc=disabled -Dnls=disabled -Dtests=enabled -Dtools=enabled -Dtests-external-fonts=disabled -Dtests-bwrap=disabled -Dcache-build=disabled -Dfontations=disabled -Dxml-backend=expat"
ARG TARGETARCH

LABEL org.opencontainers.image.source="https://gitlab.freedesktop.org/fontconfig/fontconfig" \
      org.opencontainers.image.revision="c8b9654feda3612f65eb70c4f8db8910d1a7daf1" \
      org.opencontainers.image.licenses="HPND-sell-variant" \
      org.opencontainers.image.base.name="docker.io/library/debian:trixie-slim" \
      org.opencontainers.image.base.digest="sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2"

# Toolchain and build libraries, python3/pytest, GNU patch and the CA bundle.
COPY --chown=0:0 .build/debian.sources /etc/apt/sources.list.d/debian.sources
RUN rm -f /etc/apt/sources.list \
 && arch="${TARGETARCH:-$(dpkg --print-architecture)}" \
 && case "$arch" in \
      amd64) ninja_version=1.12.1-1 ;; \
      arm64) ninja_version=1.12.1-1+b1 ;; \
      *) echo "no pinned ninja-build for $arch" >&2; exit 1 ;; \
    esac \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      ca-certificates=20250419+deb13u1 \
      gcc=4:14.2.0-1 \
      gperf=3.2.1-1 \
      libc6-dev=2.41-12+deb13u4 \
      libexpat1-dev=2.9.0-1~deb13u1 \
      libfreetype-dev=2.13.3+dfsg-1+deb13u1 \
      libjson-c-dev=0.18+ds-1 \
      ninja-build="$ninja_version" \
      patch=2.8-2 \
      pkgconf=1.8.1-4 \
      python3=3.13.5-1 \
      python3.13=3.13.5-2+deb13u5 \
      python3-pytest=8.3.5-2 \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/*.bin /var/log/apt /var/log/dpkg.log \
      /var/log/alternatives.log /var/cache/debconf/*-old /var/lib/dpkg/*-old \
      /var/cache/ldconfig/aux-cache

COPY --from=wheels /opt/meson/site/ /usr/local/lib/python3.13/dist-packages/
COPY --chown=0:0 .build/meson.sh /usr/local/bin/meson
RUN chmod 0755 /usr/local/bin/meson \
 && test "$(meson --version)" = "1.11.2"

ENV PYTHONDONTWRITEBYTECODE=1

# Unprivileged user dev (uid and gid 10001); owns /app.
RUN groupadd --gid 10001 dev \
 && useradd --uid 10001 --gid 10001 --no-log-init --create-home --home-dir /home/dev \
      --shell /bin/bash dev \
 && chage --lastday "$(( SOURCE_DATE_EPOCH / 86400 ))" dev \
 && install -d -o 10001 -g 10001 -m 0755 /app /app/fontconfig-build

COPY --from=build --chown=10001:10001 /opt/statea /app/fontconfig

# Configure and build out of tree; the source tree must be unchanged afterwards.
USER 10001:10001
RUN set -eu; \
    before="$(env -C /app/fontconfig find . -type f -print0 | sort -z | env -C /app/fontconfig xargs -0 sha256sum | sha256sum)"; \
    meson setup /app/fontconfig-build /app/fontconfig $MESON_OPTIONS >/tmp/setup.log 2>&1 \
      || { tail -n 40 /tmp/setup.log; exit 1; }; \
    ninja -C /app/fontconfig-build -j4 >/tmp/build.log 2>&1 || { tail -n 40 /tmp/build.log; exit 1; }; \
    rm -f /tmp/setup.log /tmp/build.log; \
    after="$(env -C /app/fontconfig find . -type f -print0 | sort -z | env -C /app/fontconfig xargs -0 sha256sum | sha256sum)"; \
    if [ "$before" != "$after" ]; then echo "the build wrote into the source tree" >&2; exit 1; fi; \
    printf '%s\n' "$MESON_OPTIONS" >/app/fontconfig-build/MESON_OPTIONS

ENTRYPOINT []
CMD ["bash"]
WORKDIR /app/fontconfig
# Work in the container runs as the task's [agent].user 10001:10001 (user dev); the image itself ends as root.
USER root
COPY --from=cli /usr/local/lib/node_modules/@openai/codex /usr/local/lib/node_modules/@openai/codex
COPY --from=cli /usr/local/lib/node_modules/@anthropic-ai/claude-code /usr/local/lib/node_modules/@anthropic-ai/claude-code
# Link the CLIs into /usr/local/bin and turn the agents' web tools off through their managed
# (system-level) configs. The binaries are checked to exist and be executable; none is run here.
RUN set -eu; \
    codex_bin="$(find /usr/local/lib/node_modules/@openai/codex -type f -path '*/vendor/*/bin/codex' -print -quit)"; \
    rg_bin="$(find /usr/local/lib/node_modules/@openai/codex -type f -path '*/codex-path/rg' -print -quit)"; \
    claude_bin=/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe; \
    echo "cli: codex=$codex_bin rg=$rg_bin claude=$claude_bin"; \
    test -n "$codex_bin"; test -x "$codex_bin"; \
    test -n "$rg_bin"; test -x "$rg_bin"; \
    test -f "$claude_bin"; test -x "$claude_bin"; \
    ln -sf "$codex_bin" /usr/local/bin/codex; \
    ln -sf "$claude_bin" /usr/local/bin/claude; \
    ln -sf "$rg_bin" /usr/local/bin/rg; \
    test -x /usr/local/bin/codex; test -x /usr/local/bin/claude; test -x /usr/local/bin/rg; \
    mkdir -p /etc/codex /etc/claude-code; \
    chmod 0755 /etc/codex /etc/claude-code; \
    printf 'web_search = "disabled"\n' >/etc/codex/managed_config.toml; \
    printf '{"permissions": {"deny": ["WebSearch", "WebFetch"]}}\n' >/etc/claude-code/managed-settings.json; \
    chmod 0444 /etc/codex/managed_config.toml /etc/claude-code/managed-settings.json; \
    grep -qx 'web_search = "disabled"' /etc/codex/managed_config.toml; \
    python3 -c 'import json, sys; d = set(json.load(open("/etc/claude-code/managed-settings.json"))["permissions"]["deny"]); sys.exit(0 if {"WebSearch", "WebFetch"} <= d else "managed-settings.json does not deny WebSearch and WebFetch")'; \
    if command -v node >/dev/null; then echo "node must not be in the runtime image" >&2; exit 1; fi; \
    echo "cli: linked; web tools disabled"
