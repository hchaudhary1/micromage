---
description: Sync the PR branch with the base branch.
---
# Sync PR

Use `$ARTIFACTS_DIR/review/scope.md` to identify the PR branch and base. Fetch the base branch, rebase only when needed, resolve conflicts conservatively, rerun targeted tests, and write `$ARTIFACTS_DIR/review/sync-report.md` when any sync work occurred.
