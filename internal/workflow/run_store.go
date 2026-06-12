package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const runStoreSchemaVersion = 1
const defaultCleanupRetention = 30 * 24 * time.Hour
const defaultCleanupKeepTerminalRuns = 20

type RunStatus string

const (
	RunStatusQueued      RunStatus = "queued"
	RunStatusRunning     RunStatus = "running"
	RunStatusSucceeded   RunStatus = "succeeded"
	RunStatusFailed      RunStatus = "failed"
	RunStatusCancelled   RunStatus = "cancelled"
	RunStatusInterrupted RunStatus = "interrupted"
)

type RunNodeCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

type RunRecord struct {
	SchemaVersion     int           `json:"schema_version"`
	RunID             string        `json:"run_id"`
	WorkflowID        string        `json:"workflow_id"`
	WorkflowName      string        `json:"workflow_name"`
	Mode              string        `json:"mode"`
	Status            RunStatus     `json:"status"`
	CreatedAt         time.Time     `json:"created_at"`
	StartedAt         *time.Time    `json:"started_at"`
	FinishedAt        *time.Time    `json:"finished_at"`
	DurationMS        *int64        `json:"duration_ms"`
	CWD               string        `json:"cwd"`
	ArtifactsDir      string        `json:"artifacts_dir"`
	ArgumentsRedacted bool          `json:"arguments_redacted"`
	NodeCounts        RunNodeCounts `json:"node_counts"`
	FailureReason     string        `json:"failure_reason"`
	ManifestPath      string        `json:"manifest_path"`
	SummaryPath       string        `json:"summary_path"`
}

type RunStart struct {
	RunID             string
	WorkflowID        string
	WorkflowName      string
	Mode              string
	CWD               string
	ArtifactsDir      string
	ArgumentsRedacted bool
	NodeTotal         int
}

type RunResult struct {
	CompletedNodes []string
	FailedNodes    []RunFailure
	SkippedNodes   []string
	FailureReason  string
}

type RunStore struct {
	repoRoot   string
	runsDir    string
	now        func() time.Time
	newEventID func() string
	removeAll  func(string) error
	audit      *AuditStore
}

type runIndex struct {
	SchemaVersion int         `json:"schema_version"`
	Runs          []RunRecord `json:"runs"`
}

type runLifecycleEvent struct {
	SchemaVersion int           `json:"schema_version"`
	EventID       string        `json:"event_id"`
	Type          string        `json:"type"`
	CreatedAt     time.Time     `json:"created_at"`
	RunID         string        `json:"run_id"`
	WorkflowID    string        `json:"workflow_id"`
	FromStatus    RunStatus     `json:"from_status,omitempty"`
	ToStatus      RunStatus     `json:"to_status"`
	NodeCounts    RunNodeCounts `json:"node_counts,omitempty"`
	FailureReason string        `json:"failure_reason,omitempty"`
}

type CleanupPolicy struct {
	DryRun           bool          `json:"dry_run"`
	OlderThan        time.Duration `json:"older_than"`
	KeepTerminalRuns int           `json:"keep_terminal_runs"`
}

type CleanupReport struct {
	DryRun           bool             `json:"dry_run"`
	OlderThan        time.Duration    `json:"older_than"`
	KeepTerminalRuns int              `json:"keep_terminal_runs"`
	StartedAt        time.Time        `json:"started_at"`
	Candidates       []CleanupRun     `json:"candidates"`
	Deleted          []CleanupRun     `json:"deleted"`
	Failed           []CleanupFailure `json:"failed"`
}

type CleanupRun struct {
	RunID        string    `json:"run_id"`
	WorkflowID   string    `json:"workflow_id"`
	Status       RunStatus `json:"status"`
	FinishedAt   time.Time `json:"finished_at"`
	ArtifactsDir string    `json:"artifacts_dir"`
}

type CleanupFailure struct {
	Run   CleanupRun `json:"run"`
	Error string     `json:"error"`
}

