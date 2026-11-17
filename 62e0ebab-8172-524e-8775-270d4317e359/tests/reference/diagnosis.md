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
