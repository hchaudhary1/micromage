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

//go:embed testdata/web/templates/*.html testdata/web/static/* testdata/web/workflows/*.yaml
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
	if len(templates) != 3 {
		t.Fatalf("expected three templates, got %#v", templates)
	}
	if templates[0].YAML == "" || templates[0].Name == "" {
		t.Fatalf("expected populated template metadata, got %#v", templates[0])
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

func TestRunEndpointStreamsFakeEvents(t *testing.T) {
	server := newTestServer(t)

	response := postJSON(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n"}`)

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
}

func TestRunEndpointRejectsInvalidWorkflow(t *testing.T) {
	server := newTestServer(t)

	response := postJSON(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n    depends_on: [missing]\n"}`)

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
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
