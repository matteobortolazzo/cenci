# Codex ci-repair procedure

Apply the plugin's `docs/model-escalation.md` diagnosis trigger: after two failed attempts at the
same unexplained defect, the root delegates a bounded second opinion to `domain-specialist` with
the logs, source paths and attempted fixes, and validates its evidence before applying any advice.
This adds no repair attempt and never defers the ambiguity stop below; when that stop already
applies, return the diagnosis through it without another fix attempt.

Read `project-core`, `codex-runtime`, `shell-rules`, `subagent-safety` and `testing`. This procedure is
dispatched by the babysit supervisor for one open PR. It never enters `/plan`, never runs
`cenci pipeline`, never changes a lifecycle label and never runs `gh pr create`. The root agent alone
commits, pushes and writes the checkpoint; workers never push.

## Inputs

The launch argument is the supervisor's prose prompt (`PR #<n> (<branch>) has failing CI checks: ...`)
or the manual `<pr> <branch> <checks>` form. Take only the PR number from it and validate it against
`^[1-9][0-9]*$`; an invalid number stops the run with no write. Derive `owner/repo` from
`git remote get-url origin`: strip one trailing `.git`, accept only the https form
`https://github.com/<owner>/<repo>` or the ssh forms `git@github.com:<owner>/<repo>` and
`ssh://git@github.com/<owner>/<repo>`, reduce it to `<owner>/<repo>` and validate that against
`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`. A URL of any other shape stops the run with no write, as do a failed
`git remote get-url` and a value that fails the regex. The branch name, head SHA and failing checks are never taken from the
launch text: read them with
`gh pr view <pr> --repo <owner>/<repo> --json headRefName,headRefOid,isCrossRepository,state` and
`gh pr checks <pr> --repo <owner>/<repo> --json bucket,name,state`. A failed read stops the run with no write
and the report "could not read PR state": a `gh pr view` with a non-zero exit, and either command with empty
output or output that is not valid JSON, is a read failure and is never reported as "no failing check". `gh pr
checks` exits 8 while checks are pending and exits non-zero while checks fail, yet still writes valid JSON, so
for it a non-zero exit is a read failure only when the JSON does not decode. Only checks in the `fail` bucket
are repair targets; a `pending` check is still running and is never repaired or reported as passed. Validate
the branch against `^[A-Za-z0-9._/-]+$`, with no `..` and no leading `-`, before any git command uses it.
A closed or merged PR, or a PR whose successfully read checks include no `fail` bucket entry, stops with no
write and a report saying so.

## Checkpoint

The checkpoint file is `.cenci/checkpoints/ci-repair-<pr>.json` under the main checkout, never inside the PR
worktree. The launch directory may itself be a linked worktree, so resolve the main checkout with
`git rev-parse --path-format=absolute --git-common-dir`: that directory ends in `/.git` and its parent is the
root; any other shape stops the run with no write. `<ckpt>` below is the absolute path
`<root>/.cenci/checkpoints/ci-repair-<pr>.json`.

Every helper call has one positional shape, and the phase is always the fifth argument, so `<workflow>` is
`ci-repair` and `<target>` is the PR number on every call:

```
node "${PLUGIN_ROOT}/codex/checkpoint.mjs" get <ckpt>
node "${PLUGIN_ROOT}/codex/checkpoint.mjs" init <ckpt> ci-repair <pr> planned
node "${PLUGIN_ROOT}/codex/checkpoint.mjs" advance <ckpt> ci-repair <pr> <phase>
node "${PLUGIN_ROOT}/codex/checkpoint.mjs" block <ckpt> ci-repair <pr> <phase>
node "${PLUGIN_ROOT}/codex/checkpoint.mjs" complete <ckpt> ci-repair <pr> <phase>
```

Order: run the existence check and `get` right after the input checks pass and before any `block`, because
`advance`, `block` and `complete` throw when the file is missing. A missing file is created with `init` (it
overwrites unconditionally, so it is never used on an existing file); an existing file is read with `get` and
then updated with `advance`. A file that exists but whose `get` exits non-zero or prints non-JSON is
unparseable: stop with no write and report "checkpoint unreadable", and never `init` over it.

