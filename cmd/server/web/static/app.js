const lists = document.querySelectorAll("[data-card-list]");
let draggedCard = null;

function wireCard(card) {
  card.addEventListener("dragstart", () => {
    draggedCard = card;
    card.classList.add("dragging");
  });

  card.addEventListener("dragend", () => {
    card.classList.remove("dragging");
    draggedCard = null;
  });
}

document.querySelectorAll("[data-card-id]").forEach(wireCard);

lists.forEach((list) => {
  list.addEventListener("dragover", (event) => {
    event.preventDefault();
    if (!draggedCard) return;

    const afterElement = getDropTarget(list, event.clientY);
    if (afterElement) {
      list.insertBefore(draggedCard, afterElement);
    } else {
      list.appendChild(draggedCard);
    }
  });

  list.addEventListener("dragenter", () => {
    list.classList.add("is-over");
  });

  list.addEventListener("dragleave", () => {
    list.classList.remove("is-over");
  });

  list.addEventListener("drop", async (event) => {
    event.preventDefault();
    list.classList.remove("is-over");
    if (!draggedCard) return;

    const column = list.closest("[data-column-id]");
    const cards = [...list.querySelectorAll("[data-card-id]")];
    const index = cards.findIndex((card) => card === draggedCard);
    const payload = new URLSearchParams({
      card_id: draggedCard.dataset.cardId,
      column_id: column.dataset.columnId,
      index: String(index),
    });

    // Persisting each drop keeps the shared board state aligned with what the user sees.
    const response = await fetch("/cards/move", {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
      },
      body: payload,
    });

    if (!response.ok) {
      window.location.reload();
    }
  });
});

function getDropTarget(list, y) {
  const cards = [...list.querySelectorAll(".card:not(.dragging)")];

  return cards.reduce(
    (closest, card) => {
      const box = card.getBoundingClientRect();
      const offset = y - box.top - box.height / 2;
      if (offset < 0 && offset > closest.offset) {
        return { offset, element: card };
      }
      return closest;
    },
    { offset: Number.NEGATIVE_INFINITY, element: null },
  ).element;
}
