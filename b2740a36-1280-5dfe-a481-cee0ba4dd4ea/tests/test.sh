#!/usr/bin/env bash
# Verifier entry point, run as root ([verifier] user = "root"). No set -e: each step is guarded and
# logged. A fail-closed reward of 0 is in reward.json and reward.txt before grading starts, and every
# exit path leaves both. /logs/verifier stays root-owned with mode 0755. graded.py runs as root and
# only the processes that build and test the submitted sources (meson, ninja, the tests, pytest) run
# as the unprivileged sandbox uid 20002, which can write neither /logs/verifier, the grader's files,
# nor the root-only directory where graded.py writes its report and logs; root writes every result.
# The private reference note and the rubric are readable by root only, and root takes the solver's
# note (a regular file of at most 65,536 bytes) before any sandbox process runs.
#
# Judge modes (the rubric is tests/judge.toml):
#   ANTHROPIC_API_KEY set: the Anthropic judge judge.toml names.
#   ANTHROPIC_API_KEY empty and OPENAI_API_KEY set: the same rubric judged by an OpenAI model
#     (REWARDKIT_JUDGE).
#   Neither key: nothing is sent to any judge and no retry is made; the reward is the compiled score
#     (tests/reward.toml "compiled", test_output alone) at full scale, and
#     /logs/verifier/judge_unavailable gives the reason.
# With a key, a judged attempt is accepted only when Reward Kit wrote a reward and its
# reward-details.json holds a verdict on every criterion judge.toml declares and on the guard
# (Reward Kit 0.2.4 records a judge call that timed out as an errored 0, and a judge API error or an
# unparseable reply leaves no output). Otherwise the judged run is retried, up to three attempts
# (60 s, then 120 s apart). If no attempt is accepted, whatever the cause, the reward is 0.0 in
# reward.json and reward.txt, /logs/verifier/judge_unavailable gives the reason, nothing of any
# attempt is published, and the run exits 0.
# An infrastructure fault or an unpinned Reward Kit ends the run with the fail-closed 0 and a
# non-zero exit, so the trial errors and is rerun; every solution failure still scores.
set -uo pipefail
umask 022

V=/logs/verifier
LOG=$V/test-sh.log
REWARD_JSON=$V/reward.json
REWARD_TXT=$V/reward.txt
SANDBOX_UID=20002
OPENAI_JUDGE=openai/gpt-6-astra
# Root-owned grading state: the taken sources (readable by the sandbox) and a root-only directory
# for graded.py's report and logs and for Reward Kit's output.
G=/var/lib/grade
PRIV=$G/private

mkdir -p "$V"
chown 0:0 "$V"
chmod 0755 "$V"
rm -f "$LOG" "$V/reward-details.json" "$V/judge_unavailable"
: >>"$LOG"

# rm first, so nothing planted at the reward paths is written through.
fail_closed() {
  rm -f "$REWARD_JSON" "$REWARD_TXT"
  printf '{"reward": 0.0}\n' >"$REWARD_JSON"
  printf '0.0\n' >"$REWARD_TXT"
}

# Ends every process of the sandbox uid: kill(-1) from that uid reaches all of its processes and
# none of root's.
kill_sandbox() {
  local _
  for _ in 1 2 3; do
    setpriv --reuid="$SANDBOX_UID" --regid="$SANDBOX_UID" --clear-groups -- \
      python3 -c 'import os, signal; os.kill(-1, signal.SIGKILL)' >/dev/null 2>&1 || :
  done
}

fail_closed
trap 'kill_sandbox; [ -s "$REWARD_JSON" ] && [ -s "$REWARD_TXT" ] || fail_closed' EXIT

step() {
  local name="$1"
  shift
  echo "step $name: start $(date -u +%FT%TZ)" >>"$LOG"
  "$@" >>"$LOG" 2>&1 || echo "step $name: exited $? (continuing)" >>"$LOG"
  echo "step $name: end $(date -u +%FT%TZ)" >>"$LOG"
}

if [ "$(id -u)" != "0" ]; then
  echo "verifier: must run as root to isolate the submission; the fail-closed 0 stays" >>"$LOG"
  exit 1
fi

