# Ground truth: ethara/b4321f39-1196-5335-9087-ad4c9a0fba23, version 2.1.2

Test generation on nats-server `server/stree`: wildcard characters inside a token.

## Source

- Repository: github.com/nats-io/nats-server, Go module `github.com/nats-io/nats-server/v2`,
  Apache License 2.0, copyright The NATS Authors.
- Report: the issue text in `instruction.md` (taken from the description of pull request #8765):
  literal subjects such as `a*`, `*a` or `**` were sometimes treated as containing a wildcard.
- Fix: PR #8765, merge commit d163ae747ad050ea89054f8fb03c43e64e737aea, merged
  2026-10-30T13:28:26Z. The fix is public (`task.toml` `[metadata.contamination]`).
  - Base, shipped in the agent image: 6619d7f7d7211f37479d13a8a8cd104a6acbaed1 (the merge's first
    parent), commit time 1793366398 (2026-10-30T13:19:58Z). Both Dockerfiles fetch that commit
    alone, check the commit id, the commit time and the sha256 of the whole fetched tree
    (`03cca4de0f0d77c004c81df9abfcd705604221860b70a8ff812113555e865ec2`, 599 files), remove `.git`,
    and download the modules, each checked by `go.sum`.
  - The verifier image builds a pristine module at the same commit with `server/stree` non-test
    sources, `server/gsl`, `internal/antithesis`, go.mod, go.sum and LICENSE (`tests/Dockerfile`).
- Package under test: `server/stree`, `Match` and `MatchUntil` (`instruction.md`).

## Mechanism

A `*` or `>` byte left over after a partial match inside a longer token was taken for a
wildcard. The fix touched five independent places where this happened. Correct behaviour: only
a whole token `*` matches one token and only a last whole token `>` matches the rest; every
other byte is literal, in filters and stored subjects alike (`instruction.md` rule 1). Example
from the instruction: with `KV.a`, `KV.apple`, `KV.avocado`, `KV.banana` stored,
`Match("KV.a*")` reports `KV.apple` and `KV.avocado` on the base commit, though it should match
nothing.

This is a test-generation task: the agent writes `_test.go` files, which are graded against a
hidden pool of implementations, not against the upstream tests (`instruction.md`).

## Code states (the pool)

68 members in `tests/pool/`, each a modified copy of `parts.go` and `stree.go` from base or fix,
each file marked as modified.

| Kind | Count | Notes |
|---|---|---|
| correct (`c*`) | 8 | `c01_upstream` is the upstream fix unchanged apart from that marking. Correct members differ where the contract leaves behaviour open: `c03_ordered_scan`, `c04_reverse_children` and `c08_random_order_each_call` in callback order (rule 4), `c06_unspecified_inputs_differ` on unspecified inputs (rule 8) |
| incorrect (`i*`) | 60 | `i01_prefix_original` is the code before the fix; each incorrect member breaks at least one rule of `instruction.md` ("Every incorrect fix breaks at least one rule below") |

The reference solution accepts 8 of 8 correct members and rejects 60 of 60 incorrect members
(compiled score 1). The package's existing tests alone (no change) accept 8 of 8 correct members
and reject none of the incorrect ones (compiled score 0).

## Reference solution

`solution/wildcard_literal_test.go`, written for this task, compares `Match` and `MatchUntil`
against a token-level reference model over hand-picked and seeded random trees, node48 and
node256 widths, early stop at every count, value pointers, inserts and deletes, inputs past 256
bytes and 32 tokens, and concurrent and nested calls. Its random seeds are fixed.
`solution/solve.sh` installs it in `/app/server/stree` with mode 0644, requires it to be
gofmt-clean, runs `go vet ./server/stree/`, then runs `go test -race -count=1 ./server/stree/` on
the unfixed tree and requires that run to fail with only `TestWildcardCharInsideToken*` tests
failing.

## Graded tests (compiled score)

- The submission is every regular `*_test.go` file directly in `/app/server/stree` plus a
  `testdata/` tree; nothing else is collected (`tests/grader/main.go` `harvest`; `instruction.md`
  "Requirements for your tests").
- Each member: the submission is laid over the pristine module with the member's sources and
  compiled once with `-race`, as root (300 s cap). The binary then runs three times with
  `-test.shuffle` seeds 20261030, 8765, 6619 at GOMAXPROCS 1, 2, 4 (`runPlan`), each run with
  `-test.timeout=60s` and killed 20 s later if still alive, as uid 10002 with no supplementary
  groups and the fixed environment `PATH`, `HOME`, `TMPDIR`, `GOTRACEBACK` (`runOnce`).
- The run directory holds only the submission's test files and `testdata/`, in a root-owned
  directory of mode 0755 that uid 10002 cannot write; HOME and TMPDIR are its own (mode 0700). The
  implementation sources stay in the grader's state directory `/var/lib/grader` (mode 0700, uid
  10001), which uid 10002 cannot enter. So each requirement of `instruction.md` line 27 makes a
  violating test fail: reading the non-test sources (absent), writing into the package directory
  (read-only), needing a privilege (uid 10002), needing more than 2 CPUs (`[verifier.environment]
  cpus = 2`), a run over 60 s (timeout), a result that depends on the seed or GOMAXPROCS (the
  three runs disagree).
