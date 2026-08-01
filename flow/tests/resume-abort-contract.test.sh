#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #880 (3/12 of #661): turns
# resume/re-escalation and every hard stop after the `Working` claim in
# flow/skills/implement/phases/phase-1-plan.md into a durable,
# crash-recoverable transaction. This suite is the TDD red phase for that
# ticket -- it asserts against content that does not exist yet, so every
# assertion below is expected to fail until #880's green phase lands:
#   - a new `## Restore Awaiting-Input State` section (the single named
#     routine every in-scope hard stop funnels through) stating its pinned
#     literals verbatim;
#   - a new `## Hard-Stop Inventory` table enumerating every hard stop after
#     `Working` across `## Resume From Draft`, `## Unattended Escalation
#     Path`, and `## Repair Escalation Anchor` with stable `HS-[UAR]<n>` IDs,
#     one-to-one with a citation at exactly one call site (bidirectional:
#     every inventoried ID has exactly one call site, and every call-site
#     citation is inventoried);
#   - persist-nonce-before-POST ordering in `## Resume From Draft` step 3 and
#     `## Repair Escalation Anchor` cases (ii)/(iii);
#   - the dot-prefixed candidate-plan path, assembled and validated before
#     ever replacing the draft;
#   - `## Unattended Escalation Path` step 0's explicit non-restoring
#     exception (nothing to restore before any draft exists);
#   - guards against duplicating flow/tests/plan-persist-sections-contract.test.sh's
#     pinned single-file-wide-occurrence markers.
#
# Fixture-free, grep/awk-based idiom of flow/tests/read-helper-purity-contract.test.sh
# and flow/tests/dispatch-resume-contract.test.sh: `set -uo pipefail`, a
# `failures` counter, fence-aware awk `extract_*_section` bounded to the next
# real (unfenced) "## " heading, pure `extract_*`/`first_line_of` helpers (no
# fail() side effect -- safe inside $(...)) paired with `require_*` nameref
# wrappers that call fail() in the parent shell, per
# flow/docs/shell-scripting-gotchas.md's fail()-inside-command-substitution
# rule and flow/tests/read-helper-purity-contract.test.sh's repo-wide scan.
# Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob -- no
# registration needed.
#
# Covered file: flow/skills/implement/phases/phase-1-plan.md only (per the
# plan's Files to Create entry for this suite).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "resume-abort-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "resume-abort-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

if ! PHASE1_CONTENT="$(cat "${PHASE1_PLAN}" 2>/dev/null)"; then
  fail "phase-1-plan.md: doc not found/unreadable: ${PHASE1_PLAN}"
  PHASE1_CONTENT=""
fi

# ---------------------------------------------------------------------------
# Pure extractors (no fail() side effect -- safe inside $(...))
# ---------------------------------------------------------------------------

