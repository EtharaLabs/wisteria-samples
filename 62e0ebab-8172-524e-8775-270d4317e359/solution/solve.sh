#!/bin/bash
#
# The oracle.  It does exactly the two things the task asks for and nothing
# else: it applies the upstream production repair to the tree in place, and it
# writes the root-cause and design note.  Both byte streams are embedded below
# and digest-checked, so this script needs no file beside it and the note it
# writes is byte for byte the note the judge grades against.
set -euo pipefail

tree=/app/nats-server
note=/app/diagnosis.md
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if [ ! -d "$tree" ]; then
    echo "solve: missing $tree" >&2
    exit 1
fi

cat > "$work/production.patch" <<'REFERENCE_PAYLOAD'
diff --git a/server/consumer.go b/server/consumer.go
index 21b9efd3..ec20eabf 100644
--- a/server/consumer.go
+++ b/server/consumer.go
@@ -3557,6 +3557,18 @@ func (o *consumer) info() *ConsumerInfo {
 	return o.infoWithSnap(false)
 }
 
+// resetInfo returns the consumer info for a reset to the given starting sequence.
+// Delivery resumes before the info is gathered, so report the reset point itself.
+func (o *consumer) resetInfo(seq uint64) *ConsumerInfo {
+	info := o.info()
+	if info == nil {
+		return nil
+	}
+	info.Delivered.Consumer, info.Delivered.Stream = 0, seq-1
+	info.AckFloor.Consumer, info.AckFloor.Stream = 0, seq-1
+	return info
+}
+
 func (o *consumer) infoWithSnap(snap bool) *ConsumerInfo {
 	return o.infoWithSnapAndReply(snap, _EMPTY_)
 }
