package testharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hchaudhary1/micromage/internal/runlog"
)

func TestWorkflowFilesReturnsSortedYAML(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.yaml", "notes.txt", "a.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("name: test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := WorkflowFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join([]string{filepath.Base(paths[0]), filepath.Base(paths[1])}, ","); got != "a.yaml,b.yaml" {
		t.Fatalf("unexpected sorted YAML files %q", got)
	}
}

func TestDecodeJSONLEventsReportsLineErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"workflow_started\"}\nnot-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := DecodeJSONLEvents(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line-specific decode error, got %v", err)
	}
}

func TestRequireCompletedEventLog(t *testing.T) {
	events := []runlog.Event{
		{Type: runlog.EventWorkflowStarted},
		{Type: runlog.EventNodeStarted, NodeID: "agent_smoke"},
		{Type: runlog.EventNodeOutput, NodeID: "agent_smoke", Message: "ok"},
		{Type: runlog.EventNodePassed, NodeID: "agent_smoke"},
		{Type: runlog.EventWorkflowPassed},
	}

	if err := RequireCompletedEventLog(events, "agent_smoke"); err != nil {
		t.Fatal(err)
	}
	if err := RequireCompletedEventLog(events, "missing"); err == nil {
		t.Fatal("expected missing node lifecycle error")
	}
}

func TestSanitizeName(t *testing.T) {
	if got := SanitizeName("one/two three.yaml"); got != "one-two-three-yaml" {
		t.Fatalf("unexpected sanitized name %q", got)
	}
}
