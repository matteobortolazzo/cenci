# Codex native hook evidence

What a real, authenticated Codex does with cenci's `PreToolUse` guards, recorded from the
opt-in runner `codex/native-hooks.test.sh` (#1212). Every other suite in `flow/codex/` runs
the guards from a shell script; this record is the only place the behavior of the native
binary is written down.

**Status: skeleton (the hook sections, from `## How to produce a run` through
`## Resolution conclusion`).** Nothing in those sections has been observed yet. Each is a
placeholder that the maintainer's native runs replace with sanitized runner output. A
placeholder is never evidence, and no conclusion in `codex.md` or `adapter-contract.md` may
cite this file until its section is filled in. The last section, "Native workflows:
`ci-repair` and `babysit-attention`", records a different runner
(`codex/native-workflows.test.sh`, #1236) and carries its own status line.

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
