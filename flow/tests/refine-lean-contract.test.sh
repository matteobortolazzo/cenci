#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #1273 (1/2 of #1130): the
# lean refinement policy. `refinement.autonomy: "lean"` makes the refiner ask
# only in four classes (security-sensitive, destructive or irreversible, scope
# blowup / split, product ambiguity with no recommendable option), in one round
# of at most 3 questions plus any always-asked required cap-exempt question
# (never a confirm/overrule of an already-settled decision); the refine skill enforces that cap itself; the Confirmation Gate
# lists every auto-adopted item and `decided:` entry as A1...An and accepts one
# freeform overrule; Codex mirrors all of it. Interactive refine shares question selection,
# but keeps its own relay and confirmation rather than lean overflow/overrule.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/lean-planning-contract.test.sh: `set -uo pipefail`, a `failures`
# counter, `assert_file_contains`/`assert_file_lacks` helpers built on
# `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob -- no registration needed. No `read_*` helpers, so this file
# is trivially compliant with flow/tests/read-helper-purity-contract.test.sh's
# repo-wide scan.
#
# Cross-file literals (#979 technique): the cap phrase, the exempt tag, the
# rank string, the overflow sentinel, the over-cap suffix, the `decided:`
# format (also pinned in agents/planner.md), the never-dropped clause and the
# automerge statement must be the IDENTICAL literal string in
# agents/refiner.md, skills/refine/SKILL.md and skills/refine/codex.md, so the
# three restatement sites cannot drift apart (flow/AGENTS.md: restating a rule
# at several sites means updating all of them).
#
# Marker precision (docs/shell-scripting-gotchas.md rule 3): every marker below
# is a full distinctive phrase the production prose will contain verbatim,
# never a bare identifier that already recurs in the unmodified files.
#
# Acceptance-criterion map (each AC has at least one explicit assertion here or
# in refiner-agent-contract.test.sh / refine-confirmation-gate.test.sh):
#   AC1  config + bundle flag            -> block (a)
#   AC2  zero questions                  -> block (c) case 1 (+ refiner zero-question sentence)
#   AC3  four classes + decided:         -> block (b) decided: literal, block (c) case 1 recording
#   AC4  one round, <=3, exemption       -> block (b) cap/tag/never-dropped, block (c) cases 2-3
#   AC5  skill enforces the cap          -> block (b) sentinel; refiner-agent-contract SKILL.md step 6
#   AC6  rank + over-cap heading         -> block (b) rank/suffix; refine-confirmation-gate heading
#   AC7  browser not asked               -> block (d)
#   AC8  A1...An                         -> refine-confirmation-gate lean block
#   AC9  overrule once, Confirm/Decline  -> block (c) case 4; refine-confirmation-gate lean block
#   AC10 interactive gate unchanged      -> refine-confirmation-gate retention
#   AC11 no automerge grant              -> block (b) automerge literal
#   AC12 codex parity                    -> blocks (a)-(d) on codex.md
#   AC13 contract tests + Codex cases    -> block (c) four named cases
#
# Covered files:
#   - flow/agents/refiner.md
#   - flow/skills/refine/SKILL.md
#   - flow/skills/refine/codex.md
#   - flow/agents/planner.md -- read-only reference for the shared `decided:` format
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-lean-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-lean-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REFINER_AGENT="${FLOW_DIR}/agents/refiner.md"
PLANNER_AGENT="${FLOW_DIR}/agents/planner.md"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}
assert_file_lacks() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}

# =====================================================================
# (a) Config resolution, bundle flag, refiner input flag.
# =====================================================================

