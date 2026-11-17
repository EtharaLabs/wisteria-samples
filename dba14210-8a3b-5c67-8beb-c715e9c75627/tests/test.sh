#!/bin/bash
# Verifier entry point. Fail-closed: reward.json starts as {"reward": 0} and is otherwise written
# only by Reward Kit. The reward is the compiled score alone; the advisory judge (weight 0) runs
# afterwards when a key is present and writes only judged.json, or judge-skipped.json and
# judge_unavailable when it gives no usable result.
set -o pipefail
set -u

# Judge model. judge.toml names an Anthropic model. With no Anthropic key but an OpenAI key, Reward Kit
# judges the same rubric with an OpenAI model instead (REWARDKIT_JUDGE); with neither key the judge is
# not called. No judge mode changes the reward.
OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"; export OPENAI_BASE_URL
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -n "${OPENAI_API_KEY:-}" ]; then
  export REWARDKIT_JUDGE=openai/gpt-6-astra
else
  export REWARDKIT_JUDGE=
fi

# REWARD_DIR may start writable; it is locked before any submitted code runs.
VERIFIER_OUT="${GRADE_OUT_DIR:-/work/grade/out}"
REWARD_DIR="${GRADE_REWARD_DIR:-/logs/verifier}"
TESTS_ROOT="${GRADE_TESTS_ROOT:-/tests}"
REWARD_JSON="${REWARD_DIR}/reward.json"
REWARD_TXT="${REWARD_DIR}/reward.txt"
REASON_JSON="${REWARD_DIR}/reason.json"

mkdir -p "${VERIFIER_OUT}" "${REWARD_DIR}"
chown 0:0 "${VERIFIER_OUT}" && chmod 0700 "${VERIFIER_OUT}"

# Sentinel: a crash or hang at any later point leaves a parseable zero.
printf '%s\n' '{"reward": 0}' > "${REWARD_JSON}"
# reward.txt only ever mirrors reward.json's number.
printf '%s\n' '0' > "${REWARD_TXT}"

# Copy reward.json's number into reward.txt, replaced by rename; if reward.json is unreadable or not
# a finite number in [0, 1], both go back to the zero sentinel, so the two files always agree.
mirror_reward_txt() {
  if "${PYTHON:-python3}" -B -c '
import json, math, sys
value = json.load(open(sys.argv[1]))["reward"]
assert isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)
assert 0.0 <= value <= 1.0
print(float(value))
' "${REWARD_JSON}" > "${REWARD_DIR}/.reward-txt.final" 2>/dev/null \
     && mv -f "${REWARD_DIR}/.reward-txt.final" "${REWARD_TXT}" 2>/dev/null; then
    return 0
  fi
  rm -f "${REWARD_DIR}/.reward-txt.final" 2>/dev/null
  printf '%s\n' '{"reward": 0}' > "${REWARD_JSON}"
  printf '%s\n' '0' > "${REWARD_TXT}"
}

# Every exit path, early ones included, leaves reward.txt mirroring reward.json and the carrier
# directory root-owned and 0755. After the final publish below, the files are already final.
REWARD_PUBLISHED=0
trap '[ "${REWARD_PUBLISHED}" = 1 ] || mirror_reward_txt; chown 0:0 "${REWARD_DIR}" 2>/dev/null; chmod 0755 "${REWARD_DIR}" 2>/dev/null' EXIT

reason() {
  printf '{"reason": "%s"}\n' "$1" > "${REASON_JSON}"
}

reason started

# Advisory judge state; the marker is removed only by a usable judge result.
JUDGE_DIR="${GRADE_JUDGE_DIR:-/opt/judge}"
JUDGE_OUT="${VERIFIER_OUT}/judge"
JUDGE_INPUT="${JUDGE_OUT}/input"
JUDGE_TIMEOUT_SEC=900
JUDGE_SKIPPED="${REWARD_DIR}/judge-skipped.json"
JUDGE_UNAVAILABLE="${REWARD_DIR}/judge_unavailable"
rm -f "${REWARD_DIR}/judged.json" "${REWARD_DIR}/judge-details.json" "${REWARD_DIR}/judge-input.json"
printf '%s\n' '{"judge_skipped": true, "reason_code": "not_reached", "reason": "grading stopped before the judge step", "weight_in_reward": 0}' > "${JUDGE_SKIPPED}"
printf '%s\n' 'grading stopped before the judge step; the reward is the compiled score alone' > "${JUDGE_UNAVAILABLE}"

