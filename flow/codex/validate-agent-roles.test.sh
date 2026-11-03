#!/usr/bin/env bash
# validate-agent-roles.test.sh — classifier coverage for validate-agent-roles.sh
# (#1040, reworked by #1191). Fixtures pin the two shapes that previously broke in
# this repo's own history (#409: missing description, #422: missing name), the
# four supported shapes the old five-key list rejected (#1191 positive fixtures),
# every proven type or required-field error, the unverified/advisory notes that
# must never fail, the per-layer duplicate rule, and exit 3 (validation
# unavailable) with python3 absent or unusable.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALIDATOR="${SCRIPT_DIR}/validate-agent-roles.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain [$2], got [$1]"; }
assert_not_contains() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3: expected output NOT to contain [$2], got [$1]"
}

ROOT="$(mktemp -d)" || { echo "FAIL: mktemp -d" >&2; exit 1; }
trap 'rm -rf "${ROOT}"' EXIT
CASE_N=0
newdir() {  # newdir <var> — sets <var> to a fresh empty fixture directory
  CASE_N=$((CASE_N+1))
  mkdir -p "${ROOT}/d${CASE_N}" || return 1
  printf -v "$1" '%s' "${ROOT}/d${CASE_N}"
}

valid_toml() {  # valid_toml <name> — a complete, schema-valid role body
  cat <<EOF
name = "$1"
description = "A test role."
developer_instructions = "Do the thing."
EOF
}

run_plain() {  # run_plain <dir>... — sets OUT (stdout+stderr) and CODE
  OUT="$(bash "${VALIDATOR}" --plain "$@" 2>&1)"; CODE=$?
}

echo "validate-agent-roles.test.sh"

# --- Case 1 (#409 shape): missing description → fails ---------------------
newdir DIR
cat > "${DIR}/planner.toml" <<'EOF'
name = "planner"
developer_instructions = "Do the thing."
EOF
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case1 exit code"
assert_contains "${OUT}" "missing-field ${DIR}/planner.toml: 'description'" "case1 finding"

# --- Case 2 (#422 shape): missing name → fails -----------------------------
newdir DIR
cat > "${DIR}/planner.toml" <<'EOF'
description = "A test role."
developer_instructions = "Do the thing."
EOF
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case2 exit code"
assert_contains "${OUT}" "missing-field ${DIR}/planner.toml: 'name'" "case2 finding"

# --- Case 3: missing developer_instructions → fails ------------------------
newdir DIR
cat > "${DIR}/planner.toml" <<'EOF'
name = "planner"
description = "A test role."
EOF
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case3 exit code"
assert_contains "${OUT}" "missing-field ${DIR}/planner.toml: 'developer_instructions'" "case3 finding"

# --- Case 4: each required field empty (or whitespace-only) → fails -------
for field in name description developer_instructions; do
  newdir DIR
  {
    [[ "${field}" == "name" ]] && echo 'name = ""' || echo 'name = "planner"'
    [[ "${field}" == "description" ]] && echo 'description = "   "' || echo 'description = "A test role."'
    [[ "${field}" == "developer_instructions" ]] && echo 'developer_instructions = ""' || echo 'developer_instructions = "Do the thing."'
  } > "${DIR}/planner.toml"
  run_plain "${DIR}"
  assert_eq "${CODE}" "1" "case4 empty ${field} exit code"
  assert_contains "${OUT}" "missing-field ${DIR}/planner.toml: '${field}'" "case4 empty ${field} finding"
done

# --- Case 5: required field present but not a string → fails ---------------
newdir DIR
cat > "${DIR}/planner.toml" <<'EOF'
name = "planner"
description = 42
developer_instructions = "Do the thing."
EOF
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case5 exit code"
assert_contains "${OUT}" "missing-field ${DIR}/planner.toml: 'description'" "case5 finding"

# --- Case 6: invalid TOML syntax → fails -----------------------------------
newdir DIR
printf 'name = "planner\n' > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case6 exit code"
assert_contains "${OUT}" "invalid-toml ${DIR}/planner.toml:" "case6 finding"

# --- Case 7: unreadable entry (a directory named *.toml) → fails -----------
# A directory is a kernel-enforced unreadable file even for root (EISDIR).
newdir DIR
mkdir "${DIR}/broken.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case7 exit code"
assert_contains "${OUT}" "unreadable ${DIR}/broken.toml:" "case7 finding"

