package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"micromage/internal/workflow"
)

//go:embed testdata/web/templates/*.html testdata/web/static/* testdata/web/workflows/*.yaml testdata/web/commands/*.md
var testAssets embed.FS

func TestShellRendersWorkflowApp(t *testing.T) {
	server := newTestServer(t)

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Micromage Workflows") || !strings.Contains(body, "yaml-editor") {
		t.Fatalf("expected workflow shell in response, got %q", body)
	}
}

func TestTemplatesEndpointReturnsEmbeddedTemplates(t *testing.T) {
	server := newTestServer(t)

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/templates", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var templates []workflow.Template
	if err := json.NewDecoder(response.Body).Decode(&templates); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	if len(templates) != 5 {
		t.Fatalf("expected five templates, got %#v", templates)
	}
	if templates[0].YAML == "" || templates[0].Name == "" {
		t.Fatalf("expected populated template metadata, got %#v", templates[0])
	}
	if !hasTemplate(templates, "idea-to-pr") {
		t.Fatalf("expected idea-to-pr template, got %#v", templates)
	}
	if !hasTemplate(templates, "review-last-commit") {
		t.Fatalf("expected review-last-commit template, got %#v", templates)
	}
}

func TestIdeaToPRTemplatePreviewsWithParallelReviewNodes(t *testing.T) {
	server := newTestServer(t)
	templatesResponse := httptest.NewRecorder()
	server.ServeHTTP(templatesResponse, httptest.NewRequest(http.MethodGet, "/api/templates", nil))
	var templates []workflow.Template
	if err := json.NewDecoder(templatesResponse.Body).Decode(&templates); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	var ideaTemplate workflow.Template
	for _, template := range templates {
		if template.ID == "idea-to-pr" {
			ideaTemplate = template
			break
		}
	}
	if ideaTemplate.YAML == "" {
		t.Fatal("idea-to-pr template not found")
	}

	response := postJSON(server, "/api/preview", `{"yaml": `+strconvQuote(ideaTemplate.YAML)+`}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var preview workflow.Preview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if !preview.CanRun || len(preview.Graph.Nodes) != 17 {
		t.Fatalf("expected runnable idea-to-pr graph, got %#v", preview)
	}
	reviewLayer := graphNodeLayer(preview.Graph.Nodes, "code-review")
	if reviewLayer < 0 {
		t.Fatalf("expected code-review node, got %#v", preview.Graph.Nodes)
	}
	for _, id := range []string{"error-handling", "test-coverage", "comment-quality", "docs-impact"} {
		if graphNodeLayer(preview.Graph.Nodes, id) != reviewLayer {
			t.Fatalf("expected parallel review node %s in review layer, got %#v", id, preview.Graph.Nodes)
		}
	}
}

func TestReviewLastCommitTemplatePreviewsWithParallelReviewNodes(t *testing.T) {
	server := newTestServer(t)
	templatesResponse := httptest.NewRecorder()
	server.ServeHTTP(templatesResponse, httptest.NewRequest(http.MethodGet, "/api/templates", nil))
	var templates []workflow.Template
	if err := json.NewDecoder(templatesResponse.Body).Decode(&templates); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	var reviewTemplate workflow.Template
	for _, template := range templates {
		if template.ID == "review-last-commit" {
			reviewTemplate = template
			break
		}
	}
	if reviewTemplate.YAML == "" {
		t.Fatal("review-last-commit template not found")
	}
	parsed, issues := workflow.ParseYAML(reviewTemplate.YAML)
	if workflow.HasErrors(issues) {
		t.Fatalf("expected valid review-last-commit template, got %#v", issues)
	}
	declaredOutputs := map[string]string{
		"code-review":     "$ARTIFACTS_DIR/review-last-commit/code-review-findings.md",
		"error-handling":  "$ARTIFACTS_DIR/review-last-commit/error-handling-findings.md",
		"test-coverage":   "$ARTIFACTS_DIR/review-last-commit/test-coverage-findings.md",
		"comment-quality": "$ARTIFACTS_DIR/review-last-commit/comment-quality-findings.md",
		"docs-impact":     "$ARTIFACTS_DIR/review-last-commit/docs-impact-findings.md",
		"synthesize":      "$ARTIFACTS_DIR/review-last-commit/consolidated-review.md",
	}
	for _, node := range parsed.Nodes {
		if want, ok := declaredOutputs[node.ID]; ok {
			if len(node.Outputs) != 1 || node.Outputs[0] != want {
				t.Fatalf("expected %s to declare %q, got %#v", node.ID, want, node.Outputs)
			}
			delete(declaredOutputs, node.ID)
		}
		if node.ID == "collect-context" && !strings.Contains(node.Bash, "git rev-parse --verify HEAD") {
			t.Fatalf("expected collect-context to handle missing HEAD, got %q", node.Bash)
		}
	}
	if len(declaredOutputs) > 0 {
		t.Fatalf("missing declared outputs for nodes: %#v", declaredOutputs)
	}

	response := postJSON(server, "/api/preview", `{"yaml": `+strconvQuote(reviewTemplate.YAML)+`}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var preview workflow.Preview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if !preview.CanRun || len(preview.Graph.Nodes) != 7 {
		t.Fatalf("expected runnable review-last-commit graph, got %#v", preview)
	}
	reviewLayer := graphNodeLayer(preview.Graph.Nodes, "code-review")
	if reviewLayer < 0 {
		t.Fatalf("expected code-review node, got %#v", preview.Graph.Nodes)
	}
	for _, id := range []string{"error-handling", "test-coverage", "comment-quality", "docs-impact"} {
		if graphNodeLayer(preview.Graph.Nodes, id) != reviewLayer {
			t.Fatalf("expected parallel review node %s in review layer, got %#v", id, preview.Graph.Nodes)
		}
	}
	if graphNodeLayer(preview.Graph.Nodes, "synthesize") <= reviewLayer {
		t.Fatalf("expected synthesize after review layer, got %#v", preview.Graph.Nodes)
	}
}

func TestPreviewEndpointReturnsGraphViewModel(t *testing.T) {
	server := newTestServer(t)

	response := postJSON(server, "/api/preview", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var preview workflow.Preview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if !preview.CanRun || len(preview.Graph.Nodes) != 1 || preview.Graph.Nodes[0].ID != "plan" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
}

func TestPreviewEndpointKeepsInvalidGraphButDisablesRun(t *testing.T) {
	server := newTestServer(t)

	response := postJSON(server, "/api/preview", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n    depends_on: [missing]\n"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var preview workflow.Preview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.CanRun || len(preview.Graph.Nodes) != 1 {
		t.Fatalf("expected invalid best-effort graph, got %#v", preview)
	}
}

func TestPreviewEndpointReportsEmptyAndMalformedPayloads(t *testing.T) {
	server := newTestServer(t)

	invalidJSON := postRaw(server, "/api/preview", `{`)
	if invalidJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid JSON to return 400, got %d", invalidJSON.Code)
	}

	emptyYAML := postJSON(server, "/api/preview", `{"yaml": "   "}`)
	if emptyYAML.Code != http.StatusOK {
		t.Fatalf("expected empty YAML preview to return 200, got %d", emptyYAML.Code)
	}
	var emptyPreview workflow.Preview
	if err := json.NewDecoder(emptyYAML.Body).Decode(&emptyPreview); err != nil {
		t.Fatalf("decode empty preview: %v", err)
	}
	if emptyPreview.CanRun || !previewContainsIssue(emptyPreview, "yaml") {
		t.Fatalf("expected empty YAML issue, got %#v", emptyPreview)
	}

	malformedYAML := postJSON(server, "/api/preview", `{"yaml": "name: [unterminated"}`)
	if malformedYAML.Code != http.StatusOK {
		t.Fatalf("expected malformed YAML preview to return 200, got %d", malformedYAML.Code)
	}
	var malformedPreview workflow.Preview
	if err := json.NewDecoder(malformedYAML.Body).Decode(&malformedPreview); err != nil {
		t.Fatalf("decode malformed preview: %v", err)
	}
	if malformedPreview.CanRun || !previewContainsIssue(malformedPreview, "yaml") || len(malformedPreview.Graph.Nodes) != 0 {
		t.Fatalf("expected syntax issue without graph nodes, got %#v", malformedPreview)
	}
}

func TestPreviewEndpointRejectsOversizedRequestBodies(t *testing.T) {
	server := newTestServer(t)
	body := `{"yaml": "` + strings.Repeat("x", int(maxRequestBodyBytes)) + `"}`

	response := postJSON(server, "/api/preview", body)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d with %q", response.Code, response.Body.String())
	}
}

