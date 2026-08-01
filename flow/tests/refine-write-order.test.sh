#!/usr/bin/env bash
# Behavioral suite for ticket #878, "refine: make proposal confirmation the
# first remote mutation (1/12)". refine/SKILL.md and refine/codex.md both
# defer every GitHub write — the ticket-ownership assignee claim, the
# "Working" label add, and everything downstream of it — until AFTER the
# human confirms the proposal at `## Confirmation Gate` (SKILL.md) /
# codex.md's equivalent gate: the gate's single Confirm/Decline question is
# the true first-mutation boundary for the whole run, not just for the
# proposal-content writes it already gated before #878. This suite is the
# regression guard for that invariant: it proves, against the real
# committed docs (never a hardcoded copy of their text), that zero writes
# occur before the gate, that the canonical `### Write order` section each
# doc carries satisfies the expected partial order, and that a doc edit
# which reintroduces a pre-gate write or reorders the post-confirm sequence
# is caught, not silently missed by an extractor that stops scanning too
# early or a check that ignores an "unknown"-classified call.
#
# Read before writing this suite (per flow/AGENTS.md's Critical Rules):
#   - flow/docs/shell-scripting-gotchas.md — fence-aware section extraction,
#     the read_*_raw/require_read_* purity split, marker precision on both
#     ends, unmatched-glob and unchecked-command-substitution pitfalls.
#   - flow/docs/adapter-contract.md's "Ordering, not just presence" section —
#     the drive_*/verify_* idiom (contract-lib.sh:590-768) this suite's
#     library (flow/tests/refine-write-order/lib.sh) follows, and the
#     "prove the check itself rejects bad input via a deliberately broken
#     COPY, never the real doc" non-vacuity discipline.
#
# Library: flow/tests/refine-write-order/lib.sh (extractors + oracles).
# Fixtures: flow/tests/refine-write-order/fixtures/ (a recording `gh` stub,
# never committed executable, plus canned `gh issue view` JSON responses per
# scenario: baseline, authorization-drifted, cosmetically-drifted, and
# foreign-assignee parent snapshots).
#
# Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob — no
# registration needed.
#
# Scenario matrix (each row below is implemented as either a real-doc
# extraction+classification assertion, a doc-anchor pin for #878's not-yet-
# written green-phase text, or both, plus a `replay_through_stub` execution
# proof where the scenario is about what actually happens when the
# documented commands run):
#
#   | Scenario                                   | Asserted                                            |
#   |---------------------------------------------|-----------------------------------------------------|
#   | Any prefix of pre-gate command inventory     | zero write-classified calls                          |
#   | Decline                                      | zero gh calls at all (only rm -f)                     |
#   | Confirm, no split                            | claim -> Working -> parent body -> Refined/-Working -> ui:visual-check |
#   | Confirm, 2-child split                       | each child-create immediately followed by its own child-link; all child ops before parent-exec-order; Refined/-Working after every create |
#   | Post-confirm ownership conflict              | zero writes total; no body edit, no child create      |
#   | Authorization-sensitive drift                | zero writes; stop-for-fresh-confirmation reported     |
#   | Cosmetic drift                                | full write order proceeds; disclosure present         |
#   | Unreadable parent fetch (both attempts)      | zero writes                                           |
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-write-order.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-write-order.test.sh: failed to resolve flow directory." >&2; exit 2; }
LIB_DIR="${SCRIPT_DIR}/refine-write-order"
FIXTURES_DIR="${LIB_DIR}/fixtures"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# shellcheck source=./refine-write-order/lib.sh
if ! source "${LIB_DIR}/lib.sh"; then
  echo "refine-write-order.test.sh: failed to source ${LIB_DIR}/lib.sh" >&2
  exit 2
fi

assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_ok() { [[ "$1" -eq 0 ]] || fail "$2: expected success (exit 0), got exit $1"; }
assert_nonzero() { [[ "$1" -ne 0 ]] || fail "$2: expected non-zero exit, got 0"; }
assert_reason_contains() {
  # $1=exit-code $2=printed-reason $3=required-substring $4=label
  if [[ "$1" -eq 0 ]]; then
    fail "$4: expected verify_refine_write_order to reject this sequence, but it accepted it"
    return
  fi
  [[ "$2" == *"$3"* ]] || fail "$4: expected failure reason to contain [$3], got [$2]"
}
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
cleanup_replay_log() {
  # $1=log-file-path — removes a replay_through_stub log and its
  # .stdout/.stderr/.counter companions (used at every replay call site below).
  local log="$1"
  rm -f "${log}" "${log}.stdout" "${log}.stderr" "${log}.counter"
}

