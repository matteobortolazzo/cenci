#!/usr/bin/env bash
# Fixture-driven behavioral-parity acceptance harness for ticket #524 (child of
# #517). Proves that the Claude Code and Codex client adapters exercise the
# same safety-critical implement-pipeline properties, via deterministic
# control points rather than live LLM runs. See flow/docs/adapter-contract.md
# for the full property table this harness checks.
#
# Implemented (green) state, three sections:
#   A. Good/bad behavioral simulations per property, driven through the real
#      hooks/scripts/*.sh and codex/checkpoint.mjs (or, for the two
#      no-backing-script properties, a harness-defined deterministic model)
#      via contract-lib.sh's drive_*/verify_* functions.
#   B. Synthetic good-adapter + per-property broken-adapter fixture
#      self-tests (flow/tests/parity/fixtures/), the harness's own
#      regression guard against vacuous checks -- each bad-* fixture must
#      fail for its specific, isolated reason while every other property
#      still passes; bad-wrong-order additionally guards the ordering check.
#   C. Static checks against the REAL committed Claude Code and Codex
#      adapter docs -- both adapters must pass all 8 properties.
#
# Follows the established flow test idiom (flow/tests/maintain.test.sh,
# flow/hooks/scripts/run-gate.test.sh, flow/tests/subagent-cwd-contract.test.sh):
# `set -uo pipefail`, mktemp -d fixtures, a `failures=` counter, small assert_*
# helpers, self-contained, auto-discovered by flow's gateCommand
# (`find . -name '*.test.sh' -print0 | sort -z | xargs -0 -r -n1 bash`, run from
# the flow/ project directory -- see .cenci/config.json).
#
# Read-only sources of truth this harness inspects/drives but never edits:
# flow/skills/implement/**, flow/hooks/scripts/**, flow/codex/**.
#
# OpenCode is explicitly OUT OF SCOPE here -- no real OpenCode implement
# adapter exists yet; deferred to #517's OpenCode child slice.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "parity.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)" || { echo "parity.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "parity.test.sh: failed to resolve repo root." >&2; exit 2; }
FIXTURES_DIR="${SCRIPT_DIR}/fixtures"
failures=0

# shellcheck source=./contract-lib.sh
if ! source "${SCRIPT_DIR}/contract-lib.sh"; then
  echo "parity.test.sh: failed to source contract-lib.sh" >&2
  exit 2
fi

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_ne() { [[ "$1" != "$2" ]] || fail "$3: expected NOT [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain: $2 (actual: $1)"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "$3: expected output NOT to contain: $2 (actual: $1)"; }
assert_exit_zero() { [[ "$1" -eq 0 ]] || fail "$2: expected exit 0, got $1"; }
assert_exit_nonzero() { [[ "$1" -ne 0 ]] || fail "$2: expected non-zero exit, got 0"; }

# get_prop_line <checker-output> <property-id> -- the "<property-id>:<status>..."
# line contract-lib's per-property checkers print, or empty if absent.
get_prop_line() { printf '%s\n' "$1" | grep -E "^$2:" | head -n1; }

# assert_prop_status <checker-output> <property-id> <expected-status> <label>
assert_prop_status() {
  local out="$1" prop="$2" expected="$3" label="$4" line
  line="$(get_prop_line "${out}" "${prop}")"
  [[ "${line}" == "${prop}:${expected}"* ]] || fail "${label}: expected property '${prop}' status '${expected}', got line [${line:-<absent>}]"
}

# ===========================================================================
# Header: commit SHA, plugin version, config fingerprint of the adapter/plugin
# under test -- stdout only, no persisted artifact file.
# ===========================================================================
echo "=== parity.test.sh run header ==="
print_run_header "${FLOW_DIR}"
echo "=================================="

# ===========================================================================
# Section A: behavioral good/bad simulations per property -- synthetic
# pass/fail inputs driven through the real scripts (or, for the two
# no-backing-script properties, a harness-defined deterministic model).
# ===========================================================================

