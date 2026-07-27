#!/usr/bin/env bash
# implement-codex-gate.test.sh — dedicated integration test for ticket #555
# (child of #517), which wires the baseline health gate into the Codex
# implement adapter (flow/skills/implement/codex.md).
#
# Three behavioral sections, driven directly through the real scripts (never
# a live LLM run), plus a procedure-text section against the real committed
# codex.md:
#   1. success  — hooks/scripts/run-gate.sh reports GATE_STATUS=green for a
#      green gateCommand.
#   2. failure  — a red gateCommand reports GATE_STATUS=red with a non-zero
#      exit; an unset gateCommand config reports GATE_STATUS=unset (no-op).
#   3. recovery — codex/checkpoint.mjs's `block` command sets
#      status: "needs-input", preserves the `worktree`/`planPath` fields
#      already recorded on the checkpoint, and leaves `lastCompletedGate`
#      untouched.
#   4. procedure-text — flow/skills/implement/codex.md documents the
#      run-gate.sh invocation, GATE_STATUS interpretation, the stop
#      procedure (checkpoint.mjs block + goal-clear), and worktree/branch
#      retention on failure (see docs/plan-fidelity.md and #555's Files to
#      Modify).
#
# Follows the established flow test idiom (flow/tests/parity/parity.test.sh,
# flow/codex/runtime.test.sh): `set -uo pipefail`, mktemp -d fixtures, a
# `failures=` counter, small assert_* helpers, self-contained, auto-discovered
# by flow's shared executor (`flow/scripts/run-checks.sh`, invoked by both
# CI's flow-test job and the flow gateCommand).
#
# Doc assertions use stable literal phrases anchored at the new gate step's
# expected content, never a generic marker that might already exist
# elsewhere in codex.md for an unrelated reason — see
# docs/shell-scripting-gotchas.md's grep-based-contract-test rule.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-codex-gate.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-codex-gate.test.sh: failed to resolve flow directory." >&2; exit 2; }
RUN_GATE="${FLOW_DIR}/hooks/scripts/run-gate.sh"
CHECKPOINT_MJS="${FLOW_DIR}/codex/checkpoint.mjs"
CODEX_MD="${FLOW_DIR}/skills/implement/codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain: $2 (actual: $1)"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "$3: expected output NOT to contain: $2 (actual: $1)"; }
assert_exit_zero() { [[ "$1" -eq 0 ]] || fail "$2: expected exit 0, got $1"; }
assert_exit_nonzero() { [[ "$1" -ne 0 ]] || fail "$2: expected non-zero exit, got 0"; }

# ===========================================================================
# 1. success — run-gate.sh with a green gateCommand.
# ===========================================================================
ROOT_GREEN="$(mktemp -d)" || { echo "implement-codex-gate.test.sh: mktemp failed (success)" >&2; exit 2; }
mkdir -p "${ROOT_GREEN}/.cenci"
printf '{"gateCommand":"true"}' > "${ROOT_GREEN}/.cenci/config.json"
out_green="$(cd "${ROOT_GREEN}" && sh "${RUN_GATE}")"; code_green=$?
assert_exit_zero "${code_green}" "success: run-gate.sh with a green gateCommand must exit 0"
assert_contains "${out_green}" "GATE_STATUS=green" "success: run-gate.sh with a green gateCommand must print GATE_STATUS=green"
rm -rf "${ROOT_GREEN}"

# ===========================================================================
# 2. failure — a red gateCommand, and an unset gateCommand config (no-op).
# ===========================================================================
ROOT_RED="$(mktemp -d)" || { echo "implement-codex-gate.test.sh: mktemp failed (failure/red)" >&2; exit 2; }
mkdir -p "${ROOT_RED}/.cenci"
printf '{"gateCommand":"false"}' > "${ROOT_RED}/.cenci/config.json"
out_red="$(cd "${ROOT_RED}" && sh "${RUN_GATE}")"; code_red=$?
assert_exit_nonzero "${code_red}" "failure: run-gate.sh with a red gateCommand must exit non-zero"
assert_contains "${out_red}" "GATE_STATUS=red" "failure: run-gate.sh with a red gateCommand must print GATE_STATUS=red"
rm -rf "${ROOT_RED}"

ROOT_UNSET="$(mktemp -d)" || { echo "implement-codex-gate.test.sh: mktemp failed (failure/unset)" >&2; exit 2; }
out_unset="$(cd "${ROOT_UNSET}" && sh "${RUN_GATE}")"; code_unset=$?
assert_exit_zero "${code_unset}" "failure: run-gate.sh with no .cenci/config.json (unset) must exit 0 (no-op)"
assert_contains "${out_unset}" "GATE_STATUS=unset" "failure: run-gate.sh with no .cenci/config.json (unset) must print GATE_STATUS=unset"
rm -rf "${ROOT_UNSET}"

