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
# Ticket #1274 (parent #1130) extends this suite to question 15 (`refinement`,
# the sixth autonomy switch, `refinement.autonomy`): its `### Autonomy
# Settings` body (options with lean first and Recommended, decline writes
# nothing, `--all` keep-or-switch, "never grants `automerge:ok` and does not
# remove the Confirmation Gate"), its `cenci:configure-q15-switch` jq program
# (extracted and RUN with jq, not just substring-checked), the table row, the
# step 6 `refinementAnswer` wiring, the config-field docs, codex.md parity, the
# `docs/autonomous-loop.md` sixth-switch documentation and the root AGENTS.md
# "six opt-in autonomy switches" index line. Every wording that moved to cover
# question 15 keeps its previous form as a paired-absence check.
#
# Ticket #1342 (parent #692) moves `### Autonomy Settings` (questions 13-17)
# into the conditionally read flow/skills/configure/phases/autonomy-settings.md
# and the questions 18-20 `### Fleet Autonomy Controls` Next step block into
# phases/fleet-enrollment.md. The section, option, scaffold, Q15 body and q15
# program assertions therefore read the phase files; step 6, the schema, the
# defaults table and the PR body stay on SKILL.md with pointers that name the
# phase file. The three "Under `--all`, question N is re-offered ..." sentences
# collapse into one sentence pointing at SKILL.md's `### Question Defaults`
# (P10); the old per-question sentences are paired-absence checks.
#
# Covered files:
#   - flow/skills/configure/phases/autonomy-settings.md
#   - flow/skills/configure/phases/fleet-enrollment.md (Next step block)
#   - flow/skills/configure/SKILL.md
#   - flow/skills/configure/codex.md
#   - docs/autonomous-loop.md (cross-project)
#   - AGENTS.md (repo root, cross-project)
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

# extract_from_to_raw <content> <begin-prefix> <end-prefix> — pure extractor
# (no fail() side effect, safe inside $(...)): every line from the first line
# that STARTS WITH <begin-prefix> (inclusive) up to, not including, the next
# line that starts with <end-prefix>. Anchored at column 0, like the other
# extractors here, never a phrase-presence heuristic.
extract_from_to_raw() {
  local content="$1" begin="$2" end="$3"
  awk -v b="${begin}" -v e="${end}" '
    on && index($0, e) == 1 { exit }
    index($0, b) == 1 { on=1 }
    on { print }
  ' <<<"${content}"
}

# extract_doc_section_raw <content> <heading-line> — pure extractor: every
# line after <heading-line> up to the next `## ` or `### ` heading, so a
# docs/autonomous-loop.md section is scoped to its own body.
extract_doc_section_raw() {
  local content="$1" heading="$2"
  awk -v heading="${heading}" '
    index($0, heading) == 1 { found=1; next }
    found && (index($0, "## ") == 1 || index($0, "### ") == 1) { exit }
    found { print }
  ' <<<"${content}"
}

# offset_of <content> <needle> — byte offset of the first occurrence, or -1.
offset_of() {
  local content="$1" needle="$2" prefix
  if [[ "${content}" != *"${needle}"* ]]; then
    printf '%s' "-1"
    return
  fi
  prefix="${content%%"${needle}"*}"
  printf '%s' "${#prefix}"
}

# --- Exact authored substrings Phase 4 (green) must add verbatim -----------
#
# These are pinned here, not derived, so the red phase fails for the right
# reason and the green phase has an unambiguous authoring target.

SECTION_HEADING='### Autonomy Settings'
FLEET_CONTROLS_HEADING='### Fleet Autonomy Controls'
# How SKILL.md (step 6, step 7, the schema, the defaults table) points at the
# moved section: it names the file (#1342).
AUTONOMY_FILE_POINTER='`### Autonomy Settings` in `phases/autonomy-settings.md`'
Q13_MARKER='13. **Plan approval autonomy**'
Q14_MARKER='14. **Automerge policy block**'

# Absent-only rule, stated once and scoped to both keys. Ticket #1246 scopes
# it to a delta run (the default re-run mode): `--all` re-offers both
# questions (see flow/tests/configure-delta-rerun.test.sh), so the old
# unconditional wording is retired (paired-absence check below).
# Ticket #1274 widens it to the third key (`refinement`); the two-key delta
# wording is retired (paired-absence check below).
ABSENT_ONLY_RULE='On a delta run, when `planning`, `automerge` or `refinement` is already present, report the existing value verbatim, never re-prompt, never narrow, and never remove it.'
OLD_TWO_KEY_ABSENT_ONLY_RULE='On a delta run, when `planning` or `automerge` is already present, report the existing value verbatim, never re-prompt, never narrow, and never remove it.'
OLD_UNCONDITIONAL_ABSENT_ONLY_RULE='When `planning` or `automerge` is already present, report the existing value verbatim, never re-prompt, never narrow, and never remove it.'

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
STEP6_WIRING_MARKER='`planningAnswer`/`automergeAnswer`/`refinementAnswer` are overlaid onto `merge-sandbox-config.sh`'"'"'s stdout before the write'
OLD_STEP6_WIRING_MARKER='`planningAnswer`/`automergeAnswer` are overlaid onto `merge-sandbox-config.sh`'"'"'s stdout before the write'
STEP6_REFINEMENT_WIRING='`refinementAnswer` contributes a `refinement: { "autonomy": "lean" }` key only when question 15 in `phases/autonomy-settings.md` was asked and answered `lean`'
OLD_STEP6_REFINEMENT_WIRING='only when question 15 above was asked and answered `lean`'
STEP6_Q14_PROGRAM_POINTER='`cenci:configure-q14-remove` program (`### Autonomy Settings` in `phases/autonomy-settings.md`)'
STEP6_Q15_SWITCH_WIRING='a collected "Switch to …" answer for question 15 is applied with the `cenci:configure-q15-switch` program (`--arg autonomy interactive|lean`)'
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
CODEX_AUTONOMY_HEADING_MIRROR_MARKER='Autonomy Settings mirrors the Claude procedure'"'"'s `### Autonomy Settings` section in `phases/autonomy-settings.md`'
OLD_CODEX_AUTONOMY_HEADING_MIRROR_MARKER='`### Autonomy Settings` section: the procedure on a delta run'
CODEX_INDEPENDENT_MIRROR_MARKER='Independent controls mirror questions 16–17 in `phases/autonomy-settings.md` and 18–20 in `phases/fleet-enrollment.md`'
OLD_CODEX_INDEPENDENT_MIRROR_MARKER='Independent controls mirror questions 16–20 in `SKILL.md`'
CODEX_LEAF_PROGRAMS_MARKER='`cenci:configure-confirm-before-apply` and `cenci:configure-refinement-automerge` marker-bounded jq programs from `phases/autonomy-settings.md`'
OLD_CODEX_LEAF_PROGRAMS_MARKER='marker-bounded jq programs from `SKILL.md`, with `--argjson confirmBeforeApply'
CODEX_AUTONOMY_ABSENT_ONLY_MARKER='on a delta run asks about `planning`, `automerge` and `refinement` only when absent and not already offered'
OLD_CODEX_AUTONOMY_TWO_KEY_MARKER='on a delta run asks about `planning` and `automerge` only when absent and not already offered'
CODEX_AUTONOMY_ALL_REOFFER_MARKER='`--all` re-offers all three with keep-or-change options'
OLD_CODEX_AUTONOMY_ALL_REOFFER_MARKER='`--all` re-offers both with keep-or-change options'
OLD_CODEX_AUTONOMY_UNCONDITIONAL_MARKER='asks about `planning` and `automerge` only when absent — an already-present value'
# Question 15 deliberately departs from the conservative-first pattern: the
# owner wants lean refinement as the normal mode, so the offer recommends
# `lean`, while the runtime default for an absent key stays `interactive`.
CODEX_AUTONOMY_CONSERVATIVE_DEFAULT_MARKER='defaults the planning and automerge offers to the conservative answer — `interactive` for planning, no automerge block — while question 15 recommends `lean` and an absent `refinement` key still resolves to `interactive`'
OLD_CODEX_AUTONOMY_CONSERVATIVE_DEFAULT_MARKER='defaults both offers to the conservative answer — `interactive` for planning, no automerge block'
CODEX_AUTONOMY_PLAN_MODE_MARKER='all three keys land in the apply step'"'"'s config write, never during `/plan`'
OLD_CODEX_AUTONOMY_PLAN_MODE_MARKER='both keys land in the apply step'"'"'s config write, never during `/plan`'
CODEX_Q15_OPTIONS_MARKER='Question 15 (`refinement`) mirrors question 13 with the options "Switch to `lean` (Recommended)" and "Keep `interactive`"'
CODEX_Q15_DECLINE_AND_GATE_MARKER='Declining question 15 writes no `refinement` key. Refinement autonomy controls question selection only; confirmation and automerge grants use questions 16 and 17 independently.'

