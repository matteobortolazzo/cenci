#!/usr/bin/env bash
# Repo-wide regression guard for ticket #797 (2/2 of #791): flags any
# `read_*`-named helper function in a tracked *.test.sh file whose body
# calls `fail()` directly. Calling fail() (which mutates the parent shell's
# `failures` counter) from inside a helper that callers invoke via command
# substitution ($(read_doc ...)) silently drops the failure count once bash
# forks a subshell for the substitution -- see
# flow/docs/shell-scripting-gotchas.md's "never call fail() from within
# $(...)" rule. The required shape is a pure `<name>_raw` extractor (no
# fail() side effect, safe inside $(...)) paired with a `require_<name>`
# nameref wrapper that calls fail() in the parent shell -- see
# flow/tests/skill-convention-contract.test.sh:50-78.
#
# Two deliberate carve-outs, both proven by this suite's own fixture
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
READ_FUNC_START_RE='^[[:space:]]*read_[A-Za-z0-9_]*[[:space:]]*\(\)'
COMMENT_LINE_RE='^[[:space:]]*#'

# scan_file <abs-path> <display-path> -- prints "<display>:<line>" for every
# `fail` invocation found inside a `read_*`-named function's body, or
# "<display>:UNREADABLE" (never both) if the file cannot be read -- a
# silent empty result here would let a permission-mangled tracked test file
# slip through the scan as a false all-clear. State machine: enters a
# read-function on a `read_*(...)` definition line, exits on a column-0
# `}`; while inside, a line is flagged unless its first non-blank character
# is `#` (carve-out 1 above). The definition line itself is also scanned
# (not skipped via an unconditional `continue`), so a single-line helper
# like `read_doc() { fail "..."; }` is flagged too; if that same line also
# carries a closing `}`, the state machine exits the function immediately
# rather than staying stuck in `in_read_func=1` and misattributing later
# unrelated `fail()` calls to it.
scan_file() {
  local abs="$1" display="$2"
  local -a lines=(); local line i n in_read_func=0 just_entered
  if [[ ! -r "${abs}" ]]; then
    printf '%s:UNREADABLE\n' "${display}"
    return
  fi
  while IFS= read -r line || [[ -n "${line}" ]]; do lines+=("${line}"); done < "${abs}"
  n=${#lines[@]}
  for ((i = 0; i < n; i++)); do
    line="${lines[i]}"
    just_entered=0
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
    [[ "${line}" =~ $COMMENT_LINE_RE ]] && continue
    [[ "${line}" =~ $FAIL_INVOKE_RE ]] && printf '%s:%d\n' "${display}" "$((i + 1))"
    [[ "${just_entered}" -eq 1 && "${line}" == *"}"* ]] && in_read_func=0
  done
}

# =====================================================================
# Fixture self-test -- proves the detector itself before trusting it on the
# repo scan. Temp dir outside the repo so neither the flow gate's `find`
# nor this suite's own repo scan (which also excludes this file via
# SELF_REL) sees the fixtures.
# =====================================================================
FIXTURE_DIR="$(mktemp -d)" || { echo "read-helper-purity-contract.test.sh: failed to create fixture directory." >&2; exit 2; }
trap 'rm -rf "${FIXTURE_DIR}"' EXIT

assert_count() {
  local file="$1" expected="$2" label="$3"
  local -a violations=()
  local got_count
  mapfile -t violations < <(scan_file "${file}" "${label}")
  got_count=${#violations[@]}
  [[ "${got_count}" -eq "${expected}" ]] || fail "fixture ${label}: expected ${expected} violation(s), got ${got_count} (${violations[*]:-none})"
}

# violating.test.sh -- read_doc() whose body calls fail(): exactly 1 hit.
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
assert_count "${VIOLATING}" 1 "violating"

# oneliner.test.sh -- read_doc() defined and closed entirely on a single
# line, whose body calls fail() on that same line: exactly 1 hit. Proves the
# definition line is scanned rather than unconditionally skipped, and that a
# same-line closing `}` resets in_read_func immediately instead of leaving
# the state machine stuck open past the end of the function.
ONELINER="${FIXTURE_DIR}/oneliner.test.sh"
cat > "${ONELINER}" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
read_doc() { fail "should be flagged - oneliner"; }
EOF
assert_count "${ONELINER}" 1 "oneliner"

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
assert_count "${CLEAN}" 0 "clean"

# unreadable.test.sh -- a regular file with no read permission: exactly 1
# UNREADABLE hit, proving scan_file's readability check is load-bearing
# rather than a silent "0 violations" false-clear. DAC-specific like
# flow/tests/root-safe-perms-contract.test.sh's own "unreadable" fixture:
# uid 0 bypasses read permission on any regular file, so this is a
# guard-and-skip-under-root case, not a root-proof lever.
if [[ "$(id -u)" -eq 0 ]]; then
  echo "SKIP: fixture 'unreadable' requires a non-root user (uid 0 can read any regular file)"
else
  UNREADABLE="${FIXTURE_DIR}/unreadable.test.sh"
  cat > "${UNREADABLE}" <<'EOF'
#!/usr/bin/env bash
echo "no read_* helpers in this file"
EOF
  chmod 000 "${UNREADABLE}"
  assert_count "${UNREADABLE}" 1 "unreadable"
  chmod 644 "${UNREADABLE}"
fi

# =====================================================================
# Repo-wide scan
# =====================================================================
declare -a FILES=()
if git -C "${REPO_ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  # Redirect into a real file (inside FIXTURE_DIR, already EXIT-trapped) so
  # git's own exit status is checked directly, rather than via a process
  # substitution subshell whose status is unavailable to the `if` here -- an
  # unchecked partial failure could otherwise leave FILES non-empty and
  # silently skip the find fallback below.
  GIT_LS_OUT="${FIXTURE_DIR}/git-ls-files.out"
  if git -C "${REPO_ROOT}" ls-files -z -- '*.test.sh' > "${GIT_LS_OUT}"; then
    while IFS= read -r -d '' p; do
      FILES+=("${p}")
    done < "${GIT_LS_OUT}"
  fi
fi

if [[ "${#FILES[@]}" -eq 0 ]]; then
  while IFS= read -r -d '' p; do
    FILES+=("${p#"${REPO_ROOT}/"}")
  done < <(find "${REPO_ROOT}" -type f -name '*.test.sh' \
    -not -path '*/.worktrees/*' -not -path '*/.claude/worktrees/*' -not -path '*/node_modules/*' -print0)
fi

declare -a VIOLATIONS=()
scanned=0
for rel in "${FILES[@]}"; do
  [[ "${rel}" == "${SELF_REL}" ]] && continue
  abs="${REPO_ROOT}/${rel}"
  [[ -f "${abs}" ]] || continue
  scanned=$((scanned+1))
  while IFS= read -r v; do
    [[ -n "${v}" ]] && VIOLATIONS+=("${v}")
  done < <(scan_file "${abs}" "${rel}")
done

if [[ "${scanned}" -eq 0 ]]; then
  fail "repo scan found 0 *.test.sh files -- enumeration is broken (vacuous pass guard)"
fi

for v in "${VIOLATIONS[@]}"; do
  if [[ "${v}" == *:UNREADABLE ]]; then
    fail "${v%:UNREADABLE}: file is not readable -- cannot scan for read_* helper purity violations"
  else
    fail "${v}: read_*-named function calls fail() directly -- restructure into a pure <name>_raw extractor (no fail(), safe inside \$(...)) plus a require_<name> nameref wrapper that calls fail() in the parent shell, per flow/docs/shell-scripting-gotchas.md and flow/tests/skill-convention-contract.test.sh:50-78"
  fi
done

# =====================================================================
# Doc anchor guard -- the rule this test enforces must be documented, and
# the doc's pointer text must not silently drift out from under this test.
# =====================================================================
GOTCHAS_DOC="${FLOW_DIR}/docs/shell-scripting-gotchas.md"
DOC_ANCHOR="Enforced by \`flow/tests/read-helper-purity-contract.test.sh\`, which scans every tracked \`*.test.sh\` file for a \`read_*\`-named function whose body calls \`fail()\`"
if [[ ! -f "${GOTCHAS_DOC}" ]]; then
  fail "doc not found: ${GOTCHAS_DOC}"
elif ! grep -qF "${DOC_ANCHOR}" "${GOTCHAS_DOC}"; then
  fail "doc missing anchor phrase: flow/docs/shell-scripting-gotchas.md must name flow/tests/read-helper-purity-contract.test.sh as the enforcing mechanism for the fail()-in-command-substitution rule"
fi

echo "read-helper-purity-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
