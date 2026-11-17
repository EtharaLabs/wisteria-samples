# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Verifier image: the pristine fontconfig tree under /opt/statea, builds of releases 2.17.1 and
# 2.18.1 under /opt/baselines, the same toolchain as the agent image, and pinned Reward Kit 0.2.4.

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

# fetch DIR COMMIT EPOCH: fetch only the pinned commit, verified against its id, then remove git metadata.
RUN set -eu; \
    fetch() { \
      mkdir -p "$1"; git -C "$1" init -q; \
      git -C "$1" fetch --quiet --depth 1 --no-tags "$BASE_REPO" "$2"; \
      got="$(git -C "$1" rev-parse 'FETCH_HEAD^{commit}')"; \
      if [ "$got" != "$2" ]; then echo "fetched $got, expected $2" >&2; exit 1; fi; \
      when="$(git -C "$1" log -1 --format=%ct FETCH_HEAD)"; \
      if [ "$when" != "$3" ]; then echo "commit time $when of $2 is not $3" >&2; exit 1; fi; \
      git -C "$1" -c advice.detachedHead=false checkout -q FETCH_HEAD; \
      rm -rf "$1/.git"; chmod -R a+rX,go-w "$1"; \
    }; \
    fetch /opt/statea "$BASE_REV" "$SOURCE_DATE_EPOCH"; \
    fetch /opt/baselines/2.17.1/src 6d0a98982ec351c165c9224c8b7dbdfca3010e47 1751431594; \
    fetch /opt/baselines/2.18.1/src d87ec67db56518e9e9c36ff38f151dfc9b728b2e 1780401110

# Wheels are fetched in a build-platform stage on the same Debian base.
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS wheels
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

