package workflow

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const validWorkflowYAML = `name: feature-flow
description: Plan, implement, and verify a feature
provider: codex
tags: ["Feature", "Feature ", "Review"]
interactive: true
worktree:
  enabled: false
nodes:
  - id: plan
    prompt: |
      Create a plan.
    model: medium
  - id: implement
    command: implement
    depends_on: [plan]
  - id: verify
    bash: go test ./...
    depends_on: [implement]
    trigger_rule: all_success
`

func TestParseYAMLAcceptsWorkflowMetadata(t *testing.T) {
	workflow, issues := ParseYAML(validWorkflowYAML)

	if HasErrors(issues) {
		t.Fatalf("expected no errors, got %#v", issues)
	}
	if workflow.Name != "feature-flow" || workflow.Provider != "codex" {
		t.Fatalf("unexpected workflow metadata: %#v", workflow)
	}
	if got := strings.Join(workflow.Tags, ","); got != "Feature,Review" {
		t.Fatalf("expected normalized tags, got %q", got)
	}
	if workflow.Interactive == nil || !*workflow.Interactive {
		t.Fatal("expected interactive true")
	}
	if workflow.Nodes[0].Kind() != "prompt" || workflow.Nodes[1].Kind() != "command" || workflow.Nodes[2].Kind() != "bash" {
		t.Fatalf("unexpected node kinds: %s %s %s", workflow.Nodes[0].Kind(), workflow.Nodes[1].Kind(), workflow.Nodes[2].Kind())
	}
}

func TestParseYAMLReportsRequiredFields(t *testing.T) {
	_, issues := ParseYAML(`description: missing name`)

	if !containsIssue(issues, "name") || !containsIssue(issues, "nodes") {
		t.Fatalf("expected missing name and nodes issues, got %#v", issues)
	}
}

func TestParseYAMLReportsEmptyAndMalformedYAML(t *testing.T) {
	_, emptyIssues := ParseYAML(" \n\t")
	if !containsIssue(emptyIssues, "yaml") || !containsMessage(emptyIssues, "cannot be empty") {
		t.Fatalf("expected empty YAML issue, got %#v", emptyIssues)
	}

	_, malformedIssues := ParseYAML("name: [unterminated")
	if !containsIssue(malformedIssues, "yaml") {
		t.Fatalf("expected malformed YAML issue, got %#v", malformedIssues)
	}
}

func TestParseYAMLValidatesNodeTypesAndTriggerRules(t *testing.T) {
	input := `name: invalid
description: invalid
nodes:
  - id: empty-prompt
    prompt: "   "
  - id: double-kind
    prompt: hi
    bash: echo hi
  - id: bad-rule
    command: build
    trigger_rule: any_success
`
	_, issues := ParseYAML(input)

	if !containsIssue(issues, "prompt") || !containsIssue(issues, "trigger_rule") {
		t.Fatalf("expected prompt and trigger rule issues, got %#v", issues)
	}
	if !containsMessage(issues, "only one executable") {
		t.Fatalf("expected double executable issue, got %#v", issues)
	}
}

func TestParseYAMLReportsMalformedTypesAndMetadataWarnings(t *testing.T) {
	input := `name: typed
description: typed
tags: not-a-list
interactive: yes
worktree: enabled
nodes:
  - id: bad-fields
    prompt: hi
    depends_on: plan
`
	_, issues := ParseYAML(input)

	if !containsIssue(issues, "tags") || !containsIssue(issues, "interactive") || !containsIssue(issues, "worktree") {
		t.Fatalf("expected metadata type warnings, got %#v", issues)
	}
	if !containsMessage(issues, "malformed fields") {
		t.Fatalf("expected malformed node field issue, got %#v", issues)
	}
}

func TestParseYAMLValidatesRetryAndExecutableKinds(t *testing.T) {
	input := `name: kinds
description: all executable kinds
nodes:
  - id: script
    script:
      prompt: Generate code
  - id: loop
    loop:
      over: files
  - id: approval
    approval:
      prompt: Continue?
  - id: cancel
    cancel: Stop workflow
  - id: empty-loop
    loop: {}
  - id: empty-approval
    approval:
  - id: bad-retry
    command: retry
    retry:
      max_attempts: 0
`
	workflow, issues := ParseYAML(input)

	if workflow.Nodes[0].Kind() != "script" || workflow.Nodes[1].Kind() != "loop" || workflow.Nodes[2].Kind() != "approval" || workflow.Nodes[3].Kind() != "cancel" {
		t.Fatalf("unexpected executable kinds: %#v", workflow.Nodes[:4])
	}
	if !containsIssue(issues, "loop") || !containsIssue(issues, "approval") || !containsIssue(issues, "retry.max_attempts") {
		t.Fatalf("expected loop, approval, and retry issues, got %#v", issues)
	}
}

