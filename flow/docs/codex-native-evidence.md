# Codex native hook evidence

What a real, authenticated Codex does with cenci's `PreToolUse` guards, recorded from the
opt-in runner `codex/native-hooks.test.sh` (#1212). Every other suite in `flow/codex/` runs
the guards from a shell script; this record is the only place the behavior of the native
binary is written down.

**Status: skeleton (the hook sections, from `## How to produce a run` through
`## Resolution conclusion`).** Nothing in those sections has been observed yet. Each is a
placeholder that the maintainer's native runs replace with sanitized runner output. A
placeholder is never evidence, and no conclusion in `codex.md` or `adapter-contract.md` may
cite this file until its section is filled in. The last two sections, "Native workflows:
`ci-repair` and `babysit-attention`" and "Native workflows: Plan mode, handoff and goal
lifecycle", record a different runner (`codex/native-workflows.test.sh`, #1236 and #1235)
and carry their own status lines.

## How to produce a run

```bash
CENCI_CODEX_NATIVE=1 CENCI_CODEX_NATIVE_OUT=./native-host.md bash flow/codex/native-hooks.test.sh
```

- **Host run:** install the plugin from the branch under test through a local marketplace and
  trust it with `/hooks` first. The runner never passes `--dangerously-bypass-hook-trust` on
  the host. If the hooks are pending or untrusted its canary reports `not run: hooks
  pending/untrusted` and every guard-dependent row is skipped.
- **Sandbox run (`cn xt`):** the runner passes the trust-bypass flag only when
  `CENCI_SANDBOX=1` and one of the hard-coded container marker files (`/.dockerenv`,
  `/run/.containerenv`) exists; no environment variable can substitute the markers. It records
  the trust state as "hook trust bypassed by launcher (explicit flag)".
- The runner prints `not run: <reason>` and exits 0 when `CENCI_CODEX_NATIVE` is unset, Codex
  is absent or older than 0.144.1, or `codex login status` fails.
- A run that gets past the gates ends with one `RESULT:` line. `RESULT: OK` (exit 0) means at
  least one scenario row proved something and the canary showed the hooks active;
  `RESULT: INCONCLUSIVE` (exit 3) means nothing was proven (no `ok` scenario row, hooks
  inactive, or the canary could not tell); `RESULT: FAIL` (exit 1) means a row failed. While
  the canary has not shown the hooks active, no allow or deny row is run, because an allowed
  write proves nothing when no hook is watching.
- Pass or fail is judged from the filesystem after each call, the `codex exec --json`
  tool-call events and the guards' capture tap (`CENCI_CODEX_HOOK_CAPTURE_DIR`). A row whose
  call was not proven to have been attempted is `inconclusive`, never a success.
- Home paths, user and host names, fixture roots, session IDs and token-shaped strings are
  replaced by `<HOME>`, `<USER>`, `<HOST>`, `<FIXTURE>`, `<UUID>` and `<TOKEN>` before
  anything is committed. Git commit SHAs are kept.

## Runs

| Field | Host run | Sandbox run (`cn xt`) |
|---|---|---|
| Codex version | pending native run | pending native run |
| Plugin version / commit | pending native run | pending native run |
| Host or sandbox | host | sandbox |
| Trust state | pending native run | hook trust bypassed by launcher (explicit flag) (flag passed by the runner's `codex exec` inside the sandbox) |
| `$SHELL` | pending native run | pending native run |

## Exit 127

Hypotheses carried from #1041: the interpreter missing from the login-shell PATH, an unset
or empty `PLUGIN_ROOT`, a missing script, a non-executable script, `bash` or `jq` or `awk`
missing from the login-shell PATH, and fish parsing `${PLUGIN_ROOT}`.

| Item | Host run | Sandbox run |
|---|---|---|
| Resolved plugin root | pending native run | pending native run |
| Script present and executable (every `hooks.json` handler) | pending native run | pending native run |
| `$SHELL -lc 'command -v bash jq awk'` | pending native run | pending native run |
| Each guard rerun the way Codex launches it | pending native run | pending native run |

Result: pending native run. The 127 is recorded as fixed only if it is demonstrated;
otherwise it is recorded as unreproduced together with the hypotheses tested.

## Deny matrix

Each deny row is paired with a positive control whose allowed write must land.

| Scenario | Host run | Sandbox run |
|---|---|---|
| Bash to a sensitive `.env` (check-sensitive-files) | pending native run | pending native run |
| Bash to a main-worktree file (guard-main-worktree) | pending native run | pending native run |
| Bash to a worktree file (positive control) | pending native run | pending native run |
| apply_patch to a sensitive `.env` (check-sensitive-files) | pending native run | pending native run |
| apply_patch to a main-worktree file (guard-main-worktree) | pending native run | pending native run |
| apply_patch to a worktree file (positive control) | pending native run | pending native run |

## Payload cwd, hook pwd and effective cwd

| Way | Payload cwd | Hook pwd | Effective cwd | `workdir` in `tool_input` |
|---|---|---|---|---|
| Direct apply_patch | pending native run | pending native run | pending native run | pending native run |
| exec_command with a per-call `workdir` | pending native run | pending native run | pending native run | pending native run |
| Nested code mode | pending native run (or "unsupported in vX") | pending native run | pending native run | pending native run |

Can the effective workdir differ from the hook cwd? Pending native run.

## Relative apply_patch (Add, Update, Delete, Move)

| Scenario | Host run | Sandbox run |
|---|---|---|
| Root session, relative target resolving to the main worktree (denied) | pending native run | pending native run |
| Worktree session, relative target inside the worktree (allowed) | pending native run | pending native run |
| Root session, relative target under `.worktrees/` (allowed) | pending native run | pending native run |
| Bypass shape: worktree session, root `workdir`, relative `AGENTS.md` | pending native run | pending native run |

## Hook launch failure

Does the tool continue when a hook cannot start (exit 126 or 127, not exit 0 and not exit 2)?
Pending native run.

## Resolution conclusion

Pending native run: either the relative-apply_patch predicate in `guard-main-worktree.sh` is
confirmed by the evidence above, or the exception is tightened in the same change.

## Native workflows: `ci-repair` and `babysit-attention`

**Status: pending native run.** The runner exists and its not-run gates, trust-bypass flag
handling and judge are pinned in CI against a stub `codex` (`codex/native-workflows.gate.test.sh`),
but no run against a real Codex binary has been recorded (#1236). Every cell below is a
placeholder, not evidence, until the maintainer commits a sanitized run to the same PR as
the runner. The acceptance criterion for this section stays unchecked until then, and the PR
is not automerged before it.

`codex/native-workflows.test.sh` runs the INSTALLED cenci plugin's `$cenci:ci-repair` and
`$cenci:babysit-attention` through a real `codex exec` against a disposable fixture under
`/var/tmp`: a local bare remote with a PR branch whose gate (`sh check.sh`) fails, a clone of it
as the launch directory, a stubbed `gh` that answers reads and refuses and logs every write,
and a stubbed `cenci` that logs every call (`babysit stop` fails, as it does from inside a
sandbox). The remote's `pre-receive` hook logs every ref update, so a force push or a stray
branch shows up even when the remote rejects it.

### How to produce a run

```bash
CENCI_CODEX_NATIVE=1 CENCI_CODEX_NATIVE_OUT=./native-workflows.md bash flow/codex/native-workflows.test.sh
```

- Install the plugin from the branch under test through a local marketplace first (the same
  procedure as the hook runner's host run), and trust it with `/hooks` when running on the host.
  The procedures under test are the installed plugin's, not the runner checkout's: the report
  prints `codex plugin list` so the two can be told apart. Record the installed flow plugin's
  version and commit in the table below, not the runner checkout's.
- Trust, the `not run:` gates (`CENCI_CODEX_NATIVE` unset, Codex absent or older than 0.144.1,
  `codex login status` failing), the `RESULT:` line (`OK` exit 0, `INCONCLUSIVE` exit 3, `FAIL`
  exit 1) and the sanitization (`<HOME>`, `<USER>`, `<HOST>`, `<FIXTURE>`, `<UUID>`, `<TOKEN>`,
  commit SHAs kept) are the same as for the hook runner above. The runner refuses to print a
  report in which a home path, user name or host name survived sanitization.
- `RESULT: OK` needs the five positive rows to be `ok` (fast-forward advance, gate green on the
  pushed head, checkpoint `complete` with the pushed SHA, restart adds no commit, both attention
  options printed) and no failed row. A run that fails nothing but leaves one of them
  unproven (for example a `codex` that reads the PR and gives up) is `RESULT: INCONCLUSIVE`.
- A row is judged from the fixture's own state (remote refs and ancestry, the remote's ref-update
  log, the checkpoint file, the gate rerun on a fresh clone of the pushed head, the `gh` and
  `cenci` call logs), never from the model's narration. The headless `babysit-attention` rows are
  the exception: their subject is the printed decision, so they read the agent message, and only
  once the stubbed `gh` served a `pr view` or `pr checks` read. A scenario in which nothing shows
  the procedure ran is `inconclusive`, never a pass; so is every row that rests on the remote's
  ref-update log when that log is unreadable or empty although the remote advanced. A `codex exec`
  that exits non-zero or times out puts its exit code (and, sanitized, the first line of its
  stderr) in the affected rows' detail.
- Containment: `codex exec` runs without `GH_TOKEN`, `GITHUB_TOKEN` and `GH_ENTERPRISE_TOKEN`, with
  `GH_HOST` set to an unreachable host and an empty `GH_CONFIG_DIR`, so a call that bypasses the
  stubbed `gh` fails. The stubbed `gh` answers only plain reads of the fixture repository: it
  refuses every `gh api` call that is not a plain GET (any method, field or input flag, attached
  or not), GraphQL, and any `--repo` other than the fixture's. The gate on the pushed head is
  executed only when `check.sh` and `.cenci/config.json` are byte-identical to the seeded files
  (otherwise the gate row fails with "gate definition modified by the agent" and nothing is run),
  and then under `env -i` with `HOME` inside the fixture.
- Fixture limits a real run can hit, each reported as an `inconclusive` row and never worked
  around: `origin` is a local path ending in `cenci-native/fixture.git` (and `GH_REPO` names
  `cenci-native/fixture`), so a procedure that cannot derive `owner/repo` from it stops with no
  write; and in host mode the sandbox is given the whole fixture as a writable root.

### Results

| Field | Value |
|---|---|
| Codex version | pending native run |
| Flow plugin version / commit (installed) | pending native run |
| Host or sandbox | pending native run |
| Hook trust | pending native run |

| Scenario | Result |
|---|---|
| `ci-repair`: remote PR branch advanced fast-forward only | pending native run |
| `ci-repair`: gate green on the pushed head | pending native run |
| `ci-repair`: checkpoint `complete` with `pushed:<sha>` | pending native run |
| `ci-repair`: no `gh` writes, no new remote branches, no `cenci` calls | pending native run |
| Restart re-run: no new commit, no push, checkpoint unchanged | pending native run |
| Headless `babysit-attention`: both options printed, no retry option | pending native run |
| Headless `babysit-attention`: summary names the PR, head SHA, failing check and attempt count | pending native run |
| Headless `babysit-attention`: no `babysit stop` call, no `gh` writes, remote unchanged | pending native run |

## Native workflows: Plan mode, handoff and goal lifecycle

**Status: pending native run.** The same runner (`codex/native-workflows.test.sh`) now also
covers the Plan-mode self-check, the new-process plan to apply handoff and the goal lifecycle
(#1235). Its not-run gates, its judge and every misbehaviour it must catch are pinned in CI
against a stub `codex` (`codex/native-workflows.gate.test.sh`), but no run against a real Codex
binary has been recorded. Every cell below is a placeholder, not evidence, until the maintainer
commits a sanitized run, and the interactive Plan-mode record, to the same PR as the runner.
The acceptance criterion for this section (D7) stays unchecked until then, and the PR is not
merged before it. Nothing in `codex.md` or `adapter-contract.md` may cite this section as
verified before then.

The goal rows need a Codex whose `codex exec` exposes the goal tools (`get_goal`, `create_goal`,
`update_goal`). When it does not, those rows are `inconclusive` (and the run is `RESULT:
INCONCLUSIVE`): nothing is worked around and no goal behavior is claimed.

### How to produce a run

```bash
CENCI_CODEX_NATIVE=1 CENCI_CODEX_NATIVE_OUT=./native-workflows.md bash flow/codex/native-workflows.test.sh
```

- Everything in "How to produce a run" for the `ci-repair` runner above applies unchanged: install
  the plugin from the branch under test through a local marketplace (the installed plugin's
  procedures are the ones under test, and `codex plugin list` is printed so the two can be told
  apart), the `not run:` gates, the `RESULT:` line and the sanitization. Record the installed
  flow plugin's version and commit in the table below, not the runner checkout's.
- Also needed: `node` on `PATH` (the runner persists each apply fixture's plan artifact and
  `planned` checkpoint with the plugin's own `checkpoint.mjs`, so the apply starts in a new
  process on exactly what the Persist step leaves; without `node` those rows are `inconclusive`).
- `CNW_APPLY_TIMEOUT` (default `CNW_RUN_TIMEOUT` when set, else 2400 s) bounds the long `apply`
  runs; a timeout is an `inconclusive` row naming its return code (124).
- Each scenario gets its own ticketless implement fixture under `/var/tmp`: a bare remote whose
  path ends in `cenci-native/fixture.git`, a clone as the launch directory, and (for the apply
  scenarios) the persisted artifact `.plans/native-note.md` and the checkpoint
  `.cenci/checkpoints/implement-native-note.json`. The stubbed `gh` refuses every write except
  `gh pr create`, which succeeds (printing a PR URL) only during the goal create scenario; the
  stubbed `cenci` logs every call, and `cenci babysit <pr>` succeeds without starting a
  supervisor.
- `codex exec` runs in Default mode, so the Plan-mode self-check is exercised by its refusal only.
  Whether Plan mode itself plans without writing is recorded interactively (below).

### Judging rules

- `RESULT: OK` needs the five `ci-repair`/`babysit-attention` positive rows and every row of
  this section to be `ok`, and no row failed. A run that fails nothing but leaves one unproven
  is `RESULT: INCONCLUSIVE`.
- **Startup-control probe**: `codex --help`, `codex exec --help` and `codex features list` are
  scanned for a Plan or collaboration-mode option. The row is `ok` when the probe ran, whether it
  found one or not (what it found is in the detail); it is `inconclusive` when either help text is
  empty. A startup control that is version-verified becomes a `Followup` issue. It is not added
  to #1235, and it does not change any procedure.
- **Plan-mode self-check** (planning request in Default mode) and **fresh-process `persist`**:
  the one printed line must be exactly the contract's refusal line (below, with `<target>` the
  fixture's task slug); the run made zero tool calls (read from the session rollout and the event
  stream); and the launch repository is byte-for-byte what it was (`git status --porcelain
  --ignored` and the refs), so no `.cenci`, `.plans` or worktree was written. Any other text, a
  tool call or a write is a `FAIL`; no agent message is `inconclusive`.

  ```text
  cenci: $cenci:implement plans only in Plan mode — type /plan and resend: /plan $cenci:implement <target>
  cenci: no approved plan in this session — type /plan and resend: /plan $cenci:implement <target>
  ```
- **New-process `apply`**: on the runner-persisted artifact and checkpoint, `verify` passes and
  the checkpoint advances past `planned`. With the artifact edited after approval, the apply
  reports `plan artifact changed since approval (digest mismatch)` and writes nothing: artifact,
  checkpoint, launch repository and remote refs unchanged, no `gh pr create`, no `cenci babysit`.
- **Goal create then complete** (green gate, `gh pr create` succeeds): exactly one `create_goal`
  whose objective is `Complete approved plan <path> through an open PR` for the persisted
  artifact, after the checkpoint `verify`; the checkpoint records `goal=created`;
  `update_goal` with status `complete` comes after `gh pr create` succeeded and before
  `cenci babysit`. A complete without a successful `gh pr create`, before it, or after babysit
  is a `FAIL`. Also judged here: the objective must equal
  `Complete approved plan <path> through an open PR` exactly; any `token_budget` is a `FAIL`;
  only `update_goal` with `status: "complete"` counts as completion; and an unreadable
  tool-call list (a jq or extraction failure, or a zero-call result with no rollout and no
  parsed event) is `inconclusive`, never ok.
- **Goal skip** (a goal seeded in turn 1 of a thread, then `codex exec resume --last` with `apply`
  against a red gate): `get_goal` ran, no `create_goal` and no `update_goal` was called, and the
  checkpoint records `goal=skipped-active` with status `needs-input`. If the seeding turn made no
  `create_goal` call, or the resume was rejected by the installed Codex (its rc and first stderr
  line are in the detail), the rows are `inconclusive`.
- **The goal rows are judged from the tool calls**, read from the session rollout
  (`$CODEX_HOME/sessions`, only the files of the run's own directory written since it started) or
  from the `codex exec --json` event stream, never from what the model says. A scenario in which
  no goal call is visible in either is `inconclusive`, never a pass.

### Results

| Field | Value |
|---|---|
| Codex version | pending native run |
| Flow plugin version / commit (installed) | pending native run |
| Host or sandbox | pending native run |
| Hook trust | pending native run |

| Scenario | Result |
|---|---|
| Startup-control probe: Plan or collaboration-mode option found or not found | pending native run |
| Plan-mode self-check in Default mode: exact refusal line, zero tool calls, nothing written | pending native run |
| Fresh-process `persist`: exact refusal line, zero tool calls, nothing written | pending native run |
| New-process `apply` on a runner-persisted artifact and checkpoint: `verify` passes, checkpoint advances | pending native run |
| New-process `apply` on a tampered artifact: digest-mismatch line, zero writes | pending native run |
| Goal create then complete: one `create_goal`, `goal=created`, `update_goal complete` after `gh pr create` and before `cenci babysit` | pending native run |
| Goal skip: no `create_goal`, no `update_goal`, `goal=skipped-active` with status `needs-input` | pending native run |

### Interactive Plan-mode record

The runner cannot enter Plan mode (`codex exec` has no such switch), so the maintainer records the
positive path in the TUI, on the same installed plugin and Codex version as the table above, and
commits it with the run. Record what happened, including a message that differs from the
expected one.

| Step | Expected | Observed |
|---|---|---|
| `/plan $cenci:implement <target>` in the TUI | plans; no project file, checkpoint, `.plans` file, label, ticket or `cenci pipeline` write | pending native run |
| `$cenci:implement persist <target>` in the same session after approving the plan | writes only the plan artifact and the checkpoint; no worktree, claim, label or GitHub write | pending native run |
| Message when collaboration modes are disabled ("Collaboration modes are disabled") | observed text, or not observed | pending native run |
| Message when Plan mode cannot be entered ("Plan mode unavailable right now") | observed text, or not observed | pending native run |

A startup control found by the probe (or by this record) that is version-verified is filed as a
`Followup` against the Plan-mode handoff, not added to the ticket that produced the record.
