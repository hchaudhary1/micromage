package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hchaudhary1/micromage/internal/testharness"
	"github.com/hchaudhary1/micromage/internal/workflow"
)

func TestParseComplexWorkflowFixtures(t *testing.T) {
	paths, err := testharness.WorkflowFiles(testharness.ComplexWorkflowDir(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	expectedErrors := map[string]string{
		"invalid_cycle.yaml":      "cycle detected",
		"invalid_dependency.yaml": "unknown dependency",
		"invalid_timeout.yaml":    "invalid duration",
	}

	validCount := 0
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			wf, parseErr := workflow.Parse(f)
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}

			if want := expectedErrors[filepath.Base(path)]; want != "" {
				if parseErr == nil || !strings.Contains(parseErr.Error(), want) {
					t.Fatalf("expected diagnostic containing %q, got %v", want, parseErr)
				}
				return
			}
			if parseErr != nil {
				t.Fatalf("expected fixture to validate: %v", parseErr)
			}
			if _, err := wf.PlanLayers(); err != nil {
				t.Fatalf("expected fixture to plan: %v", err)
			}
			validCount++
		})
	}
	if validCount < 3 {
		t.Fatalf("validated %d complex fixtures, want at least 3", validCount)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
