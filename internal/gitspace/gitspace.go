package gitspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Prepare(repoRoot, runID, baseDir string) (string, func() error, error) {
	if repoRoot == "" {
		return "", nil, fmt.Errorf("repo root is required")
	}
	if runID == "" {
		return "", nil, fmt.Errorf("run id is required")
	}
	if baseDir == "" {
		baseDir = filepath.Join(repoRoot, ".micromage", "worktrees")
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", nil, err
	}

	branch := "micromage/" + sanitize(runID)
	path := filepath.Join(baseDir, sanitize(runID))
	// Isolated worktrees keep simultaneous agent runs from overwriting each other.
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-B", branch, path, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("create worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() error {
		cmd := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("remove worktree: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return path, cleanup, nil
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '/':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-/")
}
