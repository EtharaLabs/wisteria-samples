# Ground truth: Openraft, leader restart reuses a log id

## Source

- Repository: https://github.com/databendlabs/openraft (MIT OR Apache-2.0; the GitHub record names
  Apache-2.0). `solution/upstream-repo.json` is the retained `gh api repos/databendlabs/openraft`
  response; its SHA-256 is the task's `fork_ancestry_digest`.
- Issue 2167 (2026-10-17): with leader restore enabled, a leader that crashes before flushing an
  entry it already replicated restarts, resumes leadership in the same term and writes a different
  entry under the same log id; followers diverge. Reporter's figures (0.10.0-alpha.35): 4 of the
  first 100 simulation seeds diverged; a same-log-id/different-entry check fired in 9 of 200 seeds;
  no divergence in 10,000 seeds with leader restore disabled; unchanged in alpha.37.
- Fix: pull request 2236, "fix: engine: re-elect former leaders during startup", squash-merged
  2026-10-30T04:45:17Z as `401ea02d3f10ab405bed509916a808e276743fe9`. Closes issue 2167.
- State A (task tree): `3c0144e34d156ed1f8e7c6f6940ef1dbb063cfb3`, the merge commit's only parent
  (workspace version 0.10.0-alpha.37).
- State F (oracle): State A plus `solution/fix.diff`, the pull request's diff restricted to its six
  non-test files (`openraft/src/config/config.rs`, `openraft/src/engine/engine_impl.rs`, four pages
  under `openraft/src/docs/faq`), unmodified.
- Transport check: all 20 files of the pull request fetched at A and at F; the six non-test diffs
  sliced from `gh pr diff 2236` equal `solution/fix.diff` byte for byte; it applies cleanly to A
  (`git apply`, GNU `patch -p1`) and yields all six files byte-identical to F. `solve.sh` checks the
  same digests.

## Mechanism

At start-up, a node whose stored vote is its own committed leader vote restored itself as leader in
the same term when `enable_leader_restore` was on (the default), without an election. Entries it
had replicated but not flushed were lost from its log, so it could propose a different entry under
the same log id. The fix always demotes that vote in memory to non-committed (the stored vote is
left alone), computes the follower state, and then, if the node was leader, leader restore is on and
membership lets it campaign, starts an election at once in term T+1. Winning appends a blank entry in
the new term, so no log id of term T is ever proposed again. With restore off the node starts as a
follower and waits for normal election timing. config.rs only changes documentation (and adds a
`since` attribute).

A naive partial fix (leaving the vote demoted but never campaigning, as if restore were always off)
was built as state TP and is still caught: it fails 6 of the 7 fail-to-pass tests (all but
`issue_881_transient_state_machine`), 2 of 2 runs.

## Code states

Measured on an aarch64 host in `rust:1.99.0-slim-trixie@sha256:2752b332...` with the tree's
`nightly-2026-03-01` toolchain, offline against the vendored crates of the tree's Cargo.lock
(sha256 463a0e21...), container `--cpus 4 --memory 8g --memory-swap 8g`, `CARGO_BUILD_JOBS=4`,
`cargo test --locked -p openraft -p tests --tests --no-fail-fast`, 933 test results per run.

| State | Tree | Runs | Exit | Fail-to-pass (7) | Pass-to-pass (918) |
|-------|------|------|------|------------------|--------------------|
| A | 3c0144e + F's test files | 3 | 101, 101, 101 (64 to 68 s) | 0/7 each run | 918/918 each run |
| F | 401ea02 | 5 | 0 x5 (55 to 57 s) | 7/7 each run | 918/918 each run |
| TP | A + F's test files, startup campaign disabled | 2 | 101, 101 | 1/7 each run | 918/918 and 918/918 (client_reads, left out, failed in run 2) |

An earlier vet at 8 CPUs without a memory cap (3 A runs, 5 F runs, logs kept) gave the same
fail-to-pass set and showed the flaky tests below.

## Graded tests

