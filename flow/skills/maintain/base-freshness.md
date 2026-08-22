# maintain — base-branch freshness gate

Shared by both `SKILL.md` and `codex.md` at their Phase 6 call sites — the same pattern
`modes/*.md` already uses for content both clients consult. See `base-freshness.md` for
the shared probe/integrate/push-policy mechanics reused at all three gates below.

**Applicability**: the four repo-audit modes (`structure`, `docs`, `clients`, `rules`, and
their combination `all`) only. `backlog` mode creates no branch and no PR, so none of this
runs for it.

## Resolve `<base>`

Resolve `<base>` once, before worktree creation, and carry it forward as literal text for
the rest of the run — it replaces every hardcoded `main` at three sites: the `worktree add`
base ref, every freshness probe below, and `gh pr create --base`.

```bash
git -C <repo-root> symbolic-ref --short refs/remotes/origin/HEAD
```

This prints `origin/<name>` — strip the `origin/` prefix to get `<base>`. If the command
errors because no remote HEAD is set, fall back to `main`.

## Freshness probe

The reusable unit, run at every gate below:

```bash
git -C <worktree-path> fetch origin <base>
git -C <worktree-path> merge-base --is-ancestor origin/<base> HEAD
```

- `fetch` exits non-zero → **stop, fail closed**. An unfetchable base is not a fresh base.
- `--is-ancestor` exits 0 → fresh, continue.
- `--is-ancestor` exits 1 → the base advanced, run **Integrate the base** below.
- Any other exit → a genuine command error, not "not an ancestor" → **stop, fail closed**;
  never read a command error as fresh, and never as advanced. This mirrors
  `watch/internal/dispatch/mainsync.go:248`'s rule that a genuine merge-base command error
  must never be reported as `MainSyncDiverged`.

## Integrate the base

The reusable routine — three call sites invoke it: Gate A, Gate B, and Gate C's bounded
repair loop.

1. Determine whether the branch is published:

   ```bash
   git -C <worktree-path> ls-remote --exit-code --heads origin chore/maintain-<run-token>
   ```

   Exit 0 → published. Exit 2 → unpublished. Any other exit → **stop, fail closed**: an
   unreadable remote cannot prove "unpublished", and guessing wrong means either a
   force-push or a needless merge.
2. **Unpublished** → `git -C <worktree-path> rebase origin/<base>`.
3. **Published** → `git -C <worktree-path> merge --no-ff origin/<base>`. Never rebase,
   amend, reset, or force-push a published maintenance branch.
4. **Conflicts** → resolve inside the maintenance worktree with `Edit`/`Write`, under
   Phase 6's existing Hard gate (absolute path containing `/.worktrees/`), then conclude
   with `rebase --continue` / `commit`. If the conflict cannot be resolved with
   confidence, abort (`rebase --abort` / `merge --abort`), retain the worktree and branch,
   stop, and ask the user.
5. **Rerun the complete verification after any integration** — `scripts/check.sh` from the
   verified absolute worktree CWD, then `sh flow/hooks/scripts/run-gate.sh flow` from the
   same CWD; both must be clean. A reduced, partial, or skipped gate is never acceptable,
   explicitly including when the incoming changes are documentation-only.

## Push policy

Plain `git -C <worktree-path> push -u origin chore/maintain-<run-token>`. Never `--force`,
`-f`, `--force-with-lease`, or `--no-verify` on a maintenance branch — deliberately
stricter than `flow/docs/adapter-contract.md`'s `push-policy` (which permits
`--force-with-lease`), because a published maintenance branch carries an open PR whose
review history a force-push would discard. A non-fast-forward rejection means the *remote
branch* advanced: fetch it, integrate with a non-rewriting merge, rerun the complete
verification, push again — never force.

## Three gates

All three run in Phase 6, at the call sites named in `SKILL.md`/`codex.md`.

**Gate A** — after the approved edits are complete, before the final verification. Probe;
integrate if advanced. The existing "Verify the repair before shipping it" step then runs
on the integrated tree.

**Gate B** — immediately before the first push. Probe again. If the base advanced again,
integrate, rerun the complete verification, and probe again.
Repeat until the probe reports fresh in the same turn as the push.
Never push, and never open a PR, from a tree the probe has not just confirmed fresh.

**Gate C** — after `gh pr create` succeeds or an existing PR is recovered. Query
mergeability:

```bash
gh pr view chore/maintain-<run-token> --json mergeable,mergeStateStatus,url,number
```

- `UNKNOWN` — GitHub has not finished computing the merge commit. Re-query up to three
  times before treating it as settled. `UNKNOWN` is never reported as mergeable and never
  as conflicting.
- `CONFLICTING` — the run is **not** complete. Integrate the base (published branch ⇒
  non-rewriting merge), resolve conflicts in the worktree, rerun the complete
  verification, push, re-query. Bound the repair to two attempts; if still `CONFLICTING`,
  stop, retain the worktree and branch, report the PR URL and the conflicting state, and
  ask the user. Never report the run as successfully completed on a `CONFLICTING` PR.
- Still `UNKNOWN` after the re-queries — report the PR URL with mergeability explicitly
  unverified. Do not claim the PR is mergeable.
- `MERGEABLE` — done.

## Completion-summary contribution

Whether the base advanced and how it was integrated at each gate, that the complete
verification reran after each integration, and the final mergeability state.
