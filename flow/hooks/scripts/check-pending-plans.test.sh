#!/usr/bin/env bash
# check-pending-plans.test.sh — runtime behavior of the SessionStart
# pending-plans advisory (#776). Modelled on check-config-staleness.test.sh
# (a `failures=` counter, small assert_* helpers, self-contained, auto-
# discovered by flow's `*.test.sh` glob via run-checks.sh).
#
# Contract pinned here: the hook globs "$PLANS_DIR" (".plans") for "*.md"
# files, emits nothing (exit 0, empty stdout) when zero plans are found,
# names the single plan when exactly one is found, lists every filename
# when more than one is found, and every non-empty payload is valid JSON
# shaped as hookSpecificOutput.additionalContext for a SessionStart event.
#
# Case 4 (files under .plans/done/ are not reported) pins the archive
# semantics ticket #776 relies on for phase-9 archiving to silence the
# reminder without a hook change. This same PR adds `-maxdepth 1` to the
# script's plan lookup (`find "$PLANS_DIR" -maxdepth 1 -name "*.md" -type
# f`) precisely so that files under .plans/done/ are excluded; pinning this
# case here guards against a future regression back to a recursive lookup
# that would once again pick up archived plans.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK="${SCRIPT_DIR}/check-pending-plans.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain [$2], got [$1]"; }

# make_fixture — creates an empty repo-root fixture; sets ROOT.
make_fixture() {
  ROOT="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 1; }
  [[ -n "${ROOT}" ]] || { echo "mktemp returned an empty path" >&2; exit 1; }
}

cleanup_fixture() { rm -rf "${ROOT}"; }

run_hook() {  # run_hook — runs the hook from ROOT (relative ".plans" lookup)
  OUT="$(cd "${ROOT}" && bash "${HOOK}" 2>&1)"
  CODE=$?
}

# --- Case 1: zero plan files → no pending-plans context emitted -----------
# Sub-case A: .plans/ does not exist at all.
make_fixture
run_hook
assert_eq "${CODE}" "0" "case1a exit"
assert_eq "${OUT}" "" "case1a no .plans dir: silent"
cleanup_fixture

# Sub-case B: .plans/ exists but is empty (no .md files).
make_fixture
mkdir -p "${ROOT}/.plans"
run_hook
assert_eq "${CODE}" "0" "case1b exit"
assert_eq "${OUT}" "" "case1b empty .plans dir: silent"
cleanup_fixture

# --- Case 2: one plan file → payload names it ------------------------------
make_fixture
mkdir -p "${ROOT}/.plans"
: > "${ROOT}/.plans/42-foo.md"
run_hook
assert_eq "${CODE}" "0" "case2 exit"
echo "${OUT}" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null 2>&1 \
  || fail "case2 output must be SessionStart hookSpecificOutput JSON (got: ${OUT})"
CTX="$(echo "${OUT}" | jq -r '.hookSpecificOutput.additionalContext')"
assert_contains "${CTX}" "Pending implementation plan found: 42-foo.md" "case2 names the single plan"
assert_contains "${CTX}" "/cenci:implement" "case2 points at implement"
cleanup_fixture

# --- Case 3: many plan files → payload lists all ---------------------------
make_fixture
mkdir -p "${ROOT}/.plans"
: > "${ROOT}/.plans/1-a.md"
: > "${ROOT}/.plans/2-b.md"
: > "${ROOT}/.plans/3-c.md"
run_hook
assert_eq "${CODE}" "0" "case3 exit"
echo "${OUT}" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null 2>&1 \
  || fail "case3 output must be SessionStart hookSpecificOutput JSON (got: ${OUT})"
CTX="$(echo "${OUT}" | jq -r '.hookSpecificOutput.additionalContext')"
assert_contains "${CTX}" "Multiple pending plans found" "case3 uses the multiple-plans phrasing"
assert_contains "${CTX}" "1-a.md" "case3 lists first plan"
assert_contains "${CTX}" "2-b.md" "case3 lists second plan"
assert_contains "${CTX}" "3-c.md" "case3 lists third plan"
cleanup_fixture

# --- Case 4: files under .plans/done/ are NOT reported ---------------------
# Pins the archive semantics from #776 part 1: once a plan is moved to
# .plans/done/, a fresh session must get no pending-plans reminder at all.
make_fixture
mkdir -p "${ROOT}/.plans/done"
: > "${ROOT}/.plans/done/9-archived.md"
run_hook
assert_eq "${CODE}" "0" "case4 exit"
assert_eq "${OUT}" "" "case4 archived-only .plans/done/ plan: silent (no reminder)"
cleanup_fixture

# --- Case 5: output is valid JSON with the hookSpecificOutput.additionalContext shape
# Re-validated explicitly via jq (beyond the inline checks in cases 2/3) so a
# future output-shape regression is pinned on its own, not just incidentally
# covered by an unrelated case.
make_fixture
mkdir -p "${ROOT}/.plans"
: > "${ROOT}/.plans/5-shape.md"
run_hook
echo "${OUT}" | jq empty >/dev/null 2>&1 || fail "case5 output must be valid JSON (got: ${OUT})"
echo "${OUT}" | jq -e 'has("hookSpecificOutput")' >/dev/null 2>&1 \
  || fail "case5 output must have a top-level hookSpecificOutput key (got: ${OUT})"
echo "${OUT}" | jq -e '.hookSpecificOutput | has("additionalContext")' >/dev/null 2>&1 \
  || fail "case5 hookSpecificOutput must have an additionalContext key (got: ${OUT})"
echo "${OUT}" | jq -e '.hookSpecificOutput.additionalContext | type == "string"' >/dev/null 2>&1 \
  || fail "case5 additionalContext must be a string (got: ${OUT})"
cleanup_fixture

echo "check-pending-plans.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
