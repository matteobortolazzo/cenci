# agentflow — Engineering Conventions and Claude Code Workflow

> Part of [agent-stack](../README.md) — the **workflow layer**. See the root README for
> the one-command install and how the isolation, workflow, and attention layers fit together.

Ticket refinement and automated implementation pipeline for GitHub.

## What it does

| Skill | Description |
|-------|-------------|
| `/agentflow:configure` | Interactive project setup: tech stack, sandboxing, MCP/LSP servers |
| `/agentflow:refine <ticket-id>` | Iterative ticket refinement until it's ready for planning |
| `/agentflow:design <ticket-id \| description>` | Interactive design reasoning and `.pen` file creation using Pencil |
| `/agentflow:implement <ticket-id>` | Full pipeline: plan, test, implement, refactor, security review, code review, lessons, PR |
| `/agentflow:address-review <pr-number>` | Address PR review comments — fetch, evaluate, fix, reply, push, re-request review |
| `/agentflow:babysit <pr-number>` | Loop-driven PR follow-through — periodically checks CI and new review comments and drives them to resolution until the PR merges or closes |
| `/agentflow:sync` | Pull latest main, rebase active worktrees, prune stale remotes, clean up merged branches |

**Codex support**: Codex receives the portable convention skills below plus the full
implementation workflow as a documented `AGENTS.md` equivalent. The interactive
pipeline itself remains Claude Code-only. See [`docs/codex.md`](docs/codex.md).

## Skill portability

The same `skills/` directory is installed in both clients. Portable skills avoid
depending on one client's tools; Claude-only descriptions are deliberately visible in
Codex so it does not mistake a pipeline command for a supported workflow.

| Skill | Claude Code | Codex | Notes |
|-------|-------------|-------|-------|
| `attachments` | Yes | Yes | Uses the active client's user-input and file/image tools |
| `frontend-classification` | Yes | Yes | Pure classification rule |
| `pr-comment-filter` | Yes | Yes | Pure review-filtering rule |
| `shell-rules` | Yes | Yes | Shared rules with client-specific approval notes |
| `stack-angular` | Yes | Yes | Framework and test conventions |
| `stack-dotnet` | Yes | Yes | Framework and test conventions |
| `stack-go` | Yes | Yes | Framework and test conventions; documentation lookup is client-neutral |
| `subagent-safety` | Yes | Yes | Shared delegation boundary with client-specific notes |
| `testing` | Yes | Yes | TDD and test-quality conventions |
| `verify-ui` | Yes | Yes | Playwright/Pencil visual-verification procedure; browser tooling availability is client-neutral |
| `worktrees` | Yes | Yes | Git worktree conventions |
| `address-review` | Yes | No | Claude interactive gates and pipeline mutations |
| `babysit` | Yes | No | Claude loop scheduling and slash commands |
| `configure` | Yes | No | Writes Claude settings and plugin configuration |
| `design` | Yes | No | Claude interactive gates and Pencil integration |
| `implement` | Yes | No | Claude subagents, hooks, goals, and approval gates |
| `refactor` | Yes | No | Claude analysis subagents and ticket workflow |
| `refine` | Yes | No | Claude interactive refinement gate |
| `review` | Yes | No | Claude specialized reviewer subagents |
| `sync` | Yes | No | Claude command/model invocation extensions |

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

Run `/agentflow:configure` to detect and enable LSP servers for your project.

### Authentication

```bash
gh auth login
```
The `gh` CLI stores credentials in `~/.config/gh/hosts.yml`. It also respects `GITHUB_TOKEN`/`GH_TOKEN` env vars as a fallback for non-interactive environments.

### Runtime — the `agent-sand` container

agentflow runs inside the [`dev-sandbox`](../dev-sandbox) `agent-sand` container with `--dangerously-skip-permissions`. The **container is the security boundary** — it provides the filesystem and network isolation for autonomous execution, so Claude Code's own host sandbox stays disabled. `permissions.allow`/`deny` are still written to `.claude/settings.json` as defense-in-depth for the case where you run plain `claude` (no skip-permissions) inside the container, e.g. via `agent-sand --shell`.

There are no bubblewrap/socat prerequisites — the container supplies the isolation. Launch it with `agent-sand` (see the [`dev-sandbox` README](../dev-sandbox)), then run `/agentflow:configure` inside it.

## Installation

### Via the agent-stack installer (recommended)

