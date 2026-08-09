#!/usr/bin/env bash
# Repo-wide regression guard for ticket #797 (2/2 of #791): flags any
# `read_*`-named helper function whose body calls `fail()` directly, in
# scope EVERY tracked *.test.sh file repo-wide PLUS every tracked *.sh
# shell library under flow/tests/ (widened by #791; see TEST_PATHSPEC /
# LIB_PATHSPEC below). The library half exists because a sourced library's
# helpers execute in the SOURCING suite's shell, not the library file's own
# -- flow/tests/parity/contract-lib.sh's read_doc_raw (a real *.test.sh
# sources it via parity.test.sh) is the concrete example a *.test.sh-only
# scan would otherwise miss. Calling fail() (which mutates the parent shell's
# `failures` counter) from inside a helper that callers invoke via command
# substitution ($(read_doc ...)) silently drops the failure count once bash
# forks a subshell for the substitution -- see
# flow/docs/shell-scripting-gotchas.md's "never call fail() from within
# $(...)" rule. The required shape is a pure `<name>_raw` extractor (no
# fail() side effect, safe inside $(...)) paired with a `require_<name>`
# nameref wrapper that calls fail() in the parent shell -- see
# flow/tests/skill-convention-contract.test.sh:68-96.
#
# Three deliberate carve-outs, all proven by this suite's own fixture
# self-test below:
#   1. FAIL_INVOKE_RE matches real `fail` invocations, not the literal
#      string "fail(" -- it excludes `failures=`/`fail_count`-style
#      identifiers (the character immediately after "fail" must be
#      whitespace, "(", or end-of-line) and it skips comment lines (first
#      non-blank char "#"), since a compliant `<name>_raw` helper
#      legitimately documents "no fail() side effect here" in a leading
#      comment.
#   2. This file's own fixtures below intentionally embed the violating
#      shape as literal heredoc text, so the repo scan self-excludes this
#      file (SELF_REL) rather than flagging itself.
#   3. A non-test *.sh file outside flow/tests/ (e.g. flow/hooks/scripts/*.sh,
#      sandbox/lib/*.sh) is out of scope: it matches neither TEST_PATHSPEC
#      ('*.test.sh', so filename must end in .test.sh) nor LIB_PATHSPEC
#      ('flow/tests/*.sh', so it must live under flow/tests/) -- see the
#      "out of scope" fixtures in the enumerate_files fixture tree below.
#
# Hardened by #812: scan_file now returns a checked status through a
# result-file contract (never process substitution, whose exit status is
# unobservable to the caller -- see flow/docs/shell-scripting-gotchas.md
# rule 14) and recognizes three Bash function definition forms:
# `read_name() {`, `function read_name() {`, and `function read_name {`.
# A heredoc opened inside a read_* function's body (tracked from its
# opening `<<`/`<<-` delimiter, quoted or bare word, to its closing
# delimiter line) is treated as opaque: a literal column-zero `}` inside
# heredoc content never resets scan scope early. Residual limitations,
# deliberately out of scope: an indented (non-column-0) closing brace
# never resets scope; two heredocs opened on the same source line are not
# distinguished (only one active delimiter is tracked at a time); the
# literal word "fail" inside a string literal on a definition line can
# still be misattributed by FAIL_INVOKE_RE, which matches raw text before
# any expansion/quote stripping; and a quoted or escaped heredoc delimiter
# (`<<'EOF-1'`, `<<"EOF.txt"`, `<<\EOF`, `<<-'END 1'`) is not recognized by
# HEREDOC_START_RE, which only matches a bare `[A-Za-z_][A-Za-z0-9_]*`
# delimiter name (inside or outside quotes) -- such a heredoc is treated as
# ordinary code, so a literal column-zero `}` inside its body can still
# reset scope early; and a `<<` appearing inside a comment or a string
# literal on a definition/body line is not treated specially and is
# currently NOT excluded from heredoc-start detection except when the
# whole line is a recognized comment line (a whole-line comment is
# excluded by the comment-line skip running before heredoc-start
# detection) -- e.g. `fail "see << EOF"` on a non-comment line can still
# open a phantom heredoc.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "read-helper-purity-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "read-helper-purity-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "read-helper-purity-contract.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

SELF_ABS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
SELF_REL="${SELF_ABS#"${REPO_ROOT}/"}"

# Matches a real `fail` invocation: preceded by start-of-line or a
# non-identifier char, followed by whitespace, "(", or end-of-line. Excludes
# `failures=`/`fail_count` (the char right after "fail" is an identifier
# char, not in the trailing alternation) and any `_fail(...)` (the char
# right before "fail" is `_`, an identifier char, so the leading
# alternation's negated class rejects it).
FAIL_INVOKE_RE='(^|[^[:alnum:]_])fail([[:space:]]|\(|$)'
# Recognizes read_name() {, function read_name() {, and function
# read_name { (#812, confirmed gap #6). Partial-match (no trailing
# anchor), so trailing content on the definition line (e.g. the opening
# "{" itself, or more) is not required to be present in the regex.
READ_FUNC_START_RE='^[[:space:]]*(function[[:space:]]+read_[A-Za-z0-9_]*[[:space:]]*(\(\)[[:space:]]*)?\{?|read_[A-Za-z0-9_]*[[:space:]]*\(\))'
COMMENT_LINE_RE='^[[:space:]]*#'
# Matches a heredoc-start operator: <<, or <<- (tab-stripped terminator
# variant), followed by a single- or double-quoted or bare word delimiter.
# Callers must strip any "<<<" (herestring) substrings from the candidate
# line first -- a herestring's three adjacent "<" characters never leave a
# clean two-character "<<" run for this regex to match, so "<<<" is
# naturally excluded once stripped. An arithmetic shift like $(( x << 2 ))
# is excluded without any extra guard: its right-hand operand is a number,
# and this regex requires the token after "<<"/"<<-" to start with a
# letter or underscore.
HEREDOC_START_RE="<<(-)?[[:space:]]*('([A-Za-z_][A-Za-z0-9_]*)'|\"([A-Za-z_][A-Za-z0-9_]*)\"|([A-Za-z_][A-Za-z0-9_]*))"

# Scan scope: every tracked *.test.sh repo-wide (unchanged) plus every
# tracked *.sh shell library under flow/tests/ (widened by #791). Git
# pathspec '*' matches '/', so 'flow/tests/*.sh' also covers
# 'flow/tests/parity/contract-lib.sh'; the find fallback's equivalent
# predicate is `-path "${root}/flow/tests/*" -a -name '*.sh'`.
TEST_PATHSPEC='*.test.sh'
LIB_PATHSPEC='flow/tests/*.sh'

