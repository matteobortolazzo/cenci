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
# flow/tests/skill-convention-contract.test.sh:50-78.
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

# Scan scope: every tracked *.test.sh repo-wide (unchanged) plus every
# tracked *.sh shell library under flow/tests/ (widened by #791). Git
# pathspec '*' matches '/', so 'flow/tests/*.sh' also covers
# 'flow/tests/parity/contract-lib.sh'; the find fallback's equivalent
# predicate is `-path "${root}/flow/tests/*" -a -name '*.sh'`.
TEST_PATHSPEC='*.test.sh'
LIB_PATHSPEC='flow/tests/*.sh'

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
  # root-safe: DAC-specific branch, root cannot reach it any other way
  chmod 000 "${UNREADABLE}"
  assert_count "${UNREADABLE}" 1 "unreadable"
  chmod 644 "${UNREADABLE}"
fi

# =====================================================================
# enumerate_files fixture tree (#791) -- proves the enumeration, once
# widened by this ticket's green phase, reaches sourced *.sh libraries
# under flow/tests/ (e.g. flow/tests/parity/contract-lib.sh's read_doc_raw),
# not only tracked *.test.sh files, in both the git ls-files and find
# enumeration paths. `enumerate_files <root> <git|find>` does not exist
# yet -- this is a deliberate TDD red: the extraction and LIB_PATHSPEC
# widening land in the green phase. Until then, invoking the undefined
# function inside the `< <(...)` process substitution below fails quietly
# (bash prints "command not found" to the redirected stderr and the
# substitution yields no stdout) rather than aborting the suite, since
# `set -uo pipefail` at the top of this file does not include `-e`; the
# assertions then compare that empty result against the expected in-scope
# set and report the specific missing library paths via fail(), giving a
# real, informative red rather than a parse error.
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

# out of scope: non-test *.sh outside flow/tests/ -- this is the green
# phase's third documented carve-out. Excluded by LIB_PATHSPEC (a shell
# library must live under flow/tests/) and by TEST_PATHSPEC (its filename
# does not end in .test.sh).
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

# End-to-end: run scan_file over the set enumerate_files (find mode) just
# proved is in scope, asserting exactly one violation and that it lands in
# the positive fixture, flow/tests/parity/lib-bad.sh -- proving the widened
# enumeration doesn't just return the right paths but that scan_file, fed
# those paths, actually catches the defective library (lib-bad.sh's
# fail()-calling read_doc) and stays silent on the compliant one
# (lib-clean.sh's read_doc_raw).
declare -a TREE_VIOLATIONS=()
TREE_ENUM_OUT="$(mktemp "${FIXTURE_DIR}/tree-enum-XXXXXX")" || { echo "read-helper-purity-contract.test.sh: failed to create tree enumeration temp file." >&2; exit 2; }
if enumerate_files "${TREE_DIR}" find > "${TREE_ENUM_OUT}" 2>/dev/null; then
  while IFS= read -r -d '' rel; do
    while IFS= read -r v; do
      [[ -n "${v}" ]] && TREE_VIOLATIONS+=("${v}")
    done < <(scan_file "${TREE_DIR}/${rel}" "${rel}")
  done < "${TREE_ENUM_OUT}"
else
  fail "enumerate_files ${TREE_DIR} find: enumeration failed (non-zero exit)"
fi
rm -f "${TREE_ENUM_OUT}"
if [[ "${#TREE_VIOLATIONS[@]}" -ne 1 ]]; then
  fail "scan_file over enumerate_files(${TREE_DIR}, find): expected exactly 1 violation, got ${#TREE_VIOLATIONS[@]} (${TREE_VIOLATIONS[*]:-none})"
elif [[ "${TREE_VIOLATIONS[0]}" != "flow/tests/parity/lib-bad.sh:"* ]]; then
  fail "scan_file over enumerate_files(${TREE_DIR}, find): expected the single violation in flow/tests/parity/lib-bad.sh, got ${TREE_VIOLATIONS[0]}"
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
  fail "repo scan found 0 files -- enumeration is broken (vacuous pass guard for the widened *.test.sh + flow/tests/*.sh scope)"
fi

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
DOC_ANCHOR="Enforced by \`flow/tests/read-helper-purity-contract.test.sh\`, which scans every tracked \`*.test.sh\` file plus every tracked \`*.sh\` shell library under \`flow/tests/\` (sourced harness libraries such as \`flow/tests/parity/contract-lib.sh\`, whose helpers run in the sourcing suite's shell) for a \`read_*\`-named function whose body calls \`fail()\`"
if [[ ! -f "${GOTCHAS_DOC}" ]]; then
  fail "doc not found: ${GOTCHAS_DOC}"
elif ! grep -qF "${DOC_ANCHOR}" "${GOTCHAS_DOC}"; then
  fail "doc missing anchor phrase: flow/docs/shell-scripting-gotchas.md must name flow/tests/read-helper-purity-contract.test.sh as the enforcing mechanism for the fail()-in-command-substitution rule"
fi

echo "read-helper-purity-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
