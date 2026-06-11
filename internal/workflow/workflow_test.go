package workflow

import (
	"context"
	"strings"
	"testing"
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

func countEvents(events []RunEvent, eventType string) int {
	var count int
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
