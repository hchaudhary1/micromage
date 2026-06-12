//go:build opencode_live

package testharness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hchaudhary1/micromage/internal/engine"
	"github.com/hchaudhary1/micromage/internal/runlog"
	"github.com/hchaudhary1/micromage/internal/workflow"
)

func TestLiveOpenCodeHarnessProducesEventLog(t *testing.T) {
	if os.Getenv(LiveOpenCodeEnv) != "1" {
		t.Skipf("set %s=1 to run the real OpenCode harness", LiveOpenCodeEnv)
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode CLI unavailable: %v", err)
	}

	workdir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "run.jsonl")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := RunLiveOpenCodeTinyWorkflow(ctx, workdir, logPath, OpenCodeModel()); err != nil {
		t.Fatalf("live OpenCode workflow failed: %v", err)
	}
	events, err := DecodeJSONLEvents(logPath)
	if err != nil {
		t.Fatalf("live OpenCode log was not parseable: %v", err)
	}
	if err := RequireCompletedEventLog(events, "agent_smoke"); err != nil {
		t.Fatalf("live OpenCode log missing lifecycle evidence: %v", err)
	}
}

func RunLiveOpenCodeTinyWorkflow(ctx context.Context, workdir, logPath, model string) error {
	wf := &workflow.Workflow{
		Name: "live opencode harness",
		Nodes: map[string]workflow.Node{
			"agent_smoke": {
				Type:   workflow.NodeAgent,
				Prompt: "Return exactly this line and do not edit files: micromage live smoke ok",
			},
		},
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	recorder := runlog.NewRecorder(logFile)
	runner := engine.OpenCodeRunner{Dir: workdir, Model: model}
	// A tiny workflow proves orchestration and logging without letting live tests mutate project files.
	return engine.New(runner, recorder).Run(ctx, wf)
}
