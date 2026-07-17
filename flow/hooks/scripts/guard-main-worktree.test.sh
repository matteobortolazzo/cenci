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
mkdir -p "${REPO}/.cenci"
touch "${REPO}/.cenci/config.json"
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

# ── Case 7: configured repo, .cenci/ path → blocked (configure now ships
# its own writes through a feature worktree + PR, so no main-worktree
# carve-out is needed for it) ────────────────────────────────────────
echo "case: configured repo blocks .cenci/ writes outside a worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.cenci/Dockerfile\"}}"
assert_exit ".cenci path" 2

echo "case: configured repo blocks AGENTS.md writes outside a worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/AGENTS.md\"}}"
assert_exit "AGENTS.md path" 2

echo "case: configured repo allows .cenci/ writes inside a feature worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.worktrees/configure-init/.cenci/Dockerfile\"}}"
assert_exit ".cenci path inside worktree" 0

echo "case: legacy-only configured repo remains protected"
LEGACY_REPO="${TEST_ROOT}/legacy-configured"
make_git_repo "${LEGACY_REPO}"
mkdir -p "${LEGACY_REPO}/.claude"
touch "${LEGACY_REPO}/.claude/config.json"
run_guard "${LEGACY_REPO}" "{\"tool_input\":{\"file_path\":\"${LEGACY_REPO}/src/foo.txt\"}}"
assert_exit "legacy configured repo" 2

# ── Case 8: non-git dir with its own .claude/config.json → no-op ────
# Pre-fix, ROOT=$(pwd) fallback would let this directory's own config.json
# satisfy the gate and incorrectly enforce (#371).
echo "case: non-git directory with .claude/config.json is still a no-op"
DIR="${TEST_ROOT}/non-git-with-config"
mkdir -p "${DIR}/.claude"
touch "${DIR}/.claude/config.json"
run_guard "${DIR}" "{\"tool_input\":{\"file_path\":\"${DIR}/src/foo.txt\"}}"
assert_exit "non-git dir with config.json" 0

# ── Case 9: configured repo, native Plan Mode storage under the sandbox
# container's home (/home/dev/.claude/plans/) → allowlisted (#431). The
# hook only ever sees an absolute file_path, never a real $HOME, so this
# exercises the container-home shape of that path.
echo "case: configured repo allows .claude/plans/ writes under container home"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"/home/dev/.claude/plans/plan.md\"}}"
assert_exit "container home .claude/plans path" 0

# ── Case 10: configured repo, native Plan Mode storage under an arbitrary
# host $HOME (not /home/dev) → allowlisted (#431). Proves the match is
# portable across $HOME values, not hardcoded to the container's home.
echo "case: configured repo allows .claude/plans/ writes under an arbitrary host home"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/home-host/.claude/plans/plan.md\"}}"
assert_exit "arbitrary host home .claude/plans path" 0

# ── Case 11: configured repo, source path adjacent to .claude but not under
# plans/ → still blocked (#431). Guards against an over-broad glob that
# would allow any .claude*-prefixed path.
echo "case: configured repo blocks source paths that merely resemble .claude/plans/"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/.claude-notes.md\"}}"
assert_exit "adjacent-to-.claude source path" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "adjacent-to-.claude source path: stderr should contain BLOCKED"
fi

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
