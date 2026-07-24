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

echo "run-gate.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
