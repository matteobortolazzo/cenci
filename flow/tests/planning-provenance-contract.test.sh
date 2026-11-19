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
# Ticket #1076 adds a fourth trigger condition (d): a server-timestamp
# freshness binding, `refinedFresh: yes | no | unknown`. Group 6 below pins it
# end to end -- the context-gatherer's GraphQL read and its jq verdict filter
# (executed here against inline fixtures, so the comparison is proven rather
# than described), the digest line and rendering rule, the planner's fourth
# condition and three new fail-closed cases, the implement storage/forwarding
# and the resume-path read ordering, refine's `Refined` re-label in SKILL.md
# and codex.md, and the docs/autonomous-loop.md sentences.
#
# Follows the grep-based idiom (plus one executable jq block in Group 6) of
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
#   - flow/skills/refine/codex.md (refinement-time restatement, unchanged;
#     #1076 `Refined` re-label in its write-order summary, write-order block
#     and label prose)
#   - flow/skills/refine/SKILL.md (#1076 step 11 `Refined` re-label)
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
# #1199: the two grounded-resolution classes are named once more, by design, in
# the Self-Answer Policy's single **Grounded resolution** paragraph (baseline 1 -> 2).
assert_occurs_exactly "${PLANNER_AGENT}" '**contradicts the refined ticket**' 2 \
  "contradicts-the-refined-ticket class: its bullet plus the one Grounded resolution paragraph (#1199)"
assert_occurs_exactly "${PLANNER_AGENT}" '**genuine product ambiguity the ticket doesn'"'"'t settle**' 2 \
  "genuine-product-ambiguity class: its bullet plus the one Grounded resolution paragraph (#1199)"
assert_occurs_exactly "${PLANNER_AGENT}" '**scope blowup**' 1 \
  "scope-blowup escalation class occurrence count must be unchanged (AC3 regression)"

# refiner.md and skills/refine/codex.md: refinement no longer asks a
# confirm/overrule question for an entailed decision (it records it with its
# derivation instead), so neither planning-era restatement may reappear there.
REFINER_EXEMPT_MARKER='This confirm/overrule question is exempt from the round'"'"'s question cap and must be asked before the round can return `None.`'
CODEX_REFINE_RESTATEMENT_MARKER="ask via the client's available user-input mechanism a confirm/overrule question that states the decision and its derivation without re-opening the full option space"

assert_file_lacks "${REFINER_AGENT}" "${REFINER_EXEMPT_MARKER}" \
  "refiner.md must not restore the entailed confirm/overrule cap exemption"
assert_file_lacks "${REFINE_CODEX}" "${CODEX_REFINE_RESTATEMENT_MARKER}" \
  "flow/skills/refine/codex.md must not restore the entailed confirm/overrule question"

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
# refine/SKILL.md's own copy of the REST author_association read (originally
# added only to feed the since-removed Design Coverage Check's per-comment
# node-ID trust screen) has no remaining consumer anywhere in refine/
# refiner.md/refine's codex.md -- unlike context-gatherer.md's copy, which
# planner.md's refinement-settled-posture suppression rule still reads via
# the digest's `ticketAuthor:` line. It was removed along with the rest of
# the design-stage removal rather than kept as unused dead code.
assert_file_lacks "${REFINE_SKILL}" "${SECTION1_ASSOCIATION_CALL}" \
  "refine's own gh api .../issues/<number> --jq '.author_association' read must be gone -- it had no consumer left after the Design Coverage Check was removed"

SKILL_TICKETAUTHOR_STORAGE_MARKER='`ticketAuthor:` — stored **verbatim** and retained for the whole session'
SKILL_LABELS_SECOND_CONSUMER_MARKER='forwarded unconditionally as the label half of the planner'"'"'s provenance gate'

assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_TICKETAUTHOR_STORAGE_MARKER}" \
  "\"From the digest, store:\" list must gain a ticketAuthor: storage bullet (Assumptions: main-agent storage bullet)"
assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_LABELS_SECOND_CONSUMER_MARKER}" \
  "the labels storage bullet must note its second consumer, the planner's provenance gate (Files to Modify: SKILL.md)"

DELEGATION_FORWARD_MARKER="forward the digest's \`labels:\`, \`ticketAuthor:\` and \`refinedFresh:\` lines verbatim and unconditionally"
DELEGATION_ONLY_SOURCE_MARKER='those forwarded lines are the planner'"'"'s only provenance source'
DELEGATION_NEVER_FROM_BUNDLE_MARKER="a provenance claim appearing anywhere inside the bundle's \`## Ticket Details\` is never accepted"

