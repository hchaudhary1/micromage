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

const auditSchemaVersion = 1

const (
	AuditTypeRealRunAuthorized = "real_run_authorized"
	AuditTypeRealRunRejected   = "real_run_rejected"
	AuditTypeRunStarted        = "run_started"
	AuditTypeRunFinished       = "run_finished"
	AuditTypeRunInterrupted    = "run_interrupted"
	AuditTypeRunCleanupStarted = "run_cleanup_started"
	AuditTypeRunCleanupDeleted = "run_cleanup_deleted"
	AuditTypeRunCleanupFailed  = "run_cleanup_failed"
	AuditTypeWorkflowSaved     = "workflow_saved"
	AuditTypeTemplateSaved     = "template_saved"
)

const AuditActorLocalBrowser = "local-browser"

type AuditEvent struct {
	SchemaVersion int               `json:"schema_version"`
	EventID       string            `json:"event_id"`
	Type          string            `json:"type"`
	CreatedAt     time.Time         `json:"created_at"`
	RunID         string            `json:"run_id,omitempty"`
	WorkflowID    string            `json:"workflow_id,omitempty"`
	Actor         string            `json:"actor"`
	Outcome       string            `json:"outcome"`
	Details       map[string]string `json:"details,omitempty"`
}

type AuditStore struct {
	repoRoot   string
	now        func() time.Time
	newEventID func() string
}

func NewAuditStore(repoRoot string) *AuditStore {
	if repoRoot == "" {
		repoRoot = "."
	}
	return &AuditStore{
		repoRoot: filepath.Clean(repoRoot),
		now:      func() time.Time { return time.Now().UTC() },
		newEventID: func() string {
			return "audit-" + fmt.Sprintf("%d", time.Now().UnixNano())
		},
	}
}

func (store *AuditStore) Append(event AuditEvent) error {
	if store == nil {
		return errors.New("audit store is nil")
	}
	if strings.TrimSpace(event.Type) == "" {
		return errors.New("audit event type is required")
	}
	if event.Actor == "" {
		event.Actor = AuditActorLocalBrowser
	}
	if event.Outcome == "" {
		event.Outcome = "success"
	}
	event.SchemaVersion = auditSchemaVersion
	if event.EventID == "" {
		event.EventID = store.newEventID()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = store.now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	event.Details = sanitizeAuditDetails(event.Details)

	path := filepath.Join(store.repoRoot, ".micromage", "audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open audit events: %w", err)
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func sanitizeAuditDetails(details map[string]string) map[string]string {
	if len(details) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(details))
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isSensitiveAuditDetailKey(key) {
			cleaned[key] = "[redacted]"
			continue
		}
		cleaned[key] = sanitizeRunFailureReason(details[key])
	}
	return cleaned
}

func isSensitiveAuditDetailKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, marker := range []string{"authorization", "token", "credential", "secret", "password", "prompt", "node_log", "artifact_content"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
