#!/usr/bin/env bash
# Tests for check-codeowners.sh (ticket #256). Follows the repo's shell-test
# precedent (check-workflow-permissions.test.sh /
# agentflow/hooks/scripts/guard-main-worktree.test.sh): plain bash, no
# framework, PASS/FAIL counters, non-zero exit on any failure. Each case
# builds a fresh throwaway .github/CODEOWNERS + referenced-file fixture tree
# under one mktemp root and runs the script with cwd set to that tree (the
# script reads a relative `.github/CODEOWNERS` path, so no script interface
# change is needed to test it from a fixture directory).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check-codeowners.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

TEST_ROOT="$(mktemp -d /var/tmp/check-codeowners-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# write_codeowners <dir> <content> — writes <dir>/.github/CODEOWNERS with the
# given content verbatim.
write_codeowners() {
    local dir="$1" content="$2"
    mkdir -p "${dir}/.github"
    printf '%s\n' "${content}" > "${dir}/.github/CODEOWNERS"
}

# touch_path <dir> <relative_path> — creates a fixture file at
# <dir>/<relative_path>, creating parent directories as needed.
touch_path() {
    local dir="$1" rel="$2"
    mkdir -p "$(dirname "${dir}/${rel}")"
    : > "${dir}/${rel}"
}

