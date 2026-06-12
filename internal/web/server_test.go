package web

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
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

func TestHealthzEndpointReportsLiveness(t *testing.T) {
	server := newTestServer(t)

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Fatalf("expected text health response, got %q", contentType)
	}
	if response.Body.String() != "ok\n" {
		t.Fatalf("expected ok body, got %q", response.Body.String())
	}
}

func TestReadyzEndpointReportsInitializedDependencies(t *testing.T) {
	server := newTestServer(t).(*Server)

	ready := httptest.NewRecorder()
	server.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if ready.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", ready.Code)
	}
	if contentType := ready.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Fatalf("expected text readiness response, got %q", contentType)
	}
	if ready.Body.String() != "ok\n" {
		t.Fatalf("expected ok body, got %q", ready.Body.String())
	}

	server.ready = func() bool { return false }
	notReady := httptest.NewRecorder()
	server.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", notReady.Code)
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

func TestPreviewEndpointDistinguishesRealRunExecutability(t *testing.T) {
	server := newTestServer(t)
	input := `name: real-preview
description: real preview
provider: codex
nodes:
  - id: approve-plan
    approval:
      prompt: Continue?
`

	simulated := postJSON(server, "/api/preview", `{"mode":"simulate","yaml": `+strconvQuote(input)+`}`)
	if simulated.Code != http.StatusOK {
		t.Fatalf("expected simulate preview 200, got %d", simulated.Code)
	}
	var simulatePreview workflow.Preview
	if err := json.NewDecoder(simulated.Body).Decode(&simulatePreview); err != nil {
		t.Fatalf("decode simulate preview: %v", err)
	}
	if !simulatePreview.CanRun {
		t.Fatalf("expected simulate preview to remain runnable, got %#v", simulatePreview.Issues)
	}

	real := postJSON(server, "/api/preview", `{"mode":"real","yaml": `+strconvQuote(input)+`}`)
	if real.Code != http.StatusOK {
		t.Fatalf("expected real preview 200, got %d", real.Code)
	}
	var realPreview workflow.Preview
	if err := json.NewDecoder(real.Body).Decode(&realPreview); err != nil {
		t.Fatalf("decode real preview: %v", err)
	}
	if !previewContainsNodeIssue(realPreview, "", "provider", "codex") || !previewContainsNodeIssue(realPreview, "approve-plan", "approval", "unsupported real node kind approval") {
		t.Fatalf("expected real preflight issues, got %#v", realPreview.Issues)
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
	if !strings.Contains(response.Body.String(), "request body too large") {
		t.Fatalf("expected clear request size error, got %q", response.Body.String())
	}
}

func TestRunEndpointStreamsFakeEvents(t *testing.T) {
	server := newTestServer(t).(*Server)
	dir := t.TempDir()
	server.workingDirectory = func() string { return dir }
	server.nextRunID = func() string { return "run-simulated" }

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

	index := readDurableRunIndex(t, dir)
	if len(index.Runs) != 1 || index.Runs[0].RunID != "run-simulated" || index.Runs[0].Status != "succeeded" {
		t.Fatalf("unexpected durable simulated run index: %#v", index)
	}
	if index.Runs[0].WorkflowID != "test" {
		t.Fatalf("expected workflow name to identify unsaved workflow, got %#v", index.Runs[0])
	}
	if index.Runs[0].NodeCounts.Total != 1 || index.Runs[0].NodeCounts.Completed != 1 || index.Runs[0].NodeCounts.Failed != 0 {
		t.Fatalf("unexpected durable simulated node counts: %#v", index.Runs[0].NodeCounts)
	}
}

func TestRequestLogsAreStructuredAndSanitized(t *testing.T) {
	server := newTestServer(t).(*Server)
	var logs bytes.Buffer
	server.logOutput = &logs
	bodySecret := "secret-token"
	bodyYAML := "name: leaked-workflow\ndescription: hidden\nnodes:\n  - id: plan\n    prompt: do not log\n"

	response := postJSONWithOptions(server, "/api/preview?token=query-secret", `{"yaml": `+strconvQuote(bodyYAML)+`, "arguments": "`+bodySecret+`"}`, requestOptions{
		token: "header-secret",
	})

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	entry := findLogEntry(t, logs.String(), "http_request")
	if entry["method"] != http.MethodPost || entry["path"] != "/api/preview" {
		t.Fatalf("unexpected request log route fields: %#v", entry)
	}
	if status, ok := entry["status"].(float64); !ok || status != http.StatusOK {
		t.Fatalf("expected status 200, got %#v", entry["status"])
	}
	if _, ok := entry["duration_ms"].(float64); !ok {
		t.Fatalf("expected numeric duration_ms, got %#v", entry["duration_ms"])
	}
	for _, leaked := range []string{bodySecret, "header-secret", "query-secret", "leaked-workflow", "do not log"} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("request logs leaked sensitive content %q in %s", leaked, logs.String())
		}
	}
}

