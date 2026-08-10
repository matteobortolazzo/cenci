# Selective adoption

> Keep the isolation and attention layers, drop the opinionated pipeline.

This page is for repos that want cenci-sandbox (isolation) and cenci-watch (attention)
without cenci's gated ticket-to-PR pipeline (workflow) — for example, a repo with its own
review process that only wants a safe container to run agent sessions in and a status bar
that shows what those sessions are doing.

This is a supported configuration, not a reduced or provisional one: it states what works,
what you lose, and who it's for. It also doesn't change how cenci is installed. You still
install all three layers with one installer and update them with one command (see the
[cohesive package decision record](cohesive-package.md)) — what changes here is per-repo
plugin *enablement* inside Claude Code, not what gets installed on your machine.

## What each layer gives you

| Layer | Plugin | What it gives you | Depends on |
|---|---|---|---|
| Workflow | `cenci` | The gated pipeline: `/cenci:refine`, `/cenci:implement`, `/cenci:maintain`, and the other pipeline skills, agents, and hooks. This is the opinionated part, and the part this recipe turns off. | Isolation and attention, but not vice versa |
| Attention | `cenci-watch` | The `cenci` binary: the status daemon, tmux integration, fleet dispatch and `babysit`, and the native sandbox launcher — `cn`/`cenci sandbox` ship inside this binary. | Nothing from the workflow layer |
| Isolation | `cenci-sandbox` | The launcher's image, entrypoint, and container-side assets. Inert on its own — the attention layer's binary is what launches a container. | The attention layer's launcher to invoke it |

Two things worth knowing before you disable the workflow layer:

- **The sandbox launcher and its assets are two different plugins.** The `cn` launcher code
  ships inside the `cenci-watch` binary, but the Dockerfiles, entrypoint, and container-side
  scripts it needs ship with `cenci-sandbox`. If the assets aren't resolvable, `cn` fails
  with an error naming `CENCI_SANDBOX_ASSETS` as the fix — that error is about a missing or
  misconfigured **isolation**-layer install, not the workflow layer you're disabling here.
