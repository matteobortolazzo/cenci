# Project: sandbox

Docker/Podman container project within the cenci monorepo.
Ships the isolation layer's image and runtime assets (Dockerfiles, fragments,
`entrypoint.sh`, container-side `lib/` scripts, skills) for running agent
sessions with `--dangerously-skip-permissions` — the container is the security
boundary. The launcher itself is the `cenci` Go binary (`cenci open` / `cn`,
`cenci sandbox <verb>`, in `watch/`); it resolves this project's assets from
the installed cenci-sandbox plugin.

## Stack
- Docker / Podman (Containerfile / Dockerfile)
- Shell scripts (`entrypoint.sh`, container-side helpers in `lib/`)
- Tests: `shellcheck` (static analysis), manual container smoke tests

## Build & Test
```bash
cenci sandbox build-base            # cenci-sandbox-base:<content-hash of Dockerfile.base + entrypoint.sh + lib/> + :latest alias, rebuild if those inputs change
cenci sandbox build                 # cenci-sandbox:latest, builds the base first if missing
cenci sandbox prune [--images] [--volumes]     # remove superseded base tags, dangling images, stopped *-cenci-* containers (--images also prompts for per-repo images; --volumes also prompts for stale home volumes; independent flags)
shellcheck sandbox/entrypoint.sh sandbox/lib/*.sh sandbox/tests/*.test.sh
bash -n sandbox/entrypoint.sh
bash sandbox/tests/smoke.test.sh   # runtime smoke test; self-skips without docker/podman
```

Host-runnable installer suites (mock PATH + fake HOME, no container needed):
```bash
bash sandbox/tests/install-update.test.sh        # daemon restart on update
bash sandbox/tests/installer-clients.test.sh     # client detection + launchers
bash sandbox/tests/cenci-widgets.test.sh         # GUI bar-widget detect/install/reload
bash sandbox/tests/settings-merge.test.sh        # lib/migrate-settings.sh deep-merge behavior
bash sandbox/tests/seed-auth.test.sh             # lib/seed-auth.sh credential seeding
bash sandbox/tests/codex-config.test.sh          # lib/codex-config.sh config generation
bash sandbox/tests/agent-cli.test.sh             # shared, verified, atomic agent update lifecycle
bash sandbox/tests/fragments-drift.test.sh       # Dockerfile vs fragments/*.dockerfile byte-parity
bash sandbox/tests/docker-fragment.test.sh       # Docker engine lives in the fragment, never in Dockerfile.base
bash sandbox/tests/heal-plugins.test.sh          # plugin self-heal (Write->Edit allow conversion)
bash sandbox/tests/dind.test.sh                  # lib/dind.sh start_dind() background dockerd + marker
bash sandbox/tests/uninstall.test.sh             # `install.sh uninstall` MODE (plugins, PATH links, daemon, sandbox cleanup)
bash sandbox/tests/lazyboards-install.test.sh    # optional lazyboards step: download, update refresh, doctor report
bash sandbox/tests/opencode-config.test.sh       # lib/opencode-config.sh opencode.json permission/plugin seeding
bash sandbox/tests/opencode-plugins.test.sh      # OpenCode cenci-src provisioning + TTL-gated refresh
bash sandbox/tests/setup-skill-content.test.sh   # sandbox/skills/setup/SKILL.md content accuracy
bash sandbox/tests/startup-marker.test.sh        # entrypoint.sh timestamped startup-failure marker prefix
bash sandbox/tests/remap-chown.test.sh           # lib/remap.sh home-volume re-own, skipping nested bind mounts
```

CI's `sandbox-test` job discovers and runs every `sandbox/tests/*.test.sh`
automatically, so a new suite is covered the moment it is added — do not
replace that discovery loop with an enumerated list. Only `fragments-drift`
and `smoke` are excluded there, because they own separate jobs.