assert_file_contains "${PHASE1_PLAN}" "${DELEGATION_FORWARD_MARKER}" \
  "## Planner Delegation ticket-mode list must forward labels:, ticketAuthor: and refinedFresh: verbatim and unconditionally (AC7, #1076)"
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

CODEX_EXCLUSION_MARKER='every plan it persists is recorded `held`'
assert_file_contains "${IMPLEMENT_CODEX}" "${CODEX_EXCLUSION_MARKER}" \
  "flow/skills/implement/codex.md's recorded exclusion (no autonomous approval path) must remain the documented reason no Codex edit is needed (Q&A round 1 answer)"

# =====================================================================
# Group 6 (#1076): refinedFresh: -- the fourth suppression condition, a
# server-timestamp freshness binding between the ticket body's last edit and
# the most recent `Refined` label event. Every anchored phrase below sits on
# one source line of its target file (docs/shell-scripting-gotchas.md rule 3).
# =====================================================================

# --- 6a. The GraphQL read and its jq verdict filter: pinned as literals in the
# context-gatherer and phase-1 resume paths, and EXECUTED here against inline
# fixtures. The constants are owned by this test; the prose must carry the
# identical single-line call, so a drifted filter in either file fails 6b.
FRESHNESS_GQL_QUERY='query($owner: String!, $repo: String!, $number: Int!) { repository(owner: $owner, name: $repo) { issue(number: $number) { createdAt lastEditedAt timelineItems(itemTypes: [LABELED_EVENT], last: 100) { nodes { ... on LabeledEvent { createdAt label { name } } } } } } }'
FRESHNESS_JQ_FILTER='.data.repository.issue as $i | if $i == null then "unknown" else ([$i.timelineItems.nodes[]? | select(.label.name? == "Refined") | .createdAt] | max) as $r | (if ($i.lastEditedAt // "") != "" then $i.lastEditedAt else $i.createdAt end) as $e | if $r == null or $e == null then "unknown" elif $e <= $r then "yes" else "no" end end'
FRESHNESS_GQL_CALL="gh api graphql -f owner=<owner> -f repo=<repo> -F number=<number> -f query='${FRESHNESS_GQL_QUERY}' --jq '${FRESHNESS_JQ_FILTER}'"

T_CREATED='2026-01-01T00:00:00Z'
T_LABELED='2026-01-02T00:00:00Z'
T_EDITED='2026-01-03T00:00:00Z'
T_RELABELED='2026-01-04T00:00:00Z'

# fx_issue <createdAt> <lastEditedAt JSON (null or a quoted string)> <nodes JSON, comma-joined> -- builds the
# `gh api graphql` response envelope the filter runs over.
fx_issue() {
  printf '{"data":{"repository":{"issue":{"createdAt":"%s","lastEditedAt":%s,"timelineItems":{"nodes":[%s]}}}}}' "$1" "$2" "$3"
}
# fx_label <label name> <createdAt> -- one LabeledEvent timeline node.
fx_label() {
  printf '{"createdAt":"%s","label":{"name":"%s"}}' "$2" "$1"
}

# assert_freshness <expected verdict> <description> <fixture JSON> -- runs the
# real filter with jq; calls fail() in the parent shell (never via $(...)).
assert_freshness() {
  local want="$1" label="$2" json="$3" got rc
  got="$(printf '%s' "${json}" | jq -r "${FRESHNESS_JQ_FILTER}")"
  rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    fail "freshness filter ${label}: jq exited ${rc} (expected verdict '${want}')"
    return
  fi
  [[ "${got}" == "${want}" ]] || fail "freshness filter ${label}: expected '${want}', got '${got}'"
}

if ! command -v jq >/dev/null 2>&1; then
  fail "jq is required to execute the refinedFresh: filter fixtures (#1076) -- install jq; this is a failure, not a skip"
