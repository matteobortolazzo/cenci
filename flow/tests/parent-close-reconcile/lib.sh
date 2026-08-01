#!/usr/bin/env bash
# lib.sh -- extractors + oracles backing
# flow/tests/parent-close-reconcile.test.sh (ticket #879, "bind
# parent-close verdict to commit and PR trailers across retries (4/12)").
#
# Sourced by the suite, never executed standalone. Follows the same idiom as
# flow/tests/refine-write-order/lib.sh (#878, sibling ticket) and
# flow/tests/parity/contract-lib.sh: pure `<name>_raw` extractors (safe
# inside `$(...)`, never call `fail()`) paired with `require_<name>` nameref
# wrappers that DO call `fail()`, but are invoked directly (never through
# `$(...)`) so the failure lands in the sourcing suite's own `failures`
# counter -- see docs/shell-scripting-gotchas.md's "never call fail() from
# within $(...)" rule and flow/tests/read-helper-purity-contract.test.sh,
# which scans every *.sh library under flow/tests/ (this file included) for
# the violation. No `read_*`-named helpers exist in this file, so it is
# trivially compliant with that contract's naming half too.
#
# Three families of helper live here, per the plan's Files to Create entry:
#   1. Pure extractors (`extract_fenced_line_raw`, `classify_commit_range_trailer`)
#      plus their `require_*` nameref wrappers.
#   2. The AC-derived oracle `verify_parent_close_reconciliation` -- a
#      deterministic partial-order/invariant checker over an abstract
#      op-token stream, no backing script, mirroring
#      flow/tests/refine-write-order/lib.sh's verify_refine_write_order.
#   3. A `drive_*` stub-replay harness (`replay_through_stub`) that actually
#      EXECUTES a real command sequence against fixtures/gh.stub.sh and
#      fixtures/git.stub.sh -- copied into a fresh `mktemp -d` and chmod
#      +x'd here at runtime (permission-adding only; both committed fixtures
#      carry no executable bit) -- and records what really happened.
set -uo pipefail

_PARENT_CLOSE_RECONCILE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || {
  echo "parent-close-reconcile/lib.sh: failed to resolve its own directory." >&2
  return 1 2>/dev/null || exit 1
}
PARENT_CLOSE_RECONCILE_FIXTURES="${_PARENT_CLOSE_RECONCILE_LIB_DIR}/fixtures"

# ---------------------------------------------------------------------------
# _trim <string> -- leading/trailing whitespace stripped. Pure, no fail().
# ---------------------------------------------------------------------------
_trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "${s}"
}

