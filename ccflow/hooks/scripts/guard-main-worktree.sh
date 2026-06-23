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
    echo "BLOCKED: Write to $FILE_PATH targets the main worktree, not a feature worktree."
    echo ""
    echo "FIX: Re-issue the SAME Write/Edit with an absolute path rooted at the feature"
    echo "worktree, i.e. under .worktrees/<id>-<desc>/ — keep the docs/<...> tail identical."
    echo "If you don't know the worktree path, run 'git worktree list' and use the"
    echo ".worktrees/<id>-<desc> entry, then retry the Write/Edit to that path."
    echo ""
    echo "DO NOT route around this with a Bash git rescue (cd <repo> && git checkout --"
    echo "<file>, git stash, git apply of a docs patch, etc.). That mutates the main"
    echo "worktree, trips the sandbox, and does not match Bash(git:*) allow-rules. If you"
    echo "must touch git for a worktree file, use 'git -C <worktree> ...' from inside the"
    echo "pipeline — never move a stranded edit by hand. Just re-issue the Write/Edit."
    echo ""
    echo "Outside /ccflow:implement (no feature worktree exists), propose the change text"
    echo "to the user instead of writing it."
    exit 2
    ;;
esac

exit 0