# def_line_code_raw <line> -- pure extractor (no fail() side effect; safe
# inside $(...)): strips ${...} parameter expansions, single- and
# double-quoted spans, and a trailing comment from a single source line
# via one sed -E. Used only to decide whether a definition line's own
# closing brace actually ends a function's scope: a `}` that only exists
# inside a quoted string or a parameter expansion like `${1}` must not be
# mistaken for the function's close (#812, confirmed gap #3).
def_line_code_raw() {
  local line="$1"
  printf '%s' "${line}" | sed -E \
    -e 's/\$\{[^}]*\}//g' \
    -e "s/'[^']*'//g" \
    -e 's/"[^"]*"//g' \
    -e 's/[[:space:]]*#.*$//'
}

# scan_file <abs-path> <display-path> <result-file> -- truncates
# <result-file>, then writes every "<display>:<line>" hit for a `fail`
# invocation found inside a `read_*`-named function's body into it.
# Returns 0 for a clean scan (result file may be empty), 1 if <abs-path>
# cannot be read or a heredoc never closes before EOF, in which case
# <result-file> contains exactly one line ("<display>:UNREADABLE" or
# "<display>:UNTERMINATED-HEREDOC" respectively -- never both a clean scan
# and an error line), and 2 if <result-file> itself could not be truncated
# (its content is then unreliable/stale and MUST NOT be read by the
# caller -- see scan_into, which special-cases status 2 to avoid
# misattributing a prior call's leftover content to the current file). The
# read is a single checked `cat` command
# substitution rather than `while read ... done < "${abs}"`: a directory
# or otherwise-unreadable path makes `cat` fail with one observable status
# (covers ENOENT/EACCES/EISDIR alike) instead of leaving a `while read`
# loop variable unset mid-iteration and aborting the whole process under
# `set -u` (#812, confirmed gap #2). State machine: enters a read-function
# on a definition-line match (READ_FUNC_START_RE), exits on a column-0
# `}` -- except while a heredoc is open (HEREDOC_START_RE), whose body
# lines are opaque and never treated as a scope-closing brace. The
# definition line itself is also scanned (not skipped via an
# unconditional `continue`), so a single-line helper like
# `read_doc() { fail "..."; }` is flagged too; if that same line also
# carries a closing `}`, the decision is made only after
# def_line_code_raw strips `${...}`/quoted spans/comments, so a `}` from
# parameter expansion does not exit the function early.
scan_file() {
  local abs="$1" display="$2" result_file="$3"
  local -a lines=()
  local line i n in_read_func=0 just_entered
  local in_heredoc=0 heredoc_delim="" heredoc_strip_tabs=0
  local content check_line def_code

  # Distinct status (2) from every other error path: a truncate failure
  # means result_file's own content is unreliable/stale (possibly a PRIOR
  # call's leftovers), so the caller must not read it as this call's
  # diagnostic -- see scan_into.
  : > "${result_file}" || return 2

  if ! content="$(cat "${abs}" 2>/dev/null)"; then
    printf '%s:UNREADABLE\n' "${display}" > "${result_file}"
    return 1
  fi

  mapfile -t lines <<< "${content}"
  n=${#lines[@]}
  for ((i = 0; i < n; i++)); do
    line="${lines[i]}"
    just_entered=0

    if [[ "${in_heredoc}" -eq 1 ]]; then
      if [[ "${heredoc_strip_tabs}" -eq 1 ]]; then
        [[ "${line}" =~ ^$'\t'*${heredoc_delim}$ ]] && in_heredoc=0
      else
        [[ "${line}" == "${heredoc_delim}" ]] && in_heredoc=0
      fi
      continue
    fi

    if [[ "${in_read_func}" -eq 0 ]]; then
      if [[ "${line}" =~ $READ_FUNC_START_RE ]]; then
        in_read_func=1
        just_entered=1
      else
        continue
      fi
    fi

    if [[ "${just_entered}" -eq 0 && "${line}" =~ ^\} ]]; then
      in_read_func=0
      continue
    fi

    # Comment-line skip must run BEFORE heredoc-start detection (security
    # review item 2): otherwise a comment line inside a read_* body (e.g.
    # `# built with cat <<EOF`) opens a phantom heredoc, silently
    # skipping every following body line -- including a genuine fail()
    # call -- until a line matching the phantom delimiter is found.
    [[ "${line}" =~ $COMMENT_LINE_RE ]] && continue

    check_line="${line//<<</}"
    if [[ "${check_line}" =~ $HEREDOC_START_RE ]]; then
      in_heredoc=1
      heredoc_strip_tabs=0
      [[ -n "${BASH_REMATCH[1]}" ]] && heredoc_strip_tabs=1
      if [[ -n "${BASH_REMATCH[3]}" ]]; then
        heredoc_delim="${BASH_REMATCH[3]}"
      elif [[ -n "${BASH_REMATCH[4]}" ]]; then
        heredoc_delim="${BASH_REMATCH[4]}"
      else
        heredoc_delim="${BASH_REMATCH[5]}"
      fi
    fi

    if [[ "${line}" =~ $FAIL_INVOKE_RE ]]; then
      # Checked append (security review item 1): if the append itself
      # fails (ENOSPC, result_file removed mid-scan), result_file's
      # content is unreliable/stale for this call -- the same fail-closed
      # shape as the truncate failure above, via scan_into's existing
      # rc==2 handling.
      printf '%s:%d\n' "${display}" "$((i + 1))" >> "${result_file}" || return 2
    fi

    if [[ "${just_entered}" -eq 1 ]]; then
      def_code="$(def_line_code_raw "${line}")"
      [[ "${def_code}" == *"}"* ]] && in_read_func=0
    fi
  done

  # Fail-closed on an unterminated heredoc (security review item 2): if a
  # heredoc-start was detected but its closing delimiter line never
  # appeared before EOF, `in_heredoc` would otherwise stay 1 through every
  # remaining line -- silently skipping a real fail() call in that tail
  # and letting scan_file return 0 (a false "clean scan"). Overwrite
  # result_file (discarding any earlier hits found before the heredoc
  # opened -- an incomplete scan cannot be trusted) with a single
  # diagnostic line and return a checked error, the same fail-closed shape
  # as the UNREADABLE path above.
  if [[ "${in_heredoc}" -eq 1 ]]; then
    printf '%s:UNTERMINATED-HEREDOC\n' "${display}" > "${result_file}"
    return 1
  fi

  return 0
}