# ---------------------------------------------------------------------------
# extract_fenced_line_raw <doc-path> <exact-substring>
#
# Pure extractor, no fail(). Fence-aware (docs/shell-scripting-gotchas.md's
# "account for code fences" rule): scans only lines inside a fenced
# ```...``` code block, and prints the first trimmed line that contains
# <exact-substring> as a literal (grep -F) match. Returns 1 (no output) when
# the file is unreadable OR when no fenced line contains the substring --
# both are "not found" from a caller's point of view, and #879's green phase
# has not landed yet, so every literal command line quoted in the plan's
# Files to Modify section is expected to be absent from today's
# phase-9-pr.md (a genuine, non-vacuous red).
# ---------------------------------------------------------------------------
extract_fenced_line_raw() {
  local file="$1" needle="$2" content infence=0 line
  content="$(cat "${file}" 2>/dev/null)" || return 1
  [[ -n "${needle}" ]] || return 1
  while IFS= read -r line || [[ -n "${line}" ]]; do
    if [[ "${line}" =~ ^\`\`\` ]]; then
      if [[ "${infence}" -eq 1 ]]; then infence=0; else infence=1; fi
      continue
    fi
    if [[ "${infence}" -eq 1 ]]; then
      local trimmed
      trimmed="$(_trim "${line}")"
      if [[ "${trimmed}" == *"${needle}"* ]]; then
        printf '%s\n' "${trimmed}"
        return 0
      fi
    fi
  done <<<"${content}"
  return 1
}

# require_extract_fenced_line <result-var> <doc-path> <exact-substring> <label>
#
# Nameref wrapper. Must NOT be invoked via `$(...)` -- its fail() call needs
# the sourcing suite's own shell, not a subshell's throwaway copy.
require_extract_fenced_line() {
  local -n _result="$1"
  local file="$2" needle="$3" label="$4" out
  if out="$(extract_fenced_line_raw "${file}" "${needle}")"; then
    _result="${out}"
    return 0
  fi
  fail "${label}: fenced command line not found in ${file}: ${needle}"
  _result=""
  return 1
}

# ---------------------------------------------------------------------------
# classify_commit_range_trailer <records> <parentId> <verdict>
#
# Pure decision logic, no fail(). Classifies a serialized commit range
# (this test suite's own record format -- see below, never real git output
# fed through directly, since real `%x00`-delimited git output cannot
# round-trip through a bash variable: NUL bytes are silently dropped by
# command substitution) against the AC's Commit-reconciliation decision
# table (plan's `## Commit` bullet):
#
#   - "absent"            -- no commit in range carries a whole-line parent
#                             reference (`Fixes #<parentId>` or
#                             `Related to #<parentId>`) at all.
#   - "agree"              -- exactly one reference, on HEAD (the first
#                             record), already matching the verdict's
#                             required line -- the idempotent no-op case.
#   - "disagree-head"      -- exactly one reference, on HEAD, but the WRONG
#                             line for the current verdict -- the amend case.
#   - "nonhead:<sha>"       -- exactly one reference, but on a non-HEAD
#                             commit -- amend cannot reach it, fail closed.
#   - "multiple:<sha1>,<sha2>,..." -- a reference on more than one commit --
#                             fail closed, report every offending SHA.
#
# <records> format (this suite's own serialization, not git's literal
# %x00-delimited output): repeating blocks, newest-first (matching `git log`
# default order, so record 0 == HEAD):
#
#   SHA:<full-sha>
#   BODY-BEGIN
#   <commit message, one or more lines, whole-line trailers included>
#   BODY-END
#
# Whole-line precision only (docs/shell-scripting-gotchas.md /
# phase-9-pr.md:287's existing `grep -qxF` idiom) -- `#7` must never match a
# body line that only contains `#70`.
# ---------------------------------------------------------------------------
classify_commit_range_trailer() {
  local records="$1" parent_id="$2" verdict="$3"
  local required_line other_line
  case "${verdict}" in
    close) required_line="Fixes #${parent_id}"; other_line="Related to #${parent_id}" ;;
    hold) required_line="Related to #${parent_id}"; other_line="Fixes #${parent_id}" ;;
    *) printf 'invalid verdict: %s\n' "${verdict}"; return 1 ;;
  esac

  local -a shas=() hits=()
  local cur_sha="" in_body=0 line rec_idx=-1

  while IFS= read -r line || [[ -n "${line}" ]]; do
    case "${line}" in
      SHA:*)
        cur_sha="${line#SHA:}"
        rec_idx=$((rec_idx + 1))
        shas+=("${cur_sha}")
        ;;
      BODY-BEGIN)
        in_body=1
        ;;
      BODY-END)
        in_body=0
        ;;
      *)
        if [[ "${in_body}" -eq 1 ]]; then
          local trimmed
          trimmed="$(_trim "${line}")"
          if [[ "${trimmed}" == "${required_line}" || "${trimmed}" == "${other_line}" ]]; then
            hits+=("${rec_idx}")
          fi
        fi
        ;;
    esac
  done <<<"${records}"

  local n_hits=${#hits[@]}
  if [[ "${n_hits}" -eq 0 ]]; then
    echo "absent"
    return 0
  fi

  if [[ "${n_hits}" -gt 1 ]]; then
    local -a offending=()
    local h
    for h in "${hits[@]}"; do offending+=("${shas[h]}"); done
    printf 'multiple:%s\n' "$(IFS=,; echo "${offending[*]}")"
    return 0
  fi

  local hit_idx="${hits[0]}"
  if [[ "${hit_idx}" -ne 0 ]]; then
    printf 'nonhead:%s\n' "${shas[hit_idx]}"
    return 0
  fi

  # hit is on record 0 (HEAD) -- re-scan HEAD's body to determine agree/disagree.
  local -a head_body=()
  local capturing=0
  while IFS= read -r line || [[ -n "${line}" ]]; do
    case "${line}" in
      SHA:*)
        [[ "${capturing}" -eq 1 ]] && break
        ;;
      BODY-BEGIN)
        capturing=1
        ;;
      BODY-END)
        [[ "${capturing}" -eq 1 ]] && break
        ;;
      *)
        [[ "${capturing}" -eq 1 ]] && head_body+=("$(_trim "${line}")")
        ;;
    esac
  done <<<"${records}"

  local bl
  for bl in "${head_body[@]:-}"; do
    if [[ "${bl}" == "${required_line}" ]]; then
      echo "agree"
      return 0
    fi
  done
  echo "disagree-head"
  return 0
}

