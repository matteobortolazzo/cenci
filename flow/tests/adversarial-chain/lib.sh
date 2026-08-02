#!/usr/bin/env bash
# adversarial-chain/lib.sh -- the unified cross-scenario driver backing
# flow/tests/adversarial-chain.test.sh (ticket #912, 1/5 of #887).
#
# Sourced by the suite, never executed standalone. Follows the same idiom as
# flow/tests/refine-write-order/lib.sh and flow/tests/parent-close-reconcile/
# lib.sh (#878/#879, siblings): pure `<name>_raw` extractors (safe inside
# $(...), never call fail()) paired with `require_<name>` nameref wrappers
# that DO call fail(), but are invoked directly (never through $(...)) so the
# failure lands in the sourcing suite's own `failures` counter -- see
# flow/docs/shell-scripting-gotchas.md's "never call fail() from within
# $(...)" rule and flow/tests/read-helper-purity-contract.test.sh, whose
# LIB_PATHSPEC='flow/tests/*.sh' scans this file too.
#
# HARD CONSTRAINT (never violate): flow/tests/refine-write-order/lib.sh and
# flow/tests/parent-close-reconcile/lib.sh both define `_trim`,
# `_substitute_placeholders`, and `replay_through_stub` -- with DIFFERENT
# `replay_through_stub` signatures ((commands, logfile, view_fixture,
# fail_pattern) vs (commands, log_prefix)) and different fixture roots.
# Sourcing both into one shell silently lets the LAST definition win, binding
# the whole driver to the wrong stubs/fixtures with no visible failure. This
# file therefore NEVER sources either sub-library at its own top level --
# every `drive_stage_*` function below sources exactly one sub-library,
# inside its own `( ... )` subshell, and nothing defined in that subshell
# escapes back into this file's top-level shell (subshell isolation is a
# fork: functions/variables defined *before* entering it are visible inside,
# but nothing defined *inside* leaks back out).
#
# Recording-stub shapes (both stubs log "$*" -- argv WITHOUT the binary
# token): refine-write-order/lib.sh copies fixtures/gh.stub.sh to both bin/gh
# and bin/git under one shared GH_STUB_LOG; parent-close-reconcile/lib.sh
# copies fixtures/gh.stub.sh and fixtures/git.stub.sh to their own binaries
# under separate GH_STUB_LOG/GIT_STUB_LOG files. merge_stage_stream_raw below
# reattaches the binary token itself (the caller's own command list already
# carries it) and re-derives, per log file, exactly which of the stage's
# input lines that log is expected to hold.
set -uo pipefail

_ADVERSARIAL_CHAIN_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || {
  echo "adversarial-chain/lib.sh: failed to resolve its own directory." >&2
  return 1 2>/dev/null || exit 1
}
ADVERSARIAL_CHAIN_FIXTURES="${_ADVERSARIAL_CHAIN_LIB_DIR}/fixtures"
GOLDEN_GRAPH_FIXTURE="${ADVERSARIAL_CHAIN_FIXTURES}/post-refinement-graph.json"

_ADVERSARIAL_CHAIN_TESTS_DIR="$(cd "${_ADVERSARIAL_CHAIN_LIB_DIR}/.." && pwd)" || {
  echo "adversarial-chain/lib.sh: failed to resolve flow/tests directory." >&2
  return 1 2>/dev/null || exit 1
}
REFINE_WRITE_ORDER_LIB="${_ADVERSARIAL_CHAIN_TESTS_DIR}/refine-write-order/lib.sh"
PARENT_CLOSE_RECONCILE_LIB="${_ADVERSARIAL_CHAIN_TESTS_DIR}/parent-close-reconcile/lib.sh"

