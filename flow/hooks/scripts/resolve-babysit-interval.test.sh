#!/usr/bin/env bash
# resolve-babysit-interval.test.sh — runtime behavior of the babysit-interval
# resolver. Modelled on run-gate.test.sh (a `failures=` counter, small assert_*
# helpers, self-contained, auto-discovered by the flow gate's `*.test.sh` glob),
# with the one documented behavioral difference: unlike run-gate.sh, a missing
# config file or an unset/empty `babysitInterval` is NOT an error — it echoes
# nothing and exits 0, because the interval is optional and `cenci babysit`
# supplies its own `15m` default. Genuine errors (missing jq, malformed config,
# no-match/ambiguous slug) still fail closed with non-zero exit + stderr, exactly
# like run-gate.sh.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESOLVER="${SCRIPT_DIR}/resolve-babysit-interval.sh"
failures=0

assert_eq() { [[ "$1" == "$2" ]] || { echo "FAIL: expected [$2], got [$1]" >&2; failures=$((failures+1)); }; }
assert_contains() { [[ "$1" == *"$2"* ]] || { echo "FAIL: expected output to contain: $2" >&2; echo "  actual: $1" >&2; failures=$((failures+1)); }; }

# --- Case 1: top-level resolution ---------------------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{"babysitInterval":"20m"}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RESOLVER}")"
code=$?
assert_eq "${out}" "20m"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 2: per-project resolution via slug ----------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/api", "babysitInterval": "5m" },
    { "slug": "web-client", "path": "apps/web-client", "babysitInterval": "30m" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RESOLVER}" api)"
code=$?
assert_eq "${out}" "5m"
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 3: unset field (absent key) → empty, exit 0 -------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RESOLVER}")"
code=$?
assert_eq "${out}" ""
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 4: unset field (present-but-empty string) → empty, exit 0 -----
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{"babysitInterval":""}' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RESOLVER}")"
code=$?
assert_eq "${out}" ""
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 5: missing config file entirely → empty, exit 0 ---------------
ROOT="$(mktemp -d)"
out="$(cd "${ROOT}" && sh "${RESOLVER}")"
code=$?
assert_eq "${out}" ""
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 6: precedence — slug selects the project value, no cross-fallback
# to top-level; top-level (no slug) ignores projects[] entries. -----------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "babysitInterval": "1h",
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/api", "babysitInterval": "5m" },
    { "slug": "web-client", "path": "apps/web-client" }
  ]
}
EOF
# slug with its own value → project value, not the top-level "1h"
out="$(cd "${ROOT}" && sh "${RESOLVER}" api)"
assert_eq "${out}" "5m"
# no slug → top-level value, not any project value
out="$(cd "${ROOT}" && sh "${RESOLVER}")"
assert_eq "${out}" "1h"
# slug whose project omits babysitInterval → empty (no cross-fallback to top-level)
out="$(cd "${ROOT}" && sh "${RESOLVER}" web-client)"
code=$?
assert_eq "${out}" ""
assert_eq "${code}" "0"
rm -rf "${ROOT}"

# --- Case 7: no-match slug (fail closed, like run-gate.sh) ---------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/api", "babysitInterval": "5m" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RESOLVER}" nonexistent-slug 2>/dev/null)"
err="$(cd "${ROOT}" && sh "${RESOLVER}" nonexistent-slug 2>&1 1>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for no-match slug" >&2; failures=$((failures+1)); }
[[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr for no-match slug" >&2; failures=$((failures+1)); }
assert_eq "${out}" ""
rm -rf "${ROOT}"

# --- Case 8: malformed config (fail closed) -----------------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
printf '{ not json' > "${ROOT}/.cenci/config.json"
out="$(cd "${ROOT}" && sh "${RESOLVER}" 2>/dev/null)"
err="$(cd "${ROOT}" && sh "${RESOLVER}" 2>&1 1>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for malformed config" >&2; failures=$((failures+1)); }
[[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr for malformed config" >&2; failures=$((failures+1)); }
assert_eq "${out}" ""
rm -rf "${ROOT}"

# --- Case 9: duplicate slug (ambiguous, fail closed) --------------------
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/.cenci"
cat > "${ROOT}/.cenci/config.json" <<EOF
{
  "isMonorepo": true,
  "projects": [
    { "slug": "api", "path": "packages/api", "babysitInterval": "5m" },
    { "slug": "api", "path": "packages/api", "babysitInterval": "9m" }
  ]
}
EOF
out="$(cd "${ROOT}" && sh "${RESOLVER}" api 2>/dev/null)"
err="$(cd "${ROOT}" && sh "${RESOLVER}" api 2>&1 1>/dev/null)"
code=$?
[[ "${code}" -ne 0 ]] || { echo "FAIL: expected non-zero exit for duplicate slug" >&2; failures=$((failures+1)); }
[[ -n "${err}" ]] || { echo "FAIL: expected non-empty stderr for duplicate slug" >&2; failures=$((failures+1)); }
assert_eq "${out}" ""
rm -rf "${ROOT}"

echo "resolve-babysit-interval.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
