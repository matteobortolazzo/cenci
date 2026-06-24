---
name: shell-rules
description: Shared shell rules for sandbox- and shell-portability. Read before generating ANY shell in a ccflow pipeline — running gh CLI commands, writing files via shell, git commands across directories, heredoc/sandbox write errors, zsh/bash dialect errors, or creating PR bodies and issue descriptions via CLI.
user-invocable: false
---

## Worktree & Command Patterns

Bash auto-approval matches on the **first token** of a command and splits compound commands on `&&`/`;`/`|`, auto-approving only when **every** sub-command matches an allow-list entry. Write commands so each token matches — otherwise the call forces a manual approval prompt, defeating the point.

- **Enter the worktree once.** Run `cd <worktree-path>` as a *standalone* Bash call. CWD persists across calls, so run all later commands bare (`go test ./...`, `git status`). If you must chain (e.g. CWD-reset concerns), only chain allow-listed commands — `cd <worktree-path> && go test ./...` is fine because both `cd` and `go` are allow-listed.
- **One command per Bash call.** Don't `&&`-chain unrelated commands; run them as separate calls so each matches an allow-list prefix. Avoid pipes to non-allow-listed tools (`grep`/`sed`/`wc`) inside command lines you need auto-approved.
- **No conditional shell scripts.** Never wrap logic in `bash -c '…'`, `if/then`, loops, or command substitution that returns a string. Run the command, read its output, and branch in your reasoning. Scripts start with `bash`/`sh`/`(` and never match the tool-name allow-list — they always prompt.

## Searching the codebase

Use the built-in `Grep`, `Glob`, and `Read` tools to search and read code — not `grep`,
`rg`, `find`, `ls`, or `cat` through Bash, and never `echo "=== label ===" && grep …`
banner batches. The built-in tools need no allow-listing, never trigger a permission
prompt, and return compact, structured results. Reserve Bash for actions with no tool
equivalent: builds, tests, `git`, `gh`, and file moves.

## Heredoc Temp-File Pattern

Heredocs (`cat <<'EOF'`) fail in the sandbox (read-only filesystem can't create temp files). For any `gh` command that accepts `--body` or `--description`, write the content to a temp file first, then read it back:
```bash
printf '%s' '<content>' > /tmp/claude/<descriptive-name>.md
BODY=$(cat /tmp/claude/<descriptive-name>.md)
gh issue edit <number> --body "$BODY"
```
Never run `gh issue edit` or `gh pr create` without explicit `--body`/`--title` flags — interactive mode will hang.

## Shell Portability (zsh-safe)

The Bash tool runs commands through the **user's login shell**, which may be **zsh** — not bash. Write POSIX/zsh-portable shell only. Avoid bashisms:

- No bash associative arrays — `declare -A map` and `${map[key]}` fail in zsh.
- No other bash-only constructs (`mapfile`/`readarray`, `${arr[@]:offset}` slicing assumptions, `[[ =~ ]]` BASH_REMATCH reliance).

Prefer plain `for` loops over explicit lists, positional args, and simple `case` statements. If you need a key→value mapping, use a `case` statement or parallel iteration over an explicit list — not an associative array.

## No Cross-Directory Compounds; Never Hand-Rescue Worktree Edits

- **Don't emit `cd <other-dir> && <tool> …` chains.** Allow-rules like `Bash(git:*)` match on the **leading token**, so a `cd`-prefixed compound never matches and prompts for approval. Worse, writing outside the project directory trips the sandbox (`allowUnsandboxedCommands: false`) and prompts every run. Use the tool's own directory flag instead — e.g. `git -C <path> status` rather than `cd <path> && git status`.
- **Never move a stranded worktree edit by hand.** If a `Write`/`Edit` landed in (or was blocked from) the wrong worktree, do NOT rescue it with `git checkout -- <file>`, `git stash`, `git apply` of a docs patch, or copying files across directories. Re-issue the **same** `Write`/`Edit` to the correct `.worktrees/<id>-<desc>/…` absolute path. That is the only correct fix — the git rescue mutates the main worktree, trips the sandbox, and defeats the allow-rule.
