# Openraft: a leader that restarts with an unflushed log reuses a log id

The Openraft source tree at `/app/openraft` is the Openraft workspace (Rust Raft library) at version
0.10.0-alpha.37. It builds and tests offline with the dated nightly toolchain its
`/app/openraft/rust-toolchain` file names: `cargo` already resolves every crate from a local
vendored copy, there is no network, and build output goes to `/var/tmp/openraft-target`. The tree
has no git metadata.

Your job is to change Openraft so that a node which was leader before it stopped behaves as
described below when it starts again.

## What a user reported

A team runs Openraft (0.10.0-alpha.35) under a deterministic simulation of a three-node cluster that
crashes nodes and loses whatever their log stores had not yet flushed to disk. In the first 100
seeds, 4 ended with the nodes' logs diverged. They added a check that no two nodes' disks ever hold
different entries under the same log id (same term, same leader, same index); it fired in 9 of 200
seeds. With the `enable_leader_restore` setting turned off, no seed diverged across the 10,000 seeds
they run before a release. They found the behaviour unchanged in 0.10.0-alpha.37.

What their traces show: the leader of term T had flushed its log up to index i-1, appended entry X at
index i and replicated X to one follower, which persisted it; the leader then crashed before its own
flush of X. It came back with its log ending at i-1 and the committed vote of term T it had stored,
and went straight back to being leader of term T without any election. It then wrote a different
entry Y at index i, under exactly the log id X has. The second follower accepted Y; the first
follower kept X, since it already had an entry with that log id. Y was committed by the leader and
the second follower, and the first follower applied X. Their state machine checks continuity, so
the follower holding X refused everything after it and got stuck; a state machine without such a
check would simply have diverged without any error.

## What must hold

Below, a "former leader" is a node that starts from log and vote storage in which its stored vote is
its own committed vote as leader of some term T. All of this must hold with the timers turned off
(`enable_tick`, `enable_heartbeat` and `enable_elect` all `false`) as well as with them on.
`enable_leader_restore` unset means the default, which is enabled.

1. **No leadership in the old term.** A former leader never acts as leader of term T again after a
   restart and never proposes a log entry in term T again. With `enable_leader_restore` enabled it
   instead starts an election for term T+1 right away at start-up, without waiting for any timer:
   its vote in the metrics becomes its own not-yet-committed vote for term T+1, it is a
   `Candidate`, and the vote request it sends to the other voters carries that vote and the last
   log id in its own log.
2. **Winning the election.** When a quorum grants that vote (a single-node cluster grants it
   itself), its vote becomes its own committed vote for term T+1, it becomes `Leader`, and it
   appends a blank entry with log id (term T+1, itself, last index + 1). That blank entry is the
   only entry from that index on until clients write; it is committed and applied, so the applied
   index becomes the old last index + 1, and the next client write gets the index after it. All
   entries applied before the restart are applied again (a state machine that starts empty ends
   with the same contents, membership included, plus the new blank entry as its last applied log
   id; a state machine restored from a snapshot is brought up to date by replaying the rest of the
   log and then the blank entry). A snapshot built afterwards is at the blank entry's log id, and a
   fresh node that installs it reaches that index with the same state.
3. **No quorum.** When the other voters are down, the former leader stays a `Candidate` with its
   not-yet-committed vote for term T+1. Its metrics show no current leader and no cluster commit,
   while its local committed and last applied log ids are the last committed log id from before the
   restart (term T, itself, that index), restored from storage. A client write is refused with a
   forward-to-leader error that names no leader id and no leader node. Its log is unchanged: no
   entry is added and the last log index stays where it was. `Raft::wait_for_recovery` with a
   timeout reports a timeout.
4. **A voter with a newer log refuses.** A voter whose log goes beyond the former leader's last
   log id (for example it holds an entry the former leader lost) answers the vote request with its
   own vote (still the committed vote of term T for the old leader), its own last log id, and the
   vote not granted. The former leader then does not become leader, keeps its not-yet-committed
   vote for term T+1, shows no cluster commit and adds nothing to its log.