func NewRunStore(repoRoot string) *RunStore {
	if repoRoot == "" {
		repoRoot = "."
	}
	cleanRoot := filepath.Clean(repoRoot)
	return &RunStore{
		repoRoot: cleanRoot,
		runsDir:  filepath.Join(cleanRoot, ".micromage", "runs"),
		now:      func() time.Time { return time.Now().UTC() },
		newEventID: func() string {
			return "run-event-" + fmt.Sprintf("%d", time.Now().UnixNano())
		},
		removeAll: os.RemoveAll,
		audit:     NewAuditStore(cleanRoot),
	}
}

func (store *RunStore) StartRun(start RunStart) (RunRecord, error) {
	if strings.TrimSpace(start.RunID) == "" {
		return RunRecord{}, errors.New("run id is required")
	}
	if err := store.ensureReady(); err != nil {
		return RunRecord{}, err
	}
	index, err := store.readIndex()
	if err != nil {
		return RunRecord{}, err
	}
	if _, ok := findRun(index.Runs, start.RunID); ok {
		return RunRecord{}, fmt.Errorf("run %s already exists", start.RunID)
	}
	now := store.now().UTC()
	record := RunRecord{
		SchemaVersion:     runStoreSchemaVersion,
		RunID:             start.RunID,
		WorkflowID:        start.WorkflowID,
		WorkflowName:      start.WorkflowName,
		Mode:              start.Mode,
		Status:            RunStatusRunning,
		CreatedAt:         now,
		StartedAt:         &now,
		CWD:               start.CWD,
		ArtifactsDir:      store.relativeToRepo(start.ArtifactsDir),
		ArgumentsRedacted: start.ArgumentsRedacted,
		NodeCounts:        RunNodeCounts{Total: start.NodeTotal},
		ManifestPath:      store.relativeToRepo(filepath.Join(store.runsDir, start.RunID, "manifest.json")),
		SummaryPath:       store.relativeToRepo(filepath.Join(store.runsDir, start.RunID, "summary.json")),
	}
	index.Runs = append(index.Runs, record)
	sortRunIndex(index.Runs)
	event := store.lifecycleEvent("run_started", record, "", RunStatusRunning, now)
	if err := store.appendEvent(event); err != nil {
		return RunRecord{}, err
	}
	if err := store.writeIndex(index); err != nil {
		return RunRecord{}, err
	}
	if err := store.auditRunLifecycle("run_started", record); err != nil {
		return RunRecord{}, err
	}
	return record, nil
}

func (store *RunStore) FinishRun(runID string, result RunResult) error {
	return store.transitionRun(runID, RunStatusSucceeded, "run_finished", result)
}

func (store *RunStore) FailRun(runID string, result RunResult) error {
	return store.transitionRun(runID, RunStatusFailed, "run_failed", result)
}

func (store *RunStore) InterruptRun(runID string, reason string) error {
	return store.transitionRun(runID, RunStatusInterrupted, "run_interrupted", RunResult{FailureReason: reason})
}

func (store *RunStore) CancelRun(runID string, reason string) error {
	return store.transitionRun(runID, RunStatusCancelled, "run_cancelled", RunResult{FailureReason: reason})
}

func (store *RunStore) ResumeRun(runID string) error {
	return store.transitionRun(runID, RunStatusRunning, "run_resumed", RunResult{})
}

