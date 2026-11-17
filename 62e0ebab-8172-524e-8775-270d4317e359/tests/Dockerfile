# syntax=docker/dockerfile:1.20@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d
# Verifier image: grader files, the project's own test corpus, the replay binary and warm Go caches.
# Nothing is fetched at grading time; only the judge hosts allowed in task.toml are reachable.

ARG SOURCE_DATE_EPOCH=1792960721
ARG DEBIAN_SNAPSHOT=20261025T000000Z

# ---- Stage 1: cross-build the replay binary for the target platform ----
FROM --platform=$BUILDPLATFORM golang:1.26.10@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS replay

ARG TARGETOS
ARG TARGETARCH
ENV GOTOOLCHAIN=local GOFLAGS=-mod=mod

WORKDIR /build/workbench
COPY workbench-go/ /build/workbench/
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN set -e; \
    go mod download; \
    gofmt -l . | tee /build/gofmt.txt; \
    test ! -s /build/gofmt.txt; \
    go vet ./... 2>&1 | tail -5; \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -o /build/replay ./cmd/replay
SHELL ["/bin/sh", "-c"]

# ---- Stage 2: warm the module and build caches the staged submission needs ----
FROM --platform=$BUILDPLATFORM golang:1.26.10@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS warm

ARG TARGETOS
ARG TARGETARCH
ENV GOTOOLCHAIN=local \
    GOFLAGS=-mod=mod \
    GOMODCACHE=/warm/mod \
    GOCACHE=/warm/build

WORKDIR /warm/src
COPY nats-server/ /warm/src/
COPY workbench-go/go.mod workbench-go/go.sum /warm/wb/
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN set -e; \
    go mod download; \
    go -C /warm/wb mod download; \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -o /warm/discard/ ./... 2>&1 | tail -5; \
    rm -rf /warm/discard
SHELL ["/bin/sh", "-c"]

# ---- Stage 3: verifier, resolved for the target platform from the same digest ----
FROM golang:1.26.10@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS verifier

ARG SOURCE_DATE_EPOCH=1792960721
ARG DEBIAN_SNAPSHOT

LABEL org.opencontainers.image.title="ethara/62e0ebab-8172-524e-8775-270d4317e359 verifier" \
      org.opencontainers.image.description="Reward Kit verifier for one reliability-engineering task: compiled checks and a judged rubric." \
      org.opencontainers.image.source="https://github.com/nats-io/nats-server" \
      org.opencontainers.image.revision="011cda33cec116deb81bd0a79ad5308e993f6b62" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/golang:1.26.10" \
      org.opencontainers.image.base.digest="sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c" \
      org.opencontainers.image.created="2026-10-25T20:38:41Z"

