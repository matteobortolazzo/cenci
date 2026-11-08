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
# Ticket #1131 (lean decide-and-notify) widened the value set with a fifth
# value, `lean-decided`: a plan carrying at least one planner `decided:` entry.
# It replaces `lean` on the Lean Approval Path and `lean-resumed` on Resume
# From Draft, overrides `human` on the lean `## New Plan` fallback, and takes
# a third plan-comment banner form that names the planner decisions. The
# #1131 assertions below also rewrote the markers that pinned the two-form
# banner rule (`every approval value other than human ...`).
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
#   - Ticket #1464: `pending-human` replaces `human` as the `## New Plan`
#     front-matter value, every `cenci pipeline plan` persist site in
#     phase-1-plan.md passes its `--authorization` kind, phase-2-worktree.md
#     step 3 is the authorization gate, SKILL.md says a reviewed plan is
#     `held`, and codex.md records `held` only (no lean approval path).
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
# Section-scoped helpers for the #1131 `lean-decided` assertions below. Pure
# extractors (no fail() inside $(...)) plus nameref wrappers that fail closed,
# mirroring lean-planning-contract.test.sh; no `read_*`-named function calls
# fail(), so flow/tests/read-helper-purity-contract.test.sh stays satisfied.
# =====================================================================

# load_content <file> -- pure read, no fail() side effect.
load_content() {
  cat "$1" 2>/dev/null
}

# require_content <result-var> <file> <label> -- nameref wrapper; must NOT be
# invoked via $(...).
require_content() {
  local -n _res="$1"
  local _c
  if ! _c="$(load_content "$2")"; then
    fail "$3: could not read file: $2"
    _res=""
    return 1
  fi
  _res="${_c}"
}

# extract_section <content> <exact-heading-line> -- pure, fence-aware: the body
# of the named "## <heading>" section through the next real (unfenced) "## "
# heading, or EOF.
extract_section() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# require_section <result-var> <content> <exact-heading-line> <label>
require_section() {
  local -n _res="$1"
  local _body
  _body="$(extract_section "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section"
    _res=""
    return 1
  fi
  _res="${_body}"
}

