---
name: codex-runtime
description: Shared native Codex stage, checkpoint, goal, and agent-adapter contract.
user-invocable: false
---

# Native Codex workflow runtime

- Every normal-mode invocation starts with `sh "${PLUGIN_ROOT}/codex/require-runtime.sh"` before any checkpoint, workflow, or GitHub mutation; if it exits non-zero (Node.js 18+ is not on PATH), stop and report its one-line stderr message. The script is silent when Node 18 or newer is present and changes no state.
- Planning workflows (configure, refine, implement, address-review, refactor, maintain) plan only in Plan mode and hand the approved plan to a fresh `apply` through the **Approved-plan handoff** section below; every companion's `codex.md` references that section and never restates it. `skills/refine/codex.md` owns refine's pre-write checks and independent automerge policy.
- Persist phase state at `.cenci/checkpoints/<workflow>-<target>.json` with `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" <init|advance|block|complete|get|clear> <path> [<workflow> <target> [<phase>]] [flags] | verify <path> <workflow> <target> | digest <plan-path>`. The helper is a strict schema-v2 state machine and writes atomically (temp file, `fsync`, rename).
  Store workflow, target, phase, plan path, worktree, last completed gate, and status.
  The contract below is Codex-only; Claude Code skills do not use it.
  - Schema v2 has exactly these 12 fields: `schemaVersion` (2), `workflow`, `target`, `phase`, `status`, `planPath`, `planDigest` (64 lowercase hex characters, the sha256 of the plan), `worktree`, `lastCompletedGate`, `agentFallbacks` (a list of `{role, worker}`), `goal`, and `updatedAt`. `planPath` and `worktree` are normalized repo-relative paths: never under `.git`, with no trailing slash. Unset fields are `null` (`agentFallbacks`: `[]`). `goal` is `null`, `created`, `skipped-active`, or `unavailable`.
  - Positional CLI: `<init|advance|block|complete|get|clear> <path> <workflow> <target> [<phase>]`. `advance` and `block` require `<phase>`; `init` accepts only `planned`; `complete` may omit it and keeps the current phase; `get` takes the identity only as an optional check; `clear` requires the identity and takes no phase. `<target>` matches `[A-Za-z0-9][A-Za-z0-9._-]{0,127}` and never contains `..`.
  - Metadata flags, valid on `init`, `advance`, `block`, and `complete` and each given at most once (`--flag value` or `--flag=value`): `--plan-path P --plan-digest D` (always together), `--worktree W`, `--gate G`, `--goal created|skipped-active|unavailable`, and a repeatable `--fallback <role>:<worker>` (both names match `[a-z][a-z0-9-]*`, merged by role across calls so a later worker replaces an earlier one). `clear` takes only `--force`; `get` takes no flags. An unknown, duplicated, or misplaced flag is a usage error (exit 2).
  - Paths: `<path>` is resolved against the main checkout (the parent of `git rev-parse --git-common-dir`, resolved to its real path), not the cwd, so the same `.cenci/checkpoints/<workflow>-<target>.json` works from the main checkout, a worktree, or the sandbox. Outside a git repository or in a bare repository the helper exits 2. Any `..` segment is rejected. The file must sit directly in `<main-checkout>/.cenci/checkpoints/` and be named `<workflow>-<target>.json`, matching the identity given and the identity recorded inside the file. A symlinked `.cenci`, a symlinked `.cenci/checkpoints`, or a symlinked checkpoint file is refused outright (`clear --force` does not override this). Only `init` creates the state directory (mode 0700) and writes a `.gitignore` of `*` in it, so `get`, `advance`, and `clear` never create it.
  - Phase tables (closed, in order; the first entry is always `planned`): implement `planned, worktree, baseline, tests, implement, refactor, review, docs, pr`; maintain `planned, worktree, baseline, fix, review, pr`; refactor `planned, worktree, baseline, refactor, review, pr`; address-review `planned, worktree, fix, verify, push`; configure `planned, apply, pr`; refine `planned, apply`; review `planned, review, post`; sync `planned, sync`. A phase outside the workflow's own table is rejected.
  - Statuses: `planned` (set only by `init`), `running` (`advance`), `needs-input` (`block`), and `complete` (`complete`, terminal: `advance`, `block`, and `complete` then exit 2 with `checkpoint is complete (terminal)`, and only `clear` resets it).
  - Transitions: `advance` and `block` cannot target `planned`, and the new phase must come strictly after `lastCompletedGate`; `complete` may land on the recorded gate but not before it. `--gate G` must be a phase of the workflow, at or after the recorded gate (equal changes nothing, backwards exits 2 with `lastCompletedGate cannot move backwards`) and at or before the new phase. Without `--gate` the recorded gate is kept. A rejected call changes no bytes and prints nothing on stdout.
  - `init` is retry-safe. With no file it creates the checkpoint and prints the state. With a compatible v2 file (same identity) it prints the existing state unchanged and ignores any metadata flags; `advance` is the only way to change metadata. It never resets: another recorded identity, a `--plan-digest` over a checkpoint that records none (`records no plan digest`), or a different plan path or digest (`plan path or digest differs`) exits 2.
  - Legacy v1 (`schemaVersion` 1): `get` prints it with `"legacy": true`. `init` with the same identity upgrades it in place to v2, applying any metadata flags given and turning an absolute path inside the checkout into a repo-relative one (a v1 file has no digest or goal, so those are `null` until set). A v1 file with an unknown phase, status, or gate, an absolute path outside the checkout, or malformed fallbacks cannot be upgraded: `init` exits 2 naming `clear`, and never maps the value to something else. `advance`, `block`, and `complete` reject v1 with `legacy v1 checkpoint; run init to upgrade it`.
  - `get` validates the whole file (strict fields, enums, relative paths, digest, timestamp) and prints the state as one JSON line. A missing file or missing state directory exits 3 with `no checkpoint found`. An unknown `schemaVersion`, malformed JSON, or any invalid field exits 2 without echoing the file content. `lastCompletedGate` is the resume gate: resume at the first phase after it.
  - `clear` requires the identity and checks it against the file name and the recorded identity (exit 2 on mismatch; a file that records a different identity than its name is never reset, and `clear` cannot remove it either, so remove it by hand). Missing state is success (exit 0, nothing created). It removes a valid v2 or a v1 checkpoint. A malformed or unknown-schema file exits 2 with `re-run with --force to remove it`; with `--force` it is removed and `removed unreadable checkpoint (...) because of --force` is reported on stderr.
  - `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" verify <path> <workflow> <target>` is the read-only approved-plan check: it takes no flags, never creates `.cenci`, and checks in order the identity, that a checkpoint exists, that it is a v2 file that is not `complete`, that it records a `planPath` and `planDigest`, and that the plan artifact (a repo-relative, non-symlinked regular file under `.plans/`) still hashes to `planDigest`. On success it prints one JSON line: the 12 state fields, `resolved` (`mainCheckout`, `planPath` and `worktree` as absolute paths under the current environment's main checkout; `worktree` is `null` when none is recorded) and `nextPhase`, the first phase after `lastCompletedGate` (with no gate recorded, the phase after `planned`; `null` after the last phase). A missing checkpoint exits 3 and every other rejection exits 2, each with one `checkpoint: <path>: <problem>; <remedy>` stderr line listed in `docs/codex.md`.
  - `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" digest <plan-path>` is read-only too: it reads the same kind of artifact with the same rules as `verify` and prints its sha256 as 64 lowercase hex characters and a newline.
  - Exit codes: 0 success; 1 unexpected I/O failure, such as `write failed (EIO); the previous checkpoint is unchanged` (the previous file stays readable and no temp file is left); 2 rejection or usage error; 3 missing state. Every error is one stderr line starting `checkpoint: <path>: <problem>; <remedy>`, with no stack trace, and stdout carries the state JSON only after the write succeeded.
- Native Codex goals follow the single **Goal lifecycle** section below; every companion's `codex.md` references its create, stop and complete steps and never restates them. On execution ambiguity, run `checkpoint.mjs block`, ask exactly one blocking question and stop (the stop step); resume from the checkpoint after the answer.
- Prefer generated `.codex/agents/*.toml` roles. If unavailable, delegate to a built-in
  worker with the same bounded role prompt and report that fallback in the checkpoint.
- Keep authenticated mutations, pushes, PR creation, and user questions in the root agent.

## Approved-plan handoff

This handoff is Codex-only: Claude Code has no collaboration-mode switch, and `phases/phase-1-plan.md` owns its approval, so Claude Code `SKILL.md` procedures stay unchanged. A planning workflow moves a plan from Plan mode to a fresh `apply` process in three steps, each in its own turn: plan in Plan mode, `persist` in Default mode, then `apply`. The plan travels in an artifact and its sha256 digest in the checkpoint, never in the conversation or a resumed session.

- **Target** (`<target>`, matching the checkpoint's `TARGET_RE`): implement uses the ticket id, or the task slug in ticketless mode; refine uses the ticket number; address-review uses the PR number; configure and refactor use `repo`; maintain uses `<mode>`, or `<mode>-<scope>` when a scope is given.
- **Artifact**: implement writes `.plans/<ticket>-<slug>.md`; every other workflow writes `.plans/codex/<workflow>-<target>.md`. Paths are stored repo-relative and `verify` resolves them against the main checkout of the current environment, host or sandbox.
- **Checkpoint** `<ckpt>` is `.cenci/checkpoints/<workflow>-<target>.json`.

### Plan-mode self-check

This check applies to the planning request only: the request that is neither `persist` nor `apply`. Before any other planning step, read the active `<collaboration_mode>` developer instruction. If it is not Plan, or it cannot be read, make no tool call at all (no read, no shell, no `require-runtime.sh`) and print exactly one line: `cenci: $cenci:<wf> plans only in Plan mode — type /plan and resend: /plan $cenci:<wf> <target>`. Then end the turn.

`persist` and `apply` run in Default mode and are not refused by this check; each keeps its own rule. A `persist` or `apply` request that arrives while Plan mode is still active is refused with no tool call and the line `leave Plan mode and resend`. Otherwise, in Default mode `persist` proceeds to the Persist step, and `apply` proceeds to `require-runtime.sh`, then `verify` (see Apply).

### Write boundaries

Planning in Plan mode makes no project or GitHub mutation: no project file, checkpoint, `.plans` file, label, ticket, ownership claim or `cenci pipeline` write. The persist step makes no claim, label, worktree, `cenci pipeline` or GitHub write either.

### Persist step

Run `persist` in Default mode, in the same session as the approved plan: `$cenci:<wf> persist <target>`. The first normal-mode turn after a Plan-mode approval in that session (for example Codex's own "implement the plan" turn) runs only this step, never apply mutations.

Approval check, before anything else: a Plan-mode-approved plan is a plan produced in Plan mode in this session for `<wf> <target>`, followed by the user's `persist` or implement-the-plan turn. If this session holds no Plan-mode-approved plan for `<wf> <target>` (for example a fresh Default-mode session launched with `cenci run <wf> persist <target>`), make no tool call, write nothing and print exactly `cenci: no approved plan in this session — type /plan and resend: /plan $cenci:<wf> <target>`, then end the turn.

Otherwise start with `sh "${PLUGIN_ROOT}/codex/require-runtime.sh"`. Persist writes only the plan artifact and the checkpoint, in this order:

1. Check for an existing checkpoint before writing anything. This step decides only and writes nothing: run `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" get <ckpt> <wf> <target>`. Exit 3 (no checkpoint) continues. Exit 0 branches on the printed `status` (`complete` is a status, never a phase: a finished run keeps its last real phase, such as `pr`). A `status` `planned` or `complete` means run `clear` later, in step 4, just before `init` (see Re-plan below), then continue; a legacy v1 checkpoint whose `status` is `planned` or `complete` is cleared and replaced like a v2 one. A `status` `running` or `needs-input` is a run in flight: refuse before writing anything, change nothing, print the resume `apply` command (step 6) and say that if that `apply` stops on `verify`, the user can discard the run with `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" clear <ckpt> <wf> <target>` and re-plan, then end the turn. Any other `status` (an unrecognised value, an absent field, or a non-string value on a legacy v1 checkpoint) is a run in flight too, with the same refusal. Any other exit: stop and report its stderr line, writing nothing.
2. Run the workflow-specific artifact check before the artifact write: implement's three-heading check is that check. A failure stops persist with no artifact write and leaves any existing checkpoint byte-identical. Write the artifact with the headings `## Target`, `## Approved Plan` (the approved plan verbatim) and `## Choices` (every answer given and every option the user declined, so `apply` needs no conversation). Implement is the exception: for implement the artifact is the planner's plan file, which starts with YAML front matter (`---` as the first line) and contains `## Ticket Details`, `## Implementation Plan` and `## Architectural Context`; the choices made are carried by appending a `## Choices` heading after them (or by the plan's own `## Q&A from Planning`). The `## Target` and `## Approved Plan` wrapper applies to the other five workflows only.
3. Run `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" digest <artifact>`.
4. Only when step 1 saw `planned` or `complete`, persist runs `get` again just before the replacing `clear`: `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" get <ckpt> <wf> <target>`. If it still shows `status` `planned` or `complete`, run `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" clear <ckpt> <wf> <target>`. Otherwise (another `status`, or any non-zero exit, exit 3 included) persist stops without `clear` or `init` and reports it: the artifact was already rewritten but the checkpoint was not changed, so that run's next `verify` stops on the digest mismatch, and the user can discard it with the user-run `clear` named in step 1.
5. Run `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" init <ckpt> <wf> <target> planned --plan-path <artifact> --plan-digest <digest>`.
6. Print `cenci run <wf> apply <target> --agent codex` and `$cenci:<wf> apply <target>` as the two ways to continue, and end the turn.

A failure of any write stops persist and reports it; the artifact is then not an approved plan.

### Apply

Apply starts with `sh "${PLUGIN_ROOT}/codex/require-runtime.sh"`, then `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" verify <ckpt> <wf> <target>`: a non-zero `verify` stops before any write and reports its one stderr line unchanged; the remedy is always to plan in Plan mode, then persist (apply never clears; to discard a leftover checkpoint first, the user runs `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" clear <ckpt> <wf> <target>`, a user-run step). A Claude-persisted `.plans/<id>-<slug>.md` with no Codex checkpoint is not an approved plan, so that stop also applies to it, with zero writes. Read the plan only from `resolved.planPath` right after `verify`. Only then run the workflow's mutations, starting at `nextPhase`. `nextPhase` `null` means every phase is done: run no workflow mutation, run `complete`, and report that the run is already finished. A failure of any checkpoint write (`advance`, `block`, `complete`) stops apply before the next mutation and is reported.

- A verified checkpoint whose `phase` is `planned` is a new-plan apply; any later phase is a saved-plan apply. The workflow's own resume routing (for implement, the `cenci pipeline` stage table) still picks the resume route; the checkpoint only picks first apply versus saved-plan apply.
- Resume from the recorded gate: skip every phase through `lastCompletedGate` and never recreate a recorded worktree, re-apply a label or reopen a PR for a completed gate. A recorded `resolved.worktree` is attached, not created again.
- Apply advances the checkpoint to a phase before that phase's first write, so a crash resumes from the recorded gate instead of repeating it.

When a workflow's apply reaches its successful end, it runs `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" complete <ckpt> <wf> <target> <last-phase>` (without `--gate`), naming the last phase of that workflow's table. That marks the run finished in the checkpoint, so a re-plan of the same target is accepted and a second `apply` is refused by `verify`. It is a separate step from the Goal lifecycle's `update_goal({status:"complete"})` and never replaces it; for implement it runs after the babysit hand-off attempt. The last phase and the success point per workflow:

| Workflow | Last phase | Success point |
|---|---|---|
| `implement` | `pr` | after `gh pr create` succeeds and the babysit hand-off has been attempted |
| `maintain` | `pr` | after the PR is open and its mergeability is confirmed not CONFLICTING (an unresolved UNKNOWN runs no `complete`) |
| `refactor` | `pr` | after the approved tickets are created |
| `address-review` | `push` | after the push, the replies and the review re-request |
| `configure` | `pr` | after the PR is opened |
| `refine` | `apply` | after the GitHub writes are verified as persisted |

Nothing completes a run early: a stopped, blocked, declined or failed apply never runs `complete`. That covers a `verify` failure, a `block`, refine's Confirmation Gate Decline (the checkpoint stays `planned`), a pre-write STOP or lean-contract STOP, maintain's unresolved UNKNOWN mergeability (the checkpoint stays `pr` and `running`), and implement's PR stop and unknown-stage stop, which touch nothing.

If `complete` fails after the final mutation succeeded, report the deliverable (PR URL, ticket numbers or pushed SHA) together with the `complete` failure, never as a clean success. The mutation is never undone. Since `complete` is the last checkpoint write, no later mutation is left to stop. The remedy is the user-run `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" clear <ckpt> <wf> <target>`.

### Re-plan

Persist over an existing checkpoint of the same identity follows its `status`, and the status check is the first persist step, before the artifact is written. A checkpoint whose `status` is `planned` or `complete` is replaced: the artifact is written and digested first, `get` is repeated, the `clear` runs just before `init`, and `init` records the new digest; a legacy v1 checkpoint whose `status` is `planned` or `complete` is cleared and replaced like a v2 one. A checkpoint whose `status` is `running` or `needs-input`, or any other `status` (an unrecognised value, an absent field, or a non-string value on a legacy v1 checkpoint), is a run in flight: refuse, change nothing (the artifact included, since the in-flight run's approved plan lives there) and print the resume `apply` command. That holds for an in-flight run that step 1 sees: a run that starts `apply` between step 1 and step 4 is caught by step 4's second `get` (the stop leaves the artifact rewritten under that run's checkpoint, so its next `verify` fails closed on the digest mismatch), and the status guard on `clear` lives in persist, not in `checkpoint.mjs clear`, which removes a checkpoint whatever its `status`. Clearing an in-flight run needs an explicit `checkpoint.mjs clear` by the user (`checkpoint.mjs clear <ckpt> <wf> <target>`, named if that `apply` stops on `verify`), in line with `init`'s never-reset rule above.

## Goal lifecycle

This lifecycle is Codex-only: Claude Code has no goal tools, so Claude Code `SKILL.md` procedures stay unchanged. Implement and maintain reference its create, stop and complete steps from their `codex.md` and never restate them. A goal never replaces the checkpoint: every case below continues on the checkpoint, which stays the source of truth.

Only a human-issued `apply` may create a goal. Planning, `persist`, `ci-repair` and `babysit-attention` never create a goal. No cenci automation (dispatch, babysit, `ci-repair`, `babysit-attention`) issues `apply`, so only a human-started `apply`, attended or not, creates a goal. The objective is fixed: `Complete approved plan <path> through an open PR`, where `<path>` is the repo-relative `planPath` that `verify` returns. Never set a `token_budget`. Babysit through merge is never part of the objective.

### Create step

Run once per `apply`, after `verify` passes and after the workflow's own stop-only checks, right before the first checkpoint `advance` of the run (so a red baseline already runs with the goal in place). Decide in this order and record the outcome with `--goal <created|skipped-active|unavailable>` on that `advance`, or on the `block` if the run stops there:

1. The client exposes no goal tools (`get_goal`, `create_goal`, `update_goal`), meaning they are absent from the client's tool list before any goal call is made: make no goal call, record `unavailable` and use checkpoint-only recovery: the run continues on `lastCompletedGate` and `nextPhase` alone.
2. `get_goal({})` reports a goal with a status other than `complete` (active, paused, blocked, or budget- or usage-limited): create none, record `skipped-active`, and continue on checkpoint state alone; that goal is left unchanged. The one exception is this run's own goal on a resumed `apply`: when the checkpoint records `created`, or records no goal (a crash or lost response between `create_goal` and the first `advance --goal`), and `get_goal` reports an unfinished goal whose objective equals this run's objective, adopt it as `created` (pass no `--goal` when the checkpoint already records `created`, otherwise record `created`); a goal with a different objective is `skipped-active`.
3. Otherwise (no goal, or only a `complete` one): call `create_goal({objective: "Complete approved plan <path> through an open PR"})` and record `created`.

`unavailable` is only for tools absent before any call: any error from an invoked goal tool (`get_goal`, `create_goal` or `update_goal`) is a stop, never downgraded to `unavailable` (see the Stop step: `checkpoint.mjs block`, report and end the turn), and that `block` records no `--goal` value.

### Stop step

A question to the user, a red or unrunnable baseline gate, a native-tool error (a goal tool error included), a failed `gh pr create`, or a budget or usage limit (the client marking the goal budget-limited or usage-limited, or implement's `Context: low` split signal) all stop the same way: run `checkpoint.mjs block` for the current phase, report, and end the turn. The unfinished state stays in the checkpoint (`needs-input`, `lastCompletedGate` unchanged) so the next `apply` resumes from it. The goal is left exactly as it is, and a stop never marks the objective complete. cenci never invents a clear or pause operation. Only when the running client exposes a pause or blocked transition, and only under the conditions of that client's own contract, may a run use it, never on cenci's own initiative.

### Complete step

Run only when the checkpoint's `goal` is `created`: with `skipped-active` or `unavailable` make no goal call, because a goal this run did not create is never completed. Run it only after a successful `gh pr create` (a recovered already-exists PR that reads back `OPEN` with a url and number counts), plus any post-create gate the workflow defines, such as maintain's mergeability check. Then call `update_goal({status:"complete"})` once, before `cenci babysit` is armed. A failed `gh pr create` is a stop, not a completion, and an error from `update_goal` is a native-tool error: the Stop step runs and the PR stays open.

### Goal cases

| Case | When | Goal call | Checkpoint |
|---|---|---|---|
| existing goal | `get_goal` reports a goal that is not `complete` | no goal call beyond `get_goal`; that goal is left unchanged, except this run's own goal on a resumed `apply` (Create step 2) | `goal` `skipped-active`; the run continues |
| no goal | no goal, or only a `complete` one | `create_goal` with the objective `Complete approved plan <path> through an open PR` | `goal` `created` |
| no tools | the client exposes no goal tools | no goal call; checkpoint-only recovery | `goal` `unavailable`; the run continues |
| ambiguity | execution needs a human answer | no goal call; the goal is left unchanged | `block`, status `needs-input` |
| red baseline | `run-gate.sh` reports red, or cannot run | no goal call; the goal is left unchanged | `block` at `baseline`, status `needs-input` |
| tool error | a native tool returns an error, or an error from an invoked goal tool | no goal call; never downgraded to `unavailable` (`unavailable` is only for tools absent before any call) | `block`, status `needs-input`; a failed create records no `goal` |
| budget exhaustion | goal budget or usage limit, or `Context: low` | no goal call; the goal is left unchanged | `block`, status `needs-input` |
| PR success | `gh pr create` succeeded (Gate C passed in maintain) | `update_goal({status:"complete"})` only when `goal` is `created` | status `complete` |
| PR failure | `gh pr create` failed and no OPEN PR is recoverable | no goal call; the goal is left unchanged | `block` at `pr`, status `needs-input` |
