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

command -v jq >/dev/null 2>&1 || {
  echo "BLOCKED: jq is required by guard-main-worktree.sh but was not found on PATH." >&2
  exit 2
}

INPUT=$(cat)
if ! FILE_PATH=$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // .tool_input.filePath // empty' 2>/dev/null); then
  echo "BLOCKED: guard-main-worktree.sh could not parse the tool call's JSON input." >&2
  exit 2
fi

[ -z "$FILE_PATH" ] && exit 0

case "$FILE_PATH" in
  /*) ;;
  *)
    echo "BLOCKED: guard-main-worktree.sh received a non-absolute file_path: $FILE_PATH" >&2
    exit 2
    ;;
esac

# Detect a path resolver able to canonicalize an *existing* path. Prefer
# realpath; fall back to GNU readlink -f (probed via a known-existing path so
# BSD/other readlink implementations that lack -f are rejected here, not at
# use time). Neither available → fail closed.
if command -v realpath >/dev/null 2>&1; then
  resolve_path() { realpath "$1"; }
elif readlink -f / >/dev/null 2>&1; then
  resolve_path() { readlink -f "$1"; }
else
  echo "BLOCKED: guard-main-worktree.sh requires realpath or 'readlink -f' to canonicalize paths, neither was found on PATH." >&2
  exit 2
fi

# Lexically collapse "." and ".." segments in an absolute path, without
# touching the filesystem. Used both as the sole normalization for
# not-yet-existing paths and as a final cleanup pass after a parent-anchored
# resolve (whose re-appended tail may itself contain traversal segments).
lexical_collapse() {
  path="$1"
  # Split on '/', rebuild collapsing '.' and '..' against the accumulated stack.
  # Disable globbing for the unquoted split so path segments containing glob
  # metacharacters (*, ?, [) are never pathname-expanded.
  old_ifs=$IFS
  IFS='/'
  set -f
  # shellcheck disable=SC2086 # intentional IFS='/' word-splitting to
  # enumerate path segments; globbing is disabled via `set -f` above.
  set -- $path
  set +f
  IFS=$old_ifs
  result=""
  for seg in "$@"; do
    case "$seg" in
      "" | ".") ;;
      "..")
        result="${result%/*}"
        ;;
      *)
        result="$result/$seg"
        ;;
    esac
  done
  [ -z "$result" ] && result="/"
  printf '%s\n' "$result"
}

# Parent-anchored canonicalization: only ever feed *existing* paths to the
# resolver, sidestepping realpath/readlink -f divergence on non-existent
# trailing components (no -m / -f-on-missing-target usage). When FILE_PATH
# itself does not exist, walk up toward / to find the nearest *existing*
# ancestor — not just the immediate parent — so a symlink several levels
# above a not-yet-existing tail (e.g. .worktrees/link/existingsub/newdir/new.txt
# where only "link" and "existingsub" exist) still gets resolved. "/" always
# exists, so the walk is guaranteed to terminate.
if [ -e "$FILE_PATH" ]; then
  if ! FILE_PATH=$(resolve_path "$FILE_PATH"); then
    echo "BLOCKED: guard-main-worktree.sh could not resolve $FILE_PATH" >&2
    exit 2
  fi
else
  ANCESTOR="$FILE_PATH"
  TAIL=""
  while [ -n "$ANCESTOR" ] && [ ! -d "$ANCESTOR" ]; do
    SEG="${ANCESTOR##*/}"
    if [ -z "$TAIL" ]; then
      TAIL="$SEG"
    else
      TAIL="$SEG/$TAIL"
    fi
    ANCESTOR="${ANCESTOR%/*}"
  done
  [ -z "$ANCESTOR" ] && ANCESTOR="/"
  if ! RESOLVED_ANCESTOR=$(resolve_path "$ANCESTOR"); then
    echo "BLOCKED: guard-main-worktree.sh could not resolve $ANCESTOR" >&2
    exit 2
  fi
  FILE_PATH=$(lexical_collapse "$RESOLVED_ANCESTOR/$TAIL")
fi

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