COPY .build/debian.sources /etc/apt/sources.list.d/debian.sources
RUN rm -f /etc/apt/sources.list \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      python3=3.13.5-1 \
      python3.13=3.13.5-2+deb13u5 \
 && rm -rf /var/lib/apt/lists/*

# The installer, checked against its digest, and the hash-locked manifests it installs from.
# (Kaniko ignores ADD --checksum, so the digest is checked by sha256sum.)
ADD https://bootstrap.pypa.io/pip/zipapp/pip-26.3.pyz /opt/pip/pip.pyz
RUN echo "be510f6d23d345eeabdd3eec2599a3ca2df3cbc98c4afbe2631561ca12e744d7  /opt/pip/pip.pyz" \
      | sha256sum --check --strict -
COPY .build/rewardkit.lock .build/meson.lock .build/litellm_model_map.py /opt/pip/

ARG TARGETARCH

# Wheels only, each checked against its manifest hash; nothing is compiled here.
RUN set -eu; \
    target="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$target" in \
      amd64) arch=x86_64 ;; \
      arm64) arch=aarch64 ;; \
      *) echo "no wheel platform for $target" >&2; exit 1 ;; \
    esac; \
    set --; \
    for minor in $(seq 41 -1 17); do set -- "$@" --platform "manylinux_2_${minor}_${arch}"; done; \
    set -- "$@" --platform "manylinux2014_${arch}"; \
    python3 /opt/pip/pip.pyz install --no-cache-dir --disable-pip-version-check --no-deps \
      --require-hashes --only-binary=:all: "$@" \
      --python-version 3.13 --implementation cp --abi cp313 \
      --target /opt/rewardkit/site -r /opt/pip/rewardkit.lock; \
    rm -rf /opt/rewardkit/site/bin; \
    python3 /opt/pip/pip.pyz install --no-cache-dir --disable-pip-version-check --no-deps \
      --require-hashes --only-binary=:all: --target /opt/meson/site -r /opt/pip/meson.lock; \
    rm -rf /opt/meson/site/bin /opt/meson/site/share

# Clear supports_native_structured_output for the judge model in litellm's packaged map so it uses
# a forced tool call instead of the output_format field the API rejects; the build fails if the entry
# is not exactly as expected. The script is a build-context file, not a heredoc (Kaniko drops heredoc
# bodies), and the same step re-checks the result independently.
RUN python3 /opt/pip/litellm_model_map.py \
 && python3 -c 'import json, sys; e = json.load(open("/opt/rewardkit/site/litellm/model_prices_and_context_window_backup.json", encoding="utf-8"))["claude-sonnet-5-5"]; sys.exit(0 if e.get("supports_native_structured_output") is False else "litellm model map not edited")' \
 && echo "litellm model map: edit verified"

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG SOURCE_DATE_EPOCH=1791169496
ARG TARGETARCH

LABEL org.opencontainers.image.source="https://gitlab.freedesktop.org/fontconfig/fontconfig" \
      org.opencontainers.image.revision="c8b9654feda3612f65eb70c4f8db8910d1a7daf1" \
      org.opencontainers.image.licenses="HPND-sell-variant" \
      org.opencontainers.image.base.name="docker.io/library/debian:trixie-slim" \
      org.opencontainers.image.base.digest="sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2"

# Same toolchain as the agent image, plus pytest for the grader.
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
      gcc=4:14.2.0-1 \
      gperf=3.2.1-1 \
      libc6-dev=2.41-12+deb13u4 \
      libexpat1-dev=2.9.0-1~deb13u1 \
      libfreetype-dev=2.13.3+dfsg-1+deb13u1 \
      libjson-c-dev=0.18+ds-1 \
      ninja-build="$ninja_version" \
      pkgconf=1.8.1-4 \
      python3=3.13.5-1 \
      python3.13=3.13.5-2+deb13u5 \
      python3-pytest=8.3.5-2 \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/*.bin /var/log/apt /var/log/dpkg.log \
      /var/log/alternatives.log /var/cache/debconf/*-old /var/lib/dpkg/*-old \
      /var/cache/ldconfig/aux-cache

COPY --from=wheels /opt/rewardkit/site/ /usr/local/lib/python3.13/dist-packages/
COPY --from=wheels /opt/meson/site/ /usr/local/lib/python3.13/dist-packages/
COPY --chown=0:0 .build/rewardkit.sh /usr/local/bin/rewardkit
COPY --chown=0:0 .build/meson.sh /usr/local/bin/meson

# Fail the build unless Reward Kit and meson are the pinned releases and import cleanly. The two
# launchers get their mode here, not from COPY --chmod.
RUN chmod 0755 /usr/local/bin/rewardkit /usr/local/bin/meson \
 && python3 -c 'import importlib.metadata as m, sys; v = m.version("harbor-rewardkit"); sys.exit(0 if v == "0.2.4" else "harbor-rewardkit " + v + " is not the pinned 0.2.4")' \
 && python3 -c 'from rewardkit.__main__ import main' \
 && test "$(meson --version)" = "1.11.2"

ENV PYTHONDONTWRITEBYTECODE=1

# Unprivileged sandbox user that builds and tests the submitted sources (tests/test.sh runs as
# root and drops to it); the pristine tree, the older releases, /tests and /app stay root-owned.
RUN groupadd --gid 20002 verifier \
 && useradd --uid 20002 --gid 20002 --no-log-init --create-home --home-dir /home/verifier \
      --shell /bin/bash verifier \
 && chage --lastday "$(( SOURCE_DATE_EPOCH / 86400 ))" verifier \
 && install -d -o 20002 -g 20002 -m 0755 /work \
 && install -d -m 0755 /app

COPY --from=build /opt/statea /opt/statea
COPY --from=build /opt/baselines /opt/baselines

# The two earlier releases, built out of tree with their tools; the cross-version tests run their
# fc-cache, fc-list and fc-match from these build directories.
RUN set -eu; \
    for tag in 2.17.1 2.18.1; do \
      meson setup /opt/baselines/$tag/build /opt/baselines/$tag/src -Ddoc=disabled -Dnls=disabled \
        -Dtests=disabled -Dtools=enabled -Dcache-build=disabled -Dfontations=disabled \
        -Dxml-backend=expat >/opt/baselines/$tag/setup.log 2>&1 || { tail -n 40 /opt/baselines/$tag/setup.log; exit 1; }; \
      ninja -C /opt/baselines/$tag/build -j4 >/opt/baselines/$tag/build.log 2>&1 || { tail -n 40 /opt/baselines/$tag/build.log; exit 1; }; \
      test -x /opt/baselines/$tag/build/fc-cache/fc-cache; \
      test -x /opt/baselines/$tag/build/fc-list/fc-list; \
      test -x /opt/baselines/$tag/build/fc-match/fc-match; \
    done; \
    chmod -R a+rX,go-w /opt/baselines

COPY --chown=0:0 test.sh test_output.py reward.toml judge.toml cheat-checks.json /tests/
COPY --chown=0:0 reference /tests/reference
COPY --chown=0:0 .verifier /tests/.verifier
COPY --chown=0:0 .cheat /tests/.cheat
COPY --chown=0:0 graded /tests/graded
# Only root (the grader and Reward Kit) can read the private reference note and the rubric;
# tests/test.sh locks them again at the start of every run.
RUN chmod 0755 /tests/test.sh \
 && chmod 0700 /tests/reference \
 && chmod 0600 /tests/reference/diagnosis.md /tests/judge.toml

# Prove, as the sandbox user, that the reference note and the rubric are unreadable and that the
# pristine tree configures out of tree with the grader's options.
USER 20002:20002
RUN set -eu; \
    for f in /tests/reference/diagnosis.md /tests/judge.toml; do \
      if head -c 1 "$f" >/dev/null 2>&1; then echo "$f is readable by the sandbox user" >&2; exit 1; fi; \
    done; \
    mkdir /work/probe; \
    env -C /opt/statea meson setup /work/probe/build /opt/statea -Ddoc=disabled -Dnls=disabled -Dtests=enabled \
      -Dtools=enabled -Dtests-external-fonts=disabled -Dtests-bwrap=disabled \
      -Dcache-build=disabled -Dfontations=disabled -Dxml-backend=expat >/work/probe/setup.log 2>&1 \
      || { tail -n 40 /work/probe/setup.log; exit 1; }; \
    rm -rf /work/probe

WORKDIR /tests
# tests/test.sh runs as root ([verifier] user = "root") and drops to uid 20002 for the build and
# test of the submitted sources.
USER root
