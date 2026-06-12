package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	store.audit.newEventID = sequenceAuditID()

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

	auditEvents := readAuditEventLines(t, repo)
	if len(auditEvents) != 2 || auditEvents[0].Type != AuditTypeRunStarted || auditEvents[1].Type != AuditTypeRunFinished {
		t.Fatalf("unexpected audit events: %#v", auditEvents)
	}
	if auditEvents[0].RunID != "run-1" || auditEvents[0].WorkflowID != "review-last-commit" || auditEvents[0].Details["arguments_redacted"] != "true" {
		t.Fatalf("unexpected run_started audit event: %#v", auditEvents[0])
	}
	if auditEvents[1].Outcome != "success" || auditEvents[1].Details["completed_nodes"] != "1" {
		t.Fatalf("unexpected run_finished audit event: %#v", auditEvents[1])
	}
}

func TestRunStoreFailureAndInterruptionUpdateLifecycle(t *testing.T) {
	repo := t.TempDir()
	store := NewRunStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 2, 0, 0, 0, time.UTC))
	store.newEventID = sequenceEventID()
	store.audit.newEventID = sequenceAuditID()

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
	auditEvents := readAuditEventLines(t, repo)
	if len(auditEvents) != 4 || auditEvents[1].Type != AuditTypeRunFinished || auditEvents[1].Outcome != "failure" || auditEvents[3].Type != AuditTypeRunInterrupted {
		t.Fatalf("unexpected audit lifecycle events: %#v", auditEvents)
	}
	if strings.Contains(readAuditFile(t, repo), strings.Repeat("x", 20)) {
		t.Fatalf("audit lifecycle events should not copy failure reason text: %s", readAuditFile(t, repo))
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

func TestRunStoreCleanupUsesDefaultRetentionPolicy(t *testing.T) {
	repo := t.TempDir()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	store := NewRunStore(repo)
	store.now = fixedClock(now)
	store.audit.newEventID = sequenceAuditID()

	var runs []RunRecord
	for index := 0; index < 22; index++ {
		finished := now.AddDate(0, 0, -45).Add(time.Duration(21-index) * time.Hour)
		runs = append(runs, testRunRecord(repo, "run-old-"+strconv.Itoa(index), RunStatusSucceeded, finished))
	}
	runs = append(runs,
		testRunRecord(repo, "run-running", RunStatusRunning, now.AddDate(0, 0, -90)),
		testRunRecord(repo, "run-interrupted", RunStatusInterrupted, now.AddDate(0, 0, -90)),
	)
	writeRunIndexFile(t, repo, runs)

	report, err := store.CleanupRuns(context.Background(), CleanupPolicy{DryRun: true})
	if err != nil {
		t.Fatalf("CleanupRuns returned error: %v", err)
	}
	if report.OlderThan != defaultCleanupRetention || report.KeepTerminalRuns != defaultCleanupKeepTerminalRuns || !report.DryRun {
		t.Fatalf("expected default dry-run policy in report, got %#v", report)
	}
	if got := cleanupRunIDs(report.Candidates); strings.Join(got, ",") != "run-old-20,run-old-21" {
		t.Fatalf("expected only terminal runs outside the most recent default keep window, got %v", got)
	}
}

func TestRunStoreCleanupDryRunReportsCandidatesWithoutDeleting(t *testing.T) {
	repo := t.TempDir()
	now := time.Date(2026, 6, 12, 13, 0, 0, 0, time.UTC)
	store := NewRunStore(repo)
	store.now = fixedClock(now)
	store.audit.newEventID = sequenceAuditID()
	old := testRunRecord(repo, "run-old", RunStatusFailed, now.AddDate(0, 0, -10))
	recent := testRunRecord(repo, "run-recent", RunStatusSucceeded, now.Add(-time.Hour))
	writeRunIndexFile(t, repo, []RunRecord{old, recent})
	writeRunDirectory(t, repo, old.RunID)
	writeRunDirectory(t, repo, recent.RunID)

	report, err := store.CleanupRuns(context.Background(), CleanupPolicy{DryRun: true, OlderThan: 24 * time.Hour, KeepTerminalRuns: 1})
	if err != nil {
		t.Fatalf("CleanupRuns returned error: %v", err)
	}
	if got := cleanupRunIDs(report.Candidates); strings.Join(got, ",") != "run-old" {
		t.Fatalf("expected old run candidate, got %v", got)
	}
	if len(report.Deleted) != 0 || len(report.Failed) != 0 {
		t.Fatalf("dry-run should only report candidates, got %#v", report)
	}
	if _, err := os.Stat(filepath.Join(repo, ".micromage", "runs", "run-old")); err != nil {
		t.Fatalf("dry-run deleted candidate directory: %v", err)
	}
	if got := cleanupRunIDsFromIndex(readRunIndexFile(t, repo)); strings.Join(got, ",") != "run-old,run-recent" {
		t.Fatalf("dry-run should preserve index, got %v", got)
	}
	auditEvents := readAuditEventLines(t, repo)
	if len(auditEvents) != 1 || auditEvents[0].Type != AuditTypeRunCleanupStarted || auditEvents[0].Details["dry_run"] != "true" || auditEvents[0].Details["candidate_runs"] != "1" {
		t.Fatalf("expected dry-run started audit event, got %#v", auditEvents)
	}
}

func TestRunStoreCleanupDeleteUpdatesIndexAndRemovesDirectories(t *testing.T) {
	repo := t.TempDir()
	now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	store := NewRunStore(repo)
	store.now = fixedClock(now)
	store.audit.newEventID = sequenceAuditID()
	old := testRunRecord(repo, "run-delete", RunStatusCancelled, now.AddDate(0, 0, -60))
	recent := testRunRecord(repo, "run-keep-terminal", RunStatusSucceeded, now.Add(-time.Hour))
	running := testRunRecord(repo, "run-running", RunStatusRunning, now.AddDate(0, 0, -60))
	writeRunIndexFile(t, repo, []RunRecord{old, recent, running})
	for _, id := range []string{old.RunID, recent.RunID, running.RunID} {
		writeRunDirectory(t, repo, id)
	}

	report, err := store.CleanupRuns(context.Background(), CleanupPolicy{OlderThan: 24 * time.Hour, KeepTerminalRuns: 1})
	if err != nil {
		t.Fatalf("CleanupRuns returned error: %v", err)
	}
	if got := cleanupRunIDs(report.Deleted); strings.Join(got, ",") != "run-delete" {
		t.Fatalf("expected deleted run report, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".micromage", "runs", "run-delete")); !os.IsNotExist(err) {
		t.Fatalf("expected candidate directory to be deleted, stat error: %v", err)
	}
	for _, id := range []string{recent.RunID, running.RunID} {
		if _, err := os.Stat(filepath.Join(repo, ".micromage", "runs", id)); err != nil {
			t.Fatalf("expected preserved run directory %s: %v", id, err)
		}
	}
	if got := cleanupRunIDsFromIndex(readRunIndexFile(t, repo)); strings.Join(got, ",") != "run-keep-terminal,run-running" {
		t.Fatalf("expected deleted run removed from index, got %v", got)
	}
	auditEvents := readAuditEventLines(t, repo)
	if len(auditEvents) != 2 || auditEvents[0].Type != AuditTypeRunCleanupStarted || auditEvents[1].Type != AuditTypeRunCleanupDeleted {
		t.Fatalf("expected started and deleted audit events, got %#v", auditEvents)
	}
	if auditEvents[1].RunID != "run-delete" || auditEvents[1].Details["artifacts_dir"] != old.ArtifactsDir {
		t.Fatalf("unexpected deleted audit event: %#v", auditEvents[1])
	}
}

func TestRunStoreCleanupRejectsPathsOutsideRunsDir(t *testing.T) {
	repo := t.TempDir()
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	store := NewRunStore(repo)
	store.now = fixedClock(now)
	store.audit.newEventID = sequenceAuditID()
	outsideDir := filepath.Join(repo, "outside-run")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	unsafe := testRunRecord(repo, "run-unsafe", RunStatusSucceeded, now.AddDate(0, 0, -60))
	unsafe.ArtifactsDir = outsideDir
	recent := testRunRecord(repo, "run-recent", RunStatusSucceeded, now.Add(-time.Hour))
	writeRunIndexFile(t, repo, []RunRecord{unsafe, recent})

	report, err := store.CleanupRuns(context.Background(), CleanupPolicy{OlderThan: 24 * time.Hour, KeepTerminalRuns: 1})
	if err == nil {
		t.Fatal("expected cleanup to reject outside run path")
	}
	if len(report.Failed) != 1 || report.Failed[0].Run.RunID != "run-unsafe" || !strings.Contains(report.Failed[0].Error, "outside") {
		t.Fatalf("expected outside path failure report, got %#v", report)
	}
	if _, err := os.Stat(outsideDir); err != nil {
		t.Fatalf("outside path should remain untouched: %v", err)
	}
	if got := cleanupRunIDsFromIndex(readRunIndexFile(t, repo)); strings.Join(got, ",") != "run-unsafe,run-recent" {
		t.Fatalf("outside path failure should preserve index, got %v", got)
	}
	auditEvents := readAuditEventLines(t, repo)
	if len(auditEvents) != 2 || auditEvents[1].Type != AuditTypeRunCleanupFailed || auditEvents[1].Outcome != "failure" {
		t.Fatalf("expected cleanup failure audit event, got %#v", auditEvents)
	}
}

func TestRunStoreCleanupReportsFailedDeleteInAudit(t *testing.T) {
	repo := t.TempDir()
	now := time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC)
	store := NewRunStore(repo)
	store.now = fixedClock(now)
	store.audit.newEventID = sequenceAuditID()
	old := testRunRecord(repo, "run-delete-fails", RunStatusFailed, now.AddDate(0, 0, -60))
	recent := testRunRecord(repo, "run-recent", RunStatusSucceeded, now.Add(-time.Hour))
	writeRunIndexFile(t, repo, []RunRecord{old, recent})
	writeRunDirectory(t, repo, old.RunID)
	store.removeAll = func(path string) error {
		return errors.New("simulated remove failure")
	}

	report, err := store.CleanupRuns(context.Background(), CleanupPolicy{OlderThan: 24 * time.Hour, KeepTerminalRuns: 1})
	if err == nil {
		t.Fatal("expected delete failure")
	}
	if len(report.Deleted) != 0 || len(report.Failed) != 1 || report.Failed[0].Run.RunID != "run-delete-fails" {
		t.Fatalf("expected failed delete report, got %#v", report)
	}
	if _, err := os.Stat(filepath.Join(repo, ".micromage", "runs", old.RunID)); err != nil {
		t.Fatalf("failed delete should leave directory in place: %v", err)
	}
	if got := cleanupRunIDsFromIndex(readRunIndexFile(t, repo)); strings.Join(got, ",") != "run-delete-fails,run-recent" {
		t.Fatalf("failed delete should preserve index, got %v", got)
	}
	auditEvents := readAuditEventLines(t, repo)
	if len(auditEvents) != 2 || auditEvents[1].Type != AuditTypeRunCleanupFailed || auditEvents[1].RunID != "run-delete-fails" || auditEvents[1].Details["reason"] != "simulated remove failure" {
		t.Fatalf("expected failed delete audit event, got %#v", auditEvents)
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

func writeRunIndexFile(t *testing.T, repo string, runs []RunRecord) {
	t.Helper()
	runsDir := filepath.Join(repo, ".micromage", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("create runs dir: %v", err)
	}
	data, err := json.MarshalIndent(runIndex{SchemaVersion: runStoreSchemaVersion, Runs: runs}, "", "  ")
	if err != nil {
		t.Fatalf("encode test index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "index.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write test index: %v", err)
	}
}

func writeRunDirectory(t *testing.T, repo string, runID string) {
	t.Helper()
	runDir := filepath.Join(repo, ".micromage", "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write run manifest: %v", err)
	}
}

func testRunRecord(repo string, runID string, status RunStatus, at time.Time) RunRecord {
	record := RunRecord{
		SchemaVersion:     runStoreSchemaVersion,
		RunID:             runID,
		WorkflowID:        "wf",
		WorkflowName:      "Workflow",
		Mode:              "real",
		Status:            status,
		CreatedAt:         at.Add(-time.Minute),
		StartedAt:         &at,
		CWD:               repo,
		ArtifactsDir:      filepath.Join(".micromage", "runs", runID),
		ArgumentsRedacted: true,
		NodeCounts:        RunNodeCounts{Total: 1, Completed: 1},
		ManifestPath:      filepath.Join(".micromage", "runs", runID, "manifest.json"),
		SummaryPath:       filepath.Join(".micromage", "runs", runID, "summary.json"),
	}
	if isTerminalRunStatus(status) || status == RunStatusInterrupted {
		finished := at
		record.FinishedAt = &finished
		duration := int64(time.Minute / time.Millisecond)
		record.DurationMS = &duration
	}
	return record
}

func cleanupRunIDs(runs []CleanupRun) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.RunID)
	}
	return ids
}

func cleanupRunIDsFromIndex(index runIndex) []string {
	ids := make([]string, 0, len(index.Runs))
	for _, run := range index.Runs {
		ids = append(ids, run.RunID)
	}
	return ids
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
