#!/usr/bin/env bash
# Tests for check-version-bump-concurrency.sh (ticket #342). Follows the same
# precedent as check-workflow-permissions.test.sh: plain bash, no framework,
# PASS/FAIL counters, non-zero exit on any failure. Each case builds a fresh
# throwaway .github/workflows/ fixture tree under one mktemp root and runs the
# script with cwd set to that tree (the script reads relative
# `.github/workflows` paths, so no script interface change is needed to test
# it from a fixture directory).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check-version-bump-concurrency.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

TEST_ROOT="$(mktemp -d /var/tmp/check-version-bump-concurrency-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# write_reusable <workflows_dir> <concurrency_block> <checkout_with_block>
# <bump_env_block> [checkout_uses] — writes a minimal plugin-version-bump.yml
# stub whose shape mirrors the real reusable workflow for the parts
# check-version-bump-concurrency.sh actually inspects: a top-level
# concurrency: block, a checkout step (name: Checkout main, uses:
# actions/checkout@v7 by default) with a `with:` block, and a "Bump version"
# step with an `env:` block. Each block is inserted verbatim (pre-indented by
# the caller) so each case can vary the concurrency settings, checkout
# inputs, and bump env independently. Passing an empty string for a block
# simply omits that key entirely (used to exercise "missing" cases, e.g. no
# concurrency: block at all). The optional 5th arg overrides the checkout
# step's `uses:` pin (defaults to actions/checkout@v7) — used by the #362
# regression case to prove rule 2 selects the step by name, not by the
# pinned action version.
write_reusable() {
    local wf_dir="$1" concurrency_block="$2" checkout_with_block="$3" bump_env_block="$4"
    local checkout_uses="${5:-actions/checkout@v7}"
    local bump_run="${6:-bash .github/scripts/bump-plugin-version.sh}"
    cat > "${wf_dir}/plugin-version-bump.yml" <<EOF
name: Reusable — Plugin Version Bump

on:
  workflow_call:
    inputs:
      plugin-name:
        required: true
        type: string

${concurrency_block}
jobs:
  bump:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout main
        uses: ${checkout_uses}
        with:
${checkout_with_block}

      - name: Configure git
        run: echo noop

      - name: Bump version
        env:
${bump_env_block}
        run: ${bump_run}
EOF

    # Rule 5 also requires the delegated script to exist on disk. Create it
    # alongside the workflow so every case satisfies that half of the rule by
    # default; the "script is missing" case deletes it afterwards.
    local repo_root
    repo_root="$(cd "${wf_dir}/../.." && pwd)"
    mkdir -p "${repo_root}/.github/scripts"
    printf '#!/usr/bin/env bash\nexit 0\n' \
        > "${repo_root}/.github/scripts/bump-plugin-version.sh"
}

# write_caller <workflows_dir> <filename> <concurrency_block> — writes a
# caller fixture whose jobs.bump.uses ends in plugin-version-bump.yml.
# <concurrency_block> is inserted verbatim (pre-indented) right before
# `jobs:` so a case can exercise a caller that wrongly redeclares the
# reusable workflow's concurrency group; pass "" to omit it (the normal,
# expected shape for the three real callers).
write_caller() {
    local wf_dir="$1" filename="$2" concurrency_block="$3"
    cat > "${wf_dir}/${filename}" <<EOF
name: fixture caller

on:
  push:
    branches: [main]

permissions:
  contents: write

${concurrency_block}
jobs:
  bump:
    uses: ./.github/workflows/plugin-version-bump.yml
    with:
      plugin-name: fixture
    secrets: inherit
EOF
}

# The concurrency/checkout/env blocks that should make every rule pass.
# Individual cases override exactly one of these to force one rule to fail.
GOOD_CONCURRENCY="concurrency:
  group: version-bump-main-\${{ inputs.tag-prefix }}
  cancel-in-progress: false
"
GOOD_CHECKOUT_WITH="          fetch-depth: 0
          token: \${{ secrets.GITHUB_TOKEN }}
          ref: main"
GOOD_BUMP_ENV="          GH_TOKEN: \${{ secrets.GITHUB_TOKEN }}
          ORIGINAL_SHA: \${{ github.sha }}"

# run_check <dir> — runs check-version-bump-concurrency.sh with cwd set to
# <dir>. Sets CHECK_EXIT and CHECK_STDERR.
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

echo "check-version-bump-concurrency.test.sh"

