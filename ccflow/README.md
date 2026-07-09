# ccflow — Claude Code Workflow Plugin

> Part of [claude-tools](../README.md) — the **workflow layer**. See the root README for
> the one-command install and how the isolation, workflow, and attention layers fit together.

Ticket refinement and automated implementation pipeline for GitHub.

## What it does

| Skill | Description |
|-------|-------------|
| `/ccflow:configure` | Interactive project setup: tech stack, sandboxing, MCP/LSP servers |
| `/ccflow:refine <ticket-id>` | Iterative ticket refinement until it's ready for planning |
| `/ccflow:design <ticket-id \| description>` | Interactive design reasoning and `.pen` file creation using Pencil |
| `/ccflow:implement <ticket-id>` | Full pipeline: plan, test, implement, refactor, security review, code review, lessons, PR |
| `/ccflow:address-review <pr-number>` | Address PR review comments — fetch, evaluate, fix, reply, push, re-request review |
| `/ccflow:babysit <pr-number>` | Loop-driven PR follow-through — periodically checks CI and new review comments and drives them to resolution until the PR merges or closes |
| `/ccflow:sync` | Pull latest main, rebase active worktrees, prune stale remotes, clean up merged branches |

**Codex support**: the implementation workflow is also available to OpenAI Codex as a documented prose equivalent (an `AGENTS.md` recipe) — see [`docs/codex.md`](docs/codex.md).

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

### Sandbox support (host profile — Linux / WSL2)

ccflow runs under one of two **profiles**, auto-detected by `/ccflow:configure`:

- **host** (default) — Claude Code runs on the host and its own sandbox is the boundary. The prerequisites below apply.
- **container** — Claude Code runs inside the [`dev-sandbox`](../dev-sandbox) `claude-sand` container with `--dangerously-skip-permissions`. The **container is the boundary**, so configure auto-detects it (via the `CLAUDE_SAND=1` env var, or `/.dockerenv`) and uses the container profile: the host sandbox is skipped, along with Bash allowlists and permission auto-fix. No bubblewrap/socat needed. You can force it with `/ccflow:configure container` (or `/ccflow:configure host` to opt back out).

The **host-profile** sandbox provides OS-level filesystem and network isolation for autonomous execution. It requires:
- **bubblewrap** (`bwrap`): `sudo apt install bubblewrap` (or `sudo pacman -S bubblewrap`)
- **socat**: `sudo apt install socat` (or `sudo pacman -S socat`)

macOS host-profile sandbox support is built into Claude Code and requires no extra packages.

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
4. **Implement (Green)** — Implementer agent makes tests pass; UI tickets also get visual verification, with final screenshots persisted for the PR
5. **Refactor** — Implementer agent simplifies and cleans up
6. **Security Review** — Security reviewer agent checks for OWASP vulnerabilities
7. **Code Review** — Code reviewer agent does a final PR-style review
8. **Capture Lessons** — Lessons collector routes genuine mistakes into the relevant `docs/<topic>.md` or `CLAUDE.md` (opt-in; most sessions capture nothing)
9. **Create PR** — Rebases on latest main, commits, pushes, and creates a pull request; UI tickets get a `## Screenshots` section in the PR body

### Board lifecycle

The skills drive a ticket through a label-based state machine (`gh issue edit`). Each label maps to a board column:

| State | Applied by | Meaning |
|---|---|---|
| `Refined` | `/ccflow:refine` | Scoped and ready for design/implementation |
| `Designed` | `/ccflow:design` | UI design spec approved (frontend tickets) |
| `Planned` | `/ccflow:implement` — Phase 1, at plan approval | Approved plan on disk (`.plans/`), ready to pick up |
| `Working` | `/ccflow:implement` — at pipeline start | Actively being implemented |
| `In Review` | `/ccflow:implement` — Phase 9, at PR-open | PR is open, under review / CI running |
| `Implemented` | `/ccflow:babysit` — on PR merge | PR merged — done |