func (store *RunStore) CleanupRuns(ctx context.Context, policy CleanupPolicy) (CleanupReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := resolveCleanupPolicy(policy)
	if err != nil {
		return CleanupReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return CleanupReport{}, err
	}
	if err := store.ensureReady(); err != nil {
		return CleanupReport{}, err
	}
	index, err := store.readIndex()
	if err != nil {
		return CleanupReport{}, err
	}
	now := store.now().UTC()
	report := CleanupReport{
		DryRun:           resolved.DryRun,
		OlderThan:        resolved.OlderThan,
		KeepTerminalRuns: resolved.KeepTerminalRuns,
		StartedAt:        now,
	}
	candidates := selectCleanupCandidates(index.Runs, resolved, now)
	report.Candidates = cleanupRunsFromRecords(candidates)
	if err := store.auditCleanupStarted(report); err != nil {
		return report, err
	}
	for _, run := range candidates {
		// Retention cleanup only acts inside the private run store so stale history cannot delete user files.
		if _, err := store.cleanupRunPath(run); err != nil {
			report.Failed = append(report.Failed, cleanupFailure(run, err))
			_ = store.auditCleanupFailed(run, resolved.DryRun, err)
		}
	}
	if len(report.Failed) > 0 {
		return report, cleanupError(report.Failed)
	}
	if resolved.DryRun {
		return report, nil
	}

	deleted := make(map[string]struct{})
	for _, run := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		runPath, err := store.cleanupRunPath(run)
		if err != nil {
			report.Failed = append(report.Failed, cleanupFailure(run, err))
			_ = store.auditCleanupFailed(run, false, err)
			continue
		}
		if err := store.removeAll(runPath); err != nil {
			report.Failed = append(report.Failed, cleanupFailure(run, err))
			_ = store.auditCleanupFailed(run, false, err)
			continue
		}
		cleanupRun := cleanupRunFromRecord(run)
		report.Deleted = append(report.Deleted, cleanupRun)
		deleted[run.RunID] = struct{}{}
		if err := store.auditCleanupDeleted(run); err != nil {
			report.Failed = append(report.Failed, CleanupFailure{Run: cleanupRun, Error: err.Error()})
			return report, err
		}
	}
	if len(deleted) > 0 {
		index.Runs = removeDeletedRuns(index.Runs, deleted)
		sortRunIndex(index.Runs)
		if err := store.writeIndex(index); err != nil {
			return report, err
		}
	}
	if len(report.Failed) > 0 {
		return report, cleanupError(report.Failed)
	}
	return report, nil
}

func ValidateRunTransition(from RunStatus, to RunStatus) error {
	allowed := map[RunStatus]map[RunStatus]struct{}{
		RunStatusQueued: {
			RunStatusRunning:   {},
			RunStatusCancelled: {},
		},
		RunStatusRunning: {
			RunStatusSucceeded:   {},
			RunStatusFailed:      {},
			RunStatusCancelled:   {},
			RunStatusInterrupted: {},
		},
		RunStatusInterrupted: {
			RunStatusRunning: {},
			RunStatusFailed:  {},
		},
	}
	if _, ok := allowed[from][to]; ok {
		return nil
	}
	return fmt.Errorf("invalid run lifecycle transition %s -> %s", from, to)
}

func (store *RunStore) transitionRun(runID string, to RunStatus, eventType string, result RunResult) error {
	if err := store.ensureReady(); err != nil {
		return err
	}
	index, err := store.readIndex()
	if err != nil {
		return err
	}
	position, ok := findRun(index.Runs, runID)
	if !ok {
		return fmt.Errorf("run %s was not found", runID)
	}
	record := index.Runs[position]
	from := record.Status
	if err := ValidateRunTransition(from, to); err != nil {
		return err
	}
	now := store.now().UTC()
	record.Status = to
	record.NodeCounts.Completed = len(result.CompletedNodes)
	record.NodeCounts.Failed = len(result.FailedNodes)
	record.NodeCounts.Skipped = len(result.SkippedNodes)
	reason := result.FailureReason
	if reason == "" && len(result.FailedNodes) > 0 {
		reason = result.FailedNodes[0].Message
	}
	record.FailureReason = sanitizeRunFailureReason(reason)
	if to != RunStatusRunning {
		record.FinishedAt = &now
		if record.StartedAt != nil {
			duration := now.Sub(*record.StartedAt).Milliseconds()
			if duration < 0 {
				duration = 0
			}
			record.DurationMS = &duration
		}
	} else {
		record.StartedAt = &now
		record.FinishedAt = nil
		record.DurationMS = nil
		record.FailureReason = ""
	}
	index.Runs[position] = record
	sortRunIndex(index.Runs)
	event := store.lifecycleEvent(eventType, record, from, to, now)
	if err := store.appendEvent(event); err != nil {
		return err
	}
	if err := store.writeIndex(index); err != nil {
		return err
	}
	return store.auditRunLifecycle(eventType, record)
}

