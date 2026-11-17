# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Verifier image: the pristine openraft tree under /opt/statea with the environment image's Cargo.lock,
# toolchain and vendored crates, the graded test sources, and pinned Reward Kit 0.2.4.

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

# Same lock file and vendored crates as the environment image.
COPY .build/Cargo.lock /opt/statea/Cargo.lock
RUN set -eu; \
    echo "$LOCK_SHA256  /opt/statea/Cargo.lock" | sha256sum -c -; \
    cp -a /opt/statea /tmp/vend; rm -f /tmp/vend/rust-toolchain; \
    env -C /tmp/vend PWD=/tmp/vend OLDPWD=/ CARGO_HOME=/tmp/cargo-home cargo vendor --locked /opt/cargo-vendor >/dev/null; \
    cmp /tmp/vend/Cargo.lock /opt/statea/Cargo.lock; \
    chmod -R a+rX,go-w /opt/cargo-vendor /opt/statea; \
    rm -rf /tmp/vend /tmp/cargo-home

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

# Reward Kit's installer and the hash-locked manifest it installs from. The installer is checked
# against its digest by sha256sum below (Kaniko ignores ADD --checksum).
ADD https://bootstrap.pypa.io/pip/zipapp/pip-26.3.pyz /opt/pip/pip.pyz
COPY .build/rewardkit.lock /opt/pip/rewardkit.lock

ARG TARGETARCH

# Wheels only, each checked against the manifest hashes; nothing is compiled here. TARGETARCH is
# unset under Kaniko, which builds for its own platform, so dpkg's architecture stands in.
RUN set -eu; \
    printf '%s  %s\n' be510f6d23d345eeabdd3eec2599a3ca2df3cbc98c4afbe2631561ca12e744d7 /opt/pip/pip.pyz > /tmp/pip.sha256; \
    sha256sum -c /tmp/pip.sha256; \
    rm -f /tmp/pip.sha256; \
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
    rm -rf /opt/rewardkit/site/bin

# Clear supports_native_structured_output for the judge model in litellm's packaged map so it uses
# a forced tool call instead of the output_format field the API rejects; the build fails if the entry
# is not exactly as expected. A script file, not a heredoc: Kaniko drops heredoc bodies.
COPY .build/litellm-map.py /opt/pip/litellm-map.py
RUN python3 /opt/pip/litellm-map.py

FROM rust:1.99.0-slim-trixie@sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581 AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG SOURCE_DATE_EPOCH=1793326612
ARG TOOLCHAIN=nightly-2026-03-01

LABEL org.opencontainers.image.source="https://github.com/databendlabs/openraft" \
      org.opencontainers.image.revision="3c0144e34d156ed1f8e7c6f6940ef1dbb063cfb3" \
      org.opencontainers.image.licenses="MIT OR Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/rust:1.99.0-slim-trixie" \
      org.opencontainers.image.base.digest="sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581"

