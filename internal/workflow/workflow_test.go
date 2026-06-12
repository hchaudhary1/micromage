package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
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
    context: fresh
    agent: general
    idle_timeout: 60000
    allowed_tools: []
    outputs:
      - $ARTIFACTS_DIR/plan.md
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
	if workflow.Nodes[0].Context != "fresh" || workflow.Nodes[0].Agent != "general" || workflow.Nodes[0].IdleTimeout == nil || len(workflow.Nodes[0].AllowedTools) != 0 {
		t.Fatalf("expected opencode runtime metadata, got %#v", workflow.Nodes[0])
	}
	if got := strings.Join(workflow.Nodes[0].Outputs, ","); got != "$ARTIFACTS_DIR/plan.md" {
		t.Fatalf("expected output metadata, got %#v", workflow.Nodes[0].Outputs)
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
    context: fresh
    agent: general
    idle_timeout: 60000
    allowed_tools: []
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
	for _, badge := range []string{"medium", "local", "fresh", "general", "when", "all_done", "mcp", "skills", "hooks"} {
		if !containsString(node.Badges, badge) {
			t.Fatalf("expected badge %q in %#v", badge, node.Badges)
		}
	}
	if node.Metadata["preview"] != "Write code" || node.Metadata["context"] != "fresh" || node.Metadata["agent"] != "general" || node.Summary != "Write code" || node.X == 0 || node.Y == 0 {
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

func TestExecuteAllowsOneSuccessJoinAfterParallelFailure(t *testing.T) {
	workflow, issues := ParseYAML(`name: review
description: review branches
nodes:
  - id: sync
    prompt: sync
  - id: code-review
    prompt: code
    depends_on: [sync]
  - id: docs-impact
    prompt: docs
    depends_on: [sync]
  - id: synthesize
    prompt: synthesize
    depends_on: [code-review, docs-impact]
    trigger_rule: one_success
`)
	if HasErrors(issues) {
		t.Fatalf("expected valid workflow, got %#v", issues)
	}

	var events []RunEvent
	err := Execute(context.Background(), workflow, selectiveFailRunner{failID: "docs-impact"}, func(event RunEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("expected tolerated branch failure, got %v", err)
	}
	if !hasNodeEvent(events, "node_complete", "synthesize") {
		t.Fatalf("expected synthesize to run after one successful review, got %#v", events)
	}
	if !hasNodeEvent(events, "node_failed", "docs-impact") {
		t.Fatalf("expected failed review event, got %#v", events)
	}
}

func TestExecuteFailsOneSuccessJoinWithAllDependencyFailures(t *testing.T) {
	workflow, issues := ParseYAML(`name: review
description: review branches
nodes:
  - id: code-review
    prompt: code
  - id: docs-impact
    prompt: docs
  - id: synthesize
    prompt: synthesize
    depends_on: [code-review, docs-impact]
    trigger_rule: one_success
`)
	if HasErrors(issues) {
		t.Fatalf("expected valid workflow, got %#v", issues)
	}

	var events []RunEvent
	err := Execute(context.Background(), workflow, selectedFailuresRunner{
		errs: map[string]error{
			"code-review": errors.New("code reviewer unavailable"),
			"docs-impact": errors.New("docs reviewer unavailable"),
		},
	}, func(event RunEvent) error {
		events = append(events, event)
		return nil
	})

	if err == nil {
		t.Fatal("expected all-failed one_success join error")
	}
	for _, want := range []string{"synthesize", "code-review: code reviewer unavailable", "docs-impact: docs reviewer unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
	if !hasNodeEventWithMessage(events, "node_skipped", "synthesize", "no successful dependencies") {
		t.Fatalf("expected actionable synthesize skip event, got %#v", events)
	}
	if hasNodeEvent(events, "workflow_complete", "") {
		t.Fatalf("did not expect workflow_complete after all reviewers failed: %#v", events)
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

func TestLoadCommandsReturnsSortedPromptMetadata(t *testing.T) {
	source := fstest.MapFS{
		"commands/z-last.md":  {Data: []byte("---\ndescription: Last command\nargument-hint: <last>\n---\n# Last\nBody")},
		"commands/a-first.md": {Data: []byte("---\ndescription: First command\n---\n# First\nBody")},
	}

	commands, err := LoadCommands(source, "commands/*.md")
	if err != nil {
		t.Fatalf("LoadCommands returned error: %v", err)
	}
	if len(commands) != 2 || commands[0].ID != "a-first" || commands[1].ID != "z-last" {
		t.Fatalf("expected sorted command IDs, got %#v", commands)
	}
	if commands[0].Description != "First command" || !strings.Contains(commands[0].Body, "# First") {
		t.Fatalf("expected parsed command metadata and body, got %#v", commands[0])
	}
	if commands[1].ArgumentHint != "<last>" {
		t.Fatalf("expected argument hint, got %#v", commands[1])
	}
}

func TestBuildPreviewWithCommandsReportsMissingCommand(t *testing.T) {
	registry := NewCommandRegistry([]Command{{ID: "known", Body: "Known command"}})
	preview := BuildPreviewWithCommands(`name: commands
description: commands
nodes:
  - id: known
    command: known
  - id: missing
    command: missing
`, registry)

	if preview.CanRun || !containsMessage(preview.Issues, "command missing was not found") {
		t.Fatalf("expected missing command issue, got %#v", preview)
	}
}

func TestOpenCodeProviderBuildsExpectedCommand(t *testing.T) {
	args := BuildOpenCodeArgs("opencode/nemotron-3-ultra-free", "/repo", "hello", false)
	want := []string{"run", "--model", "opencode/nemotron-3-ultra-free", "--format", "json", "--dir", "/repo", "hello"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected opencode args: %#v", args)
	}
	unsafeArgs := BuildOpenCodeArgs("opencode/nemotron-3-ultra-free", "/repo", "hello", true)
	if !containsString(unsafeArgs, "--dangerously-skip-permissions") {
		t.Fatalf("expected unsafe flag when explicitly enabled, got %#v", unsafeArgs)
	}
}

func TestRealRunnerRunsBashWithArgumentsArtifactsAndOutputs(t *testing.T) {
	dir := t.TempDir()
	runner := NewRealRunner(RealRunnerConfig{
		CWD:          dir,
		Arguments:    "feature input",
		WorkflowID:   "run-1",
		ArtifactsDir: dir,
		BaseBranch:   "main",
	})
	runner.outputs["plan"] = "ready"

	var logs []string
	err := runner.RunNode(context.Background(), Node{ID: "verify", Bash: `printf "%s|%s|%s|%s" "$ARGUMENTS" "$WORKFLOW_ID" "$BASE_BRANCH" "$plan.output"`}, func(event RunEvent) error {
		if event.Type == "node_log" {
			logs = append(logs, event.Message)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunNode returned error: %v", err)
	}
	if got := strings.Join(logs, "\n"); got != "feature input|run-1|main|ready" {
		t.Fatalf("unexpected bash output: %q", got)
	}
}

func TestRealRunnerExpandsOutputsInPromptNodes(t *testing.T) {
	provider := &captureProvider{}
	runner := NewRealRunner(RealRunnerConfig{
		Arguments:       "review HEAD",
		WorkflowID:      "run-2",
		ArtifactsDir:    "/tmp/micromage-review",
		DefaultProvider: "capture",
		Providers:       ProviderRegistry{"capture": provider},
	})
	runner.outputs["collect-context"] = `{"context_path":"/tmp/micromage-review/context.md","summary":"last commit"}`

	err := runner.RunNode(context.Background(), Node{
		ID:     "code-review",
		Prompt: "Review $collect-context.output.context_path for $collect-context.output.summary using $ARGUMENTS in $ARTIFACTS_DIR.",
	}, func(RunEvent) error { return nil })
	if err != nil {
		t.Fatalf("RunNode returned error: %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected one provider request, got %#v", provider.requests)
	}
	want := "Review /tmp/micromage-review/context.md for last commit using review HEAD in /tmp/micromage-review."
	if provider.requests[0].Prompt != want {
		t.Fatalf("unexpected prompt:\nwant %q\n got %q", want, provider.requests[0].Prompt)
	}
	if runner.outputs["code-review"] != "captured" {
		t.Fatalf("expected prompt output to be captured, got %#v", runner.outputs)
	}
}

func TestRealRunnerSubstitutesJSONOutputsInBashAndPrompt(t *testing.T) {
	runner := NewRealRunner(RealRunnerConfig{ArtifactsDir: t.TempDir()})
	runner.outputs["collect-context"] = `{"context_path":"/tmp/context with spaces.md","summary":"last commit"}`

	prompt := runner.substitutePromptOutputs("Review $collect-context.output.context_path for $collect-context.output.summary.")
	if prompt != "Review /tmp/context with spaces.md for last commit." {
		t.Fatalf("unexpected prompt substitution: %q", prompt)
	}

	script := runner.substituteOutputs(`printf "%s" "$collect-context.output.context_path"`)
	if script != `printf "%s" "/tmp/context with spaces.md"` {
		t.Fatalf("unexpected bash substitution: %q", script)
	}
}

func TestRealRunnerSubstitutesRawOutputWhenJSONIsMalformed(t *testing.T) {
	runner := NewRealRunner(RealRunnerConfig{ArtifactsDir: t.TempDir()})
	runner.outputs["collect-context"] = `{"context_path":`

	got := runner.substitutePromptOutputs("Raw=$collect-context.output Missing=$collect-context.output.context_path")
	want := `Raw={"context_path": Missing=$collect-context.output.context_path`
	if got != want {
		t.Fatalf("unexpected malformed JSON substitution:\nwant %q\n got %q", want, got)
	}
}

func TestDefaultArtifactsDirLivesInsideRepo(t *testing.T) {
	dir := t.TempDir()
	got := DefaultArtifactsDir(dir, "run-3")
	want := filepath.Join(dir, ".micromage", "runs", "run-3")
	if got != want {
		t.Fatalf("unexpected artifact dir:\nwant %q\n got %q", want, got)
	}
}

func TestRealRunnerMaterializesDeclaredOutputFromProviderResponse(t *testing.T) {
	provider := &captureProvider{}
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, ".micromage", "runs", "run-4", "review", "code-review-findings.md")
	runner := NewRealRunner(RealRunnerConfig{
		CWD:             dir,
		ArtifactsDir:    filepath.Join(dir, ".micromage", "runs", "run-4"),
		DefaultProvider: "capture",
		Providers:       ProviderRegistry{"capture": provider},
	})

	err := runner.RunNode(context.Background(), Node{
		ID:      "code-review",
		Prompt:  "write the finding",
		Outputs: []string{"$ARTIFACTS_DIR/review/code-review-findings.md"},
	}, func(RunEvent) error { return nil })

	if err != nil {
		t.Fatalf("RunNode returned error: %v", err)
	}
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("expected materialized artifact: %v", err)
	}
	if strings.TrimSpace(string(data)) != "captured" || runner.outputs["code-review"] != "captured" {
		t.Fatalf("expected provider output artifact, got file=%q outputs=%#v", string(data), runner.outputs)
	}
}

func TestRealWorkflowExecutionFeedsDeclaredArtifactsToDownstreamNodes(t *testing.T) {
	dir := t.TempDir()
	artifactsDir := filepath.Join(dir, ".micromage", "runs", "run-e2e")
	reviewArtifact := filepath.Join(artifactsDir, "review", "code-review-findings.md")
	synthesisArtifact := filepath.Join(artifactsDir, "review", "consolidated-review.md")
	provider := &captureProvider{writeFiles: map[string]string{
		reviewArtifact: "artifact-backed finding",
	}}
	parsed, issues := ParseYAML(`name: artifact-e2e
description: artifact e2e
provider: capture
nodes:
  - id: code-review
    prompt: review
    outputs:
      - $ARTIFACTS_DIR/review/code-review-findings.md
  - id: synthesize
    prompt: "Summarize: $code-review.output"
    depends_on: [code-review]
    outputs:
      - $ARTIFACTS_DIR/review/consolidated-review.md
`)
	if HasErrors(issues) {
		t.Fatalf("expected valid workflow, got %#v", issues)
	}
	runner := NewRealRunner(RealRunnerConfig{
		CWD:             dir,
		ArtifactsDir:    artifactsDir,
		DefaultProvider: "capture",
		Providers:       ProviderRegistry{"capture": provider},
	})

	// Real workflow runs must pass artifact-backed review output into synthesis prompts.
	err := Execute(context.Background(), parsed, runner, func(RunEvent) error { return nil })

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected two provider calls, got %#v", provider.requests)
	}
	if !strings.Contains(provider.requests[1].Prompt, "artifact-backed finding") {
		t.Fatalf("expected downstream prompt to receive artifact output, got %q", provider.requests[1].Prompt)
	}
	reviewData, err := os.ReadFile(reviewArtifact)
	if err != nil {
		t.Fatalf("expected review artifact: %v", err)
	}
	if strings.TrimSpace(string(reviewData)) != "artifact-backed finding" {
		t.Fatalf("unexpected review artifact content: %q", string(reviewData))
	}
	synthesisData, err := os.ReadFile(synthesisArtifact)
	if err != nil {
		t.Fatalf("expected synthesis artifact: %v", err)
	}
	if strings.TrimSpace(string(synthesisData)) != "captured" {
		t.Fatalf("unexpected synthesis artifact content: %q", string(synthesisData))
	}
}

func TestRealRunnerDoesNotPublishPartialProviderOutputAfterError(t *testing.T) {
	provider := &captureProvider{output: "partial finding", err: errors.New("provider stream failed")}
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, ".micromage", "runs", "run-partial", "review", "code-review-findings.md")
	runner := NewRealRunner(RealRunnerConfig{
		CWD:             dir,
		ArtifactsDir:    filepath.Join(dir, ".micromage", "runs", "run-partial"),
		DefaultProvider: "capture",
		Providers:       ProviderRegistry{"capture": provider},
	})

	err := runner.RunNode(context.Background(), Node{
		ID:      "code-review",
		Prompt:  "write the finding",
		Outputs: []string{"$ARTIFACTS_DIR/review/code-review-findings.md"},
	}, func(RunEvent) error { return nil })

	if err == nil || !strings.Contains(err.Error(), "provider stream failed") {
		t.Fatalf("expected provider error, got %v", err)
	}
	if _, ok := runner.outputs["code-review"]; ok {
		t.Fatalf("partial provider output should not be published, got %#v", runner.outputs)
	}
	if _, err := os.Stat(artifactPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial provider output should not materialize artifact, stat err=%v", err)
	}
}

func TestRealRunnerCapturesDeclaredOutput(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, ".micromage", "runs", "run-5", "review", "code-review-findings.md")
	provider := &captureProvider{writeFiles: map[string]string{artifactPath: "No findings."}}
	runner := NewRealRunner(RealRunnerConfig{
		CWD:             dir,
		ArtifactsDir:    filepath.Join(dir, ".micromage", "runs", "run-5"),
		DefaultProvider: "capture",
		Providers:       ProviderRegistry{"capture": provider},
	})

	err := runner.RunNode(context.Background(), Node{
		ID:      "code-review",
		Prompt:  "write the finding",
		Outputs: []string{"$ARTIFACTS_DIR/review/code-review-findings.md"},
	}, func(RunEvent) error { return nil })

	if err != nil {
		t.Fatalf("RunNode returned error: %v", err)
	}
	if runner.outputs["code-review"] != "No findings." {
		t.Fatalf("expected artifact content as node output, got %#v", runner.outputs)
	}
}

func TestRealRunnerFailsWhenMultipleDeclaredOutputsAreMissing(t *testing.T) {
	provider := &captureProvider{}
	dir := t.TempDir()
	runner := NewRealRunner(RealRunnerConfig{
		CWD:             dir,
		ArtifactsDir:    filepath.Join(dir, ".micromage", "runs", "run-6"),
		DefaultProvider: "capture",
		Providers:       ProviderRegistry{"capture": provider},
	})

	err := runner.RunNode(context.Background(), Node{
		ID:      "code-review",
		Prompt:  "write findings",
		Outputs: []string{"$ARTIFACTS_DIR/a.md", "$ARTIFACTS_DIR/b.md"},
	}, func(RunEvent) error { return nil })

	if err == nil || !strings.Contains(err.Error(), "expected output was not written") {
		t.Fatalf("expected missing output error, got %v", err)
	}
}

func TestRealRunnerHonorsIdleTimeout(t *testing.T) {
	provider := blockingProvider{}
	runner := NewRealRunner(RealRunnerConfig{
		DefaultProvider: "blocking",
		Providers:       ProviderRegistry{"blocking": provider},
	})
	timeout := 25

	start := time.Now()
	err := runner.RunNode(context.Background(), Node{
		ID:          "slow-review",
		Prompt:      "review",
		IdleTimeout: &timeout,
	}, func(RunEvent) error { return nil })

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("expected timeout to interrupt provider promptly, took %s", elapsed)
	}
}

func TestOpenCodeProviderFailsPermissionAutoReject(t *testing.T) {
	command := writeExecutable(t, `#!/bin/sh
printf '%s\n' '{"type":"text","part":{"type":"text","text":"! permission requested: external_directory (/tmp/run/*); auto-rejecting"}}'
`)

	_, err := (OpenCodeProvider{Command: command}).RunPrompt(context.Background(), PromptRequest{
		Prompt: "review",
		CWD:    t.TempDir(),
		Model:  DefaultOpenCodeModel,
		Node:   Node{ID: "review"},
	}, func(RunEvent) error { return nil })

	if err == nil || !strings.Contains(err.Error(), "permission auto-rejected") {
		t.Fatalf("expected permission rejection error, got %v", err)
	}
}

func TestOpenCodeProviderFailsEmptyOutput(t *testing.T) {
	command := writeExecutable(t, `#!/bin/sh
exit 0
`)

	_, err := (OpenCodeProvider{Command: command}).RunPrompt(context.Background(), PromptRequest{
		Prompt: "review",
		CWD:    t.TempDir(),
		Model:  DefaultOpenCodeModel,
		Node:   Node{ID: "review"},
	}, func(RunEvent) error { return nil })

	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("expected empty output error, got %v", err)
	}
}

func TestRealRunnerDoesNotPublishOpenCodeOutputAfterEmitError(t *testing.T) {
	command := writeExecutable(t, `#!/bin/sh
printf '%s\n' '{"type":"text","part":{"type":"text","text":"partial finding"}}'
sleep 1
`)
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, ".micromage", "runs", "run-emit-error", "review", "code-review-findings.md")
	runner := NewRealRunner(RealRunnerConfig{
		CWD:          dir,
		ArtifactsDir: filepath.Join(dir, ".micromage", "runs", "run-emit-error"),
		Providers:    ProviderRegistry{"opencode": OpenCodeProvider{Command: command}},
	})

	err := runner.RunNode(context.Background(), Node{
		ID:      "code-review",
		Prompt:  "write the finding",
		Outputs: []string{"$ARTIFACTS_DIR/review/code-review-findings.md"},
	}, func(event RunEvent) error {
		if event.Type == "node_log" {
			return errors.New("emit failed")
		}
		return nil
	})

	if err == nil || !strings.Contains(err.Error(), "emit failed") {
		t.Fatalf("expected emit error, got %v", err)
	}
	if _, ok := runner.outputs["code-review"]; ok {
		t.Fatalf("partial opencode output should not be published, got %#v", runner.outputs)
	}
	if _, err := os.Stat(artifactPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial opencode output should not materialize artifact, stat err=%v", err)
	}
}

