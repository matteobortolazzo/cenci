#!/usr/bin/env bash
# adversarial-chain/hardstop-lib.sh -- doc-derived case-list extraction,
# classification, the three canonical per-section command streams, and the
# stateful anchor ledger backing flow/tests/escalation-hardstop-matrix.test.sh
# (ticket #913, AC2, 2/5 of #887).
#
# Sourced by the suite, never executed standalone. Follows the same idiom as
# flow/tests/adversarial-chain/lib.sh and flow/tests/refine-write-order/lib.sh
# (siblings): pure `<name>_raw` extractors/oracles (safe inside `$(...)`,
# never call `fail()`) paired with `require_<name>` nameref wrappers that DO
# call `fail()`, invoked directly (never through `$(...)`) so the failure
# lands in the sourcing suite's own `failures` counter -- see
# flow/docs/shell-scripting-gotchas.md's "never call fail() from within
# $(...)" rule and flow/tests/read-helper-purity-contract.test.sh, whose
# LIB_PATHSPEC='flow/tests/*.sh' scans this file recursively too.
#
# `flow/skills/implement/phases/phase-1-plan.md` is READ-ONLY here (#887
# Technical Notes / this ticket's Assumptions) -- this file only extracts
# from its content, never writes to it, and the non-vacuity self-test in the
# suite operates on a deliberately-broken IN-MEMORY copy of that content,
# never a copy written back to disk.
#
# Sibling-file layout (Decision 6): sources flow/tests/adversarial-chain/
# lib.sh for GOLDEN_GRAPH_FIXTURE / extract_golden_field_raw reuse (the
# ledger's identities come from the SAME golden fixture #912 already
# committed -- no second fixture). This file NEVER sources
# flow/tests/refine-write-order/lib.sh or
# flow/tests/parent-close-reconcile/lib.sh -- the escalation path is
# markdown-only (Decision 3/4: a stateful ledger model, not a real gh stub
# replay), so neither sub-library's replay_through_stub is needed here, and
# the hard collision constraint documented at the top of
# flow/tests/adversarial-chain/lib.sh (colliding _trim /
# _substitute_placeholders / incompatible replay_through_stub signatures)
# never applies to this file.
set -uo pipefail

_HARDSTOP_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || {
  echo "hardstop-lib.sh: failed to resolve its own directory." >&2
  return 1 2>/dev/null || exit 1
}

_HARDSTOP_FLOW_DIR="$(cd "${_HARDSTOP_LIB_DIR}/../.." && pwd)" || {
  echo "hardstop-lib.sh: failed to resolve flow directory." >&2
  return 1 2>/dev/null || exit 1
}
PHASE1_PLAN_DOC="${_HARDSTOP_FLOW_DIR}/skills/implement/phases/phase-1-plan.md"

# ---------------------------------------------------------------------------
# _hardstop_load_golden_fields
#
# Sources adversarial-chain/lib.sh (for GOLDEN_GRAPH_FIXTURE /
# extract_golden_field_raw reuse -- Decision 6) ONLY inside this function's
# own subshell body, never at this file's top level -- the Files to Create
# commitment. Every symbol lib.sh defines (extract_golden_field_raw,
# drive_stage_*, GOLDEN_GRAPH_FIXTURE, etc.) is therefore forked away with
# the subshell and never leaks into the shell that sources this file.
# Prints "<parent>\t<child1>\t<child2>" on stdout; the one-shot call site
# below splits that back into the plain top-level HARDSTOP_GOLDEN_* string
# variables that downstream code (this file and
# escalation-hardstop-matrix.test.sh) depend on. Per-field extraction
# failure still degrades to an empty field (not a hard failure) exactly as
# before -- downstream ledger primitives reject an empty id fail-closed,
# surfacing as a real, informative test failure rather than aborting the
# whole file load. A `source` failure itself is still a hard failure.
# ---------------------------------------------------------------------------
_hardstop_load_golden_fields() {
  (
    # shellcheck source=./lib.sh
    source "${_HARDSTOP_LIB_DIR}/lib.sh" || {
      echo "hardstop-lib.sh: failed to source adversarial-chain/lib.sh" >&2
      exit 1
    }
    local parent child1 child2
    parent="$(extract_golden_field_raw '.parent.number' 2>/dev/null)" || parent=""
    child1="$(extract_golden_field_raw '.children[0].number' 2>/dev/null)" || child1=""
    child2="$(extract_golden_field_raw '.children[1].number' 2>/dev/null)" || child2=""
    printf '%s\t%s\t%s\n' "${parent}" "${child1}" "${child2}"
  )
}

