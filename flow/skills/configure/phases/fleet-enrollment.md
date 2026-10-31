# Fleet Enrollment and Fleet Autonomy Controls

Read this file when `dispatchEnroll`, `dispatch.planRefined`, `automerge.enabled` or `planning.attended` is asked this run, and on every host run (see `SKILL.md` `### Step Map`). It holds `### Fleet Dispatch Enrollment` (questions 11 and 12) and `### Fleet Autonomy Controls` (questions 18 to 20), the user-level fleet choices.

### Fleet Dispatch Enrollment

**There is no install/opt-in gate on this section**, matching `### Board Config (lazyboards)` in `phases/lazyboards.md`. Its side effect fires immediately at
question time; nothing here is deferred to the generation steps in `SKILL.md`.

**Container guard (runs first).** Use `detection.inContainer` (already resolved by `### Scripted Detection` in `phases/detection-and-delta.md`).
When it is `true`, this section asks nothing, runs no `cenci dispatch` command, and writes nothing anywhere
— the sandbox mounts `/home/dev` as a per-repo named volume, so
the container's `~/.config/cenci/config.json` is not the host file the dispatch daemon
reads, and an in-container enroll would be a silently dead fleet config. Emit an
informational message naming that reason and the host-side fix: "Fleet dispatch
enrollment can't run inside the sandbox — its `~/.config/cenci/config.json` lives in
the container's `/home/dev` volume, not the file the host dispatch daemon reads. To
enroll, run `cenci dispatch enroll --session <name>` from the repo root on the host."
Never claim the repo was enrolled, and never fabricate a host path for `--dir` — the
workspace is bind-mounted at a container-local path that has no reliable host
equivalent, so the advisory omits `--dir` entirely and lets the host-side command
resolve its own working directory. Skip the remainder of this section
(main-checkout resolution, status probe, questions 11/12) and continue directly to
the next section (`### Autonomy Settings` in `phases/autonomy-settings.md`).

**Main-checkout resolution (host runs only).** Run
`git rev-parse --path-format=absolute --git-common-dir` as its own Bash call (already
covered by the existing `Bash(git rev-parse:*)` grant) and strip the trailing `/.git` from its output to get
`<main-checkout>`. If that call fails, log one informational line noting the
fallback, then use the repository root the Scripted Detection step ran from.
`<main-checkout>` must always be used below — never
`<worktree-path>` (see `### Create Worktree` in `SKILL.md`) and never a bare default: `cenci dispatch
enroll` defaults its directory to the current directory, which inside a configure run is
the transient `.worktrees/configure-<slug>` checkout, not the repo.

**Main-checkout path validation (shell safety only).** If the resolved
`<main-checkout>` value contains a single quote or a newline, log one informational
line and skip the rest of this section — same skip-to-`### Autonomy Settings` in `phases/autonomy-settings.md`
behavior as the container guard above — rather than emit a broken command. This
mirrors the session-name rule in question 12 below; it is shell-safety only, not
semantic path validation.

**Status probe.** Run `cenci dispatch status --json --dir '<main-checkout>'` as its own
Bash call (per `cenci:shell-rules`, never compounded with the resolution call above) and
parse its stdout as JSON.

- If the command exits non-zero, or its stdout is not parseable JSON, log one
  informational line and fall through — this never blocks or aborts the configure run.
- If the parsed object has no `session` key, the installed `cenci` binary predates
  #933's `status --json` support. Ask nothing, run no `enroll`, and
  advise the user to update cenci before enrolling (flow and watch version independently — see root
  `AGENTS.md`).

**Three-way branch** on the parsed object's `enrolled`/`session` fields:

- `enrolled: false` → ask question 11 below when `dispatchEnroll` is in `delta.ask`
  (always on an `--all` or `full` run); when it is carried, ask nothing and log one
  informational line.
- `enrolled: true, session: ""` → the repo is already enrolled but has no session yet —
  the state lazyboards' board-driven enrollment leaves behind (see the "Fleet dispatch
  from the board" section of `docs/orchestration.md`), so this is the common repair
  path, not an edge case. Skip question 11 and ask only question 12. The `enrolled: true, session: ""` repair (question 12) is still asked on a delta run, whatever the ledger says: it repairs a half-enrolled state rather than re-offering a declined question, and it has no ID and is never recorded.
- `enrolled: true, session: "<set>"` → skip both questions, log the resolved session
  quietly, and fall through.

In the session-set branch, re-running `/cenci:configure` on an already-enrolled repo never
re-prompts and never rewrites the config. If the enrolled `dir` reported by `status --json`
differs from the resolved `<main-checkout>`, log the mismatch as an informational line
only — never re-enroll to correct it; the no-rewrite rule wins.

Question 11 is recorded as offered (`dispatchEnroll` in `configureOffered`) only when the
`enrolled: false` branch actually put it to the user; the container guard, the
path-validation skip, a failed status probe, and the other two branches record nothing.
Even then, `dispatchEnroll` is recorded only after a successful `cenci dispatch enroll`
or an explicit "No — skip": when the answer was "Yes" and the enroll call fails, do not
record it, report the failure (the exact error, plus that the next delta run asks question
11 again only when `dispatchEnroll` is not already in the existing ledger — otherwise
`/cenci:configure --all` re-offers it), and leave the rest of the run unchanged.

