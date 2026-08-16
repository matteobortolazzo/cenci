#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #1069: auto-adopting a
# refinement-settled security/irreversible posture instead of re-confirming
# it, when (and only when) the delegation-forwarded provenance is positively
# verified.
#
# Before #1069, `flow/agents/planner.md`'s confirm/overrule rule asked a
# human to re-confirm a posture the refined ticket's own `### Decisions`
# already fixed at the refine skill's Confirmation Gate — double-confirming
# the same decision and contributing to a ~70% lean-planning stop rate. This
# ticket narrows the confirm/overrule *trigger* (never its cap priority) to
# suppress the ask only when: (a) the delegation forwards a `Refined` label
# and a trusted `ticketAuthor:` association; (b) the posture is quotable
# verbatim from the ticket body's own `### Decisions`/`### Assumptions
# (auto-adopted)`; (c) the codebase doesn't contradict it. Every other case
# fails closed to today's ask.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/plan-approval-provenance.test.sh: `set -uo pipefail`, a
# `failures` counter, `assert_file_contains` built on `grep -qF`, markers
# kept on a single source line (per docs/shell-scripting-gotchas.md),
# auto-discovered by scripts/run-checks.sh's `*.test.sh` glob — no
# registration needed.
#
# Marker precision (docs/shell-scripting-gotchas.md rule 3): every marker
# below is a distinctive full phrase pulled from the plan's own AC/Decision
# wording — never a bare keyword — so a marker can only pass once the
# planned prose actually lands at its planned edit site.
#
# Non-duplication note: this file deliberately does NOT re-assert markers
# already owned by flow/tests/lean-planning-contract.test.sh (the
# confirm/overrule sentence, the anti-starvation priority sentence, and the
# five escalation-class literals as *content*) or by
# flow/tests/plan-persist-sections-contract.test.sh (its `assert_occurs_once`
# markers elsewhere in phase-1-plan.md). Where this ticket's Test Strategy
# table still requires a regression proof over that same unchanged material,
# this file uses a *different kind* of check instead of re-asserting the
# same needle: an occurrence-COUNT invariant (proving this ticket's edit
# didn't introduce a stray extra occurrence) or a line-ORDER invariant
# (proving the new paragraph lands after, never inside or before, the
# existing sentence) — both distinct from, and complementary to, the
# presence checks lean-planning-contract.test.sh already owns.
#
# Covered files:
#   - flow/agents/planner.md (new suppression paragraph, fail-closed
#     enumeration, precedence statement, trust-channel/body-only/secrecy
#     statements)
#   - flow/agents/refiner.md (refinement-time restatement, unchanged)
#   - flow/skills/refine/codex.md (refinement-time restatement, unchanged)
#   - flow/agents/context-gatherer.md (digest `ticketAuthor:` line)
#   - flow/skills/implement/SKILL.md (digest storage bullets)
#   - flow/skills/implement/phases/phase-1-plan.md (Planner Delegation
#     forwarding bullet, Resume From Draft step 5 provenance sub-paragraph,
#     sensitive-path backstop + HS-* row count regression)
#   - flow/skills/implement/codex.md (recorded Codex exclusion)
#   - docs/autonomous-loop.md (Planning refuses to guess paragraph)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "planning-provenance-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "planning-provenance-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "planning-provenance-contract.test.sh: failed to resolve repo root." >&2; exit 2; }

PLANNER_AGENT="${FLOW_DIR}/agents/planner.md"
REFINER_AGENT="${FLOW_DIR}/agents/refiner.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
CONTEXT_GATHERER="${FLOW_DIR}/agents/context-gatherer.md"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
IMPLEMENT_CODEX="${FLOW_DIR}/skills/implement/codex.md"
AUTONOMOUS_LOOP_DOC="${REPO_ROOT}/docs/autonomous-loop.md"

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
  ! grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected NOT to contain: $2)"
}

