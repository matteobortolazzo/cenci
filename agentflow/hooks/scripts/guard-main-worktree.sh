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
    {
      echo "BLOCKED: Write to $FILE_PATH targets the main worktree, not a feature worktree."
      echo ""
      echo "THE ONLY CORRECT FIX: re-issue the SAME Write/Edit with an absolute path rooted"
      echo "at the feature worktree — under .worktrees/<id>-<desc>/, keeping the docs/<...>"
      echo "tail identical. If you don't know the worktree path, run 'git worktree list' and"
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
      echo "Outside /agentflow:implement (no feature worktree exists), propose the change text"
      echo "to the user instead of writing it."
    } >&2
    exit 2
    ;;
esac

exit 0