# =====================================================================
# Section 1 — self-tests: classify_gh_call, classify_drift,
# verify_refine_write_order, replay_through_stub. Every negative case is
# proven against a deliberately broken/synthetic COPY, never the real doc
# (docs/adapter-contract.md's "Ordering, not just presence" non-vacuity
# rule; mirrors flow/tests/parity/parity.test.sh's bad-* fixture idiom).
# =====================================================================

# --- 1a. classify_gh_call ----------------------------------------------

assert_eq "$(classify_gh_call 'gh issue view 100 --repo acme/widgets --json labels')" "read" \
  "classify_gh_call: gh issue view must classify as read"
assert_eq "$(classify_gh_call 'gh api user --jq .login')" "read" \
  "classify_gh_call: gh api user (default GET) must classify as read"
assert_eq "$(classify_gh_call 'git remote get-url origin')" "read" \
  "classify_gh_call: git remote get-url must classify as read"
assert_eq "$(classify_gh_call 'gh issue edit 100 --repo acme/widgets --add-label Working')" "write" \
  "classify_gh_call: gh issue edit must classify as write"
assert_eq "$(classify_gh_call 'gh label create Working --repo acme/widgets --color FBCA04')" "write" \
  "classify_gh_call: gh label create must classify as write"
assert_eq "$(classify_gh_call 'gh api repos/acme/widgets/issues -X POST --input payload.json --jq .number')" "write" \
  "classify_gh_call: gh api ... -X POST must classify as write"
assert_eq "$(classify_gh_call 'gh api repos/acme/widgets/issues/100 -X PATCH --input payload.json')" "write" \
  "classify_gh_call: gh api ... -X PATCH must classify as write"

# Reject side (non-vacuity, gh api fail-open regression, #878 review): the
# `gh api` branch must not fail open on a method or field/input flag it
# doesn't literally spell as "-X POST"/"-X PATCH"/... -- `--method POST`,
# the no-space `-XPOST` form, a `graphql` endpoint, and any `-f`/`-F`/
# `--field`/`--raw-field`/`--input` flag (gh defaults to POST when fields
# or input are present, even with no explicit `-X`) must all classify as
# write, never silently read.
assert_eq "$(classify_gh_call 'gh api repos/acme/widgets/issues --method POST --input payload.json --jq .number')" "write" \
  "classify_gh_call: gh api ... --method POST must classify as write"
assert_eq "$(classify_gh_call 'gh api repos/acme/widgets/issues/100 --method patch --input payload.json')" "write" \
  "classify_gh_call: gh api ... --method patch (lowercase) must classify as write"
assert_eq "$(classify_gh_call 'gh api repos/acme/widgets/issues/100 -XPOST --input payload.json')" "write" \
  "classify_gh_call: gh api ... -XPOST (no space) must classify as write"
assert_eq "$(classify_gh_call "gh api graphql -f query='mutation { addLabel }'")" "write" \
  "classify_gh_call: gh api graphql must classify as write regardless of query/mutation payload text"
assert_eq "$(classify_gh_call 'gh api repos/acme/widgets/issues/100 -f state=closed')" "write" \
  "classify_gh_call: gh api ... -f <field> with no explicit -X must classify as write (gh defaults to POST)"
assert_eq "$(classify_gh_call 'gh api repos/acme/widgets/issues/100 --input payload.json')" "write" \
  "classify_gh_call: gh api ... --input <file> with no explicit -X must classify as write (gh defaults to POST)"

# Reject side (non-vacuity): an unrecognized gh subcommand and an
# unrecognized git subcommand must both classify as "unknown", never
# vacuously fall through to "read" — a fail-open default here would let a
# real mutating verb this function doesn't yet know about silently pass a
# zero-write assertion.
assert_eq "$(classify_gh_call 'gh issue transfer 100 --repo acme/widgets --to other/repo')" "unknown" \
  "classify_gh_call: an unrecognized gh subcommand must classify as unknown, not vacuously as read"
assert_eq "$(classify_gh_call 'git commit -m demo')" "unknown" \
  "classify_gh_call: an unrecognized git subcommand must classify as unknown, not vacuously as read"
assert_eq "$(classify_gh_call '')" "unknown" \
  "classify_gh_call: an empty invocation must classify as unknown, not vacuously as read"