func (store *RunStore) auditRunLifecycle(eventType string, record RunRecord) error {
	if store.audit == nil {
		return nil
	}
	auditType := ""
	outcome := "success"
	switch eventType {
	case "run_started":
		auditType = AuditTypeRunStarted
	case "run_finished":
		auditType = AuditTypeRunFinished
	case "run_failed":
		auditType = AuditTypeRunFinished
		outcome = "failure"
	case "run_interrupted":
		auditType = AuditTypeRunInterrupted
		outcome = "interrupted"
	case "run_cancelled":
		auditType = AuditTypeRunFinished
		outcome = "cancelled"
	default:
		return nil
	}
	// Lifecycle audits preserve security-relevant run status without copying node output.
	return store.audit.Append(AuditEvent{
		Type:       auditType,
		RunID:      record.RunID,
		WorkflowID: record.WorkflowID,
		Actor:      AuditActorLocalBrowser,
		Outcome:    outcome,
		Details: map[string]string{
			"mode":               record.Mode,
			"status":             string(record.Status),
			"node_total":         strconv.Itoa(record.NodeCounts.Total),
			"completed_nodes":    strconv.Itoa(record.NodeCounts.Completed),
			"failed_nodes":       strconv.Itoa(record.NodeCounts.Failed),
			"skipped_nodes":      strconv.Itoa(record.NodeCounts.Skipped),
			"arguments_redacted": strconv.FormatBool(record.ArgumentsRedacted),
		},
	})
}

func (store *RunStore) ensureReady() error {
	if store == nil {
		return errors.New("run store is nil")
	}
	return os.MkdirAll(store.runsDir, 0o755)
}

func (store *RunStore) readIndex() (runIndex, error) {
	path := filepath.Join(store.runsDir, "index.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return runIndex{SchemaVersion: runStoreSchemaVersion}, nil
	}
	if err != nil {
		return runIndex{}, fmt.Errorf("read run index: %w", err)
	}
	var index runIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return runIndex{}, fmt.Errorf("decode run index: %w", err)
	}
	if index.SchemaVersion != runStoreSchemaVersion {
		return runIndex{}, fmt.Errorf("unsupported run index schema version %d", index.SchemaVersion)
	}
	for _, run := range index.Runs {
		if run.SchemaVersion != runStoreSchemaVersion {
			return runIndex{}, fmt.Errorf("unsupported run %s schema version %d", run.RunID, run.SchemaVersion)
		}
		if !isRunStatus(run.Status) {
			return runIndex{}, fmt.Errorf("run %s has unknown status %q", run.RunID, run.Status)
		}
	}
	return index, nil
}

func (store *RunStore) writeIndex(index runIndex) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run index: %w", err)
	}
	data = append(data, '\n')
	tmpPath := filepath.Join(store.runsDir, "index.json.tmp")
	indexPath := filepath.Join(store.runsDir, "index.json")
	// Atomic rewrites keep the browser list recoverable if the process exits mid-update.
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write temporary run index: %w", err)
	}
	if err := os.Rename(tmpPath, indexPath); err != nil {
		return fmt.Errorf("replace run index: %w", err)
	}
	return nil
}

