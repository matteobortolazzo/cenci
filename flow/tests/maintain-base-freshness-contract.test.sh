#!/usr/bin/env bash
# Contract coverage for the maintain base-branch freshness gate: a
# /cenci:maintain run must never open a knowingly stale PR, must integrate an
# advanced base without rewriting a published branch, must rerun the complete
# health gate after every integration, and must repair -- never silently
# report success on -- a CONFLICTING PR. See flow/skills/maintain/
# base-freshness.md, and its Phase 6 call sites in SKILL.md and codex.md.
#
# House style copied from flow/tests/maintain-client-config-contract.test.sh:
# a `failures=` counter, small assert_* helpers, exact substring/ordering
# markers -- never a generic keyword that could vacuously match (see
# docs/shell-scripting-gotchas.md and flow/tests/maintain.test.sh:2121-2133's
# anchor discipline). Every phrase asserted below is a single unwrapped
# source line and the exact edit-site text in the real committed docs.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "maintain-base-freshness-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "maintain-base-freshness-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <abs-path> -- pure extraction, no fail() side effect here: it
  # is deliberately safe to call inside a $(...) command substitution.
  local _path="$1"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <abs-path> -- nameref wrapper around read_doc_raw
# that assigns the file's content into <result-var>, or fails closed with a
# distinct "not found" message and assigns "" if not found. Must NOT be
# invoked via $(...). Fails closed so no assert_not_contains below can pass
# vacuously against a missing file.
require_doc() {
  local -n _result="$1"
  local _path="$2"
  local _content
  if ! _content="$(read_doc_raw "${_path}")"; then
    fail "doc not found/unreadable: ${_path}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

assert_contains() {
  local content="$1" phrase="$2" label="$3"
  [[ -n "${phrase}" ]] || { fail "${label}: empty required phrase (test bug)"; return; }
  [[ "${content}" == *"${phrase}"* ]] || fail "${label}: required text missing: [${phrase}]"
}

assert_not_contains() {
  local content="$1" phrase="$2" label="$3"
  [[ -n "${phrase}" ]] || { fail "${label}: empty forbidden phrase (test bug)"; return; }
  [[ "${content}" != *"${phrase}"* ]] || fail "${label}: stale/forbidden text remains: [${phrase}]"
}

assert_before() {
  local content="$1" first="$2" second="$3" label="$4"
  local before_first before_second
  [[ -n "${first}" && -n "${second}" ]] || { fail "${label}: empty ordering marker (test bug)"; return; }
  [[ "${content}" == *"${first}"* ]] || { fail "${label}: first marker missing: [${first}]"; return; }
  [[ "${content}" == *"${second}"* ]] || { fail "${label}: second marker missing: [${second}]"; return; }
  before_first="${content%%"${first}"*}"
  before_second="${content%%"${second}"*}"
  [[ "${#before_first}" -lt "${#before_second}" ]] || fail "${label}: markers are out of order"
}

require_doc skill "${FLOW_DIR}/skills/maintain/SKILL.md" || true
require_doc codex "${FLOW_DIR}/skills/maintain/codex.md" || true
require_doc bf "${FLOW_DIR}/skills/maintain/base-freshness.md" || true
require_doc readme "${FLOW_DIR}/README.md" || true

# =====================================================================
# 1. Freshness check occurs after edits, before the final gate/push --
#    assert_before chains in SKILL.md and the equivalent chain in codex.md.
# =====================================================================

assert_before "${skill}" 'Apply only the approved actions' '**Gate A**' \
  "1 SKILL.md: Gate A follows the approved-edits step"
assert_before "${skill}" '**Gate A**' 'Verify the repair before shipping it' \
  "1 SKILL.md: final verification follows Gate A"
assert_before "${skill}" 'Verify the repair before shipping it' '**Gate B**' \
  "1 SKILL.md: Gate B follows the final verification"
assert_before "${skill}" '**Gate B**' 'push -u origin chore/maintain-<run-token>' \
  "1 SKILL.md: the push command follows Gate B"
assert_before "${skill}" 'push -u origin chore/maintain-<run-token>' 'gh pr create --title "chore: maintain' \
  "1 SKILL.md: gh pr create follows the push"

assert_before "${codex}" 'Apply only the selected repairs' '**Gate A**' \
  "1 codex.md: Gate A follows the approved-edits step"
assert_before "${codex}" '**Gate A**' 'Run the executable/default checker' \
  "1 codex.md: the executable checker follows Gate A"
assert_before "${codex}" 'Run the executable/default checker' '**Gate B**' \
  "1 codex.md: Gate B follows the executable checker"
assert_before "${codex}" '**Gate B**' 'push -u origin chore/maintain-<run-token>' \
  "1 codex.md: the push command follows Gate B"
assert_before "${codex}" 'push -u origin chore/maintain-<run-token>' 'open one PR containing only the approved maintenance' \
  "1 codex.md: PR creation follows the push"

# =====================================================================
# 2. Advanced unpublished base is integrated and all gates rerun --
#    base-freshness.md carries the probes, the unpublished-branch rebase,
#    and the rerun-complete-verification sentence.
# =====================================================================

assert_contains "${bf}" 'git -C <worktree-path> fetch origin <base>' \
  "2 base-freshness.md: freshness probe fetches the resolved base"
assert_contains "${bf}" 'merge-base --is-ancestor origin/<base> HEAD' \
  "2 base-freshness.md: freshness probe's merge-base --is-ancestor check"
assert_contains "${bf}" 'ls-remote --exit-code --heads origin chore/maintain-<run-token>' \
  "2 base-freshness.md: published-branch probe"
assert_contains "${bf}" 'rebase origin/<base>' \
  "2 base-freshness.md: unpublished branch is rebased onto the advanced base"
assert_contains "${bf}" 'Rerun the complete verification after any integration' \
  "2 base-freshness.md: rerun-complete-verification sentence"
assert_contains "${bf}" 'explicitly including when the incoming changes are documentation-only' \
  "2 base-freshness.md: documentation-only changes still get the full gate"

# =====================================================================
# 3. Published branch uses non-rewriting integration -- merge --no-ff is
#    present in the shared fragment; the client docs never repeat a
#    history-rewriting or hook-skipping flag.
# =====================================================================

assert_contains "${bf}" 'merge --no-ff origin/<base>' \
  "3 base-freshness.md: published branch integrates via non-rewriting merge"
assert_contains "${bf}" 'amend, reset, or force-push a published maintenance branch.' \
  "3 base-freshness.md: published branch is never rewritten"

for label_doc in "SKILL.md:${skill}" "codex.md:${codex}"; do
  doc_label="${label_doc%%:*}"
  doc_content="${label_doc#*:}"
  assert_not_contains "${doc_content}" '--force-with-lease' "3 ${doc_label}: no --force-with-lease"
  assert_not_contains "${doc_content}" '--force' "3 ${doc_label}: no --force"
  assert_not_contains "${doc_content}" 'push -f' "3 ${doc_label}: no push -f"
  assert_not_contains "${doc_content}" '--no-verify' "3 ${doc_label}: no --no-verify"
done

# =====================================================================
# 4. A second base advance before push causes another verification pass --
#    Gate B's re-probe -> integrate -> rerun-complete-verification ->
#    re-probe loop sentence, plus its ordering ahead of the push command.
# =====================================================================

assert_contains "${bf}" 'Repeat until the probe reports fresh in the same turn as the push.' \
  "4 base-freshness.md: Gate B loop sentence"
assert_contains "${skill}" 'Repeat until the probe reports fresh in the same turn as the push.' \
  "4 SKILL.md: Gate B loop sentence"
assert_contains "${codex}" 'Repeat until the probe reports fresh in the same turn as the push.' \
  "4 codex.md: Gate B loop sentence"
assert_before "${skill}" 'Repeat until the probe reports fresh in the same turn as the push.' \
  'push -u origin chore/maintain-<run-token>' \
  "4 SKILL.md: Gate B loop sentence precedes the push command"
assert_before "${codex}" 'Repeat until the probe reports fresh in the same turn as the push.' \
  'push -u origin chore/maintain-<run-token>' \
  "4 codex.md: Gate B loop sentence precedes the push command"
assert_contains "${skill}" 'Never push, and never open a PR, from a tree the probe has not just confirmed fresh.' \
  "4 SKILL.md: Gate B forbids pushing/opening a PR from an unconfirmed tree"

# =====================================================================
# 5. A conflicting PR is not reported as successfully completed --
#    gh pr view ... --json mergeable is present; UNKNOWN is handled as a
#    distinct branch from CONFLICTING; the bounded CONFLICTING repair loop;
#    the "never report success" sentence; and codex.md's goal-clear
#    precondition now names non-CONFLICTING mergeability.
# =====================================================================

assert_contains "${bf}" 'gh pr view chore/maintain-<run-token> --json mergeable,mergeStateStatus,url,number' \
  "5 base-freshness.md: Gate C mergeability query"
assert_contains "${bf}" 'never reported as mergeable and never' \
  "5 base-freshness.md: UNKNOWN is never mergeable and never conflicting"
assert_contains "${bf}" 'as conflicting.' \
  "5 base-freshness.md: UNKNOWN as-conflicting clause present"
assert_contains "${bf}" 'the run is **not**' \
  "5 base-freshness.md: CONFLICTING is a distinct, not-done branch"
assert_contains "${bf}" 'Bound the repair to two attempts' \
  "5 base-freshness.md: bounded CONFLICTING repair loop"
assert_contains "${bf}" 'Never report the run as successfully completed on a `CONFLICTING` PR.' \
  "5 base-freshness.md: never-report-success sentence"

assert_contains "${skill}" 'bounded to two repair attempts' \
  "5 SKILL.md: bounded CONFLICTING repair loop"
assert_contains "${skill}" 'Never report the run as successfully completed on a `CONFLICTING` PR.' \
  "5 SKILL.md: never-report-success sentence"

assert_contains "${codex}" 'Bound the repair to two attempts.' \
  "5 codex.md: bounded CONFLICTING repair loop"
assert_contains "${codex}" 'Never report the run as successfully completed on a `CONFLICTING` PR.' \
  "5 codex.md: never-report-success sentence"
assert_contains "${codex}" 'Clear the goal only after the PR exists and its mergeability is confirmed not' \
  "5 codex.md: goal-clear precondition now names non-CONFLICTING mergeability"

# =====================================================================
# 6. Claude/Codex contracts do not drift -- both docs reference
#    base-freshness.md and name all three gates; the mechanics live only in
#    the fragment; SKILL.md asks via AskUserQuestion, codex.md never says
#    AskUserQuestion and uses client-neutral wording.
# =====================================================================

assert_contains "${skill}" '`base-freshness.md`' "6 SKILL.md references base-freshness.md"
assert_contains "${codex}" '`base-freshness.md`' "6 codex.md references base-freshness.md"

for gate in '**Gate A**' '**Gate B**' '**Gate C**'; do
  assert_contains "${skill}" "${gate}" "6 SKILL.md names ${gate}"
  assert_contains "${codex}" "${gate}" "6 codex.md names ${gate}"
done

# The mechanics (the actual git command) live only in the shared fragment --
# never duplicated verbatim into either client doc.
assert_not_contains "${skill}" 'merge --no-ff origin/<base>' \
  "6 SKILL.md does not duplicate the merge --no-ff mechanic"
assert_not_contains "${codex}" 'merge --no-ff origin/<base>' \
  "6 codex.md does not duplicate the merge --no-ff mechanic"

assert_contains "${skill}" 'use `AskUserQuestion` to' "6 SKILL.md Gate C asks via AskUserQuestion"
assert_not_contains "${codex}" 'AskUserQuestion' "6 codex.md never says AskUserQuestion"
assert_contains "${codex}" "Codex's available user-input mechanism" \
  "6 codex.md Gate C uses client-neutral wording"

# =====================================================================
# 7. Hardcoded `main` removed from SKILL.md's worktree-add and PR-create
#    call sites, replaced by the resolved <base>.
# =====================================================================

assert_not_contains "${skill}" '-b chore/maintain-<run-token> main' \
  "7 SKILL.md: worktree add no longer hardcodes main"
assert_not_contains "${skill}" '--base main' \
  "7 SKILL.md: gh pr create no longer hardcodes --base main"
assert_contains "${skill}" '-b chore/maintain-<run-token> <base>' \
  "7 SKILL.md: worktree add branches from the resolved <base>"
assert_contains "${skill}" '--base <base>' \
  "7 SKILL.md: gh pr create targets the resolved <base>"

# codex.md never hardcoded a worktree-creation base ref (it delegates to the
# shared worktree procedure), but it named no <base> resolution at all before
# this ticket -- assert it now does.
assert_contains "${codex}" 'Resolve `<base>` first, before generating the run token' \
  "7 codex.md: now names <base> resolution, which it omitted entirely before"
assert_contains "${codex}" 'never a hardcoded `main`' \
  "7 codex.md: worktree branches from <base>, never a hardcoded main"

# =====================================================================
# 8. Generated index -- flow/README.md's workflow-deps row lists
#    base-freshness.md among maintain's procedure files.
# =====================================================================

assert_contains "${readme}" '| `maintain` | base-freshness.md, codex.md,' \
  "8 README.md workflow-deps: maintain row lists base-freshness.md"

echo "maintain-base-freshness-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
