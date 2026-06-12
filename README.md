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

The server binds to `127.0.0.1:8080` by default. Set `MICROMAGE_HOST` to opt into another bind address, and set `MICROMAGE_PORT` or `PORT` to change the port. Production HTTP limits are configurable with `MICROMAGE_READ_TIMEOUT`, `MICROMAGE_READ_HEADER_TIMEOUT`, `MICROMAGE_WRITE_TIMEOUT`, `MICROMAGE_IDLE_TIMEOUT`, and `MICROMAGE_SHUTDOWN_TIMEOUT`; values use Go duration strings such as `30s`, `5m`, or `30m`.

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

## Continuous Integration

GitHub Actions runs the remote CI quality gate on pushes and pull requests. The workflow uses the Go version declared in `go.mod`, runs tests, vet, build, race tests, and the same 70% total coverage threshold as the local pre-commit hook.

The vulnerability scan installs the official Go vulnerability checker in CI with:

```sh
go install golang.org/x/vuln/cmd/govulncheck@latest
```

Then CI runs:

```sh
"$(go env GOPATH)/bin/govulncheck" ./...
```

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
- Stores real-run artifacts under `.micromage/runs/<run-id>` so local CLI agents can read and write phase evidence
- Does not execute scripts, hooks, MCP, skills, approvals, or worktrees yet

## Real Runs

Real execution is opt-in because workflows can modify files, create commits, push branches, and open PRs.

```sh
MICROMAGE_ENABLE_REAL_RUNS=1 go run ./cmd/server
```

Then choose **Real** in the run mode selector or call `/api/run` with `mode: "real"` and optional `arguments`. OpenCode runs use:

```text
opencode run --model opencode/nemotron-3-ultra-free --format json --dir <repo> <prompt>
```

Set `MICROMAGE_OPENCODE_UNSAFE=1` only when you intentionally want Micromage to append OpenCode's `--dangerously-skip-permissions` flag.

### Running `review-last-commit`

The embedded `review-last-commit` workflow is intended to review the current `HEAD` commit against the local repository state.

1. Start the server with real runs enabled.
2. Select the `review-last-commit` template.
3. Set run mode to `Real`.
4. Optionally enter review instructions in the input field.
5. Run the workflow and watch the run log for completed nodes, failed nodes, generated artifact paths, and the run directory.

The workflow first writes shared Git context, then runs parallel reviewer prompts through OpenCode, then synthesizes any successful reviewer output. If every reviewer fails, synthesis is skipped and the stream ends with a failure event.

### Artifact Lifecycle

Every real run gets a repo-local directory:

```text
.micromage/runs/<run-id>
```

`review-last-commit` stores its files under:

```text
.micromage/runs/<run-id>/review-last-commit/
```

Important files include:

- `context.md` and `context.json` from the setup node
- `*-findings.md` from each reviewer node
- `consolidated-review.md` from the synthesis node

Prompt and command nodes can declare `outputs`. When a provider writes the declared file, Micromage uses that file as the node output for downstream `$node.output` substitutions. When a provider succeeds but only returns text, a single declared output is materialized from that text. If the provider stream fails or event emission fails, partial text is not published downstream and is not materialized as a successful artifact.

The run stream emits a final `run_summary` event for real runs. It includes the run ID, artifact directory, generated declared outputs, completed nodes, and failed node reasons. The UI renders the same summary in the run log.

### Timeouts And Partial Failures

Nodes can set `timeout` in seconds or `idle_timeout` in milliseconds. OpenCode-backed nodes apply those limits to the provider process so stuck runs fail and emit a node failure instead of hanging indefinitely.

Parallel review workflows can tolerate partial failure when a downstream node uses `trigger_rule: one_success`. In that case synthesis runs when at least one dependency succeeds. Failed reviewers are still reported in the run summary so their errors are visible without manually inspecting SSE logs.

### OpenCode Concurrency

Micromage currently serializes OpenCode provider calls. OpenCode `1.16.2` reports a single local database at:

```text
~/.local/share/opencode/opencode.db
```

`opencode run --help` exposes session, attach, and port options, but not a per-run database path or another stable isolation control. Earlier parallel provider calls hit SQLite `database is locked` failures, so the supported model is concurrent workflow scheduling with serialized OpenCode execution inside each Micromage server process.

The serialization can be revisited if OpenCode adds documented per-run storage isolation, or if Micromage switches to a long-lived `opencode serve` integration that proves safe concurrent request handling against the shared database.

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
