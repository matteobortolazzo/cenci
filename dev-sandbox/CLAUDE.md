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
agent-sand --build-base   # agent-sandbox-base:<plugin.json version>, rebuild if Dockerfile.base changes
agent-sand --build        # agent-sandbox:latest, builds the base first if missing
shellcheck dev-sandbox/entrypoint.sh dev-sandbox/agent-sand dev-sandbox/tests/*.test.sh
bash -n dev-sandbox/entrypoint.sh dev-sandbox/agent-sand
bash dev-sandbox/tests/smoke.test.sh   # runtime smoke test; self-skips without docker/podman
```

## Conventions
- Keep the image minimal; bake tools into the image rather than bind-mounting from the host.
- `entrypoint.sh` must stay POSIX-portable and pass `shellcheck`.
- The container is the security boundary — Claude Code's host sandbox stays disabled inside it.

## Testing

- **Guard clauses must be mirrored across duplicated logic.** When the same validation/parsing pattern appears in both a test file and its corresponding production script (e.g., `smoke.test.sh` and `agent-sand` both resolve a version string with jq + sed fallback), the error-handling guards must be duplicated too. A test file that is more careful than the production code it exercises is a code-review red flag — check for silent failures (e.g., empty Docker tags, null strings propagating downstream).

- **Preserve all pre-existing logic when splitting a script into conditional branches.** When refactoring a script to introduce a new conditional branch (e.g., adding git-based per-repo scoping alongside a legacy non-git fallback), the fallback branch can appear "unchanged" in the diff while actually being accidentally simplified during the rewrite (e.g., dropping a computed value and hardcoding a default). Test both branches independently to catch silent failures.

## Image architecture: base + fragments
`Dockerfile.base` builds the stack-agnostic `agent-sandbox-base:<version>` image (Ubuntu,
system packages, locale, `uv`, GitHub CLI, Docker CLI, non-root `dev` user, entrypoint — no
language runtimes). `Dockerfile` (the monolith) builds `agent-sandbox:latest` `FROM`
that base image and layers on the runtime stacks (.NET, Node, Codex, Go).

`fragments/*.dockerfile` holds the same per-stack blocks (`dotnet`, `node`, `go`, `python`,
`rust`) as standalone snippets, for a future composition tool that assembles per-project
images. **Invariant:** until that composition tool exists, each fragment and its
corresponding block in `Dockerfile` must stay byte-identical — hand-duplicated on every
change (e.g. bumping `DOTNET_SDK_VERSION` or adding a package to a stack block means
editing both `Dockerfile` and `fragments/<stack>.dockerfile` identically). `Codex` is the
one exception: it's monolith-only (no fragment), since it isn't part of the composable
per-project stack set.

## Security
- Never bake secrets or credentials into the image layers.
- Validate any host paths mounted into the container.

## Reference Docs
Repo-level conventions live at `<repo-root>/docs/` (read on demand). Project-specific notes belong in this file.
