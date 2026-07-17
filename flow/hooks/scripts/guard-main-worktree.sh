#!/bin/sh
# PreToolUse hook: keep the main worktree read-only for Write/Edit — only in
# repos configured for cenci (gated on neutral config with legacy fallback).
# File changes must land inside a feature worktree (.worktrees/) so they ship
# in PRs. This catches subagents that accidentally use relative paths, and
# planning sessions that touch source files before a plan is saved —
# permission rules cannot enforce this under --dangerously-skip-permissions,
# but hooks still run.
# Writes that legitimately live in the main worktree are allowlisted below:
# saved plans (.plans/), Claude Code's own native Plan Mode storage
# (.claude/plans/, e.g. ~/.claude/plans/), design artifacts (designs/, *.pen,
# DESIGN.md — the one documented exception, /cenci:design commits directly
# on main), and temp paths. /cenci:configure writes (.cenci/, .claude/,
# AGENTS.md, CLAUDE.md, .gitignore, .mcp.json, and everything else it
# generates) are NOT allowlisted here — configure creates its own feature
# worktree and ships its changes as a PR like every other skill; see
# flow/skills/configure/SKILL.md's "Create Worktree" section.

# Only enforce in repos configured for cenci. .cenci/config.json is canonical;
# .claude/config.json remains a read-only migration signal. In unconfigured repos this
# guard must be a no-op: the plugin is installed globally, but the worktree
# workflow only applies where the user opted in.
if ! ROOT=$(git -C "$(pwd)" rev-parse --show-toplevel 2>/dev/null); then
  exit 0
fi
[ -f "$ROOT/.cenci/config.json" ] || [ -f "$ROOT/.claude/config.json" ] || exit 0

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | grep -oE '"file_path"\s*:\s*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
if [ -z "$FILE_PATH" ]; then
  FILE_PATH=$(echo "$INPUT" | grep -oE '"filePath"\s*:\s*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
fi

[ -z "$FILE_PATH" ] && exit 0

case "$FILE_PATH" in
  # Feature worktrees — the intended write target
  */.worktrees/* | .worktrees/*) exit 0 ;;
  # Plan persistence (implement Phase 1)
  */.plans/* | .plans/*) exit 0 ;;
  # Claude Code native Plan Mode storage
  */.claude/plans/* | .claude/plans/*) exit 0 ;;
  # Temp paths: body files, context bundles, attachments, scratchpads
  /tmp/* | /private/tmp/* | /var/folders/* | */cenci-attachments-*/*) exit 0 ;;
  # Design artifacts live in the main worktree by design (/cenci:design)
  *.pen | */DESIGN.md | DESIGN.md | */designs/* | designs/*) exit 0 ;;
esac

# Everything else is a main-worktree write → block with guidance.
{
  echo "BLOCKED: Write to $FILE_PATH targets the main worktree, not a feature worktree."
  echo ""
  echo "THE ONLY CORRECT FIX: re-issue the SAME Write/Edit with an absolute path rooted"
  echo "at the feature worktree — under .worktrees/<id>-<desc>/, keeping the path tail"
  echo "identical. If you don't know the worktree path, run 'git worktree list' and"
  echo "use the .worktrees/<id>-<desc> entry, then retry the Write/Edit to that path."
  echo ""
  echo "DO NOT route around this with a Bash git rescue — no 'cd <repo> && git checkout"
  echo "-- <file>', 'git stash'/'git stash pop', 'git apply' of a patch, or copying files"
  echo "across directories. Those mutate the main worktree, trip the sandbox, FORCE A"
  echo "PERMISSION PROMPT (a 'cd ... && git ...' compound can never be auto-approved), and"
  echo "do not match Bash(git:*) allow-rules. If git must target a worktree file, use"
  echo "'git -C <worktree> ...' (never 'cd <worktree> && git ...'). Never move a stranded"
  echo "edit by hand — just re-issue the Write/Edit to the correct path."
  echo ""
  echo "In a planning session (no feature worktree yet), only .plans/ and temp paths are"
  echo "writable — implementation writes happen in the plan-file run, after approval."
  echo "Outside /cenci:implement and /cenci:configure, propose the change text to the"
  echo "user instead of writing it. /cenci:configure creates its own feature worktree"
  echo "before writing anything — see its 'Create Worktree' step."
} >&2
exit 2
