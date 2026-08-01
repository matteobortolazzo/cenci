#!/usr/bin/env bash
# Recording `gh` stub for flow/tests/refine-write-order.test.sh (ticket
# #878). Logs every invocation's argv (space-joined, one per line, WITHOUT
# the leading "gh" token itself) to $GH_STUB_LOG, then either fails (if
# GH_STUB_FAIL_PATTERN is set and the invocation contains it) or succeeds
# with a canned response depending on the subcommand:
#
#   issue view ... --json ...   -> cat $GH_STUB_VIEW_FIXTURE (or "{}" if unset/unreadable)
#   ... --jq .number ...        -> a fake, incrementing new-issue number
#   anything else               -> empty success (exit 0, no output)
#
# Never committed with an executable bit — flow/tests/refine-write-order/
# lib.sh's replay_through_stub copies this file into a fresh `mktemp -d`/
# bin/gh and chmod +x's THAT COPY at runtime (permission-adding only).
set -uo pipefail

INVOCATION="$*"

if [[ -n "${GH_STUB_LOG:-}" ]]; then
  printf '%s\n' "${INVOCATION}" >> "${GH_STUB_LOG}"
fi

if [[ -n "${GH_STUB_FAIL_PATTERN:-}" ]]; then
  case "${INVOCATION}" in
    *"${GH_STUB_FAIL_PATTERN}"*)
      echo "gh.stub.sh: simulated failure for: ${INVOCATION}" >&2
      exit 1
      ;;
  esac
fi

case "${INVOCATION}" in
  "issue view"*)
    if [[ -n "${GH_STUB_VIEW_FIXTURE:-}" && -r "${GH_STUB_VIEW_FIXTURE}" ]]; then
      cat "${GH_STUB_VIEW_FIXTURE}"
    else
      echo '{}'
    fi
    ;;
  *"--jq .number"*)
    COUNTER_FILE="${GH_STUB_LOG:-/dev/null}.counter"
    raw_n=""
    if [[ -f "${COUNTER_FILE}" ]]; then
      raw_n="$(cat "${COUNTER_FILE}" 2>/dev/null)" || raw_n=""
    fi
    if [[ -z "${raw_n}" ]]; then
      n=0
    elif [[ "${raw_n}" =~ ^[0-9]+$ ]]; then
      n="${raw_n}"
    else
      echo "gh.stub.sh: counter file ${COUNTER_FILE} contains non-numeric content: ${raw_n}" >&2
      exit 1
    fi
    n=$((n + 1))
    echo "${n}" > "${COUNTER_FILE}"
    echo "$((900 + n))"
    ;;
  *)
    : # generic mutation success: exit 0, no stdout
    ;;
esac

exit 0
