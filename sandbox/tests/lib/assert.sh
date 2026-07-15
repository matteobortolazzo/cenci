# shellcheck shell=bash
# Shared pass/fail bookkeeping for cenci's host-runnable bash test suites.
# Sourced by sandbox/tests/heal-plugins.test.sh, sandbox/tests/smoke.test.sh,
# and watch/tests/reap-orphans.test.sh. Each suite still owns its own
# domain-specific assert_* helpers; this only centralizes the counters, the
# fail()/pass() primitives, and the final "passed: X, failed: Y" summary.

FAILURES=0
PASSES=0

# fail <message>
fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# print_summary: prints the "passed: X, failed: Y" summary line pair used by
# every suite that sources this file. Does not exit — callers keep their own
# final `[[ "${FAILURES}" -eq 0 ]]` (or equivalent) as the last statement so
# the script's own exit status is unaffected by this refactor.
print_summary() {
    echo
    echo "passed: ${PASSES}, failed: ${FAILURES}"
}
