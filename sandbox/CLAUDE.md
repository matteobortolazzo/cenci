# Project: sandbox

Docker/Podman container project within the cenci monorepo.
Provides an isolated container (`cenci-sand`) for running Claude Code sessions with
`--dangerously-skip-permissions` — the container is the security boundary.

## Stack
- Docker / Podman (Containerfile / Dockerfile)
- Shell scripts (`entrypoint.sh`, helpers)
- Tests: `shellcheck` (static analysis), manual container smoke tests

## Build & Test
```bash
cenci-sand --build-base   # cenci-sandbox-base:<content-hash of Dockerfile.base + entrypoint.sh + lib/> + :latest alias, rebuild if those inputs change
cenci-sand --build        # cenci-sandbox:latest, builds the base first if missing
cenci-sand --prune        # remove superseded base tags, dangling images, stopped *-cenci-* containers (--volumes to also prompt for stale home volumes)
shellcheck sandbox/entrypoint.sh sandbox/cenci-sand sandbox/tests/*.test.sh
bash -n sandbox/entrypoint.sh sandbox/cenci-sand
bash sandbox/tests/smoke.test.sh   # runtime smoke test; self-skips without docker/podman
```

Host-runnable installer suites (mock PATH + fake HOME, no container needed):
```bash
bash sandbox/tests/install-update.test.sh        # daemon restart on update
bash sandbox/tests/installer-clients.test.sh     # client detection + launchers
bash sandbox/tests/cenci-widgets.test.sh         # GUI bar-widget detect/install/reload
bash sandbox/tests/reap-orphans.test.sh          # --reap-orphans scan/kill/escalation
```

## Conventions
- Keep the image minimal; bake tools into the image rather than bind-mounting from the host.
- `entrypoint.sh` must stay POSIX-portable and pass `shellcheck`.
- The container is the security boundary — Claude Code's host sandbox stays disabled inside it.

## Entrypoint patterns

- **Never self-remap the UID/GID of a running account via `usermod`/`groupmod`.** A live account's own UID/GID cannot be renumbered while any process (including the calling shell) runs under that UID — the utilities refuse the operation. Container entrypoints that need to remap their own running user (e.g., to match host UID for mounted volumes) must start privileged (root) with zero workload processes yet alive, perform the remap safely, then unconditionally drop privileges to the target user before the rest of the entrypoint runs. Do not try to self-remap from within the account being changed.

- **`docker run --user X` persists for the container's lifetime — it is not scoped to the initial process.** The `--user` flag is stored as the container's `Config.User` and becomes the default for every subsequent `docker exec` call that doesn't pass its own explicit `-u` flag. A pattern that starts a container as root for setup (privilege-drop), but then runs workload via `docker exec`, will silently run all exec calls as root if they omit `-u <target-user>`. The image's original `USER dev` directive from the Dockerfile does not automatically restore as the default for exec — you must audit and add explicit `-u dev` to every exec call site after a privilege-drop entrypoint.

## Testing

- **Guard clauses must be mirrored across duplicated logic.** When the same validation/parsing pattern appears in both a test file and its corresponding production script (e.g., `smoke.test.sh` and `cenci-sand` both resolve a version string with jq + sed fallback), the error-handling guards must be duplicated too. A test file that is more careful than the production code it exercises is a code-review red flag — check for silent failures (e.g., empty Docker tags, null strings propagating downstream).

- **Preserve all pre-existing logic when splitting a script into conditional branches.** When refactoring a script to introduce a new conditional branch (e.g., adding git-based per-repo scoping alongside a legacy non-git fallback), the fallback branch can appear "unchanged" in the diff while actually being accidentally simplified during the rewrite (e.g., dropping a computed value and hardcoding a default). Test both branches independently to catch silent failures.

- **Under `pipefail`, a terminal-stage filter or loop that always succeeds masks upstream failures.** Pipelines ending with `grep ... || true` or `while read` (both always exit 0) will hide real failures from earlier stages — e.g., `docker ps` failing silently appears as "nothing found." Always capture command output into a variable and check that command's exit status explicitly before filtering or looping on the captured value.

