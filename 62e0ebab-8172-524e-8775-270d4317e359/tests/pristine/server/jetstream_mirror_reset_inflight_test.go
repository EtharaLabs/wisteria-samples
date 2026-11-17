//go:build !skip_js_tests

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A mirror that sets up again against a durable flow-controlled consumer resets
// that consumer. Deliveries can resume before the reset is answered, and then they
// go out before the mirror listens again; or the consumer can hold them until the
// reset is answered. Either way the mirror must end up holding every message the
// origin holds, so it must not skip past deliveries it never received.
//
// The window between the reset being applied and being answered is held open
// deterministically: on an interest stream the reset walks the messages below its
// starting point under the stream lock, with the consumer lock released. Holding
// the stream lock parks it there, and the deliveries are made exactly as the
// consumer's own delivery loop makes them.
func TestJetStreamMirrorResetWithDeliveriesInFlight(t *testing.T) {
	s := RunBasicJetStreamServer(t)
	defer s.Shutdown()

	nc, _ := jsClientConnect(t, s)
	defer nc.Close()

	_, err := jsStreamCreate(t, nc, &StreamConfig{
		Name:      "O",
		Subjects:  []string{"foo"},
		Storage:   FileStorage,
		Retention: InterestPolicy,
	})
	require_NoError(t, err)
	// Another consumer keeps interest in every message, so the origin keeps them all.
	_, err = jsConsumerCreate(t, nc, "O", ConsumerConfig{Durable: "C2", AckPolicy: AckExplicit}, false)
	require_NoError(t, err)
	for range 10 {
		_, err = nc.Request("foo", nil, time.Second)
		require_NoError(t, err)
	}
	// Starts at s4, so the reset when the mirror sets up goes back to the ack floor at s3.
	// Deliveries go elsewhere, we hand them to the mirror ourselves.
	_, err = jsConsumerCreate(t, nc, "O", ConsumerConfig{
		Durable:        "C",
		DeliverSubject: "orig",
		AckPolicy:      AckFlowControl,
		DeliverPolicy:  DeliverByStartSequence,
		OptStartSeq:    4,
	}, false)
	require_NoError(t, err)

	omset, err := s.globalAccount().lookupStream("O")
	require_NoError(t, err)
	o := omset.lookupConsumer("C")
	require_NotNil(t, o)

	// Same as loopAndGatherMsgs, lock should be held. Returns the delivery's reply subject.
	deliverNext := func() (string, error) {
		pmsg, dc, err := o.getNextMsg()
		if err != nil {
			return _EMPTY_, err
		}
		rply := o.ackReply(pmsg.seq, o.dseq, dc, pmsg.ts, o.numPending())
		o.deliverMsg(o.cfg.DeliverSubject, rply, pmsg, dc, InterestPolicy)
		return rply, nil
	}
	deliver := func(n int) {
		t.Helper()
		o.mu.Lock()
		defer o.mu.Unlock()
		for range n {
			_, err := deliverNext()
			require_NoError(t, err)
		}
	}

	// Deliver d1..d3 = s4..s6 before the mirror exists.
	deliver(3)

	// Park the mirror's reset after it is applied and before it is answered.
	omset.mu.Lock()
	held := true
	release := func() {
		if held {
			held = false
			omset.mu.Unlock()
		}
	}
	defer release()

	created := make(chan error, 1)
	go func() {
		b, _ := json.Marshal(&StreamConfig{
			Name:    "M",
			Storage: FileStorage,
			Mirror:  &StreamSource{Name: "O", Consumer: &StreamConsumerSource{Name: "C", DeliverSubject: "deliver"}},
		})
		msg, err := nc.Request(fmt.Sprintf(JSApiStreamCreateT, "M"), b, 10*time.Second)
		if err == nil {
			var resp JSApiStreamCreateResponse
			if err = json.Unmarshal(msg.Data, &resp); err == nil && resp.Error != nil {
				err = resp.Error
			}
		}
		created <- err
	}()

	checkFor(t, 10*time.Second, 10*time.Millisecond, func() error {
		o.mu.RLock()
		defer o.mu.RUnlock()
		if o.dseq != 1 || o.sseq != 4 {
			return fmt.Errorf("reset not applied yet: dseq=%d sseq=%d", o.dseq, o.sseq)
		}
		return nil
	})
	// Delivery may resume from the reset point: d1..d3 = s4..s6 go out while the mirror
	// isn't listening. A consumer may instead hold its deliveries until the reset is
	// answered, so a refusal here is accepted; the outcome below is what counts.
	o.mu.Lock()
	for range 3 {
		if _, err := deliverNext(); err != nil {
			t.Logf("delivery refused while the reset is in flight: %v", err)
			break
		}
	}
	o.mu.Unlock()
	release()

	select {
	case err = <-created:
		require_NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("mirror was not created")
	}
	mset, err := s.globalAccount().lookupStream("M")
	require_NoError(t, err)
	checkFor(t, 10*time.Second, 10*time.Millisecond, func() error {
		mset.mu.RLock()
		defer mset.mu.RUnlock()
		if mset.mirror == nil || mset.mirror.sub == nil {
			return errors.New("mirror consumer not set up yet")
		}
		return nil
	})

	// Delivery goes on and every delivery reaches the mirror, until it holds s7. The
	// mirror may set up again on the way, so deliver only while it is listening.
	listening := func() bool {
		mset.mu.RLock()
		defer mset.mu.RUnlock()
		return mset.mirror != nil && mset.mirror.sub != nil && !mset.mirror.sip
	}
	deadline := time.Now().Add(30 * time.Second)
	for mset.state().LastSeq < 7 && time.Now().Before(deadline) {
		if listening() {
			o.mu.Lock()
			rply, err := deliverNext()
			o.mu.Unlock()
			if err == nil {
				mset.processInboundMirrorMsg(&inMsg{subj: "foo", rply: rply, msg: []byte("x")})
				continue
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The origin holds s4..s7, so the mirror must hold them all, with no interior sequence deleted.
	state := mset.state()
	if state.LastSeq < 7 || state.FirstSeq > 4 || state.NumDeleted > 0 || state.Msgs != state.LastSeq-state.FirstSeq+1 {
		t.Fatalf("mirror does not hold s4..s7 the origin holds: %+v", state)
	}
}
