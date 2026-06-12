# Operator Security Guide

Micromage is a local workflow operator for trusted users. The default simulated run mode is safe for previewing workflow shape, but real runs can execute `bash`, `command`, and provider-backed `prompt` nodes against the repository on disk.

## Trusted Environment Model

Run Micromage only in an environment where the operator, workflow definitions, local repository, configured credentials, and invoked tools are trusted together. A real workflow has the same practical authority as the operating-system user running `micromage`, plus any credentials available in that user's shell, config files, keychains, Git remotes, and provider CLIs.

Recommended boundaries:

- Use a dedicated OS user for production operation.
- Run against a dedicated working tree, not a personal checkout with unrelated credentials.
- Bind to `127.0.0.1` by default and reach it through SSH forwarding, a local browser, or an authenticated same-host reverse proxy.
- Use filesystem permissions that prevent other local users from reading `.micromage/`, `.env`, shell history, and provider config.
- Keep workflow YAML under review like code before enabling real mode.

Avoid these deployment shapes:

- Public internet exposure without an authenticating reverse proxy and TLS.
- Shared multi-user access to one Micromage process.
- Running as `root` or from a home directory that contains broad production credentials.
- Mounting a repository that contains secrets unrelated to the workflow.

## Real-Run Risk Model

Real mode is intentionally powerful. A workflow can:

- Read source, untracked files, local Git metadata, and files referenced by shell commands.
- Modify files, create commits, push branches, or open pull requests when local tools and credentials allow it.
- Write declared and incidental artifacts under `.micromage/runs/<run-id>`.
- Pass `$ARGUMENTS`, `$WORKFLOW_ID`, `$ARTIFACTS_DIR`, and `$node.output` values into downstream prompts or shell commands.
- Invoke the OpenCode CLI, which may use its own local provider configuration and storage.

Micromage adds guardrails, but they are not a sandbox:

- Real runs require `MICROMAGE_ENABLE_REAL_RUNS=1`.
- Real `/api/run` requests require a local Host/Origin, JSON content type, and `Authorization: Bearer <MICROMAGE_REAL_RUN_TOKEN>`.
- The server defaults to `127.0.0.1`.
- Request bodies, provider output streams, node logs, and artifact reads have size caps.
- Declared output artifacts are constrained to the run artifact directory before being read as downstream node output.

Those controls reduce accidental exposure and event-stream abuse. They do not prevent a trusted workflow from doing harmful work through shell commands, Git credentials, provider tools, or network-capable local CLIs.

## Credentials And Secrets

Use short-lived, least-privilege credentials for real workflows.

- Generate a fresh `MICROMAGE_REAL_RUN_TOKEN` for each operator session or deployment.
- Store secrets in environment variables, a local secret manager, or provider-specific config outside Git.
- Keep `.env` files local; they are ignored by the repository.
- Prefer repository-scoped GitHub tokens or deploy keys over personal tokens with broad account access.
- Avoid putting secrets in workflow YAML, `arguments`, prompt text, command output, or declared artifact files.
- Clear browser local storage for the Micromage origin when rotating `MICROMAGE_REAL_RUN_TOKEN`.

Example local startup:

```sh
export MICROMAGE_REAL_RUN_TOKEN="$(openssl rand -hex 24)"
MICROMAGE_ENABLE_REAL_RUNS=1 go run ./cmd/server
```

## Artifact Handling

Real runs write artifacts under:

```text
.micromage/runs/<run-id>
```

Treat `.micromage/` as private operational state. It can contain repository context, review findings, generated patches, model output, branch metadata, and other sensitive workflow evidence.

Operational practices:

- Keep `.micromage/` out of Git. The repository `.gitignore` already excludes it.
- Review artifacts before sharing logs, archives, screenshots, or bug reports.
- Back up `.micromage/` only to storage with the same sensitivity as the source repository.
- Delete run directories that contain secrets after incident review or when retention is no longer needed.
- Do not serve `.micromage/` through a static file server.

The persistence design in [docs/persistence-design.md](persistence-design.md) keeps future indexes and audit logs metadata-only where possible. Prompt bodies, bearer tokens, provider credentials, full node logs, and artifact contents should not be persisted into indexes or audit records.

## Recommended Production Settings

For a single trusted operator on the same host:

```sh
MICROMAGE_HOST=127.0.0.1 \
MICROMAGE_PORT=8080 \
MICROMAGE_READ_TIMEOUT=15s \
MICROMAGE_READ_HEADER_TIMEOUT=5s \
MICROMAGE_WRITE_TIMEOUT=30m \
MICROMAGE_IDLE_TIMEOUT=60s \
MICROMAGE_SHUTDOWN_TIMEOUT=10s \
./micromage
```

For guarded real runs:

```sh
MICROMAGE_HOST=127.0.0.1 \
MICROMAGE_PORT=8080 \
MICROMAGE_ENABLE_REAL_RUNS=1 \
MICROMAGE_REAL_RUN_TOKEN="$(openssl rand -hex 24)" \
MICROMAGE_WRITE_TIMEOUT=30m \
./micromage
```

Production guidance:

- Keep `MICROMAGE_HOST=127.0.0.1` unless the network boundary is explicitly trusted.
- Put TLS and user authentication in a reverse proxy if remote access is required.
- Keep `MICROMAGE_OPENCODE_UNSAFE` unset. Set it only for a controlled workflow that is expected to skip provider permissions.
- Set `MICROMAGE_BASE_BRANCH` when workflows should compare or target a specific branch.
- Leave generous `MICROMAGE_WRITE_TIMEOUT` values for long-running provider calls, but use node-level `timeout` and `idle_timeout` fields to bound individual workflow steps.
- Run one Micromage server process per working tree until concurrent durable persistence is implemented.

## Deployment Checklist

Before enabling real mode:

1. Build from a reviewed commit and run the quality gates in [docs/release.md](release.md).
2. Confirm the repository has no unrelated uncommitted secrets or generated files.
3. Install and authenticate required local tools, such as `git`, `gh`, and `opencode`, with least-privilege credentials.
4. Start Micromage with a fresh `MICROMAGE_REAL_RUN_TOKEN`.
5. Store the token only in the local browser origin or direct API client that needs it.
6. Run a simulated workflow first, then a real workflow against a disposable branch.
7. Inspect `.micromage/runs/<run-id>` and Git status after the run.

## Incident Response

If a token, artifact, or workflow output is exposed:

1. Stop the Micromage process.
2. Rotate `MICROMAGE_REAL_RUN_TOKEN` and any provider or Git credentials that may have been reachable.
3. Preserve the affected `.micromage/runs/<run-id>` directory if investigation is needed.
4. Review Git remotes, branches, commits, pull requests, and provider audit logs for unexpected actions.
5. Delete or quarantine sensitive artifacts after the investigation is complete.
