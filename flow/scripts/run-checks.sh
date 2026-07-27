#!/usr/bin/env bash
# run-checks.sh — shared, cwd-independent executor for flow's JSON validation
# plus *.test.sh discovery/execution (ticket #720). Both flow-ci.yml's
# flow-test job and .cenci/config.json's flow gateCommand invoke this single
# script, so "which tests run for flow" is written down in exactly one place
# instead of drifting across CI, the gate, and the maintain checker.
#
# Usage: run-checks.sh [flow-root]
#   flow-root defaults to the parent directory of this script (i.e. flow/,
#   when this script lives at flow/scripts/run-checks.sh). The optional
#   override argument exists ONLY for flow/scripts/run-checks.test.sh, which
#   must never invoke this script bare against the real flow tree -- see the
#   RECURSION WARNING at the top of that file. It always passes an explicit
#   mktemp -d fixture root instead.
#
# Behavior, in order:
#   1. JSON validation across the flow tree -- fails (non-zero exit, no
#      suite header printed) before any suite runs. Only regular files are
#      matched (find -type f), so a *.json symlink is not dereferenced.
#   2. Discovery of every *.test.sh regular file (find -type f, so a
#      *.test.sh symlink pointing at an arbitrary script is never
#      discovered/executed) under the flow tree, in deterministic order
#      (sort -z), materialized into an array before executing anything.
#   3. Aggregate execution: every discovered, non-excluded suite runs; the
#      script never stops at the first failure. Before executing a suite,
#      it must be readable and non-empty ([[ -r && -s ]], mirroring
#      check_structural_tests) -- an unreadable or empty suite is counted
#      as a failed suite instead of silently passing as `bash <empty>`
#      would. A "=== <relative path> ===" header delimits each suite's
#      output. The run ends with a summary line and exits non-zero if any
#      suite failed, or if zero suites actually ran (false-green guard per
#      docs/health-gates.md's discovery-loop rule -- covers both "nothing
#      discovered" and "everything excluded").
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || {
  echo "run-checks.sh: failed to resolve script directory." >&2
  exit 2
}

RAW_FLOW_ROOT="${1:-}"
if [[ -z "${RAW_FLOW_ROOT}" ]]; then
  RAW_FLOW_ROOT="${SCRIPT_DIR}/.."
fi
FLOW_ROOT="$(cd "${RAW_FLOW_ROOT}" && pwd)" || {
  echo "run-checks.sh: failed to resolve flow root: ${RAW_FLOW_ROOT}" >&2
  exit 2
}

if ! command -v jq >/dev/null 2>&1; then
  echo "run-checks.sh: jq is required but was not found on PATH." >&2
  exit 2
fi

# --- 1. JSON validation ------------------------------------------------------
# Must fail before any suite header is printed (run-checks.test.sh case 4).
if ! find "${FLOW_ROOT}" -type f -name '*.json' -print0 | xargs -0 -r -n1 jq empty; then
  echo "run-checks.sh: JSON validation failed under ${FLOW_ROOT}." >&2
  exit 1
fi

# --- 2. Discovery -------------------------------------------------------------
# Discover into a real temp file with a checked status -- not process
# substitution, per AGENTS.md's unchecked-command-substitution rule and the
# flow-ci.yml:83-84 / root-safe-perms-contract.test.sh:216-226 precedent.
LIST="$(mktemp)" || {
  echo "run-checks.sh: failed to create discovery list temp file." >&2
  exit 2
}
trap 'rm -f "${LIST}"' EXIT

if ! find "${FLOW_ROOT}" -type f -name '*.test.sh' -print0 | sort -z > "${LIST}"; then
  echo "run-checks.sh: suite discovery failed under ${FLOW_ROOT}." >&2
  exit 1
fi

[[ -r "${LIST}" ]] || {
  echo "run-checks.sh: discovery list is not readable: ${LIST}" >&2
  exit 1
}

# Materialize into an array before executing anything.
SUITES=()
while IFS= read -r -d '' f; do
  SUITES+=("${f}")
done < "${LIST}" || {
  echo "run-checks.sh: failed to read discovery list: ${LIST}" >&2
  exit 1
}

# --- Exclude allowlist --------------------------------------------------------
# Stays empty. Add an entry ONLY for a suite that is environment-dependent
# (e.g. requires a container runtime unavailable here) or prohibitively slow
# for a fast local/CI gate -- mirroring the sandbox gate's tests/smoke.test.sh
# carve-out (that suite triggers a full container image build). Every future
# entry needs its own one-line rationale comment; excluded paths are reported
# as skipped below, never silently dropped.
EXCLUDE=()

is_excluded() {
  local rel="$1" x
  for x in "${EXCLUDE[@]:-}"; do
    [[ -n "${x}" && "${rel}" == "${x}" ]] && return 0
  done
  return 1
}

# --- 3. Aggregate execution ----------------------------------------------------
run=0
failed=0
skipped=0
FAILED_SUITES=()

for f in "${SUITES[@]:-}"; do
  [[ -n "${f}" ]] || continue
  rel="${f#"${FLOW_ROOT}"/}"
  if is_excluded "${rel}"; then
    echo "=== ${rel} === (skipped: excluded)"
    skipped=$((skipped + 1))
    continue
  fi
  echo "=== ${rel} ==="
  if [[ ! -r "${f}" ]]; then
    echo "run-checks.sh: suite is not readable: ${rel}" >&2
    run=$((run + 1))
    failed=$((failed + 1))
    FAILED_SUITES+=("${rel}")
    continue
  fi
  if [[ ! -s "${f}" ]]; then
    echo "run-checks.sh: suite is empty: ${rel}" >&2
    run=$((run + 1))
    failed=$((failed + 1))
    FAILED_SUITES+=("${rel}")
    continue
  fi
  if bash "${f}" </dev/null; then
    run=$((run + 1))
  else
    run=$((run + 1))
    failed=$((failed + 1))
    FAILED_SUITES+=("${rel}")
  fi
done

if [[ "${run}" -eq 0 ]]; then
  echo "run-checks.sh: zero suites executed (false-green guard) -- discovered=${#SUITES[@]} skipped=${skipped}" >&2
  exit 1
fi

echo "summary: suites run=${run} failed=${failed} skipped=${skipped}"

if [[ "${failed}" -gt 0 ]]; then
  echo "failing suites:"
  for f in "${FAILED_SUITES[@]}"; do
    echo "  ${f}"
  done
  exit 1
fi

exit 0
