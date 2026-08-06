#!/usr/bin/env bash
# Test suite for run-checks.sh (ticket #720).
#
# RECURSION WARNING: run-checks.sh discovers and executes every *.test.sh
# file found under its resolved flow root. This test file lives under the
# real flow root (flow/scripts/run-checks.test.sh) and would therefore be
# discovered by a bare, argument-less invocation of run-checks.sh — which
# would run this very suite, which itself invokes run-checks.sh again, and
# so on. To avoid fork-bombing the gate, EVERY invocation of run-checks.sh
# below MUST pass an explicit fixture flow-root argument (a mktemp -d
# directory), never a bare invocation against the real tree.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "run-checks.test.sh: failed to resolve script directory." >&2; exit 2; }
RUN_CHECKS="${SCRIPT_DIR}/run-checks.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "expected output to contain: $2"$'\n'"  actual: $1"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "expected output NOT to contain: $2"$'\n'"  actual: $1"; }
assert_eq() { [[ "$1" == "$2" ]] || fail "expected [$2], got [$1]"; }

# count_headers <text> — counts lines carrying the "=== " suite-header
# marker; used to prove aggregate (no fail-fast) vs. fail-before-any-suite
# behavior.
count_headers() {
  local n
  n="$(printf '%s\n' "$1" | grep -cF '=== ')"
  printf '%s' "${n}"
}

# =====================================================================
# Case 1: Aggregate reporting — two failing suites, no fail-fast.
# Both suites' headers and output must appear, and the exit code must be
# non-zero. AC: "aggregate reporting: two failing suites → both named,
# exit non-zero, both headers printed (no fail-fast)".
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 1)." >&2; exit 2; }
mkdir -p "${ROOT}/tests"
cat > "${ROOT}/tests/fail-a.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "fail-a ran"
exit 1
EOF
cat > "${ROOT}/tests/fail-b.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "fail-b ran"
exit 1
EOF
out="$(bash "${RUN_CHECKS}" "${ROOT}" 2>&1)"
code=$?
assert_contains "${out}" "tests/fail-a.test.sh"
assert_contains "${out}" "tests/fail-b.test.sh"
assert_contains "${out}" "fail-a ran"
assert_contains "${out}" "fail-b ran"
assert_eq "$(count_headers "${out}")" "2"
[[ "${code}" -ne 0 ]] || fail "expected non-zero exit for two failing suites, got 0"
rm -rf "${ROOT}"

# =====================================================================
# Case 2: All-green — every discovered suite passes → exit 0.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 2)." >&2; exit 2; }
mkdir -p "${ROOT}/tests"
cat > "${ROOT}/tests/pass-a.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "pass-a ran"
exit 0
EOF
cat > "${ROOT}/tests/pass-b.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "pass-b ran"
exit 0
EOF
out="$(bash "${RUN_CHECKS}" "${ROOT}" 2>&1)"
code=$?
assert_contains "${out}" "tests/pass-a.test.sh"
assert_contains "${out}" "tests/pass-b.test.sh"
assert_eq "$(count_headers "${out}")" "2"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# =====================================================================
# Case 3: Zero discovered suites → exit non-zero (false-green guard, per
# docs/health-gates.md:66-69 — an explicit "at least one iteration
# executed" counter).
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 3)." >&2; exit 2; }
out="$(bash "${RUN_CHECKS}" "${ROOT}" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "expected non-zero exit for zero discovered suites, got 0"
assert_eq "$(count_headers "${out}")" "0"
rm -rf "${ROOT}"

# =====================================================================
# Case 4: Invalid JSON → exit non-zero, with NO "=== " suite header
# emitted — JSON validation must fail before any suite runs.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 4)." >&2; exit 2; }
mkdir -p "${ROOT}/tests"
printf '{ not valid json' > "${ROOT}/broken.json"
cat > "${ROOT}/tests/pass.test.sh" <<'EOF'
#!/usr/bin/env bash
touch "$(dirname "$0")/pass-ran.marker"
echo "pass ran"
exit 0
EOF
out="$(bash "${RUN_CHECKS}" "${ROOT}" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "expected non-zero exit for invalid JSON, got 0"
assert_not_contains "${out}" "=== "
[[ ! -e "${ROOT}/tests/pass-ran.marker" ]] || fail "a suite ran despite invalid JSON — JSON validation did not fail before suite execution"
rm -rf "${ROOT}"

