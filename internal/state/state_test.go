package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApprovePausedGatePersistsReview(t *testing.T) {
	run := NewRun("local", "release", "workflow.yaml")
	run.MarkPassed("build", "ok")
	run.MarkPaused("review", "approve release")

	at := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	if err := run.Approve("review", "hassan", "ship it", at); err != nil {
		t.Fatal(err)
	}
	if run.PausedNode != "" {
		t.Fatalf("expected pause to clear, got %q", run.PausedNode)
	}
	review := run.Nodes["review"]
	if review.Status != StatusPassed || review.ApprovedBy != "hassan" || review.Comment != "ship it" {
		t.Fatalf("approval was not recorded: %#v", review)
	}
}

func TestApproveRejectsWrongGate(t *testing.T) {
	run := NewRun("local", "release", "workflow.yaml")
	run.MarkPaused("review", "approve release")

	if err := run.Approve("deploy", "hassan", "", time.Time{}); err == nil {
		t.Fatal("expected wrong gate approval to fail")
	}
	if run.Nodes["review"].Status != StatusPaused {
		t.Fatalf("wrong approval changed paused gate: %#v", run.Nodes["review"])
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "run.json")
	run := NewRun("r1", "workflow", "workflow.yaml")
	run.MarkPassed("setup", "done")
	run.MarkPaused("review", "approve")
	if err := Save(path, run); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != "r1" || loaded.PausedNode != "review" || loaded.Nodes["setup"].Output != "done" {
		t.Fatalf("unexpected loaded state: %#v", loaded)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