The [one-command installer](../docs/getting-started.md) installs agentflow together with
the other layers, checks prerequisites, and walks you through the setup:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash
```

### Via marketplace

```bash
# Claude Code
claude plugin marketplace add matteobortolazzo/agent-stack
claude plugin install agentflow

# Codex (separate client-local install, same marketplace)
codex plugin marketplace add matteobortolazzo/agent-stack
codex plugin add agentflow@agent-stack
```

To update later, run the agent-stack installer or the corresponding client's
marketplace/plugin update commands.

### Manual (per-session)

```bash
claude --plugin-dir /path/to/agentflow
```

## Quick Start

```bash
# 1. Start Claude Code (plugin loads automatically if installed via marketplace)
claude

# 2. Configure the project (one-time setup)
/agentflow:configure

# 3. Refine a ticket (optional but recommended)
/agentflow:refine 12345

# 4. Design a ticket (optional — for frontend/UI tickets)
/agentflow:design 12345

# 5. Implement a ticket
/agentflow:implement 12345
```

## Working from your phone

agentflow skills (`/refine`, `/implement`, `/design`) are **interactive** — they ask clarifying questions and iterate. Triggering them via one-shot mechanisms (GitHub Actions, webhook, `claude --print`) drops all conversation state after each turn, which defeats their whole design.

**The fit-for-purpose answer is SSH + tmux**, which you probably already have:

1. **On laptop**: keep Claude Code running in a named tmux window (e.g. `tmux new -As agentflow`).
2. **Expose the laptop to your phone** via [Tailscale](https://tailscale.com) (or any VPN/SSH-accessible network).
3. **On phone**: install an SSH client — [Blink](https://blink.sh) (iOS), [Termius](https://termius.com) (iOS/Android), or [Termux](https://termux.dev) (Android).
4. **From phone**: SSH into your laptop, `tmux attach -t agentflow`, and type `/agentflow:refine 42` — you get the full interactive experience. Close the app; tmux keeps the session alive; reconnect anytime.

**Why this beats GH-comment or remote bots:**
- Real conversation state — skills ask questions, you answer, they proceed. Just like your desk.
- `/clear`, `/compact`, and every other Claude Code feature works normally.
- No new code to maintain, no webhook infra, no session-resume plumbing.
- Browse issues in the GH mobile app; trigger skills via SSH. Two apps, zero friction.

### What `/agentflow:configure` creates

```
your-project/
├── CLAUDE.md              # (or in .claude/ — user's choice during configure)
├── .claudeignore          # Files tracked by git but excluded from Claude's context
├── docs/
│   └── git-workflow.md    # On-demand reference: branching, commits, PRs
├── .claude/
│   ├── config.json        # agentflow configuration (includes claudeMdLocation)
│   └── settings.json      # permissions (sandbox disabled; container is the boundary)
└── .worktrees/            # Git worktrees for feature branches (gitignored)
```

**Where reference docs live**

- `docs/<topic>.md` — on-demand reference docs read by skills/agents when their topic intersects the work. The lessons-collector also routes new topic-specific lessons here.
- `CLAUDE.md` — always loaded. Holds critical rules and project-wide invariants.
- `.claude/rules/` — reserved for files explicitly `@`-imported by `CLAUDE.md` (auto-loaded at session start). Configure does not create files here today.

**Backward compatibility**

If your project already has `.claude/rules/lessons-learned.md` (or `lessons-learned-<slug>.md`) from an earlier agentflow setup, agentflow leaves it in place and skills still read it as a legacy fallback. New lessons go to `docs/` or `CLAUDE.md` only.

### Monorepo Support

For monorepos, `/agentflow:configure` detects projects automatically and creates a **progressive disclosure** structure — project-specific context only loads when Claude accesses files in that subtree, saving tokens.

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
│   ├── config.json            # agentflow configuration (includes isMonorepo + projects)
│   └── settings.json          # Sandbox, permissions, and allowed domains
└── .worktrees/
```

Lessons are routed by topic (`docs/caching.md`, `docs/migrations.md`, …) rather than dumped into a single growing log. Project-wide invariants land in `CLAUDE.md` directly.

## Implementation Pipeline

