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
# .codex/agents/ directory, a clean set, and a notes-only (unverified) set all
# stay silent; drift and a missing Python 3.11+ prerequisite (#1191) each get
# their own advisory. Every state is covered in both modes (cases 1-13).
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

BASH_BIN="$(command -v bash)" || { echo "FAIL: bash not on PATH" >&2; exit 1; }

# run_hook_with <path> <hook-script> [--plain] — from ROOT, with PATH replaced.
run_hook_with() {
  local path="$1" script="$2"; shift 2
  OUT="$(cd "${ROOT}" && PATH="${path}" "${BASH_BIN}" "${script}" "$@" 2>&1)"
  CODE=$?
}

# stub_python_dir — prints a dir holding a python3 that always fails, so the
# validator finds a python3 first on PATH that cannot import tomllib.
stub_python_dir() {
  local d; d="$(mktemp -d)" || return 1
  printf '#!/bin/sh\nexit 1\n' > "${d}/python3"
  chmod +x "${d}/python3"
  printf '%s' "${d}"
}

# no_python_dir — prints a dir of symlinks to only the tools the hook and the
# validator need before python3 is probed, so no host python3 can be found.
no_python_dir() {
  local d t src; d="$(mktemp -d)" || return 1
  for t in bash dirname tr sed jq find sort; do
    src="$(command -v "$t")" || { echo "FAIL: host lacks $t" >&2; return 1; }
    ln -s "${src}" "${d}/${t}"
  done
  printf '%s' "${d}"
}

