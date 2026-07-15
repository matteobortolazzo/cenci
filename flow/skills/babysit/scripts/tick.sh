#!/usr/bin/env bash
# One-tick data-gathering step for the babysit skill.
#
# Replaces 4 in-model `gh` calls (pr view, pr checks, reviews, comments) plus
# a GraphQL resolved-thread lookup with a single deterministic script. Emits
# one compact JSON verdict on stdout. Implements only the *mechanical* subset
# of pr-comment-filter's Exclude list (bot logins, resolved threads,
# outdated) — semantic judgment (requesting-changes vs. informational,
# inline-suggestion quality) and the watermark (lastCommentTimestamp /
# addressedCommentIds) stay in the calling model. See
# ../../pr-comment-filter/SKILL.md for the single source of truth on the
# filter itself; this script must be kept in sync with it, never the reverse.
#
# Usage: tick.sh <owner> <repo> <pr> <state-file>
#
# Exit codes:
#   0  success — JSON verdict on stdout
#   1  bad usage
#   3  jq is not on PATH (required)
#   *  a `gh` call failed (set -e propagates it) — no valid JSON on stdout
#
# Fixtures (for tick.test.sh — when set, read the named file instead of
# shelling out to `gh`):
#   TICK_SH_FIXTURE_PR_VIEW    -> `gh pr view ... --json ...`
#   TICK_SH_FIXTURE_PR_CHECKS  -> `gh pr checks ... --json ...`
#   TICK_SH_FIXTURE_REVIEWS    -> `gh api .../pulls/<pr>/reviews`
#   TICK_SH_FIXTURE_COMMENTS   -> `gh api .../pulls/<pr>/comments`
#   TICK_SH_FIXTURE_GRAPHQL    -> `gh api graphql` (reviewThreads query)
set -euo pipefail

usage() {
    echo "Usage: tick.sh <owner> <repo> <pr> <state-file>" >&2
}

if [[ $# -lt 4 ]]; then
    usage
    exit 1
fi

OWNER="$1"
REPO="$2"
PR="$3"
STATE_FILE="$4"

if ! command -v jq &>/dev/null; then
    echo "WARN: tick.sh requires jq on PATH — falling back to in-model gh calls" >&2
    exit 3
fi

WARNINGS=()

# A missing state file is the normal arming-tick case, not a warning. An
# unreadable/invalid one (should have been caught by the caller's own Read
# in step 3) is worth surfacing.
if [[ -f "${STATE_FILE}" ]] && ! jq empty "${STATE_FILE}" >/dev/null 2>&1; then
    WARNINGS+=("state file at ${STATE_FILE} exists but is not valid JSON")
fi

fetch_pr_view() {
    if [[ -n "${TICK_SH_FIXTURE_PR_VIEW:-}" ]]; then
        cat "${TICK_SH_FIXTURE_PR_VIEW}"
    else
        gh pr view "${PR}" --repo "${OWNER}/${REPO}" \
            --json number,title,state,headRefName,headRefOid,mergedAt,closingIssuesReferences
    fi
}

fetch_pr_checks() {
    if [[ -n "${TICK_SH_FIXTURE_PR_CHECKS:-}" ]]; then
        cat "${TICK_SH_FIXTURE_PR_CHECKS}"
    else
        gh pr checks "${PR}" --repo "${OWNER}/${REPO}" --json bucket,name,state
    fi
}

fetch_reviews() {
    if [[ -n "${TICK_SH_FIXTURE_REVIEWS:-}" ]]; then
        cat "${TICK_SH_FIXTURE_REVIEWS}"
    else
        gh api "repos/${OWNER}/${REPO}/pulls/${PR}/reviews"
    fi
}

fetch_comments() {
    if [[ -n "${TICK_SH_FIXTURE_COMMENTS:-}" ]]; then
        cat "${TICK_SH_FIXTURE_COMMENTS}"
    else
        gh api "repos/${OWNER}/${REPO}/pulls/${PR}/comments"
    fi
}

fetch_graphql() {
    if [[ -n "${TICK_SH_FIXTURE_GRAPHQL:-}" ]]; then
        cat "${TICK_SH_FIXTURE_GRAPHQL}"
    else
        gh api graphql -f query='
query($owner: String!, $repo: String!, $pr: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        pageInfo { hasNextPage }
        nodes {
          isResolved
          comments(first: 100) { nodes { databaseId } }
        }
      }
    }
  }
}' -f owner="${OWNER}" -f repo="${REPO}" -F pr="${PR}"
    fi
}

