# CLI and naming conventions

The single canonical convention doc for every user-facing command, alias,
environment variable, and runtime object name across the three cenci layers
(`flow/`, `watch/`, `sandbox/`). New surface area must follow these rules;
deviations are bugs. Per-project docs link here instead of restating the rules.

## Command grammar

One binary, verb-first:

```
cenci <verb> [subverb] [args] [flags]
```

- Verbs and subverbs are lowercase, hyphenated where multi-word
  (`reap-orphans`, `update-plugins`, `build-base`, `socket-dir`).
- Flag parsing uses Go's stdlib `flag` package — no third-party CLI
  frameworks. Both `--flag value` and `--flag=value` are accepted.
- Everything after a bare `--` is forwarded verbatim to the underlying
  program (agent CLI, container runtime) with no further parsing. This is the
  only way to pass tokens the parser doesn't recognize — unknown flags are
  errors, never silently forwarded.
- Usage errors (unknown verb, unknown flag, missing argument, conflicting
  flags) exit with status 2 and print a one-line hint to stderr.
- Conflicting inputs are errors, never silent precedence — e.g. a model
  shortcut that implies one agent combined with an `--agent` that names
  another is rejected, not resolved.

## Aliases and shortcuts

- Exactly **one** alias binary exists: `cn`, an argv[0] alias for
  `cenci open`. `cn <args>` behaves exactly as `cenci open <args>`. No other
  alias binaries, symlinked spellings, or wrapper scripts.
- One-token agent+model shortcuts (`ch`/`cs`/`co`/`cf` for Claude,
  `xl`/`xt`/`xs` for Codex) are accepted only as the first argument of
  `cenci open`/`cn`, so they can never shadow a later flag or prompt string.
- The shortcut tables are defined in exactly **one place in code**
  (`watch/internal/sandbox`) and documented in exactly **one place in docs**
  (the CLI reference in `watch/README.md`). Everything else — READMEs,
  skills, templates — links to those two homes instead of copying the table.

## Environment variables

- Host-side variables the cenci binary reads or sets are prefixed `CENCI_`.
- Sandbox-launcher variables are prefixed `CENCI_SANDBOX_`
  (e.g. `CENCI_SANDBOX_AGENT`, `CENCI_SANDBOX_REAP_GRACE_SECS`,
  `CENCI_SANDBOX_RESEED_CREDS`, `CENCI_SANDBOX_ASSETS`).
- The in-container gate is the bare `CENCI_SANDBOX=1` — set on every sandbox
  container; hooks and the daemon use it to detect "I am inside the sandbox"
  and skip host-only behavior.
- Never introduce un-prefixed names for cenci-owned state; host-standard
  variables (`TERM`, `TMUX_PANE`, `XDG_RUNTIME_DIR`, third-party API keys)
  keep their standard names.

## Runtime object naming

| Object | Pattern |
|---|---|
| Container | `${agent}-cenci-<slug>` (e.g. `claude-cenci-my-repo`) |
| Home volume | `${agent}-cenci-home-<slug>` |
| Shared agent CLI volume | `cenci-agent-cli-${agent}` |
| Monolith image | `cenci-sandbox:latest` |
| Per-repo image | `cenci-sandbox-<slug>:latest` |
| Base image | `cenci-sandbox-base:<content-hash>` (+ `:latest` alias) |
| Container hostname | `sandbox-<slug>` |
| Config | `~/.config/cenci/config.json` |
| Sockets | `$XDG_RUNTIME_DIR/cenci/` (fallback `/tmp/cenci-<uid>/`) |
| Readiness marker | `/tmp/cenci-ready` (inside the container) |
| Per-repo Dockerfile | `<repo-root>/.cenci/Dockerfile` |

`<slug>` is the repo slug (lowercased repo directory name, non-alphanumerics
collapsed to `-`), optionally suffixed `-<name>` when `--name` is given;
outside a git repo it is the legacy `--name` value (default `default`).
`${agent}` is `claude` or `codex`.

## Plugin identities and versioning

- flow is the flagship layer and keeps the bare `cenci` plugin id, so skills
  read as `/cenci:implement`. watch and sandbox are the `cenci-<layer>`
  auxiliaries: `cenci-watch`, `cenci-sandbox`.
- Each plugin versions independently. Tags are `<layer>/v<version>`
  (`flow/v*`, `watch/v*`, `sandbox/v*`), auto-bumped on push to `main`
  scoped by path (`flow/**`, `watch/**`, `sandbox/**`).
- Release commits are `chore(release): <plugin-id>/v<new>`
  (e.g. `chore(release): cenci/v1.2.0`, `chore(release): cenci-watch/v0.2.0`).
- The launcher *code* lives under `watch/**` and therefore versions with
  cenci-watch; the sandbox *assets* (Dockerfiles, fragments, entrypoint,
  container-side lib scripts, skills) live under `sandbox/**` and version
  with cenci-sandbox.

## Canonical doc homes

Each topic has exactly one home; everything else links to it.

| Topic | Home |
|---|---|
| CLI reference (verbs, flags, shortcut table) | `watch/README.md` |
| Image architecture (base/fragments, per-repo images) | `sandbox/README.md` |
| Security boundary (why `--docker`/`--host-network` are opt-in) | `SECURITY.md` |
| Old → new name mappings | `docs/migrating-to-cenci.md` |
