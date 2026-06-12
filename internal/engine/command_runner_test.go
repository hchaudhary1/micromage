package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hchaudhary1/micromage/internal/workflow"
)

func TestCommandRunnerCapturesOutput(t *testing.T) {
	var lines []string
	err := CommandRunner{Dir: t.TempDir()}.Run(context.Background(), "echo", workflow.Node{
		Type:    workflow.NodeCommand,
		Command: "printf 'hello\\nworld\\n'",
	}, func(line string) {
		lines = append(lines, line)
	})

	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, ",") != "hello,world" {
		t.Fatalf("got lines %#v", lines)
	}
}

func TestCommandRunnerSupportsExecutableArgs(t *testing.T) {
	var lines []string
	err := CommandRunner{Dir: t.TempDir()}.Run(context.Background(), "printf", workflow.Node{
		Type:    workflow.NodeCommand,
		Command: "printf",
		Args:    []string{"hello %s\n", "agent"},
	}, func(line string) {
		lines = append(lines, line)
	})

	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, ",") != "hello agent" {
		t.Fatalf("got lines %#v", lines)
	}
}

func TestCommandRunnerReturnsPromptTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := CommandRunner{Dir: t.TempDir()}.Run(ctx, "sleep", workflow.Node{
		Type:    workflow.NodeCommand,
		Command: "sleep 1",
	}, func(string) {})

	if err == nil || !strings.Contains(err.Error(), "timed out or was canceled") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