func TestRunEndpointStreamsFakeEvents(t *testing.T) {
	server := newTestServer(t)

	response := postJSON(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n", "mode": "simulate", "arguments": "feature input"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected event stream, got %q", contentType)
	}
	body := response.Body.String()
	if !strings.Contains(body, "workflow_start") || !strings.Contains(body, "would run prompt node plan") {
		t.Fatalf("expected fake run events, got %q", body)
	}
	for _, frame := range []string{
		"event: workflow_start\ndata:",
		"event: layer_start\ndata:",
		"event: node_start\ndata:",
		"event: node_log\ndata:",
		"event: node_complete\ndata:",
		"event: layer_complete\ndata:",
		"event: workflow_complete\ndata:",
	} {
		if !strings.Contains(body, frame) {
			t.Fatalf("expected SSE frame %q in %q", frame, body)
		}
	}
}

func TestRunEndpointRejectsRealModeUnlessEnabled(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "")
	server := newTestServer(t)

	response := postJSON(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n", "mode": "real"}`)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d with %q", response.Code, response.Body.String())
	}
}

func TestRunEndpointRejectsInvalidWorkflow(t *testing.T) {
	server := newTestServer(t)

	response := postJSON(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n    depends_on: [missing]\n"}`)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestRunEndpointRejectsInvalidJSON(t *testing.T) {
	server := newTestServer(t)

	response := postRaw(server, "/api/run", `{`)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestRunEndpointRejectsOversizedRequestBodies(t *testing.T) {
	server := newTestServer(t)
	body := `{"yaml": "` + strings.Repeat("x", int(maxRequestBodyBytes)) + `"}`

	response := postJSON(server, "/api/run", body)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d with %q", response.Code, response.Body.String())
	}
}

func TestRealRunEndpointStreamsArtifactsAndFailureSummary(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	server := newTestServer(t).(*Server)
	dir := t.TempDir()
	server.workingDirectory = func() string { return dir }
	server.nextRunID = func() string { return "run-summary" }
	artifactPath := filepath.Join(dir, ".micromage", "runs", "run-summary", "review", "finding.md")
	input := `name: real-summary
description: real summary
nodes:
  - id: write-review
    bash: |
      mkdir -p "$ARTIFACTS_DIR/review"
      printf "finding" > "$ARTIFACTS_DIR/review/finding.md"
    outputs:
      - $ARTIFACTS_DIR/review/finding.md
  - id: fail-review
    bash: exit 7
`

	response := postJSON(server, "/api/run", `{"mode":"real","yaml": `+strconvQuote(input)+`}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with %q", response.Code, response.Body.String())
	}
	events := decodeSSEEvents(t, response)
	summary, ok := findRunEvent(events, "run_summary")
	if !ok {
		t.Fatalf("expected run_summary event, got %#v", events)
	}
	if summary.RunID != "run-summary" || summary.ArtifactsDir != filepath.Join(dir, ".micromage", "runs", "run-summary") {
		t.Fatalf("unexpected run metadata: %#v", summary)
	}
	if !eventHasCompletedNode(summary, "write-review") {
		t.Fatalf("expected completed node in summary: %#v", summary)
	}
	if !eventHasFailedNode(summary, "fail-review") {
		t.Fatalf("expected failed node in summary: %#v", summary)
	}
	if !eventHasArtifact(summary, "write-review", artifactPath) {
		t.Fatalf("expected generated artifact in summary: %#v", summary)
	}
	if _, ok := findRunEvent(events, "workflow_failed"); !ok {
		t.Fatalf("expected workflow_failed event, got %#v", events)
	}
}

func TestRealRunSummaryRejectsArtifactsOutsideRunDirectory(t *testing.T) {
	dir := t.TempDir()
	artifactsDir := filepath.Join(dir, ".micromage", "runs", "run-summary")
	validPath := filepath.Join(artifactsDir, "review", "finding.md")
	traversalPath := filepath.Join(dir, ".micromage", "runs", "escape.md")
	absolutePath := filepath.Join(dir, "escape.md")
	for path, content := range map[string]string{
		validPath:     "valid",
		traversalPath: "traversal",
		absolutePath:  "absolute",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create artifact directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write artifact fixture: %v", err)
		}
	}
	summary := newRealRunSummary(workflow.Workflow{Nodes: []workflow.Node{
		{ID: "valid", Outputs: []string{"$ARTIFACTS_DIR/review/finding.md"}},
		{ID: "traversal", Outputs: []string{"$ARTIFACTS_DIR/../escape.md"}},
		{ID: "absolute", Outputs: []string{absolutePath}},
	}}, dir, artifactsDir, "run-summary")

	artifacts := summary.generatedArtifacts()

	if len(artifacts) != 1 || artifacts[0].NodeID != "valid" || artifacts[0].Path != validPath {
		t.Fatalf("expected only in-run artifact, got %#v", artifacts)
	}
}

func TestRealRunnerConfigUsesRepoLocalArtifacts(t *testing.T) {
	dir := t.TempDir()
	config := realRunnerConfig(workflow.CommandRegistry{}, workflow.Workflow{Provider: "opencode", Model: "model"}, yamlRequest{Arguments: "review"}, dir, "run-1")

	if config.ArtifactsDir != filepath.Join(dir, ".micromage", "runs", "run-1") {
		t.Fatalf("expected repo-local artifacts dir, got %q", config.ArtifactsDir)
	}
	if config.CWD != dir || config.Arguments != "review" || config.DefaultProvider != "opencode" || config.DefaultModel != "model" {
		t.Fatalf("unexpected real runner config: %#v", config)
	}
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	assets, err := fs.Sub(testAssets, "testdata")
	if err != nil {
		t.Fatalf("test assets unavailable: %v", err)
	}
	server, err := NewServer(assets)
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

func postJSON(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	return postRaw(handler, path, body)
}

func postRaw(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeSSEEvents(t *testing.T, response *httptest.ResponseRecorder) []workflow.RunEvent {
	t.Helper()
	var events []workflow.RunEvent
	for _, chunk := range strings.Split(response.Body.String(), "\n\n") {
		for _, line := range strings.Split(chunk, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event workflow.RunEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("decode SSE event %q: %v", line, err)
			}
			events = append(events, event)
		}
	}
	return events
}

func findRunEvent(events []workflow.RunEvent, eventType string) (workflow.RunEvent, bool) {
	for _, event := range events {
		if event.Type == eventType {
			return event, true
		}
	}
	return workflow.RunEvent{}, false
}

func eventHasCompletedNode(event workflow.RunEvent, nodeID string) bool {
	for _, completed := range event.CompletedNodes {
		if completed == nodeID {
			return true
		}
	}
	return false
}

func eventHasFailedNode(event workflow.RunEvent, nodeID string) bool {
	for _, failure := range event.FailedNodes {
		if failure.NodeID == nodeID {
			return true
		}
	}
	return false
}

func eventHasArtifact(event workflow.RunEvent, nodeID string, path string) bool {
	for _, artifact := range event.Artifacts {
		if artifact.NodeID == nodeID && artifact.Path == path {
			return true
		}
	}
	return false
}

func previewContainsIssue(preview workflow.Preview, field string) bool {
	for _, issue := range preview.Issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}

func hasTemplate(templates []workflow.Template, id string) bool {
	for _, template := range templates {
		if template.ID == id {
			return true
		}
	}
	return false
}

func graphNodeLayer(nodes []workflow.NodeView, id string) int {
	for _, node := range nodes {
		if node.ID == id {
			return node.Layer
		}
	}
	return -1
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
