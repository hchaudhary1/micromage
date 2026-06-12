package runlog

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type EventType string

const (
	EventWorkflowStarted EventType = "workflow_started"
	EventWorkflowPassed  EventType = "workflow_passed"
	EventWorkflowFailed  EventType = "workflow_failed"
	EventNodeStarted     EventType = "node_started"
	EventNodePassed      EventType = "node_passed"
	EventNodeFailed      EventType = "node_failed"
	EventNodeSkipped     EventType = "node_skipped"
	EventNodePaused      EventType = "node_paused"
	EventNodeOutput      EventType = "node_output"
)

type Event struct {
	Time    time.Time `json:"time"`
	Type    EventType `json:"type"`
	NodeID  string    `json:"node_id,omitempty"`
	Message string    `json:"message,omitempty"`
}

type Recorder struct {
	mu     sync.Mutex
	writer io.Writer
	events []Event
}

func NewRecorder(w io.Writer) *Recorder {
	return &Recorder{writer: w}
}

func (r *Recorder) Record(event Event) {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	if r.writer == nil {
		return
	}
	// JSONL logs give operators a stable tailable view into hidden agent output.
	if err := json.NewEncoder(r.writer).Encode(event); err != nil {
		_, _ = fmt.Fprintf(r.writer, `{"type":"log_error","message":%q}`+"\n", err.Error())
	}
}

func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}
