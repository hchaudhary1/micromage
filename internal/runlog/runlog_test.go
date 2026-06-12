package runlog

import (
	"bytes"
	"strings"
	"testing"
)

func TestRecorderStoresAndWritesJSONL(t *testing.T) {
	var out bytes.Buffer
	rec := NewRecorder(&out)

	rec.Record(Event{Type: EventNodeOutput, NodeID: "test", Message: "hello"})

	events := rec.Events()
	if len(events) != 1 || events[0].Message != "hello" {
		t.Fatalf("unexpected events: %#v", events)
	}
	if !strings.Contains(out.String(), `"type":"node_output"`) {
		t.Fatalf("expected JSONL event, got %s", out.String())
	}
}
