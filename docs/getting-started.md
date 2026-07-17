# Getting started

This is the detailed companion to the [README quickstart](../README.md#quickstart) —
use it for prerequisite detail, verification, troubleshooting, and
recovery/standalone installs. The steps below are the same install → verify →
launch → configure → run sequence, just with more depth at each step. cenci is
one product; the installer manages its three internal components for every
supported client it detects.

![cenci combines isolation, workflow, and attention into a safe path from issue to reviewed pull request](assets/cenci-overview.svg)

## 1. Prerequisites

Install:

- Linux, macOS, or WSL2
- git
- curl
- Docker or Podman
- Claude Code, Codex, or both

Claude Code is required for the complete interactive ticket-to-PR workflow. A
Codex-only installation still provides container isolation, monitoring, portable
engineering conventions, and the [Codex implementation recipe](../flow/docs/codex.md).

Optional features have separate dependencies:

| Feature | Dependency |
|---|---|
| GitHub issues and PRs | [GitHub CLI](https://cli.github.com) authenticated with `gh auth login` |
| tmux status | tmux |
| macOS menu-bar status | [SwiftBar](https://swiftbar.app) |
| desktop status | One of the documented cenci display widgets |

## 2. Install

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
```

From a clone, run `./install.sh`. Non-interactive automation can add `--yes`; use
`./install.sh --help` for the complete public interface. Every run reconciles the
cenci marketplace and the three components (`cenci`, `cenci-watch`, and
`cenci-sandbox`) for each detected client: it adds missing pieces and refreshes
existing ones. It then puts the `cenci` binary and its `cn` launch alias on your PATH.

## 3. Verify

`doctor` changes nothing and separates required platform dependencies, detected
clients, optional feature dependencies, installed components, launchers, and image
readiness:

```bash
cenci doctor
```

Warnings for optional features are safe to defer. Fix any required item marked with
`✗` before continuing.

## 4. Launch

```bash
cn           # Claude Code
cn xt        # Codex (or: cn --agent codex)
```

Run this from a git repository — only that repository root is mounted at
`/workspace`. cenci-watch needs no separate binary install: the first supported
client session provisions it and starts the shared host daemon.

Codex users should also make the repository's canonical `CLAUDE.md` instructions
discoverable by adding this user-level configuration (repository-level Codex config
does not control fallback instruction discovery):

```toml
# ~/.codex/config.toml
project_doc_fallback_filenames = ["CLAUDE.md"]
```

If Codex reports updated plugin hooks as pending, inspect and trust them with `/hooks`.

## 5. Configure a project

In a sandboxed Claude Code session, run once:

```text
/cenci:configure
```

This detects the stack, writes project guidance, configures workflow metadata, and can
generate a reviewed per-repository sandbox image definition. Codex-only users follow
the [portable project and implementation guidance](../flow/docs/codex.md); the
interactive configure skill is Claude Code-only. See
[flow/README.md's "What `/cenci:configure` creates"](../flow/README.md#what-cenciconfigure-creates)
for the generated file layout, including the monorepo progressive-disclosure structure.

## 6. Run a ticket

```text
/cenci:refine 42
/cenci:implement 42
/cenci:babysit <pr-number>
```

After implementation opens the PR, `babysit` checks CI and review feedback
immediately, then schedules progressively quieter checks until the PR merges or
closes. It can fix actionable failures and comments while preserving approval gates;
on merge it performs the final `In Review → Implemented` transition. The persistent
supervisor supports both Claude Code and Codex. See
[Babysitting a PR](../flow/README.md#babysitting-a-pr) for pacing and safety details.

The lifecycle is always:

![A ticket moves through human-gated refinement and planning, an autonomous engineering run, and PR follow-through](assets/cenci-pipeline.svg)

For UI work, refinement can branch through a dedicated design ticket. Planning saves
an approved `.plans/` file and applies `Planned`; implementation or automated dispatch
(`cenci dispatch`) picks it up and applies the transient `Working` state. PR creation
applies `In Review`, and merge completion applies `Implemented`.

## Update

```bash
cenci update
```

The command refreshes the cenci marketplace through the owning client, then reconciles
all three components in every detected client — missing pieces are added and existing
ones are refreshed. It resolves the active cenci-watch cache, refreshes launchers, and
replaces a stale running cenci daemon with the updated binary. Use
`cenci update --lazyboards` to install or reconcile the optional board binary; an
already-managed lazyboards installation is also refreshed by a normal update.

The components version independently. In particular, a `watch/v0.5.4` release is the
`cenci-watch` plugin release, so update output correctly reports it on the
`cenci-watch` line rather than the `cenci` workflow-plugin line.

## Troubleshooting

| Symptom | Resolution |
|---|---|
| Neither client is detected | Install Claude Code, Codex, or both, then rerun the installer |
| `cenci` or `cn` is not found | Add `~/.local/bin` to `PATH`, then rerun the install command |
| cenci status has not appeared | Start a new agent session and inspect `${TMPDIR:-/tmp}/cenci-bootstrap.log` |
| Codex skills are missing | Confirm `codex plugin list`, then restart Codex after installation |
| Claude commands are missing | Confirm `claude plugin list`, then restart Claude Code after installation |
| Sandbox image is absent | Run `cenci sandbox build` |
| GitHub operations fail | Install `gh` and run `gh auth login` |

Platform and display-specific troubleshooting lives in the internal layer references:
[cenci-sandbox](../sandbox/README.md) and [cenci-watch](../watch/README.md).

## Advanced and recovery: standalone installation

Use this only when developing a component or recovering a broken installer run. Run
the commands for each client you actually use:

```bash
claude plugin marketplace add matteobortolazzo/cenci
claude plugin install cenci@cenci
claude plugin install cenci-watch@cenci
claude plugin install cenci-sandbox@cenci

codex plugin marketplace add matteobortolazzo/cenci
codex plugin add cenci@cenci
codex plugin add cenci-watch@cenci
codex plugin add cenci-sandbox@cenci
```

Then rerun `./install.sh` to restore launchers, cenci-watch wiring, and image setup.
Optional desktop/menu-bar widgets are configured from the relevant
[cenci-watch display documentation](../watch/README.md); they are not another
cenci install.