# =====================================================================
# Case 5: EXCLUDE entry is skipped and reported in the summary.
# The committed EXCLUDE=() array must stay empty, so this is exercised by
# sed-injecting an entry into a temp COPY of run-checks.sh — never a
# test-only env var or second CLI argument (rejected alternative per the
# plan). We first assert the sed substitution actually changed the file,
# so a silently-no-op sed (e.g. if the committed array declaration text
# ever drifts) cannot masquerade as a passing test.
# =====================================================================
ORIGINAL_CONTENT=""
if [[ -f "${RUN_CHECKS}" ]]; then
  ORIGINAL_CONTENT="$(cat "${RUN_CHECKS}")"
fi
COPY="$(mktemp)" || { echo "run-checks.test.sh: failed to create run-checks.sh copy (case 5)." >&2; exit 2; }
if [[ -f "${RUN_CHECKS}" ]]; then
  cp "${RUN_CHECKS}" "${COPY}"
else
  : > "${COPY}"
fi
sed -i 's/EXCLUDE=()/EXCLUDE=("tests\/excluded.test.sh")/' "${COPY}"
COPY_CONTENT="$(cat "${COPY}")"
if [[ "${COPY_CONTENT}" == "${ORIGINAL_CONTENT}" ]]; then
  fail "sed injection did not change the run-checks.sh copy — EXCLUDE=() literal not found (case 5 EXCLUDE test is not actually exercising the excluded branch)"
fi

ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 5)." >&2; exit 2; }
mkdir -p "${ROOT}/tests"
cat > "${ROOT}/tests/excluded.test.sh" <<'EOF'
#!/usr/bin/env bash
touch "$(dirname "$0")/excluded-ran.marker"
echo "excluded ran"
exit 1
EOF
cat > "${ROOT}/tests/pass.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "pass ran"
exit 0
EOF
out="$(bash "${COPY}" "${ROOT}" 2>&1)"
code=$?
assert_eq "${code}" "0"
[[ ! -e "${ROOT}/tests/excluded-ran.marker" ]] || fail "excluded suite executed — EXCLUDE entry was not honored"
assert_contains "${out}" "tests/pass.test.sh"
assert_not_contains "${out}" "excluded ran"
assert_contains "${out}" "tests/excluded.test.sh"
assert_contains "${out}" "skipped=1"
rm -f "${COPY}"
rm -rf "${ROOT}"

# =====================================================================
# Case 6: Discovery reaches nested subdirectories (tests/parity/-shaped).
# The divergence this ticket exists to close — the old check_structural_tests
# glob missed tests/parity/parity.test.sh entirely.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 6)." >&2; exit 2; }
mkdir -p "${ROOT}/tests/parity"
cat > "${ROOT}/tests/parity/nested.test.sh" <<'EOF'
#!/usr/bin/env bash
touch "$(dirname "$0")/nested-ran.marker"
echo "nested ran"
exit 0
EOF
out="$(bash "${RUN_CHECKS}" "${ROOT}" 2>&1)"
code=$?
assert_eq "${code}" "0"
assert_contains "${out}" "tests/parity/nested.test.sh"
[[ -e "${ROOT}/tests/parity/nested-ran.marker" ]] || fail "nested suite was not discovered/executed — discovery does not reach nested subdirectories"
rm -rf "${ROOT}"

# =====================================================================
# Case 7: A *.test.sh symlink pointing at an arbitrary script must NOT be
# discovered/executed (security: discovery restricted to regular files
# via find -type f). Asserted via the target's side-effect marker file
# never being created.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 7)." >&2; exit 2; }
mkdir -p "${ROOT}/tests"
cat > "${ROOT}/tests/target.sh" <<'EOF'
#!/usr/bin/env bash
touch "$(dirname "$0")/target-ran.marker"
echo "target ran"
exit 0
EOF
chmod +x "${ROOT}/tests/target.sh"
ln -s "${ROOT}/tests/target.sh" "${ROOT}/tests/evil.test.sh"
cat > "${ROOT}/tests/pass.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "pass ran"
exit 0
EOF
out="$(bash "${RUN_CHECKS}" "${ROOT}" 2>&1)"
code=$?
assert_eq "${code}" "0"
assert_not_contains "${out}" "evil.test.sh"
[[ ! -e "${ROOT}/tests/target-ran.marker" ]] || fail "symlinked *.test.sh was discovered/executed — find is not restricted to regular files"
rm -rf "${ROOT}"

