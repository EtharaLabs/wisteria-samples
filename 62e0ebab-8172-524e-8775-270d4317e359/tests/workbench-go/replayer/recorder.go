package replayer

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Event is one line of recording.jsonl. Every event carries a monotonically
// increasing id, its type and a millisecond offset from the start of the run.
// The offset is informational only; no check reads it.
type Event map[string]any

// Recorder appends events to recording.jsonl and keeps them in memory.
type Recorder struct {
	mu     sync.Mutex
	file   *os.File
	buf    *bufio.Writer
	start  time.Time
	nextID int64
	events []Event
	err    error
	notify chan struct{}
}

// NewRecorder creates path and returns a recorder writing to it.
func NewRecorder(path string) (*Recorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Recorder{file: f, buf: bufio.NewWriter(f), start: time.Now(), nextID: 1, notify: make(chan struct{})}, nil
}

// Record appends one event and returns its id.
func (r *Recorder) Record(typ string, fields Event) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := Event{}
	for k, v := range fields {
		ev[k] = v
	}
	id := r.nextID
	r.nextID++
	ev["id"] = id
	ev["type"] = typ
	ev["t_ms"] = time.Since(r.start).Milliseconds()
	line, err := json.Marshal(ev)
	if err == nil {
		_, err = r.buf.Write(append(line, '\n'))
	}
	if err != nil && r.err == nil {
		r.err = err
	}
	r.events = append(r.events, ev)
	close(r.notify)
	r.notify = make(chan struct{})
	return id
}

// Changed returns a channel closed on the next recorded event.
func (r *Recorder) Changed() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.notify
}

// Events returns a copy of the events recorded so far.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Close flushes and closes the recording.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.buf.Flush(); err != nil && r.err == nil {
		r.err = err
	}
	if err := r.file.Close(); err != nil && r.err == nil {
		r.err = err
	}
	return r.err
}
