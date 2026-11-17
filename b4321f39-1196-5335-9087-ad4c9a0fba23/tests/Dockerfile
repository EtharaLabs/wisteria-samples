# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Verifier image. Harbor runs it as a separate container: the agent never sees these bytes, and the
# agent's /app/server/stree is copied in as an artifact before /tests/test.sh runs. test.sh copies the
# submitted test files for the judge, then runs Reward Kit over /tests: the one criterion in
# /tests/test_output.py runs the pool grader and scores its matrix, and /tests/judge.toml runs the
# judge (anthropic/claude-sonnet-5-5) against the private reference /tests/reference/. test.sh
# writes the exact reward, the compiled score alone, to the carrier; the judge is advisory (weight 0)
# and its result is published beside the reward.
#
# Identities: test.sh, Reward Kit, the grader and the judge run as root (task.toml
# [verifier].user). The grader compiles each pool member as root and runs every submitted test
# binary as uid 10002 with no supplementary groups; uid 10002 owns nothing here and can read or
# execute nothing under /tests or /opt/grader. uid 10001 (verifier) owns the grader's state, build
# cache and judge-input directories and is the group of /tests and /opt/grader; it runs only the
# build-time warm-up below. No file capabilities are set; the image ends on USER root.
#
# Build stages run on the build platform and produce nothing architecture-specific except the grader,
# which they cross-compile, and Reward Kit's wheels, which they select for the target platform by
# their hash-locked manifest. The race build cache is warmed in the runtime stage, for the target
# platform, because -race needs cgo; it compiles and never runs the test binary.

# Pristine module at the first parent of the merge of nats-server pull request 8765 (the agent
# image's tree): go.mod, go.sum, LICENSE and the packages server/stree needs, with the module
# cache; plus the package's own tests for the warm-up.
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS fetch
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG BASE_REPO=https://github.com/nats-io/nats-server.git
ARG BASE_REV=6619d7f7d7211f37479d13a8a8cd104a6acbaed1
ARG SOURCE_DATE_EPOCH=1793366398
ARG TREE_SHA256=03cca4de0f0d77c004c81df9abfcd705604221860b70a8ff812113555e865ec2

