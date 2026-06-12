package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const runStoreSchemaVersion = 1

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
	return store.writeIndex(index)
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