# ===========================================================================
# 3. recovery — checkpoint.mjs block sets status: "needs-input", preserves
#    worktree/planPath, and leaves lastCompletedGate untouched.
# ===========================================================================
ROOT_CKPT="$(mktemp -d)" || { echo "implement-codex-gate.test.sh: mktemp failed (recovery)" >&2; exit 2; }
CKPT="${ROOT_CKPT}/.cenci/checkpoints/implement-555-demo.json"
node "${CHECKPOINT_MJS}" init "${CKPT}" implement 555-demo planned >/dev/null

# Simulate a checkpoint that has already progressed past worktree creation
# (worktree/planPath/lastCompletedGate populated) -- checkpoint.mjs itself
# has no command that sets these fields directly, so this fixture writes
# them onto the checkpoint file the same way a real advance would have left
# them, purely to observe whether `block` preserves them.
tmp_ckpt="$(mktemp)" || { echo "implement-codex-gate.test.sh: mktemp failed (recovery fixture)" >&2; exit 2; }
jq '.worktree = "/repo/.worktrees/555-demo" | .planPath = ".plans/555-demo.md" | .lastCompletedGate = "flow"' \
  "${CKPT}" > "${tmp_ckpt}" && mv "${tmp_ckpt}" "${CKPT}"

node "${CHECKPOINT_MJS}" block "${CKPT}" implement 555-demo review >/dev/null

status_after="$(jq -r '.status' "${CKPT}")"
assert_eq "${status_after}" "needs-input" "recovery: checkpoint.mjs block must set status to needs-input"

worktree_after="$(jq -r '.worktree' "${CKPT}")"
assert_eq "${worktree_after}" "/repo/.worktrees/555-demo" "recovery: checkpoint.mjs block must preserve the worktree field"

planpath_after="$(jq -r '.planPath' "${CKPT}")"
assert_eq "${planpath_after}" ".plans/555-demo.md" "recovery: checkpoint.mjs block must preserve the planPath field"

lastgate_after="$(jq -r '.lastCompletedGate' "${CKPT}")"
assert_eq "${lastgate_after}" "flow" "recovery: checkpoint.mjs block must leave lastCompletedGate untouched"
rm -rf "${ROOT_CKPT}"

# ===========================================================================
# 4. procedure-text — flow/skills/implement/codex.md documents the gate step
# #555 wires in (see docs/plan-fidelity.md).
# ===========================================================================
codex_content="$(cat "${CODEX_MD}" 2>/dev/null)" || { echo "implement-codex-gate.test.sh: failed to read ${CODEX_MD}" >&2; exit 2; }

assert_contains "${codex_content}" 'sh "${PLUGIN_ROOT}/hooks/scripts/run-gate.sh"' \
  "procedure-text: codex.md must invoke run-gate.sh via \${PLUGIN_ROOT} (not \${CLAUDE_PLUGIN_ROOT})"

assert_contains "${codex_content}" '`GATE_STATUS=green` or `GATE_STATUS=unset` → proceed' \
  "procedure-text: codex.md must document that GATE_STATUS=green/unset proceeds"

assert_contains "${codex_content}" '`GATE_STATUS=red` → stop as **gate failed**' \
  "procedure-text: codex.md must document GATE_STATUS=red as the distinct 'gate failed' stop condition"

assert_contains "${codex_content}" 'non-zero exit with no `GATE_STATUS=` line → stop as **gate could not' \
  "procedure-text: codex.md must document a non-zero exit with no GATE_STATUS= line as the distinct 'gate could not run' stop condition"

assert_contains "${codex_content}" 'run `checkpoint.mjs block`, clear the goal, retain the worktree and branch' \
  "procedure-text: codex.md must document the stop procedure -- checkpoint.mjs block, clearing the goal, and retaining the worktree/branch for recovery"

assert_contains "${codex_content}" 're-run `cenci run implement apply`' \
  "procedure-text: codex.md must tell the user to fix the baseline gate and re-run cenci run implement apply"

# Wrong plugin-root var: ${CLAUDE_PLUGIN_ROOT} (the Claude-only var) would make
# the run-gate.sh path unresolvable at Codex runtime -- codex.md must only use
# ${PLUGIN_ROOT}.
assert_not_contains "${codex_content}" '${CLAUDE_PLUGIN_ROOT}' \
  "procedure-text: codex.md must never reference \${CLAUDE_PLUGIN_ROOT} (Claude-only var, unresolvable at Codex runtime)"

echo "implement-codex-gate.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