# docs/autonomous-loop.md — old claim (§3) removed, new sentence (§3) added,
# plus a one-line §1 pointer.
AUTONOMOUS_LOOP_SECTION3_HEADING='### 3. Declare what a merge is allowed to touch'
AUTONOMOUS_LOOP_SECTION1_HEADING='### 1. Let plans approve themselves'
OLD_AUTOMERGE_DOC_CLAIM='`/cenci:configure` never writes or removes this block — it'"'"'s a hand-edited escape hatch that survives reconfiguration untouched.'
# Ticket #1246 delta-scopes both claims (the `--all` re-offer and Q14's
# "Remove the block(s)" option are pinned in configure-delta-rerun.test.sh).
NEW_AUTOMERGE_DOC_CLAIM='`/cenci:configure` offers to scaffold this block on a delta run only when `automerge` is absent and has not been offered before — an existing block is reported verbatim and never re-prompted, narrowed, or removed.'
NEW_PLANNING_DOC_CLAIM='`/cenci:configure` offers `planning.autonomy` on a delta run when the key is absent and has not been offered before, defaulting to `interactive`; `/cenci:configure --all` re-offers it with the committed value pre-selected.'
OLD_UNCONDITIONAL_AUTOMERGE_DOC_CLAIM='`/cenci:configure` offers to scaffold this block only when `automerge` is absent — an existing block is reported verbatim and never re-prompted, narrowed, or removed.'
OLD_UNCONDITIONAL_PLANNING_DOC_CLAIM='`/cenci:configure` offers `planning.autonomy` when the key is absent, defaulting to `interactive`.'


# --- Ticket #1274: question 15 (`refinement`, the sixth switch) ---------------
#
# Question 15 mirrors question 13 (delta/absent-only, `--all` keep-or-switch,
# decline writes nothing, fixed marker-bounded jq overlay preserving sibling
# keys) with one deliberate departure: its ordinary offer lists "Switch to
# `lean` (Recommended)" first, because the owner wants lean refinement as the
# normal mode. The runtime default for an absent key stays `interactive`.
Q15_MARKER='15. **Refinement autonomy**'
Q15_ASKED_WHEN='(asked via `AskUserQuestion` when `refinement` is absent and, on a delta run, `refinement` is in `delta.ask`; under `--all` it is also asked when `refinement` is present, in the keep-or-change form below)'
Q15_OPTIONS='Options: "Switch to `lean` (Recommended)", "Keep `interactive`"'
Q15_OPTION_LEAN='"Switch to `lean` (Recommended)"'
Q15_OPTION_INTERACTIVE_RECOMMENDED='"Keep `interactive` (Recommended)"'
Q15_DECLINE_MARKER='Declining omits the `refinement` key entirely — no `refinement` key is written.'
Q15_ABSENT_DEFAULT_MARKER='an absent `refinement` key still resolves to `interactive`'
Q15_RECOMMEND_SCOPE_MARKER='"Switch to `lean` (Recommended)" applies only to the ordinary form, when `refinement` is absent'
Q15_ALL_HEADING_MARKER='**`--all` with `refinement` present**'
Q15_ALL_PROMPT_MARKER='`refinement.autonomy` is currently `<current>`. Keep it or switch?'
Q15_ALL_KEEP_OPTION='"Keep current (Recommended)"'
Q15_ALL_OTHER_VALUE_MARKER='a value other than the exact string `lean` is treated as `interactive`'
Q15_ALL_KEEP_WRITES_NOTHING='Keeping the current value writes nothing.'
Q15_ALL_SWITCH_EXPLICIT='Switching to `interactive` writes `refinement.autonomy: "interactive"` explicitly, preserving sibling keys.'
Q15_GATE_MARKER='Refinement autonomy controls question selection only; confirmation and automerge grants use questions 16 and 17 independently.'
Q15_SWITCH_BEGIN_MARKER='<!-- cenci:configure-q15-switch:begin -->'
Q15_SWITCH_END_MARKER='<!-- cenci:configure-q15-switch:end -->'
Q15_PROGRAM='.refinement = ((.refinement | if type == "object" then . else {} end) + {autonomy: $autonomy})'

# Section-level restatements (all inside `### Autonomy Settings`).
REFINEMENT_PRESENCE_MARKER='`refinement` is present iff `existingConfig.refinement` exists'
AUTONOMY_INTRO_KEYS_MARKER='`planning`, `automerge` and `refinement` are repo-committed keys that land inside this run'"'"'s PR'
OLD_AUTONOMY_INTRO_KEYS_MARKER='`planning` and `automerge` are repo-committed keys that land inside this run'"'"'s PR'
AUTONOMY_INTRO_QUESTIONS_MARKER='The questions below only collect an answer; none writes anything itself'
OLD_AUTONOMY_INTRO_QUESTIONS_MARKER='Both questions below only collect an answer; neither writes anything itself'
# P10 (#1342): one sentence replaces the three per-question --all re-offer
# sentences and points at the single keep-or-change rule in SKILL.md's
# `### Question Defaults`; the old sentences are paired-absence checks. The same
# pointer is carried by each question's `--all` bullet. Identical constants are
# pinned in configure-router-contract.test.sh and configure-delta-rerun.test.sh.
KEEP_RULE_POINTER='per the keep-or-change rule in `SKILL.md`'"'"'s `### Question Defaults`'
ALL_REOFFER_COLLAPSED='Under `--all`, questions 13, 14 and 15 are each re-offered even when `planning`, `automerge` or `refinement` is present, with the Keep option listed first and pre-selected '"${KEEP_RULE_POINTER}"'.'
OLD_Q13_ALL_REOFFER='Under `--all`, question 13 is re-offered even when `planning` is present, with the committed value pre-selected.'
OLD_Q14_ALL_REOFFER='Under `--all`, question 14 is re-offered even when `automerge` is present, with "Keep current block(s) (Recommended)" pre-selected.'
OLD_Q15_ALL_REOFFER='Under `--all`, question 15 is re-offered even when `refinement` is present, with "Keep current (Recommended)" pre-selected.'
NEXT_STEP_ALL_SIX_MARKER='for the full walkthrough of the switches'
OLD_NEXT_STEP_ALL_FOUR_MARKER='for the full walkthrough of all four switches'
NEXT_STEP_NO_FLEET_VERB_MARKER='Switch 6 (`refinement.autonomy`) has no fleet verb: it is read from the local checkout, so there is nothing to flip fleet-wide.'