# --- 1b. classify_drift (against REAL fixture JSON, not hardcoded label
# strings duplicated in the test -- non-vacuous connection to the fixture
# files the scenarios below also drive through the stub) -----------------

BASELINE_LABELS="$(jq -r '.labels[].name' "${FIXTURES_DIR}/parent-baseline.json" | tr '\n' ' ')"
AUTH_DRIFT_LABELS="$(jq -r '.labels[].name' "${FIXTURES_DIR}/parent-auth-drift.json" | tr '\n' ' ')"
COSMETIC_DRIFT_LABELS="$(jq -r '.labels[].name' "${FIXTURES_DIR}/parent-cosmetic-drift.json" | tr '\n' ' ')"

assert_eq "$(classify_drift "${BASELINE_LABELS}" "${AUTH_DRIFT_LABELS}")" "authorization" \
  "classify_drift: an automerge:ok/Browser/ui:visual-check label change (fixtures/parent-auth-drift.json) must classify as authorization-sensitive drift"
assert_eq "$(classify_drift "${BASELINE_LABELS}" "${COSMETIC_DRIFT_LABELS}")" "cosmetic" \
  "classify_drift: a milestone/area/priority/team/Design-only label change (fixtures/parent-cosmetic-drift.json) must classify as cosmetic drift"
assert_eq "$(classify_drift "${BASELINE_LABELS}" "${BASELINE_LABELS}")" "none" \
  "classify_drift: an unchanged label set must classify as no drift"

# Reject side (non-vacuity): a drift that touches BOTH an authorization
# label and cosmetic labels must still classify as authorization, never
# silently downgraded to cosmetic just because most of the diff is cosmetic.
MIXED_DRIFT_LABELS="${COSMETIC_DRIFT_LABELS} automerge:ok"
assert_eq "$(classify_drift "${BASELINE_LABELS}" "${MIXED_DRIFT_LABELS}")" "authorization" \
  "classify_drift: a mixed cosmetic+authorization drift must classify as authorization, never downgraded to cosmetic"

# --- 1c. verify_refine_write_order --------------------------------------

GOOD_NO_SPLIT_SEQ="claim working parent-body refined visual-check"
GOOD_SPLIT_SEQ="claim working parent-body child-create:1 child-link:1 child-create:2 child-link:2 parent-exec-order refined visual-check"

verify_refine_write_order "${GOOD_NO_SPLIT_SEQ}"; rc=$?
assert_ok "${rc}" "verify_refine_write_order: the canonical no-split order (claim -> working -> parent-body -> refined -> visual-check) must be accepted"

verify_refine_write_order "${GOOD_SPLIT_SEQ}"; rc=$?
assert_ok "${rc}" "verify_refine_write_order: the canonical 2-child-split order must be accepted"

verify_refine_write_order ""; rc=$?
assert_ok "${rc}" "verify_refine_write_order: the empty sequence (zero writes) must be vacuously accepted -- the shape produced by a decline, a post-confirm ownership conflict, or an unreadable parent"

# Reject side (non-vacuity): every negative case below is a deliberately
# broken/reordered COPY of the good sequences above -- never the real doc.
reason="$(verify_refine_write_order "working claim parent-body refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must precede working" \
  "verify_refine_write_order: claim after working must be rejected"

reason="$(verify_refine_write_order "claim working child-create:1 child-link:1 parent-body refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "after parent-body" \
  "verify_refine_write_order: a child create/link before parent-body must be rejected"

reason="$(verify_refine_write_order "claim working parent-body child-create:1 child-create:2 child-link:1 child-link:2 parent-exec-order refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "immediately followed by its own child-link" \
  "verify_refine_write_order: a non-adjacent child-create/child-link pair must be rejected"

reason="$(verify_refine_write_order "claim working parent-body child-create:1 child-link:1 parent-exec-order child-create:2 child-link:2 refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "precede parent-exec-order" \
  "verify_refine_write_order: a child op after parent-exec-order must be rejected"

reason="$(verify_refine_write_order "claim working parent-body child-create:1 child-link:1 refined child-create:2 child-link:2 visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "after every child create/link" \
  "verify_refine_write_order: refined before every child is created must be rejected"

reason="$(verify_refine_write_order "claim working parent-body visual-check refined")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "visual-check must come after refined" \
  "verify_refine_write_order: visual-check before refined must be rejected"