# =====================================================================
# Case 8: An empty discovered suite must be reported as a failure (not
# silently pass, since `bash <empty-file>` exits 0) — the false-green gap
# this ticket exists to close.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 8)." >&2; exit 2; }
mkdir -p "${ROOT}/tests"
: > "${ROOT}/tests/empty.test.sh"
cat > "${ROOT}/tests/pass.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "pass ran"
exit 0
EOF
out="$(bash "${RUN_CHECKS}" "${ROOT}" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "expected non-zero exit for an empty discovered suite, got 0"
assert_contains "${out}" "tests/empty.test.sh"
rm -rf "${ROOT}"

# =====================================================================
# Cases 9-17: coverage-map sync check (ticket #916, TDD red phase).
#
# The check itself does not exist in run-checks.sh yet -- these cases pin
# down the contract Phase 4's implementation must satisfy:
#   - Guarded on the real flow root: [[ "${FLOW_ROOT}" ==
#     "$(cd "${SCRIPT_DIR}/.." && pwd)" ]]. Every case below therefore runs
#     a COPY of run-checks.sh placed at "${ROOT}/flow/scripts/run-checks.sh"
#     and invokes it with "${ROOT}/flow" as the explicit flow-root
#     argument, so SCRIPT_DIR/.. resolves to the same path as FLOW_ROOT and
#     the guard passes -- mirroring case 5's copy-into-fixture idiom, one
#     level deeper (a full repo-shaped tree, not just a flow/ tree), since
#     this check reads docs/pipeline-coverage-map.md, watch/**/*_test.go,
#     and .github/workflows/flow-ci.yml, all *outside* flow/.
#   - Repo root for those repo-relative reads is assumed to be
#     "${FLOW_ROOT}/.." (i.e. "${ROOT}"), consistent with FLOW_ROOT always
#     being flow/ itself.
#   - Token grammar (per the plan's Assumptions): every inline-code span
#     (`...`) in docs/pipeline-coverage-map.md is a token. A token matching
#     `^Test[A-Za-z0-9_]+$` is a Go-test-name token, checked against
#     `func <name>(` in any watch/**/*_test.go. A token ending in
#     `.test.sh` is a flow-suite-path token, checked as an existing regular
#     file. A token containing `.test.sh::` is a `path::literal` token; the
#     literal (everything after `::`) must appear verbatim in that path's
#     file.
#   - Fail-closed: missing/unreadable map, or zero tokens extracted overall,
#     is a failure (never a vacuous pass).
#   - Forward: every token above must resolve.
#   - Backward: every `func Test*` in watch/internal/dispatch/chain*_test.go,
#     and both flow/tests/adversarial-chain.test.sh and
#     flow/tests/escalation-hardstop-matrix.test.sh (as suite paths), must
#     be referenced somewhere in the map.
#   - AC4: every suite path referenced in the map's
#     "## Adversarial suite bounds" section must not appear in the script's
#     own EXCLUDE array.
#   - AC2 (second half): .github/workflows/flow-ci.yml's `flow:` filter
#     block must contain both 'docs/pipeline-coverage-map.md' and a
#     'watch/**/*_test.go'-style glob.
#   - The check emits its own "=== coverage-map sync check ===" header (the
#     plan's "pseudo-suite" framing) and contributes to `failed`, not `run`.
#
# Each negative case asserts (a) non-zero overall exit and (b) the header
# plus a substring identifying the specific offending token/path, so
# Phase 4 has room to phrase the exact message while the essential signal
# stays pinned. NOTE ON CASE COUNT: the delegation's case range said
# "9-16" (8 slots) but its own itemized failure-mode list enumerates 9
# distinct scenarios (8 failure modes + 1 happy path) -- see the Phase 3
# report for this ticket. All 9 are implemented here as cases 9-17 so none
# of the plan's Files-to-Modify enumeration is silently dropped.
# =====================================================================