# Defaults table row (the ID column is the stable ledger contract).
Q15_TABLE_ROW='| 15. Refinement autonomy | `refinement` | `refinement` |'

# Config-field docs: the illustrative schema, and a `refinement` paragraph
# after the `planning` one (consumer, exact-"lean" semantics, local read).
SCHEMA_REFINEMENT_LINE='"refinement": { "autonomy": "interactive" },'
SCHEMA_NOTE_MARKER='(The `planning` and `refinement` values shown above are illustrative of the schema'
OLD_SCHEMA_NOTE_MARKER='(The `planning` value shown above is illustrative of the schema'
SCHEMA_NOTE_Q15_MARKER='question 15 (`### Autonomy Settings` in `phases/autonomy-settings.md`) writes `refinement.autonomy` only when you explicitly choose `lean` (or switch under `--all`)'
OLD_SCHEMA_NOTE_Q15_MARKER='question 15 (`### Autonomy Settings`) writes `refinement.autonomy`'
REFINEMENT_FIELD_INTRO='The `refinement` field is optional. `### Autonomy Settings` question 15 in `phases/autonomy-settings.md` asks for it only when absent'
OLD_REFINEMENT_FIELD_INTRO='`### Autonomy Settings` question 15 above asks for it only when absent'
REFINEMENT_FIELD_SEMANTICS='- `refinement.autonomy` — `"lean"` or `"interactive"`, default `"interactive"`. Only the exact string `"lean"` changes behavior'
REFINEMENT_FIELD_CONSUMER='`refinement.autonomy` is consumed by `skills/refine/SKILL.md`'
REFINEMENT_FIELD_LOCAL_READ='is read from the local `.cenci/config.json` through `project-core`, never from `origin/main`, and no fleet switch exists for it'
REFINEMENT_FIELD_GATE='Refinement autonomy controls question selection only; confirmation and automerge grants use questions 16 and 17 independently.'

# Step 7 final-report: a carried `refinement` value is stated verbatim too
# (pinned in configure-delta-rerun.test.sh as STEP7_CARRIED_VALUES).

# docs/autonomous-loop.md — the sixth switch.
LOOP_LEAD_SIX='Planning and merging remain separate choices:'
OLD_LOOP_LEAD_FIVE='Five opt-in switches'
LOOP_HEADING_SIX_LINE='## Independent controls'
OLD_LOOP_HEADING_FIVE_LINE='## The five switches'
OLD_LOOP_FIVE_SWITCHES='five switches'
LOOP_ROW6='| 6 | `refinement.autonomy: "lean"` | repo `.cenci/config.json` (committed) | `"interactive"` | Reduces refinement questions using grounded decisions. Unresolved consequential questions still block applying the proposal.'
LOOP_PER_REPO_SENTENCE='Switches 1, 3, 6, 7 and 8 are per-repo and committed, so the repo decides its own autonomy.'
OLD_LOOP_PER_REPO_SENTENCE='Switches 1 and 3 are per-repo and committed'
LOOP_NO_FLEET_COUNTERPART='Refinement settings are read from the local checkout and have no fleet counterpart.'
LOOP_SECTION5_HEADING='### 5. Start the loop'
LOOP_SECTION6_HEADING='### 6. Ask fewer questions at refinement'
LOOP_WHAT_STOPS_HEADING='## What still stops the machine'
LOOP_SECTION6_WHAT='Lean mode keeps a short interview and records grounded decisions.'
LOOP_SECTION6_GATE='`refinement.autonomy` never grants `automerge:ok` and never controls final review.'
LOOP_SECTION6_LOCAL='`refinement.autonomy` is read from the local `.cenci/config.json` through `project-core`, never from `origin/main`, and no fleet switch exists for it.'
LOOP_SECTION6_OFFER='`/cenci:configure` offers `refinement.autonomy` on a delta run when the key is absent and has not been offered before, recommending `lean` while an absent key still resolves to `interactive`; `/cenci:configure --all` re-offers it with the committed value pre-selected.'
LOOP_TURN_OFF_ROW='| Lean refinement for one repo | Set `refinement.autonomy` to anything other than `"lean"` | Next `/cenci:refine` |'
LOOP_DETAILS_ROW='| Lean refinement as the refine skill executes it | [Refine skill](../flow/skills/refine/SKILL.md) |'

# Root AGENTS.md doc index.
ROOT_AGENTS_SIX_SWITCHES='the user-facing guide to independent autonomy controls (lean planning, `dispatch.planRefined`, the `automerge` policy block, `automerge.enabled`, `planning.attended`, `refinement.autonomy`, `refinement.confirmBeforeApply`, `refinement.automerge`)'
OLD_ROOT_AGENTS_FIVE_SWITCHES='the user-facing guide to the five opt-in autonomy switches'

# jq_apply <program> <config-json> [jq args...] — run a program over a config;
# stdout is compact JSON. Pure (no fail()), safe inside $(...).
jq_apply() {
  local program="$1" cfg="$2"
  shift 2
  printf '%s' "${cfg}" | jq -c "$@" "${program}"
}

# assert_json_eq <label> <actual-json> <expected-json> — fails (parent shell)
# when the program produced nothing or the documents differ.
assert_json_eq() {
  local label="$1" actual="$2" expected="$3"
  if [[ -z "${actual}" ]]; then
    fail "${label}: no output to compare (program missing or jq error); expected ${expected}"
    return
  fi
  jq -ne --argjson a "${actual}" --argjson b "${expected}" '$a == $b' >/dev/null 2>&1 \
    || fail "${label}: expected ${expected}, got ${actual}"
}

# --- skills/configure/SKILL.md ----------------------------------------------

require_doc autonomy_phase "skills/configure/phases/autonomy-settings.md" || true
require_doc fleet_phase "skills/configure/phases/fleet-enrollment.md" || true

# --- skills/configure/phases/autonomy-settings.md ---------------------------
# `### Autonomy Settings` (questions 13-17) lives here since #1342.

section_body=""
if [[ -z "${autonomy_phase}" ]]; then
  fail "phases/autonomy-settings.md: empty or missing, section assertions skipped"