reason="$(verify_refine_write_order "claim working bogus-op parent-body refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "unknown op token" \
  "verify_refine_write_order: an unrecognized op token must be rejected fail-closed, never silently ignored"

# Reject side (non-vacuity, #878 review): `claim` is the exclusive-ownership
# control the whole ticket is about -- a non-empty sequence with it dropped
# entirely, or present but not the first op, must be rejected.
reason="$(verify_refine_write_order "working parent-body refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "claim must be present" \
  "verify_refine_write_order: a sequence with claim dropped entirely must be rejected"

reason="$(verify_refine_write_order "parent-body claim refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "claim must be present" \
  "verify_refine_write_order: claim present but not the first op (with no working token to trip the precede-working check) must be rejected"

# Reject side (non-vacuity, #878 review, newline-separated input): the real
# call sites feed this function extract_write_order_ops_raw's output, which
# is newline-separated (one op token per line), not space-separated --
# `read -a` on that input without first flattening it only consumes the
# first line, so a doc whose FIRST written op is correct but a LATER op is
# wrong would previously validate vacuously. Both a well-formed and a
# deliberately-broken newline-separated sequence are exercised here.
NEWLINE_GOOD_SEQ="$(printf 'claim\nworking\nparent-body\nrefined\nvisual-check\n')"
verify_refine_write_order "${NEWLINE_GOOD_SEQ}"; rc=$?
assert_ok "${rc}" "verify_refine_write_order: a newline-separated (not space-separated) well-formed sequence must be accepted"

NEWLINE_BAD_SEQ="$(printf 'claim\nworking\nvisual-check\nrefined\n')"
reason="$(verify_refine_write_order "${NEWLINE_BAD_SEQ}")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "visual-check must come after refined" \
  "verify_refine_write_order: a newline-separated sequence whose ordering violation is NOT on the first line must still be caught (proves the read -a flattening fix, not just the first token)"

# --- 1d. replay_through_stub mechanic (synthetic, proves the recorder
# itself works before the real-doc scenarios below trust it) -------------

SELFTEST_LOG="$(mktemp)" || { fail "mktemp failed for replay_through_stub self-test log"; SELFTEST_LOG=""; }
if [[ -n "${SELFTEST_LOG}" ]]; then
  SELFTEST_CMDS="gh issue view 100 --repo acme/widgets --json labels
gh issue edit 100 --repo acme/widgets --add-label Working"
  replay_through_stub "${SELFTEST_CMDS}" "${SELFTEST_LOG}" "${FIXTURES_DIR}/parent-baseline.json" ""
  rc=$?
  assert_ok "${rc}" "replay_through_stub self-test: both synthetic invocations should succeed against the stub"
  logged_lines="$(grep -c . "${SELFTEST_LOG}" 2>/dev/null || echo 0)"
  assert_eq "${logged_lines}" "2" \
    "replay_through_stub self-test: the log must record exactly the 2 replayed invocations"
  assert_file_contains "${SELFTEST_LOG}" "issue view 100" \
    "replay_through_stub self-test log must record the issue view invocation"
  assert_file_contains "${SELFTEST_LOG}" "issue edit 100" \
    "replay_through_stub self-test log must record the issue edit invocation"
  second_class="$(sed -n '2p' "${SELFTEST_LOG}")"
  assert_eq "$(classify_gh_call "gh ${second_class}")" "write" \
    "replay_through_stub self-test: the logged issue-edit invocation must classify as write"
  cleanup_replay_log "${SELFTEST_LOG}"
fi

# Reject side (non-vacuity, #878 review): replay_through_stub must refuse a
# line containing a shell metacharacter rather than executing it via a real
# shell -- proven against a deliberately-injected synthetic line, never a
# real doc line (none of the real docs' pre-gate spans contain one today).
METACHAR_LOG="$(mktemp)" || { fail "mktemp failed for replay_through_stub metachar-rejection self-test log"; METACHAR_LOG=""; }
if [[ -n "${METACHAR_LOG}" ]]; then
  METACHAR_CMDS='gh issue view 100 --repo acme/widgets --json labels
