package web

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"micromage/internal/kanban"
)

//go:embed testdata/web/templates/*.html testdata/web/static/*
var testAssets embed.FS

func TestBoardRendersColumnsAndCards(t *testing.T) {
	server := newTestServer(t)

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Micromage Kanban") || !strings.Contains(body, "Build the kanban shell") {
		t.Fatalf("expected board title and sample card in response, got %q", body)
	}
}

func TestCreateCardPersistsAndRedirects(t *testing.T) {
	store := kanban.NewStore()
	server := newTestServerWithStore(t, store)

	form := url.Values{
		"column_id":   {"todo"},
		"title":       {"Plan sprint"},
		"description": {"Pick the highest value work."},
	}
	response := postForm(server, "/cards", form)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", response.Code)
	}
	todo := findColumn(t, store.Snapshot(), "todo")
	if got := todo.Cards[len(todo.Cards)-1].Title; got != "Plan sprint" {
		t.Fatalf("expected persisted card title, got %q", got)
	}
}

func TestMoveCardSupportsJSONDragDrop(t *testing.T) {
	store := kanban.NewStore()
	server := newTestServerWithStore(t, store)

	form := url.Values{
		"card_id":   {"card-1"},
		"column_id": {"review"},
		"index":     {"0"},
	}
	request := httptest.NewRequest(http.MethodPost, "/cards/move", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	review := findColumn(t, store.Snapshot(), "review")
	if len(review.Cards) != 1 || review.Cards[0].ID != "card-1" {
		t.Fatalf("expected card-1 in review, got %#v", review.Cards)
	}
}

func TestMoveCardRedirectsForFormSubmission(t *testing.T) {
	store := kanban.NewStore()
	server := newTestServerWithStore(t, store)

	response := postForm(server, "/cards/move", url.Values{
		"card_id":   {"card-1"},
		"column_id": {"done"},
		"index":     {"bad-index"},
	})

	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", response.Code)
	}
	done := findColumn(t, store.Snapshot(), "done")
	if len(done.Cards) != 1 || done.Cards[0].ID != "card-1" {
		t.Fatalf("expected card-1 in done, got %#v", done.Cards)
	}
}

func TestUpdateCardPersistsAndRedirects(t *testing.T) {
	store := kanban.NewStore()
	server := newTestServerWithStore(t, store)

	response := postForm(server, "/cards/update", url.Values{
		"card_id":     {"card-2"},
		"title":       {"Clarify board flow"},
		"description": {"Make the next step obvious."},
	})

	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", response.Code)
	}
	todo := findColumn(t, store.Snapshot(), "todo")
	if got := todo.Cards[1].Title; got != "Clarify board flow" {
		t.Fatalf("expected updated title, got %q", got)
	}
}

func TestDeleteCardPersistsAndRedirects(t *testing.T) {
	store := kanban.NewStore()
	server := newTestServerWithStore(t, store)

	response := postForm(server, "/cards/delete", url.Values{
		"card_id": {"card-3"},
	})

	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", response.Code)
	}
	doing := findColumn(t, store.Snapshot(), "doing")
	if len(doing.Cards) != 0 {
		t.Fatalf("expected doing column to be empty, got %#v", doing.Cards)
	}
}

func TestBlankCreateShowsBadRequest(t *testing.T) {
	server := newTestServer(t)

	response := postForm(server, "/cards", url.Values{
		"column_id": {"todo"},
		"title":     {" "},
	})

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestUnknownMoveTargetShowsNotFound(t *testing.T) {
	server := newTestServer(t)

	response := postForm(server, "/cards/move", url.Values{
		"card_id":   {"card-1"},
		"column_id": {"missing"},
		"index":     {"0"},
	})

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newTestServerWithStore(t, kanban.NewStore())
}

func newTestServerWithStore(t *testing.T, store *kanban.Store) http.Handler {
	t.Helper()
	assets, err := fs.Sub(testAssets, "testdata")
	if err != nil {
		t.Fatalf("test assets unavailable: %v", err)
	}
	server, err := NewServer(store, assets)
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

func postForm(handler http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func findColumn(t *testing.T, board kanban.Board, id string) kanban.Column {
	t.Helper()
	for _, column := range board.Columns {
		if column.ID == id {
			return column
		}
	}
	t.Fatalf("missing column %q", id)
	return kanban.Column{}
}