else
  # Section heading and question numbering (paired: a missing heading means
  # the section-scoped checks below fail for the right reason too).
  assert_contains "${autonomy_phase}" "${SECTION_HEADING}" "autonomy-settings.md Autonomy Settings heading"
  assert_contains "${autonomy_phase}" "${Q13_MARKER}" "autonomy-settings.md question 13 (plan approval autonomy)"
  assert_contains "${autonomy_phase}" "${Q14_MARKER}" "autonomy-settings.md question 14 (automerge policy block)"

  # Absent-only rule + projects[]-aware presence detection.
  assert_contains_ws "${autonomy_phase}" "${ABSENT_ONLY_RULE}" "autonomy-settings.md absent-only rule (never re-prompt/narrow/remove)"
  assert_absent_paired_ws "${autonomy_phase}" "${ABSENT_ONLY_RULE}" "${OLD_UNCONDITIONAL_ABSENT_ONLY_RULE}" "autonomy-settings.md retired unconditional absent-only rule (now delta-scoped, #1246)"
  assert_absent_paired_ws "${autonomy_phase}" "${ABSENT_ONLY_RULE}" "${OLD_TWO_KEY_ABSENT_ONLY_RULE}" "autonomy-settings.md retired two-key absent-only rule (now covers refinement, #1274)"
  assert_contains_ws "${autonomy_phase}" "${PLANNING_PRESENCE_MARKER}" "autonomy-settings.md planning presence detection"
  assert_contains_ws "${autonomy_phase}" "${AUTOMERGE_PRESENCE_MARKER}" "autonomy-settings.md automerge presence detection covers projects[]"

  # Question 13 — conservative-first, Recommended, and the decline sentence.
  assert_contains "${autonomy_phase}" "${Q13_OPTION_INTERACTIVE}" "autonomy-settings.md question 13 interactive option (Recommended)"
  assert_contains "${autonomy_phase}" "${Q13_OPTION_LEAN}" "autonomy-settings.md question 13 lean option"
  assert_contains_ws "${autonomy_phase}" "${Q13_DECLINE_MARKER}" "autonomy-settings.md question 13 decline omits planning key entirely"

  # Question 14 — skip-is-Recommended vs scaffold, and the decline sentence.
  assert_contains "${autonomy_phase}" "${Q14_OPTION_SKIP}" "autonomy-settings.md question 14 skip option (Recommended)"
  assert_contains "${autonomy_phase}" "${Q14_OPTION_SCAFFOLD}" "autonomy-settings.md question 14 scaffold option"
  assert_contains_ws "${autonomy_phase}" "${Q14_DECLINE_MARKER}" "autonomy-settings.md question 14 decline omits automerge key entirely"

  # Section-scoped negative check: no babysitInterval/security question
  # smuggled into the new section (scoped to the section body, not the
  # whole file, where those field names legitimately appear elsewhere).
  section_body="$(extract_section_body_raw "${autonomy_phase}" "${SECTION_HEADING}")"
  if [[ -z "${section_body}" ]]; then
    fail "autonomy-settings.md Autonomy Settings section body: not found/empty (section heading missing) -- cannot verify no babysitInterval/security question was smuggled in"
  else
    assert_not_contains "${section_body}" "babysitInterval" "autonomy-settings.md Autonomy Settings section must not ask about babysitInterval"
    assert_not_contains "${section_body}" "\"security\"" "autonomy-settings.md Autonomy Settings section must not ask about security"
  fi

  # Scaffold JSON template — extract between stable markers and validate
  # structurally with jq, not substrings (AC3).
  scaffold_json="$(extract_marked_block_raw "${autonomy_phase}" "${SCAFFOLD_BEGIN_MARKER}" "${SCAFFOLD_END_MARKER}")"
  if [[ -z "${scaffold_json}" ]]; then
    fail "autonomy-settings.md automerge scaffold JSON: not found between markers [${SCAFFOLD_BEGIN_MARKER}] / [${SCAFFOLD_END_MARKER}]"
  elif ! command -v jq >/dev/null 2>&1; then
    fail "autonomy-settings.md automerge scaffold JSON: jq not available to validate structurally"
  elif ! printf '%s\n' "${scaffold_json}" | jq empty >/dev/null 2>&1; then
    fail "autonomy-settings.md automerge scaffold JSON: extracted block is not valid JSON"
  else
    max_changed_files="$(printf '%s\n' "${scaffold_json}" | jq -r '.maxChangedFiles // 0')"
    max_diff_lines="$(printf '%s\n' "${scaffold_json}" | jq -r '.maxDiffLines // 0')"
    merge_method="$(printf '%s\n' "${scaffold_json}" | jq -r '.mergeMethod // ""')"
    protected_paths_is_nonempty_string_array="$(printf '%s\n' "${scaffold_json}" | jq -r '(.protectedPaths | type == "array") and ((.protectedPaths | length) > 0) and (all(.protectedPaths[]; type == "string"))')"

    [[ "${max_changed_files}" =~ ^[0-9]+$ ]] && [[ "${max_changed_files}" -gt 0 ]] \
      || fail "autonomy-settings.md automerge scaffold JSON: maxChangedFiles must be > 0, got [${max_changed_files}]"
    [[ "${max_diff_lines}" =~ ^[0-9]+$ ]] && [[ "${max_diff_lines}" -gt 0 ]] \
      || fail "autonomy-settings.md automerge scaffold JSON: maxDiffLines must be > 0, got [${max_diff_lines}]"
    [[ "${merge_method}" == "squash" ]] \
      || fail "autonomy-settings.md automerge scaffold JSON: mergeMethod must be \"squash\", got [${merge_method}]"
    [[ "${protected_paths_is_nonempty_string_array}" == "true" ]] \
      || fail "autonomy-settings.md automerge scaffold JSON: protectedPaths must be a non-empty array of strings"

    for glob in "${SCAFFOLD_BASELINE_GLOBS[@]}"; do
      protected_paths_has_glob="$(printf '%s\n' "${scaffold_json}" | jq -r --arg g "${glob}" '(.protectedPaths // []) | index($g) != null')"
      [[ "${protected_paths_has_glob}" == "true" ]] \
        || fail "autonomy-settings.md automerge scaffold JSON: protectedPaths must seed the mandatory baseline glob [${glob}]"
    done
    protected_paths_has_ci_path="$(printf '%s\n' "${scaffold_json}" | jq -r --arg p "${SCAFFOLD_BASELINE_CI_PATH}" '(.protectedPaths // []) | index($p) != null')"
    [[ "${protected_paths_has_ci_path}" == "true" ]] \
      || fail "autonomy-settings.md automerge scaffold JSON: protectedPaths must seed the mandatory release/CI path [${SCAFFOLD_BASELINE_CI_PATH}]"
  fi

  # Scaffold prose must document unioning in existingConfig.security.sensitivePaths
  # (the repo owner's own hand-declared signal) alongside the built-in baseline
  # and confirmed repo-shape paths -- a live wizard run can't be exercised here,
  # so this pins the documented union instead.
  assert_contains_ws "${autonomy_phase}" "${Q14_SENSITIVE_PATHS_UNION_MARKER}" "autonomy-settings.md question 14 scaffold prose unions in existingConfig.security.sensitivePaths"

  # --- Question 15 (#1274): refinement autonomy, the sixth switch ---------
  assert_contains "${autonomy_phase}" "${Q15_MARKER}" "autonomy-settings.md question 15 (refinement autonomy)"

  q15_body="$(extract_from_to_raw "${autonomy_phase}" "${Q15_MARKER}" "${Q15_SWITCH_END_MARKER}")"
  if [[ -z "${q15_body}" ]]; then
    fail "autonomy-settings.md question 15 body: not found between [${Q15_MARKER}] and [${Q15_SWITCH_END_MARKER}]"
  else
    # P10 (#1342): the --all bullet points at the single keep-or-change rule.
    assert_contains_ws "${q15_body}" "${KEEP_RULE_POINTER}" "autonomy-settings.md question 15 --all bullet points at SKILL.md ### Question Defaults"
    # Every check below is scoped to question 15's own body, so a sentence
    # that question 13 already carries (e.g. the exact-string rule) cannot
    # satisfy it vacuously.
    assert_contains_ws "${q15_body}" "${Q15_ASKED_WHEN}" "autonomy-settings.md question 15 asked-when clause (absent or in delta.ask; --all keep-or-change)"
    assert_contains_ws "${q15_body}" "${Q15_OPTIONS}" "autonomy-settings.md question 15 options: lean first and Recommended, then Keep interactive"
    assert_absent_paired_ws "${q15_body}" "${Q15_OPTION_LEAN}" "${Q15_OPTION_INTERACTIVE_RECOMMENDED}" "autonomy-settings.md question 15 must not recommend Keep interactive (the Recommended label belongs to lean)"
    assert_contains_ws "${q15_body}" "${Q15_DECLINE_MARKER}" "autonomy-settings.md question 15 decline omits the refinement key entirely"
    assert_contains_ws "${q15_body}" "${Q15_ABSENT_DEFAULT_MARKER}" "autonomy-settings.md question 15 states an absent key still resolves to interactive"
    assert_contains_ws "${q15_body}" "${Q15_RECOMMEND_SCOPE_MARKER}" "autonomy-settings.md question 15 scopes the lean recommendation to the ordinary (key absent) form"
    assert_contains_ws "${q15_body}" "${Q15_ALL_HEADING_MARKER}" "autonomy-settings.md question 15 --all form heading"
    assert_contains_ws "${q15_body}" "${Q15_ALL_PROMPT_MARKER}" "autonomy-settings.md question 15 --all keep-or-switch prompt"
    assert_contains_ws "${q15_body}" "${Q15_ALL_KEEP_OPTION}" "autonomy-settings.md question 15 --all pre-selects Keep current (Recommended)"
    assert_contains_ws "${q15_body}" "${Q15_ALL_OTHER_VALUE_MARKER}" "autonomy-settings.md question 15 --all treats any value other than the exact string lean as interactive"
    assert_contains_ws "${q15_body}" "${Q15_ALL_KEEP_WRITES_NOTHING}" "autonomy-settings.md question 15 --all Keep writes nothing"
    assert_contains_ws "${q15_body}" "${Q15_ALL_SWITCH_EXPLICIT}" "autonomy-settings.md question 15 --all switch to interactive is written explicitly, preserving siblings"
    assert_contains_ws "${q15_body}" "${Q15_GATE_MARKER}" "autonomy-settings.md question 15 states lean refinement never grants automerge:ok and does not remove the Confirmation Gate"
    assert_contains "${q15_body}" "${Q15_SWITCH_BEGIN_MARKER}" "autonomy-settings.md question 15 body carries the q15 switch begin marker"
  fi

  # Section-level restatements inside `### Autonomy Settings`.
  if [[ -n "${section_body}" ]]; then
    assert_contains_ws "${section_body}" "${AUTONOMY_INTRO_KEYS_MARKER}" "autonomy-settings.md Autonomy Settings intro names the three repo-committed keys"
    assert_absent_paired_ws "${section_body}" "${AUTONOMY_INTRO_KEYS_MARKER}" "${OLD_AUTONOMY_INTRO_KEYS_MARKER}" "autonomy-settings.md retired two-key Autonomy Settings intro (#1274)"
    assert_contains_ws "${section_body}" "${AUTONOMY_INTRO_QUESTIONS_MARKER}" "autonomy-settings.md Autonomy Settings intro covers all the questions below"
    assert_absent_paired_ws "${section_body}" "${AUTONOMY_INTRO_QUESTIONS_MARKER}" "${OLD_AUTONOMY_INTRO_QUESTIONS_MARKER}" "autonomy-settings.md retired 'Both questions below' intro (#1274)"
    assert_contains_ws "${section_body}" "${REFINEMENT_PRESENCE_MARKER}" "autonomy-settings.md refinement presence detection"
    # P10 (#1342): the three per-question --all re-offer sentences collapse
    # into one that points at the router's single keep-or-change rule.
    before_q13="${section_body%%"${Q13_MARKER}"*}"
    assert_contains_ws "${before_q13}" "${ALL_REOFFER_COLLAPSED}" "autonomy-settings.md --all re-offers paragraph is one sentence covering questions 13, 14 and 15"
    assert_absent_paired_ws "${section_body}" "${ALL_REOFFER_COLLAPSED}" "${OLD_Q13_ALL_REOFFER}" "autonomy-settings.md retired per-question Q13 --all re-offer sentence (#1342)"
    assert_absent_paired_ws "${section_body}" "${ALL_REOFFER_COLLAPSED}" "${OLD_Q14_ALL_REOFFER}" "autonomy-settings.md retired per-question Q14 --all re-offer sentence (#1342)"
    assert_absent_paired_ws "${section_body}" "${ALL_REOFFER_COLLAPSED}" "${OLD_Q15_ALL_REOFFER}" "autonomy-settings.md retired per-question Q15 --all re-offer sentence (#1342)"
    # Cross-file pointers name their file.
    assert_contains_ws "${section_body}" 'unlike `### Fleet Dispatch Enrollment` in `phases/fleet-enrollment.md`' "autonomy-settings.md intro names the file holding Fleet Dispatch Enrollment"
    assert_contains_ws "${section_body}" 'step 6 in `SKILL.md`' "autonomy-settings.md intro names SKILL.md as the home of the step 6 write"
  fi

  # The q15 jq program: extracted from its markers and RUN with jq.
  q15_prog="$(extract_marked_block_raw "${autonomy_phase}" "${Q15_SWITCH_BEGIN_MARKER}" "${Q15_SWITCH_END_MARKER}")"
  if [[ -z "${q15_prog}" ]]; then
    fail "autonomy-settings.md q15 switch program: not found between [${Q15_SWITCH_BEGIN_MARKER}] / [${Q15_SWITCH_END_MARKER}]"
  elif ! command -v jq >/dev/null 2>&1; then
    fail "autonomy-settings.md q15 switch program: jq not available to run it"
  else
    [[ "${q15_prog}" == "${Q15_PROGRAM}" ]] \
      || fail "autonomy-settings.md q15 switch program: expected the fixed program [${Q15_PROGRAM}], got [${q15_prog}]"
    out="$(jq_apply "${q15_prog}" '{}' --arg autonomy lean)"
    assert_json_eq "q15 program: an absent refinement block becomes {autonomy:lean}" \
      "$(jq -c '.refinement' <<<"${out:-null}" 2>/dev/null)" '{"autonomy":"lean"}'
    out="$(jq_apply "${q15_prog}" '{"refinement":{"autonomy":"interactive","note":"keep-me"},"stack":{"backend":"go"}}' --arg autonomy lean)"
    assert_json_eq "q15 program: siblings inside refinement are preserved on a switch" \
      "$(jq -c '.refinement' <<<"${out:-null}" 2>/dev/null)" '{"autonomy":"lean","note":"keep-me"}'
    assert_json_eq "q15 program: every other top-level key is unchanged" \
      "$(jq -c 'del(.refinement)' <<<"${out:-null}" 2>/dev/null)" '{"stack":{"backend":"go"}}'
    out="$(jq_apply "${q15_prog}" '{"refinement":{"autonomy":"lean","note":"keep-me"}}' --arg autonomy interactive)"
    assert_json_eq "q15 program: interactive is written explicitly, preserving siblings" \
      "$(jq -c '.refinement' <<<"${out:-null}" 2>/dev/null)" '{"autonomy":"interactive","note":"keep-me"}'
    for non_object in '"lean"' '[1]' 'null' '7' 'true'; do
      out="$(jq_apply "${q15_prog}" "{\"refinement\":${non_object},\"stack\":{\"backend\":\"go\"}}" --arg autonomy lean)"
      assert_json_eq "q15 program: a non-object refinement block (${non_object}) is replaced" \
        "$(jq -c '.refinement' <<<"${out:-null}" 2>/dev/null)" '{"autonomy":"lean"}'
      assert_json_eq "q15 program: a non-object refinement block (${non_object}) leaves other keys unchanged" \
        "$(jq -c 'del(.refinement)' <<<"${out:-null}" 2>/dev/null)" '{"stack":{"backend":"go"}}'
    done
    out="$(jq_apply "${q15_prog}" '{"planning":{"autonomy":"lean"},"automerge":{"maxChangedFiles":5}}' --arg autonomy lean)"
    assert_json_eq "q15 program: never touches the planning or automerge blocks" \
      "$(jq -c 'del(.refinement)' <<<"${out:-null}" 2>/dev/null)" '{"planning":{"autonomy":"lean"},"automerge":{"maxChangedFiles":5}}'
    printf '%s' "${out}" | jq -e 'type == "object"' >/dev/null 2>&1 \
      || fail "q15 program: output must pass the step 6 object gate (jq -e 'type == \"object\"')"
  fi

