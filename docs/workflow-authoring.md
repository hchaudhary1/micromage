# Workflow Authoring Reference

This reference describes the workflow YAML surface Micromage accepts today. It is intentionally operator-facing: it covers what authors can write, what is validated, and what actually runs.

Use the embedded workflows in `cmd/server/web/workflows/` as examples, but treat the parser and runner behavior as the source of truth.

## Minimal Shape

```yaml
name: plan-implement-verify
description: Plan the change, implement it, then verify it.
provider: opencode
model: opencode/nemotron-3-ultra-free
nodes:
  - id: plan
    prompt: |
      Create a concise implementation plan.

  - id: verify
    bash: go test ./...
    depends_on: [plan]
```

Top-level `name`, `description`, and at least one `nodes` item are required. `provider`, `model`, `tags`, `interactive`, and `worktree` are parsed as workflow metadata. `tags` must be an array of strings; empty values and duplicate trimmed tags are dropped. `interactive` must be a boolean and `worktree` must be a mapping.

Unknown workflow fields produce warnings unless they start with `x_`. Extension fields are preserved, but Micromage does not execute them.

## Node Kinds

Every node must have exactly one executable field. The parser recognizes these node kinds:

| Kind | YAML field | Simulated run | Real run |
| --- | --- | --- | --- |
| Command | `command: micromage-create-plan` | Logs a fake command run | Executes the matching embedded command prompt through the AI provider |
| Prompt | `prompt: | ...` | Logs a fake prompt run | Sends the inline prompt to the AI provider |
| Bash | `bash: go test ./...` | Logs a fake shell run | Runs `sh -c` in the server working directory |
| Script | `script: ...` | Logs a fake script run | Rejected by real preflight |
| Loop | `loop: ...` | Logs a fake loop run | Rejected by real preflight |
| Approval | `approval: ...` | Logs a fake approval run | Rejected by real preflight |
| Cancel | `cancel: Stop workflow` | Logs a fake cancel run | Rejected by real preflight |

`command`, `prompt`, `bash`, and `cancel` must be non-empty strings. `script`, `loop`, and `approval` must be non-empty YAML values. A node with no executable field, or more than one executable field, is invalid.

Command nodes reference Markdown command assets by ID, without `.md`. The browser preview validates command IDs against the loaded command registry before allowing a run.

## Dependencies And Scheduling

Use `depends_on` as an array of upstream node IDs:

```yaml
- id: synthesize
  prompt: Summarize the reviewer outputs.
  depends_on: [code-review, docs-impact]
```

Micromage validates that every dependency exists and that the graph has no cycles. Nodes with no dependency start in the first layer. Nodes in the same layer are scheduled concurrently, sorted by node ID for stable events and preview layout.

Real OpenCode calls are still serialized inside the server process to avoid OpenCode local database lock failures. Bash nodes and non-OpenCode runners can still overlap when they are in the same graph layer.

## Trigger Rules

`trigger_rule` controls whether a node runs after its dependencies finish. The default is `all_success`.

| Rule | Runs when |
| --- | --- |
| `all_success` | Every dependency succeeded |
| `one_success` | At least one dependency succeeded |
| `none_failed_min_one_success` | No dependency failed and at least one dependency succeeded |
| `all_done` | Every dependency reached success, failure, or skipped |

If `one_success` has no successful dependency, the node is skipped and the workflow fails with the failed dependency reasons when available. A successful `one_success` join is the only current trigger rule that tolerates dependency failures for the overall workflow result. `all_done` can run cleanup-style nodes after a failure, but the upstream failure still makes the workflow fail.

## Provider And Model Resolution

`provider` and `model` can be set at workflow level or node level:

```yaml
provider: opencode
model: opencode/nemotron-3-ultra-free
nodes:
  - id: review
    prompt: Review the change.
    model: opencode/nemotron-3-ultra-free
```

For real prompt and command nodes, Micromage resolves provider as:

1. Node `provider`
2. Workflow `provider`
3. `opencode`

Model resolves as:

1. Node `model`
2. Workflow `model`
3. `opencode/nemotron-3-ultra-free`

Real preflight validates provider names against registered providers. In the shipped server, `opencode` is the registered provider. Model names are passed through to the provider; Micromage does not validate model availability.

Some embedded starter workflows use `provider: codex` as preview metadata. They can be simulated, but real mode rejects them unless a `codex` provider is registered in the server.

## Runtime Fields

The parser accepts these node metadata fields:

| Field | Current behavior |
| --- | --- |
| `context` | Parsed and displayed; accepted by real preflight, but the shipped OpenCode CLI runner does not translate it into a CLI flag |
| `agent` | Parsed and displayed; accepted by real preflight, but the shipped OpenCode CLI runner does not translate it into a CLI flag |
| `timeout` | Node timeout in seconds; used by real bash and AI nodes |
| `idle_timeout` | Node timeout in milliseconds; takes precedence over `timeout` when positive |
| `outputs` | Declared artifact paths; see Outputs And Artifacts |
| `when` | Parsed and displayed, but not evaluated; real preflight rejects it |
| `retry` | Parsed and validated only for positive `max_attempts`; not executed; real preflight rejects it |
| `hooks` | Parsed and displayed, but not executed; real preflight rejects it |
| `mcp` | Parsed and displayed, but not executed; real preflight rejects it |
| `skills` | Parsed and displayed, but not executed; real preflight rejects it |
| `allowed_tools` | Parsed and displayed, but not enforced by the shipped provider; real preflight rejects it |

