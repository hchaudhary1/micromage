package kanban

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrColumnNotFound = errors.New("column not found")
	ErrCardNotFound   = errors.New("card not found")
	ErrBlankTitle     = errors.New("card title cannot be blank")
)

type Board struct {
	Title   string
	Columns []Column
}

type Column struct {
	ID    string
	Title string
	Cards []Card
}

type Card struct {
	ID          string
	Title       string
	Description string
}

type Store struct {
	mu       sync.RWMutex
	board    Board
	nextCard int
}

func NewStore() *Store {
	return &Store{
		board: Board{
			Title: "Micromage Kanban",
			Columns: []Column{
				{ID: "todo", Title: "To Do", Cards: []Card{
					{ID: "card-1", Title: "Map the first quest", Description: "Capture the next feature idea before it wanders off."},
					{ID: "card-2", Title: "Sketch the board flow", Description: "Make movement between lists obvious and fast."},
				}},
				{ID: "doing", Title: "Doing", Cards: []Card{
					{ID: "card-3", Title: "Build the kanban shell", Description: "Give the team a shared place to see work in motion."},
				}},
				{ID: "review", Title: "Review"},
				{ID: "done", Title: "Done"},
			},
		},
		nextCard: 4,
	}
}

func (s *Store) Snapshot() Board {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clone := Board{
		Title:   s.board.Title,
		Columns: make([]Column, len(s.board.Columns)),
	}
	for i, column := range s.board.Columns {
		clone.Columns[i] = Column{
			ID:    column.ID,
			Title: column.Title,
			Cards: append([]Card(nil), column.Cards...),
		}
	}
	return clone
}

func (s *Store) AddCard(columnID, title, description string) (Card, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Card{}, ErrBlankTitle
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	columnIndex := s.columnIndex(columnID)
	if columnIndex < 0 {
		return Card{}, ErrColumnNotFound
	}

	card := Card{
		ID:          fmt.Sprintf("card-%d", s.nextCard),
		Title:       title,
		Description: strings.TrimSpace(description),
	}
	s.nextCard++
	// New cards land at the bottom so incoming work does not disrupt active priorities.
	s.board.Columns[columnIndex].Cards = append(s.board.Columns[columnIndex].Cards, card)
	return card, nil
}

func (s *Store) UpdateCard(cardID, title, description string) (Card, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Card{}, ErrBlankTitle
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for columnIndex := range s.board.Columns {
		for cardIndex := range s.board.Columns[columnIndex].Cards {
			if s.board.Columns[columnIndex].Cards[cardIndex].ID == cardID {
				// Edits keep the same card ID so shared links and board position remain stable.
				s.board.Columns[columnIndex].Cards[cardIndex].Title = title
				s.board.Columns[columnIndex].Cards[cardIndex].Description = strings.TrimSpace(description)
				return s.board.Columns[columnIndex].Cards[cardIndex], nil
			}
		}
	}
	return Card{}, ErrCardNotFound
}

func (s *Store) DeleteCard(cardID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for columnIndex := range s.board.Columns {
		for cardIndex := range s.board.Columns[columnIndex].Cards {
			if s.board.Columns[columnIndex].Cards[cardIndex].ID == cardID {
				// Removing finished or mistaken work keeps the board signal focused.
				s.board.Columns[columnIndex].Cards = append(
					s.board.Columns[columnIndex].Cards[:cardIndex],
					s.board.Columns[columnIndex].Cards[cardIndex+1:]...,
				)
				return nil
			}
		}
	}
	return ErrCardNotFound
}

func (s *Store) MoveCard(cardID, targetColumnID string, targetIndex int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	targetColumnIndex := s.columnIndex(targetColumnID)
	if targetColumnIndex < 0 {
		return ErrColumnNotFound
	}

	var moved Card
	found := false
	for columnIndex := range s.board.Columns {
		for cardIndex := range s.board.Columns[columnIndex].Cards {
			if s.board.Columns[columnIndex].Cards[cardIndex].ID == cardID {
				moved = s.board.Columns[columnIndex].Cards[cardIndex]
				s.board.Columns[columnIndex].Cards = append(
					s.board.Columns[columnIndex].Cards[:cardIndex],
					s.board.Columns[columnIndex].Cards[cardIndex+1:]...,
				)
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return ErrCardNotFound
	}

	cards := s.board.Columns[targetColumnIndex].Cards
	if targetIndex < 0 {
		targetIndex = 0
	}
	if targetIndex > len(cards) {
		targetIndex = len(cards)
	}

	// Flexible drop positions make drag-and-drop forgiving when the browser sends edge indexes.
	cards = append(cards, Card{})
	copy(cards[targetIndex+1:], cards[targetIndex:])
	cards[targetIndex] = moved
	s.board.Columns[targetColumnIndex].Cards = cards
	return nil
}

func (s *Store) columnIndex(columnID string) int {
	for i, column := range s.board.Columns {
		if column.ID == columnID {
			return i
		}
	}
	return -1
}