# Golden identities (plan Assumption / requirement #10): reuse
# GOLDEN_GRAPH_FIXTURE's own parent/child issue numbers for the ledger's
# comment/anchor identities rather than carrying a second fixture.
_HARDSTOP_GOLDEN_TSV="$(_hardstop_load_golden_fields)" || {
  echo "hardstop-lib.sh: failed to load golden identities from adversarial-chain/lib.sh" >&2
  return 1 2>/dev/null || exit 1
}
IFS=$'\t' read -r HARDSTOP_GOLDEN_PARENT HARDSTOP_GOLDEN_CHILD1 HARDSTOP_GOLDEN_CHILD2 <<<"${_HARDSTOP_GOLDEN_TSV}"
unset _HARDSTOP_GOLDEN_TSV

# ---------------------------------------------------------------------------
# extract_section_raw <content> <heading-line>
#
# Pure, no fail(). Fence-aware awk extraction of the named "## <heading>"
# section, bounded to the next real (unfenced) "## " heading, or EOF. Same
# idiom as flow/tests/resume-abort-contract.test.sh:60-67 -- reused here
# rather than reinvented, per this ticket's Architectural Context.
# ---------------------------------------------------------------------------
extract_section_raw() {
  awk -v want="$2" '
    $0 == want && !on { on=1; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# ---------------------------------------------------------------------------
# extract_hard_stop_rows_raw <content>
#
# Pure, no fail(). Fence-aware markdown-table parse of the
# "## Hard-Stop Inventory" section, emitting one "<id>\t<section>\t<trigger>"
# line per data row (the table's "Restoring?" column is intentionally
# dropped -- Decision 1's HS-U0 zero-anchor carve-out is encoded directly in
# the suite's own CASE_TABLE, never re-derived from this column). Fail-closed
# (returns 1, no output) on empty content, a missing section, or zero
# matched rows -- never a vacuous pass on any of those.
# ---------------------------------------------------------------------------
extract_hard_stop_rows_raw() {
  local content="$1"
  [[ -n "${content}" ]] || return 1
  local section
  section="$(extract_section_raw "${content}" "## Hard-Stop Inventory")"
  [[ -n "${section}" ]] || return 1
  local rows
  rows="$(awk -F'|' '
    /^\|[ \t]*HS-[UAR][0-9]+[ \t]*\|/ {
      id=$2; sect=$3; trig=$4
      gsub(/^[ \t]+|[ \t]+$/, "", id)
      gsub(/^[ \t]+|[ \t]+$/, "", sect)
      gsub(/^[ \t]+|[ \t]+$/, "", trig)
      print id "\t" sect "\t" trig
    }
  ' <<<"${section}")"
  [[ -n "${rows}" ]] || return 1
  printf '%s\n' "${rows}"
}

# require_hard_stop_rows <result-var> <content> <label>
require_hard_stop_rows() {
  local -n _result="$1"
  local content="$2" label="$3"
  local rows
  if ! rows="$(extract_hard_stop_rows_raw "${content}")"; then
    fail "${label}: failed to extract ## Hard-Stop Inventory rows (missing section, unreadable content, or zero rows)"
    _result=""
    return 1
  fi
  _result="${rows}"
}

# ---------------------------------------------------------------------------
# classify_hard_stop_kind_raw <id> <trigger-text>
#
# Pure, no fail(), total (always prints exactly one of the eight tokens
# below and returns 0 -- "no match" is itself a valid classification, never
# an extraction failure). Maps a Hard-Stop Inventory row to one of the seven
# named boundary kinds (nonce persistence, POST acceptance, readback, ID
# persistence, label mutation, planner delegation, candidate-plan
# validation), or "none".
#
# Decision 2's two exclusions are checked FIRST, by ID -- never derived
# solely from incidental trigger wording, so a future doc rewording of
# either row's Trigger text can never silently re-admit it:
#   - HS-U1 (draft assembly): escalationNonce first LANDS in this step's
#     front matter, but "nonce persistence" is explicitly scoped to
#     mint/validate/persist-and-clear only.
#   - HS-U4 (`cenci pipeline await-input <id>`): the STAGE call, not a
#     `--transition` label call -- excluded from "label mutation".
#
# Priority-ordered pattern rules below (checked in this order): a Trigger
# mentioning more than one kind (e.g. "POST/numeric-ID/readback check
# fails" mentions both POST acceptance and readback) still resolves to
# exactly one non-ambiguous kind. A row need only implement ONE of the
# seven to qualify for the case list, so which specific kind wins among
# several present in one Trigger cell is not itself load-bearing.
# ---------------------------------------------------------------------------
classify_hard_stop_kind_raw() {
  local id="$1" trigger="$2"

  case "${id}" in
    HS-U1)
      printf 'none'
      return 0
      ;;
    HS-U4)
      printf 'none'
      return 0
      ;;
  esac

  case "${trigger}" in
    *openssl*|*mint*|*persist-nonce*|*fresh-nonce*)
      printf 'nonce persistence' ;;
    *'POST/numeric-ID'*)
      printf 'POST acceptance' ;;
    *readback*)
      printf 'readback' ;;
    *escalationCommentId*persist*|*'ID persist-and-verify'*)
      printf 'ID persistence' ;;
    *--transition*)
      printf 'label mutation' ;;
    *'planner delegation'*)
      printf 'planner delegation' ;;
    *'candidate-plan'*|*'four-heading validation'*|*'post-replace verification'*)
      printf 'candidate-plan validation' ;;
    *)
      printf 'none' ;;
  esac
  return 0
}

