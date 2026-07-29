#!/bin/sh
# bash-write-targets.sh -- shared POSIX-sh library backing the PreToolUse
# Bash arms of check-sensitive-files.sh and guard-main-worktree.sh (#795).
# Meant to be `.`-sourced, never executed directly -- it defines functions
# only, has no side effects of its own, and depends on nothing but plain
# POSIX shell builtins (no jq, no external tools, no bash-isms).
#
# Extraction surface (deliberately narrow, matching the ticket's stated
# detection surface): output redirections (`>`, `>>`, `>|`), fd-prefixed and
# combined forms of the same operators (`1>`, `2>`, `&>`, `&>>`, `>&`), and
# every `tee`/`tee -a` operand. Explicitly NOT `cp`, `mv`, `dd of=`, `sed -i`,
# `truncate`, input redirection (`<`), or "any unquoted `>` unless
# allowlisted" -- those are out of scope by design (see ticket #795's
# "Decisions settled during refinement").
#
# Known limitation: a `cat <<EOF` heredoc body containing an unquoted `>` is
# tokenized as a real redirect operator and can produce a spurious
# unresolved-target block, since this tokenizer has no concept of heredoc
# bodies. flow's own conventions already route body text through
# `--body-file` (flow/skills/shell-rules/SKILL.md) rather than heredocs, so
# the documented pipeline is unaffected; callers fail closed (block, naming
# the target) rather than silently misparsing.
#
# Threat model and accepted residuals (#795 — read before "fixing" a missing
# construct; this is not an exhaustive bypass list, only the known ones):
#
#   The tokenizer is a PRECISION layer, not a completeness proof. Full shell
#   grammar is deliberately not modelled: heredoc bodies and arbitrary
#   metacharacter gluing tokenize to something that is not a recognized write
#   construct. For those, the guarantee is made by the callers' empty-parse
#   backstop (bwt_zero_parse_suspicious below): when a command looked like it
#   might write (a `>` anywhere, or a delimited `tee` token) but the tokenizer
#   extracted ZERO targets, both guards fall back to scanning the RAW command
#   text for their own sensitive markers and block on a hit. An unmodelled
#   construct therefore costs a false positive (an over-block the agent can
#   rephrase around), never a silent permit. Do not add per-construct parser
#   fixes to chase completeness — that loop does not converge (#795 review
#   rounds 1-5); the backstop is the terminating design.
#
#   Unquoted brace expansion in WRITE-TARGET position (`tee f{1,2}`,
#   `> f{1..3}`) is no longer a backstop-only case (#810): it is caught
#   directly at extraction and fails the whole call closed with exit code 6
#   (see bwt_extract_targets below), since silently handing back the raw
#   un-expanded literal as "the" target would misreport what bash would
#   actually write to (one word, multiplexed by the shell into several real
#   targets). The remaining residual is brace expansion in COMMAND position
#   for a non-tee verb (e.g. `{cp,x} f` expanding to a `cp`/`x` invocation) —
#   that shape is still out of scope here (tracked separately, #808; do not
#   implement it in this file).
#
#   Named residuals, accepted by settled #795 refinement decisions:
#   - `cp`, `mv`, `dd of=`, `sed -i`, `truncate` are out of detection scope
#     entirely (per-verb decision; #808 tracks re-examining its rationale
#     under --dangerously-skip-permissions).
#   - Out-of-repo-root targets are allowed by guard-main-worktree.sh's Bash
#     arm outright (Q1a, deliberate — see that script's header).
#   - A command that evades even bwt_has_write_candidate (e.g. an
#     escape-split `t\ee` with no `>` anywhere) never reaches extraction or
#     the backstop and is silently allowed. Closing that would require
#     treating every backslash as suspicious, which was rejected on
#     false-positive grounds; it is a known, documented residual.
#
#   Comparison-context recognition is intentionally narrow, PERMANENTLY
#   (#810, round-3 stabilization review — this is a settled design choice,
#   not a residual gap to keep chasing): a `[[`/`((` is only ever treated as
#   a suppressible comparison-region opener when the character immediately
#   preceding it (skipping only spaces/tabs, never newlines) is
#   start-of-string, `;`, `&`, `|`, or a newline — plus a literal `$`
#   directly before `((`, for `$(( ))` arithmetic expansion specifically.
#   Two prior review rounds tried to widen this to also accept a preceding
#   `)`/`{`/`}` or bare reserved word (`if`, `then`, `do`, ...) on the theory
#   that those positions are "also command position" — each attempt required
#   recursively checking THAT token's own position in turn, an open-ended
#   amount of shell-grammar replication with no natural stopping point (the
#   same "general adversarial shell-grammar sweep" this ticket's scope guard
#   already says not to chase), and each one left a residual gap (confirmed
#   bypasses via `)`-preceded, `do`/`!`-preceded, and brace-preceded `[[`).
#   The fix is to never widen past the minimal, non-recursible set above.
#   The deliberate, permanent consequence: `[[`/`((` used in an embedded or
#   compound-statement position — after `if`/`while`/`do`/`elif`/`else`/`!`,
#   inside `$(...)`, right after a `)`/`{`/`}`, etc. — is NOT recognized as a
#   comparison region at all; the tokenizer falls through to ordinary
#   redirect handling and extracts any `>` inside it as a real write target.
#   This can only ever over-block a legitimate embedded comparison (the
#   agent rephrases, e.g. hoists the `[[ ]]` to its own statement) — it can
#   never under-block, since narrowing the accepted-opener set only shrinks
#   the suppressed span, never grows it. NOTE: this "narrowing can only
#   shrink, never grow, the suppressed span" claim covers the region's
#   BOUNDARY (which openers are even recognized) — it does not, on its own,
#   cover the region's INTERIOR; see the round-5 paragraph below for the
#   interior-specific guarantee that closes that gap.
#
#   (#810 round-5 security review [CRITICAL]): bash performs command
#   substitution (`` `...` ``, `$(...)`) and process substitution (`<(...)`,
#   `>(...)`) INSIDE both `[[ ... ]]` and `(( ... ))` — a redirect or `tee`
#   invocation nested inside such a substitution is a real, executing write,
#   but mark_regions()'s marking loops used to suppress every character
#   position between a recognized opener and its matched closer
#   unconditionally, including the substitution's own contents, so a write
#   hidden inside one was never extracted and never blocked (e.g. `[[
#   $(printf x > /repo/.env) ]]`, `(( $(printf x > /repo/.env; echo 1) > 0
#   ))`, `[[ $(echo p | tee /repo/.env) ]]`). Fixed by re-scanning a
#   candidate region's INTERIOR (the span strictly between the opener and
#   closer) for any unquoted, unescaped substitution marker — a backtick, a
#   `$` immediately followed by `(`, a `<` immediately followed by `(`, or a
#   `>` immediately followed by `(` — before committing to suppress it: a
#   region whose interior contains ANY such marker is not suppressed at all
#   (nothing marked, exactly like the existing unclosed/malformed-closer
#   abandon path), and every character in it falls through to ordinary
#   redirect/tee handling instead. Regions containing a nested
#   command/process substitution are therefore NEVER suppressed — this is
#   the interior-specific guarantee correcting the note above: narrowing the
#   accepted-opener set alone was never sufficient to guarantee no
#   under-blocking, since a region's interior can itself hide a real write
#   regardless of how narrowly its opener is recognized. See
#   has_nested_substitution() below.
#
#   (#810 stabilization): the interior marker described above can appear in
#   two different contexts, and they are handled differently. A marker found
#   in UNQUOTED interior text needs no special mechanism at all: `(`, `)`,
#   and a backtick are already unconditional word-boundary characters in the
#   main tokenizer's ordinary unquoted dispatch, so a bare
#   `$(printf x > /repo/.env)`, `` `printf x > /repo/.env` ``, or
#   `${ printf x > /repo/.env; }` in unquoted position is already tokenized
#   correctly by simply not suppressing the region — the existing unquoted
#   handling takes over and extracts the real target. A marker found INSIDE a
#   double-quoted span within the region's interior is different: bash lets
#   the substitution escape the enclosing double quote for its own real
#   syntax, but this tokenizer's own double-quote handling treats all
#   double-quoted content as opaque and has no safe way to "escape into" the
#   substitution's real syntax mid-scan. Rather than build a precise
#   resume-parsing mechanism for that case (tried and reverted after
#   repeatedly introducing new bugs), this is instead treated as a
#   deliberately blunt, maximally conservative unsupported construct: the
#   WHOLE extraction fails closed with a new distinct exit code, 7 (see
#   bwt_extract_targets), blocking the whole command rather than attempting
#   to precisely resolve what is inside it. See has_nested_substitution()'s
#   return-value convention below for how these two cases are distinguished.
#
#   (#810 round-4 security review): the accepted preceding-char set itself
#   is unchanged (still exactly start-of-string/`;`/`&`/`|`/newline, plus
#   `$` before `((`), but HOW it is determined moved from a stateless
#   backward `substr()` re-scan of the raw command text into mark_regions()'s
#   own single forward pass, which already tracks quote/escape state. Two
#   bugs in the old backward re-scan are fixed by this move: (1) it had no
#   quote/escape awareness, so a backslash-escaped or quoted `;`/`&`/`|`
#   (e.g. `\;`, a literal argument character, never a real separator) was
#   wrongly accepted as a valid boundary; (2) it treated a bare `&`/`|`
#   preceding char as always sufficient, without checking whether that
#   `&`/`|` was itself the tail of a compound redirect operator (`>&`, `<&`,
#   `>|`) rather than a real separator/pipe — `echo hi >| [[ x > /repo/.env
#   ]]` is real bash where `>|` redirects to a file literally named `[[`, so
#   `[[` there is an ordinary WORD, not the reserved conditional token, and
#   the `>` inside is a genuine redirect that must not be suppressed. See
#   mark_regions(), is_boundary_char(), is_opener_boundary(), and
#   is_bracket_command_position() below.
#
# Functions:
#   bwt_has_write_candidate <command>    -- cheap early-exit predicate
#   bwt_extract_targets <command>        -- emits one raw target per line
#   bwt_zero_parse_suspicious <command>  -- empty-parse backstop trigger
#   bwt_has_delimited_tee <command>      -- delimited-tee raw-text scan (#810)
#   bwt_is_exempt_device <target>        -- true for /dev/null etc.
#   bwt_expand_safe_vars <target>        -- internal expansion, never eval
#   bwt_is_unresolved <expanded-target>  -- true if still unresolved