# =====================================================================
# Fixture self-test -- proves the detector itself before trusting it on the
# repo scan. Temp dir outside the repo so neither the flow gate's `find`
# nor this suite's own repo scan (which also excludes this file via
# SELF_REL) sees the fixtures.
# =====================================================================
FIXTURE_DIR="$(mktemp -d)" || { echo "read-helper-purity-contract.test.sh: failed to create fixture directory." >&2; exit 2; }
trap 'rm -rf "${FIXTURE_DIR}"' EXIT

# SCAN_OUT -- single reusable result file for scan_into's checked
# scan_file calls; each call truncates and rewrites it, so results must be
# consumed before scan_into is invoked again (always true here: every
# call site processes its output synchronously, before the next call).
SCAN_OUT="$(mktemp "${FIXTURE_DIR}/scan-out-XXXXXX")" || { echo "read-helper-purity-contract.test.sh: failed to create scan-out temp file." >&2; exit 2; }

# enumerate_files <root> <git|find|auto> -- prints NUL-separated paths,
# relative to <root>, for every file in scope: TEST_PATHSPEC repo-wide plus
# LIB_PATHSPEC under flow/tests/, deduped so a file matching both (e.g. a
# *.test.sh directly under flow/tests/) is emitted exactly once regardless
# of which enumerator produced it. mode "auto" picks git when <root> is
# inside a work tree, else find -- callers that need a specific path
# deterministically exercised (the fixture tree below) always pass an
# explicit git|find rather than auto. Returns 1 (with no output) on an
# internal enumeration failure (mktemp, or find failing outright even as
# git's fallback) so callers that check the exit status directly (rather
# than relying on `< <(...)` process substitution, whose status is
# unobservable) can tell "enumeration failed" apart from "matched nothing".
#   git  -- git ls-files (index-scoped; no prune needed). Output is
#     redirected into a real file inside FIXTURE_DIR (already EXIT-trapped)
#     with its own status checked directly, rather than via a process
#     substitution subshell whose status would be unavailable here -- an
#     unchecked partial failure could otherwise be mistaken for "0 files".
#     If `git ls-files` itself fails, this falls back to the find logic
#     below instead of silently returning an empty set (mirrors the
#     pre-widening behavior of falling back to find on a git failure).
#   find -- used directly when mode is "find", and as the git branch's
#     fallback above; prunes .worktrees/.claude/worktrees/node_modules
#     (git ls-files is index-scoped and needs none of those prunes). The
#     `-path "<escaped-root>/flow/tests/*" -a -name '*.sh'` clause is the
#     find equivalent of LIB_PATHSPEC; <root> is glob-escaped (`[`, `]`,
#     `*`, `?`) before being embedded in the -path pattern so a checkout
#     path containing those characters can't corrupt the match. Output is
#     likewise redirected into a real file inside FIXTURE_DIR with its exit
#     status checked directly, rather than the unchecked
#     `< <(find ... -print0)` process substitution this used to use.
enumerate_files() {
  local root="$1" mode="$2"
  if [[ "${mode}" == "auto" ]]; then
    # env -u: same guard as the git ls-files call below -- an inherited
    # GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE must never make this
    # mode-selection probe answer for the wrong repo.
    if env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE git -C "${root}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
      mode="git"
    else
      mode="find"
    fi
  fi

  local -a raw=()
  local need_find=0
  if [[ "${mode}" == "git" ]]; then
    local out
    out="$(mktemp "${FIXTURE_DIR}/enumerate-git-XXXXXX")" || return 1
    # env -u: an inherited GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE must never
    # redirect this call at the wrong repo -- load-bearing when <root> is
    # the synthetic fixture tree, harmless when <root> is REPO_ROOT.
    if env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE git -C "${root}" ls-files -z -- "${TEST_PATHSPEC}" "${LIB_PATHSPEC}" > "${out}"; then
      while IFS= read -r -d '' p; do
        raw+=("${p}")
      done < "${out}"
    else
      # git ls-files failed -- fall back to find rather than silently
      # returning success with an empty set (a failure indistinguishable
      # from "matched nothing").
      need_find=1
    fi
    rm -f "${out}"
  else
    need_find=1
  fi

  if [[ "${need_find}" -eq 1 ]]; then
    local find_out root_escaped
    root_escaped="$(printf '%s' "${root}" | sed 's/[][*?]/\\&/g')"
    find_out="$(mktemp "${FIXTURE_DIR}/enumerate-find-XXXXXX")" || return 1
    if find "${root}" -type f \( -name "${TEST_PATHSPEC}" -o \( -path "${root_escaped}/flow/tests/*" -a -name '*.sh' \) \) \
      -not -path '*/.worktrees/*' -not -path '*/.claude/worktrees/*' -not -path '*/node_modules/*' -print0 > "${find_out}"; then
      while IFS= read -r -d '' p; do
        raw+=("${p#"${root}/"}")
      done < "${find_out}"
    else
      rm -f "${find_out}"
      return 1
    fi
    rm -f "${find_out}"
  fi

  local -A seen=()
  local p
  for p in "${raw[@]}"; do
    [[ -n "${seen[${p}]:-}" ]] && continue
    seen["${p}"]=1
    printf '%s\0' "${p}"
  done
}

# scan_into <abs-path> <display-path> <accumulator-array-name> --
# plumbing wrapper around scan_file: reuses the single SCAN_OUT result
# file, and on a non-zero scanner status calls fail() in THIS (parent)
# shell, quoting the exact "<display>:UNREADABLE" (or
# "<display>:UNTERMINATED-HEREDOC") payload -- the failing path's line is
# deliberately NOT appended to the accumulator (an unreadable/unscannable
# file has no scannable violations, only a scan error). Status 2
# (result_file's own truncate failed) is special-cased FIRST and never
# reads SCAN_OUT at all: SCAN_OUT's content in that case may be stale
# leftovers from a PRIOR scan_into call (this call couldn't write to it),
# so reading it here would misattribute a prior file's error to the
# current one (silent-failure review item 4) -- fail() is instead given a
# hardcoded "<display>:RESULT_FILE_UNWRITABLE" marker that names only the
# current call's display path. Nameref target uses distinctive local names
# (_scan_acc_ref / _scan_into_acc / _scan_into_lines) to avoid a
# nameref-to-itself collision if a caller ever passed an accumulator
# variable with a colliding name.
scan_into() {
  local abs="$1" display="$2" _scan_acc_ref="$3"
  local -n _scan_into_acc="${_scan_acc_ref}"
  local -a _scan_into_lines=()
  local _scan_into_rc=0 _scan_into_content=""
  scan_file "${abs}" "${display}" "${SCAN_OUT}" || _scan_into_rc=$?
  if [[ "${_scan_into_rc}" -eq 2 ]]; then
    fail "scan error: ${display}:RESULT_FILE_UNWRITABLE"
    return
  fi
  if [[ "${_scan_into_rc}" -ne 0 ]]; then
    [[ -s "${SCAN_OUT}" ]] && _scan_into_content="$(cat "${SCAN_OUT}")"
    fail "scan error: ${_scan_into_content:-${display}:UNREADABLE}"
    return
  fi
  if [[ -s "${SCAN_OUT}" ]]; then
    mapfile -t _scan_into_lines < "${SCAN_OUT}"
    _scan_into_acc+=("${_scan_into_lines[@]}")
  fi
}