# ---------------------------------------------------------------------------
# _hardstop_qualifying_ids_raw <rows-tsv>
#
# Pure, no fail(). <rows-tsv> is extract_hard_stop_rows_raw's own output.
# Prints the sorted-unique set of IDs whose classify_hard_stop_kind_raw
# result is not "none". Fail-closed (returns 1, no output) on empty input or
# zero qualifying rows.
# ---------------------------------------------------------------------------
_hardstop_qualifying_ids_raw() {
  local rows="$1"
  [[ -n "${rows}" ]] || return 1
  local id sect trig kind out=""
  while IFS=$'\t' read -r id sect trig; do
    [[ -n "${id}" ]] || continue
    kind="$(classify_hard_stop_kind_raw "${id}" "${trig}")"
    [[ "${kind}" != "none" ]] || continue
    out+="${id}"$'\n'
  done <<<"${rows}"
  [[ -n "${out}" ]] || return 1
  printf '%s' "${out}" | LC_ALL=C sort -u
}

# ---------------------------------------------------------------------------
# _hardstop_stream_group_raw <section-step-text>
#
# Pure, no fail(). Maps a row's "Section / Step" column text to one of the
# five stream groups this file authors a canonical command stream for:
# unattended, repair-i, repair-ii, repair-iii, resume. Fail-closed (returns
# 1, no output) on unrecognized text. "(i)"/"(ii)"/"(iii)" are checked
# longest-first (defensive; they are mutually exclusive literal substrings
# of each other regardless of order -- "(ii)" never contains the exact
# substring "(i)" since its two i's are adjacent, so ordering is not itself
# load-bearing here).
# ---------------------------------------------------------------------------
_hardstop_stream_group_raw() {
  local sect="$1"
  case "${sect}" in
    *'Unattended Escalation Path'*) printf 'unattended' ;;
    *'Repair Escalation Anchor'*'(iii)'*) printf 'repair-iii' ;;
    *'Repair Escalation Anchor'*'(ii)'*) printf 'repair-ii' ;;
    *'Repair Escalation Anchor'*'(i)'*) printf 'repair-i' ;;
    *'Resume From Draft'*) printf 'resume' ;;
    *) return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# _hardstop_group_qualifying_ids_raw <rows-tsv> <group>
