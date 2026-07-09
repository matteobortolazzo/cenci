# claude-tools

Claude Code and Codex plugins and tooling.

## Plugins

### [ccflow](./ccflow)

Ticket refinement and automated implementation pipeline for GitHub. Provides skills for planning, TDD implementation, code review, and PR creation.

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install ccflow
```

### [agentwatch](./agentwatch)

Event-driven watcher that monitors Claude Code and OpenAI Codex sessions and surfaces live status across tmux, waybar, noctalia, and DMS. tmux is one frontend among several.

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install agentwatch
```

Binary install:

```bash
go install github.com/matteobortolazzo/claude-tools/agentwatch@latest
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
