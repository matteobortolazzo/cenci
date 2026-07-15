---
name: shell-rules
description: Shared shell rules for portable, approval-friendly agent commands. Use when running git, GitHub CLI, builds, tests, cross-directory commands, or commands that write files.
user-invocable: false
---

## Command Shape

- Prefer one logical command per tool call. Separate unrelated inspection, mutation,
  and verification so failures and approvals remain attributable.
- Do not assume a directory change persists between tool calls. Use the client's
  `workdir`/working-directory parameter or the command's own directory option, such
  as `git -C <path> status`.
- Avoid `bash -c`, conditional shell programs, compound pipelines, and command
  substitutions when the agent can run a command, inspect its result, and branch in
  reasoning instead.
- Never start an interactive CLI flow from automation. Pass explicit titles, bodies,
  messages, and other required inputs.

## Search and File Operations

Use the client's native read, search, glob, and patch tools when available. In Codex,
prefer `rg`/`rg --files` for repository search when no structured search tool is
available. In Claude Code, prefer `Read`, `Grep`, and `Glob`.

Do not add shell banners such as `echo "=== files ==="` to batched searches. They
make output noisier and can change approval matching.

Use the client's patch or edit tool for source changes. Shell-based generation is
appropriate only for mechanical output, formatter/codegen output, or tools whose
normal interface writes files.

## Body Files and Heredocs

Heredocs can require shell-created temporary files and may fail in restricted
sandboxes. Write long issue or pull-request content with the client's file tool, then
pass it through a CLI body-file option. Scope every such filename with a run-unique
suffix — `<scope>` below is a ticket id, PR number, or run id, whichever is already
in scope for the calling skill — so concurrent runs never collide on a shared fixed
name. Any scope key that isn't already format-constrained by its own source (a
numeric ticket/PR ID is inherently safe) must be validated against
`^[A-Za-z0-9._-]+$`, additionally rejecting `.`, `..`, or any value containing `..`,
before use — the same rule `implement`'s slug validation applies, since a scope key
can end up as a standalone path segment (e.g. a per-run subdirectory) where a
dot-only value could traverse:

```bash
gh issue edit <number> --body-file "${TMPDIR:-/tmp}/cenci/issue-body-<scope>.md"
```

```bash
gh pr create --title "<title>" --body-file "${TMPDIR:-/tmp}/cenci/pr-body-<scope>.md"
```

Never print or interpolate authentication tokens into command output.

## Shell Portability

Commands may run through zsh, bash, or another configured login shell. Prefer
portable command syntax and avoid bash-only arrays, `mapfile`/`readarray`,
`BASH_REMATCH`, and shell-specific array slicing. When complex shell logic is truly
needed, put it in a reviewed repository script with an explicit interpreter.

## Worktrees and Cross-Directory Writes

- Keep the main worktree read-only for implementation and direct edits to the feature
  worktree with an absolute path or the tool's working-directory option.
- Do not combine `cd <other-dir>` with a write, redirection, or unrelated command.
- Never rescue an edit made in the wrong worktree with stash, checkout, or file
  copying. Reapply the intended edit to the correct absolute path, then restore only
  changes created by the current operation.

## Client-Specific Approval Notes

### Claude Code

Claude Code allow rules and its shell analyzer inspect every segment of compound
commands. A call containing both `cd` and a redirection/write can hard-prompt even
when each command is allow-listed. Use standalone calls or `git -C`.

### Codex

Codex evaluates shell segments independently at control operators and may require
approval for any unmatched segment. Supply `workdir` on each shell call rather than
relying on a previous `cd`, and request narrowly scoped escalation only after a
sandbox-related failure or when the operation inherently needs it.