# normalize_ws <content> -- collapse newlines and repeated whitespace so a
# markdown-wrapped sentence matches as one substring.
normalize_ws() {
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws <content> <needle> <label>
assert_contains_ws() {
  local content="$1" needle="$2" label="$3"
  [[ -n "${needle}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${needle}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${needle}]"
}

# assert_absent_paired_ws <content> <replacement-marker> <forbidden> <label> --
# a bare absence check passes vacuously while the replacement text is still
# unauthored; requiring the replacement's own presence first makes the check
# fail for the right reason until the edit lands.
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  [[ -n "${forbidden}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  if [[ "${norm}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- replacement marker missing (whitespace-normalized): [${marker}]"
    return
  fi
  [[ "${norm}" != *"${forbidden}"* ]] || fail "${label}: retired text still present (whitespace-normalized): [${forbidden}]"
}

# assert_banner_then_marker <region> <exact-banner-line> <exact-marker-line> <label>
# -- some line equal to the banner must be followed (after blank lines only) by
# the marker on its own line, so the banner variant was never folded into the
# marker line and the marker stays non-blockquoted.
assert_banner_then_marker() {
  local region="$1" banner="$2" marker="$3" label="$4"
  [[ -n "${banner}" && -n "${marker}" ]] || { fail "${label}: empty pattern (test bug)"; return; }
  local verdict
  verdict="$(awk -v b="${banner}" -v m="${marker}" '
    { t = $0; sub(/^[ \t]+/, "", t); sub(/[ \t]+$/, "", t) }
    seen && t == "" { next }
    seen { if (t == m) ok = 1; seen = 0 }
    t == b { seen = 1 }
    END { print ok ? "ok" : "missing" }
  ' <<<"${region}")"
  [[ "${verdict}" == "ok" ]] || fail "${label}: banner [${banner}] is not followed by the marker [${marker}] on its own line"
}

# =====================================================================
# phase-1-plan.md — the front-matter key and its value table.
# =====================================================================

# The template is what an implementing agent copies, so the key has to be in
# it, not only described in the prose below it. #1131 widened the value set
# with `lean-decided` (a plan carrying at least one planner `decided:` entry).
FRONTMATTER_KEY_MARKER='approval: pending-human | lean | trivial | lean-resumed | lean-decided'
assert_file_contains "${PHASE1_PLAN}" "${FRONTMATTER_KEY_MARKER}" "front-matter template must carry the approval key"

# The value table is the single authority on which path writes which value.
# Each row is asserted separately: a table that documents three of four
# values is exactly the silent gap this ticket exists to close.
APPROVAL_PENDING_ROW='`pending-human` | `## New Plan`'
APPROVAL_LEAN_ROW='`lean` | `## Lean Approval Path`'
APPROVAL_TRIVIAL_ROW='`trivial` | `## Trivial Fast Path`'
APPROVAL_RESUMED_ROW='`lean-resumed` | `## Resume From Draft`'
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_PENDING_ROW}" "approval table must document the pending-human value (#1464)"
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_LEAN_ROW}" "approval table must document the lean value"
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_TRIVIAL_ROW}" "approval table must document the trivial value"
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_RESUMED_ROW}" "approval table must document the lean-resumed value"

# #1131: the fifth value. A plan carrying at least one planner `decided:` entry
# records `lean-decided` -- the human saw the decision comment / PR section, not
# the plan -- on every path that can persist one (Lean Approval Path, Resume
# From Draft, and the lean `## New Plan` fallback).
APPROVAL_DECIDED_ROW='`lean-decided` | any path that persists a plan carrying at least one `decided:` entry'
assert_file_contains "${PHASE1_PLAN}" "${APPROVAL_DECIDED_ROW}" "approval table must document the lean-decided value"
assert_file_contains "${PHASE1_PLAN}" 'or the lean `## New Plan` fallback (replaces `pending-human`)' "lean-decided row must say the New Plan fallback replaces pending-human (#1464)"
assert_file_lacks "${PHASE1_PLAN}" 'or the lean `## New Plan` fallback (replaces `human`)' "lean-decided row must drop the retired human replacement (#1464)"

if require_content PHASE1_CONTENT "${PHASE1_PLAN}" "phase-1-plan.md" \
   && require_section PERSIST_SECTION "${PHASE1_CONTENT}" "## Persist the Plan" "phase-1-plan.md"; then
  # The precedence sentence: lean-decided wins over every other non-trivial value,
  # and a trivial plan can never carry a decision (the planner is skipped).
  DECIDED_PRECEDENCE_MARKER='A plan carrying at least one `decided:` entry records `lean-decided`, which overrides `lean`, `lean-resumed` and `pending-human`; `trivial` can never carry a decision'
  assert_contains_ws "${PERSIST_SECTION}" "${DECIDED_PRECEDENCE_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must state lean-decided overrides lean, lean-resumed and pending-human, and trivial never carries a decision"

  # #1464: the front-matter value never carries human provenance. `human` is a
  # readable legacy value only; the `## New Plan` default is `pending-human`, and
  # a `lean-decided` fallback plan is still recorded `held` in the state record.
  PENDING_DEFAULT_MARKER='This section'"'"'s own default is `pending-human`'
  LEGACY_HUMAN_MARKER='`human` stays a readable value for plans written before #1464, but no path writes it into front matter'
  DECIDED_STILL_HELD_MARKER='A `lean-decided` plan persisted through the `## New Plan` fallback is still recorded `held`'
  assert_contains_ws "${PERSIST_SECTION}" "${PENDING_DEFAULT_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) default must be pending-human, not human (#1464)"
  assert_absent_paired_ws "${PERSIST_SECTION}" "${PENDING_DEFAULT_MARKER}" 'This section'"'"'s own default is `human`' \
    "phase-1-plan.md (## Persist the Plan) must drop the retired human default (#1464)"
  assert_contains_ws "${PERSIST_SECTION}" "${LEGACY_HUMAN_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must state human is legacy-readable and never written (#1464)"
  assert_contains_ws "${PERSIST_SECTION}" "${DECIDED_STILL_HELD_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must state a lean-decided New Plan fallback is still held (#1464)"

  # "the three autonomous values" became four once lean-decided joined lean,
  # trivial and lean-resumed.
  FOUR_AUTONOMOUS_MARKER='the four autonomous values are written only by the paths that name them'
  RETIRED_THREE_AUTONOMOUS_MARKER='the three autonomous values'
  assert_contains_ws "${PERSIST_SECTION}" "${FOUR_AUTONOMOUS_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) default-is-human sentence must count four autonomous values"
  assert_absent_paired_ws "${PERSIST_SECTION}" "${FOUR_AUTONOMOUS_MARKER}" "${RETIRED_THREE_AUTONOMOUS_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must drop the stale three-autonomous-values count"

  # The Q&A template documents the decided: form next to auto-adopted:.
  QA_DECIDED_TEMPLATE_MARKER='planner decisions as `decided: <answer> — <rationale> (alternative: <option label>)`'
  assert_contains_ws "${PERSIST_SECTION}" "${QA_DECIDED_TEMPLATE_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) ## Q&A from Planning template must document the decided: form"
fi

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

# #1131: lean-decided is stated where it is written, at both autonomous write
# sites that can carry decisions -- scoped to each section, because the bare
# phrase would otherwise be satisfied by the value table alone.
DECIDED_SITE_MARKER='front matter carries `approval: lean-decided`'
if require_section LEAN_SECTION "${PHASE1_CONTENT}" "## Lean Approval Path" "phase-1-plan.md"; then
  assert_contains_ws "${LEAN_SECTION}" "${DECIDED_SITE_MARKER}" \
    "Lean Approval Path must state its own lean-decided approval value (replaces lean when there is a decision)"
fi
if require_section RESUME_SECTION "${PHASE1_CONTENT}" "## Resume From Draft" "phase-1-plan.md"; then
  assert_contains_ws "${RESUME_SECTION}" "${DECIDED_SITE_MARKER}" \
    "Resume From Draft must state its own lean-decided approval value (replaces lean-resumed when there is a decision)"
fi

# =====================================================================
# #1464 -- every `cenci pipeline plan` persist site passes its authorization
# kind. The kind is the record `cenci dispatch` reads before it launches a plan
# unattended: a held plan is released only by a human, an autonomous path
# records the kind that explains why no human did. A site that omits it leaves
# an `unrecorded` plan that dispatch holds visibly. Each site is asserted by
# its own case, scoped to its own section and matched as a whole trimmed line,
# so the table or another site's call can never satisfy it.
# =====================================================================

# assert_has_fence_line <region> <exact-line> <label> -- some line of the region,
# trimmed, equals the exact fenced command line.
assert_has_fence_line() {
  local region="$1" exact="$2" label="$3"
  [[ -n "${exact}" ]] || { fail "${label}: empty line (test bug)"; return; }
  local verdict
  verdict="$(awk -v c="${exact}" '
    { t = $0; sub(/^[ \t]+/, "", t); sub(/[ \t]+$/, "", t) }
    t == c { found = 1 }
    END { print found ? "ok" : "missing" }
  ' <<<"${region}")"
  [[ "${verdict}" == "ok" ]] || fail "${label}: no fenced line equals [${exact}]"
}

if [[ -n "${PHASE1_CONTENT:-}" ]]; then
  if require_section TRIVIAL_SECTION "${PHASE1_CONTENT}" "## Trivial Fast Path" "phase-1-plan.md (#1464)"; then
    assert_has_fence_line "${TRIVIAL_SECTION}" 'cenci pipeline plan <id> --authorization trivial' \
      "Trivial Fast Path must record the trivial authorization kind"
  fi
  if [[ -n "${LEAN_SECTION:-}" ]]; then
    assert_has_fence_line "${LEAN_SECTION}" 'cenci pipeline plan <id> --authorization lean' \
      "Lean Approval Path must record the lean authorization kind"
    assert_contains_ws "${LEAN_SECTION}" 'When the plan carries at least one `decided:` entry, pass `--authorization lean-decided` instead of `--authorization lean`' \
      "Lean Approval Path must record lean-decided when the plan carries a decision"
  fi
  if [[ -n "${RESUME_SECTION:-}" ]]; then
    assert_has_fence_line "${RESUME_SECTION}" 'cenci pipeline plan <id> --authorization lean-resumed' \
      "Resume From Draft must record the lean-resumed authorization kind"
    assert_contains_ws "${RESUME_SECTION}" 'When the plan carries at least one `decided:` entry, pass `--authorization lean-decided` instead of `--authorization lean-resumed`' \
      "Resume From Draft must record lean-decided when the plan carries a decision"
  fi
  if [[ -n "${PERSIST_SECTION:-}" ]]; then
    assert_has_fence_line "${PERSIST_SECTION}" 'cenci pipeline plan <id> --authorization held' \
      "Persist the Plan (the ## New Plan path) must record the held authorization kind"
    assert_contains_ws "${PERSIST_SECTION}" 'never `human`' \
      "Persist the Plan must say the ## New Plan path never records human"
  fi
  if require_section NEWPLAN_SECTION "${PHASE1_CONTENT}" "## New Plan" "phase-1-plan.md (#1464)"; then
    assert_contains_ws "${NEWPLAN_SECTION}" 'cenci pipeline plan <id> --authorization held' \
      "## New Plan must name the held authorization its persist step records"
  fi
  if require_section EXISTING_SECTION "${PHASE1_CONTENT}" "## Existing Plan" "phase-1-plan.md (#1464)"; then
    assert_contains_ws "${EXISTING_SECTION}" 'Invoke `cenci pipeline plan <id>` with no `--authorization` flag' \
      "Existing Plan step 3 re-records an existing plan and must pass no --authorization flag"
  fi
  # No bare plan fence line survives anywhere, and --approve never takes a kind.
  assert_file_lacks "${PHASE1_PLAN}" 'plan <id> --approve --authorization' \
    "phase-1-plan.md: --approve is never combined with --authorization (usage error, #1464)"
  assert_file_lacks "${PHASE1_PLAN}" 'plan <id> --authorization human' \
    "phase-1-plan.md: no path passes --authorization human (written only by dispatch approve or an attended --approve, #1464)"
  BARE_FENCE_COUNT="$(grep -cE '^[[:space:]]*cenci pipeline plan <id>[[:space:]]*$' <<<"${PHASE1_CONTENT}")"
  [[ "${BARE_FENCE_COUNT}" -eq 0 ]] || \
    fail "phase-1-plan.md: ${BARE_FENCE_COUNT} bare 'cenci pipeline plan <id>' fence line(s) left without an --authorization kind"
fi

# phase-2-worktree.md: step 3 is the authorization gate, passes no kind, and
# names the release command in its stop message.
PHASE2_WORKTREE="${FLOW_DIR}/skills/implement/phases/phase-2-worktree.md"
if require_content PHASE2_CONTENT "${PHASE2_WORKTREE}" "phase-2-worktree.md"; then
  PHASE2_STEP3="$(awk '/^3\. Ticket mode: /{on=1} on&&/^## Create Worktree/{exit} on{print}' <<<"${PHASE2_CONTENT}")"
  if [[ -z "${PHASE2_STEP3}" ]]; then
    fail "phase-2-worktree.md: could not locate Gate Check step 3"
  else
    assert_contains_ws "${PHASE2_STEP3}" 'Invoke `cenci pipeline plan <id> --approve`' \
      "phase-2-worktree.md step 3 must still invoke plan --approve (with no --authorization kind)"
    assert_contains_ws "${PHASE2_STEP3}" '`plan --approve` takes no `--authorization` kind' \
      "phase-2-worktree.md step 3 must say --approve passes no kind"
    assert_contains_ws "${PHASE2_STEP3}" 'An attended session records `human` (source `attended-launch`)' \
      "phase-2-worktree.md step 3 must say an attended launch records human approval"
    assert_contains_ws "${PHASE2_STEP3}" 'an unattended session refuses a `held`, changed or unrecorded plan' \
      "phase-2-worktree.md step 3 must say an unattended run refuses held, changed and unrecorded plans"
    assert_contains_ws "${PHASE2_STEP3}" 'run `cenci dispatch approve <id>`' \
      "phase-2-worktree.md step 3 stop message must name cenci dispatch approve <id>"
    assert_absent_paired_ws "${PHASE2_STEP3}" 'run `cenci dispatch approve <id>`' 'is what this step records as approval' \
      "phase-2-worktree.md step 3 must drop the launch-is-approval claim (#1464)"
  fi
fi

# SKILL.md: the pipeline overview says a reviewed plan is held, not approved.
assert_file_lacks "${PHASE2_WORKTREE}" 'plan <id> --approve --authorization' \
  "phase-2-worktree.md: --approve never takes an --authorization kind (#1464)"

SKILL_MD="${FLOW_DIR}/skills/implement/SKILL.md"
if require_content SKILL_MD_CONTENT "${SKILL_MD}" "SKILL.md"; then
  assert_contains_ws "${SKILL_MD_CONTENT}" 'a reviewed plan is `held` until released by `cenci dispatch approve` or an attended plan-file launch' \
    "SKILL.md pipeline overview must say a saved plan is held until released (#1464)"
fi

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

# #1131: the third banner form. A plan with planner decisions says so at the top
# of its comment. It deliberately avoids the words "auto-approved": a lean
# plan persisted through the `## New Plan` fallback still stops for human
# review, so "auto-approved" would be false for it.
BANNER_DECIDED_MARKER='> 🤖 **cenci** — implementation plan posted by `/cenci:implement` (planning — with planner decisions; see the decision comment, overrule on the PR).'
assert_file_contains "${PHASE1_PLAN}" "${BANNER_DECIDED_MARKER}" "the planner-decisions banner variant must be present"

# The selection rule has to be explicit and three-way: lean, trivial and
# lean-resumed take the auto-approved banner, lean-decided takes the decisions
# banner, human keeps the original -- not guessable from the literals alone.
# The #1050 two-way rule ("every value other than human") is now false.
BANNER_SELECTION_MARKER='every `approval` value other than `human`, `pending-human` and `lean-decided` uses the auto-approved variant, and `lean-decided` uses the planner-decisions variant'
RETIRED_BANNER_SELECTION_MARKER='every `approval` value other than `human` and `lean-decided` uses the auto-approved variant'
BANNER_PENDING_MARKER='A `pending-human` plan takes the plain `(planning)` banner'
BANNER_THREE_FORMS_MARKER='The banner has three forms, selected by this plan'"'"'s `approval` value'
RETIRED_BANNER_TWO_FORMS_MARKER='The banner has two forms'
assert_file_contains "${PHASE1_PLAN}" "${BANNER_SELECTION_MARKER}" "the banner selection rule must be stated"
assert_file_contains "${PHASE1_PLAN}" "${BANNER_THREE_FORMS_MARKER}" "the banner must be documented as having three forms"
if [[ -n "${PERSIST_SECTION:-}" ]]; then
  assert_absent_paired_ws "${PERSIST_SECTION}" "${BANNER_SELECTION_MARKER}" "${RETIRED_BANNER_SELECTION_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must drop the retired two-way selection rule"
  assert_contains_ws "${PERSIST_SECTION}" "${BANNER_PENDING_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must say a pending-human plan takes the plain (planning) banner, never the auto-approved one (#1464)"
  assert_absent_paired_ws "${PERSIST_SECTION}" "${BANNER_THREE_FORMS_MARKER}" "${RETIRED_BANNER_TWO_FORMS_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must drop the retired two-forms wording"
  # The third form keeps the marker on its own non-blockquoted line, exactly
  # like the other two -- the banner variant must not be folded into it.
  assert_banner_then_marker "${PERSIST_SECTION}" "${BANNER_DECIDED_MARKER}" '<!-- cenci-plan-comment -->' \
    "phase-1-plan.md (## Persist the Plan) planner-decisions banner form"
else
  fail "phase-1-plan.md (## Persist the Plan) section unavailable for the banner-form checks"
fi

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
assert_file_contains "${ATTRIBUTION_DOC}" "${BANNER_DECIDED_MARKER}" "the attribution registry must record the planner-decisions banner variant (AC: all three banner forms documented)"
ATTRIBUTION_VARIANT_RULE='`plan-comment` is the one kind with three banner forms'
RETIRED_ATTRIBUTION_VARIANT_RULE='`plan-comment` is the one kind with two banner forms'
assert_file_contains "${ATTRIBUTION_DOC}" "${ATTRIBUTION_VARIANT_RULE}" "the attribution registry must explain why plan-comment has three forms"
# The registry's selection rule must place lean-decided with the third form and
# stop listing it among the auto-approved values (the old sentence named
# `lean`, `trivial`, `lean-resumed` as "every other" value).
ATTRIBUTION_DECIDED_RULE='A `lean-decided` plan uses the planner-decisions variant instead'
if require_content ATTRIBUTION_CONTENT "${ATTRIBUTION_DOC}" "comment-attribution.md"; then
  assert_contains_ws "${ATTRIBUTION_CONTENT}" "${ATTRIBUTION_DECIDED_RULE}" \
    "the attribution registry must say a lean-decided plan uses the planner-decisions banner variant"
  assert_absent_paired_ws "${ATTRIBUTION_CONTENT}" "${ATTRIBUTION_VARIANT_RULE}" "${RETIRED_ATTRIBUTION_VARIANT_RULE}" \
    "the attribution registry must drop the retired two-banner-forms heading"
fi

# =====================================================================
# Client surfaces (flow/docs/skill-authoring.md): a change to a skill is not
# finished until all three clients are reconciled — updated, or consciously
# excluded for a reason that is written down. These two assertions are the
# written-down half.
# =====================================================================

CODEX_EXCLUSION_MARKER='every plan it persists is recorded `held`'
assert_file_contains "${IMPLEMENT_CODEX}" "${CODEX_EXCLUSION_MARKER}" "the Codex procedure must record why it needs no approval-value branch"
assert_file_lacks "${IMPLEMENT_CODEX}" 'is `approval: human` by construction' "the Codex procedure must drop the retired human-by-construction claim (#1464)"
# No path writes `approval: human` any more, so the interactive-resolution rule
# must not name it as the value those resolutions leave alone.
assert_file_lacks "${IMPLEMENT_CODEX}" 'change `approval: human`' "codex.md must not name the retired approval: human value as the plan's approval (#1464)"
assert_file_contains "${IMPLEMENT_CODEX}" "These resolutions do not post GitHub decision comments or change the plan's \`approval\` value." "codex.md interactive resolutions must leave the plan's approval value unchanged (#1464)"
# Codex gains no lean approval path: the only kind it ever passes is held.
for retired_kind in lean trivial lean-resumed lean-decided human; do
  assert_file_lacks "${IMPLEMENT_CODEX}" "--authorization ${retired_kind}" "codex.md must pass no --authorization ${retired_kind} (no lean approval path, #1464)"
done
assert_file_contains "${IMPLEMENT_CODEX}" '`cenci pipeline plan <id> --authorization held`' "codex.md new-plan apply must record the held authorization (#1464)"

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