# --- P1 baseline-gate (hooks/scripts/run-gate.sh) -------------------------
ROOT_P1="$(mktemp -d)" || { echo "parity.test.sh: mktemp failed (P1 good sim)" >&2; exit 2; }
mkdir -p "${ROOT_P1}/.cenci"
printf '{"gateCommand":"true"}' > "${ROOT_P1}/.cenci/config.json"
status_p1_good="$(drive_baseline_gate "${ROOT_P1}")"
assert_eq "${status_p1_good}" "green" "P1 baseline-gate good sim (gateCommand true)"
rm -rf "${ROOT_P1}"

ROOT_P1B="$(mktemp -d)" || { echo "parity.test.sh: mktemp failed (P1 bad sim)" >&2; exit 2; }
mkdir -p "${ROOT_P1B}/.cenci"
printf '{"gateCommand":"false"}' > "${ROOT_P1B}/.cenci/config.json"
status_p1_bad="$(drive_baseline_gate "${ROOT_P1B}")"
assert_eq "${status_p1_bad}" "red" "P1 baseline-gate bad sim (gateCommand false)"
rm -rf "${ROOT_P1B}"

# --- P2 worktree-isolation (hooks/scripts/guard-main-worktree.sh) ---------
# GENUINE TEST BUG FIX (green phase): a plain `mktemp -d` root lands under
# /tmp (Linux) or /var/folders (macOS) -- both of which guard-main-worktree.sh
# unconditionally allowlists as legitimate temp-scratch paths, regardless of
# where inside them a write lands. Rooting this fixture there would make the
# "bad sim" (write inside main worktree) assertion below structurally
# unsatisfiable on every platform, not just fail to model the scenario: ANY
# path under a plain mktemp root is auto-allowed before the .worktrees/-vs-
# main-worktree distinction is ever evaluated. /var/tmp is not allowlisted --
# same fix flow/hooks/scripts/guard-main-worktree.test.sh already applies.
ROOT_P2="$(mktemp -d /var/tmp/parity-worktree-isolation.XXXXXX)" || { echo "parity.test.sh: mktemp failed (P2)" >&2; exit 2; }
git -C "${ROOT_P2}" init -q
mkdir -p "${ROOT_P2}/.cenci" "${ROOT_P2}/.worktrees/42-demo/src"
printf '{"isMonorepo":false}' > "${ROOT_P2}/.cenci/config.json"
result_p2_good="$(drive_worktree_guard "${ROOT_P2}" "${ROOT_P2}/.worktrees/42-demo/src/app.go")"
assert_eq "${result_p2_good}" "allowed" "P2 worktree-isolation good sim (write inside .worktrees/)"

result_p2_bad="$(drive_worktree_guard "${ROOT_P2}" "${ROOT_P2}/src/app.go")"
assert_eq "${result_p2_bad}" "blocked" "P2 worktree-isolation bad sim (write inside main worktree)"
assert_ne "${result_p2_bad}" "error" "P2 worktree-isolation bad sim must report a genuine block, not an infra error"
rm -rf "${ROOT_P2}"

# --- P3 red-before-green (no backing script: event-sequence model) -------
result_p3_good="$(verify_red_before_green "red green")"; code_p3_good=$?
assert_exit_zero "${code_p3_good}" "P3 red-before-green good sim ([red,green])"
result_p3_bad="$(verify_red_before_green "green")"; code_p3_bad=$?
assert_exit_nonzero "${code_p3_bad}" "P3 red-before-green bad sim ([green] alone)"

# --- P4 gate-result-integrity (run-gate.sh output + interpretation model) -
result_p4_good="$(verify_gate_interpretation "GATE_STATUS=red" "1" "fail")"; code_p4_good=$?
assert_exit_zero "${code_p4_good}" "P4 gate-result-integrity good sim (red correctly reported as fail)"
result_p4_bad="$(verify_gate_interpretation "GATE_STATUS=red" "1" "pass")"; code_p4_bad=$?
assert_exit_nonzero "${code_p4_bad}" "P4 gate-result-integrity bad sim (red misreported as pass)"

