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
bash sandbox/tests/heal-plugins.test.sh          # plugin self-heal (Write->Edit allow conversion)
```

The launcher-behavior suites live with the launcher code in `watch/`: Go
black-box tests in `watch/sandbox_open_test.go` plus the reap contract suite
`watch/tests/reap-orphans.test.sh` (run with `CENCI_BIN`).

## Conventions
- Keep the image minimal; agent CLIs belong in host-global volumes mounted read-only in workloads. Only the isolated updater may mount them writable.
- `entrypoint.sh` must stay POSIX-portable and pass `shellcheck`.
- The container is the security boundary — Claude Code's host sandbox stays disabled inside it.

## Entrypoint patterns

- **Never self-remap the UID/GID of a running account via `usermod`/`groupmod`.** A live account's own UID/GID cannot be renumbered while any process (including the calling shell) runs under that UID — the utilities refuse the operation. Container entrypoints that need to remap their own running user (e.g., to match host UID for mounted volumes) must start privileged (root) with zero workload processes yet alive, perform the remap safely, then unconditionally drop privileges to the target user before the rest of the entrypoint runs. Do not try to self-remap from within the account being changed.

- **`docker run --user X` persists for the container's lifetime — it is not scoped to the initial process.** The `--user` flag is stored as the container's `Config.User` and becomes the default for every subsequent `docker exec` call that doesn't pass its own explicit `-u` flag. A pattern that starts a container as root for setup (privilege-drop), but then runs workload via `docker exec`, will silently run all exec calls as root if they omit `-u <target-user>`. The image's original `USER dev` directive from the Dockerfile does not automatically restore as the default for exec — you must audit and add explicit `-u dev` to every exec call site after a privilege-drop entrypoint.

- **TTY detection via permission-bit check alone is unreliable in sandboxed environments.** A permission check like `[ -r /dev/tty ] && [ -w /dev/tty ]` returns true even when `/dev/tty` exists with permissive bits but has no actual controlling terminal attached (common in Docker/CI containers) — an actual `open(2)` of the file will then fail with ENXIO. When a conditional behavior truly depends on interactive TTY availability (e.g., destructive-action confirmation gates), verify with an actual open attempt: `exec 3<>/dev/tty 2>/dev/null`, not just a permission-bit check.

## Testing

- **Guard clauses must be mirrored across duplicated logic.** When the same validation/parsing pattern appears in both a test file and its corresponding production script (e.g., `smoke.test.sh` and `cenci-sand` both resolve a version string with jq + sed fallback), the error-handling guards must be duplicated too. A test file that is more careful than the production code it exercises is a code-review red flag — check for silent failures (e.g., empty Docker tags, null strings propagating downstream).

- **Preserve all pre-existing logic when splitting a script into conditional branches.** When refactoring a script to introduce a new conditional branch (e.g., adding git-based per-repo scoping alongside a legacy non-git fallback), the fallback branch can appear "unchanged" in the diff while actually being accidentally simplified during the rewrite (e.g., dropping a computed value and hardcoding a default). Test both branches independently to catch silent failures.

- **Under `pipefail`, a terminal-stage filter or loop that always succeeds masks upstream failures.** Pipelines ending with `grep ... || true` or `while read` (both always exit 0) will hide real failures from earlier stages — e.g., `docker ps` failing silently appears as "nothing found." Always capture command output into a variable and check that command's exit status explicitly before filtering or looping on the captured value.

- **When testing code that defends against a documented failure pattern, exercise the failure path, not just the success path.** If implementation guidance prescribes a guard (e.g., "always capture and check exit status"), the test suite must include cases that trigger the failure the guard defends against — verify that the guard's error handling (warning, skip, resume, etc.) actually executes, not just that the guard syntax is present. Happy-path-only tests cannot prove the guard works when invoked.

- **When a function is used as the condition of `if`/`while`, bash suspends `set -e` for its entire body.** This calling-convention gotcha means every command inside such a function whose failure matters must have its exit status explicitly captured and checked — do not rely on `set -e` to catch downstream failures. This differs from `pipefail` masking: it's about errexit suspension via calling convention, not pipeline status computation. Sibling instances of the same error pattern in a function (e.g., multiple `kill` escalations) may not all be caught in a single review pass—systematically sweep the entire function body for the same pattern when fixing one instance.

- **Host-runnable test harnesses that shell out via a mock PATH must scrub the subprocess environment.** Launch the mocked commands with `env -i HOME=... PATH=... <only the vars the test needs>=...` rather than inheriting the runner's ambient environment — otherwise host secrets (AWS keys, tokens) leak into the subprocess. Pair the scrub with a sentinel-secret regression assertion (set a fake secret only in the test's own env, then assert it never appears in captured calls or output) to prove the scrub actually excludes host secrets, not just that the test passes (#363).

- **When asserting on awk-extracted regions of prose/skill-doc text, use longer load-bearing substrings tied to the actual sentence, not single keywords.** An `assert_contains "${EXTRACTED_REGION}" "keyword"` check that passes proves only that the keyword exists *somewhere* in the region — a future edit could reintroduce a conditional bypass while some unrelated sentence nearby still contains the keyword, and the assertion would silently keep passing. Instead, assert on multi-word substrings that span the actual grammatical unit you're guarding (e.g., assert on the entire conditional clause or the sentence containing the invariant-critical phrase). Pair this with: verify that test case names match the region being asserted (if a case claims "5f only runs when X", assert `assert_contains "${EXTRACTED_5F}"`, not against the Q10 region), so test logic matches its description.

- **`assert_contains` with `grep -Fq` cannot match text split across markdown line breaks.** The `-F` flag treats the pattern as a literal string and `-q` does single-line matching — if prose being tested is wrapped across multiple lines (common in markdown), a test looking for `"...in\n  this session"` will fail silently. When testing documentation assertions, either refactor to match within a single line, verify the exact text location before writing the assertion, or use a different matching approach that handles line continuations.

- **Functions whose return value is captured via `$(...)` command substitution must not print anything except the intended captured value.** Any output (including `warn`, `echo`, or side-effect logging from called functions) gets swallowed into the captured string instead of reaching the terminal. Callers that do `path=$(resolve_path "$arg")` expect `$path` to be only the path, not the path prefixed by a warning message. When refactoring to extract helper functions whose results are captured, verify that no intermediate function calls print to stdout; move any logging to the caller, after the function returns and the substitution is complete.

- **Destructive operations (like `rm -rf` cleanup) must verify that prerequisite stop/disable operations actually succeeded — do not suppress their errors and proceed optimistically.** A pattern like `"$bin" daemon stop || true` followed by unconditional `ok "stopped"` and then `rm -rf` of the daemon's managed state dir risks deleting live socket/pid files out from under a daemon that didn't actually stop. When a preceding operation's success is a prerequisite for a destructive follow-up, check and propagate its exit status; only suppress the error if the destructive operation is genuinely optional or can safely tolerate the prerequisite's failure.

## Image architecture: base + fragments
`Dockerfile.base` builds the stack-agnostic `cenci-sandbox-base:<content-hash>` image
(plus an `cenci-sandbox-base:latest` alias tag), where `<content-hash>` is a 12-char
digest of `Dockerfile.base` + `entrypoint.sh` + `lib/` (Ubuntu, system packages, locale,
`uv`, GitHub CLI, Docker CLI, non-root `dev` user, entrypoint — no language runtimes).
`Dockerfile` (the monolith) builds `cenci-sandbox:latest` `FROM` that base image and
layers on the runtime stacks in order: .NET, Node, Playwright, and Go. Agent CLIs are not
image layers: the launcher bootstraps absent `cenci-agent-cli-<agent>` volumes through a
credential-free updater, and workloads mount them read-only at `/opt/cenci-agent`.
`cenci sandbox update-agent` updates that global volume explicitly and atomically.
Credentials are still staged only into per-scope home volumes.

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
`go`, `python`, `rust`) as standalone snippets used to assemble per-project images.
Generated images always include Node so the isolated updater can install either npm package;
the remaining fragments (including `playwright`, used for `verify-ui`'s Chromium
screenshot capture) follow the detected project stack. Per-repo images include the shared
Node runtime, never the agent packages. **Invariant:** each fragment and its corresponding block in `Dockerfile` must stay
byte-identical — hand-duplicated on every change (e.g. bumping `DOTNET_SDK_VERSION` or adding a
package to a stack block means editing both `Dockerfile` and `fragments/<stack>.dockerfile`
identically).

## Dependency version pins
Image dependency versions are pinned via Dockerfile `ARG`s, all checked daily by
`.github/workflows/deps-bump.yml`. Three tiers, by breaking-change risk:

- **Runtime-managed (agent CLIs)** — Codex and Claude Code bootstrap at a verified exact
  `latest` version into global read-only-at-workload volumes and update only through
  `cenci sandbox update-agent`. There is no image version ARG, so `deps-bump.yml` does not
  track them. Integrity and signatures do not defend against a legitimately published
  malicious vendor release; Codex additionally requires provenance for `openai/codex`,
  while Claude currently has no npm provenance and retains that vendor-release trust.
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

## Security
- Never bake secrets or credentials into the image layers.
- Validate any host paths mounted into the container.
- Bind-mount host paths read-only (`:ro`) unless the container genuinely needs write access — containers should be as restrictive as possible. Audit all new and existing mounts the launcher assembles (`watch/internal/sandbox/launcher`) against this principle.

## Reference Docs
Repo-level conventions live at `<repo-root>/docs/` (read on demand); CLI grammar, alias, env-var, and runtime-object naming rules are in `<repo-root>/docs/cli-conventions.md`. Project-specific notes belong in this file.
