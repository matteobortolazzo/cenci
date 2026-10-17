#!/usr/bin/env bash
# Behavioral suite for skills/refine/scripts/coverage-map.sh -- the mechanical
# half of the refine skill's split coverage exercise (Velka's "Splitting a
# ticket": every parent item has exactly one child owner; the parent gets a
# `### Coverage map`). Runs the real script against mktemp fixtures; no gh,
# no git, no network. Auto-discovered by scripts/run-checks.sh.
#
# Scenarios:
#   1 full coverage -> exit 0, counts line, rendered map with the owner labels
#   2 an item with no owner -> exit 3, `unowned:` names it, no map rendered
#   3 an item owned twice -> exit 4, `duplicate:` names both owners
#   4 whitespace differences never break verbatim ownership
#   5 a paraphrased (non-verbatim) copy is NOT ownership
#   6 malformed item line / zero items / unreadable child -> exit 2
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "coverage-map.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "coverage-map.test.sh: failed to resolve flow directory." >&2; exit 2; }
SCRIPT="${FLOW_DIR}/skills/refine/scripts/coverage-map.sh"
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected to contain [$2], got [$1]"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "$3: expected NOT to contain [$2]"; }

[[ -r "${SCRIPT}" ]] || { fail "script missing: ${SCRIPT}"; echo "coverage-map.test.sh: failures=${failures}"; exit 1; }
T="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 2; }
trap 'rm -rf "${T}"' EXIT

cat > "${T}/items.md" <<'ITEMS'
1. [acceptance criterion] `GET /items` returns 200 with a JSON array
2. [edge case] An empty list renders the "No items yet" placeholder
3. [documentation item] README documents the `--format` flag
4. [boundary] Lists are capped at 100 items per page
ITEMS
printf 'Related to #1\n\n### Acceptance Criteria\n- [ ] (P1) `GET /items` returns 200 with a JSON array\n- [ ] (P4) Lists are capped at 100 items per page\n' > "${T}/c1.md"
printf 'Related to #1\n\n### Acceptance Criteria\n- [ ] (P2) An empty list renders the "No items yet" placeholder\n- [ ] (P3) README documents the `--format` flag\n' > "${T}/c2.md"

# --- 1. full coverage ---------------------------------------------------------
OUT="$(bash "${SCRIPT}" check --items "${T}/items.md" --child "#10 (1/2)=${T}/c1.md" --child "#11 (2/2)=${T}/c2.md" 2>"${T}/err")"; rc=$?
assert_eq "${rc}" "0" "case1 exit"
assert_contains "${OUT}" "items=4 owned=4 children=2" "case1 counts line"
assert_contains "${OUT}" "### Coverage map" "case1 map heading"
assert_contains "${OUT}" "| 1 | acceptance criterion | \`GET /items\` returns 200 with a JSON array | #10 (1/2) |" "case1 row 1 owner"
assert_contains "${OUT}" "| 3 | documentation item | README documents the \`--format\` flag | #11 (2/2) |" "case1 row 3 owner"
assert_eq "$(cat "${T}/err")" "" "case1 stderr empty"

# --- 2. unowned item: the split fails, the item is named, never dropped --------
printf 'Related to #1\n\n### Acceptance Criteria\n- [ ] (P2) An empty list renders the "No items yet" placeholder\n' > "${T}/c2-short.md"
OUT="$(bash "${SCRIPT}" check --items "${T}/items.md" --child "c1=${T}/c1.md" --child "c2=${T}/c2-short.md" 2>"${T}/err")"; rc=$?
assert_eq "${rc}" "3" "case2 exit (unowned)"
assert_contains "${OUT}" "items=4 owned=3 children=2" "case2 counts"
assert_not_contains "${OUT}" "### Coverage map" "case2 no map rendered on failure"
assert_contains "$(cat "${T}/err")" "unowned: 3 [documentation item] README documents the \`--format\` flag" "case2 names the unowned item"
assert_contains "$(cat "${T}/err")" "never drop an item" "case2 says never drop"

# --- 3. duplicate owner ---------------------------------------------------------
printf 'Related to #1\n\n### Acceptance Criteria\n- [ ] (P1) `GET /items` returns 200 with a JSON array\n- [ ] (P2) An empty list renders the "No items yet" placeholder\n- [ ] (P3) README documents the `--format` flag\n' > "${T}/c2-dup.md"
OUT="$(bash "${SCRIPT}" check --items "${T}/items.md" --child "c1=${T}/c1.md" --child "c2=${T}/c2-dup.md" 2>"${T}/err")"; rc=$?
assert_eq "${rc}" "4" "case3 exit (duplicate)"
assert_contains "$(cat "${T}/err")" "duplicate: 1 [acceptance criterion]" "case3 names the duplicated item"
assert_contains "$(cat "${T}/err")" "-> c1, c2" "case3 names both owners"

# --- 4. whitespace is normalized; 5. paraphrase is not ownership ----------------
printf 'Related to #1\n\n- [ ] (P1)   `GET /items`   returns 200\n  with a JSON array\n- [ ] (P4) Lists are capped at 100 items per page\n' > "${T}/c1-ws.md"
OUT="$(bash "${SCRIPT}" check --items "${T}/items.md" --child "c1=${T}/c1-ws.md" --child "c2=${T}/c2.md" 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case4 whitespace-normalized ownership"
printf 'Related to #1\n\n- [ ] (P1) GET /items responds with 200 and a JSON list\n- [ ] (P4) Lists are capped at 100 items per page\n' > "${T}/c1-para.md"
OUT="$(bash "${SCRIPT}" check --items "${T}/items.md" --child "c1=${T}/c1-para.md" --child "c2=${T}/c2.md" 2>"${T}/err")"; rc=$?
assert_eq "${rc}" "3" "case5 a paraphrase is not ownership"
assert_contains "$(cat "${T}/err")" "unowned: 1 [acceptance criterion]" "case5 paraphrased item reported unowned"

# --- 6. malformed inputs fail closed (exit 2) ----------------------------------
printf '1. [acceptance criterion] ok\n2. [wrong kind] nope\n' > "${T}/bad-items.md"
bash "${SCRIPT}" check --items "${T}/bad-items.md" --child "c1=${T}/c1.md" >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6a malformed item line"
assert_contains "$(cat "${T}/err")" "malformed item line" "case6a message"
: > "${T}/empty-items.md"
bash "${SCRIPT}" check --items "${T}/empty-items.md" --child "c1=${T}/c1.md" >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6b zero items"
bash "${SCRIPT}" check --items "${T}/items.md" --child "c1=${T}/does-not-exist.md" >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6c unreadable child"
bash "${SCRIPT}" check --items "${T}/items.md" >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case6d no --child"

echo "coverage-map.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