assert_occurs_exactly() {
  # $1=file $2=needle $3=expected count $4=description
  # Counts actual occurrences via `grep -oF | wc -l`, not `grep -cF` (which
  # counts matching *lines*, so two occurrences on one line would silently
  # undercount as one) -- same idiom as gh-title-payload-encoding.test.sh:65.
  [[ -n "$2" ]] || { fail "$4: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$4: file not found: $1"; return; }
  local actual
  actual="$(grep -oF -- "$2" "$1" | wc -l | tr -d '[:space:]')"
  [[ "${actual}" -eq "$3" ]] || fail "$(basename "$1") $4 (expected exactly $3 occurrence(s), found ${actual})"
}

# first_match_line_in_file <file> <needle> -- pure: prints the 1-based line
# number of the needle's first literal match in the file, or nothing if
# absent. Safe inside $(...): no fail() side effect. Same idiom as
# flow/tests/implement-split-gate-contract.test.sh and
# flow/tests/implement-blocked-dependency-gate.test.sh (self-contained test
# files copy this local helper rather than sourcing a shared one).
first_match_line_in_file() {
  grep -nF -m1 -- "$2" "$1" 2>/dev/null | cut -d: -f1
}

# assert_marker_precedes <file> <needle-before> <needle-after> <label> --
# ordering assertion via first_match_line_in_file's first-match line offset.
# Calls fail() directly in the parent shell -- must NOT be invoked via $(...).
assert_marker_precedes() {
  local file="$1" before="$2" after="$3" label="$4"
  local line_before line_after
  line_before="$(first_match_line_in_file "${file}" "${before}")"
  line_after="$(first_match_line_in_file "${file}" "${after}")"
  if [[ -z "${line_before}" || -z "${line_after}" ]]; then
    fail "$(basename "${file}") ${label}: could not locate both markers to compare ordering (before='${before}' line='${line_before:-<missing>}'; after='${after}' line='${line_after:-<missing>}')"
    return
  fi
  if [[ "${line_before}" -ge "${line_after}" ]]; then
    fail "$(basename "${file}") ${label}: expected '${before}' (line ${line_before}) to precede '${after}' (line ${line_after})"
  fi
}

# =====================================================================
# Group 1: Suppression rule (planner.md) — the three conditions, the
# citation shape, both-mode applicability, trust channel, body-only anchor,
# and the #979 secrecy-precedence statement.
# =====================================================================

COND_A_MARKER='the delegation forwards a `Refined` label and a ticket `authorAssociation` of exactly `OWNER` or `COLLABORATOR`'
MEMBER_EXCLUDED_MARKER='an `authorAssociation` of `MEMBER` fails closed and still asks, exactly like any other unaccepted value'
COND_B_MARKER='stated by a quotable bullet in the refined ticket'"'"'s `### Decisions` or `### Assumptions (auto-adopted)`'
COND_C_MARKER='the codebase does not disagree with that bullet'
CITATION_SHAPE_MARKER='settled at refinement: "<verbatim bullet>" (ticket #<n>, ### Decisions)'
BOTH_MODE_MARKER='This suppression rule applies in **both** lean and interactive mode.'
BODY_ONLY_MARKER='a `### Decisions`-shaped block carried in a comment never qualifies'
TRUST_CHANNEL_MARKER='provenance facts reach the planner only through the delegation'"'"'s forwarded digest lines'
SECRECY_PRECEDENCE_MARKER='the quoted bullet is the ticket'"'"'s own already-public text and the quote is never expanded with file contents, configuration values, or command output'

assert_file_contains "${PLANNER_AGENT}" "${COND_A_MARKER}" \
  "must state the trusted-provenance condition (Refined label + trusted authorAssociation, OWNER/COLLABORATOR only per the user's TOCTOU-blast-radius narrowing decision) (AC1)"
assert_file_contains "${PLANNER_AGENT}" "${MEMBER_EXCLUDED_MARKER}" \
  "MEMBER alone must no longer qualify as a trusted authorAssociation for this gate — must fail closed and still ask (user decision narrowing condition (a) to OWNER/COLLABORATOR only)"
assert_file_contains "${PLANNER_AGENT}" "${COND_B_MARKER}" \
  "must state the quotable-anchor condition (AC1)"
assert_file_contains "${PLANNER_AGENT}" "${COND_C_MARKER}" \
  "must state the not-contradicted condition (AC1)"