# ---------------------------------------------------------------------------
# extract_golden_field_raw <jq-filter>
#
# Pure extractor, no fail(). Fail-closed: returns 1 (no output) when the
# filter is empty, the golden fixture is unreadable, or jq itself fails
# (malformed JSON, bad filter) -- never a vacuous empty-string pass. Never
# named `read_*` (read-helper-purity-contract.test.sh's naming half).
# ---------------------------------------------------------------------------
extract_golden_field_raw() {
  local filter="$1"
  [[ -n "${filter}" ]] || return 1
  [[ -r "${GOLDEN_GRAPH_FIXTURE}" ]] || return 1
  jq -r "${filter}" "${GOLDEN_GRAPH_FIXTURE}"
}

# require_golden_field <result-var> <jq-filter> <label>
#
# Nameref wrapper. Must NOT be invoked via $(...) -- its fail() call needs
# the sourcing suite's own shell, not a subshell's throwaway copy.
require_golden_field() {
  local -n _result="$1"
  local filter="$2" label="$3" out
  if out="$(extract_golden_field_raw "${filter}")"; then
    _result="${out}"
    return 0
  fi
  fail "${label}: failed to extract golden fixture field via jq filter [${filter}] from ${GOLDEN_GRAPH_FIXTURE}"
  _result=""
  return 1
}

# ---------------------------------------------------------------------------
# adversarial_chain_repo_slug -- this driver's own <owner>/<repo> resolution
# (o/r, the golden fixture's own repo -- Q3), independent of either
# sub-library's own hardcoded acme/widgets substitution.
#
# _adversarial_chain_substitute_repo <doc-style-command-template> -- pure.
# Rewrites a literal "<owner>/<repo>" placeholder to adversarial_chain_repo_slug
# BEFORE the line is ever handed to either sub-library's replay_through_stub
# -- by the time it reaches there, no "<owner>/<repo>" token remains for that
# library's own _substitute_placeholders to (mis)rewrite to acme/widgets.
# ---------------------------------------------------------------------------
adversarial_chain_repo_slug() {
  printf 'o/r'
}

_adversarial_chain_substitute_repo() {
  local line="$1"
  printf '%s' "${line//<owner>\/<repo>/$(adversarial_chain_repo_slug)}"
}

# ---------------------------------------------------------------------------
# project_refine_parent_view_json <dest-dir>
#
# Fixture projection helper. Writes <dest-dir>/parent-view.json, the
# GH_STUB_VIEW_FIXTURE shape refine-write-order/lib.sh's replay_through_stub
# expects for a `gh issue view` call: the golden parent's assignees (so the
# post-confirm ownership recheck sees no conflict against the current user)
# and its PRE-refine label set -- `Refined`/`ui:visual-check` excluded,
# since those are ADDED during this very replay; a pre-refine snapshot that
# already carried them would prove nothing (the drift check would see no
# change). Prints the written path on success; returns 1 (no output) if the
# golden fixture is unreadable or jq fails.
# ---------------------------------------------------------------------------
project_refine_parent_view_json() {
  local dest_dir="$1" out
  [[ -n "${dest_dir}" ]] || return 1
  out="${dest_dir}/parent-view.json"
  [[ -r "${GOLDEN_GRAPH_FIXTURE}" ]] || return 1
  jq '{assignees: .parent.assignees, labels: ((.parent.labels - ["Refined","ui:visual-check"]) | map({name: .}))}' \
    "${GOLDEN_GRAPH_FIXTURE}" > "${out}" || return 1
  printf '%s' "${out}"
}

