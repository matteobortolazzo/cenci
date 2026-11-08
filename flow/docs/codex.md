# Codex support

cenci installs native plugin skills, hooks, agent adapters, launch templates, and PR
babysitting for Codex CLI 0.166.0 or newer. The full gated ticket-to-PR workflow remains
in development until behavioral end-to-end acceptance passes. Shared configuration lives in
`.cenci/config.json`; shared guidance lives in `AGENTS.md`.

## Install

```bash
codex plugin marketplace add matteobortolazzo/cenci
codex plugin add cenci@cenci
```

Codex skips plugin hooks that are pending or not yet trusted, so a freshly installed or
updated plugin gives no hook protection until you review its hooks with `/hooks`. Pending
hooks are inactive protection: every mutating Codex workflow (`implement`, `address-review`,
`refactor`, `ci-repair`, `babysit-attention`, `refine`, `configure`, `maintain` and `sync`) and
`babysit`'s arm step detect that with a [guard canary](#guard-canary-preflight) and refuse to
start. cenci never changes hook trust automatically, and `cenci doctor` does not verify hook
protection.

## Native workflow foundation

Explicitly mention `$cenci:configure`, `$cenci:refine`, `$cenci:implement`,
`$cenci:review`, `$cenci:address-review`, `$cenci:refactor`, `$cenci:sync`,
`$cenci:maintain`, or `$cenci:babysit`.