@@ -4690,7 +4702,7 @@ func (o *consumer) processResetReq(_ *subscription, c *client, a *Account, _, re
 		resp.Error = NewJSConsumerInvalidResetError(err)
 		s.sendInternalAccountMsg(a, reply, s.jsonResponse(&resp))
 	} else if canRespond {
-		if resp.ConsumerInfo = setDynamicConsumerInfoMetadata(o.info()); resp.ConsumerInfo == nil {
+		if resp.ConsumerInfo = setDynamicConsumerInfoMetadata(o.resetInfo(resetSeq)); resp.ConsumerInfo == nil {
 			// The consumer was closed before we could respond.
 			resp.Error = NewJSConsumerInvalidResetError(errConsumerClosed)
 			s.sendInternalAccountMsg(a, reply, s.jsonResponse(&resp))
@@ -5025,6 +5037,7 @@ func (o *consumer) isFilterSubsetOf(subj string) bool {
 
 var (
 	errMaxAckPending = errors.New("max ack pending reached")
+	errResetPending  = errors.New("reset pending")
 	errBadConsumer   = errors.New("consumer not valid")
 	errNoInterest    = errors.New("consumer requires interest for delivery subject when ephemeral")
 )
@@ -5036,6 +5049,11 @@ func (o *consumer) getNextMsg() (*jsPubMsg, uint64, error) {
 	if o.mset == nil || o.mset.store == nil {
 		return nil, 0, errBadConsumer
 	}
+	// Deliveries proposed before a reset applies would mix into the delivered state after it.
+	// Only our own resets are tracked, these always have a reply when replicated.
+	if len(o.rsm) > 0 {
+		return nil, 0, errResetPending
+	}
 	// Process redelivered messages before looking at possibly "skip list" (deliver last per subject)
 	if o.hasRedeliveries() {
 		var seq, dc uint64
diff --git a/server/stream.go b/server/stream.go
index f93d28cd..c5e77419 100644
--- a/server/stream.go
+++ b/server/stream.go
@@ -712,6 +712,7 @@ type sourceInfo struct {
 	msgs  *ipQueue[*inMsg]    // Intra-process queue for incoming messages.
 	sseq  uint64              // Last stream message sequence number seen from the source.
 	dseq  uint64              // Last delivery (i.e. consumer's) sequence number.
+	cseq  uint64              // Stream message sequence number of the last delivery counted towards dseq.
 	lag   uint64              // 0 or number of messages pending (as last reported by the consumer) - 1.
 	err   *ApiError           // The API error that caused the last consumer setup to fail.
 	fails int                 // The number of times trying to setup the consumer failed.
@@ -3546,33 +3547,38 @@ func (mset *stream) processInboundMirrorMsg(m *inMsg) bool {
 	}
 
 	// Mirror info tracking.
-	olag, osseq, odseq := mset.mirror.lag, mset.mirror.sseq, mset.mirror.dseq
+	olag, osseq, odseq, ocseq := mset.mirror.lag, mset.mirror.sseq, mset.mirror.dseq, mset.mirror.cseq
 	if sseq <= mset.mirror.sseq {
 		// Ignore older messages.
 		// If the deliver sequence matches, we only update delivered accounting.
-		if dseq == mset.mirror.dseq+1 {
+		// A stale delivery from before a consumer reset could match too, but it won't be above the last counted.
+		if dseq == mset.mirror.dseq+1 && sseq > mset.mirror.cseq {
 			mset.mirror.dseq++
+			mset.mirror.cseq = sseq
 		}
 		mset.mu.Unlock()
 		return true
 	} else if sseq == mset.mirror.sseq+1 {
 		mset.mirror.dseq = dseq
 		mset.mirror.sseq++
+		mset.mirror.cseq = sseq
 	} else if mset.mirror.cname == _EMPTY_ {
 		mset.mirror.cname = cname
-		mset.mirror.dseq, mset.mirror.sseq = dseq, sseq
+		mset.mirror.dseq, mset.mirror.sseq, mset.mirror.cseq = dseq, sseq, sseq
 	} else {
 		// If the deliver sequence matches then the upstream stream has expired or deleted messages.
 		if dseq == mset.mirror.dseq+1 {
 			if err := mset.skipMsgs(mset.mirror.sseq+1, sseq-1); err != nil {
 				mset.mirror.sseq = osseq
 				mset.mirror.dseq = odseq
+				mset.mirror.cseq = ocseq
 				mset.mu.Unlock()
 				mset.retryMirrorConsumer()
 				return false
 			}
 			mset.mirror.dseq++
 			mset.mirror.sseq = sseq
+			mset.mirror.cseq = sseq
 		} else {
 			mset.mu.Unlock()
 			mset.retryMirrorConsumer()
@@ -4132,7 +4138,7 @@ func (mset *stream) setupMirrorConsumer() error {
 
 			// Capture consumer name.
 			mirror.cname = ccr.ConsumerInfo.Name
-			mirror.dseq = 0
+			mirror.dseq, mirror.cseq = 0, ccr.ConsumerInfo.Delivered.Stream
 			mirror.fcid, mirror.fcsc = _EMPTY_, 0
 			mirror.sseq = max(ccr.ConsumerInfo.Delivered.Stream, state.LastSeq)
 			mirror.qch = make(chan struct{})
@@ -4644,7 +4650,7 @@ func (mset *stream) trySetupSourceConsumer(iname string, seq uint64, startTime t
 				si.cname = ccr.ConsumerInfo.Name
 
 				// Do not set si.sseq to seq here. si.sseq will be set in processInboundSourceMsg
-				si.dseq = 0
+				si.dseq, si.cseq = 0, ccr.ConsumerInfo.Delivered.Stream
 				si.fcid, si.fcsc = _EMPTY_, 0
 				si.qch = make(chan struct{})
 				// Set the last seen as now so that we don't fail at the first check.
@@ -4894,28 +4900,26 @@ func (mset *stream) processInboundSourceMsg(si *sourceInfo, m *inMsg) bool {
 	}
 
 	// Tracking is done here.
-	osseq, odseq := si.sseq, si.dseq
+	osseq, odseq, ocseq := si.sseq, si.dseq, si.cseq
 	if sseq <= si.sseq {
 		// Ignore older messages.
 		// If the deliver sequence matches, we only update delivered accounting.
-		if dseq == si.dseq+1 {
+		// A stale delivery from before a consumer reset could match too, but it won't be above the last counted.
+		if dseq == si.dseq+1 && sseq > si.cseq {
 			si.dseq++
+			si.cseq = sseq
 		}
 		mset.mu.Unlock()
 		return true
 	} else if dseq == si.dseq+1 {
 		si.dseq++
-		si.sseq = sseq
-	} else if dseq > si.dseq {
-		if si.cname == _EMPTY_ {
-			si.cname = cname
-			si.dseq, si.sseq = dseq, sseq
-		} else {
-			mset.retrySourceConsumerAtSeq(si.iname, si.sseq+1)
-			mset.mu.Unlock()
-			return false
-		}
+		si.sseq, si.cseq = sseq, sseq
+	} else if dseq > si.dseq && si.cname == _EMPTY_ {
+		si.cname = cname
+		si.dseq, si.sseq, si.cseq = dseq, sseq, sseq
 	} else {
+		// Either we missed messages, or this newer message was delivered before one we already counted.
+		mset.retrySourceConsumerAtSeq(si.iname, si.sseq+1)
 		mset.mu.Unlock()
 		return false
 	}
@@ -4996,6 +5000,7 @@ func (mset *stream) processInboundSourceMsg(si *sourceInfo, m *inMsg) bool {
 			mset.mu.Lock()
 			si.dseq = odseq
 			si.sseq = osseq
+			si.cseq = ocseq
 			mset.retrySourceConsumerAtSeq(iName, sseq)
 			mset.mu.Unlock()
 		}
REFERENCE_PAYLOAD

cat > "$work/diagnosis.md" <<'REFERENCE_PAYLOAD'
# Reference root-cause and design note

## Root cause and evidence
A durable push consumer running under flow control delivers a message to its downstream
consumers before it has replicated its own delivered state. When that consumer's leadership
moves during maintenance, a delivery that was issued before the leadership change can still
arrive after it. As part of the leadership move and node restart the consumer is reset, and on
reset its delivery sequence number restarts at 1. A stale delivery left over from before the
reset can therefore carry the same delivery number that a newly delivered message now needs.

Downstream accounting then mis-fires. A source stream, seeing the new message arrive with a
delivery number that is not above the last delivery number it has already counted, treats the
new message as a duplicate and drops it. A mirror marks that stream sequence as deleted. The
origin, meanwhile, never deleted anything. The observable result is interior gaps in the
mirror and the source — sequences in the middle that the downstream stream reports deleted
while the origin still holds them — with no error logged on either side and nothing
redelivering the lost messages, while both downstream streams keep accepting new writes.

The reported figures fit this exactly: origin 878 messages, 0 deleted, against a downstream
holding 876 with sequences 551 and 552 reported deleted; and 887 against 886 with 796 deleted.
The missing sequences are interior, not a truncated tail. It is a race, so it appears only
under load (the pre-reset delivery must land after the reset) and not on an idle machine.

## Ruling out the look-alikes
- Ordinary redelivery is not the cause: redelivery re-sends a message that was already seen;
  here messages are permanently lost (interior deletes), never re-sent.
- Legitimate duplicate suppression is not the cause: suppression correctly refuses a true
  duplicate; here a genuinely new message is wrongly classified as a duplicate because of the
  delivery-number collision, so the suppression logic is mis-firing on a non-duplicate.
- Acknowledgement lag is not the cause: lag only delays acknowledgements; it neither deletes
  interior sequences nor loses messages for good.

## Why the repair takes the shape it does
1. Hold deliveries while a reset is pending. A delivery proposed before a reset applies must
   not mix into the delivered state after it; otherwise the stale pre-reset delivery lands
   after the reset and collides with a new one. Holding delivery until the reset has applied
   closes the window in which the collision can happen.
2. Report the reset point in the consumer's own reset response, its reply to a reset request.
   Delivery resumes before the post-reset
   consumer info is gathered, so the reset response must itself carry the reset point
   (delivered at the consumer's zero, stream at the sequence just before the reset). The
   downstream mirror and source seed their delivery counting from that reported reset point, so
   their accounting starts in the right place rather than from a stale value.
3. Count each delivery once. The stream must track the last stream sequence it counted toward
   its delivery number and advance that accounting only when the delivery number is exactly one
   past the last and the stream sequence is above the last counted. A stale pre-reset delivery
   matches the delivery number but is not above the last counted sequence, so it is not counted
   again; and a newer source message whose delivery number is not above the last counted is
   retried from the next stream sequence rather than dropped as a duplicate.
   Parts 2 and 3 meet at the reset point: the response from part 2 seeds the counter part 3
   keeps. A fix to only one of the consumer or the stream leaves either the collision open or
   the mis-count in place, so the defect survives a change narrower than all three parts.

## Why the operational shortcuts do not fix it
- Restarting the servers does not fix it: the defect is in the delivery-and-reset accounting
  logic, not in transient runtime state, so it returns at the next maintenance.
- Purging, truncating or re-reading a downstream stream does not fix it: it destroys messages
  already stored (which must not be dropped or rewritten) and the gap recurs at the next reset.
- Deleting and recreating the consumer, the mirror or the source does not fix it: a one-time
  re-sync masks the fault, but the objects must come through maintenance as the same objects,
  and the race returns the next time leadership moves.
- Turning off flow control does not fix it: flow control must stay active on this consumer; the
  reset-delivery race is not caused by flow control, disabling it changes behaviour the
  project's other tests rely on, and it violates the deployment's own constraint.
REFERENCE_PAYLOAD

check() {
    found="$(sha256sum "$1" | cut -d" " -f1)"
    if [ "$found" != "$2" ]; then
        echo "solve: $1 hashes to $found, not $2" >&2
        exit 1
    fi
}
check "$work/production.patch" c920d0edc42d76b12dd19e2279f538bb2053eeecb0105d705d9a9634cd3a1113
check "$work/diagnosis.md" c6fd2aba993ac2b743144c86ac695817c03a69ffe40aaa9aa029294a3f9fa14f

# D1: the repaired source tree, in place.  -p1 matches the patch's a/ b/
# roots.  git apply needs no repository, which is what the tree is: a plain
# tree with no history and no remote.
( cd "$tree" && git apply -p1 --whitespace=nowarn "$work/production.patch" )

# D2: the root-cause and design note.
cp -- "$work/diagnosis.md" "$note"
check "$note" c6fd2aba993ac2b743144c86ac695817c03a69ffe40aaa9aa029294a3f9fa14f

set -o pipefail
cd "$tree" && go build ./... 2>&1 | tail -20
