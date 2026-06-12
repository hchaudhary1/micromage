package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hchaudhary1/micromage/internal/state"
)

func TestExecuteValidateWorkflow(t *testing.T) {
	workflowPath := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(workflowPath, []byte(`
name: smoke
nodes:
  hello:
    type: command
    command: echo hello
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := execute([]string{"validate", "--workflow", workflowPath}, &out, &out)
	if code != 0 {
		t.Fatalf("validate failed: code=%d out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "valid") {
		t.Fatalf("expected validation message, got %s", out.String())
	}
}

func TestExecuteRunWorkflowWritesLog(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "workflow.yaml")
	logPath := filepath.Join(dir, "run.jsonl")
	if err := os.WriteFile(workflowPath, []byte(`
name: run smoke
nodes:
  hello:
    type: command
    command: echo hello
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := execute([]string{"run", "--workflow", workflowPath, "--log", logPath, "--workdir", dir}, &out, &out)
	if code != 0 {
		t.Fatalf("run failed: code=%d out=%s", code, out.String())
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBytes), "node_output") || !strings.Contains(string(logBytes), "hello") {
		t.Fatalf("expected node output event, got %s", logBytes)
	}
}

func TestExecuteRunProviderPresetWithBinaryOverride(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	argsPath := filepath.Join(dir, "args.txt")
	envPath := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsPath) + "\nprintf '%s\\n' \"$MICROMAGE_PROVIDER $MICROMAGE_NODE_ID\" > " + shellQuote(envPath) + "\necho '{\"type\":\"message\",\"text\":\"preset ok\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(dir, "workflow.yaml")
	logPath := filepath.Join(dir, "run.jsonl")
	if err := os.WriteFile(workflowPath, []byte(`
name: provider smoke
nodes:
  assist:
    type: agent
    prompt: Say ok
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := execute([]string{"run", "--workflow", workflowPath, "--log", logPath, "--workdir", dir, "--runner", "provider", "--provider", "opencode", "--provider-binary", bin}, &out, &out)
	if code != 0 {
		t.Fatalf("run failed: code=%d out=%s", code, out.String())
	}
	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	argsText := string(argsBytes)
	for _, want := range []string{"run\n", "--model\nopencode/deepseek-v4-flash-free\n", "--format\njson\n", "--dir\n" + dir + "\n", "--file\n"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("args missing %q in:\n%s", want, argsText)
		}
	}
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(envBytes)) != "opencode assist" {
		t.Fatalf("unexpected provider env: %s", envBytes)
	}
}

func TestExecuteWatchRendersRunLog(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "workflow.yaml")
	logPath := filepath.Join(dir, "run.jsonl")
	if err := os.WriteFile(workflowPath, []byte(`
name: watch smoke
nodes:
  hello:
    type: command
    command: echo watched
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var runOut bytes.Buffer
	code := execute([]string{"run", "--workflow", workflowPath, "--log", logPath, "--workdir", dir}, &runOut, &runOut)
	if code != 0 {
		t.Fatalf("run failed: code=%d out=%s", code, runOut.String())
	}

	var watchOut bytes.Buffer
	code = execute([]string{"watch", "--log", logPath, "--once", "--limit", "5"}, &watchOut, &watchOut)
	if code != 0 {
		t.Fatalf("watch failed: code=%d out=%s", code, watchOut.String())
	}
	rendered := watchOut.String()
	for _, want := range []string{"Micromage run dashboard", "Workflow: watch smoke [passed]", "hello", "watched"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, rendered)
		}
	}
}

func TestResolveCommandDirFromWorkflowPath(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, "workflows", "defaults")
	commandDir := filepath.Join(root, "commands", "defaults")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := resolveCommandDir("", filepath.Join(workflowDir, "assist.yaml"))
	if got != commandDir {
		t.Fatalf("got %q, want %q", got, commandDir)
	}
}

func TestResolveCommandDirFromVendoredAssets(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, "assets", "defaults", "workflows")
	commandDir := filepath.Join(root, "assets", "defaults", "commands")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := resolveCommandDir("", filepath.Join(workflowDir, "micromage-assist.yaml"))
	if got != commandDir {
		t.Fatalf("got %q, want %q", got, commandDir)
	}
}