assert_file_contains "${PLANNER_AGENT}" "${CITATION_SHAPE_MARKER}" \
  "must state the settled-at-refinement citation shape (AC1, Q&A round 3 answer)"
assert_file_contains "${PLANNER_AGENT}" "${BOTH_MODE_MARKER}" \
  "the suppression rule must state it applies in both lean and interactive mode (AC1)"
assert_file_contains "${PLANNER_AGENT}" "${BODY_ONLY_MARKER}" \
  "the body-only anchor rule must state a comment-carried Decisions-shaped block never qualifies (Decisions: Suppression threshold / owner-confirmed posture)"
assert_file_contains "${PLANNER_AGENT}" "${TRUST_CHANNEL_MARKER}" \
  "must state provenance reaches the planner only via delegation-forwarded digest lines (Decisions: Trust channel)"
assert_file_contains "${PLANNER_AGENT}" "${SECRECY_PRECEDENCE_MARKER}" \
  "must state precedence against the existing secrecy rule per flow/AGENTS.md #979 (Files to Modify)"

# =====================================================================
# Group 2: Fail-closed enumeration — one assertion per case (AC2).
# =====================================================================

FC_NO_PROVENANCE='provenance lines absent from the delegation'
FC_NO_REFINED='`Refined` absent from the forwarded labels'
FC_ASSOCIATION='`authorAssociation` any value other than the two accepted literals, including `MEMBER`, empty, or unrecognized'
FC_TICKETLESS='ticketless mode, which never carries a delegation-forwarded provenance line'
FC_RESUME_FAILED='a failed resume-time provenance read'
FC_NO_QUOTABLE='no quotable bullet — the posture needs an inferential step'
FC_BODY_COMMENT_INDETERMINATE='an anchor whose body-vs-comment origin cannot be told apart inside the bundle'

assert_file_contains "${PLANNER_AGENT}" "${FC_NO_PROVENANCE}" \
  "fail-closed case: provenance lines absent from the delegation must still ask (AC2)"
assert_file_contains "${PLANNER_AGENT}" "${FC_NO_REFINED}" \
  "fail-closed case: Refined absent must still ask (AC2)"
assert_file_contains "${PLANNER_AGENT}" "${FC_ASSOCIATION}" \
  "fail-closed case: an authorAssociation outside the two accepted literals (OWNER/COLLABORATOR — MEMBER named explicitly) must still ask (AC2)"
assert_file_contains "${PLANNER_AGENT}" "${FC_TICKETLESS}" \
  "fail-closed case: ticketless mode must still ask (AC2)"
assert_file_contains "${PLANNER_AGENT}" "${FC_RESUME_FAILED}" \
  "fail-closed case: a failed resume-time provenance read must still ask (AC2)"
assert_file_contains "${PLANNER_AGENT}" "${FC_NO_QUOTABLE}" \
  "fail-closed case: no quotable bullet must still ask (AC2)"
assert_file_contains "${PLANNER_AGENT}" "${FC_BODY_COMMENT_INDETERMINATE}" \
  "fail-closed case: an indeterminate body-vs-comment anchor must still ask (Decisions: Suppression threshold, owner Q10 answer)"

# =====================================================================
# Group 3: Precedence + untouched machinery.
# =====================================================================

PRECEDENCE_TRIGGER_MARKER='narrows the confirm/overrule **trigger**, never its **priority**'
PRECEDENCE_UNCHANGED_MARKER='whenever the trigger still fires, the anti-starvation/displacement rule is unchanged'

assert_file_contains "${PLANNER_AGENT}" "${PRECEDENCE_TRIGGER_MARKER}" \
  "must state the rule narrows the confirm/overrule trigger, never its priority (AC5)"
assert_file_contains "${PLANNER_AGENT}" "${PRECEDENCE_UNCHANGED_MARKER}" \
  "must state the anti-starvation/displacement rule is unchanged whenever the trigger still fires (AC5)"

