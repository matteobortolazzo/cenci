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
- CLI grammar, alias, env-var, and runtime-object naming conventions live in `docs/cli-conventions.md` — read it before adding or changing any user-facing command surface.
- Local per-project health checks (`gateCommand` in `.cenci/config.json`) are documented in `docs/health-gates.md` — read it before adding or changing a project's gate, or before touching `babysit`/`ci-repair`'s pre-push verification.
- Test-first: integration tests that assert behavior, not implementation details.
- When implementing tests described in a plan or ticket, verify that each claimed test assertion is actually exercised by an explicit test case—plan descriptions are intent, not proof of coverage.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- ALWAYS work in a git worktree — for any change (code, docs, config), not just feature work. Never modify files in the main worktree.
- Deliver every change as a PR unless told otherwise: commit in the worktree, push the branch, open a PR. Never commit directly to main.
- Refactor phases must not override decisions the approved plan explicitly reasoned about — if renaming, reorganizing, or restructuring something the plan called out directly, that plan reasoning is binding unless refactoring reveals a fundamental error.
- Never use unchecked command substitution for security-critical paths (especially temp directories and config files) — explicitly verify command success before use; unchecked failures silently collapse to root-relative paths and undermine hardening.
- When implementing a pattern or check that already exists elsewhere in the same file or codebase (e.g., a grep check, a validation guard, error-handling convention), audit existing examples first to match established conventions — do not implement solely from a plan description. This includes error-handling specifics like stderr redirection; implementing a similar check without those details is a silent failure that a reviewer is more likely to catch than an implementer.
- When implementing a feature from a plan with Assumptions or Risks sections, cross-check that your literal implementation aligns with those sections' stated intent, not just the Files to Modify wording. If different sections contradict (e.g., intent says "empty label is not a drift case" but Files to Modify lists a condition that treats any mismatch as drift), flag the contradiction for clarification before committing — do not resolve it by choosing the most literal section (#493).
- When implementing a child ticket within a parent epic that specifies 'Key design decisions' or similar intent-setting context, verify that documentation examples and placeholder values align with that parent epic's stated purpose — do not rely solely on generic plausibility (e.g., HTTP endpoints, standard test frameworks) that might contradict the parent context (e.g., offline-only checks) (#505).

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