func TestExecuteApproveAndResumeSkipsCompletedNodes(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "workflow.yaml")
	logPath := filepath.Join(dir, "run.jsonl")
	statePath := filepath.Join(dir, "state.json")
	countPath := filepath.Join(dir, "count.txt")
	if err := os.WriteFile(workflowPath, []byte(`
name: gated release
nodes:
  setup:
    type: command
    command: printf x >> count.txt
  review:
    type: human_gate
    message: approve release
    depends_on: [setup]
  ship:
    type: command
    command: echo shipped
    depends_on: [review]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := execute([]string{"run", "--workflow", workflowPath, "--log", logPath, "--state", statePath, "--workdir", dir}, &out, &out)
	if code == 0 || !strings.Contains(out.String(), "human gate") {
		t.Fatalf("expected gated run to pause: code=%d out=%s", code, out.String())
	}
	code = execute([]string{"approve", "--state", statePath, "--node", "review", "--reviewer", "tester"}, &out, &out)
	if code != 0 {
		t.Fatalf("approve failed: code=%d out=%s", code, out.String())
	}
	code = execute([]string{"resume", "--workflow", workflowPath, "--log", logPath, "--state", statePath, "--workdir", dir}, &out, &out)
	if code != 0 {
		t.Fatalf("resume failed: code=%d out=%s", code, out.String())
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "x" {
		t.Fatalf("setup reran during resume, count=%q", count)
	}
	runState, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if runState.Nodes["ship"].Status != state.StatusPassed {
		t.Fatalf("ship was not persisted as passed: %#v", runState.Nodes["ship"])
	}
}

func TestExecuteApproveRejectsInvalidGate(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	runState := state.NewRun("local", "gated", "workflow.yaml")
	runState.MarkPaused("review", "approve")
	if err := state.Save(statePath, runState); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := execute([]string{"approve", "--state", statePath, "--node", "deploy"}, &out, &out)
	if code == 0 {
		t.Fatalf("expected invalid approval to fail: %s", out.String())
	}
	reloaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PausedNode != "review" {
		t.Fatalf("invalid approval changed state: %#v", reloaded)
	}
}

func TestExecuteResumeUsesFakeOpenCodeAfterGate(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	argsLog := filepath.Join(dir, "opencode-args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + shellQuote(argsLog) + "\necho '{\"type\":\"message\",\"text\":\"ok COMPLETE\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(dir, "workflow.yaml")
	logPath := filepath.Join(dir, "run.jsonl")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(workflowPath, []byte(`
name: opencode gated
nodes:
  prepare:
    type: command
    command: printf x >> count.txt
  approve:
    type: human_gate
    message: approve agent
    depends_on: [prepare]
  agent:
    type: agent
    prompt: Say COMPLETE without editing files.
    depends_on: [approve]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := execute([]string{"run", "--workflow", workflowPath, "--log", logPath, "--state", statePath, "--workdir", dir}, &out, &out); code == 0 {
		t.Fatalf("expected initial run to pause: %s", out.String())
	}
	if code := execute([]string{"approve", "--state", statePath, "--node", "approve"}, &out, &out); code != 0 {
		t.Fatalf("approve failed: code=%d out=%s", code, out.String())
	}
	if code := execute([]string{"resume", "--workflow", workflowPath, "--log", logPath, "--state", statePath, "--workdir", dir, "--runner", "provider", "--provider", "opencode", "--provider-binary", bin, "--model", "opencode/free"}, &out, &out); code != 0 {
		t.Fatalf("opencode resume failed: code=%d out=%s", code, out.String())
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--model\nopencode/free") {
		t.Fatalf("fake opencode was not invoked with model: %s", args)
	}
}

func TestExecuteQualityPreCommitRejectsBannedStagedContent(t *testing.T) {
	dir := t.TempDir()
	runTestCmd(t, dir, "git", "init")
	runTestCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runTestCmd(t, dir, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/cli\n\ngo 1.26.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("Generated with "+"Claude Code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCmd(t, dir, "git", "add", ".")

	var out bytes.Buffer
	code := execute([]string{"quality", "pre-commit", "--repo", dir}, &out, &out)
	if code != 1 {
		t.Fatalf("got code=%d, want 1; out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "banned attribution terms") {
		t.Fatalf("expected banned terms report, got %s", out.String())
	}
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func runTestCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}
