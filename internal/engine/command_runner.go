package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/hchaudhary1/micromage/internal/workflow"
)

type CommandRunner struct {
	Dir string
	Env []string
}

func (r CommandRunner) Run(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", node.Command)
	if len(node.Args) > 0 {
		// Direct argv execution lets AI CLIs run without shell quoting surprises.
		cmd = exec.CommandContext(ctx, node.Command, node.Args...)
	}
	cmd.Dir = r.Dir
	// Noninteractive environment nudges wrapped CLIs toward machine-safe behavior.
	cmd.Env = append(os.Environ(), "CI=1", "TERM=dumb", "NO_COLOR=1", "MICROMAGE_NODE_ID="+nodeID)
	cmd.Env = append(cmd.Env, r.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go scanLines(&wg, stdout, record)
	go scanLines(&wg, stderr, record)

	if err := cmd.Start(); err != nil {
		return err
	}
	waitErr := cmd.Wait()
	wg.Wait()
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("command timed out or was canceled: %w", ctx.Err())
	}
	return waitErr
}

func scanLines(wg *sync.WaitGroup, r io.Reader, record func(string)) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		record(scanner.Text())
	}
}
