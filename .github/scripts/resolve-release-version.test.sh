#!/usr/bin/env bash
# Tests for resolve-release-version.sh (ticket #626). Follows the repo's
# .github/scripts/*.test.sh precedent (check-version-bump-concurrency.test.sh):
# plain bash, no framework, PASS/FAIL counters, non-zero exit on any failure.
#
# resolve-release-version.sh's contract (see the plan's Files to Create):
#   - Reads EVENT_NAME, VERSION_INPUT, GITHUB_REF_NAME, GITHUB_OUTPUT from the
#     environment only — never `${{ }}`-interpolated into the script body, and
#     never `eval`'d or re-parsed as shell source. This is the fix for the
#     privileged workflow_dispatch version input previously being interpolated
#     directly into shell source in watch-release.yml.
#   - EVENT_NAME == "workflow_dispatch" resolves VERSION from VERSION_INPUT;
#     any other EVENT_NAME resolves VERSION from GITHUB_REF_NAME with a
#     leading "watch/v" stripped (mirrors the current inline resolution step).
#   - A single stray leading "v" is stripped from VERSION before validation
#     (tolerates a manually-typed "v1.2.3" workflow_dispatch input).
#   - The complete resulting value must whole-string match
#     ^[0-9]+\.[0-9]+\.[0-9]+$ via bash `[[ =~ ]]` (never line-oriented grep,
#     which would let a malicious multi-line value with a valid first line
#     slip through — sandbox/docs/test-harness.md bash-regex-vs-grep rule).
#   - On success: writes "version=<version>" and "tag=watch/v<version>" to
#     $GITHUB_OUTPUT and exits 0.
#   - On any non-semver result (including injection payloads that don't
#     reduce to a clean semver string): exits non-zero and writes NOTHING to
#     $GITHUB_OUTPUT — no partial output, no fallback.
#
# Malicious-input matrix (#626 AC5/AC6): a resolver bug that used `eval`,
# re-interpolation, or unquoted command substitution on the (attacker-
# controlled, manual workflow_dispatch) VERSION_INPUT value could execute
# injected shell content. Every malicious case below is proven neutralized
# behaviorally, not just by exit code:
#   - the "; rm -rf ~" case runs with HOME pointed at a disposable canary
#     directory (containing a canary file) so that even a real eval/injection
#     bug would only delete the disposable directory, never the real host
#     HOME, while the canary file surviving the run proves nothing executed;
#   - the "$(id)"/backtick cases assert the real `id` output marker "uid="
#     never appears in the script's stderr or $GITHUB_OUTPUT — proving no
#     command substitution ran;
#   - every malicious case asserts $GITHUB_OUTPUT stays completely empty
#     (no "version=" / "tag=" line at all) — proving rejection happens before
#     any output is written, never a partial/fallback write.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESOLVE_SH="${SCRIPT_DIR}/resolve-release-version.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