RESOLVE_LABEL_MARKER='**Resolve refinement autonomy.**'
RESOLVE_SOURCE_MARKER='reads `refinement.autonomy` from the config returned by `project-core`'
RESOLVE_EXACT_LEAN_MARKER='Only the exact string `"lean"` means lean'
RESOLVE_FAILSAFE_MARKER='a missing or non-object `refinement` block, or any other value means `interactive`'
RESOLVE_NO_PLANNING_KEYS_MARKER='`planning.autonomy` and `planning.attended` are never consulted'
RESOLVE_ONCE_MARKER='Steps 4 and 6 and the `## Confirmation Gate` read that one value and never re-read the config'
BUNDLE_FLAG_MARKER='refinementAutonomy: lean | interactive'
REFINER_INPUT_FLAG_MARKER='`refinementAutonomy` (`lean` or `interactive`)'
CODEX_RESOLVE_MARKER='Resolve `refinementAutonomy` on every invocation, during preparation and apply, from the config `project-core` returns'

assert_file_contains "${REFINE_SKILL}" "${RESOLVE_LABEL_MARKER}" \
  "## Context must add the single-point refinement-autonomy resolution paragraph (AC1)"
assert_file_contains "${REFINE_SKILL}" "${RESOLVE_SOURCE_MARKER}" \
  "must read refinement.autonomy from the config project-core returns (AC1)"
assert_file_contains "${REFINE_SKILL}" "${RESOLVE_EXACT_LEAN_MARKER}" \
  "must state only the exact string \"lean\" turns lean mode on (AC1)"
assert_file_contains "${REFINE_SKILL}" "${RESOLVE_FAILSAFE_MARKER}" \
  "must state an absent key, a missing/non-object refinement block, or any other value is interactive (AC1)"
assert_file_contains "${REFINE_SKILL}" "${RESOLVE_NO_PLANNING_KEYS_MARKER}" \
  "must state planning.autonomy and planning.attended are never consulted (AC1)"
assert_file_contains "${REFINE_SKILL}" "${RESOLVE_ONCE_MARKER}" \
  "must resolve once and have steps 4, 6 and the gate read that one value (AC1)"
assert_file_contains "${REFINE_SKILL}" "${BUNDLE_FLAG_MARKER}" \
  "step 4's bundle must carry the resolved flag refinementAutonomy: lean | interactive (AC1)"

assert_file_contains "${REFINE_CODEX}" "${CODEX_RESOLVE_MARKER}" \
  "codex.md must resolve refinementAutonomy on every invocation, both /plan and apply (AC1, AC12)"
assert_file_contains "${REFINE_CODEX}" "${RESOLVE_EXACT_LEAN_MARKER}" \
  "codex.md must state only the exact string \"lean\" turns lean mode on (AC12)"
assert_file_contains "${REFINE_CODEX}" "${RESOLVE_FAILSAFE_MARKER}" \
  "codex.md must state an absent key, a missing/non-object refinement block, or any other value is interactive (AC12)"
assert_file_contains "${REFINE_CODEX}" "${RESOLVE_NO_PLANNING_KEYS_MARKER}" \
  "codex.md must state planning.autonomy and planning.attended are never consulted (AC12)"

assert_file_contains "${REFINER_AGENT}" "${REFINER_INPUT_FLAG_MARKER}" \
  "## Inputs must list refinementAutonomy among the bundle's resolved flags (AC1)"

# =====================================================================
# (b) Literals that must be identical across refiner.md, SKILL.md, codex.md.
# =====================================================================

CAP_PHRASE_MARKER='one question round and at most 3 questions'
EXEMPT_TAG_MARKER='(required — cap-exempt)'
LEGACY_EXEMPT_TAG_MARKER='(confirm/overrule — cap-exempt)'
LEGACY_RANK_MARKER='confirm/overrule, security'
EXEMPT_NEVER_DROPPED_MARKER='is always asked and never dropped, even when the 3-question cap is already full'
RANK_MARKER='security, destructive, split, product ambiguity'
SENTINEL_MARKER='Not asked — over the lean question cap; decide it under the overflow rule.'
DECIDED_FORMAT_MARKER='decided: <answer> — <rationale> (alternative: <option label>)'
OVER_CAP_SUFFIX_MARKER='(over the cap: <class>)'
AUTOMERGE_MARKER='Lean mode never grants automerge by itself'