# build_coverage_fixture <root> -- populates <root> with a complete,
# well-formed repo-shaped fixture tree that satisfies every invariant
# above: forward (every mapped token resolves), backward (every
# chain*_test.go Go test and both flow adversarial suites are mapped),
# AC4 (no adversarial suite in EXCLUDE), and flow-ci.yml registration.
# Cases 9-16 each build a fresh copy of this baseline and mutate exactly
# one aspect to introduce a single fault; case 17 (happy path) uses it
# unmodified.
build_coverage_fixture() {
  local root="$1"
  mkdir -p "${root}/flow/scripts" "${root}/flow/tests" "${root}/watch/internal/dispatch" "${root}/docs" "${root}/.github/workflows" \
    || { echo "run-checks.test.sh: failed to create coverage fixture directories under ${root}." >&2; exit 2; }

  if [[ -f "${RUN_CHECKS}" ]]; then
    cp "${RUN_CHECKS}" "${root}/flow/scripts/run-checks.sh" \
      || { echo "run-checks.test.sh: failed to copy run-checks.sh into coverage fixture." >&2; exit 2; }
  else
    : > "${root}/flow/scripts/run-checks.sh"
  fi

  cat > "${root}/flow/tests/dummy-pass.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "dummy-pass ran"
exit 0
EOF

  cat > "${root}/flow/tests/adversarial-chain.test.sh" <<'EOF'
#!/usr/bin/env bash
# MARKER: adversarial chain suite fixture
echo "adversarial-chain ran"
exit 0
EOF

  cat > "${root}/flow/tests/escalation-hardstop-matrix.test.sh" <<'EOF'
#!/usr/bin/env bash
# MARKER: hardstop matrix suite fixture
echo "escalation-hardstop-matrix ran"
exit 0
EOF

  cat > "${root}/watch/internal/dispatch/chainfake_test.go" <<'EOF'
package dispatch

import "testing"

func TestChainFake_ScenarioInventoryIsFixed(t *testing.T) {}
EOF

  cat > "${root}/.github/workflows/flow-ci.yml" <<'EOF'
name: flow-ci
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    outputs:
      flow: ${{ steps.filter.outputs.flow }}
    steps:
      - uses: dorny/paths-filter@v3
        id: filter
        with:
          filters: |
            flow:
              - 'flow/**'
              - 'docs/pipeline-coverage-map.md'
              - 'watch/**/*_test.go'
              - '!flow/AGENTS.md'
              - '!CLAUDE.md'
              - '!README.md'
EOF

  cat > "${root}/docs/pipeline-coverage-map.md" <<'EOF'
# Pipeline Coverage Map

Coverage-map fixture for run-checks.test.sh cases 9-17.

## Acceptance-criterion coverage

| AC | Claim | Test |
|---|---|---|
| FIXTURE AC1 | scenario inventory is fixed | `TestChainFake_ScenarioInventoryIsFixed` |
| FIXTURE AC2 | dummy suite passes | `flow/tests/dummy-pass.test.sh` |

## Doc-claim coverage

| Claim | Test |
|---|---|
| dummy doc claim | `flow/tests/dummy-pass.test.sh` |

## Adversarial suite bounds

| Suite | Scenario count | Runtime ceiling | EXCLUDE status |
|---|---|---|---|
| `flow/tests/adversarial-chain.test.sh::MARKER: adversarial chain suite fixture` | 8 | 30s | not excluded |
| `flow/tests/escalation-hardstop-matrix.test.sh::MARKER: hardstop matrix suite fixture` | 21 | 30s | not excluded |

## Followups

none
EOF
}

# =====================================================================
# Case 9: Missing coverage map file.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 9)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
rm -f "${ROOT}/docs/pipeline-coverage-map.md"
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 9: expected non-zero exit when docs/pipeline-coverage-map.md is missing, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "docs/pipeline-coverage-map.md"
rm -rf "${ROOT}"

# =====================================================================
# Case 10: Map with zero extracted rows (present, readable, but no
# inline-code tokens at all) -- fail-closed anti-vacuity guard.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 10)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat > "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'
# Pipeline Coverage Map

No inline-code tokens anywhere in this file. Zero rows should be
extracted, and the check must fail closed rather than vacuously pass.
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 10: expected non-zero exit for a coverage map with zero extracted rows, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "zero"
rm -rf "${ROOT}"

# =====================================================================
# Case 11: Unknown Go test name referenced in the map -- no matching
# `func <name>(` in any watch/**/*_test.go.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 11)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat >> "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'

| FIXTURE AC-UNKNOWN-GO-TEST | references a Go test that does not exist | `TestDoesNotExist_Whatever` |
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 11: expected non-zero exit for an unknown Go test name in the map, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "TestDoesNotExist_Whatever"
rm -rf "${ROOT}"

# =====================================================================
# Case 12: Unknown flow suite path referenced in the map -- a `.test.sh`
# path token that does not exist as a regular file.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 12)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat >> "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'

