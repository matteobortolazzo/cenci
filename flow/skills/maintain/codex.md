# Codex maintain procedure

Read `project-core` and `codex-runtime`. Run `scripts/check.sh` for the deterministic layer, then
audit structure, documentation, and client-portability drift with read-heavy workers. Present the
findings and proposed repairs in `/plan`; after approval apply them in an isolated worktree,
re-run `scripts/check.sh`, and open one reviewed PR.
