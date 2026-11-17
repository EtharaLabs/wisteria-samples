#!/bin/bash
# Verifier entry point, run as root (task.toml [verifier].user) in the separate verifier image;
# the grader runs submitted code as uid 10002.
# Fail closed: reward.txt is 0 from the first command and reward.json is {"reward": 0} right after
# it; only the last step replaces them, by rename, and any failure before that leaves the 0.
#
# The reward is the compiled score alone: the pool grader's reward, the rule instruction.md states
# (tests/reward.toml: reward = test_output, judge weight 0). The judge is advisory: when it runs,
# its result is published beside the reward as `judged` and never changes `reward`.
#
# 1. Judge input. Before any submitted code runs, copy each regular *_test.go file directly in
#    /app/server/stree (the files the grader collects; names starting with "." or "_", which Go
#    ignores, are left out) that is not byte-identical to the package's unchanged test file of the
#    same name into the verifier-only directory /var/lib/grader-judge/server/stree, which
#    tests/judge.toml names. When there is none, the directory holds one empty marker file and the
#    judged part scores 0. The list goes to /logs/verifier/judge-input.json.
# 2. Scoring.
#    With the judge key (a non-empty ANTHROPIC_API_KEY, from task.toml [verifier.env]) and the
#    judge input staged: Reward Kit (harbor-rewardkit 0.2.4) over /tests. The one criterion of /tests/test_output.py runs the
#    pool grader once (it writes grading.json and ctrf.json) and scores the grader's matrix;
#    /tests/judge.toml runs the judge. Its result goes to rewardkit.json and its per-criterion and
#    per-sample detail to reward-details.json.
#    Without the key, or when the input cannot be staged: no Reward Kit and no judge call; step 3
#    runs the grader once through the same tests/test_output.py function.
# 3. Carrier. Reward Kit rounds every aggregate to four decimals, which cannot carry k/60, so the
#    exact values are recomputed: compiled = the grader's reward restated over grading.json by
#    test_output.pool_reward; reward = compiled; judged = the weighted mean of the nine criteria's
#    median votes, 0 when the guard flagged the submission; blended = 0.85 compiled + 0.15 judged.
#    reward.json gets {"reward"} alone and reward.txt the same number, after Reward Kit's rounded
#    values are checked against them. judged.json holds the judge's detail, compiled and blended.
#
# When the judge cannot be used, the reward is the same compiled score and only the advisory values
# are missing: reward.json gets {"reward"}, and /logs/verifier/judge-skipped.json records
# {"judge_skipped": true} with a reason code (no_judge_credential, judge_input_not_staged,
# judge_run_failed, judge_result_unusable) and the reason. The compiled score then comes from this
# trial's grading.json when Reward Kit's grader run left one, otherwise from one grader run here.
# The script exits 0 on these paths: a judge that is missing or fails never errors the trial.
# A compiled value that disagrees with Reward Kit's publishes reward 0.
printf '0\n' > /logs/verifier/reward.txt
set -uo pipefail

printf '%s\n' '{"reward": 0}' > /logs/verifier/reward.json
rm -f /logs/verifier/grading.json /logs/verifier/judge-skipped.json /logs/verifier/judged.json \
  /logs/verifier/rewardkit.json /logs/verifier/reward-details.json

python3 -I -B - /app/server/stree /opt/grader/pristine-tests /var/lib/grader-judge/server/stree <<'PY'
import hashlib
import json
import os
import shutil
import sys

SOURCE, PRISTINE, TARGET = sys.argv[1:4]  # the submission, the unchanged tests, the judge's copy
MARKER = "no-changed-or-added-test-file"
FILE_CAP = 262144  # bytes of one file shown to the judge
TOTAL_CAP = 393216  # bytes of all files shown to the judge

shutil.rmtree(TARGET, ignore_errors=True)
os.makedirs(TARGET, mode=0o700)
report = {"source": SOURCE, "target": TARGET, "staged": [], "unchanged": [], "left_out": []}
try:
    entries = sorted(os.scandir(SOURCE), key=lambda e: e.name)
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
        data = handle.read(FILE_CAP + 1)
        size = os.fstat(handle.fileno()).st_size
    try:
        with open(os.path.join(PRISTINE, name), "rb") as handle:
            if handle.read() == data and size == len(data):
                report["unchanged"].append(name)
                continue
    except FileNotFoundError:
        pass
    shown = data[:FILE_CAP]
    if total + len(shown) > TOTAL_CAP:
        report["left_out"].append({"name": name, "size": size, "reason": "total cap reached"})
        continue
    total += len(shown)
    with open(os.path.join(TARGET, name), "wb") as handle:
        handle.write(shown)
    report["staged"].append({
        "name": name,
        "size": size,
        "shown_bytes": len(shown),
        "truncated": size > len(shown),
        "sha256": hashlib.sha256(shown).hexdigest(),
    })
