package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectDeclaredArtifactsUsesExistingOutputsInsideRun(t *testing.T) {
	repo := t.TempDir()
	artifactsDir := filepath.Join(repo, ".micromage", "runs", "run-artifacts")
	inside := filepath.Join(artifactsDir, "review", "finding.md")
	outside := filepath.Join(repo, "outside.md")
	for path, content := range map[string]string{
		inside:  "finding",
		outside: "outside",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create artifact dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write artifact fixture: %v", err)
		}
	}
	parsed := Workflow{Nodes: []Node{
		{ID: "inside", Outputs: []string{"$ARTIFACTS_DIR/review/finding.md"}},
		{ID: "missing", Outputs: []string{"$ARTIFACTS_DIR/review/missing.md"}},
		{ID: "escape", Outputs: []string{outside}},
	}}

	artifacts := CollectDeclaredArtifacts(parsed, artifactsDir, "run-artifacts")

	if len(artifacts) != 1 {
		t.Fatalf("expected one in-run artifact, got %#v", artifacts)
	}
	if artifacts[0].NodeID != "inside" || artifacts[0].Path != inside {
		t.Fatalf("unexpected artifact: %#v", artifacts[0])
	}
}

func TestWriteRunArtifactManifestRejectsArtifactsOutsideRun(t *testing.T) {
	repo := t.TempDir()
	artifactsDir := filepath.Join(repo, ".micromage", "runs", "run-unsafe")
	outside := filepath.Join(repo, "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}

	err := WriteRunArtifactManifest(RunArtifactManifestWrite{
		RepoRoot:     repo,
		RunID:        "run-unsafe",
		WorkflowID:   "unsafe-workflow",
		ArtifactsDir: artifactsDir,
		WorkflowYAML: "name: unsafe-workflow\nnodes: []\n",
		Summary: RunEvent{
			Type:         "run_summary",
			RunID:        "run-unsafe",
			ArtifactsDir: artifactsDir,
			Artifacts:    []RunArtifact{{NodeID: "escape", Path: outside}},
		},
		CreatedAt: time.Date(2026, 6, 12, 1, 0, 0, 0, time.UTC),
	})

	if err == nil {
		t.Fatal("expected unsafe artifact path to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(artifactsDir, "manifest.json")); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe manifest write should not create manifest.json, stat error: %v", statErr)
	}
}

func TestWriteRunArtifactManifestPersistsSummaryAndSnapshots(t *testing.T) {
	repo := t.TempDir()
	artifactsDir := filepath.Join(repo, ".micromage", "runs", "run-summary")
	artifactPath := filepath.Join(artifactsDir, "review", "finding.md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("finding"), 0o644); err != nil {
		t.Fatalf("write artifact fixture: %v", err)
	}
	workflowYAML := "name: real-summary\nnodes:\n  - id: write-review\n"
	summary := RunEvent{
		Type:           "run_summary",
		RunID:          "run-summary",
		ArtifactsDir:   artifactsDir,
		Artifacts:      []RunArtifact{{NodeID: "write-review", Path: artifactPath}},
		CompletedNodes: []string{"write-review"},
		FailedNodes:    []RunFailure{{NodeID: "fail-review", Message: "exit status 7"}},
	}

	err := WriteRunArtifactManifest(RunArtifactManifestWrite{
		RepoRoot:     repo,
		RunID:        "run-summary",
		WorkflowID:   "real-summary",
		ArtifactsDir: artifactsDir,
		WorkflowYAML: workflowYAML,
		Summary:      summary,
		CreatedAt:    time.Date(2026, 6, 12, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("WriteRunArtifactManifest returned error: %v", err)
	}

	manifest := readArtifactManifestFile(t, filepath.Join(artifactsDir, "manifest.json"))
	if manifest.SchemaVersion != 1 || manifest.RunID != "run-summary" || manifest.WorkflowID != "real-summary" {
		t.Fatalf("unexpected manifest metadata: %#v", manifest)
	}
	if manifest.ArtifactsDir != filepath.Join(".micromage", "runs", "run-summary") || manifest.WorkflowSnapshot != "workflow.yaml" {
		t.Fatalf("unexpected manifest paths: %#v", manifest)
	}
	if len(manifest.Artifacts) != 1 {
		t.Fatalf("expected one manifest artifact, got %#v", manifest.Artifacts)
	}
	artifact := manifest.Artifacts[0]
	if artifact.NodeID != "write-review" || artifact.Path != filepath.ToSlash(filepath.Join("review", "finding.md")) || artifact.Kind != "declared_output" {
		t.Fatalf("unexpected manifest artifact: %#v", artifact)
	}
	if artifact.SizeBytes != int64(len("finding")) {
		t.Fatalf("unexpected artifact size: %#v", artifact)
	}
	wantHash := sha256.Sum256([]byte("finding"))
	if artifact.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("unexpected artifact hash: %#v", artifact)
	}
	if len(manifest.CompletedNodes) != 1 || manifest.CompletedNodes[0] != "write-review" {
		t.Fatalf("unexpected completed nodes: %#v", manifest.CompletedNodes)
	}
	if len(manifest.FailedNodes) != 1 || manifest.FailedNodes[0].NodeID != "fail-review" {
		t.Fatalf("unexpected failed nodes: %#v", manifest.FailedNodes)
	}

	workflowSnapshot, err := os.ReadFile(filepath.Join(artifactsDir, "workflow.yaml"))
	if err != nil {
		t.Fatalf("read workflow snapshot: %v", err)
	}
	if string(workflowSnapshot) != workflowYAML {
		t.Fatalf("unexpected workflow snapshot: %q", string(workflowSnapshot))
	}
	summarySnapshot, err := os.ReadFile(filepath.Join(artifactsDir, "summary.json"))
	if err != nil {
		t.Fatalf("read summary snapshot: %v", err)
	}
	var persistedSummary RunEvent
	if err := json.Unmarshal(summarySnapshot, &persistedSummary); err != nil {
		t.Fatalf("decode summary snapshot: %v", err)
	}
	if persistedSummary.RunID != summary.RunID || len(persistedSummary.Artifacts) != 1 {
		t.Fatalf("unexpected persisted summary: %#v", persistedSummary)
	}
	if _, err := os.Stat(filepath.Join(artifactsDir, "manifest.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("manifest write should not leave temporary file, stat error: %v", err)
	}
}

func readArtifactManifestFile(t *testing.T, path string) ArtifactManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest ArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return manifest
}