for FILE in "${REFINER_AGENT}" "${REFINE_SKILL}" "${REFINE_CODEX}"; do
  assert_file_contains "${FILE}" "${CAP_PHRASE_MARKER}" \
    "must pin the lean cap phrase identically (AC4, AC13)"
  assert_file_contains "${FILE}" "${EXEMPT_TAG_MARKER}" \
    "must pin the cap-exempt tag identically (AC4, AC13)"
  assert_file_lacks "${FILE}" "${LEGACY_EXEMPT_TAG_MARKER}" \
    "must not ask a confirm/overrule question for a settled decision"
  assert_file_lacks "${FILE}" "${LEGACY_RANK_MARKER}" \
    "must not rank a confirm/overrule class ahead of the four question classes"
  assert_file_contains "${FILE}" "${EXEMPT_NEVER_DROPPED_MARKER}" \
    "must pin the exempt question's always-asked-never-dropped clause identically (AC4, anti-starvation #979)"
  assert_file_contains "${FILE}" "${RANK_MARKER}" \
    "must pin the overflow rank order identically (AC6, AC13)"
  assert_file_contains "${FILE}" "${SENTINEL_MARKER}" \
    "must pin the overflow sentinel answer identically (AC5)"
  assert_file_contains "${FILE}" "${OVER_CAP_SUFFIX_MARKER}" \
    "must pin the over-cap suffix of a decided entry identically (AC6)"
  assert_file_contains "${FILE}" "${AUTOMERGE_MARKER}" \
    "must state lean mode never grants automerge by itself (AC11)"
done

for FILE in "${REFINER_AGENT}" "${REFINE_SKILL}" "${REFINE_CODEX}" "${PLANNER_AGENT}"; do
  assert_file_contains "${FILE}" "${DECIDED_FORMAT_MARKER}" \
    "must use the shared decided: entry format identically to the planner (AC3, AC6)"
done

REFINER_OVERRULE_MARKER='After a `Q: Confirmation Gate overrule` answer, ask only cap-exempt questions before one final proposal'
assert_file_contains "${REFINER_AGENT}" "${REFINER_OVERRULE_MARKER}" \
  "must restrict post-overrule output to cap-exempt questions before one final proposal (AC9)"

# Review-fix literals restated identically at every site (#979 restatement rule):
# an untagged question is class `product`, a repeated cap-exempt question is
# re-fed its recorded answer once and then STOPs, post-round-1 exempt rounds are
# bounded, and the overrule is one pass that includes its exempt-question
# re-invocations.
UNTAGGED_CLASS_MARKER='An untagged question ranks last and counts against the cap, and its class is `product` for the over-cap suffix and ordering'
DUPLICATE_WORDING_MARKER='A cap-exempt question whose wording is identical to one already answered is not'
DUPLICATE_REFEED_MARKER='the recorded answer is re-fed once'
EXEMPT_ROUND_BOUND_MARKER='At most 3 cap-exempt-only rounds follow round 1 in each pass'
ONE_OVERRULE_PASS_MARKER='This is one overrule pass'

for FILE in "${REFINER_AGENT}" "${REFINE_SKILL}" "${REFINE_CODEX}"; do
  assert_file_contains "${FILE}" "${UNTAGGED_CLASS_MARKER}" \
    "must define an untagged question as class product: ranks last, counts against the cap, product for the over-cap suffix and ordering"
  assert_file_contains "${FILE}" "${DUPLICATE_WORDING_MARKER}" \
    "must state a cap-exempt question with identical wording to an answered one is not asked again"
  assert_file_contains "${FILE}" "${DUPLICATE_REFEED_MARKER}" \
    "must re-feed the recorded answer once for a repeated cap-exempt question"
  assert_file_contains "${FILE}" "${EXEMPT_ROUND_BOUND_MARKER}" \
    "must bound post-round-1 cap-exempt-only rounds so they cannot loop forever"
  assert_file_contains "${FILE}" "${ONE_OVERRULE_PASS_MARKER}" \
    "must state the overrule is one pass that contains its own cap-exempt re-invocations (AC9)"
