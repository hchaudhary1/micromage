# Micromage

Micromage is a Go-only workflow orchestrator for running AI-oriented development tasks as isolated, observable command pipelines.

## Current CLI

Validate a workflow:

```bash
go run ./cmd/micromage validate --workflow testdata/workflows/smoke.yaml
```

Run a workflow and write JSONL events:

```bash
go run ./cmd/micromage run --workflow testdata/workflows/smoke.yaml --log .micromage/run.jsonl
```

Use `type: command` nodes for shell snippets or direct executables with `args`. Use `type: human_gate` to pause the pipeline for reviewer approval. Add `depends_on` to express node dependencies; independent nodes in the same dependency layer run concurrently.

Run agent/template nodes through OpenCode:

```bash
go run ./cmd/micromage run \
  --workflow assets/defaults/workflows/micromage-assist.yaml \
  --runner provider \
  --provider opencode \
  --model opencode/deepseek-v4-flash-free \
  --arguments "review the last commit"
```

Provider presets render deterministic noninteractive argv/env settings for supported AI CLIs. OpenCode is the default provider and Codex is available with `--provider codex`; override discovery with `--provider-binary /path/to/cli` when a binary is outside `PATH`. Antigravity is only reported by discovery when an `antigravity` CLI is installed locally.

Micromage ships with 20 default workflows under `assets/defaults/workflows`
and 36 command templates under `assets/defaults/commands`. Workflows in this
layout infer the sibling command-template directory automatically. Override
with `--command-dir` when needed.

## Quality

```bash
go test ./... -cover
```

Install the repository pre-commit hook:

```bash
cp scripts/hooks/pre-commit .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit
```

The hook rejects staged files containing banned AI attribution terms and runs the Go coverage gate at the project threshold of 70%. Run the same gate directly with:

```bash
go run ./cmd/micromage quality pre-commit --repo . --threshold 70
```

Run the hook smoke test without making a commit:

```bash
./scripts/test-pre-commit-hook.sh
```

Complex migrated workflow fixtures live under `testdata/workflows/complex`.
They cover command templates, script-like commands, dependency fanout, loops,
conditions, and human gates with expected validation diagnostics.

Run the opt-in live OpenCode harness with the default free model:

```bash
MICROMAGE_OPENCODE_LIVE=1 \
MICROMAGE_OPENCODE_MODEL=opencode/deepseek-v4-flash-free \
go test -tags opencode_live ./internal/testharness -run TestLiveOpenCodeHarnessProducesEventLog -count=1 -v
```

Run the opt-in live OpenCode smoke suite for the bundled defaults:

```bash
MICROMAGE_OPENCODE_LIVE=1 \
MICROMAGE_OPENCODE_MODEL=opencode/deepseek-v4-flash-free \
go test ./internal/engine -run TestLiveOpenCodeSmokeForReferenceDefaults -count=1 -v
```
