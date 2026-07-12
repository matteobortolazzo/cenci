# Security

## Threat model

**The container is the security boundary.** [`agent-sandbox`](./dev-sandbox) runs Claude
Code with `--dangerously-skip-permissions` and Codex with
`--dangerously-bypass-approvals-and-sandbox` — both agents run fully unattended, with no
per-command approval prompts. That's only safe because isolation moves from the agent's
own permission system to the container: the agent can do anything *inside* the
container, but the container is what stands between it and your host. See
[`dev-sandbox/CLAUDE.md`](./dev-sandbox/CLAUDE.md) and
[`dev-sandbox/README.md`](./dev-sandbox/README.md#permission-model) for the full model.

### What the sandbox protects against

- Arbitrary file writes/deletes outside the mounted repo — only the current repo's root
  is mounted at `/workspace` (not your whole `~/Repos`), and the container has its own
  home directory backed by a named volume, not your host `~/`.
- Host process/credential access — the agent cannot see host processes, host
  environment variables (beyond what's explicitly forwarded), or host files outside the
  mount points documented in the [dev-sandbox README](./dev-sandbox/README.md#what-persists-home-volume).
- Inbound network exposure — the container publishes no inbound ports; network access is
  outbound-only by default.

### What it does NOT protect against

- **Anything the mounted repo's build/test/tooling can do on your behalf inside the
  container** — e.g. a malicious `package.json` script or Makefile target run by the
  agent still executes with full container privileges. The container boundary limits
  *blast radius to the container*, not what happens within it.
- **Supply-chain risk in what you mount or install** — the sandbox doesn't vet
  dependencies, MCP servers, or plugins you add.
- **The two opt-in flags below**, which deliberately widen the boundary.

### Opt-in weakenings

Both of these are off by default and must be explicitly requested per-launch:

- **`--host-network`** — switches the container to host networking, needed for OAuth
  flows that require a browser callback to `localhost`. This removes the container's
  network isolation from the host for the life of that session. Prefer the default
  (bridged) network and only reach for `--host-network` when a login flow fails without
  it. A louder, more visible warning when this flag is used is tracked in
  [#148](https://github.com/matteobortolazzo/agent-stack/issues/148) (not yet landed).
- **`--docker`** — mounts the host Docker/Podman socket into the container
  (Docker-outside-of-Docker), for TestContainers, `docker build`, and similar. Any
  container started from inside the sandbox with this flag runs on the **host** daemon,
  with full Docker privileges on the host — this is a meaningfully bigger blast radius
  than the default sandbox and is why it's opt-in. See
  [dev-sandbox/README.md#docker-optional-opt-in](./dev-sandbox/README.md#docker-optional-opt-in).

### Credentials

Host credentials (`~/.claude/.credentials.json`, `~/.codex/auth.json`,
`~/.config/gh/hosts.yml`) are bind-mounted read-only into a staging path and copied into
the container's **named home volume** on first start — not baked into any image layer.
That volume persists across container restarts (by design, so you don't re-auth every
session) but also means copied credentials live on disk in the volume until you remove
it.

To remove a volume and everything copied into it:

```bash
docker volume rm claude-sand-home-<repo-slug>
# or, for Codex sessions:
docker volume rm codex-sand-home-<repo-slug>
```

See [dev-sandbox/README.md#reset-an-instance](./dev-sandbox/README.md#reset-an-instance)
for the full naming scheme (per-repo slug, `--name` suffix, legacy `-default` volumes).
A single `agent-sand --prune --volumes` convenience command that sweeps all sandbox
volumes at once is planned ([#148](https://github.com/matteobortolazzo/agent-stack/issues/148))
but not available yet — use `docker volume ls --filter name=sand-home` plus the manual
`docker volume rm` commands above in the meantime.

## Reporting a vulnerability

Email **matteobortolazzo@pm.me** with details — this is the reliable channel today.
Please include:

- Which layer/plugin is affected (agentflow, agentwatch, agent-sandbox)
- Steps to reproduce, and the impact you believe it has
- Whether the issue is exploitable from outside the container boundary described above,
  or only from within an already-fully-privileged agent session

GitHub's private vulnerability reporting will be enabled for this repository as part of
ongoing public-release hygiene work; once it is, that becomes the preferred channel and
this section will be updated accordingly. Until then, email is the dependable path — please
do not open a public issue for a suspected vulnerability.
