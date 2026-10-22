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
# Every configure markdown file: SKILL.md plus phases/*.md and references/*.md.
# The PR-body temp path moved from SKILL.md into phases/commit-pr.md (#1344), so
# the ticket-scoped-path negative below scans all of them.
CONFIGURE_MD_FILES=("${CONFIGURE_SKILL}")
shopt -s nullglob
CONFIGURE_MD_FILES+=("${FLOW_DIR}"/skills/configure/phases/*.md "${FLOW_DIR}"/skills/configure/references/*.md)
shopt -u nullglob
CONFIGURE_COMMIT_PR="${FLOW_DIR}/skills/configure/phases/commit-pr.md"
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
  for f in "${PHASE_6_7}" "${PHASE_9}" "${IMPLEMENT_SKILL}" "${CONFIGURE_MD_FILES[@]}"; do
    [[ -f "${f}" ]] || { fail "case3 contract: file not found, cannot verify absence: ${f}"; continue; }
    if grep -qF -- "${pattern}" "${f}"; then
      fail "case3 contract: $(basename "${f}") still contains ticket-scoped path: ${pattern}"
    fi
  done
done
# Pairing for the configure scan: SKILL.md plus at least 15 phases/references
# files were scanned, and the file that now holds the PR-body temp path (a
# per-slug, non-ticket-scoped path) really carries it, so the absence above
# cannot pass against an emptied or renamed configure skill.
[[ "${#CONFIGURE_MD_FILES[@]}" -ge 16 ]] || fail "case3 contract: configure scan is vacuous: found ${#CONFIGURE_MD_FILES[@]} files (expected SKILL.md plus at least 15 phases/references files)"
if [[ -f "${CONFIGURE_COMMIT_PR}" ]]; then
  assert_contains "$(cat "${CONFIGURE_COMMIT_PR}")" 'cenci-configure-<slug>-pr-body.md' "case3 contract: phases/commit-pr.md carries the slug-scoped PR-body temp path the configure scan is paired with"
else
  fail "case3 contract: phases/commit-pr.md not found"
fi

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

# =====================================================================
# #1311 — Phase 6 + 7's changed-file list includes untracked files.
#
# Contract cases pin the Shared Context fence and the classification rule
# text; the behavioral cases extract that same fence from the doc and run it
# (allowlisted line prefixes only) against `mktemp -d` git fixtures, so the
# test cannot drift from the documented procedure. Classification itself is
# carried out by the agent from prose, so the `secret` docs-file and
# json-edit-plus-new-json edge cases are covered at contract level only.
# =====================================================================
FIXTURE_ROOT="$(mktemp -d)" || { echo "1311: failed to create fixture root" >&2; exit 2; }
[[ -n "${FIXTURE_ROOT}" && -d "${FIXTURE_ROOT}" ]] || { echo "1311: mktemp -d returned no directory" >&2; exit 2; }
trap 'rm -f "${NON_DIR_TMPDIR}"; rm -rf "${FIXTURE_ROOT}"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.invalid

assert_file_exists() { [[ -f "$1" ]] || fail "$2: expected file to exist: [$1]"; }
assert_file_absent() { [[ ! -e "$1" ]] || fail "$2: expected path to be absent: [$1]"; }

# extract_section <content> <exact-heading-line> -- pure, fence-aware: prints
# the named "## " section from its heading through the next real heading of
# the same or a higher level, or EOF. A heading-shaped line inside a fenced
# code block is never a boundary (gotchas rule 29). No side effects.
extract_section() {
  awk -v want="$2" '
    $0 == want && !on {
      on = 1
      match(want, /^#+/)
      level = RLENGTH
      print
      next
    }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^#+ / {
      match($0, /^#+/)
      if (RLENGTH <= level) exit
    }
    on { print }
  ' <<<"$1"
}

# extract_fences <content> <needle> <mode> -- mode "body" prints the body of
# every fenced block containing <needle>; mode "count" prints how many such
# blocks exist. Pure.
extract_fences() {
  awk -v needle="$2" -v mode="$3" '
    /^```/ {
      if (infence) {
        if (hit) { body = body buf; n++ }
        buf = ""; hit = 0; infence = 0
      } else {
        infence = 1
      }
      next
    }
    infence { buf = buf $0 "\n"; if (index($0, needle)) hit = 1 }
    END { if (mode == "count") print n + 0; else printf "%s", body }
  ' <<<"$1"
}

# slice_between <content> <start-prefix> <end-prefix> -- prints the lines from
# the first line starting with <start-prefix> up to (excluding) the next line
# starting with <end-prefix>. Prints nothing when either anchor is missing, so
# a positive assertion on the slice can never pass vacuously over a wider
# window. Pure.
slice_between() {
  awk -v s="$2" -v e="$3" '
    on && index($0, e) == 1 { found = 1; exit }
    !on && index($0, s) == 1 { on = 1 }
    on { buf = buf $0 "\n" }
    END { if (found) printf "%s", buf }
  ' <<<"$1"
}

# line_of <block> <exact-line> -- 1-based line number of the first exact match
# within <block>, or empty. Pure.
line_of() {
  grep -nxF -m1 -- "$2" <<<"$1" | cut -d: -f1
}

# strictly_increasing <n>... -- 0 only when every argument is a number and the
# sequence is strictly increasing (an empty argument, i.e. a missing line, fails).
strictly_increasing() {
  local prev=0 n
  for n in "$@"; do
    [[ "${n}" =~ ^[0-9]+$ ]] || return 1
    (( n > prev )) || return 1
    prev="${n}"
  done
  return 0
}

assert_fence_line() {
  # $1=fence body $2=exact line $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  grep -qxF -- "$2" <<<"$1" || fail "$3: fence lacks exact line: $2"
}

SC_DIFF_LINE='git -C <worktree-path> diff > "$RUN_DIR/diff.patch"'
SC_STAT_LINE='git -C <worktree-path> diff --stat > "$RUN_DIR/stat.txt"'
SC_RESET_LINE='rm -f "$RUN_DIR/files.txt" "$RUN_DIR/files-tracked.txt" "$RUN_DIR/untracked.txt"'
SC_TRACKED_LINE='git -C <worktree-path> diff --name-only origin/main > "$RUN_DIR/files-tracked.txt"'
SC_UNTRACKED_LINE='git -C <worktree-path> ls-files --others --exclude-standard > "$RUN_DIR/untracked.txt"'
SC_COMBINE_LINE='sort -u "$RUN_DIR/files-tracked.txt" "$RUN_DIR/untracked.txt" > "$RUN_DIR/files.txt"'
SC_EXIT_CHECK='Run each line above as its own command and check its exit status before reading its output.'
SC_STOP='If either capture command exits non-zero (for example, `origin/main` cannot be resolved), treat it as an unrecoverable Phase 6 + 7 setup failure, the same handling as a failed `run-artifact-dir.sh`: stop and report, do not run the combine step, and never continue with an empty or partial list.'
SC_COMBINE_FAIL='If the combine step itself exits non-zero, run `rm -f "$RUN_DIR/files.txt"` and stop and report the same way, because a redirect creates the file even when the command fails.'
SC_CLEANUP_PREFIX='or from the combine step, first run `'
SC_CLEANUP='On a non-zero exit from either capture command or from the combine step, first run `'"${SC_RESET_LINE}"'` (a shell redirect creates its output file even when the command fails, so a failed step can leave an empty or partial list behind), then stop and report.'
SC_NO_PIPE='Never combine the captures with a pipe such as `{ ...; ...; } | sort`, with process substitution, or with one combined command substitution: each command is captured to its own file and exit-checked on its own, so a failure cannot collapse into an empty list.'
SC_KEEP_UNTRACKED='Keep `$RUN_DIR/untracked.txt`: Review Path Classification reads it below.'
SC_LS_FILES_CWD='The `git ls-files` capture below also targets the worktree with `git -C <worktree-path>`.'
OLD_FILES_LINE='git -C <worktree-path> diff --name-only > "$RUN_DIR/files.txt"'
CLASSIFY_READS='Read `$RUN_DIR/files.txt` (changed file list), `$RUN_DIR/untracked.txt` (untracked files only) and `$RUN_DIR/stat.txt` (total changed lines)'
RULE3_UNTRACKED='`$RUN_DIR/untracked.txt` is empty (it exists and lists no path)'
RULE4_UNTRACKED='A missing `$RUN_DIR/untracked.txt` is also ambiguous'
DANGER_UNTRACKED='the full content of every file listed in `$RUN_DIR/untracked.txt`'
DANGER_UNREADABLE='a file listed there that cannot be read counts as a match'
RULE2_DANGER='**and** the diff does not match the danger check above'
RULE2_UNTRACKED='**and** `$RUN_DIR/untracked.txt` exists (a missing file never qualifies as lite-docs, because its untracked content could not be danger-scanned; it falls through to Rule 4 and resolves to **full**)'
RERUN_SEQUENCE='re-run the Shared Context capture sequence (the `rm -f` reset, both exit-checked captures and the combine step)'

PHASE_6_7_TEXT=""
if [[ -f "${PHASE_6_7}" ]] && PHASE_6_7_TEXT="$(cat "${PHASE_6_7}")"; then
  :
else
  fail "1311: could not read ${PHASE_6_7}"
  PHASE_6_7_TEXT=""
fi
SHARED_CONTEXT="$(extract_section "${PHASE_6_7_TEXT}" '## Shared Context')"
[[ -n "${SHARED_CONTEXT}" ]] || fail "1311: could not locate '## Shared Context' in phase-6-7-review.md"
SC_FENCE_COUNT="$(extract_fences "${SHARED_CONTEXT}" 'files-tracked.txt' count)"
assert_eq "${SC_FENCE_COUNT}" "1" "1311: exactly one Shared Context fence mentions files-tracked.txt"
SC_FENCE="$(extract_fences "${SHARED_CONTEXT}" 'files-tracked.txt' body)"
CLASSIFY_SECTION="$(extract_section "${PHASE_6_7_TEXT}" '## Review Path Classification')"
[[ -n "${CLASSIFY_SECTION}" ]] || fail "1311: could not locate '## Review Path Classification' in phase-6-7-review.md"

case_extractor_self_test() {
  local doc body fence reordered
  doc=$'## Alpha\nalpha-1\n```markdown\n## Fenced heading\n```\nalpha-after-fence\n## Beta\nbeta-1'
  body="$(extract_section "${doc}" '## Alpha')"
  assert_contains "${body}" 'alpha-after-fence' "extractor does not stop at a fenced ## line"
  assert_not_contains "${body}" 'beta-1' "extractor stops at the next real ## heading"
  doc=$'## Alpha\n```bash\necho one\n```\nprose\n```bash\nfiles-tracked.txt\necho two\n```'
  assert_eq "$(extract_fences "${doc}" 'files-tracked.txt' count)" "1" "fence counter counts only matching fences"
  fence="$(extract_fences "${doc}" 'files-tracked.txt' body)"
  assert_contains "${fence}" 'echo two' "fence extractor returns the matching fence body"
  assert_not_contains "${fence}" 'echo one' "fence extractor omits non-matching fences"
  assert_eq "$(slice_between $'a\nstart\nmid\nend\nz' 'start' 'end')" $'start\nmid' "slice_between returns start up to end"
  assert_eq "$(slice_between $'a\nstart\nmid' 'start' 'end')" "" "slice_between returns nothing when the end anchor is missing"
  # Injected reordering: the reset after a capture must fail the ordering check.
  reordered=$'capture-a\nrm-line\ncapture-b'
  if strictly_increasing "$(line_of "${reordered}" 'rm-line')" "$(line_of "${reordered}" 'capture-a')"; then
    fail "ordering check failed to catch a reset placed after a capture"
  fi
  strictly_increasing "$(line_of "${reordered}" 'capture-a')" "$(line_of "${reordered}" 'rm-line')" \
    || fail "ordering check rejected a correctly ordered pair"
  if strictly_increasing "$(line_of "${reordered}" 'absent-line')" "2"; then
    fail "ordering check accepted a missing line"
  fi
  # Runner exactness: only the six documented lines pass, never a prefix match.
  local doc_line bad_line before
  for doc_line in "${SC_DIFF_LINE}" "${SC_STAT_LINE}" "${SC_RESET_LINE}" "${SC_TRACKED_LINE}" "${SC_UNTRACKED_LINE}" "${SC_COMBINE_LINE}"; do
    fence_line_is_documented "${doc_line}" || fail "exactness check rejected a documented line: ${doc_line}"
  done
  for bad_line in \
    'git -C <worktree-path> diff; echo x' \
    "${SC_DIFF_LINE}; echo x" \
    'git -C <worktree-path> diff --name-only origin/main > "$RUN_DIR/extra.txt"' \
    "${SC_RESET_LINE} /" \
    'rm -f "$RUN_DIR/anything"' \
    'sort -u "$RUN_DIR/files-tracked.txt" > "$RUN_DIR/files.txt"'; do
    if fence_line_is_documented "${bad_line}"; then
      fail "exactness check accepted a non-documented line: ${bad_line}"
    fi
  done
  # The runner itself must reject such a line (placed first so nothing runs).
  before="${failures}"
  run_documented_sequence $'git -C <worktree-path> diff; echo x\n'"${SC_DIFF_LINE}" "${FIXTURE_ROOT}/selftest-wt" "${FIXTURE_ROOT}/selftest-run" 2>/dev/null
  if [[ "${SEQ_RC}" -eq 98 && "${failures}" -eq $((before + 1)) ]]; then
    failures="${before}"
  else
    fail "runner accepted a line that is not exactly one of the six documented commands (rc=${SEQ_RC})"
  fi
}

case_shared_context_captures() {
  assert_fence_line "${SC_FENCE}" "${SC_TRACKED_LINE}" "case_shared_context_captures tracked capture"
  assert_fence_line "${SC_FENCE}" "${SC_UNTRACKED_LINE}" "case_shared_context_captures untracked capture"
  assert_fence_line "${SC_FENCE}" "${SC_DIFF_LINE}" "case_shared_context_captures diff.patch line kept"
  assert_fence_line "${SC_FENCE}" "${SC_STAT_LINE}" "case_shared_context_captures stat.txt line kept"
  assert_contains "${SHARED_CONTEXT}" "${SC_KEEP_UNTRACKED}" "case_shared_context_captures untracked.txt is kept for classification"
  assert_contains "${SHARED_CONTEXT}" "${SC_LS_FILES_CWD}" "case_shared_context_captures git ls-files also targets the worktree"
}

case_shared_context_combine() {
  local t u c
  assert_contains "${SHARED_CONTEXT}" "${SC_EXIT_CHECK}" "case_shared_context_combine exit-check sentence"
  assert_contains "${SHARED_CONTEXT}" "${SC_COMBINE_FAIL}" "case_shared_context_combine failed combine removes files.txt"
  assert_contains "${SHARED_CONTEXT}" "${SC_NO_PIPE}" "case_shared_context_combine no pipe / process substitution / combined substitution sentence"
  assert_fence_line "${SC_FENCE}" "${SC_COMBINE_LINE}" "case_shared_context_combine sort -u combine"
  t="$(line_of "${SC_FENCE}" "${SC_TRACKED_LINE}")"
  u="$(line_of "${SC_FENCE}" "${SC_UNTRACKED_LINE}")"
  c="$(line_of "${SC_FENCE}" "${SC_COMBINE_LINE}")"
  strictly_increasing "${t}" "${u}" "${c}" || fail "case_shared_context_combine: capture lines must precede the combine line (tracked=${t:-none} untracked=${u:-none} combine=${c:-none})"
  # Scoped to the fence so the explanatory prose above does not false-match.
  assert_not_contains "${SC_FENCE}" '| sort' "case_shared_context_combine fence has no piped sort"
  assert_not_contains "${SC_FENCE}" '<(' "case_shared_context_combine fence has no process substitution"
  assert_not_contains "${SC_FENCE}" '$(' "case_shared_context_combine fence has no command substitution"
}

case_shared_context_reset() {
  local r t u
  assert_fence_line "${SC_FENCE}" "${SC_RESET_LINE}" "case_shared_context_reset rm -f names all three files"
  r="$(line_of "${SC_FENCE}" "${SC_RESET_LINE}")"
  t="$(line_of "${SC_FENCE}" "${SC_TRACKED_LINE}")"
  u="$(line_of "${SC_FENCE}" "${SC_UNTRACKED_LINE}")"
  strictly_increasing "${r}" "${t}" "${u}" || fail "case_shared_context_reset: reset must precede the first capture (reset=${r:-none} tracked=${t:-none} untracked=${u:-none})"
}

case_shared_context_stop_on_failure() {
  assert_contains "${SHARED_CONTEXT}" "${SC_STOP}" "case_shared_context_stop_on_failure stop sentence"
}

case_old_files_line_gone() {
  [[ -f "${PHASE_6_7}" ]] || { fail "case_old_files_line_gone: file not found: ${PHASE_6_7}"; return; }
  if grep -qF -- "${OLD_FILES_LINE}" "${PHASE_6_7}"; then
    fail "case_old_files_line_gone: phase-6-7-review.md still contains the bare diff --name-only line"
  fi
}

case_lite_small_untracked() {
  local rule3 rule4
  rule3="$(slice_between "${CLASSIFY_SECTION}" '3. **Small + safe**' '4. **Otherwise**')"
  rule4="$(slice_between "${CLASSIFY_SECTION}" '4. **Otherwise**' 'Write the chosen path string')"
  [[ -n "${rule3}" ]] || fail "case_lite_small_untracked: could not slice Rule 3"
  [[ -n "${rule4}" ]] || fail "case_lite_small_untracked: could not slice Rule 4"
  assert_contains "${rule3}" "${RULE3_UNTRACKED}" "case_lite_small_untracked Rule 3 untracked condition"
  assert_contains "${rule4}" "${RULE4_UNTRACKED}" "case_lite_small_untracked Rule 4 treats a missing untracked.txt as ambiguous"
  assert_contains "${CLASSIFY_SECTION}" "${CLASSIFY_READS}" "case_lite_small_untracked classification reads untracked.txt"
}

case_danger_scan_untracked() {
  local danger rule2
  danger="$(slice_between "${CLASSIFY_SECTION}" '**Danger check**' '0. **Toggle off**')"
  rule2="$(slice_between "${CLASSIFY_SECTION}" '2. **Docs-only**' '3. **Small + safe**')"
  [[ -n "${danger}" ]] || fail "case_danger_scan_untracked: could not slice the danger check"
  [[ -n "${rule2}" ]] || fail "case_danger_scan_untracked: could not slice Rule 2"
  assert_contains "${danger}" "${DANGER_UNTRACKED}" "case_danger_scan_untracked scans untracked file content"
  assert_contains "${danger}" "${DANGER_UNREADABLE}" "case_danger_scan_untracked an unreadable untracked file fails closed"
  # EC5 wiring: Rule 2 (lite-docs) still depends on the danger check.
  assert_contains "${rule2}" "${RULE2_DANGER}" "case_danger_scan_untracked Rule 2 still depends on the danger check"
}

case_rerun_wording() {
  assert_contains "${PHASE_6_7_TEXT}" "${RERUN_SEQUENCE}" "case_rerun_wording fix-and-rerun re-runs the whole capture sequence"
  assert_not_contains "${PHASE_6_7_TEXT}" 're-run the Shared Context git commands' "case_rerun_wording the old rerun wording is gone"
}

# --- Behavioral: run the fence extracted from the doc against temp repos ------

# fence_line_is_documented <line> -- 0 only when <line> is exactly one of the six
# documented Shared Context commands (no prefix matching, so an appended
# `; echo x` or an extra prefix-matching line is never accepted). Pure.
fence_line_is_documented() {
  case "$1" in
    "${SC_DIFF_LINE}"|"${SC_STAT_LINE}"|"${SC_RESET_LINE}"|"${SC_TRACKED_LINE}"|"${SC_UNTRACKED_LINE}"|"${SC_COMBINE_LINE}") return 0 ;;
    *) return 1 ;;
  esac
}

# run_documented_sequence <fence> <worktree> <run-dir> [<path-prefix-dir>] --
# runs every fence line in order via `bash -c`, stopping at the first non-zero
# exit exactly as the doc instructs. Each line must exactly match one of the six
# documented commands; anything else fails the case. An optional directory is
# put first on PATH for the run (used to inject a failing tool). Sets SEQ_RC and
# SEQ_FAILED_LINE.
run_documented_sequence() {
  local fence="$1" wt="$2" rd="$3" pfx="${4:-}" line quoted cmd run_path
  SEQ_RC=0
  SEQ_FAILED_LINE=""
  [[ -n "${fence}" ]] || { fail "runner: empty documented fence"; SEQ_RC=97; return 1; }
  quoted="$(printf '%q' "${wt}")" || { fail "runner: could not quote the worktree path"; SEQ_RC=99; return 1; }
  run_path="${PATH}"
  [[ -z "${pfx}" ]] || run_path="${pfx}:${PATH}"
  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    fence_line_is_documented "${line}" || { fail "runner: unrecognised line in the documented fence: ${line}"; SEQ_RC=98; return 1; }
    cmd="${line//"<worktree-path>"/"${quoted}"}"
    PATH="${run_path}" RUN_DIR="${rd}" bash -c "${cmd}" 2>/dev/null
    SEQ_RC=$?
    if [[ "${SEQ_RC}" -ne 0 ]]; then
      SEQ_FAILED_LINE="${line}"
      return 0
    fi
  done <<<"${fence}"
  return 0
}

# make_fixture <name> -- bare origin (main), a clone with one tracked file and a
# committed .gitignore, pushed, plus a `feature` worktree. Sets WT and RD.
make_fixture() {
  local base="${FIXTURE_ROOT}/$1"
  WT="${base}/feature"
  RD="${base}/run"
  {
    mkdir -p "${RD}" \
      && git init -q --bare --initial-branch=main "${base}/origin" \
      && git clone -q "${base}/origin" "${base}/main" 2>/dev/null \
      && mkdir -p "${base}/main/flow/skills" \
      && printf 'original\n' > "${base}/main/flow/skills/existing.md" \
      && printf '*.log\n' > "${base}/main/.gitignore" \
      && git -C "${base}/main" add -A \
      && git -C "${base}/main" commit -q -m initial \
      && git -C "${base}/main" push -q origin main \
      && git -C "${base}/main" worktree add -q -b feature "${WT}"
  } || { fail "make_fixture $1: fixture setup failed"; return 1; }
}

case_capture_tracked_and_untracked() {
  make_fixture capture-both || return
  printf 'modified\n' >> "${WT}/flow/skills/existing.md"
  printf 'new\n' > "${WT}/flow/skills/new-skill.md"
  printf 'noise\n' > "${WT}/flow/skills/scratch.log"
  run_documented_sequence "${SC_FENCE}" "${WT}" "${RD}" || return
  assert_exit_zero "${SEQ_RC}" "case_capture_tracked_and_untracked runner exit"
  assert_file_exists "${RD}/files.txt" "case_capture_tracked_and_untracked files.txt"
  assert_eq "$(cat "${RD}/files.txt" 2>/dev/null)" $'flow/skills/existing.md\nflow/skills/new-skill.md' "case_capture_tracked_and_untracked files.txt lists exactly both paths"
  assert_eq "$(wc -l < "${RD}/files.txt" | tr -d '[:space:]')" "2" "case_capture_tracked_and_untracked files.txt has two lines"
  assert_eq "$(cat "${RD}/untracked.txt" 2>/dev/null)" "flow/skills/new-skill.md" "case_capture_tracked_and_untracked untracked.txt lists only the new path"
  assert_not_contains "$(cat "${RD}/files.txt" 2>/dev/null)" "scratch.log" "case_capture_tracked_and_untracked gitignored file is not in files.txt"
  assert_not_contains "$(cat "${RD}/untracked.txt" 2>/dev/null)" "scratch.log" "case_capture_tracked_and_untracked gitignored file is not in untracked.txt"
  assert_file_exists "${RD}/diff.patch" "case_capture_tracked_and_untracked diff.patch still captured"
  assert_file_exists "${RD}/stat.txt" "case_capture_tracked_and_untracked stat.txt still captured"
}

case_capture_untracked_only() {
  make_fixture capture-untracked-only || return
  printf 'new\n' > "${WT}/flow/skills/only-new.md"
  run_documented_sequence "${SC_FENCE}" "${WT}" "${RD}" || return
  assert_exit_zero "${SEQ_RC}" "case_capture_untracked_only runner exit"
  assert_eq "$(cat "${RD}/files.txt" 2>/dev/null)" "flow/skills/only-new.md" "case_capture_untracked_only files.txt names the one file"
  assert_eq "$(wc -l < "${RD}/files.txt" | tr -d '[:space:]')" "1" "case_capture_untracked_only files.txt is one line, never empty"
}

case_capture_dedup() {
  make_fixture capture-dedup || return
  # Tracked in origin, removed from the index but left on disk: the path is
  # reported by the tracked diff and again as untracked.
  git -C "${WT}" rm -q --cached flow/skills/existing.md || { fail "case_capture_dedup: git rm --cached failed"; return; }
  run_documented_sequence "${SC_FENCE}" "${WT}" "${RD}" || return
  assert_exit_zero "${SEQ_RC}" "case_capture_dedup runner exit"
  assert_eq "$(grep -cxF -- 'flow/skills/existing.md' "${RD}/files-tracked.txt")" "1" "case_capture_dedup the path really is in the tracked list"
  assert_eq "$(grep -cxF -- 'flow/skills/existing.md' "${RD}/untracked.txt")" "1" "case_capture_dedup the path really is in the untracked list"
  assert_eq "$(grep -cxF -- 'flow/skills/existing.md' "${RD}/files.txt")" "1" "case_capture_dedup the path appears exactly once in files.txt"
}

case_capture_origin_main_missing() {
  local repo="${FIXTURE_ROOT}/no-origin" rd="${FIXTURE_ROOT}/no-origin-run"
  mkdir -p "${repo}" "${rd}" && git init -q "${repo}" || { fail "case_capture_origin_main_missing: fixture setup failed"; return; }
  printf 'new\n' > "${repo}/new-file.md"
  printf 'stale\n' > "${rd}/files.txt"
  printf 'stale\n' > "${rd}/files-tracked.txt"
  printf 'stale\n' > "${rd}/untracked.txt"
  run_documented_sequence "${SC_FENCE}" "${repo}" "${rd}" || return
  assert_exit_nonzero "${SEQ_RC}" "case_capture_origin_main_missing runner must stop with a non-zero exit"
  assert_eq "${SEQ_FAILED_LINE}" "${SC_TRACKED_LINE}" "case_capture_origin_main_missing stops at the tracked capture"
  assert_file_absent "${rd}/files.txt" "case_capture_origin_main_missing no stale or partial files.txt"
  assert_file_absent "${rd}/untracked.txt" "case_capture_origin_main_missing no stale untracked.txt"
}

case_capture_rerun() {
  make_fixture capture-rerun || return
  printf 'first\n' > "${WT}/flow/skills/first.md"
  run_documented_sequence "${SC_FENCE}" "${WT}" "${RD}" || return
  assert_exit_zero "${SEQ_RC}" "case_capture_rerun first run exit"
  printf 'second\n' > "${WT}/flow/skills/second.md"
  run_documented_sequence "${SC_FENCE}" "${WT}" "${RD}" || return
  assert_exit_zero "${SEQ_RC}" "case_capture_rerun second run exit"
  assert_contains "$(cat "${RD}/files.txt" 2>/dev/null)" "flow/skills/second.md" "case_capture_rerun regenerated files.txt has the new path"
  assert_contains "$(cat "${RD}/untracked.txt" 2>/dev/null)" "flow/skills/second.md" "case_capture_rerun regenerated untracked.txt has the new path"
}

case_shared_context_cleanup() {
  assert_contains "${SHARED_CONTEXT}" "${SC_CLEANUP}" "case_shared_context_cleanup any failed capture or combine runs the rm -f reset first, then stops"
  # The earlier sentences stay (not reworded): the stop sentence and the
  # combine-failure sentence are pinned by their own cases above.
  assert_contains "${SHARED_CONTEXT}" "${SC_STOP}" "case_shared_context_cleanup keeps the stop sentence"
  assert_contains "${SHARED_CONTEXT}" "${SC_COMBINE_FAIL}" "case_shared_context_cleanup keeps the combine-failure sentence"
}

case_docs_only_untracked() {
  local rule2
  rule2="$(slice_between "${CLASSIFY_SECTION}" '2. **Docs-only**' '3. **Small + safe**')"
  [[ -n "${rule2}" ]] || fail "case_docs_only_untracked: could not slice Rule 2"
  assert_contains "${rule2}" "${RULE2_UNTRACKED}" "case_docs_only_untracked Rule 2 requires untracked.txt to exist"
  # Wiring: the new condition sits on the same source line as the lite-docs decision.
  assert_contains "$(grep -F -m1 -e "${RULE2_UNTRACKED}" <<<"${rule2}")" '→ path = **lite-docs**' "case_docs_only_untracked the condition is part of the lite-docs rule line"
}

# apply_documented_cleanup <run-dir> -- extracts the command from the doc's
# "first run `<cmd>`" cleanup sentence (not a copy held in the test), requires
# it to be the documented reset line, and runs it against <run-dir>.
apply_documented_cleanup() {
  local rd="$1" line rest cmd
  line="$(grep -F -m1 -e "${SC_CLEANUP_PREFIX}" <<<"${SHARED_CONTEXT}")"
  [[ -n "${line}" ]] || { fail "cleanup: no documented cleanup sentence in Shared Context"; return 1; }
  rest="${line#*"${SC_CLEANUP_PREFIX}"}"
  cmd="${rest%%\`*}"
  [[ "${cmd}" == "${SC_RESET_LINE}" ]] || { fail "cleanup: the documented cleanup command is not the documented rm -f reset line: ${cmd}"; return 1; }
  RUN_DIR="${rd}" bash -c "${cmd}"
}

# make_failing_bin <dir> <tool> -- a PATH-prefix directory whose <tool> wrapper
# fails: `git` fails only for `ls-files` (everything else is passed through to
# the real git), `sort` always fails. Only the tool under test breaks.
make_failing_bin() {
  local dir="$1" tool="$2" real_git
  mkdir -p "${dir}" || { fail "make_failing_bin: mkdir failed"; return 1; }
  case "${tool}" in
    git)
      real_git="$(command -v git)" || { fail "make_failing_bin: git not found"; return 1; }
      printf '#!/bin/sh\nfor a in "$@"; do\n  [ "$a" = ls-files ] && exit 1\ndone\nexec %s "$@"\n' "$(printf '%q' "${real_git}")" > "${dir}/git" || return 1
      ;;
    sort)
      printf '#!/bin/sh\nexit 1\n' > "${dir}/sort" || return 1
      ;;
    *) fail "make_failing_bin: unsupported tool ${tool}"; return 1 ;;
  esac
  chmod +x "${dir}/${tool}" || { fail "make_failing_bin: chmod +x failed"; return 1; }
}

case_cleanup_on_untracked_capture_failure() {
  make_fixture cleanup-ls-files || return
  make_failing_bin "${FIXTURE_ROOT}/cleanup-ls-files/bin" git || return
  printf 'new\n' > "${WT}/flow/skills/new-skill.md"
  run_documented_sequence "${SC_FENCE}" "${WT}" "${RD}" "${FIXTURE_ROOT}/cleanup-ls-files/bin" || return
  assert_exit_nonzero "${SEQ_RC}" "case_cleanup_on_untracked_capture_failure the ls-files capture must fail"
  assert_eq "${SEQ_FAILED_LINE}" "${SC_UNTRACKED_LINE}" "case_cleanup_on_untracked_capture_failure stops at the untracked capture"
  # Non-vacuity: the failed run really left partial files behind (a redirect
  # creates its output file even when the command fails).
  assert_file_exists "${RD}/files-tracked.txt" "case_cleanup_on_untracked_capture_failure partial tracked list left behind"
  assert_file_exists "${RD}/untracked.txt" "case_cleanup_on_untracked_capture_failure empty untracked list left behind"
  apply_documented_cleanup "${RD}" || return
  assert_file_absent "${RD}/files.txt" "case_cleanup_on_untracked_capture_failure files.txt after the documented cleanup"
  assert_file_absent "${RD}/files-tracked.txt" "case_cleanup_on_untracked_capture_failure files-tracked.txt after the documented cleanup"
  assert_file_absent "${RD}/untracked.txt" "case_cleanup_on_untracked_capture_failure untracked.txt after the documented cleanup"
}

case_cleanup_on_combine_failure() {
  make_fixture cleanup-combine || return
  make_failing_bin "${FIXTURE_ROOT}/cleanup-combine/bin" sort || return
  printf 'new\n' > "${WT}/flow/skills/new-skill.md"
  run_documented_sequence "${SC_FENCE}" "${WT}" "${RD}" "${FIXTURE_ROOT}/cleanup-combine/bin" || return
  assert_exit_nonzero "${SEQ_RC}" "case_cleanup_on_combine_failure the combine step must fail"
  assert_eq "${SEQ_FAILED_LINE}" "${SC_COMBINE_LINE}" "case_cleanup_on_combine_failure stops at the combine step"
  assert_file_exists "${RD}/files.txt" "case_cleanup_on_combine_failure an empty files.txt was left behind by the redirect"
  apply_documented_cleanup "${RD}" || return
  assert_file_absent "${RD}/files.txt" "case_cleanup_on_combine_failure files.txt after the documented cleanup"
  assert_file_absent "${RD}/files-tracked.txt" "case_cleanup_on_combine_failure files-tracked.txt after the documented cleanup"
  assert_file_absent "${RD}/untracked.txt" "case_cleanup_on_combine_failure untracked.txt after the documented cleanup"
}

case_extractor_self_test
case_shared_context_captures
case_shared_context_combine
case_shared_context_reset
case_shared_context_stop_on_failure
case_shared_context_cleanup
case_docs_only_untracked
case_old_files_line_gone
case_lite_small_untracked
case_danger_scan_untracked
case_rerun_wording
case_capture_tracked_and_untracked
case_capture_untracked_only
case_capture_dedup
case_capture_origin_main_missing
case_capture_rerun
case_cleanup_on_untracked_capture_failure
case_cleanup_on_combine_failure

echo "implement-review-artifacts.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