else
  assert_freshness yes "never-edited body (lastEditedAt null) falls back to createdAt and is fresh" \
    "$(fx_issue "${T_CREATED}" null "$(fx_label Refined "${T_LABELED}")")"
  assert_freshness yes "body edit time equal to the Refined event time is fresh" \
    "$(fx_issue "${T_CREATED}" "\"${T_LABELED}\"" "$(fx_label Refined "${T_LABELED}")")"
  assert_freshness no "body edited after the Refined event is not fresh (also models the second resume round)" \
    "$(fx_issue "${T_CREATED}" "\"${T_EDITED}\"" "$(fx_label Refined "${T_LABELED}")")"
  assert_freshness unknown "no Refined LabeledEvent in the window is unknown" \
    "$(fx_issue "${T_CREATED}" null "$(fx_label Working "${T_LABELED}")")"
  assert_freshness unknown "an empty timeline window is unknown" \
    "$(fx_issue "${T_CREATED}" null "")"
  assert_freshness unknown "a lowercase 'refined' label is not a Refined match" \
    "$(fx_issue "${T_CREATED}" null "$(fx_label refined "${T_LABELED}")")"
  assert_freshness yes "labeled, edited, then re-labeled uses the most recent Refined event" \
    "$(fx_issue "${T_CREATED}" "\"${T_EDITED}\"" "$(fx_label Refined "${T_LABELED}"),$(fx_label Refined "${T_RELABELED}")")"
  assert_freshness yes "the most recent Refined event is taken order-independently" \
    "$(fx_issue "${T_CREATED}" "\"${T_EDITED}\"" "$(fx_label Refined "${T_RELABELED}"),$(fx_label Refined "${T_LABELED}")")"
  assert_freshness no "a later non-Refined label event never anchors freshness" \
    "$(fx_issue "${T_CREATED}" "\"${T_EDITED}\"" "$(fx_label Refined "${T_LABELED}"),$(fx_label Working "${T_RELABELED}")")"
  assert_freshness yes "an empty-string lastEditedAt falls back to createdAt (not a bare // short-circuit)" \
    "$(fx_issue "${T_CREATED}" '""' "$(fx_label Refined "${T_LABELED}")")"
  assert_freshness no "an empty-string lastEditedAt with a createdAt after the Refined event is not fresh" \
    "$(fx_issue "${T_EDITED}" '""' "$(fx_label Refined "${T_LABELED}")")"
  assert_freshness yes "a non-LabeledEvent timeline node ({}) is ignored" \
    "$(fx_issue "${T_CREATED}" null "{},$(fx_label Refined "${T_LABELED}")")"
  assert_freshness unknown "issue: null is unknown" \
    '{"data":{"repository":{"issue":null}}}'
  assert_freshness unknown "a response without repository.issue ({}) is unknown" \
    '{}'
  assert_freshness unknown "timelineItems.nodes null is unknown" \
    "{\"data\":{\"repository\":{\"issue\":{\"createdAt\":\"${T_CREATED}\",\"lastEditedAt\":null,\"timelineItems\":{\"nodes\":null}}}}}"
  assert_freshness unknown "timelineItems.nodes absent is unknown" \
    "{\"data\":{\"repository\":{\"issue\":{\"createdAt\":\"${T_CREATED}\",\"lastEditedAt\":null,\"timelineItems\":{}}}}}"
  assert_freshness unknown "createdAt and lastEditedAt both null beside a valid Refined event is unknown" \
    "{\"data\":{\"repository\":{\"issue\":{\"createdAt\":null,\"lastEditedAt\":null,\"timelineItems\":{\"nodes\":[$(fx_label Refined "${T_LABELED}")]}}}}}"
fi

# --- 6b. Call sites: the identical literal in both reading sites, ordered after
# the ticket-body fetch and the REST association read in §1.
assert_file_contains "${CONTEXT_GATHERER}" "${FRESHNESS_GQL_CALL}" \
  "§1 must carry the one read-only freshness GraphQL call, identical to the executed fixture filter (AC1, #1076)"
assert_file_contains "${PHASE1_PLAN}" "${FRESHNESS_GQL_CALL}" \
  "## Resume From Draft must re-derive refinedFresh: with the identical GraphQL call (AC6, #1076)"
assert_marker_precedes "${CONTEXT_GATHERER}" "${SECTION1_JSON_FIELDS}" "${FRESHNESS_GQL_CALL}" \
  "the freshness call must be ordered after the gh issue view --json body fetch (AC1, #1076)"
assert_marker_precedes "${CONTEXT_GATHERER}" "${SECTION1_ASSOCIATION_CALL}" "${FRESHNESS_GQL_CALL}" \
  "the freshness call must follow the REST author_association read (Files to Modify, #1076)"

