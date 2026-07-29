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
#   grammar is deliberately not modelled: brace expansion (`{tee,cat} f`),
#   heredoc bodies, and arbitrary metacharacter gluing all tokenize to
#   something that is not a recognized write construct. For those, the
#   guarantee is made by the callers' empty-parse backstop
#   (bwt_zero_parse_suspicious below): when a command looked like it might
#   write (a `>` anywhere, or a delimited `tee` token) but the tokenizer
#   extracted ZERO targets, both guards fall back to scanning the RAW command
#   text for their own sensitive markers and block on a hit. An unmodelled
#   construct therefore costs a false positive (an over-block the agent can
#   rephrase around), never a silent permit. Do not add per-construct parser
#   fixes to chase completeness — that loop does not converge (#795 review
#   rounds 1-5); the backstop is the terminating design.
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
# Functions:
#   bwt_has_write_candidate <command>    -- cheap early-exit predicate
#   bwt_extract_targets <command>        -- emits one raw target per line
#   bwt_zero_parse_suspicious <command>  -- empty-parse backstop trigger
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
# The delimited-tee scan runs in awk (already a hard dependency of
# bwt_extract_targets, which necessarily ran before any caller reaches this
# predicate). If awk vanished from PATH in between, fail closed: report
# suspicious rather than silently allowing.
bwt_zero_parse_suspicious() {
  case "$1" in
    *'>'*) return 0 ;;
  esac
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
# 0, which would be misread as "under threshold"). Callers must treat ANY
# non-zero exit from this function as fail-closed (block), never as an
# empty-result allow. The command string is
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

      i = 1
      while (i <= n) {
        c = substr(cmd, i, 1)
        i++

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
          if (c == dq) {
            quote = 0
          } else if (c == bsl) {
            esc = 1
          } else {
            word = word c
            wordhas = 1
          }
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

        # A plain unquoted tilde opening a word is the only tilde a real
        # shell expands -- record it so flush() can tell it apart from a
        # quoted/escaped literal ~ (see the wtilde comment in BEGIN).
        if (c == "~" && !wordhas && word == "") wtilde = 1
        word = word c
        wordhas = 1
      }
      flush()
    }

    function flush() {
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
        emit(w)
        mode = ""
        return
      }
      if (mode == "fddup") {
        if (w !~ /^[0-9]+$/) {
          if (w != "-") emit(w)
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
          emit(w)
          tee = 2
        }
        return
      }
      if (tee == 2) {
        emit(w)
        return
      }

      # Plain (non-target, non-fddup, not-mid-tee-scan) word: a basename-"tee"
      # match is ALWAYS treated as a tee invocation, unconditionally,
      # regardless of position (#795 round 3 -- see the function-level
      # header comment for why command-position tracking was reverted).
      bn = w
      sub(/.*\//, "", bn) # basename, so /usr/bin/tee and ./tee are matched too
      if (bn == "tee") tee = 1
    }

    # emit(w) -- print an extracted target, with a print-time backstop
    # (Fix A / #795 round 2 defense-in-depth) against ever emitting a raw
    # embedded newline: the caller side reads targets one per physical line,
    # so a word containing an embedded newline would otherwise silently
    # fragment into two separate (individually innocuous-looking) lines. In
    # practice this should no longer occur after the esc==1 line-continuation
    # fix above, but if it ever does, replace the embedded newline with a
    # character ("$") that bwt_is_unresolved already treats as unresolved --
    # so a future edge case fails closed (block) rather than silently
    # fragmenting again.
    function emit(w2,   e) {
      if (index(w2, nl) > 0) {
        e = w2
        gsub(nl, "$", e)
        print e
        return
      }
      print w2
    }
  '
}