# --- P5 planning-immutability (guard-main-worktree.sh, planning scenario) -
# Same genuine test bug fix as P2 above: root outside the guard's temp-path
# allowlist so the "bad sim" (planning session writes a source file) is
# actually exercised instead of vacuously auto-allowed.
ROOT_P5="$(mktemp -d /var/tmp/parity-planning-immutability.XXXXXX)" || { echo "parity.test.sh: mktemp failed (P5)" >&2; exit 2; }
git -C "${ROOT_P5}" init -q
mkdir -p "${ROOT_P5}/.cenci" "${ROOT_P5}/.plans"
printf '{"isMonorepo":false}' > "${ROOT_P5}/.cenci/config.json"
result_p5_good="$(drive_worktree_guard "${ROOT_P5}" "${ROOT_P5}/.plans/42-demo.md")"
assert_eq "${result_p5_good}" "allowed" "P5 planning-immutability good sim (write to .plans/)"

result_p5_bad="$(drive_worktree_guard "${ROOT_P5}" "${ROOT_P5}/src/app.go")"
assert_eq "${result_p5_bad}" "blocked" "P5 planning-immutability bad sim (planning session writes a source file)"
assert_ne "${result_p5_bad}" "error" "P5 planning-immutability bad sim must report a genuine block, not an infra error"
rm -rf "${ROOT_P5}"

# --- P6 sensitive-file-refusal (hooks/scripts/check-sensitive-files.sh) ---
result_p6_good="$(drive_sensitive_files_guard "/tmp/parity-demo/src/app.go")"
assert_eq "${result_p6_good}" "allowed" "P6 sensitive-file-refusal good sim (ordinary source file)"
result_p6_bad="$(drive_sensitive_files_guard "/tmp/parity-demo/.env")"
assert_eq "${result_p6_bad}" "blocked" "P6 sensitive-file-refusal bad sim (.env write)"
assert_ne "${result_p6_bad}" "error" "P6 sensitive-file-refusal bad sim must report a genuine block, not an infra error"

# --- P7 verification-locality (codex/checkpoint.mjs worktree identity) ---
TMPROOT_P7="$(mktemp -d)" || { echo "parity.test.sh: mktemp failed (P7)" >&2; exit 2; }
CKPT_P7="${TMPROOT_P7}/.cenci/checkpoints/implement-42-demo.json"
drive_checkpoint init "${CKPT_P7}" implement "42-demo" >/dev/null
result_p7_good="$(verify_worktree_match "${CKPT_P7}" "/repo/.worktrees/42-demo")"; code_p7_good=$?
assert_exit_zero "${code_p7_good}" "P7 verification-locality good sim (verify path matches recorded worktree)"
result_p7_bad="$(verify_worktree_match "${CKPT_P7}" "/repo")"; code_p7_bad=$?
assert_exit_nonzero "${code_p7_bad}" "P7 verification-locality bad sim (verify path is outside recorded worktree)"
rm -rf "${TMPROOT_P7}"

# --- P8 push-policy (no backing script: command-string model) ------------
result_p8_good1="$(verify_push_policy "git push -u origin feature/42-demo")"; code_p8_good1=$?
assert_exit_zero "${code_p8_good1}" "P8 push-policy good sim (plain git push)"
result_p8_good2="$(verify_push_policy "git push --force-with-lease -u origin feature/42-demo")"; code_p8_good2=$?
assert_exit_zero "${code_p8_good2}" "P8 push-policy good sim (--force-with-lease)"
result_p8_bad1="$(verify_push_policy "git push --force -u origin feature/42-demo")"; code_p8_bad1=$?
assert_exit_nonzero "${code_p8_bad1}" "P8 push-policy bad sim (bare --force)"
result_p8_bad2="$(verify_push_policy "git push -f -u origin feature/42-demo")"; code_p8_bad2=$?
assert_exit_nonzero "${code_p8_bad2}" "P8 push-policy bad sim (bare -f)"
result_p8_bad3="$(verify_push_policy "git commit --no-verify -m demo")"; code_p8_bad3=$?
assert_exit_nonzero "${code_p8_bad3}" "P8 push-policy bad sim (--no-verify)"