When you run `/agentflow:implement <ticket-id>`, the pipeline executes these phases:

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
| `Refined` | `/agentflow:refine` | Scoped and ready for design/implementation |
| `Design` | `/agentflow:refine` | Design-only ticket — the deliverable is a design spec, not code |
| `Designed` | `/agentflow:design` | Design spec approved — propagated from the completed design ticket to the implementation tickets that depend on it |
| `Planned` | `/agentflow:implement` — Phase 1, at plan approval | Approved plan on disk (`.plans/`), ready to pick up |
| `Working` | `/agentflow:implement` — at pipeline start | Actively being implemented |
| `In Review` | `/agentflow:implement` — Phase 9, at PR-open | PR is open, under review / CI running |
| `Implemented` | `/agentflow:babysit` — on PR merge | PR merged — done |

Full lifecycle: `New → Refined → [Designed] → Planned → Working → In Review → Implemented`. **Design always happens on a dedicated design ticket**: when a frontend ticket lacks an approved design, `/agentflow:refine` creates a `Design`-labeled companion ticket (or leads a split with a design child) that the implementation ticket depends on. `/agentflow:implement` redirects `Design` tickets to `/agentflow:design`, which commits the spec on main, propagates `Designed` to the dependent implementation tickets (satisfying implement's design gate), and closes the design ticket (`New → Refined → Designed → closed`; no PR — the one exception to "1 ticket = 1 PR"). On the board, the `Designed` column holds implementation tickets whose design is ready. A planning session ends on **`Planned`** — an approved plan sits in `.plans/`, waiting; picking it up with `/agentflow:implement .plans/<file>` swaps `Planned → Working`. Opening the PR (Phase 9) only advances the ticket to **`In Review`**; the transition to **`Implemented`** happens when the PR merges — [babysit](#babysitting-a-pr) performs that swap using the merged PR's `closingIssuesReferences`. (`configure` documents these labels but does not create them; add the matching columns to your board.)

### Autopilot (goal-driven completion)

Planning stops for your approval (Phase 1); once approved, phases 2–9 run unattended through to an open PR. But a turn that stops mid-phase — a context limit, a transient tool error — would otherwise just end the run with the work half-done.

When Claude Code is **≥ 2.1.139**, the pipeline closes that gap with the native [`/goal`](https://code.claude.com/docs/en/goal) command. At the start of Phase 2 (plan-file mode only) it arms a completion condition — "the plan `.plans/<id>.md` is implemented and a PR exists" — so any mid-phase stop restarts instead of ending. The goal is cleared automatically in Phase 9 once the PR is created, and at any error gate that hands control back to you (rebase conflict, repeated build failure, an ambiguous reviewer finding), so a genuine blocker never loops.

- **Plan approval is the human gate that arms it.** No goal is ever set in a planning session — approving the plan authorizes the autonomous run, and the goal is armed only when you launch `/agentflow:implement .plans/<id>.md`.
- **The condition references the plan file**, matching the SessionStart hook that reminds you of pending `.plans/` — a still-present plan file means "not done."
- **Graceful on older runtimes.** Below 2.1.139 (or if `/goal` is unavailable) the pipeline behaves exactly as before — it just prints a one-line notice and runs without the completion guarantee.
- **Stall cap.** The armed condition also carries a fixed 20-turn cap — if the goal restarts the turn more than 20 times without the pipeline advancing to a new phase, it stops retrying, clears itself, and reports the stall instead of looping forever.
- **Opt out** with `"agentflow": { "goalAutopilot": false }` in `.claude/config.json`.

### Babysitting a PR

Once a PR is open, `/agentflow:babysit <pr-number>` keeps it moving while you're away. It does
one **tick** immediately, then arms a self-paced Claude Code [`/loop`](https://code.claude.com/docs/en/loop)
that repeats the tick (~15 minutes by default; pass a second argument to change it, e.g.
`/agentflow:babysit 42 10m`). Each tick:

1. **Fetches PR state** — if the PR has **merged or closed**, it reports a final summary,
   stops the loop, and cleans up. On **merge**, it also performs the `In Review → Implemented`
   board transition, relabeling every issue the PR closed (from `closingIssuesReferences`).
   A PR closed **without** merging leaves labels untouched.
2. **Auto-fixes red CI** — diagnoses the failing checks, pushes a fix (never force-pushes),
   and retries up to a per-commit cap. When the cap is hit or the cause is ambiguous
   (flaky/infra/external), it escalates to you via a question instead of looping blindly.
3. **Drives new review comments** through [`/agentflow:address-review`](#what-it-does), which
   keeps its own approval gate — you still confirm the plan before any fix is pushed. A
   watermark tracks already-handled comments so the same feedback is never re-addressed.

A quiet tick (green CI, no new comments) just reports one line and schedules the next check —
each successive quiet tick doubles the wait (capped at 60 minutes), so a stalled PR is checked
less and less often, while any actionable tick (a CI fix or an addressed comment) resets the
pacing back to the base interval.

Each tick prefers a deterministic helper script (`skills/babysit/scripts/tick.sh`) to gather
PR state, CI results, and mechanically-filtered candidate comments in one call, falling back
to the equivalent raw `gh` calls if the script is unavailable or its output can't be parsed.

- **Session-scoped, 7-day expiry.** The `/loop` lives as long as the Claude Code session and
  at most 7 days. If the session ends, re-run `/agentflow:babysit <pr>` to resume.
- **Self-paced pacing needs native support.** On Bedrock / Vertex / Foundry, self-paced
  `/loop` falls back to a fixed ~10-minute schedule, so the custom interval is best-effort
  there.
- **Human gates preserved.** The `address-review` approval, the CI-escalation question, and
  the never-force-push rule all hold — babysit automates the checking and the safe fixes,
  not the decisions.

On merge, babysit performs the `In Review → Implemented` board transition (see the terminal-tick behavior above and the [Board lifecycle](#board-lifecycle) table) — relabeling each issue closed by the merged PR.

### UI tickets

UI implementations are the most error-prone, so the pipeline adds two guards for tickets classified as frontend:

- **Design gate (hard)** — a UI ticket without the `Designed` label stops the pipeline and points at the feature's design ticket: run `/agentflow:design <design-ticket-id>` if one exists (completing it propagates `Designed` here), or `/agentflow:refine` to create one — or proceed anyway. An existing `DESIGN.md` doesn't bypass the gate, since the design path persists across tickets.
- **PR screenshots** — screenshots captured during visual verification (`playwright-cli`) are embedded in the PR body to speed up review. They're hosted in a temporary **secret gist** rather than committed to the repo; delete it after merge with `gh gist delete <gist-id>` (the PR body includes the command).

### Usage controls

For lower limit pressure without removing quality gates, add optional settings to `.claude/config.json`:

```json
{
  "agentflow": {
    "compactImplementation": false,
    "reviewConcurrency": "parallel",
    "diffContextMode": "inline",
    "liteReviewEnabled": true,
    "goalAutopilot": true
  }
}
```

- `compactImplementation: true` lets small, low-risk tickets combine red/green/refactor into one implementer turn while still requiring red failures, green implementation, refactor, and final build/test reporting.
- `reviewConcurrency: "sequential"` runs the same security, code, and silent-failure reviewers one after another instead of in parallel.
- `diffContextMode: "file"` passes reviewers a patch file path and changed-file list for large diffs instead of duplicating the full diff in every prompt.
- `liteReviewEnabled: true` (default) classifies each diff into `full` (all three reviewers), `lite-docs` (no reviewers, for docs-only diffs), or `lite-small` (`code-reviewer` only, for small config/data-only diffs). Anything touching `.claude/**`, `skills/**`, `agents/**`, `CLAUDE.md`, or a danger pattern (auth/security/secrets/CI workflows) always forces `full`. Set to `false` to force the full trio on every run.
- `goalAutopilot: false` disables the [goal-driven autopilot](#autopilot-goal-driven-completion) (armed by default on Claude Code ≥ 2.1.139).

### Optional: RTK command-output compression

agentflow also benefits from external command-output compression tools such as [RTK](https://github.com/rtk-ai/rtk). RTK is a CLI proxy that filters common development command output before it enters the LLM context, with claimed 60-90% reductions on commands such as `git diff`, `rg`, test runners, build tools, Docker, and GitHub CLI.

RTK is especially useful for agentflow phases that run command-heavy verification and review:

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

After restarting Claude Code, Bash commands are rewritten through RTK automatically where supported. Claude Code built-in tools such as `Read`, `Grep`, and `Glob` do not pass through RTK hooks, so keep using agentflow's lazy phase files and concise agent outputs for context reduction inside the plugin itself.

## Ticket Splitting

When a ticket is sized M or L during `/agentflow:refine`, the skill suggests splitting it into numbered child tickets (e.g., "(1/3)", "(2/3)", "(3/3)") with explicit dependency ordering — which children can be implemented in parallel and which are sequential. Each child references the parent in its body and the parent tracks all children in a "Child Tickets" checklist with dependencies. When `/agentflow:implement` creates a PR for the last open child, it auto-closes the parent alongside the child.

## Architecture

The plugin uses specialized agents with isolated contexts:

| Agent | Role | Model | Effort | Permission Mode |
|-------|------|-------|--------|-----------------|
| **context-gatherer** | Bundles ticket, design, and project context into a file for the planner | haiku | n/a (haiku) | acceptEdits |
| **planner** | Analyzes tickets, produces implementation plans | opus | high (pinned) | plan (read-only) |
| **implementer** | TDD: writes tests first, then implementation | sonnet | high (pinned) | acceptEdits |
| **security-reviewer** | OWASP-focused security review | opus | high (pinned) | plan (read-only) |
| **code-reviewer** | PR-style quality review | sonnet | high (pinned) | plan (read-only) |
| **silent-failure-hunter** | Swallowed-error and silent-fallback detection | sonnet | high (pinned) | plan (read-only) |
| **duplication-analyzer** | Copy-paste and extraction analysis for `/agentflow:refactor` | sonnet | high (pinned) | plan (read-only) |
| **structure-analyzer** | File-size and test-organization analysis for `/agentflow:refactor` | haiku | n/a (haiku) | plan (read-only) |
| **security-analyzer** | OWASP audit for `/agentflow:refactor` | sonnet | high (pinned) | plan (read-only) |
| **lessons-collector** | Routes genuine mistakes to `docs/<topic>.md` or `CLAUDE.md` | haiku | n/a (haiku) | acceptEdits |

**Model & effort tiering**: Opus where judgment is concentrated — `/agentflow:refine` and `/agentflow:design` pin `model: opus` because scope, acceptance criteria, splits, and UX structure drive everything downstream, and the **planner** and **security-reviewer** agents run opus because the approved plan steers the whole unattended pipeline and a missed vulnerability is the costliest review failure. Sonnet for pipeline orchestration and implementation (`/agentflow:implement` pins `model: sonnet`; `/agentflow:babysit` pins `model: sonnet` so long-lived loop ticks stay cheap). Haiku for mechanical work — context-gatherer, structure-analyzer, lessons-collector, and `/agentflow:sync`.

Effort is the second lever: model picks how *capable* the agent is (failures that look like "it didn't know enough"), effort picks how *thorough* it is (failures that look like "it didn't try hard enough"). Subagents inherit the **session** effort level by default, so a session running at low effort would silently degrade the unattended pipeline's planning, implementation, and reviews. Every non-haiku agent therefore pins `effort: high` — thoroughness is guaranteed regardless of the session setting. Skills deliberately stay unpinned: they run during interactive phases, where the user's session effort preference should win. Haiku agents can't be tuned — haiku doesn't support effort.

Tiering intentionally stops at Opus. Fable is a specialist tier at the highest per-token cost; for a genuinely hard domain you can escalate an individual agent by setting `model: fable` in its frontmatter.

These pins are visible in each skill's and agent's frontmatter and override the session model for that skill/agent only. **Caveat**: the `CLAUDE_CODE_SUBAGENT_MODEL` pin (see Troubleshooting) overrides agent frontmatter and flattens the model tiering — set it only on 1M-context sessions where the delegation gate applies. Likewise, the `CLAUDE_CODE_EFFORT_LEVEL` env var overrides `effort:` frontmatter and flattens the effort tiering while set.

External integrations use the `gh` CLI rather than MCP servers, keeping permissions simple and avoiding token overhead. Optional MCP servers: Context7 (live documentation lookup) and Pencil (design file creation via `/agentflow:design`).

## Known Limitations

- **SSH git remotes in the container**: The `agent-sand` container injects the `gh` CLI's HTTPS credentials only (`~/.config/gh/hosts.yml`) — your SSH keys are **not** mounted into the container. So pushing to an SSH remote (`git@github.com:...`) has no credentials and fails. **Recommended**: use HTTPS remotes (`git remote set-url origin https://github.com/<owner>/<repo>.git`) so the `gh` credential helper authenticates the push, or push manually when prompted.
- **New repos with no commits**: `git worktree add` requires at least one commit. The pipeline handles this automatically by creating an initial commit if needed.

## Troubleshooting

### `git push` fails inside the container
The container has no SSH keys — only the `gh` CLI's HTTPS credentials are injected. An SSH remote (`git@github.com:...`) has nothing to authenticate with. Options:
1. Switch to HTTPS so the `gh` credential helper authenticates the push: `git remote set-url origin https://github.com/<owner>/<repo>.git`
2. Push manually when the pipeline prompts you

### `git worktree add` fails with "not a valid reference"
Your repo has no commits. The pipeline should handle this automatically. If it doesn't, create an initial commit: `git add -A && git commit -m "chore: initial commit" --allow-empty`

### GitHub CLI not authenticated
Run `gh auth login` and follow the prompts. Verify with `gh auth status`.

### Agent prompts for file edit permissions
This should not happen inside the `agent-sand` container, where Claude Code runs with `--dangerously-skip-permissions` and ignores `permissions.allow`/`deny` entirely. If you see prompts, you are likely running plain `claude` (no skip-permissions) — verify `.claude/settings.json` includes `Write(*)` and `Edit(*)` in `permissions.allow`, or re-run `/agentflow:configure` to regenerate settings.

### Subagent reviews blocked: "Usage credits required for 1M context"
The pipeline ran inline and skipped the dedicated reviewer agents (security-reviewer, code-reviewer, silent-failure-hunter). This happens when your session runs a **1M-context** model (model ID ends in `[1m]`, e.g. `claude-opus-4-8[1m]`).

The `[1m]` flag is session-level: every subagent inherits it but **not** the session's extra-usage entitlement, so `Task` delegation is gated — even with a `model: sonnet` override and even with usage credits enabled (Claude Code bug [#51060](https://github.com/anthropics/claude-code/issues/51060) / [#57249](https://github.com/anthropics/claude-code/issues/57249)). agentflow's reviewers need the standard 200K context.

- **Permanently (keeps your main session on 1M):** run `/agentflow:configure` and answer **Yes** to "Pin subagents to 200K" — it sets `CLAUDE_CODE_SUBAGENT_MODEL=claude-sonnet-5` in `~/.claude/settings.json` so every subagent runs on Sonnet 200K while your main session keeps 1M. Restart for it to take effect. (Pin Sonnet, not Opus: Opus auto-upgrades to 1M on Max/Team/Enterprise plans and would re-trigger the gate. Note the pin overrides agent `model:` frontmatter, flattening agentflow's model tiering while set — unset it on standard 200K sessions.)
- **Now (this session), or if pinning doesn't clear the gate:** run `/model sonnet` to put the whole session on 200K, then re-invoke the skill. Note `/model opus` will *not* drop you to 200K on a plan that auto-upgrades Opus to 1M.

## Project Structure

```
agentflow/
├── .claude-plugin/
│   └── plugin.json
├── .codex-plugin/
│   └── plugin.json
├── .mcp.json
├── .lsp.json              # LSP server configuration (generated by configure)
├── agents/
│   ├── context-gatherer.md
│   ├── planner.md
│   ├── implementer.md
│   ├── security-reviewer.md
│   ├── code-reviewer.md
│   ├── silent-failure-hunter.md
│   ├── duplication-analyzer.md
│   ├── security-analyzer.md
│   ├── structure-analyzer.md
│   └── lessons-collector.md
├── skills/
│   ├── configure/SKILL.md
│   ├── refine/SKILL.md
│   ├── design/SKILL.md
│   ├── sync/SKILL.md
│   ├── implement/
│   │   ├── SKILL.md
│   │   └── phases/
│   ├── review/SKILL.md
│   ├── refactor/SKILL.md
│   ├── address-review/SKILL.md
│   ├── babysit/SKILL.md
│   ├── worktrees/SKILL.md
│   ├── testing/SKILL.md
│   ├── shell-rules/SKILL.md
│   ├── subagent-safety/SKILL.md
│   ├── attachments/SKILL.md
│   ├── pr-comment-filter/SKILL.md
│   ├── frontend-classification/SKILL.md
│   ├── verify-ui/SKILL.md
│   ├── stack-dotnet/SKILL.md
│   ├── stack-angular/SKILL.md
│   └── stack-go/SKILL.md
├── hooks/
│   └── hooks.json
├── docs/
│   ├── git-workflow.md        # On-demand reference (read by skills as needed)
│   └── codex.md               # What agentflow offers OpenAI Codex, and how it wires
├── templates/
│   ├── claudeignore
│   ├── claude-md-root.md
│   ├── claude-md-root-monorepo.md
│   ├── claude-md-project.md
│   ├── settings.json
│   ├── agents-md-codex.md          # AGENTS.md recipe: agentflow workflow as prose for Codex
│   ├── agentwatch-codex-config.json # Codex agent block for agentwatch's run launcher
│   └── docs/
│       └── git-workflow.md
└── README.md
```
