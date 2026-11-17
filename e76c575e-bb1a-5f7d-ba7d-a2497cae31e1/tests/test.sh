#!/usr/bin/env bash
# Verifier entry point, run as root ([verifier] user = "root"). No set -e: each step is guarded,
# time-limited and logged. A fail-closed reward 0 is in reward.json and reward.txt before anything is
# graded, and the EXIT trap keeps the two in agreement, so every exit path leaves a reward. An
# infrastructure fault or an unpinned Reward Kit ends the run there with exit 1, so the trial errors;
# every solution failure still scores.
#
# Everything that executes submitted code (the cargo builds and test runs of graded.py) runs as the
# sandbox uid 20002 with no supplementary groups. It cannot write /logs/verifier (root, 0755), the
# grader's files under /tests, the collected note, or the scoring directory (root, 0700), it cannot
# read the private reference note or the judge criteria (root, 0700 / 0600), and anything it leaves
# running is killed before scoring. The note is settled before any submitted code runs: only a
# regular file of at most 65,536 bytes is judged; anything else stands in as an empty note.
#
# Judge modes:
#   1. ANTHROPIC_API_KEY set: Reward Kit scores /tests as it stands (reward = 0.5 tests + 0.5 note,
#      judged by judge.toml's Anthropic model).
#   2. ANTHROPIC_API_KEY empty and OPENAI_API_KEY set: the same, with the same rubric judged by an
#      OpenAI model through REWARDKIT_JUDGE.
#   3. Neither key: nothing is sent to any judge and no retry is made; the reward is the compiled
#      (tests) score alone at full scale, and /logs/verifier/judge_unavailable says why.
# A judged attempt is accepted only when Reward Kit wrote a reward and every judged criterion and the
# guard in its reward-details.json has a verdict (Reward Kit 0.2.4 records a judge call that timed out
# as an errored 0, and a judge API error leaves no output). Otherwise the judged run is retried, up to
# three attempts (60 s, then 120 s apart). In modes 1 and 2, when no attempt is accepted (judge
# unreachable, an HTTP error such as 401/403, a timeout, unusable output, or a criterion or the
# guard without a verdict), the reward is 0.0 in reward.json and reward.txt,
# /logs/verifier/judge_unavailable gives the reason, no attempt's partial result is published, and
# the run exits 0.
set -uo pipefail

OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
export OPENAI_BASE_URL
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  JUDGE_MODE=anthropic
  export REWARDKIT_JUDGE=
elif [ -n "${OPENAI_API_KEY:-}" ]; then
  JUDGE_MODE=openai
  export REWARDKIT_JUDGE=openai/gpt-6-astra
else
  JUDGE_MODE=none
  export REWARDKIT_JUDGE=
fi

V=/logs/verifier
LOG=$V/test-sh.log
SCORE=/var/grade
SANDBOX_UID=20002
SANDBOX_GID=20002

mkdir -p "$V"
chown 0:0 "$V" 2>/dev/null
chmod 0755 "$V" 2>/dev/null
: >>"$LOG"

# The private reference note and the judge criteria are root's alone before any submitted code runs.
# tests/Dockerfile already sets these modes; this keeps them if /tests is replaced at run time.
chown -hR 0:0 /tests 2>>"$LOG"
chmod -R go-w /tests 2>>"$LOG"
chmod 0700 /tests/reference 2>>"$LOG"
chmod 0600 /tests/reference/diagnosis.md /tests/judge.toml 2>>"$LOG"

# Fail closed first; publish() replaces both files only with a scored result.
rm -f "$V/reward.json" "$V/reward.txt" "$V/reward-details.json" "$V/.reward.json.tmp" \
  "$V/.reward.txt.tmp" "$V/judge_unavailable"
printf '{"reward": 0.0}\n' >"$V/reward.json"
printf '0.0\n' >"$V/reward.txt"

# kill_sandbox: every process of the sandbox uid, signalled by that uid itself (kill -1 skips the
# caller). Nothing to kill is not an error.
kill_sandbox() {
  setpriv --reuid="$SANDBOX_UID" --regid="$SANDBOX_GID" --clear-groups -- \
    bash -c 'kill -KILL -1' >/dev/null 2>&1 || true
}

