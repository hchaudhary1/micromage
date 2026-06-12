package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hchaudhary1/micromage/internal/engine"
	"github.com/hchaudhary1/micromage/internal/gitspace"
	"github.com/hchaudhary1/micromage/internal/provider"
	"github.com/hchaudhary1/micromage/internal/quality"
	"github.com/hchaudhary1/micromage/internal/runlog"
	"github.com/hchaudhary1/micromage/internal/state"
	"github.com/hchaudhary1/micromage/internal/watch"
	"github.com/hchaudhary1/micromage/internal/workflow"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr))
}

func execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "validate":
		return validateCmd(args[1:], stdout, stderr)
	case "run":
		return runCmd(args[1:], stdout, stderr)
	case "approve":
		return approveCmd(args[1:], stdout, stderr)
	case "resume":
		return resumeCmd(args[1:], stdout, stderr)
	case "quality":
		return qualityCmd(args[1:], stdout, stderr)
	case "watch":
		return watchCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func validateCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workflowPath := fs.String("workflow", "", "workflow YAML path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *workflowPath == "" {
		fmt.Fprintln(stderr, "--workflow is required")
		return 2
	}
	if _, err := loadWorkflow(*workflowPath); err != nil {
		fmt.Fprintf(stderr, "invalid workflow: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s is valid\n", *workflowPath)
	return 0
}

func runCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workflowPath := fs.String("workflow", "", "workflow YAML path")
	logPath := fs.String("log", ".micromage/run.jsonl", "JSONL event log path")
	statePath := fs.String("state", "", "persisted run state path")
	workdir := fs.String("workdir", ".", "command working directory")
	runnerKind := fs.String("runner", "command", "runner to use for agent nodes: command, provider, opencode, or codex")
	providerName := fs.String("provider", provider.OpenCode, "AI CLI provider preset for agent nodes")
	providerBinary := fs.String("provider-binary", "", "override provider executable path")
	model := fs.String("model", "", "AI provider model")
	commandDir := fs.String("command-dir", "", "directory containing command prompt templates")
	arguments := fs.String("arguments", "", "workflow arguments exposed to prompt templates")
	isolate := fs.Bool("isolate", false, "run commands in a git worktree")
	runID := fs.String("run-id", "local", "run identifier for isolation and persisted state")
	worktreeBase := fs.String("worktree-base", "", "base directory for isolated worktrees")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *workflowPath == "" {
		fmt.Fprintln(stderr, "--workflow is required")
		return 2
	}

	wf, err := loadWorkflow(*workflowPath)
	if err != nil {
		fmt.Fprintf(stderr, "invalid workflow: %v\n", err)
		return 1
	}

	runDir := *workdir
	var cleanup func() error
	if *isolate {
		runDir, cleanup, err = gitspace.Prepare(*workdir, *runID, *worktreeBase)
		if err != nil {
			fmt.Fprintf(stderr, "prepare worktree: %v\n", err)
			return 1
		}
		defer func() {
			if err := cleanup(); err != nil {
				fmt.Fprintf(stderr, "cleanup worktree: %v\n", err)
			}
		}()
	}

	logFile, err := openLog(*logPath)
	if err != nil {
		fmt.Fprintf(stderr, "open log: %v\n", err)
		return 1
	}
	defer logFile.Close()

	rec := runlog.NewRecorder(logFile)
	runner, err := buildRunner(*runnerKind, runDir, *providerName, *providerBinary, *model, resolveCommandDir(*commandDir, *workflowPath), *arguments)
	if err != nil {
		fmt.Fprintf(stderr, "build runner: %v\n", err)
		return 2
	}
	if *statePath == "" {
		*statePath = defaultStatePath(*runID)
	}
	runState := state.NewRun(*runID, wf.Name, *workflowPath)
	if err := state.Save(*statePath, runState); err != nil {
		fmt.Fprintf(stderr, "save state: %v\n", err)
		return 1
	}
	err = engine.New(runner, rec).RunWithOptions(context.Background(), wf, engine.RunOptions{
		OnNodeResult: persistNodeResult(*statePath, runState),
	})
	if err != nil {
		fmt.Fprintf(stderr, "workflow failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "workflow completed; log: %s state: %s\n", *logPath, *statePath)
	return 0
}

func watchCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logPath := fs.String("log", ".micromage/run.jsonl", "JSONL event log path")
	once := fs.Bool("once", false, "render one dashboard snapshot and exit")
	limit := fs.Int("limit", 10, "recent output lines to display")
	interval := fs.Duration("interval", time.Second, "refresh interval")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *logPath == "" {
		fmt.Fprintln(stderr, "--log is required")
		return 2
	}
	// Operators need a dependency-free way to inspect live agent logs over SSH.
	err := watch.Run(context.Background(), stdout, watch.Options{
		LogPath: *logPath,
		Once:    *once,
		Limit:   *limit,
		Every:   *interval,
	})
	if err != nil {
		fmt.Fprintf(stderr, "watch failed: %v\n", err)
		return 1
	}
	return 0
}

func approveCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	statePath := fs.String("state", defaultStatePath("local"), "persisted run state path")
	nodeID := fs.String("node", "", "paused human gate node id")
	reviewer := fs.String("reviewer", os.Getenv("USER"), "reviewer name")
	comment := fs.String("comment", "", "approval comment")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *nodeID == "" {
		fmt.Fprintln(stderr, "--node is required")
		return 2
	}
	runState, err := state.Load(*statePath)
	if err != nil {
		fmt.Fprintf(stderr, "load state: %v\n", err)
		return 1
	}
	if err := runState.Approve(*nodeID, *reviewer, *comment, time.Now().UTC()); err != nil {
		fmt.Fprintf(stderr, "approve gate: %v\n", err)
		return 1
	}
	if err := state.Save(*statePath, runState); err != nil {
		fmt.Fprintf(stderr, "save state: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "approved %s in %s\n", *nodeID, *statePath)
	return 0
}

func resumeCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workflowPath := fs.String("workflow", "", "workflow YAML path")
	logPath := fs.String("log", ".micromage/run.jsonl", "JSONL event log path")
	statePath := fs.String("state", defaultStatePath("local"), "persisted run state path")
	workdir := fs.String("workdir", ".", "command working directory")
	runnerKind := fs.String("runner", "command", "runner to use for agent nodes: command, provider, opencode, or codex")
	providerName := fs.String("provider", provider.OpenCode, "AI CLI provider preset for agent nodes")
	providerBinary := fs.String("provider-binary", "", "override provider executable path")
	model := fs.String("model", "", "AI provider model")
	commandDir := fs.String("command-dir", "", "directory containing command prompt templates")
	arguments := fs.String("arguments", "", "workflow arguments exposed to prompt templates")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *workflowPath == "" {
		fmt.Fprintln(stderr, "--workflow is required")
		return 2
	}
	wf, err := loadWorkflow(*workflowPath)
	if err != nil {
		fmt.Fprintf(stderr, "invalid workflow: %v\n", err)
		return 1
	}
	runState, err := state.Load(*statePath)
	if err != nil {
		fmt.Fprintf(stderr, "load state: %v\n", err)
		return 1
	}
	if runState.PausedNode != "" {
		fmt.Fprintf(stderr, "run is paused at %s; approve it before resume\n", runState.PausedNode)
		return 1
	}
	logFile, err := openLog(*logPath)
	if err != nil {
		fmt.Fprintf(stderr, "open log: %v\n", err)
		return 1
	}
	defer logFile.Close()
	runner, err := buildRunner(*runnerKind, *workdir, *providerName, *providerBinary, *model, resolveCommandDir(*commandDir, *workflowPath), *arguments)
	if err != nil {
		fmt.Fprintf(stderr, "build runner: %v\n", err)
		return 2
	}
	err = engine.New(runner, runlog.NewRecorder(logFile)).RunWithOptions(context.Background(), wf, engine.RunOptions{
		InitialResults: snapshotsFromState(runState),
		OnNodeResult:   persistNodeResult(*statePath, runState),
	})
	if err != nil {
		fmt.Fprintf(stderr, "workflow failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "workflow completed; log: %s state: %s\n", *logPath, *statePath)
	return 0
}

func qualityCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "quality subcommand is required")
		return 2
	}
	switch args[0] {
	case "pre-commit":
		return preCommitCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown quality subcommand %q\n", args[0])
		return 2
	}
}

func preCommitCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("quality pre-commit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	threshold := fs.Float64("threshold", quality.DefaultCoverageThreshold, "minimum global Go coverage percent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	result, err := quality.RunPreCommit(context.Background(), quality.PreCommitOptions{
		Repo:              *repo,
		CoverageThreshold: *threshold,
	})
	report := quality.FormatPreCommitResult(result, err)
	if err != nil {
		fmt.Fprint(stderr, report)
		return 1
	}
	fmt.Fprint(stdout, report)
	return 0
}

func buildRunner(kind, dir, providerName, providerBinary, model, commandDir, arguments string) (engine.Runner, error) {
	switch kind {
	case "command":
		return engine.CommandRunner{Dir: dir}, nil
	case "provider":
		return engine.OpenCodeRunner{Dir: dir, Provider: providerName, Binary: providerBinary, Model: model, CommandDir: commandDir, Arguments: arguments}, nil
	case provider.OpenCode, provider.Codex:
		// Runner aliases keep older commands working while provider presets become first-class.
		return engine.OpenCodeRunner{Dir: dir, Provider: kind, Binary: providerBinary, Model: model, CommandDir: commandDir, Arguments: arguments}, nil
	default:
		return nil, fmt.Errorf("unknown runner %q", kind)
	}
}

func resolveCommandDir(commandDir, workflowPath string) string {
	if commandDir != "" {
		return commandDir
	}
	defaultsDir := filepath.Dir(workflowPath)
	workflowsDir := filepath.Dir(defaultsDir)
	rootDir := filepath.Dir(workflowsDir)
	candidate := filepath.Join(rootDir, "commands", "defaults")
	if stat, err := os.Stat(candidate); err == nil && stat.IsDir() {
		return candidate
	}
	return ""
}

func loadWorkflow(path string) (*workflow.Workflow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return workflow.Parse(f)
}

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// Appending preserves run evidence when users tail the same log path.
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func defaultStatePath(runID string) string {
	return filepath.Join(".micromage", "state", runID+".json")
}

func persistNodeResult(path string, runState *state.RunState) func(engine.NodeSnapshot) {
	return func(snap engine.NodeSnapshot) {
		switch snap.Status {
		case "passed":
			runState.MarkPassed(snap.ID, snap.Output)
		case "failed":
			runState.MarkFailed(snap.ID, snap.Output, snap.Message)
		case "skipped":
			runState.MarkSkipped(snap.ID, snap.Message)
		case "paused":
			runState.MarkPaused(snap.ID, snap.Message)
		}
		// Persist after each node so a paused approval can survive process exit.
		_ = state.Save(path, runState)
	}
}

func snapshotsFromState(runState *state.RunState) map[string]engine.NodeSnapshot {
	snapshots := map[string]engine.NodeSnapshot{}
	for id, node := range runState.Nodes {
		snapshots[id] = engine.NodeSnapshot{
			ID:      id,
			Status:  string(node.Status),
			Output:  node.Output,
			Message: node.Message,
		}
	}
	return snapshots
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: micromage <validate|run|approve|resume|quality|watch> [options]")
}
