#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_GATE="${SCRIPT_DIR}/run-gate.sh"
failures=0

assert_contains() { [[ "$1" == *"$2"* ]] || { echo "FAIL: expected output to contain: $2" >&2; echo "  actual: $1" >&2; failures=$((failures+1)); }; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || { echo "FAIL: expected output NOT to contain: $2" >&2; echo "  actual: $1" >&2; failures=$((failures+1)); }; }
assert_eq() { [[ "$1" == "$2" ]] || { echo "FAIL: expected [$2], got [$1]" >&2; failures=$((failures+1)); }; }

# --- Case 1: Green (single-repo) ---------------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{"gateCommand":"true"}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RUN_GATE}")"
code=$?
assert_contains "${out}" "GATE_STATUS=green"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 2: Red (single-repo) - gate's own exit code passed through ----
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{"gateCommand":"sh -c '"'"'exit 3'"'"'"}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RUN_GATE}")"
code=$?
assert_contains "${out}" "GATE_STATUS=red"
assert_eq "${code}" "3"
rm -rf "${ROOT}"

# --- Case 3: Unset - empty string gateCommand ---------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{"gateCommand":""}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RUN_GATE}")"
code=$?
assert_contains "${out}" "GATE_STATUS=unset"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 4: Unset - absent gateCommand key -----------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RUN_GATE}")"
code=$?
assert_contains "${out}" "GATE_STATUS=unset"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 5: Unset - missing config file entirely -----------------------
ROOT="$(mktemp -d)"
out="$(cd "${ROOT}" && sh "${RUN_GATE}")"
code=$?
assert_contains "${out}" "GATE_STATUS=unset"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 6: Monorepo slug selection -------------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
mkdir -p "${ROOT}/packages/api"
mkdir -p "${ROOT}/apps/web-client"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/api", "gateCommand": "touch api-ran.marker" },
    { "slug": "web-client", "path": "apps/web-client", "gateCommand": "touch web-ran.marker" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RUN_GATE}" api)"
code=$?
assert_contains "${out}" "GATE_STATUS=green"
assert_eq "${code}" "0"
[[ -f "${ROOT}/packages/api/api-ran.marker" ]] || { echo "FAIL: expected api-ran.marker in packages/api" >&2; failures=$((failures+1)); }
[[ -f "${ROOT}/apps/web-client/web-ran.marker" ]] && { echo "FAIL: web-client gate should not have run" >&2; failures=$((failures+1)); }
rm -rf "${ROOT}"

# --- Case 7: Absolute-CWD guarantee ---------------------------------------
# The gate command must run inside an absolute, symlink-resolved project
# directory that run-gate.sh computes itself (ROOT="$(pwd -P)"), never an
# inherited/logical $PWD. To prove this, the fixture root is reached via a
# symlink from a sibling directory: bash's `cd` (without -P) leaves $PWD set
# to the symlink path, so if run-gate.sh naively trusted an inherited $PWD
# instead of resolving with `pwd -P`, the gate's own `$(pwd)` would report the
# symlink path rather than the real, physical fixture root, and this
# assertion would fail.
REAL_ROOT="$(mktemp -d)"
REAL_ROOT="$(cd "${REAL_ROOT}" && pwd -P)"
mkdir -p "${REAL_ROOT}/.cenci"
cat > "${REAL_ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "test \"\$(pwd)\" = \"${REAL_ROOT}\""}
EOF
SIBLING_DIR="$(mktemp -d)"
ln -s "${REAL_ROOT}" "${SIBLING_DIR}/proj"
out="$(cd "${SIBLING_DIR}/proj" && sh "${RUN_GATE}")"
code=$?
assert_contains "${out}" "GATE_STATUS=green"
assert_eq "${code}" "0"
rm -rf "${REAL_ROOT}" "${SIBLING_DIR}"