# ── Case 1: happy path — everything correct → exit 0 ──────────────────
echo "case: happy path (correct concurrency, checkout, env, no caller redeclare) passes"
CASE1="${TEST_ROOT}/case1-happy-path"
mkdir -p "${CASE1}/.github/workflows"
write_reusable "${CASE1}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE1}/.github/workflows" "case1-caller.yml" ""
run_check "${CASE1}"
assert_exit "case1 happy path" 0

# ── Case 2: reusable workflow has no concurrency: block at all → exit 1 ──
echo "case: missing concurrency block on reusable workflow must fail"
CASE2="${TEST_ROOT}/case2-missing-concurrency"
mkdir -p "${CASE2}/.github/workflows"
write_reusable "${CASE2}/.github/workflows" "" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE2}/.github/workflows" "case2-caller.yml" ""
run_check "${CASE2}"
assert_exit "case2 missing concurrency block" 1
assert_stderr_contains "case2 failure message identifies rule 1" "rule 1"
assert_stderr_contains "case2 failure message names version-bump-main" "version-bump-main"

# ── Case 3: reusable workflow's concurrency.group is mistyped → exit 1 ──
echo "case: mistyped concurrency group must fail"
CASE3="${TEST_ROOT}/case3-mistyped-group"
mkdir -p "${CASE3}/.github/workflows"
MISTYPED_CONCURRENCY="concurrency:
  group: version-bump-mian
  cancel-in-progress: false
"
write_reusable "${CASE3}/.github/workflows" "${MISTYPED_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE3}/.github/workflows" "case3-caller.yml" ""
run_check "${CASE3}"
assert_exit "case3 mistyped concurrency group" 1
assert_stderr_contains "case3 failure message identifies rule 1" "rule 1"
assert_stderr_contains "case3 failure message names version-bump-main" "version-bump-main"

# ── Case 4: cancel-in-progress: true (must be false) → exit 1 ──────────
echo "case: cancel-in-progress true must fail"
CASE4="${TEST_ROOT}/case4-cancel-in-progress-true"
mkdir -p "${CASE4}/.github/workflows"
CANCEL_TRUE_CONCURRENCY="concurrency:
  group: version-bump-main-\${{ inputs.tag-prefix }}
  cancel-in-progress: true
"
write_reusable "${CASE4}/.github/workflows" "${CANCEL_TRUE_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE4}/.github/workflows" "case4-caller.yml" ""
run_check "${CASE4}"
assert_exit "case4 cancel-in-progress true" 1
assert_stderr_contains "case4 failure message identifies rule 1" "rule 1"
assert_stderr_contains "case4 failure message names cancel-in-progress" "cancel-in-progress"

# ── Case 5: checkout step is missing ref: main → exit 1 ────────────────
echo "case: checkout missing ref: main must fail"
CASE5="${TEST_ROOT}/case5-checkout-missing-ref"
mkdir -p "${CASE5}/.github/workflows"
NO_REF_CHECKOUT_WITH="          fetch-depth: 0
          token: \${{ secrets.GITHUB_TOKEN }}"
write_reusable "${CASE5}/.github/workflows" "${GOOD_CONCURRENCY}" "${NO_REF_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE5}/.github/workflows" "case5-caller.yml" ""
run_check "${CASE5}"
assert_exit "case5 checkout missing ref: main" 1
assert_stderr_contains "case5 failure message identifies rule 2" "rule 2"
assert_stderr_contains "case5 failure message names ref" "ref"

# ── Case 6: "Bump version" step is missing ORIGINAL_SHA env → exit 1 ───
echo "case: missing ORIGINAL_SHA env on Bump version step must fail"
CASE6="${TEST_ROOT}/case6-missing-original-sha"
mkdir -p "${CASE6}/.github/workflows"
NO_ORIGINAL_SHA_BUMP_ENV="          GH_TOKEN: \${{ secrets.GITHUB_TOKEN }}"
write_reusable "${CASE6}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${NO_ORIGINAL_SHA_BUMP_ENV}"
write_caller "${CASE6}/.github/workflows" "case6-caller.yml" ""
run_check "${CASE6}"
assert_exit "case6 missing ORIGINAL_SHA env" 1
assert_stderr_contains "case6 failure message identifies rule 3" "rule 3"
assert_stderr_contains "case6 failure message names ORIGINAL_SHA" "ORIGINAL_SHA"

# ── Case 7: a caller redeclares the reusable's concurrency group → exit 1 ──
# This is the documented caller-self-cancel gotcha: declaring the same
# concurrency group in both caller and callee cancels the caller run when
# the callee's group is entered, so no caller may declare
# concurrency.group == "version-bump-main" itself.
echo "case: caller redeclaring the same concurrency group must fail"
CASE7="${TEST_ROOT}/case7-caller-redeclares-group"
mkdir -p "${CASE7}/.github/workflows"
write_reusable "${CASE7}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
CALLER_REDECLARE_CONCURRENCY="concurrency:
  group: version-bump-main-\${{ inputs.tag-prefix }}
  cancel-in-progress: false
