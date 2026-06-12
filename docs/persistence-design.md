# Persistence Design

Status: chosen for implementation planning. This design covers bead `micromage-usz.9` and favors local-first Go code with no new NPM dependency and no database until file storage proves insufficient.

## Decision

Micromage will persist run history, artifact metadata, saved workflows, saved templates, and audit events under the repo-local `.micromage/` directory. The store will use append-only JSONL for event streams and compact JSON index files for query surfaces that the CLI and browser need to read quickly.

The two reference projects use durable workflow runs, workflow events, artifact indexes, cleanup commands, and workflow discovery. Micromage should adopt those concepts but not their DB-backed architecture yet. A small file store fits the current single-user local app, keeps artifacts beside the checked-out repo, and stays easy to inspect and recover with standard tools.

## Reference Alignment

`EXAMPLE-1-node-workflows` separates `workflow_runs` from `workflow_events`, keeps per-run artifacts outside git, supports run listing/detail/resume/cleanup commands, and treats workflow definitions as filesystem assets. Micromage should copy those boundaries, but not the database dependency, until local JSON indexes stop being enough.

`EXAMPLE-2-kanban-workflows` uses lightweight runtime `events.jsonl` plus artifact JSON summaries for local status surfaces. Micromage should use the same recoverable-file pattern for early production readiness: append durable events first, build compact summaries from them, and tolerate missing or malformed rows without losing the whole store.

## Directory Layout

```text
.micromage/
  runs/
    index.json
    events.jsonl
    <run-id>/
      manifest.json
      workflow.yaml
      summary.json
      logs.jsonl
      files...
  workflows/
    <workflow-id>.yaml
    index.json
  templates/
    <template-id>.yaml
    index.json
  audit.jsonl
  store-version.json
```

`.micromage/` remains gitignored. Operators can back it up or delete it without changing repository source.

## Run Index

`runs/index.json` is the browser and CLI listing source. It is rewritten atomically through `index.json.tmp` plus rename after each lifecycle change.

Each run entry should include:

```json
{
  "schema_version": 1,
  "run_id": "run-...",
  "workflow_id": "review-last-commit",
  "workflow_name": "Review Last Commit",
  "mode": "real",
  "status": "running",
  "created_at": "2026-06-12T00:00:00Z",
  "started_at": "2026-06-12T00:00:01Z",
  "finished_at": null,
  "duration_ms": null,
  "cwd": "/path/to/repo",
  "artifacts_dir": ".micromage/runs/run-...",
  "arguments_redacted": true,
  "node_counts": {"total": 5, "completed": 2, "failed": 0, "skipped": 0},
  "failure_reason": "",
  "manifest_path": ".micromage/runs/run-.../manifest.json",
  "summary_path": ".micromage/runs/run-.../summary.json"
}
```

Indexes store relative paths where possible. Absolute `cwd` is allowed because the store is local-only, but UI surfaces should treat it as machine-private metadata.

## Lifecycle

Allowed states:

```text
queued -> running -> succeeded
queued -> running -> failed
queued -> running -> cancelled
queued -> cancelled
running -> interrupted
interrupted -> running
interrupted -> failed
```

The initial implementation can skip `queued` if runs still execute immediately, but the state should be reserved so future background dispatch does not need a schema break. `interrupted` is for a server/process crash or broken SSE stream where no terminal event was recorded. Startup reconciliation should mark stale `running` records as `interrupted` only when Micromage can prove the local process owns no active run.

Each transition appends a JSONL record to `runs/events.jsonl` and updates `runs/index.json`. The run summary event already emitted by the server should become the source of terminal node counts and generated artifact metadata.

## Artifact Manifest

Each run writes `.micromage/runs/<run-id>/manifest.json`. It is the durable equivalent of the current `run_summary` event and should be updated atomically.

Manifest fields:

```json
{
  "schema_version": 1,
  "run_id": "run-...",
  "workflow_id": "review-last-commit",
  "created_at": "2026-06-12T00:00:00Z",
  "artifacts_dir": ".micromage/runs/run-...",
  "workflow_snapshot": "workflow.yaml",
  "artifacts": [
    {
      "node_id": "synthesize",
      "path": "review-last-commit/consolidated-review.md",
      "kind": "declared_output",
      "size_bytes": 1234,
      "sha256": "hex...",
      "created_at": "2026-06-12T00:01:00Z"
    }
  ],
  "completed_nodes": ["setup", "synthesize"],
  "failed_nodes": [{"node_id": "docs", "message": "timeout"}]
}
```

Only files inside the run directory may be listed. Hashing is optional for the first write path but should be part of cleanup verification before deleting artifacts.

