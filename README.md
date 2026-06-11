# Micromage Kanban

Micromage is a small Trello-like kanban website written in Go. The app uses server-rendered HTML, embedded static assets, and vanilla JavaScript for drag-and-drop card movement.

## Requirements

- Go 1.22 or newer
- Git

No Node, NPM, Tailwind, Vite, Webpack, or frontend package manager is required.

## Run The App

```sh
go run ./cmd/server
```

Open the app at:

```text
http://localhost:8080
```

Use a custom port when needed:

```sh
PORT=3000 go run ./cmd/server
```

## Test And Coverage

Run the full test suite:

```sh
go test ./...
```

Run tests with coverage:

```sh
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out
```

The project target is at least 70% total code coverage.

## Git Hooks

This repo includes a tracked pre-commit hook at:

```text
.githooks/pre-commit
```

Enable it in a fresh checkout:

```sh
git config core.hooksPath .githooks
```

The hook runs Go tests with coverage and blocks commits below 70% total coverage.

## Project Layout

```text
cmd/server/                 Go entrypoint and embedded web assets
cmd/server/web/templates/   HTML templates
cmd/server/web/static/      CSS and vanilla JavaScript
internal/kanban/            Board domain logic and tests
internal/web/               HTTP handlers and tests
.githooks/                  Tracked Git hooks
```

## Current Behavior

- Shows a kanban board with To Do, Doing, Review, and Done columns
- Creates cards through standard HTML forms
- Edits card title and description inline
- Deletes cards
- Moves and reorders cards with browser drag-and-drop
- Stores board data in memory for the current server process

## Development Notes

- Keep the app Go-first and dependency-light.
- Add or update tests with each feature change.
- Maintain at least 70% total coverage.
- Leave short comments for business intent when adding non-obvious code paths.
- Avoid generated files in commits, including `coverage.out`.

## Common Commands

```sh
gofmt -w cmd internal
go test ./...
go test ./... -coverprofile=coverage.out
go run ./cmd/server
```
