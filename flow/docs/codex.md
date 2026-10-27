# Codex support

cenci installs native plugin skills, hooks, agent adapters, launch templates, and PR
babysitting for Codex CLI 0.144.1 or newer. The full gated ticket-to-PR workflow remains
in development until behavioral end-to-end acceptance passes. Shared configuration lives in
`.cenci/config.json`; shared guidance lives in `AGENTS.md`.

## Install

```bash
codex plugin marketplace add matteobortolazzo/cenci
codex plugin add cenci@cenci
```

Review changed plugin hooks with `/hooks`. cenci never changes hook trust automatically.

## Native workflow foundation

Explicitly mention `$cenci:configure`, `$cenci:refine`, `$cenci:implement`,
`$cenci:review`, `$cenci:address-review`, `$cenci:refactor`, `$cenci:sync`,
`$cenci:maintain`, or `$cenci:babysit`.

The staged procedures are under active development. Their intended contract is: planning workflows enter `/plan`, gather material choices, and produce an approved plan
without mutations. A second normal-mode invocation persists plans, updates GitHub, and
executes. Implementation uses `.cenci/checkpoints/`, generated `.codex/agents/*.toml`,
and a native goal armed only after plan approval. Missing custom agents fall back to
built-in workers with the same bounded role prompt.

Refinement can complete in one normal-mode invocation: it asks material questions,
validates the proposal and applies it without a second confirmation by default.
`refinement.confirmBeforeApply: true` restores final review in that invocation.
Explicit `/plan` remains read-only. Automerge grants are separately controlled by
`refinement.automerge: "off" | "eligible"`, regardless of planning autonomy.

In ticket mode, Codex implement records `cenci pipeline` stages and resumes a crashed `apply` from its recorded stage, the same table the Claude Code procedure follows. The checkpoint stays Codex's local goal and phase record and is never used for routing.

