package kanban

import "testing"

func TestAddCardAppendsToColumn(t *testing.T) {
	store := NewStore()

	card, err := store.AddCard("todo", "Write launch copy", "Tell the team what changed.")
	if err != nil {
		t.Fatalf("AddCard returned error: %v", err)
	}

	board := store.Snapshot()
	todo := findColumn(t, board, "todo")
	if got := todo.Cards[len(todo.Cards)-1]; got.ID != card.ID || got.Title != "Write launch copy" {
		t.Fatalf("expected new card at end of todo, got %#v", got)
	}
}

func TestAddCardRejectsBlankTitle(t *testing.T) {
	store := NewStore()

	if _, err := store.AddCard("todo", "   ", ""); err != ErrBlankTitle {
		t.Fatalf("expected ErrBlankTitle, got %v", err)
	}
}

func TestMoveCardBetweenColumnsAtRequestedPosition(t *testing.T) {
	store := NewStore()

	if err := store.MoveCard("card-1", "doing", 0); err != nil {
		t.Fatalf("MoveCard returned error: %v", err)
	}

	board := store.Snapshot()
	todo := findColumn(t, board, "todo")
	doing := findColumn(t, board, "doing")

	if containsCard(todo, "card-1") {
		t.Fatal("expected card-1 to leave todo")
	}
	if got := doing.Cards[0].ID; got != "card-1" {
		t.Fatalf("expected card-1 at top of doing, got %s", got)
	}
}

func TestMoveCardClampsIndex(t *testing.T) {
	store := NewStore()

	if err := store.MoveCard("card-1", "review", 99); err != nil {
		t.Fatalf("MoveCard returned error: %v", err)
	}

	review := findColumn(t, store.Snapshot(), "review")
	if got := review.Cards[len(review.Cards)-1].ID; got != "card-1" {
		t.Fatalf("expected card-1 at end of review, got %s", got)
	}
}

func TestUpdateCardKeepsIdentityAndPosition(t *testing.T) {
	store := NewStore()

	card, err := store.UpdateCard("card-2", "Refine board flow", "Reduce the number of clicks.")
	if err != nil {
		t.Fatalf("UpdateCard returned error: %v", err)
	}

	todo := findColumn(t, store.Snapshot(), "todo")
	if card.ID != "card-2" || todo.Cards[1].Title != "Refine board flow" {
		t.Fatalf("expected card-2 to be updated in place, got %#v", todo.Cards[1])
	}
}

func TestDeleteCardRemovesItFromBoard(t *testing.T) {
	store := NewStore()

	if err := store.DeleteCard("card-3"); err != nil {
		t.Fatalf("DeleteCard returned error: %v", err)
	}

	if containsCard(findColumn(t, store.Snapshot(), "doing"), "card-3") {
		t.Fatal("expected card-3 to be removed")
	}
}

func findColumn(t *testing.T, board Board, id string) Column {
	t.Helper()
	for _, column := range board.Columns {
		if column.ID == id {
			return column
		}
	}
	t.Fatalf("missing column %q", id)
	return Column{}
}

func containsCard(column Column, cardID string) bool {
	for _, card := range column.Cards {
		if card.ID == cardID {
			return true
		}
	}
	return false
}
