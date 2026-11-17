# Reference repair and grading of task 62e0ebab

The task asks for a repaired source tree in place at `/app/nats-server` (`instruction.md:47-56`) and a
root-cause and design note at `/app/diagnosis.md` (`instruction.md:58-79`). This file describes the defect,
the reference repair, what each grader check observes, and how the reward is computed. It is not shipped to
the agent.

## The defect

A durable push consumer running under flow control delivers a message before it has replicated its own
delivered state. After a consumer leadership change and a consumer reset, the consumer's delivery sequence
restarts at 1, so a stale delivery issued before the reset can carry the delivery number a new delivery needs.
Downstream, the source drops the new message as a duplicate and the mirror marks its sequence deleted, which
leaves interior gaps the origin still holds.

A second fault is in the consumer's reply to a reset request. In the unrepaired tree, `processResetReq` in
`server/consumer.go` builds that reply from live consumer state (`o.info()`) after the reset is applied and the
consumer lock is released. A delivery in that gap makes the reply overstate the reset point, and a mirror that
seeds its accounting from the reply skips interior messages the origin still holds. The gap exists for every
retention mode.

## The reference repair

`solution/solve.sh` embeds a patch and a note, checks the sha256 of each (`solve.sh:268-269`), applies the patch
to `/app/nats-server` with `git apply -p1`, writes the note to `/app/diagnosis.md` and checks it again
(`solve.sh:278`).

- The embedded patch hashes to `c920d0edc42d76b12dd19e2279f538bb2053eeecb0105d705d9a9634cd3a1113`.
  `solution/oracle.patch` is the same patch preceded by four comment lines, and hashes to
  `afcadb9e5b7353cc0c92d3a1bfd70c0d535ee9746dadbad5f1b31fcdb08b080c`. It is a trimmed copy of the
  production half of upstream merge 6e1022239e89 (pull request 8802).
- The embedded note hashes to `c6fd2aba993ac2b743144c86ac695817c03a69ffe40aaa9aa029294a3f9fa14f` and is byte
  for byte `tests/reference/diagnosis.md`, the reference the judge reads.

The patch has three parts, in two files:

1. **Hold deliveries while a reset is pending** (`server/consumer.go`). `getNextMsg` returns the new error
   `errResetPending` while the consumer has a reset of its own in flight (`len(o.rsm) > 0`), so a delivery
   proposed before the reset applies cannot mix into the delivered state after it. Observed by `R03`.
2. **Report the reset point in the reset reply** (`server/consumer.go`). The new `resetInfo(seq)` returns the
   consumer info with `Delivered` and `AckFloor` set to consumer sequence 0 and stream sequence `seq-1`, and
   `processResetReq` replies with it instead of `o.info()`. Delivery resumes before the post-reset info is
   gathered, so the reply carries the reset point itself. Observed by `R14`.
3. **Count each delivery once** (`server/stream.go`). `sourceInfo` gains `cseq`, the stream sequence of the
   last delivery counted towards `dseq`. `processInboundMirrorMsg` and `processInboundSourceMsg` count an
   older message only when its delivery number is exactly one past the last and its stream sequence is above
   `cseq`, so a stale pre-reset delivery is not counted again. A newer source message that is not the next
   delivery is retried from the next stream sequence instead of being dropped. `setupMirrorConsumer` and
   `trySetupSourceConsumer` seed `cseq` from the reset reply's `Delivered.Stream`. Observed by `R01` and `R02`.

## How the instruction maps to the checks

| Instruction | Checked by |
|---|---|
| Repair in non-test files; tests not weakened; the tree builds and `server` passes `go vet` (`instruction.md:49-54`) | `tests/test.sh` step 1 deletes every submitted `*_test.go` and restores `tests/pristine`; step 2 runs `go build ./...` and `go vet ./server`. A failed build skips every test, so every compiled criterion fails. |
| Keep internal names, signatures and struct fields (`instruction.md:56`) | The restored tests compile against them. |
| The note (`instruction.md:58-79`) | The judged gate `G01` and the judged criteria `O1`-`O14`. The 64 KiB cap is `NOTE_CAP` in `tests/test.sh`. |
| AC1 (`instruction.md:83-86`) | `R01`, `R02`, `R05`, `R14` |
| AC2 (`instruction.md:87-92`) | `R03` |
| AC3 (`instruction.md:93-94`) | `R04` |
| Constraints (`instruction.md:100-106`) | `R10` flow control, `R11` not recreated, `R12` duplicate stored once, `R13` stored messages unchanged |

The instruction names no routine, file or identifier the repair changes. It names only `/app/nats-server`,
`/app/diagnosis.md` and the `server` package.

