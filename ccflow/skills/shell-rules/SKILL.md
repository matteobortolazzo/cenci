---
name: shell-rules
description: Shared shell rules for sandbox- and shell-portability. Read before generating ANY shell in a ccflow pipeline — running gh CLI commands, writing files via shell, git commands across directories, cd combined with output redirection or writes, heredoc/sandbox write errors, zsh/bash dialect errors, or creating PR bodies and issue descriptions via CLI.
user-invocable: false
---

## Worktree & Command Patterns

Bash auto-approval matches on the **first token** of a command and splits compound commands on `&&`/`;`/`|`, auto-approving only when **every** sub-command matches an allow-list entry. Write commands so each token matches — otherwise the call forces a manual approval prompt, defeating the point.

- **Enter the worktree once.** Run `cd <worktree-path>` as a *standalone* Bash call. CWD persists across calls, so run all later commands bare (`go test ./...`, `git status`). If you must chain (e.g. CWD-reset concerns), only chain allow-listed commands — `cd <worktree-path> && go test ./...` is fine because both `cd` and `go` are allow-listed.
- **One command per Bash call.** Don't `&&`-chain unrelated commands; run them as separate calls so each matches an allow-list prefix. Avoid pipes to non-allow-listed tools (`grep`/`sed`/`wc`) inside command lines you need auto-approved.
- **No conditional shell scripts.** Never wrap logic in `bash -c '…'`, `if/then`, loops, or command substitution that returns a string. Run the command, read its output, and branch in your reasoning. Scripts start with `bash`/`sh`/`(` and never match the tool-name allow-list — they always prompt.

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

## Never combine `cd` with redirection or a write in one compound

Claude Code's built-in bash analyzer **hard-prompts** any *single* Bash call that contains a `cd` **and** an output redirection — `>`, `>>`, `2>&1`, `2>/dev/null`, `&>`, or a writing pipe (internal id `cd-compound-redirect`). The same prompt fires for a `cd` **and** a write-type subcommand (`cd-compound-write`). The trigger is all conditions appearing in one compound command (joined by `;`, `&&`, `||`, or `|`).

- **This is a binary-level guard, not an allow-list/sandbox one.** Unlike cross-directory compounds (above) — which are an allow-list/sandbox problem and are in principle fixable via settings *and* `git -C` — there is **no env var, setting, or allow-list entry that disables it**, sandbox on or off. Allow-listing `Bash(cd:*)` and `Bash(git:*)` does **not** help. The only remedy is reshaping the command so it never hits all the conditions at once.
- **Drop the `cd` — use the tool's own directory flag.** `git -C <repo> worktree add …` instead of `cd <repo>; git worktree add …`. This also satisfies the cross-directory rule above.
- **Or run `cd` standalone.** Issue `cd <path>` as its own Bash call (CWD persists across calls), then run later commands bare. No compound is formed, so neither guard fires.
- **Or split the compound.** When a line genuinely needs redirection (`… 2>&1 | tail`, `… 2>/dev/null`), put it in a *separate* Bash call from any `cd`. Never bundle status-gathering (`ls … 2>/dev/null`) into the same call as a `cd`.

Concrete before/after (the exact failing shape):
- ✗ `cd <repo>; git worktree add <wt> -b <branch> main 2>&1 | tail -5; ls -d node_modules 2>/dev/null`
- ✓ `git -C <repo> worktree add <wt> -b <branch> main` (one call), then inspect with separate bare calls — `ls -d <repo>/node_modules`, `ls -d <repo>/src/.../node_modules`, or `git -C <repo> status`.