The staged procedures are under active development. Their intended contract is: planning workflows plan only in Plan mode, gather material choices, and produce an approved plan
without mutations. A persist step then writes the plan artifact and its checkpoint, and a
fresh `apply` verifies both before it updates GitHub and executes; see
[Plan, persist and apply](#plan-persist-and-apply). Implementation uses `.cenci/checkpoints/`,
generated `.codex/agents/*.toml`, and a native goal created only by a human-issued `apply` (see [Native goals](#native-goals)).
A missing role file falls back to a built-in worker with the same bounded role prompt; an unavailable model or effort at spawn does not (see [Agent role templates](#agent-role-templates)).

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
  rather than repeating a commit or push. A restart decides in this order: the checkpoint
  existence check and `get`; the live PR state (`gh pr view`, `git ls-remote`, `gh pr checks`,
  then `gh pr view` again); every head read must agree, or the run is a no-op (a stale head,
  or a push already on the remote that CI has not caught up with); interrupted-push recovery
  on the agreed head; and only a `fail` bucket on the agreed head starts a fresh diagnosis
  through `checkpoint.mjs advance` to `diagnosed`. A head with no `fail` bucket is a no-op:
  green only when at least one check exists and every check is in `pass`, "still running" while
  checks are `pending`, "unknown" with zero checks, and a `cancel` or `skipping` check is not green
  and is named as itself; output that is not a JSON array is "could not read PR state". A `fail`
  bucket on the agreed head gets a fresh diagnosis rather than a no-op, whatever that head's
  ancestry relative to the recorded `pushed:<sha>`, and the recorded commit is never pushed again. A gate failure, or a repair that is
  ambiguous, infrastructure, flaky or external, pushes nothing and returns control to the
  supervisor.
- `$cenci:babysit-attention` prints the PR, head SHA, failing checks (or the merge-repair
  reason) and attempt count, then asks one blocking question with exactly three options:
  "leave paused for manual repair", "retry with a fresh budget" and "stop babysitting".
  A headless `codex exec` run prints the summary and the question and exits without
  choosing, mutating, pushing or stopping. The attempt count comes from
  `cenci babysit status <pr>` (`ciFixAttempts` or `conflictFixAttempts`, by launch reason),
  which works on the host and inside a sandbox; when that exits non-zero or its output is not
  JSON, the summary says "retry cap reached (exact count not readable from here)".
- The "stop babysitting" choice runs `cenci babysit stop <pr>`, which a sandbox forwards to
  the host daemon. When that exits non-zero (`not stopped: <reason>` from a host refusal, or
  `stop status unknown` when the host does not answer), the procedure says the supervisor
  was **not stopped** and prints the exact `cenci babysit stop <pr>` command to run on the
  host. It never reports a failed stop as success.
- The "retry with a fresh budget" choice runs `cenci babysit reset <pr>` once. The reset is
  host-only (it lifts the cap on automated repair launches), so it never travels through the
  event socket: exit 0 means the budget was reset and the supervisor re-armed. When it exits
  non-zero, including the refusal a sandbox gets because the supervisor runs on the host, the
  procedure says the budget was **not reset**, prints the exact `cenci babysit reset <pr>`
  command to run on the host and never reports success. It does not retry the command. The
  procedure's only other `cenci babysit` calls are the chosen stop and that `status` read.

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
3. After you approve the plan, `$cenci:implement persist 42`, sent in Default mode in the same session, writes only the plan artifact and the checkpoint (the artifact's path and sha256 digest) and prints the `apply` command. It makes no claim, label, worktree or GitHub write. Persist first checks that this session holds a Plan-mode-approved plan for the workflow and target, and checks for an existing checkpoint before it writes anything: a run in flight (checkpoint `status` `running` or `needs-input`) is refused with its resume `apply` command, plus a note that if that `apply` stops on `verify`, you can discard the run with `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" clear <ckpt> <wf> <target>` and re-plan (`<ckpt>` is `.cenci/checkpoints/<wf>-<target>.json`). Any other `status`, an unrecognised legacy v1 status included, is refused as in flight the same way. When `apply` reaches its successful end it marks a finished run `complete`, so planning the same target again is accepted. A `planned` or `complete` checkpoint is replaced by a later persist, but the replacing `clear` runs only after the new artifact is written and digested (and after a second check that the checkpoint is still `planned` or `complete`), so a plan that fails its artifact check leaves the existing checkpoint untouched.
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

A Claude Code `.plans/<id>-<slug>.md` file with no Codex checkpoint is not an approved plan: Codex `apply` stops with zero writes and tells you to plan in Codex. Planning again over a checkpoint whose `status` is `planned` (approved, never applied) or `complete` (a finished run keeps its last real phase, such as `pr`) replaces it, and `apply` marks a finished run `complete` so this works after a normal finish; the replacing `clear` comes only after the new artifact is written and digested. A run in flight (`status` `running` or `needs-input`), or any other `status` (an unrecognised legacy v1 status included), is refused as in flight with its resume `apply` command, and clearing it takes an explicit, user-run `checkpoint.mjs clear` (see step 3). `apply` never clears a checkpoint: when it stops on `verify` because of a leftover or terminal checkpoint, run that `clear` yourself, then plan and persist again.

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
- **Agent-role validation needs Python 3.11+.** `codex/validate-agent-roles.sh` parses role
  files with Python's `tomllib`, so **Python 3.11 or newer must be on `PATH`** where it runs
  (configure, `check-agent-role-drift.sh` and maintain's `agent-roles` check all reach it).
  When `python3` is missing, older than 3.11, or has no `tomllib`, it prints one
  `cenci: Codex agent-role validation unavailable — Python 3.11+ with tomllib not found on PATH (found: …)`
  line on stderr and exits 3, separate from an invalid role (exit 1). `found:` is `none`, a
  validated `X.Y.Z` without `tomllib`, `no tomllib`, or `unusable` when `python3` cannot run the
  check. Nothing is written. See [Validating role files](#validating-role-files).
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

**Inherit by default.** The six normal roles (`planner`, `implementer`, `critical-reviewer`,
`acceptance-reviewer`, `context-gatherer`, `structure-analyzer`) ship with no `model` and no
`model_reasoning_effort`, so a spawned role follows the parent session. A role file's own
settings beat the parent's, which is why the old hard-coded pins are gone. `acceptance-reviewer` —
the item-by-item ticket-coverage review that runs after implementation and before
`critical-reviewer`, in `$cenci:review` PR mode, and for `$cenci:close` — inherits like `planner`.
Re-running configure installs a new missing role; existing role customizations are preserved.

`domain-specialist` is the exception: it is the escalation role for qualifying tasks and keeps its
pin to `gpt-6-astra` with high reasoning effort. The [shared policy](model-escalation.md) defines
the triggers, bounded read-only dispatch, evidence validation, and unavailable-model fallback. The
repository's installed roles mirror their templates byte for byte. A pin is configuration, not a
guarantee of account-level access.

**Overrides.** `install-agents.sh --role-override <role>=<model>[:<effort>]` (repeatable) writes
`model`, and `model_reasoning_effort` when given, into a newly created role file only, right after
`description`. The role must be a known role name, the model must match `[A-Za-z0-9._-]+` and the
effort `[a-z]+`; anything else, a role given twice, or a mix with a migration mode is refused with
exit 2 before any directory or file is written. An override for a role whose file already exists is
skipped with a one-line stderr notice and exit 0. Configure asks one question per to-be-created role
(default: inherit) and passes the answers through this flag. Overrides live in the role TOML, not in
`.cenci/config.json`.

**Migration of old pins.** Earlier templates pinned a model and effort. An existing role file stays
byte-identical unless the user approves a per-file migration during a configure re-run.
`install-agents.sh --migrate-preview` is read-only and prints a `diff -u` for each file whose
`model` and `model_reasoning_effort` exactly match a pair that role's template once shipped:
`planner`, `implementer` and `critical-reviewer` `gpt-5.6` or `gpt-6.1-sol` with `high`;
`acceptance-reviewer` `gpt-6.1-sol` with `high`; `context-gatherer` and `structure-analyzer`
`gpt-5.6-terra` with `medium`. A user-chosen pin, a file with no pin, and `domain-specialist` are
never offered. `install-agents.sh --migrate-apply <role>...` deletes exactly those two lines. It is
all-or-nothing: it re-checks eligibility for every named role first and refuses (exit 2, nothing
written) an ineligible, unknown, duplicated or `domain-specialist` role. Maintain and SessionStart
never flag pins.

**Unavailable model or effort.** An override is unverified until a native spawn confirms it; the
validator never checks model names offline. Nothing is substituted. The built-in-worker fallback
applies only to a missing role file. When a spawn fails because the role's configured model or
effort is unavailable, the workflow stops and reports the role, the configured value and Codex's
error. The native evidence is tracked in
[`codex-native-evidence.md`](codex-native-evidence.md#native-agent-roles-inherit-override-unavailable-model),
pending the maintainer's native run.

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

### Validating role files

`codex/validate-agent-roles.sh [--plain] <dir> [<dir> ...]` parses every `*.toml` under
each directory, recursively (sorted path order; symlinked subdirectories are not followed), and
classifies what it finds. It checks a documented, version-stamped subset of the
role schema (Codex docs 2026-11, Codex CLI 0.144.1+), not the full schema: `name`, `description` and
`developer_instructions` are required non-empty strings; `model` and `model_reasoning_effort` are
strings; `sandbox_mode` is one of `read-only`, `workspace-write` or `danger-full-access`;
`mcp_servers` and `skills` are tables, and `skills.config` is an array of tables (`[[skills.config]]`
entries; entry fields are not checked). Model names are never checked offline.

| Class | Kinds | Effect |
|---|---|---|
| invalid | `invalid-toml`, `unreadable`, `missing-field`, `invalid-type`, `invalid-value`, `duplicate-name` | exit 1; the SessionStart hook advises and maintain warns (drift) |
| unverified | `unverified-key` (a key outside the subset, such as `retirement_plan`), `unverified-value` (an effort outside `minimal`/`low`/`medium`/`high`/`xhigh`, since supported efforts vary by model and version) | exit 0; listed as notes, never drift |
| advisory | `name-convention` (a valid `name` that differs from its filename stem; Codex treats `name` as authoritative) | exit 0; listed as a note |
| unavailable | Python 3.11+ with `tomllib` missing | exit 3 with the stderr line in [Workflow runtime](#workflow-runtime) |

Exit codes are 0 (clean or notes only), 1 (at least one invalid finding), 2 (usage error: no
directory, not a directory, or no `*.toml`) and 3 (validation unavailable). **Each directory argument
is one configuration layer**, the way Codex treats `~/.codex/agents/` and `.codex/agents/` as separate
layers, so a `name` repeated in two directories is not a `duplicate-name`; a `name` repeated inside
one directory (including its subdirectories) is. Nested files belong to the layer of the directory
argument they sit under, matching Codex's recursive discovery of `.codex/agents/**`.

`check-agent-role-drift.sh --plain` wraps the validator and prints one state, read as
`read -r state rest`: `clean`, `clean <note>[; <note>]`, `drift <failing findings>`, `absent` (no
`.codex/agents/`), `unavailable python3.11+-with-tomllib-required`, or `unknown <reason>` for a
resolver or validator failure (a validator exit 0 whose output is not a known note kind is
`unknown validator-output`, never `clean`). As a SessionStart hook it is silent for `clean`, notes
and `unknown`, gives the drift advisory for `drift`, and gives a separate Python 3.11+ prerequisite
advisory for `unavailable`. Maintain's `agent-roles` check reports `pass` for `clean` (listing any
notes), `warn` for `drift`, and a distinct `warn` naming the Python 3.11+ prerequisite with an
install fix for `unavailable`; a missing prerequisite is never a `skip` and never called "invalid".
The drift check is wired only into Claude Code's SessionStart (`hooks/hooks.json`), not
`codex/hooks.json`. `cenci doctor` and the launcher's model shortcuts are out of scope here.

The `lint-command` advisory (`hooks/scripts/check-lint-command.sh`) is wired only into Claude Code's SessionStart, like the config-staleness and agent-role-drift advisories, and not into `codex/hooks.json`.

## Hook launch failures

Codex runs every hook as `$SHELL -lc "<command>"` with `${PLUGIN_ROOT}` substituted into the
command text. When a guard or handler cannot start, the exit code and stderr tell you why.
`codex/hooks-launch.test.sh` runs every handler in `codex/hooks.json` through each launch
failure below and fails if this table lacks a row for any exit code the fixtures observe.

"What Codex does" is derived from the codex-rs source (exit 2 with non-empty stderr is
reported as Blocked; any other non-zero exit is reported as Failed and the tool call is not
blocked). The fixtures verify the guards' exit codes and messages deterministically; what a
native Codex binary does with them is the job of the opt-in runner (#1212), whose results
the maintainer records in [`codex-native-evidence.md`](codex-native-evidence.md). Until that
record is filled in from real runs, the "What Codex does" column stays derived from the
codex-rs source, not observed. The login-shell raw-form code for fish is recorded by the
suite when fish is installed; no fish row is listed because it has not been observed.

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

## Guard canary preflight

Codex does not expose hook trust state non-interactively, and it skips pending hooks, so an
installed flow plugin is not proof that `check-sensitive-files.sh` and
`guard-main-worktree.sh` are watching a session. Every mutating Codex workflow therefore
runs `bash "${PLUGIN_ROOT}/codex/guard-canary.sh"` first. The `persist` turn (it writes the plan
artifact and the checkpoint) and the normal-mode `apply` of `implement`, `address-review`,
`refactor`, `refine`, `configure` and `maintain` run it, and so do `sync`, the supervisor-started
`ci-repair` session, `babysit-attention` (before `cenci babysit reset` and before
`cenci babysit stop`, never for its summary, its question or a headless `codex exec` run), and
`babysit`'s Codex arm step. `/plan` stays mutation-free and does not run it, and the
check is unconditional, so it runs in every session, sandbox included, and again in each turn.
`skills/codex-runtime/SKILL.md` owns the procedure; Claude Code sessions are excluded because
Claude Code has no pending-trust state for plugin hooks.

The babysit check runs once, when arming. It does not carry over to the `ci-repair` and
`babysit-attention` sessions the supervisor starts later, which run their own canary. A
`ci-repair` re-launch that makes no write (a stale head, a push already on the remote, or no
`fail` bucket) exits before the canary and runs none; recovery or a fresh diagnosis runs it
before its first checkpoint write.

- **Probes.** `plan` reads `codex/hooks.json` and gives every (matcher group, guard) pair its
  own probe (four with today's manifest: `Write|Edit` and `Bash`, each with both guards). Each
  target is absolute and unique per run, and is chosen so exactly one guard denies it: a `.env`
  file in a fresh 0700 run directory outside the repository for `check-sensitive-files.sh`, and a
  `.txt` file in the main worktree for `guard-main-worktree.sh`. Before it prints anything,
  `plan` runs both real guard scripts on each probe and refuses if the probe is not
  single-guard, or if any receipt already exists. An unknown matcher or guard, an unreadable
  manifest, or a missing `jq` also refuses. For each shell probe `plan` also prints a
  `COMMAND<TAB><n><TAB>printf x > '<target>'` line with the target single-quoted (an embedded `'`
  is written `'\''`), and the session runs that line verbatim, so a repository path with spaces
  or shell metacharacters creates no stray file.
- **Guard receipts.** When a guard blocks a canary-shaped probe target, it writes a guard
  receipt into the run directory: `V1`, the guard, the run id, the probe number and the tool
  name, never the command or the diff. It writes one only when the run directory's `probes.tsv`
  lists that probe for that guard, so `plan`'s own self-check can never leave one, and a receipt
  failure never changes the guard's exit code, message or decision. The run directory must be
  a real directory owned by you and not group- or world-writable.
- **Verdict.** The session makes one tool call per probe and reports one verdict token per
  probe, then runs `check <run-dir> <n>=denied|not-denied|blocked|not-attempted…`: `denied`
  when the guard's own message appeared in the result, `blocked` when Codex's sandbox or
  approval policy refused the call before any guard answered, `not-attempted` when the call was
  never made, and `not-denied` for anything else. `CANARY_STATUS=active` needs every probe
  reported `denied`, backed by a valid guard receipt (a regular file, not a symlink, owned by you,
  with the run id, probe number, guard and tool all matching), and nothing written. A written file
  always means inactive protection, whatever the session reported (`WRITTEN` wins even next to a
  receipt). Every other outcome refuses with its own `PROBE_RESULT` line:
  `REPORTED DENIED WITHOUT GUARD RECEIPT`,
  `BLOCKED BEFORE THE GUARD (Codex sandbox or approval refusal)`, `NOT ATTEMPTED`,
  `NO GUARD MESSAGE` (`not-denied`) and `NO RESULT` (nothing reported). A receipt that does
  not match the run is named on a `BAD_RECEIPT:` line, and a valid receipt next to a `blocked`,
  `not-attempted` or `not-denied` report prints the same line prefixed
  `GUARD RECEIPT PRESENT BUT REPORTED <verdict>`, naming both.
- **What `active` proves.** `CANARY_STATUS=active` rests on the guards' own receipts, the
  session's report of each probe and the filesystem check. A receipt catches a session that
  misreports, such as a sandbox refusal reported as a denial; it cannot stop a session that
  deliberately writes a fake receipt into its own run directory, because the hook and the
  session run as the same user.
  The same user can also swap the run directory's `probes.tsv` or a receipt for a FIFO after the
  guard or `check` has tested it, which hangs that one guard or `check` call until it is killed or
  times out; both read only a bounded prefix of a regular file, so an oversized file cannot stall them.
  This is a readiness check, not proof; the opt-in native run is the real evidence.
- **Refusal.** Any probe that is not a receipt-backed denial refuses before any mutation. The
  refusal names pending or untrusted hooks as the likely cause and points to `/hooks`; rerun the
  workflow after reviewing them.
- **Cleanup.** Whatever got written is removed. A removal that fails is reported as
  `LEFTOVER: <exact path>` for you to delete by hand, and the workflow still refuses.
- **Interrupted runs.** If a session dies between `plan` and `check` while hooks were inactive,
  `<main-root>/cenci-guard-canary-<hex>-<n>.txt` files can stay in the main worktree, and a
  `.env` probe stays in the run directory under the temp area. A later run uses a new id and
  does not sweep them; delete them by hand (`cenci-guard-canary-*` in the main worktree, and the
  stale run directory).
- **Sandbox label.** In a `cn xt` session the launcher passes the trust-bypass flag, and the
  output says "hook trust bypassed by launcher (explicit flag)", never "trusted". cenci reports
  trust state and never changes it.
- **Repositories under `/tmp`.** `guard-main-worktree.sh` allows `/tmp/*` targets outright, so a
  repository that lives under `/tmp` cannot show a denial there: the probe self-check fails and
  the canary refuses (fail closed). Keep repositories elsewhere (for example under `/var/tmp`
  or your home directory).
- **Hook environment.** `guard-main-worktree.sh` looks for the run directory under `$TMPDIR`, then
  `/tmp`, the same order `plan` uses to pick it. A `guard-main-worktree` probe whose run directory the hook process cannot find under `$TMPDIR` or `/tmp` gets no receipt, so the canary refuses (fail closed). That happens when Codex gives hook processes a different `$TMPDIR` than the session's tool calls, or keeps them away from the run directory.

Verified so far: `codex/guard-canary.test.sh` exercises the helper against the real guard
scripts, `codex/native-hooks.gate.test.sh` covers the native runner's canary scenario with a
Codex stub, and `codex/workflows.test.sh` asserts where each surface runs the step. Native
Codex 0.166.0 runs on 2026-11-04 confirmed all four probes on the host with persisted hook
approval and in a Codex sandbox with launcher trust bypass. The sandbox trust-withheld
variant refused and removed all four written probes, with no leftovers. `apply_patch`
reached the `Write|Edit` matcher. Those runs predate guard receipts. Run details and remaining experiment limits are recorded in
[`codex-native-evidence.md`](codex-native-evidence.md).

## Attention behavior

Cenci `need-input` renders a red/dim foreground `!`. A red tmux background without `!`
is usually tmux's native bell alert from a BEL notification. Run `cenci doctor` to inspect
Codex notification configuration; choose `osc9` yourself if BEL overlays are unwanted.