Fail-to-pass (all in the tests package; each changed or added by the fix; no fix-added symbol):

- `life_cycle::t50_leader_restart_cluster_committed_not_restored::leader_restart_cluster_committed_not_restored`
- `life_cycle::t50_single_leader_restart_re_apply_logs::single_leader_restart_re_apply_logs`
- `life_cycle::t50_wait_for_recovery::wait_for_recovery_times_out_without_quorum`
- `life_cycle::t90_issue_2167_leader_restore_reuses_log_id::issue_2167_leader_restore_reuses_log_id`
- `life_cycle::t90_issue_2167_leader_restore_reuses_log_id::leader_restore_rejected_by_newer_log`
- `life_cycle::t90_issue_881_transient_state_machine::issue_881_transient_state_machine`
- `membership::t23_custom_payload::custom_payload_survives_membership_change_and_recovery`

Five of these are existing tests whose expectations the fix changed (restart without quorum,
single-node restart, recovery wait, snapshot recovery, custom payload). Each change is a behaviour
of the restarted former leader that instruction.md states (rules 1 to 5): new term, Candidate
without quorum, no leader shown, refused writes, unchanged log, recovery wait timing out, blank entry
in the new term raising the applied index by one. They use only public API and existing fixtures,
so they were kept. instruction.md's Rules section describes, by behaviour and without naming any
test or test file, the existing tests that expect the earlier start-up behaviour and need not pass
(mapping below); the other test of `t50_wait_for_recovery`, `wait_for_recovery_recovers_state_machine`,
is pass-to-pass and is outside that description (it restarts a follower, the case the Rules bullet
gives as one that must still pass). Two changed tests (`t50_leader_restart_leader_restore_disabled`,
`t90_issue_607_single_restart`) pass on A as well and are pass-to-pass in their graded versions.

Measured 2026-11-13 in the Kaniko-built environment image, as uid 10001, offline, with
`solve.sh` applied: the tree's own version of `t90_issue_607_single_restart::single_restart`
writes right after the restart without waiting for a leader and failed in 36 of 36 runs (30
alone, 6 within the whole test binary; `ForwardToLeader` with no leader yet; it passed 5 of 5
runs on the unpatched tree); its graded
version waits for `Leader` first. instruction.md's Rules description therefore covers it too. The
tree's versions of `t90_issue_881_transient_state_machine` and `t23_custom_payload` passed with the
reference (26 of 26 and 24 of 24 runs); they stay covered because their graded versions differ (they
expect the new blank entry). One full `cargo test -p openraft -p tests --tests` run with the
reference failed exactly the three not-run startup unit tests and the four life_cycle tests above
(607, cluster committed, re-apply logs, recovery timeout); every other test passed.

The Rules description and the tests each clause covers,
checked against the tree's sources of every test that starts or restarts a node:

| Clause | Tests covered (tree versions) |
|--------|-------------------------------|
| expects a former leader to resume leadership in its old term when it starts, e.g. leader again right after a restart without a quorum | `startup_test::{test_startup_as_leader_without_logs, test_startup_as_leader_with_proposed_logs, test_startup_leader_removed_from_effective_membership}` (Leader with the old committed vote right after `startup()`); `t50_leader_restart_cluster_committed_not_restored`, `t50_wait_for_recovery::wait_for_recovery_times_out_without_quorum` (wait for `Leader` with both followers stopped) |
| expects a former leader to accept a client write right after a restart, without waiting for leadership | `t90_issue_607_single_restart::single_restart` |
| restore enabled; expects the applied index after the restart to be exactly the last index from before it (no new blank entry) | `t50_single_leader_restart_re_apply_logs`, `t90_issue_881_transient_state_machine`, `t23_custom_payload` |

