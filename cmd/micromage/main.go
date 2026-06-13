package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hchaudhary1/micromage/internal/detach"
	"github.com/hchaudhary1/micromage/internal/engine"
	"github.com/hchaudhary1/micromage/internal/gitspace"
	"github.com/hchaudhary1/micromage/internal/provider"
	"github.com/hchaudhary1/micromage/internal/quality"
	"github.com/hchaudhary1/micromage/internal/runlog"
	"github.com/hchaudhary1/micromage/internal/runregistry"
	"github.com/hchaudhary1/micromage/internal/state"
	"github.com/hchaudhary1/micromage/internal/watch"
	"github.com/hchaudhary1/micromage/internal/workflow"
)

var (
	detachedSpawner detach.Spawner = detach.Launcher{}
	executablePath                 = os.Executable
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
	case "__run-detached":
		return detachedChildCmd(args[1:], stdout, stderr)
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
	case "runs":
		return runsCmd(args[1:], stdout, stderr)
	case "status":
		return statusCmd(args[1:], stdout, stderr)
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
	cfg, wf, explicit, code := parseRunConfig(args, stderr)
	if code != 0 {
		return code
	}
	if cfg.Detach {
		return startDetachedRun(args, cfg, wf, explicit, stdout, stderr)
	}
	code, _ = executeRun(cfg, wf, stdout, stderr)
	return code
}

type runConfig struct {
	WorkflowPath   string
	LogPath        string
	StatePath      string
	Workdir        string
	RunnerKind     string
	ProviderName   string
	ProviderBinary string
	Model          string
	CommandDir     string
	Arguments      string
	Isolate        bool
	RunID          string
	WorktreeBase   string
	Detach         bool
}

func parseRunConfig(args []string, stderr io.Writer) (runConfig, *workflow.Workflow, map[string]bool, int) {
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
	detachRun := fs.Bool("detach", false, "start workflow in the background and return immediately")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, nil, nil, 2
	}
	if *workflowPath == "" {
		fmt.Fprintln(stderr, "--workflow is required")
		return runConfig{}, nil, nil, 2
	}

	wf, err := loadWorkflow(*workflowPath)
	if err != nil {
		fmt.Fprintf(stderr, "invalid workflow: %v\n", err)
		return runConfig{}, nil, nil, 1
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
	})
	return runConfig{
		WorkflowPath:   *workflowPath,
		LogPath:        *logPath,
		StatePath:      *statePath,
		Workdir:        *workdir,
		RunnerKind:     *runnerKind,
		ProviderName:   *providerName,
		ProviderBinary: *providerBinary,
		Model:          *model,
		CommandDir:     *commandDir,
		Arguments:      *arguments,
		Isolate:        *isolate,
		RunID:          *runID,
		WorktreeBase:   *worktreeBase,
		Detach:         *detachRun,
	}, wf, explicit, 0
}

func executeRun(cfg runConfig, wf *workflow.Workflow, stdout, stderr io.Writer) (int, error) {
	runDir := cfg.Workdir
	var cleanup func() error
	if cfg.Isolate {
		var err error
		runDir, cleanup, err = gitspace.Prepare(cfg.Workdir, cfg.RunID, cfg.WorktreeBase)
		if err != nil {
			fmt.Fprintf(stderr, "prepare worktree: %v\n", err)
			return 1, err
		}
		defer func() {
			if err := cleanup(); err != nil {
				fmt.Fprintf(stderr, "cleanup worktree: %v\n", err)
			}
		}()
	}

	logFile, err := openLog(cfg.LogPath)
	if err != nil {
		fmt.Fprintf(stderr, "open log: %v\n", err)
		return 1, err
	}
	defer logFile.Close()

	rec := runlog.NewRecorder(logFile)
	runner, err := buildRunner(cfg.RunnerKind, runDir, cfg.ProviderName, cfg.ProviderBinary, cfg.Model, resolveCommandDir(cfg.CommandDir, cfg.WorkflowPath), cfg.Arguments)
	if err != nil {
		fmt.Fprintf(stderr, "build runner: %v\n", err)
		return 2, err
	}
	if cfg.StatePath == "" {
		cfg.StatePath = defaultStatePath(cfg.RunID)
	}
	runState := state.NewRun(cfg.RunID, wf.Name, cfg.WorkflowPath)
	if err := state.Save(cfg.StatePath, runState); err != nil {
		fmt.Fprintf(stderr, "save state: %v\n", err)
		return 1, err
	}
	err = engine.New(runner, rec).RunWithOptions(context.Background(), wf, engine.RunOptions{
		OnNodeResult: persistNodeResult(cfg.StatePath, runState),
	})
	if err != nil {
		fmt.Fprintf(stderr, "workflow failed: %v\n", err)
		return 1, err
	}
	fmt.Fprintf(stdout, "workflow completed; log: %s state: %s\n", cfg.LogPath, cfg.StatePath)
	return 0, nil
}

