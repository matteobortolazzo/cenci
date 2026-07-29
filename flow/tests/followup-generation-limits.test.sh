#!/usr/bin/env bash
# Tests the "tame the followup flood" contract (improvements 1-4):
#   1. Track-by-default is flipped to discard-by-default in both the Claude
#      review phase and the Codex mirror (TRACK_BAR / DISCARD_DEFAULT, x3 each).
#   2. Phase 9 no longer solicits informal out-of-scope observations.
#   3. Generation limit: a Followup ticket's own PR mints no new followup.
#   4. Dedup-before-create + resume-safe append (Phase 9 and address-review).
# Fixture-free, grep-based idiom shared with
# flow/tests/followup-ticket-inheritance.test.sh: `failures` counter,
# `grep -qF`/`grep -cF` helpers, plain `#!/usr/bin/env bash`.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PHASE_67="${FLOW_DIR}/skills/implement/phases/phase-6-7-review.md"
PHASE_9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
ADDRESS_REVIEW_SKILL="${FLOW_DIR}/skills/address-review/SKILL.md"
CODEX_AGENTS="${FLOW_DIR}/templates/agents-md-codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}
assert_absent() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected NOT to contain: $2)"
}
assert_count() {
  # $1=file $2=needle $3=expected-count $4=description
  local got
  got="$(grep -cF -- "$2" "$1")"
  [[ "$got" -eq "$3" ]] || fail "$(basename "$1") $4 (expected ${3} occurrences of '$2', got ${got})"
}

# ---------------------------------------------------------------------
# 1. Discard-by-default flip: both anchors appear on all three tier lines
#    (Medium/Low, Should Fix, Warning) in the Claude phase and the Codex
#    mirror. Byte-identical across files so a drift in either is caught.
# ---------------------------------------------------------------------
TRACK_BAR='**Track** it only when it is an actual defect with a concrete, realistic trigger path a user can hit in real use'
DISCARD_DEFAULT='discard by default — test-coverage gaps, doc polish, "consider X" suggestions, and refactor/tech-debt observations are never tracked'

for FILE in "${PHASE_67}" "${CODEX_AGENTS}"; do
  assert_count "${FILE}" "${TRACK_BAR}" 3 \
    "must state the narrow track bar on all three review-tier lines"
  assert_count "${FILE}" "${DISCARD_DEFAULT}" 3 \
    "must state discard-by-default on all three review-tier lines"
done

# Sanity: the flip must not have clobbered the parity P8 marker.
assert_contains "${PHASE_67}" 'quality gates are mandatory' \
  "must preserve the 'quality gates are mandatory' parity marker"

# ---------------------------------------------------------------------
# 2. Phase 9 no longer harvests informal out-of-scope observations.
# ---------------------------------------------------------------------
assert_absent "${PHASE_9}" 'informal out-of-scope observations' \
  "must not solicit informal out-of-scope observations into followups"

# ---------------------------------------------------------------------
# 3. Generation limit — a Followup ticket's own PR mints no new followup.
# ---------------------------------------------------------------------
assert_contains "${PHASE_9}" 'the ticket being implemented in this run is itself a Followup ticket' \
  "must detect a Followup-origin ticket from the reused meta fetch"
assert_contains "${PHASE_9}" 'not as a graceful degrade: create no new followup ticket, fail-closed' \
  "must fail closed on an unreadable label list"

# ---------------------------------------------------------------------
# 4. Dedup before create + resume-safe append (Phase 9).
# ---------------------------------------------------------------------
assert_contains "${PHASE_9}" 'match on the literal whole back-link line only' \
  "must dedup on the literal back-link, not fuzzy heuristics"
assert_contains "${PHASE_9}" 'never match on title similarity' \
  "must never dedup on title similarity"
assert_contains "${PHASE_9}" 'resume-safe idempotency guard against a Goal Autopilot re-entry double-appending' \
  "must guard the append against a re-entry double-append"

# ---------------------------------------------------------------------
# 4b. address-review parallels the generation limit + resume-safe append.
# ---------------------------------------------------------------------
assert_contains "${ADDRESS_REVIEW_SKILL}" 'the PR'"'"'s originating ticket is itself a Followup ticket' \
  "must detect a Followup-origin PR and append to the original ticket"
assert_contains "${ADDRESS_REVIEW_SKILL}" 'a fetch failure here is a graceful degrade, not a halt, unlike Phase 9' \
  "must graceful-degrade on fetch failure (per-PR search backstops it)"
assert_contains "${ADDRESS_REVIEW_SKILL}" 'to stay resume-safe if this sub-step reruns after a partial failure' \
  "must guard the found-ticket append against a re-run double-append"

echo "followup-generation-limits.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