Outside every clause (pass-to-pass, each must still pass): `wait_for_recovery_recovers_state_machine`
(restarts a follower), `t50_leader_restart_leader_restore_disabled`, `t90_issue_2232_self_notifications`
`elect_after_restart_with_capacity_one`, `t12_build_run`, the two restarts in
`t90_issue_1892_inconsistent_snapshot` and `t90_issue_2091_finalization_recovery` (restore set to
`false`), `test_startup_as_leader_leader_restore_disabled` and the follower, learner, candidate and
issue-920 start-up unit tests (not a former leader, or restore off), `t50_single_follower_restart` (its
stored vote is rewritten as a non-committed vote, and it waits for `Leader` before writing),
`t50_leader_restart_clears_state` (restarted with empty storage), `t10_learner_restart` (a learner),
`t20_startup_snapshot` (checks a snapshot, not the applied index) and `t50_append_entries_backoff_rejoin`
(applied index at least, not exactly). A clause for "unit tests that expect start-up to emit a
particular sequence of internal commands" was not used: it would also cover the pass-to-pass start-up
unit tests that assert an empty command list, and the three left-out unit tests are already covered
by the first clause.

Rule 6 states the behaviour `t50_leader_restart_leader_restore_disabled` checks: Follower, and the
stored vote and the metrics vote (the last vote written to storage) both the committed vote of
term T. Rule 6 says nothing about the vote the node holds in
memory. The graded version of the pass-to-pass unit test
`test_startup_as_leader_leader_restore_disabled` checks that this in-memory vote is the
non-committed vote of term T (`vote_ref() == Vote::new(2, 2)`) and that start-up emits no
command; it is covered by rule 7 and by the Rules bullet that every other existing test must
still pass.

Pass-to-pass: the other 918 tests of the openraft and tests packages that passed in every A and F
run (listed in `tests/graded/manifest.json`).

## Left out (not run)

- `openraft::engine::tests::startup_test::{test_startup_as_leader_without_logs,
  test_startup_as_leader_with_proposed_logs, test_startup_leader_removed_from_effective_membership}`:
  changed by the fix and fail on A, but they are white-box unit tests of the crate-private engine
  that pin the exact internal command sequence emitted at start-up (save vote, then send vote), a
  design choice a fair statement would have to dictate. The F file is still placed so the crate's
  tests compile against a fixed engine. instruction.md's Rules description covers these three (in
  the tree each expects the former leader to be leader in its old term right after `startup()`), so
  they need not pass and their command sequence need not be reproduced.
- Flaky: `metrics::t10_metrics_recorder::test_metrics_recorder_all_fields` (failed 2 of 3 A and 2
  of 5 F runs in the first vet), `membership::t51_hung_follower_blocks_loop::remove_hung_follower_must_not_block_raft_core_loop_2`
  (failed 1 of 3 A runs in the first vet), `client_api::t11_client_reads::client_reads` (failed 1 of
  3 A runs and 1 of 2 TP runs at the verifier limits).
- Ignored upstream: `append_entries::t90_issue_216_stale_last_log_id::stale_last_log_id`,
  `state_machine::t10_total_order_apply::total_order_apply`.
- Outside the graded packages: `tests-turmoil/src/scenarios/leader_restore_test.rs` (turmoil
  simulation crate) and `examples/raft-kv-rocksdb/tests/cluster/test_snapshot_recovery.rs`.

## Scoring

Version 1.1.2 grades two deliverables and combines them without any gate.

Test half (`test_output`, `tests/test_output.py`). The verifier takes the solver's changed non-test
`.rs` files under `openraft/src`, lays them over a pristine State A, places the F versions of the 10
graded test files (digest-checked), builds the openraft and tests test targets offline (limit 1800
s; killed means infrastructure fault, no reward), runs everything once skipping the 8 left-out
tests, then the 7 fail-to-pass tests twice more. A graded test without a result in a run is rerun
once by exact name; still nothing means failure with reason `no_result`. The score is 0 on a build
failure, any cheat check hit (`cheat_test_files_untouched`, `cheat_no_process_exit`,
`cheat_no_external_network`, reported in ctrf.json) or any failing pass-to-pass test; otherwise it
is the share of the 7 fail-to-pass tests that passed in all 3 runs. The cheat checks
(`tests/.cheat/run.py`) compare each taken file with its pristine version and fire only when the
file gains code or string literals of a kind the last Rules bullet of instruction.md forbids.