gh issue edit 100 --repo acme/widgets --add-label Working; rm -rf /tmp/should-not-run'
  replay_through_stub "${METACHAR_CMDS}" "${METACHAR_LOG}" "${FIXTURES_DIR}/parent-baseline.json" ""
  rc=$?
  assert_nonzero "${rc}" "replay_through_stub: a line containing a shell metacharacter must make the overall replay fail"
  logged_lines="$(grep -c . "${METACHAR_LOG}" 2>/dev/null || echo 0)"
  assert_eq "${logged_lines}" "1" \
    "replay_through_stub: the metachar-bearing line must never reach the gh stub -- only the first (clean) line should be logged"
  assert_file_contains "${METACHAR_LOG}.stderr" "refusing to replay unreplayable-by-design line" \
    "replay_through_stub: refusing a metachar-bearing line must be logged loudly to .stderr, never silently skipped"
  cleanup_replay_log "${METACHAR_LOG}"
fi

# --- 1e. extract_pre_gate_gh_calls_raw / extract_codex_pre_gate_gh_calls_raw
# non-vacuity (#878 review, Must Fix). Both extraction fixes are proven
# against a deliberately-broken COPY of the real doc -- built fresh under
# mktemp -d, never touching the real committed docs -- per this suite's
# established self-test idiom (see the header comment and 2d's declined-
# branch self-test below).
# =====================================================================

# extract_pre_gate_gh_calls_raw: a naive scan that stops at the first line
# merely mentioning "Confirmation Gate" (rather than the literal heading)
# would exit at SKILL.md's own forward reference inside `## Ownership
# Inspection` (~60 lines before the real `## Confirmation Gate` heading),
# silently skipping the entire `## Your Role` section from the scan. This
# injects a synthetic pre-gate write between `## Your Role` and `##
# Process` into a COPY of the real file and asserts the fixed extractor
# still captures it.
SKILL_SELFTEST_DIR="$(mktemp -d)" || { fail "mktemp -d failed for SKILL.md extractor non-vacuity self-test"; SKILL_SELFTEST_DIR=""; }
if [[ -n "${SKILL_SELFTEST_DIR}" ]]; then
  SKILL_INJECTED_COPY="${SKILL_SELFTEST_DIR}/SKILL-injected.md"
  awk '
    { print }
    /^## Your Role$/ && !injected {
      print ""
      print "```bash"
      print "gh issue edit <number> --repo <owner>/<repo> --add-label \"Working\""
      print "```"
      injected = 1
    }
  ' "${REFINE_SKILL}" > "${SKILL_INJECTED_COPY}"

  INJECTED_SKILL_PRE_GATE_CALLS=""
  if extracted="$(extract_pre_gate_gh_calls_raw "${SKILL_INJECTED_COPY}")"; then
    INJECTED_SKILL_PRE_GATE_CALLS="${extracted}"
  fi
  if [[ "${INJECTED_SKILL_PRE_GATE_CALLS}" != *'gh issue edit <number> --repo <owner>/<repo> --add-label "Working"'* ]]; then
    fail "extract_pre_gate_gh_calls_raw non-vacuity self-test: a synthetic pre-gate write injected between '## Your Role' and '## Process' in a COPY of refine/SKILL.md was NOT captured -- the fix did not actually widen the scan window"
  fi
  rm -rf "${SKILL_SELFTEST_DIR}"
fi

# extract_codex_pre_gate_gh_calls_raw: codex.md has zero fenced code
# blocks, so the fenced-only extractor always returns empty on it. This
# injects a synthetic inline single-backtick pre-gate write immediately
# before codex.md's own Confirmation Gate marker into a COPY of the real
# file and asserts the codex-specific extractor captures it.
CODEX_SELFTEST_DIR="$(mktemp -d)" || { fail "mktemp -d failed for codex.md extractor non-vacuity self-test"; CODEX_SELFTEST_DIR=""; }
if [[ -n "${CODEX_SELFTEST_DIR}" ]]; then
  CODEX_INJECTED_COPY="${CODEX_SELFTEST_DIR}/codex-injected.md"
  awk '
    { print }
    index($0, "runs before the gate below confirms.") > 0 && !injected {
      print ""
      print "Synthetic self-test injection: `gh issue edit 100 --repo acme/widgets --add-label \"Working\"`."
      injected = 1
    }
  ' "${REFINE_CODEX}" > "${CODEX_INJECTED_COPY}"

  INJECTED_CODEX_PRE_GATE_CALLS=""
  if extracted="$(extract_codex_pre_gate_gh_calls_raw "${CODEX_INJECTED_COPY}")"; then
    INJECTED_CODEX_PRE_GATE_CALLS="${extracted}"
  fi
  if [[ "${INJECTED_CODEX_PRE_GATE_CALLS}" != *'gh issue edit 100 --repo acme/widgets --add-label "Working"'* ]]; then
    fail "extract_codex_pre_gate_gh_calls_raw non-vacuity self-test: a synthetic inline pre-gate write injected immediately before codex.md's Confirmation Gate marker in a COPY of refine/codex.md was NOT captured -- codex.md's inline-backtick documentation style is not being scanned"
  fi
  rm -rf "${CODEX_SELFTEST_DIR}"