fi

# --- skills/configure/phases/fleet-enrollment.md (Fleet Autonomy Controls) ---
# The questions 18-20 Next step block moved with `### Fleet Autonomy Controls`.

fleet_controls_body=""
if [[ -n "${fleet_phase}" ]]; then
  fleet_controls_body="$(extract_section_body_raw "${fleet_phase}" "${FLEET_CONTROLS_HEADING}")"
fi
if [[ -z "${fleet_controls_body}" ]]; then
  fail "fleet-enrollment.md ${FLEET_CONTROLS_HEADING} body: not found/empty -- cannot verify the Next step block"
else
  # Next-step pointer — real fleet verbs.
  assert_contains "${fleet_controls_body}" "${NEXT_STEP_DISPATCH_VERB}" "fleet-enrollment.md Fleet Autonomy Controls next-step names cenci dispatch plan-refined on"
  assert_contains "${fleet_controls_body}" "${NEXT_STEP_AUTOMERGE_VERB}" "fleet-enrollment.md Fleet Autonomy Controls next-step names cenci automerge on"
  assert_contains_ws "${fleet_controls_body}" "${NEXT_STEP_ALL_SIX_MARKER}" "fleet-enrollment.md next-step points at the walkthrough of all six switches"
  assert_absent_paired_ws "${fleet_controls_body}" "${NEXT_STEP_ALL_SIX_MARKER}" "${OLD_NEXT_STEP_ALL_FOUR_MARKER}" "fleet-enrollment.md retired 'all four switches' next-step wording (#1274)"
  assert_contains_ws "${fleet_controls_body}" "${NEXT_STEP_NO_FLEET_VERB_MARKER}" "fleet-enrollment.md next-step notes switch 6 has no fleet verb"
  assert_contains_ws "${fleet_controls_body}" 'after `phases/autonomy-settings.md`' "fleet-enrollment.md Fleet Autonomy Controls says it is asked after autonomy-settings.md"