PR_VIEW_JSON="$(fetch_pr_view)"
CHECKS_JSON="$(fetch_pr_checks)"
REVIEWS_JSON="$(fetch_reviews)"
COMMENTS_JSON="$(fetch_comments)"
GRAPHQL_JSON="$(fetch_graphql)"

RESOLVED_IDS="$(echo "${GRAPHQL_JSON}" | jq '[.data.repository.pullRequest.reviewThreads.nodes[]? | select(.isResolved == true) | .comments.nodes[].databaseId]')"
HAS_NEXT_PAGE="$(echo "${GRAPHQL_JSON}" | jq -r '.data.repository.pullRequest.reviewThreads.pageInfo.hasNextPage // false')"

if [[ "${HAS_NEXT_PAGE}" == "true" ]]; then
    WARNINGS+=("reviewThreads pagination: more resolved-thread data exists than fetched (hasNextPage=true) — redo comment fetch+filter in-model for this tick")
fi

BOT_TEST='(.user.login | test("\\[bot\\]$"))'

BOT_COMMENT_COUNT="$(echo "${COMMENTS_JSON}" | jq "[.[] | select(${BOT_TEST})] | length")"
BOT_REVIEW_COUNT="$(echo "${REVIEWS_JSON}" | jq "[.[] | select(${BOT_TEST})] | length")"
BOT_COUNT=$((BOT_COMMENT_COUNT + BOT_REVIEW_COUNT))

RESOLVED_COUNT="$(echo "${COMMENTS_JSON}" | jq --argjson resolved "${RESOLVED_IDS}" \
    '[.[] | select(.id as $id | $resolved | index($id))] | length')"

OUTDATED_COUNT="$(echo "${COMMENTS_JSON}" | jq '[.[] | select(.position == null)] | length')"

INLINE_CANDIDATES="$(echo "${COMMENTS_JSON}" | jq --argjson resolved "${RESOLVED_IDS}" "
  [ .[] | select(
      (${BOT_TEST} | not)
      and (.position != null)
      and ((.id as \$id | \$resolved | index(\$id)) | not)
    ) | . + {kind: \"inline_comment\"}
  ]")"

REVIEW_CANDIDATES="$(echo "${REVIEWS_JSON}" | jq "
  [ .[] | select(${BOT_TEST} | not) | . + {kind: \"review\"} ]")"

CANDIDATE_COMMENTS="$(jq -n --argjson a "${INLINE_CANDIDATES}" --argjson b "${REVIEW_CANDIDATES}" '$a + $b')"

FAILING="$(echo "${CHECKS_JSON}" | jq '[.[] | select(.bucket == "fail") | .name]')"
ALL_PASS="$(echo "${CHECKS_JSON}" | jq '[.[] | select(.bucket != "pass" and .bucket != "skipping" and .bucket != "cancel")] | length == 0')"
ANY_PENDING="$(echo "${CHECKS_JSON}" | jq '[.[] | select(.bucket == "pending")] | length > 0')"

TERMINAL="$(echo "${PR_VIEW_JSON}" | jq '{state, mergedAt, closingIssuesReferences}')"
HEAD_REF_OID="$(echo "${PR_VIEW_JSON}" | jq -r '.headRefOid')"

WARNINGS_JSON='[]'
if [[ ${#WARNINGS[@]} -gt 0 ]]; then
    WARNINGS_JSON="$(printf '%s\n' "${WARNINGS[@]}" | jq -R . | jq -s '.')"
fi

jq -n \
    --argjson terminal "${TERMINAL}" \
    --arg headRefOid "${HEAD_REF_OID}" \
    --argjson failing "${FAILING}" \
    --argjson allPass "${ALL_PASS}" \
    --argjson anyPending "${ANY_PENDING}" \
    --argjson candidateComments "${CANDIDATE_COMMENTS}" \
    --argjson botCount "${BOT_COUNT}" \
    --argjson resolvedThreadCount "${RESOLVED_COUNT}" \
    --argjson outdatedCount "${OUTDATED_COUNT}" \
    --argjson warnings "${WARNINGS_JSON}" \
    '{
      terminal: $terminal,
      headRefOid: $headRefOid,
      checks: { failing: $failing, allPass: $allPass, anyPending: $anyPending },
      candidateComments: $candidateComments,
      excludedCounts: { bot: $botCount, resolvedThread: $resolvedThreadCount, outdated: $outdatedCount },
      warnings: $warnings
    }'
