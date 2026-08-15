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
# Symmetric out-of-repo-root policy (#1072): BOTH arms below allow an
# absolute target that canonicalizes to somewhere OUTSIDE the repository's
# scope root outright, never even reaching the allowlist/block logic -- this
# guard's purpose is keeping THIS repo's main worktree read-only, not
# blocking every possible Write/Edit/Bash write anywhere on the filesystem.
# This is what lets a Claude Code session maintain its own persistent memory
# (~/.claude/projects/<slug>/memory/...) via Write/Edit, and lets
# /cenci:configure's documented `jq '...' ~/.claude/settings.json >
# ~/.claude/settings.json.tmp` Bash pattern through. Canonicalize-before-decide
# ordering is load-bearing on both arms: the repo-scope decision is always
# made on the CANONICAL form of the target (resolve_path/canonicalize_target,
# parent-anchored for not-yet-existing paths), never the lexical (pre-symlink,
# pre-"..") form -- so a symlink or ".." sequence that lexically starts
# outside the repo but resolves back into it is still caught, and an in-root
# absolute target still falls through to the exact same
# allowlist/TMPDIR-widening/block logic on both arms.
#
# "The repository" is scoped to a shared SCOPE_ROOT (computed once below,
# after the config gate and ROOT resolution), not the literal $ROOT this
# hook's own `git rev-parse --show-toplevel` call returns. In a real linked
# feature worktree (`git worktree add .worktrees/<id>-<desc>`), $ROOT is the
# FEATURE worktree, not the main one -- a literal "outside $ROOT" check would
# therefore allow main-worktree writes outright whenever the session cwd is
# inside a feature worktree, gutting the guard's core purpose. SCOPE_ROOT is
# instead derived from `git rev-parse --git-common-dir`'s parent, adopted only
# when it is a strict ancestor of the resolved $ROOT (the linked-worktree
# case); otherwise SCOPE_ROOT falls back to the resolved $ROOT (the ordinary
# main-worktree-session case, where the two coincide). If the `--git-common-
# dir` derivation itself errors (the git command fails, or its result cannot
# be resolved to an existing directory), SCOPE_ROOT_OK is set to 0 and both
# arms fail closed at their decision site with a "could not be classified"
# message -- never "targets the main worktree", which is reserved for genuine
# main-worktree-targeting blocks and is asserted verbatim by
# flow/tests/parity/contract-lib.sh's drive_worktree_guard. Computing
# SCOPE_ROOT does NOT itself exit, so the guard stays a no-op for calls that
# never reach a decision site (unconfigured repos, non-git dirs, Bash calls
# with no extracted write target).
#
# Accepted residual: whenever the common-dir's parent is NOT a strict
# ancestor of the resolved $ROOT, SCOPE_ROOT falls back to $ROOT and
# main-worktree writes from that session are allowed. A worktree created
# OUTSIDE the main worktree (e.g. `git worktree add ../wt` instead of the
# cenci convention `.worktrees/<id>-<desc>` inside the repo) is the
# illustrative example, but the same fallback also covers `--separate-git-dir`
# layouts, bare repos, and submodules -- any layout where the common-dir
# derivation does not resolve to a genuine ancestor of $ROOT falls into this
# same residual, not just the one worktree-outside-the-repo example. This
# follows directly from the derivation rule above and is deliberate, not a
# bug -- see #1072's Risks.
#
# Deliberate remaining differences between the two arms (unaffected by
# #1072): a non-absolute Write|Edit file_path is always rejected outright,
# never reaching the scope-root decision; an extracted RELATIVE Bash write
# target is rejected per #810 Fix 2, with the apply_patch envelope exception
# per #1036 -- see the "PARSED RELATIVE Bash targets" and "EXCEPTION (#1036)"
# paragraphs immediately below for the full rationale.
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
#
# EXCEPTION (#1036): a relative target declared inside a recognized,
# strictly-shaped apply_patch envelope (bwt_is_apply_patch_payload /
# bwt_apply_patch_targets in lib/bash-write-targets.sh) is resolved against
# this hook's own cwd instead of being rejected. The #810 cwd-distrust
# rationale above does not apply to that shape: the strict envelope
# precondition means no `cd`/`&&`/`;`/pipe/assignment/subshell can appear
# anywhere in the command, so nothing in it can change the effective cwd
# before this hook runs against the same cwd Codex invoked it from.
# Resolution is cwd-join-then-canonicalize (never lexical trust) -- the
# joined path is still fed through the same canonicalize_target the rest of
# this arm uses, so a `..`-escape out of an allowlisted-looking relative
# shape is still caught before the allowlist/repo-scope decision.

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