done

for FILE in "${REFINE_SKILL}" "${REFINE_CODEX}"; do
  assert_file_contains "${FILE}" 'STOP with zero GitHub writes and report a lean-contract violation' \
    "must STOP with zero GitHub writes and report a lean-contract violation (loop guard)"
done

assert_file_contains "${REFINE_SKILL}" 'if the refiner repeats the question after that, STOP with zero GitHub writes and report a lean-contract violation' \
  "SKILL.md must STOP when the refiner repeats an already-answered cap-exempt question after the re-feed"
assert_file_contains "${REFINE_CODEX}" 'if the analysis repeats the question after that, STOP with zero GitHub writes and report a lean-contract violation' \
  "codex.md must STOP when the analysis repeats an already-answered cap-exempt question after the re-feed"
assert_file_contains "${REFINER_AGENT}" 'if you repeat the question after that, the skill STOPs with zero GitHub writes and reports a lean-contract violation' \
  "refiner.md must state the skill STOPs when it repeats an already-answered cap-exempt question after the re-feed"

# Loop guard covers the resplitAuthorized re-invocation too.
assert_file_contains "${REFINE_SKILL}" 'again after the `resplitAuthorized` re-invocation of the Oversize split child escalation' \
  "SKILL.md loop guard must cover the resplitAuthorized re-invocation"
assert_file_contains "${REFINE_CODEX}" 'after the `resplitAuthorized` re-run still produces a non-exempt question' \
  "codex.md loop guard must cover the resplitAuthorized re-run"
assert_file_contains "${REFINER_AGENT}" 'The same holds on a `resplitAuthorized` re-invocation' \
  "refiner.md must restrict a resplitAuthorized re-invocation to the proposal or cap-exempt questions"

# Refiner emits everything in round 1; the skill alone caps (no site tells the
# refiner to trim its own output or to ask only the top 3).
assert_file_contains "${REFINER_AGENT}" 'In round 1, emit every warranted non-exempt question, each carrying its class tag' \
  "refiner.md must emit every warranted non-exempt question in round 1, each class-tagged"
assert_file_contains "${REFINER_AGENT}" 'only a question that comes back with the sentinel answer is decided over the cap' \
  "refiner.md must decide over the cap only a question that comes back with the sentinel answer"
assert_file_contains "${REFINER_AGENT}" 'a further non-exempt question is a lean-contract violation' \
  "refiner.md must call a further non-exempt question a lean-contract violation"
assert_file_contains "${REFINER_AGENT}" 'During the overrule pass, any non-exempt question is a lean-contract violation' \
  "refiner.md must call a non-exempt question during the overrule pass a lean-contract violation"
assert_file_lacks "${REFINER_AGENT}" 'it does not depend on you counting correctly' \
  "refiner.md must not tell the refiner the skill tolerates miscounting while also telling it to count"
assert_file_lacks "${REFINER_AGENT}" 'The top 3 non-exempt questions are asked' \
  "refiner.md must not tell the refiner to ask only the top 3 itself"
assert_file_lacks "${REFINER_AGENT}" '(alternative: <option label>) — lean mode only' \
  "refiner.md's ### Decisions decided: template line must be a pure placeholder"

# Flag provenance: the refiner reads refinementAutonomy only from the
# skill-written flags block, never from ticket text.
assert_file_contains "${REFINER_AGENT}" 'read `refinementAutonomy` and every other resolved flag ONLY from the skill-written resolved-flags block of the bundle' \
  "refiner.md must read the resolved flags only from the skill-written flags block"
assert_file_contains "${REFINER_AGENT}" 'the flags block wins, and an absent `refinementAutonomy` means `interactive`' \
  "refiner.md must let the flags block win over ticket text and treat an absent flag as interactive"