# ===========================================================================
# Section B: synthetic good-adapter + per-property broken-adapter fixture
# self-tests -- the harness's own regression guard against vacuous checks.
# One broken fixture per property; each must fail for its specific reason,
# while the good fixture passes cleanly on every property.
# ===========================================================================

good_out="$(check_synthetic_adapter "${FIXTURES_DIR}/good-adapter")"; good_code=$?
assert_exit_zero "${good_code}" "good-adapter fixture: overall checker result"
for prop in baseline-gate worktree-isolation red-before-green gate-result-integrity \
            planning-immutability sensitive-file-refusal verification-locality push-policy; do
  assert_prop_status "${good_out}" "${prop}" "pass" "good-adapter fixture: ${prop}"
done

check_broken_fixture() {
  local fixture="$1" prop="$2" label="$3"
  local out code other
  out="$(check_synthetic_adapter "${FIXTURES_DIR}/${fixture}")"; code=$?
  assert_exit_nonzero "${code}" "${label}: overall checker result must fail"
  assert_prop_status "${out}" "${prop}" "fail" "${label}: ${prop} must fail"
  # The breakage must be ISOLATED to the one targeted property -- every
  # other property must still report pass, so a broken fixture can never be
  # mistaken for evidence the checker fails vacuously/globally.
  for other in baseline-gate worktree-isolation red-before-green gate-result-integrity \
               planning-immutability sensitive-file-refusal verification-locality push-policy; do
    [[ "${other}" == "${prop}" ]] && continue
    assert_prop_status "${out}" "${other}" "pass" "${label}: ${other} must remain pass (breakage must be isolated to ${prop})"
  done
}

check_broken_fixture "bad-omit-gate" "baseline-gate" "bad-omit-gate fixture (baseline gate omitted)"
check_broken_fixture "bad-wrong-worktree" "worktree-isolation" "bad-wrong-worktree fixture (wrong worktree used)"
check_broken_fixture "bad-green-no-red" "red-before-green" "bad-green-no-red fixture (green without observed prior red)"
check_broken_fixture "bad-probe-as-success" "gate-result-integrity" "bad-probe-as-success fixture (failed probe treated as success)"
check_broken_fixture "bad-planning-write" "planning-immutability" "bad-planning-write fixture (planning session writes a source file)"
check_broken_fixture "bad-sensitive-write" "sensitive-file-refusal" "bad-sensitive-write fixture (sensitive file written without refusal)"
check_broken_fixture "bad-verify-elsewhere" "verification-locality" "bad-verify-elsewhere fixture (verification ran outside the assigned worktree)"
check_broken_fixture "bad-force-push" "push-policy" "bad-force-push fixture (force-push / gate bypass)"

# --- Ordering-violation self-test -----------------------------------------
# bad-wrong-order/procedure.md has every one of the 8 anchor markers present
# (unlike the presence-violation fixtures above) but sections 7 and 8
# physically swapped, so this specifically exercises the ordering check
# added to check_synthetic_adapter -- proving a fixture that passes on pure
# presence still fails when its properties appear out of registry order.
order_out="$(check_synthetic_adapter "${FIXTURES_DIR}/bad-wrong-order")"; order_code=$?
assert_exit_nonzero "${order_code}" "bad-wrong-order fixture: overall checker result must fail"
order_pushpolicy_line="$(get_prop_line "${order_out}" "push-policy")"
assert_prop_status "${order_out}" "push-policy" "fail" "bad-wrong-order fixture: push-policy must fail"
assert_contains "${order_pushpolicy_line}" "out of order" \
  "bad-wrong-order fixture: push-policy must fail specifically due to ORDERING (its anchor is present, just misplaced), not a presence/omission reason"
for prop in baseline-gate worktree-isolation red-before-green gate-result-integrity \
            planning-immutability sensitive-file-refusal verification-locality; do
  assert_prop_status "${order_out}" "${prop}" "pass" "bad-wrong-order fixture: ${prop} must remain pass"
done