func startDetachedRun(originalArgs []string, cfg runConfig, wf *workflow.Workflow, explicit map[string]bool, stdout, stderr io.Writer) int {
	absWorkdir, err := filepath.Abs(cfg.Workdir)
	if err != nil {
		fmt.Fprintf(stderr, "resolve workdir: %v\n", err)
		return 1
	}
	absWorkflow, err := filepath.Abs(cfg.WorkflowPath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve workflow: %v\n", err)
		return 1
	}
	runID := cfg.RunID
	if !explicit["run-id"] || runID == "" || runID == "local" {
		runID, err = runregistry.GenerateRunID()
		if err != nil {
			fmt.Fprintf(stderr, "generate run id: %v\n", err)
			return 1
		}
	}
	paths := runregistry.DefaultPaths(absWorkdir, runID)
	if !explicit["log"] {
		cfg.LogPath = paths.LogPath
	}
	if !explicit["state"] {
		cfg.StatePath = paths.StatePath
	}
	cfg.WorkflowPath = absWorkflow
	cfg.Workdir = absWorkdir
	cfg.RunID = runID
	cfg.LogPath = absPath(cfg.LogPath, absWorkdir)
	cfg.StatePath = absPath(cfg.StatePath, absWorkdir)
	cfg.WorktreeBase = absOptionalPath(cfg.WorktreeBase, absWorkdir)
	cfg.CommandDir = absOptionalPath(cfg.CommandDir, absWorkdir)
	processLogPath := absPath(paths.ProcessLogPath, absWorkdir)
	metadataPath := absPath(paths.MetadataPath, absWorkdir)

	childArgv, err := detachedChildArgv(metadataPath, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "build detached command: %v\n", err)
		return 1
	}
	metadata := runregistry.NewMetadata(runID, wf.Name, cfg.WorkflowPath, cfg.Workdir, append([]string{"run"}, originalArgs...), childArgv, 0, time.Now().UTC())
	metadata.LogPath = cfg.LogPath
	metadata.StatePath = cfg.StatePath
	metadata.ProcessLogPath = processLogPath
	metadata.ChildArgv = childArgv
	if err := runregistry.Save(metadataPath, metadata); err != nil {
		fmt.Fprintf(stderr, "save run metadata: %v\n", err)
		return 1
	}
	pid, err := detachedSpawner.Launch(detach.LaunchRequest{
		Argv:    childArgv,
		Dir:     cfg.Workdir,
		Env:     os.Environ(),
		LogPath: processLogPath,
	})
	if err != nil {
		metadata.Status = runregistry.StatusFailed
		metadata.Error = err.Error()
		metadata.EndedAt = time.Now().UTC()
		_ = runregistry.Save(metadataPath, metadata)
		fmt.Fprintf(stderr, "start detached run: %v\n", err)
		return 1
	}
	current, err := runregistry.Load(metadataPath, func(int) bool { return true })
	if err == nil {
		metadata = current
	}
	metadata.PID = pid
	metadata.ProcessLogPath = processLogPath
	metadata.ChildArgv = childArgv
	if err := runregistry.Save(metadataPath, metadata); err != nil {
		fmt.Fprintf(stderr, "save run metadata: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "detached run started\nrun id: %s\nstatus: micromage status --run-id %s\nwatch: micromage watch --run-id %s\nlog: %s\nstate: %s\nprocess log: %s\n", runID, runID, runID, cfg.LogPath, cfg.StatePath, processLogPath)
	return 0
}

func detachedChildArgv(metadataPath string, cfg runConfig) ([]string, error) {
	exe, err := executablePath()
	if err != nil {
		return nil, err
	}
	argv := []string{exe, "__run-detached", "--metadata", metadataPath, "--",
		"--workflow", cfg.WorkflowPath,
		"--log", cfg.LogPath,
		"--state", cfg.StatePath,
		"--workdir", cfg.Workdir,
		"--runner", cfg.RunnerKind,
		"--provider", cfg.ProviderName,
		"--run-id", cfg.RunID,
	}
	if cfg.ProviderBinary != "" {
		argv = append(argv, "--provider-binary", cfg.ProviderBinary)
	}
	if cfg.Model != "" {
		argv = append(argv, "--model", cfg.Model)
	}
	if cfg.CommandDir != "" {
		argv = append(argv, "--command-dir", cfg.CommandDir)
	}
	if cfg.Arguments != "" {
		argv = append(argv, "--arguments", cfg.Arguments)
	}
	if cfg.Isolate {
		argv = append(argv, "--isolate")
	}
	if cfg.WorktreeBase != "" {
		argv = append(argv, "--worktree-base", cfg.WorktreeBase)
	}
	return argv, nil
}

func detachedChildCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("__run-detached", flag.ContinueOnError)
	fs.SetOutput(stderr)
	metadataPath := fs.String("metadata", "", "run metadata path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *metadataPath == "" {
		fmt.Fprintln(stderr, "--metadata is required")
		return 2
	}
	cfg, wf, _, code := parseRunConfig(fs.Args(), stderr)
	if code != 0 {
		finalizeDetachedMetadata(*metadataPath, code, fmt.Errorf("parse detached child arguments failed"))
		return code
	}
	code, err := executeRun(cfg, wf, stdout, stderr)
	finalizeDetachedMetadata(*metadataPath, code, err)
	return code
}

