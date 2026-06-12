package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const declaredArtifactKind = "declared_output"

type ArtifactManifest struct {
	SchemaVersion    int                        `json:"schema_version"`
	RunID            string                     `json:"run_id"`
	WorkflowID       string                     `json:"workflow_id"`
	CreatedAt        time.Time                  `json:"created_at"`
	ArtifactsDir     string                     `json:"artifacts_dir"`
	WorkflowSnapshot string                     `json:"workflow_snapshot"`
	Artifacts        []ArtifactManifestArtifact `json:"artifacts"`
	CompletedNodes   []string                   `json:"completed_nodes"`
	FailedNodes      []RunFailure               `json:"failed_nodes"`
}

type ArtifactManifestArtifact struct {
	NodeID    string    `json:"node_id"`
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type RunArtifactManifestWrite struct {
	RepoRoot     string
	RunID        string
	WorkflowID   string
	ArtifactsDir string
	WorkflowYAML string
	Summary      RunEvent
	CreatedAt    time.Time
}

func CollectDeclaredArtifacts(parsed Workflow, artifactsDir string, runID string) []RunArtifact {
	var artifacts []RunArtifact
	for _, node := range parsed.Nodes {
		for _, pattern := range node.Outputs {
			path, err := resolveDeclaredArtifactPattern(pattern, artifactsDir, runID)
			if err != nil {
				continue
			}
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				artifacts = append(artifacts, RunArtifact{NodeID: node.ID, Path: path})
			}
		}
	}
	return artifacts
}

func WriteRunArtifactManifest(write RunArtifactManifestWrite) error {
	if strings.TrimSpace(write.RunID) == "" {
		return errors.New("run id is required")
	}
	if strings.TrimSpace(write.ArtifactsDir) == "" {
		return errors.New("artifacts directory is required")
	}
	createdAt := write.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	runDir, err := filepath.Abs(filepath.Clean(write.ArtifactsDir))
	if err != nil {
		return fmt.Errorf("resolve artifacts directory: %w", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("create artifacts directory: %w", err)
	}
	manifestArtifacts, err := manifestArtifactsFromSummary(write.Summary.Artifacts, runDir)
	if err != nil {
		return err
	}
	manifest := ArtifactManifest{
		SchemaVersion:    runStoreSchemaVersion,
		RunID:            write.RunID,
		WorkflowID:       write.WorkflowID,
		CreatedAt:        createdAt,
		ArtifactsDir:     relativeArtifactDir(write.RepoRoot, runDir),
		WorkflowSnapshot: "workflow.yaml",
		Artifacts:        manifestArtifacts,
		CompletedNodes:   append([]string(nil), write.Summary.CompletedNodes...),
		FailedNodes:      append([]RunFailure(nil), write.Summary.FailedNodes...),
	}

	if err := writeAtomicFile(filepath.Join(runDir, "workflow.yaml"), []byte(write.WorkflowYAML)); err != nil {
		return fmt.Errorf("write workflow snapshot: %w", err)
	}
	summaryData, err := json.MarshalIndent(write.Summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run summary: %w", err)
	}
	if err := writeAtomicFile(filepath.Join(runDir, "summary.json"), append(summaryData, '\n')); err != nil {
		return fmt.Errorf("write run summary snapshot: %w", err)
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifact manifest: %w", err)
	}
	// The manifest is published last so readers only see complete run artifact metadata.
	if err := writeAtomicFile(filepath.Join(runDir, "manifest.json"), append(manifestData, '\n')); err != nil {
		return fmt.Errorf("write artifact manifest: %w", err)
	}
	return nil
}

func resolveDeclaredArtifactPattern(pattern string, artifactsDir string, runID string) (string, error) {
	path := strings.ReplaceAll(pattern, "$ARTIFACTS_DIR", artifactsDir)
	path = strings.ReplaceAll(path, "$WORKFLOW_ID", runID)
	return ResolveDeclaredArtifactPath(path, artifactsDir)
}

func manifestArtifactsFromSummary(artifacts []RunArtifact, runDir string) ([]ArtifactManifestArtifact, error) {
	manifestArtifacts := make([]ArtifactManifestArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		path, rel, err := containedArtifactPath(artifact.Path, runDir)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat artifact %q: %w", artifact.Path, err)
		}
		if info.IsDir() {
			continue
		}
		hash, err := sha256File(path)
		if err != nil {
			return nil, err
		}
		manifestArtifacts = append(manifestArtifacts, ArtifactManifestArtifact{
			NodeID:    artifact.NodeID,
			Path:      filepath.ToSlash(rel),
			Kind:      declaredArtifactKind,
			SizeBytes: info.Size(),
			SHA256:    hash,
			CreatedAt: info.ModTime().UTC(),
		})
	}
	return manifestArtifacts, nil
}

func containedArtifactPath(path string, runDir string) (string, string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(runDir, clean)
	}
	resolved, err := filepath.Abs(clean)
	if err != nil {
		return "", "", fmt.Errorf("resolve artifact %q: %w", path, err)
	}
	rel, err := filepath.Rel(runDir, resolved)
	if err != nil {
		return "", "", fmt.Errorf("compare artifact %q to run directory: %w", path, err)
	}
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("artifact %q resolves outside run directory %q", path, runDir)
	}
	return resolved, rel, nil
}

func relativeArtifactDir(repoRoot string, runDir string) string {
	if strings.TrimSpace(repoRoot) == "" {
		return filepath.Clean(runDir)
	}
	root, err := filepath.Abs(filepath.Clean(repoRoot))
	if err != nil {
		return filepath.Clean(runDir)
	}
	rel, err := filepath.Rel(root, runDir)
	if err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return filepath.Clean(runDir)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open artifact for hash %q: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash artifact %q: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeAtomicFile(path string, data []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