# --- Case 8: Malformed config (fail closed) -------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{ not json' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RUN_GATE}" 2>/dev/null)"
err="$(cd "${ROOT}" && sh "${RUN_GATE}" 2>&1 1>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for malformed config" >&2; failures=$((failures+1)); }
[[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr for malformed config" >&2; failures=$((failures+1)); }
assert_not_contains "${out}" "GATE_STATUS="
rm -rf "${ROOT}"

# --- Bonus case: no-match slug (locked design decision, extra coverage) --
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
mkdir -p "${ROOT}/packages/api"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/api", "gateCommand": "true" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RUN_GATE}" nonexistent-slug 2>/dev/null)"
err="$(cd "${ROOT}" && sh "${RUN_GATE}" nonexistent-slug 2>&1 1>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for no-match slug" >&2; failures=$((failures+1)); }
[[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr for no-match slug" >&2; failures=$((failures+1)); }
assert_not_contains "${out}" "GATE_STATUS="
rm -rf "${ROOT}"

# --- Case 9: Missing project directory (fail closed, not GATE_STATUS=red) --
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/does-not-exist", "gateCommand": "true" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RUN_GATE}" api 2>/dev/null)"
err="$(cd "${ROOT}" && sh "${RUN_GATE}" api 2>&1 1>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for missing project directory" >&2; failures=$((failures+1)); }
[[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr for missing project directory" >&2; failures=$((failures+1)); }
assert_not_contains "${out}" "GATE_STATUS="
rm -rf "${ROOT}"

# --- Case 10: Duplicate slug (ambiguous match, fail closed) ---------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
mkdir -p "${ROOT}/packages/api"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/api", "gateCommand": "true" },
    { "slug": "api", "path": "packages/api", "gateCommand": "true" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RUN_GATE}" api 2>/dev/null)"
err="$(cd "${ROOT}" && sh "${RUN_GATE}" api 2>&1 1>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for duplicate slug" >&2; failures=$((failures+1)); }
[[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr for duplicate slug" >&2; failures=$((failures+1)); }
assert_not_contains "${out}" "GATE_STATUS="
rm -rf "${ROOT}"

# --- Case 11: Symlinked project path cannot escape the repository ---------
ROOT="$(mktemp -d)"
OUTSIDE="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
ln -s "${OUTSIDE}" "${ROOT}/escaped"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "escaped", "gateCommand": "touch gate-ran" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RUN_GATE}" api 2>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for escaping symlink" >&2; failures=$((failures+1)); }
assert_not_contains "${out}" "GATE_STATUS="
[[ ! -e "${OUTSIDE}/gate-ran" ]] || { echo "FAIL: escaping symlink gate executed outside repository" >&2; failures=$((failures+1)); }
rm -rf "${ROOT}" "${OUTSIDE}"

# --- Case 12: Symlink ancestor plus missing tail also fails closed --------
ROOT="$(mktemp -d)"
OUTSIDE="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
ln -s "${OUTSIDE}" "${ROOT}/escaped"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "escaped/missing", "gateCommand": "touch gate-ran" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RUN_GATE}" api 2>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for escaping symlink ancestor" >&2; failures=$((failures+1)); }
assert_not_contains "${out}" "GATE_STATUS="
[[ ! -e "${OUTSIDE}/gate-ran" ]] || { echo "FAIL: symlink-ancestor gate executed outside repository" >&2; failures=$((failures+1)); }
rm -rf "${ROOT}" "${OUTSIDE}"

# --- Case 13: Canonicalization does not require GNU realpath -m ------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/project" "${ROOT}/bin"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "project", "gateCommand": "true" }
  ]
}
EOF
cat > "${ROOT}/bin/realpath" <<'EOF'
#!/bin/sh
echo "fixture realpath must not be called" >&2
exit 2
EOF
chmod +x "${ROOT}/bin/realpath"
out="$(cd "${ROOT}" && PATH="${ROOT}/bin:${PATH}" sh "${RUN_GATE}" api)"
code=$?
assert_contains "${out}" "GATE_STATUS=green"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 14: mktemp failure fallback preserves jq's diagnostic and never
# rm's /dev/null (#550) -----------------------------------------------------
# PATH is prepended (not replaced, per Case 13's style) with a curated dir
# containing a `mktemp` that always fails and an `rm` that logs its argv
# before delegating to the real rm (path captured via `command -v rm`).
# Malformed .cenci/config.json triggers jq's parse failure.
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/bin"
printf '{ not json' > "${ROOT}/.cenci/config.json"
RM_LOG="${ROOT}/rm-log.txt"
: > "${RM_LOG}"
cat > "${ROOT}/bin/mktemp" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "${ROOT}/bin/mktemp"
REAL_RM="$(command -v rm)"
cat > "${ROOT}/bin/rm" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "${RM_LOG}"
exec "${REAL_RM}" "\$@"
EOF
chmod +x "${ROOT}/bin/rm"
out="$(cd "${ROOT}" && PATH="${ROOT}/bin:${PATH}" sh "${RUN_GATE}" 2>/dev/null)"
err="$(cd "${ROOT}" && PATH="${ROOT}/bin:${PATH}" sh "${RUN_GATE}" 2>&1 1>/dev/null)"
code=$?
assert_eq "${code}" "1"
assert_not_contains "${out}" "GATE_STATUS="
assert_contains "${err}" "run-gate.sh: warning: mktemp failed; jq errors are written directly to stderr below"
assert_contains "${err}" "parse error"
RM_LOG_CONTENT="$(cat "${RM_LOG}" 2>/dev/null)"
assert_not_contains "${RM_LOG_CONTENT}" "/dev/null"
rm -rf "${ROOT}"