# On every exit: stop the sandbox, then make reward.txt the "reward" of reward.json; if that is not a
# number in [0, 1], both files become 0.
finish() {
  kill_sandbox
  if ! python3 - "$V" >>"$LOG" 2>&1 <<'PY'; then
import json, os, sys

out = sys.argv[1]
with open(os.path.join(out, "reward.json"), encoding="utf-8") as f:
    value = float(json.load(f)["reward"])
if not 0.0 <= value <= 1.0:
    raise SystemExit(f"reward {value} is outside [0, 1]")
tmp = os.path.join(out, ".reward.txt.tmp")
with open(tmp, "w", encoding="utf-8") as f:
    f.write(f"{value}\n")
os.replace(tmp, os.path.join(out, "reward.txt"))
PY
    printf '{"reward": 0.0}\n' >"$V/reward.json"
    printf '0.0\n' >"$V/reward.txt"
    echo "finish: reward.json held no reward in [0, 1]; the fail-closed reward 0 stands" >>"$LOG"
  fi
}
trap finish EXIT

# step NAME LIMIT_SECONDS CMD...: a step killed at its own limit is an infrastructure fault.
step() {
  local name="$1" limit="$2" code=0
  shift 2
  echo "step $name: start $(date -u +%FT%TZ)" >>"$LOG"
  timeout --kill-after=30 "$limit" "$@" >>"$LOG" 2>&1 || code=$?
  echo "step $name: end $(date -u +%FT%TZ) exit=$code" >>"$LOG"
  if [ "$code" -eq 124 ] || [ "$code" -eq 137 ]; then
    echo "step $name: killed at its ${limit}s limit; infrastructure fault, the fail-closed reward 0 stands" >>"$LOG"
    exit 1
  fi
}

# publish DIR: DIR/reward.json from Reward Kit becomes reward.json, its "reward" reward.txt, and
# DIR/reward-details.json reward-details.json. reward.txt is replaced first, reward.json last.
publish() {
  python3 - "$1" "$V" >>"$LOG" 2>&1 <<'PY'
import json, os, shutil, sys

src, out = sys.argv[1:3]
with open(os.path.join(src, "reward.json"), encoding="utf-8") as f:
    raw = f.read()
value = float(json.loads(raw)["reward"])
if not 0.0 <= value <= 1.0:
    raise SystemExit(f"reward {value} is outside [0, 1]")
details = os.path.join(src, "reward-details.json")
if os.path.isfile(details):
    shutil.copyfile(details, os.path.join(out, "reward-details.json"))
for name, body in (("reward.txt", f"{value}\n"), ("reward.json", raw)):
    tmp = os.path.join(out, f".{name}.tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(body)
    os.replace(tmp, os.path.join(out, name))
print(f"publish: reward {value} from {src}")
PY
}

# The scoring directory is root's alone.
rm -rf "$SCORE"
mkdir -p "$SCORE" && chmod 0700 "$SCORE"

# Before any submitted code runs, the collected trees and the note become root-owned and not
# writable by anyone else, so the sandbox can neither plant nor replace the note.
for d in /app /logs/artifacts; do
  if [ -d "$d" ] && [ ! -L "$d" ]; then
    chown -hR 0:0 "$d" 2>>"$LOG"
    chmod -R go-w "$d" 2>>"$LOG"
  fi
done

# Settle the note at /app/diagnosis.md now, before any submitted code runs, or an empty one if none
# was written. A note that is a symbolic link is removed and never judged (it could point the judge
# at the reference note); so is anything else that is not a regular file. A note larger than
# NOTE_MAX bytes stands in as an empty note, so its size cannot make the judge calls fail.
NOTE=/app/diagnosis.md
NOTE_MAX=65536
for p in "$NOTE" /logs/artifacts/app/diagnosis.md; do
  if [ -L "$p" ]; then
    if rm -f -- "$p" 2>>"$LOG"; then
      echo "note: $p was a symbolic link; removed, so it is not judged" >>"$LOG"
    else
      echo "note: $p is a symbolic link that could not be removed; the fail-closed reward 0 stands" >>"$LOG"
      exit 0
    fi
  elif [ -e "$p" ] && [ ! -f "$p" ]; then
    if rm -rf -- "$p" 2>>"$LOG"; then
      echo "note: $p was not a regular file; removed, so it is not judged" >>"$LOG"
    else
      echo "note: $p is not a regular file and could not be removed; the fail-closed reward 0 stands" >>"$LOG"
      exit 0
    fi
  fi
done
if [ ! -e "$NOTE" ] && [ -f /logs/artifacts/app/diagnosis.md ]; then
  mkdir -p /app 2>/dev/null
  cp /logs/artifacts/app/diagnosis.md "$NOTE" 2>>"$LOG" &&
    echo "note: copied from /logs/artifacts/app/diagnosis.md" >>"$LOG"
fi
if [ -e "$NOTE" ]; then
  size=$(stat -c %s -- "$NOTE" 2>>"$LOG") || size=""
  case "$size" in '' | *[!0-9]*) size=$((NOTE_MAX + 1)) ;; esac
  if [ "$size" -gt "$NOTE_MAX" ]; then
    if rm -f -- "$NOTE" 2>>"$LOG" && : >"$NOTE" 2>>"$LOG"; then
      echo "note: $NOTE is larger than $NOTE_MAX bytes ($size); an empty one stands in, so the judged channel scores 0" >>"$LOG"
    else
      echo "note: $NOTE is larger than $NOTE_MAX bytes and could not be replaced; the fail-closed reward 0 stands" >>"$LOG"
      exit 0
    fi
  else
    echo "note: present at $NOTE ($size bytes)" >>"$LOG"
  fi
