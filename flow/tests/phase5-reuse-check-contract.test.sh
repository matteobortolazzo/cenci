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
#   - skills/implement/phases/phase-5-refactor.md   (the check itself)
#   - skills/implement/phases/phase-3-test-red.md   (compact mode's operative list)
#   - skills/implement/phases/phase-6-7-review.md   (report carry-forward)
#   - skills/implement/phases/phase-9-pr.md         (report rendering)
#   - skills/implement/SKILL.md                     (config surface)
#   - skills/implement/codex.md                     (Codex parity)
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
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

# assert_not_contains <content> <forbidden-substring> <label>
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text present: [${pattern}]"
}

# extract_section <content> <exact-heading-line> -- prints the body under that
# heading, up to the next `## ` heading. Pure, safe inside $(...). Assertions
# scoped through this cannot pass on a match that landed somewhere else in the
# file, which is the whole point: a contract about compact mode must be pinned
# to the compact-mode section, not to the document as a whole.
extract_section() {
  local content="$1" heading="$2"
  awk -v h="${heading}" '
    $0 == h { inside = 1; next }
    inside && /^## / { exit }
    inside { print }
  ' <<<"${content}"
}

# extract_line <content> <literal-substring> -- prints the first line containing
# the substring, for assertions that must bind to one specific bullet.
extract_line() {
  local content="$1" needle="$2"
  grep -F -m1 -e "${needle}" <<<"${content}"
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

  # Cost accounting -- two searches per unit (name fragment AND body line),
  # so the real ceiling is 20, not 10. Stating only the unit cap understates
  # the cost the guards are supposed to bound.
  assert_contains "${CONTENT}" 'at most 20 searches for the whole check' "${FILE}"

  # Cost guard 3 -- each probe is scoped to the affected project, not the
  # whole monorepo...
  assert_contains "${CONTENT}" "Restrict the search to the affected project's directory" "${FILE}"

  # ...and that directory must be derivable from what this phase actually
  # holds. Phase 2 resolves `projects[].slug` values, not paths, and its
  # baseline gate is skipped entirely on configs with neither `gateCommand`
  # nor `projects[]` -- so a scope defined as "the path Phase 2 resolved"
  # silently has no value on those configs and degrades to the repo-wide
  # sweep this section forbids. Both the derivation and the no-widening
  # floor have to be stated here.
  assert_contains "${CONTENT}" 'Derive that directory from the changed file list this phase already receives' "${FILE}"
  assert_contains "${CONTENT}" 'the deepest single directory that contains all of them' "${FILE}"
  assert_contains "${CONTENT}" 'Never fall back to the whole tree' "${FILE}"

  # The threshold distinction is the substance of the change: pre-existing
  # duplication keeps the rule of three; duplication this change introduces
  # consolidates at two, because the second occurrence is the one being
  # written right now.
  assert_contains "${CONTENT}" 'Duplicated logic; consolidate only when used 3+ times or clearly established locally.' "${FILE}"
  assert_contains "${CONTENT}" 'even at **two** occurrences' "${FILE}"

  # Bounded by construction: this must never become the refactor skill's
  # repo-wide analyzer sweep running on every ticket.
  assert_contains "${CONTENT}" 'never a repo-wide duplication sweep' "${FILE}"

  # The structural half of that bound: the check rides inside Phase 5's
  # existing implementer delegation and spawns nothing of its own. Asserted
  # as the property (no second delegation inside the section) rather than as
  # a forbidden mention of `duplication-analyzer` -- prose that names the
  # analyzer to draw the boundary *strengthens* this contract, and a
  # forbidden-substring check would fail such an edit while still passing a
  # section that actually fanned an agent out under a different name.
  REUSE_SECTION="$(extract_section "${CONTENT}" '## Reuse Check')"
  assert_contains "${REUSE_SECTION}" 'inside the same delegation' "${FILE} (## Reuse Check)"
  assert_not_contains "${REUSE_SECTION}" 'Delegate to the' "${FILE} (## Reuse Check)"

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

  # Naming Phase 9 is not the same as reaching it: Phase 5's summary is
  # conversation state, and Phase 9 may assemble the PR body in a compacted
  # or fresh session. The report needs a stable prefix so Phase 6 + 7 can
  # persist it verbatim into the run artifact Phase 9 reads.
  assert_contains "${CONTENT}" 'the literal prefix `Reuse Check:`' "${FILE}"
  assert_contains "${CONTENT}" '$RUN_DIR/reuse-notes.txt' "${FILE}"

  # The full-suite/lint verification must cover consolidations made by the
  # Reuse Check, which reads AFTER that paragraph in the file -- so the
  # ordering has to be stated, not left to reading order.
  assert_contains "${CONTENT}" 'Run the full test suite once all cleanup is done — including the `## Reuse Check` below' "${FILE}"
fi

# =====================================================================
# phase-3-test-red.md -- compact implementation mode folds Phases 3-5 into
# a single delegation, and THIS is the file the orchestrator reads when it
# does: its `## Compact Implementation` numbered list is what gets handed to
# the implementer. A carve-out written only in phase-5-refactor.md is
# unreachable on this path (SKILL.md: "Read only the file for the phase you
# are starting"), so the pointer has to live in the list itself.
# =====================================================================
FILE="skills/implement/phases/phase-3-test-red.md"
if require_doc CONTENT "${FILE}"; then
  COMPACT_SECTION="$(extract_section "${CONTENT}" '## Compact Implementation')"

  # The section is the one that governs compact mode (guards against the
  # heading being renamed out from under this assertion).
  assert_contains "${COMPACT_SECTION}" 'cenci.compactImplementation' "${FILE} (## Compact Implementation)"

  # The operative list itself carries the check -- not a mention elsewhere
  # in the file.
  assert_contains "${COMPACT_SECTION}" '## Reuse Check' "${FILE} (## Compact Implementation)"

  # ...and points at the single definition rather than restating it, so the
  # steps and cost guards cannot drift between the two paths.
  assert_contains "${COMPACT_SECTION}" 'phases/phase-5-refactor.md' "${FILE} (## Compact Implementation)"
fi

# =====================================================================
# SKILL.md -- the config-surface statement of the same rule, which is where
# a reader configuring `compactImplementation` learns the fold does not drop
# the check. Bound to that bullet specifically: a bare whole-file match would
# pass on the phrase landing anywhere in ~400 lines.
# =====================================================================
FILE="skills/implement/SKILL.md"
if require_doc CONTENT "${FILE}"; then
  COMPACT_BULLET="$(extract_line "${CONTENT}" '`cenci.compactImplementation: true`')"
  assert_contains "${COMPACT_BULLET}" "Phase 5's Reuse Check" "${FILE} (compactImplementation bullet)"
fi

# =====================================================================
# phase-6-7-review.md / phase-9-pr.md -- the report's carry-forward path.
# Phase 5 names Phase 9's `### Considered and discarded` as the destination,
# but Phase 9 is fed from the reviewers' findings and had no instruction to
# collect a Phase 5 line; Phase 5 also writes no run artifact, and Phase 9
# may run in a compacted or fresh session. Without a persisted hand-off the
# report is silently lost, and nothing would fail.
# =====================================================================
FILE="skills/implement/phases/phase-6-7-review.md"
if require_doc CONTENT "${FILE}"; then
  # Persisted where RUN_DIR first exists, once, alongside the other
  # artifacts -- not re-appended by each fix-and-rerun cycle.
  assert_contains "${CONTENT}" '$RUN_DIR/reuse-notes.txt' "${FILE}"
  assert_contains "${CONTENT}" "Carry Phase 5's Reuse Check report forward" "${FILE}"
fi

FILE="skills/implement/phases/phase-9-pr.md"
if require_doc CONTENT "${FILE}"; then
  # Phase 9 must actually read it, and must fail honestly rather than
  # silently omitting the entry when RUN_DIR is gone -- the same rule the
  # Review/Security/Maintenance lines already follow.
  assert_contains "${CONTENT}" '$RUN_DIR/reuse-notes.txt' "${FILE}"
  assert_contains "${CONTENT}" 'Reuse Check report unavailable (RUN_DIR lost)' "${FILE}"
fi

# =====================================================================
# codex.md -- behavioral parity. The Codex companion carries no refactor
# checklist, so the Reuse Check needs its own one-line statement there or
# Codex runs a materially weaker Phase 5.
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" 'reuse it rather than re-implementing it' "${FILE}"

  # Parity is not just the check existing: a consolidation is a
  # behavior-affecting edit, so Codex must re-verify before the reviews the
  # same way Phase 5 does, and must apply the same never-tracked policy to
  # the report. Omitting either lets Codex consolidate and walk straight
  # into review with no re-run, or mint a Followup per unreusable helper.
  assert_contains "${CONTENT}" 'same full-suite-and-lint run' "${FILE}"
  assert_contains "${CONTENT}" 'never tracked or turned into a Followup ticket' "${FILE}"
fi

echo "phase5-reuse-check-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