export GRADE_OUT_DIR="${VERIFIER_OUT}"
export GRADE_TESTS_ROOT="${TESTS_ROOT}"
export GOFLAGS="-mod=vendor"
export GOPROXY=off
export GOTOOLCHAIN=local
export GONOSUMCHECK=1
export CGO_ENABLED=0
export LC_ALL=C
export TZ=UTC

GRADE_LOG="${VERIFIER_OUT}/grade.log"

# Refuse to grade without root: grade.py must drop submitted test code to an unprivileged user.
if [ "$(id -u)" != "0" ]; then
  reason "verifier-not-root"
  exit 0
fi

if ! chown 0:0 "${TESTS_ROOT}" "${REWARD_DIR}" || ! chmod 0700 "${TESTS_ROOT}" "${REWARD_DIR}"; then
  reason "private-verifier-assets-unavailable"
  exit 0
fi

# Stage the judge input before any submitted code runs: added test files whole, changed ones as a
# diff against upstream. Nothing here touches what grade.py reads.
"${PYTHON:-python3}" -I -B - "${GRADE_SUBMISSION_DIR:-}" "${TESTS_ROOT}/base-module.tar.gz" \
  "${JUDGE_INPUT}" "${REWARD_DIR}/judge-input.json" <<'STAGE' 2>&1 | tee -a "${GRADE_LOG}"
import difflib
import hashlib
import json
import os
import shutil
import sys
import tarfile

SOURCE, BASE_ARCHIVE, TARGET, REPORT = sys.argv[1:5]
PACKAGE = "processor/k8sattributesprocessor/internal/kube"
MARKER = "no-added-or-changed-test-file"
FILE_CAP = 262144  # bytes of one staged file
TOTAL_CAP = 524288  # bytes of all staged files

base = {}
with tarfile.open(BASE_ARCHIVE, "r:gz") as tar:
    for member in tar.getmembers():
        name = member.name[2:] if member.name.startswith("./") else member.name
        directory, _, leaf = name.rpartition("/")
        if member.isfile() and directory == PACKAGE and leaf.endswith("_test.go"):
            base[leaf] = tar.extractfile(member).read()

shutil.rmtree(TARGET, ignore_errors=True)
os.makedirs(TARGET, mode=0o700)
report = {"source": SOURCE, "staged": [], "unchanged": [], "left_out": []}
try:
    entries = sorted(os.scandir(SOURCE), key=lambda e: e.name) if SOURCE else []
except OSError as exc:
    entries, report["source_error"] = [], type(exc).__name__
total = 0
for entry in entries:
    name = entry.name
    if not name.endswith("_test.go") or name.startswith((".", "_")):
        continue
    if not entry.is_file(follow_symlinks=False):
        continue
    with open(entry.path, "rb") as handle:
        data = handle.read()
    if name in base and data == base[name]:
        report["unchanged"].append(name)
        continue
    if name in base:
        kind, staged_name = "changed", name + ".diff"
        diff = difflib.unified_diff(
            base[name].decode("utf-8", "replace").splitlines(keepends=True),
            data.decode("utf-8", "replace").splitlines(keepends=True),
            fromfile=f"a/{name} (unchanged upstream file)",
            tofile=f"b/{name} (as submitted)",
        )
        shown = "".join(diff).encode("utf-8")
    else:
        kind, staged_name, shown = "added", name, data
    truncated = len(shown) > FILE_CAP
    shown = shown[:FILE_CAP]
    if total + len(shown) > TOTAL_CAP:
        report["left_out"].append({"name": name, "kind": kind, "reason": "total cap reached"})
        continue
    total += len(shown)
    with open(os.path.join(TARGET, staged_name), "wb") as handle:
        handle.write(shown)
    report["staged"].append({"name": name, "kind": kind, "staged_as": staged_name, "size": len(data),
                             "shown_bytes": len(shown), "truncated": truncated,
                             "sha256": hashlib.sha256(data).hexdigest()})
if not report["staged"]:
    open(os.path.join(TARGET, MARKER), "wb").close()
    report["marker"] = MARKER
with open(REPORT, "w", encoding="utf-8") as handle:
    json.dump(report, handle, indent=1, sort_keys=True)
    handle.write("\n")
print(f"judge input: {len(report['staged'])} staged, {len(report['unchanged'])} unchanged left out")
STAGE
JUDGE_STAGE_RC="${PIPESTATUS[0]}"

"${PYTHON:-python3}" "${TESTS_ROOT}/grade.py" 2>&1 | tee "${GRADE_LOG}"
GRADE_RC="${PIPESTATUS[0]}"