# Structural (non-duplicate) proof that the new paragraph lands *after* the
# existing confirm/overrule + anti-starvation paragraph, never inside or
# before it — the byte-identity of that paragraph itself is already pinned
# by lean-planning-contract.test.sh:332-341,444 and is not re-asserted here.
ANTI_STARVATION_TAIL='never silently dropped for lack of budget.'
assert_marker_precedes "${PLANNER_AGENT}" "${ANTI_STARVATION_TAIL}" "${COND_A_MARKER}" \
  "the new suppression paragraph must be additive, landing after the existing confirm/overrule + anti-starvation paragraph (Files to Modify: additive paragraph placement)"

# Occurrence-count regression: the five escalation-class literals must
# appear exactly as often after this edit as they do today — proving the
# new paragraph didn't restate (and thus duplicate) any of them. Baseline
# captured against the unedited file.
assert_occurs_exactly "${PLANNER_AGENT}" '**security-sensitive**' 2 \
  "security-sensitive escalation class occurrence count must be unchanged (AC3 regression)"
assert_occurs_exactly "${PLANNER_AGENT}" '**destructive or irreversible**' 2 \
  "destructive-or-irreversible escalation class occurrence count must be unchanged (AC3 regression)"
assert_occurs_exactly "${PLANNER_AGENT}" '**contradicts the refined ticket**' 1 \
  "contradicts-the-refined-ticket escalation class occurrence count must be unchanged (AC4 regression)"
assert_occurs_exactly "${PLANNER_AGENT}" '**genuine product ambiguity the ticket doesn'"'"'t settle**' 1 \
  "genuine-product-ambiguity escalation class occurrence count must be unchanged (AC3 regression)"
assert_occurs_exactly "${PLANNER_AGENT}" '**scope blowup**' 1 \
  "scope-blowup escalation class occurrence count must be unchanged (AC3 regression)"

# refiner.md and skills/refine/codex.md: the refinement-time restatement
# sites are deliberately unchanged (restatement-site audit, AC5). Neither
# marker below is asserted by lean-planning-contract.test.sh today.
REFINER_EXEMPT_MARKER='This confirm/overrule question is exempt from the round'"'"'s question cap and must be asked before the round can return `None.`'
CODEX_REFINE_RESTATEMENT_MARKER="ask via the client's available user-input mechanism a confirm/overrule question that states the decision and its derivation without re-opening the full option space"

assert_file_contains "${REFINER_AGENT}" "${REFINER_EXEMPT_MARKER}" \
  "refiner.md's confirm/overrule-exempt-from-cap restatement must be unchanged (restatement-site audit, AC5)"
assert_file_contains "${REFINE_CODEX}" "${CODEX_REFINE_RESTATEMENT_MARKER}" \
  "flow/skills/refine/codex.md's refinement-time restatement must be unchanged (restatement-site audit, AC5)"

# phase-1-plan.md: the sensitive-path backstop bullet is explicitly out of
# scope and byte-unchanged (AC7). Occurrence-count check, not a duplicate of
# lean-planning-contract.test.sh's existing full-sentence contains check.
assert_occurs_exactly "${PHASE1_PLAN}" '**Deterministic sensitive-path backstop.**' 1 \
  "the deterministic sensitive-path backstop bullet occurrence count must be unchanged (AC7 regression)"

# phase-1-plan.md: total `| HS-*` row count must be unchanged — the resume
# path's read-only provenance re-derivation introduces no new hard-stop row
# (AC5/Technical Notes trap: escalation-hardstop-matrix.test.sh's pinned
# 21-row qualifying count must not change; this is a broader raw-count
# regression guard over the same table).
HS_ROW_COUNT="$(grep -c -- '^| HS-' "${PHASE1_PLAN}")"
[[ "${HS_ROW_COUNT}" -eq 29 ]] || fail "phase-1-plan.md total | HS-* row count must stay 29 (no new hard-stop row for the resume-path provenance read) — found ${HS_ROW_COUNT}"

# =====================================================================
# Group 4: Plumbing — digest line, storage, delegation forwarding, resume.
# =====================================================================