## The compiled channel (`tests/test_output.py`)

Ten criteria of weight 5 each. `tests/test.sh` reduces every step to a JSON report under
`/logs/verifier/reports`, and each criterion reads those reports.

- `R01`, `R02`: the `Mirror` and `Source` subtests of `TestJetStreamDurableStreamSourcingStaleDeliveryAfterReset`
  (added to `tests/pristine/server/jetstream_test.go`). Each test feeds a stale delivery straight into
  `processInboundMirrorMsg` or `processInboundSourceMsg`, with a hand-made ack reply subject, and requires that
  it is not counted. So the downstream accounting must be repaired as well as the consumer.
- `R03`: `TestJetStreamConsumerResetPendingHoldsDeliveries` (added to
  `tests/pristine/server/jetstream_consumer_test.go`). On a three-server cluster, with the consumer lock held, it
  proposes a reset with a reply and calls `o.getNextMsg()`. The call must return an error, and the delivered
  sequence, the stream sequence and `numPending()` must not move. After the reset applies and 8 deliveries are
  made, every replica's stored delivered state must be 8/18. After a leader step-down, the new leader must
  continue at delivered sequence 9 and stream sequence 19. AC2 states this behaviour without naming the
  routine: the consumer's own selection of the next message to deliver is refused with an error, numbering
  resumes on every replica, and a later leader continues it.
- `R04`: all eleven pass-to-pass tests in `PASSTOPASS`.
- `R05`: the replay (`tests/workbench-go`, built into `/opt/replay/replay`) against a server built from the
  staged tree. It runs in two topologies, `n3` (three servers, timeline `stale-after-reset`) and `n1` (one
  server, `stale-after-reset-1n`), and requires `exactly_once_mirror`, `exactly_once_source`, `mirror_last_seq`
  and `no_deleted` in both.
- `R10`-`R13`: the replay's constraint checks `c1_flow_control_active`, `c2_not_recreated`,
  `c3_duplicate_refused` and `c4_stored_unchanged`, in both topologies.
- `R14`: `TestJetStreamMirrorResetWithDeliveriesInFlight`
  (`tests/pristine/server/jetstream_mirror_reset_inflight_test.go`, a file this task adds). A mirror sets up
  again against a durable flow-controlled consumer while deliveries are in flight. The test holds open the
  window between the reset being applied and being answered: on an interest stream, the reset walks the
  messages below its starting point under the stream lock with the consumer lock released, so holding the
  stream lock parks it there. Interest retention is only the device for this; AC1 names no retention mode.
  The assertion is at `jetstream_mirror_reset_inflight_test.go:173-174`: once `LastSeq >= 7`, the mirror must
  hold s4..s7 with `FirstSeq <= 4` and `NumDeleted == 0`. The test compiles on the unrepaired tree and needs
  no identifier the fix introduces (no `resetInfo`, no `errResetPending`).

**The compiled repair gate.** Every compiled criterion is ANDed with `GATE` in `tests/test_output.py`: the
relations `discriminator-mirror`, `discriminator-source` and `discriminator-pending` (the tests behind `R01`,
`R02` and `R03`). The other criteria are regression guards that already hold on an unrepaired tree, so without
the gate they would pay a submission that changed nothing. Below the gate the compiled channel scores 0.

The cheat checks in `tests/cheat-checks.json` (`CHEAT_CHECKS` in `tests/test_output.py`) are written to
`/logs/verifier/ctrf.json` and carry no reward weight.

## The judged channel

- **Root-cause gate `G01`** (`tests/.gate/judge.toml`, `samples = 3`, all-pass). It asks whether the note names
  this incident's genuine root cause rather than a look-alike. A gate reward of 1.0 lets `rewardkit /tests` run.
  Anything else takes the zero route: `tests/.gate/zero.toml` becomes the reward file, beside a `judged_gate`
  criterion that `tests/test.sh` writes and that scores 0. The run then scores 0.
- **Note criteria `O1`-`O14`** (`tests/judge.toml`). These are binary, with weights summing to 52, a weighted
  mean, `guard = "penalize"` and `samples = 3`. Reward Kit 0.2.4 judges each criterion three times and keeps the
  median of the votes that came back (the lower one when only two did). It records an error only when no
  sample voted. `O8` ends with "An equivalent mechanism of the submitted repair counts."
- Model: `anthropic/claude-sonnet-5-5`; `openai/gpt-6-astra` when only `OPENAI_API_KEY` is set. The private
  reference note is `tests/reference/diagnosis.md`. The reference note, the gate and `judge.toml` are
  root-only in the verifier image.

## How the reward is computed

