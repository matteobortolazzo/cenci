# Contributing

Thanks for looking at cenci. This repo contains one product implemented as three
independently versioned internal plugins—read the relevant layer's `CLAUDE.md` before making changes, and
see the root [`CLAUDE.md`](./CLAUDE.md) and [`README.md`](./README.md) for the overall
architecture.

## Dev setup per project

- **watch** (Go): install the Go toolchain per [`watch/CLAUDE.md`](./watch/CLAUDE.md),
  then from `watch/`:
  ```bash
  make build
  make test    # or: go test ./...
  make lint    # requires golangci-lint
  ```
- **sandbox** (shell): install [`shellcheck`](https://www.shellcheck.net/), then:
  ```bash
  shellcheck install.sh sandbox/entrypoint.sh \
    sandbox/lib/*.sh sandbox/tests/*.test.sh
  bash -n install.sh sandbox/entrypoint.sh
  ```
  See [`sandbox/CLAUDE.md`](./sandbox/CLAUDE.md) for the full build/test/smoke
  commands.
- **flow** (markdown/JSON/shell): no build step. Skills and docs are plain
  Markdown; hooks are shell scripts and should still pass `shellcheck`/`bash -n`. See
  [`flow/CLAUDE.md`](./flow/CLAUDE.md).

## Workflow: worktrees, 1 ticket = 1 PR

All work — code, docs, or config — happens in a git worktree, never directly in the
main worktree:

```bash
git worktree add .worktrees/<id>-<desc> -b feature/<id>-<desc>
```

One ticket maps to one PR targeting `main`. Multiple commits within a PR are fine; use
them to organize logical steps. Full conventions (branch naming, commit format) live in
[`flow/docs/git-workflow.md`](./flow/docs/git-workflow.md).

## Conventional commits and release impact

Commit — and PR title, since PRs are squash-merged — using
[conventional commits](https://www.conventionalcommits.org/): `<type>(<scope>): <description>`.
The **type of the commit that lands on `main`, in the paths it touches,** drives an
automated version bump for the affected plugin(s). The mapping (derived from the
version-bump workflows; see the
[Versioning section in `CLAUDE.md`](./CLAUDE.md#versioning) for the per-plugin
path/tag list):

| Commit prefix | Version bump | Example |
|---|---|---|
| `feat` | minor (`1.x.0`) | New skill, new agent |
| `feat!:` / `BREAKING CHANGE` footer | major (`x.0.0`) | Removed or renamed skill |
| `fix`, `refactor`, `test`, `docs`, `chore` | patch (`1.0.x`) | Bug fix, cleanup, docs |

Each plugin versions independently, gated by which paths a merge touches:

| Plugin | Paths | Tag |
|---|---|---|
| flow | `flow/**` | `flow/vX.Y.Z` |
| watch | `watch/**` | `watch/vX.Y.Z` |
| sandbox | `sandbox/**` | `sandbox/vX.Y.Z` |

A PR that only touches, say, `docs/` or root-level files (like this one) doesn't trigger
any plugin version bump.

## CI layout

Workflows live in [`.github/workflows/`](./.github/workflows/):

- **`flow-version-bump.yml`**, **`watch-version-bump.yml`**,
  **`sandbox-version-bump.yml`** — run on push to `main`, path-filtered per
  plugin. Each reads the commit that triggered it, computes the bump from the mapping
  above, updates that plugin's `plugin.json`(s) and the root `marketplace.json`, commits
  as `chore(release): <plugin>/v<new>`, and tags it. They skip when the actor is
  `github-actions[bot]` (sandbox's also allows `workflow_dispatch` for manual
  re-runs) and when the last commit is already a release commit, to avoid bump loops.
- **`watch-ci.yml`** — `go test`, `go build`, and `golangci-lint` on push/PR
  touching `watch/**`.
- **`watch-release.yml`** — builds and publishes release artifacts with GoReleaser
  when a `watch/v*` tag is pushed (or via `workflow_dispatch`), triggered by the
  version-bump job.
- **`sandbox-ci.yml`** (workflow name `sandbox — CI`) — shellcheck/`bash -n` lint,
  the host-runnable test suites, the fragment-drift guard, a full build + toolchain
  smoke test, and hadolint, on push/PR touching `sandbox/**` or `install.sh`.
- **`deps-bump.yml`** — a daily scheduled job that checks pinned sandbox dependencies,
  opens scoped update PRs, and applies the documented auto/manual merge policy.

## Marketplace and plugin versioning

Each plugin's `.claude-plugin/plugin.json` is the **source of truth** for its version:

- `flow/.claude-plugin/plugin.json`
- `watch/plugin/.claude-plugin/plugin.json`
- `sandbox/.claude-plugin/plugin.json`

A version-bump workflow updates that file, the matching `.codex-plugin/plugin.json` (so
Claude Code and Codex manifests stay in sync), and the plugin's entry in the root
[`.claude-plugin/marketplace.json`](./.claude-plugin/marketplace.json) — the single
catalog both `claude plugin marketplace add` and `codex plugin marketplace add` read
from. The resulting commit is tagged `<plugin>/vX.Y.Z` (see the table above). You don't
need to hand-edit any of these files — the workflow does it as part of the merge that
triggers a bump.

## Questions

Repo-wide conventions live in [`CLAUDE.md`](./CLAUDE.md); user-facing setup and
architecture are in [`README.md`](./README.md). For anything security-sensitive, see
[`SECURITY.md`](./SECURITY.md).
