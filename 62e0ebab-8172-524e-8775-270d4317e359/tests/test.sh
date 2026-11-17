#!/bin/bash
# Verifier entry point, run as root ([verifier] user = "root" in task.toml).
#
# Fail closed: reward 0 is in reward.json and reward.txt before grading starts, and the EXIT trap leaves that
# zero on every path that ends before a reward is published. Everything that stages, builds, tests or runs the
# submitted tree runs as the unprivileged sandbox uid SANDBOX_UID with no supplementary groups and without the
# judge credentials, so it can write neither /logs/verifier nor the grader's own files; this script, as root,
# writes every step report and the reward.
#
# Judge modes. The judge reads the note /app/diagnosis.md, which root copies before any submitted code runs (a
# regular file of at most 65,536 bytes; anything else counts as no note) and puts back there itself before the judge
# runs; the judge never runs submitted code.
#   1. ANTHROPIC_API_KEY set: the judge judge.toml names, through the judged admission gate.
#   2. ANTHROPIC_API_KEY empty and OPENAI_API_KEY set: the same rubric and gate, judged by an OpenAI model
#      (REWARDKIT_JUDGE).
#   3. Neither key: no judge call, and the reward is the compiled score alone (the `compiled` table of
#      reward.toml, 0 to 1); judge_unavailable says why.
# With a key set, one probe request goes to the judge endpoint first. If it gets no HTTP response, or HTTP 401 or
# 403, the judge is not called. Otherwise the judge runs, up to three attempts, and the first attempt that returns
# a clean verdict is published as is: with a working judge the published reward.json is Reward Kit's own, byte for
# byte. A key that is set but yields no clean verdict, for any reason (no HTTP response, 401 or 403, or no clean
# verdict in three attempts, including an attempt that decided the gate closed but gave no verdict on the note),
# publishes the fail-closed 0 in reward.json and reward.txt and no partial result (judge_unavailable says why).
# Every step writes a report and a failure is a defined zero, so `set -e` is deliberately not used.
set -uo pipefail

LOG=/logs/verifier
SANDBOX_UID="${SANDBOX_UID:-40000}"
FAIL_CLOSED='{"reward": 0.0, "compiled": 0.0}'
PUBLISHED=0
# Each carrier is replaced by rename, so a reward file exists at every moment once the first one is written.
fail_closed() {
    { printf '%s\n' "$FAIL_CLOSED" > "$LOG/reward.json.part" && mv -fT "$LOG/reward.json.part" "$LOG/reward.json"; } \
        || printf '%s\n' "$FAIL_CLOSED" > "$LOG/reward.json"
    { printf '0.0\n' > "$LOG/reward.txt.part" && mv -fT "$LOG/reward.txt.part" "$LOG/reward.txt"; } \
        || printf '0.0\n' > "$LOG/reward.txt"
}
# Run a command as the sandbox uid: no supplementary groups, no capabilities, no judge credentials.
sandbox() {
    setpriv --reuid="$SANDBOX_UID" --regid="$SANDBOX_UID" --clear-groups --no-new-privs \
        env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY HOME=/nonexistent "$@"
}
# Stop everything the sandbox uid still runs (kill -1 as that uid reaches only its own processes).
sandbox_stop() { sandbox bash -c 'kill -KILL -1' 2>/dev/null; return 0; }
mkdir -p "$LOG"
chown 0:0 "$LOG"
chmod 0755 "$LOG"
rm -f "$LOG/reward.txt" "$LOG/reward.json" "$LOG/reward-details.json" "$LOG/judge-details.json" \
      "$LOG/judge_unavailable"
fail_closed
trap 'sandbox_stop; [ "$PUBLISHED" = 1 ] || fail_closed; [ -z "${PRIV:-}" ] || rm -rf "$PRIV"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
# The private reference note, the gate and the rubric are read by the judge (root) only, never by the sandbox uid.
for private in /tests/reference /tests/.gate; do
    [ -d "$private" ] && [ ! -L "$private" ] && chown 0:0 "$private" && chmod 0700 "$private"
