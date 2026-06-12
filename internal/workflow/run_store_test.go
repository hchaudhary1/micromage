package workflow

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunStoreStartAndFinishWritesIndexAndEvents(t *testing.T) {
	repo := t.TempDir()
	store := NewRunStore(repo)
	times := []time.Time{
		time.Date(2026, 6, 12, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 12, 1, 0, 2, 0, time.UTC),
	}
	store.now = func() time.Time {
		next := times[0]
		times = times[1:]
		return next
	}
	store.newEventID = sequenceEventID()

	_, err := store.StartRun(RunStart{
		RunID:             "run-1",
		WorkflowID:        "review-last-commit",
		WorkflowName:      "Review Last Commit",
		Mode:              "real",
		CWD:               repo,
		ArtifactsDir:      DefaultArtifactsDir(repo, "run-1"),
		ArgumentsRedacted: true,
		NodeTotal:         2,
	})
	if err != nil {
		t.Fatalf("StartRun returned error: %v", err)
	}
	if err := store.FinishRun("run-1", RunResult{
		CompletedNodes: []string{"collect"},
		FailedNodes:    []RunFailure{},
	}); err != nil {
		t.Fatalf("FinishRun returned error: %v", err)
	}

	index := readRunIndexFile(t, repo)
	if index.SchemaVersion != 1 || len(index.Runs) != 1 {
		t.Fatalf("unexpected index: %#v", index)
	}
	run := index.Runs[0]
	if run.RunID != "run-1" || run.Status != RunStatusSucceeded {
		t.Fatalf("unexpected run status: %#v", run)
	}
	if run.StartedAt == nil || run.FinishedAt == nil || run.DurationMS == nil || *run.DurationMS != 2000 {
		t.Fatalf("expected durable timing metadata, got %#v", run)
	}
	if run.ArtifactsDir != filepath.Join(".micromage", "runs", "run-1") {
		t.Fatalf("expected repo-relative artifact path, got %q", run.ArtifactsDir)
	}
	if run.NodeCounts.Total != 2 || run.NodeCounts.Completed != 1 || run.NodeCounts.Failed != 0 {
		t.Fatalf("unexpected node counts: %#v", run.NodeCounts)
	}

	events := readRunEventLines(t, repo)
	if len(events) != 2 || events[0].Type != "run_started" || events[1].Type != "run_finished" {
		t.Fatalf("unexpected lifecycle events: %#v", events)
	}
	if events[1].FromStatus != RunStatusRunning || events[1].ToStatus != RunStatusSucceeded {
		t.Fatalf("expected terminal transition in event: %#v", events[1])
	}
}

func TestRunStoreFailureAndInterruptionUpdateLifecycle(t *testing.T) {
	repo := t.TempDir()
	store := NewRunStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 2, 0, 0, 0, time.UTC))
	store.newEventID = sequenceEventID()

	if _, err := store.StartRun(RunStart{RunID: "run-failed", WorkflowID: "wf", WorkflowName: "Workflow", Mode: "real", CWD: repo, ArtifactsDir: DefaultArtifactsDir(repo, "run-failed"), NodeTotal: 3}); err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	if err := store.FailRun("run-failed", RunResult{CompletedNodes: []string{"a"}, FailedNodes: []RunFailure{{NodeID: "b", Message: strings.Repeat("x", 300)}}}); err != nil {
		t.Fatalf("FailRun returned error: %v", err)
	}
	if _, err := store.StartRun(RunStart{RunID: "run-interrupted", WorkflowID: "wf", WorkflowName: "Workflow", Mode: "real", CWD: repo, ArtifactsDir: DefaultArtifactsDir(repo, "run-interrupted")}); err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	if err := store.InterruptRun("run-interrupted", "client disconnected"); err != nil {
		t.Fatalf("InterruptRun returned error: %v", err)
	}

	index := readRunIndexFile(t, repo)
	failed := findRunRecord(t, index.Runs, "run-failed")
	if failed.Status != RunStatusFailed || failed.FailureReason == "" || len(failed.FailureReason) > 259 {
		t.Fatalf("expected sanitized failed run, got %#v", failed)
	}
	if failed.NodeCounts.Completed != 1 || failed.NodeCounts.Failed != 1 {
		t.Fatalf("unexpected failed run counts: %#v", failed.NodeCounts)
	}
	interrupted := findRunRecord(t, index.Runs, "run-interrupted")
	if interrupted.Status != RunStatusInterrupted || interrupted.FailureReason != "client disconnected" {
		t.Fatalf("expected interrupted run, got %#v", interrupted)
	}

	events := readRunEventLines(t, repo)
	if len(events) != 4 || events[1].Type != "run_failed" || events[3].Type != "run_interrupted" {
		t.Fatalf("unexpected lifecycle events: %#v", events)
	}
}