if not report["staged"]:
    open(os.path.join(TARGET, MARKER), "wb").close()
    report["marker"] = MARKER
with open("/logs/verifier/judge-input.json", "w", encoding="utf-8") as handle:
    json.dump(report, handle, indent=1, sort_keys=True)
    handle.write("\n")
print(f"judge input: {len(report['staged'])} file(s) staged, {len(report['unchanged'])} unchanged left out")
PY
stage_status=$?
echo "judge input exit status: ${stage_status}"

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  rewardkit_status=no-credential
elif [ "${stage_status}" = 0 ]; then
  rewardkit /tests --workspace /app --output /logs/verifier/rewardkit.json
  rewardkit_status=$?
else
  rewardkit_status=not-run
fi
echo "rewardkit exit status: ${rewardkit_status}"

python3 -I -B - "${stage_status}" "${rewardkit_status}" <<'PY'
import json
import math
import os
import sys
import tomllib

sys.modules["rewardkit"] = None  # import the restated rule only, never register criteria again
sys.path.insert(0, "/tests")
from test_output import grade, pool_reward  # noqa: E402

LOGS = os.path.join(os.sep, "logs", "verifier")
GRADING = os.path.join(LOGS, "grading.json")
# tests/reward.toml, in Reward Kit's order of dimensions and summation (judge, then test_output).
NAMED = {
    "reward": {"judge": 0.0, "test_output": 1.0},
    "compiled": {"judge": 0.0, "test_output": 1.0},
    "judged": {"judge": 1.0, "test_output": 0.0},
    "blended": {"judge": 0.15, "test_output": 0.85},
}
SAMPLES = 3  # tests/judge.toml, [judge].samples
VOTES = {"yes": 1.0, "no": 0.0}


def publish(name: str, text: str) -> None:
    temporary = os.path.join(LOGS, "." + name + ".final")
    with open(temporary, "w", encoding="ascii") as handle:
        handle.write(text)
    os.replace(temporary, os.path.join(LOGS, name))


def compiled_only(code: str, reason: str) -> None:
    """The judge cannot be used: publish the compiled score as the reward, mark the skip, stop."""
    if os.path.exists(GRADING):  # removed before Reward Kit ran, so written by this trial's grader
        exact, source = pool_reward(), "grading.json of this trial's Reward Kit run"
    else:
        exact, source = grade(), "one grader run by tests/test.sh"
    score = exact["score"]
    with open(os.path.join(LOGS, "judge-skipped.json"), "w", encoding="utf-8") as handle:
        json.dump({"judge_skipped": True, "reason_code": code, "reason": reason,
                   "reward": score, "compiled": score, "compiled_reasoning": exact["reasoning"],
                   "compiled_from": source}, handle, indent=1)
        handle.write("\n")
    publish("reward.json", json.dumps({"reward": score}) + "\n")
    publish("reward.txt", repr(score) + "\n")
    print(f"judge skipped ({code}: {reason}); reward = compiled {score!r} ({exact['reasoning']})")
    sys.exit(0)


def unusable(reason: str) -> None:
    compiled_only("judge_result_unusable", reason)


def number(value: object) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def named(name: str, parts: dict[str, float]) -> float:
    return sum(v * NAMED[name][k] for k, v in sorted(parts.items()))  # Reward Kit's order and sum