# expect_one_failure <label> <cmd...> -- proves a failure mechanism fires
# in THIS (parent) shell without leaving the shipped suite red: snapshots
# `failures`, runs <cmd...> directly (no subshell, so its own fail() calls
# mutate this shell's counter), asserts the delta is exactly +1, then
# restores `failures` to the snapshot regardless of outcome. A wrong delta
# additionally calls fail() itself (after the restore), so an unexpected
# extra or missing failure becomes one explicit FAIL rather than silence
# (Q&A #2).
expect_one_failure() {
  local label="$1"
  shift
  local before="${failures}"
  "$@"
  local after="${failures}"
  local delta=$((after - before))
  failures="${before}"
  [[ "${delta}" -eq 1 ]] || fail "expect_one_failure ${label}: expected exactly 1 failure, observed ${delta}"
}

# assert_scan <abs> <label> [<expected-result-line>...] -- asserts
# scan_file returns status 0 (a clean scan) and its result file contains
# exactly the given ordered set of "<label>:<line>" hits, no more, no
# fewer, in order.
assert_scan() {
  local abs="$1" label="$2"
  shift 2
  local -a expected=("$@")
  local result_file
  result_file="$(mktemp "${FIXTURE_DIR}/scan-XXXXXX")" || { fail "assert_scan ${label}: failed to create result temp file"; return; }
  local rc=0
  scan_file "${abs}" "${label}" "${result_file}" || rc=$?
  local -a got=()
  [[ -s "${result_file}" ]] && mapfile -t got < "${result_file}"
  rm -f "${result_file}"
  if [[ "${rc}" -ne 0 ]]; then
    fail "fixture ${label}: expected a clean scan (status 0), got status ${rc} (result: ${got[*]:-none})"
    return
  fi
  if [[ "${got[*]:-}" != "${expected[*]:-}" ]]; then
    fail "fixture ${label}: expected result lines {${expected[*]:-none}}, got {${got[*]:-none}}"
  fi
}

# assert_scan_error <abs> <label> <expected-result-line> -- asserts
# scan_file returns non-zero status and its result file contains exactly
# the one given line (typically "<label>:UNREADABLE") (#812, confirmed
# gap #5: cardinality alone cannot tell "the right diagnostic" apart from
# "any other single-line output").
assert_scan_error() {
  local abs="$1" label="$2" expected_line="$3"
  local result_file
  result_file="$(mktemp "${FIXTURE_DIR}/scan-err-XXXXXX")" || { fail "assert_scan_error ${label}: failed to create result temp file"; return; }
  local rc=0
  scan_file "${abs}" "${label}" "${result_file}" || rc=$?
  local content=""
  [[ -s "${result_file}" ]] && content="$(cat "${result_file}")"
  rm -f "${result_file}"
  if [[ "${rc}" -eq 0 ]]; then
    fail "fixture ${label}: expected a non-zero (error) scan status, got 0 (result: ${content:-<empty>})"
    return
  fi
  if [[ "${content}" != "${expected_line}" ]]; then
    fail "fixture ${label}: expected result file content exactly '${expected_line}', got '${content:-<empty>}'"
  fi
}

# violating.test.sh -- read_doc() whose body calls fail(): exactly 1 hit,
# at line 9 (the fail() call inside the if-block).
VIOLATING="${FIXTURE_DIR}/violating.test.sh"
cat > "${VIOLATING}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
read_doc() {
  local path="$1"
  local content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "$1: doc not found/unreadable: ${path}"
    printf ''
    return 1
  fi
  printf '%s' "${content}"
}
EOF
assert_scan "${VIOLATING}" "violating" "violating:9"

# oneliner.test.sh -- read_doc() defined and closed entirely on a single
# line, whose body calls fail() on that same line, followed by an unrelated
# outside fail(): exactly 1 hit, and it must be the in-function line (5),
# not the outside one (6). Proves the definition line is scanned rather
# than unconditionally skipped, and that a same-line closing `}` resets
# in_read_func immediately instead of leaving the state machine stuck open
# past the end of the function. Confirmed gap #4: the original fixture
# ended at EOF right after the function, so a scanner that never resets
# in_read_func on a same-line close would still report exactly 1 hit --
# nothing followed to misattribute. The trailing outside fail() line is
# the actual reset proof: a scanner stuck open would flag it too (2 hits).
ONELINER="${FIXTURE_DIR}/oneliner.test.sh"
cat > "${ONELINER}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
read_doc() { fail "should be flagged - oneliner"; }
fail "should not be flagged - outside"
EOF
assert_scan "${ONELINER}" "oneliner" "oneliner:5"

# clean.test.sh -- read_doc_raw() whose body carries a "no fail() side
# effect" comment (proves carve-out 1's comment-skip: without it, this
# comment line's literal "fail()" text would itself match FAIL_INVOKE_RE)
# plus a `local failures=0` assignment (proves carve-out 1's
# identifier-boundary rule: `failures=` must never be treated as a `fail`
# invocation): 0 hits.
CLEAN="${FIXTURE_DIR}/clean.test.sh"
cat > "${CLEAN}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
read_doc_raw() {
  # read_doc_raw <path> -- pure extraction, no fail() side effect here: it
  # is deliberately safe to call inside a $(...) command substitution.
  local path="$1"
  local failures=0
  cat "${path}" 2>/dev/null
}
EOF
assert_scan "${CLEAN}" "clean"

