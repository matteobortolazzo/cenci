# OpenCode support

cenci ships its portable convention skills to [OpenCode](https://opencode.ai) as real
filesystem symlinks. OpenCode has no plugin marketplace or hook system today, so there is
no executable OpenCode primary agent or `/cenci:*` command in this ticket — AGENTS.md
prose is the delivery mechanism for project guidance and the pipeline description, exactly
as it is for Codex (see [`docs/codex.md`](codex.md)).

## Install

```bash
PLUGIN_ROOT=/path/to/flow sh opencode/install-skills.sh install
```

`PLUGIN_ROOT` must point at this `flow/` plugin root (the directory containing `skills/`).
The script links each portable skill from `$PLUGIN_ROOT/skills/<name>` into OpenCode's
global skills directory:

```
${XDG_CONFIG_HOME:-$HOME/.config}/opencode/skills/<name>
```

Each entry is a real symlink, not a copy — editing a skill in `flow/skills/` is
immediately visible to OpenCode. Re-running `install` is idempotent: existing correct
links are left alone, and anything already occupying a target path that isn't one of
this script's own links (a real directory, an unrelated symlink, a plain file) is left
untouched rather than clobbered.

## Update

Re-run the install command above after pulling a newer `flow/` — there's no separate
update step; `install` always converges the target directory to the current
`PORTABLE_SKILLS` list.

## Remove

```bash
PLUGIN_ROOT=/path/to/flow sh opencode/install-skills.sh remove
```

`remove` deletes only the symlinks this script would have created (matching both the
skill name and the `$PLUGIN_ROOT/skills/<name>` target) and leaves anything else at that
path untouched.

## OpenCode skill discovery paths

OpenCode discovers skills from both project-local and global directories. Project paths
are walked upward from the current file to the worktree root; global paths are searched
after that:

**Project (walked up to the worktree root):**
- `.opencode/skills/`
- `.claude/skills/`
- `.agents/skills/`

**Global:**
- `~/.config/opencode/skills/`
- `~/.claude/skills/`
- `~/.agents/skills/`

`install-skills.sh` populates `~/.config/opencode/skills/` (or
`$XDG_CONFIG_HOME/opencode/skills/` when that variable is set). A project that also
checks out cenci's plugin skills under one of the project-local paths above shadows the
global links for that project — project discovery wins over global, matching OpenCode's
own resolution order.

## Portable vs. excluded skills

`install-skills.sh` links exactly the 12 skills declared in its `PORTABLE_SKILLS`
variable — the same skills marked `Yes` in the OpenCode column of the root
[`README.md`](../README.md#skill-portability) skill-portability table:

`attachments`, `babysit`, `frontend-classification`, `pr-comment-filter`, `shell-rules`,
`stack-angular`, `stack-dotnet`, `stack-go`, `subagent-safety`, `testing`, `verify-ui`,
`worktrees`.

These are portable because each one avoids depending on a Claude Code-only tool or
approval mechanism.

Pipeline and interactive skills — `implement`, `configure`, `refine`, `design`,
`maintain`, `address-review`, `review`, `refactor`, `sync`, `codex-runtime`,
`project-core`, `ci-repair`, `ticket-ownership`, `babysit-attention` — are **not**
linked. They assume
Claude Code's interactive approval flow (`AskUserQuestion`, checkpoints, native
subagents) that OpenCode does not provide, so shipping them would let OpenCode
mistake a Claude-only pipeline command for a supported workflow.

## Host installer, update, and doctor

`install.sh` detects OpenCode on the host and offers this skill linking (plus
cenci-watch plugin registration) as an opt-in step — OpenCode integration is never a
standalone install; it layers on top of an existing Claude Code or Codex install. See:

- [Root README quickstart](../../README.md#quickstart) and
  [Getting started's OpenCode section](../../docs/getting-started.md#opencode-support-additional-opt-in-agent)
  for the opt-in prompt, what it links/registers, and what `cenci doctor`/`cenci
  uninstall` do with it.
- [cenci-watch's OpenCode adapter section](../../watch/README.md#dispatching-workflows-cenci-run)
  for the CLI reference (`--agent opencode`, no one-token shortcut yet), the pinned
  minimum OpenCode version `cenci doctor` enforces, and known limitations.

## Provider configuration

OpenCode itself resolves provider credentials — cenci only stages what's already on the
host, never a provider config of its own. Either of these two mechanisms satisfies both
`cenci doctor`'s "provider authenticated" check and `cenci open --agent opencode`'s
auth requirement:

- `~/.local/share/opencode/auth.json`, created by `opencode auth login` (Anthropic or
  OpenAI sign-in) — staged read-only into the sandbox exactly like Codex's `auth.json`.
- `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` in the host environment — forwarded into the
  sandbox per-exec, never baked into the container's create-time environment.

See [cenci-sandbox's OpenCode auth section](../../sandbox/README.md#opencode-auth) for
the full staging and error-handling details.

## Known limitations

- No native gated workflow (`/cenci:implement` and friends) — see "Portable vs.
  excluded skills" above. Use `cenci run implement <ticket> --agent opencode` (a
  `config.json` `opencode` agent entry is required — see
  [cenci-watch's configuration reference](../../watch/README.md)) to drive OpenCode
  from an AGENTS.md-guided run instead.
- No usage-budget/token-headroom tracking yet — no `TokenReader` is wired for OpenCode,
  so `cenci status`/`cenci dispatch` never show it headroom (see
  [cenci-watch's budget headroom section](../../watch/README.md#budget-headroom)).
- No one-token `cenci open`/`cn` shortcut yet — always pass `--agent opencode`.
- No universal cross-provider credential mechanism beyond the two above (e.g. no
  support for provider-specific SSO/enterprise credential brokers) and no OpenCode
  desktop or web UI — cenci only launches and monitors the OpenCode CLI inside the
  sandbox.

A maintainer-run, non-CI manual checklist for exercising a real ticket-to-PR run with
OpenCode against both Anthropic and OpenAI lives at
[`docs/opencode-smoke-matrix.md`](../../docs/opencode-smoke-matrix.md).
