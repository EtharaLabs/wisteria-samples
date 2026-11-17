# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b
# check=error=true

ARG PYTHON_BASE=python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f
ARG GO_VERSION=1.25.14

# Prewarm and grading must use this same path: without -trimpath the go command keys build actions
# by absolute source directory. grade.py refuses to grade on a mismatch.
ARG GRADE_MODULE_DIR=/work/grade/module
ARG GO_SHA256_AMD64=a21ae5633a269bcd7e90cf767e48225633795e99d831742cbf3397064fee7712
ARG GO_SHA256_ARM64=9bf234ea70ffec9347fdf6b22ce4add51717d3386a38a441e8c8743fceb5eaee

# Stage 1: fetch the target platform's Go toolchain, one sha256 per architecture.
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

# Stage 2: hash-locked Python closure for the target platform.
FROM python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f AS pybuild
SHELL ["/bin/bash", "-o", "pipefail", "-euc"]
COPY requirements.txt /build/requirements.txt
# No RUN --mount cache (a BuildKit feature not proven on the build platform); pip's cache is dropped instead.
RUN python -m venv /opt/venv; \
    /opt/venv/bin/pip install --no-cache-dir --require-hashes --no-deps -r /build/requirements.txt

# The API rejects litellm's native structured-output request for claude-sonnet-5-5, so clear that flag in the
# local model map (read because LITELLM_LOCAL_MODEL_COST_MAP=True); the build fails if the entry is unexpected.
# No heredoc (Kaniko drops heredoc bodies); the trailing -c check fails the build if the edit did not happen.
RUN printf '%s\n' \
      'import glob, json' \
      'paths = glob.glob("/opt/venv/lib/python3*/site-packages/litellm/model_prices_and_context_window_backup.json")' \
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
    | /opt/venv/bin/python -; \
    /opt/venv/bin/python -c 'import glob, json; p = glob.glob("/opt/venv/lib/python3*/site-packages/litellm/model_prices_and_context_window_backup.json"); assert len(p) == 1, p; v = json.load(open(p[0], encoding="utf-8"))["claude-sonnet-5-5"].get("supports_native_structured_output"); assert v is False, v; print("litellm model map check: claude-sonnet-5-5 supports_native_structured_output is", v)'

# Stage 3: warm the build cache at GRADE_MODULE_DIR, then seal it root-owned read-only.
# GOFLAGS, CGO_ENABLED and GOOS/GOARCH must match the runtime stage.
FROM python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f AS prewarm
SHELL ["/bin/bash", "-o", "pipefail", "-euc"]
COPY --from=fetch /opt/go /usr/local/go
ARG GRADE_MODULE_DIR
ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
    GOFLAGS=-mod=vendor \
    GOPROXY=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    CGO_ENABLED=0 \
    GOCACHE=/opt/gocache \
    GOMODCACHE=/prewarm/gomodcache \
    GOPATH=/prewarm/gopath \
    GRADE_MODULE_DIR=${GRADE_MODULE_DIR} \
    LC_ALL=C \
    TZ=UTC
COPY base-module.tar.gz /prewarm/base-module.tar.gz
COPY candidates /prewarm/candidates
COPY tools/prewarm.py /prewarm/prewarm.py
RUN python3 /prewarm/prewarm.py

# Stage 4: compile the trusted parser.
FROM python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f AS discovery
SHELL ["/bin/bash", "-o", "pipefail", "-euc"]
COPY --from=fetch /opt/go /usr/local/go
COPY tools/discover.go /build/discover.go
RUN GOCACHE=/tmp/discovery-cache GOFLAGS='' CGO_ENABLED=0 /usr/local/go/bin/go build -trimpath -o /opt/go-test-discover /build/discover.go; \
    rm -rf /tmp/discovery-cache

# Stage 5: runtime.
FROM python@sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-euc"]
ARG GRADE_MODULE_DIR