# Exit code 3 is grade.py's fail-closed refusal; keep its own reason.json and the zero sentinel.
if [ "${GRADE_RC}" = "3" ]; then
  exit 0
fi

if [ "${GRADE_RC}" != "0" ]; then
  reason "grader-failed"
  exit 0
fi

if [ ! -s "${VERIFIER_OUT}/verdict-matrix.json" ]; then
  reason "no-verdict-matrix"
  exit 0
fi

if ! "${REWARDKIT:-rewardkit}" "${TESTS_ROOT}" \
     --workspace "${WORKSPACE:-/work}" \
     --output "${REWARD_JSON}" 2>&1 | tee -a "${GRADE_LOG}"; then
  # Reward Kit may have left a partial or absent reward.json; restore the
  # numeric sentinel so the carrier stays parseable.
  printf '%s\n' '{"reward": 0}' > "${REWARD_JSON}"
  printf '%s\n' '0' > "${REWARD_TXT}"
  reason "rewardkit-failed"
  exit 0
fi
# Mirror the compiled reward at once: the advisory judge below may run for up to 915 s, and a
# verifier killed meanwhile must still leave reward.txt agreeing with reward.json.
mirror_reward_txt

# Reap the test user's processes, then replace the carrier by rename and make it root-only, so a
# descriptor opened by submitted code cannot modify the final reward.
"${PYTHON:-python3}" -B -c '
import os, signal, time
from pathlib import Path
uid = int(os.environ.get("GRADE_TEST_UID", "20002"))
killed = set()
for _ in range(5):
    alive = []
    for e in Path("/proc").iterdir():
        if not e.name.isdigit() or int(e.name) == os.getpid():
            continue
        try:
            if e.stat().st_uid != uid:
                continue
        except OSError:
            continue
        alive.append(int(e.name))
    if not alive:
        break
    for pid in alive:
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            continue
        killed.add(pid)
    time.sleep(0.2)
if killed:
    print("test.sh reaped: " + repr(sorted(killed)))
' 2>&1 | tee -a "${GRADE_LOG}"

# The advisory judge never writes reward.json or reward.txt.
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
  JUDGE_RC=no-credential
elif [ "${JUDGE_STAGE_RC}" != "0" ]; then
  JUDGE_RC=not-staged
else
  timeout --kill-after=15 "${JUDGE_TIMEOUT_SEC}" "${REWARDKIT:-rewardkit}" "${JUDGE_DIR}" \
    --workspace "${JUDGE_INPUT}" \
    --output "${JUDGE_OUT}/rewardkit.json" >> "${GRADE_LOG}" 2>&1
  JUDGE_RC=$?
fi
echo "judge model: ${REWARDKIT_JUDGE:-judge.toml}; judge exit status: ${JUDGE_RC}" | tee -a "${GRADE_LOG}"

"${PYTHON:-python3}" -I -B - "${JUDGE_RC}" "${JUDGE_DIR}/judge.toml" "${JUDGE_OUT}" "${REWARD_DIR}" <<'MERGE' 2>&1 | tee -a "${GRADE_LOG}" || true
import json
import math
import os
import sys
import tomllib

status, rubric, out, logs = sys.argv[1:5]
VOTES = {"yes": 1.0, "no": 0.0}
SAMPLES = 3  # tests/judge.toml, [judge].samples
SKIPPED = os.path.join(logs, "judge-skipped.json")
UNAVAILABLE = os.path.join(logs, "judge_unavailable")
REWARD = os.path.join(logs, "reward.json")


def write(path: str, payload: dict) -> None:
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2)
        handle.write("\n")
    os.replace(temporary, path)


def skip(code: str, why: str) -> None:
    write(SKIPPED, {"judge_skipped": True, "reason_code": code, "reason": why, "weight_in_reward": 0})
    with open(UNAVAILABLE + ".tmp", "w", encoding="utf-8") as handle:
        handle.write(f"{code}: {why}; the reward is the compiled score alone\n")
    os.replace(UNAVAILABLE + ".tmp", UNAVAILABLE)
    print(f"judge skipped ({code}): {why}; reward unchanged")
    sys.exit(0)


def number(value: object) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


if status == "no-credential":
    skip("no_judge_credential", "ANTHROPIC_API_KEY and OPENAI_API_KEY are unset or empty; the judge was not called")
if status == "not-staged":
    skip("judge_input_not_staged", "the judge input could not be staged")
if status in ("124", "137"):
    skip("judge_timed_out", "Reward Kit for the judge exceeded its time limit")
