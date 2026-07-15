#!/usr/bin/env bash
# Tests for guard-main-worktree.sh. Follows the repo's shell-test precedent
# (flow/skills/babysit/scripts/tick.test.sh, sandbox/tests/*.test.sh):
# plain bash, no framework, PASS/FAIL counters, non-zero exit on failure.
# Each case runs in a fresh directory under one mktemp root and drives the
# hook script with PreToolUse-style JSON on stdin.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD_SH="${SCRIPT_DIR}/guard-main-worktree.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# run_guard <cwd> <json> — runs the hook from <cwd> with <json> on stdin.
# Sets GUARD_EXIT and GUARD_STDERR.
run_guard() {
    local cwd="$1" json="$2"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | sh "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${GUARD_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${GUARD_EXIT}, expected ${expected}"
    fi
}

echo "guard-main-worktree.test.sh"

# Not under /tmp: the guard allowlists /tmp/* paths, which would make every
# case exit 0 regardless of the config gate. /var/tmp is not allowlisted.
TEST_ROOT="$(mktemp -d /var/tmp/guard-main-worktree-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

make_git_repo() {
    local dir="$1"
    mkdir -p "${dir}"
    git -C "${dir}" init -q
}

# ── Case 1: unconfigured git repo → guard is a no-op (the bug fix) ──
echo "case: unconfigured git repo allows source writes"
REPO="${TEST_ROOT}/unconfigured"
make_git_repo "${REPO}"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "unconfigured repo" 0

# ── Case 2: non-git directory, no config → no-op ────────────────────
echo "case: non-git directory allows source writes"
DIR="${TEST_ROOT}/plain-dir"
mkdir -p "${DIR}"
run_guard "${DIR}" "{\"tool_input\":{\"file_path\":\"${DIR}/src/foo.txt\"}}"
assert_exit "non-git dir" 0

# ── Case 3: configured repo → source writes blocked ─────────────────
echo "case: configured repo blocks source writes"
REPO="${TEST_ROOT}/configured"
make_git_repo "${REPO}"
mkdir -p "${REPO}/.claude"
touch "${REPO}/.claude/config.json"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "configured repo" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "configured repo: stderr should contain BLOCKED"
fi

# ── Case 4: configured repo, .worktrees/ path → allowlisted ─────────
echo "case: configured repo allows .worktrees/ writes"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.worktrees/1-feat/src/foo.txt\"}}"
assert_exit "worktree path" 0

# ── Case 5: configured repo, cwd in a subdirectory → still blocked ──
echo "case: configured repo blocks from a subdirectory cwd"
mkdir -p "${REPO}/src/nested"
run_guard "${REPO}/src/nested" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "subdirectory cwd" 2

# ── Case 6: missing file_path → no-op (existing behavior) ───────────
echo "case: missing file_path is a no-op"
run_guard "${REPO}" "{\"tool_input\":{}}"
assert_exit "missing file_path" 0

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
