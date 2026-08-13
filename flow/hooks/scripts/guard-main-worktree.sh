#!/bin/sh
# PreToolUse hook: keep the main worktree read-only for Write/Edit and for
# Bash redirection/tee write targets (#795) — only in repos configured for
# cenci (gated on neutral config with legacy fallback).
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
#
# Bash arm asymmetry (#795, Q1a): the Bash arm below is DELIBERATELY more
# permissive than the Write|Edit arm above, for ABSOLUTE targets only (#810).
# An absolute Bash write target that canonicalizes to somewhere OUTSIDE the
# resolved repo root is allowed outright, never even reaching the
# allowlist/block logic -- this guard's purpose is keeping THIS repo's main
# worktree read-only, not blocking every possible Bash write anywhere on the
# filesystem. This is what lets /cenci:configure's documented `jq '...'
# ~/.claude/settings.json > ~/.claude/settings.json.tmp` pattern through. An
# in-root absolute Bash target still falls through to the exact same
# allowlist/TMPDIR-widening/block logic as the Write|Edit arm.
#
# PARSED RELATIVE Bash targets get their own, separate policy (#810 Fix 2):
# this hook process's cwd is not trustworthy evidence of where a relative
# target actually resolves, since the command text itself can `cd` first.
# Even a lexically allowlisted shape can resolve under the wrong directory
# or traverse a symlink in the command's effective cwd. Extracted relative
# targets are therefore rejected; callers must use an absolute target so it
# can be canonicalized before the allowlist/repo-scope decision. Constructs
# that produce zero extracted targets retain the bounded backstop/residual
# documented in the Bash-arm comment below and adapter-contract.md.

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

