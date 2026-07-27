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

echo "run-checks.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