The helper's `phase` argument carries both evidence and the pushed SHA. Progress phases are `diagnosed`,
`reproduced`, `gate-green`, `committed:<sha>` and `pushed:<sha>`; block phases are `gate-red:<head-sha>` and
`unrepaired:<class>:<head-sha>`. The checkpoint contract is not extended here: the full evidence (failing
check names, the gate's `GATE_LOG=` path and the classification) goes in the final report.

Check every helper call. A non-zero exit, or an echoed `phase` (in the JSON the helper prints) that differs from
the requested one, is reported as "checkpoint not recorded". Before the push that stops the run with no push,
because a restart depends on the record. After the push already succeeded, the push stands: a failed `pushed:`
record or a failed `complete` after a successful push is reported plainly with the pushed SHA and "checkpoint
not recorded", never as a clean success.

## Worktree

Work only in the PR branch worktree. When `isCrossRepository` is true, initialize the checkpoint, then run `block` with phase
`unrepaired:external:<head-sha>`, before any worktree or write, because a fork's branch is not ours to push
(`block` needs the file `## Checkpoint` just created, and the checkpoint is the only write). Otherwise find a worktree already
on the head branch with `git worktree list --porcelain`. Reuse it only when `git -C <abs-worktree> status --short`
is empty; a dirty worktree is an `unrepaired:ambiguous:<head-sha>` block that never touches its uncommitted work
and never creates a fresh worktree beside it. With no worktree on the branch, run
`git fetch origin <branch>` and create one under `.worktrees/` with `git worktree add`. The worktree's HEAD must
equal the PR head SHA or fast-forward to it; anything else is the same `ambiguous` block. Every write stays in
that worktree; target it with `git -C <abs-worktree>` and absolute paths on every command, and never stash, reset,
check out over or discard anything.

## Repair

1. Record `diagnosed` after reading the failing checks' logs (`gh run view <run-id> --log-failed` for the run each
   failing check names).
2. Reproduce the cause locally by running the failing command in the worktree and record `reproduced`. A cause that
   does not reproduce is classified, never guessed at.
3. Make the smallest test-backed fix: a failing test first, then the code. Workers may write files in the worktree.
4. Run the project's local gate (`docs/health-gates.md`): `( cd "<abs-worktree>" && sh "${PLUGIN_ROOT}/hooks/scripts/run-gate.sh" "<slug>" )`,
   where `<slug>` is the `.cenci/config.json` `projects[]` entry matching the changed paths (no argument for a
   single-repo config). In a monorepo config, pass the project slug: an omitted or mismatched slug makes
   `run-gate.sh` report unset for a project that does have a gate. Capture both the exit status and the output.
   Parse `GATE_STATUS=` and `GATE_LOG=` with a last-match scan, never the first match.
   - Green needs the exit status 0 AND the last non-empty line of the output is exactly `GATE_STATUS=green`
     (line-anchored: the whole line, nothing before or after it). A `GATE_STATUS=green` printed earlier by the
     repo's own gate fixtures or tests is never evidence, and a killed or timed-out gate has a non-zero exit.
     Green records `gate-green`.
   - These are gate failures: any disagreement between the exit status and the status line (exit 0 with another
     last line, or a non-zero exit with a green line), `GATE_STATUS=red`, and any non-zero exit (a non-zero exit is a
     gate failure whatever the output says). A gate failure continues at `### Gate failure`.
   - `GATE_STATUS=unset` is skipped, not failed, only when `.cenci/config.json` has no `gateCommand` at the top
     level and in no `projects[]` entry. Check that with
     `jq -e '((.gateCommand // "") == "") and (([.projects[]? | select((.gateCommand // "") != "")] | length) == 0)' "<abs-worktree>/.cenci/config.json"`
     (exit 0 means no gate is configured anywhere). Unset caused by an omitted or mismatched slug, by a missing
     config, or by a `jq` failure is a gate failure, not a skip. When the skip is allowed, the push comes only
     after the locally reproduced failing command passes, and the report says "gate unset" and states why the
     gate was unset (no `gateCommand` at the top level or in any `projects[]` entry).
