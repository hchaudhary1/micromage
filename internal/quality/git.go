package quality

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// StagedFileContent contains the index version of a file so commits are checked exactly as staged.
type StagedFileContent struct {
	Path    string
	Content string
}

func loadStagedFiles(ctx context.Context, repo string) ([]StagedFileContent, error) {
	out, err := git(ctx, repo, "diff", "--cached", "--name-only", "--diff-filter=ACMR")
	if err != nil {
		return nil, err
	}
	var files []StagedFileContent
	for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		content, err := git(ctx, repo, "show", ":"+path)
		if err != nil {
			return nil, fmt.Errorf("read staged %s: %w", path, err)
		}
		files = append(files, StagedFileContent{Path: path, Content: string(content)})
	}
	return files, nil
}

func git(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