func (store *RunStore) appendEvent(event runLifecycleEvent) error {
	path := filepath.Join(store.runsDir, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open run events: %w", err)
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode run event: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}

func (store *RunStore) lifecycleEvent(eventType string, record RunRecord, from RunStatus, to RunStatus, createdAt time.Time) runLifecycleEvent {
	return runLifecycleEvent{
		SchemaVersion: runStoreSchemaVersion,
		EventID:       store.newEventID(),
		Type:          eventType,
		CreatedAt:     createdAt.UTC(),
		RunID:         record.RunID,
		WorkflowID:    record.WorkflowID,
		FromStatus:    from,
		ToStatus:      to,
		NodeCounts:    record.NodeCounts,
		FailureReason: record.FailureReason,
	}
}

func (store *RunStore) relativeToRepo(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Clean(filepath.Join(store.repoRoot, clean))
	}
	rel, err := filepath.Rel(store.repoRoot, clean)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Clean(path)
}

func findRun(runs []RunRecord, runID string) (int, bool) {
	for index, run := range runs {
		if run.RunID == runID {
			return index, true
		}
	}
	return 0, false
}

func sortRunIndex(runs []RunRecord) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
}

func isRunStatus(status RunStatus) bool {
	switch status {
	case RunStatusQueued, RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled, RunStatusInterrupted:
		return true
	default:
		return false
	}
}

func sanitizeRunFailureReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if index := strings.IndexAny(reason, "\r\n"); index >= 0 {
		reason = reason[:index]
	}
	const maxFailureReasonBytes = 256
	if len(reason) > maxFailureReasonBytes {
		reason = reason[:maxFailureReasonBytes] + "..."
	}
	return reason
}

func resolveCleanupPolicy(policy CleanupPolicy) (CleanupPolicy, error) {
	if policy.OlderThan < 0 {
		return CleanupPolicy{}, errors.New("cleanup older-than duration cannot be negative")
	}
	if policy.KeepTerminalRuns < 0 {
		return CleanupPolicy{}, errors.New("cleanup keep terminal runs cannot be negative")
	}
	if policy.OlderThan == 0 {
		policy.OlderThan = defaultCleanupRetention
	}
	if policy.KeepTerminalRuns == 0 {
		policy.KeepTerminalRuns = defaultCleanupKeepTerminalRuns
	}
	return policy, nil
}

func selectCleanupCandidates(runs []RunRecord, policy CleanupPolicy, now time.Time) []RunRecord {
	terminal := make([]RunRecord, 0, len(runs))
	for _, run := range runs {
		if isTerminalRunStatus(run.Status) {
			terminal = append(terminal, run)
		}
	}
	// Retention balances disk cleanup with enough recent terminal history for audit and debugging.
	sort.SliceStable(terminal, func(i, j int) bool {
		left := cleanupReferenceTime(terminal[i])
		right := cleanupReferenceTime(terminal[j])
		if left.Equal(right) {
			return terminal[i].RunID < terminal[j].RunID
		}
		return left.After(right)
	})
	kept := make(map[string]struct{}, policy.KeepTerminalRuns)
	for index, run := range terminal {
		if index >= policy.KeepTerminalRuns {
			break
		}
		kept[run.RunID] = struct{}{}
	}
	cutoff := now.Add(-policy.OlderThan)
	var candidates []RunRecord
	for _, run := range terminal {
		if _, ok := kept[run.RunID]; ok {
			continue
		}
		if cleanupReferenceTime(run).Before(cutoff) {
			candidates = append(candidates, run)
		}
	}
	return candidates
}

func isTerminalRunStatus(status RunStatus) bool {
	switch status {
	case RunStatusSucceeded, RunStatusFailed, RunStatusCancelled:
		return true
	default:
		return false
	}
}

func cleanupReferenceTime(run RunRecord) time.Time {
	if run.FinishedAt != nil && !run.FinishedAt.IsZero() {
		return run.FinishedAt.UTC()
	}
	return run.CreatedAt.UTC()
}

func cleanupRunFromRecord(run RunRecord) CleanupRun {
	return CleanupRun{
		RunID:        run.RunID,
		WorkflowID:   run.WorkflowID,
		Status:       run.Status,
		FinishedAt:   cleanupReferenceTime(run),
		ArtifactsDir: run.ArtifactsDir,
	}
}

