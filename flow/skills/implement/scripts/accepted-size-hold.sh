#!/usr/bin/env bash
# Revoke the live source-issue automerge grant for a root-validated Size
# Acceptance. Call on every publish attempt and immediately before arming.
# Usage: accepted-size-hold.sh <owner/repo> <source-ticket> [--pr <number>]
# Exit 0 only after a live read verifies absence; every error exits 2.
set -euo pipefail

die() { echo "accepted-size-hold.sh: $*" >&2; exit 2; }
REPO="${1:-}"; TICKET="${2:-}"; PR=""
[[ $# -ge 2 ]] || die 'expected repository and source ticket'
shift 2
[[ "${REPO}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ && "${REPO}" != *..* ]] || die 'invalid repository'
[[ "${TICKET}" =~ ^[1-9][0-9]*$ ]] || die 'invalid source ticket'
if [[ $# -gt 0 ]]; then
  [[ $# -eq 2 && "$1" == --pr && "$2" =~ ^[1-9][0-9]*$ ]] || die 'invalid PR argument'
  PR="$2"
fi
command -v jq >/dev/null 2>&1 || die 'jq is required'

read_issue() {
  local response
  response="$(gh issue view "${TICKET}" --repo "${REPO}" --json id,number,labels)" || die 'source issue labels unreadable; hold not verified'
  jq -ce --argjson ticket "${TICKET}" '
    if type == "object" and .number == $ticket and (.id | type == "string" and length > 0)
       and (.labels | type == "array")
       and all(.labels[]; type == "object" and (.name | type == "string"))
    then {id, labels: [.labels[].name]} else error("invalid source issue labels") end
  ' <<< "${response}" || die 'invalid source issue labels; hold not verified'
}

ISSUE="$(read_issue)" || exit 2
SOURCE_ID="$(jq -r '.id' <<< "${ISSUE}")" || die 'source issue identity unreadable'
if jq -e '.labels | index("automerge:ok") != null' <<< "${ISSUE}" >/dev/null; then
  # Narrow removal preserves all other labels, including concurrent additions.
  gh issue edit "${TICKET}" --repo "${REPO}" --remove-label 'automerge:ok' >/dev/null || die 'grant revocation failed; hold not verified'
fi
# Always re-fetch, even when the initial read already lacked the grant.
ISSUE="$(read_issue)" || exit 2
jq -e --arg id "${SOURCE_ID}" '.id == $id and (.labels | index("automerge:ok") == null)' <<< "${ISSUE}" >/dev/null || die 'source issue identity changed or still grants automerge; hold not verified'

if [[ -n "${PR}" ]]; then
  # Babysit consumes every closing issue's live labels, not PR labels. The
  # revoked source must actually participate in that merge-time condition.
  REFERENCES="$(gh pr view "${PR}" --repo "${REPO}" --json closingIssuesReferences)" || die 'PR closing issues unreadable; hold not verified'
  jq -e --argjson ticket "${TICKET}" --arg id "${SOURCE_ID}" '
    type == "object" and (.closingIssuesReferences | type == "array")
    and any(.closingIssuesReferences[]; .number == $ticket and .id == $id)
  ' <<< "${REFERENCES}" >/dev/null || die 'PR does not close the revoked source issue; hold not verified'
fi
printf 'hold=verified source=%s#%s grant=absent\n' "${REPO}" "${TICKET}"
