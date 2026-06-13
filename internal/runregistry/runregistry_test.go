package runregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestDefaultPathsResolveUnderMicromageRuns(t *testing.T) {
	workdir := t.TempDir()

	paths := DefaultPaths(workdir, "run-1")

	wantDir := filepath.Join(workdir, ".micromage", "runs", "run-1")
	if paths.Dir != wantDir {
		t.Fatalf("unexpected run dir: %s", paths.Dir)
	}
	if paths.MetadataPath != filepath.Join(wantDir, "run.json") {
		t.Fatalf("unexpected metadata path: %s", paths.MetadataPath)
	}
	if paths.LogPath != filepath.Join(wantDir, "run.jsonl") {
		t.Fatalf("unexpected log path: %s", paths.LogPath)
	}
	if paths.StatePath != filepath.Join(wantDir, "state.json") {
		t.Fatalf("unexpected state path: %s", paths.StatePath)
	}
	if paths.ProcessLogPath != filepath.Join(wantDir, "process.log") {
		t.Fatalf("unexpected process log path: %s", paths.ProcessLogPath)
	}
}

func TestNewMetadataPopulatesJSONFields(t *testing.T) {
	workdir := t.TempDir()
	startedAt := time.Date(2026, 6, 13, 4, 0, 0, 0, time.UTC)

	metadata := NewMetadata("run-1", "build", "workflow.yaml", workdir, []string{"micromage", "run"}, []string{"micromage-child"}, 123, startedAt)
	bytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]any
	if err := json.Unmarshal(bytes, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"schema_version",
		"run_id",
		"workflow_name",
		"workflow_path",
		"workdir",
		"log_path",
		"state_path",
		"process_log_path",
		"original_argv",
		"child_argv",
		"pid",
		"status",
		"started_at",
		"ended_at",
		"exit_code",
		"error",
	} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("missing JSON field %q in %s", field, string(bytes))
		}
	}
	if metadata.Status != StatusRunning {
		t.Fatalf("expected running status, got %s", metadata.Status)
	}
}

func TestGenerateRunIDIsSafeAndDistinct(t *testing.T) {
	seen := map[string]bool{}
	safe := regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

	for i := 0; i < 200; i++ {
		runID, err := GenerateRunID()
		if err != nil {
			t.Fatal(err)
		}
		if !safe.MatchString(runID) {
			t.Fatalf("run id is not filesystem safe: %q", runID)
		}
		if seen[runID] {
			t.Fatalf("duplicate run id: %q", runID)
		}
		seen[runID] = true
	}
}

func TestSaveLoadRoundTripAtomically(t *testing.T) {
	workdir := t.TempDir()
	metadata := NewMetadata("run-1", "build", "workflow.yaml", workdir, []string{"micromage"}, []string{"child"}, 123, time.Date(2026, 6, 13, 4, 0, 0, 0, time.UTC))
	paths := DefaultPaths(workdir, metadata.RunID)

	if err := Save(paths.MetadataPath, metadata); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths.MetadataPath, func(pid int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != metadata.RunID || loaded.LogPath != paths.LogPath || loaded.ChildArgv[0] != "child" {
		t.Fatalf("unexpected loaded metadata: %#v", loaded)
	}
	matches, err := filepath.Glob(filepath.Join(paths.Dir, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files were left behind: %#v", matches)
	}
}

func TestLoadReportsRunningRunAsStaleWhenPIDIsNotAlive(t *testing.T) {
	workdir := t.TempDir()
	metadata := NewMetadata("run-1", "build", "workflow.yaml", workdir, nil, nil, 999, time.Now().UTC())
	paths := DefaultPaths(workdir, metadata.RunID)
	if err := Save(paths.MetadataPath, metadata); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(paths.MetadataPath, func(pid int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusStale {
		t.Fatalf("expected stale status, got %s", loaded.Status)
	}
}

func TestLoadDoesNotMarkCompletedRunStale(t *testing.T) {
	workdir := t.TempDir()
	metadata := NewMetadata("run-1", "build", "workflow.yaml", workdir, nil, nil, 999, time.Now().UTC())
	metadata.Status = StatusFailed
	paths := DefaultPaths(workdir, metadata.RunID)
	if err := Save(paths.MetadataPath, metadata); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(paths.MetadataPath, func(pid int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusFailed {
		t.Fatalf("expected failed status, got %s", loaded.Status)
	}
}

func TestListSortsNewestFirstAndLatestReturnsNewest(t *testing.T) {
	workdir := t.TempDir()
	oldRun := NewMetadata("old", "build", "workflow.yaml", workdir, nil, nil, 1, time.Date(2026, 6, 13, 1, 0, 0, 0, time.UTC))
	newRun := NewMetadata("new", "deploy", "workflow.yaml", workdir, nil, nil, 2, time.Date(2026, 6, 13, 2, 0, 0, 0, time.UTC))
	for _, metadata := range []Metadata{oldRun, newRun} {
		paths := DefaultPaths(workdir, metadata.RunID)
		if err := Save(paths.MetadataPath, metadata); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := List(workdir, func(pid int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].RunID != "new" || runs[1].RunID != "old" {
		t.Fatalf("runs were not sorted newest first: %#v", runs)
	}
	latest, err := Latest(workdir, func(pid int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if latest.RunID != "new" {
		t.Fatalf("expected latest run new, got %s", latest.RunID)
	}
}

func TestListIgnoresRunDirectoriesWithoutMetadata(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".micromage", "runs", "partial"), 0o755); err != nil {
		t.Fatal(err)
	}

	runs, err := List(workdir, func(pid int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no runs, got %#v", runs)
	}
}

func TestLatestReturnsNotFoundWhenRegistryIsEmpty(t *testing.T) {
	_, err := Latest(t.TempDir(), func(pid int) bool { return true })
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
