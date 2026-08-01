#!/usr/bin/env bash
# Recording `gh` stub for flow/tests/parent-close-reconcile.test.sh (ticket
# #879). Logs every invocation's argv (space-joined, one per line, WITHOUT
# the leading "gh" token itself) to $GH_STUB_LOG, then either fails (if
# GH_STUB_FAIL_PATTERN is set and the invocation contains it) or succeeds
# with a canned response depending on the subcommand:
#
#   issue view ...   -> cat $GH_STUB_ISSUE_VIEW_FIXTURE (or "{}" if unset/unreadable)
#   pr view ...       -> cat $GH_STUB_PR_VIEW_FIXTURE (or "{}" if unset/unreadable)
#   pr create ...     -> a fake PR URL
#   pr edit ...       -> empty success (exit 0, no output)
#   issue comment ...  -> empty success
#   anything else      -> empty success (exit 0, no output)
#
# GH_STUB_NO_PR_YET=1 -- narrower than GH_STUB_FAIL_PATTERN: when set, any
# `pr view ...` invocation fails with literal "no pull requests found for
# branch ..." on stderr (case-insensitive substring phase-9-pr.md's
# Merged-PR Guard matches to treat a first-time entry -- no PR created yet --
# as ordinary pass-through, never an ambiguous/merged stop). Every other
# subcommand is unaffected.
#
# Never committed with an executable bit -- flow/tests/parent-close-reconcile/
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

if [[ -n "${GH_STUB_NO_PR_YET:-}" ]]; then
  case "${INVOCATION}" in
    "pr view"*)
      echo "no pull requests found for branch \"${GH_STUB_NO_PR_YET_BRANCH:-feature/800-demo}\"" >&2
      exit 1
      ;;
  esac
fi

case "${INVOCATION}" in
  "issue view"*)
    if [[ -n "${GH_STUB_ISSUE_VIEW_FIXTURE:-}" && -r "${GH_STUB_ISSUE_VIEW_FIXTURE}" ]]; then
      cat "${GH_STUB_ISSUE_VIEW_FIXTURE}"
    else
      echo '{}'
    fi
    ;;
  "pr view"*)
    if [[ -n "${GH_STUB_PR_VIEW_FIXTURE:-}" && -r "${GH_STUB_PR_VIEW_FIXTURE}" ]]; then
      cat "${GH_STUB_PR_VIEW_FIXTURE}"
    else
      echo '{}'
    fi
    ;;
  "pr create"*)
    echo "https://github.com/acme/widgets/pull/999"
    ;;
  *)
    : # generic mutation success: exit 0, no stdout (pr edit, issue comment, issue edit, ...)
    ;;
esac

exit 0
