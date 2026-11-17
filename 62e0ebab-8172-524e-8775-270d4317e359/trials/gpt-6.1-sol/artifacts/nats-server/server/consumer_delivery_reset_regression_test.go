// Copyright 2026 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestJetStreamClusterConsumerResetBlocksSelection(t *testing.T) {
	c := createJetStreamClusterExplicit(t, "R3S", 3)
	defer c.shutdown()
	nc, js := jsClientConnect(t, c.randomServer())
	defer nc.Close()
	_, err := js.AddStream(&nats.StreamConfig{Name: "TEST", Subjects: []string{"foo"}, Replicas: 3})
	require_NoError(t, err)
	for range 3 {
		_, err = js.Publish("foo", nil)
		require_NoError(t, err)
	}
	sub, err := js.PullSubscribe("foo", "C", nats.ConsumerReplicas(3))
	require_NoError(t, err)
	defer sub.Unsubscribe()
	msgs, err := sub.Fetch(1, nats.MaxWait(time.Second))
	require_NoError(t, err)
	require_Len(t, len(msgs), 1)
	cl := c.consumerLeader(globalAccountName, "TEST", "C")
	mset, err := cl.globalAccount().lookupStream("TEST")
	require_NoError(t, err)
	o := mset.lookupConsumer("C")
	o.mu.Lock()
	// Keeping this lock prevents Raft apply from completing the proposed reset.
	ss, ds, np := o.sseq, o.dseq, o.npc
	_, _, err = o.resetStartingSeqLocked(2, _EMPTY_, false, false)
	if err != nil {
		o.mu.Unlock()
		t.Fatal(err)
	}
	msg, _, err := o.getNextMsg()
	if err == nil || msg != nil || o.sseq != ss || o.dseq != ds || o.npc != np {
		o.mu.Unlock()
		t.Fatal("selection advanced while reset was awaiting quorum")
	}
	o.mu.Unlock()
	checkFor(t, 5*time.Second, 10*time.Millisecond, func() error {
		for _, s := range c.servers {
			ms, err := s.globalAccount().lookupStream("TEST")
			if err != nil {
				return err
			}
			co := ms.lookupConsumer("C")
			state, err := co.store.State()
			if err != nil {
				return err
			}
			if state.Delivered.Stream != 1 || state.Delivered.Consumer != 0 {
				return fmt.Errorf("reset not yet applied: %+v", state.Delivered)
			}
		}
		return nil
	})
	require_NoError(t, o.raftNode().StepDown())
	c.waitOnConsumerLeader(globalAccountName, "TEST", "C")
	msgs, err = sub.Fetch(1, nats.MaxWait(5*time.Second))
	require_NoError(t, err)
	require_Len(t, len(msgs), 1)
	md, err := msgs[0].Metadata()
	require_NoError(t, err)
	require_Equal(t, md.Sequence.Stream, uint64(2))
	require_Equal(t, md.Sequence.Consumer, uint64(1))
}

// Control traffic must not overtake data while a replicated delivery waits for
// application. Applying the recorded proposal stands in for quorum here.
func TestJetStreamConsumerFlowControlWaitsForDeliveredQuorum(t *testing.T) {
	s := &Server{}
	q := &jsOutQ{newIPQueue[*jsPubMsg](s, "quorum-regression")}
	defer q.unregister()
	cfg := ConsumerConfig{DeliverSubject: "deliver", AckPolicy: AckFlowControl, FlowControl: true, AckWait: time.Hour}
	o := &consumer{
		cfg: cfg, node: &raft{}, mset: &stream{}, outq: q,
		store: &consumerMemStore{cfg: cfg}, dseq: 1, sseq: 2,
		maxp: 1, maxpb: 1024,
	}
	defer func() { o.mu.Lock(); o.stopAndClearPtmr(); o.mu.Unlock() }()
	pmsg := getJSPubMsgFromPool()
	pmsg.seq, pmsg.ts, pmsg.subj, pmsg.msg = 1, time.Now().UnixNano(), "foo", []byte("one")
	o.mu.Lock()
	o.deliverMsg("deliver", "ack", pmsg, 1, LimitsPolicy)
	o.sendIdleHeartbeat("deliver")
	o.mu.Unlock()
	require_Equal(t, q.len(), 0)
	require_Equal(t, len(o.pendingDeliveries), 1)
	require_NotNil(t, o.phead)
	require_NoError(t, (&jetStream{}).applyConsumerMsgOp(o, o.phead.data))
	sent := q.pop()
	defer q.recycle(&sent)
	require_Len(t, len(sent), 2)
	require_Equal(t, string(sent[0].msg), "one")
	require_True(t, len(sent[1].hdr) > 0 && sent[1].reply != _EMPTY_)
	require_Equal(t, sent[1].reply, o.fcid)
	for _, msg := range sent {
		msg.returnToPool()
	}

	// A reset proposed after selection must also block a delivery that was
	// already queued waiting for quorum.
	pmsg = getJSPubMsgFromPool()
	pmsg.seq, pmsg.ts, pmsg.subj = 2, time.Now().UnixNano(), "foo"
	o.mu.Lock()
	o.deliverMsg("deliver", "ack", pmsg, 1, LimitsPolicy)
	entry := o.ptail.data
	o.resetPending = 1
	o.mu.Unlock()
	require_NoError(t, (&jetStream{}).applyConsumerMsgOp(o, entry))
	require_Equal(t, q.len(), 0)
	require_Equal(t, len(o.pendingDeliveries), 0)
}
