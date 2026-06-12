package gitspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrepareCreatesBranchWorktree(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "initial")

	base := filepath.Join(t.TempDir(), "runs")
	path, cleanup, err := Prepare(root, "ticket-123", base)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if path == root {
		t.Fatal("expected isolated worktree path")
	}
	if _, err := exec.Command("git", "-C", path, "status", "--short").Output(); err != nil {
		t.Fatalf("expected usable worktree: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