# --- Case 8: non-string model → fails --------------------------------------
newdir DIR
{ valid_toml "planner"; echo 'model = 5'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case8 exit code"
assert_contains "${OUT}" "invalid-type ${DIR}/planner.toml: 'model'" "case8 finding"

# --- Case 9: non-string model_reasoning_effort → fails ---------------------
newdir DIR
{ valid_toml "planner"; echo 'model_reasoning_effort = true'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case9 exit code"
assert_contains "${OUT}" "invalid-type ${DIR}/planner.toml: 'model_reasoning_effort'" "case9 finding"

# --- Case 10: sandbox_mode outside the closed set → fails ------------------
newdir DIR
{ valid_toml "planner"; echo 'sandbox_mode = "yolo"'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case10 exit code"
assert_contains "${OUT}" "invalid-value ${DIR}/planner.toml: 'sandbox_mode'" "case10 finding"

# --- Case 11: non-string sandbox_mode → fails ------------------------------
newdir DIR
{ valid_toml "planner"; echo 'sandbox_mode = 1'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case11 exit code"
assert_contains "${OUT}" "invalid-type ${DIR}/planner.toml: 'sandbox_mode'" "case11 finding"

# --- Case 12: non-table mcp_servers → fails --------------------------------
newdir DIR
{ valid_toml "planner"; echo 'mcp_servers = "docs"'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case12 exit code"
assert_contains "${OUT}" "invalid-type ${DIR}/planner.toml: 'mcp_servers'" "case12 finding"

# --- Case 13: non-table skills.config → fails ------------------------------
newdir DIR
{ valid_toml "planner"; printf '[skills]\nconfig = "x"\n'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case13 exit code"
assert_contains "${OUT}" "invalid-type ${DIR}/planner.toml: 'skills.config'" "case13 finding"

# --- Case 14: non-table skills (skills.config cannot exist under it) -------
newdir DIR
{ echo 'skills = "x"'; valid_toml "planner"; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case14 exit code"
assert_contains "${OUT}" "invalid-type ${DIR}/planner.toml: 'skills'" "case14 finding"

# --- Case 15: two files, same directory, same name → fails -----------------
newdir DIR
valid_toml "dup" > "${DIR}/one.toml"
valid_toml "dup" > "${DIR}/two.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case15 exit code"
assert_contains "${OUT}" "duplicate-name dup:" "case15 finding"
assert_contains "${OUT}" "${DIR}/one.toml" "case15 names first file"
assert_contains "${OUT}" "${DIR}/two.toml" "case15 names second file"

# --- Case 16: positive — sandbox_mode in the closed set → passes -----------
for mode in read-only workspace-write danger-full-access; do
  newdir DIR
  { valid_toml "planner"; echo "sandbox_mode = \"${mode}\""; } > "${DIR}/planner.toml"
  run_plain "${DIR}"
  assert_eq "${CODE}" "0" "case16 ${mode} exit code"
  assert_eq "${OUT}" "" "case16 ${mode} no findings"
done

# --- Case 17: positive — [mcp_servers.<id>] table → passes -----------------
newdir DIR
{ valid_toml "planner"; printf '\n[mcp_servers.docs]\ncommand = "docs-server"\n'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "0" "case17 exit code"
assert_eq "${OUT}" "" "case17 no findings"

# --- Case 18: positive — [skills.config] table → passes --------------------
newdir DIR
{ valid_toml "planner"; printf '\n[skills.config]\nenabled = true\n'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "0" "case18 exit code"
assert_eq "${OUT}" "" "case18 no findings"

# --- Case 19: positive — valid name differing from filename → advisory only
newdir DIR
valid_toml "someone-else" > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "0" "case19 exit code"
assert_contains "${OUT}" "name-convention ${DIR}/planner.toml: name 'someone-else' differs from filename stem 'planner'" "case19 note"
for kind in invalid-toml unreadable missing-field invalid-type invalid-value duplicate-name; do
  assert_not_contains "${OUT}" "${kind} " "case19 no ${kind}"
done

# --- Case 20: unknown top-level key → unverified-key note, exit 0 ----------
newdir DIR
{ valid_toml "planner"; echo 'retirement_plan = "generous"'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "0" "case20 exit code"
assert_contains "${OUT}" "unverified-key ${DIR}/planner.toml: 'retirement_plan'" "case20 note"

# --- Case 21: effort outside the documented set → unverified-value, exit 0
newdir DIR
{ valid_toml "planner"; echo 'model_reasoning_effort = "turbo"'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "0" "case21 exit code"
assert_contains "${OUT}" "unverified-value ${DIR}/planner.toml: 'model_reasoning_effort'" "case21 note"

# --- Case 22: documented effort and any model string pass silently ---------
newdir DIR
{ valid_toml "planner"; printf 'model = "some-future-model"\nmodel_reasoning_effort = "xhigh"\n'; } > "${DIR}/planner.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "0" "case22 exit code"
assert_eq "${OUT}" "" "case22 no findings"

# --- Case 23: exit is 1 iff a failing kind appears (notes + failure) -------
newdir DIR
{ valid_toml "planner"; echo 'retirement_plan = "generous"'; } > "${DIR}/planner.toml"
printf 'name = "broken"\n' > "${DIR}/broken.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case23 exit code"
assert_contains "${OUT}" "unverified-key ${DIR}/planner.toml: 'retirement_plan'" "case23 note still reported"
assert_contains "${OUT}" "missing-field ${DIR}/broken.toml: 'description'" "case23 failing finding"

# --- Case 24: byte-identical files in two directory args → no duplicate ----
newdir DIR_A
newdir DIR_B
valid_toml "dup" > "${DIR_A}/dup.toml"
valid_toml "dup" > "${DIR_B}/dup.toml"
run_plain "${DIR_A}" "${DIR_B}"
assert_eq "${CODE}" "0" "case24 exit code"
assert_not_contains "${OUT}" "duplicate-name" "case24 no duplicate-name"

# --- Case 25: a same-layer duplicate is still caught beside other layers ---
newdir DIR_A
newdir DIR_B
valid_toml "dup" > "${DIR_A}/dup.toml"
valid_toml "dup" > "${DIR_B}/dup.toml"
valid_toml "dup" > "${DIR_B}/dup-copy.toml"
run_plain "${DIR_A}" "${DIR_B}"
assert_eq "${CODE}" "1" "case25 exit code"
assert_contains "${OUT}" "duplicate-name dup:" "case25 finding"
assert_contains "${OUT}" "${DIR_B}/dup-copy.toml" "case25 names the same-layer copy"
assert_not_contains "${OUT}" "${DIR_A}/dup.toml" "case25 does not name the other layer"

# --- Case 26: valid set → passes; plain silent, human mode reports OK ------
newdir DIR
valid_toml "planner" > "${DIR}/planner.toml"
valid_toml "implementer" > "${DIR}/implementer.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "0" "case26 plain exit code"
assert_eq "${OUT}" "" "case26 plain silent on clean set"
OUT="$(bash "${VALIDATOR}" "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "0" "case26 human exit code"
assert_contains "${OUT}" "2 agent role file(s) OK" "case26 human summary"

# --- Case 27: human mode with a note exits 0 and still reports OK ----------
newdir DIR
{ valid_toml "planner"; echo 'retirement_plan = "generous"'; } > "${DIR}/planner.toml"
OUT="$(bash "${VALIDATOR}" "${DIR}" 2>&1)"; CODE=$?
assert_eq "${CODE}" "0" "case27 exit code"
assert_contains "${OUT}" "unverified-key ${DIR}/planner.toml: 'retirement_plan'" "case27 note shown"
assert_contains "${OUT}" "1 agent role file(s) OK" "case27 summary"

# --- Case 28: usage errors keep exit 2 -------------------------------------
OUT="$(bash "${VALIDATOR}" --plain 2>&1)"; CODE=$?
assert_eq "${CODE}" "2" "case28 no-dir exit code"
assert_contains "${OUT}" "usage: validate-agent-roles.sh" "case28 usage text"
run_plain "${ROOT}/does-not-exist"
assert_eq "${CODE}" "2" "case28 missing dir exit code"
newdir DIR
run_plain "${DIR}"
assert_eq "${CODE}" "2" "case28 empty dir exit code"

# --- Exit 3 fixtures: validation unavailable --------------------------------
BASH_BIN="$(command -v bash)" || { echo "FAIL: bash not on PATH" >&2; exit 1; }
FIND_BIN="$(command -v find)" || { echo "FAIL: find not on PATH" >&2; exit 1; }
SORT_BIN="$(command -v sort)" || { echo "FAIL: sort not on PATH" >&2; exit 1; }
EXPECT_TAIL="Install Python 3.11 or newer, then rerun; no agent-role file was changed."

run_unavailable() {  # run_unavailable <PATH> <dir> — sets OUT (stdout), ERR (stderr), CODE
  local errfile="${ROOT}/stderr.txt"
  OUT="$(PATH="$1" "${BASH_BIN}" "${VALIDATOR}" --plain "$2" 2>"${errfile}")"; CODE=$?
  ERR="$(cat "${errfile}")" || ERR="<unreadable>"
}

# --- Case 29: python3 absent → exit 3, names Python 3.11+, found: none -----
# The PATH holds symlinks to find and sort only, so a host python3 cannot mask it.
ABSENT_BIN="${ROOT}/absent-bin"
mkdir -p "${ABSENT_BIN}"
ln -s "${FIND_BIN}" "${ABSENT_BIN}/find"
ln -s "${SORT_BIN}" "${ABSENT_BIN}/sort"
newdir DIR
valid_toml "planner" > "${DIR}/planner.toml"
run_unavailable "${ABSENT_BIN}" "${DIR}"
assert_eq "${CODE}" "3" "case29 exit code"
assert_eq "${OUT}" "" "case29 stdout empty"
assert_eq "${ERR}" "cenci: Codex agent-role validation unavailable — Python 3.11+ with tomllib not found on PATH (found: none). ${EXPECT_TAIL}" "case29 stderr line"

# --- Case 30: unusable python3 stub (fails everything) → exit 3 ------------
# The stub directory comes first on PATH, ahead of any real python3.
STUB_FAIL="${ROOT}/stub-fail"
mkdir -p "${STUB_FAIL}"
printf '#!/bin/sh\nexit 1\n' > "${STUB_FAIL}/python3"
chmod +x "${STUB_FAIL}/python3"
run_unavailable "${STUB_FAIL}:${PATH}" "${DIR}"
assert_eq "${CODE}" "3" "case30 exit code"
assert_eq "${OUT}" "" "case30 stdout empty"
assert_eq "${ERR}" "cenci: Codex agent-role validation unavailable — Python 3.11+ with tomllib not found on PATH (found: no tomllib). ${EXPECT_TAIL}" "case30 stderr line"

# --- Case 31: python3 too old for tomllib → exit 3, validated version shown
STUB_OLD="${ROOT}/stub-old"
mkdir -p "${STUB_OLD}"
cat > "${STUB_OLD}/python3" <<'EOF'
#!/bin/sh
case "$*" in
  *tomllib*) exit 1 ;;
  *) echo "3.9.18" ;;
esac
EOF
chmod +x "${STUB_OLD}/python3"
run_unavailable "${STUB_OLD}:${PATH}" "${DIR}"
assert_eq "${CODE}" "3" "case31 exit code"
assert_eq "${ERR}" "cenci: Codex agent-role validation unavailable — Python 3.11+ with tomllib not found on PATH (found: 3.9.18). ${EXPECT_TAIL}" "case31 stderr line"

# --- Case 32: a garbage version from python3 is never echoed ---------------
STUB_JUNK="${ROOT}/stub-junk"
mkdir -p "${STUB_JUNK}"
cat > "${STUB_JUNK}/python3" <<'EOF'
#!/bin/sh
case "$*" in
  *tomllib*) exit 1 ;;
  *) echo "9.9.9; echo pwned" ;;
esac
EOF
chmod +x "${STUB_JUNK}/python3"
run_unavailable "${STUB_JUNK}:${PATH}" "${DIR}"
assert_eq "${CODE}" "3" "case32 exit code"
assert_not_contains "${ERR}" "pwned" "case32 junk version not echoed"
assert_contains "${ERR}" "(found: no tomllib)" "case32 falls back to no tomllib"

# --- Case 33: a repo-planted tomllib.py in the cwd is never imported --------
# SessionStart and maintain run the validator from the repo root; without
# `python3 -I` the cwd leads sys.path and a planted tomllib.py would execute.
PLANT="${ROOT}/plant"
SENTINEL="${ROOT}/plant-sentinel"
mkdir -p "${PLANT}"
printf 'open("%s", "w").close()\n' "${SENTINEL}" > "${PLANT}/tomllib.py"
newdir DIR
valid_toml "planner" > "${DIR}/planner.toml"
OUT="$(cd "${PLANT}" && env -u PYTHONSAFEPATH bash "${VALIDATOR}" --plain "${DIR}" 2>&1)"; CODE=$?
[[ ! -e "${SENTINEL}" ]] || fail "case33 planted tomllib.py was imported (sentinel written)"
assert_eq "${CODE}" "0" "case33 exit code"
assert_eq "${OUT}" "" "case33 valid role still clean"

# --- Case 34: one pathological file fails per-file, not the whole run ------
# A deeply nested array raises RecursionError, which is not a ValueError.
newdir DIR
valid_toml "planner" > "${DIR}/planner.toml"
OPENS="$(printf '%*s' 20000 '' | tr ' ' '[')"
CLOSES="$(printf '%*s' 20000 '' | tr ' ' ']')"
{ valid_toml "deep"; printf 'x = %s%s\n' "${OPENS}" "${CLOSES}"; } > "${DIR}/deep.toml"
run_plain "${DIR}"
assert_eq "${CODE}" "1" "case34 exit code"
assert_contains "${OUT}" "invalid-toml ${DIR}/deep.toml: file is nested too deeply or could not be parsed within limits" "case34 per-file invalid-toml finding with the fixed message"
assert_not_contains "${OUT}" "RecursionError" "case34 exception text not echoed"
assert_not_contains "${OUT}" "unavailable" "case34 not reported as unavailable"
assert_not_contains "${OUT}" "${DIR}/planner.toml" "case34 healthy neighbor unaffected"

# --- Case 35: probe passes but the parse run fails → found: unusable --------
STUB_RUNFAIL="${ROOT}/stub-runfail"
mkdir -p "${STUB_RUNFAIL}"
cat > "${STUB_RUNFAIL}/python3" <<'EOF'
#!/bin/sh
case "$*" in
  "-I -c "*) exit 0 ;;
  *) exit 7 ;;
esac
EOF
chmod +x "${STUB_RUNFAIL}/python3"
newdir DIR
valid_toml "planner" > "${DIR}/planner.toml"
run_unavailable "${STUB_RUNFAIL}:${PATH}" "${DIR}"
assert_eq "${CODE}" "3" "case35 exit code"
assert_eq "${OUT}" "" "case35 stdout empty"
assert_eq "${ERR}" "cenci: Codex agent-role validation unavailable — Python 3.11+ with tomllib not found on PATH (found: unusable). ${EXPECT_TAIL}" "case35 stderr line"

# --- Case 36: a broken tomllib is an environment fault, not a role-file fault -
# The wrapper runs the real python3 but patches tomllib.load to raise a
# non-RecursionError exception; that must reach exit 3, never exit 1 invalid-toml.
REAL_PY="$(command -v python3)" || { echo "FAIL: python3 not on PATH" >&2; exit 1; }
STUB_BROKEN="${ROOT}/stub-broken-tomllib"
mkdir -p "${STUB_BROKEN}"
cat > "${STUB_BROKEN}/python3" <<EOF
#!/bin/sh
case "\$*" in
  "-I - "*)
    shift 2
    { printf 'import tomllib\ndef _broken(*a, **k):\n    raise AttributeError("patched")\ntomllib.load = _broken\n'; cat; } | exec "${REAL_PY}" -I - "\$@"
    ;;
  *) exec "${REAL_PY}" "\$@" ;;
esac
EOF
chmod +x "${STUB_BROKEN}/python3"
newdir DIR
valid_toml "planner" > "${DIR}/planner.toml"
run_unavailable "${STUB_BROKEN}:${PATH}" "${DIR}"
assert_eq "${CODE}" "3" "case36 exit code"
assert_eq "${OUT}" "" "case36 stdout empty (no invalid-toml finding)"
assert_eq "${ERR}" "cenci: Codex agent-role validation unavailable — Python 3.11+ with tomllib not found on PATH (found: unusable). ${EXPECT_TAIL}" "case36 single stderr line, no traceback or invalid-toml finding"

echo "validate-agent-roles.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
