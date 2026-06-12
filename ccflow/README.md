# ccflow — Claude Code Workflow Plugin

Ticket refinement and automated implementation pipeline for GitHub.

## What it does

| Skill | Description |
|-------|-------------|
| `/ccflow:configure` | Interactive project setup: tech stack, sandboxing, MCP/LSP servers |
| `/ccflow:refine <ticket-id>` | Iterative ticket refinement until it's ready for planning |
| `/ccflow:design <ticket-id \| description>` | Interactive design reasoning and `.pen` file creation using Pencil |
| `/ccflow:implement <ticket-id>` | Full pipeline: plan, test, implement, refactor, security review, code review, lessons, PR |
| `/ccflow:address-review <pr-number>` | Address PR review comments — fetch, evaluate, fix, reply, push, re-request review |
| `/ccflow:sync` | Pull latest main, rebase active worktrees, prune stale remotes, clean up merged branches |

## Prerequisites

### Required
- **Claude Code** CLI installed and authenticated
- **GitHub CLI** (`gh`): for GitHub Issues and PRs — [install](https://cli.github.com/)
- **Node.js**: only required if using Context7 (MCP server for live documentation lookup)

### Optional: LSP Servers

LSP servers provide real-time diagnostics (type errors, unused variables, dead code) during implementation. Install any that match your stack:

| Stack | Server | Install Command |
|-------|--------|----------------|
| TypeScript / JavaScript | typescript-language-server | `npm install -g typescript-language-server typescript` |
| Python | pyright | `pip install pyright` or `npm install -g pyright` |
| Rust | rust-analyzer | See [rust-analyzer docs](https://rust-analyzer.github.io/manual.html#installation) |
| C# / .NET | csharp-ls | `dotnet tool install --global csharp-ls` |
| Go | gopls | `go install golang.org/x/tools/gopls@latest` |

Run `/ccflow:configure` to detect and enable LSP servers for your project.

### Authentication

```bash
gh auth login
```
The `gh` CLI stores credentials in `~/.config/gh/hosts.yml`. It also respects `GITHUB_TOKEN`/`GH_TOKEN` env vars as a fallback for non-interactive environments.

### Sandbox support (Linux / WSL2)
Sandboxing provides OS-level filesystem and network isolation for autonomous execution. It requires:
- **bubblewrap** (`bwrap`): `sudo apt install bubblewrap` (or `sudo pacman -S bubblewrap`)
- **socat**: `sudo apt install socat` (or `sudo pacman -S socat`)

macOS sandbox support is built into Claude Code and requires no extra packages.

## Installation

### Via marketplace (recommended)

```bash
# Register the repo as a marketplace (works with private repos too)
claude plugin marketplace add matteobortolazzo/claude-tools

# Install the plugin (persists across sessions)
claude plugin install ccflow
```

To update later: `claude plugin update ccflow`

### Manual (per-session)

```bash
claude --plugin-dir /path/to/ccflow
```

## Quick Start

```bash
# 1. Start Claude Code (plugin loads automatically if installed via marketplace)
claude

# 2. Configure the project (one-time setup)
/ccflow:configure

# 3. Refine a ticket (optional but recommended)
/ccflow:refine 12345

# 4. Design a ticket (optional — for frontend/UI tickets)
/ccflow:design 12345

# 5. Implement a ticket
/ccflow:implement 12345
```

## Working from your phone

ccflow skills (`/refine`, `/implement`, `/design`) are **interactive** — they ask clarifying questions and iterate. Triggering them via one-shot mechanisms (GitHub Actions, webhook, `claude --print`) drops all conversation state after each turn, which defeats their whole design.

**The fit-for-purpose answer is SSH + tmux**, which you probably already have:

1. **On laptop**: keep Claude Code running in a named tmux window (e.g. `tmux new -As ccflow`).
2. **Expose the laptop to your phone** via [Tailscale](https://tailscale.com) (or any VPN/SSH-accessible network).
3. **On phone**: install an SSH client — [Blink](https://blink.sh) (iOS), [Termius](https://termius.com) (iOS/Android), or [Termux](https://termux.dev) (Android).
4. **From phone**: SSH into your laptop, `tmux attach -t ccflow`, and type `/ccflow:refine 42` — you get the full interactive experience. Close the app; tmux keeps the session alive; reconnect anytime.

**Why this beats GH-comment or remote bots:**
- Real conversation state — skills ask questions, you answer, they proceed. Just like your desk.
- `/clear`, `/compact`, and every other Claude Code feature works normally.
- No new code to maintain, no webhook infra, no session-resume plumbing.
- Browse issues in the GH mobile app; trigger skills via SSH. Two apps, zero friction.

### What `/ccflow:configure` creates

```
your-project/
├── CLAUDE.md              # (or in .claude/ — user's choice during configure)
├── .claudeignore          # Files tracked by git but excluded from Claude's context
├── docs/
│   └── git-workflow.md    # On-demand reference: branching, commits, PRs
├── .claude/
│   ├── config.json        # ccflow configuration (includes claudeMdLocation)
│   └── settings.json      # Sandbox, permissions, and allowed domains
└── .worktrees/            # Git worktrees for feature branches (gitignored)
```

**Where reference docs live**

- `docs/<topic>.md` — on-demand reference docs read by skills/agents when their topic intersects the work. The lessons-collector also routes new topic-specific lessons here.
- `CLAUDE.md` — always loaded. Holds critical rules and project-wide invariants.
- `.claude/rules/` — reserved for files explicitly `@`-imported by `CLAUDE.md` (auto-loaded at session start). Configure does not create files here today.

**Backward compatibility**

If your project already has `.claude/rules/lessons-learned.md` (or `lessons-learned-<slug>.md`) from an earlier ccflow setup, ccflow leaves it in place and skills still read it as a legacy fallback. New lessons go to `docs/` or `CLAUDE.md` only.

### Monorepo Support

For monorepos, `/ccflow:configure` detects projects automatically and creates a **progressive disclosure** structure — project-specific context only loads when Claude accesses files in that subtree, saving tokens.

**Three-tier strategy:**

| Tier | Mechanism | Loading | Content |
|------|-----------|---------|---------|
| Root | `CLAUDE.md` at repo root | Eager | Repo-wide conventions, projects table, critical rules |
| Project | `packages/api/CLAUDE.md` etc. | Lazy (on file access) | Stack, build/test commands, project conventions |
| Reference docs | `docs/<topic>.md` at repo root | On-demand | Git workflow + topic-specific lessons routed by the collector |

**Monorepo file structure:**

```
your-project/
├── CLAUDE.md                  # Root — projects table + critical rules (eager)
├── .claudeignore              # Files tracked by git but excluded from Claude's context
├── docs/
│   └── git-workflow.md        # On-demand reference (read by skills as needed)
├── packages/
│   ├── api/
│   │   └── CLAUDE.md          # Per-project — stack, build/test (lazy)
│   └── web/
│       └── CLAUDE.md          # Per-project — stack, build/test (lazy)
├── .claude/
│   ├── config.json            # ccflow configuration (includes isMonorepo + projects)
│   └── settings.json          # Sandbox, permissions, and allowed domains
└── .worktrees/
```

Lessons are routed by topic (`docs/caching.md`, `docs/migrations.md`, …) rather than dumped into a single growing log. Project-wide invariants land in `CLAUDE.md` directly.

## Implementation Pipeline

When you run `/ccflow:implement <ticket-id>`, the pipeline executes these phases:

1. **Plan** — Context-gatherer agent bundles the ticket, design, and project context into a file (only a short digest enters the main context); planner agent reads the bundle, analyzes the codebase, and proposes an implementation plan (waits for your approval).
2. **Worktree Setup** — Creates an isolated git worktree for the feature branch
3. **Test First (Red)** — Implementer agent writes failing tests
4. **Implement (Green)** — Implementer agent makes tests pass
5. **Refactor** — Implementer agent simplifies and cleans up
6. **Security Review** — Security reviewer agent checks for OWASP vulnerabilities
7. **Code Review** — Code reviewer agent does a final PR-style review
8. **Capture Lessons** — Lessons collector routes genuine mistakes into the relevant `docs/<topic>.md` or `CLAUDE.md` (opt-in; most sessions capture nothing)
9. **Create PR** — Rebases on latest main, commits, pushes, and creates a pull request

### Usage controls

For lower limit pressure without removing quality gates, add optional settings to `.claude/config.json`:

```json
{
  "ccflow": {
    "compactImplementation": false,
    "reviewConcurrency": "parallel",
    "diffContextMode": "inline"
  }
}
```

- `compactImplementation: true` lets small, low-risk tickets combine red/green/refactor into one implementer turn while still requiring red failures, green implementation, refactor, and final build/test reporting.
- `reviewConcurrency: "sequential"` runs the same security, code, and silent-failure reviewers one after another instead of in parallel.
- `diffContextMode: "file"` passes reviewers a patch file path and changed-file list for large diffs instead of duplicating the full diff in every prompt.

### Optional: RTK command-output compression

ccflow also benefits from external command-output compression tools such as [RTK](https://github.com/rtk-ai/rtk). RTK is a CLI proxy that filters common development command output before it enters the LLM context, with claimed 60-90% reductions on commands such as `git diff`, `rg`, test runners, build tools, Docker, and GitHub CLI.

RTK is especially useful for ccflow phases that run command-heavy verification and review:

- Phase 3-5: test, build, lint, and type-check output
- Phase 6-7: `git diff`, changed-file lists, and reviewer context
- Phase 9: `git status`, `git log`, `gh`, push/rebase diagnostics

Install and initialize RTK separately:

```bash
# Linux/macOS quick install
curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh

# Initialize for Claude Code
rtk init -g
```

After restarting Claude Code, Bash commands are rewritten through RTK automatically where supported. Claude Code built-in tools such as `Read`, `Grep`, and `Glob` do not pass through RTK hooks, so keep using ccflow's lazy phase files and concise agent outputs for context reduction inside the plugin itself.

## Ticket Splitting

When a ticket is sized M or L during `/ccflow:refine`, the skill suggests splitting it into numbered child tickets (e.g., "(1/3)", "(2/3)", "(3/3)") with explicit dependency ordering — which children can be implemented in parallel and which are sequential. Each child references the parent in its body and the parent tracks all children in a "Child Tickets" checklist with dependencies. When `/ccflow:implement` creates a PR for the last open child, it auto-closes the parent alongside the child.

## Architecture

The plugin uses specialized agents with isolated contexts:

| Agent | Role | Model | Permission Mode |
|-------|------|-------|-----------------|
| **context-gatherer** | Bundles ticket, design, and project context into a file for the planner | sonnet | acceptEdits |
| **planner** | Analyzes tickets, produces implementation plans | inherit | plan (read-only) |
| **implementer** | TDD: writes tests first, then implementation | inherit | acceptEdits |
| **security-reviewer** | OWASP-focused security review | sonnet | plan (read-only) |
| **code-reviewer** | PR-style quality review | sonnet | plan (read-only) |
| **lessons-collector** | Routes genuine mistakes to `docs/<topic>.md` or `CLAUDE.md` | haiku | acceptEdits |

External integrations use the `gh` CLI rather than MCP servers, keeping permissions simple and avoiding token overhead. Optional MCP servers: Context7 (live documentation lookup) and Pencil (design file creation via `/ccflow:design`).

## Known Limitations

- **SSH git remotes + sandbox**: The sandbox uses `allowedDomains` for network filtering, which works with HTTPS but not SSH. If you have an SSH remote (`git@github.com:...`), `git push` will fail inside the sandbox. **Recommended**: switch to HTTPS remotes (`git remote set-url origin https://github.com/<owner>/<repo>.git`), or push manually when prompted.
- **New repos with no commits**: `git worktree add` requires at least one commit. The pipeline handles this automatically by creating an initial commit if needed.

## Troubleshooting

### `git push` fails inside sandbox
The sandbox blocks SSH connections. Options:
1. Switch to HTTPS: `git remote set-url origin https://github.com/<owner>/<repo>.git`
2. Push manually when the pipeline prompts you
3. Disable sandbox in `.claude/settings.json` (not recommended)

### `git worktree add` fails with "not a valid reference"
Your repo has no commits. The pipeline should handle this automatically. If it doesn't, create an initial commit: `git add -A && git commit -m "chore: initial commit" --allow-empty`

### Sandbox permissions errors on Linux
Ensure bubblewrap and socat are installed:
```bash
sudo apt install bubblewrap socat   # Debian/Ubuntu
sudo pacman -S bubblewrap socat     # Arch
```

### GitHub CLI not authenticated
Run `gh auth login` and follow the prompts. Verify with `gh auth status`.

### Agent prompts for file edit permissions
This should not happen with the default settings. Verify `.claude/settings.json` includes `Write(*)` and `Edit(*)` in `permissions.allow`. Running `/ccflow:implement` will auto-detect missing permissions and offer to fix them, or you can re-run `/ccflow:configure` to regenerate settings.

### Subagent reviews blocked: "Usage credits required for 1M context"
The pipeline ran inline and skipped the dedicated reviewer agents (security-reviewer, code-reviewer, silent-failure-hunter). This happens when your session runs a **1M-context** model (model ID ends in `[1m]`, e.g. `claude-opus-4-8[1m]`).

The `[1m]` flag is session-level: every subagent inherits it but **not** the session's extra-usage entitlement, so `Task` delegation is gated — even with a `model: sonnet` override and even with usage credits enabled (Claude Code bug [#51060](https://github.com/anthropics/claude-code/issues/51060) / [#57249](https://github.com/anthropics/claude-code/issues/57249)). ccflow's reviewers need the standard 200K context.

- **Permanently (keeps your main session on 1M):** run `/ccflow:configure` and answer **Yes** to "Pin subagents to 200K" — it sets `CLAUDE_CODE_SUBAGENT_MODEL=claude-sonnet-4-6` in `~/.claude/settings.json` so every subagent runs on Sonnet 200K while your main session keeps 1M. Restart for it to take effect. (Pin Sonnet, not Opus: Opus auto-upgrades to 1M on Max/Team/Enterprise plans and would re-trigger the gate.)
- **Now (this session), or if pinning doesn't clear the gate:** run `/model sonnet` to put the whole session on 200K, then re-invoke the skill. Note `/model opus` will *not* drop you to 200K on a plan that auto-upgrades Opus to 1M.

## Project Structure

```
ccflow/
├── .claude-plugin/
│   └── plugin.json
├── .mcp.json
├── .lsp.json              # LSP server configuration (generated by configure)
├── agents/
│   ├── context-gatherer.md
│   ├── planner.md
│   ├── implementer.md
│   ├── security-reviewer.md
│   ├── code-reviewer.md
│   └── lessons-collector.md
├── skills/
│   ├── configure/SKILL.md
│   ├── refine/SKILL.md
│   ├── design/SKILL.md
│   ├── sync/SKILL.md
│   ├── implement/
│   │   ├── SKILL.md
│   │   └── phases/
│   ├── address-review/SKILL.md
│   ├── worktrees/SKILL.md
│   ├── testing/SKILL.md
│   ├── stack-dotnet/SKILL.md
│   └── stack-angular/SKILL.md
├── hooks/
│   └── hooks.json
├── templates/
│   ├── claudeignore
│   ├── claude-md-root.md
│   ├── claude-md-root-monorepo.md
│   ├── claude-md-project.md
│   ├── settings.json
│   └── docs/
│       └── git-workflow.md
└── README.md
```
