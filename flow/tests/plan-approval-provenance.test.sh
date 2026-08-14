#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #1050: the `approval`
# front-matter key that records *who* approved a persisted plan, and the
# plan-comment banner variant that surfaces it on the ticket.
#
# Before #1050 the four approving paths were indistinguishable after the
# fact: every one wrote `status: planned`, the Lean Approval Path called
# `cenci pipeline plan <id> --approve` exactly as a human-approved run does
# ("no separate lean-mode approval call, no new CLI flag"), and the plan
# comment's banner was byte-identical either way. `## Q&A from Planning`'s
# `auto-adopted:` entries were the closest available signal, but interactive
# mode emits those too (entailment-derived, per agents/planner.md), so their
# presence never proved lean mode. An operator reading a merged ticket months
# later could not tell whether a human had ever looked at the plan.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/lean-planning-contract.test.sh: `set -uo pipefail`, a `failures`
# counter, `assert_file_contains`/`assert_file_lacks` helpers built on
# `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob — no registration needed. No `read_*` helpers, so this file
# is trivially compliant with flow/tests/read-helper-purity-contract.test.sh's
# repo-wide scan.
#
# Marker precision (docs/shell-scripting-gotchas.md rule 3 — assert the
# specific replacement text at the edit site, never a generic marker that may
# already match unrelated prose): a bare `approval:` would match nothing
# meaningful and a bare `approval` recurs throughout the file's existing
# plan-approval prose, so every marker below pins a distinctive full phrase
# from the edit site rather than the bare key name.
#
# Covered files:
#   - flow/skills/implement/phases/phase-1-plan.md (front-matter template,
#     the `approval` value table, each of the four approving paths' own
#     value, the awaiting-input draft's no-key rule, and the plan-comment
#     banner variant)
#   - flow/docs/comment-attribution.md (the plan-comment banner's two forms)
#   - flow/skills/implement/codex.md (the recorded Codex exclusion)
#   - flow/opencode/install-skills.sh (the recorded OpenCode exclusion)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "plan-approval-provenance.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "plan-approval-provenance.test.sh: failed to resolve flow directory." >&2; exit 2; }
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
ATTRIBUTION_DOC="${FLOW_DIR}/docs/comment-attribution.md"
IMPLEMENT_CODEX="${FLOW_DIR}/skills/implement/codex.md"
OPENCODE_INSTALL="${FLOW_DIR}/opencode/install-skills.sh"
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
# phase-1-plan.md — the front-matter key and its value table.
# =====================================================================

# The template is what an implementing agent copies, so the key has to be in
# it, not only described in the prose below it.
FRONTMATTER_KEY_MARKER='approval: human | lean | trivial | lean-resumed'
assert_file_contains "${PHASE1_PLAN}" "${FRONTMATTER_KEY_MARKER}" "front-matter template must carry the approval key"

# The value table is the single authority on which path writes which value.
# Each row is asserted separately: a table that documents three of four
# values is exactly the silent gap this ticket exists to close.
APPROVAL_HUMAN_ROW='`human` | `## New Plan`'
APPROVAL_LEAN_ROW='`lean` | `## Lean Approval Path`'
APPROVAL_TRIVIAL_ROW='`trivial` | `## Trivial Fast Path`'
APPROVAL_RESUMED_ROW='`lean-resumed` | `## Resume From Draft`'
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_HUMAN_ROW}" "approval table must document the human value"
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_LEAN_ROW}" "approval table must document the lean value"
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_TRIVIAL_ROW}" "approval table must document the trivial value"
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_RESUMED_ROW}" "approval table must document the lean-resumed value"

# An awaiting-input draft is not an approved plan, so it must carry no key at
# all rather than a placeholder value a consumer could misread as approved.
DRAFT_NO_KEY_MARKER='carries no `approval` key at all — it is not an approved plan yet'
assert_file_contains "${PHASE1_PLAN}" "${DRAFT_NO_KEY_MARKER}" "awaiting-input draft must be documented as carrying no approval key"

# Backward compatibility: every plan written before this key existed has no
# `approval`, and no consumer may treat that as corruption.
BACKCOMPAT_MARKER='A plan file with no `approval` key is never malformed'
assert_file_contains "${PHASE1_PLAN}" "${BACKCOMPAT_MARKER}" "the absent-key case must be documented as valid, not malformed"