| FIXTURE AC-UNKNOWN-SUITE-PATH | references a flow suite that does not exist | `flow/tests/does-not-exist.test.sh` |
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 12: expected non-zero exit for an unknown flow suite path in the map, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "flow/tests/does-not-exist.test.sh"
rm -rf "${ROOT}"

# =====================================================================
# Case 13: A `path::literal` token whose literal no longer appears in
# that file.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 13)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat >> "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'

| FIXTURE AC-MISSING-LITERAL | references a literal absent from the suite | `flow/tests/dummy-pass.test.sh::LITERAL_NOT_PRESENT_ANYWHERE` |
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 13: expected non-zero exit for a path::literal token whose literal is absent, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "LITERAL_NOT_PRESENT_ANYWHERE"
rm -rf "${ROOT}"

# =====================================================================
# Case 14: An orphan adversarial Go test (in
# watch/internal/dispatch/chain*_test.go) absent from the map --
# backward-invariant violation.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 14)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat >> "${ROOT}/watch/internal/dispatch/chainfake_test.go" <<'EOF'

func TestChainFake_OrphanScenario(t *testing.T) {}
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 14: expected non-zero exit for an orphan adversarial Go test absent from the map, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "TestChainFake_OrphanScenario"
rm -rf "${ROOT}"

# =====================================================================
# Case 15: An adversarial suite injected into EXCLUDE in the fixture's
# copied run-checks.sh (same sed idiom as case 5, with a "sed actually
# changed the file" non-vacuity proof) -- AC4 requires the check to fail
# this even though the ordinary discovery/skip machinery also honors it.
# The injected EXCLUDE entry uses "tests/adversarial-chain.test.sh"
# (flow-root-relative), matching is_excluded()'s own comparison target
# (rel="${f#"${FLOW_ROOT}"/}"), NOT the map's repo-root-relative
# "flow/tests/adversarial-chain.test.sh" token spelling -- proven by the
# not_contains assertion below (the ordinary skip mechanism must engage on
# the exact string is_excluded() compares against). Phase 4's AC4 check
# must therefore normalize the map's suite-path token (strip the leading
# "flow/") before comparing it against EXCLUDE.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 15)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
BEFORE_SED="$(cat "${ROOT}/flow/scripts/run-checks.sh")"
sed -i 's/EXCLUDE=()/EXCLUDE=("tests\/adversarial-chain.test.sh")/' "${ROOT}/flow/scripts/run-checks.sh"
AFTER_SED="$(cat "${ROOT}/flow/scripts/run-checks.sh")"
if [[ "${AFTER_SED}" == "${BEFORE_SED}" ]]; then
  fail "case 15: sed injection did not change the fixture's run-checks.sh copy -- EXCLUDE=() literal not found (case 15 EXCLUDE test is not actually exercising the excluded branch)"
fi
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 15: expected non-zero exit for an adversarial suite injected into EXCLUDE, got 0"
assert_contains "${out}" "tests/adversarial-chain.test.sh === (skipped: excluded)"
assert_not_contains "${out}" "adversarial-chain ran"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "adversarial-chain.test.sh"
assert_contains "${out}" "EXCLUDE"
rm -rf "${ROOT}"

# =====================================================================
# Case 16: flow-ci.yml's `flow` filter missing the coverage-map path /
# `watch/**` test glob registration.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 16)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat > "${ROOT}/.github/workflows/flow-ci.yml" <<'EOF'
name: flow-ci
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    outputs:
      flow: ${{ steps.filter.outputs.flow }}
    steps:
      - uses: dorny/paths-filter@v3
        id: filter
        with:
          filters: |
            flow:
              - 'flow/**'
              - '!flow/AGENTS.md'
              - '!CLAUDE.md'
              - '!README.md'
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 16: expected non-zero exit when flow-ci.yml's flow filter is missing the coverage-map/watch-glob registration, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "flow-ci.yml"
rm -rf "${ROOT}"

# =====================================================================
# Case 17: Happy path -- a well-formed fixture (valid map, all tokens
# resolve, no EXCLUDE violations, flow-ci.yml registration present).
# Once the check exists, this is expected to pass (exit 0) with the
# coverage-map sync check's own header/pass line present. TODAY (red
# phase, check not yet implemented), the exit-code assertion alone would
# NOT be red -- the unmodified fixture already exits 0 via the ordinary
# suite-discovery path with nothing to fail it. The load-bearing red
# assertion for this case is therefore the header: it proves the check
# ran at all, which it does not yet.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 17)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
assert_eq "${code}" "0"
assert_contains "${out}" "coverage-map sync check"
rm -rf "${ROOT}"

