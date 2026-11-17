#!/bin/bash
# Reference solution. Places the reference test file into the collected package directory of
# the task repository, then runs the package's tests offline with the same go test invocation
# as instruction.md and checks that the placed tests reproduce the issue on this checkout: the
# package builds, exactly the reference tests that need the fix fail and every other placed test
# passes; a failure outside the placed file is reported, not enforced. Only *_test.go files and testdata under that directory cross
# into grading, so the placed file is the whole submission.
set -euo pipefail

SOLUTION_DIR="${SOLUTION_DIR:-/solution}"
REPO_DIR="${REPO_DIR:-/app/opentelemetry-collector-contrib}"

GOLD_TEST="stale_associations_test.go"
MODULE_DIR="${REPO_DIR}/processor/k8sattributesprocessor"
PACKAGE_DIR="${MODULE_DIR}/internal/kube"

if [ ! -f "${SOLUTION_DIR}/${GOLD_TEST}" ]; then
  echo "missing ${SOLUTION_DIR}/${GOLD_TEST}" >&2
  exit 1
fi

if [ ! -d "${PACKAGE_DIR}" ]; then
  echo "missing collected package directory ${PACKAGE_DIR}" >&2
  exit 1
fi

install -m 0644 "${SOLUTION_DIR}/${GOLD_TEST}" "${PACKAGE_DIR}/${GOLD_TEST}"
echo "placed ${GOLD_TEST} in ${PACKAGE_DIR}"

# Offline, with the vendored modules and the image's warmed build cache.
export GOPROXY=off GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=vendor GOTELEMETRY=off

# The tests of the placed file that fail on this unfixed checkout and pass once the issue is fixed.
EXPECTED_FAILING="TestReactivatedContainerIDSurvivesSweep
TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain
TestResyncAfterRemovalDoesNotRescueStaleKey
TestStaleAnnotationKeyEvictedAfterGracePeriod
TestStaleLabelChangeThenDeleteLeavesNoKey
TestStaleLabelKeyEvictedAfterGracePeriod
TestStaleLabelValueChangeEvictsOldValue
TestStalePodIPKeysEvictedAfterGracePeriod"

events="$(mktemp)"
trap 'rm -f "${events}"' EXIT

cd "${MODULE_DIR}"
started=$(date +%s)
# go test exits non-zero here by design (the issue is not fixed); the check below decides.
go test -count=1 -json ./internal/kube/... > "${events}" 2>&1 || true
echo "go test -count=1 -json ./internal/kube/... took $(( $(date +%s) - started )) s"

python3 -I -B - "${events}" "${PACKAGE_DIR}/${GOLD_TEST}" "${EXPECTED_FAILING}" <<'CHECK'
import json
import re
import sys

events_path, gold_path, expected_text = sys.argv[1:4]
expected = set(expected_text.split())
with open(gold_path, encoding="utf-8") as handle:
    placed = set(re.findall(r"^func (Test\w+)\(t \*testing\.T\)", handle.read(), re.M))

results, build_failed, other = {}, False, []
with open(events_path, encoding="utf-8", errors="replace") as handle:
    for line in handle:
        try:
            event = json.loads(line)
        except ValueError:
            other.append(line.rstrip())
            continue
        if not isinstance(event, dict):
            continue
        action, name = event.get("Action"), event.get("Test")
        if action == "build-fail" or (action == "output" and "build failed" in (event.get("Output") or "")):
            build_failed = True
        if name and "/" not in name and action in ("pass", "fail", "skip"):
            results[name] = action

problems = []
if build_failed or not results:
    problems.append("the package did not build or ran no test")
    problems.extend(other[-20:])
if not expected <= placed:
    problems.append(f"expected failures not in the placed file: {sorted(expected - placed)}")
missing = sorted(placed - set(results))
if missing:
    problems.append(f"placed tests that did not run: {missing}")
failing = {name for name, action in results.items() if action == "fail"}
if failing & placed != expected:
    problems.append(f"unexpected failures among the placed tests: {sorted((failing & placed) - expected)}; expected failures that did not fail: {sorted(expected - failing)}")
elsewhere = sorted(failing - placed)
if elsewhere:
    print(f"note: tests outside the placed file also failed in this run: {elsewhere}", file=sys.stderr)

passed = sum(1 for action in results.values() if action == "pass")
print(f"tests run: {len(results)}; passed: {passed}; failed: {len(failing)} "
      f"(the {len(expected)} reference tests that need the fix); placed: {len(placed)}")
if problems:
    print("reference tests do not reproduce the issue as expected:", file=sys.stderr)
    for problem in problems:
        print("  " + problem, file=sys.stderr)
    sys.exit(1)
print("the placed tests build offline and reproduce the issue on the unfixed checkout")
CHECK