# Dual-mode extraction (#795): a single jq call extracts EITHER the
# Write|Edit file_path/filePath field OR, when neither is present, the Bash
# tool_input.command string. Two logical values are printed, one per line:
# line 1 is the resolved file_path/filePath (or empty string), and every
# remaining line (2 onward, rejoined with newlines) is tool_input.command (or
# empty) — rejoining rather than taking a single line 2 means an embedded
# literal newline inside a multi-line Bash command is never truncated.
# Explicit emptiness check, not a bare `//` chain: jq's `//` only falls back on
# null/false and would treat a present-but-empty file_path ("") as truthy,
# short-circuiting past filePath to the empty-path allow below.
if ! JQ_OUT=$(printf '%s' "$INPUT" | jq -r '
    (if (.tool_input.file_path // "") != "" then
       .tool_input.file_path
     elif (.tool_input.filePath // "") != "" then
       .tool_input.filePath
     else
       ""
     end),
    (.tool_input.command // "")
  ' 2>&9); then
  echo "BLOCKED: guard-main-worktree.sh could not parse the tool call's JSON input: $(jq_err_detail)" >&2
  jq_err_cleanup
  exit 2
fi
jq_err_cleanup

# Split JQ_OUT's two logical values without depending on sed (kept out of
# this guard's tool dependency footprint): `read` (a shell builtin) peels off
# line 1, then `cat` passes the remainder straight through, preserving any
# embedded newlines in a multi-line Bash command.
FILE_PATH=$(printf '%s\n' "$JQ_OUT" | { IFS= read -r JQ_LINE1; printf '%s' "$JQ_LINE1"; })
TOOL_COMMAND=$(printf '%s\n' "$JQ_OUT" | { IFS= read -r JQ_DISCARD; cat; })

[ -z "$FILE_PATH" ] && [ -z "$TOOL_COMMAND" ] && exit 0

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

# canonicalize_target <path> -- parent-anchored canonicalization shared by
# the Bash arm below (#795): prints the canonical form of an absolute path
# to stdout and returns 0, reusing resolve_path/lexical_collapse exactly like
# the Write|Edit arm's own inline ancestor walk below (never reimplemented).
# Returns 1 (prints nothing) when a path component is an unresolvable
# symlink (dangling target or ELOOP cycle) or resolve_path itself fails.
canonicalize_target() {
  _ct_path="$1"
  if [ -e "$_ct_path" ]; then
    resolve_path "$_ct_path" 2>/dev/null || return 1
    return 0
  fi
  _ct_ancestor="$_ct_path"
  _ct_tail=""
  while [ -n "$_ct_ancestor" ] && [ ! -d "$_ct_ancestor" ]; do
    if [ -L "$_ct_ancestor" ] && [ ! -e "$_ct_ancestor" ]; then
      return 1
    fi
    _ct_seg="${_ct_ancestor##*/}"
    if [ -z "$_ct_tail" ]; then
      _ct_tail="$_ct_seg"
    else
      _ct_tail="$_ct_seg/$_ct_tail"
    fi
    _ct_ancestor="${_ct_ancestor%/*}"
  done
  [ -z "$_ct_ancestor" ] && _ct_ancestor="/"
  _ct_resolved_ancestor=$(resolve_path "$_ct_ancestor" 2>/dev/null) || return 1
  lexical_collapse "$_ct_resolved_ancestor/$_ct_tail"
}

# Compute the canonicalized TMPDIR-widening allowlist prefix once (#749).
# Empty TMPDIR_ALLOW means the widening is disabled; every failure mode below
# (unset, relative, non-existent, unresolvable) falls through to that empty
# default rather than failing closed — this widening is purely additive.
# Computed unconditionally (shared by both arms below), since it depends only
# on $TMPDIR/$ROOT, never on FILE_PATH/TOOL_COMMAND.
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

# bash_target_allowed <canonical-path> -- the same allowlist/TMPDIR-widening
# logic the Write|Edit arm applies inline below, factored out so the Bash
# arm's per-target loop can call it once per target without an early `exit`
# short-circuiting the rest of the command's targets. Returns 0 (allowed) or
# 1 (falls through to a block).
bash_target_allowed() {
  _bt_path="$1"
  case "$_bt_path" in
    # Feature worktrees — the intended write target
    */.worktrees/*) return 0 ;;
    # Plan persistence (implement Phase 1)
    */.plans/*) return 0 ;;
    # Claude Code native Plan Mode storage
    */.claude/plans/*) return 0 ;;
    # Temp paths: body files, context bundles, attachments, scratchpads
    /tmp/* | /private/tmp/* | /var/folders/* | */cenci-attachments-*/*) return 0 ;;
    # Design artifacts live in the main worktree by design (/cenci:design)
    *.pen | */DESIGN.md | */designs/*) return 0 ;;
  esac
  # TMPDIR widening (#749) — see the guard above for the full rationale. A
  # quoted "$TMPDIR_ALLOW" is matched literally by POSIX sh case patterns, so
  # glob metacharacters inside a resolved TMPDIR value cannot widen the match.
  if [ -n "$TMPDIR_ALLOW" ]; then
    case "$_bt_path" in
      "$TMPDIR_ALLOW"/*) return 0 ;;
    esac
  fi
  return 1
}

if [ -n "$FILE_PATH" ]; then
  case "$FILE_PATH" in
    /*) ;;
    *)
      echo "BLOCKED: guard-main-worktree.sh received a non-absolute file_path: $FILE_PATH" >&2
      exit 2
      ;;
  esac

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
elif [ -n "$TOOL_COMMAND" ]; then
  # Bash arm (#795): see the header comment's "Bash arm asymmetry" note for
  # the Q1a repo-root scoping rationale (deliberately more permissive than
  # the Write|Edit arm above for out-of-repo-root targets).
  LIB_DIR="${0%/*}"
  [ "$LIB_DIR" = "$0" ] && LIB_DIR="."
  BWT_LIB="$LIB_DIR/lib/bash-write-targets.sh"
  if [ ! -r "$BWT_LIB" ]; then
    echo "BLOCKED: guard-main-worktree.sh could not find its required helper library ($BWT_LIB) to inspect this Bash command's write targets." >&2
    exit 2
  fi
  # shellcheck source=./lib/bash-write-targets.sh
  . "$BWT_LIB"

  bwt_has_write_candidate "$TOOL_COMMAND" || exit 0

  # bwt_extract_targets fails closed (non-zero, no output) when awk is
  # missing from PATH, when the command is too long to safely inspect (#795
  # round 2, Fix D), or when wc is missing from PATH (#795 round 3, needed by
  # the length pre-check) -- an empty result in any of these cases must never
  # be treated as "no write targets found" (which would silently allow the
  # command). These failure modes get distinct exit codes (3, 4, 5, 6, 7) from
  # bwt_extract_targets so the message here is accurate rather than
  # misreporting one failure as another.
  BWT_TARGETS=$(bwt_extract_targets "$TOOL_COMMAND")
  BWT_EXTRACT_STATUS=$?
  if [ "$BWT_EXTRACT_STATUS" -ne 0 ]; then
    if [ "$BWT_EXTRACT_STATUS" -eq 4 ]; then
      echo "BLOCKED: guard-main-worktree.sh: command too long to inspect for write targets (bwt_extract_targets)." >&2
    elif [ "$BWT_EXTRACT_STATUS" -eq 5 ]; then
      echo "BLOCKED: guard-main-worktree.sh requires wc (via lib/bash-write-targets.sh) to inspect this Bash command's write targets, but wc was not found on PATH." >&2
    elif [ "$BWT_EXTRACT_STATUS" -eq 6 ]; then
      # #810 Fix 1: an unquoted brace expansion ({a,b}, {1..3}) in
      # write-target position is not supported for direct extraction --
      # handing back the raw un-expanded literal would misreport what bash
      # actually writes to (possibly a main-worktree target).
      echo "BLOCKED: guard-main-worktree.sh could not inspect this Bash command's write targets: it contains an unquoted brace expansion (e.g. {a,b} or {1..3}) in write-target position, which is not supported for direct extraction: $TOOL_COMMAND" >&2
      echo "Rewrite the command without brace expansion (e.g. one command per target) targeting the feature worktree (.worktrees/<id>-<desc>/) or edit manually if needed." >&2
    elif [ "$BWT_EXTRACT_STATUS" -eq 7 ]; then
      # #810 stabilization: a command/process/function substitution appears
      # inside a double-quoted string within a comparison construct
      # (`[[ ]]`/`(( ))`), which cannot be safely resolved -- this tokenizer
      # has no safe way to resume precise parsing through the real syntax of
      # such a substitution once it is wrapped in an outer double-quoted
      # string (possibly hiding a main-worktree target), so the whole command
      # fails closed unconditionally.
      echo "BLOCKED: guard-main-worktree.sh could not inspect this Bash command's write targets: a command/process/function substitution appears inside a double-quoted string within a comparison construct, which cannot be safely resolved: $TOOL_COMMAND" >&2
      echo "Rewrite the command without a double-quoted nested substitution inside [[ ]] / (( )) (e.g. hoist it to its own statement first) targeting the feature worktree (.worktrees/<id>-<desc>/) or edit manually if needed." >&2
    else
      echo "BLOCKED: guard-main-worktree.sh requires awk (via lib/bash-write-targets.sh) to inspect this Bash command's write targets, but awk was not found on PATH." >&2
    fi
    exit 2
  fi
  if ! RESOLVED_ROOT=$(resolve_path "$ROOT" 2>/dev/null); then
    echo "BLOCKED: guard-main-worktree.sh could not resolve the repo root ($ROOT) to scope this Bash command's write targets." >&2
    exit 2
  fi

  # Empty-parse backstop (#795): zero extracted targets is byte-identical
  # for "this command genuinely writes nothing" and "the tokenizer met a
  # construct it does not model". When the command still looks like it might
  # write (bwt_zero_parse_suspicious: any `>`, or a delimited `tee` token),
  # scan the RAW command text for an occurrence of the repo root (raw or
  # resolved form) and block on a hit — this guard's decision axis is path
  # containment under $RESOLVED_ROOT, so a root mention in an unparseable
  # write-shaped command is the marker equivalent of check-sensitive-files'
  # check_sensitive_raw. Defence in depth, not the load-bearing fix: a
  # truncated-but-non-empty parse here still resolves in-root and blocks
  # (path containment degrades safe), unlike the filename-glob guard.
  #
  # Mentions of the always-allowlisted subtrees (<root>/.worktrees,
  # <root>/.plans, <root>/.claude/plans) are neutralized (replaced with a
  # newline, which can never glue adjacent text back into a root match)
  # before the scan, so a bread-and-butter command such as
  # `git -C <root>/.worktrees/<id> commit -m "a -> b"` is not blocked merely
  # because its quoted `>` and allowlisted absolute path produce zero
  # extracted targets. The neutralization runs in awk (index/substr, no
  # regex — root text must match literally); if awk is missing the raw text
  # is scanned un-neutralized (fail closed: over-block, never allow).
  #
  # #810 Fix 2: a zero-parse command containing a DELIMITED tee token
  # (bwt_has_delimited_tee) blocks UNCONDITIONALLY, before the root-mention
  # scan ever runs — the absence of a literal repo-root string in the raw
  # text is not sufficient evidence a tee's target is out-of-root, since the
  # target may be RELATIVE (e.g. `{tee,cat} AGENTS.md` brace-expands to a
  # `tee` invocation writing a file literally named "AGENTS.md", with no root
  # string anywhere in the command for the scan below to find). Only once
  # this narrower, more certain signal is ruled out does the broader `>`-or-
  # delimited-tee `bwt_zero_parse_suspicious` scan run (unchanged from
  # before).
  #
  # An unmodelled construct costs a false positive, never a silent permit; a
  # RELATIVE in-root target inside such a construct (no root string to match)
  # is an accepted residual of the root-mention scan below (closed by the
  # unconditional tee check above for the tee case specifically).
  if [ -z "$BWT_TARGETS" ]; then
    if bwt_has_delimited_tee "$TOOL_COMMAND"; then
      {
        echo "BLOCKED: guard-main-worktree.sh could not extract this Bash command's write targets (unmodelled construct containing a tee invocation); its target may be relative to the main worktree: $TOOL_COMMAND"
        echo "Rewrite the command using a plain, directly-parseable tee form targeting the feature worktree (.worktrees/<id>-<desc>/) or a temp path."
      } >&2
      exit 2
    elif bwt_zero_parse_suspicious "$TOOL_COMMAND"; then
      BWT_SCAN_TEXT="$TOOL_COMMAND"
      if command -v awk >/dev/null 2>&1; then
        BWT_SCAN_TEXT=$(BWT_SCAN_CMD="$TOOL_COMMAND" BWT_SCAN_ROOT="$ROOT" BWT_SCAN_RROOT="$RESOLVED_ROOT" awk '
          BEGIN {
            s = ENVIRON["BWT_SCAN_CMD"]
            roots[1] = ENVIRON["BWT_SCAN_ROOT"]
            roots[2] = ENVIRON["BWT_SCAN_RROOT"]
            subs[1] = "/.worktrees"
            subs[2] = "/.plans"
            subs[3] = "/.claude/plans"
            nl = sprintf("%c", 10)
            for (r = 1; r <= 2; r++)
              for (p = 1; p <= 3; p++) {
                t = roots[r] subs[p]
                while ((idx = index(s, t)) > 0)
                  s = substr(s, 1, idx - 1) nl substr(s, idx + length(t))
              }
            print s
          }')
      fi
      case "$BWT_SCAN_TEXT" in
        *"$ROOT"* | *"$RESOLVED_ROOT"*)
          {
            echo "BLOCKED: guard-main-worktree.sh could not extract this Bash command's write targets (unmodelled shell construct), and the raw command text mentions the main worktree root ($RESOLVED_ROOT): $TOOL_COMMAND"
            echo "Rewrite the command using a plain, directly-parseable redirect (>, >>) or tee form targeting the feature worktree (.worktrees/<id>-<desc>/) or a temp path."
          } >&2
          exit 2
          ;;
      esac
    fi
    exit 0
  fi

  while IFS= read -r BWT_TARGET; do
    [ -z "$BWT_TARGET" ] && continue

    bwt_is_exempt_device "$BWT_TARGET" && continue

    BWT_EXPANDED=$(bwt_expand_safe_vars "$BWT_TARGET")

    if bwt_is_unresolved "$BWT_EXPANDED"; then
      {
        echo "BLOCKED: guard-main-worktree.sh cannot resolve the Bash write target '$BWT_EXPANDED' in: $TOOL_COMMAND"
        echo "Use a literal absolute path, or one of \${TMPDIR:-/tmp}, \$TMPDIR, \$HOME, \$PWD."
      } >&2
      exit 2
    fi

    case "$BWT_EXPANDED" in
      /*)
        BWT_ABS="$BWT_EXPANDED"
        ;;
      *)
        # Relative-target policy (#810 Fix 2): a relative Bash write target
        # cannot be trusted to resolve against this hook process's cwd --
        # the command text itself may `cd` to a different directory before
        # the write actually executes. Lexical shape is not sufficient
        # either: `.plans/x` can resolve under a subdirectory rather than
        # the repo root, and an allowlisted-looking relative path can be a
        # symlink to protected source.
        {
          echo "BLOCKED: guard-main-worktree.sh cannot verify the relative Bash write target '$BWT_EXPANDED' in: $TOOL_COMMAND"
          echo "The relative write target cannot be verified against the command's effective cwd or canonicalized safely; use an absolute feature-worktree, plan, design, or temp path."
        } >&2
        exit 2
        ;;
    esac

    if ! BWT_CANON=$(canonicalize_target "$BWT_ABS"); then
      echo "BLOCKED: guard-main-worktree.sh refusing to resolve Bash write target $BWT_ABS: an ancestor component is an unresolvable symlink (dangling target or symlink loop)." >&2
      exit 2
    fi

    # Repo-root scope pre-check (Q1a): a target outside the resolved repo
    # root is allowed outright -- see the header comment's "Bash arm
    # asymmetry" note.
    case "$BWT_CANON" in
      "$RESOLVED_ROOT" | "$RESOLVED_ROOT"/*)
        : # equal to, or inside, the repo root -- falls through below.
        ;;
      *)
        continue # outside the repo root entirely -- allowed (Q1a).
        ;;
    esac

    if ! bash_target_allowed "$BWT_CANON"; then
      {
        echo "BLOCKED: Bash write to $BWT_CANON targets the main worktree, not a feature worktree."
        echo ""
        echo "THE ONLY CORRECT FIX: redirect to an absolute path rooted at the feature"
        echo "worktree — under .worktrees/<id>-<desc>/ — or to a \${TMPDIR:-/tmp} path."
        echo "If you don't know the worktree path, run 'git worktree list' and use the"
        echo ".worktrees/<id>-<desc> entry."
      } >&2
      exit 2
    fi
  done <<BWT_TARGETS_EOF
$BWT_TARGETS
BWT_TARGETS_EOF

  exit 0
else
  exit 0
fi