# unreadable.test.sh -- a regular file with no read permission: exactly the
# diagnostic "unreadable:UNREADABLE", proving scan_file's readability check
# is load-bearing rather than a silent "0 violations" false-clear.
# DAC-specific like flow/tests/root-safe-perms-contract.test.sh's own
# "unreadable" fixture: uid 0 bypasses read permission on any regular
# file, so this is a guard-and-skip-under-root case, not a root-proof
# lever.
if [[ "$(id -u)" -eq 0 ]]; then
  echo "SKIP: fixture 'unreadable' requires a non-root user (uid 0 can read any regular file)"
else
  UNREADABLE="${FIXTURE_DIR}/unreadable.test.sh"
  cat > "${UNREADABLE}" <<'EOF'
#!/usr/bin/env bash
echo "no read_* helpers in this file"
EOF
  # root-safe: DAC-specific branch, root cannot reach it any other way
  chmod 000 "${UNREADABLE}"
  assert_scan_error "${UNREADABLE}" "unreadable" "unreadable:UNREADABLE"
  chmod 644 "${UNREADABLE}"
fi

# =====================================================================
# Bash-form / heredoc-scoping fixtures (#812) -- prove the scanner's
# claimed coverage of every documented definition form and the heredoc
# carve-out.
# =====================================================================

# multiline.test.sh -- read_doc() { local path="${1}" spans multiple
# lines; the definition line's own `}` comes from `${1}` parameter
# expansion, not a function close. Confirmed gap #3: an unstripped
# definition-line reset that checks for literal `}` substring presence
# would reset scanning immediately on this line and hide the later
# fail() at line 5.
MULTILINE="${FIXTURE_DIR}/multiline.test.sh"
cat > "${MULTILINE}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
read_doc() { local path="${1}"
  local content
  fail "should be flagged - multiline"
  printf '%s' "${content}"
}
EOF
assert_scan "${MULTILINE}" "multiline" "multiline:5"

# func-paren.test.sh -- `function read_doc() { ... }`: a valid Bash
# function definition form a bare `read_name(...)`-only start regex would
# miss entirely. Confirmed gap #6.
FUNC_PAREN="${FIXTURE_DIR}/func-paren.test.sh"
cat > "${FUNC_PAREN}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
function read_doc() {
  fail "should be flagged - func paren"
}
EOF
assert_scan "${FUNC_PAREN}" "func-paren" "func-paren:4"

# func-noparen.test.sh -- `function read_doc { ... }`: the parenthesis-
# less function-keyword form, likewise missed by a bare-only start regex.
# Confirmed gap #6.
FUNC_NOPAREN="${FIXTURE_DIR}/func-noparen.test.sh"
cat > "${FUNC_NOPAREN}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
function read_doc {
  fail "should be flagged - func noparen"
}
EOF
assert_scan "${FUNC_NOPAREN}" "func-noparen" "func-noparen:4"

# heredoc.test.sh -- a read_* function whose body embeds a heredoc whose
# content contains a literal column-zero `}` before the function's real
# in-function fail() call at line 7. A scanner that treats any column-zero
# `}` as the function's close regardless of heredoc context would exit
# scan scope at the heredoc's literal `}` line and hide the later fail().
# Required behavior: heredoc content must not terminate scanning early.
HEREDOC="${FIXTURE_DIR}/heredoc.test.sh"
cat > "${HEREDOC}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
read_doc() {
  cat <<EOD
}
EOD
  fail "should be flagged - after heredoc"
}
EOF
assert_scan "${HEREDOC}" "heredoc" "heredoc:7"