done
[ -f /tests/judge.toml ] && [ ! -L /tests/judge.toml ] && chown 0:0 /tests/judge.toml && chmod 0600 /tests/judge.toml

TESTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PY=/opt/rewardkit/bin/python
PRISTINE="${PRISTINE:-/opt/pristine}"
REPLAY="${REPLAY:-/opt/replay/replay}"
SCRATCH="${SCRATCH:-/opt/scratch}"
WORKSPACE="${WORKSPACE:-/app}"
GOTEST_TIMEOUT="${GOTEST_TIMEOUT:-420s}"
# Attempts per topology when the replay refuses its own verdict on a failed precondition.
REPLAY_ATTEMPTS="${REPLAY_ATTEMPTS:-4}"
# Root-only working files (test name lists, raw test streams), out of the sandbox's reach.
PRIV="$(mktemp -d)"

plan() { "$PY" -c '
import ast, json, sys
tree = ast.parse(open(sys.argv[1], encoding="utf-8").read())
for node in tree.body:
    if isinstance(node, ast.Assign) and getattr(node.targets[0], "id", "") == "PLAN":
        print(json.dumps(ast.literal_eval(node.value)[sys.argv[2]]))
        break
' "$TESTS/test_output.py" "$1" | tr -d '"'; }

REPORTS="$(plan reports_root)"
RAW="$(plan raw_root)"
PACKAGE="$(plan package)"
TREE="$(plan tree)"
SUBMITTED="${WORKSPACE}${TREE#/app}"

mkdir -p "$LOG" "$REPORTS" "$RAW" "$SCRATCH"
rm -rf "$REPORTS"/*.json "$SCRATCH/tree" "$SCRATCH/bin" "$SCRATCH/replay"

report() { "$PY" "$TESTS/report.py" "$@"; }
note() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$RAW/test.log"; }
mark() { printf '%s\t%s\n' "$1" "$(date +%s.%N)" >> "$RAW/timings.tsv"; }

# Digest of each step report as written; step 7 checks none changed before Reward Kit reads them.
declare -A REPORT_SHA=()
seal() { [ -f "$1" ] && REPORT_SHA["${1##*/}"]="$(sha256sum "$1" | cut -d' ' -f1)"; }

