---
name: shell-rules
description: Shared shell rules for sandbox- and shell-portability. Read before generating ANY shell in a ccflow pipeline — running gh CLI commands, writing files via shell, git commands across directories, heredoc/sandbox write errors, zsh/bash dialect errors, or creating PR bodies and issue descriptions via CLI.
user-invocable: false
---

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