# ===========================================================================
# Self-test: the xfail() primitive itself (both branches). Isolated from the
# real property checks above and below -- it proves xfail() flips correctly
# in BOTH directions, independent of which real check currently happens to
# be xfail'd. Uses plain assert_* helpers, so it adds to `failures` only if
# xfail() itself misbehaves; it never touches the real per-property counts.
# ===========================================================================

xpass_self_out="$(xfail "self-test: xfail() fed a passing check" 0 "self-test, not a real gap")"; xpass_self_code=$?
assert_contains "${xpass_self_out}" "XPASS" "xfail() self-test: a passing check (code 0) must report XPASS"
assert_exit_nonzero "${xpass_self_code}" "xfail() self-test: a passing check (code 0) must return non-zero (the caller must count it as a failure)"

xfail_self_out="$(xfail "self-test: xfail() fed a failing check" 1 "self-test, not a real gap")"; xfail_self_code=$?
assert_contains "${xfail_self_out}" "XFAIL" "xfail() self-test: a failing check (code 1) must report XFAIL"
assert_exit_zero "${xfail_self_code}" "xfail() self-test: a failing check (code 1) must return zero (the caller must NOT count it as a failure)"

# ===========================================================================
# Section C: static checks against the REAL Claude Code and Codex adapters
# (not only hand-crafted synthetic fixtures). Both adapters must pass all 8
# properties -- #555 wired run-gate.sh/GATE_STATUS into codex.md, closing the
# baseline-gate/gate-result-integrity gap tracked by #517's "Codex implement
# gate parity" child slice.
# ===========================================================================

claude_out="$(check_claude_adapter "${FLOW_DIR}")"; claude_code=$?
assert_exit_zero "${claude_code}" "real Claude adapter: overall checker result"
for prop in baseline-gate worktree-isolation red-before-green gate-result-integrity \
            planning-immutability sensitive-file-refusal verification-locality push-policy; do
  assert_prop_status "${claude_out}" "${prop}" "pass" "real Claude adapter: ${prop}"
done

# check_codex_adapter's own exit code IS captured (mirroring claude_code
# above) and asserted zero -- codex.md now invokes run-gate.sh and interprets
# GATE_STATUS, so all 8 properties must pass, matching the Claude loop above.
codex_out="$(check_codex_adapter "${FLOW_DIR}")"; codex_code=$?
assert_exit_zero "${codex_code}" "real Codex adapter: overall checker result"
for prop in baseline-gate worktree-isolation red-before-green gate-result-integrity \
            planning-immutability sensitive-file-refusal verification-locality push-policy; do
  assert_prop_status "${codex_out}" "${prop}" "pass" "real Codex adapter: ${prop}"
done

# --- Real-adapter ordering-violation self-test ------------------------------
# check_claude_adapter/check_codex_adapter's worktree-isolation property now
# checks ORDER, not just presence, against the real committed docs (not only
# check_synthetic_adapter's hand-crafted fixtures) -- e.g. the real
# phase-2-worktree.md must create the worktree BEFORE it invokes the
# baseline gate, which itself must run BEFORE its GATE_STATUS is
# interpreted. Both real adapters' docs already satisfy this order today (as
# asserted above via the plain worktree-isolation:pass checks), so that alone
# can't prove the ordering CHECK ITSELF actually rejects a reordering rather
# than passing vacuously. This proves it, using _markers_strictly_increasing
# -- the exact helper both checks call -- fed a deliberately-reordered COPY
# of each real doc's anchor text, never the real files (which stay untouched
# and correctly ordered).
claude_good_order_copy="intro ${_CLAUDE_WT_MARKER} middle ${_CLAUDE_GATE_MARKER} later ${_CLAUDE_STATUS_MARKER} end"
if ! _markers_strictly_increasing "${claude_good_order_copy}" \
    "${_CLAUDE_WT_MARKER}" "${_CLAUDE_GATE_MARKER}" "${_CLAUDE_STATUS_MARKER}"; then
  fail "Claude ordering self-test: a correctly-ordered synthetic copy must pass _markers_strictly_increasing"
fi