- A member counts as rejected only if all three runs fail and as accepted only if all three pass;
  a mixed result earns nothing (`instruction.md`). A test binary that does not compile counts as
  failing every run (`build_failed`).
- Order: `c01_upstream` first, then the other correct members, then the incorrect ones, each group
  shuffled per grading. When a correct member is not accepted, grading stops and the rest are
  recorded as `skipped`.

## Judged part (advisory)

- Input: before any submitted code runs, `tests/test.sh` copies every regular, non-symlink
  `*_test.go` file directly in `/app/server/stree` (the files the grader collects) whose name does
  not start with `.` or `_` (Go ignores those) and that is not byte-identical to the package's
  unchanged test file of the same name (`/opt/grader/pristine-tests`, from the same commit) into
  `/var/lib/grader-judge/server/stree` (created by root with mode 0700 inside
  `/var/lib/grader-judge`, uid 10001, mode 0700), so submitted code (uid 10002) can neither read
  nor change it. At most 262,144 bytes of a file and 393,216 bytes in all are shown, in name order;
  the list, sizes and hashes go to `/logs/verifier/judge-input.json`. When nothing is copied, the
  directory holds one empty marker file and every criterion scores 0. `testdata/` is not shown.
- Judge (`tests/judge.toml`): `anthropic/claude-sonnet-5-5` through Reward Kit, `mode =
  "individual"` (one call per criterion), `timeout = 300`, `samples = 3` (the median of three
  votes counts), `guard = "penalize"` (the guard asks whether the submission writes to the
  evaluator; a flagged submission scores 0 on this part), binary criteria, weighted mean. The judge
  prompt fences the submitted files in `<submission>` tags and tells the judge to treat their
  content as data. The private reference `tests/reference/judge-reference.md` restates the rules
  of `instruction.md`, says how to decide (any testing style counts), and describes how the
  reference suite meets each criterion; it names no pool member and no way an implementation fails.
- Criteria, each a requirement `instruction.md` already gives the solver (weight in brackets):
  - J1 `literal_chars_in_filters` (5) and J2 `literal_chars_in_stored_subjects` (5): rule 1, "a
    `*` or `>` inside a longer token ... matches only itself, in filters and in stored subjects
    alike".
  - J3 `real_and_literal_wildcards_mixed` (3): rule 1, "for filters that mix real wildcards with
    literal `*` and `>` characters".
  - J4 `long_subjects_and_filters` (3): rule 1, "for subjects and filters of any length and any
    number of tokens". The criterion uses that wording and sets no length or token threshold.
  - J5 `each_match_reported_once` (5): rule 2, "exactly once for each stored subject that the
    filter matches, and never for anything else", with rule 4's unspecified order. The criterion
    accepts any sound way of checking it (for example a comparison of the reported subjects with
    the expected ones as a collection, or a check of each callback against the expected matches
    and the subjects already reported plus a check of the number of calls).
  - J6 `match_until_stops` (3): rule 2's `MatchUntil` sentence. J7 `callback_receives_stored_value`
    (3): rule 3. J8 `results_after_updates` (3): rule 5. J9 `overlapping_calls` (3): rule 6.
  - The reference suite meets all nine.
