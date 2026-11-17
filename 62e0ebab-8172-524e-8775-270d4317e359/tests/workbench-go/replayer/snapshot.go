package replayer

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// snapshot records origin, mirror, source and consumer state at a boundary:
// message count, first and last sequence, number deleted, the deleted list,
// the creation timestamp, and per stored sequence the payload digest and the
// origin sequence it came from.
func (r *run) snapshot(boundary string) int64 {
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	streams := Event{}
	for _, name := range []string{OriginStream, MirrorStream, SourceStream} {
		streams[name] = r.streamSnapshot(ctx, name)
	}
	cons := Event{"ok": false}
	if ci, err := getConsumerInfo(ctx, r.api, OriginStream, Consumer); err != nil {
		cons["error"] = err.Error()
	} else {
		cons = Event{
			"ok": true, "created": ci.Created, "flow_control": ci.Config != nil && ci.Config.FlowControl,
			"delivered_stream": ci.Delivered.Stream, "delivered_consumer": ci.Delivered.Consumer,
			"ack_floor_stream": ci.AckFloor.Stream, "num_pending": ci.NumPending,
			"num_ack_pending": ci.NumAckPending,
		}
		if ci.Cluster != nil {
			cons["leader"] = ci.Cluster.Leader
		}
	}
	return r.rec.Record("snapshot", Event{"boundary": boundary, "streams": streams, "consumer": cons})
}

func (r *run) streamSnapshot(ctx context.Context, name string) Event {
	si, err := getStreamInfo(ctx, r.api, name)
	if err != nil {
		return Event{"ok": false, "error": err.Error()}
	}
	deleted := si.State.Deleted
	if deleted == nil {
		deleted = []uint64{}
	}
	snap := Event{
		"ok": true, "created": si.Created, "messages": si.State.Msgs, "first_seq": si.State.FirstSeq,
		"last_seq": si.State.LastSeq, "num_deleted": si.State.NumDeleted, "deleted": deleted,
	}
	if si.Cluster != nil {
		snap["leader"] = si.Cluster.Leader
	}
	msgs := []Event{}
	var fetchErr error
	if si.State.Msgs > 0 {
		for seq := si.State.FirstSeq; seq <= si.State.LastSeq; seq++ {
			sm, err := getMsg(ctx, r.api, name, seq)
			if errors.Is(err, errNoMessage) {
				continue
			}
			if err != nil {
				fetchErr = err
				break
			}
			m := Event{"seq": seq, "digest": Digest(sm.Message.Data), "subject": sm.Message.Subject}
			switch name {
			case OriginStream, MirrorStream:
				m["origin_seq"] = seq
			case SourceStream:
				if oseq, ok := sourceOriginSeq(sm.Message.Header); ok {
					m["origin_seq"] = oseq
				}
			}
			msgs = append(msgs, m)
		}
	}
	snap["msgs"] = msgs
	if fetchErr != nil {
		snap["ok"] = false
		snap["error"] = fmt.Sprintf("fetch messages: %v", fetchErr)
	}
	return snap
}

// operationalAction applies one E6 action after the grading timeline has run
// and the symptom, if any, has appeared, then waits for the system to settle
// and records an after_action snapshot.
func (r *run) operationalAction() error {
	r.setPhase("action")
	mark := r.lastID()
	var err error
	switch r.cfg.Timeline {
	case "e6-restart-nodes":
		r.rec.Record("action", Event{"name": "restart_nodes"})
		for i := range r.cluster.Nodes {
			r.rec.Record("node_stop", Event{"node": r.cluster.Nodes[i].Name})
		}
		r.cluster.Shutdown()
		for i := range r.cluster.Nodes {
			r.rec.Record("node_start", Event{"node": r.cluster.Nodes[i].Name})
			if serr := r.cluster.StartNode(i); serr != nil {
				return &InfraError{serr}
			}
		}
		ctx, cancel := context.WithTimeout(r.ctx, 90*time.Second)
		_, err = r.cluster.WaitReady(ctx)
		cancel()
		if err != nil {
			return &InfraError{err}
		}
		err = r.waitDownstreamReady(mark)
	case "e6-recreate-consumer":
		r.rec.Record("action", Event{"name": "recreate_consumer"})
		if derr := r.apiCall("delete_consumer",
			fmt.Sprintf("$JS.API.CONSUMER.DELETE.%s.%s", OriginStream, Consumer), nil); derr != nil {
			return derr
		}
		if cerr := r.createConsumer(); cerr != nil {
			return cerr
		}
		err = r.waitDownstreamReady(mark)
	case "e6-recreate-downstream":
		r.rec.Record("action", Event{"name": "recreate_mirror_and_source"})
		for _, name := range []string{MirrorStream, SourceStream} {
			if derr := r.apiCall("delete_stream_"+name, "$JS.API.STREAM.DELETE."+name, nil); derr != nil {
				return derr
			}
		}
		for _, name := range []string{MirrorStream, SourceStream} {
			if cerr := r.createDownstream(name); cerr != nil {
				return cerr
			}
		}
		err = r.waitDownstreamReady(mark)
	case "e6-flow-control-off":
		r.rec.Record("action", Event{"name": "flow_control_off"})
		// The update is expected to be refused; the refusal is the fact recorded.
		_ = r.apiCall("update_consumer_flow_control_off",
			fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.%s", OriginStream, Consumer),
			map[string]any{"stream_name": OriginStream, "config": r.consumerConfig(false), "action": "update"})
	}
	r.precondition("action_settled", err == nil, nil)
	r.precondition("action_quiescent", r.waitQuiescent(uint64(2*BatchSize)) == nil, nil)
	r.snapshot("after_action")
	return nil
}