func TestRunLogsSimulatedLifecycleWithoutSensitiveContent(t *testing.T) {
	server := newTestServer(t).(*Server)
	var logs bytes.Buffer
	server.logOutput = &logs
	input := `name: secret-run
description: hidden
nodes:
  - id: plan
    prompt: provider secret output should not be logged
`

	response := postJSON(server, "/api/run", `{"yaml": `+strconvQuote(input)+`, "mode": "simulate", "arguments": "token-like argument"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	start := findLogEntry(t, logs.String(), "workflow_start")
	if start["mode"] != "simulate" {
		t.Fatalf("expected simulate start log, got %#v", start)
	}
	finish := findLogEntry(t, logs.String(), "workflow_complete")
	if finish["mode"] != "simulate" || finish["status"] != "success" {
		t.Fatalf("expected successful simulate completion log, got %#v", finish)
	}
	if _, ok := finish["duration_ms"].(float64); !ok {
		t.Fatalf("expected numeric duration_ms, got %#v", finish["duration_ms"])
	}
	if _, ok := finish["run_id"]; ok {
		t.Fatalf("simulate log should not include a run_id, got %#v", finish)
	}
	for _, leaked := range []string{"secret-run", "provider secret output", "token-like argument", "would run prompt node plan"} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("run logs leaked sensitive content %q in %s", leaked, logs.String())
		}
	}
}

func TestRunLogsRealFailureWithRunIDAndSanitizedReason(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t).(*Server)
	server.workingDirectory = func() string { return t.TempDir() }
	server.nextRunID = func() string { return "run-log-id" }
	var logs bytes.Buffer
	server.logOutput = &logs
	input := `name: real-log
description: real log
nodes:
  - id: fail-review
    bash: |
      echo "artifact content should stay in SSE only"
      echo "provider output should stay private" >&2
      exit 7
`

	response := postJSONWithToken(server, "/api/run", `{"mode":"real","yaml": `+strconvQuote(input)+`}`, "secret")

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with %q", response.Code, response.Body.String())
	}
	start := findLogEntry(t, logs.String(), "workflow_start")
	if start["mode"] != "real" || start["run_id"] != "run-log-id" {
		t.Fatalf("expected real start log with run_id, got %#v", start)
	}
	failure := findLogEntry(t, logs.String(), "workflow_failed")
	if failure["mode"] != "real" || failure["run_id"] != "run-log-id" || failure["status"] != "failed" {
		t.Fatalf("expected real failure log with run_id, got %#v", failure)
	}
	if reason, ok := failure["failure_reason"].(string); !ok || reason != "exit status 7" {
		t.Fatalf("expected sanitized failure reason, got %#v", failure["failure_reason"])
	}
	if _, ok := failure["duration_ms"].(float64); !ok {
		t.Fatalf("expected numeric duration_ms, got %#v", failure["duration_ms"])
	}
	for _, leaked := range []string{"artifact content should stay in SSE only", "provider output should stay private", "secret"} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("run logs leaked sensitive content %q in %s", leaked, logs.String())
		}
	}
}

func TestPublicFailureReasonIsSingleLineAndBounded(t *testing.T) {
	reason := publicFailureReason(errors.New(strings.Repeat("x", 300) + "\nprovider output should not appear"))

	if strings.Contains(reason, "\n") || strings.Contains(reason, "provider output") {
		t.Fatalf("expected single-line public reason, got %q", reason)
	}
	if len(reason) > 259 || !strings.HasSuffix(reason, "...") {
		t.Fatalf("expected bounded reason with suffix, got len=%d %q", len(reason), reason)
	}
}

func TestRunEndpointRejectsRealModeUnlessEnabled(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "")
	server := newTestServer(t)

	response := postJSONWithToken(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n", "mode": "real"}`, "secret")

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d with %q", response.Code, response.Body.String())
	}
}

func TestRunEndpointRejectsRealModeWhenTokenIsNotConfigured(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "")
	server := newTestServer(t)

	response := postJSONWithToken(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    bash: echo ok\n", "mode": "real"}`, "secret")

	assertPlainHTTPError(t, response, http.StatusForbidden)
	if !strings.Contains(response.Body.String(), "MICROMAGE_REAL_RUN_TOKEN") {
		t.Fatalf("expected actionable token configuration error, got %q", response.Body.String())
	}
}