5. **The reported case.** Three voters stop; the former leader lost entry X at index i, which one
   follower holds. With that follower still down, the other follower and then the former leader
   start. The former leader wins term T+1 with the other follower, its blank entry (term T+1, at
   index i) is committed and applied, and a client write is committed at index i+1 on both running
   nodes. When the follower holding X starts again it catches up, X is replaced, and on all three
   nodes the log from index i on is exactly: the blank entry (term T+1, index i), then the client
   write's normal entry (term T+1, index i+1). Every node's last applied log id is the latter, and
   the state machine holds only the effect of that client write after the earlier entries.
6. **Restore disabled.** With `enable_leader_restore` set to `false`, a former leader starts as a
   `Follower`; its stored vote stays the committed vote of term T, and so does the vote its metrics
   show, and its log is unchanged. It starts no election by itself while elections are off; an
   explicitly triggered election makes it leader in term T+1.
7. **Nothing else changes.** Every other behaviour of Openraft stays as it is, including what the
   other existing tests of the `openraft` and `tests` packages check (the existing tests that expect
   the earlier start-up behaviour are described under Rules).

## The root-cause note

Also write `/app/diagnosis.md`, a short root-cause note in your own words, as a regular file (not a
symbolic link), that answers:

- What goes wrong across the restart: which log id ends up naming two different entries, and how
  do the nodes end up disagreeing?
- Why does it happen: which behaviours of Openraft combine to let a log id be reused?
- Why does your repair prevent the reuse, and why would a weaker repair not?
- Why does your repair keep the other start-up behaviours intact: the stored vote, the contract
  between Openraft and log storage, a node with `enable_leader_restore` set to `false`, and nodes
  that were not leader?

The note must be at most 64 KiB (65,536 bytes).

## Rules

- The repair must be complete in the Rust source files under `/app/openraft/openraft/src` that are
  not test files (files below a directory named `tests` and files named `*_test.rs` are test files),
  each a regular `.rs` file, valid UTF-8, at most 1 MiB. It must build with every other file exactly
  as it is in this tree (manifests, the lock file, documentation, test files and the other crates),
  and deleting a file is not part of the repair. Changes you make to test files are not part of the
  repair.
- Keep the public API: every public item, its name and signature, including the `Config` fields and
  the `Raft` methods tests call. You may add items. Crate-private items that the existing unit tests
  in `/app/openraft/openraft/src` use must keep their names and signatures, so that every test
  target of the `openraft` and `tests` packages still compiles offline with the pinned toolchain and
  lock file.
- Existing tests that expect the earlier start-up behaviour need not pass, and you may update them.
  They are the tests that expect a former leader to resume leadership in its old term when it
  starts (for example, to be leader again right after a restart in which it cannot reach a quorum
  of voters); the tests that expect a former leader to accept a client write right after a restart,
  without first waiting for it to become leader again; and the tests that expect a former leader
  restarted with `enable_leader_restore` enabled to add no new blank entry, by checking that its
  applied index after the restart is exactly its last log index from before the restart. A test
  that starts or restarts only nodes that are not former leaders, or only former leaders with
  `enable_leader_restore` set to `false`, is not one of them; for example, a test that restarts a
  follower and waits for `Raft::wait_for_recovery` to succeed must still pass.
- Every other existing test of those packages that passes on this tree today must still pass; tests
  that already fail intermittently on this tree and tests marked `#[ignore]` need not pass. The
  behaviour described above must hold on every run, not intermittently.
- Library code must not end or signal a process (add no `use std::process` declaration, call no
  `exit` or `abort` of the standard library or the compiler intrinsics, and no `exit`, `abort`,
  `kill` or `raise` of the C library), must not refer to test files, test directories or the build
  environment (no added `include!`, `include_str!` or `include_bytes!`, and no string literal naming
  a test file, a `tests` directory, the `CARGO_MANIFEST_DIR` variable, a `target/debug` directory or
  another path of the build environment), and must add no network access (no use of `std::net`, no
  other socket or name-resolution code, and no string literal naming a non-loopback URL, IPv4
  address or host name).

## Building and testing locally

```
cd /app/openraft
cargo test -p openraft -p tests --tests
```
