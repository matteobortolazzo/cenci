#!/usr/bin/env bash
# Behavioral suite for skills/close/scripts/close-parent.sh -- the "close
# parent" step (Velka's "Closing a split parent requires a coverage review").
# Replays the real script against a recording `gh` stub (fixtures under
# close-parent/) so every refuse/post/create/close branch is exercised end to
# end with zero network. Auto-discovered by scripts/run-checks.sh.
#
# Scenarios:
#   1 an open child -> exit 3, refused, ZERO writes
#   2 not a split parent -> exit 5, zero writes
#   3 a missing + a partial item -> exit 4: review comment posted (banner,
#     table verbatim, marker, read back by id), one new child per gap under
#     the same parent with the parent's milestone, non-lifecycle labels and
#     the item verbatim, linked and verified; parent NOT closed
#   4 full coverage -> exit 0: review posted, parent closed as completed,
#     Implemented swapped in, state re-read; no child created
#   5 --dry-run -> zero writes
#   6 malformed table -> exit 2, zero writes; a failed link -> exit 2 with
#     the partial state reported, parent left open
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "close-parent.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "close-parent.test.sh: failed to resolve flow directory." >&2; exit 2; }
SCRIPT="${FLOW_DIR}/skills/close/scripts/close-parent.sh"
FIX="${SCRIPT_DIR}/close-parent/fixtures"
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected to contain [$2], got [$1]"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "$3: expected NOT to contain [$2]"; }

[[ -r "${SCRIPT}" ]] || { fail "script missing: ${SCRIPT}"; echo "close-parent.test.sh: failures=${failures}"; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "close-parent.test.sh: jq required" >&2; exit 2; }
T="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 2; }
trap 'rm -rf "${T}"' EXIT
mkdir -p "${T}/bin" && cp "${FIX}/gh.stub.sh" "${T}/bin/gh" && chmod +x "${T}/bin/gh"

# run_case <name> <parent-fixture> <review> [extra args...] -- fresh state per case.
run_case() {
  local name="$1" fixture="$2" review="$3"; shift 3
  export GH_STUB_LOG="${T}/${name}.log" GH_STUB_STATE_DIR="${T}/${name}.state" GH_STUB_PARENT_FIXTURE="${fixture}"
  : > "${GH_STUB_LOG}"; rm -rf "${GH_STUB_STATE_DIR}"
  PATH="${T}/bin:${PATH}" TMPDIR="${T}/tmp-${name}" bash "${SCRIPT}" --repo acme/widgets --parent 480 --review "${review}" "$@" > "${T}/${name}.out" 2> "${T}/${name}.err"
  echo $?
}
writes_in() { grep -cE '^(issue comment|issue close|issue edit|api repos/[^ ]+ -X POST|api repos/[^ ]+/comments -F)' "$1" || true; }

# --- 1. an open child refuses with zero writes ---------------------------------
rc="$(run_case open "${FIX}/parent-open-child.json" "${FIX}/review-full.md")"
assert_eq "${rc}" "3" "case1 exit"
assert_contains "$(cat "${T}/open.out")" "parent=480 children=2 open=1 outcome=refused-open-children" "case1 outcome line"
assert_contains "$(cat "${T}/open.err")" "open children #482" "case1 names the open child"
assert_eq "$(writes_in "${T}/open.log")" "0" "case1 zero writes"

# --- 2. not a split parent --------------------------------------------------------
rc="$(run_case plain "${FIX}/parent-no-children.json" "${FIX}/review-full.md")"
assert_eq "${rc}" "5" "case2 exit"
assert_eq "$(writes_in "${T}/plain.log")" "0" "case2 zero writes"

