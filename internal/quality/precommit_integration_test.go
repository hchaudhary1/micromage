package quality

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunPreCommitAgainstTempGitRepo(t *testing.T) {
	repo := initTempGoRepo(t)
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/hook\n\ngo 1.26.4\n")
	writeFile(t, filepath.Join(repo, "calc.go"), "package hook\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(repo, "calc_test.go"), "package hook\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"bad sum\") } }\n")
	gitAdd(t, repo, ".")

	result, err := RunPreCommit(context.Background(), PreCommitOptions{Repo: repo, CoverageThreshold: 70})
	if err != nil {
		t.Fatalf("pre-commit failed: %v\n%s", err, FormatPreCommitResult(result, err))
	}
	if result.Coverage.Percent < 70 {
		t.Fatalf("coverage %.1f below threshold", result.Coverage.Percent)
	}
}

func TestRunPreCommitRejectsBannedStagedContent(t *testing.T) {
	repo := initTempGoRepo(t)
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/hook\n\ngo 1.26.4\n")
	writeFile(t, filepath.Join(repo, "note.md"), bannedTerm("Generated with ", "Claude Code")+"\n")
	gitAdd(t, repo, ".")

	result, err := RunPreCommit(context.Background(), PreCommitOptions{Repo: repo, CoverageThreshold: 70})
	if err == nil {
		t.Fatal("expected pre-commit to reject banned staged attribution")
	}
	if len(result.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(result.Findings))
	}
}

func TestRunPreCommitRejectsLowCoverage(t *testing.T) {
	repo := initTempGoRepo(t)
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/hook\n\ngo 1.26.4\n")
	writeFile(t, filepath.Join(repo, "calc.go"), "package hook\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Untested() int { return 42 }\n")
	writeFile(t, filepath.Join(repo, "calc_test.go"), "package hook\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"bad sum\") } }\n")
	gitAdd(t, repo, ".")

	result, err := RunPreCommit(context.Background(), PreCommitOptions{Repo: repo, CoverageThreshold: 90})
	if err == nil {
		t.Fatal("expected pre-commit to reject low coverage")
	}
	if result.Coverage.Percent >= 90 {
		t.Fatalf("coverage %.1f unexpectedly satisfied threshold", result.Coverage.Percent)
	}
}

func initTempGoRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test User")
	return repo
}

func gitAdd(t *testing.T, repo string, paths ...string) {
	t.Helper()
	args := append([]string{"add"}, paths...)
	run(t, repo, "git", args...)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}