fi

# --- skills/configure/SKILL.md ----------------------------------------------
# Step 6, step 7, the defaults table and the config schema stay inline in the
# router; their pointers at the moved section name the phase file (#1342).

require_doc skill "skills/configure/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  # The moved questions are gone from the router (paired with a pointer that
  # proves the router still names the section's new home).
  assert_absent_paired_ws "${skill}" "${AUTONOMY_FILE_POINTER}" "${Q13_MARKER}" "SKILL.md no longer carries question 13 (moved to phases/autonomy-settings.md)"
  assert_absent_paired_ws "${skill}" "${AUTONOMY_FILE_POINTER}" "${Q14_MARKER}" "SKILL.md no longer carries question 14 (moved to phases/autonomy-settings.md)"
  assert_absent_paired_ws "${skill}" "${AUTONOMY_FILE_POINTER}" "${Q15_MARKER}" "SKILL.md no longer carries question 15 (moved to phases/autonomy-settings.md)"
  assert_absent_paired_ws "${skill}" "${AUTONOMY_FILE_POINTER}" "${SCAFFOLD_BEGIN_MARKER}" "SKILL.md no longer carries the automerge scaffold block"

  # Step 6 wiring — AGENTS.md #824: validated-but-consumed.
  assert_contains_ws "${skill}" "${STEP6_WIRING_MARKER}" "SKILL.md step 6 overlays planningAnswer/automergeAnswer/refinementAnswer onto merge-sandbox-config.sh stdout"
  assert_absent_paired_ws "${skill}" "${STEP6_WIRING_MARKER}" "${OLD_STEP6_WIRING_MARKER}" "SKILL.md retired two-answer step 6 wiring sentence (#1274)"
  assert_contains_ws "${skill}" "${STEP6_REFINEMENT_WIRING}" "SKILL.md step 6 refinementAnswer contributes refinement.autonomy lean only when question 15 (in phases/autonomy-settings.md) was asked and answered lean"
  assert_absent_paired_ws "${skill}" "${STEP6_REFINEMENT_WIRING}" "${OLD_STEP6_REFINEMENT_WIRING}" "SKILL.md retired positional 'question 15 above' step 6 pointer (#1342)"
  assert_contains_ws "${skill}" "${STEP6_Q14_PROGRAM_POINTER}" "SKILL.md step 6 names phases/autonomy-settings.md for the q14 removal program"
  assert_contains_ws "${skill}" "${STEP6_Q15_SWITCH_WIRING}" "SKILL.md step 6 applies a collected question 15 switch with the q15 program"
  assert_contains_ws "${skill}" "${STEP6_DECLINE_CONTRIBUTES_NO_KEY_MARKER}" "SKILL.md step 6 states a declined answer contributes no key"

  # PR-body "Review required" section.
  assert_contains "${skill}" "${PR_BODY_REVIEW_HEADING}" "SKILL.md PR body Review required — automerge policy heading"
  assert_contains "${skill}" "${PR_BODY_REVIEW_LINK}" "SKILL.md PR body links docs/autonomous-loop.md#3-declare-what-a-merge-is-allowed-to-touch"
  assert_contains_ws "${skill}" "${PR_BODY_REVIEW_CONDITIONAL}" "SKILL.md PR body Review required section is conditional on a scaffold this run"

  # Defaults table and config schema.
  assert_contains_ws "${skill}" "${Q15_TABLE_ROW}" "SKILL.md defaults table row for question 15 (ID refinement, key refinement)"
  assert_contains_ws "${skill}" "${SCHEMA_REFINEMENT_LINE}" "SKILL.md illustrative config schema shows the refinement block"
  assert_contains_ws "${skill}" "${SCHEMA_NOTE_MARKER}" "SKILL.md illustrative-schema note covers planning and refinement"
  assert_absent_paired_ws "${skill}" "${SCHEMA_NOTE_MARKER}" "${OLD_SCHEMA_NOTE_MARKER}" "SKILL.md retired planning-only illustrative-schema note (#1274)"
  assert_contains_ws "${skill}" "${SCHEMA_NOTE_Q15_MARKER}" "SKILL.md illustrative-schema note says question 15 (in phases/autonomy-settings.md) writes refinement.autonomy only on an explicit lean choice"
  assert_absent_paired_ws "${skill}" "${SCHEMA_NOTE_Q15_MARKER}" "${OLD_SCHEMA_NOTE_Q15_MARKER}" "SKILL.md retired positional illustrative-schema pointer (#1342)"
  assert_contains_ws "${skill}" "${REFINEMENT_FIELD_INTRO}" "SKILL.md refinement config-field paragraph (optional, asked only when absent)"
  assert_absent_paired_ws "${skill}" "${REFINEMENT_FIELD_INTRO}" "${OLD_REFINEMENT_FIELD_INTRO}" "SKILL.md retired positional refinement field pointer (#1342)"
  assert_contains_ws "${skill}" "${REFINEMENT_FIELD_SEMANTICS}" "SKILL.md refinement.autonomy schema bullet: only the exact string lean changes behavior"
  assert_contains_ws "${skill}" "${REFINEMENT_FIELD_CONSUMER}" "SKILL.md refinement field names its consumer skills/refine/SKILL.md"
  assert_contains_ws "${skill}" "${REFINEMENT_FIELD_LOCAL_READ}" "SKILL.md refinement field states the local read via project-core and no fleet switch"
  assert_contains_ws "${skill}" "${REFINEMENT_FIELD_GATE}" "SKILL.md refinement field states lean never grants automerge:ok and keeps the Confirmation Gate"

  # Stale-claim removal, paired with the router still naming the section's file
  # (so this can't pass vacuously on an emptied SKILL.md).
  assert_absent_paired "${skill}" "${AUTONOMY_FILE_POINTER}" "${OLD_PLANNING_NEVER_WRITTEN_CLAIM}" "SKILL.md stale planning never-written-by-a-configure-prompt claim removed"
  assert_absent_paired "${skill}" "${AUTONOMY_FILE_POINTER}" "${OLD_AUTOMERGE_NEVER_WRITTEN_CLAIM}" "SKILL.md stale automerge never-written-by-a-configure-prompt claim removed"

  # security/babysitInterval/maintenance stay question-less and unedited.
  assert_contains "${skill}" "${SECURITY_NEVER_WRITTEN_CLAIM}" "SKILL.md security never-written-by-a-configure-prompt claim untouched"
  assert_contains "${skill}" "${MAINTENANCE_NEVER_WRITTEN_CLAIM}" "SKILL.md maintenance never-written-by-a-configure-prompt claim untouched"
  assert_contains "${skill}" "${BABYSIT_NO_QUESTION_CLAIM}" "SKILL.md babysitInterval no-configure-question claim untouched"
