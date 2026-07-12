# GitHub Actions Workflow Authoring

Conventions and gotchas for authoring `.github/workflows/*.yml` files in this monorepo.

## Rules

- **Errexit is ON before your `set` line runs** — GitHub Actions' default Linux shell is `bash --noprofile --norc -eo pipefail {0}`, so errexit (`-e`) is already active when your script starts. A `set -uo pipefail` line only adds nounset; it does NOT clear the inherited `-e`. If a workflow `run:` block intends manual/continue-on-error handling (e.g., accumulate failures across multiple checks and exit at the end), add an explicit `set +e` after `set -uo pipefail` to disable errexit. Without it, the first unguarded command failure will abort the whole step and skip remaining checks. (Note: Bash exempts a function body from errexit only when the function call is the left operand of `||`, e.g. `fn ... || FAILED=1`, so that specific pattern works even under `-e`, but explicit `set +e` is clearer for step-level error handling.)

- **Copy all validation steps when adding a new per-dependency section to `deps-bump.yml`** — The workflow validates externally-fetched version strings both for null/empty (basic guard) and for format (e.g., `grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'` before using in `sed`, git branch names, or version comparisons). When adding a new dependency section, copy the complete validation pattern from an existing section, including both null/empty checks AND format-validation regexes. Omitting format validation allows malformed upstream values to slip through, creating silent-merge hazards. During PR review, check that all validation steps are mirrored across new and existing sections.
