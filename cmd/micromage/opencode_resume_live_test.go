package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLiveOpenCodeResumeAfterApprovedGate(t *testing.T) {
	if os.Getenv("MICROMAGE_OPENCODE_LIVE") != "1" {
		t.Skip("set MICROMAGE_OPENCODE_LIVE=1 to run real OpenCode resume smoke")
	}
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "workflow.yaml")
	logPath := filepath.Join(dir, "run.jsonl")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(workflowPath, []byte(`
name: live opencode resume
nodes:
  prepare:
    type: command
    command: echo prepared
  approve:
    type: human_gate
    message: approve live smoke
    depends_on: [prepare]
  agent:
    type: agent
    prompt: Return exactly "micromage resume live ok" and do not edit files.
    depends_on: [approve]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := execute([]string{"run", "--workflow", workflowPath, "--log", logPath, "--state", statePath, "--workdir", dir}, &out, &out); code == 0 {
		t.Fatalf("expected initial run to pause: %s", out.String())
	}
	if code := execute([]string{"approve", "--state", statePath, "--node", "approve", "--reviewer", "live-test"}, &out, &out); code != 0 {
		t.Fatalf("approve failed: code=%d out=%s", code, out.String())
	}
	model := os.Getenv("MICROMAGE_OPENCODE_MODEL")
	if model == "" {
		model = "opencode/deepseek-v4-flash-free"
	}
	if code := execute([]string{"resume", "--workflow", workflowPath, "--log", logPath, "--state", statePath, "--workdir", dir, "--runner", "provider", "--provider", "opencode", "--model", model}, &out, &out); code != 0 {
		t.Fatalf("live opencode resume failed: code=%d out=%s", code, out.String())
	}
}
