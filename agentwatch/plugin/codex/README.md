# AgentWatch for Codex

Forwards Codex lifecycle hooks to `agentwatch notify` so the agentwatch daemon can update tmux window indicators and Waybar-compatible counts.

## Requirements

- `agentwatch` binary on `$PATH`
- `agentwatch` daemon running in the same tmux/server environment
- Codex hooks enabled (default in current Codex releases)

## Manual hook install

If you do not already have Codex hooks configured:

```bash
mkdir -p ~/.codex
cp /path/to/claude-tools/agentwatch/plugin/codex/hooks.json ~/.codex/hooks.json
```

If `~/.codex/hooks.json` already exists, merge the `hooks` entries from this directory's `hooks.json` instead of replacing the file.

Codex will ask you to review/trust new hooks. Use `/hooks` in Codex if the hooks are listed as pending review.

## Plugin install

This directory also includes a Codex plugin manifest. Plugin-bundled hooks currently require:

```toml
[features]
plugin_hooks = true
```

in your Codex `config.toml`.