# --- 3. gaps: review posted, new children created, parent left open -------------
rc="$(run_case gaps "${FIX}/parent-all-closed.json" "${FIX}/review-gap.md")"
assert_eq "${rc}" "4" "case3 exit"
OUT="$(cat "${T}/gaps.out")"
assert_contains "${OUT}" "covered=1 partial=1 missing=1 unrequested=0 outcome=gaps" "case3 outcome"
assert_contains "${OUT}" "review-comment: 777" "case3 review comment id"
assert_contains "${OUT}" "created: #900 (item 2, missing)" "case3 child for the missing item"
assert_contains "${OUT}" "created: #901 (item 3, partial)" "case3 child for the partial item"
LOG="$(cat "${T}/gaps.log")"
assert_contains "${LOG}" "api repos/acme/widgets/issues/480/comments -F body=@" "case3 comment posted via the comments API"
assert_contains "${LOG}" "api repos/acme/widgets/issues/comments/777 --jq .body" "case3 comment read back by its own id"
assert_contains "${LOG}" "issue edit 900 --repo acme/widgets --parent 480" "case3 first child linked under the same parent"
assert_contains "${LOG}" "issue edit 901 --repo acme/widgets --parent 480" "case3 second child linked under the same parent"
assert_not_contains "${LOG}" "issue close" "case3 parent NOT closed"
COMMENT="$(cat "${T}/gaps.state/comment-777.md")"
assert_contains "${COMMENT}" "> 🤖 **cenci** — split-parent coverage review posted by \`/cenci:close\` (parent closure)." "case3 banner"
assert_contains "${COMMENT}" "| 2 | edge case | An empty list renders the \`No items yet\` placeholder | missing |" "case3 table verbatim"
assert_contains "${COMMENT}" "<!-- cenci-parent-close-review -->" "case3 marker"
assert_contains "${COMMENT}" "1 partial and 1 missing item(s) remain" "case3 comment states the gaps"
P900="$(cat "${T}/gaps.state/payload-900.json")"
assert_eq "$(jq -r '.milestone' <<< "${P900}")" "7" "case3 child inherits the parent's milestone number"
assert_eq "$(jq -c '.labels' <<< "${P900}")" '["area:api"]' "case3 child inherits non-lifecycle labels only (no In Review/Refined)"
assert_contains "$(jq -r '.title' <<< "${P900}")" "Coverage gap: An empty list renders the \`No items yet\` placeholder (parent #480, item 2)" "case3 child title"
assert_contains "$(jq -r '.body' <<< "${P900}")" "Related to #480" "case3 child body backlink"
assert_contains "$(jq -r '.body' <<< "${P900}")" "- [ ] An empty list renders the \`No items yet\` placeholder" "case3 child carries the item verbatim as its criterion"
assert_contains "$(jq -r '.body' <<< "${P900}")" "- Verdict: missing" "case3 child records the verdict"
assert_contains "$(jq -r '.body' <<< "$(cat "${T}/gaps.state/payload-901.json")")" "- Verdict: partial" "case3 partial child records its verdict"
assert_contains "$(cat "${T}/gaps.err")" "#480 left open -- 2 new child(ren) created" "case3 stderr summary"

# --- 4. full coverage closes the parent ---------------------------------------------
rc="$(run_case full "${FIX}/parent-all-closed.json" "${FIX}/review-full.md")"
assert_eq "${rc}" "0" "case4 exit"
OUT="$(cat "${T}/full.out")"
assert_contains "${OUT}" "covered=3 partial=0 missing=0 unrequested=0 outcome=close" "case4 outcome"
assert_contains "${OUT}" "closed: #480" "case4 closed line"
LOG="$(cat "${T}/full.log")"
assert_contains "${LOG}" "issue close 480 --repo acme/widgets --reason completed" "case4 closed as completed"
assert_contains "${LOG}" "issue edit 480 --repo acme/widgets --add-label Implemented --remove-label In Review" "case4 Implemented swapped in"
assert_contains "${LOG}" "issue view 480 --repo acme/widgets --json state --jq .state" "case4 state re-read after closing"
assert_not_contains "${LOG}" "issues -X POST" "case4 no child created"
assert_contains "$(cat "${T}/full.state/comment-777.md")" "Closing the parent." "case4 review comment says closing"
# ordering: the review comment precedes the close
cpos="$(grep -n 'comments -F body=@' "${T}/full.log" | head -1 | cut -d: -f1)"; kpos="$(grep -n '^issue close' "${T}/full.log" | head -1 | cut -d: -f1)"
[[ -n "${cpos}" && -n "${kpos}" && "${cpos}" -lt "${kpos}" ]] || fail "case4 the review comment must be posted before the close (comment@${cpos:-none}, close@${kpos:-none})"

# --- 5. dry run performs zero writes -----------------------------------------------------
rc="$(run_case dry "${FIX}/parent-all-closed.json" "${FIX}/review-gap.md" --dry-run)"
assert_eq "${rc}" "0" "case5 exit"
assert_contains "$(cat "${T}/dry.out")" "outcome=dry-run-gaps" "case5 outcome"
assert_eq "$(writes_in "${T}/dry.log")" "0" "case5 zero writes"

# --- 6. malformed table / failed link fail closed -------------------------------------------
printf '| # | Kind | Item | Status | Proof |\n|---|---|---|---|---|\n| 1 | edge case | x | done | y |\n' > "${T}/bad.md"
rc="$(run_case bad "${FIX}/parent-all-closed.json" "${T}/bad.md")"
assert_eq "${rc}" "2" "case6a malformed table exits 2"
assert_eq "$(writes_in "${T}/bad.log")" "0" "case6a zero writes"
export GH_STUB_FAIL_PATTERN="issue edit 900 --repo acme/widgets --parent 480"
rc="$(run_case linkfail "${FIX}/parent-all-closed.json" "${FIX}/review-gap.md")"
unset GH_STUB_FAIL_PATTERN
assert_eq "${rc}" "2" "case6b failed link exits 2"
assert_contains "$(cat "${T}/linkfail.err")" "linking #900 as a sub-issue of #480 failed" "case6b names the failed link"
assert_contains "$(cat "${T}/linkfail.err")" "gh issue edit 900 --repo acme/widgets --parent 480" "case6b hands the user the manual link command"
assert_not_contains "$(cat "${T}/linkfail.log")" "issue close" "case6b parent left open"

echo "close-parent.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
