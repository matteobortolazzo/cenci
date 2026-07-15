#!/usr/bin/env bash
# Tests for check-workflow-permissions.sh (ticket #226). Follows the repo's
# shell-test precedent (flow/hooks/scripts/guard-main-worktree.test.sh):
# plain bash, no framework, PASS/FAIL counters, non-zero exit on any failure.
# Each case builds a fresh throwaway .github/workflows/ fixture tree under
# one mktemp root and runs the script with cwd set to that tree (the script
# reads relative `.github/workflows` paths, so no script interface change is
# needed to test it from a fixture directory).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check-workflow-permissions.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

TEST_ROOT="$(mktemp -d /var/tmp/check-workflow-permissions-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# write_reusable <workflows_dir> — writes a minimal plugin-version-bump.yml
# stub: a workflow_call trigger and no top-level permissions: block (mirrors
# the real reusable workflow's shape for the parts check-workflow-permissions
# .sh actually inspects).
write_reusable() {
    local wf_dir="$1"
    cat > "${wf_dir}/plugin-version-bump.yml" <<'EOF'
name: Reusable — Plugin Version Bump

on:
  workflow_call:
    inputs:
      dispatch-workflow:
        required: false
        type: string
        default: ''

jobs:
  bump:
    runs-on: ubuntu-latest
    steps:
      - run: echo noop
EOF
}

# write_caller <workflows_dir> <filename> <permissions_block> <with_block>
# — writes a caller fixture whose jobs.bump.uses ends in
# plugin-version-bump.yml. <permissions_block> and <with_block> are inserted
# verbatim (pre-indented by the caller) so each case can vary the
# permissions: keys and the `with:` inputs independently.
write_caller() {
    local wf_dir="$1" filename="$2" permissions_block="$3" with_block="$4"
    cat > "${wf_dir}/${filename}" <<EOF
name: fixture caller

on:
  push:
    branches: [main]

permissions:
${permissions_block}

jobs:
  bump:
    uses: ./.github/workflows/plugin-version-bump.yml
    with:
${with_block}
    secrets: inherit
EOF
}

# write_caller_scalar_permissions <workflows_dir> <filename> <permissions_value>
# <with_block> — like write_caller, but for fixtures that need a scalar
# permissions: value (e.g. `write-all`) rather than a nested mapping.
# write_caller always emits `permissions:\n  <block>`, which can only ever
# produce a mapping — this variant is needed to cover the scalar-shorthand
# case that rule 2 (mapping-type assertion) must reject.
write_caller_scalar_permissions() {
    local wf_dir="$1" filename="$2" permissions_value="$3" with_block="$4"
    cat > "${wf_dir}/${filename}" <<EOF
name: fixture caller

on:
  push:
    branches: [main]

permissions: ${permissions_value}

jobs:
  bump:
    uses: ./.github/workflows/plugin-version-bump.yml
    with:
${with_block}
    secrets: inherit
EOF
}

# run_check <dir> — runs check-workflow-permissions.sh with cwd set to <dir>.
# Sets CHECK_EXIT and CHECK_STDERR.
run_check() {
    local dir="$1"
    CHECK_STDERR="$(cd "${dir}" && bash "${CHECK_SH}" 2>&1 >/dev/null)"
    CHECK_EXIT=$?
}

