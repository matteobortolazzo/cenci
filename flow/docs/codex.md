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

The staged procedures are under active development. Their intended contract is: planning workflows plan only in Plan mode, gather material choices, and produce an approved plan
without mutations. A persist step then writes the plan artifact and its checkpoint, and a
fresh `apply` verifies both before it updates GitHub and executes; see
[Plan, persist and apply](#plan-persist-and-apply). Implementation uses `.cenci/checkpoints/`,
generated `.codex/agents/*.toml`, and a native goal created only by a human-issued `apply` (see [Native goals](#native-goals)).
Missing custom agents fall back to built-in workers with the same bounded role prompt.

Refinement follows the same handoff. Plan mode asks the material questions, validates the
proposal and renders it; `apply` re-fetches the ticket, runs the pre-write checks and
writes. `refinement.confirmBeforeApply: true` restores final review in `apply`, and false
or absent does not ask again there. Explicit `/plan` remains read-only. Automerge grants
are separately controlled by `refinement.automerge: "off" | "eligible"`, regardless of
planning autonomy.

In ticket mode, Codex implement records `cenci pipeline` stages and resumes a crashed `apply` from its recorded stage, the same table the Claude Code procedure follows. Beyond holding Codex's local goal and phase record, the checkpoint selects first-apply vs saved-plan apply only; the pipeline stage still picks the resume route.

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
  choosing, mutating, pushing or stopping. The attempt count comes from
  `cenci babysit status <pr>` (`ciFixAttempts` or `conflictFixAttempts`, by launch reason),
  which works on the host and inside a sandbox; when that exits non-zero or its output is not
  JSON, the summary says "retry cap reached (exact count not readable from here)".
- The "stop babysitting" choice runs `cenci babysit stop <pr>`, which a sandbox forwards to
  the host daemon. When that exits non-zero (`not stopped: <reason>` from a host refusal, or
  `stop status unknown` when the host does not answer), the procedure says the supervisor
  was **not stopped** and prints the exact `cenci babysit stop <pr>` command to run on the
  host. It never reports a failed stop as success. The procedure's only other `cenci babysit`
  call is that `status` read.

The opt-in runner `codex/native-workflows.test.sh` (`CENCI_CODEX_NATIVE=1`) runs both
procedures through a real `codex exec` against a disposable fixture. It also covers the
Plan-mode self-check, the new-process plan to apply handoff and the goal lifecycle (create,
skip when a goal exists, complete on PR). Its sanitized results are recorded in
[`codex-native-evidence.md`](codex-native-evidence.md#native-workflows-ci-repair-and-babysit-attention)
and
[`codex-native-evidence.md`](codex-native-evidence.md#native-workflows-plan-mode-handoff-and-goal-lifecycle);
each section stays "pending native run" until a maintainer commits one.

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

## Plan, persist and apply

The planning workflows (`configure`, `refine`, `implement`, `address-review`, `refactor`, `maintain`) share one handoff, specified in [`skills/codex-runtime/SKILL.md`](../skills/codex-runtime/SKILL.md) under **Approved-plan handoff**. `cenci run` no longer adds `/plan` to the prompt: Codex has no config key or flag that starts a session in Plan mode, so the workflow checks the active collaboration mode itself and does nothing outside Plan mode. A plan moves from Plan mode to a new process in three turns, with `implement` and ticket 42 as the example:

```
cenci run implement 42 --agent codex
/plan $cenci:implement 42
$cenci:implement persist 42
cenci run implement apply 42 --agent codex
```

1. `cenci run implement 42 --agent codex` starts Codex in Default mode. The procedure refuses (see the self-check line below) and tells you to resend with `/plan`.
2. `/plan $cenci:implement 42` plans in Plan mode. It asks the material questions and makes no project or GitHub mutation.
3. After you approve the plan, `$cenci:implement persist 42`, sent in Default mode in the same session, writes only the plan artifact and the checkpoint (the artifact's path and sha256 digest) and prints the `apply` command. It makes no claim, label, worktree or GitHub write. Persist first checks that this session holds a Plan-mode-approved plan for the workflow and target, and checks for an existing checkpoint before it writes anything: a run in flight (checkpoint `status` `running` or `needs-input`) is refused with its resume `apply` command, plus a note that if that `apply` stops on `verify`, you can discard the run with `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" clear <ckpt> <wf> <target>` and re-plan (`<ckpt>` is `.cenci/checkpoints/<wf>-<target>.json`).
4. `cenci run implement apply 42 --agent codex`, or `$cenci:implement apply 42` in the same session, starts a fresh process. It runs `require-runtime.sh`, then `checkpoint.mjs verify`, and only then makes the workflow's mutations, resuming from the checkpoint's recorded gate: it never recreates a recorded worktree, re-applies a label or reopens a PR.

Each workflow uses its own target, artifact and `apply` command. Artifacts live under `.plans/` (gitignored, local to the checkout). Paths are stored repo-relative and resolved against the main checkout of the current environment, so host and sandbox behave the same.

The Plan-mode self-check applies to the planning request only. `persist` and `apply` run in Default mode and are exempt from it; they have their own checks (sent while Plan mode is still active they are refused with `leave Plan mode and resend`, and `apply` stops when `checkpoint.mjs verify` fails).

| Workflow | Target | Artifact | Apply command |
|---|---|---|---|
| `configure` | `repo` | `.plans/codex/configure-repo.md` | `cenci run configure apply repo --agent codex` |
| `refine` | ticket number | `.plans/codex/refine-<ticket>.md` | `cenci run refine apply <ticket> --agent codex` |
| `implement` | ticket id (task slug in ticketless mode) | `.plans/<ticket>-<slug>.md` | `cenci run implement apply <ticket> --agent codex` |
| `address-review` | PR number | `.plans/codex/address-review-<pr>.md` | `cenci run address-review apply <pr> --agent codex` |
| `refactor` | `repo` | `.plans/codex/refactor-repo.md` | `cenci run refactor apply repo --agent codex` |
| `maintain` | `<mode>` or `<mode>-<scope>` | `.plans/codex/maintain-<mode>[-<scope>].md` | `cenci run maintain apply <mode>[-<scope>] --agent codex` |

**Explicit errors.** Nothing falls back silently; each stop names its remedy.

- The Plan-mode self-check prints one line and makes no tool call, so nothing is written: `cenci: $cenci:<wf> plans only in Plan mode — type /plan and resend: /plan $cenci:<wf> <target>`. A `persist` or `apply` sent while Plan mode is still active is refused with `leave Plan mode and resend`.
- `persist` in a session with no Plan-mode-approved plan for that workflow and target (for example a fresh Default-mode session launched with `cenci run <wf> persist <target>`) makes no tool call and writes nothing: `cenci: no approved plan in this session — type /plan and resend: /plan $cenci:<wf> <target>`.
- Codex itself can refuse `/plan`: it reports "Collaboration modes are disabled" when the feature is off and "Plan mode unavailable" when Plan mode cannot be entered right now. cenci does not plan in Default mode as a fallback; enable collaboration modes or update Codex, then resend. The native evidence for these messages is tracked in [`codex-native-evidence.md`](codex-native-evidence.md#native-workflows-plan-mode-handoff-and-goal-lifecycle), pending the maintainer's native run.
- `require-runtime.sh` stops the persist and apply steps before any write when Node.js 18 or newer is missing: `cenci: Codex workflow runtime unavailable — Node.js 18+ not found on PATH (found: <none|unparseable|version>). Install Node.js 18 or newer, then rerun; no checkpoint, workflow, or GitHub state was changed.`
- `checkpoint.mjs verify <path> <workflow> <target>` is read-only and stops `apply` before any write with one stderr line of the form `checkpoint: <file>: <problem>; <remedy>`. The `<problem>; <remedy>` part is one of these (exit 3 for the first, exit 2 for the rest):
  - `no checkpoint found; no approved plan was persisted — plan in Plan mode, then persist`
  - `path does not match workflow and target; the file name must be <wf>-<target>.json`
  - `checkpoint records a different workflow or target than requested; it is never reset silently; clear cannot remove it either, so remove the file by hand if it is stale`
  - `legacy v1 checkpoint records no approved plan; plan in Plan mode, then persist`
  - `checkpoint is complete (terminal); plan in Plan mode, then persist a new plan`
  - `checkpoint records no approved plan; plan in Plan mode, then persist`
  - `plan artifact path is not under .plans/; use a repo-relative path such as .plans/<name>.md`
  - `plan artifact missing; plan in Plan mode, then persist`
  - `plan artifact unreadable; plan in Plan mode, then persist again to rewrite it`
  - `plan artifact changed since approval (digest mismatch); plan in Plan mode, then persist again`
- A malformed checkpoint, an unknown `schemaVersion` or an invalid field also exits 2, and a usage mistake (`verify requires <workflow> <target>`, `verify takes no <phase>`, `digest requires <plan-path>`, `too many arguments`) exits 2.

A Claude Code `.plans/<id>-<slug>.md` file with no Codex checkpoint is not an approved plan: Codex `apply` stops with zero writes and tells you to plan in Codex. Planning again over a checkpoint whose `status` is `planned` (approved, never applied) or `complete` (a finished run keeps its last real phase, such as `pr`) replaces it; a run in flight (`status` `running` or `needs-input`) is refused with its resume `apply` command, and clearing it takes an explicit, user-run `checkpoint.mjs clear` (see step 3). `apply` never clears a checkpoint: when it stops on `verify` because of a leftover or terminal checkpoint, run that `clear` yourself, then plan and persist again.

**Unattended launches.** Babysit's `address-review` and dispatch's `implement` launch Codex without a human in Plan mode, so they show the self-check line and end their turn instead of planning. That is intended: approving a plan always needs a person, and before this handoff those launches did not reliably plan either. Plan and persist in an attended session, then let `cenci run <workflow> apply <target> --agent codex` resume unattended.

## Native goals

Implement and maintain use Codex's `get_goal`, `create_goal` and `update_goal` through one **Goal lifecycle** contract in [`skills/codex-runtime/SKILL.md`](../skills/codex-runtime/SKILL.md); their `codex.md` companions reference its create, stop and complete steps and never restate them.

- **Create.** Only a human-issued `apply` creates a goal, after `verify` passes, with the objective `Complete approved plan <path> through an open PR` (`<path>` is the checkpoint's repo-relative `planPath`; no `token_budget` is set). A launch started by cenci automation cannot create one: no cenci automation (dispatch, babysit, ci-repair, babysit-attention) issues `apply`, so only a human-started `apply`, attended or not, creates a goal. A `cenci run <workflow> apply <target> --agent codex` that you start yourself counts as human-started even though it then runs unattended (see Unattended launches above).
- **Existing goal or no tools.** If `get_goal` reports a goal that is not `complete`, the run leaves it unchanged, creates none and continues on checkpoint state alone, except this run's own goal on a resumed `apply` (Create step 2). Without goal tools it uses checkpoint-only recovery. The outcome is recorded in the checkpoint's `goal` field: `created`, `skipped-active` or `unavailable`.
- **Stop.** A question, a red baseline, a native-tool error (a goal tool error included) or a budget or usage limit runs `checkpoint.mjs block` and leaves the goal as it is. The unfinished state stays in the checkpoint. cenci never invents a clear or pause operation; a pause or blocked status is used only through a transition the running client exposes, under that client's own conditions.
- **Complete.** A goal this run created is completed with `update_goal` only after a successful `gh pr create` (and, in maintain, a mergeability check confirmed not `CONFLICTING` (an unresolved `UNKNOWN` runs no complete step)), before babysit is armed. A failed `gh pr create` never completes it, and babysit through merge is never part of the objective.

The nine behavior cases are pinned by `codex/workflows.test.sh`. The goal tool calls themselves exist only in native Codex, so they are judged in the opt-in native run, whose record is tracked in [`codex-native-evidence.md`](codex-native-evidence.md#native-workflows-plan-mode-handoff-and-goal-lifecycle), pending the maintainer's native run.

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
- **Host launches check the installed plugin before spawning.** `cenci run … --agent codex`
  (and `cenci babysit <pr> --agent codex`, which launches through it) on the host runs
  `codex plugin list --json` first and refuses, before any tmux window is created, unless
  `cenci@cenci` is installed, enabled, and its installed plugin root under `CODEX_HOME`
  (default `~/.codex`) holds the workflow's declared adapter (`skills/<workflow>/codex.md`,
  or `skills/babysit/SKILL.md` for the portable `babysit`). The check fails closed: if it
  cannot tell, the launch is refused. The error names the plugin, the workflow and the
  remedy: `codex plugin add cenci@cenci`, set `plugins."cenci@cenci".enabled = true` in
  the Codex config, or launch in the sandbox. A babysit launch refused this way does not
  advance ci-repair attempts or mark feedback as launched, and never switches agents.
  `--dry-run` does not run the check. Sandbox launches skip it and rely on boot-time
  provisioning (`provision_codex_plugins` in `sandbox/lib/migrate-settings.sh`). Known
  limitation: the probe asks the Codex binary named by the first token of the configured
  host command, from the `cenci` process's working directory and environment; it does not
  apply `--profile`/`-c` flags, a project-scoped `.codex/config.toml`, or a different
  `CODEX_HOME`/`PATH` set only in the tmux server environment, so it can pass where the
  session's own Codex configuration would not.
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