# fake_tree <validator-body> — builds a copy of the hook beside a fake
# validator (body is the shell text after the shebang); prints the hook path.
# Omit the body to build a tree with no validator at all.
fake_tree() {
  local d; d="$(mktemp -d)" || return 1
  mkdir -p "${d}/flow/hooks/scripts" "${d}/flow/codex"
  cp "${HOOK}" "${d}/flow/hooks/scripts/check-agent-role-drift.sh"
  if [[ $# -gt 0 ]]; then
    printf '#!/usr/bin/env bash\n%s\n' "$1" > "${d}/flow/codex/validate-agent-roles.sh"
  fi
  printf '%s' "${d}/flow/hooks/scripts/check-agent-role-drift.sh"
}

# remove_fake_tree <hook path printed by fake_tree> — deletes that tree's temp dir.
remove_fake_tree() {
  local tree="${1%/flow/hooks/scripts/check-agent-role-drift.sh}"
  if [[ -n "$1" && "${tree}" != "$1" && -d "${tree}" ]]; then
    rm -rf "${tree}"
  fi
}

write_role() {  # write_role <file-stem> [extra toml lines...] — a valid role in ROOT
  local stem="$1"; shift
  mkdir -p "${ROOT}/.codex/agents"
  {
    printf 'name = "%s"\n' "${stem}"
    printf 'description = "A test role."\n'
    printf 'developer_instructions = "Do the thing."\n'
    local line
    for line in "$@"; do printf '%s\n' "${line}"; done
  } > "${ROOT}/.codex/agents/${stem}.toml"
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

HOOK_DRIFT_MARKER="no longer match the current Codex agent-role schema"
HOOK_PREREQ_MARKER="Python 3.11+"
UNAVAILABLE_LINE="unavailable python3.11+-with-tomllib-required"

# hook_context — prints the additionalContext of the SessionStart JSON in OUT,
# or fails the case when OUT is not that JSON.
hook_context() {
  printf '%s' "${OUT}" | jq -e -r '.hookSpecificOutput | select(.hookEventName == "SessionStart") | .additionalContext' 2>/dev/null
}

# --- Case 6: clean with notes (unknown key) → `clean <note>`; hook silent ---
make_fixture
write_role planner 'retirement_plan = "none"'
run_hook --plain
assert_eq "${OUT}" "clean unverified-key .codex/agents/planner.toml: 'retirement_plan' is not in the documented key subset" "case6 plain clean with one note"
assert_eq "${CODE}" "0" "case6 plain exit"
run_hook
assert_eq "${OUT}" "" "case6 hook silent for unverified-only"
assert_eq "${CODE}" "0" "case6 hook exit"
cleanup_fixture

# --- Case 7: two notes are joined with "; " ---------------------------------
make_fixture
mkdir -p "${ROOT}/.codex/agents"
cat > "${ROOT}/.codex/agents/planner.toml" <<'EOF'
name = "architect"
description = "A test role."
developer_instructions = "Do the thing."
retirement_plan = "none"
EOF
run_hook --plain
assert_eq "${OUT}" "clean unverified-key .codex/agents/planner.toml: 'retirement_plan' is not in the documented key subset; name-convention .codex/agents/planner.toml: name 'architect' differs from filename stem 'planner' (advisory)" "case7 plain clean joins notes with '; '"
run_hook
assert_eq "${OUT}" "" "case7 hook silent for notes-only (name-convention is advisory)"
cleanup_fixture

# --- Case 8: drift reports failing findings only, not notes -----------------
make_fixture
mkdir -p "${ROOT}/.codex/agents"
cat > "${ROOT}/.codex/agents/planner.toml" <<'EOF'
name = "planner"
developer_instructions = "Do the thing."
retirement_plan = "none"
EOF
run_hook --plain
assert_eq "${OUT}" "drift missing-field .codex/agents/planner.toml: 'description' must be a non-empty string" "case8 plain drift lists failing findings only"
run_hook
CTX="$(hook_context)" || fail "case8 hook output must be SessionStart JSON (got: ${OUT})"
assert_contains "${CTX}" "${HOOK_DRIFT_MARKER}" "case8 hook gives the drift advisory"
[[ "${CTX}" != *"retirement_plan"* ]] || fail "case8 drift advisory must not list the unverified note (got: ${CTX})"
[[ "${CTX}" != *"${HOOK_PREREQ_MARKER}"* ]] || fail "case8 drift advisory must not be the prerequisite advisory"
cleanup_fixture

# --- Case 9: unavailable via a stub python3 first on PATH -------------------
make_fixture
write_role planner
STUB="$(stub_python_dir)" || fail "case9 could not build the python3 stub"
run_hook_with "${STUB}:${PATH}" "${HOOK}" --plain
assert_eq "${OUT}" "${UNAVAILABLE_LINE}" "case9 plain unavailable (python3 cannot import tomllib)"
assert_eq "${CODE}" "0" "case9 plain exit stays 0"
run_hook_with "${STUB}:${PATH}" "${HOOK}"
assert_eq "${CODE}" "0" "case9 hook exit stays 0"
CTX="$(hook_context)" || fail "case9 hook output must be SessionStart JSON (got: ${OUT})"
assert_contains "${CTX}" "${HOOK_PREREQ_MARKER}" "case9 hook gives the separate prerequisite advisory"
[[ "${CTX}" != *"${HOOK_DRIFT_MARKER}"* ]] || fail "case9 prerequisite advisory must not claim drift (got: ${CTX})"
[[ "${CTX}" != *"Traceback"* ]] || fail "case9 advisory must carry no stack trace (got: ${CTX})"
rm -rf "${STUB}"
cleanup_fixture

# --- Case 10: unavailable with no python3 on PATH at all ---------------------
make_fixture
write_role planner
NOPY="$(no_python_dir)" || fail "case10 could not build the python-free PATH"
if (PATH="${NOPY}" command -v python3 >/dev/null 2>&1); then
  fail "case10 fixture PATH still resolves a python3 — the case would be masked"
fi
run_hook_with "${NOPY}" "${HOOK}" --plain
assert_eq "${OUT}" "${UNAVAILABLE_LINE}" "case10 plain unavailable (python3 absent)"
run_hook_with "${NOPY}" "${HOOK}"
CTX="$(hook_context)" || fail "case10 hook output must be SessionStart JSON (got: ${OUT})"
assert_contains "${CTX}" "${HOOK_PREREQ_MARKER}" "case10 hook gives the prerequisite advisory"
rm -rf "${NOPY}"
cleanup_fixture

# --- Case 11: unknown — no validator next to the hook ------------------------
make_fixture
write_role planner
FAKE_HOOK="$(fake_tree)" || fail "case11 could not build the validator-less tree"
run_hook_with "${PATH}" "${FAKE_HOOK}" --plain
assert_eq "${OUT}" "unknown no-validator" "case11 plain unknown (resolver failure)"
run_hook_with "${PATH}" "${FAKE_HOOK}"
assert_eq "${OUT}" "" "case11 hook silent for unknown"
assert_eq "${CODE}" "0" "case11 hook exit stays 0"
remove_fake_tree "${FAKE_HOOK}"
cleanup_fixture

# --- Case 12: exit 0 with an unrecognized output line is never `clean` -------
make_fixture
write_role planner
FAKE_HOOK="$(fake_tree 'echo "surprise-kind planner: something new"; exit 0')" || fail "case12 could not build the fake validator tree"
run_hook_with "${PATH}" "${FAKE_HOOK}" --plain
assert_eq "${OUT}" "unknown validator-output" "case12 plain unknown validator-output"
run_hook_with "${PATH}" "${FAKE_HOOK}"
assert_eq "${OUT}" "" "case12 hook silent for unknown"
remove_fake_tree "${FAKE_HOOK}"
FAKE_HOOK="$(fake_tree 'echo "unverified-key a.toml: x"; echo "surprise-kind b.toml: y"; exit 0')" || fail "case12b could not build the fake validator tree"
run_hook_with "${PATH}" "${FAKE_HOOK}" --plain
assert_eq "${OUT}" "unknown validator-output" "case12b a known note beside an unknown line is still unknown"
remove_fake_tree "${FAKE_HOOK}"
cleanup_fixture

# --- Case 13: any other validator exit is unknown, never clean or drift ------
make_fixture
write_role planner
FAKE_HOOK="$(fake_tree 'echo "usage problem" >&2; exit 2')" || fail "case13 could not build the fake validator tree"
run_hook_with "${PATH}" "${FAKE_HOOK}" --plain
assert_eq "${OUT}" "unknown validator-error" "case13 plain unknown for validator exit 2"
run_hook_with "${PATH}" "${FAKE_HOOK}"
assert_eq "${OUT}" "" "case13 hook silent for unknown"
remove_fake_tree "${FAKE_HOOK}"
cleanup_fixture

echo "check-agent-role-drift.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