# ---------------------------------------------------------------------------
# verify_parent_close_reconciliation <scenario> <op-sequence>
#
# AC-derived partial-order + invariant oracle (no backing script). Validates
# a recorded op-token stream against the expected reconciliation shape for
# one of the ticket's named scenarios -- see the scenario matrix in
# flow/tests/parent-close-reconcile.test.sh's header comment, 1:1 with the
# plan's Test Strategy table.
#
# Recognized op tokens (abstract -- not literal command text):
#   subissue-recheck, merged-guard, no-pr-pass-through, stop-merged,
#   commit-scan, commit-noop, commit-amend, commit-verify,
#   stop-nonhead, stop-fail-closed,
#   push, push-verify, push-verify-failed,
#   body-read, body-noop, body-edit, body-verify,
#   closing-verify-exclude-ok, closing-verify-exclude-failed,
#   closing-verify-include-ok, closing-verify-include-failed,
#   notes-absent, notes-unreadable, label-cascade, label-no-cascade,
#   stale-inreview-note
#
# notes-absent vs notes-unreadable (both close-verdict degrade-honestly
# outcomes, phase-9-pr.md's "Confirmed absent" / "Unreadable after retry"
# bullets): notes-absent records a retried closing-reference read that
# SUCCEEDED and genuinely does not list the parent; notes-unreadable records
# a retried read that itself FAILED. The two must never be conflated -- the
# Notes wording differs (GitHub affirmatively did not report it, vs GitHub's
# record could not be read), so each scenario below asserts on its own
# specific token, never a shared generic one.
#
# no-pr-pass-through (Merged-PR Guard, "No PR yet is not a failure"): records
# a `gh pr view` read whose output matched "no pull requests found"
# (case-insensitive) -- the ordinary first-time-entry case, which passes
# through to the Maintenance Gate exactly as if the guard had not fired.
# Never the same as stop-merged or stop-fail-closed.
#
# push-verify-failed (Push step's post-push remote-tip verification): records
# a failed `fetch` or an unreadable `FETCH_HEAD` log -- distinct from
# push-verify (a successful read, whether or not its content agrees with the
# verdict). Both a disagreement and an unreadable read stop fail closed; this
# token isolates the "read itself failed" sub-case.
#
# On success: prints nothing, returns 0. On failure: prints a distinguishable
# one-line reason to stdout and returns 1 -- mirrors
# flow/tests/refine-write-order/lib.sh's verify_refine_write_order and
# check_synthetic_adapter's "...fail:out of order: ..." reason convention in
# flow/docs/adapter-contract.md.
# ---------------------------------------------------------------------------
verify_parent_close_reconciliation() {
  local scenario="$1" seq="$2"
  local -a tokens
  local flat="${seq//$'\n'/ }"
  read -r -a tokens <<<"${flat}"
  local n=${#tokens[@]}
  local -A pos=()
  local i t
  for ((i = 0; i < n; i++)); do
    t="${tokens[i]}"
    if [[ -n "${pos[${t}]:-}" ]]; then
      echo "duplicate op: ${t}"
      return 1
    fi
    pos["${t}"]=${i}
  done

  _pcr_order() {
    # $1=earlier-op $2=later-op -- both required present, earlier strictly
    # before later. Relies on the caller's `pos` associative array via
    # bash's dynamic scoping (this helper is only ever called from within
    # verify_parent_close_reconciliation's own call stack).
    local a="$1" b="$2"
    if [[ -z "${pos[${a}]:-}" ]]; then echo "missing required op: ${a}"; return 1; fi
    if [[ -z "${pos[${b}]:-}" ]]; then echo "missing required op: ${b}"; return 1; fi
    if [[ "${pos[${a}]}" -ge "${pos[${b}]}" ]]; then echo "${a} must precede ${b}"; return 1; fi
    return 0
  }

  local reason

  case "${scenario}" in
    merged)
      if [[ "${n}" -ne 2 || "${tokens[0]:-}" != "merged-guard" || "${tokens[1]:-}" != "stop-merged" ]]; then
        echo "a MERGED PR read must stop immediately with zero further writes (expected exactly [merged-guard stop-merged], got [${tokens[*]:-}])"
        return 1
      fi
      ;;

    no-pr-yet)
      if [[ "${n}" -ne 2 || "${tokens[0]:-}" != "merged-guard" || "${tokens[1]:-}" != "no-pr-pass-through" ]]; then
        echo "a 'no PR yet' read must pass through with exactly [merged-guard no-pr-pass-through] and proceed to the Maintenance Gate exactly as if the guard hadn't fired (got [${tokens[*]:-}])"
        return 1
      fi
      ;;

    nonhead)
      if [[ -z "${pos[stop-nonhead]:-}" ]]; then
        echo "a non-HEAD stale trailer must stop-fail-closed via stop-nonhead"
        return 1
      fi
      if [[ -n "${pos[commit-amend]:-}" ]]; then
        echo "a non-HEAD stale trailer must never be amended (--amend cannot reach a non-HEAD commit)"
        return 1
      fi
      ;;

    hold-unverifiable-removal)
      if [[ -z "${pos[stop-fail-closed]:-}" ]]; then
        echo "an unverifiable removal on a hold verdict must stop-fail-closed"
        return 1
      fi
      if [[ -n "${pos[label-cascade]:-}" || -n "${pos[label-no-cascade]:-}" ]]; then
        echo "a fail-closed stop must never reach the Labels step"
        return 1
      fi
      ;;

    close-unverifiable-addition-verified-commit)
      # Models phase-9-pr.md's "Unreadable after retry" bullet: the retried
      # closing-reference read itself failed. Must emit notes-unreadable,
      # never the distinct notes-absent (confirmed-absent) wording.
      if [[ -z "${pos[commit-verify]:-}" ]]; then
        echo "the commit-message channel must be verified before degrading honestly on an unconfirmed closing reference"
        return 1
      fi
      if [[ -n "${pos[notes-absent]:-}" ]]; then
        echo "an unreadable-after-retry closing reference must never claim GitHub affirmatively confirmed absence -- notes-absent is the confirmed-absent wording, not this scenario's"
        return 1
      fi
      if [[ -z "${pos[notes-unreadable]:-}" ]]; then
        echo "an unreadable-after-retry addition with a provably-reconciled commit trailer must proceed AND emit the ## Notes 'could not be verified' line"
        return 1
      fi
      if [[ -n "${pos[stop-fail-closed]:-}" ]]; then
        echo "a verified commit channel must not hard-stop merely because the closing reference itself is unconfirmed"
        return 1
      fi
      reason="$(_pcr_order commit-verify notes-unreadable)" || { echo "${reason}"; return 1; }
      ;;

    close-confirmed-absent-verified-commit)
      # Models phase-9-pr.md's "Confirmed absent" bullet: the retried
      # closing-reference read SUCCEEDED and genuinely does not list the
      # parent. Must emit notes-absent, never the distinct notes-unreadable
      # (unreadable-after-retry) wording.
      if [[ -z "${pos[commit-verify]:-}" ]]; then
        echo "the commit-message channel must be verified before degrading honestly on a confirmed-absent closing reference"
        return 1
      fi
      if [[ -n "${pos[notes-unreadable]:-}" ]]; then
        echo "a confirmed-absent closing reference must never claim the read itself was unreadable -- notes-unreadable is the unreadable-after-retry wording, not this scenario's"
        return 1
      fi
      if [[ -z "${pos[notes-absent]:-}" ]]; then
        echo "a confirmed-absent addition with a provably-reconciled commit trailer must proceed AND emit the ## Notes 'GitHub did not report it' line"
        return 1
      fi
      if [[ -n "${pos[stop-fail-closed]:-}" ]]; then
        echo "a verified commit channel must not hard-stop merely because the closing reference is confirmed absent"
        return 1
      fi
      reason="$(_pcr_order commit-verify notes-absent)" || { echo "${reason}"; return 1; }
      ;;

    close-unverifiable-addition-unverified-commit)
      if [[ -z "${pos[stop-fail-closed]:-}" ]]; then
        echo "an unverifiable commit-message channel must hard-stop even on a close verdict"
        return 1
      fi
      if [[ -n "${pos[notes-absent]:-}" || -n "${pos[notes-unreadable]:-}" ]]; then
        echo "must never claim either honest-degrade Notes line when the commit channel itself could not be verified"
        return 1
      fi
      ;;

    push-verify-unreadable)
      # Models phase-9-pr.md's Push step: "A failed fetch or an unreadable
      # FETCH_HEAD log => stop, fail closed, the same as a disagreement --
      # never treat an unverifiable remote read as an implicit pass." Must
      # stop before any further write (body edit, closing-ref verification,
      # Labels).
      if [[ -n "${pos[push-verify]:-}" && -n "${pos[push-verify-failed]:-}" ]]; then
        echo "push-verify and push-verify-failed are mutually exclusive outcomes of the same remote-tip verification read -- pick one"
        return 1
      fi
      if [[ -z "${pos[push-verify-failed]:-}" ]]; then
        echo "a failed fetch or an unreadable FETCH_HEAD log must record push-verify-failed"
        return 1
      fi
      if [[ -n "${pos[body-read]:-}" || -n "${pos[body-edit]:-}" || -n "${pos[body-verify]:-}" \
            || -n "${pos[closing-verify-include-ok]:-}" || -n "${pos[closing-verify-include-failed]:-}" \
            || -n "${pos[closing-verify-exclude-ok]:-}" || -n "${pos[closing-verify-exclude-failed]:-}" \
            || -n "${pos[label-cascade]:-}" || -n "${pos[label-no-cascade]:-}" \
            || -n "${pos[notes-absent]:-}" || -n "${pos[notes-unreadable]:-}" ]]; then
        echo "an unreadable push-verify must stop before any further write -- no body edit, closing-ref verification, or Labels step (no further writes)"
        return 1
      fi
      if [[ -z "${pos[stop-fail-closed]:-}" ]]; then
        echo "an unreadable remote-tip verification must stop fail closed, the same as a disagreement -- never treat an unverifiable remote read as an implicit pass"
        return 1
      fi
      reason="$(_pcr_order push push-verify-failed)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order push-verify-failed stop-fail-closed)" || { echo "${reason}"; return 1; }
      ;;

    close-to-hold)
      # Forbidden-op presence checks run BEFORE the ordering chain below --
      # a synthetic reject case that omits one of the chain's required ops
      # (e.g. label-no-cascade) while also carrying a forbidden op (e.g.
      # label-cascade) must report the forbidden-op violation, not a
      # generic "missing required op" from the chain tripping first.
      if [[ -n "${pos[label-cascade]:-}" ]]; then
        echo "close-to-hold must never cascade --parent"
        return 1
      fi
      if [[ -n "${pos[closing-verify-include-ok]:-}" || -n "${pos[closing-verify-include-failed]:-}" ]]; then
        echo "close-to-hold must verify EXCLUSION (removal), not inclusion"
        return 1
      fi
      reason="$(_pcr_order commit-scan commit-amend)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order commit-amend commit-verify)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order commit-verify push)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order push push-verify)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order push-verify body-read)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order body-read body-edit)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order body-edit body-verify)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order body-verify closing-verify-exclude-ok)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order closing-verify-exclude-ok label-no-cascade)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order label-no-cascade stale-inreview-note)" || { echo "${reason}"; return 1; }
      ;;

    hold-to-close)
      # See close-to-hold above: forbidden-op checks run before the
      # ordering chain for the same non-vacuity reason.
      if [[ -n "${pos[label-no-cascade]:-}" ]]; then
        echo "hold-to-close must cascade --parent, never omit it"
        return 1
      fi
      if [[ -n "${pos[closing-verify-exclude-ok]:-}" || -n "${pos[closing-verify-exclude-failed]:-}" ]]; then
        echo "hold-to-close must verify INCLUSION (addition), not exclusion"
        return 1
      fi
      if [[ -n "${pos[stale-inreview-note]:-}" ]]; then
        echo "hold-to-close must not emit the stale In Review note -- nothing was cascaded that needs un-cascading"
        return 1
      fi
      reason="$(_pcr_order commit-scan commit-amend)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order commit-amend commit-verify)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order commit-verify push)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order push push-verify)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order push-verify body-read)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order body-read body-edit)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order body-edit body-verify)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order body-verify closing-verify-include-ok)" || { echo "${reason}"; return 1; }
      reason="$(_pcr_order closing-verify-include-ok label-cascade)" || { echo "${reason}"; return 1; }
      ;;

    unchanged-close | unchanged-hold)
      # Forbidden-op presence checks run BEFORE the commit-noop presence
      # check for the same non-vacuity reason as close-to-hold/hold-to-close
      # above -- a synthetic reject case carrying commit-amend (and lacking
      # commit-noop entirely) must report the amend violation, not the
      # generic "must record commit-noop" absence check tripping first.
      if [[ -n "${pos[commit-amend]:-}" ]]; then
        echo "an unchanged verdict must never amend the commit"
        return 1
      fi
      if [[ -n "${pos[body-edit]:-}" ]]; then
        echo "an unchanged verdict must never edit the PR body"
        return 1
      fi
      if [[ -n "${pos[push]:-}" ]]; then
        echo "an unchanged verdict must never force-push -- no amend means no rewritten SHA to push"
        return 1
      fi
      if [[ -z "${pos[commit-noop]:-}" ]]; then
        echo "an unchanged verdict must record commit-noop -- the idempotent, zero-amend path"
        return 1
      fi
      ;;

    *)
      echo "unknown scenario: ${scenario}"
      return 1
      ;;
  esac

  return 0
}

