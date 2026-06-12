package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveOpenCodeWatchSmoke(t *testing.T) {
	if os.Getenv("MICROMAGE_OPENCODE_LIVE") != "1" {
		t.Skip("set MICROMAGE_OPENCODE_LIVE=1 to run real OpenCode dashboard smoke")
	}
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "workflow.yaml")
	logPath := filepath.Join(dir, "run.jsonl")
	if err := os.WriteFile(workflowPath, []byte(`
name: live watch smoke
nodes:
  agent:
    type: agent
    prompt: "Return exactly this line and do not edit files: micromage watch live ok"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("MICROMAGE_OPENCODE_MODEL")
	if model == "" {
		model = "opencode/deepseek-v4-flash-free"
	}

	var runOut bytes.Buffer
	code := execute([]string{
		"run",
		"--workflow", workflowPath,
		"--log", logPath,
		"--workdir", dir,
		"--runner", "opencode",
		"--model", model,
	}, &runOut, &runOut)
	if code != 0 {
		t.Fatalf("live opencode run failed: code=%d out=%s", code, runOut.String())
	}

	var watchOut bytes.Buffer
	code = execute([]string{"watch", "--log", logPath, "--once", "--limit", "20"}, &watchOut, &watchOut)
	if code != 0 {
		t.Fatalf("watch failed: code=%d out=%s", code, watchOut.String())
	}
	rendered := watchOut.String()
	for _, want := range []string{"Workflow: live watch smoke [passed]", "agent                    passed", "agent: {\"type\":\"step_start\""} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, rendered)
		}
	}
}