def judged(details: dict, declared: list[dict]) -> tuple[float, list[dict], bool]:
    """The exact judged part from Reward Kit's per-criterion votes, or the compiled score alone."""
    components = details["reward"]["components"]
    (judge,) = [c["detail"] for c in components if c["name"] == "judge"]
    if any("timed out" in str(w) for w in judge.get("warnings", [])):
        unusable("a judge call timed out")
    guard = judge.get("guard")
    if not isinstance(guard, dict) or guard.get("error") or not isinstance(guard.get("flagged"), bool):
        unusable("the guard has no verdict")
    criteria = judge["criteria"]
    if [c.get("id") for c in criteria] != [c["id"] for c in declared]:
        unusable("the judge's criteria differ from tests/judge.toml")
    rows = []
    for got, want in zip(criteria, declared, strict=True):
        samples = got.get("samples")
        if got.get("error") or not isinstance(samples, list) or len(samples) != SAMPLES:
            unusable(f"criterion {want['id']} has no complete set of samples")
        if any(not isinstance(s, str) or s.strip().lower() not in VOTES for s in samples):
            unusable(f"criterion {want['id']} has a sample without a yes/no vote")
        votes = sorted(VOTES[s.strip().lower()] for s in samples)
        median = votes[(len(votes) - 1) // 2]
        if got.get("value") != median or float(got.get("weight")) != float(want["weight"]):
            unusable(f"criterion {want['id']} disagrees with its votes or weight")
        rows.append({"id": want["id"], "name": want["name"], "weight": float(want["weight"]),
                     "samples": samples, "value": median, "agreement": got.get("agreement")})
    total = sum(r["weight"] for r in rows)
    score = sum(r["value"] * r["weight"] for r in rows) / total
    if guard["flagged"]:
        score = min(score, 0.0)
    if round(score, 4) != judge.get("score"):
        unusable("the judged part disagrees with Reward Kit's")
    return score, rows, guard["flagged"]


stage_status, rewardkit_status = sys.argv[1:3]
if rewardkit_status == "no-credential":
    compiled_only("no_judge_credential", "ANTHROPIC_API_KEY is unset or empty; the judge was not called")
if stage_status != "0":
    compiled_only("judge_input_not_staged", f"the judge input could not be staged (exit {stage_status})")
if rewardkit_status != "0":
    compiled_only("judge_run_failed",
                  f"Reward Kit exited {rewardkit_status}; a judge call that errors makes it exit without a result")
try:
    with open(os.path.join(LOGS, "rewardkit.json"), encoding="utf-8") as handle:
        rounded = json.load(handle)
    with open(os.path.join(LOGS, "reward-details.json"), encoding="utf-8") as handle:
        details = json.load(handle)
    with open("/tests/judge.toml", "rb") as handle:
        declared = tomllib.load(handle)["criterion"]
    judge_score, rows, flagged = judged(details, declared)
    if not isinstance(rounded, dict) or not all(number(rounded.get(k)) for k in NAMED):
        unusable("Reward Kit's result does not hold the four named numbers")
except Exception as exc:  # any unreadable judge result: the compiled score alone
    unusable(f"Reward Kit's result is unreadable ({type(exc).__name__})")

exact = pool_reward()
reason = exact["reasoning"]
if round(exact["score"], 4) != rounded["compiled"] or round(exact["score"], 4) != rounded["reward"]:
    compiled = 0.0
    reason = f"rewardkit compiled {rounded['compiled']!r} differs from the grader's {exact['score']!r}"
else:
    compiled = exact["score"]
parts = {"judge": judge_score, "test_output": compiled}
values = {name: named(name, parts) for name in NAMED}
if values["reward"] != compiled or values["compiled"] != compiled:
    compiled_only("judge_result_unusable", "the reward does not equal the compiled score")
if compiled == exact["score"] and any(round(values[k], 4) != rounded[k] for k in ("judged", "blended")):
    unusable("Reward Kit's judged or blended value differs from the recomputed one")
with open(os.path.join(LOGS, "judged.json"), "w", encoding="utf-8") as handle:
    json.dump({"judged": judge_score, "guard_flagged": flagged, "criteria": rows,
               "weight_in_reward": 0.0, "compiled": values["compiled"],
               "blended": values["blended"]}, handle, indent=1)
    handle.write("\n")
publish("reward.json", json.dumps({"reward": values["reward"]}) + "\n")
publish("reward.txt", repr(values["reward"]) + "\n")
print(f"reward: {values['reward']!r} = compiled ({reason}); advisory judged {judge_score!r}, "
      f"blended {values['blended']!r}")
PY
carrier_status=$?
echo "carrier exit status: ${carrier_status}"
cat /logs/verifier/reward.txt 2>/dev/null
exit "${carrier_status}"