# ---------------------------------------------------------------------------
# project_parent_subissues_view_json <dest-dir> <mode> <child-id>
#
# Fixture projection helper. Writes <dest-dir>/subissues-<mode>.json, the
# GH_STUB_ISSUE_VIEW_FIXTURE shape parent-close-reconcile/lib.sh's
# replay_through_stub expects for the `gh issue view <parentId> --json
# subIssues` sub-issue recheck (mirrors fixtures/parent-subissues-open-
# sibling.json / parent-subissues-all-closed.json, this time derived from
# the shared golden fixture rather than a parent-close-reconcile-local
# fixture -- the AC1 cross-layer contract point).
#
#   open-sibling -- every golden parent.subIssues entry as-is: the golden
#     fixture's own post-refinement state, where every child (including
#     every sibling of <child-id>) is still open -- forces `hold`.
#   all-closed   -- every sibling OTHER than <child-id> is closed, modeling
#     the state once every other child has merged -- allows `close`.
#
# Prints the written path on success; returns 1 (no output) on an unreadable
# fixture, a bad mode, or a jq failure.
# ---------------------------------------------------------------------------
project_parent_subissues_view_json() {
  local dest_dir="$1" mode="$2" child_id="$3" out
  [[ -n "${dest_dir}" && -n "${mode}" && -n "${child_id}" ]] || return 1
  out="${dest_dir}/subissues-${mode}.json"
  [[ -r "${GOLDEN_GRAPH_FIXTURE}" ]] || return 1
  case "${mode}" in
    open-sibling)
      jq '{subIssues: .parent.subIssues}' "${GOLDEN_GRAPH_FIXTURE}" > "${out}" || return 1
      ;;
    all-closed)
      jq --argjson child "${child_id}" \
        '{subIssues: (.parent.subIssues | map(if .number == $child then . else (.state = "CLOSED") end))}' \
        "${GOLDEN_GRAPH_FIXTURE}" > "${out}" || return 1
      ;;
    *)
      return 1
      ;;
  esac
  printf '%s' "${out}"
}

