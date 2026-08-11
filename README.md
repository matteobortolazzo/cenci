# cenci

**Let coding agents run longer. Keep the important decisions.**

[![Claude Code](https://img.shields.io/badge/Claude_Code-supported-d97757?style=flat-square)](#claude-code-codex-and-opencode)
[![Codex](https://img.shields.io/badge/Codex-supported-10a37f?style=flat-square)](flow/docs/codex.md)
[![OpenCode](https://img.shields.io/badge/OpenCode-supported-fab040?style=flat-square)](flow/docs/opencode.md)
[![Platforms](https://img.shields.io/badge/Linux_%C2%B7_macOS_%C2%B7_WSL2-supported-64748b?style=flat-square)](docs/getting-started.md)
[![License](https://img.shields.io/badge/license-MIT-8b5cf6?style=flat-square)](LICENSE)

cenci takes a GitHub issue to a tested, reviewed pull request. You approve the intent
and the plan; the agent works at full permissions inside a per-repository container;
live state returns to tmux and optional desktop surfaces so you know when to step in.

[Try it](#quickstart) · [See the workflow](#approve-decisions-not-commands) ·
[Read the security model](SECURITY.md) · [Browse the docs](#learn-more)

![cenci combines isolation, workflow, and attention into a safe path from issue to reviewed pull request](docs/assets/cenci-overview.svg)

One install adds three cooperating layers:

- **[Isolation](sandbox/README.md):** run the agent at full permissions inside a
  repository-scoped Docker or Podman container.
- **[Workflow](flow/README.md):** turn an issue into a tested, specialist-reviewed
  pull request, with human gates for the decisions that matter.
- **[Attention](watch/README.md):** see which sessions are running, done, idle, or
  waiting for you without watching every terminal.

Maintenance spans all three: `/cenci:maintain` audits and repairs workflow structure,
documentation, client adapters, and accumulated rules without requiring lazyboards.

## How the pieces work together

1. **Frame the work.** Start from a GitHub issue, refine its scope, and complete a
   design pass for UI work.
2. **Approve the handoff.** cenci saves the implementation plan in `.plans/`; that
   file preserves the decisions behind the run and makes it resumable.
3. **Deliver inside the boundary.** The implementation run creates a worktree, writes
   tests first, implements, refactors, runs specialist reviews, and opens the PR—all
   inside the repository-scoped container.
4. **Stay informed while it runs.** Native hooks publish live state to tmux and any
   enabled desktop surface, so attention is pulled only when the agent finishes or
   needs input.
5. **Follow through.** PR babysitting watches CI and review activity until merge while
   preserving approval gates for ambiguous fixes and reviewer feedback.

## See every session without watching every terminal

![A lazyboards board in a tmux window; in the tmux window list below, cenci-watch marks one agent window blue and running, one red and needing input, and one green and done](docs/assets/cenci-tmux.png)

cenci-watch marks each tmux window `▶` running, `!` needing input, or `✓` done.
Native Claude Code and Codex hooks update that state immediately; the same signal can
appear in Linux desktop bars and the macOS menu bar. The board shown above is optional
[lazyboards](https://github.com/matteobortolazzo/lazyboards), and the surrounding tmux
theme is user-provided.

[Explore tmux, status commands, and desktop integrations →](watch/README.md)

## Quickstart

Requirements: Linux, macOS, or WSL2; git; curl; jq; Docker or Podman; and Claude Code,
Codex, or both. [OpenCode](flow/docs/opencode.md) is supported as an additional,
opt-in agent — it layers on top of an existing Claude Code or Codex install rather
than standing alone.

**1. Install and verify.**

```bash
curl -fsSL -o install.sh https://github.com/matteobortolazzo/cenci/releases/latest/download/install.sh
curl -fsSL -o install.sh.bundle https://github.com/matteobortolazzo/cenci/releases/latest/download/install.sh.bundle
cosign verify-blob --bundle install.sh.bundle \
  --certificate-identity-regexp '^https://github\.com/matteobortolazzo/cenci/\.github/workflows/watch-release\.yml@refs/(heads/main|tags/watch/v[0-9]+\.[0-9]+\.[0-9]+)$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  install.sh
bash install.sh
cenci doctor
```

Requires [cosign](https://docs.sigstore.dev/system_config/installation/) — the installer
verifies its own bytes against the release before running, and fails closed with no
fallback to an unverified ref. The legacy one-liner still works and re-execs itself
through this same verified path:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
```

Resolves to the latest release tag by default. That resolved ref pins the client
marketplace manifests and all three plugins' content — not just which install.sh
runs — for every install, update, and repair run. Set `CENCI_REF=main` (or pass
`--ref main`) to explicitly opt into bleeding-edge, unverified main instead (unsafe;
development use only) — it is the only path that intentionally tracks main.

The installer detects your clients, reconciles all three layers on every run (adding
missing components and refreshing existing ones), and puts `cenci` and its `cn` launch
alias on your PATH. `doctor` reports what is ready and what needs attention without
changing anything.

**2. Launch from a git repository.**

```bash
cn      # Claude Code
cn xt   # Codex (or: cn --agent codex)
```

Only that repository is mounted at `/workspace`. The first session also provisions
cenci-watch and starts its shared host daemon.

**3. Run the gated workflow with Claude Code.**

```text
/cenci:configure
/cenci:refine 42
/cenci:implement 42
/cenci:babysit <pr-number>
```

The complete interactive ticket-to-PR workflow is Claude Code-only today. Codex
already supports isolation, monitoring, portable engineering conventions, and PR
babysitting; its native gated workflow is in development.

[Continue with setup details and troubleshooting →](docs/getting-started.md)

## Approve decisions, not commands

![cenci moves a ticket through human-gated refinement and planning, an autonomous engineering run, and PR follow-through](docs/assets/cenci-pipeline.svg)

You keep scope, tradeoffs, design choices, and plan approval. After that handoff,
cenci handles the worktree, tests, implementation, refactoring, specialist reviews,
rebase, and pull request. `/cenci:babysit` can then follow CI and review activity
through to merge while preserving the same human gates. The approved `.plans/` file
is a durable handoff, so the run can be resumed or dispatched without recreating the
decisions behind it. Normal `/cenci:implement` runs already automatically check
documentation and generated indexes affected by their own changes.

`/cenci:maintain` — audit and repair workflow, docs, client adapters, and accumulated
rules — is a separate, on-demand full-repo audit; it runs standalone and needs no
lazyboards setup.

[Explore the workflow and every skill →](flow/README.md)

## Full speed inside a deliberate boundary

![cenci-sandbox mounts the current repository into a deliberately small container boundary for a full-permission coding agent](docs/assets/cenci-sandbox-boundary.svg)

Claude Code runs with `--dangerously-skip-permissions` and Codex with
`--dangerously-bypass-approvals-and-sandbox`, but only inside the container. By
default, cenci mounts the current repository, not your whole host, and publishes no
inbound ports. This limits the host blast radius; you still trust what the agent
installs and runs inside that boundary.

[Understand the sandbox →](sandbox/README.md) ·
[Read its guarantees and limits →](SECURITY.md)

## Optional: use GitHub issues as the cockpit

![A lazyboards board driving cenci: live agent badges on cards, the dispatch panel, one-key refine and implement actions spawning running agents, and the agents overview](docs/assets/cenci-demo.gif)

cenci works from a plain terminal. If you prefer a board, optional lazyboards can
dispatch refinement and implementation with one keypress, show live agent state on
each card, and expose the same session overview. `cenci dispatch` can also pick up an
approved `Planned` ticket by policy. No board or LLM is required for the pickup step.

[Follow the board-orchestration recipe →](docs/orchestration.md) ·
[See the deterministic demo recipe →](demo.tape)

## Optional: go hands-off

Four opt-in switches remove the human gates one at a time — plans that approve
themselves, dispatch that starts its own planning sessions, and `cenci babysit`
merging the PR once CI is green and review feedback is clear. Fully armed, the chain
runs refine → plan → implement → PR → merge → next ticket with no human touch between
refinement and merge.

Every switch is off by default and independently reversible, the per-ticket merge
grant is always a human decision, and each link fails closed. Nothing here changes
until you turn it on.

[Read the autonomous loop guide →](docs/autonomous-loop.md)

## Claude Code, Codex, and OpenCode

| Client | Ready today |
|---|---|
| **Claude Code** | Full gated workflow, sandbox isolation, live monitoring, optional design, and PR babysitting |
| **Codex** | Sandbox isolation, live monitoring, portable conventions, and PR babysitting; native gated workflow in development |
| **OpenCode** | Sandbox isolation, live monitoring, and portable engineering conventions via `--agent opencode`; opt-in during install and requires an existing Claude Code or Codex install; no native gated workflow, usage-budget tracking, or PR babysitting yet |

All three clients can share one install, one repository, and one board. `cenci run`
chooses an agent per invocation, while `cenci dispatch` can route tickets by
`agent:<name>` label.

[See current delivery status →](docs/roadmap.md) ·
[Read the Codex implementation guide →](flow/docs/codex.md) ·
[Read the OpenCode implementation guide →](flow/docs/opencode.md)

## Learn more

| I want to… | Read… |
|---|---|
| Install, update, or troubleshoot | [Getting started](docs/getting-started.md) |
| Understand the gated engineering workflow | [Workflow guide](flow/README.md) |
| Use tmux, status surfaces, dispatch, or the CLI | [Attention and CLI reference](watch/README.md) |
| Configure and maintain the container boundary | [Isolation guide](sandbox/README.md) and [security model](SECURITY.md) |
| Drive cenci from a GitHub board | [Orchestration recipe](docs/orchestration.md) |
| Let it plan, implement, and merge unattended | [The autonomous loop](docs/autonomous-loop.md) |
| Use isolation and attention without the workflow layer | [Selective adoption](docs/selective-adoption.md) |
| Check what is available or in development | [Roadmap](docs/roadmap.md) |
| Contribute | [Contributing guide](CONTRIBUTING.md) |
| Upgrade from agent-stack | [Migration guide](docs/migrating-to-cenci.md) |
| Manually verify an OpenCode end-to-end run | [OpenCode smoke matrix](docs/opencode-smoke-matrix.md) |

## License

MIT
