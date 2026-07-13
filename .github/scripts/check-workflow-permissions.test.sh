#!/usr/bin/env bash
# Tests for check-workflow-permissions.sh (ticket #226). Follows the repo's
# shell-test precedent (agentflow/hooks/scripts/guard-main-worktree.test.sh):
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

# run_check <dir> — runs check-workflow-permissions.sh with cwd set to <dir>.
# Sets CHECK_EXIT and CHECK_STDERR.
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

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