COPY .build/debian.sources /etc/apt/sources.list.d/debian.sources
RUN rm -f /etc/apt/sources.list \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      python3=3.13.5-1 \
      python3.13=3.13.5-2+deb13u5 \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/*.bin /var/log/apt /var/log/dpkg.log \
      /var/log/alternatives.log /var/cache/debconf/*-old /var/lib/dpkg/*-old \
      /var/cache/ldconfig/aux-cache

# Install the pinned nightly; components are checked against the dist manifest.
RUN rustup toolchain install "$TOOLCHAIN" --profile minimal \
 && rustup default "$TOOLCHAIN" \
 && rm -rf /usr/local/rustup/downloads /usr/local/rustup/tmp \
 && chmod -R a+rX,go-w /usr/local/rustup /usr/local/cargo

# Sandbox user (uid 20002): tests/test.sh runs as root and runs every cargo build and test run of
# the submission as this uid. The pristine tree, the vendored crates, the grader under /tests and
# /work and /app stay root-owned.
RUN groupadd --gid 20002 verifier \
 && useradd --uid 20002 --gid 20002 --no-log-init --create-home --home-dir /home/verifier \
      --shell /bin/bash verifier \
 && chage --lastday "$(( SOURCE_DATE_EPOCH / 86400 ))" verifier \
 && install -d -m 0755 /work /app

COPY --from=build /opt/statea /opt/statea
COPY --from=build /opt/cargo-vendor /opt/cargo-vendor
COPY --from=wheels /opt/rewardkit/site/ /usr/local/lib/python3.13/dist-packages/
COPY --chown=0:0 .build/rewardkit.sh /usr/local/bin/rewardkit

# Offline cargo with a sandbox-owned CARGO_HOME. Fail the build unless Reward Kit and the toolchain
# are the pinned releases; the toolchain is also put on the login-shell PATH.
RUN chmod 0755 /usr/local/bin/rewardkit \
 && printf '%s\n' \
      '[source.crates-io]' \
      'replace-with = "vendored-sources"' \
      '' \
      '[source.vendored-sources]' \
      'directory = "/opt/cargo-vendor"' \
      '' \
      '[net]' \
      'offline = true' \
      > /tmp/config.toml \
 && install -d -o 20002 -g 20002 -m 0755 /home/verifier/.cargo \
 && install -o 20002 -g 20002 -m 0644 /tmp/config.toml /home/verifier/.cargo/config.toml \
 && rm /tmp/config.toml \
 && python3 -c 'import importlib.metadata as m, sys; v = m.version("harbor-rewardkit"); sys.exit(0 if v == "0.2.4" else "harbor-rewardkit " + v + " is not the pinned 0.2.4")' \
 && python3 -c 'from rewardkit.__main__ import main' \
 && python3 -c 'import json, sys; m = json.load(open("/usr/local/lib/python3.13/dist-packages/litellm/model_prices_and_context_window_backup.json")); sys.exit(0 if m["claude-sonnet-5-5"]["supports_native_structured_output"] is False else "litellm map fix missing")' \
 && rustc -V | grep -q '^rustc 1\.96\.0-nightly (38c0de8dc 2026-02-28)$' \
 && cargo -V | grep -q '^cargo 1\.96\.0-nightly (f298b8c82 2026-02-24)$' \
 && printf '%s\n' \
      'export CARGO_HOME=/home/verifier/.cargo' \
      'export RUSTUP_HOME=/usr/local/rustup' \
      "export PATH=\"/usr/local/cargo/bin:\$PATH\"" \
      > /etc/profile.d/rust-toolchain.sh \
 && chmod 0644 /etc/profile.d/rust-toolchain.sh

ENV PYTHONDONTWRITEBYTECODE=1 \
    CARGO_HOME=/home/verifier/.cargo

# The grader is root-owned; test.sh is made executable here rather than with COPY --chmod.
COPY --chown=0:0 test.sh /tests/test.sh
COPY --chown=0:0 test_output.py reward.toml cheat-checks.json /tests/
COPY --chown=0:0 .verifier /tests/.verifier
COPY --chown=0:0 .cheat /tests/.cheat
COPY --chown=0:0 graded /tests/graded
RUN chmod 0755 /tests/test.sh

# Prove the offline build of the pristine tree with the graded test sources in place, as the
# sandbox uid that builds the submission.
USER 20002:20002
RUN set -eu; cp -a /opt/statea /tmp/probe; \
    python3 -c 'import json, shutil; [shutil.copyfile("/tests/graded/" + f["name"], "/tmp/probe/" + f["path"]) for f in json.load(open("/tests/graded/manifest.json"))["test_files"]]'; \
    env -C /tmp/probe PWD=/tmp/probe OLDPWD=/ CARGO_TARGET_DIR=/tmp/probe-target cargo test --locked -p openraft -p tests --tests --no-run 2>&1 | tail -n 3; \
    rm -rf /tmp/probe /tmp/probe-target

# The judge criteria and private reference note are baked into the image; only root (the grader)
# can read them.
USER root
COPY --chown=0:0 judge.toml /tests/judge.toml
COPY --chown=0:0 reference /tests/reference
RUN chmod 0700 /tests/reference \
 && chmod 0600 /tests/reference/diagnosis.md /tests/judge.toml

WORKDIR /tests
