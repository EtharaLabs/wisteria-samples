// Package replayer drives one named timeline against a cluster started from a
// server binary, records every publish, delivery, advisory, reset and boundary
// snapshot to recording.jsonl, and derives a verdict of named checks from that
// recording alone.
package replayer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"replay.local/workbench/controller"
	"replay.local/workbench/launcher"
)

// Fixed names and parameters of every timeline.
const (
	OriginStream   = "O"
	OriginSubject  = "orders"
	Consumer       = "C"
	DeliverSubject = "dlv"
	MirrorStream   = "M"
	SourceStream   = "S"
	BatchSize      = 10
	MaxAckPending  = 1000
	Heartbeat      = time.Second
)

// Timelines lists the timelines the replayer knows. The first is the grading
// timeline; the -1n form is a single-node development aid; the e6- forms run
// the grading timeline and then one operational action on top of it.
var Timelines = []string{
	"stale-after-reset",
	"stale-after-reset-1n",
	"e6-restart-nodes",
	"e6-recreate-consumer",
	"e6-recreate-downstream",
	"e6-flow-control-off",
}

// InfraError marks a failure to run the timeline at all, as opposed to a
// behaviour of the server under test.
type InfraError struct{ Err error }

func (e *InfraError) Error() string { return "infrastructure: " + e.Err.Error() }
func (e *InfraError) Unwrap() error { return e.Err }

// Config is the input of one replay.
type Config struct {
	Server   string
	Timeline string
	Nodes    int
	Seed     uint64
	Out      string
	Load     int
	KeepRoot bool
}

type run struct {
	cfg     Config
	ctx     context.Context
	rec     *Recorder
	cluster *launcher.Cluster
	api     *nats.Conn
	watch   *nats.Conn
	target  int // index of the node holding the mirror and the source
	rng     *rand.Rand

	mu           sync.Mutex
	phase        string
	firstD1      *nats.Msg
	firstD1Phase string
	ownCids      map[uint64]bool

	pre map[string]bool
}

func (r *run) setPhase(p string) {
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
	r.rec.Record("phase", Event{"name": p})
}

func (r *run) currentPhase() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase
}

func (r *run) precondition(name string, ok bool, detail Event) bool {
	r.pre[name] = ok
	ev := Event{"name": name, "ok": ok}
	for k, v := range detail {
		ev[k] = v
	}
	r.rec.Record("precondition", ev)
	return ok
}

// waitFor polls cond until it reports true or the bound elapses.
func (r *run) waitFor(bound time.Duration, cond func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(r.ctx, bound)
	defer cancel()
	var lastErr error
	for {
		ok, err := cond(ctx)
		if err == nil && ok {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("%w (last error: %v)", ctx.Err(), lastErr)
			}
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Run executes cfg.Timeline and writes recording.jsonl and verdict.json under cfg.Out.
func Run(parent context.Context, cfg Config) (*Verdict, error) {
	if !slices.Contains(Timelines, cfg.Timeline) {
		return nil, &InfraError{fmt.Errorf("unknown timeline %q", cfg.Timeline)}
	}
	if cfg.Timeline == "stale-after-reset-1n" {
		cfg.Nodes = 1
	}
	if cfg.Nodes != 1 && cfg.Nodes != 3 {
		return nil, &InfraError{fmt.Errorf("nodes must be 1 or 3, got %d", cfg.Nodes)}
	}
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		return nil, &InfraError{err}
	}
	root, err := os.MkdirTemp("", "replay-")
	if err != nil {
		return nil, &InfraError{err}
	}
	rec, err := NewRecorder(filepath.Join(cfg.Out, "recording.jsonl"))
	if err != nil {
		return nil, &InfraError{err}
	}
	stopLoad := controller.Load(cfg.Load)
	defer stopLoad()

	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	r := &run{
		cfg:     cfg,
		ctx:     ctx,
		rec:     rec,
		rng:     rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15)),
		ownCids: map[uint64]bool{},
		pre:     map[string]bool{},
	}
	rec.Record("run", Event{
		"timeline": cfg.Timeline, "nodes": cfg.Nodes, "seed": cfg.Seed, "load": cfg.Load,
		"origin": OriginStream, "consumer": Consumer, "mirror": MirrorStream, "source": SourceStream,
		"deliver_subject": DeliverSubject, "batch_size": BatchSize,
	})
	runErr := r.execute(root)
	if r.cluster != nil {
		r.closeConns()
		r.cluster.Shutdown()
		r.copyLogs()
		if !cfg.KeepRoot {
			_ = r.cluster.Cleanup()
		}
	} else if !cfg.KeepRoot {
		_ = os.RemoveAll(root)
	}
	var infra *InfraError
	if errors.As(runErr, &infra) {
		rec.Record("infrastructure_error", Event{"error": runErr.Error()})
		_ = rec.Close()
		return nil, runErr
	}
	if runErr != nil {
		rec.Record("timeline_error", Event{"error": runErr.Error()})
	}
	rec.Record("end", Event{"preconditions": r.pre})
	if err := rec.Close(); err != nil {
		return nil, &InfraError{err}
	}
	events, err := LoadRecording(filepath.Join(cfg.Out, "recording.jsonl"))
	if err != nil {
		return nil, &InfraError{err}
	}
	v := Judge(events)
	if err := WriteJSON(filepath.Join(cfg.Out, "verdict.json"), v); err != nil {
		return nil, &InfraError{err}
	}
	return v, nil
}