#
# Pure, no fail(). The sorted-unique qualifying IDs (classify != "none")
# whose Section/Step column resolves (via _hardstop_stream_group_raw) to
# <group>. Fail-closed (returns 1, no output) on empty input or zero
# matches.
# ---------------------------------------------------------------------------
_hardstop_group_qualifying_ids_raw() {
  local rows="$1" want_group="$2"
  [[ -n "${rows}" && -n "${want_group}" ]] || return 1
  local id sect trig kind grp out=""
  while IFS=$'\t' read -r id sect trig; do
    [[ -n "${id}" ]] || continue
    kind="$(classify_hard_stop_kind_raw "${id}" "${trig}")"
    [[ "${kind}" != "none" ]] || continue
    grp="$(_hardstop_stream_group_raw "${sect}")" || continue
    [[ "${grp}" == "${want_group}" ]] || continue
    out+="${id}"$'\n'
  done <<<"${rows}"
  [[ -n "${out}" ]] || return 1
  printf '%s' "${out}" | LC_ALL=C sort -u
}

# ---------------------------------------------------------------------------
# _hardstop_stream_ids_raw <stream>
#
# Pure, no fail(). The sorted-unique set of boundary-label IDs (column 1)
# appearing in an authored stream.
# ---------------------------------------------------------------------------
_hardstop_stream_ids_raw() {
  local stream="$1"
  [[ -n "${stream}" ]] || return 1
  cut -f1 <<<"${stream}" | LC_ALL=C sort -u
}

# ---------------------------------------------------------------------------
# The three canonical per-section command streams (Decision 4: hand-authored
# here, matching #912's drive_stage_refine pattern -- the doc's fenced
# blocks are too sparse to mechanically extract them; only the HS-* row set,
# section, and boundary ordering stay doc-derived, verified by Section 2's
# stream/doc fidelity check in the suite). Repair-Anchor is authored as
# THREE independent streams (cases (i)/(ii)/(iii) are mutually exclusive
# alternative branches in the doc -- never a sequential pipeline -- so each
# gets its own fresh ledger seed in the suite, never a shared running one).
#
# Format: one line per op, tab-separated "<HS-ID>\t<op>\t[arg1]\t[arg2]".
# ONLY qualifying HS-* IDs appear as boundary labels (Section 2's fidelity
# check asserts this) -- every excluded row's real-world effect (e.g.
# HS-U1's draft-assembly nonce landing, HS-A2/A6's mkdir, HS-R1/R2/R5/R9's
# no-ledger-effect steps) is folded into the ledger effect of the next
# qualifying boundary in the same stream, since those rows are never
# truncation points a case is tested against.
#
# Ops interpreted by _hardstop_apply_ops below:
#   noop                    -- no ledger effect (stage calls, reads, label
#                               re-applies, planner delegation,
#                               candidate-plan steps -- none of these touch
#                               nonce/id/comments).
#   persist_nonce <nonce>   -- sets draft.escalationNonce, clearing
#                               draft.escalationCommentId in the same write
#                               (mirrors the doc's real combined write).
#   escalate <nonce> <id>   -- persist_nonce(<nonce>) then post(<id>): folds
#                               HS-U1's draft-assembly nonce-persist together
#                               with HS-U2's POST under HS-U2's own label.
#   post <id>               -- appends a new ledger comment {<id>, CURRENT
#                               draft nonce} (the marker embeds whatever
#                               nonce is already persisted, exactly as the
#                               doc's real POST does).
#   persist_id <id>         -- sets draft.escalationCommentId = <id>
#                               (nonce unchanged).
#   scan_persist_id <id>    -- identical ledger effect to persist_id; kept
#                               as a distinct op name purely for readability
#                               (case (i) locates an ALREADY-posted comment
#                               by nonce-scan rather than posting a new one).
# ---------------------------------------------------------------------------