# --- Fixture helper: a gate.sh writing N sequential "line<i>" entries to
# combined stdout+stderr, then exiting with the given code. Written into
# ${ROOT} so relative gateCommand "sh ./gate.sh" runs it via run-gate.sh's
# `cd "${ABS_DIR}"`. ------------------------------------------------------
write_line_gate() {
  # write_line_gate <root> <count> <exit-code>
  local _root="$1" _count="$2" _rc="$3"
  cat > "${_root}/gate.sh" <<EOF
#!/bin/sh
i=1
while [ "\$i" -le ${_count} ]; do
  echo "line\${i}"
  i=\$((i+1))
done
exit ${_rc}
EOF
  chmod +x "${_root}/gate.sh"
}

# --- Case 15: Red + truncate -- shows only the last N lines, appends
# GATE_LOG=, the full untruncated output is retrievable at that path, and
# the gate's own exit code is preserved. ------------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
write_line_gate "${ROOT}" 500 3
cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": 5}}
EOF
out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
code=$?
assert_eq "${code}" "3"
assert_contains "${out}" "GATE_STATUS=red"
assert_contains "${out}" "run-gate.sh: output truncated: showing the last 5 of 500 lines."
assert_contains "${out}" "line500"
assert_not_contains "${out}" "line495"
GATE_LOG_LINE="$(printf '%s\n' "${out}" | grep '^GATE_LOG=')"
[[ -n "${GATE_LOG_LINE}" ]] || { echo "FAIL: expected a GATE_LOG= line in red output" >&2; failures=$((failures+1)); }
GATE_LOG_PATH="${GATE_LOG_LINE#GATE_LOG=}"
[[ -n "${GATE_LOG_PATH}" && -f "${GATE_LOG_PATH}" ]] || { echo "FAIL: expected GATE_LOG path to exist and be a file: ${GATE_LOG_PATH}" >&2; failures=$((failures+1)); }
if [[ -f "${GATE_LOG_PATH}" ]]; then
  LOG_LINES="$(wc -l < "${GATE_LOG_PATH}" | tr -d '[:space:]')"
  assert_eq "${LOG_LINES}" "500"
fi
rm -rf "${ROOT}"

# --- Case 16: Green + truncate -- still truncates the shown tail (and
# still prints the truncation notice), but emits NO GATE_LOG= line and the
# log is deleted (not left behind) on exit. ----------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
write_line_gate "${ROOT}" 200 0
cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": 5}}
EOF
out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
code=$?
assert_eq "${code}" "0"
assert_contains "${out}" "GATE_STATUS=green"
assert_contains "${out}" "run-gate.sh: output truncated: showing the last 5 of 200 lines."
GATE_LINES="$(printf '%s\n' "${out}" | grep -c '^line' || true)"
assert_eq "${GATE_LINES}" "5"
assert_not_contains "${out}" "GATE_LOG="
REMAINING="$(find "${ROOT}/tmp/cenci" -maxdepth 1 -name 'gate-output-*' 2>/dev/null)"
[[ -z "${REMAINING}" ]] || { echo "FAIL: expected no leftover gate-output-* logs after a green run, found: ${REMAINING}" >&2; failures=$((failures+1)); }
rm -rf "${ROOT}"

# --- Case 17: Default line count -- absent cenci block resolves to 120. -----
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
write_line_gate "${ROOT}" 200 5
printf '{"gateCommand": "sh ./gate.sh"}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
code=$?
assert_eq "${code}" "5"
assert_contains "${out}" "GATE_STATUS=red"
assert_contains "${out}" "run-gate.sh: output truncated: showing the last 120 of 200 lines."
assert_contains "${out}" "line200"
assert_not_contains "${out}" "line80"
assert_contains "${out}" "line81"
GATE_LINES="$(printf '%s\n' "${out}" | grep -c '^line' || true)"
assert_eq "${GATE_LINES}" "120"
rm -rf "${ROOT}"

