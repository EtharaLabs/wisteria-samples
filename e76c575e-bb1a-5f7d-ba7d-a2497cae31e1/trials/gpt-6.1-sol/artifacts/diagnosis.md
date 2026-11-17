# Restarted leaders must campaign in a new term

The ambiguous log id is (term T, old leader, index i). Before the crash it
names X, which one follower has persisted. The leader loses its unflushed X
and restarts at i-1. Restoring leadership from its persisted committed vote
lets it propose Y with precisely the same id. Another follower stores Y;
the follower holding X sees a matching id and retains X. A quorum can commit
Y while the first follower applies X, producing inconsistent state machines.

This combines asynchronous local log durability, replication before the
leader's flush completes, persisted votes surviving independently of logs,
and startup restoring authority without an election. Matching log ids are
supposed to identify identical entries; follower reconciliation relies on
that invariant rather than comparing payloads.

Startup now sends a former leader through the ordinary election path in
term T+1. It persists a new, uncommitted vote, advertises its actual last log
id, and cannot propose until a quorum grants authority. A voter with a newer
log refuses the request. With a quorum of the surviving shorter logs, the new
leader's blank entry and subsequent client entries have term T+1. The stale
X conflicts with that blank entry and is replaced during reconciliation.
Without a quorum, the node remains a candidate, adds no entries, and cannot
serve writes. A single voter wins through the normal self-vote path.

Merely waiting for a timer, comparing payloads on followers, appending an old
term blank entry, or changing the in-memory vote's committed bit would not
establish a fresh log-id namespace and quorum authority. The repair needs
both the new term and the normal election's log freshness check.

The stored vote is never rolled back or demoted. With restoration enabled,
the ordinary election writes a strictly newer vote, preserving monotonic
vote storage and IO progress. The log storage contract is unchanged: local
append completion may still be asynchronous, and replication need not wait
for the local flush. No stronger durability requirement is imposed.

With restoration disabled, startup leaves both the stored and in-memory
committed vote intact, creates no leadership state, and selects follower or
learner from membership. An explicit election still advances to T+1. Nodes
that were not leaders follow their existing startup path without a new
election or vote rewrite. Existing storage recovery still restores snapshots
and replays committed logs; the newly elected leader's blank entry then
commits and applies through the ordinary replication and state-machine path.
