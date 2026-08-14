#!/usr/bin/env bash
# Contract test for the Phase 5 Reuse Check.
#
# The gap this pins down: Phase 5 reviews "touched code only", so duplication
# the change itself *introduces* against code outside the diff -- a helper,
# constant, or fixture re-implemented when an equivalent already exists
# elsewhere -- reads as clean code from inside the diff and is invisible to
# every phase of the implement pipeline. `/cenci:refactor` catches that class,
# but only when a human runs it, over a whole scope, producing tickets rather
# than fixes.
#
# The fix must not be "add a repo-wide duplication sweep to the pipeline". The
# check is bounded by construction -- it inspects only the named units this
# diff ADDS, no-ops when there are none, caps how many it probes, and scopes
# each probe to the affected project -- so its cost is a handful of greps
# inside a delegation that is already running, not a new agent or a new pass.
# This test pins down BOTH halves: that the check exists, and that its cost
# guards exist. A future edit that drops the guards would turn a cheap check
# into a repo-wide scan on every ticket, which is exactly the regression this
# suite exists to catch.
#
# Follows the fixture-free idiom of tests/subagent-cwd-contract.test.sh: a
# `failures=` counter, small assert_* helpers, self-contained, auto-discovered
# by the flow gate's `*.test.sh` glob. It greps the real committed docs
# directly, since the contract under test lives in those docs' prose.
#
# Covered files (the only docs this test scans):
#   - skills/implement/phases/phase-5-refactor.md
#   - skills/implement/SKILL.md
#   - skills/implement/codex.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "phase5-reuse-check-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "phase5-reuse-check-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # Pure extraction, no fail() side effect -- safe to call inside $(...).
  local _relpath="$1"
  cat "${FLOW_DIR}/${_relpath}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> -- nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing/unreadable file must never silently masquerade as empty content,
# which would make assert_not_contains trivially "pass"). Must NOT be
# invoked via $(...).
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

# assert_contains <content> <required-substring> <label>
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

# assert_not_contains <content> <forbidden-substring> <label>
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text present: [${pattern}]"
}

# =====================================================================
# phase-5-refactor.md -- the Reuse Check itself.
# =====================================================================
FILE="skills/implement/phases/phase-5-refactor.md"
if require_doc CONTENT "${FILE}"; then
  # The check exists as its own named step of the same implementer
  # delegation.
  assert_contains "${CONTENT}" '## Reuse Check' "${FILE}"

  # Scope: only units this diff ADDS. Without this the check degenerates
  # into re-reviewing all touched code, which Phase 5 already does.
  assert_contains "${CONTENT}" 'named units this diff adds' "${FILE}"

  # Cost guard 1 -- no added units means the check costs nothing at all.
  # This is the common case (edits, deletions, config-only diffs).
  assert_contains "${CONTENT}" 'Skip the rest of this check entirely when that list is empty' "${FILE}"

  # Cost guard 2 -- a hard cap on probes, so a large feature diff cannot
  # fan out into an unbounded number of searches.
  assert_contains "${CONTENT}" 'at most the 10 largest added units' "${FILE}"

  # Cost guard 3 -- each probe is scoped to the affected project, not the
  # whole monorepo.
  assert_contains "${CONTENT}" "Restrict the search to the affected project's directory" "${FILE}"

  # The threshold distinction is the substance of the change: pre-existing
  # duplication keeps the rule of three; duplication this change introduces
  # consolidates at two, because the second occurrence is the one being
  # written right now.
  assert_contains "${CONTENT}" 'Duplicated logic; consolidate only when used 3+ times or clearly established locally.' "${FILE}"
  assert_contains "${CONTENT}" 'even at **two** occurrences' "${FILE}"

  # Bounded by construction: this must never become the refactor skill's
  # repo-wide analyzer sweep running on every ticket.
  assert_contains "${CONTENT}" 'never a repo-wide duplication sweep' "${FILE}"
  assert_not_contains "${CONTENT}" 'duplication-analyzer' "${FILE}"

  # An equivalent that cannot be reused without changing behavior for its
  # existing callers is reported, not silently forced -- rewiring other
  # callers is outside the ticket's scope.
  assert_contains "${CONTENT}" 'without changing behavior for its current callers' "${FILE}"

  # ...and that report must land under Phase 9's `### Considered and
  # discarded`, NOT in tracked `## Notes`. phase-9-pr.md makes tracked
  # `## Notes` the sole source of Followup ticket creation, and the Phase
  # 6 + 7 reviewers already rule that refactor/tech-debt observations are
  # never tracked. Routing a near-duplicate into tracked Notes would mint a
  # Followup ticket per unreusable helper -- a backlog leak, and an
  # inconsistency with the reviewers' identical policy.
  assert_contains "${CONTENT}" '### Considered and discarded' "${FILE}"
  assert_contains "${CONTENT}" 'never** tracked or turned into a Followup ticket' "${FILE}"

  # The full-suite/lint verification must cover consolidations made by the
  # Reuse Check, which reads AFTER that paragraph in the file -- so the
  # ordering has to be stated, not left to reading order.
  assert_contains "${CONTENT}" 'Run the full test suite once all cleanup is done — including the `## Reuse Check` below' "${FILE}"
fi

# =====================================================================
# SKILL.md -- compact implementation mode folds Phases 3-5 into a single
# delegation. The Reuse Check must survive that fold rather than being
# silently dropped with the rest of Phase 5; it is cheap enough that
# skipping it saves nothing.
# =====================================================================
FILE="skills/implement/SKILL.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "Phase 5's Reuse Check" "${FILE}"
fi

# =====================================================================
# codex.md -- behavioral parity. The Codex companion carries no refactor
# checklist, so the Reuse Check needs its own one-line statement there or
# Codex runs a materially weaker Phase 5.
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" 'reuse it rather than re-implementing it' "${FILE}"
fi

echo "phase5-reuse-check-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
