# claude-tools

Claude Code and Codex plugins and tooling.

## Plugins

### [ccflow](./ccflow)

Ticket refinement and automated implementation pipeline for GitHub. Provides skills for planning, TDD implementation, code review, and PR creation.

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install ccflow
```

### [muxwatch](./muxwatch)

Event-driven tmux watcher that monitors Claude Code and OpenAI Codex sessions and displays live status in window titles and waybar.

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install muxwatch
```

Binary install:

```bash
go install github.com/matteobortolazzo/claude-tools/muxwatch@latest
```

## Tooling

### [dev-sandbox](./dev-sandbox)

Docker/Podman container for running Claude Code in isolation. Includes .NET, Node.js, Go, and common dev tools.

```bash
ln -s "$(pwd)/dev-sandbox/claude-sand" ~/.local/bin/claude-sand
claude-sand --build  # Build image
claude-sand          # Launch Claude Code in container
```

## License

MIT