# ---------------------------------------------------------------------------
# merge_stage_stream_raw <stage> <binary-filter|""> <expected-commands> <log-file> <stream-file>
#
# Verifies logfile's recorded invocations equal EXACTLY the subsequence of
# <expected-commands> (newline-separated, each line carrying its own leading
# binary token, e.g. "gh issue edit ...") whose binary token matches
# <binary-filter> ("" accepts every line -- used for a stage whose sub-
# library shares one combined log across both binaries) -- same argv text,
# same count, same order -- then appends one normalized
# "<stage>\t<binary>\t<argv>" record per invocation to <stream-file>
# (append-only: never truncates a sibling stage's records already written
# there). This is the proof that a stage's real replay executed exactly what
# this driver handed it, not merely "some invocations happened" -- the
# per-binary-log/exact-subsequence check the plan's Files to Create entry
# names.
#
# On success: prints nothing, returns 0. On failure: prints a distinguishable
# one-line reason and returns 1 -- mirrors the sub-libraries' own verify_*
# oracle convention.
#
# Defense-in-depth: every expected line's leading token must be "gh" or
# "git" -- the only two binaries either sub-library's stub ever shims. This
# rejects (fail-closed) rather than silently replays a line naming any other
# binary, guarding against a future edit accidentally introducing an
# unshimmed binary into the replay stream.
#
# Fail-closed against a vacuous pass: an empty expected-commands list would
# otherwise trivially "match" an empty log (0 == 0) and return success having
# asserted nothing -- reject that case explicitly rather than let it read as
# a real proof of replay.
# ---------------------------------------------------------------------------
merge_stage_stream_raw() {
  local stage="$1" binary_filter="$2" expected="$3" logfile="$4" streamfile="$5"
  [[ -n "${stage}" ]] || return 1
  [[ -r "${logfile}" ]] || return 1

  local -a expected_lines=() logged_lines=()
  local line binary
  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    binary="${line%% *}"
    if [[ "${binary}" != "gh" && "${binary}" != "git" ]]; then
      printf 'merge_stage_stream_raw(%s/%s): expected-line leading token [%s] is not gh/git -- rejecting (allowlist)\n' \
        "${stage}" "${binary_filter:-any}" "${binary}"
      return 1
    fi
    if [[ -n "${binary_filter}" && "${binary}" != "${binary_filter}" ]]; then
      continue
    fi
    expected_lines+=("${line}")
  done <<<"${expected}"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -n "${line}" ]] && logged_lines+=("${line}")
  done < "${logfile}"

  [[ "${#expected_lines[@]}" -gt 0 ]] || return 1

  if [[ "${#logged_lines[@]}" -ne "${#expected_lines[@]}" ]]; then
    printf 'merge_stage_stream_raw(%s/%s): logged %d invocation(s), expected %d (subsequence of the stage input list)\n' \
      "${stage}" "${binary_filter:-any}" "${#logged_lines[@]}" "${#expected_lines[@]}"
    return 1
  fi

  : >> "${streamfile}" || return 1
  local i argv_only expected_line
  for ((i = 0; i < ${#expected_lines[@]}; i++)); do
    expected_line="${expected_lines[i]}"
    binary="${expected_line%% *}"
    argv_only="${expected_line#* }"
    if [[ "${logged_lines[i]}" != "${argv_only}" ]]; then
      printf 'merge_stage_stream_raw(%s/%s): invocation %d mismatch: logged [%s], want [%s] (from expected [%s])\n' \
        "${stage}" "${binary_filter:-any}" "$((i + 1))" "${logged_lines[i]}" "${argv_only}" "${expected_line}"
      return 1
    fi
    printf '%s\t%s\t%s\n' "${stage}" "${binary}" "${logged_lines[i]}" >> "${streamfile}" || return 1
  done
  return 0
}

# ---------------------------------------------------------------------------
# drive_stage_refine <dest-dir> <streamfile> <mode>
#
# Subshell-isolated: sources refine-write-order/lib.sh ONLY inside this
# subshell (never at this file's top level). Replays this stage's own
# canonical command list -- the manifest-revalidation + ownership-recheck
# read prefix, and (mode=confirmed only) the full claim -> working ->
# parent-body -> per-child create+link -> parent-exec-order -> refined ->
# visual-check write sequence for the golden fixture's 2-child split --
# through refine-write-order/lib.sh's replay_through_stub against a
# projected parent view, merging the resulting log into <streamfile>.
#
#   confirmed              -- the full read+write sequence (AC2's positive
#                              proof: one real recorded stream, exact
#                              argv/counts/ordering).
#   declined                -- zero commands at all (mirrors SKILL.md's
#                              declined branch: "only rm -f", no gh/git call
#                              ever made).
#   interrupted-after-revalidation -- only the manifest-revalidation read
#                              (a hard stop between the fetch and the
#                              ownership recheck): still zero writes.
#
# Returns 0 on success (including the declined/interrupted zero-command
# cases, which trivially "replay" nothing), 1 on any replay or merge
# failure.
# ---------------------------------------------------------------------------
drive_stage_refine() {
  local dest_dir="$1" streamfile="$2" mode="$3"
  (
    set -uo pipefail
    # shellcheck source=../refine-write-order/lib.sh
    source "${REFINE_WRITE_ORDER_LIB}" || exit 1

    local view_json
    view_json="$(project_refine_parent_view_json "${dest_dir}")" || exit 1

    local -a cmds=()
    case "${mode}" in
      declined)
        cmds=()
        ;;
      interrupted-after-revalidation)
        cmds=(
          "$(_adversarial_chain_substitute_repo 'gh issue view 100 --repo <owner>/<repo> --json milestone,labels')"
        )
        ;;
      confirmed)
        cmds=(
          "$(_adversarial_chain_substitute_repo 'gh issue view 100 --repo <owner>/<repo> --json milestone,labels')"
          "$(_adversarial_chain_substitute_repo 'gh issue view 100 --repo <owner>/<repo> --json assignees')"
          "$(_adversarial_chain_substitute_repo 'gh issue edit 100 --repo <owner>/<repo> --add-assignee octocat')"
          "$(_adversarial_chain_substitute_repo 'gh issue edit 100 --repo <owner>/<repo> --add-label Working')"
          "$(_adversarial_chain_substitute_repo 'gh issue edit 100 --repo <owner>/<repo> --body parent-body-updated')"
          "$(_adversarial_chain_substitute_repo 'gh issue create --repo <owner>/<repo> --title child-101 --jq .number')"
          "$(_adversarial_chain_substitute_repo 'gh api repos/<owner>/<repo>/issues/100/sub_issues -f sub_issue_id=101')"
          "$(_adversarial_chain_substitute_repo 'gh issue create --repo <owner>/<repo> --title child-102 --jq .number')"
          "$(_adversarial_chain_substitute_repo 'gh api repos/<owner>/<repo>/issues/100/sub_issues -f sub_issue_id=102')"
          "$(_adversarial_chain_substitute_repo 'gh issue edit 100 --repo <owner>/<repo> --body parent-body-with-exec-order')"
          "$(_adversarial_chain_substitute_repo 'gh issue edit 100 --repo <owner>/<repo> --add-label Refined --remove-label Working')"
          "$(_adversarial_chain_substitute_repo 'gh issue edit 100 --repo <owner>/<repo> --add-label ui:visual-check')"
        )
        ;;
      *)
        exit 1
        ;;
    esac

    local logfile="${dest_dir}/refine.log"
    if [[ "${#cmds[@]}" -eq 0 ]]; then
      : > "${logfile}" || exit 1
      : >> "${streamfile}" || exit 1
      exit 0
    fi

    local joined rc
    joined="$(printf '%s\n' "${cmds[@]}")"
    replay_through_stub "${joined}" "${logfile}" "${view_json}" ""
    rc=$?
    if [[ "${rc}" -ne 0 ]]; then
      cat "${logfile}.stderr" >&2 2>/dev/null
      exit 1
    fi

    merge_stage_stream_raw "refine" "" "${joined}" "${logfile}" "${streamfile}"
  )
}