fi

# =====================================================================
# Section 2 — real-doc scenarios.
# =====================================================================

# --- 2a. "Any prefix of the pre-gate command inventory: zero
# write-classified calls" -- statically, against the real committed docs.
# refine/SKILL.md's extraction is fenced-code-only, anchored on the literal
# `^## Confirmation Gate$` heading; refine/codex.md documents every
# gh/git invocation as inline single-backtick prose with no fenced blocks
# at all, so it uses the sibling codex-specific extractor bounded by its
# own inline gate marker instead of the fenced-only one.
# =====================================================================

require_extract_pre_gate_gh_calls SKILL_PRE_GATE_CALLS "${REFINE_SKILL}" "refine/SKILL.md pre-gate command inventory"
if [[ -n "${SKILL_PRE_GATE_CALLS:-}" ]]; then
  while IFS= read -r call; do
    [[ -z "${call}" ]] && continue
    cls="$(classify_gh_call "${call}")"
    if [[ "${cls}" != "read" ]]; then
      fail "refine/SKILL.md: pre-gate call classified '${cls}' (expected read) -- the Confirmation Gate's Confirm must be the first GitHub write (#878): ${call}"
    fi
  done <<<"${SKILL_PRE_GATE_CALLS}"
fi

require_extract_codex_pre_gate_gh_calls CODEX_PRE_GATE_CALLS "${REFINE_CODEX}" "refine/codex.md pre-gate command inventory"
if [[ -n "${CODEX_PRE_GATE_CALLS:-}" ]]; then
  while IFS= read -r call; do
    [[ -z "${call}" ]] && continue
    cls="$(classify_gh_call "${call}")"
    if [[ "${cls}" != "read" ]]; then
      fail "refine/codex.md: pre-gate call classified '${cls}' (expected read) -- the Confirmation Gate's Confirm must be the first GitHub write (#878): ${call}"
    fi
  done <<<"${CODEX_PRE_GATE_CALLS}"
fi

# --- 2b. Same invariant, proven by actually EXECUTING refine/SKILL.md's
# real pre-gate commands through the recording gh stub (replay_through_stub)
# rather than only pattern-matching the doc text. A write- or
# unknown-classified logged invocation both count as a violation here --
# classify_gh_call's own contract is fail-closed ("unknown" must be treated
# the same as "write" by a zero-write assertion), so this check must never
# only look for the literal string "write".
# =====================================================================

if [[ -n "${SKILL_PRE_GATE_CALLS:-}" ]]; then
  REPLAY_LOG="$(mktemp)" || { fail "mktemp failed for pre-gate replay log"; REPLAY_LOG=""; }
  if [[ -n "${REPLAY_LOG}" ]]; then
    replay_through_stub "${SKILL_PRE_GATE_CALLS}" "${REPLAY_LOG}" "${FIXTURES_DIR}/parent-baseline.json" ""
    saw_write=0
    while IFS= read -r logged; do
      [[ -z "${logged}" ]] && continue
      cls="$(classify_gh_call "gh ${logged}")"
      [[ "${cls}" == "write" || "${cls}" == "unknown" ]] && saw_write=1
    done < "${REPLAY_LOG}"
    if [[ "${saw_write}" -eq 1 ]]; then
      fail "replaying refine/SKILL.md's real pre-gate commands through the recording gh stub produced a write- or unknown-classified invocation -- the Confirmation Gate's Confirm must be the first GitHub write (#878); log: ${REPLAY_LOG}"
    fi
    cleanup_replay_log "${REPLAY_LOG}"
  fi
fi

# =====================================================================
# 2c. "### Write order" canonical section -- both docs carry it (#878);
# require_extract_write_order_ops calls fail() itself if either doc's
# section goes missing in a future edit, and the extracted op sequence is
# then re-validated against the partial-order oracle below so a reordering
# is caught as a live regression, not just a presence check.
# =====================================================================

require_extract_write_order_ops SKILL_WRITE_ORDER "${REFINE_SKILL}" "refine/SKILL.md ### Write order section"
require_extract_write_order_ops CODEX_WRITE_ORDER "${REFINE_CODEX}" "refine/codex.md Write order parity block"

