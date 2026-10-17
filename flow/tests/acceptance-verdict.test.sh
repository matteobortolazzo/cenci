#!/usr/bin/env bash
# Behavioral suite for skills/review/scripts/acceptance-verdict.sh -- the
# deterministic pass/fail gate over the acceptance reviewer's item table,
# shared by Phase 6 + 7, Phase 9, /cenci:review and /cenci:close.
#
# Scenarios:
#   1 full coverage -> pass, exit 0
#   2 a missing item -> fail, exit 1, `blocking:` names it (blocks the pipeline)
#   3 a partial item -> fail, exit 1
#   4 unrequested rows never block on their own
#   5 UI evidence: --ui requires gallery+gist in en-US and it-IT; --ui-local
#     requires gallery+capture; a diff-only UI review fails
#   6 malformed: unknown status / zero rows / unreadable -> exit 2 (never a pass)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "acceptance-verdict.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "acceptance-verdict.test.sh: failed to resolve flow directory." >&2; exit 2; }
SCRIPT="${FLOW_DIR}/skills/review/scripts/acceptance-verdict.sh"
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected to contain [$2], got [$1]"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "$3: expected NOT to contain [$2]"; }

[[ -r "${SCRIPT}" ]] || { fail "script missing: ${SCRIPT}"; echo "acceptance-verdict.test.sh: failures=${failures}"; exit 1; }
T="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 2; }
trap 'rm -rf "${T}"' EXIT

header='| # | Kind | Item | Status | Proof |
|---|---|---|---|---|'

# --- 1. full coverage passes ---------------------------------------------------
printf '%s\n| 1 | acceptance criterion | GET returns 200 | covered | tests/api.test.sh::returns 200 |\n| 2 | edge case | empty list placeholder | covered | ui/list.spec.ts::empty |\n' "${header}" > "${T}/pass.md"
OUT="$(bash "${SCRIPT}" "${T}/pass.md" 2>"${T}/err")"; rc=$?
assert_eq "${rc}" "0" "case1 exit"
assert_eq "${OUT}" "covered=2 partial=0 missing=0 unrequested=0 verdict=pass" "case1 summary"

# --- 2. a missing item blocks ---------------------------------------------------
printf '%s\n| 1 | acceptance criterion | GET returns 200 | covered | tests/api.test.sh::returns 200 |\n| 2 | edge case | empty list placeholder | missing | — |\n' "${header}" > "${T}/missing.md"
OUT="$(bash "${SCRIPT}" "${T}/missing.md" 2>/dev/null)"; rc=$?
assert_eq "${rc}" "1" "case2 exit"
assert_contains "${OUT}" "covered=1 partial=0 missing=1 unrequested=0 verdict=fail" "case2 summary"
assert_contains "${OUT}" "blocking: 2 [edge case] empty list placeholder -- missing" "case2 blocking row named"

# --- 3. a partial item blocks ---------------------------------------------------
printf '%s\n| 1 | documentation item | README documents --format | partial | flag added, README not updated |\n' "${header}" > "${T}/partial.md"
OUT="$(bash "${SCRIPT}" "${T}/partial.md" 2>/dev/null)"; rc=$?
assert_eq "${rc}" "1" "case3 exit"
assert_contains "${OUT}" "partial=1" "case3 partial counted"
assert_contains "${OUT}" "blocking: 1 [documentation item] README documents --format -- partial" "case3 blocking row"

# --- 4. unrequested never blocks on its own -------------------------------------
printf '%s\n| 1 | acceptance criterion | GET returns 200 | covered | tests/api.test.sh::returns 200 |\n| 2 | unrequested | extra CSV export | unrequested | src/export.ts |\n' "${header}" > "${T}/unreq.md"
OUT="$(bash "${SCRIPT}" "${T}/unreq.md" 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case4 exit"
assert_contains "${OUT}" "unrequested=1 verdict=pass" "case4 summary"

# --- 5. UI evidence ---------------------------------------------------------------
cp "${T}/pass.md" "${T}/ui-none.md"
OUT="$(bash "${SCRIPT}" "${T}/ui-none.md" --ui 2>/dev/null)"; rc=$?
assert_eq "${rc}" "1" "case5a diff-only UI review fails"
assert_contains "${OUT}" "ui-evidence-missing: gallery en-US" "case5a gallery en-US missing"
assert_contains "${OUT}" "ui-evidence-missing: gallery it-IT" "case5a gallery it-IT missing"
assert_contains "${OUT}" "ui-evidence-missing: gist en-US" "case5a gist en-US missing"
assert_contains "${OUT}" "ui-evidence-missing: gist it-IT" "case5a gist it-IT missing"
{ cat "${T}/pass.md"; printf 'Evidence: gallery en-US: https://gist.githubusercontent.com/u/abc/raw/gallery-en-US.png\nEvidence: gallery it-IT: https://gist.githubusercontent.com/u/abc/raw/gallery-it-IT.png\nEvidence: gist en-US: https://gist.githubusercontent.com/u/abc/raw/list-en-US.png\n'; } > "${T}/ui-three.md"
OUT="$(bash "${SCRIPT}" "${T}/ui-three.md" --ui 2>/dev/null)"; rc=$?
assert_eq "${rc}" "1" "case5b one locale missing still fails"
assert_contains "${OUT}" "ui-evidence-missing: gist it-IT" "case5b names the missing locale"
assert_not_contains "${OUT}" "ui-evidence-missing: gallery en-US" "case5b present evidence not reported"
{ cat "${T}/ui-three.md"; printf 'Evidence: gist it-IT: https://gist.githubusercontent.com/u/abc/raw/list-it-IT.png\n'; } > "${T}/ui-four.md"
OUT="$(bash "${SCRIPT}" "${T}/ui-four.md" --ui 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case5c all four UI evidence lines pass"
{ cat "${T}/pass.md"; printf 'Evidence: gallery en-US: /tmp/s/gallery-en-US.png\nEvidence: gallery it-IT: /tmp/s/gallery-it-IT.png\nEvidence: capture en-US: /tmp/s/list-en-US.png\nEvidence: capture it-IT: /tmp/s/list-it-IT.png\n'; } > "${T}/ui-local.md"
OUT="$(bash "${SCRIPT}" "${T}/ui-local.md" --ui-local 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case5d --ui-local accepts local captures"
OUT="$(bash "${SCRIPT}" "${T}/ui-local.md" --ui 2>/dev/null)"; rc=$?
assert_eq "${rc}" "1" "case5e local captures are not gist evidence under --ui"

# --- 6. malformed fails closed ------------------------------------------------------
printf '%s\n| 1 | acceptance criterion | GET returns 200 | done | tests |\n' "${header}" > "${T}/bad-status.md"
bash "${SCRIPT}" "${T}/bad-status.md" >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6a unknown status"
assert_contains "$(cat "${T}/err")" "status must be covered|partial|missing|unrequested" "case6a message"
printf '%s\n' "${header}" > "${T}/zero.md"
bash "${SCRIPT}" "${T}/zero.md" >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6b zero rows"
bash "${SCRIPT}" "${T}/nope.md" >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6c unreadable"
bash "${SCRIPT}" "${T}/pass.md" --bogus >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6d unknown flag"

echo "acceptance-verdict.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