# extract_section_raw <content> <heading-line> -- fence-aware awk extraction
# of the named "## <heading>" section, bounded to the next real (unfenced)
# "## " heading, or EOF.
extract_section_raw() {
  awk -v want="$2" '
    $0 == want && !on { on=1; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_case_ii_raw <repair-section-content> -- bounds case (ii) through
# (not including) case (iii).
extract_case_ii_raw() {
  awk '
    /^\*\*\(ii\)/ && !on { on=1; print; next }
    on && /^\*\*\(iii\)/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_case_iii_raw <repair-section-content> -- case (iii) through EOF (the
# last case in the section).
extract_case_iii_raw() {
  awk '
    /^\*\*\(iii\)/ { on=1 }
    on { print }
  ' <<<"$1"
}

# extract_step6_raw <resume-section-content> -- bounds numbered step 6
# ("**Persist the finalized plan.**") through (not including) step 7.
extract_step6_raw() {
  awk '
    /^6\. \*\*Persist the finalized plan\.\*\*/ && !on { on=1; print; next }
    on && /^7\. / { exit }
    on { print }
  ' <<<"$1"
}

# first_offset_of <content> <marker> -- 0-based BYTE offset of the marker's
# first occurrence, or empty if absent. Byte offset, not line number: this
# doc's paragraphs are frequently single unwrapped source lines carrying
# several pinned literals each, so a line-number-based ordering check cannot
# distinguish which of two same-line markers comes first (GNU grep's `-b -o`
# gives the offset of the match itself, not merely the line it starts on).
first_offset_of() {
  local content="$1" marker="$2"
  [[ -n "${marker}" ]] || { printf ''; return; }
  grep -abo -m1 -F -- "${marker}" <<<"${content}" | cut -d: -f1
}

# extract_hs_ids_unique / extract_hs_ids_all <content> -- every `HS-[UAR]<n>`
# token found, unique-sorted or all-occurrences-sorted (duplicates kept) for
# counting purposes.
extract_hs_ids_unique() {
  grep -oE 'HS-[UAR][0-9]+' <<<"$1" 2>/dev/null | LC_ALL=C sort -u
}
extract_hs_ids_all() {
  grep -oE 'HS-[UAR][0-9]+' <<<"$1" 2>/dev/null | LC_ALL=C sort
}

# ---------------------------------------------------------------------------
# Nameref wrappers (call fail() in the parent shell -- must NOT be invoked
# via $(...))
# ---------------------------------------------------------------------------

# require_section <result-var> <content> <heading-line> <label> -- assigns
# the extracted section body, or fails closed with a distinct "not found"
# message and assigns "" (a missing section must never masquerade as an
# empty-but-present one).
require_section() {
  local -n _result="$1"
  local _body
  _body="$(extract_section_raw "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section"
    _result=""
    return 1
  fi
  _result="${_body}"
}

# require_first_offset <result-var> <content> <marker> <label> -- assigns the
# 0-based byte offset, or fails loudly and assigns "-1" (never "0" -- a real
# match can legitimately start at offset 0, so the sentinel for "not found"
# must be a value no real offset can produce).
require_first_offset() {
  local -n _result="$1"
  local content="$2" marker="$3" label="$4"
  local off
  off="$(first_offset_of "${content}" "${marker}")"
  if [[ -z "${off}" ]]; then
    fail "${label}: marker not found: [${marker}]"
    _result=-1
  else
    _result="${off}"
  fi
}

# assert_contains <content> <needle> <label>
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label} (expected to contain: ${pattern})"
}

# assert_file_occurs_at_most_once <file> <needle> <label> -- protects a
# marker string pinned to exactly one file-wide occurrence by
# flow/tests/plan-persist-sections-contract.test.sh's assert_occurs_once from
# being duplicated by this ticket's restated restoration-routine prose.
assert_file_occurs_at_most_once() {
  local file="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty needle"; return; }
  [[ -f "${file}" ]] || { fail "${label}: file not found: ${file}"; return; }
  local count
  count="$(grep -cF -- "${pattern}" "${file}")"
  [[ "${count}" -le 1 ]] || fail "${label} (expected at most one file-wide occurrence, found ${count}: ${pattern})"
}

# =====================================================================
# 1. `## Restore Awaiting-Input State` -- the single named routine every
#    in-scope hard stop funnels through, stating its pinned literals verbatim.
# =====================================================================

require_section RESTORE_SECTION "${PHASE1_CONTENT}" "## Restore Awaiting-Input State" "phase-1-plan.md" || true

if [[ -n "${RESTORE_SECTION:-}" ]]; then
  assert_restore_contains() {
    assert_contains "${RESTORE_SECTION}" "$1" "phase-1-plan.md (## Restore Awaiting-Input State) $2"
  }
  assert_restore_contains 'cenci pipeline await-input <id>' \
    "must name the pinned command cenci pipeline await-input <id> verbatim"
  assert_restore_contains '--transition input-needed' \
    "must name the pinned label transition --transition input-needed verbatim"
  assert_restore_contains 'Input Needed' \
    "must name the pinned label Input Needed verbatim"
  assert_restore_contains 'status: awaiting-input' \
    "must name the pinned plan front matter status: awaiting-input verbatim"
  assert_restore_contains 'planCommitSha' \
    "must name planCommitSha verbatim (never rewritten -- the original freshness baseline)"
fi

# =====================================================================
# 2. `## Hard-Stop Inventory` -- bidirectional one-to-one coverage: every
#    inventoried HS-[UAR]<n> ID has exactly one call-site citation across the
#    three escalating sections, and no call-site citation is uninventoried.
# =====================================================================

require_section INVENTORY_SECTION "${PHASE1_CONTENT}" "## Hard-Stop Inventory" "phase-1-plan.md" || true
require_section RESUME_SECTION "${PHASE1_CONTENT}" "## Resume From Draft" "phase-1-plan.md" || true
require_section UNATTENDED_SECTION "${PHASE1_CONTENT}" "## Unattended Escalation Path" "phase-1-plan.md" || true
require_section REPAIR_SECTION "${PHASE1_CONTENT}" "## Repair Escalation Anchor" "phase-1-plan.md" || true

if [[ -n "${INVENTORY_SECTION:-}" ]]; then
  INVENTORY_IDS="$(extract_hs_ids_unique "${INVENTORY_SECTION}")"
  if [[ -z "${INVENTORY_IDS}" ]]; then
    fail "phase-1-plan.md (## Hard-Stop Inventory) must enumerate at least one HS-[UAR]<n> row ID"
  else
    CALLSITES_COMBINED="${RESUME_SECTION:-}"$'\n'"${UNATTENDED_SECTION:-}"$'\n'"${REPAIR_SECTION:-}"
    ALL_CALLSITE_IDS="$(extract_hs_ids_all "${CALLSITES_COMBINED}")"

    # Every inventoried ID must appear at exactly one call site.
    while IFS= read -r id; do
      [[ -z "${id}" ]] && continue
      count=$(grep -c -x -F -- "${id}" <<<"${ALL_CALLSITE_IDS}")
      if [[ "${count}" -ne 1 ]]; then
        fail "phase-1-plan.md: hard-stop ID ${id} must be restated at exactly one call site across ## Resume From Draft / ## Unattended Escalation Path / ## Repair Escalation Anchor (found ${count})"
      fi
    done <<<"${INVENTORY_IDS}"

    # No call-site citation may be uninventoried (drift protection).
    CALLSITE_UNIQUE_IDS="$(printf '%s\n' "${ALL_CALLSITE_IDS}" | LC_ALL=C sort -u)"
    while IFS= read -r id; do
      [[ -z "${id}" ]] && continue
      if ! grep -q -x -F -- "${id}" <<<"${INVENTORY_IDS}"; then
        fail "phase-1-plan.md: hard-stop ID ${id} is cited at a call site but is not listed in ## Hard-Stop Inventory"
      fi
    done <<<"${CALLSITE_UNIQUE_IDS}"
  fi
fi

# =====================================================================
# 3. Ordering: persist-nonce precedes the POST literal, which precedes the
#    readback literal, which precedes persist-comment-ID -- in
#    `## Resume From Draft` step 3, and in `## Repair Escalation Anchor`
#    cases (ii)/(iii).
# =====================================================================

# --- ## Resume From Draft step 3 --------------------------------------------
if [[ -n "${RESUME_SECTION:-}" ]]; then
  RESUME_ORDER_LABEL="phase-1-plan.md (## Resume From Draft step 3 ordering)"
  require_first_offset RO1 "${RESUME_SECTION}" 'persist the fresh `escalationNonce` and clear `escalationCommentId` in one write, verify by re-read' "${RESUME_ORDER_LABEL}"
  require_first_offset RO2 "${RESUME_SECTION}" 'gh api repos/<owner>/<repo>/issues/<number>/comments -F body=@<questions-file> --jq .id' "${RESUME_ORDER_LABEL}"
  require_first_offset RO3 "${RESUME_SECTION}" "gh api repos/<owner>/<repo>/issues/<number>/comments/<id> --jq '{id, body}'" "${RESUME_ORDER_LABEL}"
  require_first_offset RO4 "${RESUME_SECTION}" 'persist `escalationCommentId`, verify by re-read' "${RESUME_ORDER_LABEL}"
  if ! { [[ "${RO1}" -lt "${RO2}" ]] && [[ "${RO2}" -lt "${RO3}" ]] && [[ "${RO3}" -lt "${RO4}" ]]; }; then
    fail "${RESUME_ORDER_LABEL} violated (expected strictly increasing): persist-nonce=${RO1} post=${RO2} readback=${RO3} persist-comment-id=${RO4}"
  fi
fi

# --- ## Repair Escalation Anchor case (iii): mint+persist a fresh nonce, so
#     the full four-part chain applies ----------------------------------------
if [[ -n "${REPAIR_SECTION:-}" ]]; then
  CASE_III="$(extract_case_iii_raw "${REPAIR_SECTION}")"
  if [[ -z "${CASE_III}" ]]; then
    fail "phase-1-plan.md: could not locate '## Repair Escalation Anchor' case (iii) (extract_case_iii_raw returned empty)"
  else
    CASE_III_LABEL="phase-1-plan.md (## Repair Escalation Anchor case (iii) ordering)"
    require_first_offset C3O1 "${CASE_III}" 'persist it into `escalationNonce`' "${CASE_III_LABEL}"
    require_first_offset C3O2 "${CASE_III}" 'gh api repos/<owner>/<repo>/issues/<number>/comments -F body=@<questions-file> --jq .id' "${CASE_III_LABEL}"
    require_first_offset C3O3 "${CASE_III}" "gh api repos/<owner>/<repo>/issues/<number>/comments/<id> --jq '{id, body}'" "${CASE_III_LABEL}"
    require_first_offset C3O4 "${CASE_III}" 'persist the ID into `escalationCommentId`' "${CASE_III_LABEL}"
    if ! { [[ "${C3O1}" -lt "${C3O2}" ]] && [[ "${C3O2}" -lt "${C3O3}" ]] && [[ "${C3O3}" -lt "${C3O4}" ]]; }; then
      fail "${CASE_III_LABEL} violated (expected strictly increasing): persist-nonce=${C3O1} post=${C3O2} readback=${C3O3} persist-comment-id=${C3O4}"
    fi
  fi

  # --- case (ii): reuses the existing nonce (never mints one), so only the
  #     POST < readback < persist-comment-ID chain applies -------------------
  CASE_II="$(extract_case_ii_raw "${REPAIR_SECTION}")"
  if [[ -z "${CASE_II}" ]]; then
    fail "phase-1-plan.md: could not locate '## Repair Escalation Anchor' case (ii) (extract_case_ii_raw returned empty)"
  else
    CASE_II_LABEL="phase-1-plan.md (## Repair Escalation Anchor case (ii) ordering)"
    require_first_offset C2O1 "${CASE_II}" 'gh api repos/<owner>/<repo>/issues/<number>/comments -F body=@<questions-file> --jq .id' "${CASE_II_LABEL}"
    require_first_offset C2O2 "${CASE_II}" "gh api repos/<owner>/<repo>/issues/<number>/comments/<id> --jq '{id, body}'" "${CASE_II_LABEL}"
    require_first_offset C2O3 "${CASE_II}" 'persist the ID into `escalationCommentId`' "${CASE_II_LABEL}"
    if ! { [[ "${C2O1}" -lt "${C2O2}" ]] && [[ "${C2O2}" -lt "${C2O3}" ]]; }; then
      fail "${CASE_II_LABEL} violated (expected strictly increasing): post=${C2O1} readback=${C2O2} persist-comment-id=${C2O3}"
    fi
  fi
fi

# =====================================================================
# 4. Candidate-plan path: assembled and validated before ever replacing the
#    draft; the draft path is never a Write target before validation.
# =====================================================================

CANDIDATE_PATH_LITERAL='.plans/.<id>-<slug>.candidate.md'
DRAFT_PATH_LITERAL='.plans/<id>-<slug>.md'
MV_LITERAL='atomically `mv` over the draft only on success'

assert_contains "${PHASE1_CONTENT}" "${CANDIDATE_PATH_LITERAL}" \
  "phase-1-plan.md must name the dot-prefixed candidate-plan path literal verbatim"

if [[ -n "${RESUME_SECTION:-}" ]]; then
  STEP6="$(extract_step6_raw "${RESUME_SECTION}")"
  if [[ -z "${STEP6}" ]]; then
    fail "phase-1-plan.md: could not locate '## Resume From Draft' step 6 (extract_step6_raw returned empty)"
  else
    assert_contains "${STEP6}" "${CANDIDATE_PATH_LITERAL}" \
      "phase-1-plan.md (## Resume From Draft step 6) must assemble to the candidate path, not the draft path directly"
    STEP6_LABEL="phase-1-plan.md (## Resume From Draft step 6 candidate-then-replace ordering)"
    require_first_offset S6C "${STEP6}" "${CANDIDATE_PATH_LITERAL}" "${STEP6_LABEL}"
    require_first_offset S6M "${STEP6}" "${MV_LITERAL}" "${STEP6_LABEL}"
    if [[ "${S6M}" -ge 0 && "${S6C}" -ge 0 ]] && ! [[ "${S6C}" -lt "${S6M}" ]]; then
      fail "${STEP6_LABEL} violated: the candidate must be assembled (offset ${S6C}) before the atomic replace (offset ${S6M})"
    fi

    # Never a Write target before validation: if the bare draft-path literal
    # appears in step 6 at all, it must be used only at/after the atomic
    # replace point (the mv's destination), never earlier as a write target
    # a malformed candidate could otherwise clobber.
    DP_OFFSET="$(first_offset_of "${STEP6}" "${DRAFT_PATH_LITERAL}")"
    if [[ -n "${DP_OFFSET}" ]]; then
      if [[ "${S6M}" -lt 0 ]] || [[ "${DP_OFFSET}" -lt "${S6M}" ]]; then
        fail "${STEP6_LABEL}: the draft path (${DRAFT_PATH_LITERAL}) must never be used as a target before the atomic replace point (found at offset ${DP_OFFSET})"
      fi
    fi
  fi
fi

# =====================================================================
# 5. `## Unattended Escalation Path` step 0's explicit non-restoring
#    exception: nonce mint failing before any draft exists has nothing to
#    restore, so it stays a bare-`Working` stop -- stated explicitly, not
#    silently omitted.
# =====================================================================

if [[ -n "${UNATTENDED_SECTION:-}" ]]; then
  assert_unattended_contains() {
    assert_contains "${UNATTENDED_SECTION}" "$1" "phase-1-plan.md (## Unattended Escalation Path) $2"
  }
  assert_unattended_contains 'nothing to restore' \
    "step 0's exception must state explicitly that there is nothing to restore before any draft exists"
  assert_unattended_contains 'bare-`Working` stop' \
    "step 0's exception must state explicitly that it stays a bare-Working stop"
fi

# =====================================================================
# 6. Negative: must not duplicate plan-persist-sections-contract.test.sh's
#    pinned single-file-wide-occurrence markers.
# =====================================================================

assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** post the `planComment` audit comment' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-post-planComment marker (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** run the `Planned` label transition' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-run-label-transition marker (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** record the plan artifact' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-record-artifact marker (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** set `hasPlanFile = true`' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-set-hasPlanFile marker (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'watch/internal/pipeline/planfile.go:49-55' \
  "must not duplicate plan-persist-sections-contract.test.sh's planfile.go citation (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'MUST update this step in the same PR' \
  "must not duplicate plan-persist-sections-contract.test.sh's same-PR co-update marker (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" '3. **Verify the assembled plan.**' \
  "must not duplicate plan-persist-sections-contract.test.sh's step-3 heading marker (restate with distinct wording)"

# =====================================================================
# 7. Strengthened bidirectional check: verifying an HS-* ID token appears
#    exactly once (section 2 above) is not enough -- it does not verify the
#    call site's surrounding text actually restates the routine's name or
#    its two pinned commands, which is exactly why gap #1 (steps 1, 2, 4 of
#    `## Resume From Draft` citing their ID but only running the old
#    pre-existing "Abort contract" boilerplate) passed this suite
#    undetected before #880's review fix. For every HS-[UAR]<n> occurrence,
#    this asserts the text from that occurrence through the next HS-*
#    occurrence (its "call site window") names the routine
#    (`## Restore Awaiting-Input State` / `Restore Awaiting-Input State`)
#    or restates both of its pinned commands (`cenci pipeline await-input
#    <id>` / `--transition input-needed`) -- outside the generic "Abort
#    contract applies here" disclaimer sentence, which merely repeats the
#    commands without citing the routine and is exactly gap #1's failure
#    mode. A pure awk token-stream split (not byte-offset slicing) is used
#    to build each window, since this doc's prose is full of multi-byte
#    em dashes that would desync a `grep -abo` byte offset from a bash
#    character-index substring.
# =====================================================================

# callsite_windows <content> -- prints, for every HS-[UAR]<n> token found in
# <content> in document order, a block delimited by sentinel lines:
#   ===HSID===<id>
#   <all text from this token's occurrence up to (not including) the next
#    HS-* token's occurrence, or EOF>
#   ===HSEND===
# Pure token-stream split (single awk pass, no fail() side effect -- safe to
# call inside $(...) via process substitution below).
callsite_windows() {
  awk '
    {
      line = $0
      while (match(line, /HS-[UAR][0-9]+/)) {
        tok = substr(line, RSTART, RLENGTH)
        pre = substr(line, 1, RSTART - 1)
        if (curid != "") { buf[curid] = buf[curid] pre "\n" }
        if (!(tok in seen)) { order[++n] = tok; seen[tok] = 1 }
        curid = tok
        line = substr(line, RSTART + RLENGTH)
      }
      if (curid != "") { buf[curid] = buf[curid] line "\n" }
    }
    END {
      for (i = 1; i <= n; i++) {
        print "===HSID===" order[i]
        printf "%s", buf[order[i]]
        print "===HSEND==="
      }
    }
  ' <<<"$1"
}

# window_cites_routine <window-text> -- true (0) if the window names the
# routine by heading text, or restates both pinned commands outside the
# generic "Abort contract applies here" disclaimer (gap #1's failure mode:
# citing an ID but only running that shared boilerplate, never naming the
# routine or restating its commands standalone).
window_cites_routine() {
  local window="$1"
  [[ "${window}" == *'Restore Awaiting-Input State'* ]] && return 0
  if [[ "${window}" == *'cenci pipeline await-input <id>'* ]] \
    && [[ "${window}" == *'--transition input-needed'* ]] \
    && [[ "${window}" != *'Abort contract applies here'* ]]; then
    return 0
  fi
  return 1
}

# assert_callsites_cite_routine <content> <label> -- parses callsite_windows'
# sentinel-delimited output in the parent shell (fail() has a side effect,
# so this must not run inside $(...) itself) and fails for every HS-* call
# site whose window does not satisfy window_cites_routine.
assert_callsites_cite_routine() {
  local content="$1" label="$2"
  local cur_id="" window="" in_block=0 line
  while IFS= read -r line; do
    if [[ "${line}" == "===HSID==="* ]]; then
      if [[ -n "${cur_id}" ]]; then
        window_cites_routine "${window}" || \
          fail "${label}: call site ${cur_id} must restate '## Restore Awaiting-Input State' by name, or both its pinned commands outside the generic Abort-contract disclaimer -- not just the bare ID token (#880 review fix)"
      fi
      cur_id="${line#===HSID===}"
      window=""
      in_block=1
      continue
    fi
    if [[ "${line}" == "===HSEND===" ]]; then
      continue
    fi
    if [[ "${in_block}" -eq 1 ]]; then
      window+="${line}"$'\n'
    fi
  done < <(callsite_windows "${content}")
  if [[ -n "${cur_id}" ]]; then
    window_cites_routine "${window}" || \
      fail "${label}: call site ${cur_id} must restate '## Restore Awaiting-Input State' by name, or both its pinned commands outside the generic Abort-contract disclaimer -- not just the bare ID token (#880 review fix)"
  fi
}

if [[ -n "${RESUME_SECTION:-}" ]]; then
  assert_callsites_cite_routine "${RESUME_SECTION}" "phase-1-plan.md (## Resume From Draft)"
fi
if [[ -n "${UNATTENDED_SECTION:-}" ]]; then
  assert_callsites_cite_routine "${UNATTENDED_SECTION}" "phase-1-plan.md (## Unattended Escalation Path)"
fi
if [[ -n "${REPAIR_SECTION:-}" ]]; then
  assert_callsites_cite_routine "${REPAIR_SECTION}" "phase-1-plan.md (## Repair Escalation Anchor)"
fi

# =====================================================================
# 8. HS-R12's two independent verification conditions (destination
#    `status: planned` check AND candidate-path-gone check) -- both must be
#    named verbatim; a regression that silently drops either one would
#    leave the post-`mv` verification only half-checking the replace.
# =====================================================================

if [[ -n "${RESUME_SECTION:-}" ]]; then
  STEP6_HSR12="$(extract_step6_raw "${RESUME_SECTION}")"
  if [[ -z "${STEP6_HSR12}" ]]; then
    fail "phase-1-plan.md: could not locate '## Resume From Draft' step 6 for HS-R12 checks (extract_step6_raw returned empty)"
  else
    assert_contains "${STEP6_HSR12}" 'HS-R12' \
      "phase-1-plan.md (## Resume From Draft step 6) must cite HS-R12 at the post-mv verification sub-step"
    assert_contains "${STEP6_HSR12}" 'confirm it now carries `status: planned`' \
      "phase-1-plan.md (## Resume From Draft step 6, HS-R12) must name the destination status: planned verification condition verbatim"
    assert_contains "${STEP6_HSR12}" 'the candidate path (`"<repo-root>/.plans/.<id>-<slug>.candidate.md"`) no longer exists on disk' \
      "phase-1-plan.md (## Resume From Draft step 6, HS-R12) must name the candidate-path-gone verification condition verbatim"
  fi
fi

# =====================================================================
# 9. The entry recovery probe (`## Resume From Draft` step 3, HS-R3) has
#    three distinct named outcomes -- genuine match, zero matches, author
#    mismatch -- each with its own nonce-handling behavior (reuse the
#    existing nonce vs. mint a fresh one). The author-mismatch branch must
#    route to the persist-nonce sub-step (HS-R4) before falling through to
#    the remaining sub-steps, mirroring case (iii) -- this is the #880
#    review-fix regression this suite must pin.
# =====================================================================

# extract_entry_probe_raw <resume-section-content> -- bounds the
# "Entry recovery probe / mint (HS-R3)" bullet through (not including) the
# next bullet, "Persist the nonce and clear the stale comment ID... (HS-R4)".
extract_entry_probe_raw() {
  awk '
    /Entry recovery probe \/ mint \(HS-R3\)/ && !on { on=1; print; next }
    on && /Persist the nonce and clear the stale comment ID, before posting anything \(HS-R4\)/ { exit }
    on { print }
  ' <<<"$1"
}

if [[ -n "${RESUME_SECTION:-}" ]]; then
  ENTRY_PROBE="$(extract_entry_probe_raw "${RESUME_SECTION}")"
  if [[ -z "${ENTRY_PROBE}" ]]; then
    fail "phase-1-plan.md: could not locate '## Resume From Draft' step 3's entry recovery probe (extract_entry_probe_raw returned empty)"
  else
    assert_probe_contains() {
      assert_contains "${ENTRY_PROBE}" "$1" "phase-1-plan.md (## Resume From Draft step 3 entry recovery probe) $2"
    }
    # Outcome 1: genuine match -- reuses the existing (already-persisted) nonce.
    assert_probe_contains '**a genuine match**' \
      "must name the genuine-match outcome verbatim"
    assert_probe_contains 'reusing the existing nonce — never re-minting it' \
      "genuine-match outcome must reuse the existing nonce, never mint a fresh one"
    # Outcome 2: zero matches -- also reuses the existing (unposted) nonce.
    assert_probe_contains '**Zero matches found**' \
      "must name the zero-matches outcome verbatim"
    assert_probe_contains 'reusing the *existing* nonce (never re-minting)' \
      "zero-matches outcome must reuse the existing nonce, never mint a fresh one"
    # Outcome 3: author mismatch -- mints a fresh nonce AND must persist it
    # (HS-R4) before falling through to the remaining sub-steps -- the
    # exact bug this review-fix cycle corrected.
    assert_probe_contains "earliest match whose author login does not equal the authenticated user's own login" \
      "must name the author-mismatch outcome verbatim"
    assert_probe_contains 'mint a **fresh** nonce, then continue with the persist-nonce-and-clear-`escalationCommentId` sub-step below' \
      "author-mismatch outcome must route through the persist-nonce sub-step (HS-R4) before falling through to the remaining sub-steps, mirroring case (iii) (#880 review fix)"
  fi
fi

echo "resume-abort-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
