# AGENTS.md — cenci workflow for Codex

Repository guidance for Codex. The cenci workflow is carried by the installed native `$cenci:*`
skills; this file points at them and states the rules that always apply.

## Workflow

Use the installed native skills instead of a hand-written procedure:

- `$cenci:refine` — turn a ticket into an agreed, sized scope.
- `$cenci:implement` — plan and build the change for a refined ticket.
- `$cenci:review` — review a diff against the ticket's acceptance criteria.
- `$cenci:babysit` — watch a pull request through CI and merge.

The cenci plugin's `docs/codex.md` lists every native skill and what each supports today.

The native procedures use `.plans/` plan files, the board lifecycle labels and babysit, the same as the Claude Code pipeline.

The full gated ticket-to-PR flow is still under native acceptance, so do not treat one working skill as proof that the whole flow works.

If a native procedure is unavailable or stops, report and stop; do not rebuild the pipeline by hand.

## Critical Rules

- Always work in a feature worktree; never implement directly in the main worktree and never commit directly to main.
- Write tests first; assert behavior rather than implementation details.
- Review your own diff for security, code quality and silent failures before opening a pull request.
- Keep tickets well scoped: one ticket equals one pull request.
- When a step does not need the user's input, keep going and put status notes in the same message as the next action. Stop and ask only when you cannot continue without the user, or before anything destructive: deleting data, force-pushing, or changing anything outside this repository.

## Reference Docs

- `docs/git-workflow.md` — branching, commits, pull requests, and versioning.