5. Commit only explicit fix paths with `git -C <abs-worktree> add <path>...`, never `git add -A`, and record
   `committed:<sha>`.
6. Push without force: `git -C <abs-worktree> push origin HEAD:refs/heads/<branch>`, never a force flag, never a `+`
   refspec and never `--force-with-lease`. Verify with `git ls-remote origin refs/heads/<branch>` that the remote
   head equals the pushed SHA, then record `pushed:<sha>`.

A cause that is infrastructure, flaky or external is never repaired by editing code to hide it.

## Behavior cases

### Gate failure

The gate is red, or could not run. There is no push. Run `checkpoint.mjs block` with phase `gate-red:<head-sha>`,
retain the worktree and branch, and report the failing check names, the gate's `GATE_LOG=` path (locate the failing
detail there with `rg` rather than reading the whole log) and the classification. The run exits and returns control to
the supervisor, which owns what happens next.

### Repair failure, ambiguous, infra, flaky or external cause

The cause cannot be reproduced or repaired, or is not ours to repair. There is no push and the PR is never marked
repaired. Run `checkpoint.mjs block` with phase `unrepaired:<class>:<head-sha>`, where `<class>` is `ambiguous`,
`infra`, `flaky`, `external` or `push-rejected`. A cross-repository (fork) PR is `external`, a dirty existing worktree
is `ambiguous`, and a rejected push is `push-rejected`: never retry it with a force flag, never rewrite history, and
leave the remote untouched. Report the evidence (failing check names, `GATE_LOG=` path, classification). The run exits
and returns control to the supervisor for human input; the PR receives no label write and no comment from this
procedure.

### Successful push

Only after the gate is green (or unset with the reproduced failing command passing) and the commit exists. Push
without force with `git push origin HEAD:refs/heads/<branch>`, verify with `git ls-remote`, run
`checkpoint.mjs complete` with phase `pushed:<sha>`, and report the PR, the pushed SHA and the gate result. Stage only
explicit fix paths and never `git add -A`. The supervisor then re-reads CI for the new head.

### Restart

A re-launch reads `.cenci/checkpoints/ci-repair-<pr>.json` with `checkpoint.mjs get` before anything else. When the
recorded pushed SHA equals the current head from `gh pr view`, the run is a no-op: exit with no commit, no push and
no re-diagnosis. The report still states that the head still has failing checks, names them, and says no repair was
attempted because that SHA was already pushed. The same no-op applies when the recorded `pushed:<sha>` is already on
the remote although `gh pr view` still shows an older head: read `git ls-remote origin refs/heads/<branch>`, and when
the remote head equals the recorded SHA, or `git fetch origin <branch>` then
`git merge-base --is-ancestor <recorded-sha> FETCH_HEAD` shows the remote head descends from it, report that the push
is already on the remote and CI has not caught up, with no re-diagnosis. Otherwise resume from the last completed gate: `diagnosed` re-reproduces, `reproduced` goes on to the fix,
`gate-green` goes on to commit, and `committed:<sha>` goes straight to the push when the worktree HEAD already equals
that SHA, so a restart never duplicates a commit. A `gate-red:` or `unrepaired:` checkpoint at the same head starts a
fresh diagnosis with `advance`. A checkpoint whose recorded head differs from the current head describes an older
state: begin again at `diagnosed` with `advance`, never re-push the older commit.

## Final report

State the PR, the head SHA, the failing check names, the gate result (or "gate unset" with the reason), the `GATE_LOG=`
path when red, the classification, the pushed SHA when one exists, and any "checkpoint not recorded" or "could not read
PR state" outcome. Stop every background shell this run started.
