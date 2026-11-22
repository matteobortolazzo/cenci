#!/usr/bin/env bash
# The executable workflow here is skill prose: scope each failure assertion to
# its owning step so a fallback in HS-U5 cannot cover the earlier exits.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 2
PHASE1="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
CODEX="${FLOW_DIR}/skills/implement/codex.md"
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

extract_section() {
  awk -v want="$2" '$0 == want { on=1; next } on && /^## / { exit } on { print }' "$1"
}
extract_step() {
  awk -v want="$2" '$0 ~ "^" want "\\. " { on=1; print; next } on && /^[0-9]+\. / { exit } on { print }' <<<"$1"
}
assert_contains() {
  [[ -n "$1" ]] || { fail "$3: missing section/step"; return; }
  [[ "$1" == *"$2"* ]] || fail "$3: missing '$2'"
}

UNATTENDED="$(extract_section "$PHASE1" '## Unattended Escalation Path')"
RESTORE="$(extract_section "$PHASE1" '## Restore Awaiting-Input State')"
INVENTORY="$(extract_section "$PHASE1" '## Hard-Stop Inventory')"

# Provenance and exclusions belong to the shared rule, not an individual error.
assert_contains "$UNATTENDED" 'Sizing escalation failure cleanup' 'named sizing failure rule'
assert_contains "$UNATTENDED" 'Split Gate route' 'sizing provenance'
assert_contains "$UNATTENDED" 'never infer sizing provenance from an error or an L estimate alone' 'unrelated holds remain outside sizing cleanup'
assert_contains "$UNATTENDED" 'never in explicit Plan mode or ticketless mode' 'write exclusions'
assert_contains "$UNATTENDED" 'source implementation issue' 'source child rather than parent'
assert_contains "$UNATTENDED" 'preserve the existing draft, escalationNonce, escalationCommentId, human answers, planCommitSha and stalenessPaths' 'durable recovery context'

# Pre-draft mint, malformed assembly, staging/post/readback, anchor persist,
# stage and label failures each explicitly invoke the shared reconciliation.
for step in 0 1 2 3 4 5; do
  STEP="$(extract_step "$UNATTENDED" "$step")"
  assert_contains "$STEP" 'Sizing escalation failure cleanup' "HS-U${step} failure cleanup"
done
STEP0="$(extract_step "$UNATTENDED" 0)"
assert_contains "$STEP0" 'For a sizing escalation, run **Sizing escalation failure cleanup** before stopping' 'nonce failure releases claim before draft exists'
assert_contains "$STEP0" 'An unrelated non-sizing escalation still takes the bare-`Working` stop' 'nonce failure retains unrelated hold'
STEP1="$(extract_step "$UNATTENDED" 1)"
assert_contains "$STEP1" 'do not fabricate an awaiting-input draft or apply Input Needed' 'malformed draft does not invent resumable state'
assert_contains "$STEP1" 'existing valid draft' 'assembly retains prior valid recovery state'
assert_contains "$STEP1" 'Before any sizing-retry write' 'existing recovery state checked before assembly'
STEP6="$(extract_step "$UNATTENDED" 6)"
[[ "$STEP6" != *'Sizing escalation failure cleanup'* ]] || fail 'successful escalation must retain its normal Input Needed stop'

# Restoration errors use the same narrow fallback at every exit, including a
# failed validity check where awaiting-input restoration is impossible.
assert_contains "$RESTORE" 'For a sizing escalation, run **Sizing escalation failure cleanup** before stopping' 'restore failure invokes source release'
assert_contains "$RESTORE" 'draft-validity, await-input, input-needed label, or re-read failure' 'all restore failure branches covered'
assert_contains "$RESTORE" 'Unrelated non-sizing failures retain the existing restoration/backstop behavior' 'unrelated restore holds preserved'
assert_contains "$RESTORE" 'Once `Working` is verified absent, do not promise a Working-gated reconciler recovery' 'release does not promise inapplicable crash recovery'
assert_contains "$RESTORE" 'Only while `Working` is still verified present' 'reconciler claims match actual residual labels'
assert_contains "$RESTORE" 'If label removal is unverified, say so' 'uncertain removal does not imply automatic recovery'
assert_contains "$INVENTORY" 'sizing escalation uses Sizing escalation failure cleanup' 'HS-U0 inventory matches cleanup exception'

# Source-only removal is already the verified read/edit/re-read procedure. It
# preserves the valid board/draft state rather than undoing Input Needed.
RELEASE="$(awk '/^\*\*Release the stopped implementation issue\.\*\*/ { on=1 } on && /^## / { exit } on { print }' "$CODEX")"
assert_contains "$RELEASE" 'HS-U0 through HS-U5' 'Codex companion names every Claude unattended failure'
assert_contains "$RELEASE" 'failed Restore Awaiting-Input State' 'Codex companion covers restore failures'
assert_contains "$RELEASE" 'preserve any already-valid Input Needed state and awaiting-input draft' 'valid recovery state retained'
assert_contains "$RELEASE" '--remove-label Working' 'only Working removal'
assert_contains "$RELEASE" 're-fetch source labels and confirm absence' 'live verification'
assert_contains "$RELEASE" 'preserve the assignee and all other labels' 'unrelated labels retained'
assert_contains "$RELEASE" 'never in explicit Plan mode and never in ticketless mode' 'companion write exclusions'

echo "unattended-sizing-release.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