## Retention And Cleanup

Default retention:

- Keep terminal runs for 30 days.
- Always keep the most recent 20 terminal runs.
- Keep `running` and `interrupted` runs until a user explicitly deletes or reconciles them.
- Delete both the run directory and its index/manifest references during cleanup.

Cleanup surfaces:

- Go API: `Store.CleanupRuns(ctx, CleanupPolicy) (CleanupReport, error)`.
- CLI or server command later: `micromage cleanup --older-than 30d --keep 20 --dry-run`.
- Browser API later: `DELETE /api/runs?older_than=30d&keep=20&dry_run=true`.

The implementation must support dry-run first, then delete with path containment checks under `.micromage/runs`. Cleanup appends `run_cleanup_started`, `run_cleanup_deleted`, and `run_cleanup_failed` audit events.

## Saved Workflows And Templates

Embedded workflows remain bundled starter content. User-managed definitions live on disk:

- `.micromage/workflows/<workflow-id>.yaml` for project-local workflows users can edit and rerun.
- `.micromage/templates/<template-id>.yaml` for reusable starter templates.
- `index.json` files record display metadata, source, created/updated timestamps, and validation status.

Discovery order:

1. Embedded workflows and templates.
2. Saved project workflows.
3. Saved project templates.

Saved items override embedded items only by exact ID and should record `"source": "project"` in API responses. Save operations validate YAML before writing, preserve the prior file as `<id>.yaml.bak` on overwrite, and update the index atomically.

Global user-level storage is a non-goal for the first implementation. If needed later, add `~/.micromage/workflows` as an explicit second store with source labels rather than changing project-local paths.

## Audit Events

`audit.jsonl` records security-relevant actions without storing prompt bodies, node logs, tokens, or artifact contents.

Event fields:

```json
{
  "schema_version": 1,
  "event_id": "audit-...",
  "type": "real_run_authorized",
  "created_at": "2026-06-12T00:00:00Z",
  "run_id": "run-...",
  "workflow_id": "review-last-commit",
  "actor": "local-browser",
  "outcome": "success",
  "details": {"mode": "real"}
}
```

Required event types for the first implementation:

- `real_run_authorized`
- `real_run_rejected`
- `run_started`
- `run_finished`
- `run_interrupted`
- `run_cleanup_started`
- `run_cleanup_deleted`
- `workflow_saved`
- `template_saved`

`runs/events.jsonl` is for operational workflow events. `audit.jsonl` is for durable security and user-action events.

## Privacy, Security, And Logging

- Do not persist request authorization headers, bearer tokens, provider credentials, prompt bodies, node logs, or full artifact contents in indexes or audit records.
- Persist artifact paths, sizes, hashes, node IDs, and high-level failure reasons only.
- Truncate persisted failure reasons to the same public size limit used by server logs.
- Treat `.micromage/` as private local state; keep it gitignored and document that users should review it before sharing archives.
- Use path-cleaning and containment checks for all reads, writes, and deletes under `.micromage/`.
- Write files with owner-writable permissions and avoid world-writable directories.
- Use file locks or a simple process lock before supporting concurrent run writes from more than one server process.

## Migration Path

`store-version.json` records the current store schema:

```json
{"schema_version": 1, "created_at": "2026-06-12T00:00:00Z"}
```

Version 1 migrations should be simple file transforms:

1. Read all known JSON/JSONL files.
2. Write migrated files to `.micromage/.migrate-<timestamp>/`.
3. Rename them into place only after validation.
4. Leave a backup directory until cleanup removes it.

Move to SQLite only if one of these becomes true:

- Run indexes become too large for atomic JSON rewrite.
- Multiple Micromage processes need safe concurrent writes.
- Query requirements need compound filters that are awkward or slow in files.
- Durable resumability needs transactional updates across run, event, and node-session state.

If that happens, JSONL remains an export/import format so users can inspect and recover local history.

## Non-Goals

- Cloud sync, multi-user access control, and remote artifact hosting.
- Durable provider session replay or resumability beyond recording completed node/artifact state.
- Storing full prompt transcripts, model tokens, or provider-specific logs.
- Changing the current workflow YAML schema as part of persistence.
- Adding a database or NPM dependency for the initial implementation.

## Implementation Slices

Implementation should stay test-driven and split into small beads:

- Run store and index writer with lifecycle transition tests.
- Artifact manifest writer and summary integration tests.
- Retention cleanup API/command with dry-run and path-containment tests.
- Saved workflow/template filesystem store with validation and overwrite tests.
- Audit event writer plus authorization and cleanup event tests.
- Browser/API surfaces for history, artifacts, saved workflow/template management, and cleanup.