LABEL org.opencontainers.image.title="k8sattributes kube test-generation verifier" \
      org.opencontainers.image.description="Offline differential grader and Reward Kit criteria for the collected Go test submission, with an advisory judge of weight 0." \
      org.opencontainers.image.source="https://github.com/open-telemetry/opentelemetry-collector-contrib" \
      org.opencontainers.image.revision="bd71eee5197f6cf5869d9958ea9ff67782296a69" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="docker.io/library/python:3.12-trixie" \
      org.opencontainers.image.base.digest="sha256:17b9724493d916f56565c131f389faff2597f46dec6ce09516f95125bc9f519f"

# The entry point runs as root; uid 20002 (gotest) runs every go test child and owns only its home
# and its per-run scratch and build-cache snapshot directories, so submitted test code cannot write
# the verdict or a sealed candidate tree.
RUN groupadd --gid 20002 gotest; \
    useradd --uid 20002 --gid 20002 --no-log-init --create-home --home-dir /home/gotest gotest; \
    mkdir -p /work /work/grade/out /logs/verifier; \
    chown 0:0 /work /work/grade /work/grade/out /logs/verifier; \
    chmod 0755 /work /work/grade /logs/verifier; \
    chmod 0700 /work/grade/out

COPY --from=fetch --chown=0:0 /opt/go /usr/local/go
COPY --from=pybuild --chown=0:0 /opt/venv /opt/venv
COPY --from=discovery --chown=0:0 /opt/go-test-discover /usr/local/bin/go-test-discover
# Read-only; grade.py gives each candidate run its own hardlinked snapshot.
COPY --from=prewarm --chown=0:0 /opt/gocache /opt/gocache

COPY --chown=0:0 base-module.tar.gz /tests/base-module.tar.gz
COPY --chown=0:0 candidates /tests/candidates
COPY --chown=0:0 grade.py /tests/grade.py
COPY --chown=0:0 outcomes.py /tests/outcomes.py
COPY --chown=0:0 isolation.py /tests/isolation.py
COPY --chown=0:0 test_output.py /tests/test_output.py
COPY --chown=0:0 reward.toml /tests/reward.toml
COPY --chown=0:0 cheat-checks.json /tests/cheat-checks.json
COPY --chown=0:0 requirements.txt /tests/requirements.txt
COPY --chown=0:0 test.sh /tests/test.sh
# The advisory judge rubric lives outside /tests so the scored Reward Kit run never discovers it.
COPY --chown=0:0 judge.toml /opt/judge/judge.toml
RUN chmod 0555 /tests/test.sh; chmod 0700 /tests /opt/gocache /opt/judge; chmod 0400 /opt/judge/judge.toml

ENV PATH=/opt/venv/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
    PYTHONPATH=/tests \
    PYTHON=/opt/venv/bin/python \
    REWARDKIT=/opt/venv/bin/rewardkit \
    GRADE_TESTS_ROOT=/tests \
    GRADE_OUT_DIR=/work/grade/out \
    GRADE_REWARD_DIR=/logs/verifier \
    GRADE_ARTIFACTS_ROOT=/logs/artifacts \
    GRADE_SCRATCH_PARENT=/work \
    GRADE_GO=/usr/local/go/bin/go \
    GRADE_DISCOVER=/usr/local/bin/go-test-discover \
    GRADE_GOCACHE=/opt/gocache \
    GRADE_MODULE_DIR=${GRADE_MODULE_DIR} \
    GRADE_SUBMISSION_DIR=/app/opentelemetry-collector-contrib/processor/k8sattributesprocessor/internal/kube \
    GRADE_TEST_UID=20002 \
    GRADE_TEST_GID=20002 \
    GRADE_RUN_TIMEOUT_SEC=120 \
    LITELLM_LOCAL_MODEL_COST_MAP=True \
    LITELLM_TELEMETRY=False \
    LITELLM_MODE=PRODUCTION \
    HF_HUB_OFFLINE=1 \
    TRANSFORMERS_OFFLINE=1 \
    DO_NOT_TRACK=1 \
    NO_PROXY=* \
    GOFLAGS=-mod=vendor \
    GOPROXY=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    CGO_ENABLED=0 \
    PYTHONDONTWRITEBYTECODE=1 \
    LC_ALL=C \
    TZ=UTC

WORKDIR /work
USER root
# No ENTRYPOINT: Harbor runs the service with sh -c and execs each verifier step into it.
CMD ["/bin/bash"]