GATHERER_ORDERING_MARKER='ordered after the `gh issue view --json` body fetch above, so an edit landing between the two reads renders `refinedFresh: no`'
GATHERER_BANNER_MARKER='the two read-only `gh api` calls in §1 — the REST issue `GET` and the GraphQL `query` (never a `mutation`)'
GATHERER_FOUR_CONDITIONS='one of its four trigger conditions'
GATHERER_THREE_CONDITIONS='one of its three trigger conditions'

assert_file_contains "${CONTEXT_GATHERER}" "${GATHERER_ORDERING_MARKER}" \
  "§1 must state the freshness read's ordering after the body fetch and why (edge: edit between the two reads yields no) (#1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${GATHERER_BANNER_MARKER}" \
  "the gh safety banner must name §1's two read-only gh api calls and bar a GraphQL mutation (flow/AGENTS.md #979 restatement site)"
assert_file_contains "${CONTEXT_GATHERER}" "${GATHERER_FOUR_CONDITIONS}" \
  "§1's consumer sentence must count four planner trigger conditions (#1076)"
assert_file_lacks "${CONTEXT_GATHERER}" "${GATHERER_THREE_CONDITIONS}" \
  "§1's consumer sentence must no longer say three trigger conditions (#1076)"

# --- 6c. Digest template line + rendering rule.
DIGEST_REFINEDFRESH_TEMPLATE='refinedFresh: yes | refinedFresh: no | refinedFresh: unknown — mandatory in ticket mode, omitted entirely in ticketless mode; never this placeholder text'
RF_MANDATORY_RULE='**The `refinedFresh:` line is mandatory in ticket mode.**'
RF_YES_RULE='`refinedFresh: yes` only when a `Refined` `LabeledEvent` is found and the body edit time (`lastEditedAt`, else `createdAt`) is at or before that event'"'"'s `createdAt`'
RF_NO_RULE='`refinedFresh: no` when the body edit time is later'
RF_UNKNOWN_RULE='`refinedFresh: unknown` on any failed or partial read, or when no `Refined` `LabeledEvent` is returned'
RF_NEVER_BODY_RULE='never derived from the ticket body or comment text'
RF_RENDERED_RULE='emit exactly one concrete `refinedFresh:` form and never the template'"'"'s placeholder wording or an alternation'
RF_VERDICT_RULE='any exit other than 0, or any output other than `yes` or `no`, renders `refinedFresh: unknown`, which is carried only by this line and never written to `errors:`'
RF_PRECEDENCE_RULE='for this one call a failed read renders `refinedFresh: unknown` and does not fill `errors:`, overriding the gh safety banner'"'"'s report-the-exact-error instruction and the closing rule'"'"'s fill-`errors:` instruction'
RF_BODY_ONLY_RULE='the binding covers the ticket body only: comments are never anchors, and a title edit is out of scope'

assert_file_contains "${CONTEXT_GATHERER}" "${DIGEST_REFINEDFRESH_TEMPLATE}" \
  "digest template must carry the mandatory refinedFresh: yes | no | unknown line, omitted in ticketless mode (AC2, #1076)"
assert_marker_precedes "${CONTEXT_GATHERER}" "${DIGEST_TICKETAUTHOR_TEMPLATE}" "${DIGEST_REFINEDFRESH_TEMPLATE}" \
  "the refinedFresh: template line must sit after the ticketAuthor: template line (#1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_MANDATORY_RULE}" \
  "must open a rendering-rule paragraph stating refinedFresh: is mandatory in ticket mode (AC2, #1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_YES_RULE}" \
  "rendering rule: yes only when a Refined LabeledEvent is found and the body edit time is at or before it (AC2, #1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_NO_RULE}" \
  "rendering rule: no when the body edit time is later (AC2, #1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_UNKNOWN_RULE}" \
  "rendering rule: unknown on a failed or partial read, or when no Refined LabeledEvent is returned (AC2, #1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_NEVER_BODY_RULE}" \
  "rendering rule: the value is never derived from ticket body or comment text (AC2, #1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_RENDERED_RULE}" \
  "rendering rule: the value is rendered, never echoed (#1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_VERDICT_RULE}" \
  "rendering rule: a non-zero exit or out-of-set output renders unknown, carried only by this line and never written to errors: (Q&A 3, #1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_PRECEDENCE_RULE}" \
  "rendering rule: a failed freshness read does not fill errors:, overriding the gh safety banner and the closing fill-errors: rule (flow/AGENTS.md #979, #1076)"