- The judged part has weight 0 in the reward. It is a model's reading of the test source, three
  samples per criterion, and can differ between gradings of the same submission; the reward
  cannot.

## Grading

- reward = compiled (`tests/reward.toml`: `reward` weights test_output 1.0, judge 0.0). This is
  the rule `instruction.md` states, and it holds whether the judge runs, fails or is not called.
- compiled = incorrect members rejected / 60, and 0 unless every correct member is accepted
  (`tests/grader/main.go` `grade`; restated over the matrix in `tests/test_output.py`
  `pool_reward`).
- judged (advisory) = the weighted mean of J1..J9 (weights 5, 5, 3, 3, 5, 3, 3, 3, 3; total 33),
  each the median of three yes/no votes; 0 when the guard flags the submission. blended
  (advisory) = 0.85 x compiled + 0.15 x judged, for information only.
- Route (`tests/test.sh`): the first command writes `0` to `/logs/verifier/reward.txt`, and
  right after `set -uo pipefail` it writes `{"reward": 0}` to `reward.json`; then the judge's input is staged. With a non-empty
  `ANTHROPIC_API_KEY` (passed from the runner's environment by `task.toml` `[verifier.env]`, empty
  by default) it runs Reward Kit (harbor-rewardkit 0.2.4) over `/tests`, with its output in
  `rewardkit.json` and its per-criterion, per-sample detail in `reward-details.json`. The criterion
  `pool_discrimination` in `tests/test_output.py` runs the grader, which writes
  `/logs/verifier/grading.json`, and returns the matrix's reward after checking it against the
  restated rule; `tests/judge.toml` runs the judge. Reward Kit rounds every aggregate to four
  decimals, which cannot carry k/60, so `test.sh` recomputes compiled (`pool_reward`) and judged
  (from the votes and weights) exactly and checks them against Reward Kit's rounded values. It
  writes `{"reward"}` alone to `reward.json` and the same number to `reward.txt`; `judged.json`
  holds `judged`, `guard_flagged`, the per-criterion rows, `weight_in_reward` (0), `compiled` and
  `blended`. A compiled value that disagrees with Reward Kit's publishes 0.
- Judge skipped: when the key is empty, the judge input cannot be staged, Reward Kit fails (a
  judge call that errors makes it exit without a result), or any sample of any criterion or of the
  guard timed out, carries no yes/no vote or disagrees with Reward Kit's aggregate, the reward is
  the same compiled score (`tests/reward.deterministic.toml`), from this trial's `grading.json`
  when Reward Kit's grader run left one, otherwise from one grader run by `test.sh` (the same
  `grade` function). `reward.json` holds `{"reward"}` alone, and
  `/logs/verifier/judge-skipped.json` records `"judge_skipped": true`, a reason code
  (`no_judge_credential`, `judge_input_not_staged`, `judge_run_failed` or
  `judge_result_unusable`), the reason and the score. `test.sh` exits 0 on these paths.
- Failure paths that are not about the judge fail closed to 0: the grader cannot start, crashes or
  is killed (no `grading.json`: `matrix_missing`); the grader reports an error, for example an
  unreadable or missing submission directory, a submission over 32 MiB or a test binary that
  cannot be started (`grading.json` carries a `grader error` note); the matrix disagrees with the
  restated rule (`matrix_inconsistent`); the final step of `test.sh` fails (the zeros written
  first stay). A verifier timeout is recorded by the harness as an error, not a score.
