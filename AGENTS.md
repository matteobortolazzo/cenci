# cenci

One cenci product implemented as a monorepo of client plugins and development tooling.
GitHub Issues for tracking. GitHub for code and PRs.

## Projects

- `flow/` — workflow layer: Claude Code pipeline plus portable Codex conventions
- `watch/` — attention layer: Go daemon and native Claude Code/Codex hooks
- `sandbox/` — isolation layer: Docker/Podman launcher for Claude Code and Codex

Each project has its own `AGENTS.md` with project-specific context — ALWAYS read the relevant project's `AGENTS.md` before working on any layer.

## Critical Rules
- Test-first: integration tests that assert behavior, not implementation details.
- When implementing tests described in a plan or ticket, verify that each claimed test assertion is actually exercised by an explicit test case—plan descriptions are intent, not proof of coverage.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- ALWAYS work in a git worktree — for any change (code, docs, config), not just feature work. Never modify files in the main worktree.
- Deliver every change as a PR unless told otherwise: commit in the worktree, push the branch, open a PR. Never commit directly to main.
- Never use unchecked command substitution for security-critical paths (especially temp directories and config files) — explicitly verify command success before use; unchecked failures silently collapse to root-relative paths and undermine hardening.
- When implementing from a plan or ticket, cross-check literal implementation against every section's stated intent — not just the Files to Modify wording — per `docs/plan-fidelity.md`.
- Never report CI green from `conclusion` values — a running check's conclusion is empty, so a conclusion-only scan reads "still running" as "passed" (#900). Read `gh pr checks <pr> --json bucket,name,state`: green needs every bucket `pass`; name any `pending`. See `shell-rules`.
- When a condition chain validates a config field, verify the subsequent action actually consumes that validated value — don't hardcode the action after checking structure. Validation gates only gate; wiring the value in is separate and can silently disconnect (#824).

## Build & Test

Each project documents its own build/test/lint commands in its `AGENTS.md`:

- flow: `flow/AGENTS.md`
- watch: `watch/AGENTS.md`
- sandbox: `sandbox/AGENTS.md`

Root-level only, not covered by any project: `bash -n install.sh`

## Versioning

Each plugin versions independently:
- flow: auto-bumped on push to main (paths: `flow/**`), tags: `flow/v*`
- watch: auto-bumped on push to main (paths: `watch/**`), tags: `watch/v*`
- sandbox: auto-bumped on push to main (paths: `sandbox/**`), tags: `sandbox/v*`

## Sandbox Image

- `.cenci/Dockerfile` — committed, single per-repo image covering the union of every project's stack; the whole team builds the same image
- Rebuild after changing any project's stack or the Dockerfile: `cenci sandbox build` (run from inside this repo)

## Reference Docs

On-demand topic docs live in `docs/` at the repo root. Read the file matching your work area:
- `docs/autonomous-loop.md` — the user-facing guide to the four opt-in autonomy switches (lean planning, `dispatch.planRefined`, the `automerge` policy block, `automerge.enabled`), their quick start, and the automerge condition-chain legend; read before changing any of those switches' user-visible behavior so the guide stays in sync
- `docs/git-workflow.md` — branching, commits, PRs, versioning
- `docs/cli-conventions.md` — CLI grammar, alias, env-var, and runtime-object naming; read before touching any user-facing command surface
- `docs/health-gates.md` — per-project local health gates (`gateCommand`), consumed by the implement pipeline's baseline check and by `babysit`/`ci-repair`; read before adding or changing a project's gate or touching `babysit`/`ci-repair`'s pre-push verification
- `docs/plan-fidelity.md` — cross-checking an implementation against a plan or ticket's full stated intent, not just its Files to Modify wording, including refactor phases that would override plan decisions
- `docs/error-codes.md` — the `CENCI-<AREA>-<SUBAREA>-<NNN>` structured error-identifier naming convention and registered-code index
- `docs/pipeline-coverage-map.md` — the committed acceptance-criterion/doc-claim-to-named-test coverage map for the `#661` idempotent-refine/autonomous-chain split, enforced by `flow/scripts/run-checks.sh`'s coverage-map sync check
- `docs/github-actions.md` — conventions and gotchas for authoring `.github/workflows/*.yml`, including the `dorny/paths-filter` negation/quantifier rule enforced by `check-paths-filters.sh`; read before touching any workflow's `paths-filter` step
- `docs/selective-adoption.md` — the supported per-repo configuration that runs cenci-watch + cenci-sandbox with the flow plugin disabled, its `permissions.deny` replacement for `check-sensitive-files.sh`, and the residual gaps; read before changing per-repo plugin-enablement guidance or the sensitive-file guard