# unterminated-heredoc.test.sh -- a heredoc-start (`<<EOD`) whose closing
# delimiter line never appears before EOF: `in_heredoc` would otherwise
# stay 1 through the file's end, silently skipping the fail() call that
# lives inside its (never-closing) body, and scan_file would return 0 (a
# false "clean scan"). Security review item 2: proves scan_file now fails
# closed on this shape instead of silently scanning nothing.
UNTERMINATED_HEREDOC="${FIXTURE_DIR}/unterminated-heredoc.test.sh"
cat > "${UNTERMINATED_HEREDOC}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
read_doc() {
  cat <<EOD
  fail "should never be scanned - inside an unterminated heredoc body"
EOF
assert_scan_error "${UNTERMINATED_HEREDOC}" "unterminated-heredoc" "unterminated-heredoc:UNTERMINATED-HEREDOC"

# =====================================================================
# Fake fallback-find enumeration guard (AC1) -- a fallback find that emits
# one NUL-delimited path and then exits non-zero must never look like
# "matched one file": enumerate_files must return non-zero AND emit zero
# bytes of output (no partial list scanned as complete). drive_fake_find_
# enumeration is called DIRECTLY (not wrapped in expect_one_failure): its
# own direct `[[ ... ]] || fail ...` assertion calls fail() in THIS
# (parent) shell on the regression path only, so correct fail-closed
# behavior yields 0 additional fail() calls (suite stays green) and a
# regression yields exactly 1 (suite goes red) -- the delta genuinely
# differs between the two cases. (A prior version routed the "did
# enumerate_files fail closed correctly" check itself through
# expect_one_failure and called fail() on BOTH the correct-behavior and
# regression branches, so the asserted delta was +1 either way -- a
# tautology that could never catch a regression; #812 review.) PATH-stub
# idiom: flow/tests/maintain.test.sh case 41c (fake realpath),
# flow/tests/parent-close-reconcile.test.sh header.
# =====================================================================
FAKE_FIND_BIN_DIR="${FIXTURE_DIR}/fake-find-bin"
mkdir -p "${FAKE_FIND_BIN_DIR}" || { echo "read-helper-purity-contract.test.sh: failed to create fake-find bin directory." >&2; exit 2; }
if ! cat > "${FAKE_FIND_BIN_DIR}/find" <<'EOF'
#!/usr/bin/env bash
# fake fallback find: emits one NUL-delimited path then exits non-zero,
# simulating a find that dies partway through enumeration.
printf '%s/flow/tests/a.test.sh\0' "$1"
exit 1
EOF
then
  echo "read-helper-purity-contract.test.sh: failed to write fake find stub." >&2
  exit 2
fi
chmod +x "${FAKE_FIND_BIN_DIR}/find" || { echo "read-helper-purity-contract.test.sh: failed to chmod fake find stub." >&2; exit 2; }

# drive_fake_find_enumeration -- runs enumerate_files with the fake find on
# PATH and directly asserts the fail-closed contract: non-zero exit AND
# zero bytes of output. fail() fires only on the regression branch (the
# `||` right-hand side), never on the correct-behavior branch.
drive_fake_find_enumeration() {
  local out rc=0
  out="$(mktemp "${FIXTURE_DIR}/fake-find-enum-XXXXXX")" || { fail "drive_fake_find_enumeration: failed to create temp file"; return; }
  PATH="${FAKE_FIND_BIN_DIR}:${PATH}" enumerate_files "${FIXTURE_DIR}" find > "${out}" 2>/dev/null || rc=$?
  local bytes
  # Checked substitution (security review item 3): an unchecked failure
  # here would leave bytes="", and `[[ "" -eq 0 ]]` evaluates true in
  # Bash, letting the "zero bytes" half of the AC1 guard below pass
  # vacuously for the wrong reason. -1 can never equal a legitimate byte
  # count, so a wc failure correctly fails the -eq 0 check.
  bytes="$(wc -c < "${out}")" || bytes=-1
  rm -f "${out}"
  [[ "${rc}" -ne 0 && "${bytes}" -eq 0 ]] || fail "enumerate_files ${FIXTURE_DIR} find (fake find): expected non-zero exit AND zero bytes of output (AC1 fail-closed), got rc=${rc} bytes=${bytes} (a partial list may have been scanned as complete, or the fallback find's failure vanished silently)"
}
drive_fake_find_enumeration

# =====================================================================
# enumerate_files fixture tree (#791) -- proves the enumeration reaches
# sourced *.sh libraries under flow/tests/ (e.g.
# flow/tests/parity/contract-lib.sh's read_doc_raw), not only tracked
# *.test.sh files, in both the git ls-files and find enumeration paths.
# =====================================================================
TREE_DIR="${FIXTURE_DIR}/tree"
mkdir -p "${TREE_DIR}/flow/tests/parity" "${TREE_DIR}/flow/hooks/scripts" "${TREE_DIR}/other" \
  || { echo "read-helper-purity-contract.test.sh: failed to create fixture tree directories." >&2; exit 2; }

# in-scope: sourced library under flow/tests/ with a fail()-calling
# read_doc -- a synthetic stand-in for the shape a defective sourced
# library would take (contract-lib.sh's real read_doc_raw is, and always
# was, compliant; this fixture proves the scanner would catch it if it
# weren't). scan_file coverage for this fixture (asserting the single
# violation it produces once enumerated) is exercised further down.
cat > "${TREE_DIR}/flow/tests/parity/lib-bad.sh" <<'EOF'
#!/usr/bin/env bash
# read_doc <path> -- defective: calls fail() from inside a helper callers
# invoke via $(...), silently dropping the parent shell's failure count.
read_doc() {
  local path="$1" content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "doc not found/unreadable: ${path}"
    return 1
  fi
  printf '%s' "${content}"
}
EOF

# in-scope: sourced library under flow/tests/, compliant read_doc_raw --
# pure extractor, no fail() side effect.
cat > "${TREE_DIR}/flow/tests/lib-clean.sh" <<'EOF'
#!/usr/bin/env bash
# read_doc_raw <path> -- pure extraction, no fail() side effect here: it is
# deliberately safe to call inside a $(...) command substitution.
read_doc_raw() {
  local path="$1"
  cat "${path}" 2>/dev/null
}
EOF

# in-scope: tracked test file under flow/tests/ (already covered by the
# unwidened *.test.sh-only scan).
cat > "${TREE_DIR}/flow/tests/a.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "in scope: *.test.sh under flow/tests/"
EOF

# out of scope: non-test *.sh outside flow/tests/ -- this is the third
# documented carve-out. Excluded by LIB_PATHSPEC (a shell library must
# live under flow/tests/) and by TEST_PATHSPEC (its filename does not end
# in .test.sh).
cat > "${TREE_DIR}/flow/hooks/scripts/helper.sh" <<'EOF'
#!/usr/bin/env bash
echo "out of scope: not under flow/tests/ and not *.test.sh"
EOF

cat > "${TREE_DIR}/other/notes.sh" <<'EOF'
#!/usr/bin/env bash
echo "out of scope: not under flow/tests/ and not *.test.sh"
EOF

# in-scope via the repo-wide *.test.sh half (outside flow/tests/, but
# TEST_PATHSPEC='*.test.sh' matches regardless of directory).
cat > "${TREE_DIR}/other/b.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "in scope: *.test.sh matches repo-wide regardless of directory"
EOF

EXPECTED_TREE_SET=(
  "flow/tests/a.test.sh"
  "flow/tests/lib-clean.sh"
  "flow/tests/parity/lib-bad.sh"
  "other/b.test.sh"
)

# assert_enumerate_set <root> <git|find> -- asserts enumerate_files <root>
# <mode> returns exactly EXPECTED_TREE_SET (order-independent). The
# comparison and fail() call both run directly in this (parent) shell.
# enumerate_files' NUL-separated stdout is captured into a real checked
# temp file rather than a `< <(...)` process substitution, whose exit
# status would be unobservable here -- enumerate_files' own internal
# failures (e.g. mktemp) must not look identical to "enumerated zero
# files".
assert_enumerate_set() {
  local root="$1" mode="$2"
  local -a got=()
  local out
  out="$(mktemp "${FIXTURE_DIR}/assert-enumerate-XXXXXX")" || { fail "assert_enumerate_set ${root} ${mode}: failed to create temp file"; return; }
  if ! enumerate_files "${root}" "${mode}" > "${out}" 2>/dev/null; then
    fail "enumerate_files ${root} ${mode}: enumeration failed (non-zero exit)"
    rm -f "${out}"
    return
  fi
  while IFS= read -r -d '' p; do got+=("${p}"); done < "${out}"
  rm -f "${out}"
  local -a got_sorted=() expected_sorted=()
  mapfile -t got_sorted < <(printf '%s\n' "${got[@]:-}" | sort)
  mapfile -t expected_sorted < <(printf '%s\n' "${EXPECTED_TREE_SET[@]}" | sort)
  if [[ "${got_sorted[*]}" != "${expected_sorted[*]}" ]]; then
    fail "enumerate_files ${root} ${mode}: expected {${expected_sorted[*]}}, got {${got_sorted[*]:-<empty>}}"
  fi
}

assert_enumerate_set "${TREE_DIR}" find

# git mode: explicit `env -u` on every git call so an inherited
# GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE can never redirect these calls at
# the wrong repo (see Risks in the plan); no commit needed since
# `ls-files` reads the index directly after `add -A`.
TREE_GIT_OK=1
env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE git -C "${TREE_DIR}" init -q >/dev/null 2>&1 || TREE_GIT_OK=0
if [[ "${TREE_GIT_OK}" -eq 1 ]]; then
  env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE git -C "${TREE_DIR}" add -A >/dev/null 2>&1 || TREE_GIT_OK=0
fi
if [[ "${TREE_GIT_OK}" -eq 1 ]]; then
  assert_enumerate_set "${TREE_DIR}" git
else
  echo "SKIP: fixture tree git mode -- 'git init'/'git add' failed in ${TREE_DIR}"
fi

# End-to-end: run scan_into over the set enumerate_files (find mode) just
# proved is in scope, asserting exactly one violation and that it lands in
# the positive fixture, flow/tests/parity/lib-bad.sh -- proving the widened
# enumeration doesn't just return the right paths but that scan_into, fed
# those paths, actually catches the defective library (lib-bad.sh's
# fail()-calling read_doc) and stays silent on the compliant one
# (lib-clean.sh's read_doc_raw).
declare -a TREE_VIOLATIONS=()
TREE_ENUM_OUT="$(mktemp "${FIXTURE_DIR}/tree-enum-XXXXXX")" || { echo "read-helper-purity-contract.test.sh: failed to create tree enumeration temp file." >&2; exit 2; }
if enumerate_files "${TREE_DIR}" find > "${TREE_ENUM_OUT}" 2>/dev/null; then
  while IFS= read -r -d '' rel; do
    scan_into "${TREE_DIR}/${rel}" "${rel}" TREE_VIOLATIONS
  done < "${TREE_ENUM_OUT}"
else
  fail "enumerate_files ${TREE_DIR} find: enumeration failed (non-zero exit)"
fi
rm -f "${TREE_ENUM_OUT}"
if [[ "${#TREE_VIOLATIONS[@]}" -ne 1 ]]; then
  fail "scan_into over enumerate_files(${TREE_DIR}, find): expected exactly 1 violation, got ${#TREE_VIOLATIONS[@]} (${TREE_VIOLATIONS[*]:-none})"
elif [[ "${TREE_VIOLATIONS[0]}" != "flow/tests/parity/lib-bad.sh:"* ]]; then
  fail "scan_into over enumerate_files(${TREE_DIR}, find): expected the single violation in flow/tests/parity/lib-bad.sh, got ${TREE_VIOLATIONS[0]}"
fi

# =====================================================================
# Repo-wide scan
# =====================================================================
declare -a FILES=()
REPO_ENUM_OUT="$(mktemp "${FIXTURE_DIR}/repo-enum-XXXXXX")" || { echo "read-helper-purity-contract.test.sh: failed to create repo enumeration temp file." >&2; exit 2; }
if enumerate_files "${REPO_ROOT}" auto > "${REPO_ENUM_OUT}" 2>/dev/null; then
  while IFS= read -r -d '' p; do
    FILES+=("${p}")
  done < "${REPO_ENUM_OUT}"
else
  fail "enumerate_files ${REPO_ROOT} auto: enumeration failed (non-zero exit)"
fi
rm -f "${REPO_ENUM_OUT}"

declare -a VIOLATIONS=()
scanned=0
self_skip_count=0
for rel in "${FILES[@]}"; do
  if [[ "${rel}" == "${SELF_REL}" ]]; then
    self_skip_count=$((self_skip_count+1))
    continue
  fi
  abs="${REPO_ROOT}/${rel}"
  if [[ ! -f "${abs}" ]]; then
    fail "repo scan: enumerated path is not a regular file: ${rel} -- enumeration must only report scannable regular files, or the scan is silently incomplete (Q&A #7)"
    continue
  fi
  scanned=$((scanned+1))
  scan_into "${abs}" "${rel}" VIOLATIONS
done

if [[ "${scanned}" -eq 0 ]]; then
  fail "repo scan found 0 files -- enumeration is broken (vacuous pass guard for the widened *.test.sh + flow/tests/*.sh scope)"
fi

# AC9 -- the carve-out excludes exactly this file: SELF_REL must actually
# be present in the enumerated set (otherwise "excludes only itself" is a
# vacuous claim about a file the enumeration never reached), and the
# self-skip must fire exactly once.
self_rel_found=0
for rel in "${FILES[@]}"; do
  [[ "${rel}" == "${SELF_REL}" ]] && { self_rel_found=1; break; }
done
[[ "${self_rel_found}" -eq 1 ]] || fail "repo scan: SELF_REL (${SELF_REL}) not found in enumerated FILES -- the self-exclusion carve-out is meaningless if the enumeration never reaches this file (AC9)"
[[ "${self_skip_count}" -eq 1 ]] || fail "repo scan: expected the self-skip carve-out to fire exactly once, fired ${self_skip_count} times (AC9)"

# =====================================================================
# contract-lib.sh coverage anchor (#791) -- proves the widened enumeration
# reaches a real sourced library in this repo, not only the synthetic
# fixture tree above. Today's enumeration only collects tracked *.test.sh
# files, so flow/tests/parity/contract-lib.sh (a sourced library, never
# itself run as a suite) is absent from FILES and this assertion fails for
# the right reason until the green phase widens the scan to flow/tests/*.sh
# libraries. If flow/tests/parity/contract-lib.sh is ever renamed or
# removed, retarget this anchor to the new sourced-library path under
# flow/tests/ (or drop the anchor if no sourced library remains) -- same
# intent as the DOC_ANCHOR guard below.
# =====================================================================
LIB_COVERAGE_ANCHOR="flow/tests/parity/contract-lib.sh"
lib_covered=0
for rel in "${FILES[@]}"; do
  [[ "${rel}" == "${LIB_COVERAGE_ANCHOR}" ]] && { lib_covered=1; break; }
done
[[ "${lib_covered}" -eq 1 ]] || fail "repo scan does not cover ${LIB_COVERAGE_ANCHOR} -- the *.test.sh-only enumeration misses sourced shell libraries under flow/tests/ whose read_*-named helpers execute in the sourcing suite's shell"

for v in "${VIOLATIONS[@]}"; do
  fail "${v}: read_*-named function calls fail() directly -- restructure into a pure <name>_raw extractor (no fail(), safe inside \$(...)) plus a require_<name> nameref wrapper that calls fail() in the parent shell, per flow/docs/shell-scripting-gotchas.md and flow/tests/skill-convention-contract.test.sh:68-96"
done

# =====================================================================
# Doc anchor guard -- the rules this test enforces must be documented, and
# the doc's pointer text must not silently drift out from under this test.
# =====================================================================
GOTCHAS_DOC="${FLOW_DIR}/docs/shell-scripting-gotchas.md"
DOC_ANCHOR="Enforced by \`flow/tests/read-helper-purity-contract.test.sh\`, which scans every tracked \`*.test.sh\` file plus every tracked \`*.sh\` shell library under \`flow/tests/\` (sourced harness libraries such as \`flow/tests/parity/contract-lib.sh\`, whose helpers run in the sourcing suite's shell) for a \`read_*\`-named function whose body calls \`fail()\`"
if [[ ! -f "${GOTCHAS_DOC}" ]]; then
  fail "doc not found: ${GOTCHAS_DOC}"
elif ! grep -qF "${DOC_ANCHOR}" "${GOTCHAS_DOC}"; then
  fail "doc missing anchor phrase: flow/docs/shell-scripting-gotchas.md must name flow/tests/read-helper-purity-contract.test.sh as the enforcing mechanism for the fail()-in-command-substitution rule"
fi

# Second doc anchor (#812): the state-machine-scanner rule (line 34) must
# also name the widened definition-form recognition and the heredoc
# carve-out, so that clause cannot silently drift out from under this
# suite's actual behavior.
DOC_ANCHOR_2="It also recognizes \`read_name() {\`, \`function read_name() {\`, and \`function read_name {\` as equivalent definition forms, and treats heredoc body lines (from an opening \`<<\`/\`<<-\` delimiter to its closing line) as opaque text that never resets scope, even when a literal column-zero \`}\` appears inside the heredoc body"
if [[ ! -f "${GOTCHAS_DOC}" ]]; then
  : # already reported above
elif ! grep -qF "${DOC_ANCHOR_2}" "${GOTCHAS_DOC}"; then
  fail "doc missing anchor phrase: flow/docs/shell-scripting-gotchas.md's state-machine-scanner rule must name the recognized read_* definition forms and the heredoc body carve-out (#812)"
fi

# =====================================================================
# EISDIR read failure (AC2, #812) -- proves scan_file's read failure is a
# checked, observable status rather than a `set -u` abort (the pre-#812
# behavior: `read` failing mid-loop on a directory input left `line`
# unset and aborted this entire suite process). Root-proof lever per
# flow/docs/shell-scripting-gotchas.md rule 15 (EISDIR needs no chmod,
# unlike the DAC-specific `unreadable` fixture above). Two assertions:
# assert_scan_error proves the exact diagnostic ("eisdir:UNREADABLE", not
# merely a non-zero status), and expect_one_failure(drive_eisdir_scan)
# proves the failure actually increments `failures` in the parent shell
# when routed through the real scan_into plumbing every repo-scan call
# site uses.
# =====================================================================
EISDIR_DIR="${FIXTURE_DIR}/eisdir"
mkdir -p "${EISDIR_DIR}" || { echo "read-helper-purity-contract.test.sh: failed to create eisdir fixture directory." >&2; exit 2; }
assert_scan_error "${EISDIR_DIR}" "eisdir" "eisdir:UNREADABLE"

drive_eisdir_scan() {
  local -a acc=()
  scan_into "${EISDIR_DIR}" "eisdir" acc
}
expect_one_failure "EISDIR read failure surfaces as a suite failure (AC2)" drive_eisdir_scan

# =====================================================================
# Result-file truncate failure guard (silent-failure review item 4) --
# proves scan_file surfaces its OWN result_file truncate failure as a
# distinct status (2), and that scan_into never misattributes SCAN_OUT's
# stale content from a PRIOR call as the CURRENT file's error when the
# truncate itself fails.
# =====================================================================

# scan_file-level, root-proof (no chmod needed): result_file is a
# directory, so `: > result_file` fails with EISDIR regardless of uid.
RESULT_FILE_AS_DIR="${FIXTURE_DIR}/result-file-as-dir"
mkdir -p "${RESULT_FILE_AS_DIR}" || { echo "read-helper-purity-contract.test.sh: failed to create result-file-as-dir fixture directory." >&2; exit 2; }
rf_rc=0
scan_file "${CLEAN}" "clean-again" "${RESULT_FILE_AS_DIR}" 2>/dev/null || rf_rc=$?
[[ "${rf_rc}" -eq 2 ]] || fail "scan_file with an unwritable result_file (a directory): expected distinct status 2 (result file truncate failed), got ${rf_rc}"

# scan_into-level, DAC-specific (root-skip, mirrors the 'unreadable'
# fixture above): make SCAN_OUT itself unwritable so scan_file's truncate
# fails inside the real scan_into plumbing, after seeding SCAN_OUT with
# stale content from an unrelated prior call, then assert scan_into's
# fail() message names the CURRENT display path (not the stale content).
if [[ "$(id -u)" -eq 0 ]]; then
  echo "SKIP: fixture 'result-file-unwritable' requires a non-root user (uid 0 can write any regular file)"
else
  printf 'stale-from-prior-call:UNREADABLE\n' > "${SCAN_OUT}"
  # root-safe: DAC-specific branch, root cannot reach it any other way
  chmod 000 "${SCAN_OUT}"
  drive_result_file_unwritable() {
    local -a acc=()
    scan_into "${CLEAN}" "clean-again" acc
  }
  rfu_stderr="$(mktemp "${FIXTURE_DIR}/rfu-stderr-XXXXXX")" || { chmod 644 "${SCAN_OUT}"; fail "fixture result-file-unwritable: failed to create stderr capture temp file"; }
  if [[ -n "${rfu_stderr:-}" ]]; then
    before_rfu="${failures}"
    drive_result_file_unwritable 2> "${rfu_stderr}"
    after_rfu="${failures}"
    chmod 644 "${SCAN_OUT}"
    : > "${SCAN_OUT}" || { echo "read-helper-purity-contract.test.sh: failed to restore SCAN_OUT after result-file-unwritable fixture." >&2; exit 2; }
    delta_rfu=$((after_rfu - before_rfu))
    failures="${before_rfu}"
    if [[ "${delta_rfu}" -ne 1 ]]; then
      fail "scan_into with an unwritable SCAN_OUT: expected exactly 1 failure, observed ${delta_rfu}"
    fi
    if ! grep -qF "clean-again:RESULT_FILE_UNWRITABLE" "${rfu_stderr}"; then
      fail "scan_into with an unwritable SCAN_OUT: expected fail() message to name clean-again:RESULT_FILE_UNWRITABLE, got: $(cat "${rfu_stderr}")"
    fi
    if grep -qF "stale-from-prior-call" "${rfu_stderr}"; then
      fail "scan_into with an unwritable SCAN_OUT: fail() message misattributed stale SCAN_OUT content from a prior call: $(cat "${rfu_stderr}")"
    fi
    rm -f "${rfu_stderr}"
  fi
fi

echo "read-helper-purity-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