DIGEST_TICKETAUTHOR_TEMPLATE='ticketAuthor: <login> (<AUTHORASSOCIATION>)'
DIGEST_UNKNOWN_RENDERING='rendered `ticketAuthor: unknown` when the field is absent or unreadable'
DIGEST_TICKETLESS_OMISSION='omitted entirely in ticketless mode, as `blockers:` already is'
DIGEST_NO_NEW_GH_CALL='derived solely from §1'"'"'s two ticket reads'
SECTION1_JSON_FIELDS='gh issue view <number> --repo <owner>/<repo> --json number,title,body,labels,state,assignees,milestone,comments,author'
SECTION1_ASSOCIATION_CALL="gh api repos/<owner>/<repo>/issues/<number> --jq '.author_association'"
SECTION1_NO_TOPLEVEL_FIELD='exposes **no** top-level `authorAssociation` field'

assert_file_contains "${CONTEXT_GATHERER}" "${DIGEST_TICKETAUTHOR_TEMPLATE}" \
  "digest template must carry the ticketAuthor: line in the exact <login> (<AUTHORASSOCIATION>) shape (AC6, Q&A round 1 answer)"
assert_file_contains "${CONTEXT_GATHERER}" "${DIGEST_UNKNOWN_RENDERING}" \
  "must state the unknown rendering when the field is absent/unreadable (AC6)"
assert_file_contains "${CONTEXT_GATHERER}" "${DIGEST_TICKETLESS_OMISSION}" \
  "must state the line is omitted entirely in ticketless mode, as blockers: already is (AC6)"
assert_file_contains "${CONTEXT_GATHERER}" "${DIGEST_NO_NEW_GH_CALL}" \
  "must state the line is derived solely from §1's ticket reads, never from body/comment text (AC6)"
assert_file_contains "${CONTEXT_GATHERER}" "${SECTION1_JSON_FIELDS}" \
  "§1's --json field list must request the ticket's top-level author login"
assert_file_lacks "${CONTEXT_GATHERER}" "--json number,title,body,labels,state,assignees,milestone,comments,author,authorAssociation" \
  "§1 must not request a top-level authorAssociation from gh issue view — the field does not exist there and the whole fetch exits non-zero with Unknown JSON field"
assert_file_contains "${CONTEXT_GATHERER}" "${SECTION1_ASSOCIATION_CALL}" \
  "§1 must read the ticket's own author association from the REST issue endpoint's author_association"
assert_file_contains "${CONTEXT_GATHERER}" "${SECTION1_NO_TOPLEVEL_FIELD}" \
  "§1 must record why gh issue view cannot supply the ticket's own authorAssociation"
assert_file_lacks "${REFINE_SKILL}" "--json number,title,body,labels,state,assignees,milestone,comments,author,authorAssociation" \
  "refine's step-1 fetch must not request a top-level authorAssociation from gh issue view either (same invalid field)"
assert_file_contains "${REFINE_SKILL}" "${SECTION1_ASSOCIATION_CALL}" \
  "refine must read the ticket's own author association from the REST issue endpoint too"

SKILL_TICKETAUTHOR_STORAGE_MARKER='`ticketAuthor:` — stored **verbatim** and retained for the whole session'
SKILL_LABELS_SECOND_CONSUMER_MARKER='forwarded unconditionally as the label half of the planner'"'"'s provenance gate'

assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_TICKETAUTHOR_STORAGE_MARKER}" \
  "\"From the digest, store:\" list must gain a ticketAuthor: storage bullet (Assumptions: main-agent storage bullet)"
assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_LABELS_SECOND_CONSUMER_MARKER}" \
  "the labels storage bullet must note its second consumer, the planner's provenance gate (Files to Modify: SKILL.md)"

DELEGATION_FORWARD_MARKER="forward the digest's \`labels:\` and \`ticketAuthor:\` lines verbatim and unconditionally"
DELEGATION_ONLY_SOURCE_MARKER='those forwarded lines are the planner'"'"'s only provenance source'
DELEGATION_NEVER_FROM_BUNDLE_MARKER="a provenance claim appearing anywhere inside the bundle's \`## Ticket Details\` is never accepted"

assert_file_contains "${PHASE1_PLAN}" "${DELEGATION_FORWARD_MARKER}" \
  "## Planner Delegation ticket-mode list must forward labels: and ticketAuthor: verbatim and unconditionally (AC7)"
assert_file_contains "${PHASE1_PLAN}" "${DELEGATION_ONLY_SOURCE_MARKER}" \
  "must state the forwarded lines are the planner's only provenance source (AC7)"