- Cheat checks (`tests/cheat-checks.json`, reported in `/logs/verifier/ctrf.json`, no weight):
  `cheat_no_verifier_paths` (a harvested file names a verifier path), `cheat_reward_carrier_intact`
  (the reward directory and its files belong to the verifier alone and `grading.json` holds the
  score), `cheat_no_escaped_processes` (no uid 10002 process outlives the grader).

## Images and identities

- Agent image (`environment/Dockerfile`): `golang:1.26.10-bookworm` pinned by digest, the base
  tree at `/app` with its modules in the module cache, `GOPROXY=off`, `GOTOOLCHAIN=local`,
  `GOCACHE=/home/dev/.cache/go-build`, `go` and `gofmt` in `/usr/local/bin`, the pinned `codex`
  (@openai/codex 0.166.0) and `claude` (Claude Code 2.1.311) command-line tools and `rg` on the
  PATH with their web tools turned off, so an agent run needs no package download. User `dev`, uid
  1000, gid 1000, home `/home/dev`, owns `/app`; its build cache is warmed with
  `go test -race ./server/stree/`. `task.toml` `[agent].user = "1000"`.
- Verifier image (`tests/Dockerfile`): the same Go base, CPython 3.13 and Reward Kit from the
  hash-locked `tests/.build/rewardkit.lock`, litellm's packaged model map edited by
  `tests/.build/litellm_model_map.py` (`supports_native_structured_output` false for
  `claude-sonnet-5-5`, which litellm 1.108.0 would otherwise drive with a field the API refuses),
  the pristine module, the package's unchanged tests in `/opt/grader/pristine-tests`, the pool and
  the grader under `/opt/grader`. The Dockerfile has no heredoc, no `COPY --chmod`, and every use
  of a platform argument has a fallback.
- `tests/test.sh`, Reward Kit, the grader and the judge run as root (`task.toml`
  `[verifier].user = "root"`). As root the grader is privileged (`privileged`), so before grading
  it makes `/logs/verifier` root's with mode 0755, `/tmp` and `/var/tmp` root's with mode 1770,
  and `/logs`, `/dev/shm`, `/dev/mqueue` and `/run/lock` root's with mode 0755 (`lockDown`). It
  compiles as root with `GOCACHE=/var/cache/grader-go`, the cache uid 10001 warmed at build time.
  Each test binary runs as uid 10002; after each run every uid 10002 process is killed and its
  files in `/tmp`, `/var/tmp`, `/dev/shm`, `/run/lock` and `/var/lock` are removed. Uid 10002
  cannot read or execute the grader, the pool, the pristine module or `/tests` (root:10001, no
  access for others) and cannot enter `/var/lib/grader` or `/var/lib/grader-judge` (uid 10001, mode
  0700). Uid 10001 (`verifier`) owns those directories and the build cache and runs only the
  build-time warm-up. No file capabilities are set. The grader's code also handles a non-root start
  (by capabilities, or unisolated with `runner_isolated: false`); with the verifier running as root
  neither path is taken.
- Both images end on `USER root`, as the task runtime starts images that way.
- Network: the verifier may reach the judge's host only (`api.anthropic.com`, `[verifier]` and
  `[verifier.environment]`), so submitted test code, which runs in the verifier as uid 10002, can
  too. It runs with a fixed environment holding no credential, and uid 10002 cannot read the
  environment of root's processes.
