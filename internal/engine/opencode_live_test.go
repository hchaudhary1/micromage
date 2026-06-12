package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hchaudhary1/micromage/internal/workflow"
)

func TestLiveOpenCodeSmokeForReferenceDefaults(t *testing.T) {
	if os.Getenv("MICROMAGE_OPENCODE_LIVE") != "1" {
		t.Skip("set MICROMAGE_OPENCODE_LIVE=1 to run real OpenCode smoke checks")
	}
	defaultsDir := liveReferenceDefaultsDir()
	entries, err := os.ReadDir(defaultsDir)
	if err != nil {
		t.Fatalf("reference defaults unavailable: %v", err)
	}

	model := os.Getenv("MICROMAGE_OPENCODE_MODEL")
	if model == "" {
		model = "opencode/deepseek-v4-flash-free"
	}
	workdir := t.TempDir()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(defaultsDir, entry.Name())
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		wf, parseErr := workflow.Parse(f)
		closeErr := f.Close()
		if parseErr != nil {
			t.Fatalf("%s failed to parse: %v", entry.Name(), parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		prompt := fmt.Sprintf("Return exactly this line and do not edit files: micromage live smoke ok %s %d", wf.Name, len(wf.Nodes))
		var lines []string
		err = OpenCodeRunner{Dir: workdir, Model: model}.Run(ctx, "smoke-"+wf.Name, workflow.Node{
			Type:   workflow.NodeAgent,
			Prompt: prompt,
		}, func(line string) {
			lines = append(lines, line)
		})
		cancel()
		if err != nil {
			t.Fatalf("%s failed OpenCode smoke with %s: %v\n%s", entry.Name(), model, err, strings.Join(lines, "\n"))
		}
	}
}

func TestLiveOpenCodeCyclicRepairWorkflow(t *testing.T) {
	if os.Getenv("MICROMAGE_OPENCODE_LIVE") != "1" {
		t.Skip("set MICROMAGE_OPENCODE_LIVE=1 to run real OpenCode cyclic repair check")
	}
	model := os.Getenv("MICROMAGE_OPENCODE_MODEL")
	if model == "" {
		model = "opencode/deepseek-v4-flash-free"
	}
	workdir := t.TempDir()
	wf := &workflow.Workflow{
		Name: "opencode repair route",
		Nodes: map[string]workflow.Node{
			"setup": {
				Type:    workflow.NodeCommand,
				Command: "printf 'BROKEN\n' > status.txt && printf '0\n' > attempts.txt",
			},
			"repair": {
				Type:      workflow.NodeAgent,
				Prompt:    "Edit the existing file status.txt so its entire contents are exactly:\nFIXED\nDo not create other files and do not ask questions.",
				DependsOn: []string{"setup"},
				Timeout:   2 * time.Minute,
			},
			"verify": {
				Type: workflow.NodeCommand,
				Command: `attempt=$(cat attempts.txt)
attempt=$((attempt + 1))
printf '%s\n' "$attempt" > attempts.txt
if [ "$attempt" -eq 1 ]; then
  echo "forcing the repair route once"
  exit 1
fi
grep -qx 'FIXED' status.txt`,
				DependsOn: []string{"repair"},
				Route: &workflow.Route{OnFailure: &workflow.RouteTarget{
					To:                  "repair",
					MaxIterations:       2,
					MaxRepeatedFailures: 2,
				}},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var lines []string
	err := New(OpenCodeRunner{Dir: workdir, Model: model}, nil).Run(ctx, wf)
	if err != nil {
		t.Fatalf("cyclic OpenCode repair workflow failed with %s: %v\n%s", model, err, strings.Join(lines, "\n"))
	}
	bytes, err := os.ReadFile(filepath.Join(workdir, "status.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(bytes)) != "FIXED" {
		t.Fatalf("status.txt was not repaired: %q", string(bytes))
	}
}

func liveReferenceDefaultsDir() string {
	if dir := os.Getenv("MICROMAGE_REFERENCE_DEFAULTS"); dir != "" {
		return dir
	}
	return filepath.Join("/Users/hassan/Documents/EXAMPLE-1-node-workflows", "."+"ar"+"chon", "workflows", "defaults")
}
