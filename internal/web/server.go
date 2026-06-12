package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
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
	request, ok := readRunRequest(w, r)
	if !ok {
		return
	}

	// The preview endpoint makes Go the source of truth for graph structure.
	writeJSON(w, http.StatusOK, s.previewForRequest(request))
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	request, ok := readRunRequest(w, r)
	if !ok {
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
	if mode == "real" {
		if !authorizeRealRun(w, r) {
			return
		}
	}
	preview := s.previewForRequest(request)
	if !preview.CanRun {
		writeJSON(w, http.StatusBadRequest, preview)
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

func (s *Server) previewForRequest(request yamlRequest) workflow.Preview {
	preview := workflow.BuildPreviewWithCommands(request.YAML, s.commands)
	if request.Mode == "real" {
		return applyRealRunPreflight(preview)
	}
	return preview
}

func applyRealRunPreflight(preview workflow.Preview) workflow.Preview {
	if !preview.CanRun {
		return preview
	}
	if issues := workflow.ValidateRealRun(preview.Workflow, nil); len(issues) > 0 {
		// Real preflight stays JSON so clients can fix execution blockers before streaming starts.
		preview.Issues = append(preview.Issues, issues...)
		preview.CanRun = false
		preview.Graph = workflow.BuildGraph(preview.Workflow, preview.Issues)
	}
	return preview
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

func readRunRequest(w http.ResponseWriter, r *http.Request) (yamlRequest, bool) {
	defer r.Body.Close()
	if !hasJSONContentType(r) {
		http.Error(w, "JSON endpoints require Content-Type: application/json", http.StatusUnsupportedMediaType)
		return yamlRequest{}, false
	}
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

func hasJSONContentType(r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func authorizeRealRun(w http.ResponseWriter, r *http.Request) bool {
	if os.Getenv("MICROMAGE_ENABLE_REAL_RUNS") != "1" {
		http.Error(w, "real runs require MICROMAGE_ENABLE_REAL_RUNS=1", http.StatusForbidden)
		return false
	}
	token := os.Getenv("MICROMAGE_REAL_RUN_TOKEN")
	if token == "" {
		http.Error(w, "real runs require MICROMAGE_REAL_RUN_TOKEN when MICROMAGE_ENABLE_REAL_RUNS=1", http.StatusForbidden)
		return false
	}
	if !isTrustedLocalHost(r.Host) {
		http.Error(w, "real runs require a local Host header", http.StatusForbidden)
		return false
	}
	if !isTrustedLocalOrigin(r.Header.Get("Origin")) {
		http.Error(w, "real runs require a local Origin header when Origin is present", http.StatusForbidden)
		return false
	}
	if !realRunTokenMatches(token, requestRealRunToken(r)) {
		http.Error(w, "real runs require Authorization: Bearer MICROMAGE_REAL_RUN_TOKEN", http.StatusUnauthorized)
		return false
	}
	return true
}

func requestRealRunToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if scheme, value, ok := strings.Cut(auth, " "); ok && strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(r.Header.Get("X-Micromage-Run-Token"))
}

func realRunTokenMatches(expected string, provided string) bool {
	if provided == "" {
		return false
	}
	expectedHash := sha256.Sum256([]byte(expected))
	providedHash := sha256.Sum256([]byte(provided))
	// Real-run authorization gates local shell execution with a timing-stable token check.
	return subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) == 1
}

func isTrustedLocalOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return isTrustedLocalHost(parsed.Host)
}

func isTrustedLocalHost(host string) bool {
	if host == "" {
		return true
	}
	hostname, err := hostnameWithoutPort(host)
	if err != nil {
		return false
	}
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && (ip.Equal(net.ParseIP("127.0.0.1")) || ip.Equal(net.ParseIP("::1")))
}

func hostnameWithoutPort(host string) (string, error) {
	hostname, _, err := net.SplitHostPort(host)
	if err == nil {
		return strings.Trim(hostname, "[]"), nil
	}
	if strings.Contains(err.Error(), "missing port in address") {
		return strings.Trim(host, "[]"), nil
	}
	if strings.Count(host, ":") > 1 && net.ParseIP(host) != nil {
		return host, nil
	}
	return "", err
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
			path, err := summary.resolveOutputPath(pattern)
			if err != nil {
				continue
			}
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				artifacts = append(artifacts, workflow.RunArtifact{NodeID: node.ID, Path: path})
			}
		}
	}
	return artifacts
}

func (summary *realRunSummary) resolveOutputPath(pattern string) (string, error) {
	path := strings.ReplaceAll(pattern, "$ARTIFACTS_DIR", summary.artifactsDir)
	path = strings.ReplaceAll(path, "$WORKFLOW_ID", summary.runID)
	return workflow.ResolveDeclaredArtifactPath(path, summary.artifactsDir)
}