claude_bad_order_copy="intro ${_CLAUDE_STATUS_MARKER} middle ${_CLAUDE_GATE_MARKER} later ${_CLAUDE_WT_MARKER} end"
if _markers_strictly_increasing "${claude_bad_order_copy}" \
    "${_CLAUDE_WT_MARKER}" "${_CLAUDE_GATE_MARKER}" "${_CLAUDE_STATUS_MARKER}"; then
  fail "Claude ordering self-test: a deliberately-reordered synthetic copy must be REJECTED by _markers_strictly_increasing, but it passed"
fi

codex_good_order_copy="intro ${_CODEX_STOP_MARKER} later ${_CODEX_CREATE_MARKER} end"
if ! _markers_strictly_increasing "${codex_good_order_copy}" "${_CODEX_STOP_MARKER}" "${_CODEX_CREATE_MARKER}"; then
  fail "Codex ordering self-test: a correctly-ordered synthetic copy must pass _markers_strictly_increasing"
fi

codex_bad_order_copy="intro ${_CODEX_CREATE_MARKER} later ${_CODEX_STOP_MARKER} end"
if _markers_strictly_increasing "${codex_bad_order_copy}" "${_CODEX_STOP_MARKER}" "${_CODEX_CREATE_MARKER}"; then
  fail "Codex ordering self-test: a deliberately-reordered synthetic copy must be REJECTED by _markers_strictly_increasing, but it passed"
fi

# --- _contains_ws_insensitive self-test (#556) ------------------------------
# check_codex_adapter's P8 push-policy check requires the exact contiguous
# sentence "Never force-push or bypass security/design/approval gates.", but
# flow/skills/implement/codex.md wraps that sentence across a Markdown line
# break -- an ordinary substring match can never span the wrap. This proves
# the whitespace-insensitive helper the fix will route P8 through: spaces,
# tabs, and newlines between words must be fungible, but the full required
# word sequence must still be mandatory (a dropped word must still fail).
_PUSH_POLICY_SENTENCE="Never force-push or bypass security/design/approval gates."

# 1. A deliberately Markdown-wrapped, but complete, copy of the sentence
#    (mirrors the real wrap in codex.md, between "bypass" and "security")
#    must still be found -- this is the regression case for the reported bug.
codex_wrapped_copy="Never force-push or bypass
security/design/approval gates."
if ! _contains_ws_insensitive "${codex_wrapped_copy}" "${_PUSH_POLICY_SENTENCE}"; then
  fail "_contains_ws_insensitive self-test: a Markdown-wrapped but complete copy of the push-policy sentence must be found (regression for #556), but it was not"
fi

# 2. A weakened copy (a required word, "force-push", dropped) must still
#    fail -- proves whitespace-insensitivity never widens into
#    word-insensitivity; the full required word sequence stays mandatory.
codex_weakened_copy="Never or bypass
security/design/approval gates."
if _contains_ws_insensitive "${codex_weakened_copy}" "${_PUSH_POLICY_SENTENCE}"; then
  fail "_contains_ws_insensitive self-test: a weakened copy of the push-policy sentence (missing 'force-push') must NOT be found, but it was"
fi

# 3. Extra whitespace and multiple newlines between words must still match --
#    proves whitespace runs of any length/kind collapse equivalently, not
#    just a single line-wrap newline.
codex_extra_ws_copy="Never   force-push  or

bypass
   security/design/approval    gates."
if ! _contains_ws_insensitive "${codex_extra_ws_copy}" "${_PUSH_POLICY_SENTENCE}"; then
  fail "_contains_ws_insensitive self-test: extra whitespace/multi-newline variant of the push-policy sentence must still be found, but it was not"
fi

# 4. An empty phrase must NOT match -- an empty needle would otherwise
#    vacuously match any (or even empty) content via the substring glob,
#    silently masking a caller bug (unset/empty required phrase) as a pass.
if _contains_ws_insensitive "${codex_wrapped_copy}" ""; then
  fail "_contains_ws_insensitive self-test: an empty phrase must NOT vacuously match, but it did"
fi

echo "parity.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