# /tests holds the grader: root-owned and writable by root only, however it was copied in.
chown -R 0:0 /tests 2>/dev/null || :
chmod -R go-w /tests 2>/dev/null || :
# The private reference note and the rubric are read by root (Reward Kit) only, never by the
# sandbox uid: directories 0700, files 0600.
for private in /tests/reference /tests/judge.toml; do
  if [ -e "$private" ] && [ ! -L "$private" ]; then
    chmod -R go= "$private" 2>>"$LOG" || echo "lock: could not make $private root-only" >>"$LOG"
  fi
done

# graded.py gets a fresh minimal environment, so no judge key reaches it or the sandbox: these are
# the variables it passes on to the build and the tests.
sandbox_env() {
  env -i PATH="$PATH" HOME=/home/verifier USER=verifier LOGNAME=verifier \
    PYTHONDONTWRITEBYTECODE=1 ${TMPDIR:+TMPDIR="$TMPDIR"} "$@"
}

kill_sandbox
rm -rf "$G" /work
install -d -m 0755 "$G"
install -d -m 0700 "$PRIV"
install -d -m 0700 "$PRIV/logs"
install -d -o "$SANDBOX_UID" -g "$SANDBOX_UID" -m 0755 /work

# Take the solver's note into the root-only $PRIV now, before any process of the sandbox uid
# exists, so nothing the submission runs can read or change what is judged. The note is the first
# of the two paths that is a regular file, opened without following a symbolic link (a link could
# point the judge at the reference note) and without blocking; a note larger than 65,536 bytes
# counts as no note. With no note the judge sees an empty file.
take_note() {
  python3 - "$PRIV/diagnosis.md" /logs/artifacts/app/diagnosis.md /app/diagnosis.md <<'PY'
import os, stat, sys
CAP = 65536
dst, sources = sys.argv[1], sys.argv[2:]
data, msg = b"", "note: none submitted; the judge sees an empty note"
for src in sources:
    try:
        fd = os.open(src, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError:
        continue
    with os.fdopen(fd, "rb") as f:
        if not stat.S_ISREG(os.fstat(f.fileno()).st_mode):
            continue
        try:
            data = f.read(CAP + 1)
        except OSError as exc:
            data, msg = b"", f"note: {src} unreadable ({type(exc).__name__}); the judge sees an empty note"
            break
    if len(data) > CAP:
        data, msg = b"", f"note: {src} is larger than {CAP} bytes; the judge sees an empty note"
    else:
        msg = f"note: taken from {src} ({len(data)} bytes)"
    break
with open(dst, "wb") as f:
    f.write(data)
print(msg, file=sys.stderr)
PY
}
step note take_note

# Take the solver's changed copies of existing files under src/ out of the collected tree; nothing
# else of the tree is used. Read as root, so the artifact's file modes never hide a file.
step prepare python3 /tests/.verifier/prepare.py \
  --source /logs/artifacts/app/fontconfig --source /app/fontconfig --pristine /opt/statea \
  --out "$G/submission" --json "$V/prepare.json"

# graded.py, as root: lay them over the pristine tree, place the verifier's cross-version test file,
# hand the tree to the sandbox uid, and, as that uid, build out of tree, run the meson test suite and
# the pytest suite once, and the cross-version file against each older release three times. The
# report and the suite logs are written by root into the root-only $PRIV; graded.py's own output goes
# to a root-only file and reaches the log once no sandbox process is left.
echo "step graded: start $(date -u +%FT%TZ)" >>"$LOG"
sandbox_env python3 /tests/.verifier/graded.py --prepare "$V/prepare.json" \
  --pristine /opt/statea --graded /tests/graded --work /work --logs "$PRIV/logs" \
  --json "$PRIV/report.json" --sandbox "$SANDBOX_UID:$SANDBOX_UID" </dev/null >"$PRIV/graded.out" 2>&1 \
  || echo "step graded: exited $? (continuing)" >>"$LOG"
kill_sandbox
head -c $((16 << 20)) "$PRIV/graded.out" >>"$LOG"
rm -f "$PRIV/graded.out"
echo "step graded: end $(date -u +%FT%TZ)" >>"$LOG"

# With no sandbox process left, copy the report and the suite logs from $PRIV into /logs/verifier:
# regular files only, by fixed names, size-capped.
collect() {
  python3 - "$V" "$PRIV" <<'PY'
import os, re, stat, sys
dst, priv = sys.argv[1:3]
LOG_NAME = re.compile(r"^(build-(setup|compile)|meson-test-[12]|pytest-[12]|xv-[0-9.]+-run[0-9]+-[12])\.log$")

def copy(src, name, cap):
    try:
        fd = os.open(src, os.O_RDONLY | os.O_NOFOLLOW)
    except OSError:
        return
    with os.fdopen(fd, "rb") as f:
        st = os.fstat(f.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_size > cap:
            print(f"collect: {src} refused", file=sys.stderr)
            return
        data = f.read(cap + 1)
    out = os.path.join(dst, name)
    tmp = out + ".tmp"
    with open(tmp, "wb") as f:
        f.write(data)
    os.chmod(tmp, 0o644)
    os.replace(tmp, out)

copy(os.path.join(priv, "report.json"), "report.json", 64 << 20)
logs = os.path.join(priv, "logs")
if os.path.isdir(logs) and not os.path.islink(logs):
    for name in sorted(os.listdir(logs)):
        if LOG_NAME.match(name):
            copy(os.path.join(logs, name), name, 16 << 20)
PY
}
rm -f "$V/report.json"
step collect collect

# An infrastructure fault is never a grading result: the run ends here with the fail-closed 0 and a
# non-zero exit, so the trial errors and is rerun rather than scored.
if python3 -c 'import json, sys; sys.exit(0 if json.load(open("/logs/verifier/report.json")).get("infra_fault") else 1)' >>"$LOG" 2>&1; then
  echo "step graded: infrastructure fault; the fail-closed 0 stays" >>"$LOG"
  fail_closed
  exit 1
fi

step cheat python3 /tests/.cheat/run.py --prepare "$V/prepare.json" \
  --pristine /opt/statea --declaration /tests/cheat-checks.json \
  --json "$V/ctrf.json"

# Place the note root took before the build at /app/diagnosis.md, where the judge reads it,
# replacing whatever is there; with no sandbox process left, nothing else writes it.
place_note() {
  [ -f "$PRIV/diagnosis.md" ] && [ ! -L "$PRIV/diagnosis.md" ] || : >"$PRIV/diagnosis.md"
  mkdir -p /app
  rm -rf /app/diagnosis.md
  install -m 0644 "$PRIV/diagnosis.md" /app/diagnosis.md
}
step place-note place_note

# Any Reward Kit release other than 0.2.4 ends the run with the fail-closed 0.
if ! python3 -c 'import importlib.metadata as m, sys; sys.exit(0 if m.version("harbor-rewardkit") == "0.2.4" else 1)' >>"$LOG" 2>&1; then
  echo "step rewardkit-pin: harbor-rewardkit is not the pinned 0.2.4; the fail-closed 0 stays" >>"$LOG"
  fail_closed
  exit 1
fi

# Reward Kit writes into the root-only directory; only publish() puts a result at the reward paths.
RK_OUT=$PRIV/rewardkit
install -d -m 0700 "$RK_OUT"

OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
export OPENAI_BASE_URL
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  judge=anthropic
  export REWARDKIT_JUDGE=''
elif [ -n "${OPENAI_API_KEY:-}" ]; then
  judge=openai
  export REWARDKIT_JUDGE="$OPENAI_JUDGE"
else
  judge=none
fi

# Exit 0 when a reward-details.json holds a complete, clean verdict: at least one judged component,
# each listing exactly the criterion ids tests/judge.toml declares, and no criterion row and no
# guard with an error. Reward Kit 0.2.4 records a judge call that timed out as an errored 0 and
# keeps the error only where no sample voted, so an error means no judgment.
judged_cleanly() { python3 -c '
import json, sys, tomllib
with open(sys.argv[2], "rb") as f:
    want = sorted(str(c["id"]) for c in tomllib.load(f)["criterion"])
judged = []
def walk(node):
    if isinstance(node, dict):
        if node.get("kind") in ("llm", "agent", "jev"):
            rows = node.get("criteria")
            if not rows or any(row.get("error") for row in rows):
                raise SystemExit(1)
            if sorted(str(row.get("id")) for row in rows) != want:
                raise SystemExit(1)
            if (node.get("guard") or {}).get("error"):
                raise SystemExit(1)
            judged.append(node)
        for value in node.values():
            walk(value)
    elif isinstance(node, list):
        for value in node:
            walk(value)
walk(json.load(open(sys.argv[1], encoding="utf-8")))
sys.exit(0 if judged and want else 1)
' "$1" /tests/judge.toml 2>>"$LOG"; }

# A judge key is set and no attempt gave a complete, clean verdict: the reward is 0.0 in reward.json
# (with the keys Reward Kit writes there, reward and compiled) and in reward.txt, judge_unavailable
# gives the reason, and no attempt's reward-details.json is published.
judge_failed() {
  local tmp
  echo "judge: $1" >>"$LOG"
  rm -f "$V/judge_unavailable" "$V/reward-details.json"
  printf '%s\n' "$1" >"$V/judge_unavailable" && chmod 0644 "$V/judge_unavailable" || return 1
  tmp="$(mktemp "$V/.reward-txt.XXXXXX")" && printf '0.0\n' >"$tmp" \
    && chmod 0644 "$tmp" && mv -f "$tmp" "$REWARD_TXT" || return 1
  tmp="$(mktemp "$V/.reward-json.XXXXXX")" && printf '{"reward": 0.0, "compiled": 0.0}\n' >"$tmp" \
    && chmod 0644 "$tmp" && mv -f "$tmp" "$REWARD_JSON" || return 1
  echo "reward: 0.0 (judge: $judge, no complete verdict)" >>"$LOG"
}

# Judged run: tests/reward.toml as it stands (reward = 0.5 test_output + 0.5 judge). Each attempt
# writes into its own root-only directory, and the first attempt with a complete, clean verdict is
# published. If none of the three has one, whatever the cause (no reward written: an API, network or
# authentication error or an unparseable reply; or a reward with an errored or missing criterion or
# guard), the reward is 0 and nothing of any attempt is published. With no key nothing is sent and
# nothing is retried; the compiled-only run below decides the reward.
unavailable=""
scored=""
if [ "$judge" = none ]; then
  unavailable="no judge key (ANTHROPIC_API_KEY and OPENAI_API_KEY are empty); the reward is the compiled score alone"
else
  failures=""
  for attempt in 1 2 3; do
    run="$RK_OUT/judged-$attempt"
    rm -rf "$run"
    install -d -m 0700 "$run"
    rewardkit /tests --output "$run/reward.json"
    if [ -s "$run/reward.json" ]; then
      if judged_cleanly "$run/reward-details.json"; then
        scored="$run"
        break
      fi
      why="attempt $attempt wrote a reward without a complete verdict (a judged criterion or the guard errored or is missing, or no reward-details.json)"
    else
      why="attempt $attempt wrote no reward (judge $judge: API, network or authentication error, or an unparseable reply)"
    fi
    echo "rewardkit: $why" >>"$LOG"
    failures="${failures:+$failures; }$why"
    [ "$attempt" -lt 3 ] && sleep $((attempt * 60))
  done
  if [ -z "$scored" ]; then
    if judge_failed "the $judge judge key is set but none of 3 attempts gave a complete, clean verdict ($failures); the reward is 0"; then
      exit 0
    fi
    echo "judge: could not write the zero reward; the fail-closed 0 stays" >>"$LOG"
    rm -f "$V"/.reward-txt.* "$V"/.reward-json.*
    fail_closed
    exit 1
  fi
  # The chosen attempt goes where publish() reads it; a failed copy is an infrastructure fault,
  # never a reason to fall back to the compiled score.
  rm -f "$RK_OUT/reward.json" "$RK_OUT/reward-details.json"
  if ! cp "$scored/reward.json" "$RK_OUT/reward.json" \
    || { [ -f "$scored/reward-details.json" ] && ! cp "$scored/reward-details.json" "$RK_OUT/reward-details.json"; }; then
    echo "rewardkit: could not stage $scored for publishing; the fail-closed 0 stays" >>"$LOG"
    fail_closed
    exit 1
  fi
fi

# Compiled-only run, only when no judge key is set: the same test_output criterion, scored by Reward
# Kit under the compiled reward that tests/reward.toml declares, with no judge; reward = compiled,
# at full scale.
if [ -n "$unavailable" ]; then
  echo "judge: $unavailable" >>"$LOG"
  printf '%s\n' "$unavailable" >"$V/judge_unavailable"
  chmod 0644 "$V/judge_unavailable"
  stage=$PRIV/compiled
  rm -rf "$stage" "$RK_OUT/reward.json" "$RK_OUT/reward-details.json"
  if ! { install -d -m 0700 "$stage" && cp /tests/test_output.py "$stage/test_output.py"; } \
    || ! python3 - /tests/reward.toml "$stage/reward.toml" >>"$LOG" 2>&1 <<'PY'
import json, sys, tomllib

src, dst = sys.argv[1:3]
with open(src, "rb") as f:
    cfg = tomllib.load(f)
compiled = next(r for r in cfg["reward"] if r["name"] == "compiled")
weights = {k: float(v) for k, v in compiled["weights"].items() if k != "judge"}
assert weights and float(compiled["weights"].get("judge", 0.0)) == 0.0, compiled
lines = []
for name, table in cfg.get("scoring", {}).items():
    lines.append(f"[scoring.{name}]")
    lines += [f"{k} = {json.dumps(v)}" for k, v in table.items()]
    lines.append("")
for name in ("reward", "compiled"):
    lines += ["[[reward]]", f"name = {json.dumps(name)}",
              f"aggregation = {json.dumps(compiled['aggregation'])}", "", "[reward.weights]"]
    lines += [f"{k} = {v!r}" for k, v in weights.items()]
    lines.append("")
with open(dst, "w", encoding="utf-8") as f:
    f.write("\n".join(lines))
PY
  then
    echo "compiled-only: could not derive the compiled reward.toml; the fail-closed 0 stays" >>"$LOG"
    fail_closed
    exit 1
  fi
  REWARDKIT_JUDGE='' rewardkit "$stage" --output "$RK_OUT/reward.json"
  if [ ! -s "$RK_OUT/reward.json" ]; then
    echo "compiled-only: Reward Kit wrote no reward; the fail-closed 0 stays" >>"$LOG"
    fail_closed
    exit 1
  fi
fi

# Publish Reward Kit's reward.json unchanged and its reward in reward.txt: details first, then
# reward.txt, then reward.json, each by rename.
publish() {
  local value tmp
  value="$(python3 -c 'import json, math, sys; v = float(json.load(open(sys.argv[1]))["reward"]); sys.exit("reward out of range") if not (math.isfinite(v) and 0.0 <= v <= 1.0) else print(v)' "$RK_OUT/reward.json")" \
    || return 1
  if [ -f "$RK_OUT/reward-details.json" ]; then
    tmp="$(mktemp "$V/.reward-details.XXXXXX")" && cp "$RK_OUT/reward-details.json" "$tmp" \
      && chmod 0644 "$tmp" && mv -f "$tmp" "$V/reward-details.json" || return 1
  fi
  tmp="$(mktemp "$V/.reward-txt.XXXXXX")" && printf '%s\n' "$value" >"$tmp" \
    && chmod 0644 "$tmp" && mv -f "$tmp" "$REWARD_TXT" || return 1
  tmp="$(mktemp "$V/.reward-json.XXXXXX")" && cp "$RK_OUT/reward.json" "$tmp" \
    && chmod 0644 "$tmp" && mv -f "$tmp" "$REWARD_JSON" || return 1
  echo "reward: $value (judge: $judge${unavailable:+, unavailable})" >>"$LOG"
}
if ! publish; then
  echo "publish: Reward Kit's reward is unreadable or out of range; the fail-closed 0 stays" >>"$LOG"
  rm -f "$V"/.reward-details.* "$V"/.reward-txt.* "$V"/.reward-json.*
  fail_closed
  exit 1
fi
exit 0
