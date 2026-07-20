# cenci

One cenci product implemented as a monorepo of client plugins and development tooling.
GitHub Issues for tracking. GitHub for code and PRs.

## Projects

- `flow/` — workflow layer: Claude Code pipeline plus portable Codex conventions
- `watch/` — attention layer: Go daemon and native Claude Code/Codex hooks
- `sandbox/` — isolation layer: Docker/Podman launcher for Claude Code and Codex

Each project has its own `AGENTS.md` with project-specific context.

## Critical Rules
- ALWAYS read the relevant project's `AGENTS.md` — plus its `.claude/rules/` files where they exist — before working on any layer.
- Read the relevant topic doc before touching its area: `docs/cli-conventions.md` for CLI grammar, alias, env-var, and runtime-object naming on any user-facing command surface; `docs/health-gates.md` for per-project `gateCommand` health checks, before adding/changing a project's gate or touching `babysit`/`ci-repair`'s pre-push verification.
- Test-first: integration tests that assert behavior, not implementation details.
- When implementing tests described in a plan or ticket, verify that each claimed test assertion is actually exercised by an explicit test case—plan descriptions are intent, not proof of coverage.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- ALWAYS work in a git worktree — for any change (code, docs, config), not just feature work. Never modify files in the main worktree.
- Deliver every change as a PR unless told otherwise: commit in the worktree, push the branch, open a PR. Never commit directly to main.
- Refactor phases must not override decisions the approved plan explicitly reasoned about — if renaming, reorganizing, or restructuring something the plan called out directly, that plan reasoning is binding unless refactoring reveals a fundamental error.
- Never use unchecked command substitution for security-critical paths (especially temp directories and config files) — explicitly verify command success before use; unchecked failures silently collapse to root-relative paths and undermine hardening.
- When implementing from a plan or ticket, cross-check literal implementation against every section's stated intent — not just the Files to Modify wording — per `docs/plan-fidelity.md`.

## Build & Test

### watch
- Build: `cd watch && make build`
- Test: `cd watch && make test` or `cd watch && go test ./...`
- Lint: `cd watch && make lint`

### flow
- No build step (markdown/shell plugin)

### sandbox and installer
- Syntax: `bash -n install.sh sandbox/entrypoint.sh`
- Tests: `bash sandbox/tests/installer-clients.test.sh`, `bash sandbox/tests/agent-cli.test.sh`, and the other host-runnable suites in `sandbox/tests/`

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
- `docs/git-workflow.md` — branching, commits, PRs, versioning
- `docs/cli-conventions.md` — CLI grammar, alias, env-var, and runtime-object naming
- `docs/health-gates.md` — per-project local health gates (`gateCommand`), consumed by the implement pipeline's baseline check and by `babysit`/`ci-repair`
- `docs/plan-fidelity.md` — cross-checking an implementation against a plan or ticket's full stated intent, not just its Files to Modify wording
- `docs/error-codes.md` — the `CENCI-<AREA>-<SUBAREA>-<NNN>` structured error-identifier naming convention and registered-code index
