# Codex native hook evidence

What a real, authenticated Codex does with cenci's `PreToolUse` guards, recorded from the
opt-in runner `codex/native-hooks.test.sh` (#1212). Every other suite in `flow/codex/` runs
the guards from a shell script; this record is the only place the behavior of the native
binary is written down.

**Status: skeleton.** Nothing below has been observed yet. Each section is a placeholder
that the maintainer's native runs replace with sanitized runner output. A placeholder is
never evidence, and no conclusion in `codex.md` or `adapter-contract.md` may cite this file
until its section is filled in.

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

## Guard canary preflight

What `codex/guard-canary.sh` does against a real Codex (#1213): the mutating Codex workflows
refuse to start unless every (matcher group, guard) pair denies its own probe. Each cell below
is `pending maintainer run` until the maintainer commits sanitized runner output; the
deterministic suite `codex/guard-canary.test.sh` and the stub cases in
`codex/native-hooks.gate.test.sh` prove the helper and the runner scenario, not the native binary.

| Variant | Host run | Sandbox run (`cn xt`) |
|---|---|---|
| As launched: trust state | pending maintainer run | pending maintainer run (hook trust bypassed by launcher (explicit flag)) |
| As launched: `CANARY_STATUS` and per-probe results (4 probes) | pending maintainer run | pending maintainer run |
| As launched: refusal text when hooks are pending (names pending trust and `/hooks`) | pending maintainer run | pending maintainer run |
| Trust withheld (flag left out, hooks pending): `CANARY_STATUS` and refusal text | not run on the host (cenci never changes persisted trust) | pending maintainer run |
| Cleanup: `REMOVED:` targets and any `LEFTOVER:` path | pending maintainer run | pending maintainer run |

Open question this section settles: whether Codex sends `apply_patch` through the `Write|Edit`
matcher, since the `apply_patch` probes depend on it. Pending maintainer run; if it does not,
every Codex mutating workflow refuses (fail closed) and the refusal names the failing pair.

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