assert_file_contains "${CONTEXT_GATHERER}" "${RF_BODY_ONLY_RULE}" \
  "must record the binding covers the body only: comments are not anchors and title edits are out of scope (#1076)"

# --- 6d. Planner: the fourth condition, the three new fail-closed cases, the
# accepted residual, and the retired "three" wording.
PLANNER_COND_D='(d) the delegation forwards `refinedFresh: yes`'
PLANNER_ALL_FOUR_GATE='only when all four of the following hold'
PLANNER_ALL_FOUR_RECORD='When all four hold'
PLANNER_TRUST_CHANNEL_LINES='forwarded digest lines (`labels:`, `ticketAuthor:`, `refinedFresh:`)'
FC_REFINEDFRESH_NO='`refinedFresh: no` (the body was edited after the most recent `Refined` label)'
FC_REFINEDFRESH_UNKNOWN='`refinedFresh: unknown` (the freshness read failed or found no `Refined` label event)'
FC_REFINEDFRESH_ABSENT='an absent or malformed `refinedFresh:` line'
PLANNER_RESIDUAL='a bot or app with `issues:write` that edits the body and then re-applies `Refined` is not detected by the binding'

assert_file_contains "${PLANNER_AGENT}" "${PLANNER_COND_D}" \
  "settled-posture paragraph must name refinedFresh: yes as the fourth required condition (AC3, #1076)"
assert_file_contains "${PLANNER_AGENT}" "${PLANNER_ALL_FOUR_GATE}" \
  "settled-posture paragraph must gate on all four conditions (#1076)"
assert_file_contains "${PLANNER_AGENT}" "${PLANNER_ALL_FOUR_RECORD}" \
  "settled-posture paragraph must record the suppression only when all four hold (#1076)"
assert_file_lacks "${PLANNER_AGENT}" 'only when all three of the following hold' \
  "settled-posture paragraph must no longer gate on three conditions (#1076)"
assert_file_lacks "${PLANNER_AGENT}" 'When all three hold' \
  "settled-posture paragraph must no longer record the suppression on three conditions (#1076)"
assert_file_contains "${PLANNER_AGENT}" "${PLANNER_TRUST_CHANNEL_LINES}" \
  "the trust-channel parenthetical must list refinedFresh: among the forwarded digest lines (#1076)"
assert_file_contains "${PLANNER_AGENT}" "${FC_REFINEDFRESH_NO}" \
  "fail-closed case: refinedFresh: no (body edited after the most recent Refined label) must still ask (AC3, #1076)"
assert_file_contains "${PLANNER_AGENT}" "${FC_REFINEDFRESH_UNKNOWN}" \
  "fail-closed case: refinedFresh: unknown must still ask (AC3, #1076)"
assert_file_contains "${PLANNER_AGENT}" "${FC_REFINEDFRESH_ABSENT}" \
  "fail-closed case: an absent or malformed refinedFresh: line must still ask (AC3, #1076)"
assert_file_contains "${PLANNER_AGENT}" "${PLANNER_RESIDUAL}" \
  "must record the accepted residual: an issues:write bot/app that edits then re-applies Refined is not detected (AC4, #1076)"

# --- 6e. Implement skill storage, phase-1 forwarding (the forwarding bullet
# itself is DELEGATION_FORWARD_MARKER in Group 4 and P1_LABELS_LINE in
# plan-path-handoff-contract.test.sh) and the resume-path ordering.
SKILL_REFINEDFRESH_STORAGE='`refinedFresh:` — stored **verbatim** and retained for the whole session'
SKILL_REFINEDFRESH_NOT_ERROR='`refinedFresh: unknown` is never treated as a digest `errors:` value and never stops the run'
SKILL_LABELS_BACKREF='the label half of the planner'"'"'s provenance gate (see `ticketAuthor:` and `refinedFresh:` below'

assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_REFINEDFRESH_STORAGE}" \
  "\"From the digest, store:\" list must gain a refinedFresh: storage bullet (AC5, #1076)"
assert_marker_precedes "${IMPLEMENT_SKILL}" "${SKILL_TICKETAUTHOR_STORAGE_MARKER}" "${SKILL_REFINEDFRESH_STORAGE}" \
  "the refinedFresh: storage bullet must follow the ticketAuthor: bullet (#1076)"
assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_REFINEDFRESH_NOT_ERROR}" \
  "a refinedFresh: unknown must never be treated as a digest error or stop the run (Q&A 3, #1076)"
assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_LABELS_BACKREF}" \
  "the labels storage bullet's back-reference must name refinedFresh: too (#1076)"

RESUME_STEP4_HEADING='4. **Append the answers to `### Decisions` (HS-R9).**'
RESUME_STEP5_HEADING='5. **Check freshness, then re-delegate to the `planner` agent (HS-R10).**'
RESUME_BODY_FILE_EDIT='gh issue edit <number> --repo <owner>/<repo> --body-file ${TMPDIR:-/tmp}/cenci/issue-<number>-<session-uuid>-resume-body.md'
RESUME_STEP4_BODY_FETCH='gh issue view <number> --repo <owner>/<repo> --json body'
RESUME_RF_DEGRADE='a failed refinement-freshness read degrades to `refinedFresh: unknown`'
RESUME_RF_LATER_ROUND='a later round, or a retry after an earlier attempt'"'"'s append, renders `refinedFresh: no` and asks'
RESUME_RF_BEFORE_APPEND='the read happens before this step'"'"'s `gh issue edit ... --body-file` append'
RESUME_RF_STEP5_BACKREF='The `labels:`, `ticketAuthor:` and `refinedFresh:` lines read at the top of step 4 are forwarded on both the `fresh` and `stale`/`unknown` branches'

# The whole provenance paragraph is the first paragraph of step 4: after the
# step-4 heading, before step 4's body fetch and --body-file append, and no
# longer inside step 5.
assert_marker_precedes "${PHASE1_PLAN}" "${RESUME_STEP4_HEADING}" "${RESUME_PROVENANCE_HEADING}" \
  "the resume provenance paragraph must sit inside step 4, after its heading (AC6, #1076)"
assert_marker_precedes "${PHASE1_PLAN}" "${RESUME_PROVENANCE_HEADING}" "${RESUME_BODY_FILE_EDIT}" \
  "the resume provenance read must precede step 4's --body-file answer append, so the pipeline's own append is never mistaken for a post-refinement edit (AC6, #1076)"
assert_marker_precedes "${PHASE1_PLAN}" "${RESUME_PROVENANCE_HEADING}" "${RESUME_STEP5_HEADING}" \
  "the resume provenance paragraph must no longer sit under step 5 (#1076)"

# Nothing but the provenance paragraph's own position may separate it from the
# step-4 heading: the body fetch must come after the paragraph. sed prints the
# step-4-heading..provenance-heading range; a fetch inside it fails the assert.
step4_line="$(first_match_line_in_file "${PHASE1_PLAN}" "${RESUME_STEP4_HEADING}")"
prov_line="$(first_match_line_in_file "${PHASE1_PLAN}" "${RESUME_PROVENANCE_HEADING}")"
if [[ -z "${step4_line}" || -z "${prov_line}" ]]; then
  fail "phase-1-plan.md could not locate the step-4 heading and the provenance heading to bound the pre-fetch range (step4='${step4_line:-<missing>}'; provenance='${prov_line:-<missing>}')"
elif [[ "${prov_line}" -le "${step4_line}" ]]; then
  fail "phase-1-plan.md provenance heading (line ${prov_line}) must come after the step-4 heading (line ${step4_line}) to be its first paragraph"
elif sed -n "${step4_line},${prov_line}p" "${PHASE1_PLAN}" | grep -qF -- "${RESUME_STEP4_BODY_FETCH}"; then
  fail "phase-1-plan.md the step-4 body fetch ('${RESUME_STEP4_BODY_FETCH}') appears before the provenance read; the read must be step 4's first action (AC6, #1076)"
fi

assert_file_contains "${PHASE1_PLAN}" "${RESUME_RF_DEGRADE}" \
  "a failed resume-path freshness read must degrade to refinedFresh: unknown, never a hard stop (AC6, #1076)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_RF_LATER_ROUND}" \
  "a later resume round or a retry after a partial append must render no and ask (edge case, #1076)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_RF_BEFORE_APPEND}" \
  "must state the freshness read happens before step 4's --body-file append (AC6, #1076)"
assert_file_contains "${PHASE1_PLAN}" "${RESUME_RF_STEP5_BACKREF}" \
  "step 5 must keep a back-reference forwarding the three lines on both branches (AC6, #1076)"

