#!/usr/bin/env bash
# Contract test for ticket #965 — `/cenci:configure` never surfaces
# `planning.autonomy` or the `automerge` policy block, so the two
# highest-leverage autonomy settings in the product are only discoverable by
# reading docs/autonomous-loop.md. This suite pins the new `### Autonomy
# Settings` section's load-bearing prose in `flow/skills/configure/SKILL.md`
# (questions 13/14, the absent-only rule with its `projects[]`-aware
# `automerge` presence clause, the conservative-default/Recommended wording
# for both questions, the declined-question-writes-nothing sentences, the
# fenced JSON scaffold template between stable extraction markers, the step
# 6 wiring sentence naming `planningAnswer`/`automergeAnswer` — the
# AGENTS.md #824 "validated but actually consumed" rule — the PR-body
# "Review required" section, and the fleet-side next-step verbs), the
# `codex.md` parity paragraph, and the `docs/autonomous-loop.md` claim
# update (the offer-when-absent behavior replacing the old "never writes or
# removes this block" claim).
#
# Follows the exact idiom of flow/tests/configure-dispatch-enrollment.test.sh
# (the direct precedent for "new configure section + its contract test" per
# the plan's Architectural Context): pinned exact authored substrings as
# constants (the red phase fails for the right reason, and green has an
# unambiguous authoring target — never derive strings from the doc under
# test), `read_doc_raw`/`require_doc` nameref helpers for in-project docs,
# `read_repo_doc_raw`/`require_repo_doc` for the cross-project
# `docs/autonomous-loop.md`, `assert_contains_ws` for prose that may be
# line-wrapped in the source markdown (flow/docs/shell-scripting-gotchas.md),
# `assert_absent_paired`/`assert_absent_paired_ws` for non-vacuous absence
# checks (a bare `assert_not_contains` on a stale-claim sentence would pass
# vacuously if the section/heading it is scoped to were itself missing or
# renamed), a `failures=` counter, no fixtures. Never calls `fail()` inside
# `$(...)` — every extractor below is a pure function (no side effects) per
# flow/docs/shell-scripting-gotchas.md's read-helper-purity rule, invoked in
# `$(...)` only for its stdout, with the caller checking the result and
# calling `fail()` in the parent shell.
#
# CI path-filter caveat (same accepted coupling as
# configure-dispatch-enrollment.test.sh): this suite lives under
# `flow/tests/**` and is only run by `flow-ci.yml`'s `flow-test` job, which
# is path-filtered on `flow == 'true'`. One assertion below reaches outside
# `flow/**` (`docs/autonomous-loop.md`). Ticket #965's own Files to Modify
# closes this specific coupling gap by adding `docs/autonomous-loop.md` to
# `flow-ci.yml`'s `extra` filter — until that lands, a future PR touching
# only `docs/autonomous-loop.md` would not trigger `flow-ci` and so would not
# run this suite in CI.
#
# Covered files:
#   - flow/skills/configure/SKILL.md
#   - flow/skills/configure/codex.md
#   - docs/autonomous-loop.md (cross-project)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "configure-autonomy-questions.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "configure-autonomy-questions.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "configure-autonomy-questions.test.sh: failed to resolve repo root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, no fail() side
  # effect here: it is deliberately safe to call inside a $(...) command
  # substitution.
  local _relpath="$1"
  local _path="${FLOW_DIR}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing file must never masquerade as empty content, which would make
# assert_not_contains trivially pass). Must NOT be invoked via $(...).
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

