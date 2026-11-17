// Package controller injects the faults of a replay timeline: consumer leader
// step down, consumer reset, re-sending one already-issued delivery, node stop
// and start, and bounded CPU load. It speaks only the client protocol.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"replay.local/workbench/launcher"
)

// APIError is the error object of a JetStream API response.
type APIError struct {
	Code        int    `json:"code"`
	ErrCode     int    `json:"err_code"`
	Description string `json:"description"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error %d/%d: %s", e.Code, e.ErrCode, e.Description)
}

// Request sends a JetStream API request and decodes the response into out.
// It retries "no responders" while the deadline allows, since an API request
// can race a leader election. Any API error in the response is returned.
func Request(ctx context.Context, nc *nats.Conn, subject string, body any, out any) ([]byte, error) {
	var payload []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		payload = b
	default:
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		msg, err := nc.RequestWithContext(attempt, subject, payload)
		cancel()
		if err == nil {
			var envelope struct {
				Error *APIError `json:"error"`
			}
			if jerr := json.Unmarshal(msg.Data, &envelope); jerr != nil {
				return msg.Data, fmt.Errorf("decode %s response: %w", subject, jerr)
			}
			if out != nil {
				if jerr := json.Unmarshal(msg.Data, out); jerr != nil {
					return msg.Data, fmt.Errorf("decode %s response: %w", subject, jerr)
				}
			}
			if envelope.Error != nil {
				return msg.Data, envelope.Error
			}
			return msg.Data, nil
		}
		retryable := errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrTimeout) ||
			errors.Is(err, context.DeadlineExceeded) || !nc.IsConnected()
		if !retryable || ctx.Err() != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("%s: %w (last error: %v)", subject, ctx.Err(), err)
			}
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: %w (last error: %v)", subject, ctx.Err(), err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// StepDownConsumer asks the consumer's leader to step down, preferring the named server.
func StepDownConsumer(ctx context.Context, nc *nats.Conn, stream, consumer, preferred string) ([]byte, error) {
	var body any
	if preferred != "" {
		body = map[string]any{"placement": map[string]string{"preferred": preferred}}
	}
	subject := fmt.Sprintf("$JS.API.CONSUMER.LEADER.STEPDOWN.%s.%s", stream, consumer)
	return Request(ctx, nc, subject, body, nil)
}

// StepDownStream asks the stream's leader to step down, preferring the named server.
func StepDownStream(ctx context.Context, nc *nats.Conn, stream, preferred string) ([]byte, error) {
	var body any
	if preferred != "" {
		body = map[string]any{"placement": map[string]string{"preferred": preferred}}
	}
	return Request(ctx, nc, "$JS.API.STREAM.LEADER.STEPDOWN."+stream, body, nil)
}

// ResetConsumer sends a consumer reset request. Seq 0 resets to the ack floor.
func ResetConsumer(ctx context.Context, nc *nats.Conn, stream, consumer string, seq uint64) ([]byte, error) {
	var body any
	if seq > 0 {
		body = map[string]uint64{"seq": seq}
	}
	subject := fmt.Sprintf("$JS.API.CONSUMER.RESET.%s.%s", stream, consumer)
	return Request(ctx, nc, subject, body, nil)
}

// Resend publishes one already-issued delivery again on the consumer's
// deliver subject: same reply subject, so the same stream and delivery
// sequences, same headers and same payload. It flushes, so once it returns
// the server the connection is attached to has processed the publish and
// handed it to every local subscription.
func Resend(nc *nats.Conn, deliverSubject string, delivery *nats.Msg) error {
	if delivery == nil {
		return errors.New("controller: no delivery to re-send")
	}
	msg := &nats.Msg{Subject: deliverSubject, Reply: delivery.Reply, Data: delivery.Data}
	if len(delivery.Header) > 0 {
		msg.Header = nats.Header{}
		for k, vs := range delivery.Header {
			for _, v := range vs {
				msg.Header.Add(k, v)
			}
		}
	}
	if err := nc.PublishMsg(msg); err != nil {
		return err
	}
	return nc.FlushTimeout(5 * time.Second)
}

// StopNode stops node i of the cluster gracefully.
func StopNode(c *launcher.Cluster, i int) error {
	return c.StopNode(i, 15*time.Second)
}

// StartNode starts node i and waits until it is healthy.
func StartNode(ctx context.Context, c *launcher.Cluster, i int) error {
	if err := c.StartNode(i); err != nil {
		return err
	}
	return c.WaitNodeHealthy(ctx, i)
}

// Load keeps n goroutines busy until the returned stop function is called.
// n is bounded by the number of CPUs.
func Load(n int) (stop func()) {
	if n <= 0 {
		return func() {}
	}
	if limit := runtime.NumCPU(); n > limit {
		n = limit
	}
	var quit atomic.Bool
	var wg sync.WaitGroup
	var sink atomic.Uint64
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var x uint64 = 1
			for !quit.Load() {
				for range 1 << 16 {
					x = x*6364136223846793005 + 1442695040888963407
				}
			}
			sink.Add(x)
		}()
	}
	return func() {
		quit.Store(true)
		wg.Wait()
	}
}