11. **Enroll in fleet dispatch**: ask via `AskUserQuestion`: "Enroll this repo in fleet
    dispatch? This lets `cenci dispatch` and the fleet dispatch loop pick up approved
    plans for this repo automatically. `dispatch.*` is a user-level, fleet-wide setting
    stored in `~/.config/cenci/config.json` — distinct from this repo's
    `.cenci/config.json` — and the effect is immediate, not part of this PR."
    - Options: "Yes — enroll this repo", "No — skip"
    - On "No", write nothing to `~/.config/cenci/config.json` and leave any existing
      block untouched. A decline of question 11 is recorded in `configureOffered`; a delta run does not re-ask it, and `--all` does. The ledger lives in this repo's `.cenci/config.json`, so the decline lands in this run's PR. There is no
      unenroll offer here.

12. **Fleet dispatch session name** (asked via `AskUserQuestion` when question 11 was
    answered "Yes", or directly when the branch above found `enrolled: true, session:
    ""`): "What tmux session should fleet dispatch use for this repo? Without one, this
    repo is skipped on every dispatch pass and the daemon reports
    `dispatch_session_unconfigured` (#927) — this is not optional." Collect the answer
    in a single `AskUserQuestion` free-text field only: no pre-filled value and no picker
    of running sessions. This section never guesses the session name from `$TMUX_PANE`,
    an attached client, the current pane, running tmux sessions, or the repo/directory
    name — guessing ambient state is the bug #927 exists to close. Reject the answer and
    re-ask when it is empty, whitespace-only, or contains a single quote or a newline
    (shell safety only; semantic validation of the name stays in `cenci dispatch
    enroll`).

    Once a valid session name is collected, issue a single combined write — both
    enrollment and session in one call, never two separate writes — as its own Bash
    call: `cenci dispatch enroll --dir '<main-checkout>' --session '<name>'`, with both
    `<main-checkout>` and `<name>` single-quoted. A non-zero exit means nothing was
    enrolled: do not add `dispatchEnroll` to the offered set, and report the failure.

**Scope.** `dispatch.*` lives in `~/.config/cenci/config.json` (or
`$XDG_CONFIG_HOME/cenci/config.json`) — user-level and fleet-wide, the same file
`automerge.enabled` reads from — but `repos[].session` and each repo's enrollment
are nonetheless per-repo entries inside that fleet-wide file. This section
never reads, modifies, or writes ~/.config/cenci/config.json itself: every mutation goes through
`cenci dispatch enroll`, which already owns the atomic, key-preserving write path
(`EnrollRepo`, `watch/internal/dispatch/enroll.go`), including creating the file and its
parent directory. The effect is immediate and user-level, so nothing lands in
`<worktree-path>` and no dispatch key is added to this repo's `.cenci/config.json` — the only
thing this section can contribute to the configure PR diff is the `dispatchEnroll` entry
in the `configureOffered` ledger (step 6).

**Next step.** After a successful enroll, point the user at `cenci dispatch loop on` as
the explicit next step to start the recurring dispatch loop — that command already
defaults its interval to `5m` when the user doesn't set one, so this section never asks
about the dispatch loop's enable flag or interval setting itself.

### Fleet Autonomy Controls

Questions 18 to 20 are asked after `phases/autonomy-settings.md` (questions 13 to 17), so the question order stays 10, 11/12, 13–17, 18–20. They are user-level host choices, not repo config.

18–20. **Fleet autonomy controls**: independently offer `dispatch.planRefined`,
    `automerge.enabled`, and `planning.attended` via `AskUserQuestion`, only for IDs in
    `delta.ask` on a delta run (all three on `--all`, first run, or fallback full run). These keyless IDs track offers in
    `configureOffered`; existing repo `planning`, `automerge`, or `refinement` keys
    and ledger entries never suppress them. Each question offers "Keep current
    (Recommended)", "On", and "Off"; a first run with no known state defaults to keep.
    Explain each scope: automatic dispatch planning, hands-off fleet merging, and the
    machine's interactive planning override. Never infer one answer from another.
    On the host, probe each current value separately through `cenci dispatch plan-refined
    status`, `cenci automerge status`, and `cenci planning attended status --json`;
    report probe failures without inventing a current value. Collect keep/on/off
    answers separately; explicit on/off is still available if a probe fails.

    **Container guard and apply timing.** Use Scripted Detection's existing container
    guard from Fleet Dispatch Enrollment. In a sandbox, collect the desired choices
    alongside the repo choices, but run no fleet status or mutation commands and never
    read or write `~/.config/cenci/config.json`. Print each concrete host command for
    an explicit on/off answer: `cenci dispatch plan-refined on` or `off`,
    `cenci automerge on` or `off`, `cenci planning attended on` or `off`. Tell the user
    to run them on the host; never claim deferred fleet choices were applied. Deferred
    choices are not stamped in the ledger, so they remain available on a later run.
    On the host, apply explicit answers only after configuration choices are gathered,
    one standalone CLI call per selected setting; never edit the host config directly.
    Record a fleet ID only after its CLI call succeeds or an explicit keep choice;
    a failed CLI call is reported and not recorded, and never blocks the repo config
    write. Its setting may already have persisted before a later status query failed:
    report the outcome as unknown or potentially partially applied, never unchanged.
    Re-probe through the matching status command when possible, report only what it
    verifies, and leave the failed call's ledger ID unstamped.
    Fleet controls never land as repo config fields. No host mutations run in Codex
    Plan mode; carry answers through the approved-plan artifact and execute during apply only.

**Next step.** Report the actual results of the fleet choices above and any deferred
host commands. The independent fleet-side verbs are:
- `cenci dispatch plan-refined on|off|status` to let dispatch pick up unattended planning
  once `planning.autonomy` is `lean` on `origin/main`.
- `cenci automerge on|off|status` to arm the fleet-wide automerge kill switch once a
  policy block exists.
- Switch 6 (`refinement.autonomy`) has no fleet verb: it is read from the local checkout, so there is nothing to flip fleet-wide.
- See `docs/autonomous-loop.md` for the full walkthrough of the switches.
