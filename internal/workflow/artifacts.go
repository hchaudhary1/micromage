package workflow

import (
	"fmt"
	"path/filepath"
	"strings"
)

func ResolveDeclaredArtifactPath(path string, artifactsDir string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(artifactsDir))
	if err != nil {
		return "", fmt.Errorf("resolve artifacts directory: %w", err)
	}
	resolved := filepath.Clean(path)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, resolved)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve declared output %q: %w", path, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("compare declared output %q to artifacts directory %q: %w", path, root, err)
	}
	// Declared artifact paths stay inside one run so workflow metadata cannot touch unrelated files.
	if rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return resolved, nil
	}
	return "", fmt.Errorf("declared output %q resolves outside artifacts directory %q", path, root)
}