# run_check_with_path <dir> <extra_path_dir> — like run_check, but prepends
# <extra_path_dir> to PATH for this single invocation only. The PATH
# assignment lives inside the `$(...)` command substitution subshell, so it
# never leaks into the rest of this test script or later cases.
run_check_with_path() {
    local dir="$1" extra_path_dir="$2"
    CHECK_STDERR="$(cd "${dir}" && PATH="${extra_path_dir}:${PATH}" bash "${CHECK_SH}" 2>&1 >/dev/null)"
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

echo "check-workflow-permissions.test.sh"

# ── Case 1: extra permission key (packages) beyond {contents, actions} ──
# Dispatching caller: contents=write + actions=write (satisfies rule 2 and
# the actions/dispatch-workflow pairing rule 3 requires), plus an
# undeclared extra key `packages: write`. Rule 3's allowlist rejects any
# .permissions key outside {contents, actions} regardless of its value, so
# this must fail and name `packages`.
echo "case: extra permission key (packages) must fail and name the key"
CASE1="${TEST_ROOT}/case1-extra-packages"
mkdir -p "${CASE1}/.github/workflows"
write_reusable "${CASE1}/.github/workflows"
write_caller "${CASE1}/.github/workflows" "case1-caller.yml" \
"  contents: write
  actions: write
  packages: write" \
"      dispatch-workflow: some-downstream.yml"
run_check "${CASE1}"
assert_exit "case1 extra permission key (packages)" 1
assert_stderr_contains "case1 failure message names packages" "packages"

# ── Case 2: dispatching caller, no extra keys → exit 0 (already green) ──
echo "case: dispatching caller with contents+actions write and no extra keys passes"
CASE2="${TEST_ROOT}/case2-dispatching-clean"
mkdir -p "${CASE2}/.github/workflows"
write_reusable "${CASE2}/.github/workflows"
write_caller "${CASE2}/.github/workflows" "case2-caller.yml" \
"  contents: write
  actions: write" \
"      dispatch-workflow: some-downstream.yml"
run_check "${CASE2}"
assert_exit "case2 dispatching caller clean" 0

# ── Case 3: non-dispatching caller, contents only → exit 0 (already green) ──
echo "case: non-dispatching caller with contents write only passes"
CASE3="${TEST_ROOT}/case3-non-dispatching-clean"
mkdir -p "${CASE3}/.github/workflows"
write_reusable "${CASE3}/.github/workflows"
write_caller "${CASE3}/.github/workflows" "case3-caller.yml" \
"  contents: write" \
"      plugin-name: fixture"
run_check "${CASE3}"
assert_exit "case3 non-dispatching caller clean" 0

# ── Case 4: non-dispatching caller with actions: write → exit 1 ──
# Rule 3 requires permissions.actions to only be present when a
# dispatch-workflow input is passed — this caller declares actions: write
# without one, so it must fail.
echo "case: non-dispatching caller with actions: write fails"
CASE4="${TEST_ROOT}/case4-non-dispatching-actions"
mkdir -p "${CASE4}/.github/workflows"
write_reusable "${CASE4}/.github/workflows"
write_caller "${CASE4}/.github/workflows" "case4-caller.yml" \
"  contents: write
  actions: write" \
"      plugin-name: fixture"
run_check "${CASE4}"
assert_exit "case4 non-dispatching caller with actions: write" 1

# ── Case 5: extra key (id-token) beyond {contents, actions} ──
# Non-dispatching caller: contents=write only (satisfies rules 2/3), plus
# an undeclared extra key `id-token: write`. Same allowlist violation as
# case 1, generalized to a different key.
echo "case: extra permission key (id-token) must fail"
CASE5="${TEST_ROOT}/case5-extra-id-token"
mkdir -p "${CASE5}/.github/workflows"
write_reusable "${CASE5}/.github/workflows"
write_caller "${CASE5}/.github/workflows" "case5-caller.yml" \
"  contents: write
  id-token: write" \
"      plugin-name: fixture"
run_check "${CASE5}"
assert_exit "case5 extra permission key (id-token)" 1

# ── Case 6: scalar `permissions: write-all` (not a mapping) must fail ──
# Rule 2 (new) asserts `.permissions | tag` is `!!map` (or `!!null` when
# absent). A scalar shorthand like `write-all` has tag `!!str`, so this must
# fail with a message naming the file and the offending tag/value, and must
# not also emit rule 3/4-style per-key messages for the same file (rule 2
# `continue`s past the rest of the per-caller checks once it fails).
echo "case: scalar permissions (write-all) must fail as a mapping violation"
CASE6="${TEST_ROOT}/case6-scalar-permissions"
mkdir -p "${CASE6}/.github/workflows"
write_reusable "${CASE6}/.github/workflows"
write_caller_scalar_permissions "${CASE6}/.github/workflows" "case6-caller.yml" "write-all" \
"      plugin-name: fixture"
run_check "${CASE6}"
assert_exit "case6 scalar permissions (write-all)" 1
assert_stderr_contains "case6 failure message names the file" "case6-caller.yml"
assert_stderr_contains "case6 failure message identifies rule 2" "rule 2 violated"
assert_stderr_contains "case6 failure message says must be a mapping" "must be a mapping"
assert_stderr_contains "case6 failure message names the offending tag" "!!str"
assert_stderr_contains "case6 failure message names the offending value" "write-all"
case6_fail_lines="$(grep -c "case6-caller.yml" <<< "${CHECK_STDERR}")"
if [[ "${case6_fail_lines}" -eq 1 ]]; then
    pass
else
    fail "case6 rule 2 must 'continue' past further per-file checks (expected 1 FAIL line naming case6-caller.yml, got ${case6_fail_lines}; stderr: ${CHECK_STDERR})"
fi

# ── Case 7: non-mikefarah yq on PATH must fail the flavor guard ──
# A fake `yq` shim is placed on PATH ahead of everything else for this one
# invocation only (via run_check_with_path, scoped to the command
# substitution subshell) so it never leaks into other cases. The shim
# answers `--version` with a bogus, non-mikefarah string; the script must
# detect the mismatch and exit 1 before any rule or file processing begins.
# This does not depend on whether the real yq is installed in this
# environment, since the shim is first on PATH regardless.
echo "case: non-mikefarah yq on PATH must fail the flavor guard"
CASE7="${TEST_ROOT}/case7-yq-flavor"
mkdir -p "${CASE7}/.github/workflows"
write_reusable "${CASE7}/.github/workflows"
write_caller "${CASE7}/.github/workflows" "case7-caller.yml" \
"  contents: write" \
"      plugin-name: fixture"

SHIM_DIR="${TEST_ROOT}/case7-fake-yq-bin"
mkdir -p "${SHIM_DIR}"
cat > "${SHIM_DIR}/yq" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "--version" ]]; then
    echo "yq version 3.4.1 (https://pypi.org/project/yq/)"
    exit 0
fi
echo "fake yq: unsupported invocation in test shim" >&2
exit 1
EOF
chmod +x "${SHIM_DIR}/yq"

run_check_with_path "${CASE7}" "${SHIM_DIR}"
assert_exit "case7 non-mikefarah yq on PATH" 1
assert_stderr_contains "case7 failure message names expected flavor" "mikefarah"
assert_stderr_contains "case7 failure message names detected version string" "3.4.1"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
