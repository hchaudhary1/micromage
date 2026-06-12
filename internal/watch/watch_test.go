package watch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hchaudhary1/micromage/internal/runlog"
)

func TestReadModelTracksWorkflowNodesAndRecentOutput(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"type":"workflow_started","message":"demo"}`,
		`{"type":"node_started","node_id":"plan"}`,
		`{"type":"node_output","node_id":"plan","message":"one"}`,
		`not-json`,
		`{"type":"node_passed","node_id":"plan"}`,
		`{"type":"workflow_passed","message":"demo"}`,
	}, "\n"))

	model, err := ReadModel(input, 3)
	if err != nil {
		t.Fatal(err)
	}
	if model.WorkflowName != "demo" || model.WorkflowStatus != StatusPassed {
		t.Fatalf("unexpected workflow state: %#v", model)
	}
	if got := model.Nodes["plan"].Status; got != StatusPassed {
		t.Fatalf("node status = %s, want %s", got, StatusPassed)
	}
	if len(model.Recent) != 1 || !strings.Contains(model.Recent[0], "one") {
		t.Fatalf("unexpected recent output: %#v", model.Recent)
	}
	if len(model.Errors) != 1 || !strings.Contains(model.Errors[0], "line 4") {
		t.Fatalf("expected malformed line warning, got %#v", model.Errors)
	}
}

func TestRenderShowsDashboardSummary(t *testing.T) {
	model := NewModel()
	model.Apply(event("workflow_started", "", "demo"), 5)
	model.Apply(event("node_started", "build", ""), 5)
	model.Apply(event("node_output", "build", "compiled"), 5)
	model.Apply(event("node_failed", "build", "exit status 1"), 5)
	model.Apply(event("workflow_failed", "", "node build failed"), 5)

	var out bytes.Buffer
	model.Render(&out, 5)
	rendered := out.String()
	for _, want := range []string{
		"Micromage run dashboard",
		"Workflow: demo [failed]",
		"build                    failed - exit status 1",
		"build: compiled",
		"workflow: node build failed",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered dashboard missing %q:\n%s", want, rendered)
		}
	}
}

func event(kind, nodeID, message string) runlog.Event {
	return runlog.Event{Type: runlog.EventType(kind), NodeID: nodeID, Message: message}
}