Note: docs-only edits to `sandbox/AGENTS.md`, `sandbox/CLAUDE.md`, or
`sandbox/README.md` do **not** trigger the sandbox suite —
`sandbox-ci.yml`'s `sandbox` filter excludes them, an exclusion that only
became real once the filter moved to `predicate-quantifier: 'every'` (#950).

The launcher-behavior suites live with the launcher code in `watch/`: Go
black-box tests in `watch/sandbox_open_test.go` plus the reap contract suite
`watch/tests/reap-orphans.test.sh` (run with `CENCI_BIN`).

## Critical Rules
- Keep the image minimal; agent CLIs belong in host-global volumes mounted read-only in workloads. Only the isolated updater may mount them writable.
- `entrypoint.sh` must stay POSIX-portable and pass `shellcheck`.
- The container is the security boundary — Claude Code's host sandbox stays disabled inside it.
- When a check or filter depends on a shared helper with multiple conditional return branches (e.g. `find_plugin_path` in `install.sh`), audit every branch: a hardcoded substring match that fits one return shape silently false-negatives on the others (#491).
- Never bake secrets or credentials into the image layers.
- Validate any host paths mounted into the container.
- Bind-mount host paths read-only (`:ro`) unless the container genuinely needs write access — containers should be as restrictive as possible. Audit all new and existing mounts the launcher assembles (`watch/internal/sandbox/launcher`) against this principle.
- When porting a Go fallback chain to shell, don't test a concatenated fallback string for emptiness to detect an unresolved source — `result="${VAR:-$OTHER/path}"` is always non-empty. Test the source vars directly: `[ -n "${VAR:-}" ] || [ -n "${OTHER:-}" ]` (#1143).
- Normalize paths before exact-string blocklist comparisons in security-critical `rm -rf` guards — raw strings miscompare on trailing slashes/dots. Use checked `CDPATH= cd -P -- "$p" && pwd -P` (#1143).

## Image architecture: base + fragments
`Dockerfile.base` builds the stack-agnostic `cenci-sandbox-base:<content-hash>` image
(plus an `cenci-sandbox-base:latest` alias tag), where `<content-hash>` is a 12-char
digest of `Dockerfile.base` + `entrypoint.sh` + `lib/` (Ubuntu, system packages, locale,
`uv`, GitHub CLI, non-root `dev` user, entrypoint — no language runtimes, and no Docker
CLI or engine: those moved to the config-selected `docker` fragment in #831).
`Dockerfile` (the monolith) builds `cenci-sandbox:latest` `FROM` that base image and
layers on the runtime stacks in order: .NET, Node, Playwright, and Go. Agent CLIs are not
image layers: the launcher bootstraps absent `cenci-agent-cli-<agent>` volumes through a
credential-free updater, and workloads mount them read-only at `/opt/cenci-agent`.
`cenci sandbox update-agent` updates that global volume explicitly and atomically
(see "Dependency version pins" below for how `cenci update` now also refreshes it
automatically).
Credentials are still staged only into per-scope home volumes. Derived images (the
monolith and per-repo builds) are stamped with a `cenci.base-version` label at build
time, so `cenci open` / `cenci sandbox build` detect base drift and auto-rebuild with a
one-line stdout notice instead of silently running a stale base.

The pipeline plugins (`cenci`/`flow` and `cenci-watch`) are **not** baked into the image
or copied from the repo either. `entrypoint.sh` provisions them at container boot via the
official CLI (`claude plugin marketplace add matteobortolazzo/cenci` + `claude plugin
install`, and the Codex equivalents in `lib/migrate-settings.sh`), materializing them into
the per-scope home volume and refreshing them on a TTL. So there is **no** sandbox-local
copy of the agents/skills to maintain — `flow/agents/` and `flow/skills/` are the single
source of truth, published through `.claude-plugin/marketplace.json` and installed the same
way a host `install.sh` run would. A consequence worth knowing: a running container carries
the last *published* plugin version, not your working tree — un-merged edits to
`flow/skills/` only reach it after the plugin version bumps.

`fragments/*.dockerfile` holds the same composable blocks (`dotnet`, `node`, `playwright`,
`go`, `python`, `rust`, `docker`, `azure`) as standalone snippets used to assemble per-project images.
Generated images always include Node so the isolated updater can install either npm package;
the remaining fragments (including `playwright`, used for `verify-ui`'s Chromium
screenshot capture) follow the detected project stack. `docker` is instead config-selected
(`sandbox.dind: true`): it carries the Docker CLI, `docker-ce` engine and `containerd.io` that
the inner daemon needs, and lived in `Dockerfile.base` until #831 — so a `dind: true` repo whose
`.cenci/Dockerfile` predates that change builds an image with no `dockerd`, which `start_dind`
reports by name in `~/.cenci-dockerd-startup-error` (non-fatal; the container stays usable). `azure` is
config-selected the same way (`sandbox.azure: true`, #1080): it installs the Azure CLI from Microsoft's apt
repo so an agent can verify `az` syntax with `--help` instead of guessing it, and — unlike `docker` — has **no**
monolith fallback, so `sandbox.azure` without a per-repo `.cenci/Dockerfile` yields no `az` at all. The same key
also gates the launcher's Azure credential staging (`ResolveAzure`, `watch/internal/sandbox/launcher/azure.go`):
the host's `~/.azure` auth files are bind-mounted read-only under `/tmp/host-azure-creds` and seeded once into
`/home/dev/.azure` by `entrypoint.sh` (`seed_azure_creds`, `lib/seed-auth.sh`) — atomically, since a per-file
copy could splice the host's profile onto a container-side token cache. Cloud credentials are staged only where
`az` exists to use them; every other credential is staged for every sandbox. Per-repo images include the shared
Node runtime, never the agent packages. **Invariant:** each fragment and its corresponding block in `Dockerfile` must stay
byte-identical — hand-duplicated on every change (e.g. bumping `DOTNET_SDK_VERSION` or adding a
package to a stack block means editing both `Dockerfile` and `fragments/<stack>.dockerfile`
identically). The exceptions are `python`, `rust` and `azure`, which deliberately have **no** monolith
block because this repo's own stack doesn't use them; `fragments-drift.test.sh` enforces both directions
(byte-identity for monolith-backed fragments, deliberate absence for the three monolith-less ones), so
adding a monolith block for one of them means removing it from that suite's `MONOLITH_LESS` list.

**Fragment drift detection (#1048).** A per-repo `.cenci/Dockerfile` is generated once, by
hand, via `/cenci:configure` — `cenci sandbox build` never recomposes it from
`fragments/*.dockerfile`, so a fragment fix (a `docker` group add, a CVE bump, ...) never
reaches an already-committed repo Dockerfile on its own. `watch/internal/sandbox/launcher/fragmentdrift.go`
closes the silent half of that gap: at freshness-check time (`cenci open` and `cenci
sandbox build`, scoped to `Scope.UsingRepoImage`), it compares the repo's committed
`# cenci:managed-begin` … `# cenci:managed-end` block against the installed
`fragments/*.dockerfile` and prints a non-fatal stderr warning naming any drifted
fragment and `/cenci:configure` as the remedy. It never auto-rebuilds and never flips
`imageCurrent`'s freshness verdict — a rebuild alone cannot fix stale fragment content,
since the build path never writes `.cenci/Dockerfile`.

Per-fragment `# cenci:fragment-begin <name>` / `# cenci:fragment-end <name>` markers
(emitted by `/cenci:configure` going forward, see `sandbox/README.md`) are the exact
identification path. Where a repo's block predates those markers, the detector falls back
to each fragment's `# ── <title> ──…` banner line as a legacy identity anchor. **Treat a
fragment's banner line as a stable, load-bearing anchor**: rewriting it (not just the body
below it) silently stops a marker-less, already-committed block from being recognized as
selecting that fragment, and drift on it goes unreported instead of warned. Keep banner
text unchanged unless deliberately retiring the legacy fallback for that fragment;
`fragments-drift.test.sh` asserts every fragment has exactly one banner line and that
banners are unique across fragments.

## Dependency version pins
Image dependency versions are pinned via Dockerfile `ARG`s, all checked daily by
`.github/workflows/deps-bump.yml`. Three tiers, by breaking-change risk:

- **Runtime-managed (agent CLIs)** — Codex and Claude Code bootstrap at a verified exact
  `latest` version into global read-only-at-workload volumes. `cenci sandbox update-agent`
  still updates the volume explicitly (and its `--unpin`/`--all` flags manage pins and
  bulk refreshes), but refresh is no longer purely manual: `cenci update`'s installer hook
  (`step_sandbox_update_agents`) now also runs `cenci sandbox update-agent --all`
  automatically after every normal `cenci update`, best-effort (warn-not-fail), refreshing
  every agent-CLI volume that already exists rather than leaving it on a stale version
  until someone remembers to update it by hand. There is no image version ARG, so
  `deps-bump.yml` does not track them. Integrity and signatures do not defend against a
  legitimately published malicious vendor release; Codex additionally requires provenance
  for `openai/codex`, while Claude currently has no npm provenance and retains that
  vendor-release trust.
- **Auto-bumped, auto-merged** — one auto-merged PR per outdated dependency, then the
  cenci-sandbox rebuild is dispatched once the merge lands:
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
    follow-up if it proves stable enough to auto-merge like Go/uv.
    A stale pin is degraded, not fatal, since #1096: `/ms-playwright` is owned by `dev` at
    build time and re-owned by `entrypoint.sh`'s remap, so a repo whose `@playwright/test`
    needs a different Chromium build installs it there itself. Keep both properties when
    touching this block — root ownership silently turns that recoverable drift back into a
    hard failure that only a manual in-container `chown` clears. The recovery costs a
    re-download every session, though: `/ms-playwright` is in the container's writable
    layer, not a volume, and workloads run `--rm`. So a stale pin is still worth bumping —
    it is the only thing that makes the common case free.
- **Deliberately unpinned (no ARG, so nothing for `deps-bump.yml` to track)**:
  - `fragments/docker.dockerfile` and `fragments/azure.dockerfile` install from an apt repo
    with no version ARG. For Azure this is a decision, not an oversight: the repo publishes
    one rolling `azure-cli` package and Azure's service-side APIs move under it, so a pinned
    CLI drifts out of date against the services it targets. Because the version lives in the
    apt repo rather than in the fragment text, an uncached rebuild alone picks up the current
    CLI — no per-repo Dockerfile re-generation needed.

## Reference Docs
Repo-level conventions live at `<repo-root>/docs/` (read on demand); CLI grammar, alias, env-var, and runtime-object naming rules are in `<repo-root>/docs/cli-conventions.md`. Project-specific notes belong in this file.
On-demand topic docs live at `docs/`:
- `docs/entrypoint.md` — `entrypoint.sh` + its sourced `lib/` helpers, their root phase, and every `docker exec` call site the launcher assembles
- `docs/test-harness.md` — host-runnable `sandbox/tests/*.test.sh` suites