else
  mkdir -p /app 2>/dev/null
  if : >"$NOTE" 2>>"$LOG"; then
    echo "note: none at $NOTE; an empty one stands in, so the judged channel scores 0" >>"$LOG"
  else
    echo "note: none at $NOTE and none could be created; the judge is shown [not found]" >>"$LOG"
  fi
fi

# Take only the solver's non-test .rs files under openraft/src from the collected tree.
step prepare 300 python3 /tests/.verifier/prepare.py \
  --source /logs/artifacts/app/openraft --source /app/openraft --pristine /opt/statea \
  --out /work/submission --json /logs/verifier/prepare.json

# Build them over the pristine tree with the verifier's tests, run the graded tests once and the
# fail-to-pass tests twice more; missing results and pass-to-pass failures are rerun once. Every
# cargo process runs as the sandbox uid, with the sandbox user's home and cargo configuration.
step graded 6600 env HOME=/home/verifier python3 /tests/.verifier/graded.py \
  --prepare /logs/verifier/prepare.json --pristine /opt/statea --graded /tests/graded --work /work \
  --logs /logs/verifier --json /logs/verifier/report.json --sandbox "$SANDBOX_UID:$SANDBOX_GID"
kill_sandbox

# An infrastructure fault is never a grading result: the run ends here with the fail-closed 0 and
# exit 1, so the trial errors and is rerun rather than scored.
if python3 -c 'import json, sys; sys.exit(0 if json.load(open("/logs/verifier/report.json")).get("infra_fault") is not False else 1)' >>"$LOG" 2>&1; then
  echo "step graded: infrastructure fault; the fail-closed reward 0 stands" >>"$LOG"
  exit 1
fi

step cheat 300 python3 /tests/.cheat/run.py --prepare /logs/verifier/prepare.json \
  --pristine /opt/statea --declaration /tests/cheat-checks.json \
  --json /logs/verifier/ctrf.json

# Any Reward Kit release other than 0.2.4 ends the run with the fail-closed 0.
if ! python3 -c 'import importlib.metadata as m, sys; sys.exit(0 if m.version("harbor-rewardkit") == "0.2.4" else 1)' >>"$LOG" 2>&1; then
  echo "step rewardkit-pin: harbor-rewardkit is not the pinned 0.2.4; the fail-closed reward 0 stands" >>"$LOG"
  exit 1
fi

# judged_cleanly DETAILS: exit 0 when every judged component in a reward-details.json gave a verdict.
# Reward Kit 0.2.4 records a judge call that timed out as an errored 0 and keeps the error only where
# no sample voted, so an error means that criterion was not judged.
judged_cleanly() {
  python3 -c '
import json, sys
def walk(node):
    if isinstance(node, dict):
        if node.get("kind") in ("llm", "agent", "jev"):
            rows = node.get("criteria")
            if not rows or any(row.get("error") for row in rows):
                raise SystemExit(1)
            if (node.get("guard") or {}).get("error"):
                raise SystemExit(1)
        for value in node.values():
            walk(value)
    elif isinstance(node, list):
        for value in node:
            walk(value)
walk(json.load(open(sys.argv[1], encoding="utf-8")))
' "$1" 2>>"$LOG"
}