- The compiled dimension: positive weights sum to 50, aggregated as a weighted sum with weight 1/(2*50) = 0.01,
  so a perfect compiled run contributes 0.5.
- The judged dimension: positive weights sum to 52, aggregated as a weighted mean (which divides by 52 inside
  the channel), with weight 0.5, so a perfect judged run contributes 0.5.
- A perfect reward is 1.0.
- The `compiled` key: the compiled dimension alone on a 0-1 scale, weight 1/50 = 0.02, with the judged
  dimension pinned to 0.0. A perfect compiled run scores 1.0 and an unrepaired tree 0.0. This is the published
  reward when no judge key is set.

Judge availability (`tests/test.sh` step 8):

| Case | Published reward |
|---|---|
| No key (`ANTHROPIC_API_KEY` and `OPENAI_API_KEY` both empty) | The compiled score at full scale; `judge_unavailable` says why |
| Key set; the one probe request gets no HTTP response, or HTTP 401/403 | 0.0: `{"reward": 0.0, "compiled": 0.0}` and `reward.txt` 0.0; `judge_unavailable` says why |
| Key set; judge reached, but no clean verdict in three attempts (60 s, then 120 s apart) | 0.0, the same; no partial judge details are published |
| Clean verdict, gate closed | 0, through the zero route |
| Clean verdict, gate open | `0.01 x compiled weights held + 0.5 x note weighted mean` (less any guard penalty), as Reward Kit wrote it |

`tests/test.sh` writes the fail-closed zero to `reward.json` and `reward.txt` before grading starts, and an EXIT
trap writes it again on any path that publishes nothing else.

## Routes that look right and fail

- Turn flow control off on the durable consumer so the reset race cannot be reached. Rejected by `R10`.
- Delete and recreate the consumer, the mirror or the source to re-sync them. Rejected by `R11`.
- Accept every delivery without checking its sequence, so that no new message is ever called a duplicate.
  Rejected by `R12`.
- Purge, truncate or re-read a downstream stream so that its gaps disappear. Rejected by `R13`.
- Add a passing test of your own in place of the repair. Submitted test files are deleted at staging.
- Restart the servers and declare the deployment healthy. Rejected by `R01`.
- Repair only the consumer, or only the stream, and leave the other side as it was. Rejected by `R01`/`R02` or
  `R03`, and through the compiled repair gate by every compiled criterion.
- Write a note that names a look-alike cause, or no cause at all. Rejected by the root-cause gate `G01`, which
  scores the run 0.

## Upstream hunks this reference leaves out

Each upstream production hunk that the reference patch leaves out is listed below with the reason. The `hNN`
labels only name the hunks in this list.

- `h05` in `server/consumer.go`, `loopAndGatherMsgs`: the branch `if err == ErrStoreEOF` also takes
  `|| err == errResetPending`. Left out because it changes no behaviour: both branches reach
  `goto waitForMsgs`, and only a log line differs.
- `h06` in `server/consumer.go`, `loopAndGatherMsgs`: `o.processWaiting(err != errMaxAckPending)` also excludes
  `errResetPending`. Left out because it reaches only pull consumers (inside `if o.isPullMode()`), which the
  task does not cover.
- `h07` in `server/jetstream_cluster.go`, `processClusterCreateConsumer`: the `else if canRespond` reset reply
  is built with `resetInfo(..)` instead of `o.info()`. Left out as dead code on the live path:
  `resetStartingSeqLocked` returns `canRespond=false` on the clustered path.
- `h08` in `server/jetstream_cluster.go`, `applyConsumerMsgOp`: the leader's `resetSeqOp` reply is built with
  `o.resetInfo(sseq)` instead of `o.info()`. Left out because its only window is lock-free and no deterministic
  check separates it.
- `h09` in `server/jetstream_cluster.go`, `processConsumerLeaderChangeWithAssignment`: the sourcing-consumer
  reset reply is built with `resetInfo` instead of `o.info()`. Left out because it reaches only R1
  non-durable sourcing consumers, which the task does not cover.
- `h12` in `server/stream.go`, `processInboundMirrorMsg`: the `errLastSeqMismatch` rollback also restores
  `mset.mirror.cseq = ocseq`. Left out as dead on the live path: `retryMirrorConsumer` clears the mirror's
  consumer name and the next setup reseeds the value, so the rolled-back value is never read.

So the reference changes `server/consumer.go` and `server/stream.go` and leaves `server/jetstream_cluster.go`
as it is.

## Where the repair lands

These are the `+++ b/` headers of the patch embedded in `solution/solve.sh`. Each is a file the environment
image carries under `/app/nats-server`. Neither is named in `instruction.md`.

- `server/consumer.go`
- `server/stream.go`