# =====================================================================
# phase-1-plan.md — each approving path names its own value at its own
# write site. The value table above documents the mapping; these assertions
# prove the mapping is actually stated where the plan is written, so an
# agent following one path in isolation cannot miss it.
# =====================================================================

TRIVIAL_SITE_MARKER='front matter carries `approval: trivial`'
LEAN_SITE_MARKER='front matter carries `approval: lean`'
RESUMED_SITE_MARKER='front matter carries `approval: lean-resumed`'
assert_file_contains "${PHASE1_PLAN}" "${TRIVIAL_SITE_MARKER}" "Trivial Fast Path must state its own approval value"
assert_file_contains "${PHASE1_PLAN}" "${LEAN_SITE_MARKER}" "Lean Approval Path must state its own approval value"
assert_file_contains "${PHASE1_PLAN}" "${RESUMED_SITE_MARKER}" "Resume From Draft must state its own approval value"

# =====================================================================
# phase-1-plan.md — the plan-comment banner variant.
# =====================================================================

# The `human` banner is unchanged: an existing-behavior regression guard, so
# a future edit cannot quietly relabel human-approved plans as autonomous.
BANNER_HUMAN_MARKER='> 🤖 **cenci** — implementation plan posted by `/cenci:implement` (planning).'
assert_file_contains "${PHASE1_PLAN}" "${BANNER_HUMAN_MARKER}" "the human-approval banner must stay byte-identical"

# The autonomous banner is the whole visible point of the ticket: the ticket
# reader must see the provenance without opening .plans/.
BANNER_AUTO_MARKER='> 🤖 **cenci** — implementation plan posted by `/cenci:implement` (planning — auto-approved, no human review).'
assert_file_contains "${PHASE1_PLAN}" "${BANNER_AUTO_MARKER}" "the auto-approved banner variant must be present"

# The selection rule has to be explicit — three of the four values take the
# autonomous banner, which is not guessable from the two literals alone.
BANNER_SELECTION_MARKER='every `approval` value other than `human` uses the auto-approved variant'
assert_file_contains "${PHASE1_PLAN}" "${BANNER_SELECTION_MARKER}" "the banner selection rule must be stated"

# The marker is unchanged and must stay on its own non-blockquoted line —
# the banner variant must not have been folded into the marker line.
PLAN_COMMENT_MARKER='<!-- cenci-plan-comment -->'
assert_file_contains "${PHASE1_PLAN}" "${PLAN_COMMENT_MARKER}" "the plan-comment marker must be unchanged"

# =====================================================================
# comment-attribution.md — the registry must describe both banner forms,
# since it is the doc a future call site is required to update in the same
# PR.
# =====================================================================

assert_file_contains "${ATTRIBUTION_DOC}" "${BANNER_AUTO_MARKER}" "the attribution registry must record the auto-approved banner variant"
ATTRIBUTION_VARIANT_RULE='`plan-comment` is the one kind with two banner forms'
assert_file_contains "${ATTRIBUTION_DOC}" "${ATTRIBUTION_VARIANT_RULE}" "the attribution registry must explain why plan-comment has two forms"

# =====================================================================
# Client surfaces (flow/docs/skill-authoring.md): a change to a skill is not
# finished until all three clients are reconciled — updated, or consciously
# excluded for a reason that is written down. These two assertions are the
# written-down half.
# =====================================================================

CODEX_EXCLUSION_MARKER='every plan it persists is `approval: human` by construction'
assert_file_contains "${IMPLEMENT_CODEX}" "${CODEX_EXCLUSION_MARKER}" "the Codex procedure must record why it needs no approval-value branch"

# OpenCode needs no edit for this ticket — `implement`'s exclusion is already
# recorded next to PORTABLE_SKILLS, and this assertion pins that it stays
# recorded rather than silently decaying into an undecided omission.
OPENCODE_EXCLUSION_MARKER='Pipeline/interactive skills (implement,'
assert_file_contains "${OPENCODE_INSTALL}" "${OPENCODE_EXCLUSION_MARKER}" "install-skills.sh must keep recording implement's exclusion from OpenCode delivery"

if [[ "${failures}" -gt 0 ]]; then
  echo "plan-approval-provenance.test.sh: failures=${failures}" >&2
  exit 1
fi
echo "plan-approval-provenance.test.sh: failures=0"
