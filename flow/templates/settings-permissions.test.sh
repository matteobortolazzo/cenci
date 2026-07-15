#!/usr/bin/env bash
# Tests for the shipped settings.json permission lists. Claude Code's file
# permission checker only matches scoped-path Edit(<path>) rules, not
# scoped-path Write(<path>) ones — a scoped Write(...) entry is dead weight
# that produces a startup warning, in permissions.allow and permissions.deny
# alike. Follows the repo's shell-test precedent
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

# assert_no_scoped_write <file> <list> — fails if permissions.<list>
# (allow or deny) contains any scoped-path Write(...) entry
# (e.g. "Write(~/.ssh/**)"), since only bare "Write" is honored by
# Claude Code's file permission checker.
assert_no_scoped_write() {
    local file="$1" list="$2"
    if jq -e --arg list "${list}" \
        '[.permissions[$list][]? | select(test("^Write\\("))] | length == 0' \
        "${file}" >/dev/null 2>&1; then
        pass
    else
        local offenders
        if offenders="$(jq -r --arg list "${list}" \
            '[.permissions[$list][]? | select(test("^Write\\("))] | join(", ")' \
            "${file}" 2>&1)"; then
            fail "${file}: scoped Write(...) ${list} entries found: ${offenders}"
        else
            fail "${file}: could not evaluate permissions.${list} (jq error: ${offenders})"
        fi
    fi
}

# assert_edit_deny_present <file> <path> — fails if permissions.deny lacks
# the Edit(<path>) rule that actually blocks file writes to <path>.
assert_edit_deny_present() {
    local file="$1" path="$2"
    if jq -e --arg rule "Edit(${path})" \
        '.permissions.deny | index($rule) != null' "${file}" >/dev/null 2>&1; then
        pass
    else
        fail "${file}: missing deny rule Edit(${path})"
    fi
}

echo "settings-permissions.test.sh"

for file in "flow/templates/settings.json" ".claude/settings.json"; do
    echo "case: ${file} has no scoped Write(...) allow entries"
    assert_no_scoped_write "${REPO_ROOT}/${file}" allow

    echo "case: ${file} has no scoped Write(...) deny entries"
    assert_no_scoped_write "${REPO_ROOT}/${file}" deny

    echo "case: ${file} keeps Edit(...) deny coverage for sensitive paths"
    assert_edit_deny_present "${REPO_ROOT}/${file}" "~/.ssh/**"
    assert_edit_deny_present "${REPO_ROOT}/${file}" "~/.aws/**"
    assert_edit_deny_present "${REPO_ROOT}/${file}" ".env"
    assert_edit_deny_present "${REPO_ROOT}/${file}" ".env.*"
done

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