func TestParseYAMLPreservesUnknownFieldsAndRuntimeWarnings(t *testing.T) {
	workflow, issues := ParseYAML(`name: runtime
description: runtime fields
provider: local
custom_root: true
nodes:
  - id: build
    command: build
    when: branch == main
    mcp: filesystem
    skills: ["review", " review "]
    hooks:
      before: echo before
    custom_node: value
`)

	if HasErrors(issues) {
		t.Fatalf("expected runtime warnings only, got %#v", issues)
	}
	if !containsIssue(issues, "runtime") {
		t.Fatalf("expected runtime warning, got %#v", issues)
	}
	if workflow.Extra["custom_root"] != true || workflow.Nodes[0].Extra["custom_node"] != "value" {
		t.Fatalf("expected unknown fields to be preserved, got %#v %#v", workflow.Extra, workflow.Nodes[0].Extra)
	}
	if got := strings.Join(workflow.Nodes[0].Skills, ","); got != "review,review" {
		t.Fatalf("expected trimmed skills, got %q", got)
	}
}

func TestValidateFindsDuplicateMissingDependencyAndCycle(t *testing.T) {
	input := `name: invalid-dag
description: invalid dag
nodes:
  - id: a
    prompt: a
    depends_on: [c]
  - id: a
    prompt: duplicate
  - id: b
    prompt: b
    depends_on: [b]
`
	_, issues := ParseYAML(input)

	if !containsMessage(issues, "duplicate node id") {
		t.Fatalf("expected duplicate issue, got %#v", issues)
	}
	if !containsMessage(issues, "dependency c was not found") {
		t.Fatalf("expected missing dependency issue, got %#v", issues)
	}
	if !containsMessage(issues, "cycle") {
		t.Fatalf("expected cycle issue, got %#v", issues)
	}
}

func TestBuildPreviewReturnsDeterministicGraph(t *testing.T) {
	preview := BuildPreview(validWorkflowYAML)

	if !preview.CanRun {
		t.Fatalf("expected preview to be runnable, got %#v", preview.Issues)
	}
	if len(preview.Graph.Nodes) != 3 || len(preview.Graph.Edges) != 2 {
		t.Fatalf("unexpected graph: %#v", preview.Graph)
	}
	if preview.Graph.Nodes[0].ID != "plan" || preview.Graph.Nodes[0].Layer != 0 {
		t.Fatalf("expected plan at layer 0, got %#v", preview.Graph.Nodes[0])
	}
	if preview.Graph.Nodes[2].ID != "verify" || preview.Graph.Nodes[2].Layer != 2 {
		t.Fatalf("expected verify at layer 2, got %#v", preview.Graph.Nodes[2])
	}
}

func TestBuildPreviewReturnsBadgesMetadataSummariesAndStablePositions(t *testing.T) {
	input := `name: view
description: view model
nodes:
  - id: first
    script:
      prompt: |
        Write code
        Then test
    model: medium
    provider: local
    when: ready
    trigger_rule: all_done
    mcp: filesystem
    skills: [review]
    hooks:
      after: echo done
  - id: second
    command: finish
    depends_on: [first]
`
	first := BuildPreview(input)
	second := BuildPreview(input)

	if !reflect.DeepEqual(first.Graph, second.Graph) {
		t.Fatalf("expected deterministic graph positions, got %#v and %#v", first.Graph, second.Graph)
	}
	if len(first.Graph.Nodes) != 2 {
		t.Fatalf("expected two nodes, got %#v", first.Graph.Nodes)
	}
	node := first.Graph.Nodes[0]
	for _, badge := range []string{"medium", "local", "when", "all_done", "mcp", "skills", "hooks"} {
		if !containsString(node.Badges, badge) {
			t.Fatalf("expected badge %q in %#v", badge, node.Badges)
		}
	}
	if node.Metadata["preview"] != "Write code" || node.Summary != "Write code" || node.X == 0 || node.Y == 0 {
		t.Fatalf("unexpected node view metadata: %#v", node)
	}
}

