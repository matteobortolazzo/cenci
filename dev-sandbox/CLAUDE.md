# Project: agent-sandbox

Docker/Podman container project within the agent-stack monorepo.
Provides an isolated container (`agent-sand`) for running Claude Code sessions with
`--dangerously-skip-permissions` — the container is the security boundary.

## Stack
- Docker / Podman (Containerfile / Dockerfile)
- Shell scripts (`entrypoint.sh`, helpers)
- Tests: `shellcheck` (static analysis), manual container smoke tests

## Build & Test
```bash
agent-sand --build-base   # agent-sandbox-base:<content-hash of Dockerfile.base + entrypoint.sh + lib/> + :latest alias, rebuild if those inputs change
agent-sand --build        # agent-sandbox:latest, builds the base first if missing
agent-sand --prune        # remove superseded base tags, dangling images, stopped *-sand-* containers (--volumes to also prompt for stale home volumes)
shellcheck dev-sandbox/entrypoint.sh dev-sandbox/agent-sand dev-sandbox/tests/*.test.sh
bash -n dev-sandbox/entrypoint.sh dev-sandbox/agent-sand
bash dev-sandbox/tests/smoke.test.sh   # runtime smoke test; self-skips without docker/podman
```

## Conventions
- Keep the image minimal; bake tools into the image rather than bind-mounting from the host.
- `entrypoint.sh` must stay POSIX-portable and pass `shellcheck`.
- The container is the security boundary — Claude Code's host sandbox stays disabled inside it.

## Entrypoint patterns

- **Never self-remap the UID/GID of a running account via `usermod`/`groupmod`.** A live account's own UID/GID cannot be renumbered while any process (including the calling shell) runs under that UID — the utilities refuse the operation. Container entrypoints that need to remap their own running user (e.g., to match host UID for mounted volumes) must start privileged (root) with zero workload processes yet alive, perform the remap safely, then unconditionally drop privileges to the target user before the rest of the entrypoint runs. Do not try to self-remap from within the account being changed.

- **`docker run --user X` persists for the container's lifetime — it is not scoped to the initial process.** The `--user` flag is stored as the container's `Config.User` and becomes the default for every subsequent `docker exec` call that doesn't pass its own explicit `-u` flag. A pattern that starts a container as root for setup (privilege-drop), but then runs workload via `docker exec`, will silently run all exec calls as root if they omit `-u <target-user>`. The image's original `USER dev` directive from the Dockerfile does not automatically restore as the default for exec — you must audit and add explicit `-u dev` to every exec call site after a privilege-drop entrypoint.

## Testing

- **Guard clauses must be mirrored across duplicated logic.** When the same validation/parsing pattern appears in both a test file and its corresponding production script (e.g., `smoke.test.sh` and `agent-sand` both resolve a version string with jq + sed fallback), the error-handling guards must be duplicated too. A test file that is more careful than the production code it exercises is a code-review red flag — check for silent failures (e.g., empty Docker tags, null strings propagating downstream).

- **Preserve all pre-existing logic when splitting a script into conditional branches.** When refactoring a script to introduce a new conditional branch (e.g., adding git-based per-repo scoping alongside a legacy non-git fallback), the fallback branch can appear "unchanged" in the diff while actually being accidentally simplified during the rewrite (e.g., dropping a computed value and hardcoding a default). Test both branches independently to catch silent failures.

- **Under `pipefail`, a terminal-stage filter or loop that always succeeds masks upstream failures.** Pipelines ending with `grep ... || true` or `while read` (both always exit 0) will hide real failures from earlier stages — e.g., `docker ps` failing silently appears as "nothing found." Always capture command output into a variable and check that command's exit status explicitly before filtering or looping on the captured value.

## Image architecture: base + fragments
`Dockerfile.base` builds the stack-agnostic `agent-sandbox-base:<content-hash>` image
(plus an `agent-sandbox-base:latest` alias tag), where `<content-hash>` is a 12-char
digest of `Dockerfile.base` + `entrypoint.sh` + `lib/` (Ubuntu, system packages, locale,
`uv`, GitHub CLI, Docker CLI, non-root `dev` user, entrypoint — no language runtimes).
`Dockerfile` (the monolith) builds `agent-sandbox:latest` `FROM` that base image and
layers on the runtime stacks in order: .NET, Node, Go, then Codex last (Codex bumps
daily via the deps-bump workflow, so keeping it last avoids invalidating the other
stacks' layer cache on every bump).

`fragments/*.dockerfile` holds the same per-stack blocks (`dotnet`, `node`, `go`, `python`,
`rust`) as standalone snippets, for a future composition tool that assembles per-project
images. **Invariant:** until that composition tool exists, each fragment and its
corresponding block in `Dockerfile` must stay byte-identical — hand-duplicated on every
change (e.g. bumping `DOTNET_SDK_VERSION` or adding a package to a stack block means
editing both `Dockerfile` and `fragments/<stack>.dockerfile` identically). `Codex` is the
one exception: it's monolith-only (no fragment), since it isn't part of the composable
per-project stack set.

## Dependency version pins
Image dependency versions are pinned via Dockerfile `ARG`s. Two policies:

- **Auto-bumped** by `.github/workflows/deps-bump.yml` (daily cron), one auto-merged PR per
  outdated dependency:
  - `CODEX_VERSION` — `Dockerfile` (monolith only).
  - `GO_VERSION` — `Dockerfile` **and** `fragments/go.dockerfile` (both stamped, kept in sync).
  - `UV_VERSION` — `Dockerfile.base`.
- **Manual pins** — deliberately NOT auto-bumped; update them by hand:
  - `DOTNET_SDK_VERSION` — `Dockerfile` (+ `fragments/dotnet.dockerfile`, byte-identical).
  - `NODE_MAJOR` — `Dockerfile` (+ `fragments/node.dockerfile`, byte-identical).

## Security
- Never bake secrets or credentials into the image layers.
- Validate any host paths mounted into the container.

## Reference Docs
Repo-level conventions live at `<repo-root>/docs/` (read on demand). Project-specific notes belong in this file.
