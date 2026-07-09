# AgentWatch for Codex

Forwards Codex lifecycle hooks to `agentwatch notify` so the agentwatch daemon can update tmux window indicators and Waybar-compatible counts.

## Requirements

- `agentwatch` binary on `$PATH`
- `agentwatch` daemon running in the same tmux/server environment
- Codex hooks enabled (default in current Codex releases)

## Binary and daemon (reuse from the Claude Code plugin)

Unlike the Claude Code plugin, these Codex hooks do **not** bootstrap the binary
themselves — they call a bare `agentwatch` on `$PATH` and rely on a shared host
daemon. Provision it one of two ways:

- **Recommended:** install the Claude Code plugin
  (`claude plugin install agentwatch`). Its SessionStart hook downloads the
  `agentwatch` binary and starts the daemon; Codex then reuses that same daemon.
  (Note: the plugin installs the binary into the plugin's `bin/`, not `$PATH`, so
  for Codex you still need `agentwatch` reachable on `$PATH` — symlink or copy the
  plugin binary, or use the manual install below.)
- **Codex-only:** install the binary manually
  (`go install github.com/matteobortolazzo/claude-tools/agentwatch@latest` or
  `make build`) and start the daemon once (`agentwatch`). See the main
  [README](../../README.md#advanced--development).

Full self-contained Codex bootstrap (matching the Claude plugin) is tracked in
[#33](https://github.com/matteobortolazzo/claude-tools/issues/33).

## Manual hook install

If you do not already have Codex hooks configured:

```bash
mkdir -p ~/.codex
cp /path/to/claude-tools/agentwatch/plugin/codex/hooks.json ~/.codex/hooks.json
```

If `~/.codex/hooks.json` already exists, merge the `hooks` entries from this directory's `hooks.json` instead of replacing the file.

Codex will ask you to review/trust new hooks. Use `/hooks` in Codex if the hooks are listed as pending review.

## Plugin install

This directory also includes a Codex plugin manifest. Plugins and their bundled
hooks are stable and enabled by default in current Codex releases — no feature
flag is required.

## Trust model

Codex hash-pins `hooks.json`: it records a hash of the file it trusted. Every
plugin update that changes `hooks.json` changes that hash, so Codex marks the
hooks as pending review and you must re-trust them via `/hooks` in Codex before
they run again.
