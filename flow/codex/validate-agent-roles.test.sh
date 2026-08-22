#!/usr/bin/env bash
# validate-agent-roles.test.sh — schema-validation coverage for
# validate-agent-roles.sh (#1040). Fixtures pin the two shapes that
# previously broke in this repo's own history (#409: missing description,
# #422: missing name) plus the classes the old grep-based pins never caught:
# a duplicate `name` across files, an unknown top-level key, and a clean
# valid set.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALIDATOR="${SCRIPT_DIR}/validate-agent-roles.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain [$2], got [$1]"; }

valid_toml() {  # valid_toml <name> — a complete, schema-valid role body
  cat <<EOF
name = "$1"
description = "A test role."
developer_instructions = "Do the thing."
EOF
}

echo "validate-agent-roles.test.sh"

# --- Case 1 (#409 shape): missing description → fails ---------------------
DIR="$(mktemp -d)"
cat > "${DIR}/planner.toml" <<'EOF'
name = "planner"
developer_instructions = "Do the thing."
EOF
OUT="$(bash "${VALIDATOR}" --plain "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "1" "case1 exit code"
assert_contains "${OUT}" "missing-field ${DIR}/planner.toml: 'description'" "case1 finding"
rm -rf "${DIR}"

# --- Case 2 (#422 shape): missing name → fails -----------------------------
DIR="$(mktemp -d)"
cat > "${DIR}/planner.toml" <<'EOF'
description = "A test role."
developer_instructions = "Do the thing."
EOF
OUT="$(bash "${VALIDATOR}" --plain "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "1" "case2 exit code"
assert_contains "${OUT}" "missing-field ${DIR}/planner.toml: 'name'" "case2 finding"
rm -rf "${DIR}"

# --- Case 3: duplicate name across files (different directories) → fails --
DIR_A="$(mktemp -d)"
DIR_B="$(mktemp -d)"
valid_toml "dup" > "${DIR_A}/dup.toml"
valid_toml "dup" > "${DIR_B}/dup.toml"
OUT="$(bash "${VALIDATOR}" --plain "${DIR_A}" "${DIR_B}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "1" "case3 exit code"
assert_contains "${OUT}" "duplicate-name dup:" "case3 finding"
assert_contains "${OUT}" "${DIR_A}/dup.toml" "case3 names first file"
assert_contains "${OUT}" "${DIR_B}/dup.toml" "case3 names second file"
rm -rf "${DIR_A}" "${DIR_B}"

# --- Case 4: unknown top-level key → fails ---------------------------------
DIR="$(mktemp -d)"
cat > "${DIR}/planner.toml" <<'EOF'
name = "planner"
description = "A test role."
developer_instructions = "Do the thing."
retirement_plan = "generous"
EOF
OUT="$(bash "${VALIDATOR}" --plain "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "1" "case4 exit code"
assert_contains "${OUT}" "unknown-key ${DIR}/planner.toml: 'retirement_plan'" "case4 finding"
rm -rf "${DIR}"

# --- Case 5: valid set → passes, no findings, human mode reports OK -------
DIR="$(mktemp -d)"
valid_toml "planner" > "${DIR}/planner.toml"
valid_toml "implementer" > "${DIR}/implementer.toml"
OUT="$(bash "${VALIDATOR}" --plain "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "0" "case5 plain exit code"
assert_eq "${OUT}" "" "case5 plain silent on clean set"
OUT="$(bash "${VALIDATOR}" "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "0" "case5 human exit code"
assert_contains "${OUT}" "2 agent role file(s) OK" "case5 human summary"
rm -rf "${DIR}"

# --- Case 6: invalid TOML syntax → fails -----------------------------------
DIR="$(mktemp -d)"
printf 'name = "planner\n' > "${DIR}/planner.toml"
OUT="$(bash "${VALIDATOR}" --plain "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "1" "case6 exit code"
assert_contains "${OUT}" "invalid-toml ${DIR}/planner.toml:" "case6 finding"
rm -rf "${DIR}"

# --- Case 7: name field doesn't match filename stem → fails ----------------
DIR="$(mktemp -d)"
valid_toml "someone-else" > "${DIR}/planner.toml"
OUT="$(bash "${VALIDATOR}" --plain "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "1" "case7 exit code"
assert_contains "${OUT}" "name-mismatch ${DIR}/planner.toml: name 'someone-else' does not match filename stem 'planner'" "case7 finding"
rm -rf "${DIR}"

echo "validate-agent-roles.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