func TestRunEndpointRejectsRealModeWithMissingOrWrongToken(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t)
	body := `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    bash: echo ok\n", "mode": "real"}`

	missing := postJSON(server, "/api/run", body)
	assertPlainHTTPError(t, missing, http.StatusUnauthorized)

	wrong := postJSONWithToken(server, "/api/run", body, "wrong")
	assertPlainHTTPError(t, wrong, http.StatusUnauthorized)
}

func TestRunEndpointRejectsRealModeFromRemoteHost(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t)

	response := postJSONWithOptions(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    bash: echo ok\n", "mode": "real"}`, requestOptions{
		token: "secret",
		host:  "workstation.example.com",
	})

	assertPlainHTTPError(t, response, http.StatusForbidden)
}

func TestRunEndpointRejectsRealModeFromRemoteOrigin(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t)

	response := postJSONWithOptions(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    bash: echo ok\n", "mode": "real"}`, requestOptions{
		token:  "secret",
		origin: "https://evil.example.com",
	})

	assertPlainHTTPError(t, response, http.StatusForbidden)
}

func TestRunEndpointAllowsRealModeFromLocalOriginWithBearerToken(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t).(*Server)
	server.workingDirectory = func() string { return t.TempDir() }
	server.nextRunID = func() string { return "local-origin" }

	response := postJSONWithOptions(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    bash: echo ok\n", "mode": "real"}`, requestOptions{
		contentType: "Application/JSON; charset=utf-8",
		token:       "secret",
		host:        "127.0.0.1:8080",
		origin:      "http://localhost:8080",
	})

	if response.Code != http.StatusOK {
		t.Fatalf("expected local real run to stream, got %d with %q", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected event stream, got %q", contentType)
	}
	if _, ok := findRunEvent(decodeSSEEvents(t, response), "run_summary"); !ok {
		t.Fatalf("expected run_summary event, got %q", response.Body.String())
	}
}

func TestRunAndPreviewEndpointsRequireJSONContentType(t *testing.T) {
	server := newTestServer(t)
	body := `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n"}`

	preview := postRawWithOptions(server, "/api/preview", body, requestOptions{})
	if preview.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected preview 415, got %d with %q", preview.Code, preview.Body.String())
	}

	run := postRawWithOptions(server, "/api/run", body, requestOptions{contentType: "text/plain"})
	if run.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected run 415, got %d with %q", run.Code, run.Body.String())
	}
}

func TestRunEndpointRejectsInvalidWorkflow(t *testing.T) {
	server := newTestServer(t)

	response := postJSON(server, "/api/run", `{"yaml": "name: test\ndescription: test\nnodes:\n  - id: plan\n    prompt: plan\n    depends_on: [missing]\n"}`)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestRunEndpointRejectsUnsupportedRealNodeKindsBeforeStreaming(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t)
	input := `name: real-kinds
description: real kinds
nodes:
  - id: approval-step
    approval:
      prompt: Continue?
  - id: cancel-step
    cancel: stop now
  - id: loop-step
    loop:
      items: [one]
  - id: script-step
    script:
      language: js
      source: console.log("hi")
`

	response := postJSONWithToken(server, "/api/run", `{"mode":"real","yaml": `+strconvQuote(input)+`}`, "secret")

	assertPreviewValidationResponse(t, response)
	var preview workflow.Preview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode validation preview: %v", err)
	}
	for _, want := range []struct {
		nodeID string
		field  string
		kind   string
	}{
		{"approval-step", "approval", "approval"},
		{"cancel-step", "cancel", "cancel"},
		{"loop-step", "loop", "loop"},
		{"script-step", "script", "script"},
	} {
		if !previewContainsNodeIssue(preview, want.nodeID, want.field, want.kind) {
			t.Fatalf("expected unsupported %s issue for %s, got %#v", want.kind, want.nodeID, preview.Issues)
		}
	}
}

func TestRunEndpointRejectsUnregisteredRealProviderBeforeStreaming(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t)
	input := `name: real-provider
description: real provider
provider: codex
nodes:
  - id: plan
    prompt: plan
`

	response := postJSONWithToken(server, "/api/run", `{"mode":"real","yaml": `+strconvQuote(input)+`}`, "secret")

	assertPreviewValidationResponse(t, response)
	var preview workflow.Preview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode validation preview: %v", err)
	}
	if !previewContainsNodeIssue(preview, "", "provider", "codex") || !previewContainsNodeIssue(preview, "", "provider", "opencode") {
		t.Fatalf("expected provider issue to name codex and registered providers, got %#v", preview.Issues)
	}
}