Judged half (`judge`, `tests/judge.toml`): the note at `/app/diagnosis.md` is judged against
`tests/reference/diagnosis.md` on 14 binary criteria (weights 5, 3 or 1, total 42), each criterion
asked 3 times (`samples = 3`), the median of the samples that voted kept (the lower of the two
when only two voted), weighted mean of the criteria; guard
`penalize` scores the note 0 if it addresses the evaluator. A missing note is replaced by an empty
file before Reward Kit runs, so it scores 0; so is a note larger than 65,536 bytes (instruction.md
states the limit as "The note must be at most 64 KiB (65,536 bytes)."; the reference note is 3786
bytes). The cap keeps the size of a note from making the judge calls fail.

`reward` = 0.5 x test half + 0.5 x judged half (weighted sum). `compiled` = test half alone. This
task has no gate. Solved only at 1.0. Expected: oracle 1.0 and compiled 1.0, nop 0.0.

Judge modes (`tests/test.sh`): with `ANTHROPIC_API_KEY` set the judge is `judge.toml`'s Anthropic
model; with only `OPENAI_API_KEY` set the same criteria are judged by an OpenAI model through
`REWARDKIT_JUDGE`; with neither key nothing is sent and the reward is the test half alone at full
scale (`reward` = `compiled`), with `/logs/verifier/judge_unavailable` saying why.

Judge-call errors: a judge call that times out scores that sample 0 with a warning, and a criterion
none of whose samples voted is recorded with an `error` (value 0) in `reward-details.json`; the
guard likewise. Any other judge-call error propagates out of Reward Kit 0.2.4, which then writes no
reward.json. `test.sh` accepts a judged attempt only when it wrote a reward and its
`reward-details.json` has no errored criterion and no guard error (`judged_cleanly`); otherwise it
retries, up to three attempts 60 s and 120 s apart. When a key is set and none of the three
attempts is accepted (judge unreachable, 401/403 or another HTTP error, a timeout, unusable output,
or a criterion or the guard without a verdict), the reward is 0.0: `reward.json` is
`{"reward": 0.0, "compiled": 0.0}`, `reward.txt` is `0.0`, `judge_unavailable` gives the reason
for each attempt, no attempt's partial result or `reward-details.json` is published, and `test.sh`
exits 0. A missing verdict therefore never scores above a judged one. An infrastructure fault (a
step killed at its limit, a graded-file digest mismatch, an unpinned Reward Kit) still ends the run
with the fail-closed 0 and exit 1, so the trial errors and is rerun.

## D2: the reference note and its sources

`tests/reference/diagnosis.md` (sha256 d261e9a4c11f1d502829f2a0c4143f31c1b3c7f6b7408629107508d0b4465a40,
identical to `solution/diagnosis.md`, which `solve.sh` copies to `/app/diagnosis.md` with a digest
check on both ends) is written only from the upstream record. Paragraph by paragraph:

1. "What goes wrong across the restart": issue 2167 body, steps 1 to 6 (X at index i replicated to
   F1, crash before flush, restore in term T, Y under log id (T-L, i), F1 keeps X by the log
   matching property, state machines diverge, nothing in Openraft notices).