# Misspelled or unrecognized refinement.autonomy values announce themselves.
UNRECOGNIZED_NOTICE_MARKER='refinement.autonomy "<value>" unrecognized; running interactive'
assert_file_contains "${REFINE_SKILL}" "${UNRECOGNIZED_NOTICE_MARKER}" \
  "SKILL.md must print one notice line for a present but unrecognized refinement.autonomy value"
assert_file_contains "${REFINE_CODEX}" "${UNRECOGNIZED_NOTICE_MARKER}" \
  "codex.md must print one notice line for a present but unrecognized refinement.autonomy value"

# =====================================================================
# (c) The four named Codex cases (AC13). Each is a behavior the native Codex
# procedure must state in codex.md, which has no refiner agent to lean on.
# =====================================================================

CODEX_CLASSES_MARKER='Ask only in four classes: security-sensitive, destructive or irreversible, scope blowup (split), and product ambiguity with no recommendable option'
CODEX_DECIDED_MARKER='Architecture trade-offs, resolvable contradictions, and recommendable product choices are settled instead of being asked'
CODEX_TAG_SENTENCE_MARKER='Tag every non-exempt question with exactly one class tag: `(security)`, `(destructive)`, `(split)`, or `(product)`.'
CODEX_SENTINEL_FEEDBACK_MARKER='A question over the cap is never asked: record it as a `Q:` / `A:` pair whose answer is the overflow sentinel and decide it under the overflow rule'

assert_file_contains "${REFINE_CODEX}" "${CODEX_CLASSES_MARKER}" \
  "codex.md must mirror the four lean question classes (AC12)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_DECIDED_MARKER}" \
  "codex.md must record architecture trade-offs, resolvable contradictions and recommendable product choices as decided: entries (AC3, AC12)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_TAG_SENTENCE_MARKER}" \
  "codex.md must mirror the per-question class tags the overflow rank sorts by (AC12)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_SENTINEL_FEEDBACK_MARKER}" \
  "codex.md must decide an over-cap question under the overflow rule, never ask it (AC5, AC12)"

# --- Codex case 1: zero unnecessary questions -------------------------------

ZERO_QUESTIONS_MARKER='a ticket with only conventional decisions reaches the Confirmation Gate with zero questions asked'
ZERO_QUESTIONS_RECORDED_MARKER='every such decision is recorded in `### Assumptions (auto-adopted)` or `### Decisions`'

assert_file_contains "${REFINE_CODEX}" "${ZERO_QUESTIONS_MARKER}" \
  "Codex case 1 (zero unnecessary questions): a conventional-decisions-only ticket reaches the gate with zero questions (AC2)"
assert_file_contains "${REFINE_CODEX}" "${ZERO_QUESTIONS_RECORDED_MARKER}" \
  "Codex case 1 (zero unnecessary questions): every conventional decision is recorded, none silently dropped (AC2)"
assert_file_contains "${REFINER_AGENT}" "${ZERO_QUESTIONS_MARKER}" \
  "refiner.md must carry the same zero-question sentence as Codex case 1 (AC2)"
assert_file_contains "${REFINER_AGENT}" "${ZERO_QUESTIONS_RECORDED_MARKER}" \
  "refiner.md must record every conventional decision under lean mode (AC2)"

# --- Codex case 2: round / question cap -------------------------------------

CODEX_ROUND_CAP_MARKER='after round 1 only cap-exempt questions are asked'

assert_file_contains "${REFINE_CODEX}" "${CAP_PHRASE_MARKER}" \
  "Codex case 2 (round/question cap): at most one round and 3 questions (AC4)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_ROUND_CAP_MARKER}" \
  "Codex case 2 (round/question cap): after round 1 only cap-exempt questions are asked (AC4)"

# --- Codex case 3: required cap-exempt question ----------------------------

