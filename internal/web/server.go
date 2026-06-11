package web

import (
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"micromage/internal/workflow"
)

type Server struct {
	templates         *template.Template
	workflowTemplates []workflow.Template
	commands          workflow.CommandRegistry
	mux               *http.ServeMux
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
	if mode == "real" {
		runID := "run-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		runner = workflow.NewRealRunner(workflow.RealRunnerConfig{
			Commands:        s.commands,
			CWD:             mustGetwd(),
			Arguments:       request.Arguments,
			WorkflowID:      runID,
			ArtifactsDir:    filepath.Join(os.TempDir(), "micromage-runs", runID),
			BaseBranch:      os.Getenv("MICROMAGE_BASE_BRANCH"),
			DefaultProvider: preview.Workflow.Provider,
			DefaultModel:    preview.Workflow.Model,
			Unsafe:          os.Getenv("MICROMAGE_OPENCODE_UNSAFE") == "1",
		})
	}

	if err := workflow.Execute(r.Context(), preview.Workflow, runner, emit); err != nil {
		_ = emit(workflow.RunEvent{Type: "workflow_failed", Message: err.Error()})
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
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
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