func TestOpenCodeProviderSerializesConcurrentRuns(t *testing.T) {
	lockDir := filepath.Join(t.TempDir(), "opencode-lock")
	command := writeExecutable(t, `#!/bin/sh
lock_dir="$MICROMAGE_TEST_LOCK_DIR"
if ! mkdir "$lock_dir" 2>/dev/null; then
  echo "database is locked" >&2
  exit 1
fi
trap 'rmdir "$lock_dir"' EXIT
sleep 0.1
printf '%s\n' '{"type":"text","part":{"type":"text","text":"ok"}}'
`)
	t.Setenv("MICROMAGE_TEST_LOCK_DIR", lockDir)

	provider := OpenCodeProvider{Command: command}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := provider.RunPrompt(context.Background(), PromptRequest{
				Prompt: "review",
				CWD:    t.TempDir(),
				Model:  DefaultOpenCodeModel,
				Node:   Node{ID: "review"},
			}, func(RunEvent) error { return nil })
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("expected serialized provider calls to succeed, got %v", err)
		}
	}
}

func TestScanOpenCodeAcceptsLongJSONLines(t *testing.T) {
	longText := strings.Repeat("review finding ", 7000)
	line := `{"type":"text","part":{"type":"text","text":` + quoteJSON(longText) + `}}`
	var output strings.Builder
	errs := make(chan error, 1)

	scanOpenCode(strings.NewReader(line), "review", func(RunEvent) error { return nil }, &output, errs)

	if err := <-errs; err != nil {
		t.Fatalf("scanOpenCode returned error: %v", err)
	}
	if output.String() != longText {
		t.Fatalf("expected long text output, got length %d", output.Len())
	}
}

