# cenci

**Let coding agents run longer—without giving up control.**

[![Claude Code](https://img.shields.io/badge/Claude_Code-supported-d97757?style=flat-square)](#claude-code-and-codex)
[![Codex](https://img.shields.io/badge/Codex-supported-10a37f?style=flat-square)](flow/docs/codex.md)
[![Platforms](https://img.shields.io/badge/Linux_%C2%B7_macOS_%C2%B7_WSL2-supported-64748b?style=flat-square)](docs/getting-started.md)
[![License](https://img.shields.io/badge/license-MIT-8b5cf6?style=flat-square)](LICENSE)

![cenci combines isolation, workflow, and attention into a safe path from issue to reviewed pull request](docs/assets/cenci-overview.svg)

Coding agents are useful when they can keep working. They are trustworthy when the
security boundary, approval points, and waiting states are explicit.

cenci provides those missing operating layers as one install:

- **Isolation** limits a full-permission session to a Docker or Podman container.
- **Workflow** turns an issue into a tested, reviewed pull request with human gates.
- **Attention** shows when a session is running, finished, idle, or waiting for you.

The human owns intent and consequential decisions. The agent owns the mechanical work
between them.

## From issue to reviewed PR

![cenci moves a ticket through human-gated refinement and planning, an autonomous engineering run, and PR follow-through](docs/assets/cenci-pipeline.svg)

| You stay responsible for | cenci handles |
|---|---|
| Scope, tradeoffs, and plan review | Worktrees, tests, implementation, and refactoring |
| Design decisions that change the product | Security review, code review, rebase, and PR creation |
| Genuine blockers and final judgment | Session status, follow-through, and optional dispatch |

The approved `.plans/` file is the durable handoff. Once you launch that plan, the
workflow can run unattended through an open PR. cenci-watch keeps its state visible;
[`/cenci:babysit`](flow/README.md#babysitting-a-pr) can follow CI and review
activity through to merge.

## Install

Requirements: Linux, macOS, or WSL2; git; curl; Docker or Podman; and Claude Code,
Codex, or both.

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
```

Then launch your agent inside the project boundary:

```bash
cn      # Claude Code (cenci open)
cn xt   # Codex
```

The installer detects available clients, registers the marketplace, installs all
three layers, and puts the `cenci` binary — with its `cn` launch alias — on your
PATH. The same command handles routine maintenance:

```bash
cenci doctor   # inspect prerequisites and installation state
cenci update   # update the complete stack
```

The command fetches the current official installer before it runs, so the updater
itself stays current. Existing installations from before the command was introduced
can bootstrap it once by rerunning the install command above. Once cenci is
installed, `cenci doctor` and `cenci update` reach the same two modes
(see [cenci-watch's README](watch/README.md#installer-integration-cenci-doctor-cenci-update)).

Follow the [guided getting-started path](docs/getting-started.md) for first-run
configuration and your first ticket.

## Three layers, one product

| Layer | What it changes | Learn more |
|---|---|---|
| **cenci-sandbox** | Runs Claude Code or Codex at full permissions while mounting only the current repository at `/workspace`. The container—not a prompt—is the security boundary. | [Isolation details](sandbox/README.md) |
| **cenci** | Adds refinement, optional UI design, persisted planning, test-first implementation, specialist reviews, and PR follow-through. | [Workflow details](flow/README.md) |
| **cenci-watch** | Turns native hooks into shared live state for tmux and optional Linux/macOS status surfaces. It can also dispatch approved plans by policy. | [Attention details](watch/README.md) |

Each layer is independently versioned internally, but normal installation and updates
treat cenci as one product.

## Claude Code and Codex

| Capability | Claude Code | Codex |
|---|---:|---:|
| Container isolation | Yes | Yes |
| Live session monitoring and self-bootstrap | Yes | Yes |
| Portable shell, testing, stack, worktree, and review conventions | Yes | Yes |
| Interactive refinement, design, implementation, and PR babysitting | Yes | Not yet |
| Documented implementation recipe | Built in | [Available](flow/docs/codex.md) |

Claude Code currently provides the full interactive ticket-to-PR workflow. Codex uses
the same isolation and attention layers plus portable engineering conventions and a
documented implementation recipe.

## Fits the tools you already use

cenci-watch can surface the same state in tmux, Waybar, Noctalia, DMS, GNOME, KDE
Plasma, and the macOS menu bar. An optional
[lazyboards](https://github.com/matteobortolazzo/lazyboards) board can dispatch the
documented workflow from issue labels; it is a separate project, not an installation
requirement.

The lifecycle stays inspectable either way:

```text
New → Refined → [Designed] → Planned → Working → In Review → Implemented
```

## Read next

- [Getting started](docs/getting-started.md) — install, verify, and run the first ticket
- [Security model](SECURITY.md) — what the container boundary protects and what it does not
- [Orchestration contract](docs/orchestration.md) — labels, plans, and dispatch behavior
- [Roadmap](docs/roadmap.md) — current delivery status
- [Contributing](CONTRIBUTING.md) — development workflow
- [Migrating from agent-stack](docs/migrating-to-cenci.md) — old→new names for anyone upgrading from a pre-rename install

## License

MIT
