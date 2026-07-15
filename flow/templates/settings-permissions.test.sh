#!/usr/bin/env bash
# Tests for the shipped settings.json permission lists. Claude Code's file
# permission checker only matches scoped-path Edit(<path>) allow rules, not
# scoped-path Write(<path>) ones — a scoped Write(...) entry is dead weight
# that produces a startup warning. Follows the repo's shell-test precedent
# (flow/hooks/scripts/guard-main-worktree.test.sh): plain bash, no
# framework, PASS/FAIL counters, non-zero exit on failure.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../" && pwd)"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# assert_no_scoped_write <file> — fails if permissions.allow contains any
# scoped-path Write(...) entry (e.g. "Write(//tmp/claude*/**)"), since only
# bare "Write" is honored by Claude Code's file permission checker.
assert_no_scoped_write() {
    local file="$1"
    if jq -e '[.permissions.allow[] | select(test("^Write\\("))] | length == 0' "${file}" >/dev/null 2>&1; then
        pass
    else
        local offenders
        if offenders="$(jq -r '[.permissions.allow[] | select(test("^Write\\("))] | join(", ")' "${file}" 2>&1)"; then
            fail "${file}: scoped Write(...) allow entries found: ${offenders}"
        else
            fail "${file}: could not evaluate permissions.allow (jq error: ${offenders})"
        fi
    fi
}

echo "settings-permissions.test.sh"

echo "case: flow/templates/settings.json has no scoped Write(...) allow entries"
assert_no_scoped_write "${REPO_ROOT}/flow/templates/settings.json"

echo "case: .claude/settings.json has no scoped Write(...) allow entries"
assert_no_scoped_write "${REPO_ROOT}/.claude/settings.json"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