- Limits: verifier timeout 20,700 s (the grader's worst case of 19,800 s plus 900 s for the
  judge's at most three attempts of 300 s), agent 3,000 s, 2 CPUs and 4096 MB for both images
  (`task.toml`).

## Known limits (behaviour that `instruction.md` does not state)

- Advisory judge: `instruction.md` does not mention that a model reads the submitted test files
  and reports a judged value beside the reward. That value has weight 0 in the reward. Each
  criterion restates a requirement the instruction gives, so the judged part asks nothing the
  instruction does not.
- The judged part sees only test files the solver added or changed: the package's unchanged
  existing tests, which the instruction says stay part of the suite, earn nothing there. A file
  whose name starts with `.` or `_`, `testdata/`, and bytes past the caps above are not shown.
- Submission size: the collected files may total at most 32 MiB; a larger submission is a grader
  error and scores 0 (`maxSubmissionBytes`, `harvest`).
- Compile cap: each member is compiled once with a 300 s cap. A compile that fails or is killed
  counts as failing every run: `build_failed`, which credits an incorrect member as rejected and
  leaves a correct member not accepted, so the compiled score is 0 (`gradeMember`).
- Race detector: some incorrect members are wrong only through unsynchronised shared state. A
  suite rejects them only when the race detector (or a wrong result caused by the race) shows up
  in all three runs, so credit for them can vary from one grading to the next and between
  architectures.
- Architecture: the 60 s run cap and 300 s compile cap were calibrated on linux/arm64, where the
  agent rollouts also ran; the reference solution scores 1 on native linux/amd64 as well. Under
  qemu user emulation the Go race runtime aborts at start-up with exit status 0, so an amd64
  grading needs a native amd64 host.
- Network: `instruction.md` says there is no network. The agent image runs with
  `[environment] network_mode = "no-network"`; the agent process itself may reach only the model
  hosts of `[agent]`; submitted tests in the verifier can reach `api.anthropic.com` (see
  "Images and identities").
- Without the judge key the reward is unchanged and only the advisory `judged` and `blended`
  values are missing; `judge-skipped.json` marks such a trial.

## Left out, with reasons

- Upstream regression tests of PR #8765 are not graded directly; the task grades the agent's
  tests against the pool instead (`instruction.md`).
- Inputs the contract leaves unspecified (empty tokens, a non-last `>`, stored subjects with a
  whole-token `*` or `>`) and callback order are not graded; correct members are built to differ
  there (`instruction.md` rules 4 and 8; `c06_unspecified_inputs_differ`,
  `c08_random_order_each_call`, `c03_ordered_scan`, `c04_reverse_children` in `tests/pool/`).
- Rules 7 (time) and 8 (unspecified inputs) and the section "Requirements for your tests" are not
  judged: they constrain what tests may rely on.

## Versions

- 2.1.2 (this bundle): `instruction.md` line 27 states the run requirements as requirements on the
  tests (they must not read the package's non-test source files, must not write into the package
  directory, must not need more than 2 CPUs or any privilege), and line 24 names
  `/app/server/stree/testdata/` and `/app/server/stree` by absolute path; the agent image's user is
  `dev` (uid and gid 1000 unchanged) with `GOCACHE=/home/dev/.cache/go-build`; `task.toml` gains
  `[metadata.contamination]`, and `[verifier.env]` passes `ANTHROPIC_API_KEY` (empty by default)
  and `LITELLM_LOCAL_ANTHROPIC_BETA_HEADERS`; comments, image labels, the cheat-check
  schema string and this file. The pool, the grader's logic, the run plan, the caps, the judge
  criteria and the reference suite are unchanged.
- 2.1.1: the judge became advisory (weight 0; reward = compiled on every path) and J4 and J5 took
  their current wording; the verifier runs as root and the grader drops submitted code to uid
  10002 (previously the verifier ran as uid 10001 with file capabilities on the grader);
  `reward.json` holds `{"reward"}` alone; the agent image gained the pinned command-line tools;
  `solve.sh` checks the suite on the unfixed tree.
- 2.1.0 added the judged part (then 0.15 of the reward). 2.0.0 reworked the form of the version 1
  Dockerfiles, moved the grading route to Reward Kit and moved the lock-down from `test.sh` into
  the grader. The pool, the reference suite and the grader's scoring rule are unchanged since
  version 1.