# ---------------------------------------------------------------------------
# drive_stage_escalation <dest-dir> <streamfile> <mode>
#
# Subshell-isolated for symmetry with the other two stage drivers, though it
# sources refine-write-order/lib.sh purely for replay_through_stub/fixtures
# reuse (no fixture-specific gh.stub.sh of its own is needed for this
# minimal segment). The escalation/resume segment is deliberately the
# *middle* of the ordered stream only, per the plan's Assumptions -- enough
# to prove AC3's "every pre-confirmation interruption" spans this stage too;
# the HS-* truncate-and-re-enter matrix is #913's, out of scope here.
#
#   resumed     -- post the escalation question, read the answer back, then
#                  the resume claim (Working re-added / Input Needed
#                  removed): 2 writes, 1 read.
#   interrupted -- zero commands: a hard stop before ever posting the
#                  escalation anchor.
# ---------------------------------------------------------------------------
drive_stage_escalation() {
  local dest_dir="$1" streamfile="$2" mode="$3"
  (
    set -uo pipefail
    # shellcheck source=../refine-write-order/lib.sh
    source "${REFINE_WRITE_ORDER_LIB}" || exit 1

    local -a cmds=()
    case "${mode}" in
      interrupted)
        cmds=()
        ;;
      resumed)
        cmds=(
          "$(_adversarial_chain_substitute_repo 'gh issue comment 101 --repo <owner>/<repo> --body escalation-question')"
          "$(_adversarial_chain_substitute_repo 'gh issue view 101 --repo <owner>/<repo> --json comments')"
          "$(_adversarial_chain_substitute_repo 'gh issue edit 101 --repo <owner>/<repo> --add-label Working --remove-label InputNeeded')"
        )
        ;;
      *)
        exit 1
        ;;
    esac

    local logfile="${dest_dir}/escalation.log"
    if [[ "${#cmds[@]}" -eq 0 ]]; then
      : > "${logfile}" || exit 1
      : >> "${streamfile}" || exit 1
      exit 0
    fi

    local joined rc
    joined="$(printf '%s\n' "${cmds[@]}")"
    replay_through_stub "${joined}" "${logfile}" "" ""
    rc=$?
    if [[ "${rc}" -ne 0 ]]; then
      cat "${logfile}.stderr" >&2 2>/dev/null
      exit 1
    fi

    merge_stage_stream_raw "escalation" "" "${joined}" "${logfile}" "${streamfile}"
  )
}