UNATTENDED_ESCALATION_STREAM="$(printf 'HS-U0\tnoop\nHS-U2\tescalate\tnonce-unattended\t%s\nHS-U3\tpersist_id\t%s\nHS-U5\tnoop\n' \
  "${HARDSTOP_GOLDEN_PARENT}" "${HARDSTOP_GOLDEN_PARENT}")"

REPAIR_CASE_I_STREAM="$(printf 'HS-A1\tscan_persist_id\t%s\nHS-A9\tnoop\n' "${HARDSTOP_GOLDEN_CHILD1}")"

REPAIR_CASE_II_STREAM="$(printf 'HS-A3\tpost\t%s\nHS-A4\tpersist_id\t%s\nHS-A10\tnoop\n' \
  "${HARDSTOP_GOLDEN_CHILD2}" "${HARDSTOP_GOLDEN_CHILD2}")"

REPAIR_CASE_III_STREAM="$(printf 'HS-A5\tpersist_nonce\tnonce-repair-iii\nHS-A7\tpost\t%s\nHS-A8\tpersist_id\t%s\nHS-A11\tnoop\n' \
  "${HARDSTOP_GOLDEN_PARENT}" "${HARDSTOP_GOLDEN_PARENT}")"

RESUME_FROM_DRAFT_STREAM="$(printf 'HS-R3\tnoop\nHS-R4\tpersist_nonce\tnonce-resume-fresh\nHS-R6\tpost\t%s\nHS-R7\tpersist_id\t%s\nHS-R8\tnoop\nHS-R10\tnoop\nHS-R11\tnoop\nHS-R12\tnoop\n' \
  "${HARDSTOP_GOLDEN_CHILD1}" "${HARDSTOP_GOLDEN_CHILD1}")"

# ---------------------------------------------------------------------------
# truncate_stream_at_boundary_raw <stream> <boundary-id>
#
# Pure, no fail(). Returns the lines of <stream> strictly BEFORE the line
# whose boundary-label column equals <boundary-id> (the boundary's own line
# is excluded -- it is the op that FAILS at this hard stop, so its ledger
# effect belongs to the recovery continuation, never the pre-crash prefix).
# Fail-closed (returns 1, no output) when <boundary-id> does not appear in
# <stream> at all -- a caller/stream mismatch must never silently degrade
# into "empty prefix".
# ---------------------------------------------------------------------------
truncate_stream_at_boundary_raw() {
  local stream="$1" boundary="$2"
  [[ -n "${stream}" && -n "${boundary}" ]] || return 1
  awk -F'\t' -v b="${boundary}" '$1==b{f=1} END{exit(f?0:1)}' <<<"${stream}" || return 1
  awk -F'\t' -v b="${boundary}" '$1==b{exit} {print}' <<<"${stream}"
}

# ---------------------------------------------------------------------------
# _hardstop_stream_from_boundary_raw <stream> <boundary-id>
#
# Pure, no fail(). The boundary line itself plus every line after it -- the
# doc-prescribed recovery continuation for every row except HS-U0 (see
# replay_hardstop_case below). Fail-closed (returns 1, no output) if the
# boundary is absent or the resulting slice is empty.
# ---------------------------------------------------------------------------
_hardstop_stream_from_boundary_raw() {
  local stream="$1" boundary="$2"
  [[ -n "${stream}" && -n "${boundary}" ]] || return 1
  local out
  out="$(awk -F'\t' -v b="${boundary}" '$1==b{f=1} f{print}' <<<"${stream}")"
  [[ -n "${out}" ]] || return 1
  printf '%s\n' "${out}"
}