TEST_ROOT="$(mktemp -d /var/tmp/resolve-release-version-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# run_resolve <case_name> <event_name> <version_input> <ref_name> [extra_env]
# — invokes resolve-release-version.sh with EVENT_NAME/VERSION_INPUT/
# GITHUB_REF_NAME/GITHUB_OUTPUT set for this call only, plus any additional
# word-split NAME=value tokens in <extra_env> (e.g. "HOME=/some/canary/dir"
# for the rm -rf canary case below) — mirrors installer-clients.test.sh's
# run_piped_case env_extra convention. Sets RESOLVE_EXIT, RESOLVE_STDERR, and
# RESOLVE_OUTPUT_FILE (the path GITHUB_OUTPUT pointed at, pre-truncated to
# empty before the call so "stayed empty" assertions are unambiguous).
run_resolve() {
    local case_name="$1" event_name="$2" version_input="$3" ref_name="$4"
    local extra_env="${5:-}"
    local case_dir="${TEST_ROOT}/${case_name}"
    mkdir -p "${case_dir}"
    local output_file="${case_dir}/github-output"
    : > "${output_file}"
    set +e
    # shellcheck disable=SC2086 # extra_env is intentionally word-split into
    # zero or more separate NAME=value tokens (see doc comment above).
    RESOLVE_STDERR="$(EVENT_NAME="${event_name}" VERSION_INPUT="${version_input}" GITHUB_REF_NAME="${ref_name}" GITHUB_OUTPUT="${output_file}" ${extra_env} bash "${RESOLVE_SH}" 2>&1 >/dev/null)"
    RESOLVE_EXIT=$?
    set -e
    RESOLVE_OUTPUT_FILE="${output_file}"
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${RESOLVE_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${RESOLVE_EXIT}, expected ${expected} (stderr: ${RESOLVE_STDERR})"
    fi
}

assert_exit_nonzero() {
    local label="$1"
    if [[ "${RESOLVE_EXIT}" -ne 0 ]]; then
        pass
    else
        fail "${label}: exit 0, expected non-zero (stderr: ${RESOLVE_STDERR})"
    fi
}

assert_output_contains() {
    local label="$1" needle="$2"
    if grep -Fq -- "${needle}" "${RESOLVE_OUTPUT_FILE}" 2>/dev/null; then
        pass
    else
        fail "${label}: expected GITHUB_OUTPUT to contain '${needle}' (got: $(cat "${RESOLVE_OUTPUT_FILE}" 2>/dev/null))"
    fi
}

assert_output_empty() {
    local label="$1"
    if [[ ! -s "${RESOLVE_OUTPUT_FILE}" ]]; then
        pass
    else
        fail "${label}: expected GITHUB_OUTPUT to stay empty on rejection (got: $(cat "${RESOLVE_OUTPUT_FILE}"))"
    fi
}

assert_stderr_not_contains() {
    local label="$1" needle="$2"
    if [[ "${RESOLVE_STDERR}" != *"${needle}"* ]]; then
        pass
    else
        fail "${label}: stderr unexpectedly contained '${needle}' (evidence of command execution) — got: ${RESOLVE_STDERR}"
    fi
}

assert_output_not_contains() {
    local label="$1" needle="$2"
    if ! grep -Fq -- "${needle}" "${RESOLVE_OUTPUT_FILE}" 2>/dev/null; then
        pass
    else
        fail "${label}: GITHUB_OUTPUT unexpectedly contained '${needle}' (evidence of command execution) — got: $(cat "${RESOLVE_OUTPUT_FILE}")"
    fi
}

echo "resolve-release-version.test.sh"

# ── Case: valid workflow_dispatch input ─────────────────────────────────
echo "case: valid workflow_dispatch version input resolves version + tag and exits 0"
run_resolve valid-dispatch workflow_dispatch "1.2.3" ""
assert_exit "valid-dispatch exit 0" 0
assert_output_contains "valid-dispatch version output" "version=1.2.3"
assert_output_contains "valid-dispatch tag output" "tag=watch/v1.2.3"

# ── Case: valid workflow_dispatch input with a stray leading "v" ───────
echo "case: workflow_dispatch input with a stray leading 'v' is stripped before validation"
run_resolve valid-dispatch-stray-v workflow_dispatch "v1.2.3" ""
assert_exit "valid-dispatch-stray-v exit 0" 0
assert_output_contains "valid-dispatch-stray-v version output" "version=1.2.3"
assert_output_contains "valid-dispatch-stray-v tag output" "tag=watch/v1.2.3"
assert_output_not_contains "valid-dispatch-stray-v no double-v tag" "tag=watch/vv1.2.3"

# ── Case: valid tag-push ref ────────────────────────────────────────────
echo "case: valid watch/v* tag ref (non-workflow_dispatch event) resolves version + tag and exits 0"
run_resolve valid-tag-ref push "" "watch/v2.3.4"
assert_exit "valid-tag-ref exit 0" 0
assert_output_contains "valid-tag-ref version output" "version=2.3.4"
assert_output_contains "valid-tag-ref tag output" "tag=watch/v2.3.4"

# ── Malicious matrix: workflow_dispatch is the untrusted, manually-typed
# input surface (the reusable-workflow-dispatched path passes an internally
# computed, already-trusted semver — see the plan's Integration points).

echo "case: '1.0.0; rm -rf ~' is rejected, never executed (canary HOME survives), and writes nothing to GITHUB_OUTPUT"
CANARY_HOME="${TEST_ROOT}/rm-rf-canary-home"
mkdir -p "${CANARY_HOME}"
printf 'do not delete me\n' > "${CANARY_HOME}/canary-file"
run_resolve malicious-rm-rf workflow_dispatch "1.0.0; rm -rf ~" "" "HOME=${CANARY_HOME}"
assert_exit_nonzero "malicious-rm-rf rejected"
assert_output_empty "malicious-rm-rf output stays empty"
if [[ -d "${CANARY_HOME}" && -f "${CANARY_HOME}/canary-file" ]]; then
    pass
else
    fail "malicious-rm-rf: canary HOME or canary file was deleted — injected 'rm -rf ~' executed"
fi

echo "case: '\$(id)' command substitution is neutralized — never executed, rejected, no output written"
run_resolve malicious-dollar-paren-id workflow_dispatch '$(id)' ""
assert_exit_nonzero "malicious-dollar-paren-id rejected"
assert_output_empty "malicious-dollar-paren-id output stays empty"
assert_stderr_not_contains "malicious-dollar-paren-id no uid= in stderr" "uid="
assert_output_not_contains "malicious-dollar-paren-id no uid= in output" "uid="

echo "case: backtick command substitution is neutralized — never executed, rejected, no output written"
run_resolve malicious-backticks workflow_dispatch '`id`' ""
assert_exit_nonzero "malicious-backticks rejected"
assert_output_empty "malicious-backticks output stays empty"
assert_stderr_not_contains "malicious-backticks no uid= in stderr" "uid="
assert_output_not_contains "malicious-backticks no uid= in output" "uid="

echo "case: path-traversal-shaped value '../evil' is rejected as non-semver, no output written"
run_resolve malicious-path-traversal workflow_dispatch "../evil" ""
assert_exit_nonzero "malicious-path-traversal rejected"
assert_output_empty "malicious-path-traversal output stays empty"

echo "case: multi-line value with a valid-looking first line is rejected by whole-string regex, not line-oriented matching"
run_resolve malicious-multiline workflow_dispatch "$(printf '1.0.0\nrm -rf /')" ""
assert_exit_nonzero "malicious-multiline rejected"
assert_output_empty "malicious-multiline output stays empty"

echo "case: empty version input is rejected, no output written"
run_resolve malicious-empty workflow_dispatch "" ""
assert_exit_nonzero "malicious-empty rejected"
assert_output_empty "malicious-empty output stays empty"

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
