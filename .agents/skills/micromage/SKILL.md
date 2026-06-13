---
name: micromage
description: |
  Use when working in the Micromage repository: authoring or validating workflow
  YAML, running foreground or detached Micromage workflows, inspecting run logs,
  managing approvals/resume, changing the Go CLI/engine/provider code, or
  planning features against the Archon and Kanban reference projects.
argument-hint: "[workflow|run-id|feature request]"
---

# Micromage Skill

Micromage is a Go workflow orchestrator for AI-oriented development tasks. Use
this skill inside the Micromage repo when the task touches workflows, CLI
behavior, detached runs, provider runners, or project process.

## First Steps

1. Read `AGENTS.md` and follow its active rules.
2. Use Beads (`bd`) for durable work tracking before implementation.
3. Prefer `rg`/`rg --files` to discover code and workflows.
4. Keep changes test driven and maintain the 70% coverage gate.
5. Avoid NPM dependencies; Go libraries are allowed when they fit.

## Repo References

- Similar workflow runner: `/Users/hassan/Documents/EXAMPLE-1-node-workflows`
- Similar kanban/session system: `/Users/hassan/Documents/EXAMPLE-2-kanban-workflows`
- Current CLI entrypoint: `cmd/micromage/main.go`
- Workflow engine: `internal/engine`
- Run state: `internal/state`
- JSONL watch dashboard: `internal/watch`
- Detached run registry: `internal/runregistry`
- Detached child process launcher: `internal/detach`

Use the reference projects to understand design patterns, but implement in
Micromage's Go style and current architecture.

## Running Workflows

Foreground run blocks until complete:

```bash
go run ./cmd/micromage run --workflow testdata/workflows/smoke.yaml
```

Detached run returns immediately and records run metadata:

```bash
go run ./cmd/micromage run --detach --workflow testdata/workflows/smoke.yaml
```

Inspect detached runs:

```bash
go run ./cmd/micromage runs
go run ./cmd/micromage status --run-id latest
go run ./cmd/micromage watch --run-id latest
```

Watch a direct JSONL log:

```bash
go run ./cmd/micromage watch --log .micromage/run.jsonl --once
```

Approve and resume a human gate:

```bash
go run ./cmd/micromage approve --state .micromage/state/local.json --node review
go run ./cmd/micromage resume --workflow path/to/workflow.yaml --state .micromage/state/local.json
```

## Authoring Workflows

Micromage workflows are YAML DAGs. Use command nodes for deterministic shell work,
agent nodes for provider-backed AI tasks, and human gates before irreversible
steps. Independent nodes in the same dependency layer run concurrently.

When creating a new reusable workflow, prefer the default meta workflow:

```bash
go run ./cmd/micromage run \
  --workflow assets/defaults/workflows/micromage-workflow-builder.yaml \
  --arguments "create a workflow that ..."
```

The builder inspects repo context, designs a workflow, pauses at a human gate,
writes YAML, validates it, and reports foreground/detached run commands.

Minimal workflow:

```yaml
name: smoke
nodes:
  hello:
    type: command
    command: echo hello
```

Validate before running:

```bash
go run ./cmd/micromage validate --workflow path/to/workflow.yaml
```

## Workflow Node Model

Supported node shapes:

- `type: command` with `command:` runs deterministic shell work. Use this for
  validation, file checks, git/gh commands, and glue scripts.
- `type: agent` with `prompt:` or `command:` runs provider-backed reasoning or
  code work. Use `command:` to reference a command template from the command
  directory.
- `type: human_gate` with `message:` pauses the run until `approve` records a
  reviewer decision. Use gates before risky writes, publishing, or expensive
  execution.
- `type: loop` with `loop.prompt`, `loop.until`, and `loop.max_iterations`
  repeats an agent task until the completion signal appears.

Routing and control:

- `depends_on` defines DAG edges. Nodes with satisfied dependencies in the same
  layer run concurrently.
- `when` skips a node unless a prior output condition matches, for example
  `$classify.output.issue_type == 'bug'`.
- `trigger_rule` controls dependency readiness: use `all_success` by default,
  `one_success` for branch joins, and `all_done` for reports/cleanup.
- `route.on_failure.to` sends execution back to a repair node. Always cap repair
  routes with `max_iterations` or `max_repeated_failures`.
- `$node.output.field` works only when the node's raw output is valid JSON.
  Classification nodes should end with a compact JSON object if downstream
  `when` conditions need fields.
- Failure routes must target an earlier node in dependency order. After the
  repair node reruns, Micromage continues forward through the DAG and re-runs
  cleared nodes, including the failed validation node.
- Resume trusts persisted passed nodes and skips them, so design nodes to leave
  useful artifacts or outputs for downstream work.

Failure-route example:

```yaml
nodes:
  repair:
    type: agent
    prompt: Fix the validation failure using $validate.output
  validate:
    type: command
    command: go test ./...
    depends_on: [repair]
    route:
      on_failure:
        to: repair
        max_iterations: 2
        max_repeated_failures: 2
```

Workflow authoring defaults:

- Prefer command nodes for deterministic work and agent nodes for judgment.
- Add a human gate between planning and irreversible implementation when the
  workflow could modify code, publish, push, or spend significant time.
- Keep generated workflows valid for the Go parser; do not use Archon-only
  fields such as `script`, `runtime`, `bash`, `approval`, or `cancel`.
- Do not introduce npm, node, bun, or frontend assumptions.

## Implementation Rules

- Create or claim a Beads issue before code changes.
- Write focused tests before or alongside implementation.
- Add one short business-intent comment for meaningful production changes.
- Keep foreground behavior stable when adding detached-run behavior.
- Preserve explicit user paths and flags; defaults should be predictable.
- Avoid broad refactors unless they directly reduce risk for the requested work.

## Quality Gates

Run these before closing implementation work:

```bash
go test ./... -cover
go run ./cmd/micromage quality pre-commit --repo . --threshold 70
```

For live provider checks, use the opt-in harnesses described in `README.md`.
Do not run live provider tests unless the user asks or the environment is
explicitly configured for them.

## Session Close

1. Close completed Beads issues.
2. Run quality gates when code changed.
3. Check `git status`.
4. Commit completed work when the active instructions authorize it.
5. Push only when the user explicitly asks.
