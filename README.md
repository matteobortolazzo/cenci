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

### [sandbox](./dev-sandbox)

Docker/Podman container for running Claude Code in isolation with full permissions — the container is the security boundary. Includes .NET, Node.js, Go, and common dev tools.

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install sandbox
/sandbox:setup   # symlink the claude-sand launcher and build the image
```

## License

MIT