# =====================================================================
# Case 18: watch/internal/dispatch renamed/missing -- the backward
# adversarial-Go-test invariant's `find` must fail closed instead of
# silently performing zero comparisons (Phase 6/7 fix #1).
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 18)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
mv "${ROOT}/watch/internal/dispatch" "${ROOT}/watch/internal/dispatch-renamed"
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 18: expected non-zero exit when watch/internal/dispatch is renamed/missing, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "chain*_test.go"
rm -rf "${ROOT}"

# =====================================================================
# Case 19: A `*_test.go::literal` token (the "## Adversarial suite
# bounds" table's Go-glob-and-function-name shape) references a Go test
# function that does not exist under the glob's matching files (Phase 6/7
# fix #2).
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 19)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat >> "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'

| FIXTURE AC-UNKNOWN-GO-GLOB-TEST | references a Go test function absent from the glob's files | `watch/internal/dispatch/chain*_test.go::TestDoesNotExist_GlobForm` |
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 19: expected non-zero exit for an unknown Go test function referenced via a *_test.go::literal token, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "TestDoesNotExist_GlobForm"
rm -rf "${ROOT}"

# =====================================================================
# Case 20: A path-traversal token must be rejected outright (never read),
# for both the .test.sh::literal shape and the new .go::literal shape
# (Phase 6/7 fix #4).
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 20)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat >> "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'

| FIXTURE AC-PATH-TRAVERSAL | a path-traversal token must be rejected, not read | `flow/tests/../../../../../etc/passwd.test.sh::literal` |
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 20: expected non-zero exit for a path-traversal token, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "unsafe or malformed path"
rm -rf "${ROOT}"

# =====================================================================
# Case 21: The "## Adversarial suite bounds" heading is renamed/missing
# -- AC4's section extraction must fail closed instead of silently
# performing zero comparisons (Phase 6/7 fix #3).
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 21)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
sed -i 's/## Adversarial suite bounds/## Adversarial Suite Bounds (renamed)/' "${ROOT}/docs/pipeline-coverage-map.md"
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 21: expected non-zero exit when the '## Adversarial suite bounds' heading is renamed/missing, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "Adversarial suite bounds"
rm -rf "${ROOT}"

# =====================================================================
# Case 22: A `path::literal` token with an empty literal after `::` (e.g.
# `flow/tests/dummy-pass.test.sh::` with nothing following) must fail
# closed rather than vacuously matching every non-empty file via
# `grep -qF -- ""` (Phase 6+7 final fix round, fix #2).
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 22)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat >> "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'

| FIXTURE AC-EMPTY-LITERAL | a path::literal token with nothing after :: | `flow/tests/dummy-pass.test.sh::` |
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 22: expected non-zero exit for a path::literal token with an empty literal, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "empty literal"
rm -rf "${ROOT}"

# =====================================================================
# Case 23: The "## Adversarial suite bounds" heading survives, but every
# backtick token inside it is non-suite-shaped (e.g. a bare number instead
# of a *.test.sh or *.test.sh::literal token). ADV_TOKENS_FILE stays
# non-empty (the raw extraction guard passes) but AC4's own loop filter
# would skip every token, so the EXCLUDE-membership check must fail closed
# instead of silently performing zero comparisons (Phase 6+7 final fix
# round, fix #3). The two adversarial suites are re-referenced elsewhere
# in the map so backward check 2 (both suites mapped) still passes,
# isolating the failure to the AC4 anti-vacuity guard.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 23)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
sed -i '/adversarial-chain\.test\.sh::MARKER: adversarial chain suite fixture/d' "${ROOT}/docs/pipeline-coverage-map.md"
sed -i '/escalation-hardstop-matrix\.test\.sh::MARKER: hardstop matrix suite fixture/d' "${ROOT}/docs/pipeline-coverage-map.md"
sed -i '/^## Adversarial suite bounds/a\
| `42` | 8 | 30s | not excluded |' "${ROOT}/docs/pipeline-coverage-map.md"
cat >> "${ROOT}/docs/pipeline-coverage-map.md" <<'EOF'