func TestExtractOpenCodeTextIgnoresToolOutput(t *testing.T) {
	line := `{"type":"tool_use","part":{"type":"tool","state":{"output":"file contents"},"metadata":{"display":{"text":"preview text"}}}}`
	if got := extractOpenCodeText(line); got != "" {
		t.Fatalf("expected tool output to be ignored, got %q", got)
	}
}

func TestExtractOpenCodeTextAcceptsAssistantTextEvent(t *testing.T) {
	line := `{"type":"text","part":{"type":"text","text":"final review"}}`
	if got := extractOpenCodeText(line); got != "final review" {
		t.Fatalf("expected assistant text, got %q", got)
	}
}

func TestRealRunnerFailsUnsupportedRealNodeKinds(t *testing.T) {
	runner := NewRealRunner(RealRunnerConfig{})
	err := runner.RunNode(context.Background(), Node{ID: "approve", Approval: map[string]any{"prompt": "continue?"}}, func(RunEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unsupported real node kind") {
		t.Fatalf("expected unsupported node error, got %v", err)
	}
}

func TestOpenCodeProviderSmokeOptIn(t *testing.T) {
	if os.Getenv("MICROMAGE_OPENCODE_E2E") != "1" {
		t.Skip("set MICROMAGE_OPENCODE_E2E=1 to run the local OpenCode smoke test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode unavailable: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	var logs []string
	output, err := (OpenCodeProvider{}).RunPrompt(context.Background(), PromptRequest{
		Prompt: "Reply with exactly MICROMAGE_OPENCODE_OK and do not edit files.",
		// Smoke tests use the package repo so OpenCode has the same project context as real runs.
		CWD:   cwd,
		Model: DefaultOpenCodeModel,
		Node:  Node{ID: "opencode-smoke", Prompt: "smoke"},
	}, func(event RunEvent) error {
		if event.Type == "node_log" {
			logs = append(logs, event.Message)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("OpenCode smoke failed: %v; logs=%q", err, strings.Join(logs, "\n"))
	}
	if !strings.Contains(output, "MICROMAGE_OPENCODE_OK") {
		t.Fatalf("expected smoke marker in output %q; logs=%q", output, strings.Join(logs, "\n"))
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

type captureProvider struct {
	requests   []PromptRequest
	writeFiles map[string]string
	output     string
	err        error
}

func (provider *captureProvider) RunPrompt(_ context.Context, request PromptRequest, _ EventSink) (string, error) {
	provider.requests = append(provider.requests, request)
	for path, content := range provider.writeFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	if provider.output != "" || provider.err != nil {
		return provider.output, provider.err
	}
	return "captured", nil
}

type blockingProvider struct{}

func (blockingProvider) RunPrompt(ctx context.Context, _ PromptRequest, _ EventSink) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func writeExecutable(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-opencode")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func hasNodeEvent(events []RunEvent, eventType string, nodeID string) bool {
	for _, event := range events {
		if event.Type == eventType && event.NodeID == nodeID {
			return true
		}
	}
	return false
}

func hasNodeEventWithMessage(events []RunEvent, eventType string, nodeID string, message string) bool {
	for _, event := range events {
		if event.Type == eventType && event.NodeID == nodeID && strings.Contains(event.Message, message) {
			return true
		}
	}
	return false
}

type failingRunner struct {
	err error
}

func (runner failingRunner) RunNode(context.Context, Node, EventSink) error {
	return runner.err
}

type selectiveFailRunner struct {
	failID string
}

func (runner selectiveFailRunner) RunNode(_ context.Context, node Node, emit EventSink) error {
	if node.ID == runner.failID {
		return errors.New("selected failure")
	}
	return emit(RunEvent{Type: "node_log", NodeID: node.ID, Message: "ok"})
}

type selectedFailuresRunner struct {
	errs map[string]error
}

func (runner selectedFailuresRunner) RunNode(_ context.Context, node Node, emit EventSink) error {
	if err := runner.errs[node.ID]; err != nil {
		return err
	}
	return emit(RunEvent{Type: "node_log", NodeID: node.ID, Message: "ok"})
}