# ---------------------------------------------------------------------------
# _hardstop_apply_ops <ledger-workdir> <ops-tsv>
#
# Applies each op line (see the stream-format doc comment above) to the
# ledger at <ledger-workdir>. Fail-closed: an unrecognized op, or any
# underlying ledger-primitive failure, aborts and returns 1 immediately.
# Empty <ops-tsv> is a legitimate no-op (an empty prefix/suffix), not a
# failure.
# ---------------------------------------------------------------------------
_hardstop_apply_ops() {
  local wd="$1" ops="$2"
  [[ -n "${wd}" ]] || return 1
  [[ -n "${ops}" ]] || return 0
  local id op a1 a2 cur
  while IFS=$'\t' read -r id op a1 a2; do
    [[ -n "${id}" ]] || continue
    case "${op}" in
      noop)
        : ;;
      persist_nonce)
        ledger_set_draft_raw "${wd}" "${a1}" "" || return 1
        ;;
      escalate)
        ledger_set_draft_raw "${wd}" "${a1}" "" || return 1
        ledger_post_comment_raw "${wd}" "${a2}" "${a1}" || return 1
        ;;
      post)
        cur="$(ledger_draft_field_raw "${wd}" nonce)" || return 1
        ledger_post_comment_raw "${wd}" "${a1}" "${cur}" || return 1
        ;;
      persist_id|scan_persist_id)
        cur="$(ledger_draft_field_raw "${wd}" nonce)" || return 1
        ledger_set_draft_raw "${wd}" "${cur}" "${a1}" || return 1
        ;;
      *)
        printf '_hardstop_apply_ops: unrecognized op [%s] on boundary [%s]\n' "${op}" "${id}" >&2
        return 1
        ;;
    esac
  done <<<"${ops}"
}

# ---------------------------------------------------------------------------
# The anchor ledger (Decision 3): models the draft's escalationNonce /
# escalationCommentId plus the set of posted comments (each with its own
# nonce + id), on disk under a caller-provided workdir:
#   <workdir>/draft.tsv     -- exactly two lines: "nonce\t<value>" and
#                               "id\t<value>" (either may be empty).
#   <workdir>/comments.tsv  -- append-only "<id>\t<nonce>" per posted
#                               comment.
# ---------------------------------------------------------------------------

# ledger_init_raw <workdir> -- pure, no fail(). Creates a fresh empty ledger.
ledger_init_raw() {
  local wd="$1"
  [[ -n "${wd}" ]] || return 1
  mkdir -p "${wd}" || return 1
  printf 'nonce\t\nid\t\n' > "${wd}/draft.tsv" || return 1
  : > "${wd}/comments.tsv" || return 1
}

# ledger_set_draft_raw <workdir> <nonce> <id> -- pure, no fail(). Overwrites
# the draft's current (nonce, id) pair.
ledger_set_draft_raw() {
  local wd="$1" nonce="$2" id="$3"
  [[ -n "${wd}" ]] || return 1
  printf 'nonce\t%s\nid\t%s\n' "${nonce}" "${id}" > "${wd}/draft.tsv"
}

# ledger_draft_field_raw <workdir> <nonce|id> -- pure, no fail(). Fail-closed
# (returns 1, no output) on an unreadable ledger or a missing field.
ledger_draft_field_raw() {
  local wd="$1" field="$2"
  [[ -n "${wd}" && -n "${field}" && -r "${wd}/draft.tsv" ]] || return 1
  awk -F'\t' -v f="${field}" '$1==f{print $2; found=1} END{exit(found?0:1)}' "${wd}/draft.tsv"
}

# ledger_post_comment_raw <workdir> <comment-id> <nonce> -- pure, no fail().
# Appends a new comment record (append-only; never deduplicated -- a
# duplicate append is exactly what the HS-R4 inversion non-vacuity proof
# constructs on purpose).
ledger_post_comment_raw() {
  local wd="$1" id="$2" nonce="$3"
  [[ -n "${wd}" && -n "${id}" && -f "${wd}/comments.tsv" ]] || return 1
  printf '%s\t%s\n' "${id}" "${nonce}" >> "${wd}/comments.tsv"
}

