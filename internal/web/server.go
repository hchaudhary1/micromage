package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"micromage/internal/workflow"
)

const maxRequestBodyBytes int64 = 1 << 20

type Server struct {
	templates         *template.Template
	workflowTemplates []workflow.Template
	commands          workflow.CommandRegistry
	mux               *http.ServeMux
	workingDirectory  func() string
	nextRunID         func() string
}

func NewServer(assets fs.FS) (*Server, error) {
	templates, err := template.ParseFS(assets, "web/templates/*.html")
	if err != nil {
		return nil, err
	}

	staticFiles, err := fs.Sub(assets, "web/static")
	if err != nil {
		return nil, err
	}

	workflowTemplates, err := workflow.LoadTemplates(assets, "web/workflows/*.yaml")
	if err != nil {
		return nil, err
	}
	commands, err := workflow.LoadCommands(assets, "web/commands/*.md")
	if err != nil {
		return nil, err
	}

	server := &Server{
		templates:         templates,
		workflowTemplates: workflowTemplates,
		commands:          workflow.NewCommandRegistry(commands),
		mux:               http.NewServeMux(),
		workingDirectory:  mustGetwd,
		nextRunID:         func() string { return "run-" + strconv.FormatInt(time.Now().UnixNano(), 10) },
	}

	server.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))
	server.mux.HandleFunc("GET /", server.handleShell)
	server.mux.HandleFunc("GET /api/templates", server.handleTemplates)
	server.mux.HandleFunc("POST /api/preview", server.handlePreview)
	server.mux.HandleFunc("POST /api/run", server.handleRun)

	return server, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		Title string
	}{
		Title: "Micromage Workflows",
	}
	if err := s.templates.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, "could not render workflow shell", http.StatusInternalServerError)
	}
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.workflowTemplates)
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	input, ok := readYAMLRequest(w, r)
	if !ok {
		return
	}

	// The preview endpoint makes Go the source of truth for graph structure.
	writeJSON(w, http.StatusOK, workflow.BuildPreviewWithCommands(input, s.commands))
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	request, ok := readRunRequest(w, r)
	if !ok {
		return
	}

	preview := workflow.BuildPreviewWithCommands(request.YAML, s.commands)
	if !preview.CanRun {
		writeJSON(w, http.StatusBadRequest, preview)
		return
	}
	mode := request.Mode
	if mode == "" {
		mode = "simulate"
	}
	if mode != "simulate" && mode != "real" {
		http.Error(w, "invalid run mode", http.StatusBadRequest)
		return
	}
	if mode == "real" && os.Getenv("MICROMAGE_ENABLE_REAL_RUNS") != "1" {
		http.Error(w, "real runs require MICROMAGE_ENABLE_REAL_RUNS=1", http.StatusForbidden)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	emit := func(event workflow.RunEvent) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte("event: " + event.Type + "\n")); err != nil {
			return err
		}
		if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	runner := workflow.NodeRunner(workflow.LoggingRunner{})
	var summary *realRunSummary
	if mode == "real" {
		runID := s.nextRunID()
		cwd := s.workingDirectory()
		config := realRunnerConfig(s.commands, preview.Workflow, request, cwd, runID)
		summary = newRealRunSummary(preview.Workflow, cwd, config.ArtifactsDir, runID)
		runner = workflow.NewRealRunner(config)
	}

	if summary != nil {
		emitRaw := emit
		emit = func(event workflow.RunEvent) error {
			summary.observe(event)
			return emitRaw(event)
		}
	}

	if err := workflow.Execute(r.Context(), preview.Workflow, runner, emit); err != nil {
		_ = emit(workflow.RunEvent{Type: "workflow_failed", Message: err.Error()})
	}
	if summary != nil {
		// Real-run summaries keep artifact evidence visible without shell inspection.
		_ = emit(summary.event())
	}
}

func realRunnerConfig(commands workflow.CommandRegistry, parsed workflow.Workflow, request yamlRequest, cwd string, runID string) workflow.RealRunnerConfig {
	return workflow.RealRunnerConfig{
		Commands:        commands,
		CWD:             cwd,
		Arguments:       request.Arguments,
		WorkflowID:      runID,
		ArtifactsDir:    workflow.DefaultArtifactsDir(cwd, runID),
		BaseBranch:      os.Getenv("MICROMAGE_BASE_BRANCH"),
		DefaultProvider: parsed.Provider,
		DefaultModel:    parsed.Model,
		Unsafe:          os.Getenv("MICROMAGE_OPENCODE_UNSAFE") == "1",
	}
}

type yamlRequest struct {
	YAML      string `json:"yaml"`
	Arguments string `json:"arguments"`
	Mode      string `json:"mode"`
}

func readYAMLRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	request, ok := readRunRequest(w, r)
	if !ok {
		return "", false
	}
	return request.YAML, true
}

func readRunRequest(w http.ResponseWriter, r *http.Request) (yamlRequest, bool) {
	defer r.Body.Close()
	var request yamlRequest
	// Bound request bodies keep workflow submissions from exhausting server memory.
	limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(limitedBody).Decode(&request); err != nil {
		var bodyTooLarge *http.MaxBytesError
		if errors.As(err, &bodyTooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return yamlRequest{}, false
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return yamlRequest{}, false
	}
	return request, true
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

type realRunSummary struct {
	workflow     workflow.Workflow
	cwd          string
	artifactsDir string
	runID        string
	completed    []string
	failures     []workflow.RunFailure
}

func newRealRunSummary(parsed workflow.Workflow, cwd string, artifactsDir string, runID string) *realRunSummary {
	return &realRunSummary{
		workflow:     parsed,
		cwd:          cwd,
		artifactsDir: artifactsDir,
		runID:        runID,
	}
}

func (summary *realRunSummary) observe(event workflow.RunEvent) {
	switch event.Type {
	case "node_complete":
		summary.completed = append(summary.completed, event.NodeID)
	case "node_failed":
		summary.failures = append(summary.failures, workflow.RunFailure{NodeID: event.NodeID, Message: event.Message})
	}
}

func (summary *realRunSummary) event() workflow.RunEvent {
	return workflow.RunEvent{
		Type:           "run_summary",
		Message:        "run artifacts: " + summary.artifactsDir,
		RunID:          summary.runID,
		ArtifactsDir:   summary.artifactsDir,
		Artifacts:      summary.generatedArtifacts(),
		CompletedNodes: append([]string(nil), summary.completed...),
		FailedNodes:    append([]workflow.RunFailure(nil), summary.failures...),
	}
}

func (summary *realRunSummary) generatedArtifacts() []workflow.RunArtifact {
	var artifacts []workflow.RunArtifact
	for _, node := range summary.workflow.Nodes {
		for _, pattern := range node.Outputs {
			path := summary.resolveOutputPath(pattern)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				artifacts = append(artifacts, workflow.RunArtifact{NodeID: node.ID, Path: path})
			}
		}
	}
	return artifacts
}

func (summary *realRunSummary) resolveOutputPath(pattern string) string {
	path := strings.ReplaceAll(pattern, "$ARTIFACTS_DIR", summary.artifactsDir)
	path = strings.ReplaceAll(path, "$WORKFLOW_ID", summary.runID)
	if !filepath.IsAbs(path) {
		path = filepath.Join(summary.cwd, path)
	}
	return path
}
