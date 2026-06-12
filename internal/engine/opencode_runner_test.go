package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hchaudhary1/micromage/internal/workflow"
)

func TestOpenCodeRunnerLoadsCommandTemplate(t *testing.T) {
	dir := t.TempDir()
	commandDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandDir, "assist.md"), []byte("Help with $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := OpenCodeRunner{CommandDir: commandDir, Arguments: "tests"}
	prompt, err := runner.nodePrompt(workflow.Node{Type: workflow.NodeAgent, Command: "assist"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "Help with tests" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
}

func TestOpenCodeRunnerInvokesBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"type\":\"message\",\"text\":\"ok COMPLETE\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var lines []string
	err := OpenCodeRunner{Dir: dir, Binary: bin, Model: "opencode/free"}.Run(context.Background(), "agent", workflow.Node{
		Type:   workflow.NodeAgent,
		Prompt: "Say ok",
	}, func(line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "COMPLETE") {
		t.Fatalf("expected fake output, got %#v", lines)
	}
}

func TestOpenCodeRunnerReportsMissingProviderBinary(t *testing.T) {
	err := OpenCodeRunner{Dir: t.TempDir(), Binary: "definitely-missing-opencode"}.Run(context.Background(), "agent", workflow.Node{
		Type:   workflow.NodeAgent,
		Prompt: "Say ok",
	}, func(string) {})
	if err == nil {
		t.Fatal("expected missing binary error")
	}
	for _, want := range []string{"provider \"opencode\"", "definitely-missing-opencode", "--provider-binary"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestOpenCodeRunnerLoopChecksCompletionMarker(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho COMPLETE\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := OpenCodeRunner{Dir: dir, Binary: bin, Model: "opencode/free"}.Run(context.Background(), "loop", workflow.Node{
		Type: workflow.NodeLoop,
		Loop: &workflow.Loop{Prompt: "loop", Until: "COMPLETE", MaxIterations: 2},
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
}