# ---------------------------------------------------------------------------
# _substitute_placeholders <doc-command-line>
#
# Pure. Turns a doc-literal command line's angle-bracket placeholders into
# concrete values so it can actually be executed against the recording
# gh/git stubs. Deliberately narrow: only the placeholders that appear in
# phase-9-pr.md's Parent Close Gate / Commit / Push / PR spans this suite
# drives.
# ---------------------------------------------------------------------------
_substitute_placeholders() {
  local line="$1"
  line="${line//<worktree-path>/\/tmp\/wt}"
  line="${line//<parentId>/900}"
  line="${line//<childId>/800}"
  line="${line//<ticket-id-or-slug>/900-demo}"
  line="${line//<owner>\/<repo>/acme\/widgets}"
  line="${line//<branch>/feature\/800-demo}"
  printf '%s' "${line}"
}

# ---------------------------------------------------------------------------
# replay_through_stub <commands-newline-separated> <log-prefix>
#
# `drive_*`-style helper (mirrors flow/tests/refine-write-order/lib.sh's
# replay_through_stub and contract-lib.sh's drive_baseline_gate /
# drive_worktree_guard): actually EXECUTES a real doc-extracted (or
# suite-authored canonical) command sequence against fixtures/gh.stub.sh and
# fixtures/git.stub.sh -- each copied into a fresh `mktemp -d`/bin/{gh,git}
# and chmod +x'd here at runtime (permission-adding only; both committed
# fixtures carry no executable bit) -- and captures what really happened.
#
# <log-prefix>.gh.log / <log-prefix>.git.log receive one line per
# invocation of that binary (its "$*", i.e. the binary's argv without the
# binary's own name). <log-prefix>.stdout receives each invocation's
# captured stdout, one per replayed line, in order. <log-prefix>.stderr
# receives stderr. Every GH_STUB_*/GIT_STUB_* env var already exported by
# the caller (view/log-range/head-message fixtures, fail patterns) is
# inherited by the replayed invocations; this function only fixes
# GH_STUB_LOG/GIT_STUB_LOG to the two log files above.
#
# Each extracted line is tokenized (`read -r -a argv`) and exec'd directly
# as `"${argv[@]}"` -- never `bash -c "${line}"`. A line containing any
# shell metacharacter (`| ; & < > $ \` ( ) { }`) is refused outright --
# logged as an error to <log-prefix>.stderr and counted as a failure --
# rather than silently skipped or passed through to a shell.
#
# Returns 0 if every replayed line exited 0, 1 if any line failed or was
# refused as unreplayable (the harness still replays every line rather than
# aborting at the first failure, so the log/stdout capture is always
# complete).
# ---------------------------------------------------------------------------
replay_through_stub() {
  local commands="$1" log_prefix="$2"
  local binroot line subst out overall=0 rc
  local gh_log="${log_prefix}.gh.log" git_log="${log_prefix}.git.log"

  binroot="$(mktemp -d)" || return 1
  if ! cp "${PARENT_CLOSE_RECONCILE_FIXTURES}/gh.stub.sh" "${binroot}/gh"; then
    rm -rf "${binroot}"
    return 1
  fi
  if ! cp "${PARENT_CLOSE_RECONCILE_FIXTURES}/git.stub.sh" "${binroot}/git"; then
    rm -rf "${binroot}"
    return 1
  fi
  chmod +x "${binroot}/gh" "${binroot}/git" || { rm -rf "${binroot}"; return 1; }

  : > "${gh_log}" || { rm -rf "${binroot}"; return 1; }
  : > "${git_log}" || { rm -rf "${binroot}"; return 1; }
  : > "${log_prefix}.stdout" || { rm -rf "${binroot}"; return 1; }
  : > "${log_prefix}.stderr" || { rm -rf "${binroot}"; return 1; }

  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    subst="$(_substitute_placeholders "${line}")"
    if printf '%s' "${subst}" | grep -qE '[|;&<>$`(){}]'; then
      printf 'replay_through_stub: refusing to replay unreplayable-by-design line (shell metacharacter present): %s\n' "${subst}" >> "${log_prefix}.stderr"
      overall=1
      continue
    fi
    local -a argv=()
    read -r -a argv <<<"${subst}"
    if [[ "${#argv[@]}" -eq 0 ]]; then
      overall=1
      continue
    fi
    if out="$(PATH="${binroot}:${PATH}" GH_STUB_LOG="${gh_log}" GIT_STUB_LOG="${git_log}" "${argv[@]}" 2>>"${log_prefix}.stderr")"; then
      rc=0
    else
      rc=1
      overall=1
    fi
    printf '%s\n' "${out}" >> "${log_prefix}.stdout"
    : "${rc}"
  done <<<"${commands}"

  rm -rf "${binroot}"
  return "${overall}"
}
