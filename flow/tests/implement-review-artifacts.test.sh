#!/usr/bin/env bash
# Tests for flow/skills/implement/scripts/run-artifact-dir.sh — the reviewed
# mktemp -d helper that scopes Phase 6/7 review artifacts per implement run,
# per ticket #525. Follows the fixture-driven idiom of flow/tests/maintain.test.sh:
# `failures` counter, `assert_*` helpers, plain `#!/usr/bin/env bash`.
#
# Also covers the contract (AC #4: no reintroduction of the ticket-scoped
# Phase 6/7 artifact paths) and drift (run-scoping present in phase-6-7-review.md,
# fail-closed fallback preserved in phase-9-pr.md) assertions over the phase
# files and SKILL.md docs.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
HELPER="${FLOW_DIR}/skills/implement/scripts/run-artifact-dir.sh"
PHASE_6_7="${FLOW_DIR}/skills/implement/phases/phase-6-7-review.md"
PHASE_9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
CONFIGURE_SKILL="${FLOW_DIR}/skills/configure/SKILL.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_ne() { [[ "$1" != "$2" ]] || fail "$3: expected values to differ, both were [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain: $2 (actual: $1)"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "$3: expected output to NOT contain: $2"; }
assert_dir_exists() { [[ -d "$1" ]] || fail "$2: expected directory to exist: [$1]"; }
assert_exit_zero() { [[ "$1" -eq 0 ]] || fail "$2: expected exit 0, got $1"; }
assert_exit_nonzero() { [[ "$1" -ne 0 ]] || fail "$2: expected non-zero exit, got 0"; }

# =====================================================================
# Case 1: concurrent isolation (AC #1, #2) — two invocations resolve two
# distinct, existing directories; a file written into one is invisible to
# and does not affect the other.
# =====================================================================
DIR_A="$(bash "${HELPER}")"
CODE_A=$?
DIR_B="$(bash "${HELPER}")"
CODE_B=$?

assert_exit_zero "${CODE_A}" "case1 first invocation exit code"
assert_exit_zero "${CODE_B}" "case1 second invocation exit code"
assert_dir_exists "${DIR_A}" "case1 first invocation returns an existing directory"
assert_dir_exists "${DIR_B}" "case1 second invocation returns an existing directory"
assert_ne "${DIR_A}" "${DIR_B}" "case1 two runs resolve different artifact locations"

if [[ -d "${DIR_A}" ]]; then
  echo "dir-a-review-path" > "${DIR_A}/review-path.txt"
fi
if [[ -d "${DIR_B}" ]]; then
  [[ -f "${DIR_B}/review-path.txt" ]] && fail "case1 dir B must not see dir A's review-path.txt"
fi
if [[ -d "${DIR_A}" ]]; then
  assert_eq "$(cat "${DIR_A}/review-path.txt" 2>/dev/null)" "dir-a-review-path" "case1 dir A's own file is unaffected by dir B's existence"
fi

[[ -n "${DIR_A}" && -d "${DIR_A}" ]] && rm -rf "${DIR_A}"
[[ -n "${DIR_B}" && -d "${DIR_B}" ]] && rm -rf "${DIR_B}"

# =====================================================================
# Case 2: fail-closed helper (AC #3 support) — when the helper cannot
# create a directory (non-directory TMPDIR), it must exit non-zero and
# print no path to stdout, so a caller never proceeds with an empty/
# root-relative path.
# Root-proof lever: TMPDIR points at a *regular file*, so the helper's
# `mkdir -p "${TMPDIR}/cenci"` fails with ENOTDIR — a kernel path-resolution
# error uid 0 cannot bypass. `chmod 000` was a no-op for root, which made this
# assertion false-fail in root containers (#642); see
# flow/docs/shell-scripting-gotchas.md.
# =====================================================================
NON_DIR_TMPDIR="$(mktemp)" || { echo "case2: failed to create non-directory TMPDIR fixture" >&2; exit 2; }
trap 'rm -f "${NON_DIR_TMPDIR}"' EXIT

OUT_C="$(TMPDIR="${NON_DIR_TMPDIR}" bash "${HELPER}" 2>/dev/null)"
CODE_C=$?

rm -f "${NON_DIR_TMPDIR}"

assert_exit_nonzero "${CODE_C}" "case2 non-directory TMPDIR must exit non-zero"
assert_eq "${OUT_C}" "" "case2 non-directory TMPDIR must print no path to stdout"

# =====================================================================
# Case 3: contract (AC #4) — none of the four Phase 6/7 ticket-scoped
# artifact paths may reappear in the phase files or the two SKILL.md docs
# that reference diffContextMode. Scope is locked to exactly these 4
# basenames (Q&A decision above) — not the other ticket-slug-scoped temp
# files (explore-*, pr-body, followup-*, cenci-context-*, screenshots dir),
# which stay out of scope for this ticket.
# =====================================================================
for pattern in \
  "cenci-<ticket-id-or-slug>-diff.patch" \
  "cenci-<ticket-id-or-slug>-files.txt" \
  "cenci-<ticket-id-or-slug>-stat.txt" \
  "cenci-<ticket-id-or-slug>-review-path.txt"; do
  for f in "${PHASE_6_7}" "${PHASE_9}" "${IMPLEMENT_SKILL}" "${CONFIGURE_SKILL}"; do
    if grep -qF -- "${pattern}" "${f}"; then
      fail "case3 contract: $(basename "${f}") still contains ticket-scoped path: ${pattern}"
    fi
  done
done

# =====================================================================
# Run-scoping present (drift) — phase-6-7-review.md must call the helper,
# establish RUN_DIR, and reference all four artifacts through that variable.
# =====================================================================
PHASE_6_7_CONTENT="$(cat "${PHASE_6_7}")"
assert_contains "${PHASE_6_7_CONTENT}" "run-artifact-dir.sh" "drift phase-6-7-review.md must call run-artifact-dir.sh"
assert_contains "${PHASE_6_7_CONTENT}" "RUN_DIR" "drift phase-6-7-review.md must establish RUN_DIR"
assert_contains "${PHASE_6_7_CONTENT}" "\$RUN_DIR/diff.patch" "drift phase-6-7-review.md must write diff.patch via RUN_DIR"
assert_contains "${PHASE_6_7_CONTENT}" "\$RUN_DIR/files.txt" "drift phase-6-7-review.md must write files.txt via RUN_DIR"
assert_contains "${PHASE_6_7_CONTENT}" "\$RUN_DIR/stat.txt" "drift phase-6-7-review.md must write stat.txt via RUN_DIR"
assert_contains "${PHASE_6_7_CONTENT}" "\$RUN_DIR/review-path.txt" "drift phase-6-7-review.md must write review-path.txt via RUN_DIR"

# =====================================================================
# Fail-closed preserved (drift, AC #3) — phase-9-pr.md must report an
# honest "unknown" state, not a false claim of full/lite, when
# review-path is absent or RUN_DIR is unknown (#525 silent-failure fix:
# claiming "full trio" / "Security review done" for an unrecoverable
# path would be a false assurance).
# =====================================================================
PHASE_9_CONTENT="$(cat "${PHASE_9}")"
assert_contains "${PHASE_9_CONTENT}" "Review: unknown (RUN_DIR lost" "drift phase-9-pr.md must report Review: unknown on absent/unknown review-path"
assert_contains "${PHASE_9_CONTENT}" "Security review status unknown (RUN_DIR lost" "drift phase-9-pr.md must report Security review status unknown on absent/unknown review-path"

echo "implement-review-artifacts.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
