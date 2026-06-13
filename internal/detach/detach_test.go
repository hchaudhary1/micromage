package detach

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPrepareCommandBuildsCommandAndAttachesStdio(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := dir + "/process.log"
	req := LaunchRequest{
		Argv:    []string{"echo", "hello"},
		Dir:     dir,
		Env:     []string{"MICROMAGE_TEST=value"},
		LogPath: logPath,
	}

	cmd, cleanup, err := prepareCommand(req)
	if err != nil {
		t.Fatalf("prepareCommand() error = %v", err)
	}
	defer cleanup()

	if got := strings.Join(cmd.Args, " "); got != "echo hello" {
		t.Fatalf("cmd.Args = %q, want echo hello", got)
	}
	if cmd.Dir != dir {
		t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, dir)
	}
	if got := strings.Join(cmd.Env, " "); got != "MICROMAGE_TEST=value" {
		t.Fatalf("cmd.Env = %q, want MICROMAGE_TEST=value", got)
	}
	if cmd.Stdin == nil || cmd.Stdout == nil || cmd.Stderr == nil {
		t.Fatal("stdio was not fully attached")
	}
	if cmd.Stdout != cmd.Stderr {
		t.Fatal("stdout and stderr should share the process log")
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
			t.Fatal("unix command should request a separate process group")
		}
	}
}

func TestPrepareCommandRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  LaunchRequest
	}{
		{name: "empty argv", req: LaunchRequest{LogPath: "process.log"}},
		{name: "empty command", req: LaunchRequest{Argv: []string{""}, LogPath: "process.log"}},
		{name: "empty log", req: LaunchRequest{Argv: []string{"echo"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := prepareCommand(tt.req)
			if err == nil {
				t.Fatal("prepareCommand() error = nil, want error")
			}
		})
	}
}

func TestAttachDetachedStdioAppendsProcessLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := dir + "/process.log"
	if err := os.WriteFile(logPath, []byte("existing\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	spawner := Launcher{}
	pid, err := spawner.Launch(LaunchRequest{
		Argv:    []string{"sh", "-c", "echo stdout; echo stderr >&2"},
		LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want positive", pid)
	}

	var data []byte
	for i := 0; i < 20; i++ {
		data, err = os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if strings.Contains(string(data), "stdout") && strings.Contains(string(data), "stderr") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	got := string(data)
	for _, want := range []string{"existing\n", "stdout\n", "stderr\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log = %q, want it to contain %q", got, want)
		}
	}
}
