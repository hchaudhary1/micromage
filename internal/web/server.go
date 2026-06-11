package web

import (
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"micromage/internal/kanban"
)

type Server struct {
	store     *kanban.Store
	templates *template.Template
	mux       *http.ServeMux
}

func NewServer(store *kanban.Store, assets fs.FS) (*Server, error) {
	templates, err := template.ParseFS(assets, "web/templates/*.html")
	if err != nil {
		return nil, err
	}

	staticFiles, err := fs.Sub(assets, "web/static")
	if err != nil {
		return nil, err
	}

	server := &Server{
		store:     store,
		templates: templates,
		mux:       http.NewServeMux(),
	}

	server.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))
	server.mux.HandleFunc("GET /", server.handleBoard)
	server.mux.HandleFunc("POST /cards", server.handleCreateCard)
	server.mux.HandleFunc("POST /cards/update", server.handleUpdateCard)
	server.mux.HandleFunc("POST /cards/delete", server.handleDeleteCard)
	server.mux.HandleFunc("POST /cards/move", server.handleMoveCard)

	return server, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "index.html", s.store.Snapshot()); err != nil {
		http.Error(w, "could not render board", http.StatusInternalServerError)
	}
}

func (s *Server) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	_, err := s.store.AddCard(r.FormValue("column_id"), r.FormValue("title"), r.FormValue("description"))
	if err != nil {
		respondBoardError(w, err)
		return
	}

	// Redirects keep browser refreshes from creating duplicate cards.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleUpdateCard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	_, err := s.store.UpdateCard(r.FormValue("card_id"), r.FormValue("title"), r.FormValue("description"))
	if err != nil {
		respondBoardError(w, err)
		return
	}

	// Redirects keep edit submissions aligned with the latest board snapshot.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteCard(r.FormValue("card_id")); err != nil {
		respondBoardError(w, err)
		return
	}

	// Redirects make delete actions predictable for keyboard-only users too.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleMoveCard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	index, err := strconv.Atoi(r.FormValue("index"))
	if err != nil {
		index = 0
	}

	err = s.store.MoveCard(r.FormValue("card_id"), r.FormValue("column_id"), index)
	if err != nil {
		respondBoardError(w, err)
		return
	}

	if wantsJSON(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Non-JavaScript submissions can still converge on the updated board.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func respondBoardError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, kanban.ErrBlankTitle):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, kanban.ErrColumnNotFound), errors.Is(err, kanban.ErrCardNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, "could not update board", http.StatusInternalServerError)
	}
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}