# ---------------------------------------------------------------------------
# bwt_has_write_candidate <command>
#
# Cheap `case` predicate: returns non-zero (false) when the command contains
# no unquoted-or-not `>` character and no `tee` substring at all, so callers
# can exit 0 before any resolution work. This is deliberately loose (it does
# not attempt quote-awareness) -- a false positive here only costs a full
# bwt_extract_targets parse that then correctly finds no real target; a false
# negative would incorrectly skip a real write target, which this predicate
# is designed never to do (any command containing `tee` or `>` anywhere,
# quoted or not, passes through to the real tokenizer).
bwt_has_write_candidate() {
  case "$1" in
    *'>'* | *tee*) return 0 ;;
    *) return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# bwt_zero_parse_suspicious <command>
#
# Empty-parse backstop trigger (#795): callers invoke this ONLY after
# bwt_extract_targets succeeded (exit 0) yet emitted zero targets. Zero
# targets is byte-identical for "this command genuinely writes nothing" and
# "the tokenizer met a construct it does not model" -- this predicate is the
# observable proxy separating the two. True (suspicious -- the caller should
# scan the raw command text for its own sensitive markers and block on a
# hit) when the command contains any `>` character at all, or a DELIMITED
# `tee` token (a literal `tee` not embedded inside a longer
# alphanumeric/underscore word).
#
# The tee half is deliberately narrower than bwt_has_write_candidate's
# `*tee*` substring match: an alnum-embedded "tee" (`sixteen`, `steel`,
# `committee`, `guarantee`) can never invoke tee under any shell parse -- no
# expansion mechanism splits a plain alphanumeric word -- so treating those
# as suspicious would turn every `grep guarantee <abs-path>` into a block.
# Every construct that CAN actually execute tee keeps the literal substring
# "tee" adjacent to a non-word character (`{tee,cat}`, `$(tee ...)`,
# `\tee`, `''tee`), all of which this delimited match still catches. (An
# escape-SPLIT `t\ee` contains no contiguous "tee" substring at all and
# already evades bwt_has_write_candidate upstream -- a documented residual,
# see the header; it is not reachable here.)
#
# The `>` half stays a plain substring check: a quoted `>` with zero parsed
# targets only costs a raw-marker scan, and only blocks when that scan hits
# a sensitive marker -- an accepted, narrow false-positive surface.
#
# The tee half delegates to bwt_has_delimited_tee (below), which runs the
# delimited-tee scan in awk (already a hard dependency of
# bwt_extract_targets, which necessarily ran before any caller reaches this
# predicate) and fails closed (reports a delimited tee -- "suspicious") if
# awk vanished from PATH in between.
bwt_zero_parse_suspicious() {
  case "$1" in
    *'>'*) return 0 ;;
  esac
  bwt_has_delimited_tee "$1"
}

# ---------------------------------------------------------------------------
# bwt_has_delimited_tee <command>
#
# True when <command>'s raw text contains a DELIMITED `tee` token (a literal
# `tee` not embedded inside a longer alphanumeric/underscore word) -- e.g.
# `{tee,cat}`, `$(tee ...)`, `` `tee ...` ``, `\tee`, `''tee`. False for an
# alnum-embedded "tee" (`sixteen`, `steel`, `committee`, `guarantee`): no
# expansion mechanism splits a plain alphanumeric word, so those can never
# invoke tee under any shell parse -- treating them as suspicious would turn
# every `grep guarantee <abs-path>` into a block. (An escape-SPLIT `t\ee`
# contains no contiguous "tee" substring at all and already evades
# bwt_has_write_candidate upstream -- a documented residual, see the file
# header; it is not reachable here.)
#
# Extracted from bwt_zero_parse_suspicious's original tee-detection logic
# (#810) so guard-main-worktree.sh's zero-parse backstop can invoke this
# signal directly: a zero-parse command containing a delimited tee token must
# block unconditionally there, since the tee's target may be RELATIVE to the
# main worktree -- with no repo-root string anywhere in the raw text for the
# root-mention scan to find (the "absence of a root string" backstop is
# necessary but not sufficient evidence of an out-of-root write).
#
# If `awk` is missing from PATH, fails closed: reports a delimited tee found
# (true) rather than silently reporting none.
bwt_has_delimited_tee() {
  case "$1" in
    *tee*) ;;
    *) return 1 ;;
  esac
  command -v awk >/dev/null 2>&1 || return 0
  BWT_CMD="$1" awk 'BEGIN {
    if (ENVIRON["BWT_CMD"] ~ /(^|[^A-Za-z0-9_])tee([^A-Za-z0-9_]|$)/) exit 0
    exit 1
  }'
}