func finalizeDetachedMetadata(path string, code int, runErr error) {
	metadata, err := runregistry.Load(path, func(int) bool { return true })
	if err != nil {
		return
	}
	metadata.EndedAt = time.Now().UTC()
	metadata.ExitCode = code
	metadata.Error = ""
	switch {
	case runErr == nil && code == 0:
		metadata.Status = runregistry.StatusPassed
	case errors.Is(runErr, engine.ErrHumanGate):
		metadata.Status = runregistry.StatusPaused
		metadata.Error = runErr.Error()
	default:
		metadata.Status = runregistry.StatusFailed
		if runErr != nil {
			metadata.Error = runErr.Error()
		}
	}
	_ = runregistry.Save(path, metadata)
}

func watchCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logPath := fs.String("log", ".micromage/run.jsonl", "JSONL event log path")
	runID := fs.String("run-id", "", "detached run id or latest")
	workdir := fs.String("workdir", ".", "run registry working directory")
	once := fs.Bool("once", false, "render one dashboard snapshot and exit")
	limit := fs.Int("limit", 10, "recent output lines to display")
	interval := fs.Duration("interval", time.Second, "refresh interval")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runID != "" {
		metadata, err := loadRunMetadata(*workdir, *runID)
		if err != nil {
			fmt.Fprintf(stderr, "load run: %v\n", err)
			return 1
		}
		*logPath = metadata.LogPath
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

func runsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workdir := fs.String("workdir", ".", "run registry working directory")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	absWorkdir, err := filepath.Abs(*workdir)
	if err != nil {
		fmt.Fprintf(stderr, "resolve workdir: %v\n", err)
		return 1
	}
	runs, err := runregistry.List(absWorkdir, runregistry.DefaultPIDLiveness)
	if err != nil {
		fmt.Fprintf(stderr, "list runs: %v\n", err)
		return 1
	}
	if *jsonOut {
		return writeJSON(stdout, runs, stderr)
	}
	if len(runs) == 0 {
		fmt.Fprintln(stdout, "no detached runs")
		return 0
	}
	fmt.Fprintln(stdout, "RUN ID\tSTATUS\tWORKFLOW\tSTARTED")
	for _, run := range runs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", run.RunID, run.Status, firstNonEmpty(run.WorkflowName, "(unknown)"), run.StartedAt.Format(time.RFC3339))
	}
	return 0
}

func statusCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workdir := fs.String("workdir", ".", "run registry working directory")
	runID := fs.String("run-id", "latest", "detached run id or latest")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	metadata, err := loadRunMetadata(*workdir, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "load run: %v\n", err)
		return 1
	}
	if *jsonOut {
		return writeJSON(stdout, metadata, stderr)
	}
	fmt.Fprintf(stdout, "Run: %s\n", metadata.RunID)
	fmt.Fprintf(stdout, "Status: %s\n", metadata.Status)
	fmt.Fprintf(stdout, "Workflow: %s\n", firstNonEmpty(metadata.WorkflowName, "(unknown)"))
	fmt.Fprintf(stdout, "Started: %s\n", metadata.StartedAt.Format(time.RFC3339))
	if !metadata.EndedAt.IsZero() {
		fmt.Fprintf(stdout, "Ended: %s\n", metadata.EndedAt.Format(time.RFC3339))
	}
	if metadata.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", metadata.Error)
	}
	fmt.Fprintf(stdout, "Log: %s\n", metadata.LogPath)
	fmt.Fprintf(stdout, "State: %s\n", metadata.StatePath)
	fmt.Fprintf(stdout, "Process log: %s\n", metadata.ProcessLogPath)
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
	assetCandidate := filepath.Join(filepath.Dir(defaultsDir), "commands")
	if stat, err := os.Stat(assetCandidate); err == nil && stat.IsDir() {
		return assetCandidate
	}
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

func loadRunMetadata(workdir, runID string) (runregistry.Metadata, error) {
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return runregistry.Metadata{}, err
	}
	if runID == "" || runID == "latest" {
		return runregistry.Latest(absWorkdir, runregistry.DefaultPIDLiveness)
	}
	paths := runregistry.DefaultPaths(absWorkdir, runID)
	return runregistry.Load(paths.MetadataPath, runregistry.DefaultPIDLiveness)
}

func writeJSON(w io.Writer, value any, stderr io.Writer) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintf(stderr, "write json: %v\n", err)
		return 1
	}
	return 0
}

func absPath(path, base string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

func absOptionalPath(path, base string) string {
	if path == "" {
		return ""
	}
	return absPath(path, base)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: micromage <validate|run|approve|resume|quality|watch|runs|status> [options]")
}
