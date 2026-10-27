---
name: codex-runtime
description: Shared native Codex stage, checkpoint, goal, and agent-adapter contract.
user-invocable: false
---

# Native Codex workflow runtime

- Every normal-mode invocation starts with `sh "${PLUGIN_ROOT}/codex/require-runtime.sh"` before any checkpoint, workflow, or GitHub mutation; if it exits non-zero (Node.js 18+ is not on PATH), stop and report its one-line stderr message. The script is silent when Node 18 or newer is present and changes no state.
- Interactive planning workflows must begin in `/plan`. Gather every material choice
  there and produce an approved plan without mutating GitHub or project files.
- Continue mutations in a second normal-mode invocation of the same skill, passing the
  approved plan or `.plans/<file>` explicitly.
- Refinement is an exception to that two-invocation procedure: an ordinary normal-mode
  refine invocation asks its material questions, validates the proposal and applies it
  in the same invocation. `refinement.confirmBeforeApply: true` adds final review in
  that invocation; false or absent does not ask again. An explicit `/plan` refinement
  remains read-only and returns the proposal for a later normal-mode invocation.
  `skills/refine/codex.md` owns the pre-write checks and independent automerge policy.
- Persist phase state at `.cenci/checkpoints/<workflow>-<target>.json` with `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" <init|advance|block|complete|get|clear> <path> [<workflow> <target> [<phase>]] [flags]`. The helper is a strict schema-v2 state machine and writes atomically (temp file, `fsync`, rename).
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
  - Exit codes: 0 success; 1 unexpected I/O failure, such as `write failed (EIO); the previous checkpoint is unchanged` (the previous file stays readable and no temp file is left); 2 rejection or usage error; 3 missing state. Every error is one stderr line starting `checkpoint: <path>: <problem>; <remedy>`, with no stack trace, and stdout carries the state JSON only after the write succeeded.
- On execution ambiguity, persist the checkpoint, clear the active goal, ask exactly one
  blocking question, and stop. Resume from the checkpoint after the answer.
- Arm a native Codex goal only after an approved implementation plan enters execution:
  call `create_goal({objective: "Complete approved plan <path> through an open PR"})` after
  verifying `get_goal({})` has no active goal. Clear it before human input/errors and after
  PR creation with `update_goal({status:"complete"})` only when the objective is achieved;
  otherwise checkpoint and return control so the client clears/pauses the active goal.
- Prefer generated `.codex/agents/*.toml` roles. If unavailable, delegate to a built-in
  worker with the same bounded role prompt and report that fallback in the checkpoint.
- Keep authenticated mutations, pushes, PR creation, and user questions in the root agent.