# Once #878's green phase adds the section, its extracted op sequence must
# itself satisfy the oracle -- wired here so this stays a live regression
# guard rather than only a presence check.
if [[ -n "${SKILL_WRITE_ORDER:-}" ]]; then
  reason="$(verify_refine_write_order "${SKILL_WRITE_ORDER}")"; rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    fail "refine/SKILL.md's ### Write order section does not satisfy the expected partial order: ${reason}"
  fi
fi
if [[ -n "${CODEX_WRITE_ORDER:-}" ]]; then
  reason="$(verify_refine_write_order "${CODEX_WRITE_ORDER}")"; rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    fail "refine/codex.md's Write order parity block does not satisfy the expected partial order: ${reason}"
  fi
fi

# =====================================================================
# 2d. Decline: zero gh/git calls at all in the declined branch (only
# rm -f). Extracted from step 13's "**Declined branch.**" span. This may
# already pass today (the literal span itself has no gh/git call) -- the
# overall zero-mutation intent for a decline is what 2a/2b's pre-gate
# check pins as red; this scoped check is a standing regression guard for
# the green phase, proven non-vacuous below against a deliberately broken
# COPY.
# =====================================================================

extract_declined_branch_span() {
  awk '
    /\*\*Declined branch\.\*\*/ { on=1 }
    on && /For a Confirm run/ { exit }
    on { print }
  ' "$1"
}

DECLINED_SPAN="$(extract_declined_branch_span "${REFINE_SKILL}")"
if [[ -z "${DECLINED_SPAN}" ]]; then
  fail "refine/SKILL.md: could not locate step 13's '**Declined branch.**' span -- needed to verify the decline path performs zero gh/git calls"
else
  DECLINED_CALLS="$(_extract_gh_git_lines_from_text "${DECLINED_SPAN}")"
  if [[ -n "${DECLINED_CALLS}" ]]; then
    fail "refine/SKILL.md step 13's declined branch must perform zero gh/git calls (only rm -f); found: $(printf '%s' "${DECLINED_CALLS}" | head -n1)"
  fi
fi

# Non-vacuity self-test: a deliberately broken COPY of the declined-branch
# shape (a synthetic gh call injected into it) must be caught by the same
# extraction the real-doc check above relies on.
BROKEN_DECLINED_TEXT='**Declined branch.** Reached only from the gate'"'"'s Decline option.
```bash
gh issue edit 100 --repo acme/widgets --remove-label Working
```
For a Confirm run, check the marker file.'
BROKEN_DECLINED_CALLS="$(_extract_gh_git_lines_from_text "${BROKEN_DECLINED_TEXT}")"
if [[ -z "${BROKEN_DECLINED_CALLS}" ]]; then
  fail "self-test: the declined-branch gh/git-call detector failed to catch a deliberately-injected gh call in a broken copy of the span -- detector is vacuous"
fi

# =====================================================================
# 2e. Post-confirm ownership conflict / authorization-sensitive drift /
# cosmetic drift / unreadable parent (both attempts). refine/SKILL.md
# documents the post-confirm re-verification step for all four (#878); the
# doc-anchor assertions below pin that prose as a standing regression
# guard. The plumbing each anchor depends on (classify_drift against real
# fixtures, replay_through_stub against the foreign-assignee/fail-pattern
# fixtures) is proven correct here too, independent of the doc anchors.
# =====================================================================

OWNERSHIP_RECHECK_MARKER='re-verify exclusive ownership'
assert_file_contains "${REFINE_SKILL}" "${OWNERSHIP_RECHECK_MARKER}" \
  "must re-verify exclusive ownership as the first action after the gate's Confirm, so a concurrent claim is caught before any write (#878)"

AUTH_DRIFT_STOP_MARKER='stop and ask for a fresh confirmation'
assert_file_contains "${REFINE_SKILL}" "${AUTH_DRIFT_STOP_MARKER}" \
  "must document that authorization-sensitive parent-label drift detected after Confirm stops the run and asks for a fresh confirmation before any write (#878)"

COSMETIC_DRIFT_DISCLOSURE_MARKER='cosmetic label drift'
assert_file_contains "${REFINE_SKILL}" "${COSMETIC_DRIFT_DISCLOSURE_MARKER}" \
  "must disclose cosmetic parent-label drift in the Final Message rather than silently proceeding unremarked (#878)"

