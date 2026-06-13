package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStrictRejectsUnknownFields(t *testing.T) {
	_, err := Parse(strings.NewReader(`
name: typo demo
nodes:
  build:
    type: command
    commnad: go test ./...
`))

	if err == nil {
		t.Fatal("expected strict schema error")
	}
	if !strings.Contains(err.Error(), `unknown field "commnad"`) {
		t.Fatalf("expected unknown field detail, got %v", err)
	}
}

func TestParseValidatesDependenciesAndCycles(t *testing.T) {
	_, err := Parse(strings.NewReader(`
name: missing dep
nodes:
  test:
    type: command
    command: go test ./...
    depends_on: [build]
`))
	if err == nil || !strings.Contains(err.Error(), "unknown dependency") {
		t.Fatalf("expected missing dependency error, got %v", err)
	}

	_, err = Parse(strings.NewReader(`
name: cycle
nodes:
  a:
    type: command
    command: echo a
    depends_on: [b]
  b:
    type: command
    command: echo b
    depends_on: [a]
`))
	if err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestPlanLayersIndependentNodesTogether(t *testing.T) {
	wf, err := Parse(strings.NewReader(`
name: layers
nodes:
  setup:
    type: command
    command: echo setup
  lint:
    type: command
    command: echo lint
    depends_on: [setup]
  test:
    type: command
    command: echo test
    depends_on: [setup]
  report:
    type: command
    command: echo report
    depends_on: [lint, test]
`))
	if err != nil {
		t.Fatal(err)
	}

	layers, err := wf.PlanLayers()
	if err != nil {
		t.Fatal(err)
	}

	want := [][]string{{"setup"}, {"lint", "test"}, {"report"}}
	if len(layers) != len(want) {
		t.Fatalf("got %d layers, want %d: %#v", len(layers), len(want), layers)
	}
	for i := range want {
		if strings.Join(layers[i], ",") != strings.Join(want[i], ",") {
			t.Fatalf("layer %d got %#v, want %#v", i, layers[i], want[i])
		}
	}
}

func TestParseListStylePromptAndGateNodes(t *testing.T) {
	wf, err := Parse(strings.NewReader(`
name: list style
description: compatible schema
provider: other
model: medium
interactive: true
nodes:
  - id: ask
    prompt: Say hello to $ARGUMENTS
  - id: approve
    approval:
      message: Continue?
      capture_response: true
    depends_on: [ask]
`))
	if err != nil {
		t.Fatal(err)
	}
	if wf.Nodes["ask"].Type != NodeAgent {
		t.Fatalf("expected agent node, got %q", wf.Nodes["ask"].Type)
	}
	if wf.Nodes["approve"].Type != NodeHumanGate || wf.Nodes["approve"].Message != "Continue?" {
		t.Fatalf("expected approval gate, got %#v", wf.Nodes["approve"])
	}
}

func TestParseFailureRouteControls(t *testing.T) {
	wf, err := Parse(strings.NewReader(`
name: repair route
nodes:
  repair:
    type: command
    command: echo repair
  verify:
    type: command
    command: echo verify
    depends_on: [repair]
    route:
      on_failure:
        to: repair
        max_iterations: 2
        max_repeated_failures: 2
`))
	if err != nil {
		t.Fatal(err)
	}
	route := wf.Nodes["verify"].Route.OnFailure
	if route.To != "repair" || route.MaxIterations != 2 || route.MaxRepeatedFailures != 2 {
		t.Fatalf("unexpected route controls: %#v", route)
	}

	_, err = Parse(strings.NewReader(`
name: bad route
nodes:
  verify:
    type: command
    command: echo verify
    route:
      on_failure:
        to: missing
`))
	if err == nil || !strings.Contains(err.Error(), "unknown failure route target") {
		t.Fatalf("expected route validation error, got %v", err)
	}
}

func TestParseReferenceDefaultWorkflows(t *testing.T) {
	defaultsDir := referenceDefaultsDir()
	entries, err := os.ReadDir(defaultsDir)
	if err != nil {
		t.Skipf("reference defaults unavailable: %v", err)
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		count++
		path := filepath.Join(defaultsDir, entry.Name())
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		_, parseErr := Parse(f)
		closeErr := f.Close()
		if parseErr != nil {
			t.Fatalf("%s failed to parse: %v", entry.Name(), parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	if count != 20 {
		t.Fatalf("parsed %d default workflows, want 20", count)
	}
}

func TestDefaultCommandTemplatesAreVendored(t *testing.T) {
	entries, err := os.ReadDir(referenceCommandsDir())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".md" {
			count++
		}
	}
	if count != 36 {
		t.Fatalf("found %d default command templates, want 36", count)
	}
}

func TestWorkflowBuilderGuidanceIsMicromageNative(t *testing.T) {
	path := filepath.Join(referenceDefaultsDir(), "micromage-workflow-builder.yaml")
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(bytes)
	for _, want := range []string{
		"type: human_gate",
		"route.on_failure",
		"go run ./cmd/micromage validate",
		"Do not use non-Micromage fields",
		"Do not assume npm, node, bun, or TypeScript",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("workflow builder missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"runtime: bun",
		"bun run",
		"Node inspector",
		"frontend builder",
		"Arc" + "hon",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("workflow builder still contains legacy platform guidance %q", forbidden)
		}
	}
}

func TestDefaultWorkflowCommandReferencesResolveLocally(t *testing.T) {
	workflowEntries, err := os.ReadDir(referenceDefaultsDir())
	if err != nil {
		t.Fatal(err)
	}
	commandDir := referenceCommandsDir()
	for _, entry := range workflowEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(referenceDefaultsDir(), entry.Name())
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		wf, parseErr := Parse(f)
		closeErr := f.Close()
		if parseErr != nil {
			t.Fatalf("%s failed to parse: %v", entry.Name(), parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		for id, node := range wf.Nodes {
			if node.Type != NodeAgent || strings.TrimSpace(node.Command) == "" {
				continue
			}
			templatePath := filepath.Join(commandDir, node.Command+".md")
			if _, err := os.Stat(templatePath); err != nil {
				t.Fatalf("%s node %s references missing template %s", entry.Name(), id, templatePath)
			}
		}
	}
}

func referenceDefaultsDir() string {
	if dir := os.Getenv("MICROMAGE_REFERENCE_DEFAULTS"); dir != "" {
		return dir
	}
	return filepath.Join(repoRootFromCwd(), "assets", "defaults", "workflows")
}

func referenceCommandsDir() string {
	if dir := os.Getenv("MICROMAGE_DEFAULT_COMMANDS"); dir != "" {
		return dir
	}
	return filepath.Join(repoRootFromCwd(), "assets", "defaults", "commands")
}

func repoRootFromCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
