# Migrating from agent-stack to cenci

The product formerly known as **agent-stack** (three layers: **agentflow**,
**agentwatch**, **agent-sandbox** / `agent-sand`) is now **cenci** (three layers:
**cenci**, **cenci-watch**, **cenci-sandbox**). The GitHub repository
moved from `matteobortolazzo/agent-stack` to `matteobortolazzo/cenci` —
**GitHub automatically redirects the old repository URL** (clone links, issue
links, and `git remote` fetches against the old path keep working), but update
bookmarks and local remotes when convenient.

This page is a reference for anyone upgrading an existing pre-rename install. If
you're installing fresh, you don't need this — just follow
[Getting started](getting-started.md).

## Neutral project core migration

Project workflow configuration now lives in `.cenci/config.json`, and shared guidance
lives in `AGENTS.md`. Existing `.claude/config.json` projects remain readable as a
legacy fallback. Run `cenci:configure` to preview and approve migration; unknown keys
and substantive guidance are preserved. Claude Code receives a generated `CLAUDE.md`
importing `@AGENTS.md`; settings and native agent files remain client-specific adapters.

## Old → new name reference

### CLI / binaries

| Old | New |
|---|---|
| `agentwatch` (daemon/CLI binary) | `cenci` |
| `agent-sand` (bash sandbox launcher) | `cenci-sand` — itself since retired in favor of `cn` / `cenci open` (see [cenci-sand → cenci](#cenci-sand--cenci) below) |
| `agent-stack` (repo-root installer wrapper) | `cenci` |
| — | `cenci-installer` (the on-`PATH` wrapper `cenci doctor`/`cenci update` shell out to; new in this rename to avoid colliding with the `cenci` binary symlink) |
| `sb` (bash shortcut launcher) | Deprecated — the `cenci` binary's built-in `cenci open`/`cn` alias (see [cenci-watch's README](../watch/README.md#sandbox-management-and-session-launching-cenci-sandbox-cenci-open)) replaces it. The installer no longer creates an `sb` symlink. |

### Plugins (Claude Code / Codex marketplace)

| Old plugin id | New plugin id |
|---|---|
| `agentflow` | `cenci` |
| `agentwatch` | `cenci-watch` |
| `agent-sandbox` | `cenci-sandbox` |
| Marketplace repo `matteobortolazzo/agent-stack` | Marketplace repo `matteobortolazzo/cenci` |

### Directories (this repo's own layout)

| Old | New |
|---|---|
| `agentflow/` | `flow/` |
| `agentwatch/` | `watch/` |
| `dev-sandbox/` | `sandbox/` |

### Sockets, PID files, and config paths

| Old | New |
|---|---|
| `$XDG_RUNTIME_DIR/agentwatch/agentwatch.sock` | `$XDG_RUNTIME_DIR/cenci/cenci.sock` |
| `$XDG_RUNTIME_DIR/agentwatch/agentwatch.pid` | `$XDG_RUNTIME_DIR/cenci/cenci.pid` |
| `$XDG_RUNTIME_DIR/agentwatch/agentwatch-events.sock` | `$XDG_RUNTIME_DIR/cenci/cenci-events.sock` |
| `/tmp/agentwatch-<uid>.sock` / `.pid` | `/tmp/cenci-<uid>.sock` / `.pid` |
| `~/.config/agentwatch/config.json` | `~/.config/cenci/config.json` |
| `.claude/config.json`'s `"agentflow"` settings block | `"cenci"` settings block |
| `${TMPDIR:-/tmp}/agentwatch-bootstrap.log` | `${TMPDIR:-/tmp}/cenci-bootstrap.log` |
| Generated per-repo `.agent-sand/Dockerfile` | `.cenci/Dockerfile` |
| `# agentflow:managed-begin` / `# agentflow:managed-end` Dockerfile markers | `# cenci:managed-begin` / `# cenci:managed-end` |

### Environment variables

| Old | New |
|---|---|
| `AGENTWATCH_BIN` | `CENCI_BIN` |
| `AGENTWATCH_AVAILABLE`, `AGENTWATCH_CALLS`, `AGENTWATCH_JSON`, `AGENTWATCH_PRESENT`, `AGENTWATCH_SOCKET_DIR*`, `AGENTWATCH_SOCKET_MOUNT_DEST`, `AGENTWATCH_TEST_*`, `AGENTWATCH_EVENTS_SOCKET` | `CENCI_*` equivalents |
| `AGENT_SAND` (bare in-container gate) | `CENCI_SANDBOX` |
| `AGENT_SAND_AGENT` | `CENCI_SANDBOX_AGENT` |
| `AGENT_SAND_REAP_GRACE_SECS` | `CENCI_SANDBOX_REAP_GRACE_SECS` |
| `AGENT_SAND_RESEED_CREDS` | `CENCI_SANDBOX_RESEED_CREDS` |

### Container, volume, and image names

| Old | New |
|---|---|
| `claude-sand-<repo-slug>` / `codex-sand-<repo-slug>` (containers) | `claude-cenci-<repo-slug>` / `codex-cenci-<repo-slug>` |
| `<agent>-sand-home-<repo-slug>` (volumes) | `<agent>-cenci-home-<repo-slug>` |
| `agent-sandbox` / `agent-sandbox-base` (images) | `cenci-sandbox` / `cenci-sandbox-base` |
| `/tmp/agent-sand-ready` (container readiness marker) | `/tmp/cenci-ready` |

### tmux user variables

| Old | New |
|---|---|
| `@agentwatch-symbol` | `@cenci-symbol` |
| `@agentwatch-style` | `@cenci-style` |
| `@agentwatch-headroom-<agent>` | `@cenci-headroom-<agent>` |

### Desktop widget identities

| Surface | Old id | New id |
|---|---|---|
| GNOME Shell extension UUID | `agentwatch@matteobortolazzo.github.io` | `cenci@matteobortolazzo.github.io` |
| GNOME gschema id / filename | `org.gnome.shell.extensions.agentwatch` | `org.gnome.shell.extensions.cenci` |
| KDE Plasma plasmoid id | `com.github.matteobortolazzo.agentwatch` | `com.github.matteobortolazzo.cenci` |
| DMS / noctalia plugin id | `agentwatch` | `cenci` |
| DMS / noctalia settings key | `agentwatchPath` | `cenciPath` |
| SwiftBar plugin file | `agentwatch.5s.sh` | `cenci.5s.sh` |
| DMS widget QML files | `AgentWatchWidget.qml` / `AgentWatchSettings.qml` | `CenciWidget.qml` / `CenciSettings.qml` |

### Go module path

| Old | New |
|---|---|
| `github.com/matteobortolazzo/agent-stack/agentwatch/v4` | `github.com/matteobortolazzo/cenci/watch` (major version reset to 0.x with the rename, so no `/vN` suffix) |

### Skills / slash-commands

| Old | New |
|---|---|
| `/cenci:garden [project]` | `/cenci:maintain rules` — garden retired; rule curation folded into the maintain skill's `rules` mode |

## cenci-sand → cenci

The `cenci-sand` bash launcher has been folded into the `cenci` Go binary: sessions
launch with `cenci open` (or its one alias binary, `cn` — `cn <args>` is exactly
`cenci open <args>`), and the maintenance flags became `cenci sandbox <verb>` verbs.
The full CLI reference lives in
[cenci-watch's README](../watch/README.md#sandbox-management-and-session-launching-cenci-sandbox-cenci-open).

| Old | New |
|---|---|
| `cenci-sand` | `cn` (or `cenci open`) |
| `cenci-sand <shortcut>` (e.g. `cenci-sand xt`) | `cn <shortcut>` (e.g. `cn xt`) |
| `cenci-sand --agent codex` | `cenci open --agent codex` (or a Codex shortcut, e.g. `cn xt`) |
| `cenci-sand --shell` | `cenci open --shell` |
| `cenci-sand --build` | `cenci sandbox build` |
| `cenci-sand --build-base` | `cenci sandbox build-base` |
| `cenci-sand --prune [--volumes]` | `cenci sandbox prune [--volumes]` |
| `cenci-sand --update-plugins` | `cenci sandbox update-plugins [--agent claude\|codex] [--name <n>]` |
| `cenci-sand --reap-orphans` | `cenci sandbox reap-orphans` |
| `cenci-sand --reseed-creds` | `cenci open --reseed-creds` (alias: `cenci sandbox reseed-creds`) |
| `cenci-sand -p "fix the bug"` (bare positionals / single-dash agent args) | Stricter now — agent args must go after a bare `--`: `cenci open -- -p "fix the bug"` (or `cn -- -p "fix the bug"`) |

Usage errors (unknown flag, unknown verb, stray positional) now exit 2.

**Unchanged** — no action needed for any of these:

- `CENCI_SANDBOX*` environment variables (`CENCI_SANDBOX`, `CENCI_SANDBOX_AGENT`,
  `CENCI_SANDBOX_REAP_GRACE_SECS`, `CENCI_SANDBOX_RESEED_CREDS`, …)
- Container names (`claude-cenci-*` / `codex-cenci-*`) and home volumes
  (`*-cenci-home-*`) remain named the same. Existing home data is preserved, but agent
  executables there are ignored. Stop and relaunch already-running containers once so they
  gain the shared read-only `cenci-agent-cli-${agent}` mount.
- Image names (`cenci-sandbox`, `cenci-sandbox-base:<hash>`, `cenci-sandbox-<slug>`)
- Per-repo `.cenci/Dockerfile` files — no regeneration required for the file itself

**Clean up the old launcher.** Fresh installs no longer create
`~/.local/bin/cenci-sand`. On upgraded machines the installer repoints a stale
`cenci-sand` symlink at the `cenci` binary, which treats that name as a tombstone: it
prints this migration map and exits 2 instead of launching anything. Once your
muscle memory has moved on, remove it:

```bash
rm -f ~/.local/bin/cenci-sand
```

**Regenerate generated CLAUDE.md content.** If a repo's `CLAUDE.md` was generated by
`/cenci:configure` before the fold, it may still instruct agents to run
`cenci-sand --build` (the old "Sandbox Image" rebuild line). Re-run `/cenci:configure`
in that repo to regenerate it with the new `cenci sandbox build` form.

## Upgrade steps

1. **Rerun the installer.** It detects the existing installation and updates
   everything to the new names in place:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
   ```

   Resolves to the latest release tag by default; set `CENCI_REF=main` (or
   pass `--ref main`) for bleeding-edge main instead.

   Or, if you already have `cenci` on `PATH`, `cenci update`.

2. **Fix hand-customized tmux status formats.** If you followed cenci-watch's
   README ["Custom status-format integration"](../watch/README.md#custom-status-format-integration)
   section before the rename, your own `~/.tmux.conf` (or
   `~/.config/tmux/tmux.conf`) has a `window-status-format` /
   `window-status-current-format` override that references the old
   `@agentwatch-symbol` / `@agentwatch-style` / `@agentwatch-headroom-<agent>`
   tmux user variables directly. The installer never touches personal dotfiles,
   so it can't fix these for you — tmux format lookups against a variable that
   no longer exists just silently fall back to the format's default (no error),
   so a stale reference looks like nothing happened rather than a visible
   break. Manually replace each old variable with its new name — see the
   [tmux user variables](#tmux-user-variables) table above — then
   `tmux source-file` your config to pick up the change. `cenci doctor` flags
   stale `@agentwatch-*` references in your tmux config if you're not sure.

3. **Reinstall desktop widgets.** The GNOME/KDE Plasma/DMS/noctalia extension ids
   changed (see the table above), so an old-id widget install won't pick up
   automatically — re-run the installer (it re-prompts for each detected bar) or
   each widget's own `install.sh` under `watch/plugin/<surface>/`.

4. **Clean up orphaned volumes (optional).** Old `*-sand-home-*` container home
   volumes are not migrated automatically — list and remove them once you've
   confirmed you don't need the credentials/history inside:

   ```bash
   docker volume ls --filter name=sand-home
   docker volume rm <old-volume-name>
   ```

5. **Clean up old leftovers (optional).** Once you've confirmed the new install
   works:

   ```bash
   rm -rf ~/.config/agentwatch
   rm -f ~/.local/bin/agentwatch ~/.local/bin/agent-sand ~/.local/bin/sb ~/.local/bin/agent-stack
   ```

   (Only remove the ones that exist on your machine — a fresh install never
   created some of these.)

## FAQ

**Do I need to do anything if I never installed the old `agent-stack`?** No —
this page only matters for pre-rename installs.

**Will old bookmarks/clones to `matteobortolazzo/agent-stack` break?** No — GitHub
redirects repository URLs after a rename, so `git clone`, issue links, and raw
file URLs against the old path keep working. Update them when convenient; the
redirect is not guaranteed to be permanent if the old name is ever reused by
someone else.