2. "Why it happens": issue 2167 body (replication once submitted to the log store, before the
   leader's flush; restore resumes in the same term) and the PR 2236 description ("restoring the
   saved leader vote can reuse a log ID for different contents").
3. "The repair", first paragraph: PR 2236 diff of `engine_impl.rs` (`Engine::startup`: in-memory
   demotion, following state, `restore_leader && may_campaign` then `elect()`), its `config.rs`
   documentation (campaign at once, independent of `enable_tick`/`enable_elect`; log-freshness and
   leader-lease checks may reject it; higher term prevents reuse; blank entry in the new term), the
   PR description (win a higher-term election before accepting proposals; blank entry under the new
   leader ID) and the test `leader_restore_rejected_by_newer_log` added by the PR.
4. "The repair", second paragraph: PR 2172 thread, nremond 2026-10-26 (same-term re-election keeps
   the leader id; a follower down during that round can still hold the lost entry; only a new
   leader id, a new term, works), answering drmingdrmer's 2026-10-23 suggestion of a re-election
   round in the same term.
5. "Why the other start-up behaviours stay intact", first paragraph: the comment in PR 2236's
   `Engine::startup` (saving the non-committed vote would revert the stored vote, breaking
   monotonicity of vote storage and I/O progress tracking; no vote lies between, so the next save
   heals) and the PR description ("demote the former leader's vote in memory while preserving the
   stored vote").
6. Second paragraph: PR 2236 description ("preserving ... asynchronous append contract") and
   `config.rs` docs (`RaftLogStorage::append` may return before persistence; the callback reports
   durability); PR 2172 thread (drmingdrmer 2026-10-23 on keeping that contract; Phoenix500526
   2026-10-18 on not serialising persistence and replication; the reverted "replicate only flushed
   entries" approach).
7. Third paragraph: PR 2236 `config.rs` (`enable_leader_restore` defaults to true, the `since`
   note, behaviour when false, follower/learner/unfinished-candidate start-up unchanged,
   `save_committed` or `Raft::wait_for_recovery` before serving reads), the PR description
   (disabling leaves the node passive until a normal election; durable committed pointer or recovery
   wait), against the "default restore off" direction in issue 2167 and nremond's PR 2172 comment.

Look-alike criteria come only from the threads: C07 (same-term re-election or quorum probe, PR 2172
2026-10-23/26), C09 (durable-before-readable appends or holding replication until flush, PR 2172
and issue 2167's direction 2) and C14 (turning restore off by default, issue 2167 direction 3 and
nremond 2026-10-18 in PR 2172).

## Judge

`tests/judge.toml` (Reward Kit 0.2.4): `anthropic/claude-sonnet-5-5`, mode `individual` (one call
per criterion, plus the guard call), timeout 300 s, `files = ["/app/diagnosis.md"]` (Harbor 0.25.0
re-materialises each artifact at its original path in the separate verifier; `test.sh` also copies
`/logs/artifacts/app/diagnosis.md` there if needed), `reference = "/tests/reference/diagnosis.md"`
(absolute; Reward Kit resolves it against its working directory), weight 1.0, guard `penalize`,
`samples = 3`, `[scoring] aggregation = "weighted-mean"`. Every criterion checks one fact, names
`/app/diagnosis.md`, is at most 40 words and weighs 1, 3 or 5. No criterion annotation contains the
words Reward Kit treats as `negative` or `optional`.

In 5 evaluation passes over the reference note all 14 criteria said yes, and in 2 passes over an
empty note all 14 said no (98 calls through Reward Kit's own judge path, made before C08's last
sentence was added); no change was made to the reference to suit the judge. C08 asks for the
reference's mechanism (the former leader's vote demoted in memory only and the stored committed
vote not overwritten, so vote storage never moves backwards) and ends with "An equivalent
mechanism of the submitted repair counts.", so a repair that keeps vote storage from moving
backwards another way is not penalised. The reference note states the mechanism in its "Why the
other start-up behaviours stay intact" paragraph ("the stored committed vote is not
overwritten").

Harbor 0.25.0 does not upload `tests/` when `tests/` has its own Dockerfile: the verifier uses the
image's `/tests`. `tests/Dockerfile` therefore copies `judge.toml` and `reference/` into the image
(without them Reward Kit stops with `unknown [[reward]].weights inputs: 'judge'`). The image also
creates `/app` (root-owned, mode 0755), so `test.sh`, which runs as root, can put an empty note
there when the solver wrote none (otherwise the judge is shown `[not found]`). The sandbox uid
20002 that builds and runs the submission cannot write `/app`. `judge.toml` (0600) and `reference/`
(0700, note 0600) are root-only in the image, and `test.sh` sets the same modes again before any
submitted code runs, so the sandbox uid can read neither the
rubric nor the reference note. `test.sh` also settles the note before the first sandbox step: a
symbolic link or anything else that is not a regular file is removed and an empty note stands in,
and a regular file larger than 65,536 bytes is replaced by an empty note (the rule task b2740a36's
`take_note` uses).

## Verifier limits

`[verifier].timeout_sec` is 21600. Worst case, from the limits in the
files: the test steps run under `timeout --kill-after=30` with limits prepare 300 + graded 6600 +
cheat 300 = 7200 s, and a step killed at its limit ends the run at once (exit 1), so the judge only
runs after less than 7200 s. One judged attempt is 15 calls (14 criteria + guard) x 3 samples = 45
calls; Reward Kit 0.2.4 runs them all at once (its 8-slot LLM semaphore wraps each of the 3 samples,
and within a sample `_arun_llm_individual` starts all 15 calls together), so an attempt lasts as long
as its slowest call. One call is at most 3 requests (Reward Kit's `_MAX_JUDGE_RETRIES`, taken only
when a response does not match the schema; a timeout ends the call at once as an errored 0), each
with the 300 s `timeout` of judge.toml. In the Anthropic mode litellm 1.108.0 sends each request
once (its Anthropic chat path has no retry): 3 x 300 = 900 s per attempt. In the OpenAI mode
litellm hands each request to the OpenAI SDK, which tries it up to 3 times (`max_retries` 2),
waiting at most 120 s between tries (its cap on `Retry-After`): 3 x (3 x 300 + 2 x 120) = 3420 s per
attempt. Three attempts and the 60 s + 120 s pauses: 2880 s (Anthropic) or 10440 s (OpenAI). Total:
under 7200 + 10440 = 17640 s, plus a few seconds for the compiled-only Reward Kit run and the
bookkeeping; 21600 s (6 h) leaves about 3900 s of margin. In the measured runs the test steps
took about 2 minutes (oracle, 2026-11-13: graded step 121 s and 127 s), and a judged attempt a few
minutes. The verifier needs the network (allowlist `api.anthropic.com`, `api.openai.com`) and the
seven `[verifier.env]` keys of `task.toml`. Its resources (4 CPUs, 8192 MB, 30720 MB storage,
3600 s build) are sized for the Rust build and test run measured above.

## Verifier compatibility fix

Anthropic's API rejects the request litellm 1.108.0 builds for `claude-sonnet-5-5` with a
`json_schema` response format:

    HTTP 400 "output_format: This field is deprecated. Use 'output_config.format' instead"

Reward Kit 0.2.4 always passes a `json_schema` `response_format` (`rewardkit/judges.py`, about line
556). litellm sends it as the old `output_format` field whenever its model map marks the model
`supports_native_structured_output: true`, and its packaged map does so for `claude-sonnet-5-5`.
When the flag is false litellm uses a forced tool call instead, which the API accepts. Patch
releases 1.108.1 to 1.108.4 do not change this. `tests/Dockerfile` therefore, right after the
hash-locked install, opens litellm's packaged `model_prices_and_context_window_backup.json`, asserts
the flag is true for `claude-sonnet-5-5`, sets it false, writes the file and asserts the change; the
runtime stage asserts it again. `LITELLM_LOCAL_MODEL_COST_MAP` (`True` unless the runner sets it,
in `[verifier.env]`; `test.sh` sets `True` on the compiled-only run) makes litellm read only
that local map. No package is added or changed: litellm and its dependencies come only from the
existing hash lock `tests/.build/rewardkit.lock`. Proven in the rebuilt verifier image:
`acompletion` with a `json_schema` response format returns `{"answer": "OK"}` from
`claude-sonnet-5-5`, and the 98 judge calls described under Judge succeeded.
