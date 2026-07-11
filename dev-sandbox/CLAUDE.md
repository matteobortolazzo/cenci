# dev-sandbox

Docker/Podman container project within the agent-stack monorepo.
Provides an isolated container (`agent-sand`) for running Claude Code sessions with
`--dangerously-skip-permissions` — the container is the security boundary.

## Stack
- Docker / Podman (Containerfile / Dockerfile)
- Shell scripts (`entrypoint.sh`, helpers)
- Tests: `shellcheck` (static analysis), manual container smoke tests

## Build & Test
```bash
docker build -t agent-sand dev-sandbox/
shellcheck dev-sandbox/entrypoint.sh
bash -n dev-sandbox/entrypoint.sh
```

## Conventions
- Keep the image minimal; bake tools into the image rather than bind-mounting from the host.
- `entrypoint.sh` must stay POSIX-portable and pass `shellcheck`.
- The container is the security boundary — Claude Code's host sandbox stays disabled inside it.

## Security
- Never bake secrets or credentials into the image layers.
- Validate any host paths mounted into the container.

## Reference Docs
Repo-level conventions live at `<repo-root>/docs/` (read on demand). Project-specific notes belong in this file.
