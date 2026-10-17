#!/usr/bin/env bash
# Recording `gh` stub for flow/tests/close-parent.test.sh. Logs every
# invocation's argv (space-joined, one per line, without the leading "gh") to
# $GH_STUB_LOG, then answers from fixtures plus a tiny amount of state kept
# under $GH_STUB_STATE_DIR so the script's own read-back verifications see
# the effect of its writes:
#
#   issue view <parent> ... --json subIssues --jq '.subIssues.nodes[].number'
#       -> the fixture's sub-issue numbers plus every number linked so far
#   issue view <parent> ... --json state --jq .state
#       -> CLOSED once `issue close` ran, else the fixture's state
#   issue view <parent> ...                      -> cat $GH_STUB_PARENT_FIXTURE
#   api repos/<r>/issues/<n>/comments -F body=@<file> --jq .id
#       -> prints 777, copies <file> to $GH_STUB_STATE_DIR/comment-777.md
#   api repos/<r>/issues/comments/777 --jq .body  -> cat that copy
#   api repos/<r>/issues -X POST --input <payload> --jq .number
#       -> prints 900, 901, ...; copies the payload to $GH_STUB_STATE_DIR/payload-<n>.json
#   issue edit <n> --repo <r> --parent <p>        -> records <n> as linked
#   issue close ...                               -> records closed
#   anything else                                 -> exit 0, no output
#
# GH_STUB_FAIL_PATTERN: an invocation containing it fails (exit 1).
# Never committed with an executable bit -- the suite copies this file into a
# fresh mktemp -d/bin/gh and chmod +x's that copy.
set -uo pipefail
INVOCATION="$*"
[[ -n "${GH_STUB_LOG:-}" ]] && printf '%s\n' "${INVOCATION}" >> "${GH_STUB_LOG}"
STATE="${GH_STUB_STATE_DIR:?GH_STUB_STATE_DIR is required}"
mkdir -p "${STATE}"
if [[ -n "${GH_STUB_FAIL_PATTERN:-}" ]]; then
  case "${INVOCATION}" in
    *"${GH_STUB_FAIL_PATTERN}"*) echo "gh.stub.sh: simulated failure for: ${INVOCATION}" >&2; exit 1 ;;
  esac
fi
case "${INVOCATION}" in
  "issue view"*"--json subIssues --jq .subIssues.nodes[].number"*)
    jq -r '.subIssues.nodes[].number' "${GH_STUB_PARENT_FIXTURE}"
    [[ -f "${STATE}/linked" ]] && cat "${STATE}/linked"
    ;;
  "issue view"*"--json state --jq .state"*)
    if [[ -f "${STATE}/closed" ]]; then echo CLOSED; else jq -r '.state' "${GH_STUB_PARENT_FIXTURE}"; fi
    ;;
  "issue view"*)
    cat "${GH_STUB_PARENT_FIXTURE}"
    ;;
  "api repos/"*"/comments -F body=@"*"--jq .id"*)
    file="${INVOCATION#*body=@}"; file="${file%% *}"
    cp "${file}" "${STATE}/comment-777.md"
    echo 777
    ;;
  "api repos/"*"/issues/comments/777 --jq .body"*)
    cat "${STATE}/comment-777.md"
    ;;
  "api repos/"*"/issues -X POST --input "*"--jq .number"*)
    file="${INVOCATION#*--input }"; file="${file%% *}"
    n=900
    [[ -f "${STATE}/next" ]] && n="$(cat "${STATE}/next")"
    cp "${file}" "${STATE}/payload-${n}.json"
    echo $((n + 1)) > "${STATE}/next"
    echo "${n}"
    ;;
  "issue edit "*" --parent "*)
    set -- ${INVOCATION}
    echo "$3" >> "${STATE}/linked"
    ;;
  "issue close"*)
    : > "${STATE}/closed"
    ;;
  *) : ;;
esac
exit 0
