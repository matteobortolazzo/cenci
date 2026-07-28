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
#
# TMPDIR widening (#749): several skills follow shell-rules' repo-wide
# `${TMPDIR:-/tmp}/cenci/<name>-<scope>.md` body-file convention (e.g.
# design's `${TMPDIR:-/tmp}/cenci/design-comment-<number>.md` ticket-comment
# temp file). Under a custom TMPDIR that write is outside the pre-existing
# /tmp/*, /private/tmp/*, /var/folders/* arms and would be blocked. Below,
# the allowlist additionally admits paths under a canonicalized $TMPDIR,
# gated on TMPDIR being set, absolute, resolvable to an existing directory,
# and neither equal to, an ancestor of, nor a descendant of (i.e. nested
# inside) the resolved repo root. The descendant rejection matters just as
# much as the ancestor one: a TMPDIR set to e.g. $ROOT/tmp would otherwise set
# TMPDIR_ALLOW to that path and allowlist writes under a main-worktree
# subtree, defeating the guard's purpose. A `*/cenci/*.md` glob-only arm was
# considered and rejected: it matches any
# fully-resolved absolute path containing a `cenci` path segment, so
# `~/src/cenci/AGENTS.md` in *this* repo's own main worktree would be
# silently allowed too — gutting the guard for the very repo it protects.
# Canonicalizing $TMPDIR itself has no such repo-name hole and automatically
# covers every skill's convention, not just design's.

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

# mktemp-failure fallback (#550): if a temp file can be created, jq's stderr
# is captured there (fd 9 points at it) so the normal-path message keeps its
# exact inline-detail format. If mktemp fails (e.g. TMPDIR exhaustion), fd 9
# is instead dup'd straight to the script's own stderr — /dev/null is never
# assigned to JQ_ERR, so it can never be handed to `rm` (which as root would
# unlink the /dev/null device node), and jq's diagnostic is never dropped.
if JQ_ERR=$(mktemp 2>/dev/null); then
  exec 9>"$JQ_ERR"
else
  JQ_ERR=""
  exec 9>&2
  echo "guard-main-worktree.sh: warning: mktemp failed; jq errors are written directly to stderr below" >&2
fi

# jq_err_detail — the jq diagnostic text to interpolate into a BLOCKED
# message: the captured temp-file content when one exists, else a pointer to
# the raw jq stderr already printed above (fd 9 passthrough case).
jq_err_detail() {
  if [ -n "$JQ_ERR" ]; then
    cat "$JQ_ERR" 2>/dev/null
  else
    echo "(see the jq error printed on stderr above)"
  fi
}

# jq_err_cleanup — close fd 9 and remove the temp file, only when one exists.
# Always returns 0 on success: an `if` whose final command is unconditional,
# not a `&&`-chain whose truth value would become the function's return code
# (which would make the no-temp-file case return 1 even though cleanup
# succeeded — harmless today since no caller uses `set -e`, but latent).
jq_err_cleanup() {
  exec 9>&-
  if [ -n "$JQ_ERR" ]; then
    rm -f "$JQ_ERR"
  fi
}

# Explicit emptiness check, not a bare `//` chain: jq's `//` only falls back on
# null/false and would treat a present-but-empty file_path ("") as truthy,
# short-circuiting past filePath to the empty-path allow below.
if ! FILE_PATH=$(printf '%s' "$INPUT" | jq -r '
    if (.tool_input.file_path // "") != "" then
      .tool_input.file_path
    else
      (.tool_input.filePath // empty)
    end
  ' 2>&9); then
  echo "BLOCKED: guard-main-worktree.sh could not parse the tool call's JSON input: $(jq_err_detail)" >&2
  jq_err_cleanup
  exit 2
fi
jq_err_cleanup

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
    # A component present as a symlink node (lstat via [ -L ], which does not
    # dereference) but unresolvable to an existing target ([ -e ] follows the
    # link and is false for a dangling target or an ELOOP cycle) must fail
    # closed rather than be walked past as a not-yet-existing segment.
    # A genuinely absent component ([ -L ] false) still walks up unchanged.
    if [ -L "$ANCESTOR" ] && [ ! -e "$ANCESTOR" ]; then
      echo "BLOCKED: guard-main-worktree.sh refusing to resolve $FILE_PATH: component $ANCESTOR is a symlink that does not resolve (dangling target or symlink loop)." >&2
      exit 2
    fi
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

# Compute the canonicalized TMPDIR-widening allowlist prefix once (#749).
# Empty TMPDIR_ALLOW means the widening is disabled; every failure mode below
# (unset, relative, non-existent, unresolvable) falls through to that empty
# default rather than failing closed — this widening is purely additive.
TMPDIR_ALLOW=""
if [ -n "${TMPDIR:-}" ]; then
  case "$TMPDIR" in
    /*)
      if [ -d "$TMPDIR" ] \
        && RESOLVED_TMPDIR=$(resolve_path "$TMPDIR" 2>/dev/null) \
        && RESOLVED_ROOT=$(resolve_path "$ROOT" 2>/dev/null); then
        # Strip exactly one trailing "/" so TMPDIR=/ collapses to "" — an
        # empty TMPDIR_ALLOW is never used as a case pattern prefix below,
        # so this can't accidentally allowlist "/*".
        RESOLVED_TMPDIR="${RESOLVED_TMPDIR%/}"
        if [ "$RESOLVED_TMPDIR" = "$RESOLVED_ROOT" ]; then
          : # TMPDIR resolves to the repo root itself — reject.
        else
          case "$RESOLVED_ROOT" in
            "$RESOLVED_TMPDIR"/*)
              : # TMPDIR resolves to an ancestor of $ROOT — reject (would
                # otherwise allowlist the entire repository).
              ;;
            *)
              case "$RESOLVED_TMPDIR" in
                "$RESOLVED_ROOT"/*)
                  : # TMPDIR resolves to a path INSIDE $ROOT — reject (would
                    # otherwise allowlist a main-worktree subtree, defeating
                    # the guard's purpose).
                  ;;
                *)
                  TMPDIR_ALLOW="$RESOLVED_TMPDIR"
                  ;;
              esac
              ;;
          esac
        fi
      fi
      ;;
  esac
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

# The TMPDIR widening is a separate, guarded case rather than an arm folded
# into the case above (#749): an empty TMPDIR_ALLOW would degrade a bare
# "$TMPDIR_ALLOW"/* pattern to "/*" — allowlisting every absolute path, a
# silent total bypass one refactor away. Keeping it behind its own
# `[ -n "$TMPDIR_ALLOW" ]` guard means a disabled widening can never affect
# the case above. A quoted "$TMPDIR_ALLOW" is matched literally by POSIX sh
# case patterns, so glob metacharacters inside a resolved TMPDIR value
# cannot widen the match beyond an exact path-prefix comparison.
if [ -n "$TMPDIR_ALLOW" ]; then
  case "$FILE_PATH" in
    "$TMPDIR_ALLOW"/*) exit 0 ;;
  esac
fi

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