Top-level `interactive` and `worktree` are also parsed but not executed in real mode, so real preflight rejects them. Unknown node fields produce warnings unless they start with `x_`; extension fields are preserved as metadata only.

## Substitutions And Environment

Real prompt and command nodes replace these tokens before provider execution:

| Token | Value |
| --- | --- |
| `$ARGUMENTS` | The `/api/run` request `arguments` string |
| `$WORKFLOW_ID` | The generated run ID |
| `$ARTIFACTS_DIR` | The run artifact directory |
| `$node-id.output` | Output published by a successful upstream node |
| `$node-id.output.key` | JSON object field from a successful upstream node output |

Prompt substitutions are raw text replacements. If an upstream output is JSON, dot-field references use that JSON object. If the output is not valid JSON or the key is absent, the field reference remains unchanged.

Real bash nodes receive these environment variables:

| Variable | Value |
| --- | --- |
| `ARGUMENTS` | The `/api/run` request `arguments` string |
| `WORKFLOW_ID` | The generated run ID |
| `ARTIFACTS_DIR` | The run artifact directory |
| `BASE_BRANCH` | `MICROMAGE_BASE_BRANCH` from the server environment |
| `NODE_<ID>_OUTPUT` | Output from each successful prior node, with the node ID uppercased and `-`, `.`, and spaces converted to `_` |

Bash scripts also support `$node-id.output` and `$node-id.output.key` substitutions before the shell starts. Values are escaped for use inside double quotes. Node IDs must avoid normalized environment collisions, so IDs such as `foo-bar` and `foo_bar` cannot both appear in one workflow.

Only successful nodes publish outputs. Failed provider streams and failed event emission do not publish partial provider text downstream.

## Outputs And Artifacts

Real runs use a repo-local run directory:

```text
.micromage/runs/<run-id>
```

Nodes can declare output artifacts:

```yaml
- id: code-review
  prompt: Write concise review findings.
  outputs:
    - $ARTIFACTS_DIR/review-last-commit/code-review-findings.md
```

Declared paths may be absolute paths under `$ARTIFACTS_DIR` or relative paths, which resolve inside the run artifact directory. Paths that escape the run artifact directory are rejected.

For real prompt and command nodes:

- If the provider writes a declared output file, Micromage reads it and uses that content as `$node.output`.
- If exactly one declared output is missing and the provider returned text, Micromage materializes that text into the declared file and uses it as `$node.output`.
- If multiple outputs are declared, each file must exist.
- Multiple declared outputs are joined with a blank line for `$node.output`.
- Declared output files are capped at 4 MiB each before reading.

For real bash nodes, stdout is the node output. Declared output files are not read back into `$node.output`, but files matching `outputs` are collected into the final real-run summary and artifact manifest.

Real run streams emit a final `run_summary` event with the run ID, artifact directory, generated declared artifacts, completed nodes, and failed node reasons. The server also writes `workflow.yaml`, `summary.json`, and `manifest.json` in the run directory for real runs.

## Timeouts And Limits

`idle_timeout` is milliseconds. `timeout` is seconds. If both are positive, `idle_timeout` wins.

Real bash nodes run with a context timeout when either timeout field is set. OpenCode-backed prompt and command nodes apply the same timeout to the OpenCode process. Other registered providers receive a context with the timeout applied by Micromage.

Other runtime limits:

- `/api/preview` and `/api/run` JSON bodies are capped at 1 MiB.
- Bash stdout and stderr are capped at 1 MiB each.
- Provider stdout and stderr lines are capped at 1 MiB.
- Accumulated provider assistant output is capped at 4 MiB per node.
- Individual node log messages are capped at 256 KiB.
- Declared output artifact reads are capped at 4 MiB per file.

## Validation Summary

Micromage reports errors for:

- Empty or malformed YAML
- Missing `name`, `description`, or `nodes`
- Missing, duplicate, or invalid node IDs; IDs must match `[A-Za-z0-9][A-Za-z0-9_-]*`
- Node IDs that collide after output environment normalization
- Missing dependencies
- Dependency cycles
- Nodes without exactly one executable field
- Empty executable payloads
- Invalid `trigger_rule`
- `retry.max_attempts` values that are numeric and not positive
- Missing command IDs when a command registry is available
- Real-mode use of unsupported node kinds or ignored real fields
- Real-mode provider names that are not registered

Micromage reports warnings for:

- Unknown workflow or node fields without an `x_` prefix
- Common field typos such as `descripton`, `depend_on`, `dependsOn`, `triggerRule`, `modle`, and `output`
- Invalid top-level metadata types for `tags`, `interactive`, and `worktree`
- Runtime metadata that is parsed and displayed but not fully executed in the preview/simulate path

Warnings do not block simulated runs. Errors block runs.

## Simulate Versus Real

Simulated run is the default. It validates the YAML and command references, schedules the graph, and emits Server-Sent Events with fake node logs. It does not call AI providers, run shell commands, evaluate `when`, enforce runtime controls, create declared artifacts, or emit `run_summary`.

Real run is opt-in and requires `MICROMAGE_ENABLE_REAL_RUNS=1`, `MICROMAGE_REAL_RUN_TOKEN`, a trusted local Host/Origin, and a bearer token on the request. Real mode adds real-run preflight, then executes only `prompt`, `command`, and `bash` nodes. Real runs can modify the repository and write `.micromage/runs/<run-id>` artifacts.

Run simulated mode first when authoring a workflow, then switch to real mode only after preview validation and real preflight both pass.