# --- Case 18: Invalid gateOutputLines values (non-digit, zero, negative,
# boolean, decimal) all resolve to the default 120, warn on stderr, and
# never change GATE_STATUS/exit code. -----------------------------------
for VALUE_JSON in '"abc"' '0' '-5' 'true' '12.5'; do
  ROOT="$(mktemp -d)"
  mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
  write_line_gate "${ROOT}" 200 0
  cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": ${VALUE_JSON}}}
EOF
  out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}" 2>/dev/null)"
  err="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}" 2>&1 1>/dev/null)"
  code=$?
  assert_eq "${code}" "0"
  assert_contains "${out}" "GATE_STATUS=green"
  assert_contains "${out}" "run-gate.sh: output truncated: showing the last 120 of 200 lines."
  [[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr warning for invalid gateOutputLines=${VALUE_JSON}" >&2; failures=$((failures+1)); }
  rm -rf "${ROOT}"
done

# --- Case 19: Output within the configured limit -- no truncation notice
# is printed (not even a vacuous partial match of its prefix), full output
# is shown, and the log directory is still minted (proving the mint-before-
# execute step runs unconditionally, not only when truncation is needed). --
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
write_line_gate "${ROOT}" 10 0
cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": 120}}
EOF
out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
code=$?
assert_eq "${code}" "0"
assert_contains "${out}" "GATE_STATUS=green"
assert_not_contains "${out}" "run-gate.sh: output truncated:"
assert_contains "${out}" "line1"
assert_contains "${out}" "line10"
GATE_LINES="$(printf '%s\n' "${out}" | grep -c '^line' || true)"
assert_eq "${GATE_LINES}" "10"
[[ -d "${ROOT}/tmp/cenci" ]] || { echo "FAIL: expected the log base dir ${ROOT}/tmp/cenci to be minted even when output is within the limit" >&2; failures=$((failures+1)); }
rm -rf "${ROOT}"

# --- Case 20: mktemp failure (Case 14's PATH-prepend idiom, reused for the
# gate-output log's own mktemp) -- fail-open: untruncated passthrough, a
# stderr warning, no GATE_LOG= line, GATE_STATUS/exit code unaffected. ------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/bin" "${ROOT}/tmp"
write_line_gate "${ROOT}" 200 7
cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": 5}}
EOF
cat > "${ROOT}/bin/mktemp" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "${ROOT}/bin/mktemp"
out="$(cd "${ROOT}" && PATH="${ROOT}/bin:${PATH}" TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}" 2>/dev/null)"
err="$(cd "${ROOT}" && PATH="${ROOT}/bin:${PATH}" TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}" 2>&1 1>/dev/null)"
code=$?
assert_eq "${code}" "7"
assert_contains "${out}" "GATE_STATUS=red"
assert_not_contains "${out}" "GATE_LOG="
assert_not_contains "${out}" "output truncated:"
assert_contains "${out}" "line1"
assert_contains "${out}" "line200"
assert_contains "${err}" "run-gate.sh: warning: failed to create gate output log; falling back to untruncated output"
rm -rf "${ROOT}"

# --- Case 20b: Base log dir pre-existing as a symlink (security hardening,
# #1101) -- run-gate.sh must refuse to write through a symlinked
# TMPDIR/cenci base dir rather than following it, since `mkdir -p` alone
# succeeds silently on a pre-existing symlink. Routes into the exact same
# fail-open fallback as Case 20: untruncated passthrough, the same stderr
# warning, no GATE_LOG= line, GATE_STATUS/exit code unaffected. The symlink
# target directory must also remain untouched (no gate-output-* log written
# through it). ----------------------------------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
SYMLINK_TARGET="$(mktemp -d)"
ln -s "${SYMLINK_TARGET}" "${ROOT}/tmp/cenci"
write_line_gate "${ROOT}" 200 7
cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": 5}}
EOF
out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}" 2>/dev/null)"
err="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}" 2>&1 1>/dev/null)"
code=$?
assert_eq "${code}" "7"
assert_contains "${out}" "GATE_STATUS=red"
assert_not_contains "${out}" "GATE_LOG="
assert_not_contains "${out}" "output truncated:"
assert_contains "${out}" "line1"
assert_contains "${out}" "line200"
assert_contains "${err}" "run-gate.sh: warning: failed to create gate output log; falling back to untruncated output"
REMAINING="$(find "${SYMLINK_TARGET}" -maxdepth 1 -name 'gate-output-*' 2>/dev/null)"
[[ -z "${REMAINING}" ]] || { echo "FAIL: expected no gate-output-* log written through the symlinked base dir, found: ${REMAINING}" >&2; failures=$((failures+1)); }
rm -rf "${ROOT}" "${SYMLINK_TARGET}"

