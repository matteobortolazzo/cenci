# Legacy Config Migration

Read this file only when `.claude/config.json` exists — a legacy-only migration run, or both config files present (see `SKILL.md` `### Step Map`). A `.cenci/config.json`-only run never reads it.

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