fi

# --- skills/configure/codex.md ----------------------------------------------

require_doc codex "skills/configure/codex.md" || true
if [[ -n "${codex}" ]]; then
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_HEADING_MIRROR_MARKER}" "codex.md Autonomy Settings parity paragraph mirrors the Claude section and names phases/autonomy-settings.md"
  assert_absent_paired_ws "${codex}" "${CODEX_AUTONOMY_HEADING_MIRROR_MARKER}" "${OLD_CODEX_AUTONOMY_HEADING_MIRROR_MARKER}" "codex.md retired Autonomy Settings mirror sentence that does not name its file (#1342)"
  assert_contains_ws "${codex}" "${CODEX_INDEPENDENT_MIRROR_MARKER}" "codex.md independent controls paragraph names the files holding questions 16-17 and 18-20"
  assert_absent_paired_ws "${codex}" "${CODEX_INDEPENDENT_MIRROR_MARKER}" "${OLD_CODEX_INDEPENDENT_MIRROR_MARKER}" "codex.md retired 'questions 16–20 in SKILL.md' pointer (#1342)"
  assert_contains_ws "${codex}" "${CODEX_LEAF_PROGRAMS_MARKER}" "codex.md applies the leaf overlays from phases/autonomy-settings.md"
  assert_absent_paired_ws "${codex}" "${CODEX_LEAF_PROGRAMS_MARKER}" "${OLD_CODEX_LEAF_PROGRAMS_MARKER}" "codex.md retired leaf-program pointer at SKILL.md (#1342)"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_ABSENT_ONLY_MARKER}" "codex.md Autonomy Settings parity paragraph states the delta-scoped absent-only rule"
  assert_absent_paired_ws "${codex}" "${CODEX_AUTONOMY_ABSENT_ONLY_MARKER}" "${OLD_CODEX_AUTONOMY_UNCONDITIONAL_MARKER}" "codex.md retired unconditional absent-only claim (now delta-scoped, #1246)"
  assert_absent_paired_ws "${codex}" "${CODEX_AUTONOMY_ABSENT_ONLY_MARKER}" "${OLD_CODEX_AUTONOMY_TWO_KEY_MARKER}" "codex.md retired two-key absent-only claim (now covers refinement, #1274)"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_ALL_REOFFER_MARKER}" "codex.md Autonomy Settings parity paragraph states --all re-offers all three with keep-or-change options"
  assert_absent_paired_ws "${codex}" "${CODEX_AUTONOMY_ALL_REOFFER_MARKER}" "${OLD_CODEX_AUTONOMY_ALL_REOFFER_MARKER}" "codex.md retired '--all re-offers both' claim (#1274)"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_CONSERVATIVE_DEFAULT_MARKER}" "codex.md Autonomy Settings parity paragraph states conservative defaults and that question 15 recommends lean while an absent key resolves to interactive"
  assert_absent_paired_ws "${codex}" "${CODEX_AUTONOMY_CONSERVATIVE_DEFAULT_MARKER}" "${OLD_CODEX_AUTONOMY_CONSERVATIVE_DEFAULT_MARKER}" "codex.md retired 'defaults both offers to the conservative answer' sentence (#1274)"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_PLAN_MODE_MARKER}" "codex.md Autonomy Settings parity paragraph states the apply-step-only / never-during-plan rule for all three keys"
  assert_absent_paired_ws "${codex}" "${CODEX_AUTONOMY_PLAN_MODE_MARKER}" "${OLD_CODEX_AUTONOMY_PLAN_MODE_MARKER}" "codex.md retired 'both keys land in the apply step' sentence (#1274)"
  assert_contains_ws "${codex}" "${CODEX_Q15_OPTIONS_MARKER}" "codex.md Autonomy Settings parity paragraph names question 15 and its lean-first options"
  assert_contains_ws "${codex}" "${CODEX_Q15_DECLINE_AND_GATE_MARKER}" "codex.md Autonomy Settings parity paragraph states question 15 decline writes no key and lean never grants automerge:ok"
fi

# --- Cross-project: docs/autonomous-loop.md ---------------------------------