# Python 3.13 venv module from a dated Debian snapshot, pinned to that snapshot's version.
RUN set -e; \
    printf 'Types: deb\nURIs: https://snapshot.debian.org/archive/debian/%s\nSuites: trixie\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' "$DEBIAN_SNAPSHOT" > /etc/apt/sources.list.d/debian.sources; \
    apt-get -o Acquire::Check-Valid-Until=false -o Acquire::Retries=5 update; \
    apt-get -o Acquire::Check-Valid-Until=false -o Acquire::Retries=5 install -y --no-install-recommends \
        python3.13=3.13.5-2+deb13u5 \
        python3.13-venv=3.13.5-2+deb13u5 \
        ca-certificates=20250419+deb13u1; \
    rm -rf /var/lib/apt/lists/*

# Reward Kit and its dependencies, hash-locked; the build fails if the rewardkit executable does not resolve.
COPY --chown=0:0 requirements.txt /opt/pins/requirements.txt
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN set -e; \
    python3.13 -m venv /opt/rewardkit; \
    /opt/rewardkit/bin/pip install --no-cache-dir --no-deps --require-hashes \
        -r /opt/pins/requirements.txt 2>&1 | tail -3; \
    /opt/rewardkit/bin/python -c "import rewardkit, sys; sys.stdout.write(rewardkit.__name__)"; \
    ln -s /opt/rewardkit/bin/rewardkit /usr/local/bin/rewardkit; \
    test -x /usr/local/bin/rewardkit

# The API rejects litellm's native structured-output request for claude-sonnet-5-5, so clear that flag in the
# local model map (read because LITELLM_LOCAL_MODEL_COST_MAP=True); the build fails if the entry is unexpected.
# The script is piped in line by line rather than as a heredoc, which Kaniko drops; the second command fails the
# build unless the flag really is false afterwards.
RUN set -e; \
    printf '%s\n' \
        'import glob, json' \
        'paths = glob.glob("/opt/rewardkit/lib/python3*/site-packages/litellm/model_prices_and_context_window_backup.json")' \
        'assert len(paths) == 1, paths' \
        'path = paths[0]' \
        'with open(path, encoding="utf-8") as f:' \
        '    data = json.load(f)' \
        'entry = data["claude-sonnet-5-5"]' \
        'assert entry.get("supports_native_structured_output") is True, entry.get("supports_native_structured_output")' \
        'entry["supports_native_structured_output"] = False' \
        'with open(path, "w", encoding="utf-8") as f:' \
        '    json.dump(data, f, indent=4)' \
        '    f.write("\n")' \
        'with open(path, encoding="utf-8") as f:' \
        '    assert json.load(f)["claude-sonnet-5-5"]["supports_native_structured_output"] is False' \
        'print("litellm model map: claude-sonnet-5-5 supports_native_structured_output set to false")' \
    | /opt/rewardkit/bin/python -; \
    /opt/rewardkit/bin/python -c 'import glob, json, sys; (path,) = glob.glob("/opt/rewardkit/lib/python3*/site-packages/litellm/model_prices_and_context_window_backup.json"); flag = json.load(open(path, encoding="utf-8"))["claude-sonnet-5-5"]["supports_native_structured_output"]; assert flag is False, flag; sys.stdout.write("litellm model map check: supports_native_structured_output is False\n")'
SHELL ["/bin/sh", "-c"]

COPY --chown=0:0 pristine/ /opt/pristine/
COPY --from=replay /build/replay /opt/replay/replay
COPY --from=warm /warm/mod/ /opt/gocache/mod/
COPY --from=warm /warm/build/ /opt/gocache/build/

# The verifier's own tests tree, which the harness preflight requires at /tests. Root-owned explicitly (Docker's
# default; Kaniko keeps the build context's owner): the submission's sandbox uid must not own a grader file.
COPY --chown=0:0 test.sh test_output.py report.py reward.toml judge.toml cheat-checks.json requirements.txt /tests/
COPY --chown=0:0 reference/ /tests/reference/
COPY --chown=0:0 .gate/ /tests/.gate/

ENV GOFLAGS=-mod=mod \
    GOPROXY=off \
    GOTOOLCHAIN=local \
    GOMODCACHE=/opt/gocache/mod \
    GOCACHE=/opt/gocache/build \
    PRISTINE=/opt/pristine \
    REPLAY=/opt/replay/replay \
    PATH=/opt/rewardkit/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin

WORKDIR /opt/verifier

# tests/test.sh runs as root and writes /logs/verifier (root-owned, 0755). It stages, builds, tests and runs
# the submitted tree as the sandbox uid 40000, whose only writable places are the scratch root the staged
# submission is built in and the Go caches; it cannot write /logs/verifier or the grader's files (root-owned,
# not group- or world-writable); the reference note, the gate and the rubric (judge.toml) are root-only,
# read by the judge as root.
# setpriv (util-linux) drops to that uid. Nothing is setuid.
RUN set -e; \
    command -v setpriv; \
    /usr/sbin/groupadd --gid 40000 sandbox; \
    /usr/sbin/useradd --uid 40000 --gid 40000 --no-create-home --no-log-init --home-dir /nonexistent --shell /usr/sbin/nologin sandbox; \
    mkdir -p /opt/verifier /opt/scratch /logs/verifier; \
    chmod 0755 /tests /logs /logs/verifier; \
    chmod 0700 /tests/reference /tests/.gate; \
    chmod 0600 /tests/judge.toml; \
    chown -R 40000:40000 /opt/scratch /opt/gocache; \
    find /usr/bin /usr/sbin /usr/lib -xdev -perm /6000 -type f -print -delete; \
    find /opt /tests -newermt "@${SOURCE_DATE_EPOCH}" -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

USER root