func TestRunStoreRejectsInvalidTransitions(t *testing.T) {
	for _, tc := range []struct {
		from RunStatus
		to   RunStatus
		ok   bool
	}{
		{RunStatusQueued, RunStatusRunning, true},
		{RunStatusQueued, RunStatusCancelled, true},
		{RunStatusRunning, RunStatusSucceeded, true},
		{RunStatusRunning, RunStatusFailed, true},
		{RunStatusRunning, RunStatusCancelled, true},
		{RunStatusRunning, RunStatusInterrupted, true},
		{RunStatusInterrupted, RunStatusRunning, true},
		{RunStatusInterrupted, RunStatusFailed, true},
		{RunStatusSucceeded, RunStatusFailed, false},
		{RunStatusFailed, RunStatusRunning, false},
		{RunStatusQueued, RunStatusSucceeded, false},
	} {
		err := ValidateRunTransition(tc.from, tc.to)
		if tc.ok && err != nil {
			t.Fatalf("expected %s -> %s to be allowed: %v", tc.from, tc.to, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("expected %s -> %s to be rejected", tc.from, tc.to)
		}
	}

	repo := t.TempDir()
	store := NewRunStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 3, 0, 0, 0, time.UTC))
	if _, err := store.StartRun(RunStart{RunID: "run-terminal", WorkflowID: "wf", WorkflowName: "Workflow", Mode: "real", CWD: repo, ArtifactsDir: DefaultArtifactsDir(repo, "run-terminal")}); err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	if err := store.FinishRun("run-terminal", RunResult{}); err != nil {
		t.Fatalf("FinishRun failed: %v", err)
	}
	if err := store.FailRun("run-terminal", RunResult{FailureReason: "late failure"}); err == nil {
		t.Fatal("expected terminal run transition to be rejected")
	}
}

func TestRunStoreMalformedIndexFailsSafely(t *testing.T) {
	repo := t.TempDir()
	runsDir := filepath.Join(repo, ".micromage", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("create runs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "index.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write malformed index: %v", err)
	}
	store := NewRunStore(repo)

	_, err := store.StartRun(RunStart{RunID: "run-unsafe", WorkflowID: "wf", WorkflowName: "Workflow", Mode: "real", CWD: repo, ArtifactsDir: DefaultArtifactsDir(repo, "run-unsafe")})
	if err == nil {
		t.Fatal("expected malformed index error")
	}
	if _, err := os.Stat(filepath.Join(runsDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("malformed index should block event append, stat error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(runsDir, "index.json"))
	if err != nil {
		t.Fatalf("read malformed index: %v", err)
	}
	if string(data) != "{not-json" {
		t.Fatalf("malformed index should remain untouched, got %q", string(data))
	}
}

func readRunIndexFile(t *testing.T, repo string) runIndex {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".micromage", "runs", "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var index runIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	return index
}

func readRunEventLines(t *testing.T, repo string) []runLifecycleEvent {
	t.Helper()
	file, err := os.Open(filepath.Join(repo, ".micromage", "runs", "events.jsonl"))
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer file.Close()
	var events []runLifecycleEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event runLifecycleEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode event %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan events: %v", err)
	}
	return events
}

func findRunRecord(t *testing.T, runs []RunRecord, runID string) RunRecord {
	t.Helper()
	for _, run := range runs {
		if run.RunID == runID {
			return run
		}
	}
	t.Fatalf("run %q not found in %#v", runID, runs)
	return RunRecord{}
}

func fixedClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

func sequenceEventID() func() string {
	next := 0
	return func() string {
		next++
		return "event-" + strconv.Itoa(next)
	}
}