# Judged run (modes 1 and 2): tests/reward.toml as it stands (reward = 0.5 tests + 0.5 note). Each
# attempt's output is kept apart from the fail-closed files, and only an attempt whose criteria and
# guard all have a verdict is published. With a key set, no such attempt in 3 scores 0.0: a missing
# verdict never scores above a judged one and never falls back to the tests alone.
scored=""
cause=""
if [ "$JUDGE_MODE" = none ]; then
  cause="no judge key: neither ANTHROPIC_API_KEY nor OPENAI_API_KEY is set; the reward is the compiled (tests) score alone"
else
  echo "judge: $JUDGE_MODE (REWARDKIT_JUDGE=${REWARDKIT_JUDGE:-judge.toml})" >>"$LOG"
  failures=""
  for attempt in 1 2 3; do
    run="$SCORE/judged-$attempt"
    rm -rf "$run"
    mkdir -p "$run" 2>>"$LOG"
    rewardkit /tests --output "$run/reward.json"
    if [ -s "$run/reward.json" ]; then
      if judged_cleanly "$run/reward-details.json"; then
        scored="$run"
        break
      fi
      why="a reward without a verdict on every judged criterion and the guard (errored row, or no reward-details.json)"
    else
      why="no reward (judge API, HTTP or network error, or output Reward Kit could not use)"
    fi
    echo "rewardkit: attempt $attempt wrote $why" >>"$LOG"
    failures="${failures:+$failures; }attempt $attempt wrote $why"
    [ "$attempt" -lt 3 ] && sleep $((attempt * 60))
  done
  # Key set, no clean and complete verdict: fail closed at 0.0 in both files, the same keys Reward
  # Kit writes, the reason in judge_unavailable; no attempt's result or details are published.
  if [ -z "$scored" ]; then
    cause="the $JUDGE_MODE judge key is set but none of 3 attempts gave a clean, complete verdict ($failures); the reward is 0.0 and no partial result is published"
    printf '%s\n' "$cause" >"$V/judge_unavailable"
    echo "judge: unavailable: $cause" >>"$LOG"
    printf '{"reward": 0.0, "compiled": 0.0}\n' >"$V/reward.json"
    printf '0.0\n' >"$V/reward.txt"
    exit 0
  fi
fi

# Compiled-only run (mode 3 only, no judge key at all): the same test_output criterion, alone at
# full scale, with no judge.toml, so nothing is sent over the network. It is the number
# tests/reward.toml names "compiled".
if [ -z "$scored" ]; then
  printf '%s\n' "$cause" >"$V/judge_unavailable"
  echo "judge: unavailable: $cause" >>"$LOG"
  rm -rf "$SCORE/compiled"
  if mkdir -p "$SCORE/compiled/tests" 2>>"$LOG" &&
    cp /tests/test_output.py "$SCORE/compiled/tests/test_output.py" 2>>"$LOG" &&
    printf '%s\n' \
      '[scoring.test_output]' \
      'aggregation = "weighted-mean"' \
      '' \
      '[[reward]]' \
      'name = "reward"' \
      'aggregation = "weighted-sum"' \
      'weights = { test_output = 1.0 }' \
      '' \
      '[[reward]]' \
      'name = "compiled"' \
      'aggregation = "weighted-sum"' \
      'weights = { test_output = 1.0 }' \
      >"$SCORE/compiled/tests/reward.toml" 2>>"$LOG"; then
    LITELLM_LOCAL_MODEL_COST_MAP=True rewardkit "$SCORE/compiled/tests" --output "$SCORE/compiled/reward.json"
  fi
  if [ ! -s "$SCORE/compiled/reward.json" ]; then
    echo "rewardkit: the compiled-only run wrote no reward; the fail-closed reward 0 stands" >>"$LOG"
    exit 1
  fi
  scored="$SCORE/compiled"
fi

if ! publish "$scored"; then
  printf '{"reward": 0.0}\n' >"$V/reward.json"
  printf '0.0\n' >"$V/reward.txt"
  echo "publish: $scored/reward.json is not a reward in [0, 1]; the fail-closed reward 0 stands" >>"$LOG"
  exit 1
fi
exit 0
