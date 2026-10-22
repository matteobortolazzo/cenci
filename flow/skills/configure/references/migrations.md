# Migrations

This file holds two sections, each read under its own condition (see `SKILL.md` `### Step Map`).

## Legacy config migration

Read this section only when `.claude/config.json` exists — a legacy-only migration run, or both config files present (see `SKILL.md` `### Step Map`). A `.cenci/config.json`-only run never reads this section.

On migration, preserve every unknown key from `legacyConfig`; new writes go only to
`.cenci/config.json`. If both files exist, recursively merge them into one
`migrationBase` with canonical values winning, then overlay managed answers. Use
`"${CLAUDE_PLUGIN_ROOT}/scripts/migrate-project-core.sh" <root>` to preview the exact
config/guidance diff:
```bash
bash "${CLAUDE_PLUGIN_ROOT}/scripts/migrate-project-core.sh" <root> [--apply]
```
Rerun it with `--apply` only after approval, with `<root>` set to `<worktree-path>` (see
Create Worktree in `SKILL.md` — the worktree exists by the time any `--apply` can run). Never
rewrite or delete `.claude/config.json`; it is a read-only compatibility artifact.

## Board migration notes

Read this section only when re-configuring an existing project (`existingConfig` or `migrationBase` is present), and state the note that matches how old the project's board is (see `SKILL.md` `### Step Map` and `### Board lifecycle labels`).

**Migration note for existing boards** (state this when re-configuring a project that predates
`In Review`): add an **`In Review`** column/label. Previously, tickets dropped straight into
`Implemented` the moment the PR was opened; now PR-open lands them in `In Review`, and
`/cenci:babysit` promotes them to `Implemented` when the PR actually merges. Existing tickets
that carry `Implemented` from before this change but whose PRs are still open can be relabeled to
`In Review` by hand; new work follows the split automatically.

**Migration note for existing boards** (state this when re-configuring a project that predates
`Planned`): add a **`Planned`** column/label between `Refined` and `Working`. Previously, a
planning session applied `Working` at pipeline start and then stopped once the plan was saved —
so the board showed `Working` on a ticket nobody was actively working. Now a planning session
lands the ticket on `Planned` ("a plan exists on disk, ready to pick up"); the swap to
`Working` happens when the plan-file implementation run begins. New work follows this
automatically.

**Migration note for existing boards** (state this when re-configuring a project that predates
the reconciler-managed labels): re-running `/cenci:configure` on an existing project creates
the `dispatch-failed`, `plan-invalid`, and `reconcile-stuck` labels retroactively via
step 3c in `phases/guidance-settings.md`'s missing-only `gh label create` loop. No other action is needed — the reconciler already
self-heals these labels itself via `EnsureLabels`, so a board that skips this step is not
blocked; re-running configure just creates them proactively instead of waiting for the
reconciler to hit the corresponding error state first.