# --- 6f. Refine: a re-refine removes then re-adds `Refined` so a fresh label
# event always postdates refine's own body edit.
REFINE_REMOVE_REFINED='gh issue edit <number> --repo <owner>/<repo> --remove-label "Refined"'
REFINE_REMOVE_VERIFIED='verified by a label re-fetch showing `Refined` absent'
REFINE_REMOVE_BEFORE_ADD='before the existing add-label command'
REFINE_ADD_REFINED_BROWSER='--add-label "Refined" --add-label "Browser"'
REFINE_PARTIAL_STATE='`Refined` is absent on ticket #`<number>`'
REFINE_WRITE_ORDER_RELABEL='on a re-refine the already-present `Refined` label is first removed, then re-added'
REFINE_CODEX_RELABEL_PLAIN='on a re-refine the already-present Refined label is first removed, then re-added'

assert_file_contains "${REFINE_SKILL}" "${REFINE_REMOVE_REFINED}" \
  "step 11 must run a standalone --remove-label \"Refined\" when the issue already carries Refined (AC7, #1076)"
assert_file_contains "${REFINE_SKILL}" "${REFINE_REMOVE_VERIFIED}" \
  "step 11 must verify the standalone Refined removal by label re-fetch (AC7, #1076)"
assert_file_contains "${REFINE_SKILL}" "${REFINE_REMOVE_BEFORE_ADD}" \
  "step 11 must state the removal runs before the existing add-label command (AC7, #1076)"
assert_marker_precedes "${REFINE_SKILL}" "${REFINE_REMOVE_REFINED}" "${REFINE_ADD_REFINED_BROWSER}" \
  "the standalone Refined removal must precede the step-11 add-label command (AC7, #1076)"
assert_file_contains "${REFINE_SKILL}" "${REFINE_PARTIAL_STATE}" \
  "the step-11 write-failure partial-state report must name Refined as absent on the ticket (edge case, #1076)"
assert_file_contains "${REFINE_SKILL}" "${REFINE_WRITE_ORDER_RELABEL}" \
  "write-order item 6 must note the re-refine remove-then-add (#1076)"

assert_file_contains "${REFINE_CODEX}" "${REFINE_REMOVE_REFINED}" \
  "codex.md's label-application prose must state the standalone --remove-label \"Refined\" literal (AC7, #1076)"
assert_file_contains "${REFINE_CODEX}" "${REFINE_REMOVE_VERIFIED}" \
  "codex.md must state the standalone Refined removal is verified by label re-fetch (AC7, #1076)"
assert_file_contains "${REFINE_CODEX}" "${REFINE_REMOVE_BEFORE_ADD}" \
  "codex.md must state the removal runs before the existing add-label command (AC7, #1076)"
# Both the write-order summary and the ### Write order prose block carry the re-label.
assert_occurs_exactly "${REFINE_CODEX}" "${REFINE_CODEX_RELABEL_PLAIN}" 2 \
  "codex.md's write-order summary and its ### Write order block must each state the re-refine re-label, in plain words (AC7, #1076)"

# --- 6g. docs/autonomous-loop.md settled-posture paragraph.
AUTOLOOP_BODY_EDIT_MARKER='a body edited after the latest `Refined` label falls back to asking'
AUTOLOOP_REREFINE_MARKER='re-refining restores suppression'
AUTOLOOP_RESIDUAL_MARKER='a bot or app with `issues:write` that edits the body and then re-applies `Refined` is not detected'

assert_file_contains "${AUTONOMOUS_LOOP_DOC}" "${AUTOLOOP_BODY_EDIT_MARKER}" \
  "docs/autonomous-loop.md must state a body edited after the latest Refined label falls back to asking (AC8, #1076)"
assert_file_contains "${AUTONOMOUS_LOOP_DOC}" "${AUTOLOOP_REREFINE_MARKER}" \
  "docs/autonomous-loop.md must state re-refining restores suppression (AC8, #1076)"
assert_file_contains "${AUTONOMOUS_LOOP_DOC}" "${AUTOLOOP_RESIDUAL_MARKER}" \
  "docs/autonomous-loop.md must record the accepted issues:write residual (#1076)"

if [[ "${failures}" -gt 0 ]]; then
  echo "planning-provenance-contract.test.sh: failures=${failures}" >&2
  exit 1
fi
echo "planning-provenance-contract.test.sh: failures=0"