UNREADABLE_PARENT_MARKER='parent cannot be read after one retry'
assert_file_contains "${REFINE_SKILL}" "${UNREADABLE_PARENT_MARKER}" \
  "must stop with zero writes when the post-confirm parent re-fetch fails both attempts (#878)"

# --- Fixture sanity: parent-foreign-assignee.json must actually record a
# different assignee than parent-baseline.json, or the ownership-conflict
# replay below would be vacuous.
BASELINE_ASSIGNEE="$(jq -r '.assignees[0].login' "${FIXTURES_DIR}/parent-baseline.json")"
FOREIGN_ASSIGNEE="$(jq -r '.assignees[0].login' "${FIXTURES_DIR}/parent-foreign-assignee.json")"
if [[ "${BASELINE_ASSIGNEE}" == "${FOREIGN_ASSIGNEE}" ]]; then
  fail "fixture bug: fixtures/parent-foreign-assignee.json must record a different assignee login than fixtures/parent-baseline.json"
fi

# --- Ownership-conflict replay: the canonical post-confirm re-fetch
# command (not yet in the doc -- this is the shape #878's green phase must
# add) driven against the foreign-assignee fixture.
OWNERSHIP_LOG="$(mktemp)" || { fail "mktemp failed for ownership-conflict replay log"; OWNERSHIP_LOG=""; }
if [[ -n "${OWNERSHIP_LOG}" ]]; then
  OWNERSHIP_RECHECK_CMD='gh issue view <number> --repo <owner>/<repo> --json assignees'
  replay_through_stub "${OWNERSHIP_RECHECK_CMD}" "${OWNERSHIP_LOG}" "${FIXTURES_DIR}/parent-foreign-assignee.json" ""
  rc=$?
  assert_ok "${rc}" "replaying the canonical post-confirm ownership re-fetch command must succeed against the gh stub"
  assert_file_contains "${OWNERSHIP_LOG}" "issue view" \
    "replay_through_stub must log the ownership re-fetch invocation"
  RETURNED_LOGIN="$(jq -r '.assignees[0].login // empty' "${OWNERSHIP_LOG}.stdout" 2>/dev/null)"
  assert_eq "${RETURNED_LOGIN}" "bob" \
    "replay_through_stub's captured stdout must carry the foreign-assignee fixture's JSON through to the caller"
  # A conflict means the claim step never ran and every subsequent write
  # op is unreachable -- verify_refine_write_order's empty-sequence
  # acceptance (Section 1c) is exactly this shape.
  verify_refine_write_order ""; rc=$?
  assert_ok "${rc}" "an ownership conflict must resolve to the empty (zero-write) op sequence, which verify_refine_write_order accepts"
  cleanup_replay_log "${OWNERSHIP_LOG}"
fi

# --- Unreadable parent (both attempts): replay two fetch attempts, both
# simulated as failing (GH_STUB_FAIL_PATTERN matches "issue view"), and
# assert exactly 2 attempts were made and none reached a write.
UNREADABLE_LOG="$(mktemp)" || { fail "mktemp failed for unreadable-parent replay log"; UNREADABLE_LOG=""; }
if [[ -n "${UNREADABLE_LOG}" ]]; then
  UNREADABLE_ATTEMPTS="gh issue view <number> --repo <owner>/<repo> --json labels,assignees
gh issue view <number> --repo <owner>/<repo> --json labels,assignees"
  replay_through_stub "${UNREADABLE_ATTEMPTS}" "${UNREADABLE_LOG}" "" "issue view"
  rc=$?
  assert_nonzero "${rc}" "replay_through_stub: both simulated parent-fetch attempts must fail (GH_STUB_FAIL_PATTERN='issue view')"
  attempt_count="$(grep -c . "${UNREADABLE_LOG}" 2>/dev/null || echo 0)"
  assert_eq "${attempt_count}" "2" \
    "replay_through_stub: expected exactly 2 logged fetch attempts (the documented one-retry protocol)"
  while IFS= read -r logged; do
    [[ -z "${logged}" ]] && continue
    cls="$(classify_gh_call "gh ${logged}")"
    if [[ "${cls}" == "write" || "${cls}" == "unknown" ]]; then
      fail "an unreadable-parent scenario must never reach a write- or unknown-classified call, but logged: gh ${logged}"
    fi
  done < "${UNREADABLE_LOG}"
  cleanup_replay_log "${UNREADABLE_LOG}"
fi

echo "refine-write-order.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
