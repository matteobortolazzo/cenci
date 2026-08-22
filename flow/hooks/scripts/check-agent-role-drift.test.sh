#!/usr/bin/env bash
# check-agent-role-drift.test.sh — runtime behavior of the SessionStart
# agent-role-drift advisory (#1040). Modelled on
# check-config-staleness.test.sh: a `failures=` counter, small assert_*
# helpers, self-contained, auto-discovered by the flow gate's `*.test.sh`
# glob.
#
# Contract pinned here: advisory-only (always exit 0), silent in hook mode
# unless .codex/agents/ exists and fails schema validation; `--plain` prints
# one machine-readable line for check.sh's agent-roles check. A missing
# .codex/agents/ directory and a clean, schema-valid set both stay silent.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK="${SCRIPT_DIR}/check-agent-role-drift.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain [$2], got [$1]"; }

make_fixture() { ROOT="$(mktemp -d)"; }
cleanup_fixture() { rm -rf "${ROOT}"; }

run_hook() {  # run_hook [--plain] — from ROOT
  OUT="$(cd "${ROOT}" && bash "${HOOK}" "$@" 2>&1)"
  CODE=$?
}

echo "check-agent-role-drift.test.sh"

# --- Case 1: no .codex/agents/ → silent, exit 0; plain says "absent" ------
make_fixture
run_hook
assert_eq "${CODE}" "0" "case1 hook exit"
assert_eq "${OUT}" "" "case1 hook silent"
run_hook --plain
assert_eq "${OUT}" "absent" "case1 plain state"
assert_eq "${CODE}" "0" "case1 plain exit"
cleanup_fixture

# --- Case 2: clean, schema-valid set → silent; plain "clean" --------------
make_fixture
mkdir -p "${ROOT}/.codex/agents"
cat > "${ROOT}/.codex/agents/planner.toml" <<'EOF'
name = "planner"
description = "A test role."
developer_instructions = "Do the thing."
EOF
run_hook
assert_eq "${CODE}" "0" "case2 hook exit"
assert_eq "${OUT}" "" "case2 hook silent"
run_hook --plain
assert_eq "${OUT}" "clean" "case2 plain state"
cleanup_fixture

# --- Case 3 (#409 shape): missing description → drift advisory ------------
make_fixture
mkdir -p "${ROOT}/.codex/agents"
cat > "${ROOT}/.codex/agents/planner.toml" <<'EOF'
name = "planner"
developer_instructions = "Do the thing."
EOF
run_hook
assert_eq "${CODE}" "0" "case3 hook exit"
echo "${OUT}" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null 2>&1 \
  || fail "case3 hook output must be SessionStart hookSpecificOutput JSON (got: ${OUT})"
CTX="$(echo "${OUT}" | jq -r '.hookSpecificOutput.additionalContext')"
assert_contains "${CTX}" "description" "case3 advisory names the missing field"
assert_contains "${CTX}" "install-agents.sh" "case3 advisory explains why drift persists"
run_hook --plain
assert_contains "${OUT}" "drift missing-field" "case3 plain state"
assert_contains "${OUT}" "'description'" "case3 plain state names the field"
cleanup_fixture

# --- Case 4 (#422 shape): missing name → drift advisory --------------------
make_fixture
mkdir -p "${ROOT}/.codex/agents"
cat > "${ROOT}/.codex/agents/planner.toml" <<'EOF'
description = "A test role."
developer_instructions = "Do the thing."
EOF
run_hook --plain
assert_contains "${OUT}" "drift missing-field" "case4 plain state"
assert_contains "${OUT}" "'name'" "case4 plain state names the field"
cleanup_fixture

# --- Case 5: never blocks — exit 0 even on drift, both modes --------------
make_fixture
mkdir -p "${ROOT}/.codex/agents"
printf 'not = "valid"\n' > "${ROOT}/.codex/agents/planner.toml"
run_hook
assert_eq "${CODE}" "0" "case5 hook exit stays 0 on drift"
run_hook --plain
assert_eq "${CODE}" "0" "case5 plain exit stays 0 on drift"
cleanup_fixture

echo "check-agent-role-drift.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
