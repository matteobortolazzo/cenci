#!/bin/sh
# PreToolUse hook: block writes to docs/ in the main worktree.
# These changes must land inside a feature worktree (.worktrees/) so they
# are included in PRs. Catches subagents that accidentally use relative paths.

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | grep -oE '"file_path"\s*:\s*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
if [ -z "$FILE_PATH" ]; then
  FILE_PATH=$(echo "$INPUT" | grep -oE '"filePath"\s*:\s*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
fi

[ -z "$FILE_PATH" ] && exit 0

# Allow writes inside feature worktrees
case "$FILE_PATH" in
  */.worktrees/*) exit 0 ;;
esac

# Block writes to docs/ files in the main worktree
case "$FILE_PATH" in
  */docs/*.md)
    echo "BLOCKED: Write to $FILE_PATH targets the main worktree."
    echo "docs/ changes must land inside a feature worktree (.worktrees/)."
    echo "If in /ccflow:implement, pass the worktree path to the subagent."
    echo "Outside a pipeline, propose the change text to the user instead."
    exit 2
    ;;
esac

exit 0