require_repo_doc loop_doc "docs/autonomous-loop.md" || true
if [[ -n "${loop_doc}" ]]; then
  # §3: old "never writes or removes this block" claim removed, paired with
  # the §3 heading so this can't pass vacuously if the heading itself moved.
  assert_absent_paired_ws "${loop_doc}" "${AUTONOMOUS_LOOP_SECTION3_HEADING}" "${OLD_AUTOMERGE_DOC_CLAIM}" "docs/autonomous-loop.md stale automerge never-writes-or-removes claim removed"

  # §3: new offer-when-absent sentence present.
  assert_contains_ws "${loop_doc}" "${NEW_AUTOMERGE_DOC_CLAIM}" "docs/autonomous-loop.md new delta-scoped automerge offer-when-absent sentence"
  assert_absent_paired_ws "${loop_doc}" "${NEW_AUTOMERGE_DOC_CLAIM}" "${OLD_UNCONDITIONAL_AUTOMERGE_DOC_CLAIM}" "docs/autonomous-loop.md retired unconditional automerge offer-when-absent sentence (#1246)"
  assert_absent_paired_ws "${loop_doc}" "${NEW_PLANNING_DOC_CLAIM}" "${OLD_UNCONDITIONAL_PLANNING_DOC_CLAIM}" "docs/autonomous-loop.md retired unconditional planning offer-when-absent sentence (#1246)"

  # §1: new one-line pointer that configure offers planning.autonomy when
  # absent, defaulting to interactive.
  assert_contains_ws "${loop_doc}" "${NEW_PLANNING_DOC_CLAIM}" "docs/autonomous-loop.md new delta-scoped planning.autonomy offer-when-absent pointer in section 1"

  # Sanity: section 1 heading itself must still exist, or the pointer above
  # would be floating free of its documented location.
  assert_contains "${loop_doc}" "${AUTONOMOUS_LOOP_SECTION1_HEADING}" "docs/autonomous-loop.md section 1 heading present"

  # --- #1274: the sixth switch (refinement.autonomy) -----------------------
  assert_contains_ws "${loop_doc}" "${LOOP_LEAD_SIX}" "docs/autonomous-loop.md lead paragraph counts six opt-in switches"
  assert_absent_paired_ws "${loop_doc}" "${LOOP_LEAD_SIX}" "${OLD_LOOP_LEAD_FIVE}" "docs/autonomous-loop.md retired 'Five opt-in switches' lead (#1274)"
  assert_contains "${loop_doc}" $'\n'"${LOOP_HEADING_SIX_LINE}"$'\n' "docs/autonomous-loop.md switch table heading is '${LOOP_HEADING_SIX_LINE}'"
  assert_absent_paired "${loop_doc}" "${LOOP_HEADING_SIX_LINE}" "${OLD_LOOP_HEADING_FIVE_LINE}" "docs/autonomous-loop.md retired '${OLD_LOOP_HEADING_FIVE_LINE}' heading (#1274)"
  assert_absent_paired_ws "${loop_doc}" "${LOOP_HEADING_SIX_LINE}" "${OLD_LOOP_FIVE_SWITCHES}" "docs/autonomous-loop.md no remaining 'five switches' wording (#1274)"
  assert_contains_ws "${loop_doc}" "${LOOP_ROW6}" "docs/autonomous-loop.md switch table row 6 (refinement.autonomy lean)"
  assert_contains_ws "${loop_doc}" "${LOOP_PER_REPO_SENTENCE}" "docs/autonomous-loop.md per-repo paragraph counts switches 1, 3 and 6"
  assert_absent_paired_ws "${loop_doc}" "${LOOP_PER_REPO_SENTENCE}" "${OLD_LOOP_PER_REPO_SENTENCE}" "docs/autonomous-loop.md retired 'Switches 1 and 3 are per-repo' sentence (#1274)"
  assert_contains_ws "${loop_doc}" "${LOOP_NO_FLEET_COUNTERPART}" "docs/autonomous-loop.md says switch 6 has no fleet counterpart"
  assert_contains_ws "${loop_doc}" "${LOOP_TURN_OFF_ROW}" "docs/autonomous-loop.md Turning it off row for lean refinement"
  assert_contains_ws "${loop_doc}" "${LOOP_DETAILS_ROW}" "docs/autonomous-loop.md Where the details live row points at the refine skill"

  # Quick-start anchors 1 and 3 are linked and checked by tests: never renumbered.
  assert_contains "${loop_doc}" "${AUTONOMOUS_LOOP_SECTION3_HEADING}" "docs/autonomous-loop.md section 3 heading keeps its anchor"

  # The new section sits at the end of Quick start: after step 5, before the
  # next top-level section.
  s5_at="$(offset_of "${loop_doc}" "${LOOP_SECTION5_HEADING}")"
  s6_at="$(offset_of "${loop_doc}" "${LOOP_SECTION6_HEADING}")"
  stops_at="$(offset_of "${loop_doc}" "${LOOP_WHAT_STOPS_HEADING}")"
  if [[ "${s6_at}" -lt 0 ]]; then
    fail "docs/autonomous-loop.md: '${LOOP_SECTION6_HEADING}' section missing"
  elif [[ "${s5_at}" -lt 0 || "${stops_at}" -lt 0 ]]; then
    fail "docs/autonomous-loop.md: cannot place section 6 -- anchor '${LOOP_SECTION5_HEADING}' or '${LOOP_WHAT_STOPS_HEADING}' missing"
  elif ! [[ "${s5_at}" -lt "${s6_at}" && "${s6_at}" -lt "${stops_at}" ]]; then
    fail "docs/autonomous-loop.md: '${LOOP_SECTION6_HEADING}' must sit after '${LOOP_SECTION5_HEADING}' and before '${LOOP_WHAT_STOPS_HEADING}'"
  fi

  loop_section6="$(extract_doc_section_raw "${loop_doc}" "${LOOP_SECTION6_HEADING}")"
  if [[ -z "${loop_section6}" ]]; then
    fail "docs/autonomous-loop.md section 6 body: not found/empty -- cannot verify its claims"
  else
    assert_contains_ws "${loop_section6}" "${LOOP_SECTION6_WHAT}" "docs/autonomous-loop.md section 6 describes what lean refinement does (one round, at most 3 questions plus the confirm/overrule question)"
    assert_contains_ws "${loop_section6}" "${LOOP_SECTION6_GATE}" "docs/autonomous-loop.md section 6 states the switch never grants automerge:ok and keeps the Confirmation Gate"
    assert_contains_ws "${loop_section6}" "${LOOP_SECTION6_LOCAL}" "docs/autonomous-loop.md section 6 states the local read via project-core and that no fleet switch exists"
    assert_contains_ws "${loop_section6}" "${LOOP_SECTION6_OFFER}" "docs/autonomous-loop.md section 6 states the configure offer (delta-run absent-only, --all re-offer)"
  fi
fi

# --- Cross-project: root AGENTS.md doc index --------------------------------

require_repo_doc root_agents "AGENTS.md" || true
if [[ -n "${root_agents}" ]]; then
  assert_contains_ws "${root_agents}" "${ROOT_AGENTS_SIX_SWITCHES}" "root AGENTS.md doc index counts six opt-in autonomy switches and lists refinement.autonomy"
  assert_absent_paired_ws "${root_agents}" "${ROOT_AGENTS_SIX_SWITCHES}" "${OLD_ROOT_AGENTS_FIVE_SWITCHES}" "root AGENTS.md retired 'five opt-in autonomy switches' index line (#1274)"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-autonomy-questions.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-autonomy-questions.test.sh: all checks passed."
