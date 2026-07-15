#!/usr/bin/env bash
# Fixture-driven tests for tick.sh. No network required — the
# TICK_SH_FIXTURE_* env vars point tick.sh at static JSON files instead of
# shelling out to `gh`. Follows the self-skipping style of
# sandbox/tests/*.test.sh (the only shell-test precedent in this repo),
# though nothing here needs to self-skip since fixtures never require a
# container runtime or network access.
#
# Optional live path: set RUN_LIVE_TICK_TEST=1 and TICK_SH_LIVE_OWNER/REPO/PR
# to exercise tick.sh against a real, disposable PR over the network.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TICK_SH="${SCRIPT_DIR}/tick.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# assert_jq <label> <json> <jq-filter-that-must-be-true>
assert_jq() {
    local label="$1" json="$2" filter="$3"
    if echo "${json}" | jq -e "${filter}" >/dev/null 2>&1; then
        pass
    else
        fail "${label} (filter: ${filter})"
    fi
}

echo "tick.test.sh"

if ! command -v jq &>/dev/null; then
    echo "SKIP: jq not found on PATH — tick.sh requires it."
    exit 0
fi

FIXTURE_DIR="$(mktemp -d)"
trap 'rm -rf "${FIXTURE_DIR}"' EXIT

# ── Shared fixture: a mix of bot / resolved / outdated / actionable ──
cat >"${FIXTURE_DIR}/pr_view.json" <<'EOF'
{"number":42,"title":"Test PR","state":"OPEN","headRefName":"feature/x","headRefOid":"abc123","mergedAt":null,"closingIssuesReferences":[]}
EOF

cat >"${FIXTURE_DIR}/pr_checks.json" <<'EOF'
[
  {"bucket":"pass","name":"build","state":"SUCCESS"},
  {"bucket":"fail","name":"test","state":"FAILURE"},
  {"bucket":"pending","name":"lint","state":"PENDING"}
]
EOF

cat >"${FIXTURE_DIR}/reviews.json" <<'EOF'
[
  {"id":1,"user":{"login":"reviewer1"},"body":"Please fix the bug","state":"CHANGES_REQUESTED"},
  {"id":2,"user":{"login":"github-actions[bot]"},"body":"Automated check passed","state":"COMMENTED"}
]
EOF

cat >"${FIXTURE_DIR}/comments.json" <<'EOF'
[
  {"id":101,"user":{"login":"dependabot[bot]"},"body":"bump dep","position":3,"path":"go.mod"},
  {"id":102,"user":{"login":"human1"},"body":"resolved suggestion","position":5,"path":"main.go"},
  {"id":103,"user":{"login":"human2"},"body":"outdated comment","position":null,"path":"main.go"},
  {"id":104,"user":{"login":"human3"},"body":"please add error handling here","position":10,"path":"handler.go"}
]
EOF

cat >"${FIXTURE_DIR}/graphql.json" <<'EOF'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
  {"isResolved":true,"comments":{"nodes":[{"databaseId":102}]}},
  {"isResolved":false,"comments":{"nodes":[{"databaseId":104}]}}
]}}}}}
EOF

run_tick() {
    TICK_SH_FIXTURE_PR_VIEW="${FIXTURE_DIR}/pr_view.json" \
    TICK_SH_FIXTURE_PR_CHECKS="${FIXTURE_DIR}/pr_checks.json" \
    TICK_SH_FIXTURE_REVIEWS="${FIXTURE_DIR}/reviews.json" \
    TICK_SH_FIXTURE_COMMENTS="${FIXTURE_DIR}/comments.json" \
    TICK_SH_FIXTURE_GRAPHQL="${FIXTURE_DIR}/graphql.json" \
        "${TICK_SH}" acme widgets 42 "${FIXTURE_DIR}/nonexistent-state.json"
}

echo "case: mixed fixture (bot / resolved / outdated / actionable)"
OUT="$(run_tick)"
EXIT_CODE=$?

if [[ ${EXIT_CODE} -eq 0 ]]; then
    pass
else
    fail "tick.sh exited ${EXIT_CODE}, expected 0"
fi

assert_jq "terminal.state"            "${OUT}" '.terminal.state == "OPEN"'
assert_jq "headRefOid"                "${OUT}" '.headRefOid == "abc123"'
assert_jq "checks.failing"            "${OUT}" '.checks.failing == ["test"]'
assert_jq "checks.allPass is false"   "${OUT}" '.checks.allPass == false'
assert_jq "checks.anyPending is true" "${OUT}" '.checks.anyPending == true'
assert_jq "excludedCounts.bot == 2"             "${OUT}" '.excludedCounts.bot == 2'
assert_jq "excludedCounts.resolvedThread == 1"  "${OUT}" '.excludedCounts.resolvedThread == 1'
assert_jq "excludedCounts.outdated == 1"        "${OUT}" '.excludedCounts.outdated == 1'
assert_jq "candidateComments has 2 entries"     "${OUT}" '(.candidateComments | length) == 2'
assert_jq "candidateComments includes actionable inline comment" "${OUT}" \
    '[.candidateComments[] | select(.id == 104 and .kind == "inline_comment")] | length == 1'
