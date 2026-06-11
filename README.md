# Micromage Workflows

Micromage is a Go-only workflow DAG UI shell. It uses server-rendered HTML, embedded static assets, vanilla JavaScript, and Go-powered YAML parsing, validation, layout, command prompts, and guarded workflow execution.

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

Run the opt-in local OpenCode smoke test:

```sh
MICROMAGE_OPENCODE_E2E=1 go test ./internal/workflow -run TestOpenCodeProviderSmokeOptIn
```

The smoke test uses `opencode/nemotron-3-ultra-free` and skips unless explicitly enabled.

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
cmd/server/web/commands/       Embedded command prompt Markdown assets
internal/workflow/             YAML parsing, validation, layout, templates, commands, and runners
internal/web/                  HTTP handlers and tests
.githooks/                     Tracked Git hooks
```

## Current Behavior

- Provides embedded workflow templates for linear, parallel, approval-gate, idea-to-PR, and last-commit review DAGs
- Provides embedded command prompt assets for command nodes
- Lets developers edit workflow YAML in a split view
- Validates workflow structure and renders a deterministic SVG DAG preview
- Validates command references against embedded command assets
- Shows read-only node details for selected graph nodes
- Streams a simulated run over Server-Sent Events by default
- Supports guarded real runs for `prompt`, `command`, and `bash` nodes through the OpenCode CLI
- Substitutes `$ARGUMENTS`, `$WORKFLOW_ID`, `$ARTIFACTS_DIR`, and `$node.output` references into real AI prompts
- Does not execute scripts, hooks, MCP, skills, approvals, or worktrees yet

## Real Runs

Real execution is opt-in because workflows can modify files, create commits, push branches, and open PRs.

```sh
MICROMAGE_ENABLE_REAL_RUNS=1 go run ./cmd/server
```

Then call `/api/run` with `mode: "real"` and optional `arguments`. OpenCode runs use:

```text
opencode run --model opencode/nemotron-3-ultra-free --format json --dir <repo> <prompt>
```

Set `MICROMAGE_OPENCODE_UNSAFE=1` only when you intentionally want Micromage to append OpenCode's `--dangerously-skip-permissions` flag.

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