The babysit supervisor also dispatches two native procedures that are not meant to be
invoked by hand: `$cenci:ci-repair` (repair one PR's failing CI) and
`$cenci:babysit-attention` (summarize a paused PR and ask a human what to do). Both have a
`codex.md` companion, and neither ever enters `/plan`.

- `$cenci:ci-repair` works only in the PR branch worktree. It reproduces the failure
  locally, makes the smallest test-backed fix, passes the project's local gate
  (`docs/health-gates.md` at the repo root), commits explicit paths and pushes
  without force. It never runs `cenci pipeline`, changes a lifecycle label or opens a PR.
  Its checkpoint lives at `.cenci/checkpoints/ci-repair-<pr>.json` and a restart reads it
  rather than repeating a commit or push. A gate failure, or a repair that is ambiguous,
  infrastructure, flaky or external, pushes nothing and returns control to the supervisor.
- `$cenci:babysit-attention` prints the PR, head SHA, failing checks (or the merge-repair
  reason) and attempt count, then asks one blocking question with exactly two options:
  "leave paused for manual repair" and "stop babysitting". There is no retry option.
  A headless `codex exec` run prints the summary and the question and exits without
  choosing, mutating, pushing or stopping.
- The "stop babysitting" choice runs `cenci babysit stop <pr>`. When that exits non-zero, or
  the sandbox cannot reach the host supervisor's state, the procedure says the supervisor
  was **not stopped** and prints the exact `cenci babysit stop <pr>` command to run on the
  host. It never reports a failed stop as success.

The opt-in runner `codex/native-workflows.test.sh` (`CENCI_CODEX_NATIVE=1`) runs both
procedures through a real `codex exec` against a disposable fixture. Its sanitized results
are recorded in
[`codex-native-evidence.md`](codex-native-evidence.md#native-workflows-ci-repair-and-babysit-attention);
that section stays "pending native run" until a maintainer commits one.

The launcher exposes development entry points:

```bash
cenci run refine 42 --agent codex
cenci run implement 42 --agent codex
cenci babysit 123 --agent codex --interval 15m
```

**Authoring for Codex.** A skill reaches Codex through a `skills/<name>/codex.md`
companion — a separate procedure for a client with different primitives, not a
translation of `SKILL.md`. See
[`skill-authoring.md`](skill-authoring.md#client-surfaces) for the three client surfaces
a skill change must reconcile, and [`adapter-contract.md`](adapter-contract.md) for the
narrower eight-property behavioral-parity contract the implement pipeline's Claude Code
and Codex adapters both have to satisfy.

## Workflow runtime

Installing the cenci plugin and being able to run its workflows are two separate facts.
`codex plugin add cenci@cenci` installs skills, hooks, and agent templates, but the
workflows also run Node helpers (`codex/checkpoint.mjs`), so **Node.js 18 or newer must
be on `PATH`** in the environment where Codex runs. A plugin can be installed while its
workflows cannot run.

- **First normal-mode step.** Every normal-mode workflow invocation begins with
  `sh "${PLUGIN_ROOT}/codex/require-runtime.sh"`, before any checkpoint, workflow, or
  GitHub mutation. When Node is missing, older than 18, or reports an unparseable
  version, it prints one `cenci: Codex workflow runtime unavailable — Node.js 18+ not found on PATH (found: …)`
  line on stderr and exits 2. The run stops there and reports that line. Nothing has
  been written: no checkpoint, no workflow state, no GitHub change. With Node 18 or
  newer it is silent and exits 0.
- **`cenci doctor` reports the runtime on its own line**, next to the Codex version line.
  `✓ Codex workflow runtime: Node <version> on PATH` means the workflows can run;
  `! Codex workflow runtime not executable: Node.js 18+ not found on PATH (found: <none|unparseable|version>)`
  means the plugin may be installed while the workflows cannot run. Doctor parses
  `node --version` with the same grammar as `require-runtime.sh`, so the two cannot
  disagree. It is a warning, not
  a doctor failure, because Codex is an optional client. The 18 threshold appears in both
  `require-runtime.sh` and `install.sh`, and `flow/codex/require-runtime.test.sh` keeps
  them equal.
- **Sandbox images ship Node.** The monolith `sandbox/Dockerfile` installs Node
  (`NODE_MAJOR`), and generated per-repo images always include the Node fragment (see
  `sandbox/AGENTS.md`), so inside `cn` / `cenci open` the prerequisite is already met.
  Only host runs need a separate install.
- **Checkpoint contract.** The schema-v2 checkpoint (fields, phase tables, transitions,
  `init` retry-safety, legacy v1 handling, `get` and `clear`, exit codes 0/1/2/3, and the
  `.cenci/checkpoints` containment rules) is specified in
  [`skills/codex-runtime/SKILL.md`](../skills/codex-runtime/SKILL.md).

## Agent role templates

`domain-specialist` is the escalation role for qualifying tasks, pinned to `gpt-6-astra`
with high reasoning effort. The [shared policy](model-escalation.md) defines the
triggers, bounded read-only dispatch, evidence validation, and unavailable-model
fallback. Normal planner, implementer, reviewer, and context roles keep their
existing pins. `acceptance-reviewer` — the item-by-item ticket-coverage review that runs
after implementation and before `critical-reviewer`, in `$cenci:review` PR mode, and for
`$cenci:close` — ships on the same pin as `planner`. Re-running configure installs the new missing role; existing role
customizations are preserved. The repository's installed specialist mirrors its
template. These pins are configuration, not a guarantee of account-level access.

Role TOMLs live at `templates/codex/agent-roles/`, not `templates/codex/agents/`
(#1040). An unscoped directory literally named `agents` sitting inside this Codex
plugin's own root (`flow/`) is suspected of being auto-discovered by Codex's plugin
loader as a second source of the same role names, which would explain the
"duplicate agent role name ... declared in the same config layer" warning users saw
alongside "must define a description" on every startup — the flow plugin's own
source is the one place that carries both the installed copies (`.codex/agents/`)
and the template source side by side. This couldn't be confirmed against a live
Codex binary; the rename removes the collision regardless of the exact mechanism.

`install-agents.sh` never overwrites an existing `.codex/agents/*.toml` (they're
user-editable), so a repo configured before a template fix lands stays on the
broken shape indefinitely. `hooks/scripts/check-agent-role-drift.sh` is a
SessionStart advisory (mirroring `check-config-staleness.sh`) that flags this: it
runs `codex/validate-agent-roles.sh --plain` against the installed
`.codex/agents/`, and surfaces a report-only nudge when validation fails. It never
rewrites files. The same validator backs `/cenci:maintain`'s `agent-roles` check.

## Hook launch failures

Codex runs every hook as `$SHELL -lc "<command>"` with `${PLUGIN_ROOT}` substituted into the
command text. When a guard or handler cannot start, the exit code and stderr tell you why.
`codex/hooks-launch.test.sh` runs every handler in `codex/hooks.json` through each launch
failure below and fails if this table lacks a row for any exit code the fixtures observe.

"What Codex does" is derived from the codex-rs source (exit 2 with non-empty stderr is
reported as Blocked; any other non-zero exit is reported as Failed and the tool call is not
blocked). It has not been confirmed against a native Codex binary; that confirmation belongs
to #1212. The login-shell raw-form code for fish is recorded by the suite when fish is
installed; no fish row is listed because it has not been observed.

| Exit code | Stderr signature | Cause | Classification | What Codex does | Fix |
|---|---|---|---|---|---|
| 0 | none | The handler ran and allowed the call, or a lifecycle handler returned its payload. | allowed | Continues; lifecycle stdout is used as the hook output. | None needed. |
| 0 | `hook-output.sh: jq not found on PATH; emitting no additionalContext.` | `jq` is missing from the login-shell PATH; `hook-output.sh` degrades to an empty payload. | allowed (degraded: no additionalContext) | Continues, but the SessionStart, PreCompact, or Stop context is not injected. | Install `jq` where a login shell finds it (`~/.profile`, not only `~/.bashrc`). |
| 2 | Policy marker such as `Refusing to write` or `targets the main worktree` | A guard ran and refused the call by policy. | denied | Blocked: the tool call does not run and stderr is shown as the reason. | Make the change from a worktree or a non-sensitive path; this is the guard working. |
| 2 | `BLOCKED: jq is required ...` or `... requires awk ...` | A required tool is missing from the login-shell PATH; the guard fails closed. | denied | Blocked: every tool call the guard matches is refused until the tool is available. | Install `jq` and `awk` where a login shell finds them. |
| 2 | empty | `${PLUGIN_ROOT}` was substituted into the path but not exported, so `hook-output.sh` aborts without a reason. | handler not running | Failed (exit 2 without stderr is not Blocked); the tool call is not blocked. | Run the hook from a plugin-bundled source; config-layer hook files do not get `PLUGIN_ROOT` exported. |
| 126 | `Permission denied` naming the full script path | The script lost its execute bit (a lossy copy, archive, or checkout). | guard not running for PreToolUse guards; handler not running otherwise | Failed; the tool call is not blocked, so a guard that should refuse does not. | Restore the bit with `chmod +x` or reinstall the plugin. |
| 127 | `No such file or directory` for a path starting `/hooks/` or `/codex/` | `PLUGIN_ROOT` is unset or empty and the command was not substituted, or hooks were copied into a config-layer file. | guard not running for PreToolUse guards; handler not running otherwise | Failed; the tool call is not blocked. | Use the plugin-bundled hooks only; never copy `codex/hooks.json` entries into `config.toml` or `hooks.json` of a repo. |
| 127 | `No such file or directory` naming the full plugin path | The script is missing: a broken or partial plugin install. | guard not running for PreToolUse guards; handler not running otherwise | Failed; the tool call is not blocked. | Reinstall with `codex plugin add cenci@cenci`. |
| 127 | `/usr/bin/env: 'bash': No such file or directory` | `bash` is missing from the login-shell PATH; only the bash-shebang handlers (`check-pending-plans.sh`, `maintenance-reminder.sh`) are affected. The `#!/bin/sh` guards and `hook-output.sh` still run. | handler not running | Failed; the tool call is not blocked and the SessionStart reminders are skipped. | Put `bash` on the PATH a login shell sees. |

Review changed hooks with `/hooks`, and rerun `bash flow/codex/hooks-launch.test.sh` after
changing `codex/hooks.json` or any script it names.

The opt-in runner `codex/native-hooks.test.sh` (`CENCI_CODEX_NATIVE=1`) runs a real `codex exec`
against these hooks. Its sanitized results are recorded in
[`codex-native-evidence.md`](codex-native-evidence.md).

## Attention behavior

Cenci `need-input` renders a red/dim foreground `!`. A red tmux background without `!`
is usually tmux's native bell alert from a BEL notification. Run `cenci doctor` to inspect
Codex notification configuration; choose `osc9` yourself if BEL overlays are unwanted.