- **Fleet dispatch needs the workflow layer.** `cenci dispatch` spawns sessions that run
  `cenci run implement` — the workflow layer's implement skill. See
  [Do not enroll a flow-disabled repo in dispatch](#do-not-enroll-a-flow-disabled-repo-in-dispatch)
  below.

For the full CLI surface, tmux/status integration, and dispatch mechanics, see
[cenci-watch's README](../watch/README.md). For image architecture and container internals,
see [cenci-sandbox's README](../sandbox/README.md).

## Disable the workflow layer in one repository

Add a project-scope `.claude/settings.json` in the repository (committed, so it applies for
every contributor and every session in that repo):

```json
{
  "enabledPlugins": {
    "cenci@cenci": false
  }
}
```

`cenci@cenci` is the same `plugin@marketplace` key `sandbox/lib/migrate-settings.sh` already
manipulates when it provisions plugins on container boot.

**Precedence, scoped to `enabledPlugins` specifically.** Claude Code generally ignores
project-level settings files for plugin configuration, but `enabledPlugins` is an explicit
exception: it is honored from project settings. For that key, the order is managed settings
(highest) > local settings (`.claude/settings.local.json`) > project settings
(`.claude/settings.json`, the file above) > user settings (lowest). A committed
project-scope `false` overrides your personal user-scope `true` — which also means any repo
you clone or open can silently disable the `cenci` plugin, and its `check-sensitive-files.sh`
guard, for sessions in that repo: inspect an unfamiliar repo's `.claude/settings.json` before
starting a session in it. A personal `.claude/settings.local.json` can override the committed
project value if you need to re-enable it just for yourself.

**What stops loading:** `cenci`'s skills, agents, and hooks — `/cenci:implement`,
`/cenci:refine`, `/cenci:maintain`, and the rest of the pipeline surface become unavailable
in that repo's sessions.

**What keeps working:** the `cenci-watch` daemon, `cenci status`, `cn`/`cenci sandbox`
launches, and everything else that doesn't come from the `cenci` plugin. The sandbox
entrypoint still installs and updates the `cenci` plugin into the container's plugin cache
on every boot regardless of this setting — only whether that repo's sessions *load* it
changes, not whether it's provisioned.

**Verified inside a real `cn` session.** This precedence claim was checked empirically,
because `cn`'s entrypoint force-merges `"cenci@cenci": true` into the container's
user-scope `/home/dev/.claude/settings.json` on every boot — the repo-level `false` above
has to survive that force-enable, not just a plain multi-scope Claude Code install. Inside a
real `cn` session with the force-enable active (confirmed present:
`"enabledPlugins": {"cenci@cenci": true}` and `"permissions": {"defaultMode":
"bypassPermissions"}` in the container's user-scope settings), a project-scope `false`
resolved the plugin to disabled (`claude plugin list --json` reported `"enabled": false`
for `cenci@cenci`, while `cenci-watch@cenci` stayed `"enabled": true`), and `/cenci:implement`
was reported as an unknown command in that repo — while `cenci status` continued to report
the daemon and running sessions normally. A repo without the disable key showed
`cenci@cenci` enabled and `/cenci:implement` recognized, confirming the difference came from
the setting and not from anything else about the environment. The documented precedence held
as observed; no fallback behavior applies.

## Security: what you lose, and how to replace it

**Disabling the workflow layer also disables its secret-file guard.** `cenci`'s
`check-sensitive-files.sh` hook blocks writes to environment files, credential/secret/key
files, and SSH/keystore files, and it runs on both file-editing tool calls and Bash calls.
With the workflow layer disabled, that hook no longer runs in that repo's sessions.

This matters more inside `cn` than in an ordinary terminal session: the sandbox entrypoint
force-sets `permissions.defaultMode: bypassPermissions` in the container's user-scope
settings on every boot, so every session in `cn` already runs with no per-command approval
prompts. The secret-file guard was one of the few things still standing between a careless
or compromised session and files like `.env` — losing it without a replacement leaves that
gap open.

**Replace it with a project-scope `permissions.deny` block**, covering the same file
markers the hook blocks:

```json
{
  "permissions": {
    "deny": [
      "Edit(**/*.env)",
      "Edit(**/*.env.*)",
      "Edit(**/*credentials*)",
      "Edit(**/*secrets*)",
      "Edit(**/*secret.*)",
      "Edit(**/secrets/**)",
      "Edit(**/credentials/**)",
      "Edit(**/*.pem)",
      "Edit(**/*.key)",
      "Edit(**/*.pfx)",
      "Edit(**/*.p12)",
      "Edit(**/*id_rsa*)",
      "Edit(**/*id_ed25519*)",
      "Edit(**/*id_ecdsa*)",
      "Edit(**/*.keystore)",
      "Edit(**/*.jks)",
      "Edit(~/.ssh/**)",
      "Edit(~/.aws/**)",
      "Read(**/*.env)",
      "Read(**/*.env.*)",
      "Read(**/*credentials*)",
      "Read(**/*secrets*)",
      "Read(**/*secret.*)",
      "Read(**/secrets/**)",
      "Read(**/credentials/**)",
      "Read(**/*.pem)",
      "Read(**/*.key)",
      "Read(**/*.pfx)",
      "Read(**/*.p12)",
      "Read(**/*id_rsa*)",
      "Read(**/*id_ed25519*)",
      "Read(**/*id_ecdsa*)",
      "Read(**/*.keystore)",
      "Read(**/*.jks)",
      "Read(~/.ssh/**)",
      "Read(~/.aws/**)",
      "Read(~/.claude/.credentials.json)",
      "Read(//tmp/host-claude-creds/**)"
    ]
  }
}
```

This block lives in the same `.claude/settings.json` as the `enabledPlugins` key shown above;
if the repo already has a settings file — including this repo's own, which already has
roughly 200 lines of `permissions.deny` — merge these keys in rather than replacing the file:
`permissions.deny` is a list to append to, not replace.

Bare `**/`-prefixed patterns only cover the project tree (they resolve relative to the
settings file's directory), not the filesystem generally — unlike the hook they're
replacing, which matched an absolute canonicalized path anywhere on disk. Anything sensitive
outside the repo, such as SSH keys or cloud credentials under your home directory, needs its
own home-scoped entry, which is why the `~/.ssh/**` and `~/.aws/**` entries above are listed
separately from the `**/`-prefixed ones. Inside `cn`, the launcher also bind-mounts your host
Claude Code credentials read-only into every container (at
`/tmp/host-claude-creds/.credentials.json`) — the highest-value read target present in every
session — so the snippet above adds explicit entries for it and for the equivalent
`~/.claude/.credentials.json` path.

Deny rules still apply under `bypassPermissions` — only *allow* rules become inert there —
so this mitigation works inside `cn` too.

The write-blocking half of this snippet (the `Edit(...)` entries) was verified empirically in
the same `cn` pass; the `Read(...)` entries were not independently exercised in this session,
and Bash-mediated reads like `cat`/`grep` are a known gap regardless (see Gaps below). A few
things worth calling out about what was actually observed on the write side, because they
differ from what you might expect from a `Write`/`Edit`/`Read` split:

- Inside a `cn` session, the `Edit(...)` pattern above was sufficient on its own to block a
  `Write`-tool write to `.env`, both at the repository root and nested (e.g. `sub/.env`) —
  the running CLI reported that `Edit` deny rules currently cover all file-editing tools,
  including `Write` and `NotebookEdit`, and that adding separate `Write(...)`/
  `NotebookEdit(...)` deny entries is currently a no-op for permission checks (the CLI warns
  about this explicitly if you add one). The snippet above relies on `Edit` and `Read`
  for that reason. This was observed as of August 2026, with no specific Claude Code
  version recorded during the empirical pass; re-verify it after any Claude Code upgrade,
  since the CLI's internal deny-rule matching behavior could change.
- A negative control confirmed the block was caused by the deny rule and not by something
  else in the environment: the identical write succeeded in an otherwise-identical repo
  with no `permissions.deny` block.
- A Bash-mediated write using shell redirection (`printf ... > .env`) was also blocked by
  the same rule — Claude Code recognized the redirect as a file-write target and matched it
  against the `Edit` deny pattern.

This repository's own `.claude/settings.json` is a live example of the shortfall this
recipe closes: it denies `.env` reads and edits, but only at the repository root
(`Read(.env)`/`Edit(.env)`, no `**/` prefix), so a nested `.env` file elsewhere in the repo
is not covered. The patterns above use `**/` specifically to close that gap.

### Gaps this does not close

Deny rules cover `Write`/`Edit`/`Read`/`NotebookEdit` tool calls (via the `Edit`/`Read`
patterns above, per the CLI's current matching behavior) and Bash calls Claude Code
recognizes as file writes — shell redirection was verified blocked in this session's `cn`
pass; `tee` is documented to be recognized the same way but was not itself exercised here.
At least four gaps remain; only the first was empirically observed in this session's `cn`
pass, the other three are inferred from how permission patterns match and were not tested
here:

1. **Arbitrary subprocess writes (empirically verified).** Deny rules do not reach writes
   performed by an arbitrary subprocess the agent starts — a `python` or `node` one-liner,
   for example. In the same `cn` verification pass, a
   `python3 -c "open('.env', 'w').write(...)"` call succeeded in writing `.env` with the deny
   block above active, even though the equivalent `Write`-tool and Bash-redirect writes were
   both blocked.
2. **Non-redirect write verbs (not tested in this session, but follows from how permission
   patterns match).** Claude Code's Bash-write recognition covers redirection and `tee`, but
   not other command-line verbs that write files, such as `cp`, `mv`, `dd of=`, `sed -i`, or
   `truncate` — see [the adapter contract's `sensitive-file-refusal`
   row](../flow/docs/adapter-contract.md), which names these same verbs as gaps for the
   workflow layer's own tokenizer-based Bash hook.
3. **Symlinked targets (not tested in this session, but follows from how permission patterns
   match).** `check-sensitive-files.sh` canonicalized symlinks and matched the resolved path,
   but `permissions.deny` patterns match the literal path — so `ln -s .env notes.txt` followed
   by a write to `notes.txt` is not caught by the `.env` deny pattern.
4. **Bash-mediated reads (not tested in this session, but follows from how permission
   patterns match).** Commands like `cat .env` or `grep -r ... .` are not blocked by the
   `Read(...)` deny entries above. This matters more than the write-side gaps under `cn`'s
   forced `bypassPermissions`, since it's exfiltration rather than corruption (see the
   narrowed empirical-verification note above the deny snippet, which scopes what was
   actually tested to the write side).

None of these mechanisms is the actual security boundary — the container is. See
[`SECURITY.md`](../SECURITY.md) for the full threat model.

## Do not enroll a flow-disabled repo in dispatch

`cenci dispatch` picks up `Planned` tickets and runs `cenci run implement` against them —
the workflow layer's implement skill. A repo with the workflow layer disabled has nowhere
for that session to go: don't enroll it in dispatch. Use the attention layer's status and
tmux integration, and launch sessions with `cn` directly, instead of through dispatch.

## Codex

Codex's own configuration format (`.codex/config.toml`) has a `plugins` table keyed by
`plugin@marketplace`, with a per-plugin `enabled` flag — the same shape as Claude Code's
`enabledPlugins`, and, per Codex's `codex-rs` source (checked via Context7), project-level
`.codex/config.toml` files are allowed to set it (it is not one of the keys stripped from
project-local config). That indicates an equivalent per-project mechanism exists, but this
repo does not yet configure or document it: `sandbox/lib/codex-config.sh` has no plugin
handling today, and this environment has no Codex CLI to empirically verify it against, the
way the Claude Code recipe above was verified inside a real `cn` session. Until that
verification happens, treat the Codex per-project mechanism as unconfirmed for this repo. The
only Codex plugin surface this repo currently documents and provisions is the global one —
`codex plugin marketplace add` / `codex plugin add` — covered in
[the getting started guide's standalone installation section](getting-started.md#advanced-and-recovery-standalone-installation).

## Where the details live

| For… | Read… |
|---|---|
| The full CLI reference, tmux/status integration, dispatch mechanics | [cenci-watch README](../watch/README.md) |
| Image architecture, container internals, launcher assets | [cenci-sandbox README](../sandbox/README.md) |
| The container security boundary and full threat model | [Security](../SECURITY.md) |
