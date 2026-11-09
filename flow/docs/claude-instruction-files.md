# Claude Code instruction files

How Claude Code decides between `CLAUDE.md` and `AGENTS.md`, what that means for a repository
that keeps its guidance only in `AGENTS.md` (the setup `/cenci:configure` generates since #1448),
and the sanitized runtime evidence behind each claim. `/cenci:configure` reads the floor line
below at run time and prints it in its final report; `flow/skills/maintain/scripts/check.sh`
points at this doc from its `instruction-docs` result.

Every statement is tagged by how it is known. **Observed** means a real Claude Code session
showed it (see "Evidence" below). **Documented, not observed** means it comes from Claude Code's
own documentation or from reading the product's behavior, and no run recorded here exercised it.
Nothing in the second group is presented as tested.

## Version floor

Claude Code >= 2.1.319

Earlier builds are untested and unsupported. 2.1.319 is the floor because it is the only build
the evidence below was recorded on. A lower floor would need the same runs repeated on older
builds, and that bisect was declined (see "Floor decision"), so the floor is not a measured
minimum. It is simply the lowest build that has been tested.

## Default-mode suppression

Under the default mode, Claude Code reads `AGENTS.md` natively only when the session has no
`CLAUDE.md`, `.claude/CLAUDE.md` or `CLAUDE.local.md` in the working directory or in any
directory above it. If one of those exists, Claude Code reads the `CLAUDE.md` family instead and
skips native `AGENTS.md` loading. Such a file is a suppressor even when it holds nothing but
`@AGENTS.md`, because the choice is made by the file's existence, not its content.

- **Observed (2.1.319):** a `CLAUDE.md` in the repository's parent directory suppressed both the
  root `AGENTS.md` and the project `AGENTS.md` (scenario c). A `CLAUDE.local.md` at the
  repository root did the same, while `CLAUDE.local.md` itself loaded (scenario d).
- **Documented, not observed:** `.claude/CLAUDE.md` suppresses in the same way. The run recorded
  here never created one.
- **Documented, not observed:** a home directory counts only when it is an ancestor of the
  working directory, so `~/CLAUDE.md` and `~/CLAUDE.local.md` suppress when the repository lives
  under the home directory. The user memory file `~/.claude/CLAUDE.md` does not suppress. No run
  here wrote into the real home directory, so these home-directory cases are untested. Only the
  parent-directory case (c) stands in for them.
- A suppressed session sees no `AGENTS.md` at all: neither the root file nor a project file that
  Claude Code would otherwise load lazily when it touches that subtree.

Because the walk climbs out of the repository, a stray file outside the repository can switch
native `AGENTS.md` loading off for every repository below it. `check.sh` reports such a file as
an `instruction-docs` warn with a `../CLAUDE.md` style relative path.

### Worktree sessions (transitional case)

cenci does its work in `.worktrees/<slug>/`, which is inside the main checkout. A session started
there has the main checkout, and anything above it, as ancestors. While the main checkout still
holds a root `CLAUDE.md` (for example a generated adapter that has not been migrated yet), that
file is an ancestor of every worktree session and suppresses the worktree's `AGENTS.md` under the
default mode, even though the worktree's own tree has none. This is the same walk as scenario
(c), applied to a worktree path; no separate worktree run was recorded (documented, not
observed). The case disappears once the main checkout's `CLAUDE.md` files are gone. Until then, use
the user-scope remedy below or the opt-in fallback. This repository no longer carries a root or
project `CLAUDE.md` (removed in #1449), so its worktree sessions report no ancestor warn.

## User-scope remedy

Setting `instructionFiles` to `claude-md-and-agents-md` makes Claude Code read both families
together, so a suppressor no longer hides `AGENTS.md`:

```json
{
  "pluginConfigs": {
    "cc-plugin-agents-md@builtin": {
      "options": {
        "instructionFiles": "claude-md-and-agents-md"
      }
    }
  }
}
```

The setting is honored from these places only:

- the user settings file, `~/.claude/settings.json`;
- a settings file passed with `--settings <file>`;
- managed settings.

**Project `.claude/settings.json` is not read for this setting**, and neither is
`.claude/settings.local.json`. A committed repository file cannot enable it for the team; each
user (or the organization, through managed settings) has to set it. In the cenci sandbox,
`~/.claude` is the container's home volume, so a user-scope edit there lasts for that volume only.

**cenci never writes this setting.** `/cenci:configure`, `migrate-project-core.sh` and `check.sh`
read and write no user-scope, `--settings` or managed settings for `instructionFiles` or
`pluginConfigs`. The configure final report names the remedy so the user can apply it.

- **Observed (2.1.319):** with the setting passed through a temporary `--settings` file, both the
  root and the project canary loaded in the scenario (c) and scenario (d) repositories
  (scenario e). The real `~/.claude/settings.json` was never edited.
- **Not tested:** the legacy plugin ID `agents-md@builtin`. Only `cc-plugin-agents-md@builtin`
  was exercised. The doc does not tell anyone to use the legacy ID.

## Cases where `AGENTS.md` does not load natively

All four cases below are **documented, not observed**. No run recorded in this doc exercised
them.

- **Built-in plugin disabled.** If the built-in plugin that reads `AGENTS.md` was disabled with
  `/plugin`, Claude Code reads the `CLAUDE.md` family only, whatever the files say. The
  `Project instructions` choice is then not offered in `/config`.
- **`--bare`.** The bare mode starts without auto-discovering customizations. Treat it as not
  loading either instruction family and pass the guidance explicitly.
- **`--safe-mode`.** Starts with every customization disabled, including `CLAUDE.md`, so no
  instruction file is expected to load.
- **`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1`.** Prevents loading any `CLAUDE.md` memory file. Whether
  it also switches off native `AGENTS.md` loading has not been tested here.

In each case the opt-in fallback below is the only route that puts `AGENTS.md` into the session,
and only where `CLAUDE.md` loading itself still works.

## Opt-in fallback adapter

AGENTS.md-only is the default. A repository that must keep loading guidance on Claude Code builds
below the floor, or that cannot rely on the user-scope remedy, can opt in to the fallback:

1. Copy `flow/templates/claude-md-adapter.md` to `CLAUDE.md` at the repository root.
2. Copy it to `CLAUDE.md` in **every** configured project (each `.projects[].path`).

The template starts with the marker line `<!-- cenci:agents-md-fallback -->`, followed by
`@AGENTS.md` and an optional Claude-only block. The marker is how cenci recognizes a deliberate
fallback: configure and `migrate-project-core.sh --apply` never touch a marked file, and `check.sh`
accepts a fully covered set.

Both levels are needed. A root `CLAUDE.md` is a suppressor, so under the default mode a project
that has no `CLAUDE.md` of its own gets no `AGENTS.md` either. `check.sh` warns when the root has a
`CLAUDE.md` and a configured project has none. The fallback does not remove the suppression trap
for ancestors outside the repository. It only routes the repository's own guidance through the
`CLAUDE.md` family that Claude Code then reads.

## What `check.sh` verifies

The `instruction-docs` check in `flow/skills/maintain/scripts/check.sh` verifies that the
canonical files are present:

- **Fails** when the root guidance file (`guidanceLocation`, default `AGENTS.md`) or a configured
  project's `AGENTS.md` is missing, when `guidanceLocation` is an unsafe path (absolute, with a `.`
  or `..` segment, or resolving outside the repository), or when the root guidance file is a symlink
  that resolves outside the repository.
- **Warns** (never fails) on a `CLAUDE.local.md` at the root or in a project, on a `CLAUDE.md` or
  `CLAUDE.local.md` in an ancestor directory, on a `CLAUDE.md` or `.claude/CLAUDE.md` without its
  import line, and on a project left uncovered by a root `CLAUDE.md`. A repository whose adapters
  are all inert `@AGENTS.md` files does not warn.

**It does not verify runtime loading.** A passing `instruction-docs` result means the canonical
files exist. It says nothing about whether a given Claude Code build, plugin state or settings
combination loads them, and its pass message says so. The runtime facts are the ones recorded in
this doc and nowhere else.

This doc makes no claim about token savings or context size from any of these setups.

## Evidence

### How to produce a run

Runs use an authenticated Claude Code CLI against a throwaway repository, never this monorepo
and never a real repository. Record the build with `claude --version`.

1. Create a throwaway repository under `${TMPDIR:-/tmp}/cenci/<scope>/` with a root `AGENTS.md`
   and `apps/api/AGENTS.md`. Put one random throwaway token in each file, and a non-guidance
   file, `apps/api/notes.txt`, to touch the subtree.
2. Root-level loading: run `claude -p "<ask for the token from your project instructions>"
   --tools ""`. With no tools at all, the answer can only come from instructions loaded at
   start-up.
3. Project-level loading: run `claude -p` with `--tools "Read"` and tell the session to read only
   `apps/api/notes.txt`. Check the stream-json log shows that single Read and never a guidance
   file; the project token then proves the subtree's `AGENTS.md` loaded.
4. Suppression: add a `CLAUDE.md` (no token in it) to a throwaway parent directory of the
   repository for (c), or a `CLAUDE.local.md` at the repository root for (d), and repeat 2 and 3.
5. Remedy: repeat (c) and (d) with `--settings <temp file>` holding the JSON from "User-scope
   remedy". Never edit `~/.claude/settings.json` for a run.

**Method finding.** Do not block direct reads with `Read(**/CLAUDE.md)` or `Read(**/AGENTS.md)`
deny rules. Those rules also suppress memory loading: a control run with the literal phrase in
`CLAUDE.md` returned NONE under the deny rules and loaded correctly under `--tools ""`. The first
attempt at scenario (a) under deny rules returned NONE and was discarded as a harness artifact,
not a result. Use `--tools ""` and `--tools "Read"`.

Paths in any committed record are relative or `<placeholder>` paths. The random canary tokens
are omitted. No home paths, tokens or credentials belong in this doc.

### Results (Claude Code 2.1.319, 2026-11-07)

| Scenario | Setup | Root canary | Project canary |
|---|---|---|---|
| (a) | root and project `AGENTS.md`, no `CLAUDE.md` | loaded | n/a (see b) |
| (b) | same as (a), session reads `apps/api/notes.txt` | loaded | loaded after the subtree read |
| (c) | `CLAUDE.md` (no canary) in the repository's parent directory | not loaded | not loaded |
| (d) | root `CLAUDE.local.md` (no canary) | not loaded | not loaded (`CLAUDE.local.md` itself loaded) |
| (e) on (c) | (c) plus the remedy through `--settings` | loaded | loaded |
| (e) on (d) | (d) plus the remedy through `--settings` | loaded | loaded |

### Floor decision

The floor is `Claude Code >= 2.1.319`, decided on 2026-11-08. It is the only build tested.
Testing older builds was approved in principle, but it needs the OAuth credentials in
`~/.claude/.credentials.json`, and an old build could rewrite `~/.claude.json`, migrate state or
rotate the token. When asked how to proceed, the maintainer chose to skip the bisect. A wider
floor is a possible follow-up that needs those old-build runs.

### Not tested

- The home-directory ancestor cases (`~/CLAUDE.md` and `~/CLAUDE.local.md` as ancestors, against
  `~/.claude/CLAUDE.md`): they would need writes into the real home directory, which no run
  here does.
- The legacy plugin ID `agents-md@builtin`.
- `.claude/CLAUDE.md` as a suppressor, a worktree-path session, and the four cases under "Cases
  where `AGENTS.md` does not load natively".

### Configure runs

User-attended, 2026-11-08, with this branch's `flow/` loaded through `claude --plugin-dir`, on
Claude Code 2.1.319, in a throwaway two-project repository (a Go `apps/api` and a Node
`apps/web`) with a local bare origin.

- **Run 1, fresh: done.** The configure branch held `AGENTS.md` at the root, in `apps/api` and in
  `apps/web`, and no `CLAUDE.md` anywhere (none existed before). This was checked with
  `git ls-tree -r --name-only` on the branch, which was then fast-forwarded into the throwaway
  repository's `main`.
- **Compatibility notice: omitted in run 1.** The notice reads its `Claude Code >= <version>` line
  from this doc, which did not exist yet. Configure reported that it omitted the notice, with
  the reason that the doc could not be read, instead of guessing a version. That matches the
  specified behavior: the rest of the final report still prints, and the notice is reported as
  omitted, with the reason. The notice text has therefore **not been observed in a real run**.
- **Run 2, rerun with an empty `git status --porcelain` for the guidance files: not run.** The
  maintainer skipped it for time, so no idempotence claim is made from a real configure re-run.
  Script-level idempotence is covered only by case M8 of
  `flow/scripts/migrate-project-core.test.sh`, which is a different mechanism from configure's
  own procedure.