# run_check <dir> — runs check-codeowners.sh with cwd set to <dir>. Sets
# CHECK_EXIT and CHECK_STDERR.
run_check() {
    local dir="$1"
    CHECK_STDERR="$(cd "${dir}" && bash "${CHECK_SH}" 2>&1 >/dev/null)"
    CHECK_EXIT=$?
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${CHECK_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${CHECK_EXIT}, expected ${expected} (stderr: ${CHECK_STDERR})"
    fi
}

assert_stderr_contains() {
    local label="$1" needle="$2"
    if [[ "${CHECK_STDERR}" == *"${needle}"* ]]; then
        pass
    else
        fail "${label}: stderr did not contain '${needle}' (got: ${CHECK_STDERR})"
    fi
}

assert_stderr_not_contains() {
    local label="$1" needle="$2"
    if [[ "${CHECK_STDERR}" != *"${needle}"* ]]; then
        pass
    else
        fail "${label}: stderr unexpectedly contained '${needle}' (got: ${CHECK_STDERR})"
    fi
}

echo "check-codeowners.test.sh"

# ── Case 1: all listed paths exist → exit 0 ──────────────────────────────
echo "case: all listed paths exist passes"
CASE1="${TEST_ROOT}/case1-all-exist"
touch_path "${CASE1}" ".github/scripts/check-workflow-permissions.sh"
touch_path "${CASE1}" ".github/scripts/check-workflow-permissions.test.sh"
write_codeowners "${CASE1}" \
".github/scripts/check-workflow-permissions.sh       @matteobortolazzo
.github/scripts/check-workflow-permissions.test.sh  @matteobortolazzo"
run_check "${CASE1}"
assert_exit "case1 all paths exist" 0

# ── Case 2: one missing literal path → exit 1, message names it ─────────
echo "case: one missing literal path fails and names the path"
CASE2="${TEST_ROOT}/case2-one-missing"
touch_path "${CASE2}" ".github/scripts/check-workflow-permissions.sh"
write_codeowners "${CASE2}" \
".github/scripts/check-workflow-permissions.sh       @matteobortolazzo
.github/scripts/does-not-exist.sh                    @matteobortolazzo"
run_check "${CASE2}"
assert_exit "case2 one missing path" 1
assert_stderr_contains "case2 message names missing path" ".github/scripts/does-not-exist.sh"

# ── Case 3: multiple missing paths → all reported in a single run ───────
echo "case: multiple missing paths are all reported in one run"
CASE3="${TEST_ROOT}/case3-multi-missing"
write_codeowners "${CASE3}" \
".github/scripts/missing-one.sh  @matteobortolazzo
.github/scripts/missing-two.sh  @matteobortolazzo"
run_check "${CASE3}"
assert_exit "case3 multiple missing paths" 1
assert_stderr_contains "case3 names first missing path" ".github/scripts/missing-one.sh"
assert_stderr_contains "case3 names second missing path" ".github/scripts/missing-two.sh"

# ── Case 4: comment and blank lines are skipped without affecting result ─
echo "case: comment and blank lines are skipped without affecting result"
CASE4="${TEST_ROOT}/case4-comments-blanks"
touch_path "${CASE4}" ".github/scripts/check-workflow-permissions.sh"
write_codeowners "${CASE4}" \
"# This is a comment line, not a path

.github/scripts/check-workflow-permissions.sh  @matteobortolazzo

# Another comment"
run_check "${CASE4}"
assert_exit "case4 comments and blanks skipped" 0

# ── Case 5: a glob-pattern line is skipped without causing a failure ────
echo "case: glob-pattern line is skipped without causing a failure"
CASE5="${TEST_ROOT}/case5-glob"
touch_path "${CASE5}" ".github/scripts/check-workflow-permissions.sh"
write_codeowners "${CASE5}" \
".github/scripts/check-workflow-permissions.sh  @matteobortolazzo
*.md                                            @matteobortolazzo
docs/*.md                                       @matteobortolazzo"
run_check "${CASE5}"
assert_exit "case5 glob pattern skipped" 0
assert_stderr_not_contains "case5 does not fail on glob path" "*.md"

# ── Case 6: trailing-slash directory pattern (docs/) is skipped ─────────
echo "case: trailing-slash directory pattern is skipped without causing a failure"
CASE6="${TEST_ROOT}/case6-trailing-slash"
touch_path "${CASE6}" ".github/scripts/check-workflow-permissions.sh"
write_codeowners "${CASE6}" \
".github/scripts/check-workflow-permissions.sh  @matteobortolazzo
docs/                                           @matteobortolazzo"
run_check "${CASE6}"
assert_exit "case6 trailing-slash pattern skipped" 0
assert_stderr_not_contains "case6 does not fail on trailing-slash path" "docs/ (from"

# ── Case 7: leading-slash anchored path resolves against repo root ──────
echo "case: leading-slash anchored path resolves against repo root, not filesystem root"
CASE7="${TEST_ROOT}/case7-leading-slash"
touch_path "${CASE7}" ".github/scripts/check-workflow-permissions.sh"
write_codeowners "${CASE7}" \
"/.github/scripts/check-workflow-permissions.sh  @matteobortolazzo"
run_check "${CASE7}"
assert_exit "case7 leading-slash anchored path resolves against repo root" 0

# ── Case 8: unreadable CODEOWNERS file fails instead of silently passing ─
echo "case: unreadable CODEOWNERS file fails instead of silently passing"
CASE8="${TEST_ROOT}/case8-unreadable"
write_codeowners "${CASE8}" \
".github/scripts/check-workflow-permissions.sh  @matteobortolazzo"
chmod 000 "${CASE8}/.github/CODEOWNERS"
run_check "${CASE8}"
assert_exit "case8 unreadable CODEOWNERS file fails" 1
assert_stderr_contains "case8 message names the unreadable file" "is not readable"
chmod 644 "${CASE8}/.github/CODEOWNERS"

# ── Case 9: glob-skip line does not mask a genuinely missing literal path ─
echo "case: glob-skip line does not mask a genuinely missing literal path"
CASE9="${TEST_ROOT}/case9-glob-and-missing"
write_codeowners "${CASE9}" \
"*.md                                     @matteobortolazzo
.github/scripts/does-not-exist.sh        @matteobortolazzo"
run_check "${CASE9}"
assert_exit "case9 glob plus missing path fails" 1
assert_stderr_contains "case9 names missing literal path" ".github/scripts/does-not-exist.sh"
assert_stderr_not_contains "case9 does not name skipped glob path" "*.md"

# ── Case 10: all three documented glob characters (*, ?, [) are skipped ──
echo "case: all three documented glob characters are skipped"
CASE10="${TEST_ROOT}/case10-glob-chars"
touch_path "${CASE10}" ".github/scripts/check-workflow-permissions.sh"
write_codeowners "${CASE10}" \
".github/scripts/check-workflow-permissions.sh  @matteobortolazzo
*.md                                            @matteobortolazzo
file?.sh                                        @matteobortolazzo
src/[abc]                                       @matteobortolazzo"
run_check "${CASE10}"
assert_exit "case10 all glob characters skipped" 0
assert_stderr_not_contains "case10 does not fail on * path" "*.md"
assert_stderr_not_contains "case10 does not fail on ? path" "file?.sh"
assert_stderr_not_contains "case10 does not fail on [ path" "src/[abc]"

# ── Case 11: failure message matches the exact AC-specified format ──────
echo "case: failure message matches the exact AC-specified format"
CASE11="${TEST_ROOT}/case11-message-format"
write_codeowners "${CASE11}" \
".github/scripts/does-not-exist.sh  @matteobortolazzo"
run_check "${CASE11}"
assert_exit "case11 missing path fails" 1
assert_stderr_contains "case11 message matches exact format" \
    ".github/scripts/does-not-exist.sh (from .github/CODEOWNERS) does not exist"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