- **When a function is used as the condition of `if`/`while`, bash suspends `set -e` for its entire body.** This calling-convention gotcha means every command inside such a function whose failure matters must have its exit status explicitly captured and checked — do not rely on `set -e` to catch downstream failures. This differs from `pipefail` masking: it's about errexit suspension via calling convention, not pipeline status computation. Sibling instances of the same error pattern in a function (e.g., multiple `kill` escalations) may not all be caught in a single review pass—systematically sweep the entire function body for the same pattern when fixing one instance.

## Image architecture: base + fragments
`Dockerfile.base` builds the stack-agnostic `cenci-sandbox-base:<content-hash>` image
(plus an `cenci-sandbox-base:latest` alias tag), where `<content-hash>` is a 12-char
digest of `Dockerfile.base` + `entrypoint.sh` + `lib/` (Ubuntu, system packages, locale,
`uv`, GitHub CLI, Docker CLI, non-root `dev` user, entrypoint — no language runtimes).
`Dockerfile` (the monolith) builds `cenci-sandbox:latest` `FROM` that base image and
layers on the runtime stacks in order: .NET, Node, Playwright, Go, then Codex last (Codex
bumps daily via the deps-bump workflow, so keeping it last avoids invalidating the other
stacks' layer cache on every bump).

`fragments/*.dockerfile` holds the same composable blocks (`dotnet`, `node`, `playwright`,
`go`, `python`, `rust`, `codex`) as standalone snippets used to assemble per-project images.
Generated images always include Node and Codex so either supported agent can launch; the
remaining fragments (including `playwright`, used for `verify-ui`'s Chromium screenshot
capture) follow the detected project stack. **Invariant:** each fragment and its
corresponding block in `Dockerfile` must stay byte-identical — hand-duplicated on every
change (e.g. bumping `DOTNET_SDK_VERSION` or adding a package to a stack block means
editing both `Dockerfile` and `fragments/<stack>.dockerfile` identically).

## Dependency version pins
Image dependency versions are pinned via Dockerfile `ARG`s, all checked daily by
`.github/workflows/deps-bump.yml`. Three tiers, by breaking-change risk:

- **Auto-bumped, auto-merged** — one auto-merged PR per outdated dependency, then the
  cenci-sandbox rebuild is dispatched once the merge lands:
  - `CODEX_VERSION` — `Dockerfile` **and** `fragments/codex.dockerfile` (both stamped, kept in sync).
  - `GO_VERSION` — `Dockerfile` **and** `fragments/go.dockerfile` (both stamped, kept in sync).
  - `UV_VERSION` — `Dockerfile.base`.
- **Auto-proposed, in-band auto-merges / out-of-band opens a manual-merge PR**:
  - `DOTNET_SDK_VERSION` — `Dockerfile` (+ `fragments/dotnet.dockerfile`, byte-identical). A
    patch bump within the currently-pinned major.minor band auto-merges like the tier above.
    A newer GA feature band or major instead opens a PR left for manual review, manual
    merge, and a manual `gh workflow run "sandbox — Version Bump"`.
- **Auto-proposed, always manual-merge**:
  - `NODE_MAJOR` — `Dockerfile` (+ `fragments/node.dockerfile`, byte-identical). Only
    proposed once the currently-pinned major's LTS support has ended; the PR is never
    auto-merged — always reviewed, merged, and rebuild-dispatched by hand.
- **Manual only (not yet wired into `deps-bump.yml`)**:
  - `PLAYWRIGHT_VERSION` — `Dockerfile` (+ `fragments/playwright.dockerfile`,
    byte-identical). Bump by hand and rebuild; add it to the auto-bumped tier above in a
    follow-up if it proves stable enough to auto-merge like Codex/Go/uv.

## Security
- Never bake secrets or credentials into the image layers.
- Validate any host paths mounted into the container.
- Bind-mount host paths read-only (`:ro`) unless the container genuinely needs write access — containers should be as restrictive as possible. Audit all new and existing mounts in `cenci-sand` against this principle.

## Reference Docs
Repo-level conventions live at `<repo-root>/docs/` (read on demand). Project-specific notes belong in this file.