# ---------------------------------------------------------------------------
# bwt_is_exempt_device <target>
#
# True for /dev/null, /dev/stdout, /dev/stderr, and /dev/fd/* -- flow's own
# documented commands redirect stderr through these pervasively (e.g.
# `2>/dev/null`) and must never be blocked.
# /dev/fd/* is matched only when the suffix after "/dev/fd/" is purely
# numeric (real /dev/fd/N targets are always all-digits) -- a raw prefix-glob
# match against the un-canonicalized target would let a traversal payload
# like `/dev/fd/../../../etc/passwd` masquerade as an exempt device and skip
# every downstream check.
bwt_is_exempt_device() {
  case "$1" in
    /dev/null | /dev/stdout | /dev/stderr) return 0 ;;
    /dev/fd/*)
      _bwt_fdnum="${1#/dev/fd/}"
      case "${_bwt_fdnum}" in
        '' | *[!0-9]*) return 1 ;;
        *) return 0 ;;
      esac
      ;;
    *) return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# bwt_expand_safe_vars <target>
#
# Internal (never `eval`) expansion of a fixed safe-var allowlist:
# `${TMPDIR:-/tmp}`, `$TMPDIR`, `${TMPDIR}`, `$HOME`, `${HOME}`, `$PWD`,
# `${PWD}`. `$TMPDIR`/`${TMPDIR}` (and the `$TMPDIR` half of the `:-/tmp}`
# fallback form) expand only when TMPDIR is set, absolute, and resolves to an
# existing directory -- the same checks guard-main-worktree.sh applies to
# $TMPDIR today (#749). `${TMPDIR:-/tmp}` uses $TMPDIR when it passes those
# checks, falls back to the literal `/tmp` when TMPDIR is unset/empty, and is
# left UNRESOLVED (unexpanded) when TMPDIR is set but fails the checks --
# never silently substitutes a wrong value. $HOME/$PWD need only be set and
# absolute (no existing-directory requirement). Any other value (including
# an unrecognized $VAR) is returned unchanged; bwt_is_unresolved is
# responsible for deciding whether the (possibly still-unexpanded) result is
# safe to use.
bwt_expand_safe_vars() {
  _bwt_in="$1"

  # Tilde expansion (#795): a leading unquoted `~` mirrors the $HOME branch
  # below -- expand only when HOME is set and absolute, else return the
  # input unchanged. Without this, a literal `~/.claude/settings.json`
  # target mis-canonicalizes to an in-root relative path and over-blocks,
  # breaking the /cenci:configure pattern that guard-main-worktree.sh's Q1a
  # out-of-root allowance exists for. Only the bare `~` / `~/...` forms are
  # handled (`~user/...` stays literal, same as any unrecognized form). A
  # QUOTED or escaped leading tilde never reaches this branch: the tokenizer
  # emits it as `./~...` (see the wtilde note in bwt_extract_targets), since
  # real shells do not tilde-expand a quoted `~` and expanding it here would
  # misclassify an in-root literal-`~` write as out-of-root.
  case "${_bwt_in}" in
    '~' | '~/'*)
      if [ -n "${HOME:-}" ]; then
        case "${HOME}" in
          /*)
            if [ "${_bwt_in}" = '~' ]; then
              printf '%s\n' "${HOME%/}"
            else
              printf '%s%s\n' "${HOME%/}" "${_bwt_in#'~'}"
            fi
            return 0
            ;;
        esac
      fi
      printf '%s\n' "${_bwt_in}"
      return 0
      ;;
  esac

  case "${_bwt_in}" in
    '${TMPDIR:-/tmp}'*)
      _bwt_suffix="${_bwt_in#'${TMPDIR:-/tmp}'}"
      if [ -n "${TMPDIR:-}" ]; then
        case "${TMPDIR}" in
          /*)
            if [ -d "${TMPDIR}" ]; then
              printf '%s%s\n' "${TMPDIR%/}" "${_bwt_suffix}"
              return 0
            fi
            ;;
        esac
        printf '%s\n' "${_bwt_in}"
        return 0
      fi
      printf '/tmp%s\n' "${_bwt_suffix}"
      return 0
      ;;
  esac

  case "${_bwt_in}" in
    '${TMPDIR}'*)
      _bwt_suffix="${_bwt_in#'${TMPDIR}'}"
      if [ -n "${TMPDIR:-}" ]; then
        case "${TMPDIR}" in
          /*)
            if [ -d "${TMPDIR}" ]; then
              printf '%s%s\n' "${TMPDIR%/}" "${_bwt_suffix}"
              return 0
            fi
            ;;
        esac
      fi
      printf '%s\n' "${_bwt_in}"
      return 0
      ;;
    '$TMPDIR'*)
      _bwt_suffix="${_bwt_in#'$TMPDIR'}"
      if [ -n "${TMPDIR:-}" ]; then
        case "${TMPDIR}" in
          /*)
            if [ -d "${TMPDIR}" ]; then
              printf '%s%s\n' "${TMPDIR%/}" "${_bwt_suffix}"
              return 0
            fi
            ;;
        esac
      fi
      printf '%s\n' "${_bwt_in}"
      return 0
      ;;
    '${HOME}'*)
      _bwt_suffix="${_bwt_in#'${HOME}'}"
      if [ -n "${HOME:-}" ]; then
        case "${HOME}" in
          /*)
            printf '%s%s\n' "${HOME%/}" "${_bwt_suffix}"
            return 0
            ;;
        esac
      fi
      printf '%s\n' "${_bwt_in}"
      return 0
      ;;
    '$HOME'*)
      _bwt_suffix="${_bwt_in#'$HOME'}"
      if [ -n "${HOME:-}" ]; then
        case "${HOME}" in
          /*)
            printf '%s%s\n' "${HOME%/}" "${_bwt_suffix}"
            return 0
            ;;
        esac
      fi
      printf '%s\n' "${_bwt_in}"
      return 0
      ;;
    '${PWD}'*)
      _bwt_suffix="${_bwt_in#'${PWD}'}"
      if [ -n "${PWD:-}" ]; then
        case "${PWD}" in
          /*)
            printf '%s%s\n' "${PWD%/}" "${_bwt_suffix}"
            return 0
            ;;
        esac
      fi
      printf '%s\n' "${_bwt_in}"
      return 0
      ;;
    '$PWD'*)
      _bwt_suffix="${_bwt_in#'$PWD'}"
      if [ -n "${PWD:-}" ]; then
        case "${PWD}" in
          /*)
            printf '%s%s\n' "${PWD%/}" "${_bwt_suffix}"
            return 0
            ;;
        esac
      fi
      printf '%s\n' "${_bwt_in}"
      return 0
      ;;
  esac

  printf '%s\n' "${_bwt_in}"
}

# ---------------------------------------------------------------------------
# bwt_is_unresolved <expanded-target>
#
# True when the (already safe-var-expanded) target still contains `$`, a
# backtick, or `(` -- covers `$(...)` command substitution, backticks,
# `>(...)` process substitution, and any unrecognized `$VAR` left unexpanded
# by bwt_expand_safe_vars. Also true when the target is empty or contains a
# newline (a newline-containing "path" is never a legitimate literal
# filesystem path here and must fail closed).
bwt_is_unresolved() {
  _bwt_u="$1"
  [ -z "${_bwt_u}" ] && return 0
  case "${_bwt_u}" in
    *'$'* | *'`'* | *'('*) return 0 ;;
  esac
  case "${_bwt_u}" in
    *"
"*)
      return 0
      ;;
  esac
  return 1
}

# ---------------------------------------------------------------------------
# bwt_extract_targets <command>
#
# Quote-state-aware tokenizer (single quotes, double quotes, backslash
# escapes -- operators are recognized only when unquoted and unescaped).
# Emits one raw (dequoted, un-expanded) target per line, in command order,
# for `>`, `>>`, `>|`, fd-prefixed `N>`/`N>>`, `&>`, `&>>`, `>&`, and for
# every `tee`/`tee -a` operand. Option words are skipped either until a
# literal `--`, or implicitly, on the first word that does not start with
# `-` (there need not be a `--` present at all -- e.g. `tee -a <target>` ends
# its options phase on `<target>` itself, with no explicit `--`); every
# remaining word up to the next unquoted `|`, `&`, or `;` is an operand,
# matched by basename so `/usr/bin/tee`/`./tee` are detected too.
#
# Tee-detection design decision (#795, round 3): a flushed plain word whose
# basename equals `tee` sets `tee = 1` UNCONDITIONALLY -- regardless of its
# position in the command -- deliberately including non-command-position
# occurrences (e.g. `grep tee /repo/.env` is treated as a possible tee
# invocation and its next word is checked against the blocklist too). A
# prior round added `expect_cmd`, a command-position tracker meant to narrow
# this to only genuine invocations; it was reverted after review found it
# silently disabled detection for `sudo tee ...`, `{ tee ...; }`,
# `if ...; then tee ...; fi`, and other ordinary constructs, because
# re-arming `expect_cmd` correctly after every shell reserved word (`if`,
# `then`, `while`, `case`, `{`, ...) and every wrapper/prefix command
# (`sudo`, `env`, `VAR=val`, `xargs`, `nice`, ...) cannot be done safely or
# completely by lexical means alone -- the latter set is open-ended. A false
# positive here only costs a harmless extra target check; a false negative
# would silently skip a real write target, which this predicate is designed
# never to do.
#
# For `>&` an operand that is purely numeric or `-` is an fd dup and is
# skipped, not treated as a path; bare `&>`/`&>>` are NEVER fd dups (real
# shell semantics: `&>word` always redirects to a literal file named `word`)
# and always extract their operand as a target. A literal embedded newline is
# treated as a statement separator, same as `|`/`;`, so a multi-line
# command's tee/redirect detection is not defeated by `tee` landing as the
# first token on a new line. Unquoted `(` and `)` are treated as
# operator/boundary characters (mirroring `;`/`|`), since real shell grammar
# separates words on them with no whitespace required (e.g. `(tee /path)`,
# `(echo hi>/repo/.env)`); unquoted `<` is treated the same way purely as a
# boundary (no target extraction -- input redirection stays out of scope) so
# it cannot be glued onto an adjacent word undetected (e.g. `tee<file`).
# Unquoted `{` and `}` are deliberately NOT treated as boundary characters
# (reverted, #795 round 4) -- see the dispatch comment below for why. An
# unquoted backtick IS treated exactly like `(`/`)` (added, #795 round 4): it
# is an unconditional shell metacharacter (always triggers command
# substitution, regardless of surrounding whitespace, unlike `{`/`}`), so
# `` `tee /repo/.env` `` must not tokenize its first word as the glued
# 4-character literal `` `tee ``, which would never match the basename "tee"
# check. A backslash immediately followed by an unquoted newline is a true
# shell line continuation: both characters are consumed and the two
# physical-line fragments join into one word with no newline in between
# (never emitted as two separate physical lines to the caller).
#
# Implementation note: the quote/escape state machine runs as a single `awk`
# subprocess invocation (one process per bwt_extract_targets call) rather
# than the shell one-character-at-a-time `${var#?}` idiom this function used
# previously -- that idiom is quadratic in command length under this shell's
# `${var#pattern}` implementation (a 5000-char command measured ~33s), a
# DoS/timeout risk given this hook runs unconditionally on every Bash call.
# `awk` is POSIX-guaranteed present, matching this codebase's existing
# pattern of depending on a well-known external tool (`jq`) with fail-closed
# behavior if missing: if `awk` is not on PATH, this function returns 3
# (emitting nothing) instead of silently reporting "no targets found". A
# command whose length exceeds a safe threshold under the OS's
# MAX_ARG_STRLEN ceiling (128 KiB on Linux) is rejected before ever invoking
# awk, returning 4 (also emitting nothing) -- distinct from 3, since an
# oversized command failing the `awk` exec itself (E2BIG) is not the same
# fault as awk being missing, and callers should report an accurate message
# for each. The length pre-check itself depends on `wc`; if `wc` is missing
# from PATH this function returns 5 (also emitting nothing) -- a third,
# distinct fail-closed signal for diagnostic accuracy, mirroring the `awk`-
# missing check (a `wc` failure must never silently collapse to a length of
# 0, which would be misread as "under threshold"). An unquoted brace
# expansion (`{a,b}`, `{1..3}`) reaching write-target position (a redirect
# target, a `>&` non-fd operand, or a `tee`/`tee -a` operand) is a fourth
# distinct fail-closed signal, returning 6 (#810): handing back the raw
# un-expanded word as "the" target would misreport what bash actually writes
# to (one word the shell itself multiplexes into several real targets), so
# the whole call fails closed instead of emitting a bogus literal. A
# command/process/function substitution marker discovered INSIDE a
# double-quoted string within a `[[ ]]`/`(( ))` comparison region's interior
# (see has_nested_substitution() and mark_regions() below) is a fifth
# distinct fail-closed signal, returning 7 (#810 stabilization): this
# tokenizer's own double-quote handling has no safe way to resume precise
# parsing through such a marker's real syntax, so rather than attempt that
# (and risk the class of bugs a precise-resume mechanism kept introducing),
# the whole call fails closed instead -- deliberately blunt and maximally
# conservative, but never silently wrong. The same substitution appearing in
# UNQUOTED text is unaffected by this and is still precisely handled: the
# existing unquoted-path tokenizing already extracts the real write inside
# it, no special mechanism needed. All extracted-target output is buffered
# internally and only printed once the whole scan completes successfully, so
# a non-zero exit (3, 4, 5, 6, or 7) is guaranteed to emit nothing,
# preserving this function's "non-zero -> emits nothing" contract. Callers
# must treat ANY non-zero exit from this function as fail-closed (block),
# never as an empty-result allow. The command string is
# handed to awk via ENVIRON (an environment variable), never via `-v` (which
# applies string-escape processing to its value and would corrupt a command
# containing backslashes) and never via stdin (which would require a
# non-portable record-separator trick to preserve embedded newlines
# faithfully) -- ENVIRON values are passed through unprocessed, embedded
# newlines included.
bwt_extract_targets() {
  command -v awk >/dev/null 2>&1 || return 3

  _bwt_cmd="$1"

  # Fix D (#795 round 2): the OS's MAX_ARG_STRLEN ceiling (128 KiB / 131072
  # bytes on Linux) caps a single env-var string handed to execve. An
  # unusually long (but legitimate) command would make the `awk` exec below
  # itself fail (E2BIG -> "Argument list too long", exit 126) -- a failure
  # mode indistinguishable, to the caller, from "awk is not installed"
  # (which also surfaces as non-zero from this function). Pre-check the
  # length here, comfortably under the ceiling, and fail closed with a
  # distinct, documented exit code (4) so callers can report an accurate
  # "command too long" message instead of misreporting "awk not found".
  # `wc -c` is used rather than a bash-ism (${#var}) since this file must
  # stay portable POSIX /bin/sh. Fail closed (return 5), distinct from the
  # awk-missing (3) and command-too-long (4) codes, if `wc` itself is not on
  # PATH -- otherwise a missing `wc` would silently collapse `_bwt_cmd_len` to
  # 0 below (treated as "under threshold"), defeating this check's purpose.
  command -v wc >/dev/null 2>&1 || return 5

  _bwt_cmd_len=$(printf '%s' "$_bwt_cmd" | wc -c | tr -d '[:space:]')
  case "$_bwt_cmd_len" in
    '' | *[!0-9]*) _bwt_cmd_len=0 ;;
  esac
  if [ "$_bwt_cmd_len" -gt 100000 ]; then
    return 4
  fi

  BWT_CMD="$_bwt_cmd" awk '
    BEGIN {
      sq = sprintf("%c", 39)  # single quote
      dq = sprintf("%c", 34)  # double quote
      bsl = sprintf("%c", 92) # backslash
      nl = sprintf("%c", 10)  # newline (used by emit()s print-time backstop)

      cmd = ENVIRON["BWT_CMD"]
      n = length(cmd)

      quote = 0 # 0=none 1=single 2=double
      esc = 0
      word = ""
      wordhas = 0
      mode = "" # "" | "target" | "fddup"
      tee = 0   # 0=not-in-tee 1=tee-options-phase 2=tee-operand-phase
      # wtilde: 1 when the current word began with a PLAIN UNQUOTED tilde --
      # the only form a real shell tilde-expands. A word whose leading ~
      # arrived via quotes or a backslash escape is a literal filename
      # character; flush() prefixes such a word with "./" so the callers
      # safe-var expander cannot mistake it for an expandable ~/... target
      # (which would misclassify an in-root literal-~ write as out-of-root,
      # a fail-open). "./" preserves the paths meaning exactly: it stays a
      # cwd-relative path and lexical collapse later removes the "." segment.
      wtilde = 0

      # Brace-expansion tracking (#810): per-word flags mirroring the
      # wtilde idiom above. bracedepth counts unquoted, unescaped `{` not
      # yet closed by a matching unquoted `}` (reaching this code only in
      # unquoted, unescaped context -- quoted/escaped braces never reach
      # these checks, see the dispatch site below). bracehasdelim latches
      # true once an unquoted `,` or unquoted `..` (range form) is seen
      # while bracedepth > 0. wordbrace latches true (for the CURRENT word
      # only -- reset every flush()) once a `}` closes with bracehasdelim
      # true: a bare `{x}` with no comma/range inside never sets it, since
      # real bash does not brace-expand that shape (see the
      # bwt_extract_targets header comment).
      bracedepth = 0
      bracehasdelim = 0
      wordbrace = 0

      # curregion: 1 when any character consumed into the CURRENT word fell
      # inside a closed [[ ... ]] / (( ... )) region (see mark_regions()
      # below), reset every flush(). Used only to suppress tee-ARMING for a
      # word entirely inside such a region (#810 Fix 3) -- the `>`
      # suppression itself is handled inline at the dispatch site via the
      # suppress[] array directly, not via this flag.
      curregion = 0

      # outbuf/outn: emit() buffers every extracted target here instead of
      # printing directly, so a brace-expansion failure (exit 6, detected
      # mid-scan) can discard everything already "emitted" simply by never
      # reaching the print loop at the end of this BEGIN block -- preserving
      # the documented "non-zero exit -> emits nothing" contract.
      outn = 0

      # Pre-pass (#810 Fix 3): populate suppress[] with every character
      # position that falls inside a CLOSED, unquoted, standalone
      # [[ ... ]] or (( ... )) region, so the main loop below can treat a
      # `>` inside one as an ordinary literal character (a comparison, never
      # a redirect) instead of a redirect operator. An UNCLOSED opener (e.g.
      # `echo [[ > /repo/out`, never closed) leaves suppress[] untouched for
      # that span -- deliberately anti-fail-open: a construct this scan
      # cannot prove closed must never suppress a real redirect.
      mark_regions()

      i = 1
      while (i <= n) {
        c = substr(cmd, i, 1)
        p = i
        i++

        if (suppress[p]) curregion = 1

        if (esc == 1) {
          if (c == "\n") {
            # True shell line continuation: backslash-newline deletes BOTH
            # characters and joins the two physical-line fragments into one
            # word with no newline in between (Fix A / #795 round 2) --
            # nothing is appended to word, just clear the escape flag and
            # keep accumulating onto the same word.
            esc = 0
            continue
          }
          word = word c
          wordhas = 1
          esc = 0
          continue
        }

        if (quote == 1) {
          if (c == sq) {
            quote = 0
          } else {
            word = word c
            wordhas = 1
          }
          continue
        }

        if (quote == 2) {
          # Double-quoted content is opaque/literal, full stop -- no attempt
          # is made here to detect or "escape into" the internal syntax of a
          # nested substitution from this main loop. A `[[ ]]`/`(( ))` region
          # whose interior contains a command/process/function substitution
          # marker INSIDE a double-quoted span is instead caught upstream by
          # has_nested_substitution() / mark_regions() and fails the whole
          # extraction closed (exit 7) before this loop ever runs on it --
          # see the file header and the bwt_extract_targets comment. A marker in
          # UNQUOTED text needs no special handling at all here either: `(`,
          # `)`, and backtick are already unconditional word-boundary
          # characters in the unquoted dispatch below, so that case is
          # already correctly tokenized without this branch doing anything
          # special.
          if (c == dq) { quote = 0; continue }
          if (c == bsl) { esc = 1; continue }
          word = word c
          wordhas = 1
          continue
        }

        # Unquoted context from here on.
        if (c == sq) { quote = 1; wordhas = 1; continue }
        if (c == dq) { quote = 2; wordhas = 1; continue }
        if (c == bsl) { esc = 1; continue }
        if (c == " " || c == "\t") { flush(); continue }
        if (c == "\n") { flush(); tee = 0; continue }

        # Unquoted ( and ) are POSIX operator/metacharacters that always
        # separate words, with no whitespace required (Fix B / #795 round 2)
        # -- e.g. `(tee /path)`, `(echo hi>/repo/.env)` glue directly onto an
        # adjacent word without this, defeating both basename matching and
        # target-suffix matching. `<` needs no target extraction (input
        # redirection is out of scope for this ticket) but must still act as
        # a boundary so a following word (e.g. `tee<file`) is not glued onto
        # it undetected.
        if (c == "(") { flush(); tee = 0; continue }
        if (c == ")") { flush(); tee = 0; continue }
        if (c == "<") { flush(); tee = 0; continue }

        # Unquoted { and } are deliberately NOT given this same boundary
        # treatment (reverted, #795 round 4 -- Fix 1). A prior round added
        # them on the theory that a glued `{tee ...;}` needed the same
        # treatment as glued `(tee ...)`; that premise was wrong. Unlike (/),
        # which are unconditionally shell metacharacters (mid-word or not,
        # they always break tokenization -- `(tee` is never a valid single
        # word), { and } are reserved words only when they appear as their
        # own whitespace-delimited token. A bare {/} glued into a word with no
        # surrounding whitespace is just an ordinary literal character to
        # the bash lexer: `{tee /path; }` and `{tee /path}` are real bash syntax
        # errors -- that "glued brace" construct never actually executes as
        # tee in the first place, so there was never a real bypass to fix.
        # Meanwhile real bash does NOT brace-expand a bare `{x}` with no
        # comma/range inside -- `/repo/{x}.env` is a perfectly ordinary, valid,
        # literal filename -- and special-casing { and } here truncated that
        # literal filename at the first `{` (a real, verified bypass: `tee
        # /repo/{x}credentials.txt` produced only `/repo/` as the "target").
        # { and } are therefore left as ordinary word characters (the default
        # `word = word c` fallthrough at the bottom of this dispatch). Real
        # `{ tee ...; }` command-grouping already gets correct word separation
        # for free from its mandatory surrounding whitespace (a space before
        # `tee`, a space/`;` before `}`) -- no special boundary handling is
        # needed for genuine `{ }` grouping syntax.

        # Unquoted backtick IS an unconditional shell metacharacter (Fix 2,
        # #795 round 4), unlike { and } above: it always triggers
        # command-substitution parsing regardless of adjacent whitespace, with
        # the content between backticks itself parsed and executed as a nested
        # command. Treated exactly like ( and ) -- flush the current word,
        # reset tee-scan state, and emit nothing for the backtick character
        # itself -- so `` `tee /repo/.env` `` does not tokenize its first word
        # as the glued literal `` `tee ``, which would never equal "tee" in
        # the basename check and would silently produce zero targets for the
        # whole command even though real bash genuinely executes `tee
        # /repo/.env` as a subprocess. A backtick inside single or double
        # quotes never reaches this branch (the quote-state checks above run
        # first and unconditionally continue), so a quoted backtick remains an
        # ordinary literal character.
        if (c == "`") { flush(); tee = 0; continue }

        # Redirect operators do NOT clear tee state (#795 final round): a
        # redirect between a commands operands does not end its argument
        # list -- `tee /tmp/decoy >/dev/null /repo/.env` keeps /repo/.env as
        # a tee operand in real shells, and clearing tee here silently
        # dropped it (a wrong-but-non-empty parse the empty-parse backstop
        # can never catch). Only genuine command separators reset tee:
        # `;`, `|`, newline, `(`, `)`, backtick, and plain job-control `&`.
        if (c == ">") {
          # #810 Fix 3: a `>` at a position inside a CLOSED, unquoted [[ ... ]]
          # / (( ... )) region (see mark_regions()) is a comparison operator
          # in that context, never a redirect -- treat it as an ordinary
          # literal word character instead of dispatching redirect-operator
          # handling. An unclosed opener never populates suppress[], so this
          # can never suppress a real redirect (anti-fail-open).
          if (suppress[p]) {
            word = word c
            wordhas = 1
            continue
          }
          # A digits-only word glued immediately before > is an fd number
          # (POSIX IO_NUMBER: `2>>log`, `1>out`) -- discard it rather than
          # flushing it as a word. Flushing it mattered once tee state
          # started surviving redirects (#795 final round): mid-tee, the
          # glued "2" of `tee f1 2>/dev/null` would otherwise be emitted as
          # a tee operand named "2" (an in-root relative path -- a new
          # over-block on a pervasive construct). A SPACED digits word
          # (`tee f1 2 > x`) is a real operand in POSIX too, and stays one
          # here: the space already flushed it before this branch runs.
          if (wordhas && word ~ /^[0-9]+$/) {
            word = ""
            wordhas = 0
            wtilde = 0
          }
          flush()
          nc = (i <= n) ? substr(cmd, i, 1) : ""
          if (nc == ">") {
            i++
            mode = "target"
          } else if (nc == "|") {
            i++
            mode = "target"
          } else if (nc == "&") {
            i++
            mode = "fddup"
          } else {
            mode = "target"
          }
          continue
        }

        if (c == "&") {
          nc = (i <= n) ? substr(cmd, i, 1) : ""
          if (nc == ">") {
            flush()
            i++
            nc2 = (i <= n) ? substr(cmd, i, 1) : ""
            if (nc2 == ">") {
              i++
            }
            # Bare &> and &>> are both real-target forms, never fd dups.
            mode = "target"
          } else {
            # Plain job-control `&` is a statement/command-boundary
            # separator, same as `;`/`|`.
            flush()
            tee = 0
          }
          continue
        }

        if (c == "|" || c == ";") { flush(); tee = 0; continue }

        # Unquoted brace-expansion tracking (#810 Fix 1): `{`, an in-between
        # unquoted `,` or unquoted `..` (range form), and a matching `}` mark
        # the CURRENT word as an unresolved brace-expansion word (see the
        # bracedepth/bracehasdelim/wordbrace comment in BEGIN above). These
        # checks deliberately do NOT `continue`: the brace/comma/dot
        # characters themselves are still ordinary literal word characters
        # in bash (whether or not the pair ends up expanding) and must still
        # reach the default append at the bottom of this dispatch.
        if (c == "{") { bracedepth++ }
        if (c == "," && bracedepth > 0) { bracehasdelim = 1 }
        if (c == "." && bracedepth > 0) {
          nc = (i <= n) ? substr(cmd, i, 1) : ""
          if (nc == ".") bracehasdelim = 1
        }
        if (c == "}" && bracedepth > 0) {
          bracedepth--
          if (bracehasdelim) wordbrace = 1
        }

        # A plain unquoted tilde opening a word is the only tilde a real
        # shell expands -- record it so flush() can tell it apart from a
        # quoted/escaped literal ~ (see the wtilde comment in BEGIN).
        if (c == "~" && !wordhas && word == "") wtilde = 1
        word = word c
        wordhas = 1
      }
      flush()

      # #810: only print buffered targets once the whole scan completes
      # successfully -- any exit 6 above (brace-expansion fail-closed) exits
      # immediately from inside flush(), before this loop ever runs, so the
      # buffer is silently discarded rather than partially printed. An exit 7
      # (double-quoted nested-substitution fail-closed) exits even earlier,
      # from inside mark_regions() itself (called once, before this loop
      # starts at all -- see mark_regions()), so outn is guaranteed to still
      # be 0 at that point regardless.
      for (k = 1; k <= outn; k++) print outbuf[k]
    }

    # is_boundary_char(lastsig, beforesig) (#810 round-4 security review):
    # the actual boundary-determination logic, consuming state that the
    # mark_regions() forward scan maintains as it goes (see mark_regions()
    # below) rather than re-deriving it via a separate stateless backward
    # `substr()` re-scan of the raw command text. `lastsig` is the last
    # UNQUOTED, UNESCAPED, non-space/tab character mark_regions() has seen
    # so far (the empty string "" means start-of-string / nothing
    # significant seen yet); `beforesig` is the character mark_regions() saw
    # immediately before THAT one (also unquoted/unescaped-tracked, never a
    # raw re-derivation). This is the exact same minimal, non-recursible
    # allowlist as before -- start-of-string, `;`, `&`, `|`, or a newline --
    # plus one added refinement (round-4 Finding, was the missing piece):
    # a preceding `&`/`|` is accepted ONLY IF the character immediately
    # before THAT `&`/`|` is not `>` and not `<` -- i.e. rejected when the
    # real preceding token is the compound redirect operator `>&`, `<&`, or
    # `>|` (bash parses what follows one of those as an ordinary WORD, not a
    # reserved conditional/arithmetic opener) rather than a bare `&`/`|`/
    # `&&`/`||`/`|&`/`;&`. Confirmed exploit this closes: `echo hi >| [[ x >
    # /repo/.env ]]` is real bash where `>|` redirects to a file literally
    # named `[[` and the `>` inside is a genuine, unsuppressed redirect.
    # Because `lastsig`/`beforesig` are only ever updated by mark_regions()
    # for characters reaching its truly unquoted/unescaped dispatch (see
    # below), a backslash-escaped or quoted `;`/`&`/`|` never updates them
    # and therefore can never count as a boundary -- the second bug this
    # move fixes, since the old raw-text re-scan had no quote/escape
    # awareness at all.
    function is_boundary_char(lastsig, beforesig) {
      if (lastsig == "") return 1 # start-of-string
      if (lastsig == ";" || lastsig == "\n") return 1
      if (lastsig == "&" || lastsig == "|") {
        if (beforesig == ">" || beforesig == "<") return 0
        return 1
      }
      return 0
    }

    # is_opener_boundary(pos, allow_dollar, lastsig, beforesig) (#810 Fix 5,
    # re-narrowed by the #810 round-3 review, Finding 1; restructured to
    # consume the mark_regions() forward-scan state by the round-4 review):
    # true when "((" starting at `pos` is a real arithmetic-expansion/
    # standalone-construct opener. `allow_dollar` additionally permits a
    # preceding `$` with NO intervening whitespace (checked via a direct,
    # single-character, position-based lookback -- unchanged by the round-4
    # move, since `$((` is only ever a single glued token) -- needed for
    # `$(( ... ))` arithmetic expansion. Otherwise delegates to
    # is_boundary_char (above) using `lastsig`/`beforesig` as maintained by
    # the mark_regions() single forward pass, never a separate backward
    # re-scan. See the file-header "Comparison-context recognition" note for
    # why the accepted set itself stays minimal and non-recursible.
    function is_opener_boundary(pos, allow_dollar, lastsig, beforesig) {
      if (allow_dollar && pos > 1 && substr(cmd, pos - 1, 1) == "$") return 1
      return is_boundary_char(lastsig, beforesig)
    }

    # is_bracket_command_position(lastsig, beforesig) (#810 stabilization
    # review, Bug 3 Vector 2; re-narrowed by round-3 Finding 1; restructured
    # by round-4): "[[" has no `$`-before-`((`-style exception, so this
    # simply delegates to is_boundary_char (above) using the mark_regions()
    # forward-scan state.
    function is_bracket_command_position(lastsig, beforesig) {
      return is_boundary_char(lastsig, beforesig)
    }

    # is_opener_followed_by_ws(pos) (#810 Fix 5): true when the character
    # immediately AFTER a two-char opener starting at `pos` (i.e. at
    # pos+2) is whitespace. Real bash requires a space after "[["; this
    # tokenizer applies the same requirement to "((" for simplicity/
    # precision (a deliberately conservative choice: an unspaced "((x>0))"
    # is treated as NOT a region, costing only a false-positive over-block,
    # never a silent permit). Without this check, `[[z > path ]]` (no space
    # after "[[") wrongly opened a suppressed region even though real bash
    # does not parse a glued "[[z" as the reserved conditional token at
    # all -- the `>` there is a genuine redirect.
    function is_opener_followed_by_ws(pos,   nc2) {
      nc2 = (pos + 2 <= n) ? substr(cmd, pos + 2, 1) : ""
      return (nc2 == " " || nc2 == "\t" || nc2 == "\n")
    }

    # find_close(open_pos, ochar, cchar) (#810 Fix 5): scans forward from
    # just after a two-char opener (ochar repeated twice) at `open_pos`,
    # tracking quote/escape state AND single-character nesting depth of
    # ochar/cchar, looking for the well-formed, properly nested matching
    # double-close (cchar repeated twice). Returns the index of the SECOND
    # cchar of that matching pair on success. Returns -1 (abandon -- mark
    # nothing) when: the region runs off the end of the command unclosed
    # (existing anti-fail-open behavior), OR a lone/unbalanced cchar is hit
    # with no pending nested depth to unwind (well-formedness broken by a
    # mismatched close before any valid match is found) -- e.g.
    # `((printf x > f); (true))` is not a real `(( ))` arithmetic construct
    # (bash arithmetic parse fails and this actually runs as nested
    # subshells); "cannot prove a well-formed region" is treated exactly
    # like "unclosed", per the same anti-fail-open principle already
    # applied there. Depth tracking also correctly balances legitimate
    # nested content that reuses the same characters, e.g. a POSIX
    # character class inside `[[ $x =~ [[:alpha:]] ]]` ("[[:alpha:]]"
    # contains one extra "[" and one extra "]", which balance to net zero).
    function find_close(open_pos, ochar, cchar,   fi, fc, fquote, fesc, fdepth, fnc) {
      fquote = 0
      fesc = 0
      fdepth = 0
      fi = open_pos + 2
      while (fi <= n) {
        fc = substr(cmd, fi, 1)

        if (fesc == 1) { fesc = 0; fi++; continue }
        if (fquote == 1) {
          if (fc == sq) fquote = 0
          fi++
          continue
        }
        if (fquote == 2) {
          if (fc == dq) fquote = 0
          else if (fc == bsl) fesc = 1
          fi++
          continue
        }
        if (fc == sq) { fquote = 1; fi++; continue }
        if (fc == dq) { fquote = 2; fi++; continue }
        if (fc == bsl) { fesc = 1; fi++; continue }

        if (fc == ochar) { fdepth++; fi++; continue }
        if (fc == cchar) {
          if (fdepth > 0) {
            fdepth--
            fi++
            continue
          }
          fnc = (fi + 1 <= n) ? substr(cmd, fi + 1, 1) : ""
          if (fnc == cchar) return fi + 1
          return -1
        }
        fi++
      }
      return -1
    }

    # find_close_bracket(open_pos) (#810 stabilization review, Bug 3 Vector
    # 3): scans forward from just after a "[[" opener at `open_pos` for the
    # FIRST standalone "]]" token, matching the REAL termination rule bash
    # applies for a conditional expression -- bash closes "[[ ... ]]" at the
    # first "]]" that is its own whitespace-delimited token; it does NOT
    # balance nested "["/"]" characters the way the generic depth-counting
    # in find_close models real arithmetic/subshell nesting for "(( ))".
    # Confirmed exploit: `[[ [[ ]] ; echo x > AGENTS.md ]]` is valid bash
    # where `[[ [[ ]]` is itself a complete (if degenerate) conditional and
    # `echo x > AGENTS.md` afterward really executes the write -- but the
    # depth-counting in find_close incorrectly consumed the inner "]]" as a
    # nesting decrement and paired the opener with the FAR TRAILING "]]"
    # instead, suppressing the real redirect in between.
    #
    # A candidate "]]" (two consecutive, unquoted, unescaped "]" characters)
    # is the real standalone closer only when: the character immediately
    # before it is whitespace (space/tab/newline -- always true for a
    # well-formed "[[ ... ]]", since is_opener_followed_by_ws already
    # requires whitespace right after the opener, so there is always at
    # least one whitespace char somewhere before any candidate closer), AND
    # the character immediately after it is whitespace, `;`, `|`, `&`, `)`,
    # `>`, `<`, or end-of-string. `>`/`<` are included (round-3 stabilization
    # review, Finding 2): real bash terminates the "]]" word at ANY unquoted
    # metacharacter, including a glued redirect (`]]>file`, `]]<file`) --
    # the prior set omitted them, so a "]]" immediately glued to a real
    # redirect was wrongly rejected as the closer and the scan continued
    # past it looking for a later standalone "]]", extending the suppressed
    # region over the real redirect in between. Widening this set can only
    # ever SHORTEN a recognized region (a candidate now accepted was
    # previously rejected, never the reverse), so this is a safe,
    # one-directional fix with no new ambiguity. A glued "]]" that fails
    # either check (e.g. the "]]" ending a POSIX character class like
    # "[[:alpha:]]", glued to the preceding ":" with no whitespace) is left
    # as ordinary content and the scan continues past it looking for the
    # next candidate. Quote/escape state is tracked exactly like find_close,
    # so a quoted/escaped "]]" never counts. Returns -1 (abandon -- mark
    # nothing, same anti-fail-open principle as find_close) when the command
    # ends with no standalone "]]" ever found.
    function find_close_bracket(open_pos,   fi, fc, fquote, fesc, fnc, fafter, prevc) {
      fquote = 0
      fesc = 0
      prevc = substr(cmd, open_pos + 1, 1) # second "[" of the opener -- never "]"/whitespace, a safe seed
      fi = open_pos + 2
      while (fi <= n) {
        fc = substr(cmd, fi, 1)

        if (fesc == 1) { fesc = 0; prevc = fc; fi++; continue }
        if (fquote == 1) {
          if (fc == sq) fquote = 0
          prevc = fc; fi++; continue
        }
        if (fquote == 2) {
          if (fc == dq) fquote = 0
          else if (fc == bsl) fesc = 1
          prevc = fc; fi++; continue
        }
        if (fc == sq) { fquote = 1; prevc = fc; fi++; continue }
        if (fc == dq) { fquote = 2; prevc = fc; fi++; continue }
        if (fc == bsl) { fesc = 1; prevc = fc; fi++; continue }

        if (fc == "]") {
          fnc = (fi + 1 <= n) ? substr(cmd, fi + 1, 1) : ""
          if (fnc == "]" && (prevc == " " || prevc == "\t" || prevc == "\n")) {
            fafter = (fi + 2 <= n) ? substr(cmd, fi + 2, 1) : ""
            if (fafter == "" || fafter == " " || fafter == "\t" || fafter == "\n" || fafter == ";" || fafter == "|" || fafter == "&" || fafter == ")" || fafter == ">" || fafter == "<") {
              return fi + 1
            }
          }
        }
        prevc = fc
        fi++
      }
      return -1
    }

    # has_nested_substitution(start_pos, end_pos) (#810 round-5 security
    # review [CRITICAL]; return-value convention split by #810
    # stabilization): scans cmd[start_pos..end_pos] -- the INTERIOR of a
    # candidate [[ ... ]] / (( ... )) region, i.e. everything strictly
    # between the two-char opener and its matched two-char closer -- for any
    # unquoted, unescaped command/process-substitution marker: a backtick, a
    # `$` immediately followed by `(`, a `<` immediately followed by `(`, or
    # a `>` immediately followed by `(`. Uses the same quote/escape state
    # machine as find_close/find_close_bracket, started fresh (quote=0,
    # esc=0) since the interior always begins immediately after a two-char
    # opener that is never itself a quote-opening or escape character, so no
    # state needs to carry over the boundary. Returns 0 (false) when the
    # interior is clean, including when start_pos > end_pos (an empty
    # interior, e.g. "[[]]" with no opener/closer gap); 1 when the FIRST
    # marker found was in UNQUOTED interior text; 2 when the FIRST marker
    # found was INSIDE a double-quoted span within the interior. This is a
    # single-pass, non-recursive check either way: it never evaluates what is
    # INSIDE the substitution as a nested command, it only detects that one
    # might be present at all (and where) and reports that fact upward so
    # the caller (mark_regions()) can decide what to do with it -- return 1
    # means "do not suppress the region, fall through to ordinary tokenizing"
    # (already correctly handled there with no special mechanism, since `(`,
    # `)`, and backtick are already unconditional word-boundary characters in
    # the main tokenizer unquoted dispatch); return 2 means "fail the whole
    # extraction closed" (exit 7), since this tokenizer own double-quote
    # handling has no safe way to resume precise parsing through a nested
    # substitution real syntax mid-scan (see the bwt_extract_targets and
    # file header comments for why a precise-resume mechanism was tried and
    # reverted rather than pursued further).
    #
    # Root cause this closes: bash performs command/process substitution
    # INSIDE both [[ ... ]] and (( ... )) -- a redirect or `tee` invocation
    # nested inside such a substitution is a real, executing write, but the
    # mark_regions marking loops previously suppressed every character
    # position between the opener and closer unconditionally, including the
    # substitution content itself, so the real write inside was never
    # extracted and never blocked. Confirmed exploits: `[[ $(printf x >
    # /repo/.env) ]]`, `(( $(printf x > /repo/.env; echo 1) > 0 ))`,
    # `[[ $(echo p | tee /repo/.env) ]]` (tee-arming, not just `>`
    # suppression), a backtick variant, and a `<(...)` process-substitution
    # variant -- all in UNQUOTED interior text, so all return 1 here.
    #
    # Later addition: bash 5.3+ also introduces function substitution forms
    # `${ cmd; }` and `${| cmd; }`, which execute a real command from inside
    # a `[[ ]]`/`(( ))` construct just like `$(...)`. These are distinguished
    # from an ordinary parameter expansion (`${var}`, `${x:-default}`, which
    # never execute arbitrary commands) by requiring whitespace or `|`
    # immediately after the `${`. Confirmed exploit: `[[ -n ${ printf x >
    # /repo/.env; } ]]`.
    #
    # Deliberately over-blocks in the safe direction: `(( $((1+1)) > 0 ))`
    # now leaves its whole region unsuppressed too (an arithmetic expansion
    # is a substitution marker, same as command/process substitution), so
    # "0" is extracted as an apparent write target even though this specific
    # case is actually benign -- accepted per the anti-fail-open philosophy
    # already established in this file: erring toward extraction, never
    # suppression, whenever a substitution might be present.
    #
    # The hquote==2 (double-quote) branch below ALSO checks for a backtick /
    # `$(`/`${`-funsub marker before skipping past a double-quoted character,
    # returning 2 on a hit (distinct from the unquoted path return of 1).
    # Bash double-quote state does not extend into a nested
    # `$(...)`/backtick command substitution or `${ ...; }`/`${| ...; }`
    # function substitution -- those still parse and execute as real
    # commands, real redirects included, even wrapped in outer double
    # quotes -- but this tokenizer own quote state cannot safely resume
    # precise parsing through one, so these are reported as the
    # fail-closed-eligible case rather than the fall-through case. Confirmed
    # exploits that must return 2 here: `[[ -n "$(printf x > /repo/.env)" ]]`,
    # the `(( ))` variant, a double-quoted backtick variant, and a
    # double-quoted `${ ...; }` funsub variant. The hquote==1 (single-quote)
    # branch is intentionally left unchanged -- single quotes genuinely
    # suppress everything in real bash, including command substitution.
    # Process substitution (`<(`/`>(`) is intentionally NOT added to the
    # hquote==2 branch -- it IS genuinely inert inside double quotes in real
    # bash (bash performs no special handling of `<(`/`>(` inside "..."), so
    # excluding it there is correct.
    function has_nested_substitution(start_pos, end_pos,   hi, hc, hquote, hesc, hnc, hn2) {
      hquote = 0
      hesc = 0
      hi = start_pos
      while (hi <= end_pos) {
        hc = substr(cmd, hi, 1)

        if (hesc == 1) { hesc = 0; hi++; continue }
        if (hquote == 1) {
          if (hc == sq) hquote = 0
          hi++
          continue
        }
        if (hquote == 2) {
          if (hc == dq) { hquote = 0; hi++; continue }
          if (hc == bsl) { hesc = 1; hi++; continue }
          # Bash double-quote state does NOT extend into the internal syntax
          # of a nested command substitution (backtick or `$(...)`) or
          # function substitution (`${ ...; }` / `${| ...; }`) -- those still
          # parse and execute their contents as real commands, with real
          # redirects, regardless of being wrapped in outer double quotes. So
          # these markers must be checked here too, but -- unlike the
          # unquoted path below -- a marker found here means this tokenizer
          # cannot safely resume precise parsing through it, so this returns
          # 2 (fail closed), not 1. Only genuinely inert markers inside
          # double quotes -- process substitution `<(...)`/`>(...)`, which
          # bash does NOT treat specially inside "..." -- are deliberately
          # excluded here; that exclusion must stay.
          if (hc == "`") return 2
          if (hc == "$") {
            hnc = (hi + 1 <= end_pos) ? substr(cmd, hi + 1, 1) : ""
            if (hnc == "(") return 2
            if (hnc == "{") {
              hn2 = (hi + 2 <= end_pos) ? substr(cmd, hi + 2, 1) : ""
              if (hn2 == " " || hn2 == "\t" || hn2 == "\n" || hn2 == "|") return 2
            }
          }
          hi++
          continue
        }
        if (hc == sq) { hquote = 1; hi++; continue }
        if (hc == dq) { hquote = 2; hi++; continue }
        if (hc == bsl) { hesc = 1; hi++; continue }

        if (hc == "`") return 1
        if (hc == "$" || hc == "<" || hc == ">") {
          hnc = (hi + 1 <= end_pos) ? substr(cmd, hi + 1, 1) : ""
          if (hnc == "(") return 1
          if (hc == "$" && hnc == "{") {
            hn2 = (hi + 2 <= end_pos) ? substr(cmd, hi + 2, 1) : ""
            if (hn2 == " " || hn2 == "\t" || hn2 == "\n" || hn2 == "|") return 1
          }
        }
        hi++
      }
      return 0
    }

    # mark_regions() (#810 Fix 3, hardened by Fix 5; restructured by the
    # round-4 security review): a self-contained quote/escape-aware pre-pass
    # over `cmd`, run once before the main tokenizer loop, that populates
    # suppress[] with every character position falling inside a CLOSED,
    # unquoted, standalone [[ ... ]] or (( ... )) region. Uses its own local
    # quote/escape state (never the main tokenizer loop) so it can run to
    # completion independently beforehand. A region opens on an unquoted,
    # unescaped "[[" or "((" -- for "((", ONLY when it is its own token
    # (is_opener_boundary) followed by whitespace (is_opener_followed_by_ws),
    # per Fix 5, and closes on the next well-formed, properly nested "))"
    # found by find_close() (depth-counting genuinely models real
    # arithmetic/subshell nesting here, per the #810 stabilization review
    # Bug 3 confirmation -- left unchanged). For "[[" (#810 stabilization
    # review, Bug 3 Vectors 2-3; re-narrowed by the round-3 review,
    # Finding 1), ONLY when it is preceded by the same minimal,
    # non-recursible boundary rule as "((" (is_bracket_command_position)
    # followed by whitespace (is_opener_followed_by_ws), and closes on the
    # FIRST standalone "]]" token found by find_close_bracket() -- the real
    # termination rule bash applies for a conditional expression, which does
    # NOT balance nested "["/"]" characters the way the arithmetic/subshell
    # nesting for "(( ))" genuinely does. An opener that fails either
    # boundary/position check is left completely alone (not even a
    # candidate); an opener that passes both but has no matching closer is
    # also left unmarked (anti-fail-open in both cases -- a construct this
    # scan cannot prove is a real, closed conditional/arithmetic region must
    # never suppress a real redirect). A quoted "[[" (e.g. inside a
    # single-quoted echo argument) never opens a region at all, since the
    # quote-state checks below consume it first.
    #
    # Round-4 addition: this SAME forward pass now also maintains the
    # rolling `lastsig`/`beforesig` state consumed by is_opener_boundary /
    # is_bracket_command_position (via is_boundary_char), replacing what was
    # previously a separate, stateless backward `substr()` re-scan of the
    # raw command text (is_opener_boundary used to walk backward from each
    # candidate position itself). `lastsig` is the last character seen so
    # far that reached this functions truly unquoted, unescaped dispatch
    # (the branches below this comment, never the rquote==1/rquote==2/
    # resc==1 early-continue branches above) AND was not a space/tab;
    # `beforesig` is whatever `lastsig` -- via the also-forward-scan-derived
    # `prevunq` (every unquoted/unescaped character, space/tab included) --
    # held immediately before that update. Both start at "" (start-of-
    # string). This single O(1)-extra-state-per-character forward pass gets
    # both round-4 fixes for free: (1) a backslash-escaped or quoted
    # `;`/`&`/`|` never reaches the dispatch that updates lastsig/beforesig,
    # so it can never count as a boundary; (2) `beforesig` lets
    # is_boundary_char reject a `&`/`|` that is really the tail of `>&`/
    # `<&`/`>|` rather than a bare separator. Updating lastsig/beforesig/
    # prevunq happens AFTER a candidate "[["/"((" is checked (so the check
    # sees state as of just before the candidate, exactly mirroring the old
    # backward scans intent of looking at what precedes this position) but
    # for EVERY character that reaches the dispatch, including "["/"("
    # themselves when they do not end up matching as a real, closed region
    # (so a later candidate correctly sees them as ordinary preceding
    # content), and including the quote-opening/escape-starting characters
    # themselves (a single quote, a double quote, a backslash) -- none of
    # which can ever equal the recognized separator or `>`/`<` values, so
    # including them in the rolling state is harmless. When a region IS
    # matched and suppressed, lastsig/beforesig/prevunq are advanced to
    # reflect the closing tokens own last two characters (e.g. the second
    # `]` of `]]`) before `ri` jumps past it, so a construct immediately
    # following the region still sees accurate preceding-character state.
    #
    # Round-5 addition (#810, CRITICAL; routing split by #810 stabilization):
    # once a candidate region is found closed (a matching, well-formed
    # "))"/"]]"), its INTERIOR (the span strictly between the two-char opener
    # and the two-char closer) is re-scanned by has_nested_substitution()
    # (above) before the suppress[] marking loop runs, and its return value
    # routed one of three ways:
    #   0 (no marker) -- suppress the region as an ordinary comparison,
    #     exactly as before.
    #   1 (marker found in UNQUOTED interior text) -- the region is NOT
    #     suppressed at all: nothing is marked for it, and every character in
    #     the span falls through to ordinary redirect/tee handling in the
    #     main tokenizer loop, exactly as an unclosed/malformed region already
    #     does. This is unaffected by the stabilization split -- the existing
    #     unquoted-path tokenizing already correctly extracts a real write
    #     nested this way, no special mechanism needed.
    #   2 (marker found INSIDE a double-quoted span within the interior) --
    #     this tokenizer cannot safely resume precise parsing through the
    #     real syntax of the substitution from inside the main loop own
    #     double-quote handling, so rather than attempt that, the WHOLE
    #     extraction fails closed immediately: `exit 7`. This exits the
    #     entire awk program right here, before the main tokenizing loop
    #     (which has not even started yet -- mark_regions() is a pre-pass) and
    #     before any emit() call, so the "non-zero exit -> emits nothing"
    #     contract holds trivially (see the bwt_extract_targets comment).
    # In cases 0 and 1, `ri` still advances past the whole matched region and
    # lastsig/beforesig/prevunq still update as if it were recognized (it WAS
    # a real, well-formed [[ ]]/(( )) as far as real bash grammar is
    # concerned -- only case 1 interior is deliberately left unsuppressed),
    # so a construct immediately following it still sees accurate boundary
    # state.
    function mark_regions(   ri, rc, rquote, resc, rnc, rk, close_pos, lastsig, beforesig, prevunq, nsub) {
      rquote = 0
      resc = 0
      lastsig = ""
      beforesig = ""
      prevunq = ""
      ri = 1
      while (ri <= n) {
        rc = substr(cmd, ri, 1)

        if (resc == 1) {
          resc = 0
          ri++
          continue
        }
        if (rquote == 1) {
          if (rc == sq) rquote = 0
          ri++
          continue
        }
        if (rquote == 2) {
          if (rc == dq) rquote = 0
          else if (rc == bsl) resc = 1
          ri++
          continue
        }

        # Unquoted, unescaped dispatch: `rc` is a real character bash itself
        # would see unquoted and unescaped at this exact position.

        if (rc == "[") {
          rnc = (ri + 1 <= n) ? substr(cmd, ri + 1, 1) : ""
          if (rnc == "[" && is_bracket_command_position(lastsig, beforesig) && is_opener_followed_by_ws(ri)) {
            close_pos = find_close_bracket(ri)
            if (close_pos > 0) {
              nsub = has_nested_substitution(ri + 2, close_pos - 2)
              if (nsub == 2) exit 7
              if (nsub == 0) {
                for (rk = ri; rk <= close_pos; rk++) suppress[rk] = 1
              }
              beforesig = (close_pos - 1 >= 1) ? substr(cmd, close_pos - 1, 1) : ""
              lastsig = substr(cmd, close_pos, 1)
              prevunq = lastsig
              ri = close_pos + 1
              continue
            }
          }
        }
        if (rc == "(") {
          rnc = (ri + 1 <= n) ? substr(cmd, ri + 1, 1) : ""
          if (rnc == "(" && is_opener_boundary(ri, 1, lastsig, beforesig) && is_opener_followed_by_ws(ri)) {
            close_pos = find_close(ri, "(", ")")
            if (close_pos > 0) {
              nsub = has_nested_substitution(ri + 2, close_pos - 2)
              if (nsub == 2) exit 7
              if (nsub == 0) {
                for (rk = ri; rk <= close_pos; rk++) suppress[rk] = 1
              }
              beforesig = (close_pos - 1 >= 1) ? substr(cmd, close_pos - 1, 1) : ""
              lastsig = substr(cmd, close_pos, 1)
              prevunq = lastsig
              ri = close_pos + 1
              continue
            }
          }
        }

        if (rc == sq) { rquote = 1 }
        else if (rc == dq) { rquote = 2 }
        else if (rc == bsl) { resc = 1 }

        if (rc != " " && rc != "\t") {
          beforesig = prevunq
          lastsig = rc
        }
        prevunq = rc
        ri++
      }
    }

    function flush(   wb, wregion) {
      # #810 Fix 4 (critical): capture-then-reset every PER-WORD flag
      # (wordbrace/bracedepth/bracehasdelim/curregion) unconditionally, at
      # the very top of flush(), BEFORE the `if (!wordhas) return` early
      # exit below. A `(( ... ))` / `$(( ... ))` region always ends by
      # dispatching a flush() call with no pending word (the region-closing
      # `)` characters are boundary characters, not word content) -- if the
      # early return ran first, curregion (and these brace flags) would
      # never be cleared for that no-op flush and would leak into the
      # state for the NEXT word instead. A leaked curregion=1 silently
      # suppressed tee-ARMING for a following `tee` word that was never
      # actually inside any region (e.g. `echo $((1+1)) | tee /repo/.env`),
      # a fail-open confirmed against real bash. Every word boundary must
      # clear the previous words leftover per-word flags regardless of
      # whether the new word turns out to be empty.
      wb = wordbrace
      wordbrace = 0
      bracedepth = 0
      bracehasdelim = 0
      wregion = curregion
      curregion = 0
      if (!wordhas) return
      w = word
      word = ""
      wordhas = 0
      # Literal-tilde neutralization: this word starts with ~ but did NOT
      # begin as a plain unquoted tilde (it was quoted, escaped, or preceded
      # by an empty quoted string) -- real shells treat it as a literal
      # filename character, so prefix "./" to keep the downstream safe-var
      # expander from tilde-expanding it (see the wtilde comment in BEGIN).
      if (substr(w, 1, 1) == "~" && !wtilde) w = "./" w
      wtilde = 0

      if (mode == "target") {
        # #810 Fix 1: an unresolved brace-expansion word reaching
        # write-target position fails the WHOLE extraction closed (exit 6)
        # rather than emitting the raw, un-expanded literal -- see the
        # bwt_extract_targets header comment.
        if (wb) exit 6
        emit(w)
        mode = ""
        return
      }
      if (mode == "fddup") {
        if (w !~ /^[0-9]+$/) {
          if (w != "-") {
            if (wb) exit 6
            emit(w)
          }
        }
        mode = ""
        return
      }

      if (tee == 1) {
        if (w == "--") {
          tee = 2
        } else if (substr(w, 1, 1) == "-") {
          # option word (e.g. -a) -- consumed, nothing to do
        } else {
          if (wb) exit 6
          emit(w)
          tee = 2
        }
        return
      }
      if (tee == 2) {
        if (wb) exit 6
        emit(w)
        return
      }

      # Plain (non-target, non-fddup, not-mid-tee-scan) word: a basename-"tee"
      # match is ALWAYS treated as a tee invocation, unconditionally,
      # regardless of position (#795 round 3 -- see the function-level
      # header comment for why command-position tracking was reverted).
      # #810 Fix 3: a word entirely inside a closed [[ ... ]] / (( ... ))
      # region never arms tee (defense-in-depth alongside the `>`
      # suppression above).
      bn = w
      sub(/.*\//, "", bn) # basename, so /usr/bin/tee and ./tee are matched too
      if (bn == "tee" && !wregion) tee = 1
    }

    # emit(w) -- buffer an extracted target into outbuf[] (printed only once
    # the whole scan completes successfully, see the print loop above), with
    # a buffer-time backstop (Fix A / #795 round 2 defense-in-depth) against
    # ever emitting a raw embedded newline: the caller side reads targets one
    # per physical line, so a word containing an embedded newline would
    # otherwise silently fragment into two separate (individually innocuous-
    # looking) lines. In practice this should no longer occur after the
    # esc==1 line-continuation fix above, but if it ever does, replace the
    # embedded newline with a character ("$") that bwt_is_unresolved already
    # treats as unresolved -- so a future edge case fails closed (block)
    # rather than silently fragmenting again.
    function emit(w2,   e) {
      outn++
      if (index(w2, nl) > 0) {
        e = w2
        gsub(nl, "$", e)
        outbuf[outn] = e
        return
      }
      outbuf[outn] = w2
    }
  '
}