# ---------------------------------------------------------------------------
# count_active_anchors_raw <workdir>
#
# Pure `verify_*`-style oracle, no fail(). Active anchors = comments whose
# id == draft.escalationCommentId AND nonce == draft.escalationNonce
# (Decision 3, exact wording). On success, prints the integer count (0, 1,
# 2, ...) and returns 0. On failure (unreadable ledger, or either draft
# field unreadable), prints a distinguishable one-line reason (never a bare
# number, so a caller cannot mistake a failure message for a real count) and
# returns 1. An empty draft.escalationCommentId can never match any real
# posted comment's id, so it short-circuits to 0 without a table scan.
# ---------------------------------------------------------------------------
count_active_anchors_raw() {
  local wd="$1" dn di
  [[ -n "${wd}" && -r "${wd}/draft.tsv" && -r "${wd}/comments.tsv" ]] || {
    printf 'count_active_anchors_raw: unreadable ledger at [%s]\n' "${wd}"
    return 1
  }
  dn="$(ledger_draft_field_raw "${wd}" nonce)" || {
    printf 'count_active_anchors_raw: failed to read draft nonce at [%s]\n' "${wd}"
    return 1
  }
  di="$(ledger_draft_field_raw "${wd}" id)" || {
    printf 'count_active_anchors_raw: failed to read draft id at [%s]\n' "${wd}"
    return 1
  }
  if [[ -z "${di}" ]]; then
    printf '0'
    return 0
  fi
  awk -F'\t' -v n="${dn}" -v i="${di}" '$1==i && $2==n{c++} END{print c+0}' "${wd}/comments.tsv"
}

# ---------------------------------------------------------------------------
# replay_hardstop_case <ledger-workdir> <stream> <boundary-id> <seed-nonce> <seed-id> <seed-comments-tsv-or-"">
#
# `drive_*`-style (mirrors #912's drive_stage_* -- executes a real replay
# against the ledger and reports success/failure; callers assert the
# resulting anchor count themselves via count_active_anchors_raw, exactly
# as adversarial-chain.test.sh's drive_stage_* callers assert via
# merge_stage_stream_raw/the oracle wrappers). Builds a FRESH ledger at
# <ledger-workdir> (caller must pass a not-yet-used directory -- one per
# case, matching the plan's Risks "one mktemp -d work dir per suite" via
# per-case subdirectories under it), seeds it with the section's entry-
# condition state (<seed-nonce>/<seed-id>/<seed-comments-tsv>, the latter
# "<id>\t<nonce>" lines or "" for none), truncates <stream> at <boundary-id>
# (prefix exclusive of the boundary line), applies the prefix, then applies
# the doc-prescribed recovery continuation (the boundary line onward).
#
# HS-U0 is the sole documented non-restoring exception (## Hard-Stop
# Inventory's own note): no draft or comment exists yet at that boundary, so
# there is nothing for ## Restore Awaiting-Input State to restore and no
# recovery continuation ever runs for it -- the replay stops exactly at the
# truncation point (an empty prefix, since HS-U0 is always the first line of
# the Unattended-Escalation stream).
#
# Returns 0 on a structurally valid replay (regardless of the resulting
# anchor count), 1 on a setup/apply failure (bad stream, unknown boundary,
# or a ledger-primitive failure).
# ---------------------------------------------------------------------------
replay_hardstop_case() {
  local wd="$1" stream="$2" boundary="$3" seed_nonce="$4" seed_id="$5" seed_comments="$6"
  [[ -n "${wd}" && -n "${stream}" && -n "${boundary}" ]] || return 1

  ledger_init_raw "${wd}" || return 1
  ledger_set_draft_raw "${wd}" "${seed_nonce}" "${seed_id}" || return 1
  if [[ -n "${seed_comments}" ]]; then
    local cid cnonce
    while IFS=$'\t' read -r cid cnonce; do
      [[ -n "${cid}" ]] || continue
      ledger_post_comment_raw "${wd}" "${cid}" "${cnonce}" || return 1
    done <<<"${seed_comments}"
  fi

  local prefix
  prefix="$(truncate_stream_at_boundary_raw "${stream}" "${boundary}")" || return 1
  _hardstop_apply_ops "${wd}" "${prefix}" || return 1

  if [[ "${boundary}" == "HS-U0" ]]; then
    return 0
  fi

  local suffix
  suffix="$(_hardstop_stream_from_boundary_raw "${stream}" "${boundary}")" || return 1
  _hardstop_apply_ops "${wd}" "${suffix}" || return 1
}
