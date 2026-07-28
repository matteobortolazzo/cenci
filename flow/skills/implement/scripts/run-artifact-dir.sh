#!/usr/bin/env bash
# Creates and verifies a run-unique artifact directory for Phase 6 + 7 review
# artifacts (ticket #525). `mktemp -d` atomically guarantees a distinct,
# unpredictable location per invocation, so two concurrent runs for the same
# ticket never collide. This script owns only directory creation/verification
# -- it runs no git commands; the caller writes diff/files/stat/review-path
# artifacts into the returned directory itself.
#
# Usage: run-artifact-dir.sh
#
# On success: prints exactly the created directory path to stdout, exits 0.
# On failure (parent dir cannot be created, mktemp fails, or the result is
# not a non-empty existing directory): prints nothing to stdout, an error to
# stderr, exits non-zero. Callers must verify the exit status before using
# the printed path -- never proceed on an unverified/empty path.
set -u

BASE_DIR="${TMPDIR:-/tmp}/cenci"

if ! mkdir -p "${BASE_DIR}" 2>/dev/null; then
  echo "run-artifact-dir.sh: failed to create base directory: ${BASE_DIR}" >&2
  exit 1
fi

RUN_DIR="$(mktemp -d "${BASE_DIR}/cenci-run-XXXXXX" 2>/dev/null)"
STATUS=$?

if [[ "${STATUS}" -ne 0 || -z "${RUN_DIR}" || ! -d "${RUN_DIR}" ]]; then
  echo "run-artifact-dir.sh: failed to create a run-unique artifact directory under ${BASE_DIR}" >&2
  exit 1
fi

printf '%s\n' "${RUN_DIR}"
