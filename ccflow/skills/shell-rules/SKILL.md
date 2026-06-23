---
name: shell-rules
description: Shared shell rules for sandbox compatibility. Read before running gh CLI commands, when encountering heredoc sandbox errors, sandbox write errors, or when creating PR bodies or issue descriptions via CLI.
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