# SCOPE_ROOT derivation (#1072) -- see the header comment's "Symmetric
# out-of-repo-root policy" paragraph for the full rationale. Computed once
# here, shared by both arms' repo-scope pre-checks below. Every step is
# checked explicitly; no unchecked command substitution on this
# security-critical path (root AGENTS.md). On any derivation error,
# SCOPE_ROOT_OK is set to 0 and this code does NOT exit -- the flag is
# consumed only at the two decision sites (the Write|Edit arm's pre-check and
# the Bash arm's repo-scope pre-check), keeping the guard a no-op for calls
# that never reach a decision site.
SCOPE_ROOT_OK=1
if ! RESOLVED_ROOT=$(resolve_path "$ROOT" 2>/dev/null); then
  SCOPE_ROOT_OK=0
  SCOPE_ROOT="$ROOT"
elif ! SCOPE_CWD=$(pwd) || [ -z "$SCOPE_CWD" ]; then
  SCOPE_ROOT_OK=0
  SCOPE_ROOT="$RESOLVED_ROOT"
else
  SCOPE_ROOT="$RESOLVED_ROOT"
  # Not `-C "$ROOT"`: this process's cwd is already guaranteed to be inside
  # the same repo $ROOT was derived from (the earlier `git -C "$(pwd)"
  # rev-parse --show-toplevel` config-gate call already succeeded from this
  # same cwd, and nothing in this script changes directory), and
  # --git-common-dir's answer does not depend on which subdirectory of the
  # working tree it is run from -- but when its result IS relative, git
  # prints it relative to the CURRENT DIRECTORY, not to $ROOT, so it must be
  # joined against $SCOPE_CWD (never $ROOT) below.
  if GIT_COMMON_DIR=$(git rev-parse --git-common-dir 2>/dev/null); then
    case "$GIT_COMMON_DIR" in
      /*) ;;
      *) GIT_COMMON_DIR="$SCOPE_CWD/$GIT_COMMON_DIR" ;;
    esac
    if RESOLVED_COMMON_DIR=$(resolve_path "$GIT_COMMON_DIR" 2>/dev/null); then
      COMMON_DIR_PARENT="${RESOLVED_COMMON_DIR%/*}"
      [ -z "$COMMON_DIR_PARENT" ] && COMMON_DIR_PARENT="/"
      if RESOLVED_COMMON_PARENT=$(resolve_path "$COMMON_DIR_PARENT" 2>/dev/null); then
        case "$RESOLVED_ROOT" in
          "$RESOLVED_COMMON_PARENT"/*)
            # $RESOLVED_COMMON_PARENT is a strict ancestor of $RESOLVED_ROOT --
            # a real linked feature worktree; adopt the main worktree as scope.
            SCOPE_ROOT="$RESOLVED_COMMON_PARENT"
            ;;
          *)
            # Not a strict ancestor: the ordinary main-worktree-session case
            # (the two coincide), the accepted out-of-tree-worktree residual
            # documented above, or a bare/submodule common-dir layout -- keep
            # SCOPE_ROOT = RESOLVED_ROOT.
            ;;
        esac
      else
        SCOPE_ROOT_OK=0
      fi
    else
      SCOPE_ROOT_OK=0
    fi
  else
    SCOPE_ROOT_OK=0
  fi
fi

# scope_root_blocked_message <path> -- the "could not be classified"
# fail-closed BLOCKED message shared by both arms' decision sites when
# SCOPE_ROOT_OK=0 (#1072). Matches the tone of the existing "could not
# resolve"/"refusing to resolve" carve-out messages above and NEVER says
# "targets the main worktree" -- that phrase is reserved for genuine
# main-worktree-targeting blocks (flow/tests/parity/contract-lib.sh's
# drive_worktree_guard depends on it staying exact).
scope_root_blocked_message() {
  echo "BLOCKED: guard-main-worktree.sh: $1 could not be classified -- the repository's main-worktree scope could not be determined (git rev-parse --git-common-dir failed, or its result could not be resolved to an existing directory)." >&2
}

# scope_precheck <canonical-path> -- the repo-scope pre-check shared by both
# arms' decision sites (#1072): when SCOPE_ROOT_OK=0, blocks and exits 2 via
# scope_root_blocked_message (never returns). Otherwise returns 0 when
# <canonical-path> is equal to, or inside, SCOPE_ROOT (caller falls through
# to the allowlist), or 1 when it is outside (caller allows the target
# outright). A plain return code, not a hardcoded exit/continue here: the two
# call sites need different out-of-scope behavior (the Write|Edit arm exits
# 0; the Bash arm's per-target loop continues to the next target). A quoted
# "$SCOPE_ROOT" is matched literally by POSIX sh case patterns, defending
# against glob metacharacters in the resolved path.
scope_precheck() {
  if [ "$SCOPE_ROOT_OK" -eq 0 ]; then
    scope_root_blocked_message "$1"
    exit 2
  fi
  case "$1" in
    "$SCOPE_ROOT" | "$SCOPE_ROOT"/*) return 0 ;;
    *) return 1 ;;
  esac
}

# scope_self_protection_denied <canonical-path> -- self-protection denylist
# (#1072 Fix 3), checked on BOTH arms but ONLY on the out-of-scope path --
# i.e. only once scope_precheck has already determined the target is OUTSIDE
# SCOPE_ROOT and is about to be allowed through via scope_precheck's
# out-of-repo allow. scope_precheck's out-of-repo allow deliberately lets a
# session write to ANY out-of-repo absolute path (Claude Code's own
# persistent memory, /cenci:configure's documented
# `... > ~/.claude/settings.json.tmp` pattern), but a handful of out-of-repo
# paths are session-security-sensitive regardless of repo scope:
# ~/.claude/settings*.json (hook definitions -- writable here means arbitrary
# command execution on the next tool call), ~/.claude/plugins/** (this hook's
# own script and every other installed plugin -- writable here means
# self-disable), and ~/.ssh/** (SSH config/authorized_keys tampering).
# check-sensitive-files.sh (a separate, unconditional hook) does not cover
# these -- it only matches secret-shaped paths (.env, credentials, *.pem,
# *.key, id_*, keystore shapes), not hook-config or plugin-script paths.
#
# MUST be gated on out-of-scope (regression fixed post-#1072 initial cycle):
# an EARLIER revision ran this check unconditionally, BEFORE scope_precheck.
# That let an IN-SCOPE path matching these shapes -- e.g. a real in-repo
# `<worktree>/.claude/settings.json`, exactly what /cenci:configure writes
# inside its own feature worktree -- get hard-blocked before ever reaching
# the ordinary allowlist/block logic below, breaking that documented flow and
# making a repo's own `.claude/settings.json` unmaintainable via Write/Edit.
# An in-repo path is NOT "essentially never" a real match for these shapes;
# it is the normal case for /cenci:configure. The denylist must therefore run
# strictly after scope_precheck confirms the target is out-of-scope, so an
# in-scope path is completely unaffected by it and falls through to the
# existing allowlist/block logic exactly as before this feature was added.
# Plain glob case patterns (not quoted variable interpolation), matching
# bash_target_allowed's idiom below -- these are literal patterns, not
# variables, so ordinary case globbing is exactly what's wanted here.
#
# Best-effort defense-in-depth, NOT a complete security boundary: it only
# catches direct-redirect/Write shapes against this fixed pattern set. It
# does NOT catch unmodelled Bash write verbs (mv, cp, sed -i, and friends --
# only Bash-arm redirect/tee-shaped targets are extracted at all), a
# symlinked ~/.claude or ~/.ssh directory (the pattern match is purely
# lexical on the canonicalized target path, not a symlink-aware policy
# decision), or every session-security-sensitive path in general --
# ~/.gitconfig, ~/.codex/, and shell rc files (.bashrc, .zshrc, etc.) are
# NOT covered. These are accepted residual gaps for this cycle (#1072
# follow-up), not bugs to close here -- see also the zero-parse backstop's
# own documented residual below for the same "defense-in-depth, not a load-
# bearing guarantee" posture on the Bash arm's unmodelled-construct path.
scope_self_protection_denied() {
  case "$1" in
    */.claude/settings*.json) return 0 ;;
    */.claude/plugins/*) return 0 ;;
    */.ssh/*) return 0 ;;
    *) return 1 ;;
  esac
}

# scope_self_protection_message <path> -- the BLOCKED message shared by both
# arms' self-protection denylist hit (#1072 Fix 3). Deliberately never says
# "targets the main worktree" -- these targets are outside the repository
# entirely; the risk is session self-compromise (hook config, plugin script,
# or SSH config/keys), not a main-worktree write.
scope_self_protection_message() {
  echo "BLOCKED: $1 targets a session-security-sensitive path outside the repository (hook config, plugin script, or SSH config/keys) and is refused regardless of the out-of-repo-scope allow policy." >&2
}

# Compute the canonicalized TMPDIR-widening allowlist prefix once (#749).
# Empty TMPDIR_ALLOW means the widening is disabled; every failure mode below
# (unset, relative, non-existent, unresolvable) falls through to that empty
# default rather than failing closed — this widening is purely additive.
# Computed unconditionally (shared by both arms below), since it depends only
# on $TMPDIR/$SCOPE_ROOT (or $ROOT as its pre-#1072 fallback), never on
# FILE_PATH/TOOL_COMMAND.
#
# #1072 Fix 2: the three containment checks below are measured against
# $SCOPE_ROOT, not $RESOLVED_ROOT, whenever SCOPE_ROOT_OK=1 — in a feature-
# worktree session $RESOLVED_ROOT is the FEATURE worktree, so a TMPDIR
# pointing inside the MAIN worktree (but outside the feature worktree) would
# otherwise pass all three rejections and become TMPDIR_ALLOW, silently
# re-opening main-worktree containment via this widening path on both arms.
# When SCOPE_ROOT_OK=0 (derivation failed), this specific check keeps its
# pre-#1072 behavior unchanged (measured against $RESOLVED_ROOT) — this is
# purely additive widening logic, not a decision site, so it does not itself
# fail closed the way scope_precheck's two call sites do.
TMPDIR_ALLOW=""
if [ -n "${TMPDIR:-}" ]; then
  case "$TMPDIR" in
    /*)
      if [ -d "$TMPDIR" ] \
        && RESOLVED_TMPDIR=$(resolve_path "$TMPDIR" 2>/dev/null) \
        && RESOLVED_ROOT=$(resolve_path "$ROOT" 2>/dev/null); then
        if [ "$SCOPE_ROOT_OK" -eq 1 ]; then
          TMPDIR_CONTAINMENT_ROOT="$SCOPE_ROOT"
        else
          TMPDIR_CONTAINMENT_ROOT="$RESOLVED_ROOT"
        fi
        # Strip exactly one trailing "/" so TMPDIR=/ collapses to "" — an
        # empty TMPDIR_ALLOW is never used as a case pattern prefix below,
        # so this can't accidentally allowlist "/*".
        RESOLVED_TMPDIR="${RESOLVED_TMPDIR%/}"
        if [ "$RESOLVED_TMPDIR" = "$TMPDIR_CONTAINMENT_ROOT" ]; then
          : # TMPDIR resolves to the repo scope root itself — reject.
        else
          case "$TMPDIR_CONTAINMENT_ROOT" in
            "$RESOLVED_TMPDIR"/*)
              : # TMPDIR resolves to an ancestor of the repo scope root —
                # reject (would otherwise allowlist the entire repository).
              ;;
            *)
              case "$RESOLVED_TMPDIR" in
                "$TMPDIR_CONTAINMENT_ROOT"/*)
                  : # TMPDIR resolves to a path INSIDE the repo scope root —
                    # reject (would otherwise allowlist a main-worktree
                    # subtree, defeating the guard's purpose).
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

  # Repo-scope pre-check (#1072): mirrors the Bash arm's pre-check below --
  # see the header comment's "Symmetric out-of-repo-root policy" paragraph and
  # the scope_precheck() helper above. A target outside SCOPE_ROOT is allowed
  # outright, before ever reaching the allowlist. Made on the CANONICAL
  # $FILE_PATH computed above, never the lexical form.
  #
  # Self-protection denylist (#1072 Fix 3, gated post-regression-fix): only
  # consulted HERE, in the out-of-scope branch, immediately before the target
  # would otherwise be allowed through outright. An in-scope $FILE_PATH (e.g.
  # this worktree's own <worktree>/.claude/settings.json) never reaches this
  # branch at all -- it falls straight through to the ordinary allowlist/block
  # logic below, completely unaffected by the denylist. See
  # scope_self_protection_denied() above for the full rationale.
  if ! scope_precheck "$FILE_PATH"; then
    if scope_self_protection_denied "$FILE_PATH"; then
      scope_self_protection_message "$FILE_PATH"
      exit 2
    fi
    exit 0 # outside the repository entirely -- allowed (#1072).
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
  # Bash arm (#795): see the header comment's "Symmetric out-of-repo-root
  # policy" (#1072) note for the SCOPE_ROOT scoping rationale, shared with the
  # Write|Edit arm above.
  LIB_DIR="${0%/*}"
  [ "$LIB_DIR" = "$0" ] && LIB_DIR="."
  BWT_LIB="$LIB_DIR/lib/bash-write-targets.sh"
  if [ ! -r "$BWT_LIB" ]; then
    echo "BLOCKED: guard-main-worktree.sh could not find its required helper library ($BWT_LIB) to inspect this Bash command's write targets." >&2
    exit 2
  fi
  # shellcheck source=./lib/bash-write-targets.sh
  . "$BWT_LIB"

  # apply_patch envelope recognition (#1036): branches BEFORE the ordinary
  # bwt_has_write_candidate early-exit so the shell tokenizer never sees the
  # diff body. See the header's "PARSED RELATIVE Bash targets" paragraph for
  # the strict-shape precondition this unlocks a cwd-join resolution
  # exception for.
  BWT_APPLY_PATCH=0
  if bwt_is_apply_patch_payload "$TOOL_COMMAND"; then
    BWT_AP_TARGETS=$(bwt_apply_patch_targets "$TOOL_COMMAND")
    BWT_AP_STATUS=$?
    case "$BWT_AP_STATUS" in
      0)
        BWT_APPLY_PATCH=1
        BWT_TARGETS="$BWT_AP_TARGETS"
        ;;
      9)
        # Not the strict shape (shell-composed) -- fall through unchanged to
        # the existing bwt_has_write_candidate/bwt_extract_targets path below.
        BWT_APPLY_PATCH=0
        ;;
      3)
        echo "BLOCKED: guard-main-worktree.sh requires awk (via lib/bash-write-targets.sh) to inspect this apply_patch envelope's write targets, but awk was not found on PATH." >&2
        exit 2
        ;;
      8)
        # #1036 Fix 6: never interpolate the full $TOOL_COMMAND here -- for a
        # recognized apply_patch envelope it IS the diff body, which may
        # itself carry the exact secret material a caller upstream refused
        # to write, and echoing it into a BLOCKED stderr message would
        # surface it into hook transcripts/logs. Name only the first line
        # (the sentinel/wrapper line, never body content) via
        # bwt_ap_first_line.
        BWT_AP_FIRST_LINE=$(bwt_ap_first_line "$TOOL_COMMAND")
        {
          echo "BLOCKED: guard-main-worktree.sh recognized this Bash command as an apply_patch envelope (starting: $BWT_AP_FIRST_LINE), but it does not parse."
          echo "Only two shapes are accepted: bare '*** Begin Patch' ... '*** End Patch' text, or that same text wrapped in an apply_patch heredoc whose delimiter does not expand -- apply_patch <<'DELIM', <<\"DELIM\", <<\\DELIM, or their <<- variants."
        } >&2
        exit 2
        ;;
      *)
        echo "BLOCKED: guard-main-worktree.sh could not classify this Bash command's apply_patch envelope status (unexpected internal state)." >&2
        exit 2
        ;;
    esac
  fi

  if [ "$BWT_APPLY_PATCH" -eq 0 ]; then
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
  fi
  if ! RESOLVED_ROOT=$(resolve_path "$ROOT" 2>/dev/null); then
    echo "BLOCKED: guard-main-worktree.sh could not resolve the repo root ($ROOT) to scope this Bash command's write targets." >&2
    exit 2
  fi
  # apply_patch relative-target resolution (#1036): computed once,
  # unconditionally (cheap), used only by the relative-target case arm below
  # when BWT_APPLY_PATCH=1. Checked explicitly -- never unchecked command
  # substitution on this security-critical path (root AGENTS.md).
  if ! BWT_CWD=$(pwd) || [ -z "$BWT_CWD" ]; then
    echo "BLOCKED: guard-main-worktree.sh could not determine its own working directory to resolve a relative apply_patch target." >&2
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
  #
  # Gated on BWT_APPLY_PATCH=0 (#1036): once a bare apply_patch envelope was
  # successfully recognized and parsed, BWT_TARGETS already holds its real
  # declared targets (never empty on that success path), so this backstop
  # must never re-scan the diff BODY text for arrow/tee-shaped noise
  # (`>>`, `=>`, `List<Foo>`, "committee", ...) that is not a real write.
  if [ "$BWT_APPLY_PATCH" -eq 0 ] && [ -z "$BWT_TARGETS" ]; then
    if bwt_has_delimited_tee "$TOOL_COMMAND"; then
      {
        echo "BLOCKED: guard-main-worktree.sh could not extract this Bash command's write targets (unmodelled construct containing a tee invocation); its target may be relative to the main worktree: $TOOL_COMMAND"
        echo "Rewrite the command using a plain, directly-parseable tee form targeting the feature worktree (.worktrees/<id>-<desc>/) or a temp path."
      } >&2
      exit 2
    elif bwt_zero_parse_suspicious "$TOOL_COMMAND"; then
      # #1072 Fix 1: $SCOPE_ROOT is added to the roots[] array (and the
      # BWT_SCAN_TEXT match below), guarded on SCOPE_ROOT_OK=1, ADDITIVE to
      # $ROOT/$RESOLVED_ROOT (never a replacement) -- in a feature-worktree
      # session an unparseable/unmodeled Bash write whose raw text names the
      # MAIN worktree path (not the feature worktree $ROOT/$RESOLVED_ROOT)
      # previously escaped this backstop entirely, the same Q1 hole
      # scope_precheck() closes for parsed targets, left open here. When
      # SCOPE_ROOT_OK=0, BWT_SCAN_SCOPE_ROOT stays empty and is never added
      # as a root -- an empty/garbage value must never widen the scan.
      BWT_SCAN_SCOPE_ROOT=""
      [ "$SCOPE_ROOT_OK" -eq 1 ] && BWT_SCAN_SCOPE_ROOT="$SCOPE_ROOT"
      BWT_SCAN_TEXT="$TOOL_COMMAND"
      if command -v awk >/dev/null 2>&1; then
        BWT_SCAN_TEXT=$(BWT_SCAN_CMD="$TOOL_COMMAND" BWT_SCAN_ROOT="$ROOT" BWT_SCAN_RROOT="$RESOLVED_ROOT" BWT_SCAN_SROOT="$BWT_SCAN_SCOPE_ROOT" awk '
          BEGIN {
            s = ENVIRON["BWT_SCAN_CMD"]
            nr = 0
            roots[++nr] = ENVIRON["BWT_SCAN_ROOT"]
            roots[++nr] = ENVIRON["BWT_SCAN_RROOT"]
            if (ENVIRON["BWT_SCAN_SROOT"] != "")
              roots[++nr] = ENVIRON["BWT_SCAN_SROOT"]
            subs[1] = "/.worktrees"
            subs[2] = "/.plans"
            subs[3] = "/.claude/plans"
            nl = sprintf("%c", 10)
            for (r = 1; r <= nr; r++)
              for (p = 1; p <= 3; p++) {
                t = roots[r] subs[p]
                while ((idx = index(s, t)) > 0)
                  s = substr(s, 1, idx - 1) nl substr(s, idx + length(t))
              }
            print s
          }')
      fi
      BWT_SCAN_HIT=0
      case "$BWT_SCAN_TEXT" in
        *"$ROOT"* | *"$RESOLVED_ROOT"*) BWT_SCAN_HIT=1 ;;
      esac
      if [ "$BWT_SCAN_HIT" -eq 0 ] && [ "$SCOPE_ROOT_OK" -eq 1 ]; then
        case "$BWT_SCAN_TEXT" in
          *"$SCOPE_ROOT"*) BWT_SCAN_HIT=1 ;;
        esac
      fi
      if [ "$BWT_SCAN_HIT" -eq 1 ]; then
        {
          echo "BLOCKED: guard-main-worktree.sh could not extract this Bash command's write targets (unmodelled shell construct), and the raw command text mentions the main worktree root ($RESOLVED_ROOT): $TOOL_COMMAND"
          echo "Rewrite the command using a plain, directly-parseable redirect (>, >>) or tee form targeting the feature worktree (.worktrees/<id>-<desc>/) or a temp path."
        } >&2
        exit 2
      fi
    fi
    exit 0
  fi

  while IFS= read -r BWT_TARGET; do
    [ -z "$BWT_TARGET" ] && continue

    bwt_is_exempt_device "$BWT_TARGET" && continue

    # #1036: an apply_patch declared path is the literal, verbatim payload
    # text -- under a quoted heredoc delimiter (the only form recognized) the
    # body is never shell-expanded, so expanding $HOME/$TMPDIR/etc. here
    # would misreport what the tool actually writes to.
    if [ "$BWT_APPLY_PATCH" -eq 1 ]; then
      BWT_EXPANDED="$BWT_TARGET"
    else
      BWT_EXPANDED=$(bwt_expand_safe_vars "$BWT_TARGET")
    fi

    # #1045 review, Fix 9: which unresolved-test applies depends on the SOURCE
    # of the target. bwt_is_unresolved models shell-expansion residue, which
    # only the tokenizer path can have; an apply_patch declared path comes out
    # of a never-expanded (non-expanding-delimiter) heredoc body, so `$`, a
    # backtick, and `(` in it are ordinary filename characters and blocking on
    # them rejected legitimate writes like `reports/summary (1).csv`. See
    # bwt_is_unresolved_literal for what stays fail-closed on that path.
    BWT_UNRESOLVED=0
    if [ "$BWT_APPLY_PATCH" -eq 1 ]; then
      bwt_is_unresolved_literal "$BWT_EXPANDED" && BWT_UNRESOLVED=1
    else
      bwt_is_unresolved "$BWT_EXPANDED" && BWT_UNRESOLVED=1
    fi

    if [ "$BWT_UNRESOLVED" -eq 1 ]; then
      # #1036 second review, Fix A: this per-target loop runs for BOTH the
      # apply_patch and ordinary-tokenizer sources of BWT_TARGETS, but only
      # the tokenizer's $TOOL_COMMAND is an ordinary shell command safe to
      # echo in full -- for a recognized apply_patch envelope (BWT_APPLY_PATCH
      # =1) $TOOL_COMMAND IS the diff body and may itself carry the exact
      # secret material (e.g. a .env file's contents) a caller upstream just
      # refused to write (the same leak vector Fix 6 closed for the exit-8
      # parse-failure message, but reachable here too: an ordinary declared
      # filename containing '$', a backtick, or '(' -- e.g.
      # `reports/summary (1).csv` -- reaches this branch on the SUCCESS path,
      # no malformed envelope required). Name only the first line via
      # bwt_ap_first_line in that case; the ordinary tokenizer path
      # (BWT_APPLY_PATCH=0) is unaffected and keeps the full $TOOL_COMMAND,
      # which was never the leak vector and remains useful context there.
      if [ "$BWT_APPLY_PATCH" -eq 1 ]; then
        BWT_MSG_CMD=$(bwt_ap_first_line "$TOOL_COMMAND")
      else
        BWT_MSG_CMD="$TOOL_COMMAND"
      fi
      {
        echo "BLOCKED: guard-main-worktree.sh cannot resolve the Bash write target '$BWT_EXPANDED' in: $BWT_MSG_CMD"
        echo "Use a literal absolute path, or one of \${TMPDIR:-/tmp}, \$TMPDIR, \$HOME, \$PWD."
      } >&2
      exit 2
    fi

    case "$BWT_EXPANDED" in
      /*)
        BWT_ABS="$BWT_EXPANDED"
        ;;
      *)
        if [ "$BWT_APPLY_PATCH" -eq 1 ]; then
          # Relative-target exception for a recognized apply_patch envelope
          # (#1036): under the strict bare-envelope shape, no `cd`/`&&`/`;`/
          # pipe/assignment/subshell can appear at all -- the #810 cwd-
          # distrust rationale below does not apply, since the command
          # cannot itself change the effective cwd before this hook runs.
          # Resolution is cwd-join-then-canonicalize, never lexical trust.
          BWT_ABS="$BWT_CWD/$BWT_EXPANDED"
        else
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
        fi
        ;;
    esac

    if ! BWT_CANON=$(canonicalize_target "$BWT_ABS"); then
      echo "BLOCKED: guard-main-worktree.sh refusing to resolve Bash write target $BWT_ABS: an ancestor component is an unresolvable symlink (dangling target or symlink loop)." >&2
      exit 2
    fi

    # Repo-scope pre-check (#1072, formerly Q1a): a target outside SCOPE_ROOT
    # is allowed outright -- see the header comment's "Symmetric
    # out-of-repo-root policy" note and the scope_precheck() helper above,
    # shared with the Write|Edit arm.
    #
    # Self-protection denylist (#1072 Fix 3, gated post-regression-fix):
    # mirrored from the Write|Edit arm above -- only consulted HERE, in the
    # out-of-scope branch, immediately before this target would otherwise be
    # allowed through via `continue`. An in-scope $BWT_CANON never reaches
    # this branch -- it falls straight through to bash_target_allowed below,
    # completely unaffected by the denylist. See scope_self_protection_denied()
    # above for the full rationale.
    if ! scope_precheck "$BWT_CANON"; then
      if scope_self_protection_denied "$BWT_CANON"; then
        scope_self_protection_message "$BWT_CANON"
        exit 2
      fi
      continue # outside the repository entirely -- allowed (#1072).
    fi

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