"
write_caller "${CASE7}/.github/workflows" "case7-caller.yml" "${CALLER_REDECLARE_CONCURRENCY}"
run_check "${CASE7}"
assert_exit "case7 caller redeclares concurrency group" 1
assert_stderr_contains "case7 failure message identifies rule 4" "rule 4"
assert_stderr_contains "case7 failure message names the offending caller" "case7-caller.yml"

# ── Case 8: non-mikefarah yq on PATH must fail the flavor guard ────────
# A fake `yq` shim is placed on PATH ahead of everything else for this one
# invocation only (via run_check_with_path, scoped to the command
# substitution subshell) so it never leaks into other cases. The shim
# answers `--version` with a bogus, non-mikefarah string; the script must
# detect the mismatch and exit 1 before any rule or file processing begins.
# This does not depend on whether the real yq is installed in this
# environment, since the shim is first on PATH regardless.
echo "case: non-mikefarah yq on PATH must fail the flavor guard"
CASE8="${TEST_ROOT}/case8-yq-flavor"
mkdir -p "${CASE8}/.github/workflows"
write_reusable "${CASE8}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE8}/.github/workflows" "case8-caller.yml" ""

SHIM_DIR="${TEST_ROOT}/case8-fake-yq-bin"
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

run_check_with_path "${CASE8}" "${SHIM_DIR}"
assert_exit "case8 non-mikefarah yq on PATH" 1
assert_stderr_contains "case8 failure message names expected flavor" "mikefarah"
assert_stderr_contains "case8 failure message names detected version string" "3.4.1"

# ── Case 9: checkout step has ref: main but fetch-depth != 0 → exit 1 ──
# Mirrors case 5 (checkout missing ref: main), but exercises the other half
# of rule 2: ref: main is present, fetch-depth is the wrong value.
echo "case: checkout fetch-depth not 0 must fail"
CASE9="${TEST_ROOT}/case9-checkout-bad-fetch-depth"
mkdir -p "${CASE9}/.github/workflows"
BAD_FETCH_DEPTH_CHECKOUT_WITH="          fetch-depth: 1
          token: \${{ secrets.GITHUB_TOKEN }}
          ref: main"
write_reusable "${CASE9}/.github/workflows" "${GOOD_CONCURRENCY}" "${BAD_FETCH_DEPTH_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE9}/.github/workflows" "case9-caller.yml" ""
run_check "${CASE9}"
assert_exit "case9 checkout bad fetch-depth" 1
assert_stderr_contains "case9 failure message identifies rule 2" "rule 2"
assert_stderr_contains "case9 failure message names fetch-depth" "fetch-depth"

# ── Case 10: no caller workflows discovered → exit 1 ───────────────────
# Exercises the "zero callers discovered" branch of rule 4 (guards against a
# yq path/query typo silently passing with zero workflows checked). The
# reusable workflow itself passes rules 1-3; the only other file present is
# an unrelated workflow whose jobs.*.uses does not end in
# plugin-version-bump.yml.
echo "case: zero caller workflows discovered must fail"
CASE10="${TEST_ROOT}/case10-no-callers"
mkdir -p "${CASE10}/.github/workflows"
write_reusable "${CASE10}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
cat > "${CASE10}/.github/workflows/unrelated.yml" <<EOF
name: unrelated workflow
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo noop
EOF
run_check "${CASE10}"
assert_exit "case10 no caller workflows discovered" 1
assert_stderr_contains "case10 failure message identifies rule 4" "rule 4"
assert_stderr_contains "case10 failure message names zero callers" "no caller workflows discovered"

# ── Case 11: checkout step's uses: version differs but name matches ────
# Regression test for #362: rule 2 must select the checkout step by
# `name: Checkout main`, not by the pinned `uses: actions/checkout@v7`
# string. A Dependabot bump to actions/checkout@v8 (or any other version)
# must not make rule 2 stop finding the step.
echo "case: checkout step's uses: version bumped but name matches must still pass"
CASE11="${TEST_ROOT}/case11-checkout-uses-version-bumped"
mkdir -p "${CASE11}/.github/workflows"
write_reusable "${CASE11}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}" "actions/checkout@v8"
write_caller "${CASE11}/.github/workflows" "case11-caller.yml" ""
run_check "${CASE11}"
assert_exit "case11 checkout uses: version bumped, name matches" 0

