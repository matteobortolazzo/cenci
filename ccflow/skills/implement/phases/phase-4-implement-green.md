# Phase 4: Implement (Green)

Read this file only when Phase 4 starts. Skip this separate phase if Phase 3 is running approved compact implementation mode.

Delegate to the `implementer` agent to make failing tests pass.

## Design Pre-Read

If `pencil.enabled` and `pencilAvailable` are true, the main agent pre-reads relevant design structure because subagents cannot use Pencil tools reliably.

Identify affected screen node IDs from the plan file's `## Design Context` section — the `designScreenIds` and `designComponentMap` lists at the top of that section (written there by the context-gatherer). Narrow to the screens the plan actually touches.

CLI-app mode: batch reads in one invocation:

```bash
pencil interactive -a desktop <<'EOF'
batch_get({ nodeIds: ["<screen-node-id>"], readDepth: 3 })
get_variables()
export_nodes({ nodeIds: ["<screen-node-id>"], outputDir: "$TMPDIR/design-screenshots", format: "png" })
EOF
```

Editor mode: call `batch_get`, `get_variables`, and `get_screenshot` via MCP.

Pass design hierarchy, token values, and screenshot references to the implementer. If any read fails, state what is missing and proceed with `DESIGN.md` text context only.

## Delegation Context

Pass:

- Worktree path. Tell the agent: enter it with a standalone `cd <worktree-path>` as the first Bash call (CWD persists for later calls) — do **not** prefix every command with `cd <path> &&` (a `cd … && git …` compound can never be auto-approved; use `git -C <path>` if git must target another directory). If a `Write`/`Edit` is blocked or stranded, re-issue the same edit to the correct `.worktrees/<id>-<desc>/` path — never hand-rescue it with `git stash`/`git checkout`/`git apply`. See the `shell-rules` skill for command patterns.
- Plan file sections: `## Ticket Details`, `## Implementation Plan`, `## Architectural Context`, and relevant `## Design Context`.
- The failing tests and their failure output.
- Attachment paths if relevant.
- Full DESIGN.md only when needed for UI/component mapping.
- LSP diagnostic reminder if configured.

## Rules

- Follow the approved plan exactly.
- Make tests pass with the simplest correct implementation.
- Consult only relevant `docs/<topic>.md` files.
- Honor `CLAUDE.md` and `README.md`; update docs if behavior, setup, configuration, or user-visible contracts change.
- No premature abstractions, dead code, commented-out code, or TODOs without ticket references.

## Verification

After implementation:

1. Run the full build.
2. Run the full test suite.
3. Report exact commands and results.

If build/tests fail, analyze root cause, fix, and rerun. Retry up to 3 times, then clear the Goal Autopilot (`/goal clear` via `SlashCommand`, a no-op if none is armed — see `SKILL.md`) and stop, reporting exact errors, attempts, and best hypothesis. Clearing prevents the goal from restarting the turn straight back into the same failing build.

## Visual Verification

For frontend plans with visual components:

- Prefer Playwright Test with `toHaveScreenshot()` when configured.
- Use Playwright CLI for interactive screenshots/snapshots only as development verification.
- If no browser tooling is available, note that visual verification was not performed.

If Pencil is available, compare implementation screenshots against design screenshots and inspect `snapshot_layout(..., problemsOnly: true)` for clipping, overflow, and misalignment. Fix significant discrepancies or get explicit user acceptance before Phase 5.

### Persist Screenshots For The PR

If `isUiTicket` is true, save a final screenshot of every affected screen/state to `/tmp/claude/ccflow-screenshots/<ticket-id-or-slug>/` with descriptive kebab-case filenames (e.g. `login-form-error-state.png`). Capture with `playwright-cli screenshot` against the running dev build, or copy the relevant Playwright Test `toHaveScreenshot` output. Capture **after** visual verification passes so the images show the final state — Phase 9 uploads them and embeds them in the PR body as review aids. If no browser tooling is available, skip this step; Phase 9 will note the gap in the PR.