func TestRunEndpointRejectsIgnoredRealSemanticsBeforeStreaming(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
	server := newTestServer(t)
	input := `name: real-semantics
description: real semantics
interactive: true
worktree:
  enabled: true
nodes:
  - id: plan
    prompt: plan
    when: branch == main
    retry:
      max_attempts: 2
    hooks:
      before: echo before
    mcp: filesystem
    skills: [review]
    allowed_tools: [Read]
`

	response := postJSONWithToken(server, "/api/run", `{"mode":"real","yaml": `+strconvQuote(input)+`}`, "secret")

	assertPreviewValidationResponse(t, response)
	var preview workflow.Preview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode validation preview: %v", err)
	}
	for _, field := range []string{"interactive", "worktree"} {
		if !previewContainsNodeIssue(preview, "", field, "not executed in real mode") {
			t.Fatalf("expected workflow %s issue, got %#v", field, preview.Issues)
		}
	}
	for _, field := range []string{"when", "retry", "hooks", "mcp", "skills", "allowed_tools"} {
		if !previewContainsNodeIssue(preview, "plan", field, "not executed in real mode") {
			t.Fatalf("expected node %s issue, got %#v", field, preview.Issues)
		}
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
	if !strings.Contains(response.Body.String(), "request body too large") {
		t.Fatalf("expected clear request size error, got %q", response.Body.String())
	}
}

func TestRealRunEndpointStreamsArtifactsAndFailureSummary(t *testing.T) {
	t.Setenv("MICROMAGE_ENABLE_REAL_RUNS", "1")
	t.Setenv("MICROMAGE_REAL_RUN_TOKEN", "secret")
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
  - id: skipped-after-failure
    bash: printf skipped
    depends_on: [fail-review]
`

	response := postJSONWithOptions(server, "/api/run", `{"mode":"real","yaml": `+strconvQuote(input)+`}`, requestOptions{
		token: "secret",
	})

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

	index := readDurableRunIndex(t, dir)
	if len(index.Runs) != 1 || index.Runs[0].RunID != "run-summary" || index.Runs[0].Status != "failed" {
		t.Fatalf("unexpected durable run index: %#v", index)
	}
	if index.Runs[0].WorkflowID != "real-summary" || index.Runs[0].WorkflowName != "real-summary" || index.Runs[0].ArtifactsDir != filepath.Join(".micromage", "runs", "run-summary") {
		t.Fatalf("unexpected durable run metadata: %#v", index.Runs[0])
	}
	if index.Runs[0].NodeCounts.Total != 3 || index.Runs[0].NodeCounts.Completed != 1 || index.Runs[0].NodeCounts.Failed != 1 || index.Runs[0].NodeCounts.Skipped != 1 {
		t.Fatalf("unexpected durable node counts: %#v", index.Runs[0].NodeCounts)
	}
	eventsData, err := os.ReadFile(filepath.Join(dir, ".micromage", "runs", "events.jsonl"))
	if err != nil {
		t.Fatalf("expected durable run events: %v", err)
	}
	if !strings.Contains(string(eventsData), `"type":"run_started"`) || !strings.Contains(string(eventsData), `"type":"run_failed"`) {
		t.Fatalf("expected durable lifecycle events, got %q", string(eventsData))
	}
	manifestPath := filepath.Join(dir, ".micromage", "runs", "run-summary", "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected artifact manifest: %v", err)
	}
	var manifest workflow.ArtifactManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode artifact manifest: %v", err)
	}
	if manifest.RunID != "run-summary" || manifest.WorkflowID != "real-summary" {
		t.Fatalf("unexpected artifact manifest metadata: %#v", manifest)
	}
	if len(manifest.Artifacts) != 1 || manifest.Artifacts[0].Path != filepath.ToSlash(filepath.Join("review", "finding.md")) || manifest.Artifacts[0].SizeBytes != int64(len("finding")) {
		t.Fatalf("unexpected manifest artifacts: %#v", manifest.Artifacts)
	}
	if manifest.Artifacts[0].SHA256 == "" {
		t.Fatalf("expected artifact hash in manifest: %#v", manifest.Artifacts[0])
	}
	if len(manifest.CompletedNodes) != 1 || manifest.CompletedNodes[0] != "write-review" || len(manifest.FailedNodes) != 1 || manifest.FailedNodes[0].NodeID != "fail-review" {
		t.Fatalf("unexpected manifest node status: %#v", manifest)
	}
	workflowSnapshot, err := os.ReadFile(filepath.Join(dir, ".micromage", "runs", "run-summary", "workflow.yaml"))
	if err != nil {
		t.Fatalf("expected workflow snapshot: %v", err)
	}
	if string(workflowSnapshot) != input {
		t.Fatalf("unexpected workflow snapshot: %q", string(workflowSnapshot))
	}
	summaryData, err := os.ReadFile(filepath.Join(dir, ".micromage", "runs", "run-summary", "summary.json"))
	if err != nil {
		t.Fatalf("expected summary snapshot: %v", err)
	}
	var persistedSummary workflow.RunEvent
	if err := json.Unmarshal(summaryData, &persistedSummary); err != nil {
		t.Fatalf("decode persisted run summary: %v", err)
	}
	if persistedSummary.RunID != "run-summary" || !eventHasArtifact(persistedSummary, "write-review", artifactPath) {
		t.Fatalf("unexpected persisted run summary: %#v", persistedSummary)
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
	dir := t.TempDir()
	server.workingDirectory = func() string { return dir }
	return server
}

func readDurableRunIndex(t *testing.T, dir string) struct {
	Runs []struct {
		RunID        string `json:"run_id"`
		WorkflowID   string `json:"workflow_id"`
		WorkflowName string `json:"workflow_name"`
		Status       string `json:"status"`
		ArtifactsDir string `json:"artifacts_dir"`
		NodeCounts   struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
			Failed    int `json:"failed"`
			Skipped   int `json:"skipped"`
		} `json:"node_counts"`
	} `json:"runs"`
} {
	t.Helper()
	indexData, err := os.ReadFile(filepath.Join(dir, ".micromage", "runs", "index.json"))
	if err != nil {
		t.Fatalf("expected durable run index: %v", err)
	}
	var index struct {
		Runs []struct {
			RunID        string `json:"run_id"`
			WorkflowID   string `json:"workflow_id"`
			WorkflowName string `json:"workflow_name"`
			Status       string `json:"status"`
			ArtifactsDir string `json:"artifacts_dir"`
			NodeCounts   struct {
				Total     int `json:"total"`
				Completed int `json:"completed"`
				Failed    int `json:"failed"`
				Skipped   int `json:"skipped"`
			} `json:"node_counts"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatalf("decode durable run index: %v", err)
	}
	return index
}