REQUIRED_OPEN_CHOICE_MARKER='A required question asks an open choice; it never asks the user to confirm a decision that is already settled.'

for FILE in "${REFINER_AGENT}" "${REFINE_CODEX}"; do
  assert_file_contains "${FILE}" "${REQUIRED_OPEN_CHOICE_MARKER}" \
    "Codex case 3 (required question): a required question asks an open choice, never a confirmation of a settled one"
done
assert_file_contains "${REFINE_CODEX}" "${EXEMPT_NEVER_DROPPED_MARKER}" \
  "Codex case 3 (required question): the exempt question is always asked and never dropped (AC4, anti-starvation #979)"
assert_file_contains "${REFINE_CODEX}" "${EXEMPT_TAG_MARKER}" \
  "Codex case 3 (required question): the exempt question carries the exempt tag (AC4)"

# --- Codex case 4: Confirmation Gate overrule -------------------------------

CODEX_OVERRULE_RECORD_MARKER='Q: Confirmation Gate overrule'
CODEX_OVERRULE_ONCE_MARKER='re-runs the analysis inline exactly once, read-only'
CODEX_OVERRULE_RERENDER_MARKER='The re-rendered gate offers only Confirm and Decline'
CODEX_OVERRULE_NO_WRITE_MARKER='the overrule itself performs no GitHub write'
CODEX_OVERRULE_NARROW_EXCEPTION_MARKER='preserves the same normal-mode preparation boundary'

assert_file_contains "${REFINE_CODEX}" "${CODEX_OVERRULE_RECORD_MARKER}" \
  "Codex case 4 (Confirmation Gate overrule): the overrule is recorded as Q: Confirmation Gate overrule (AC9)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_OVERRULE_ONCE_MARKER}" \
  "Codex case 4 (Confirmation Gate overrule): the inline re-analysis runs exactly once and read-only (AC9)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_OVERRULE_RERENDER_MARKER}" \
  "Codex case 4 (Confirmation Gate overrule): the re-rendered gate offers only Confirm/Decline (AC9)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_OVERRULE_NO_WRITE_MARKER}" \
  "Codex case 4 (Confirmation Gate overrule): no GitHub write happens on the overrule (AC9)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_OVERRULE_NARROW_EXCEPTION_MARKER}" \
  "Codex case 4 (Confirmation Gate overrule): the in-apply re-analysis is documented as a narrow exception to codex-runtime's gather-in-/plan rule (AC9)"

# =====================================================================
# (d) Browser invariant in lean mode (AC7, #1174): the requirement is derived
# from classification, never asked, and the `Browser` label is still applied.
# =====================================================================

REFINER_BROWSER_MARKER='The `Browser` requirement is never a question in either mode'
SKILL_BROWSER_MARKER='Lean mode does not change step 8: `browserRequired` is still derived from classification, never asked, and the `Browser` label is still applied from it'
CODEX_BROWSER_MARKER='Lean mode does not change the `Browser` derivation: it is still derived from classification, never asked, and the `Browser` label is still applied from it'

assert_file_contains "${REFINER_AGENT}" "${REFINER_BROWSER_MARKER}" \
  "lean policy must state the Browser requirement is never a question (AC7)"
assert_file_contains "${REFINE_SKILL}" "${SKILL_BROWSER_MARKER}" \
  "lean mode must keep step 8's classification-derived browserRequired and the Browser label (AC7)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_BROWSER_MARKER}" \
  "codex.md lean mode must keep the classification-derived Browser requirement and label (AC7, AC12)"
assert_file_lacks "${REFINER_AGENT}" 'need interactive browser access during implementation?' \
  "must not ask a browser-access question in either mode (AC7)"
assert_file_lacks "${REFINE_SKILL}" 'need interactive browser access during implementation?' \
  "must not ask a browser-access question in either mode (AC7)"
assert_file_lacks "${REFINE_CODEX}" 'browser question' \
  "codex.md must not describe a browser question in either mode (AC7)"

echo "refine-lean-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
