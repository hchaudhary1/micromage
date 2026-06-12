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

func TestAuditStoreAppendsStructuredEventsAndRedactsSensitiveDetails(t *testing.T) {
	repo := t.TempDir()
	store := NewAuditStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 8, 0, 0, 0, time.UTC))
	store.newEventID = sequenceAuditID()

	err := store.Append(AuditEvent{
		Type:       AuditTypeRealRunRejected,
		Actor:      AuditActorLocalBrowser,
		Outcome:    "failure",
		WorkflowID: "review-last-commit",
		Details: map[string]string{
			"mode":          "real",
			"reason":        "token_mismatch",
			"authorization": "Bearer secret-token",
			"prompt_body":   "do sensitive work",
		},
	})
	if err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	events := readAuditEventLines(t, repo)
	if len(events) != 1 {
		t.Fatalf("expected one audit event, got %#v", events)
	}
	event := events[0]
	if event.SchemaVersion != 1 || event.EventID != "audit-1" || event.Type != AuditTypeRealRunRejected || event.WorkflowID != "review-last-commit" {
		t.Fatalf("unexpected audit event shape: %#v", event)
	}
	if event.Actor != AuditActorLocalBrowser || event.Outcome != "failure" {
		t.Fatalf("unexpected actor/outcome: %#v", event)
	}
	if event.Details["mode"] != "real" || event.Details["reason"] != "token_mismatch" {
		t.Fatalf("expected non-sensitive details to remain, got %#v", event.Details)
	}
	raw := readAuditFile(t, repo)
	for _, leaked := range []string{"secret-token", "Bearer secret-token", "do sensitive work"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("audit log leaked sensitive detail %q in %s", leaked, raw)
		}
	}
	if !strings.Contains(raw, `"[redacted]"`) {
		t.Fatalf("expected redacted marker in audit log, got %s", raw)
	}
}

func TestAuditStoreSupportsCleanupEventTypesWithoutCleanupAPI(t *testing.T) {
	repo := t.TempDir()
	store := NewAuditStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 8, 30, 0, 0, time.UTC))
	store.newEventID = sequenceAuditID()

	for _, event := range []AuditEvent{
		{Type: AuditTypeRunCleanupStarted, Outcome: "success", Details: map[string]string{"dry_run": "true", "deleted_runs": "0"}},
		{Type: AuditTypeRunCleanupDeleted, Outcome: "success", Details: map[string]string{"dry_run": "false", "deleted_runs": "1"}},
		{Type: AuditTypeRunCleanupFailed, Outcome: "failure", Details: map[string]string{"dry_run": "false", "deleted_runs": "0"}},
	} {
		if err := store.Append(event); err != nil {
			t.Fatalf("Append %s returned error: %v", event.Type, err)
		}
	}

	events := readAuditEventLines(t, repo)
	if len(events) != 3 {
		t.Fatalf("expected cleanup audit events, got %#v", events)
	}
	for index, eventType := range []string{AuditTypeRunCleanupStarted, AuditTypeRunCleanupDeleted, AuditTypeRunCleanupFailed} {
		if events[index].Type != eventType || events[index].Actor != AuditActorLocalBrowser {
			t.Fatalf("unexpected cleanup event %d: %#v", index, events[index])
		}
	}
	if events[2].Outcome != "failure" {
		t.Fatalf("expected cleanup failure outcome, got %#v", events[2])
	}
}

func readAuditEventLines(t *testing.T, repo string) []AuditEvent {
	t.Helper()
	file, err := os.Open(filepath.Join(repo, ".micromage", "audit.jsonl"))
	if err != nil {
		t.Fatalf("open audit events: %v", err)
	}
	defer file.Close()
	var events []AuditEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event AuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode audit event %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan audit events: %v", err)
	}
	return events
}

func readAuditFile(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".micromage", "audit.jsonl"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	return string(data)
}

func sequenceAuditID() func() string {
	next := 0
	return func() string {
		next++
		return "audit-" + strconv.Itoa(next)
	}
}