WORKDIR /src/ns
RUN set -eu; \
    go version | grep -q 'go1.26.10 '; \
    git init -q; \
    git fetch --quiet --depth 1 --no-tags "$BASE_REPO" "$BASE_REV"; \
    got="$(git rev-parse 'FETCH_HEAD^{commit}')"; \
    if [ "$got" != "$BASE_REV" ]; then echo "fetched $got, expected $BASE_REV" >&2; exit 1; fi; \
    when="$(git log -1 --format=%ct FETCH_HEAD)"; \
    if [ "$when" != "$SOURCE_DATE_EPOCH" ]; then echo "commit time $when is not $SOURCE_DATE_EPOCH" >&2; exit 1; fi; \
    git -c advice.detachedHead=false checkout -q FETCH_HEAD; \
    rm -rf .git; \
    sum="$(find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"; \
    if [ "$sum" != "$TREE_SHA256" ]; then echo "tree digest $sum is not $TREE_SHA256" >&2; exit 1; fi; \
    b=/opt/grader/base; \
    mkdir -p $b/internal $b/server/gsl $b/server/stree /opt/warm-tests; \
    cp go.mod go.sum LICENSE $b/; \
    cp -r internal/antithesis $b/internal/; \
    cp server/gsl/gsl.go $b/server/gsl/; \
    cp server/stree/*.go $b/server/stree/; \
    rm $b/server/stree/*_test.go; \
    cp server/stree/*_test.go /opt/warm-tests/; \
    env -C "$b" GOTOOLCHAIN=local GOFLAGS=-mod=mod GOTELEMETRY=off GOCACHE=/tmp/gocache go mod download; \
    rm -rf /src/ns /tmp/gocache

# The grader is pure Go with no dependencies, so it is cross-compiled here for the target.
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS grader-build
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY grader/ ./
RUN CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-$(dpkg --print-architecture)}" GOFLAGS='' GOPROXY=off GOTOOLCHAIN=local \
      GOTELEMETRY=off GOCACHE=/tmp/gocache go build -trimpath -o /out/grader . \
 && rm -rf /tmp/gocache

# Reward Kit (harbor-rewardkit 0.2.4, the release Harbor v0.25.0 names) and its dependencies, as
# wheels for the target platform, each checked against the hash-locked manifest. Only wheels for
# glibc 2.36 or older are accepted, the C library of the runtime stage.
FROM --platform=$BUILDPLATFORM python:3.13-slim-bookworm@sha256:a5a7017efc5969e00ae2b91c8eb0631049a569652aa4ffc4501769049ae45c72 AS wheels
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ARG TARGETARCH
COPY .build/rewardkit.lock /opt/pip/rewardkit.lock
RUN set -eu; \
    case "${TARGETARCH:-$(dpkg --print-architecture)}" in \
      amd64) arch=x86_64 ;; \
      arm64) arch=aarch64 ;; \
      *) echo "no wheel platform for $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    platforms=""; \
    for minor in $(seq 36 -1 17); do platforms="$platforms manylinux_2_${minor}_${arch}"; done; \
    export PIP_PLATFORM="$platforms manylinux2014_${arch}"; \
    python3 -m pip install --no-cache-dir --disable-pip-version-check --no-deps --no-compile \
      --require-hashes --only-binary=:all: \
      --python-version=3.13 --implementation=cp --abi=cp313 \
      --target=/opt/rewardkit/site -r /opt/pip/rewardkit.lock; \
    rm -rf /opt/rewardkit/site/bin

# Judge compatibility fix, as in the repository's other judged tasks with this litellm release:
# litellm 1.108.0 sends the deprecated output_format field for claude-sonnet-5-5 when its packaged
# model map marks the model supports_native_structured_output, and the API answers HTTP 400; with
# the flag false it uses a forced tool call. LITELLM_LOCAL_MODEL_COST_MAP=True makes litellm read
# only this packaged map.
# A build-context script, not a heredoc: Kaniko drops heredoc bodies.
COPY .build/litellm_model_map.py /opt/pip/litellm_model_map.py
RUN python3 /opt/pip/litellm_model_map.py

# CPython 3.13 for the target platform (Reward Kit needs Python 3.12 or later; the Go image's Debian
# Python is 3.11). Built on the same Debian release as the runtime stage, so it links the same libraries.
FROM python:3.13-slim-bookworm@sha256:a5a7017efc5969e00ae2b91c8eb0631049a569652aa4ffc4501769049ae45c72 AS python

FROM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

LABEL org.opencontainers.image.title="nats-server server/stree test-generation verifier" \
      org.opencontainers.image.description="Offline pool grader, Reward Kit criterion and judge for the collected server/stree test submission." \
      org.opencontainers.image.source="https://github.com/nats-io/nats-server" \
      org.opencontainers.image.revision="6619d7f7d7211f37479d13a8a8cd104a6acbaed1" \
      org.opencontainers.image.version="2.1.2" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/golang:1.26.10-bookworm" \
      org.opencontainers.image.base.digest="sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb"

ENV GOTOOLCHAIN=local CGO_ENABLED=1 GOFLAGS=-mod=mod GOCACHE=/var/cache/grader-go GOPROXY=off \
    GOTELEMETRY=off PYTHONDONTWRITEBYTECODE=1 LITELLM_LOCAL_MODEL_COST_MAP=True

COPY --from=python /usr/local/bin/python3.13 /usr/local/bin/python3.13
COPY --from=python /usr/local/lib/libpython3.13.so.1.0 /usr/local/lib/libpython3.13.so.1.0
COPY --from=python /usr/local/lib/python3.13 /usr/local/lib/python3.13
COPY --from=wheels /opt/rewardkit/site/ /usr/local/lib/python3.13/site-packages/
COPY .build/rewardkit.sh /usr/local/bin/rewardkit
RUN chmod 0755 /usr/local/bin/rewardkit \
 && ldconfig \
 && ln -s python3.13 /usr/local/bin/python3 \
 && python3 -c 'import sys; assert sys.version_info[:2] == (3, 13), sys.version' \
 && python3 -c 'import ssl, sqlite3, ctypes, lzma, bz2, zlib, json, tomllib, asyncio' \
 && python3 -c 'import importlib.metadata as m, sys; v = m.version("harbor-rewardkit"); sys.exit(0 if v == "0.2.4" else "harbor-rewardkit " + v + " is not the pinned 0.2.4")' \
 && python3 -c 'from rewardkit.__main__ import main' \
 && go version | grep -q 'go1.26.10 '

# 10001 owns the grader's directories and warms the build cache at build time; at run time root runs
# the tests, the grader and the judge, and 10002 runs submitted test code and owns nothing here.
# Shared scratch space is closed to everyone but root and 10001 (the grader, as root, makes /tmp and
# /var/tmp root's alone before any submitted code runs), the base image's world-writable GOPATH
# directories are closed, and the base image's setuid and setgid bits are removed, so uid 10002 can
# write only where the grader gives it a directory and can gain no other identity.
RUN groupadd --gid 10001 verifier \
 && useradd --uid 10001 --gid 10001 --no-log-init --create-home --home-dir /home/verifier \
      --shell /usr/sbin/nologin verifier \
 && useradd --system --uid 10002 --user-group --no-create-home --shell /usr/sbin/nologin grader-run \
 && chown root:10001 /tmp /var/tmp && chmod 1770 /tmp /var/tmp \
 && chmod 0755 /go /go/bin /go/src /run/lock \
 && find / -xdev -type f -perm /6000 -exec chmod ug-s {} + \
 && install -d -o 10001 -g 10001 -m 0700 /var/cache/grader-go /var/lib/grader \
 && install -d -o 10001 -g 10001 -m 0711 /var/lib/grader-run \
 && install -d -o 10001 -g 10001 -m 0700 /var/lib/grader-judge

COPY --from=fetch /go/pkg/mod /go/pkg/mod
COPY --from=fetch --chown=0:10001 /opt/grader/base /opt/grader/base
COPY --from=fetch --chown=10001:10001 /opt/warm-tests /tmp/warm-tests
# The package's unchanged test files: test.sh leaves byte-identical copies out of the judge's input.
COPY --from=fetch --chown=0:10001 /opt/warm-tests /opt/grader/pristine-tests

# Warm the race build cache once, as uid 10001, on the target platform. The grader compiles as root
# with the same GOCACHE (it keeps GOCACHE in the compile environment), which root can read and extend.
USER 10001:10001
RUN set -eu; \
    mkdir /tmp/warm; cp -r /opt/grader/base/. /tmp/warm/; cp /tmp/warm-tests/*_test.go /tmp/warm/server/stree/; \
    env -C /tmp/warm go test -c -race -vet=off -o /tmp/warm/stree.test ./server/stree; \
    rm -rf /tmp/warm /tmp/warm-tests
USER 0:0

COPY --chown=0:10001 pool/ /opt/grader/pool/
COPY --from=grader-build --chown=0:10001 /out/grader /opt/grader/bin/grader
COPY --chown=0:10001 test.sh test_output.py reward.toml reward.deterministic.toml judge.toml cheat-checks.json /tests/
COPY --chown=0:10001 reference/ /tests/reference/

# Only root and group 10001 read the grader, the pool, the pristine module, the tests and the
# judge's rubric and reference; the runner uid 10002 is in neither.
RUN set -eu; \
    chown -R 0:10001 /opt/grader /tests; \
    chmod -R u=rwX,g=rX,o= /opt/grader /tests; \
    chmod 0750 /tests/test.sh /opt/grader/bin/grader

WORKDIR /app
USER root
CMD ["bash"]
