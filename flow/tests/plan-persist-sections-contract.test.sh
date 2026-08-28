#!/usr/bin/env bash
# Contract test for ticket #782 — phase-1's "Persist the Plan" step must
# mechanically verify the assembled plan file contains all required headings
# before the plan is announced or the ticket is labeled, and hard-stop when
# any is missing (no `planComment` audit comment, no `Planned` label
# transition, no plan-artifact recording, no `hasPlanFile = true`). Pins:
#   - `flow/skills/implement/phases/phase-1-plan.md`'s new numbered step 3
#     under `## Persist the Plan` (the grep loop, the hard-stop bullet, the
#     ordering rationale), its strict position between the bundle append and
#     the `gh issue comment` / label-transition / artifact-recording calls,
#     and the Trivial Fast Path's explicit inheritance clause.
#   - `flow/skills/implement/codex.md`'s mirror clause (adapter-contract
#     parity per `docs/adapter-contract.md`).
#
# NOTE (Pencil/design removal): the required-heading count dropped from four
# to three when `## Design Context` and its self-repair path (append
# `## Design Context` / `N/A`, report the repair) were removed along with
# the rest of Pencil/design — all three remaining headings (`## Ticket
# Details`, `## Implementation Plan`, `## Architectural Context`) are now
# hard-stop-only, with no self-repair branch for any of them. The
# `watch/internal/pipeline/planfile.go` cross-boundary check below was
# updated to match, and `planfile.go`'s own `requiredPlanSections` literal
# was updated in the same change so the two stay in sync.
#
# Marker choice, per docs/shell-scripting-gotchas.md rule 3 (assert the
# specific replacement text at the edit site, never a generic marker that may
# already match unrelated prose elsewhere): the three heading literals
# (`## Ticket Details`, `## Implementation Plan`, `## Architectural Context`)
# legitimately recur elsewhere in phase-1-plan.md — the front-matter template
# block, the Trivial Fast Path prose, and assembly step 2 all mention them —
# so every heading assertion below is scoped to the extracted `## Persist the
# Plan` section and then to the extracted step-3 sub-step region, never
# asserted whole-file. Only markers that are genuinely unique to the new text
# (the `do **not**` hard-stop clauses, the step-3 heading line, the
# fast-path inheritance sentence) are asserted whole-file, and those are
# additionally checked with `assert_occurs_once` so a future duplicate
# insertion is caught too.
#
# Deliberate cross-project read: this test also reads
# `watch/internal/pipeline/planfile.go` (the source of truth for the three
# literals, `requiredPlanSections`) across the project boundary. Per ticket
# Q&A #2, the read is best-effort: when the file is present it's a real
# set-equality cross-check; when absent (flow distributed standalone, no
# `watch/` checkout) the test prints a visible `SKIP:` line on stdout and
# passes that leg without asserting anything.
#
# Idiom mirrors flow/tests/stale-plan-scoping-contract.test.sh: a `failures=`
# counter, small assert_* helpers, self-contained, auto-discovered by the flow
# gate's `*.test.sh` glob (see skills/maintain/scripts/check.sh
# check_structural_tests) — no registration anywhere. `normalize_ws` /
# `assert_contains_ws` (including its empty-pattern guard) are copied from
# flow/tests/design-sandbox-guard.test.sh:124-142 for the hard-wrapped
# codex.md assertions.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "plan-persist-sections-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "plan-persist-sections-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "plan-persist-sections-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
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
# line-wrapped in the source markdown (copied from
# flow/tests/design-sandbox-guard.test.sh:136-142, including the empty-
# pattern guard).
assert_contains_ws() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# assert_occurs_once <content> <required-substring> <label> — whole-file line
# count via grep -c -F, so a marker that should be edited at exactly one site
# fails loudly both when missing (count 0) and when accidentally duplicated
# (count > 1).
assert_occurs_once() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local count
  count="$(grep -c -F -- "${pattern}" <<<"${content}")"
  [[ "${count}" -eq 1 ]] || fail "${label}: expected exactly one occurrence, found ${count}: [${pattern}]"
}