| FIXTURE AC-ADV-ELSEWHERE-1 | adversarial-chain suite referenced outside the bounds table so backward check 2 still passes | `flow/tests/adversarial-chain.test.sh` |
| FIXTURE AC-ADV-ELSEWHERE-2 | hardstop matrix suite referenced outside the bounds table so backward check 2 still passes | `flow/tests/escalation-hardstop-matrix.test.sh` |
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 23: expected non-zero exit when '## Adversarial suite bounds' contains only non-suite-shaped tokens, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "zero suite-shaped"
rm -rf "${ROOT}"

# =====================================================================
# Case 24: flow-ci.yml with TWO paths-filter steps (#950 code/extra split)
# -- positive, pins the union. run-checks.sh:417-448's awk `flow:`-block
# extractor was written against a SINGLE `flow:` block; #950 splits the
# `changes` job's paths-filter step into an `id: code` step
# (predicate-quantifier: 'every', root + exclusions) and an `id: extra`
# step (default 'some', OR-ed extra roots) each carrying their own `flow:`
# block. The awk survives this only because it unions every `flow:` block
# it finds in the file rather than stopping at the first one -- but
# nothing pinned that before this case. Both coverage-map registration
# paths (docs/pipeline-coverage-map.md, watch/**/*_test.go) live ONLY in
# the second (`extra`) block here, so an extractor that reads just the
# first block, or that stops at a step boundary, fails this case. Must
# pass against TODAY's unmodified run-checks.sh, before any workflow edit.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 24)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat > "${ROOT}/.github/workflows/flow-ci.yml" <<'EOF'
name: flow-ci
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    outputs:
      flow: ${{ steps.code.outputs.flow == 'true' || steps.extra.outputs.flow == 'true' }}
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: code
        with:
          predicate-quantifier: 'every'
          filters: |
            flow:
              - 'flow/**'
              - '!flow/AGENTS.md'
              - '!flow/CLAUDE.md'
              - '!flow/README.md'
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: extra
        with:
          filters: |
            flow:
              - '.cenci/config.json'
              - '.github/workflows/flow-ci.yml'
              - 'docs/pipeline-coverage-map.md'
              - 'watch/**/*_test.go'
            maintenance:
              - 'docs/**'
              - 'AGENTS.md'
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -eq 0 ]] || fail "case 24: expected exit 0 for a two-step flow-ci.yml carrying the coverage-map/watch-glob registration only in the second ('extra') flow: block, got ${code}"$'\n'"  actual: ${out}"
assert_contains "${out}" "coverage-map sync check"
rm -rf "${ROOT}"

# =====================================================================
# Case 25: same two-step flow-ci.yml shape as case 24, but with
# 'docs/pipeline-coverage-map.md' and 'watch/**/*_test.go' removed from
# BOTH `flow:` blocks -- anti-vacuity companion to case 24. Proves the
# two-block shape does not silently disable the registration check (i.e.
# case 24 is not passing merely because the union makes every fixture
# pass regardless of content). Red-by-construction against its own
# mutated fixture: the case itself PASSES by correctly observing the
# failure.
# =====================================================================
ROOT="$(mktemp -d)" || { echo "run-checks.test.sh: failed to create fixture root (case 25)." >&2; exit 2; }
build_coverage_fixture "${ROOT}"
cat > "${ROOT}/.github/workflows/flow-ci.yml" <<'EOF'
name: flow-ci
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    outputs:
      flow: ${{ steps.code.outputs.flow == 'true' || steps.extra.outputs.flow == 'true' }}
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: code
        with:
          predicate-quantifier: 'every'
          filters: |
            flow:
              - 'flow/**'
              - '!flow/AGENTS.md'
              - '!flow/CLAUDE.md'
              - '!flow/README.md'
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: extra
        with:
          filters: |
            flow:
              - '.cenci/config.json'
              - '.github/workflows/flow-ci.yml'
            maintenance:
              - 'docs/**'
              - 'AGENTS.md'
EOF
out="$(bash "${ROOT}/flow/scripts/run-checks.sh" "${ROOT}/flow" 2>&1)"
code=$?
[[ "${code}" -ne 0 ]] || fail "case 25: expected non-zero exit when the coverage-map/watch-glob registration is absent from both flow: blocks, got 0"
assert_contains "${out}" "coverage-map sync check"
assert_contains "${out}" "flow-ci.yml"
rm -rf "${ROOT}"

echo "run-checks.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