assert_jq "candidateComments includes non-bot review" "${OUT}" \
    '[.candidateComments[] | select(.id == 1 and .kind == "review")] | length == 1'
assert_jq "candidateComments excludes bot comment"    "${OUT}" '[.candidateComments[] | select(.id == 101)] | length == 0'
assert_jq "candidateComments excludes resolved comment" "${OUT}" '[.candidateComments[] | select(.id == 102)] | length == 0'
assert_jq "candidateComments excludes outdated comment"  "${OUT}" '[.candidateComments[] | select(.id == 103)] | length == 0'
assert_jq "candidateComments excludes bot review"     "${OUT}" '[.candidateComments[] | select(.id == 2)] | length == 0'
assert_jq "no warnings for a missing (arming-tick) state file" "${OUT}" '.warnings == []'

# ── Case: pagination guard emits a warning ────────────────────────
echo "case: reviewThreads pagination guard"
cat >"${FIXTURE_DIR}/graphql_paginated.json" <<'EOF'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":true},"nodes":[]}}}}}
EOF
OUT_PAGINATED="$(
    TICK_SH_FIXTURE_PR_VIEW="${FIXTURE_DIR}/pr_view.json" \
    TICK_SH_FIXTURE_PR_CHECKS="${FIXTURE_DIR}/pr_checks.json" \
    TICK_SH_FIXTURE_REVIEWS="${FIXTURE_DIR}/reviews.json" \
    TICK_SH_FIXTURE_COMMENTS="${FIXTURE_DIR}/comments.json" \
    TICK_SH_FIXTURE_GRAPHQL="${FIXTURE_DIR}/graphql_paginated.json" \
        "${TICK_SH}" acme widgets 42 "${FIXTURE_DIR}/nonexistent-state.json"
)"
assert_jq "warns on hasNextPage=true" "${OUT_PAGINATED}" '[.warnings[] | select(contains("pagination"))] | length == 1'

# ── Case: an existing-but-invalid state file produces a warning ───
echo "case: invalid state file warns but does not fail the tick"
echo 'not valid json' >"${FIXTURE_DIR}/bad-state.json"
OUT_BAD_STATE="$(
    TICK_SH_FIXTURE_PR_VIEW="${FIXTURE_DIR}/pr_view.json" \
    TICK_SH_FIXTURE_PR_CHECKS="${FIXTURE_DIR}/pr_checks.json" \
    TICK_SH_FIXTURE_REVIEWS="${FIXTURE_DIR}/reviews.json" \
    TICK_SH_FIXTURE_COMMENTS="${FIXTURE_DIR}/comments.json" \
    TICK_SH_FIXTURE_GRAPHQL="${FIXTURE_DIR}/graphql.json" \
        "${TICK_SH}" acme widgets 42 "${FIXTURE_DIR}/bad-state.json"
)"
assert_jq "warns on invalid state file" "${OUT_BAD_STATE}" '[.warnings[] | select(contains("not valid JSON"))] | length == 1'

# ── Case: bad usage exits 1 ────────────────────────────────────────
echo "case: missing arguments"
if "${TICK_SH}" acme widgets 42 >/dev/null 2>&1; then
    fail "tick.sh should exit non-zero when the state-file argument is missing"
else
    pass
fi

# ── Optional live path ─────────────────────────────────────────────
if [[ "${RUN_LIVE_TICK_TEST:-0}" == "1" ]]; then
    if [[ -z "${TICK_SH_LIVE_OWNER:-}" || -z "${TICK_SH_LIVE_REPO:-}" || -z "${TICK_SH_LIVE_PR:-}" ]]; then
        echo "SKIP: RUN_LIVE_TICK_TEST=1 requires TICK_SH_LIVE_OWNER/REPO/PR"
    elif ! command -v gh &>/dev/null || ! gh auth status &>/dev/null; then
        echo "SKIP: gh not installed or not authenticated"
    else
        echo "case: live tick.sh run against ${TICK_SH_LIVE_OWNER}/${TICK_SH_LIVE_REPO}#${TICK_SH_LIVE_PR}"
        LIVE_STATE="${FIXTURE_DIR}/live-state.json"
        if LIVE_OUT="$("${TICK_SH}" "${TICK_SH_LIVE_OWNER}" "${TICK_SH_LIVE_REPO}" "${TICK_SH_LIVE_PR}" "${LIVE_STATE}")"; then
            assert_jq "live output has a terminal.state" "${LIVE_OUT}" '.terminal.state | type == "string"'
        else
            fail "live tick.sh run failed"
        fi
    fi
fi

# ── Summary ─────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