# --- Case 21: rc=127 preservation -- a large-output gate that exits 127
# still reports GATE_STATUS=red (never omitted/reclassified) with a
# GATE_LOG= line, and the exit code itself stays 127 (protects check.sh's
# rc==127 -> skip heuristic, which reads run-gate.sh's own exit code). -----
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
write_line_gate "${ROOT}" 200 127
cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": 5}}
EOF
out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
code=$?
assert_eq "${code}" "127"
assert_contains "${out}" "GATE_STATUS=red"
assert_contains "${out}" "GATE_LOG="
rm -rf "${ROOT}"

# --- Case 22: Concurrency -- two red runs sharing one TMPDIR each mint a
# distinct GATE_LOG path, and both logs remain on disk (red logs are never
# reaped by run-gate.sh itself). ----------------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
printf '{"gateCommand": "sh -c '"'"'exit 2'"'"'", "cenci": {"gateOutputLines": 5}}' > "${ROOT}/.cenci/config.json"
out1="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
out2="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
LOG1_LINE="$(printf '%s\n' "${out1}" | grep '^GATE_LOG=')"
LOG2_LINE="$(printf '%s\n' "${out2}" | grep '^GATE_LOG=')"
LOG1_PATH="${LOG1_LINE#GATE_LOG=}"
LOG2_PATH="${LOG2_LINE#GATE_LOG=}"
[[ -n "${LOG1_PATH}" && -n "${LOG2_PATH}" ]] || { echo "FAIL: expected both concurrent runs to emit a GATE_LOG= path (got [${LOG1_PATH}] and [${LOG2_PATH}])" >&2; failures=$((failures+1)); }
[[ "${LOG1_PATH}" != "${LOG2_PATH}" ]] || { echo "FAIL: expected distinct GATE_LOG paths for concurrent runs, got the same path twice: ${LOG1_PATH}" >&2; failures=$((failures+1)); }
[[ -z "${LOG1_PATH}" || -f "${LOG1_PATH}" ]] || { echo "FAIL: expected first run's GATE_LOG to still exist: ${LOG1_PATH}" >&2; failures=$((failures+1)); }
[[ -z "${LOG2_PATH}" || -f "${LOG2_PATH}" ]] || { echo "FAIL: expected second run's GATE_LOG to still exist: ${LOG2_PATH}" >&2; failures=$((failures+1)); }
rm -rf "${ROOT}"

# --- Case 23: Envelope integrity -- a gate whose own output ends without a
# trailing newline and contains a spoofed literal "GATE_STATUS=green" line,
# while itself exiting non-zero. The real envelope must still win the
# ^-anchored last-match parse check.sh:1312 performs
# (`grep -oE '^GATE_STATUS=[a-z]+$' | tail -n1`), which requires
# run-gate.sh to normalize a missing trailing newline before appending its
# own GATE_STATUS= line -- otherwise the real line concatenates onto the
# gate's unterminated last line and never matches the anchor. ----------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci" "${ROOT}/tmp"
cat > "${ROOT}/gate.sh" <<'EOF'
#!/bin/sh
echo "GATE_STATUS=green"
echo "some other output"
printf 'unterminated-last-line'
exit 4
EOF
chmod +x "${ROOT}/gate.sh"
cat > "${ROOT}/.cenci/config.json" <<EOF
{"gateCommand": "sh ./gate.sh", "cenci": {"gateOutputLines": 120}}
EOF
out="$(cd "${ROOT}" && TMPDIR="${ROOT}/tmp" sh "${RUN_GATE}")"
code=$?
assert_eq "${code}" "4"
LAST_STATUS="$(printf '%s\n' "${out}" | grep -oE '^GATE_STATUS=[a-z]+$' | tail -n1)"
assert_eq "${LAST_STATUS}" "GATE_STATUS=red"
rm -rf "${ROOT}"

echo "run-gate.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