Full lifecycle: `New → Refined → [Designed] → Planned → Working → In Review → Implemented`. A planning session ends on **`Planned`** — an approved plan sits in `.plans/`, waiting; picking it up with `/ccflow:implement .plans/<file>` swaps `Planned → Working`. Opening the PR (Phase 9) only advances the ticket to **`In Review`**; the transition to **`Implemented`** happens when the PR merges — [babysit](#babysitting-a-pr) performs that swap using the merged PR's `closingIssuesReferences`. (`configure` documents these labels but does not create them; add the matching columns to your board.)

### Autopilot (goal-driven completion)

Planning stops for your approval (Phase 1); once approved, phases 2–9 run unattended through to an open PR. But a turn that stops mid-phase — a context limit, a transient tool error — would otherwise just end the run with the work half-done.

When Claude Code is **≥ 2.1.139**, the pipeline closes that gap with the native [`/goal`](https://code.claude.com/docs/en/goal) command. At the start of Phase 2 (plan-file mode only) it arms a completion condition — "the plan `.plans/<id>.md` is implemented and a PR exists" — so any mid-phase stop restarts instead of ending. The goal is cleared automatically in Phase 9 once the PR is created, and at any error gate that hands control back to you (rebase conflict, repeated build failure, an ambiguous reviewer finding), so a genuine blocker never loops.

- **Plan approval is the human gate that arms it.** No goal is ever set in a planning session — approving the plan authorizes the autonomous run, and the goal is armed only when you launch `/ccflow:implement .plans/<id>.md`.
- **The condition references the plan file**, matching the SessionStart hook that reminds you of pending `.plans/` — a still-present plan file means "not done."
- **Graceful on older runtimes.** Below 2.1.139 (or if `/goal` is unavailable) the pipeline behaves exactly as before — it just prints a one-line notice and runs without the completion guarantee.
- **Opt out** with `"ccflow": { "goalAutopilot": false }` in `.claude/config.json`.

### Babysitting a PR

Once a PR is open, `/ccflow:babysit <pr-number>` keeps it moving while you're away. It does
one **tick** immediately, then arms a self-paced Claude Code [`/loop`](https://code.claude.com/docs/en/loop)
that repeats the tick (~15 minutes by default; pass a second argument to change it, e.g.
`/ccflow:babysit 42 10m`). Each tick:

1. **Fetches PR state** — if the PR has **merged or closed**, it reports a final summary,
   stops the loop, and cleans up. On **merge**, it also performs the `In Review → Implemented`
   board transition, relabeling every issue the PR closed (from `closingIssuesReferences`).
   A PR closed **without** merging leaves labels untouched.
2. **Auto-fixes red CI** — diagnoses the failing checks, pushes a fix (never force-pushes),
   and retries up to a per-commit cap. When the cap is hit or the cause is ambiguous
   (flaky/infra/external), it escalates to you via a question instead of looping blindly.
3. **Drives new review comments** through [`/ccflow:address-review`](#what-it-does), which
   keeps its own approval gate — you still confirm the plan before any fix is pushed. A
   watermark tracks already-handled comments so the same feedback is never re-addressed.

A quiet tick (green CI, no new comments) just reports one line and schedules the next check.

- **Session-scoped, 7-day expiry.** The `/loop` lives as long as the Claude Code session and
  at most 7 days. If the session ends, re-run `/ccflow:babysit <pr>` to resume.
- **Self-paced pacing needs native support.** On Bedrock / Vertex / Foundry, self-paced
  `/loop` falls back to a fixed ~10-minute schedule, so the custom interval is best-effort
  there.
- **Human gates preserved.** The `address-review` approval, the CI-escalation question, and
  the never-force-push rule all hold — babysit automates the checking and the safe fixes,
  not the decisions.

On merge, babysit performs the `In Review → Implemented` board transition (see the terminal-tick behavior above and the [Board lifecycle](#board-lifecycle) table) — relabeling each issue closed by the merged PR.

### UI tickets

UI implementations are the most error-prone, so the pipeline adds two guards for tickets classified as frontend:

- **Design gate (hard)** — a UI ticket without the `Designed` label stops the pipeline and asks whether to design first (`/ccflow:design`) or proceed anyway. An existing `DESIGN.md` doesn't bypass the gate, since the design path persists across tickets.
- **PR screenshots** — screenshots captured during visual verification (`playwright-cli`) are embedded in the PR body to speed up review. They're hosted in a temporary **secret gist** rather than committed to the repo; delete it after merge with `gh gist delete <gist-id>` (the PR body includes the command).

### Usage controls

For lower limit pressure without removing quality gates, add optional settings to `.claude/config.json`:

```json
{
  "ccflow": {
    "compactImplementation": false,
    "reviewConcurrency": "parallel",
    "diffContextMode": "inline",
    "goalAutopilot": true
  }
}
```

- `compactImplementation: true` lets small, low-risk tickets combine red/green/refactor into one implementer turn while still requiring red failures, green implementation, refactor, and final build/test reporting.
- `reviewConcurrency: "sequential"` runs the same security, code, and silent-failure reviewers one after another instead of in parallel.
- `diffContextMode: "file"` passes reviewers a patch file path and changed-file list for large diffs instead of duplicating the full diff in every prompt.
- `goalAutopilot: false` disables the [goal-driven autopilot](#autopilot-goal-driven-completion) (armed by default on Claude Code ≥ 2.1.139).

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

**Model tiering**: Opus where judgment is concentrated — `/ccflow:refine` and `/ccflow:design` pin `model: opus` because scope, acceptance criteria, splits, and UX structure drive everything downstream. Sonnet for pipeline orchestration and implementation (`/ccflow:implement` pins `model: sonnet`). Haiku for mechanical collection (lessons-collector). These pins are visible in each skill's frontmatter and override the session model for that skill only.

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
│   ├── babysit/SKILL.md
│   ├── worktrees/SKILL.md
│   ├── testing/SKILL.md
│   ├── stack-dotnet/SKILL.md
│   └── stack-angular/SKILL.md
├── hooks/
│   └── hooks.json
├── docs/
│   ├── git-workflow.md        # On-demand reference (read by skills as needed)
│   └── codex.md               # What ccflow offers OpenAI Codex, and how it wires
├── templates/
│   ├── claudeignore
│   ├── claude-md-root.md
│   ├── claude-md-root-monorepo.md
│   ├── claude-md-project.md
│   ├── settings.json
│   ├── agents-md-codex.md          # AGENTS.md recipe: ccflow workflow as prose for Codex
│   ├── agentwatch-codex-config.json # Codex agent block for agentwatch's run launcher
│   └── docs/
│       └── git-workflow.md
└── README.md
```