# extract_persist_section <phase-1-plan-md-content>
# Returns the "## Persist the Plan" section body only (through the next real
# "## " heading, or EOF) so the four heading literals cannot be satisfied by
# the front-matter template block, the Trivial Fast Path prose, or assembly
# step 2 elsewhere in the file. Fence-aware: a line that merely *looks like* a
# "## " heading while inside a fenced (```) code block — e.g. the front-matter
# template's illustrative `## Ticket Details` example — is never treated as
# the section-boundary match; only a real, unfenced "## " heading ends the
# section.
extract_persist_section() {
  awk '
    /^## Persist the Plan$/ && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_verify_substep <persist-section-content>
# Within the persist section, returns numbered step 3 ("**Verify the
# assembled plan.**") through the line immediately before the first following
# non-blank, column-0 line (the next unindented heading/paragraph, e.g.
# "### Mark the ticket `Planned`").
extract_verify_substep() {
  awk '
    /^3\. \*\*Verify the assembled plan\.\*\*/ { on=1 }
    on && NF==0 { print; next }
    on && !/^3\. \*\*Verify the assembled plan\.\*\*/ && /^[^[:space:]]/ { exit }
    on { print }
  ' <<<"$1"
}

# first_line_of <content> <marker> — 1-based line number of the marker's
# first occurrence within <content>, or empty if not found. Pure extraction,
# no fail() side effect here: it is deliberately safe to call inside a $(...)
# command substitution.
first_line_of() {
  local content="$1" marker="$2"
  grep -n -m1 -F -- "${marker}" <<<"${content}" | cut -d: -f1
}

# require_first_line <result-var> <content> <marker> <label> — nameref
# wrapper around first_line_of that assigns the 1-based line number into
# <result-var>, or fails loudly and assigns "0" if not found. Must NOT be
# invoked via $(...): a fail() call made from inside a command-substitution
# subshell only increments that subshell's copy of `failures`, silently
# losing the failure count in the parent shell (docs/shell-scripting-
# gotchas.md's "unchecked command substitution" class of pitfall).
require_first_line() {
  local -n _result="$1"
  local content="$2" marker="$3" label="$4"
  local line
  line="$(first_line_of "${content}" "${marker}")"
  if [[ -z "${line}" ]]; then
    fail "${label}: marker not found: [${marker}]"
    _result=0
  else
    _result="${line}"
  fi
}

# --- Reusable markers (shared between substep-scoped and whole-file checks) -

HEAD_TICKET='## Ticket Details'
HEAD_IMPL='## Implementation Plan'
HEAD_ARCH='## Architectural Context'
# HEAD_DESIGN / `## Design Context` was dropped from requiredPlanSections
# along with the rest of Pencil/design -- plan-check now requires exactly
# three headings, and watch/internal/pipeline/planfile.go's
# requiredPlanSections literal was updated to match (see the cross-boundary
# sync check at the bottom of this file).

MARK_STEP3_HEADING='3. **Verify the assembled plan.**'
MARK_MUST_UPDATE='MUST update this step in the same PR'
# The former Design Context self-repair path (append `## Design Context` /
# `N/A`, "never silent", "report it in this phase's final message") is gone
# -- all three remaining required headings are hard-stop-only now, so there
# is no self-repair branch left to assert on.
MARK_DO_NOT_POST='do **not** post the `planComment` audit comment'
MARK_DO_NOT_LABEL='do **not** run the `Planned` label transition'
MARK_DO_NOT_ARTIFACT='do **not** record the plan artifact'
MARK_DO_NOT_HASPLANFILE='do **not** set `hasPlanFile = true`'
MARK_CONTEXT_BUNDLE_VS_PLANNER='context bundle vs. planner sections'
MARK_NEVER_POSTED='never posted to the ticket'
MARK_NEVER_RECORDED='never recorded for a plan that would fail validation'
MARK_DO_NOT_SELF_REPAIR='Do **not** self-repair'

MARK_FAST_PATH_INHERITANCE="Then run assembly step 3's three-heading verification exactly as written"

# --- flow/skills/implement/phases/phase-1-plan.md ----------------------------

FILE1="skills/implement/phases/phase-1-plan.md"
if require_doc CONTENT1 "${FILE1}"; then
  PERSIST_SECTION="$(extract_persist_section "${CONTENT1}")"
  if [[ -z "${PERSIST_SECTION}" ]]; then
    fail "${FILE1}: could not locate '## Persist the Plan' section (extract_persist_section returned empty)"
  else
    # --- Strict ordering inside the persist section -------------------------
    ORDER_LABEL="${FILE1} (persist-section ordering)"
    require_first_line L1 "${PERSIST_SECTION}" 'Append the context bundle verbatim via shell' "${ORDER_LABEL}"
    require_first_line L2 "${PERSIST_SECTION}" "${MARK_STEP3_HEADING}" "${ORDER_LABEL}"
    require_first_line L3 "${PERSIST_SECTION}" 'gh issue comment <number> --repo' "${ORDER_LABEL}"
    require_first_line L4 "${PERSIST_SECTION}" 'cenci pipeline label <id> --transition planned' "${ORDER_LABEL}"
    require_first_line L5 "${PERSIST_SECTION}" 'cenci pipeline artifact <id> --plan' "${ORDER_LABEL}"
    if ! { [[ "${L1}" -lt "${L2}" ]] && [[ "${L2}" -lt "${L3}" ]] && [[ "${L3}" -lt "${L4}" ]] && [[ "${L4}" -lt "${L5}" ]]; }; then
      fail "${FILE1}: persist-section ordering violated (expected strictly increasing): bundle-append=${L1} step3-verify=${L2} gh-issue-comment=${L3} label-transition=${L4} artifact-record=${L5}"
    fi

    # --- Extracted step-3 sub-step region, over-capture guards --------------
    VERIFY_SUBSTEP="$(extract_verify_substep "${PERSIST_SECTION}")"
    if [[ -z "${VERIFY_SUBSTEP}" ]]; then
      fail "${FILE1}: could not locate step-3 'Verify the assembled plan' sub-step (extract_verify_substep returned empty)"
    else
      assert_not_contains "${VERIFY_SUBSTEP}" '<planner output>' "${FILE1} (step-3 over-capture guard)"
      assert_not_contains "${VERIFY_SUBSTEP}" '### Mark the ticket' "${FILE1} (step-3 over-capture guard)"

      # --- Substep-scoped assertions (region-scoped per shell-scripting-
      #     gotchas rule 3 — these literals recur elsewhere in the file) ----
      assert_contains "${VERIFY_SUBSTEP}" "${HEAD_TICKET}" "${FILE1} (step 3: ## Ticket Details heading)"
      assert_contains "${VERIFY_SUBSTEP}" "${HEAD_IMPL}" "${FILE1} (step 3: ## Implementation Plan heading)"
      assert_contains "${VERIFY_SUBSTEP}" "${HEAD_ARCH}" "${FILE1} (step 3: ## Architectural Context heading)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_MUST_UPDATE}" "${FILE1} (step 3: same-PR co-update requirement)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_DO_NOT_SELF_REPAIR}" "${FILE1} (step 3: no self-repair for any of the three headings)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_DO_NOT_POST}" "${FILE1} (step 3: hard-stop no planComment)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_DO_NOT_LABEL}" "${FILE1} (step 3: hard-stop no label transition)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_DO_NOT_ARTIFACT}" "${FILE1} (step 3: hard-stop no artifact recording)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_DO_NOT_HASPLANFILE}" "${FILE1} (step 3: hard-stop no hasPlanFile)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_CONTEXT_BUNDLE_VS_PLANNER}" "${FILE1} (step 3: implicated assembly input)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_NEVER_POSTED}" "${FILE1} (step 3: ordering rationale, never posted to ticket)"
      assert_contains "${VERIFY_SUBSTEP}" "${MARK_NEVER_RECORDED}" "${FILE1} (step 3: ordering rationale, never recorded stale baseline)"
    fi
  fi

  # --- Whole-file assert_occurs_once: markers unique to the new text -------
  assert_occurs_once "${CONTENT1}" "${MARK_DO_NOT_POST}" "${FILE1} (occurs-once: do not post planComment)"
  assert_occurs_once "${CONTENT1}" "${MARK_DO_NOT_LABEL}" "${FILE1} (occurs-once: do not run label transition)"
  assert_occurs_once "${CONTENT1}" "${MARK_DO_NOT_ARTIFACT}" "${FILE1} (occurs-once: do not record plan artifact)"
  assert_occurs_once "${CONTENT1}" "${MARK_DO_NOT_HASPLANFILE}" "${FILE1} (occurs-once: do not set hasPlanFile)"
  assert_occurs_once "${CONTENT1}" "${MARK_MUST_UPDATE}" "${FILE1} (occurs-once: same-PR co-update requirement)"
  assert_occurs_once "${CONTENT1}" "${MARK_STEP3_HEADING}" "${FILE1} (occurs-once: step-3 heading line)"

  # --- Fast-path inheritance (whole-file; unique) ---------------------------
  assert_contains "${CONTENT1}" "${MARK_FAST_PATH_INHERITANCE}" "${FILE1} (Trivial Fast Path inherits step 3 explicitly)"
else
  fail "${FILE1}: could not read file, skipping its assertions"
fi

# --- flow/skills/implement/codex.md ------------------------------------------

FILE2="skills/implement/codex.md"
if require_doc CONTENT2 "${FILE2}"; then
  # The full three-heading parenthetical, asserted as a single whitespace-
  # normalized marker together with the planfile.go reference — bare
  # `## Ticket Details` already appears once elsewhere in codex.md (the
  # Technical Notes fallback) and would pass vacuously on its own.
  # `## Design Context` was dropped from this parenthetical along with the
  # rest of Pencil/design, and there is no self-repair left to assert on
  # (all three remaining headings are hard-stop-only).
  CODEX_THREE_HEADING_MARKER='(`## Ticket Details`, `## Implementation Plan`, `## Architectural Context` — the `requiredPlanSections` list in `watch/internal/pipeline/planfile.go`)'
  assert_contains_ws "${CONTENT2}" "${CODEX_THREE_HEADING_MARKER}" "${FILE2} (three-heading parenthetical + planfile.go reference)"
  assert_contains_ws "${CONTENT2}" 'stop before the checkpoint/label mutations' "${FILE2} (hard stop before checkpoint/label mutations)"
else
  fail "${FILE2}: could not read file, skipping its assertions"
fi

# --- Cross-boundary sync: watch/internal/pipeline/planfile.go ----------------

PLANFILE_GO="${REPO_ROOT}/watch/internal/pipeline/planfile.go"
if [[ -f "${PLANFILE_GO}" && -r "${PLANFILE_GO}" ]]; then
  extracted_literals="$(awk '
    /var requiredPlanSections = \[\]string\{/ { on=1; next }
    on && /^\}/ { exit }
    on { print }
  ' "${PLANFILE_GO}" | grep -oE '"[^"]*"' | sed 's/^"//; s/"$//' | LC_ALL=C sort)"
  expected_literals="$(printf '%s\n' "${HEAD_TICKET}" "${HEAD_IMPL}" "${HEAD_ARCH}" | LC_ALL=C sort)"
  if [[ "${extracted_literals}" != "${expected_literals}" ]]; then
    fail "watch/internal/pipeline/planfile.go: requiredPlanSections literals do not equal the three pinned headings:
--- extracted ---
${extracted_literals}
--- expected ---
${expected_literals}"
  fi
else
  echo "SKIP: watch/internal/pipeline/planfile.go not present (flow installed standalone); three-heading literals not cross-checked"
fi

echo "plan-persist-sections-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