func TestTopologicalLayersGroupsParallelNodes(t *testing.T) {
	workflow, issues := ParseYAML(`name: parallel
description: parallel branches
nodes:
  - id: start
    prompt: start
  - id: review-a
    prompt: a
    depends_on: [start]
  - id: review-b
    prompt: b
    depends_on: [start]
  - id: merge
    prompt: merge
    depends_on: [review-a, review-b]
`)
	if HasErrors(issues) {
		t.Fatalf("expected valid workflow, got %#v", issues)
	}

	layers := TopologicalLayers(workflow.Nodes)
	if layers["start"] != 0 || layers["review-a"] != 1 || layers["review-b"] != 1 || layers["merge"] != 2 {
		t.Fatalf("unexpected layers: %#v", layers)
	}
}

func TestExecuteEmitsLayeredFakeRunEvents(t *testing.T) {
	workflow, issues := ParseYAML(validWorkflowYAML)
	if HasErrors(issues) {
		t.Fatalf("expected valid workflow, got %#v", issues)
	}

	var events []RunEvent
	err := Execute(context.Background(), workflow, LoggingRunner{}, func(event RunEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if len(events) == 0 || events[0].Type != "workflow_start" || events[len(events)-1].Type != "workflow_complete" {
		t.Fatalf("unexpected event boundaries: %#v", events)
	}
	if countEvents(events, "layer_start") != 3 || countEvents(events, "node_log") != 3 {
		t.Fatalf("expected three layer starts and logs, got %#v", events)
	}
	wantPrefix := []string{"workflow_start", "layer_start", "node_start", "node_log", "node_complete", "layer_complete"}
	for i, eventType := range wantPrefix {
		if events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, events)
		}
	}
}

func TestExecutePropagatesRunnerFailuresAndCanceledContext(t *testing.T) {
	workflow, issues := ParseYAML(`name: failures
description: failures
nodes:
  - id: fail
    command: fail
`)
	if HasErrors(issues) {
		t.Fatalf("expected valid workflow, got %#v", issues)
	}

	expected := errors.New("runner failed")
	if err := Execute(context.Background(), workflow, failingRunner{err: expected}, func(RunEvent) error { return nil }); !errors.Is(err, expected) {
		t.Fatalf("expected runner error, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Execute(ctx, workflow, LoggingRunner{}, func(RunEvent) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}
}

func TestLoadTemplatesReturnsSortedMetadataAndReadErrors(t *testing.T) {
	source := fstest.MapFS{
		"workflows/z-last.yaml":  {Data: []byte("name: Z Last\ndescription: Last\nnodes:\n  - id: z\n    command: z\n")},
		"workflows/a-first.yaml": {Data: []byte("name: A First\ndescription: First\nnodes:\n  - id: a\n    command: a\n")},
	}

	templates, err := LoadTemplates(source, "workflows/*.yaml")
	if err != nil {
		t.Fatalf("LoadTemplates returned error: %v", err)
	}
	if len(templates) != 2 || templates[0].ID != "a-first" || templates[1].ID != "z-last" {
		t.Fatalf("expected sorted template IDs, got %#v", templates)
	}
	if templates[0].Name != "A First" || templates[0].Description != "First" || templates[0].YAML == "" {
		t.Fatalf("expected populated metadata, got %#v", templates[0])
	}

	if _, err := LoadTemplates(source, "["); err == nil {
		t.Fatal("expected invalid glob error")
	}

	broken := fstest.MapFS{"workflows/broken.yaml": {Mode: 0o755 | fs.ModeDir}}
	if _, err := LoadTemplates(broken, "workflows/*.yaml"); err == nil {
		t.Fatal("expected read error for directory template")
	}
}

func containsIssue(issues []Issue, field string) bool {
	for _, issue := range issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}

func containsMessage(issues []Issue, message string) bool {
	for _, issue := range issues {
		if strings.Contains(issue.Message, message) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countEvents(events []RunEvent, eventType string) int {
	var count int
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

type failingRunner struct {
	err error
}

func (runner failingRunner) RunNode(context.Context, Node, EventSink) error {
	return runner.err
}