if status != "0":
    skip("judge_run_failed", f"Reward Kit for the judge exited {status}")
try:
    with open(REWARD, encoding="utf-8") as handle:
        carrier = json.load(handle)
    if not isinstance(carrier, dict) or set(carrier) != {"reward"} or not number(carrier["reward"]):
        skip("judge_result_unusable", "reward.json does not hold the compiled reward alone")
    compiled = carrier["reward"]
    with open(os.path.join(out, "rewardkit.json"), encoding="utf-8") as handle:
        rounded = json.load(handle)
    with open(os.path.join(out, "reward-details.json"), encoding="utf-8") as handle:
        details = json.load(handle)["reward"]
    with open(rubric, "rb") as handle:
        declared = tomllib.load(handle)["criterion"]
    if any("timed out" in str(w) for w in details.get("warnings") or []):
        skip("judge_result_unusable", "a judge call timed out")
    guard = details.get("guard")
    if not isinstance(guard, dict) or guard.get("error") or not isinstance(guard.get("flagged"), bool):
        skip("judge_result_unusable", "the guard has no verdict")
    criteria = details["criteria"]
    if [c.get("id") for c in criteria] != [c["id"] for c in declared]:
        skip("judge_result_unusable", "the judged criteria differ from the rubric")
    rows = []
    for got, want in zip(criteria, declared, strict=True):
        samples = got.get("samples")
        if got.get("error") or not isinstance(samples, list) or len(samples) != SAMPLES:
            skip("judge_result_unusable", f"criterion {want['id']} has no complete set of samples")
        if any(not isinstance(v, str) or v.strip().lower() not in VOTES for v in samples):
            skip("judge_result_unusable", f"criterion {want['id']} has a sample without a yes/no vote")
        votes = sorted(VOTES[v.strip().lower()] for v in samples)
        median = votes[(len(votes) - 1) // 2]
        if got.get("value") != median or float(got.get("weight")) != float(want["weight"]):
            skip("judge_result_unusable", f"criterion {want['id']} disagrees with its votes or weight")
        rows.append({"id": want["id"], "name": want["name"], "weight": float(want["weight"]),
                     "samples": samples, "value": median})
    judged = sum(r["value"] * r["weight"] for r in rows) / sum(r["weight"] for r in rows)
    if guard["flagged"]:
        judged = 0.0
    if round(judged, 4) != details.get("score") or round(judged, 4) != rounded.get("reward"):
        skip("judge_result_unusable", "the recomputed judged score disagrees with Reward Kit's")
except SystemExit:
    raise
except Exception as exc:  # any unreadable judge result leaves the reward as it is
    skip("judge_result_unusable", f"the judge result is unreadable ({type(exc).__name__})")

values = {"compiled": compiled, "judged": judged}
if not all(number(v) for v in values.values()):
    skip("judge_result_unusable", "the judged result is not numeric")
# reward.json is left exactly as the compiled run wrote it: {"reward": x}.
write(os.path.join(logs, "judged.json"), {**values, "guard_flagged": guard["flagged"],
                                          "weight_in_reward": 0, "criteria": rows})
os.remove(SKIPPED)
if os.path.exists(UNAVAILABLE):
    os.remove(UNAVAILABLE)
print(f"reward {compiled!r} (compiled, unchanged); advisory judged {judged!r}")
MERGE
if [ -s "${JUDGE_OUT}/reward-details.json" ]; then
  cp -f "${JUDGE_OUT}/reward-details.json" "${REWARD_DIR}/judge-details.json" 2>/dev/null || true
fi

for f in verdict-matrix.json ctrf.json grade.log; do
  if [ -s "${VERIFIER_OUT}/${f}" ]; then
    cp -f "${VERIFIER_OUT}/${f}" "${REWARD_DIR}/${f}" 2>/dev/null || true
  fi
done

reason "scored"

if [ -s "${REWARD_JSON}" ]; then
  cp -f "${REWARD_JSON}" "${REWARD_DIR}/.reward.final" 2>/dev/null || true
  mv -f "${REWARD_DIR}/.reward.final" "${REWARD_JSON}" 2>/dev/null || true
fi
# Copy the final number into reward.txt, also replaced by rename; any doubt leaves 0 in both.
mirror_reward_txt
REWARD_PUBLISHED=1
chown -R 0:0 "${REWARD_DIR}" 2>/dev/null || true
chmod 0755 "${REWARD_DIR}" 2>/dev/null || true
find "${REWARD_DIR}" -maxdepth 1 -type f -exec chmod 0444 {} + 2>/dev/null || true
exit 0