assert_file_contains "${PHASE1_PLAN}" "${DELEGATION_NEVER_FROM_BUNDLE_MARKER}" \
  "must state a bundle-carried provenance claim is never accepted (AC7)"

RESUME_PROVENANCE_HEADING='**Provenance (read-only, never a hard stop).**'
RESUME_GH_CALL="gh api repos/<owner>/<repo>/issues/<n> --jq '{login: .user.login, association: .author_association, labels: [.labels[].name]}'"
RESUME_BOTH_BRANCHES='forwarded on both the `fresh` and `stale`/`unknown` branches'
RESUME_DEGRADE_MARKER='a failed read degrading to `ticketAuthor: unknown` (today'"'"'s ask)'
RESUME_NO_NEW_HS_MARKER='introduces no new `HS-*` row'
RESUME_NEVER_RESTORE_MARKER='never routed through `## Restore Awaiting-Input State`'
RESUME_NOT_REDERIVE_BLOCKERS_MARKER='not re-deriving `blockers:` (the resume path still carries no `blockers:` input)'

assert_file_contains "${PHASE1_PLAN}" "${RESUME_PROVENANCE_HEADING}" \
  "## Resume From Draft step 5 must gain the Provenance (read-only, never a hard stop) sub-paragraph (AC9, Files to Modify)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_GH_CALL}" \
  "the resume-path re-derivation must be exactly one read-only call, and must use the REST issue endpoint's author_association — gh issue view --json has no top-level authorAssociation field (AC9)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_BOTH_BRANCHES}" \
  "the two facts must be forwarded on both the fresh and stale/unknown branches (AC9)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_DEGRADE_MARKER}" \
  "a failed resume-path read must degrade to ticketAuthor: unknown, i.e. today's ask (AC9)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_NO_NEW_HS_MARKER}" \
  "must state the resume-path read introduces no new HS-* row (AC9)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_NEVER_RESTORE_MARKER}" \
  "must state the resume-path read is never routed through ## Restore Awaiting-Input State (AC9)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_NOT_REDERIVE_BLOCKERS_MARKER}" \
  "must state the resume path still does not re-derive blockers: (Files to Modify: phase-1-plan.md site 2)"

# =====================================================================
# Group 5: Docs + client surfaces.
# =====================================================================

AUTOLOOP_SETTLED_MARKER='a posture already settled verbatim in a `Refined`, trusted-author ticket'"'"'s `### Decisions`/`### Assumptions (auto-adopted)` is auto-adopted rather than re-asked'
AUTOLOOP_FALLBACK_MARKER='unverifiable provenance falls back to asking'
AUTOLOOP_SENSITIVE_PATH_BULLET_UNCHANGED='no file in `### Files to Modify`/`### Files to Create` matching the sensitive-path'

assert_file_contains "${AUTONOMOUS_LOOP_DOC}" "${AUTOLOOP_SETTLED_MARKER}" \
  "docs/autonomous-loop.md must state a Refined, trusted-author settled posture is auto-adopted rather than re-asked (AC8)"
assert_file_contains "${AUTONOMOUS_LOOP_DOC}" "${AUTOLOOP_FALLBACK_MARKER}" \
  "docs/autonomous-loop.md must state unverifiable provenance falls back to asking (AC8)"
assert_file_contains "${AUTONOMOUS_LOOP_DOC}" "${AUTOLOOP_SENSITIVE_PATH_BULLET_UNCHANGED}" \
  "docs/autonomous-loop.md's existing sensitive-path disqualifier bullet must stay unchanged (Q&A round 5 answer)"

CODEX_EXCLUSION_MARKER='every plan it persists is `approval: human` by construction'
assert_file_contains "${IMPLEMENT_CODEX}" "${CODEX_EXCLUSION_MARKER}" \
  "flow/skills/implement/codex.md's recorded exclusion (no autonomous approval path) must remain the documented reason no Codex edit is needed (Q&A round 1 answer)"

if [[ "${failures}" -gt 0 ]]; then
  echo "planning-provenance-contract.test.sh: failures=${failures}" >&2
  exit 1
fi
echo "planning-provenance-contract.test.sh: failures=0"