func (r *run) closeConns() {
	for _, nc := range []*nats.Conn{r.api, r.watch} {
		if nc != nil {
			nc.Close()
		}
	}
}

func (r *run) copyLogs() {
	dir := filepath.Join(r.cfg.Out, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	for _, n := range r.cluster.Nodes {
		if b, err := os.ReadFile(n.LogPath); err == nil {
			_ = os.WriteFile(filepath.Join(dir, n.Name+".log"), b, 0o644)
		}
	}
}

func (r *run) connect(name string, urls []string) (*nats.Conn, error) {
	nc, err := nats.Connect(strings.Join(urls, ","),
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(100*time.Millisecond),
		nats.DontRandomize(),
		nats.Timeout(5*time.Second),
	)
	if err != nil {
		return nil, err
	}
	if cid, err := nc.GetClientID(); err == nil {
		r.mu.Lock()
		r.ownCids[cid] = true
		r.mu.Unlock()
	}
	return nc, nil
}

func (r *run) execute(root string) error {
	c, err := launcher.New(r.cfg.Server, r.cfg.Nodes, root)
	if err != nil {
		return &InfraError{err}
	}
	r.cluster = c
	r.target = len(c.Nodes) - 1
	if err := c.Start(); err != nil {
		return &InfraError{err}
	}
	readyCtx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	leader, err := c.WaitReady(readyCtx)
	cancel()
	if err != nil {
		return &InfraError{err}
	}
	nodes := make([]Event, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		nodes = append(nodes, Event{"name": n.Name, "tag": n.Tag})
	}
	r.rec.Record("cluster_ready", Event{"meta_leader": leader, "nodes": nodes, "target": c.Nodes[r.target].Name})

	// The replay's own connections avoid the node holding the mirror and the
	// source, so restarting that node never drops them in the 3-node form.
	var urls []string
	for i := range c.Nodes {
		if i != r.target || len(c.Nodes) == 1 {
			urls = append(urls, c.URL(i))
		}
	}
	if r.api, err = r.connect("replay-api", urls); err != nil {
		return &InfraError{err}
	}
	if r.watch, err = r.connect("replay-watch", urls); err != nil {
		return &InfraError{err}
	}
	if err := r.subscribeWatchers(); err != nil {
		return &InfraError{err}
	}

	if err := r.setup(); err != nil {
		return err
	}
	if err := r.batch(1); err != nil {
		return err
	}
	r.snapshot("after_batch1")
	if err := r.leaderChange(); err != nil {
		return err
	}
	if err := r.downstreamReset(); err != nil {
		return err
	}
	if err := r.resend(); err != nil {
		return err
	}
	if err := r.batch(2); err != nil {
		return err
	}
	if err := r.duplicate(); err != nil {
		return err
	}
	r.snapshot("final")
	if strings.HasPrefix(r.cfg.Timeline, "e6-") {
		if err := r.operationalAction(); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) subscribeWatchers() error {
	if _, err := r.watch.Subscribe(DeliverSubject, r.onDeliver); err != nil {
		return err
	}
	if _, err := r.watch.Subscribe("$JS.EVENT.ADVISORY.>", r.onAdvisory); err != nil {
		return err
	}
	resetSubject := fmt.Sprintf("$JS.API.CONSUMER.RESET.%s.%s", OriginStream, Consumer)
	if _, err := r.watch.Subscribe(resetSubject, func(m *nats.Msg) {
		r.rec.Record("reset_request", Event{
			"origin": "observed", "subject": m.Subject, "reply": m.Reply, "body": string(m.Data),
			"phase": r.currentPhase(),
		})
	}); err != nil {
		return err
	}
	if _, err := r.watch.Subscribe("$JSC.R.>", r.onResetResponse); err != nil {
		return err
	}
	return r.watch.FlushTimeout(5 * time.Second)
}

func (r *run) onDeliver(m *nats.Msg) {
	if m.Header.Get("Status") == "100" {
		desc := m.Header.Get("Description")
		kind := "other"
		switch {
		case strings.Contains(desc, "FlowControl"):
			kind = "flow_control"
		case strings.Contains(desc, "Heartbeat"):
			kind = "heartbeat"
		}
		ev := Event{"kind": kind, "description": desc, "reply": m.Reply, "phase": r.currentPhase()}
		if v := m.Header.Get("Nats-Last-Consumer"); v != "" {
			ev["last_consumer"] = v
		}
		if v := m.Header.Get("Nats-Last-Stream"); v != "" {
			ev["last_stream"] = v
		}
		if v := m.Header.Get("Nats-Consumer-Stalled"); v != "" {
			ev["stalled"] = v
		}
		r.rec.Record("control", ev)
		return
	}
	ai := parseAckReply(m.Reply)
	phase := r.currentPhase()
	r.rec.Record("delivery", Event{
		"subject": m.Subject, "reply": m.Reply, "parsed": ai.OK,
		"stream_seq": ai.StreamSeq, "delivery_seq": ai.DelivSeq, "delivery_count": ai.Delivered,
		"pending": ai.Pending, "digest": Digest(m.Data), "phase": phase,
	})
	if ai.OK && ai.DelivSeq == 1 && phase == "batch1" {
		r.mu.Lock()
		if r.firstD1 == nil {
			r.firstD1, r.firstD1Phase = m, phase
		}
		r.mu.Unlock()
	}
}

func (r *run) onAdvisory(m *nats.Msg) {
	var body struct {
		Type     string `json:"type"`
		Stream   string `json:"stream"`
		Consumer string `json:"consumer"`
		Leader   string `json:"leader"`
	}
	_ = json.Unmarshal(m.Data, &body)
	r.rec.Record("advisory", Event{
		"subject": m.Subject, "advisory_type": body.Type, "stream": body.Stream,
		"consumer": body.Consumer, "leader": body.Leader, "phase": r.currentPhase(),
	})
}

func (r *run) onResetResponse(m *nats.Msg) {
	var body struct {
		Type     string               `json:"type"`
		Error    *controller.APIError `json:"error"`
		ResetSeq uint64               `json:"reset_seq"`
		Name     string               `json:"name"`
		Deliv    seqInfo              `json:"delivered"`
		AckFloor seqInfo              `json:"ack_floor"`
	}
	if err := json.Unmarshal(m.Data, &body); err != nil {
		return
	}
	ev := Event{
		"origin": "observed", "subject": m.Subject, "response_type": body.Type,
		"reset_seq": body.ResetSeq, "delivered_stream": body.Deliv.Stream,
		"delivered_consumer": body.Deliv.Consumer, "ack_floor_stream": body.AckFloor.Stream,
		"phase": r.currentPhase(),
	}
	if body.Error != nil {
		ev["error"] = body.Error.Error()
	}
	r.rec.Record("reset_response", ev)
}

func (r *run) placement() map[string]any {
	return map[string]any{"tags": []string{r.cluster.Nodes[r.target].Tag}}
}

func (r *run) consumerConfig(flowControl bool) map[string]any {
	return map[string]any{
		"durable_name":    Consumer,
		"deliver_subject": DeliverSubject,
		"ack_policy":      "flow_control",
		"flow_control":    flowControl,
		"idle_heartbeat":  int64(Heartbeat),
		"max_ack_pending": MaxAckPending,
		"deliver_policy":  "all",
		"num_replicas":    r.cfg.Nodes,
	}
}

func (r *run) downstreamConfig(name string) map[string]any {
	src := map[string]any{
		"name":     OriginStream,
		"consumer": map[string]string{"name": Consumer, "deliver_subject": DeliverSubject},
	}
	cfg := map[string]any{"name": name, "storage": "file", "num_replicas": 1, "placement": r.placement()}
	if name == MirrorStream {
		cfg["mirror"] = src
	} else {
		cfg["sources"] = []any{src}
	}
	return cfg
}

func (r *run) apiCall(what, subject string, body any) error {
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	raw, err := controller.Request(ctx, r.api, subject, body, nil)
	ev := Event{"what": what, "subject": subject, "ok": err == nil}
	if err != nil {
		ev["error"] = err.Error()
		ev["response"] = string(raw)
	}
	r.rec.Record("api", ev)
	return err
}

func (r *run) createConsumer() error {
	return r.apiCall("create_consumer",
		fmt.Sprintf("$JS.API.CONSUMER.DURABLE.CREATE.%s.%s", OriginStream, Consumer),
		map[string]any{"stream_name": OriginStream, "config": r.consumerConfig(true)})
}

func (r *run) createDownstream(name string) error {
	return r.apiCall("create_stream_"+name, "$JS.API.STREAM.CREATE."+name, r.downstreamConfig(name))
}

func (r *run) setup() error {
	r.setPhase("setup")
	origin := map[string]any{
		"name": OriginStream, "subjects": []string{OriginSubject}, "storage": "file",
		"num_replicas": r.cfg.Nodes,
	}
	if err := r.apiCall("create_stream_O", "$JS.API.STREAM.CREATE."+OriginStream, origin); err != nil {
		return fmt.Errorf("create origin: %w", err)
	}
	if err := r.createConsumer(); err != nil {
		return fmt.Errorf("create consumer: %w", err)
	}
	if err := r.createDownstream(MirrorStream); err != nil {
		return fmt.Errorf("create mirror: %w", err)
	}
	if err := r.createDownstream(SourceStream); err != nil {
		return fmt.Errorf("create source: %w", err)
	}
	ok := r.waitDownstreamReady(0) == nil
	if !r.precondition("setup_confirmed", ok, nil) {
		return errors.New("downstream streams did not set up their consumer")
	}
	return nil
}

// streamSubs counts the subscriptions on the deliver subject held on the
// target node by anything other than the replay's own connections: the
// mirror's and the source's.
func (r *run) streamSubs(ctx context.Context) ([]launcher.SubDetail, error) {
	subs, err := r.cluster.LocalSubs(ctx, r.target, DeliverSubject)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []launcher.SubDetail
	for _, s := range subs {
		if s.Subject == DeliverSubject && !r.ownCids[s.Cid] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *run) countEvents(after int64, pred func(Event) bool) int {
	n := 0
	for _, ev := range r.rec.Events() {
		if id, _ := ev["id"].(int64); id > after && pred(ev) {
			n++
		}
	}
	return n
}

// waitDownstreamReady waits until both downstream streams have reset the
// durable consumer since event id `after`, both report no error, and both
// hold their subscription on the deliver subject.
func (r *run) waitDownstreamReady(after int64) error {
	return r.waitFor(90*time.Second, func(ctx context.Context) (bool, error) {
		responses := r.countEvents(after, func(ev Event) bool {
			_, failed := ev["error"]
			return ev["type"] == "reset_response" && !failed
		})
		// In the single-node form the replay's own connections drop with the
		// node, so responses sent before they reconnect cannot be observed;
		// the subscriptions below are only made after a reset response anyway.
		if len(r.cluster.Nodes) > 1 && responses < 2 {
			return false, nil
		}
		subs, err := r.streamSubs(ctx)
		if err != nil || len(subs) < 2 {
			return false, err
		}
		for _, name := range []string{MirrorStream, SourceStream} {
			si, err := getStreamInfo(ctx, r.api, name)
			if err != nil {
				return false, err
			}
			src := si.Mirror
			if name == SourceStream && len(si.Sources) > 0 {
				src = si.Sources[0]
			}
			if src == nil || src.Error != nil {
				return false, nil
			}
		}
		return true, nil
	})
}

func (r *run) payload(batch, index int) []byte {
	noise := r.rng.Uint64()
	return fmt.Appendf(nil, "seed=%d batch=%d index=%d noise=%016x", r.cfg.Seed, batch, index, noise)
}

func (r *run) publish(batch, index int) error {
	data := r.payload(batch, index)
	msgID := fmt.Sprintf("b%d-%d", batch, index)
	msg := &nats.Msg{Subject: OriginSubject, Data: data, Header: nats.Header{}}
	msg.Header.Set("Nats-Msg-Id", msgID)
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	for {
		attempt, acancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := r.api.RequestMsgWithContext(attempt, msg)
		acancel()
		if err == nil {
			var ack pubAck
			var envelope struct {
				Error *controller.APIError `json:"error"`
			}
			_ = json.Unmarshal(resp.Data, &envelope)
			if envelope.Error == nil && json.Unmarshal(resp.Data, &ack) == nil && ack.Seq > 0 {
				r.rec.Record("publish", Event{
					"batch": batch, "index": index, "subject": OriginSubject, "msg_id": msgID,
					"origin_seq": ack.Seq, "duplicate": ack.Duplicate, "digest": Digest(data),
				})
				return nil
			}
			err = fmt.Errorf("publish ack: %s", resp.Data)
		}
		select {
		case <-ctx.Done():
			r.rec.Record("publish_failed", Event{"batch": batch, "index": index, "error": err.Error()})
			return fmt.Errorf("publish %s: %w", msgID, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *run) batch(n int) error {
	r.setPhase(fmt.Sprintf("batch%d", n))
	for i := 1; i <= BatchSize; i++ {
		if err := r.publish(n, i); err != nil {
			return err
		}
	}
	r.setPhase(fmt.Sprintf("batch%d_wait", n))
	last := uint64(n * BatchSize)
	if n == 1 {
		// Both downstream streams caught up and the consumer's ack floor, moved
		// only by flow control replies, has reached the origin's last sequence.
		err := r.waitFor(60*time.Second, func(ctx context.Context) (bool, error) {
			for _, name := range []string{MirrorStream, SourceStream} {
				si, err := getStreamInfo(ctx, r.api, name)
				if err != nil || si.State.Msgs != last {
					return false, err
				}
			}
			ci, err := getConsumerInfo(ctx, r.api, OriginStream, Consumer)
			if err != nil {
				return false, err
			}
			return ci.Delivered.Stream == last && ci.AckFloor.Stream == last && ci.NumPending == 0, nil
		})
		if !r.precondition("batch1_caught_up", err == nil, Event{"last": last}) {
			return fmt.Errorf("batch 1 did not settle: %w", err)
		}
		return nil
	}
	ok := r.waitQuiescent(last) == nil
	r.precondition("quiescent", ok, Event{"last": last})
	return nil
}

// waitQuiescent waits until the consumer has delivered and been acked up to
// last with nothing pending, and then until three further idle heartbeats
// pass with no reset request and no change in either downstream count.
func (r *run) waitQuiescent(last uint64) error {
	err := r.waitFor(90*time.Second, func(ctx context.Context) (bool, error) {
		ci, err := getConsumerInfo(ctx, r.api, OriginStream, Consumer)
		if err != nil {
			return false, err
		}
		return ci.Delivered.Stream >= last && ci.AckFloor.Stream >= last && ci.NumPending == 0, nil
	})
	if err != nil {
		return err
	}
	for range 5 {
		before, err := r.downstreamCounts()
		if err != nil {
			return err
		}
		mark := r.lastID()
		if err := r.waitHeartbeats(mark, 3, 30*time.Second); err != nil {
			return err
		}
		after, err := r.downstreamCounts()
		if err != nil {
			return err
		}
		resets := r.countEvents(mark, func(ev Event) bool { return ev["type"] == "reset_request" })
		if resets == 0 && before == after {
			return nil
		}
	}
	return errors.New("downstream streams kept changing")
}

func (r *run) lastID() int64 {
	events := r.rec.Events()
	if len(events) == 0 {
		return 0
	}
	id, _ := events[len(events)-1]["id"].(int64)
	return id
}

func (r *run) downstreamCounts() ([2]uint64, error) {
	var out [2]uint64
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	for i, name := range []string{MirrorStream, SourceStream} {
		si, err := getStreamInfo(ctx, r.api, name)
		if err != nil {
			return out, err
		}
		out[i] = si.State.Msgs
	}
	return out, nil
}

func (r *run) waitHeartbeats(after int64, n int, bound time.Duration) error {
	return r.waitFor(bound, func(context.Context) (bool, error) {
		got := r.countEvents(after, func(ev Event) bool {
			return ev["type"] == "control" && ev["kind"] == "heartbeat"
		})
		return got >= n, nil
	})
}

func (r *run) consumerLeader(ctx context.Context) (string, error) {
	ci, err := getConsumerInfo(ctx, r.api, OriginStream, Consumer)
	if err != nil {
		return "", err
	}
	if ci.Cluster == nil {
		return "", nil
	}
	return ci.Cluster.Leader, nil
}

func (r *run) streamLeader(ctx context.Context, name string) (string, error) {
	si, err := getStreamInfo(ctx, r.api, name)
	if err != nil {
		return "", err
	}
	if si.Cluster == nil {
		return "", nil
	}
	return si.Cluster.Leader, nil
}

// leaderChange moves the consumer's leadership to a node other than its
// current leader and other than the node holding the downstream streams, and
// moves the origin's leadership off that node too, so that restarting it
// later changes no leader of the origin or the consumer.
func (r *run) leaderChange() error {
	r.setPhase("leader_change")
	if len(r.cluster.Nodes) == 1 {
		r.rec.Record("leader_change", Event{"group": "consumer", "skipped": "single node"})
		r.precondition("leader_changed", true, Event{"skipped": "single node"})
		return r.checkNothingInFlight()
	}
	target := r.cluster.Nodes[r.target].Name
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	from, err := r.consumerLeader(ctx)
	if err != nil {
		return fmt.Errorf("consumer leader: %w", err)
	}
	var preferred string
	for _, n := range r.cluster.Nodes {
		if n.Name != from && n.Name != target {
			preferred = n.Name
			break
		}
	}
	raw, err := controller.StepDownConsumer(ctx, r.api, OriginStream, Consumer, preferred)
	ev := Event{"group": "consumer", "from": from, "preferred": preferred, "ok": err == nil}
	if err != nil {
		ev["error"], ev["response"] = err.Error(), string(raw)
	}
	r.rec.Record("stepdown_request", ev)
	var to string
	werr := r.waitFor(60*time.Second, func(ctx context.Context) (bool, error) {
		l, err := r.consumerLeader(ctx)
		to = l
		return err == nil && l != "" && l != from, err
	})
	r.rec.Record("leader_change", Event{"group": "consumer", "from": from, "to": to, "ok": werr == nil})
	if !r.precondition("leader_changed", werr == nil && to != target, Event{"from": from, "to": to}) {
		return fmt.Errorf("consumer leader did not change: %v", werr)
	}
	ol, err := r.streamLeader(ctx, OriginStream)
	if err == nil && ol == target {
		_, serr := controller.StepDownStream(ctx, r.api, OriginStream, preferred)
		var nl string
		werr := r.waitFor(60*time.Second, func(ctx context.Context) (bool, error) {
			l, err := r.streamLeader(ctx, OriginStream)
			nl = l
			return err == nil && l != "" && l != target, err
		})
		ev := Event{"group": "origin", "from": ol, "to": nl, "ok": werr == nil && serr == nil}
		r.rec.Record("leader_change", ev)
	}
	return r.checkNothingInFlight()
}

func (r *run) checkNothingInFlight() error {
	last := uint64(BatchSize)
	var ci *consumerInfo
	err := r.waitFor(60*time.Second, func(ctx context.Context) (bool, error) {
		var err error
		ci, err = getConsumerInfo(ctx, r.api, OriginStream, Consumer)
		if err != nil {
			return false, err
		}
		return ci.Delivered.Stream == last && ci.AckFloor.Stream == last && ci.NumPending == 0 &&
			ci.NumAckPending == 0, nil
	})
	detail := Event{}
	if ci != nil {
		detail = Event{
			"delivered_stream": ci.Delivered.Stream, "ack_floor_stream": ci.AckFloor.Stream,
			"num_pending": ci.NumPending, "num_ack_pending": ci.NumAckPending,
		}
	}
	if !r.precondition("nothing_in_flight", err == nil, detail) {
		return fmt.Errorf("consumer still has work in flight: %v", err)
	}
	return nil
}

// downstreamReset restarts the node holding the mirror and the source. On
// recovery each of them resets the durable consumer itself, through the
// server's own setup path, which is what this timeline needs: both downstream
// streams start counting deliveries afresh from the reset point.
func (r *run) downstreamReset() error {
	r.setPhase("downstream_reset")
	node := r.cluster.Nodes[r.target]
	r.rec.Record("node_stop", Event{"node": node.Name})
	if err := controller.StopNode(r.cluster, r.target); err != nil {
		return &InfraError{err}
	}
	mark := r.rec.Record("node_start", Event{"node": node.Name})
	ctx, cancel := context.WithTimeout(r.ctx, 90*time.Second)
	defer cancel()
	if err := controller.StartNode(ctx, r.cluster, r.target); err != nil {
		return &InfraError{err}
	}
	if _, err := r.cluster.WaitReady(ctx); err != nil {
		return &InfraError{err}
	}
	r.rec.Record("node_ready", Event{"node": node.Name})
	err := r.waitDownstreamReady(mark)
	if !r.precondition("downstream_reset_confirmed", err == nil, nil) {
		return fmt.Errorf("downstream streams did not reset the consumer: %v", err)
	}
	return nil
}

// resend re-sends the first delivery of batch 1, d1, on the deliver subject
// from a connection to the node holding both downstream streams. That node
// hands a publish to its local subscriptions inline, so once the flush
// returns the re-sent delivery sits in both streams' inbound queues, ahead of
// any delivery the consumer issues for batch 2, which is not yet published.
func (r *run) resend() error {
	r.snapshot("before_resend")
	r.setPhase("resend")
	r.mu.Lock()
	d1 := r.firstD1
	r.mu.Unlock()
	if d1 == nil {
		r.precondition("resend_confirmed", false, Event{"reason": "no d1 recorded in batch 1"})
		return errors.New("no first delivery recorded")
	}
	ai := parseAckReply(d1.Reply)
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	before, err := r.streamSubs(ctx)
	if err != nil {
		return fmt.Errorf("subscriptions before re-send: %w", err)
	}
	// The re-send goes out on the watcher's own connection. A separate
	// connection adds an identity the recording cannot account for, and the
	// watcher already holds a subscription on the deliver subject, so one
	// probe on a scratch subject states plainly whether a publish from this
	// connection reaches a subscription on it at all.
	probe := fmt.Sprintf("replay.probe.%d", time.Now().UnixNano())
	probeSub, err := r.watch.SubscribeSync(probe)
	if err != nil {
		return &InfraError{err}
	}
	defer func() { _ = probeSub.Unsubscribe() }()
	probeErr := r.watch.Publish(probe, []byte("probe"))
	if probeErr == nil {
		probeErr = r.watch.FlushTimeout(5 * time.Second)
	}
	probeOK := false
	if probeErr == nil {
		if _, err := probeSub.NextMsg(5 * time.Second); err == nil {
			probeOK = true
		}
	}
	mark := r.rec.Record("resend", Event{
		"reply": d1.Reply, "stream_seq": ai.StreamSeq, "delivery_seq": ai.DelivSeq,
		"parsed": ai.OK, "digest": Digest(d1.Data),
		"node": r.cluster.Nodes[r.target].Name, "connection": "watcher",
		"probe_subject": probe, "probe_delivered": probeOK,
		"stream_sub_msgs_before": subMsgs(before),
	})
	// A client may not publish a message whose reply names the ack subject
	// space, and nats-server enforces that itself: there is no authorization
	// block in the generated config to relax. The reply carries the stream and
	// delivery sequence, so no other subject would do, and the project's own
	// tests reproduce this fault in process for the same reason. The attempt
	// stays, recorded with whatever the server answered, and the timeline goes
	// on to measure what natural traffic shows.
	resendErr := ""
	if err := controller.Resend(r.watch, DeliverSubject, d1); err != nil {
		resendErr = err.Error()
	}
	// The confirmation rests on the effect the timeline needs, the downstream
	// subscriptions taking the message. The watcher's own sight of it is a
	// diagnostic beside that, never the gate: gating on it left the counters
	// unread whenever the observation failed.
	var after []launcher.SubDetail
	err = r.waitFor(15*time.Second, func(ctx context.Context) (bool, error) {
		subs, err := r.streamSubs(ctx)
		if err != nil {
			return false, err
		}
		after = subs
		return delivered(before, after), nil
	})
	observed := r.countEvents(mark, func(ev Event) bool {
		return ev["type"] == "delivery" && ev["reply"] == d1.Reply
	})
	watchErr := ""
	if e := r.watch.LastError(); e != nil {
		watchErr = e.Error()
	}
	r.rec.Record("resend_seen", Event{
		"ok": err == nil, "observed_on_watcher": observed,
		"probe_delivered": probeOK, "resend_error": resendErr,
		"watcher_last_error": watchErr, "stream_sub_msgs_after": subMsgs(after),
	})
	// Counters advancing is not evidence that the re-sent delivery landed:
	// ordinary traffic advances them, which passed both trees on an earlier
	// run. The timeline records what happened and claims nothing.
	r.rec.Record("injection_unavailable", Event{
		"refused":             resendErr != "" || watchErr != "",
		"observed_on_watcher": observed,
		"counters_advanced":   err == nil,
	})
	// Two idle heartbeats queue behind the re-sent delivery on both streams.
	_ = r.waitHeartbeats(mark, 2, 15*time.Second)
	r.snapshot("after_resend")
	return nil
}

// sendOnce publishes data to subject under msgID and returns the stored
// sequence the server assigned and whether it marked the publish a duplicate.
// It shares publish's request-and-retry shape but records no event of its own,
// so its caller can describe the exchange in the terms that caller needs.
func (r *run) sendOnce(subject, msgID string, data []byte) (uint64, bool, error) {
	msg := &nats.Msg{Subject: subject, Data: data, Header: nats.Header{}}
	msg.Header.Set("Nats-Msg-Id", msgID)
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	for {
		attempt, acancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := r.api.RequestMsgWithContext(attempt, msg)
		acancel()
		if err == nil {
			var ack pubAck
			var envelope struct {
				Error *controller.APIError `json:"error"`
			}
			_ = json.Unmarshal(resp.Data, &envelope)
			if envelope.Error == nil && json.Unmarshal(resp.Data, &ack) == nil && ack.Seq > 0 {
				return ack.Seq, ack.Duplicate, nil
			}
			err = fmt.Errorf("publish ack: %s", resp.Data)
		}
		select {
		case <-ctx.Done():
			return 0, false, fmt.Errorf("publish %s: %w", msgID, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// duplicate exercises the one constraint a client may test over the wire: the
// origin's own duplicate window. It publishes the same payload twice to the
// origin subject under one Nats-Msg-Id. The origin stores the first and
// refuses the second, marking the second PubAck a duplicate, and each
// downstream stream ends holding exactly that one copy. No $JS.ACK reply is
// involved, so the ack-space permission that blocks a re-sent delivery does
// not apply here. It records a duplicate event carrying the id, the stored
// sequence, the duplicate flags, and the downstream counts on each side.
func (r *run) duplicate() error {
	r.setPhase("duplicate")
	before, err := r.downstreamCounts()
	if err != nil {
		return fmt.Errorf("downstream counts before duplicate: %w", err)
	}
	data := r.payload(0, 0)
	const msgID = "dup-probe"
	first, dup1, err := r.sendOnce(OriginSubject, msgID, data)
	if err != nil {
		return fmt.Errorf("first duplicate publish: %w", err)
	}
	second, dup2, err := r.sendOnce(OriginSubject, msgID, data)
	if err != nil {
		return fmt.Errorf("second duplicate publish: %w", err)
	}
	want := [2]uint64{before[0] + 1, before[1] + 1}
	settled := r.waitFor(30*time.Second, func(context.Context) (bool, error) {
		c, err := r.downstreamCounts()
		if err != nil {
			return false, err
		}
		return c == want, nil
	}) == nil
	after, err := r.downstreamCounts()
	if err != nil {
		return fmt.Errorf("downstream counts after duplicate: %w", err)
	}
	r.rec.Record("duplicate", Event{
		"msg_id": msgID, "first_seq": first, "second_seq": second,
		"first_duplicate": dup1, "second_duplicate": dup2,
		"mirror_before": before[0], "source_before": before[1],
		"mirror_after": after[0], "source_after": after[1],
		"settled": settled,
	})
	return nil
}

func subMsgs(subs []launcher.SubDetail) map[string]int64 {
	out := map[string]int64{}
	for _, s := range subs {
		out[strconv.FormatUint(s.Cid, 10)+"/"+s.Sid] = s.Msgs
	}
	return out
}

// delivered reports whether every downstream subscription present before the
// re-send received at least one more message after it.
func delivered(before, after []launcher.SubDetail) bool {
	if len(before) < 2 {
		return false
	}
	for _, b := range before {
		found := false
		for _, a := range after {
			if a.Cid == b.Cid && a.Sid == b.Sid && a.Msgs > b.Msgs {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