# ---- The note is taken before any sandbox process exists ----
# Root copies the note into $PRIV now, and step 6 installs that copy where the judge reads it, so nothing the
# sandbox uid runs can change what is judged. Every path component is opened without following a symlink; only a
# regular file of at most NOTE_CAP bytes is taken, and anything else is taken as an empty note (no note).
NOTE_CAP=65536
NOTE="$(plan note)"
SUBMITTED_NOTE="${WORKSPACE}${NOTE#/app}"
TAKEN_NOTE="$PRIV/note.md"
take_note() { "$PY" -c '
import errno, os, stat, sys
src, dst, cap = sys.argv[1], sys.argv[2], int(sys.argv[3])
data, found = b"", ""
fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
try:
    parts = [part for part in src.split("/") if part]
    for part in parts[:-1]:
        nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
        os.close(fd)
        fd = nxt
    leaf = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
    with os.fdopen(leaf, "rb") as handle:
        if not stat.S_ISREG(os.fstat(handle.fileno()).st_mode):
            found = "the entry at %s is not a regular file; it counts as no note" % src
        else:
            data = handle.read(cap + 1)
            if len(data) > cap:
                data, found = b"", "the note at %s is larger than %d bytes; it counts as no note" % (src, cap)
            else:
                found = "note present at %s (%d bytes)" % (src, len(data))
except OSError as problem:
    if problem.errno == errno.ELOOP:
        found = "the entry at %s is a symlink or is reached through one; it counts as no note" % src
    else:
        found = "no note at %s (%s); an empty one stands in, so the judged channel is a defined 0" % (src, problem.strerror)
finally:
    os.close(fd)
with os.fdopen(os.open(dst, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as handle:
    handle.write(data)
print(found)
' "$SUBMITTED_NOTE" "$TAKEN_NOTE" "$NOTE_CAP"; }
note "note taken: $(take_note 2>> "$RAW/test.log" || echo "failed; step 6 installs an empty note")"
[ -f "$TAKEN_NOTE" ] || : > "$TAKEN_NOTE"

chown "$SANDBOX_UID:$SANDBOX_UID" "$SCRATCH"
sandbox mkdir -p "$SCRATCH/bin"

# ---- Step 1: stage the submission; its own test files are replaced by the project's corpus ----
mark stage-begin
# Anti-cheat: static checks read the submission's bytes before any of it is copied, built or run.
CHEAT_STATIC="$(report cheat-static --source "$TESTS/test_output.py" --tree "$SUBMITTED")"
note "cheat-static $CHEAT_STATIC"
stage_rc=0
stage_detail=""
if [ -L "$SUBMITTED" ] || [ ! -d "$SUBMITTED" ]; then
    stage_rc=1
    stage_detail="the submission carries no directory at $TREE (a symlink there is not followed)"
else
    # Staged by root, so a file only its owner can read still stages; cp -a never dereferences a symlink and
    # the target does not exist yet, so nothing is written outside $SCRATCH/tree. The copy then belongs to the
    # sandbox uid; device nodes, FIFOs and sockets are not source and are dropped.
    sandbox_stop
    rm -rf "$SCRATCH/tree"
    { cp -a "$SUBMITTED" "$SCRATCH/tree" \
        && find "$SCRATCH/tree" \( -type b -o -type c -o -type p -o -type s \) -delete \
        && chown -hR "$SANDBOX_UID:$SANDBOX_UID" "$SCRATCH/tree" \
        && chmod -R u+rwX "$SCRATCH/tree"; } || stage_rc=$?
    if [ "$stage_rc" -eq 0 ]; then
        removed="$(sandbox find "$SCRATCH/tree" -name '*_test.go' -type f -print -delete | wc -l | tr -d ' ')"
        sandbox rm -rf "$SCRATCH/tree/.git"
        restored="$( (cd "$PRISTINE" && find . -name '*_test.go' -type f -print) | wc -l | tr -d ' ')"
        (cd "$PRISTINE" && tar cf - .) | (cd "$SCRATCH/tree" && sandbox tar xf -) || stage_rc=$?
        stage_detail="deleted $removed submitted test files, restored $restored project test files"
    fi
fi
note "stage rc=$stage_rc $stage_detail"
report plain --step stage --exit-code "$stage_rc" \
    --reason "the submitted tree could not be staged for grading" \
    --detail "$stage_detail" --out "$REPORTS/stage.json"
seal "$REPORTS/stage.json"
mark stage-end

# ---- Step 2: build (a build failure is a defined zero with a reason) ----
mark build-begin
build_rc=0
if [ "$stage_rc" -ne 0 ]; then
    build_rc=1
    build_detail="skipped: the tree was never staged"
else
    (cd "$SCRATCH/tree" && sandbox env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY go build ./... \
        && sandbox env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY go vet "$PACKAGE") > "$RAW/build.log" 2>&1
    build_rc=$?
    build_detail="$(tail -c 2000 "$RAW/build.log" | tr '\n' ' ')"
fi
note "build rc=$build_rc"
report plain --step build --exit-code "$build_rc" \
    --reason "the staged package did not build or did not pass vet" \
    --detail "$build_detail" --out "$REPORTS/build.json"
seal "$REPORTS/build.json"
mark build-end

# ---- Steps 3-4: fail-to-pass, then pass-to-pass tests, reduced from `go test -json` ----
run_tests() {
    local step=$1 constant=$2 out=$3
    local names="$PRIV/$step.names" pattern
    report names --source "$TESTS/test_output.py" --constant "$constant" > "$names"
    # One anchored alternation of the parent test names: Go selects subtests by
    # their parent, and the reducer scores the leaves.
    pattern="$(cut -d/ -f1 < "$names" | sort -u | paste -sd'|' -)"
    mark "$step-begin"
    if [ "$build_rc" -ne 0 ]; then
        report gotest --step "$step" --names "$names" --exit-code 2 \
            --out "$out" --raw "$RAW/$step.jsonl" < /dev/null
        seal "$out"
        note "$step skipped: the tree did not build"
        mark "$step-end"
        return 0
    fi
    (cd "$SCRATCH/tree" && sandbox env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY go test -json -count=1 \
        -timeout "$GOTEST_TIMEOUT" -run "^(${pattern})\$" "$PACKAGE") > "$PRIV/$step.jsonl" 2> "$RAW/$step.stderr"
    local rc=$?
    report gotest --step "$step" --names "$names" --exit-code "$rc" \
        --out "$out" --raw "$RAW/$step.jsonl" < "$PRIV/$step.jsonl"
    seal "$out"
    note "$step rc=$rc"
    mark "$step-end"
}
run_tests graded DISCRIMINATORS "$REPORTS/graded.json"
run_tests passtopass PASSTOPASS "$REPORTS/passtopass.json"

# ---- Step 5: replay against a server built from the staged tree, in both topologies ----
mark replay-begin
verdicts=()
server_rc=1
if [ "$build_rc" -eq 0 ]; then
    (cd "$SCRATCH/tree" && sandbox env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY go build -o "$SCRATCH/bin/nats-server" .) \
        >> "$RAW/build.log" 2>&1
    server_rc=$?
fi
# Copy one verdict out of the sandbox's scratch into a root-only file: every path component is opened without
# following a symlink, only a regular file of at most VERDICT_CAP bytes is taken, and anything else leaves no copy.
VERDICT_CAP="${VERDICT_CAP:-1048576}"
take_verdict() { "$PY" -c '
import os, stat, sys
root, rel, dst, cap = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
parts = rel.split("/")
fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
try:
    for part in parts[:-1]:
        nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
        os.close(fd)
        fd = nxt
    leaf = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
except OSError as problem:
    sys.exit("verdict not taken from %s/%s: %s" % (root, rel, problem.strerror))
finally:
    os.close(fd)
with os.fdopen(leaf, "rb") as handle:
    if not stat.S_ISREG(os.fstat(handle.fileno()).st_mode):
        sys.exit("verdict not taken from %s/%s: not a regular file" % (root, rel))
    data = handle.read(cap + 1)
if len(data) > cap:
    sys.exit("verdict not taken from %s/%s: larger than %d bytes" % (root, rel, cap))
with os.fdopen(os.open(dst, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as handle:
    handle.write(data)
' "$SCRATCH" "$1" "$2" "$VERDICT_CAP"; }
HELD="$PRIV/verdicts"
mkdir -m 0700 "$HELD"
while IFS= read -r line; do
    id="${line%%:*}"; rest="${line#*:}"
    timeline="${rest%%:*}"; nodes="${rest##*:}"
    dir="$SCRATCH/replay/$id"
    held="$HELD/$id.json"
    sandbox mkdir -p "$dir"
    attempt=0
    while [ "$server_rc" -eq 0 ] && [ "$attempt" -lt "$REPLAY_ATTEMPTS" ]; do
        attempt=$((attempt + 1))
        rm -f "$held"
        sandbox_stop
        sandbox rm -rf "$dir"; sandbox mkdir -p "$dir"
        sandbox env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY "$REPLAY" -server "$SCRATCH/bin/nats-server" \
            -timeline "$timeline" -nodes "$nodes" -seed "$(plan replay_seed)" -load "$(plan replay_load)" -out "$dir" \
            > "$RAW/replay-$id-$attempt.json" 2> "$RAW/replay-$id-$attempt.stderr"
        note "replay $id attempt $attempt rc=$?"
        # The sandbox uid owns $dir: nothing it runs survives the replay, and root scores its own copy of the verdict.
        sandbox_stop
        take_verdict "replay/$id/verdict.json" "$held" 2>> "$RAW/replay-$id.refusals" \
            || note "replay $id attempt $attempt left no verdict root could take"
        # Retry only a replay that refused its verdict on a precondition; a real check failure is never retried away.
        if report usable --verdict "$held" 2>> "$RAW/replay-$id.refusals"; then
            break
        fi
        note "replay $id attempt $attempt refused its verdict"
    done
    [ "$server_rc" -ne 0 ] && note "replay $id skipped: no server binary"
    verdicts+=(--verdict "$id=$held")
done < <("$PY" -c '
import ast, json, sys
tree = ast.parse(open(sys.argv[1], encoding="utf-8").read())
for node in tree.body:
    if isinstance(node, ast.Assign) and getattr(node.targets[0], "id", "") == "PLAN":
        for one in ast.literal_eval(node.value)["topologies"]:
            print("{id}:{timeline}:{nodes}".format(**one))
        break
' "$TESTS/test_output.py")
report replay --step replay "${verdicts[@]}" --out "$REPORTS/replay.json"
seal "$REPORTS/replay.json"
# Nothing the submission started outlives its steps.
sandbox_stop
mark replay-end

# ---- Step 6: the note ----
# No sandbox process is left. Whatever is now at the note path (a file, a symlink, which could point the judge at
# the reference note, or any other entry) is replaced by root's copy taken before step 1, as a root-owned 0644
# regular file: the note as submitted when it was a regular file of at most NOTE_CAP bytes, else an empty one,
# which scores a defined 0.
mark note-begin
install_note() { "$PY" -c '
import os, shutil, stat, sys
src, dst = sys.argv[1], sys.argv[2]
data = open(src, "rb").read()
parts = [part for part in dst.split("/") if part]
fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
try:
    for part in parts[:-1]:
        try:
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
        except FileNotFoundError:
            os.mkdir(part, 0o755, dir_fd=fd)
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
        os.close(fd)
        fd = nxt
    leaf = parts[-1]
    try:
        if stat.S_ISDIR(os.stat(leaf, dir_fd=fd, follow_symlinks=False).st_mode):
            shutil.rmtree(leaf, dir_fd=fd)
    except FileNotFoundError:
        pass
    part = ".%s.%d" % (leaf, os.getpid())
    out = os.open(part, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644, dir_fd=fd)
    with os.fdopen(out, "wb") as handle:
        os.fchown(handle.fileno(), 0, 0)
        os.fchmod(handle.fileno(), 0o644)
        handle.write(data)
    os.replace(part, leaf, src_dir_fd=fd, dst_dir_fd=fd)
finally:
    os.close(fd)
print("%s now holds the note taken before step 1 (%d bytes, root-owned, 0644)" % (dst, len(data)))
' "$TAKEN_NOTE" "$1"; }
for target in "$NOTE" "$SUBMITTED_NOTE"; do
    note "$(install_note "$target" 2>> "$RAW/test.log" || echo "the note taken before step 1 could not be installed at $target")"
    [ "$SUBMITTED_NOTE" = "$NOTE" ] && break
done
mark note-end

# ---- Step 7: CTRF report for the declared cheat checks (no reward weight) ----
mark ctrf-begin
# reward_files_untouched requires no reward carrier before Reward Kit runs other than this script's own fail-closed
# ones, byte-for-byte as written; they stay in place, so a reward file exists throughout, and a carrier anything
# else wrote is still seen.
expect=()
for name in "${!REPORT_SHA[@]}"; do
    expect+=(--expect "$name=${REPORT_SHA[$name]}")
done
report ctrf --source "$TESTS/test_output.py" --static "$CHEAT_STATIC" \
    --reports "$REPORTS" --logs /logs/verifier "${expect[@]}" \
    --fail-closed "reward.json=$FAIL_CLOSED" --fail-closed "reward.txt=0.0" \
    --out /logs/verifier/ctrf.json
note "ctrf rc=$? $(tr -d '\n ' < /logs/verifier/ctrf.json 2>/dev/null)"
fail_closed
mark ctrf-end

# ---- Step 8: Reward Kit, through the judged admission gate (a failed gate scores via tests/.gate/zero.toml) ----
# Fallback only: task.toml [verifier.env] already sets LITELLM_DROP_PARAMS and LITELLM_LOCAL_MODEL_COST_MAP.
export LITELLM_DROP_PARAMS="${LITELLM_DROP_PARAMS:-True}"
export LITELLM_LOCAL_MODEL_COST_MAP="${LITELLM_LOCAL_MODEL_COST_MAP:-True}"
export OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    judge_mode=anthropic
    export REWARDKIT_JUDGE=
elif [ -n "${OPENAI_API_KEY:-}" ]; then
    judge_mode=openai
    export REWARDKIT_JUDGE=openai/gpt-6-astra
else
    judge_mode=none
    export REWARDKIT_JUDGE=
fi
note "reports: $(ls -1 "$REPORTS" | tr '\n' ' ')"
note "judge mode: $judge_mode"
mark rewardkit-begin

# Exit 0 when every judged component in a reward-details.json gave a vote: Reward Kit 0.2.4 records a judge call
# that timed out as an errored 0 and keeps the error only where no sample voted, so an error means no judgment.
judged_cleanly() { "$PY" -c '
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
' "$1" 2>> "$RAW/test.log"; }

# One authenticated request to the endpoint Reward Kit's litellm calls in this mode (the same base-URL variables,
# the same model), carrying the fixed text "ping" and nothing from the submission. It prints what happened, never
# the key, and exits 3 when no HTTP response came back, 4 on HTTP 401 or 403, 0 on any other response.
JUDGE_PROBE_TIMEOUT="${JUDGE_PROBE_TIMEOUT:-20}"
judge_probe() { "$PY" -c '
import os, sys, tomllib, urllib.parse
import httpx
mode, rubric, timeout = sys.argv[1], sys.argv[2], float(sys.argv[3])
if mode == "anthropic":
    model = tomllib.load(open(rubric, "rb"))["judge"]["judge"].removeprefix("anthropic/")
    url = os.environ.get("ANTHROPIC_API_BASE") or os.environ.get("ANTHROPIC_BASE_URL") or "https://api.anthropic.com/v1/messages"
    if os.environ.get("LITELLM_ANTHROPIC_DISABLE_URL_SUFFIX", "").lower() not in ("1", "true") and not url.endswith("/v1/messages"):
        url += "/v1/messages"
    headers = {"x-api-key": os.environ["ANTHROPIC_API_KEY"], "anthropic-version": "2023-06-01"}
    body = {"model": model, "max_tokens": 1, "messages": [{"role": "user", "content": "ping"}]}
else:
    model = os.environ["REWARDKIT_JUDGE"].removeprefix("openai/")
    base = os.environ.get("OPENAI_BASE_URL") or os.environ.get("OPENAI_API_BASE") or "https://api.openai.com/v1"
    url = base.rstrip("/") + "/chat/completions"
    headers = {"Authorization": "Bearer " + os.environ["OPENAI_API_KEY"]}
    body = {"model": model, "max_completion_tokens": 16, "messages": [{"role": "user", "content": "ping"}]}
host = urllib.parse.urlsplit(url).hostname or "the judge host"
try:
    status = httpx.post(url, headers=headers, json=body, timeout=timeout).status_code
except httpx.HTTPError as problem:
    print("no HTTP response from %s (%s)" % (host, type(problem).__name__))
    sys.exit(3)
print("HTTP %d from %s" % (status, host))
sys.exit(4 if status in (401, 403) else 0)
' "$judge_mode" "$TESTS/judge.toml" "$JUDGE_PROBE_TIMEOUT"; }

judged=0
unavailable=""
# When no judged reward is published: with no judge key at all, the reward is the compiled score alone; with a key
# set, the fail-closed 0 is published and no partial result (gate_closed only words the reason).
gate_closed=0
probe=""
probe_rc=0
if [ "$judge_mode" != none ]; then
  probe="$(judge_probe 2>> "$RAW/test.log")"
  probe_rc=$?
  note "judge probe ($judge_mode): ${probe:-no result} (rc=$probe_rc)"
fi
if [ "$judge_mode" = none ]; then
  unavailable="no judge key is set (ANTHROPIC_API_KEY and OPENAI_API_KEY are empty); the reward is the compiled score alone"
elif [ "$probe_rc" -eq 3 ]; then
  unavailable="the $judge_mode judge is unreachable: one probe request got no HTTP response ($probe); a judge key is set and no verdict was obtained, so the reward is 0"
elif [ "$probe_rc" -eq 4 ]; then
  unavailable="the $judge_mode judge rejected the credential: one probe request got $probe; a judge key is set and no verdict was obtained, so the reward is 0"
else
# A judge error leaves no reward, so the gate route is retried up to three times (60 s, then 120 s apart).
for attempt in 1 2 3; do
  rm -f /logs/verifier/reward.json
# Gate route begin: a gate reward of 1.0 runs the real grading; anything else scores through the zero route.
gate_scratch="$(mktemp -d)"
rewardkit /tests/.gate --output "$gate_scratch/reward.json"
if grep -Eq '"reward": 1(\.0+)?,?$' "$gate_scratch/reward.json"; then
  rewardkit /tests
else
  export JUDGED_GATE_VERDICT="$gate_scratch/reward-details.json"
  gate_route="$gate_scratch/route"
  mkdir "$gate_route"
  cp -R /tests/. "$gate_route/"
  cp /tests/.gate/zero.toml "$gate_route/reward.toml"
  cat > "$gate_route/judged_gate.py" <<'JUDGED_GATE'
import json
import os

import rewardkit as rk

with open(os.environ["JUDGED_GATE_VERDICT"], encoding="utf-8") as handle:
    VERDICT = json.load(handle)
FAILED = [
    str(row.get("id") or row.get("name"))
    for detail in VERDICT.values()
    for row in detail.get("criteria", [])
    if row.get("value", 0) <= 0
]


@rk.criterion(description="gate failed: {gates}")
def judged_gate(workspace, gates: str) -> float:
    return 0.0


rk.judged_gate(", ".join(FAILED) or "unattributed", name="judged_gate")
JUDGED_GATE
  rewardkit "$gate_route"
fi
# Gate route end
  if [ -d "$gate_scratch/route" ] && [ -s "$gate_scratch/reward.json" ] \
      && judged_cleanly "$gate_scratch/reward-details.json"; then
    gate_closed=1
  fi
  if [ ! -s "$gate_scratch/reward.json" ]; then
    note "gate judge wrote no verdict (attempt $attempt)"
    rm -f /logs/verifier/reward.json
  elif ! judged_cleanly "$gate_scratch/reward-details.json"; then
    note "gate judge gave no vote (attempt $attempt)"
    rm -f /logs/verifier/reward.json
  elif [ ! -s /logs/verifier/reward.json ]; then
    note "Reward Kit wrote no reward (attempt $attempt)"
  elif [ ! -d "$gate_scratch/route" ] && ! judged_cleanly /logs/verifier/reward-details.json; then
    note "judge gave no vote on the note (attempt $attempt)"
    rm -f /logs/verifier/reward.json
  else
    judged=1
    break
  fi
  [ "$attempt" -lt 3 ] && sleep $((attempt * 60))
done
if [ "$judged" -ne 1 ] && [ "$gate_closed" -eq 1 ]; then
  unavailable="the $judge_mode judge was reached and decided the gate closed, but returned no verdict on the note in 3 attempts; a judge key is set and no clean verdict was obtained, so the reward is 0"
elif [ "$judged" -ne 1 ]; then
  unavailable="the $judge_mode judge was reached but returned no clean verdict in 3 attempts; a judge key is set and no verdict was obtained, so the reward is 0"
fi
fi
mark rewardkit-end

# No judged reward. With no judge key at all: test_output.py beside a reward.toml holding only the `compiled` table
# of tests/reward.toml, minus the judged input it pins to 0, so its `compiled` key equals the `compiled` key of a
# judged run and its `reward` key is that same score, which is published. With a key set and no clean verdict,
# nothing is scored here: the fail-closed 0 is what is published.
SCORED="$LOG/reward.json"
if [ "$judged" -ne 1 ]; then
  printf '%s\n' "$unavailable" > "$LOG/judge_unavailable"
  note "judge unavailable: $unavailable"
  # No partial judge result is published: a failed attempt's details are removed.
  rm -f "$LOG/reward-details.json" "$LOG/judge-details.json"
  fail_closed
fi
if [ "$judged" -ne 1 ] && [ "$judge_mode" != none ]; then
  SCORED=""
elif [ "$judged" -ne 1 ]; then
  compiled_root="$(mktemp -d)"
  mkdir "$compiled_root/tests"
  cp "$TESTS/test_output.py" "$compiled_root/tests/test_output.py"
  if "$PY" -c '
import json, sys, tomllib
src, dst = sys.argv[1:3]
cfg = tomllib.load(open(src, "rb"))
spec = next(one for one in cfg["reward"] if one["name"] == "compiled")
kept = {name: weight for name, weight in spec["weights"].items() if name == "test_output"}
dropped = {name: weight for name, weight in spec["weights"].items() if name not in kept}
assert kept and all(weight == 0 for weight in dropped.values()), (kept, dropped)
weights = ", ".join("%s = %r" % (name, float(weight)) for name, weight in kept.items())
lines = []
for name in ("reward", "compiled"):
    lines += ["[[reward]]", "name = %s" % json.dumps(name), "aggregation = %s" % json.dumps(spec["aggregation"]),
              "weights = { %s }" % weights, ""]
lines += ["[scoring.test_output]", "aggregation = %s" % json.dumps(cfg["scoring"]["test_output"]["aggregation"])]
open(dst, "w", encoding="utf-8").write("\n".join(lines) + "\n")
' "$TESTS/reward.toml" "$compiled_root/tests/reward.toml" 2>> "$RAW/test.log" \
    && env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY rewardkit "$compiled_root/tests" --output "$compiled_root/reward.json"; then
    SCORED="$compiled_root/reward.json"
  else
    SCORED=""
    note "compiled-only scoring failed"
  fi
fi

# Publish: reward.txt first, then reward.json by rename; both hold the same number.
if [ -n "$SCORED" ] && value="$("$PY" -c '
import json, math, os, shutil, sys
src, out = sys.argv[1:3]
value = float(json.load(open(src, encoding="utf-8"))["reward"])
assert math.isfinite(value) and 0.0 <= value <= 1.0, value
with open(os.path.join(out, "reward.txt.part"), "w", encoding="utf-8") as handle:
    handle.write(f"{value!r}\n")
os.replace(os.path.join(out, "reward.txt.part"), os.path.join(out, "reward.txt"))
dst = os.path.join(out, "reward.json")
if os.path.realpath(src) != os.path.realpath(dst):
    details = os.path.join(os.path.dirname(src), "reward-details.json")
    if os.path.isfile(details):
        shutil.copyfile(details, os.path.join(out, "reward-details.json"))
    shutil.copyfile(src, dst + ".part")
    os.replace(dst + ".part", dst)
print(value)
' "$SCORED" "$LOG" 2>> "$RAW/test.log")"; then
  PUBLISHED=1
  note "published reward $value ($([ "$judged" -eq 1 ] && echo "judged, $judge_mode" || echo "compiled only, no judge key"))"
elif [ "$judged" -ne 1 ] && [ "$judge_mode" != none ]; then
  fail_closed
  PUBLISHED=1
  note "published the fail-closed reward 0.0: a judge key is set and no clean verdict was obtained"
else
  fail_closed
  note "no valid reward was computed; fail-closed zero written"
fi
