#!/usr/bin/env bash
# Recording `git` stub for flow/tests/parent-close-reconcile.test.sh (ticket
# #879). Logs every invocation's argv (space-joined, one per line, WITHOUT
# the leading "git" token itself) to $GIT_STUB_LOG, then either fails (if
# GIT_STUB_FAIL_PATTERN is set and the invocation contains it) or succeeds
# with a canned response depending on the subcommand. Unlike gh.stub.sh's
# canned responses (served from committed *.json fixture files), git's
# canned text is passed directly via env vars -- this suite has no *.json
# fixtures for git-log-range/commit-message text (only gh JSON is in
# flow/tests/parent-close-reconcile/fixtures/*.json, per the plan's Files to
# Create list), so the caller sets these before invoking replay_through_stub:
#
#   GIT_STUB_LOG_RANGE_OUTPUT   -> stdout for `git log origin/main..HEAD --format=%H%x00%B`
#   GIT_STUB_HEAD_MESSAGE       -> stdout for `git log -1 --format=%B`
#   GIT_STUB_FETCH_HEAD_MESSAGE -> stdout for `git log -1 FETCH_HEAD --format=%B` (falls back to GIT_STUB_HEAD_MESSAGE)
#   GIT_STUB_HEAD_SHA           -> stdout for `git rev-parse HEAD`
#   GIT_STUB_TREE_SHA           -> stdout for `git rev-parse HEAD^{tree}`
#
# Anything else (commit --amend, push, push --force-with-lease, fetch, add,
# rebase, ...) is a generic mutation/no-op success: exit 0, no stdout.
#
# Never committed with an executable bit -- flow/tests/parent-close-reconcile/
# lib.sh's replay_through_stub copies this file into a fresh `mktemp -d`/
# bin/git and chmod +x's THAT COPY at runtime (permission-adding only).
set -uo pipefail

INVOCATION="$*"

if [[ -n "${GIT_STUB_LOG:-}" ]]; then
  printf '%s\n' "${INVOCATION}" >> "${GIT_STUB_LOG}"
fi

if [[ -n "${GIT_STUB_FAIL_PATTERN:-}" ]]; then
  case "${INVOCATION}" in
    *"${GIT_STUB_FAIL_PATTERN}"*)
      echo "git.stub.sh: simulated failure for: ${INVOCATION}" >&2
      exit 1
      ;;
  esac
fi

case "${INVOCATION}" in
  *"log origin/main..HEAD --format="*)
    printf '%s' "${GIT_STUB_LOG_RANGE_OUTPUT:-}"
    ;;
  *"log -1 FETCH_HEAD --format=%B"*)
    printf '%s' "${GIT_STUB_FETCH_HEAD_MESSAGE:-${GIT_STUB_HEAD_MESSAGE:-}}"
    ;;
  *"log -1 --format=%B"*)
    printf '%s' "${GIT_STUB_HEAD_MESSAGE:-}"
    ;;
  *"rev-parse HEAD^{tree}"*)
    printf '%s' "${GIT_STUB_TREE_SHA:-tree0000000000000000000000000000000000}"
    ;;
  *"rev-parse HEAD"*)
    printf '%s' "${GIT_STUB_HEAD_SHA:-abc0000000000000000000000000000000000}"
    ;;
  *)
    : # generic mutation/no-op success: exit 0, no stdout (commit --amend, push, fetch, add, ...)
    ;;
esac

exit 0
