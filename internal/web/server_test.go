package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
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
