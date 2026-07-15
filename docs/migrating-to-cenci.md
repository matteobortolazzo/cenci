# Migrating from agent-stack to cenci

The product formerly known as **agent-stack** (three layers: **agentflow**,
**agentwatch**, **agent-sandbox** / `agent-sand`) is now **cenci** (three layers:
**cenci**, **cenci-watch**, **cenci-sandbox** / `cenci-sand`). The GitHub repository
moved from `matteobortolazzo/agent-stack` to `matteobortolazzo/cenci` —
**GitHub automatically redirects the old repository URL** (clone links, issue
links, and `git remote` fetches against the old path keep working), but update
bookmarks and local remotes when convenient.

This page is a reference for anyone upgrading an existing pre-rename install. If
you're installing fresh, you don't need this — just follow
[Getting started](getting-started.md).

## Old → new name reference

### CLI / binaries

| Old | New |
|---|---|
| `agentwatch` (daemon/CLI binary) | `cenci` |
| `agent-sand` (bash sandbox launcher) | `cenci-sand` |
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
| `github.com/matteobortolazzo/agent-stack/agentwatch/v4` | `github.com/matteobortolazzo/cenci/watch/v4` |

## Upgrade steps

1. **Rerun the installer.** It detects the existing installation and updates
   everything to the new names in place:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
   ```

   Or, if you already have `cenci` on `PATH`, `cenci update`.

2. **Reinstall desktop widgets.** The GNOME/KDE Plasma/DMS/noctalia extension ids
   changed (see the table above), so an old-id widget install won't pick up
   automatically — re-run the installer (it re-prompts for each detected bar) or
   each widget's own `install.sh` under `watch/plugin/<surface>/`.

3. **Clean up orphaned volumes (optional).** Old `*-sand-home-*` container home
   volumes are not migrated automatically — list and remove them once you've
   confirmed you don't need the credentials/history inside:

   ```bash
   docker volume ls --filter name=sand-home
   docker volume rm <old-volume-name>
   ```

4. **Clean up old leftovers (optional).** Once you've confirmed the new install
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
