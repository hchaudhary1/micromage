package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hchaudhary1/micromage/internal/provider"
	"github.com/hchaudhary1/micromage/internal/workflow"
)

type OpenCodeRunner struct {
	Dir        string
	Env        []string
	Provider   string
	Binary     string
	Model      string
	CommandDir string
	Arguments  string
}

func (r OpenCodeRunner) Run(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
	switch node.Type {
	case workflow.NodeCommand:
		return CommandRunner{Dir: r.Dir, Env: r.Env}.Run(ctx, nodeID, node, record)
	case workflow.NodeAgent:
		prompt, err := r.nodePrompt(node)
		if err != nil {
			return err
		}
		_, err = r.runPrompt(ctx, nodeID, prompt, record)
		return err
	case workflow.NodeLoop:
		return r.runLoop(ctx, nodeID, node, record)
	default:
		return fmt.Errorf("opencode runner does not support node type %q", node.Type)
	}
}

func (r OpenCodeRunner) runLoop(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
	maxIterations := node.Loop.MaxIterations
	if maxIterations == 0 {
		maxIterations = 1
	}
	for i := 1; i <= maxIterations; i++ {
		record(fmt.Sprintf("loop iteration %d/%d", i, maxIterations))
		out, err := r.runPrompt(ctx, nodeID, expandPrompt(node.Loop.Prompt, r.Arguments), record)
		if err != nil {
			return err
		}
		if node.Loop.Until == "" || strings.Contains(out, node.Loop.Until) {
			return nil
		}
	}
	return fmt.Errorf("loop node %s did not reach completion marker %q", nodeID, node.Loop.Until)
}

func (r OpenCodeRunner) nodePrompt(node workflow.Node) (string, error) {
	if strings.TrimSpace(node.Prompt) != "" {
		return expandPrompt(node.Prompt, r.Arguments), nil
	}
	if strings.TrimSpace(r.CommandDir) == "" {
		return "", fmt.Errorf("command template %q requires --command-dir", node.Command)
	}
	path := filepath.Join(r.CommandDir, node.Command+".md")
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read command template %q: %w", node.Command, err)
	}
	// Command templates let migrated workflows keep their intent while changing providers.
	return expandPrompt(string(bytes), r.Arguments), nil
}

func (r OpenCodeRunner) runPrompt(ctx context.Context, nodeID, prompt string, record func(string)) (string, error) {
	promptFile, cleanup, err := writePromptFile(nodeID, prompt)
	if err != nil {
		return "", err
	}
	defer cleanup()

	inv, err := provider.Render(provider.Request{
		Name:       r.Provider,
		Binary:     r.Binary,
		Model:      r.Model,
		Dir:        r.Dir,
		Prompt:     prompt,
		PromptFile: promptFile,
		NodeID:     nodeID,
	})
	if err != nil {
		return "", err
	}
	if err := provider.CheckBinary(inv, nil); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	cmd.Dir = r.Dir
	// Provider CLIs run as child processes so Micromage can keep deterministic event logs.
	cmd.Env = append(os.Environ(), inv.Env...)
	cmd.Env = append(cmd.Env, r.Env...)
	if inv.Stdin != "" {
		cmd.Stdin = strings.NewReader(inv.Stdin)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	var wg sync.WaitGroup
	var outMu sync.Mutex
	var combined strings.Builder
	collect := func(line string) {
		record(line)
		outMu.Lock()
		combined.WriteString(line)
		combined.WriteByte('\n')
		outMu.Unlock()
	}
	wg.Add(2)
	go scanOpenCode(&wg, stdout, collect)
	go scanOpenCode(&wg, stderr, collect)

	if err := cmd.Start(); err != nil {
		return "", err
	}
	waitErr := cmd.Wait()
	wg.Wait()
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return combined.String(), fmt.Errorf("opencode timed out or was canceled: %w", ctx.Err())
	}
	return combined.String(), waitErr
}

func writePromptFile(nodeID, prompt string) (string, func(), error) {
	f, err := os.CreateTemp("", "micromage-"+safeTempName(nodeID)+"-*.md")
	if err != nil {
		return "", func() {}, err
	}
	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", func() {}, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

func safeTempName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func expandPrompt(prompt, arguments string) string {
	return os.Expand(prompt, func(key string) string {
		if key == "ARGUMENTS" || key == "USER_MESSAGE" {
			return arguments
		}
		return os.Getenv(key)
	})
}

func scanOpenCode(wg *sync.WaitGroup, r io.Reader, record func(string)) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		record(scanner.Text())
	}
}
