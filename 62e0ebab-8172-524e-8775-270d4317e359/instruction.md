# Durable mirror and source fall behind the origin after routine consumer maintenance

You are handed a running message-broker deployment that has started losing data on its
replicas, and the source tree the broker was built from. Find the defect and repair it.

## The deployment

A hub of three servers holds one replicated, key/value-shaped stream, call it the origin.
A second account consumes the origin through a single durable **push consumer running under
flow control** — created with acknowledgement policy `flow_control`, `flow_control: true`,
`idle_heartbeat: 1s`, `max_ack_pending: 1000`, delivering from the first sequence. Through
that one consumer the second account feeds two downstream streams: a **mirror** and a
**source**. Writers append to the origin steadily, pausing ten milliseconds between
acknowledged writes.

## What happened

During ordinary maintenance an operator moved that consumer's leadership — one leadership
step-down — and then stopped and restarted the server that had been leading it. Nothing else
changed; no stream, consumer, mirror or source was deleted or recreated.

Afterwards the origin still holds every message it ever accepted. A downstream stream does
not. It reaches the origin's latest sequence while holding **fewer** messages, and the
missing ones are **interior** sequences — sequences in the middle that the downstream stream
now reports as deleted, while the origin still has them. For example, against an origin with
878 messages and nothing deleted, a downstream stream held 876 with sequences `[551, 552]`
reported deleted; in another run, 886 against 887 with `[796]` deleted.

It is intermittent. Under CPU load it appears in roughly half of the maintenance runs; on an
idle machine it has not been seen. No error is logged on either side when it happens, nothing
redelivers the missing messages, and both downstream streams keep accepting new writes as if
all were well.

During the same window the deployment also shows ordinary message redelivery, ordinary
duplicate suppression, and acknowledgement lag.

## What you have

- `/app/nats-server` — the broker's source tree at the exact commit this deployment runs.
  There is no version history and no remote: it is a plain tree, not a repository, so
  `git log` will not help you. There is no network; the Go toolchain and every module this
  tree needs are already in the local cache, so building and testing work offline and
  fetching does not.

## What to deliver

**D1 — the repaired source tree, in place at `/app/nats-server`.**

Make the repair in the production source, in non-test files: a change to a `*_test.go` file is
no part of the repair. The project's own tests are the specification of correct behaviour and
must not be weakened: editing, deleting or skipping a test is no part of the repair, and a test
you add does not stand in for the fix. The project's mirror, source, flow-control and
consumer-reset tests must pass on the repaired tree. The whole tree must build, and the
`server` package must pass `go vet`.

Keep every existing internal name, signature and struct field of the `server` package as it is.

**D2 — your root-cause and design note, as a Markdown file at `/app/diagnosis.md`
(a regular file, not a link).**

A thorough written analysis, written for a careful engineer who has the same deployment but
has not found the cause. It must:

1. name the root cause and the evidence that shows it, and trace the causal chain from the
   maintenance action to the interior downstream gaps;
2. for each of the other behaviours visible in that window — ordinary message redelivery,
   ordinary duplicate suppression, and ordinary acknowledgement lag — establish whether it
   causes the loss, and why;
3. justify why your repair takes the shape it does — what each part of it prevents, and why a
   narrower change would leave the defect in place;
4. explain why each of these common operational shortcuts does **not** fix it: restarting the
   servers; purging, truncating or re-reading a downstream stream; deleting and recreating the
   consumer, the mirror or the source; and turning off flow control on the consumer.

Make each claim explicit and self-contained; every statement must be correct, and together
they must answer all four points. The root cause the note names must be this incident's genuine root cause,
not a plausible look-alike.

The note must be at most 64 KiB (65,536 bytes).

## Acceptance criteria

1. A mirror and a source fed by a durable flow-controlled consumer hold every message the
   origin holds, exactly once, with no interior sequence reported deleted downstream, through
   consumer leadership changes, consumer resets and server restarts, whatever the timing of
   deliveries still in flight when those happen.
2. While a reset of the consumer is in flight the consumer delivers nothing: a request for its
   next message, including the consumer's own selection of the next message to deliver, is
   refused with an error, and its delivered sequence, its stream sequence and its count of
   pending messages do not move. Once the reset completes, delivery numbering resumes from the
   reset's own starting point on every replica, and a leader elected later continues that
   numbering.
3. The behaviour the project's existing mirror, source, flow-control and consumer-reset tests
   already rely on is unchanged.

## Constraints

Each of these must hold.

- Flow control stays active on the durable consumer that feeds the mirror and the source.
  Turning it off, or routing around it, is not a repair.
- The durable consumer, the mirror and the source are not deleted and recreated to recover.
  They come through the maintenance as the same objects.
- A message published to the origin twice under the same message id is still stored once.
- Messages already stored downstream are neither dropped nor rewritten. Purging, truncating or
  re-reading from scratch is not a repair.