func cleanupRunsFromRecords(runs []RunRecord) []CleanupRun {
	cleanupRuns := make([]CleanupRun, 0, len(runs))
	for _, run := range runs {
		cleanupRuns = append(cleanupRuns, cleanupRunFromRecord(run))
	}
	return cleanupRuns
}

func (store *RunStore) cleanupRunPath(run RunRecord) (string, error) {
	if strings.TrimSpace(run.ArtifactsDir) == "" {
		return "", fmt.Errorf("run %s has no artifacts directory", run.RunID)
	}
	cleanPath := filepath.Clean(run.ArtifactsDir)
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(store.repoRoot, cleanPath)
	}
	absRunPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("resolve run %s artifacts directory: %w", run.RunID, err)
	}
	absRunsDir, err := filepath.Abs(store.runsDir)
	if err != nil {
		return "", fmt.Errorf("resolve runs directory: %w", err)
	}
	rel, err := filepath.Rel(absRunsDir, absRunPath)
	if err != nil {
		return "", fmt.Errorf("check run %s artifacts directory containment: %w", run.RunID, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("run %s artifacts directory %q is outside %s", run.RunID, run.ArtifactsDir, store.runsDir)
	}
	return absRunPath, nil
}

func removeDeletedRuns(runs []RunRecord, deleted map[string]struct{}) []RunRecord {
	kept := runs[:0]
	for _, run := range runs {
		if _, ok := deleted[run.RunID]; ok {
			continue
		}
		kept = append(kept, run)
	}
	return kept
}

func cleanupFailure(run RunRecord, err error) CleanupFailure {
	return CleanupFailure{Run: cleanupRunFromRecord(run), Error: sanitizeRunFailureReason(err.Error())}
}

func cleanupError(failures []CleanupFailure) error {
	if len(failures) == 0 {
		return nil
	}
	if len(failures) == 1 {
		return fmt.Errorf("cleanup failed for run %s: %s", failures[0].Run.RunID, failures[0].Error)
	}
	return fmt.Errorf("cleanup failed for %d runs", len(failures))
}

func (store *RunStore) auditCleanupStarted(report CleanupReport) error {
	if store.audit == nil {
		return nil
	}
	return store.audit.Append(AuditEvent{
		Type:    AuditTypeRunCleanupStarted,
		Actor:   AuditActorLocalBrowser,
		Outcome: "success",
		Details: map[string]string{
			"dry_run":            strconv.FormatBool(report.DryRun),
			"older_than":         report.OlderThan.String(),
			"keep_terminal_runs": strconv.Itoa(report.KeepTerminalRuns),
			"candidate_runs":     strconv.Itoa(len(report.Candidates)),
		},
	})
}

func (store *RunStore) auditCleanupDeleted(run RunRecord) error {
	if store.audit == nil {
		return nil
	}
	return store.audit.Append(AuditEvent{
		Type:       AuditTypeRunCleanupDeleted,
		RunID:      run.RunID,
		WorkflowID: run.WorkflowID,
		Actor:      AuditActorLocalBrowser,
		Outcome:    "success",
		Details: map[string]string{
			"dry_run":       "false",
			"artifacts_dir": run.ArtifactsDir,
			"status":        string(run.Status),
		},
	})
}

func (store *RunStore) auditCleanupFailed(run RunRecord, dryRun bool, cause error) error {
	if store.audit == nil {
		return nil
	}
	return store.audit.Append(AuditEvent{
		Type:       AuditTypeRunCleanupFailed,
		RunID:      run.RunID,
		WorkflowID: run.WorkflowID,
		Actor:      AuditActorLocalBrowser,
		Outcome:    "failure",
		Details: map[string]string{
			"dry_run":       strconv.FormatBool(dryRun),
			"artifacts_dir": run.ArtifactsDir,
			"status":        string(run.Status),
			"reason":        cause.Error(),
		},
	})
}