read_repo_doc_raw() {
  # read_repo_doc_raw <repo-relative-path> — same pure-extraction contract as
  # read_doc_raw, but resolved from REPO_ROOT for the cross-project
  # docs/autonomous-loop.md, which lives outside flow/**. No fail() side
  # effect here: deliberately safe to call inside a $(...) command
  # substitution.
  local _relpath="$1"
  local _path="${REPO_ROOT}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_repo_doc <result-var> <repo-relative-path> — nameref wrapper
# around read_repo_doc_raw. Must NOT be invoked via $(...).
require_repo_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_repo_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${REPO_ROOT}/${_relpath}"
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

normalize_ws() {
  # normalize_ws <content> — collapses newlines and repeated whitespace to a
  # single space so a markdown-wrapped sentence can be matched as one
  # substring, per docs/shell-scripting-gotchas.md's line-wrapping pitfall.
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws <content> <required-substring> <label> — whitespace-
# normalized variant of assert_contains, for sentences that may be
# line-wrapped in the source markdown.
assert_contains_ws() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# assert_absent_paired <content> <existence-marker> <forbidden-substring> <label>
#
# A bare assert_not_contains on a stale-claim sentence would pass vacuously
# before the new section/marker exists at all — it would prove nothing about
# this ticket. Requiring the marker's own existence first means this only
# proves the intended thing (the section exists AND deliberately no longer
# carries the stale claim), and fails loudly — for the right reason — while
# the section is still unauthored (this red phase). Mirrors
# configure-dispatch-enrollment.test.sh's helper of the same name.
assert_absent_paired() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  if [[ "${content}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- existence marker missing: [${marker}]"
    return
  fi
  [[ "${content}" != *"${forbidden}"* ]] || fail "${label}: forbidden text present: [${forbidden}]"
}

# assert_absent_paired_ws <content> <existence-marker> <forbidden-substring> <label>
#
# Whitespace-normalized variant of assert_absent_paired, for a forbidden
# sentence that is line-wrapped in the source markdown (so a raw substring
# check on either side would spuriously fail to match across the wrap).
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  local norm
  norm="$(normalize_ws "${content}")"
  if [[ "${norm}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- existence marker missing (whitespace-normalized): [${marker}]"
    return
  fi
  [[ "${norm}" != *"${forbidden}"* ]] || fail "${label}: forbidden text present (whitespace-normalized): [${forbidden}]"
}

# extract_section_body_raw <content> <heading-line> — pure extractor (no
# fail() side effect, safe inside $(...)): returns every line strictly
# between <heading-line> and the next `### `-level heading, so a negative
# assertion ("this section must not smuggle in a babysitInterval/security
# question") is scoped to the new section's own body, not the whole file
# (where those field names legitimately appear elsewhere). Anchors on the
# exact heading text at the start of a line, never a phrase-presence
# heuristic, per docs/shell-scripting-gotchas.md's awk-boundary-extraction
# rule.
extract_section_body_raw() {
  local content="$1" heading="$2"
  awk -v heading="${heading}" '
    index($0, heading) == 1 { found=1; next }
    found && index($0, "### ") == 1 { exit }
    found { print }
  ' <<<"${content}"
}

# extract_marked_block_raw <content> <begin-marker> <end-marker> — pure
# extractor (no fail() side effect, safe inside $(...)): returns every line
# strictly between the begin/end marker lines, with any fenced-code-block
# delimiter lines (```json / ```) stripped, so the result is raw JSON text
# suitable for `jq`. Anchors on the exact marker text at the start of a
# line (same discipline as extract_section_body_raw above).
extract_marked_block_raw() {
  local content="$1" begin_marker="$2" end_marker="$3"
  awk -v b="${begin_marker}" -v e="${end_marker}" '
    index($0, e) == 1 { on=0 }
    on && index($0, "```") != 1 { print }
    index($0, b) == 1 { on=1 }
  ' <<<"${content}"
}

# --- Exact authored substrings Phase 4 (green) must add verbatim -----------
#
# These are pinned here, not derived, so the red phase fails for the right
# reason and the green phase has an unambiguous authoring target.

SECTION_HEADING='### Autonomy Settings'
Q13_MARKER='13. **Plan approval autonomy**'
Q14_MARKER='14. **Automerge policy block**'

# Absent-only rule, stated once and scoped to both keys.
ABSENT_ONLY_RULE='When `planning` or `automerge` is already present, report the existing value verbatim, never re-prompt, never narrow, and never remove it.'

# Presence detection — `automerge` explicitly covers projects[] (monorepo
# automerge can be per-project only; this repo itself has no top-level
# `automerge` key, only per-project ones).
PLANNING_PRESENCE_MARKER='`planning` is present iff `existingConfig.planning` exists'
AUTOMERGE_PRESENCE_MARKER='`automerge` is present iff a top-level `automerge` key exists or any `projects[]` entry carries its own `automerge` key — monorepo automerge can be per-project only'

# Question 13 — conservative option first, marked Recommended, vs lean.
Q13_OPTION_INTERACTIVE='"Keep `interactive` (Recommended)"'
Q13_OPTION_LEAN='"Switch to `lean`"'
Q13_DECLINE_MARKER='Declining omits the `planning` key entirely — no `planning` key is written.'

# Question 14 — skip (deny-by-default, Recommended) vs scaffold for review.
Q14_OPTION_SKIP='"Skip — no block (deny-by-default) (Recommended)"'
Q14_OPTION_SCAFFOLD='"Scaffold a starter block for hand review"'
Q14_DECLINE_MARKER='Declining omits the `automerge` key entirely — no `automerge` key is written.'

# Scaffold JSON template — stable extraction markers around a fenced json
# block, so a contract test can pull the block out and validate it
# structurally with jq rather than by substring (AC3).
SCAFFOLD_BEGIN_MARKER='<!-- cenci:automerge-scaffold:begin -->'
SCAFFOLD_END_MARKER='<!-- cenci:automerge-scaffold:end -->'

# Baseline category globs the scaffold's protectedPaths must seed from — the
# FULL implement Sensitive-path backstop default pattern list
# (flow/skills/implement/SKILL.md's built-in defaults, 36 patterns, asserted
# here as a complete set, not a sample — a narrowed scaffold must fail this
# suite per the security review of #965) plus the release/CI path.
SCAFFOLD_BASELINE_GLOBS=(
  '*auth*' '*login*' '*logout*' '*session*'
  '*password*' '*passwd*' '*credential*' '*secret*' '*secrets*'
  '*token*' '*jwt*' '*apikey*' '*api_key*' '*.pem' '*.key' '*.env*'
  '*oauth*' '*sso*' '*saml*' '*openid*'
  '*permission*' '*acl*' '*rbac*' '*role*'
  '*crypto*' '*encrypt*' '*decrypt*' '*sign*' '*hash*'
  '*payment*' '*billing*' '*invoice*' '*checkout*' '*stripe*'
  '*migrat*' '*schema*'
)
SCAFFOLD_BASELINE_CI_PATH='.github/'

# Scaffold prose must state the three-source union that seeds protectedPaths
# — the built-in baseline, confirmed repo-shape paths, and the repo owner's
# own hand-declared `security.sensitivePaths` — deduplicated and additive
# only. A doc-contract test can't exercise a live wizard run against a repo
# config carrying `security.sensitivePaths`, so this pins the prose sentence
# stating the union instead (security re-review of #965: the scaffold must
# not silently drop the repo owner's own signal).
Q14_SENSITIVE_PATHS_UNION_MARKER='`existingConfig.security.sensitivePaths` when that key is present'

# Step 6 wiring — the AGENTS.md #824 "validated but actually consumed" rule:
# the questions are inert unless step 6 literally names them and states the
# overlay order relative to merge-sandbox-config.sh's stdout.
STEP6_WIRING_MARKER='`planningAnswer`/`automergeAnswer` are overlaid onto `merge-sandbox-config.sh`'"'"'s stdout before the write'
STEP6_DECLINE_CONTRIBUTES_NO_KEY_MARKER='a declined answer contributes no key'

# PR-body "Review required" section, emitted only when a block was
# scaffolded this run.
PR_BODY_REVIEW_HEADING='## Review required — automerge policy'
PR_BODY_REVIEW_LINK='docs/autonomous-loop.md#3-declare-what-a-merge-is-allowed-to-touch'
PR_BODY_REVIEW_CONDITIONAL='emitted only when question 14 scaffolded a block this run'

# Next-step pointer — real fleet verbs shipped by #964/#968.
NEXT_STEP_DISPATCH_VERB='cenci dispatch plan-refined on'
NEXT_STEP_AUTOMERGE_VERB='cenci automerge on'

# Stale claims this ticket must remove (paired with SECTION_HEADING so the
# absence check can't pass vacuously before the section itself exists).
OLD_PLANNING_NEVER_WRITTEN_CLAIM='The `planning` field is optional and is **never written by a configure prompt**'
OLD_AUTOMERGE_NEVER_WRITTEN_CLAIM='The `automerge` field is optional and is **never written by a configure prompt**'

# Stale claims that must survive untouched (security/babysitInterval/
# maintenance stay question-less per the plan's Constraints).
SECURITY_NEVER_WRITTEN_CLAIM='The `security` field is optional and is **never written by a configure prompt**'
MAINTENANCE_NEVER_WRITTEN_CLAIM='The `maintenance` field is optional and is **never written by a configure prompt**'
BABYSIT_NO_QUESTION_CLAIM='`babysitInterval` is an optional field — there is **no configure question for it**'

# codex.md parity paragraph.
CODEX_AUTONOMY_HEADING_MIRROR_MARKER='Autonomy Settings mirrors the Claude procedure'"'"'s `### Autonomy Settings` section'
CODEX_AUTONOMY_ABSENT_ONLY_MARKER='asks about `planning` and `automerge` only when absent'
CODEX_AUTONOMY_CONSERVATIVE_DEFAULT_MARKER='defaults both offers to the conservative answer — `interactive` for planning, no automerge block'
CODEX_AUTONOMY_PLAN_MODE_MARKER='both keys land in the apply step'"'"'s config write, never during `/plan`'

# docs/autonomous-loop.md — old claim (§3) removed, new sentence (§3) added,
# plus a one-line §1 pointer.
AUTONOMOUS_LOOP_SECTION3_HEADING='### 3. Declare what a merge is allowed to touch'
AUTONOMOUS_LOOP_SECTION1_HEADING='### 1. Let plans approve themselves'
OLD_AUTOMERGE_DOC_CLAIM='`/cenci:configure` never writes or removes this block — it'"'"'s a hand-edited escape hatch that survives reconfiguration untouched.'
NEW_AUTOMERGE_DOC_CLAIM='`/cenci:configure` offers to scaffold this block only when `automerge` is absent — an existing block is reported verbatim and never re-prompted, narrowed, or removed.'
NEW_PLANNING_DOC_CLAIM='`/cenci:configure` offers `planning.autonomy` when the key is absent, defaulting to `interactive`.'

# --- skills/configure/SKILL.md ----------------------------------------------

require_doc skill "skills/configure/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  # Section heading and question numbering (paired: a missing heading means
  # the section-scoped checks below fail for the right reason too).
  assert_contains "${skill}" "${SECTION_HEADING}" "SKILL.md Autonomy Settings heading"
  assert_contains "${skill}" "${Q13_MARKER}" "SKILL.md question 13 (plan approval autonomy)"
  assert_contains "${skill}" "${Q14_MARKER}" "SKILL.md question 14 (automerge policy block)"

  # Absent-only rule + projects[]-aware presence detection.
  assert_contains_ws "${skill}" "${ABSENT_ONLY_RULE}" "SKILL.md absent-only rule (never re-prompt/narrow/remove)"
  assert_contains_ws "${skill}" "${PLANNING_PRESENCE_MARKER}" "SKILL.md planning presence detection"
  assert_contains_ws "${skill}" "${AUTOMERGE_PRESENCE_MARKER}" "SKILL.md automerge presence detection covers projects[]"

  # Question 13 — conservative-first, Recommended, and the decline sentence.
  assert_contains "${skill}" "${Q13_OPTION_INTERACTIVE}" "SKILL.md question 13 interactive option (Recommended)"
  assert_contains "${skill}" "${Q13_OPTION_LEAN}" "SKILL.md question 13 lean option"
  assert_contains_ws "${skill}" "${Q13_DECLINE_MARKER}" "SKILL.md question 13 decline omits planning key entirely"

  # Question 14 — skip-is-Recommended vs scaffold, and the decline sentence.
  assert_contains "${skill}" "${Q14_OPTION_SKIP}" "SKILL.md question 14 skip option (Recommended)"
  assert_contains "${skill}" "${Q14_OPTION_SCAFFOLD}" "SKILL.md question 14 scaffold option"
  assert_contains_ws "${skill}" "${Q14_DECLINE_MARKER}" "SKILL.md question 14 decline omits automerge key entirely"

  # Section-scoped negative check: no babysitInterval/security question
  # smuggled into the new section (scoped to the section body, not the
  # whole file, where those field names legitimately appear elsewhere).
  section_body="$(extract_section_body_raw "${skill}" "${SECTION_HEADING}")"
  if [[ -z "${section_body}" ]]; then
    fail "SKILL.md Autonomy Settings section body: not found/empty (section heading missing) -- cannot verify no babysitInterval/security question was smuggled in"
  else
    assert_not_contains "${section_body}" "babysitInterval" "SKILL.md Autonomy Settings section must not ask about babysitInterval"
    assert_not_contains "${section_body}" "\"security\"" "SKILL.md Autonomy Settings section must not ask about security"
  fi

  # Scaffold JSON template — extract between stable markers and validate
  # structurally with jq, not substrings (AC3).
  scaffold_json="$(extract_marked_block_raw "${skill}" "${SCAFFOLD_BEGIN_MARKER}" "${SCAFFOLD_END_MARKER}")"
  if [[ -z "${scaffold_json}" ]]; then
    fail "SKILL.md automerge scaffold JSON: not found between markers [${SCAFFOLD_BEGIN_MARKER}] / [${SCAFFOLD_END_MARKER}]"
  elif ! command -v jq >/dev/null 2>&1; then
    fail "SKILL.md automerge scaffold JSON: jq not available to validate structurally"
  elif ! printf '%s\n' "${scaffold_json}" | jq empty >/dev/null 2>&1; then
    fail "SKILL.md automerge scaffold JSON: extracted block is not valid JSON"
  else
    max_changed_files="$(printf '%s\n' "${scaffold_json}" | jq -r '.maxChangedFiles // 0')"
    max_diff_lines="$(printf '%s\n' "${scaffold_json}" | jq -r '.maxDiffLines // 0')"
    merge_method="$(printf '%s\n' "${scaffold_json}" | jq -r '.mergeMethod // ""')"
    protected_paths_is_nonempty_string_array="$(printf '%s\n' "${scaffold_json}" | jq -r '(.protectedPaths | type == "array") and ((.protectedPaths | length) > 0) and (all(.protectedPaths[]; type == "string"))')"

    [[ "${max_changed_files}" =~ ^[0-9]+$ ]] && [[ "${max_changed_files}" -gt 0 ]] \
      || fail "SKILL.md automerge scaffold JSON: maxChangedFiles must be > 0, got [${max_changed_files}]"
    [[ "${max_diff_lines}" =~ ^[0-9]+$ ]] && [[ "${max_diff_lines}" -gt 0 ]] \
      || fail "SKILL.md automerge scaffold JSON: maxDiffLines must be > 0, got [${max_diff_lines}]"
    [[ "${merge_method}" == "squash" ]] \
      || fail "SKILL.md automerge scaffold JSON: mergeMethod must be \"squash\", got [${merge_method}]"
    [[ "${protected_paths_is_nonempty_string_array}" == "true" ]] \
      || fail "SKILL.md automerge scaffold JSON: protectedPaths must be a non-empty array of strings"

    for glob in "${SCAFFOLD_BASELINE_GLOBS[@]}"; do
      protected_paths_has_glob="$(printf '%s\n' "${scaffold_json}" | jq -r --arg g "${glob}" '(.protectedPaths // []) | index($g) != null')"
      [[ "${protected_paths_has_glob}" == "true" ]] \
        || fail "SKILL.md automerge scaffold JSON: protectedPaths must seed the mandatory baseline glob [${glob}]"
    done
    protected_paths_has_ci_path="$(printf '%s\n' "${scaffold_json}" | jq -r --arg p "${SCAFFOLD_BASELINE_CI_PATH}" '(.protectedPaths // []) | index($p) != null')"
    [[ "${protected_paths_has_ci_path}" == "true" ]] \
      || fail "SKILL.md automerge scaffold JSON: protectedPaths must seed the mandatory release/CI path [${SCAFFOLD_BASELINE_CI_PATH}]"
  fi

  # Scaffold prose must document unioning in existingConfig.security.sensitivePaths
  # (the repo owner's own hand-declared signal) alongside the built-in baseline
  # and confirmed repo-shape paths -- a live wizard run can't be exercised here,
  # so this pins the documented union instead.
  assert_contains_ws "${skill}" "${Q14_SENSITIVE_PATHS_UNION_MARKER}" "SKILL.md question 14 scaffold prose unions in existingConfig.security.sensitivePaths"

  # Step 6 wiring — AGENTS.md #824: validated-but-consumed.
  assert_contains_ws "${skill}" "${STEP6_WIRING_MARKER}" "SKILL.md step 6 overlays planningAnswer/automergeAnswer onto merge-sandbox-config.sh stdout"
  assert_contains_ws "${skill}" "${STEP6_DECLINE_CONTRIBUTES_NO_KEY_MARKER}" "SKILL.md step 6 states a declined answer contributes no key"

  # PR-body "Review required" section.
  assert_contains "${skill}" "${PR_BODY_REVIEW_HEADING}" "SKILL.md PR body Review required — automerge policy heading"
  assert_contains "${skill}" "${PR_BODY_REVIEW_LINK}" "SKILL.md PR body links docs/autonomous-loop.md#3-declare-what-a-merge-is-allowed-to-touch"
  assert_contains_ws "${skill}" "${PR_BODY_REVIEW_CONDITIONAL}" "SKILL.md PR body Review required section is conditional on a scaffold this run"

  # Next-step pointer — real fleet verbs.
  assert_contains "${skill}" "${NEXT_STEP_DISPATCH_VERB}" "SKILL.md Autonomy Settings next-step names cenci dispatch plan-refined on"
  assert_contains "${skill}" "${NEXT_STEP_AUTOMERGE_VERB}" "SKILL.md Autonomy Settings next-step names cenci automerge on"

  # Stale-claim removal, paired with the section existing (so this can't
  # pass vacuously before the section is authored).
  assert_absent_paired "${skill}" "${SECTION_HEADING}" "${OLD_PLANNING_NEVER_WRITTEN_CLAIM}" "SKILL.md stale planning never-written-by-a-configure-prompt claim removed"
  assert_absent_paired "${skill}" "${SECTION_HEADING}" "${OLD_AUTOMERGE_NEVER_WRITTEN_CLAIM}" "SKILL.md stale automerge never-written-by-a-configure-prompt claim removed"

  # security/babysitInterval/maintenance stay question-less and unedited.
  assert_contains "${skill}" "${SECURITY_NEVER_WRITTEN_CLAIM}" "SKILL.md security never-written-by-a-configure-prompt claim untouched"
  assert_contains "${skill}" "${MAINTENANCE_NEVER_WRITTEN_CLAIM}" "SKILL.md maintenance never-written-by-a-configure-prompt claim untouched"
  assert_contains "${skill}" "${BABYSIT_NO_QUESTION_CLAIM}" "SKILL.md babysitInterval no-configure-question claim untouched"
fi

# --- skills/configure/codex.md ----------------------------------------------

require_doc codex "skills/configure/codex.md" || true
if [[ -n "${codex}" ]]; then
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_HEADING_MIRROR_MARKER}" "codex.md Autonomy Settings parity paragraph mirrors the Claude section"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_ABSENT_ONLY_MARKER}" "codex.md Autonomy Settings parity paragraph states the absent-only rule"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_CONSERVATIVE_DEFAULT_MARKER}" "codex.md Autonomy Settings parity paragraph states conservative defaults"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_PLAN_MODE_MARKER}" "codex.md Autonomy Settings parity paragraph states the apply-step-only / never-during-plan rule"
fi

# --- Cross-project: docs/autonomous-loop.md ---------------------------------

require_repo_doc loop_doc "docs/autonomous-loop.md" || true
if [[ -n "${loop_doc}" ]]; then
  # §3: old "never writes or removes this block" claim removed, paired with
  # the §3 heading so this can't pass vacuously if the heading itself moved.
  assert_absent_paired_ws "${loop_doc}" "${AUTONOMOUS_LOOP_SECTION3_HEADING}" "${OLD_AUTOMERGE_DOC_CLAIM}" "docs/autonomous-loop.md stale automerge never-writes-or-removes claim removed"

  # §3: new offer-when-absent sentence present.
  assert_contains_ws "${loop_doc}" "${NEW_AUTOMERGE_DOC_CLAIM}" "docs/autonomous-loop.md new automerge offer-when-absent sentence"

  # §1: new one-line pointer that configure offers planning.autonomy when
  # absent, defaulting to interactive.
  assert_contains_ws "${loop_doc}" "${NEW_PLANNING_DOC_CLAIM}" "docs/autonomous-loop.md new planning.autonomy offer-when-absent pointer in section 1"

  # Sanity: section 1 heading itself must still exist, or the pointer above
  # would be floating free of its documented location.
  assert_contains "${loop_doc}" "${AUTONOMOUS_LOOP_SECTION1_HEADING}" "docs/autonomous-loop.md section 1 heading present"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-autonomy-questions.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-autonomy-questions.test.sh: all checks passed."
