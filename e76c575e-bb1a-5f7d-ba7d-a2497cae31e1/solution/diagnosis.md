# Root cause: a restored leader reuses a log id it lost in a crash

## What goes wrong across the restart

The leader of term T has flushed its log up to index i-1, appends entry X at index i and replicates
X to follower F1, which persists it. The leader crashes before its own flush of X. It restarts with
its log ending at i-1 and its stored committed vote as leader of term T. With
`enable_leader_restore` on, start-up makes it leader of term T again at once, without an election.
It then appends a different entry Y at index i under exactly the log id X has: (term T, the same
leader, index i). Follower F2 accepts Y and Y is committed by the leader and F2. F1 keeps X, because
the log matching property says an equal log id means an equal entry, so it reports X as matching and
applies it. The state machines diverge and nothing in Openraft notices.

## Why it happens

Two behaviours combine. Openraft replicates an entry as soon as it is submitted to the log store,
before the leader's own flush completes, so a follower can hold an entry that the leader then loses
in a crash. Leader restore then resumes leadership in the same term with the same leader id, so the
next entry the leader writes at that index carries the same log id as the lost one. A log id names
an entry only while a leader never reuses it for different contents; restore breaks that.

## The repair

At start-up a former leader no longer resumes leadership. Its vote is demoted in memory to a
non-committed vote, it starts in the following state (follower or learner), and, when
`enable_leader_restore` is on and it may campaign under the membership, it starts an election for a
higher term at once, without waiting for an election timeout and independently of `enable_tick` and
`enable_elect`. It accepts proposals only after winning that election. The higher term gives it a
new leader id, so no entry it writes can carry the lost entry's log id; on winning it appends a
blank entry under the new leader id, which moves the log index on before the next client write. The
campaign obeys the usual log-freshness and leader-lease checks, so voters may reject it; a voter
holding a newer log (such as the lost entry) does.

A re-election, or a quorum probe, inside the same term is not enough: the leader keeps its leader
id, and a follower that is down during that round can still hold the lost entry. Only a new leader
id, that is a new term, rules out the reuse.

## Why the other start-up behaviours stay intact

The demotion is made in memory only; the stored committed vote is not overwritten. Saving the
non-committed vote would move the stored vote backwards (for the same leader a non-committed vote is
smaller than the committed one) and break the monotonic vote storage and I/O progress tracking.
Keeping the stored vote is safe: no vote lies between the two, so any vote accepted later is greater
and the next vote save replaces it.

The asynchronous append contract between the core and storage is kept: `RaftLogStorage::append` may
still return before the entries are persisted, and its completion callback reports when they are
durable. The repair therefore does not require appends to be durable before they are replicated, and
does not hold replication back until the leader's own flush, which would give up overlapping
replication with the local flush.

Leader restore stays enabled by default; it is not turned off as the fix. With
`enable_leader_restore` set to false, a former leader starts as a follower or learner, keeps its
stored vote and waits for a normal election. Start-up of followers, learners and candidates that had
not finished an election is unchanged. Applications still need a durable committed pointer
(`save_committed`) or `Raft::wait_for_recovery` before serving reads.