# ── Case 12: THE #743 REGRESSION — a constant, shared group must fail ──
# The pre-#743 shape: one `version-bump-main` queue shared by all three
# callers. GitHub keeps at most one pending run per concurrency group, so a
# third simultaneous bump was silently cancelled and its release never
# shipped. Reverting to any constant group must be rejected by rule 1.
echo "case: reverting to a constant shared concurrency group must fail"
CASE12="${TEST_ROOT}/case12-constant-shared-group"
mkdir -p "${CASE12}/.github/workflows"
CONSTANT_CONCURRENCY="concurrency:
  group: version-bump-main
  cancel-in-progress: false
"
write_reusable "${CASE12}/.github/workflows" "${CONSTANT_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE12}/.github/workflows" "case12-caller.yml" ""
run_check "${CASE12}"
assert_exit "case12 constant shared concurrency group" 1
assert_stderr_contains "case12 failure message identifies rule 1" "rule 1"
assert_stderr_contains "case12 failure message names the required per-plugin key" "inputs.tag-prefix"

# ── Case 13: rule 5 — re-inlining the bump logic must fail ────────────
# The push-retry loop is only unit-testable while it lives in the script.
# A workflow that re-inlines the bump into its own run: block drops it back
# out of coverage, which is where the original race went unnoticed.
echo "case: re-inlining the bump logic instead of delegating to the script must fail"
CASE13="${TEST_ROOT}/case13-inlined-bump"
mkdir -p "${CASE13}/.github/workflows"
write_reusable "${CASE13}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}" \
    "actions/checkout@v7" "jq '.version = \"1.0.0\"' plugin.json && git push origin HEAD:main"
write_caller "${CASE13}/.github/workflows" "case13-caller.yml" ""
run_check "${CASE13}"
assert_exit "case13 inlined bump logic" 1
assert_stderr_contains "case13 failure message identifies rule 5" "rule 5"
assert_stderr_contains "case13 failure message names the script" "bump-plugin-version.sh"

# ── Case 14: rule 5 — the delegated script must exist on disk ─────────
echo "case: a Bump version step delegating to a missing script must fail"
CASE14="${TEST_ROOT}/case14-missing-script"
mkdir -p "${CASE14}/.github/workflows"
write_reusable "${CASE14}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
write_caller "${CASE14}/.github/workflows" "case14-caller.yml" ""
rm -f "${CASE14}/.github/scripts/bump-plugin-version.sh"
run_check "${CASE14}"
assert_exit "case14 missing delegated script" 1
assert_stderr_contains "case14 failure message identifies rule 5" "rule 5"
assert_stderr_contains "case14 failure message names the missing script" "bump-plugin-version.sh"

# ── Case 15: rule 4 — a caller colliding with a per-plugin group ──────
# Rule 4 is a prefix match, not an equality test: now that the reusable
# workflow's group is templated, a caller declaring any concrete
# version-bump-main-* group can still collide and self-cancel.
echo "case: a caller declaring a per-plugin version-bump-main-* group must fail"
CASE15="${TEST_ROOT}/case15-caller-per-plugin-group"
mkdir -p "${CASE15}/.github/workflows"
write_reusable "${CASE15}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
CALLER_CONCRETE_GROUP="concurrency:
  group: version-bump-main-flow
  cancel-in-progress: false
"
write_caller "${CASE15}/.github/workflows" "case15-caller.yml" "${CALLER_CONCRETE_GROUP}"
run_check "${CASE15}"
assert_exit "case15 caller declares a concrete per-plugin group" 1
assert_stderr_contains "case15 failure message identifies rule 4" "rule 4"
assert_stderr_contains "case15 failure message names the offending caller" "case15-caller.yml"

# ── Case 16: an unrelated caller concurrency group is still allowed ────
# Rule 4 must not over-reach: a caller may serialize itself on any group
# that cannot collide with the reusable workflow's.
echo "case: a caller with an unrelated concurrency group still passes"
CASE16="${TEST_ROOT}/case16-caller-unrelated-group"
mkdir -p "${CASE16}/.github/workflows"
write_reusable "${CASE16}/.github/workflows" "${GOOD_CONCURRENCY}" "${GOOD_CHECKOUT_WITH}" "${GOOD_BUMP_ENV}"
CALLER_UNRELATED_GROUP="concurrency:
  group: docs-publish
  cancel-in-progress: false
"
write_caller "${CASE16}/.github/workflows" "case16-caller.yml" "${CALLER_UNRELATED_GROUP}"
run_check "${CASE16}"
assert_exit "case16 caller with an unrelated group" 0

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
