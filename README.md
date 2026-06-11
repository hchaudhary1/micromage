# Micromage Workflows

Micromage is a Go-only workflow DAG UI shell inspired by Archon. It uses server-rendered HTML, embedded static assets, vanilla JavaScript, and Go-powered YAML parsing, validation, layout, and fake run streaming.

## Requirements

- Go 1.22 or newer
- Git

No Node, NPM, Tailwind, Vite, Webpack, or frontend package manager is required. Go libraries are allowed when they fit the project.

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
cmd/server/                    Go entrypoint and embedded web assets
cmd/server/web/templates/      HTML templates
cmd/server/web/static/         CSS and vanilla JavaScript
cmd/server/web/workflows/      Embedded starter workflow YAML templates
internal/workflow/             YAML parsing, validation, layout, templates, and fake runner
internal/web/                  HTTP handlers and tests
.githooks/                     Tracked Git hooks
```

## Current Behavior

- Provides embedded workflow templates for linear, parallel, and approval-gate DAGs
- Lets developers edit Archon-like YAML in a split view
- Validates workflow structure and renders a deterministic SVG DAG preview
- Shows read-only node details for selected graph nodes
- Streams a fake run over Server-Sent Events using topological layers
- Does not persist edited YAML or execute real providers, commands, scripts, hooks, MCP, skills, approvals, or worktrees yet

## Development Notes

- Keep the app Go-first and dependency-light.
- Avoid NPM dependencies at all costs.
- Golang libraries are allowed when they fit the project.
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
