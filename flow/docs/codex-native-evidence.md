# Codex native hook evidence

What a real, authenticated Codex does with cenci's `PreToolUse` guards, recorded from the
opt-in runner `codex/native-hooks.test.sh` (#1212). Every other suite in `flow/codex/` runs
the guards from a shell script; this record is the only place the behavior of the native
binary is written down.

**Status: native hook runs recorded on 2026-11-04.** Host and sandbox runs used Codex
0.166.0 and runner commit `3d7a9be2f38df46577ee70e9bac7dbd12a32cc51`. Both returned
`RESULT: OK`; the four guard-canary probes passed in both environments, and the sandbox
trust-withheld variant refused and cleaned up all four written probes. The initial full
runs each retained one inconclusive relative-MOVE classification, subsequently resolved
by focused native reruns of the repaired classifier in both environments, described below; the
host's disposable project-local launch-failure hooks were not approved and were not run.
These limits do not turn skipped or inconclusive rows into passes. The sections "Native workflows:
`ci-repair` and `babysit-attention`" and "Native workflows: Plan mode, handoff and goal
lifecycle", record a different runner (`codex/native-workflows.test.sh`, #1236 and #1235)
and carry their own status lines, as does the last section, "Native agent roles: inherit,
override, unavailable model" (`codex/native-agent-roles.test.sh`, #1191).

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
  is absent or older than 0.166.0, or `codex login status` fails.
- A run that gets past the gates ends with one `RESULT:` line. `RESULT: OK` (exit 0) means at
  least one scenario row proved something and the canary showed the hooks active;
  `RESULT: INCONCLUSIVE` (exit 3) means nothing was proven (no `ok` scenario row, hooks
  inactive, or the canary could not tell); `RESULT: FAIL` (exit 1) means a row failed. While
  the canary has not shown the hooks active, no allow or deny row is run, because an allowed
  write proves nothing when no hook is watching.
- **Guard canary preflight (#1213):** the same run also executes `codex/guard-canary.sh`
  end to end under a `### Guard canary preflight` report section: `plan`, one `codex exec` per
  probe from a feature-worktree session, then `check`. The "as launched" variant runs on the
  host (persisted `/hooks` trust) and in the sandbox (with the trust-bypass flag); the "trust
  withheld" variant runs in the sandbox only, with just `--dangerously-bypass-hook-trust` left
  out inside the disposable container, and the host records it as `not run` because cenci never
  changes persisted trust. A run FAILs when the preflight result contradicts the trust state
  the deny-matrix canary established. Copy the sanitized `### Guard canary preflight` section,
  including each variant's `CANARY_STATUS`, refusal text and `REMOVED:`/`LEFTOVER:` lines,
  into the "Guard canary preflight" section below.
- Pass or fail is judged from the filesystem after each call, the `codex exec --json`
  tool-call events and the guards' capture tap (`CENCI_CODEX_HOOK_CAPTURE_DIR`). Codex
  0.166.0 can omit a blocked call from those events; the runner also accepts its native
  router denial on stderr when the command or patch matches the probe exactly, the expected
  tool has a capture tap, and Codex exits successfully. The guard's own denial message must
  appear in the diagnostic body. Unrelated stderr and model narration are not evidence.
  A row whose call was not proven to have been attempted is `inconclusive`, never a success.
- Home paths, user and host names, fixture roots, session IDs and token-shaped strings are
  replaced by `<HOME>`, `<USER>`, `<HOST>`, `<FIXTURE>`, `<UUID>` and `<TOKEN>` before
  anything is committed. Git commit SHAs are kept.

## Runs

| Field | Host run | Sandbox run (`cn xt`) |
|---|---|---|
| Codex version | 0.166.0 | 0.166.0 |
| Plugin version / commit | 1.31.0 / `3d7a9be2f38df46577ee70e9bac7dbd12a32cc51` | 1.31.0 / `3d7a9be2f38df46577ee70e9bac7dbd12a32cc51` |
| Host or sandbox | host | sandbox |
| Trust state | active; persisted `/hooks` approval, no bypass flag | hook trust bypassed by launcher (explicit flag) (flag passed by the runner's `codex exec` inside the sandbox) |
| `$SHELL` | `/bin/zsh` | `/bin/bash` |
| Full-run result | OK: 21 scenario rows passed, 1 inconclusive, 2 not run | OK: 24 scenario rows passed, 1 inconclusive, 0 not run |

The host plugin was initially installed from the PR checkout through a local marketplace;
its full-run plugin listing subsequently showed the Git marketplace registration. Installed
`codex/hooks.json` and both guard scripts were hash-checked against the tested checkout and
matched. The runner and canary helper were executed from the PR checkout. The sandbox
plugin was installed from its local clone of that commit. The sandbox was
created through `cenci open --agent codex --shell --name native-1362` with a fresh home volume,
the same Codex sandbox environment used by `cn xt`; the runner used a clone of the tested
commit inside the container so its Git paths were valid there.

## Exit 127

Hypotheses carried from #1041: the interpreter missing from the login-shell PATH, an unset
or empty `PLUGIN_ROOT`, a missing script, a non-executable script, `bash` or `jq` or `awk`
missing from the login-shell PATH, and fish parsing `${PLUGIN_ROOT}`.

| Item | Host run | Sandbox run |
|---|---|---|
| Resolved plugin root | `<HOME>/.codex/plugins/cache/cenci/cenci/1.31.0` | `<HOME>/.codex/plugins/cache/cenci/cenci/1.31.0` |
| Script present and executable (every `hooks.json` handler) | yes, checkout and installed copy | yes, checkout and installed copy |
| `$SHELL -lc 'command -v bash jq awk'` | `/usr/bin/bash`, `/usr/bin/jq`, `/usr/bin/awk` | `/usr/bin/bash`, `/usr/bin/jq`, `/usr/bin/awk` |
| Each guard rerun the way Codex launches it | allow exits 0; denial exits 2 with guard message | allow exits 0; denial exits 2 with guard message |

Result: exit 127 was not reproduced in either preflight (27 host handler executions across
three discovered roots, and 18 sandbox executions across two roots).
This does not establish that the original launch failure is fixed. The separate intentional
launch-failure experiment below records what happens when a hook cannot start.

## Deny matrix

Each deny row is paired with a positive control whose allowed write must land.

| Scenario | Host run | Sandbox run |
|---|---|---|
| Bash to a sensitive `.env` (check-sensitive-files) | denied | denied |
| Bash to a main-worktree file (guard-main-worktree) | denied | denied |
| Bash to a worktree file (positive control) | written, hook tap present | written, hook tap present |
| apply_patch to a sensitive `.env` (check-sensitive-files) | denied | denied |
| apply_patch to a main-worktree file (guard-main-worktree) | denied | denied |
| apply_patch to a worktree file (positive control) | written, hook tap present | written, hook tap present |

## Guard canary preflight

What `codex/guard-canary.sh` does against a real Codex (#1213): the mutating Codex workflows
refuse to start unless every (matcher group, guard) pair denies its own probe. These results
come from the authenticated native runs above, separately from the deterministic suite
`codex/guard-canary.test.sh` and the stub cases in `codex/native-hooks.gate.test.sh`.

| Variant | Host run | Sandbox run (`cn xt`) |
|---|---|---|
| As launched: trust state | active, persisted `/hooks` approval | active, hook trust bypassed by launcher (explicit flag) |
| As launched: `CANARY_STATUS` and per-probe results (4 probes) | `active`; probes 1–4 denied by their own guards | `active`; probes 1–4 denied by their own guards |
| As launched: refusal text when hooks are pending (names pending trust and `/hooks`) | not applicable: approved hooks were active | not applicable: launcher supplied trust bypass |
| Trust withheld (flag left out, hooks pending): `CANARY_STATUS` and refusal text | not run on the host (cenci never changes persisted trust) | `refused`, helper exit 1; all four probes attempted and written; refusal names pending/untrusted hooks and `/hooks` |
| Cleanup: `REMOVED:` targets and any `LEFTOVER:` path | 0 written, 0 removed, no leftovers | as launched: 0 written, 0 removed; withheld: 4 removed; no leftovers in either variant |

The two `apply_patch` probes reached the `Write|Edit` matcher and were denied. Both native
as-launched runs produced these helper results:

```text
PROBE_RESULT: 1 (apply_patch, check-sensitive-files, group Write|Edit): denied by check-sensitive-files
PROBE_RESULT: 2 (apply_patch, guard-main-worktree, group Write|Edit): denied by guard-main-worktree
PROBE_RESULT: 3 (shell, check-sensitive-files, group Bash): denied by check-sensitive-files
PROBE_RESULT: 4 (shell, guard-main-worktree, group Bash): denied by guard-main-worktree
CANARY_STATUS=active
```

The sandbox trust-withheld run recorded the following cleanup and refusal. There were no
`LEFTOVER:` lines. Host trust was never changed by the runner.

```text
REMOVED: /tmp/cenci-guard-canary-47ab22f4f1ff7937/write-edit-1.env
REMOVED: <FIXTURE>/repo/cenci-guard-canary-47ab22f4f1ff7937-2.txt
REMOVED: /tmp/cenci-guard-canary-47ab22f4f1ff7937/bash-3.env
REMOVED: <FIXTURE>/repo/cenci-guard-canary-47ab22f4f1ff7937-4.txt
CANARY_STATUS=refused
GUARD CANARY REFUSED: the cenci write guards are not confirmed active, so protection is inactive and no mutation may start.
Likely cause: the cenci hooks are pending or untrusted in Codex, and Codex skips pending hooks. Review and enable them with /hooks, then rerun this workflow.
```

## Payload cwd, hook pwd and effective cwd

| Way | Payload cwd | Hook pwd | Effective cwd | `workdir` in `tool_input` |
|---|---|---|---|---|
| Direct apply_patch | `<FIXTURE>/repo/.worktrees/1-native` | `<FIXTURE>/repo/.worktrees/1-native` | `<FIXTURE>/repo/.worktrees/1-native` | false |
| exec_command with a per-call `workdir` | `<FIXTURE>/repo` | `<FIXTURE>/repo/.worktrees/1-native` | `<FIXTURE>/repo` | false |
| Nested code mode | `<FIXTURE>/repo` | `<FIXTURE>/repo/.worktrees/1-native` | `<FIXTURE>/repo` | false |

Both environments produced these values. The effective workdir can differ from the hook
process cwd; in these probes the payload cwd followed the effective workdir. The nested row
requested `features.code_mode=true` and observed a capture tap and the expected file.

## Relative apply_patch (Add, Update, Delete, Move)

| Scenario | Host run | Sandbox run |
|---|---|---|
| Root session, relative target resolving to the main worktree (denied) | Add/Update/Delete denied; MOVE denied in focused rerun | Add/Update/Delete denied; MOVE denied in focused rerun |
| Worktree session, relative target inside the worktree (allowed) | Add/Update/Delete/Move written | Add/Update/Delete/Move written |
| Root session, relative target under `.worktrees/` (allowed) | written | written |
| Bypass shape: worktree session, root `workdir`, relative `AGENTS.md` | denied | denied |

The initial MOVE row was a runner classification gap: the native router reported a denial
at `rel-move-a.txt`, while the runner searched the error body for destination `rel-move-b.txt`.
The source remained and no destination was written. The full-run counts above retain that
inconclusive classification rather than retroactively reporting a pass.

After the classifier was changed to correlate the destination with the exact denied patch
and the guard message only with the error body, focused native MOVE reruns passed on both
host and sandbox. Each used the production `cnh_router_block` parser with SHA-256
`7dbac353eafca53c323afeaa5fe7c83af68f74d67d65669dd80034b58a0e1078`, a fresh private
`/var/tmp` fixture, and the same relative source/destination patch. Both Codex and the
parser exited 0; an `apply_patch` capture tap was present; the error named only the source;
the complete patch matched exactly; source bytes were unchanged and the destination was
absent. The regression suite separately reproduced this gap (269 passed / 1 failed) and
passed after the fix (270 passed / 0 failed).

## Hook launch failure

In the sandbox, the marker control ran and its write landed. With an intentionally missing
script (exit 127), and with an intentionally non-executable script (exit 126), the broken hook
was invoked and the tool **continued**, writing its target. Hook launch failure therefore
does not itself enforce a denial. On the host, the disposable project-local hooks were not
approved; that experiment was not run and supplies no host conclusion.

## Resolution conclusion

The canary confirmed each configured guard/matcher pair on host and sandbox, and refused
when sandbox hook trust was withheld. Relative Add/Update/Delete isolation and the explicit
workdir bypass check passed; allowed worktree operations landed. Focused host and sandbox
reruns resolved the MOVE reporting gap. The guard scripts were unchanged. Intentional exit 126/127 hook failures
continued in the sandbox, reinforcing why the preflight must test actual denials instead of
assuming that a configured hook is active.

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
and a stubbed `cenci` that logs every call (`babysit status` answers with the attempt count as
one JSON line, `babysit stop` fails like a host nack, `not stopped: <reason>`, and `babysit reset`
fails like the host-only refusal a sandbox gets, `not reset: ...`). The remote's `pre-receive` hook logs every ref update, so a force push or a stray
branch shows up even when the remote rejects it.

The restart scenarios (#1454) need two more things from that fixture. The stubbed `gh` answers
`gh pr checks` from a check file the runner arms per scenario (`pending` only, it exits 8 with valid
JSON as the real one does; an unarmed `gh` keeps the failing `native-gate`) and `gh run view` from a
log file the runner arms with it. And the fixture seeds a second failure outside `gateCommand`:
`lint.sh` (check `native-lint`) fails until `CHANGELOG.md` holds the line `- native fixture entry`,
so a repair of a head whose gate is already green can differ from the first repair. After the first
repair the runner arms `pending` checks for the duplicate invocation, a failing `native-lint` for the
failed pushed head, and `pending` checks again for the interrupted push, whose `committed:<sha>`
checkpoint it writes with the plugin's own `checkpoint.mjs` (`clear`, `init`, `advance`), so that
row is `inconclusive` without `node`. The check answers are reset after each scenario.

### How to produce a run

```bash
CENCI_CODEX_NATIVE=1 CENCI_CODEX_NATIVE_OUT=./native-workflows.md bash flow/codex/native-workflows.test.sh
```

- Install the plugin from the branch under test through a local marketplace first (the same
  procedure as the hook runner's host run), and trust it with `/hooks` when running on the host.
  The procedures under test are the installed plugin's, not the runner checkout's: the report
  prints `codex plugin list` so the two can be told apart. Record the installed flow plugin's
  version and commit in the table below, not the runner checkout's.
- Trust, the `not run:` gates (`CENCI_CODEX_NATIVE` unset, Codex absent or older than 0.166.0,
  `codex login status` failing), the `RESULT:` line (`OK` exit 0, `INCONCLUSIVE` exit 3, `FAIL`
  exit 1) and the sanitization (`<HOME>`, `<USER>`, `<HOST>`, `<FIXTURE>`, `<UUID>`, `<TOKEN>`,
  commit SHAs kept) are the same as for the hook runner above. The runner refuses to print a
  report in which a home path, user name or host name survived sanitization.
- `RESULT: OK` needs the seven positive rows to be `ok` (fast-forward advance, gate green on the
  pushed head, checkpoint `complete` with the pushed SHA, restart adds no commit, the interrupted
  push is recorded with no new commit and no push, the failed pushed head is repaired with one
  fast-forward push of a new commit, all three attention options printed) and no failed row. A run
  that fails nothing but leaves one of them unproven (for example a `codex` that reads the PR and
  gives up) is `RESULT: INCONCLUSIVE`.
- The failed-pushed-head row is `not run` unless the remote head is the checkpoint's pushed SHA
  and that head still fails `native-lint` (a model that added the changelog line while repairing the
  first failure leaves no second failure). It is `ok` only when the new commit's parent is the
  pushed head, exactly one ref update was logged, none was a force, the checkpoint is `complete`
  with the new SHA, the seeded gate is green at the new head, `lint.sh` is unchanged and the
  changelog line is present. A no-op, or a block, after the PR was read is a `FAIL`.
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
| Restart (interrupted push): checkpoint completed with the pushed SHA, no new commit, no push | pending native run |
| Restart (failed pushed head): one fast-forward push of a new commit on the pushed head, checkpoint complete with it | pending native run |
| Headless `babysit-attention`: three options printed, retry option offered | pending native run |
| Headless `babysit-attention`: summary names the PR, head SHA, failing check and attempt count | pending native run |
| Headless `babysit-attention`: no `babysit stop` or `reset` call (a `babysit status` read is allowed), no `gh` writes, remote unchanged | pending native run |

## Native workflows: Plan mode, handoff and goal lifecycle

**Status: recorded, `RESULT: FAIL (5 failed rows)`.** The same runner (`codex/native-workflows.test.sh`)
also covers the Plan-mode self-check, the new-process plan to apply handoff and the goal lifecycle
(#1235). Its not-run gates, its judge and every misbehaviour it must catch are pinned in CI
against a stub `codex` (`codex/native-workflows.gate.test.sh`). The record below is the sanitized
native run on a real Codex binary (`codex-cli 0.171.0`, #1395), copied from the owner's #1395
comment and not judged again.

- **Counts**: the runner reported `ok=4 failed=5 inconclusive=21 not-run=3` across the run shared
  with #1396. All five failed rows are in this section.
  This section's 17 verbatim rows account for all 4 `ok` and all 5 `FAIL` results and 8 of the 21 `inconclusive` results; the remaining 13 `inconclusive` and 3 `not-run` results of that shared run are not committed in this section (they are not recorded here).
- **Exit code**: it was not captured, because another shell command ran before it could be read.
  `RESULT: FAIL` maps to exit 1 by the runner's contract; that is inferred from the runner, not
  observed.
- **Retention**: the first completed run is retained. Failures were not rerun away.
- **Still `FAIL`**: the Plan-mode self-check and the fresh-process `persist` printed extra output
  and made 2 tool calls before the refusal line ([#1584](https://github.com/matteobortolazzo/cenci/issues/1584)),
  and the tampered-artifact `apply` did not print the digest-mismatch line
  ([#1585](https://github.com/matteobortolazzo/cenci/issues/1585)).
- **Still `inconclusive`**: the tampered-artifact zero-writes row, goal create then complete, and
  goal skip (no goal tool calls were visible to `codex exec`).
- **Not observed**: the interactive `persist` positive path (the guard canary refused first) and
  both Codex refusal messages.
- **Acceptance**: this record does not tick D7 or D8 of #1193. The owner of #1193 judges them.
- **Citation rule**: other docs may cite only the rows below that read `ok`, or interactive rows
  whose Observed matches Expected, and must name a scenario's `FAIL` or `inconclusive` result in
  the same sentence as any of its `ok` sub-rows.

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

- `RESULT: OK` needs the seven `ci-repair`/`babysit-attention` positive rows and every row of
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
| Codex version | `codex-cli 0.171.0` |
| Flow plugin version / commit (installed) | `cenci@cenci` installed and enabled, `1.38.1` / `706da87ddbbcd32da6aa050b32eb9ed76f67ece1`; the commit was resolved from the marketplace source checkout and byte-compared with the installed `1.38.1` cache (its `flow/` directory: no differences). `codex plugin list` supplied the version and source but printed no commit. |
| Installed plugin source | `<HOME>/.codex/.tmp/marketplaces/cenci/flow` |
| Runner checkout commit | `706da87ddbbcd32da6aa050b32eb9ed76f67ece1` |
| Host or sandbox | host |
| Hook trust | all cenci hooks trusted through `/hooks`; no bypass flag (owner-confirmed for the installed version) |
| Exit code | not captured; `RESULT: FAIL` maps to exit 1 (inferred from the runner, not observed); another shell command ran before the status could be read |

| Scenario | Result |
|---|---|
| Startup-control probe: Plan or collaboration-mode option found or not found | `ok`: the probe found one candidate, `codex features list: collaboration_modes removed true`; no startup control was version-verified |
| Plan-mode self-check in Default mode: exact refusal line, zero tool calls, nothing written | `FAIL`: the refusal line was printed with other output, and the run made 2 tool calls before refusing; the nothing-written row is `ok` ([#1584](https://github.com/matteobortolazzo/cenci/issues/1584)) |
| Fresh-process `persist`: exact refusal line, zero tool calls, nothing written | `FAIL`: the refusal line was printed with other output, and the run made 2 tool calls before refusing; the nothing-written row is `ok` ([#1584](https://github.com/matteobortolazzo/cenci/issues/1584)) |
| New-process `apply` on a runner-persisted artifact and checkpoint: `verify` passes, checkpoint advances | `ok`: the runner-persisted checkpoint advanced to status=needs-input, phase=tests, goal=none (codex rc=0) |
| New-process `apply` on a tampered artifact: digest-mismatch line, zero writes | `FAIL`: the apply did not report the digest mismatch; the zero-writes row is `inconclusive`: nothing changed, but nothing shows the apply refused ([#1585](https://github.com/matteobortolazzo/cenci/issues/1585)) |
| Goal create then complete: one `create_goal`, `goal=created`, `update_goal complete` after `gh pr create` and before `cenci babysit` | `inconclusive`: no goal events (no `get_goal`, `create_goal` or `update_goal` call in the 1 rollout file or the event stream); the goal tools may not be exposed by `codex exec` at this version, or the rollout could not be read; the checkpoint records goal=none |
| Goal skip: no `create_goal`, no `update_goal`, `goal=skipped-active` with status `needs-input` | `inconclusive`: no `create_goal` call was seen in the seeding turn (codex rc=0), so no goal exists to skip |

#### Verbatim runner rows

The runner's own `scenario | result | detail` rows for this section, as copied from the #1395
comment (sanitized; details are truncated by the runner).

| scenario | result | detail |
|---|---|---|
| startup-control probe: Plan/collaboration-mode option recorded | ok | found a candidate Plan/collaboration-mode option: codex features list: collaboration_modes removed true . A version-verified startup control is filed as a Followup, not added to this ticket. Goal-related features listed: goals stable true . |
| plan-mode self-check: exact refusal line printed | FAIL | the refusal line was printed with other output after or before it: 'I’m using the cenci implement skill and will follow its native Codex planning procedure for `native-note`. cenci: $cenci:implement plans only in Plan mode — type /plan and resend: /plan $cenci:impleme' |
| plan-mode self-check: zero tool calls | FAIL | the run made 2 tool call(s) before refusing: exec text(await tools.exec_command({cmd:"cat <HOME>/.codex/plugins/cache/cenci/cenci;shell cat <HOME>/.codex/plugins/cache/cenci/cenci/1.38.1/skills/codex-runtime/SKILL.; |
| plan-mode self-check: nothing written | ok | git status --ignored and the refs are unchanged: no .cenci, .plans or worktree was written |
| persist (fresh process): exact refusal line printed | FAIL | the refusal line was printed with other output after or before it: 'I’m using the cenci implement skill and will follow its native Codex workflow for `persist native-note`. cenci: no approved plan in this session — type /plan and resend: /plan $cenci:implement native-' |
| persist (fresh process): zero tool calls | FAIL | the run made 2 tool call(s) before refusing: exec text(await tools.exec_command({cmd:"cat <HOME>/.codex/plugins/cache/cenci/cenci;shell cat <HOME>/.codex/plugins/cache/cenci/cenci/1.38.1/skills/codex-runtime/SKILL.; |
| persist (fresh process): nothing written | ok | git status --ignored and the refs are unchanged: no .cenci, .plans or worktree was written |
| apply (tampered artifact): digest-mismatch line printed | FAIL | the apply on a tampered artifact did not report the digest mismatch: 'I’m using the cenci implementation skill and will check its native Codex procedure for applying `native-note`. Apply stopped: `.plans/native-note.md` has changed since approval, so its digest no longe' |
| apply (tampered artifact): zero writes | inconclusive | nothing changed, but nothing shows the apply refused |
| apply (new process): verify passes and the checkpoint advances past planned | ok | new-process apply advanced the runner-persisted checkpoint: status=needs-input, phase=tests, goal=none (codex rc=0) |
| goal create: create_goal once, with the approved-plan objective | inconclusive | no goal events (no get_goal, create_goal or update_goal call in the 1 rollout file(s) or the event stream): the goal tools may not be exposed by codex exec at this version, or the rollout could not be read |
| goal create: checkpoint records goal=created | inconclusive | no goal events (no get_goal, create_goal or update_goal call in the 1 rollout file(s) or the event stream): the goal tools may not be exposed by codex exec at this version, or the rollout could not be read (the checkpoint records goal=none) |
| goal complete: update_goal complete only after a successful gh pr create | inconclusive | no goal events (no get_goal, create_goal or update_goal call in the 1 rollout file(s) or the event stream): the goal tools may not be exposed by codex exec at this version, or the rollout could not be read |
| goal complete: update_goal complete before cenci babysit | inconclusive | no goal events (no get_goal, create_goal or update_goal call in the 1 rollout file(s) or the event stream): the goal tools may not be exposed by codex exec at this version, or the rollout could not be read |
| goal skip: no create_goal while a goal exists | inconclusive | no goal events: no create_goal call was seen in the seeding turn (codex rc=0), so no goal exists to skip; the goal tools may not be exposed by codex exec at this version |
| goal skip: no update_goal | inconclusive | no goal events: no create_goal call was seen in the seeding turn (codex rc=0), so no goal exists to skip; the goal tools may not be exposed by codex exec at this version |
| goal skip: checkpoint records skipped-active and needs-input | inconclusive | no goal events: no create_goal call was seen in the seeding turn (codex rc=0), so no goal exists to skip; the goal tools may not be exposed by codex exec at this version |

### Interactive Plan-mode record

The runner cannot enter Plan mode (`codex exec` has no such switch), so the maintainer records the
positive path in the TUI, on the same installed plugin and Codex version as the table above, and
commits it with the run. Record what happened, including a message that differs from the
expected one.

Recorded with the same Codex and installed flow plugin versions as above, in a disposable
ticketless Git repository at `<FIXTURE>` with no remote configured.

Preparation note: the first planning attempt stopped because neither `.cenci/config.json` nor
`.claude/config.json` existed, and reported no files changed. The owner then seeded
`.cenci/config.json` with `{"gateCommand":"true"}` and committed it, before the measured planning
attempt in a fresh TUI session. These fixture setup writes are not attributed to `/plan` or
`persist`.

| Step | Expected | Observed |
|---|---|---|
| `/plan $cenci:implement <target>` in the TUI | plans; no project file, checkpoint, `.plans` file, label, ticket or `cenci pipeline` write | Plan mode planned: it produced the plan "Add README evidence" (target slug `add-readme-native-plan-mode`) for the requested root `README.md` text. Before approval `git status --porcelain --ignored` was empty, HEAD stayed at the seed commit and the worktree list held only the original checkout: no project file, checkpoint, `.plans`, worktree, label, ticket, GitHub or `cenci pipeline` write (the owner confirmed no label, ticket, GitHub or `cenci pipeline` write). |
| `$cenci:implement persist <target>` in the same session after approving the plan | writes only the plan artifact and the checkpoint; no worktree, claim, label or GitHub write | refused at guard canary preflight (exit 1): the owner chose "Yes, implement this plan" to switch to Default mode, interrupted (a filesystem check showed no writes), then sent the explicit `persist` command in the same session. It ran require-runtime, read the runtime skill and config, and stopped at the guard canary. No plan artifact, checkpoint, project file or worktree was written, Git status stayed empty and HEAD stayed at the seed commit; the owner confirmed no claim, label, ticket, GitHub or `cenci pipeline` write. Result: positive path not exercised (`apply` was not run). Refusal text below. |
| Message when collaboration modes are disabled ("Collaboration modes are disabled") | observed text, or not observed | not observed in the supplied interactive record |
| Message when Plan mode cannot be entered ("Plan mode unavailable right now") | observed text, or not observed | not observed in the supplied interactive record; Plan mode was entered and produced a plan |

Interactive `persist` refusal, as reported by the TUI:

```text
CANARY_STATUS=refused
Guard canary refused: the probe self-check failed against the real guard scripts: probe 2 (apply_patch): guard-main-worktree should deny its own probe (exit 2 with 'targets the main worktree') but exited 0. This is expected when the repository is not configured for cenci, lives under /tmp, or a guard script is broken.
The cenci write guards are not confirmed active, so do not start any mutation.
If the hooks are pending or untrusted in Codex, review them with /hooks and rerun.
```

The fixture was under `/tmp`. That is a condition the diagnostic names; this record does not
establish a separate root-cause diagnosis. The refusal is retained as observed, without bypassing
the guard or retrying it away.

A startup control found by the probe (or by this record) that is version-verified is filed as a
`Followup` against the Plan-mode handoff, not added to the ticket that produced the record. Here
the probe's only candidate (`codex features list: collaboration_modes removed true`) is not a version-verified startup control, so no `Followup` is filed.

## Native agent roles: inherit, override, unavailable model

**Status: pending native run.** The runner `codex/native-agent-roles.test.sh` (#1191) exists, but
no run against a real Codex binary has been recorded. Every result cell below is a placeholder, not
evidence, until the maintainer commits a sanitized run to the same PR as the runner. The #1191
acceptance criterion for this section stays unchecked, and the PR is not merged, until then.
Nothing in `codex.md` may cite this section as verified before then.

The runner builds a fixture under `/var/tmp` with `codex/install-agents.sh` itself, then runs
`codex exec --json` read-only and spawns one role per row. Resolved model and effort are read from
the session records (the `turn_context` of the parent thread and of the spawned role's own thread)
and failures from the event stream, never from the model's narration. A row whose evidence is not
visible is `inconclusive`; the runner never infers a value.

### How to produce a run

```bash
CENCI_CODEX_NATIVE=1 CENCI_CODEX_NATIVE_OVERRIDE_MODEL=<model your account can use> \
  CENCI_CODEX_NATIVE_OUT=./native-agent-roles.md bash flow/codex/native-agent-roles.test.sh
```

- `CENCI_CODEX_NATIVE=1` opts in. The runner prints `not run: <reason>` and exits 0 when it is
  unset, `codex`, `jq` or `git` is absent, Codex is older than 0.166.0, or `codex login status`
  fails.
- `CENCI_CODEX_NATIVE_OVERRIDE_MODEL` (`[A-Za-z0-9._-]+`) is the model for the override role; when it
  is unset the override row is `not run`. `CENCI_CODEX_NATIVE_OVERRIDE_EFFORT` (`[a-z]+`) is
  optional and needs the model.
- `CNAR_RUN_TIMEOUT` bounds each `codex` run in seconds (default 600).
- `CENCI_CODEX_NATIVE_OUT` also writes the sanitized report to that file.
- The run ends with one `RESULT:` line: `RESULT: OK` (exit 0) needs all three rows `ok`;
  `RESULT: INCONCLUSIVE` (exit 3) means nothing failed but a required row is not `ok`;
  `RESULT: FAIL` (exit 1) means a row failed. Home paths, user and host names, fixture roots,
  session IDs and token-shaped strings are replaced before anything is committed; git commit SHAs
  are kept.

### Judging rules

- **inherit**: the planner role is installed with no override and resolves the parent session's
  model and effort. A difference is a `FAIL`.
- **override**: the implementer role is installed with `--role-override` from the environment
  values and resolves exactly that model (and effort when given). A difference is a `FAIL`.
- **unavailable model**: the critical-reviewer role is installed with a deliberately unavailable
  model. `ok` needs an error Codex reports that names the model. No error with the role running on
  another model is a `FAIL` (silent substitution); an error that does not name the model, or no
  evidence, is `inconclusive`.

### Results

| Field | Value |
|---|---|
| Codex version | pending native run |
| Flow plugin commit | pending native run |
| Host or sandbox | pending native run |

| Scenario | Result |
|---|---|
| `inherit (planner)`: spawned role resolves the parent's model and effort | pending native run |
| `override (implementer)`: spawned role resolves the overridden model and effort | pending native run |
| `unavailable model (critical-reviewer)`: Codex reports it and nothing is substituted | pending native run |
