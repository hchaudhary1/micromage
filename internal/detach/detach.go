package detach

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type LaunchRequest struct {
	Argv    []string
	Dir     string
	Env     []string
	LogPath string
}

type Spawner interface {
	Launch(req LaunchRequest) (int, error)
}

type Launcher struct{}

func (Launcher) Launch(req LaunchRequest) (int, error) {
	cmd, cleanup, err := prepareCommand(req)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func prepareCommand(req LaunchRequest) (*exec.Cmd, func(), error) {
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return nil, nil, errors.New("detach: argv must include a command")
	}
	if req.LogPath == "" {
		return nil, nil, errors.New("detach: log path is required")
	}

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = req.Env
	cmd.SysProcAttr = detachedSysProcAttr()

	cleanup, err := attachDetachedStdio(cmd, req.LogPath)
	if err != nil {
		return nil, nil, err
	}
	return cmd, cleanup, nil
}

func attachDetachedStdio(cmd *exec.Cmd, logPath string) (func(), error) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return nil, fmt.Errorf("detach: open devnull: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("detach: open process log: %w", err)
	}

	cmd.Stdin = stdin
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	return func() {
		closeIfPossible(stdin)
		closeIfPossible(logFile)
	}, nil
}

func closeIfPossible(value any) {
	closer, ok := value.(io.Closer)
	if ok {
		_ = closer.Close()
	}
}