type requestOptions struct {
	contentType string
	token       string
	host        string
	origin      string
}

func postJSON(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	return postJSONWithOptions(handler, path, body, requestOptions{})
}

func postJSONWithToken(handler http.Handler, path string, body string, token string) *httptest.ResponseRecorder {
	return postJSONWithOptions(handler, path, body, requestOptions{token: token})
}

func postJSONWithOptions(handler http.Handler, path string, body string, options requestOptions) *httptest.ResponseRecorder {
	if options.contentType == "" {
		options.contentType = "application/json"
	}
	return postRawWithOptions(handler, path, body, options)
}

func postRaw(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	return postJSON(handler, path, body)
}

func postRawWithOptions(handler http.Handler, path string, body string, options requestOptions) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Host = "localhost"
	if options.host != "" {
		request.Host = options.host
	}
	if options.contentType != "" {
		request.Header.Set("Content-Type", options.contentType)
	}
	if options.token != "" {
		request.Header.Set("Authorization", "Bearer "+options.token)
	}
	if options.origin != "" {
		request.Header.Set("Origin", options.origin)
	}
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

func assertPreviewValidationResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d with %q", response.Code, response.Body.String())
	}
	contentType := response.Header().Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") || !strings.Contains(contentType, "application/json") {
		t.Fatalf("expected JSON validation response before streaming, got content-type %q and body %q", contentType, response.Body.String())
	}
}

func assertPlainHTTPError(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("expected %d, got %d with %q", status, response.Code, response.Body.String())
	}
	if strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected normal HTTP error before streaming, got SSE body %q", response.Body.String())
	}
}

func previewContainsIssue(preview workflow.Preview, field string) bool {
	for _, issue := range preview.Issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}

func previewContainsNodeIssue(preview workflow.Preview, nodeID string, field string, messagePart string) bool {
	if preview.CanRun {
		return false
	}
	for _, issue := range preview.Issues {
		if issue.NodeID == nodeID && issue.Field == field && strings.Contains(issue.Message, messagePart) {
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

func findLogEntry(t *testing.T, logs string, event string) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(logs))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode log entry %q: %v", scanner.Text(), err)
		}
		if entry["event"] == event {
			return entry
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan logs: %v", err)
	}
	t.Fatalf("log entry %q not found in %s", event, logs)
	return nil
}
