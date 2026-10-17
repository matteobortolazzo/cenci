#!/usr/bin/env bash
# Behavioral suite for skills/implement/scripts/pr-size.sh -- Phase 9's
# measurement of the final diff against the S/M/L table (docs/ticket-sizing.md)
# and the PR-body `Size: <letter> (<files> files, <lines> lines) vs ticket
# <letter>` line. Builds a throwaway git repo per scenario; no gh, no network.
#
# Scenarios:
#   1 S diff vs ticket S -> mismatch=none, no flag line
#   2 M diff (lines > 400) vs ticket S -> mismatch=above, founder flag
#   3 letter read from --plan's `### Size Estimate: <letter>` heading
#   4 no ticket letter -> ticket=unknown, mismatch=unknown, unverified flag
#   5 > L bound (61 files) -> size=XL, mismatch=beyond, must-split flag
#   6 lines are insertions + deletions; a binary file counts as one file, 0 lines
#   7 bad base ref -> exit 2
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "pr-size.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "pr-size.test.sh: failed to resolve flow directory." >&2; exit 2; }
SCRIPT="${FLOW_DIR}/skills/implement/scripts/pr-size.sh"
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected to contain [$2], got [$1]"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] || fail "$3: expected NOT to contain [$2]"; }

[[ -r "${SCRIPT}" ]] || { fail "script missing: ${SCRIPT}"; echo "pr-size.test.sh: failures=${failures}"; exit 1; }
T="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 2; }
trap 'rm -rf "${T}"' EXIT

# new_repo <dir> -- a repo with one base commit on `main`.
new_repo() {
  local d="$1"
  git init -q -b main "${d}" >/dev/null 2>&1 || git init -q "${d}"
  git -C "${d}" config user.email t@example.com
  git -C "${d}" config user.name t
  printf 'base\n' > "${d}/base.txt"
  git -C "${d}" add -A && git -C "${d}" commit -q -m base
  git -C "${d}" checkout -q -b feature
}
# add_lines <dir> <file> <n> -- append n lines to file.
add_lines() { local i; for ((i=0;i<$3;i++)); do echo "line $i $RANDOM" >> "$1/$2"; done; }

# --- 1. S vs S ------------------------------------------------------------------
R="${T}/r1"; new_repo "${R}"; add_lines "${R}" a.txt 50; add_lines "${R}" b.txt 50
git -C "${R}" add -A && git -C "${R}" commit -q -m s
OUT="$(bash "${SCRIPT}" "${R}" main --ticket S 2>"${T}/err")"; rc=$?
assert_eq "${rc}" "0" "case1 exit"
assert_contains "${OUT}" "files=2 lines=100 size=S ticket=S mismatch=none" "case1 machine line"
assert_contains "${OUT}" "Size: S (2 files, 100 lines) vs ticket S" "case1 body line"
assert_not_contains "${OUT}" "Size mismatch" "case1 no flag"

# --- 2. M diff vs ticket S -> one size above, flagged to the founder -------------
R="${T}/r2"; new_repo "${R}"; add_lines "${R}" a.txt 450
git -C "${R}" add -A && git -C "${R}" commit -q -m m
OUT="$(bash "${SCRIPT}" "${R}" main --ticket S 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case2 exit (a mismatch is a flag, not an error)"
assert_contains "${OUT}" "files=1 lines=450 size=M ticket=S mismatch=above" "case2 machine line"
assert_contains "${OUT}" "Size: M (1 files, 450 lines) vs ticket S" "case2 body line"
assert_contains "${OUT}" "**Size mismatch**: the PR is one size above its ticket (M vs S) — founder decision required; do not merge on green checks alone." "case2 founder flag"

# --- 3. letter from the plan file -------------------------------------------------
printf -- '---\nticketId: 42\n---\n## Implementation Plan\n### Size Estimate: M\nBound: <= 30 changed files, <= 2,000 changed lines, one feature slice, at most one migration.\nRationale: two files.\n' > "${T}/plan.md"
OUT="$(bash "${SCRIPT}" "${R}" main --plan "${T}/plan.md" 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case3 exit"
assert_contains "${OUT}" "size=M ticket=M mismatch=none" "case3 letter read from the plan heading"
OUT="$(bash "${SCRIPT}" "${R}" main --plan "${T}/plan.md" --ticket L 2>/dev/null)"
assert_contains "${OUT}" "ticket=L mismatch=below" "case3b --ticket overrides --plan"

# --- 4. unknown ticket size ------------------------------------------------------
OUT="$(bash "${SCRIPT}" "${R}" main 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case4 exit"
assert_contains "${OUT}" "ticket=unknown mismatch=unknown" "case4 machine line"
assert_contains "${OUT}" "vs ticket unknown" "case4 body line"
assert_contains "${OUT}" "**Size unverified**" "case4 unverified flag"

# --- 5. beyond L ---------------------------------------------------------------------
R="${T}/r5"; new_repo "${R}"; for ((i=0;i<61;i++)); do printf 'x\n' > "${R}/f${i}.txt"; done
git -C "${R}" add -A && git -C "${R}" commit -q -m xl
OUT="$(bash "${SCRIPT}" "${R}" main --ticket L 2>/dev/null)"; rc=$?
assert_eq "${rc}" "0" "case5 exit"
assert_contains "${OUT}" "files=61 lines=61 size=XL ticket=L mismatch=beyond" "case5 machine line"
assert_contains "${OUT}" "Size: beyond L (61 files, 61 lines) vs ticket L" "case5 body line"
assert_contains "${OUT}" "it must be split; do not merge" "case5 split flag"

# --- 6. insertions + deletions; binary counts as a file with 0 lines ------------
R="${T}/r6"; new_repo "${R}"
printf 'one\ntwo\nthree\n' > "${R}/base.txt"   # 1 deletion (base) + 3 insertions
printf '\x89PNG\x00\x01\x02\x00\xff\xfe\x00' > "${R}/img.bin"
git -C "${R}" add -A && git -C "${R}" commit -q -m mix
OUT="$(bash "${SCRIPT}" "${R}" main --ticket S 2>/dev/null)"
assert_contains "${OUT}" "files=2 lines=4 size=S" "case6 insertions+deletions, binary = 0 lines"

# --- 7. git failure -> exit 2 ----------------------------------------------------------
bash "${SCRIPT}" "${R}" no-such-ref --ticket S >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case7 bad base ref exits 2"
bash "${SCRIPT}" "${R}" main --ticket Q >/dev/null 2>"${T}/err"; rc=$?
assert_eq "${rc}" "2" "case7b invalid --ticket letter exits 2"

echo "pr-size.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