# ---------------------------------------------------------------------------
# drive_stage_parent_close <dest-dir> <streamfile> <sibling-mode>
#
# Subshell-isolated: sources parent-close-reconcile/lib.sh ONLY inside this
# subshell. Drives the last child's (childId 102, the fixture's own
# isLastChild:true slot) parent-audit sub-issue recheck against the golden
# fixture's projected subIssues view: `sibling-mode` open-sibling keeps
# every golden subIssues entry open (forces hold); all-closed closes every
# sibling other than 102 (allows close) -- AC4's "an open sibling projected
# from the fixture forces hold" claim, proven against THIS ticket's own
# fixture rather than parent-close-reconcile's local one (AC1's cross-layer
# reuse point).
# ---------------------------------------------------------------------------
drive_stage_parent_close() {
  local dest_dir="$1" streamfile="$2" sibling_mode="$3"
  (
    set -uo pipefail
    # shellcheck source=../parent-close-reconcile/lib.sh
    source "${PARENT_CLOSE_RECONCILE_LIB}" || exit 1

    local view_json
    view_json="$(project_parent_subissues_view_json "${dest_dir}" "${sibling_mode}" 102)" || exit 1

    local -a cmds=(
      "$(_adversarial_chain_substitute_repo 'gh issue view 100 --repo <owner>/<repo> --json subIssues')"
    )
    local joined rc
    joined="$(printf '%s\n' "${cmds[@]}")"
    local log_prefix="${dest_dir}/parent-close-${sibling_mode}"
    GH_STUB_ISSUE_VIEW_FIXTURE="${view_json}" replay_through_stub "${joined}" "${log_prefix}"
    rc=$?
    if [[ "${rc}" -ne 0 ]]; then
      cat "${log_prefix}.stderr" >&2 2>/dev/null
      exit 1
    fi

    # No git invocation in this minimal sub-issue-recheck segment (AC4's
    # close<->hold retry ordering itself is proven independently via
    # verify_parent_close_reconciliation_via_lib below, never re-derived
    # here) -- the git.log this stage's replay produces is expected empty,
    # left on disk at <log_prefix>.git.log for a caller that wants to assert
    # that directly, never merged as a zero-length no-op subsequence.
    merge_stage_stream_raw "parent-close" "gh" "${joined}" "${log_prefix}.gh.log" "${streamfile}"
  )
}

# ---------------------------------------------------------------------------
# Pure subshell oracle wrappers -- each sources exactly ONE sub-library
# inside its own subshell (never at this file's top level) and relays that
# library's real function verbatim: stdout reason and exit code untouched.
# Safe inside $(...): none of these call fail() themselves.
# ---------------------------------------------------------------------------

# verify_refine_write_order_via_lib <op-sequence>
verify_refine_write_order_via_lib() {
  local seq="$1"
  (
    source "${REFINE_WRITE_ORDER_LIB}" >/dev/null 2>&1 || exit 1
    verify_refine_write_order "${seq}"
  )
}

# verify_parent_close_reconciliation_via_lib <scenario> <op-sequence>
verify_parent_close_reconciliation_via_lib() {
  local scenario="$1" seq="$2"
  (
    source "${PARENT_CLOSE_RECONCILE_LIB}" >/dev/null 2>&1 || exit 1
    verify_parent_close_reconciliation "${scenario}" "${seq}"
  )
}

# classify_gh_call_via_lib <command-line> -- parent-close-reconcile/lib.sh
# defines no classifier of its own, so this always relays
# refine-write-order/lib.sh's classify_gh_call.
classify_gh_call_via_lib() {
  local cmd="$1"
  (
    source "${REFINE_WRITE_ORDER_LIB}" >/dev/null 2>&1 || { echo unknown; exit 1; }
    classify_gh_call "${cmd}"
  )
}
