#!/usr/bin/env bash
# Contract test for ticket #695 -- the board lifecycle labels have ONE home,
# flow/docs/board-labels.md (its `## Labels` table: Label | Color |
# Description | Applied by). Every `gh label create "<name>" ... --color "<c>"
# --description "<d>"` literal copied into a skill (flow/skills/**/*.md and
# flow/skills/**/*.sh) must stay identical to that table's row, so the inline
# allow-listed commands and the shell appliers can never drift from the
# canonical colors/descriptions.
#
# What it enforces:
#   - the canonical table parses (non-vacuity: zero rows fails), covers all 13
#     lifecycle labels, and every row has a hex color, a description and an
#     "Applied by" cell;
#   - every real literal under flow/skills matches its row exactly, names a
#     label that is in the table, and carries both --color and --description;
#     only a `<...>` placeholder NAME (configure's create loop) is skipped, and
#     it is not counted; a real name with a placeholder/odd color or
#     description is still checked;
#   - the scan finds at least one real literal, and finds the hard-wrapped
#     `automerge:ok` literal in refine/codex.md (the wrapped-literal edge case
#     must not silently escape the check);
#   - every skill file holding a `gh label create "..."` literal points at the
#     plugin's docs/board-labels.md;
#   - configure (SKILL.md plus phases/ and references/) no longer carries the
#     color table or the "Applied by / Meaning" table (nor a "label table
#     above" pointer), and refine no longer cites the stale
#     configure/SKILL.md:715;
#   - configure step 3c (phases/guidance-settings.md since #1343), refine/SKILL.md
#     and refine/codex.md, which read colors/descriptions from the doc, say to
#     stop rather than guess a color when the doc cannot be read; step 3c also
#     keeps its exact `gh label create "<name>" ...` literal and its pointer to
#     the plugin's docs/board-labels.md;
#   - flow/README.md keeps its generated-repo git-workflow.md tree entries and
#     flow/docs/git-workflow.md stays deleted;
#   - the doc itself carries the ensure-then-add procedure and the "How
#     existence is ensured" section.
#
# Idiom of flow/tests/template-stop-rule-contract.test.sh: `set -uo
# pipefail`, a `failures` counter, pure `extract_*` helpers (no fail() side
# effect, safe inside $(...), compliant with
# read-helper-purity-contract.test.sh), and non-vacuity self-tests on fixtures
# under a checked `mktemp -d`. The scan's checks record problems in PROBLEMS
# instead of calling fail(), so the self-tests can assert the exact problem
# that was caught. `find` exit status is checked and its stderr is not
# suppressed (docs/shell-scripting-gotchas.md). Auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "board-labels-drift-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "board-labels-drift-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
LABELS_DOC="${FLOW_DIR}/docs/board-labels.md"
SKILLS_DIR="${FLOW_DIR}/skills"
CONFIGURE_SKILL="${SKILLS_DIR}/configure/SKILL.md"
REFINE_SKILL="${SKILLS_DIR}/refine/SKILL.md"
REFINE_CODEX_REL="refine/codex.md"
CLOSE_PARENT_REL="close/scripts/close-parent.sh"
FLOW_AGENTS="${FLOW_DIR}/AGENTS.md"
DOC_REF='board-labels.md'
TABLE_HEADING='## Labels'
TABLE_HEADER_ROW='| Label | Color | Description | Applied by |'
REQUIRED_LABELS=(
  "Working" "Refined" "Planned" "In Review" "Implemented" "Input Needed"
  "Followup" "Browser" "ui:visual-check" "automerge:ok"
  "dispatch-failed" "plan-invalid" "reconcile-stuck"
)
failures=0
PROBLEMS=""
# Field separator for the extractors' output: the ASCII unit separator, a
# non-whitespace IFS character, so `read` keeps empty fields.
SEP=$'\037'

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
add_problem() { PROBLEMS+="$1"$'\n'; }

WORK="$(mktemp -d)" || { echo "board-labels-drift-contract.test.sh: failed to create work directory." >&2; exit 2; }
[[ -n "${WORK}" && -d "${WORK}" ]] || { echo "board-labels-drift-contract.test.sh: work directory missing after mktemp." >&2; exit 2; }
cleanup() { rm -rf "${WORK}"; }
trap cleanup EXIT

# extract_table_rows_raw <content> -- pure, fence-aware. Prints one
# name|color|description|applied-by line per data row of the
# markdown table inside the "## Labels" section (bounded to the next "## "
# heading; lines inside a code fence are ignored). Fields are separated by SEP.
# Backticks are stripped from name and color; whitespace is trimmed and
# collapsed. The header and the |---| separator rows are skipped. Safe inside
# $(...).
extract_table_rows_raw() {
  awk -v want="${TABLE_HEADING}" '
    function clean(s) {
      gsub(/`/, "", s)
      gsub(/[ \t]+/, " ", s)
      sub(/^ /, "", s)
      sub(/ $/, "", s)
      return s
    }
    /^```/ { infence = !infence; next }
    infence { next }
    /^## / { on = ($0 == want); next }
    on && /^\|/ {
      split($0, f, "|")
      name = clean(f[2])
      if (name == "" || name == "Label" || name ~ /^[ :-]+$/) next
      printf "%s\037%s\037%s\037%s\n", name, clean(f[3]), clean(f[4]), clean(f[5])
    }
  ' <<<"$1"
}

# extract_literals_raw <content> -- pure. Joins every line of <content> and
# collapses whitespace (so a literal hard-wrapped across source lines is still
# seen), then prints one name|color|description|has-color|has-description
# line (fields separated by SEP) per `gh label create "<name>" ...` occurrence.
# Options after the name are parsed as `--flag "quoted value"` / `--flag word`
# pairs, in any order. Safe inside $(...).
extract_literals_raw() {
  awk '
    { s = s " " $0 }
    END {
      gsub(/[ \t\r]+/, " ", s)
      key = "gh label create \""
      while ((i = index(s, key)) > 0) {
        s = substr(s, i + length(key))
        j = index(s, "\"")
        if (j == 0) break
        name = substr(s, 1, j - 1)
        rest = substr(s, j + 1)
        color = ""; desc = ""; hc = 0; hd = 0
        while (1) {
          sub(/^ +/, "", rest)
          if (substr(rest, 1, 2) != "--") break
          sp = index(rest, " ")
          if (sp == 0) break
          flag = substr(rest, 1, sp - 1)
          rest = substr(rest, sp + 1)
          sub(/^ +/, "", rest)
          if (substr(rest, 1, 1) == "\"") {
            rest = substr(rest, 2)
            q = index(rest, "\"")
            if (q == 0) { val = rest; rest = "" } else { val = substr(rest, 1, q - 1); rest = substr(rest, q + 1) }
          } else {
            sp = index(rest, " ")
            if (sp == 0) { val = rest; rest = "" } else { val = substr(rest, 1, sp - 1); rest = substr(rest, sp + 1) }
          }
          if (flag == "--color") { color = val; hc = 1 }
          else if (flag == "--description") { desc = val; hd = 1 }
        }
        printf "%s\037%s\037%s\037%d\037%d\n", name, color, desc, hc, hd
      }
    }
  ' <<<"$1"
}

# lookup_row_raw <table-rows> <name> -- pure; prints the first row for <name>.
lookup_row_raw() {
  awk -F"${SEP}" -v n="$2" '$1 == n { print; exit }' <<<"$1"
}

# is_placeholder <value> -- true for a `<...>` template token.
is_placeholder() { [[ "$1" == "<"*">" ]]; }

# check_table <table-rows> -- records problems with the canonical table:
# zero rows (non-vacuity), a missing required label, a duplicate name, a
# non-hex color, an empty description or an empty "Applied by" cell.
check_table() {
  local table="$1" name color desc applied dups label
  if [[ -z "${table}" ]]; then
    add_problem "table: '${TABLE_HEADING}' parsed to zero rows (missing doc, heading or table)"
    return 0
  fi
  while IFS="${SEP}" read -r name color desc applied; do
    [[ "${color}" =~ ^[0-9A-Fa-f]{6}$ ]] || add_problem "table: '${name}' has a non-hex color: [${color}]"
    [[ -n "${desc}" ]] || add_problem "table: '${name}' has an empty description"
    [[ -n "${applied}" ]] || add_problem "table: '${name}' has an empty 'Applied by' cell"
  done <<<"${table}"
  dups="$(awk -F"${SEP}" 'seen[$1]++ { print $1 }' <<<"${table}")"
  [[ -z "${dups}" ]] || add_problem "table: duplicate label row(s): ${dups//$'\n'/, }"
  for label in "${REQUIRED_LABELS[@]}"; do
    [[ -n "$(lookup_row_raw "${table}" "${label}")" ]] || add_problem "table: required label '${label}' has no row"
  done
}

# check_literals <table-rows> <literals> <origin> -- records problems for each
# real literal: missing --color/--description, unknown label, color mismatch,
# description mismatch. `<...>` placeholder literals are skipped.
check_literals() {
  local table="$1" literals="$2" origin="$3" name color desc hc hd row want_color want_desc
  [[ -n "${literals}" ]] || return 0
  while IFS="${SEP}" read -r name color desc hc hd; do
    if is_placeholder "${name}"; then
      continue
    fi
    if [[ "${hc}" != "1" || "${hd}" != "1" ]]; then
      add_problem "${origin}: literal for '${name}' lacks --color and/or --description"
      continue
    fi
    row="$(lookup_row_raw "${table}" "${name}")"
    if [[ -z "${row}" ]]; then
      add_problem "${origin}: unknown label '${name}' (no row in ${TABLE_HEADING} table)"
      continue
    fi
    IFS="${SEP}" read -r _ want_color want_desc _ <<<"${row}"
    [[ "${color}" == "${want_color}" ]] || add_problem "${origin}: color mismatch for '${name}': literal [${color}] vs table [${want_color}]"
    [[ "${desc}" == "${want_desc}" ]] || add_problem "${origin}: description mismatch for '${name}': literal [${desc}] vs table [${want_desc}]"
  done <<<"${literals}"
}

# SCAN_LITERAL_FILES: newline-separated skills-relative paths whose scan found
# at least one literal. SCAN_LITERAL_COUNT: total literals found.
SCAN_LITERAL_FILES=""
SCAN_LITERAL_COUNT=0

# scan_skills <table-rows> <skills-dir> -- lists every *.md and *.sh under <skills-dir>
# (find status checked, stderr not suppressed), extracts each file's literals
# and records problems. Not for use inside $(...) (it sets globals).
scan_skills() {
  local table="$1" dir="$2" raw list file rel content literals real count
  SCAN_LITERAL_FILES=""
  SCAN_LITERAL_COUNT=0
  raw="$(mktemp "${WORK}/scan-raw.XXXXXX")" || { add_problem "scan: mktemp failed"; return 0; }
  list="$(mktemp "${WORK}/scan-list.XXXXXX")" || { add_problem "scan: mktemp failed"; return 0; }
  if ! find "${dir}" -type f \( -name '*.md' -o -name '*.sh' \) -print0 > "${raw}"; then
    add_problem "scan: find failed under ${dir}"
    return 0
  fi
  if ! LC_ALL=C sort -z "${raw}" > "${list}"; then
    add_problem "scan: sort failed"
    return 0
  fi
  while IFS= read -r -d '' file; do
    rel="${file#"${dir}/"}"
    if ! content="$(cat "${file}")"; then
      add_problem "scan: cannot read ${rel}"
      continue
    fi
    if ! literals="$(extract_literals_raw "${content}")"; then
      add_problem "scan: literal extraction failed for ${rel}"
      continue
    fi
    [[ -n "${literals}" ]] || continue
    grep -qF -- "${DOC_REF}" <<<"${content}" || add_problem "${rel}: holds a 'gh label create \"...\"' literal but does not reference the plugin's docs/${DOC_REF}"
    # Only a placeholder NAME (the template) is skipped, and it does not count
    # toward the non-vacuity floor.
    real="$(awk -F"${SEP}" '$1 !~ /^<.*>$/' <<<"${literals}")" || { add_problem "scan: literal filter failed for ${rel}"; continue; }
    [[ -n "${real}" ]] || continue
    count="$(wc -l <<<"${real}" | tr -d '[:space:]')"
    SCAN_LITERAL_COUNT=$((SCAN_LITERAL_COUNT + count))
    SCAN_LITERAL_FILES+="${rel}"$'\n'
    if [[ -n "${table}" ]]; then
      check_literals "${table}" "${real}" "${rel}"
    fi
  done < "${list}"
  if [[ "${SCAN_LITERAL_COUNT}" -eq 0 ]]; then
    add_problem "scan: found zero 'gh label create \"...\"' literals under ${dir} (vacuous scan)"
  fi
}

# --- Self-tests (non-vacuity): fixtures under the checked mktemp -d ----------
FIXTURE_TABLE_DOC="$(printf '%s\n' \
  '# Board labels' \
  '' \
  '## Before' \
  '| `Decoy` | `000000` | outside the section | nobody |' \
  '' \
  "${TABLE_HEADING}" \
  '' \
  "${TABLE_HEADER_ROW}" \
  '|---|---|---|---|' \
  '| `Working` | `FBCA04` | Actively being refined or implemented | refine |' \
  '| `In Review` | `A2EEEF` | PR open, under review / CI running | implement |' \
  '' \
  '```text' \
  '## Fenced heading must not end the section' \
  '| `FencedRow` | `111111` | inside a fence | nobody |' \
  '```' \
  '' \
  '### Sub-heading stays inside the section' \
  '| `Followup` | `C5DEF5` | Deferred item | refine |' \
  '' \
  '## After' \
  '| `Late` | `222222` | past the section | nobody |')"
FIXTURE_ROWS="$(extract_table_rows_raw "${FIXTURE_TABLE_DOC}")"
grep -qF $'Working\037FBCA04\037Actively being refined or implemented\037refine' <<<"${FIXTURE_ROWS}" || fail "table extractor self-test: dropped a row or mis-split its columns: ${FIXTURE_ROWS}"
grep -qF $'In Review\037A2EEEF' <<<"${FIXTURE_ROWS}" || fail "table extractor self-test: dropped the second row"
grep -qF $'Followup\037C5DEF5' <<<"${FIXTURE_ROWS}" || fail "table extractor self-test: a fenced '## ' line or a '### ' sub-heading ended the section early"
grep -qF 'Decoy' <<<"${FIXTURE_ROWS}" && fail "table extractor self-test: captured a row above the heading"
grep -qF 'Late' <<<"${FIXTURE_ROWS}" && fail "table extractor self-test: ran past the next '## ' heading"
grep -qF 'FencedRow' <<<"${FIXTURE_ROWS}" && fail "table extractor self-test: captured a row inside a code fence"
grep -qE '^(Label|---)' <<<"${FIXTURE_ROWS}" && fail "table extractor self-test: kept the header or separator row"
[[ -z "$(extract_table_rows_raw '# no table here')" ]] || fail "table extractor self-test: produced rows from a doc with no '${TABLE_HEADING}' section"

# Literal extractor: single-line, wrapped, placeholder, no-quote and
# missing-flag shapes.
FIXTURE_LITERAL_TEXT="$(printf '%s\n' \
  'gh label create "Working" --repo <owner>/<repo> --color "FBCA04" --description "Actively being refined or implemented" 2>/dev/null || true' \
  'ensure the label exists (`gh label create' \
  '"automerge:ok" --repo <owner>/<repo> --color "006B75" --description "Repository policy' \
  'granted hands-off merge" 2>/dev/null ||' \
  'true`)' \
  'gh label create "<name>" --repo <owner>/<repo> --color "<color>" --description "<description>"' \
  'plain prose: gh label create … and Bash(gh label create:*) are not literals' \
  'gh label create "Bare" --repo <owner>/<repo>')"
FIXTURE_LITERALS="$(extract_literals_raw "${FIXTURE_LITERAL_TEXT}")"
grep -qF $'Working\037FBCA04\037Actively being refined or implemented\0371\0371' <<<"${FIXTURE_LITERALS}" || fail "literal extractor self-test: missed a single-line literal: ${FIXTURE_LITERALS}"
grep -qF $'automerge:ok\037006B75\037Repository policy granted hands-off merge\0371\0371' <<<"${FIXTURE_LITERALS}" || fail "literal extractor self-test: missed a hard-wrapped literal (whitespace was not normalized): ${FIXTURE_LITERALS}"
grep -qF $'<name>\037<color>\037<description>\0371\0371' <<<"${FIXTURE_LITERALS}" || fail "literal extractor self-test: did not surface the placeholder literal for skipping"
grep -qF $'Bare\037\037\0370\0370' <<<"${FIXTURE_LITERALS}" || fail "literal extractor self-test: did not report a quoted literal lacking --color/--description"
[[ "$(wc -l <<<"${FIXTURE_LITERALS}" | tr -d '[:space:]')" == "4" ]] || fail "literal extractor self-test: expected exactly 4 literals (prose mentions without a quoted name must not count), got: ${FIXTURE_LITERALS}"

# Scan self-tests against fixture skills trees. Each case builds a tree, runs
# scan_skills, and asserts the exact problem (or none).
FIXTURE_TABLE_DOC_FULL="$(printf '%s\n' \
  "${TABLE_HEADING}" "${TABLE_HEADER_ROW}" '|---|---|---|---|' \
  '| `Working` | `FBCA04` | Actively being refined or implemented | refine |' \
  '| `Implemented` | `6F42C1` | PR merged — done | babysit |' \
  '| `automerge:ok` | `006B75` | Repository policy granted hands-off merge | refine |')"
FIXTURE_TABLE="$(extract_table_rows_raw "${FIXTURE_TABLE_DOC_FULL}")"

# run_scan_case <case-name> <file-content> [<skills-relative-path>] -- builds
# <WORK>/<case>/skills/<path> (default x/SKILL.md) holding <file-content>, scans
# it, leaves the problems in PROBLEMS.
run_scan_case() {
  local case_dir="${WORK}/$1" rel="${3:-x/SKILL.md}"
  mkdir -p "$(dirname "${case_dir}/skills/${rel}")" || { add_problem "self-test: mkdir failed for $1"; return 0; }
  printf '%s\n' "$2" > "${case_dir}/skills/${rel}" || { add_problem "self-test: write failed for $1"; return 0; }
  PROBLEMS=""
  scan_skills "${FIXTURE_TABLE}" "${case_dir}/skills"
}

MATCHING='See the plugin docs/board-labels.md.
gh label create "Working" --repo <owner>/<repo> --color "FBCA04" --description "Actively being refined or implemented" 2>/dev/null || true
gh label create "<name>" --repo <owner>/<repo> --color "<color>" --description "<description>"'
run_scan_case matching "${MATCHING}"
[[ -z "${PROBLEMS}" ]] || fail "scan self-test: a matching fixture was flagged: ${PROBLEMS}"
[[ "${SCAN_LITERAL_COUNT}" -eq 1 ]] || fail "scan self-test: matching fixture expected 1 counted literal (the real one; the <name> placeholder must not count toward the non-vacuity floor), got ${SCAN_LITERAL_COUNT}"

# A placeholder NAME is the only thing that skips a literal: it is not checked
# and not counted, so a file holding only the template is still a vacuous scan.
run_scan_case placeholder-only 'See docs/board-labels.md.
gh label create "<name>" --repo <owner>/<repo> --color "<color>" --description "<description>"'
[[ "${SCAN_LITERAL_COUNT}" -eq 0 ]] || fail "scan self-test: a placeholder-name literal was counted toward the non-vacuity floor (got ${SCAN_LITERAL_COUNT})"
grep -qF "zero 'gh label create" <<<"${PROBLEMS}" || fail "scan self-test: a scan holding only placeholder-name literals did not fail as vacuous: [${PROBLEMS}]"

# A real name with a placeholder/odd color or description must be checked, not skipped.
run_scan_case placeholder-color 'See docs/board-labels.md.
gh label create "Working" --repo <owner>/<repo> --color "<color>" --description "Actively being refined or implemented"'
grep -qF "color mismatch for 'Working'" <<<"${PROBLEMS}" || fail "scan self-test: a real name with a placeholder color escaped the check: [${PROBLEMS}]"
run_scan_case placeholder-description 'See docs/board-labels.md.
gh label create "Working" --repo <owner>/<repo> --color "FBCA04" --description "<description>"'
grep -qF "description mismatch for 'Working'" <<<"${PROBLEMS}" || fail "scan self-test: a real name with a placeholder description escaped the check: [${PROBLEMS}]"

# An extractor failure must surface as a problem, not look like "no literals".
if EXTRACT_LITERALS_DEF="$(declare -f extract_literals_raw)"; then
  extract_literals_raw() { return 1; }
  run_scan_case extractor-failure "${MATCHING}"
  eval "${EXTRACT_LITERALS_DEF}"
  grep -qF "literal extraction failed" <<<"${PROBLEMS}" || fail "scan self-test: a failing literal extractor was indistinguishable from 'no literals': [${PROBLEMS}]"
else
  fail "scan self-test: could not capture extract_literals_raw for the extractor-failure case"
fi

run_scan_case color-mismatch "${MATCHING/FBCA04/FBCA05}"
grep -qF "color mismatch for 'Working'" <<<"${PROBLEMS}" || fail "scan self-test: an injected color mismatch was not caught: [${PROBLEMS}]"

run_scan_case description-mismatch "${MATCHING/Actively being/Actively not being}"
grep -qF "description mismatch for 'Working'" <<<"${PROBLEMS}" || fail "scan self-test: an injected description mismatch was not caught: [${PROBLEMS}]"

run_scan_case unknown-label "${MATCHING/\"Working\"/\"Mystery\"}"
grep -qF "unknown label 'Mystery'" <<<"${PROBLEMS}" || fail "scan self-test: an injected unknown label was not caught: [${PROBLEMS}]"

run_scan_case missing-flags 'See docs/board-labels.md.
gh label create "Working" --repo <owner>/<repo> --color "FBCA04" 2>/dev/null || true'
grep -qF "lacks --color and/or --description" <<<"${PROBLEMS}" || fail "scan self-test: a literal lacking --description was not caught: [${PROBLEMS}]"

run_scan_case no-doc-reference 'gh label create "Working" --repo <owner>/<repo> --color "FBCA04" --description "Actively being refined or implemented" 2>/dev/null || true'
grep -qF "does not reference the plugin's docs/${DOC_REF}" <<<"${PROBLEMS}" || fail "scan self-test: a literal with no pointer to the label doc was not caught: [${PROBLEMS}]"

WRAPPED='See docs/board-labels.md.
ensure the label exists (`gh label create
"automerge:ok" --repo <owner>/<repo> --color "006B75" --description "Repository policy granted
hands-off merge" 2>/dev/null ||
true`)'
run_scan_case wrapped-matching "${WRAPPED}"
[[ -z "${PROBLEMS}" ]] || fail "scan self-test: a matching wrapped literal was flagged: [${PROBLEMS}]"
[[ "${SCAN_LITERAL_COUNT}" -eq 1 ]] || fail "scan self-test: the wrapped literal was not counted (got ${SCAN_LITERAL_COUNT})"
run_scan_case wrapped-mismatch "${WRAPPED/006B75/006B76}"
grep -qF "color mismatch for 'automerge:ok'" <<<"${PROBLEMS}" || fail "scan self-test: a wrapped literal with an injected color mismatch escaped the check: [${PROBLEMS}]"
run_scan_case wrapped-description "${WRAPPED/hands-off merge\"/hands-off merger\"}"
grep -qF "description mismatch for 'automerge:ok'" <<<"${PROBLEMS}" || fail "scan self-test: a wrapped literal with an injected description mismatch escaped the check: [${PROBLEMS}]"

# Shell appliers (#1337): a *.sh under the skills tree is scanned like a *.md.
SH_MATCHING='# Colors and descriptions: the plugin docs/board-labels.md.
gh label create "Implemented" --repo "${REPO}" --color "6F42C1" --description "PR merged — done" >/dev/null 2>&1 || true'
run_scan_case sh-matching "${SH_MATCHING}" close/scripts/apply.sh
[[ -z "${PROBLEMS}" ]] || fail "scan self-test: a matching .sh fixture was flagged: [${PROBLEMS}]"
[[ "${SCAN_LITERAL_COUNT}" -eq 1 ]] || fail "scan self-test: a matching .sh fixture expected 1 counted literal (the .sh scan is missing or the em dash did not match), got ${SCAN_LITERAL_COUNT}"
grep -qxF -- "close/scripts/apply.sh" <<<"${SCAN_LITERAL_FILES}" || fail "scan self-test: the .sh fixture was not listed in the scanned literal files: [${SCAN_LITERAL_FILES}]"

run_scan_case sh-color-mismatch "${SH_MATCHING/6F42C1/0E8A16}" close/scripts/apply.sh
grep -qF "color mismatch for 'Implemented'" <<<"${PROBLEMS}" || fail "scan self-test: a .sh literal carrying a wrong color escaped the check: [${PROBLEMS}]"

run_scan_case sh-description-mismatch "${SH_MATCHING/PR merged — done/PR merged}" close/scripts/apply.sh
grep -qF "description mismatch for 'Implemented'" <<<"${PROBLEMS}" || fail "scan self-test: a .sh literal carrying a truncated description escaped the check: [${PROBLEMS}]"

run_scan_case sh-no-doc-reference 'gh label create "Implemented" --repo "${REPO}" --color "6F42C1" --description "PR merged — done" >/dev/null 2>&1 || true' close/scripts/apply.sh
grep -qF "does not reference the plugin's docs/${DOC_REF}" <<<"${PROBLEMS}" || fail "scan self-test: a .sh literal with no pointer to the label doc was not caught: [${PROBLEMS}]"

# maintain/scripts/check.sh prints an escaped hint (`create \"$l\" ... --color <hex>`);
# it is not a literal, so it must record no lacks-flags / unknown-label problem.
run_scan_case sh-escaped-hint 'echo "  fix: gh label create \"$l\" --repo \"$REPO\" --color <hex>"' maintain/scripts/check.sh
grep -qF "lacks --color" <<<"${PROBLEMS}" && fail "scan self-test: an escaped check.sh-style hint was counted as a literal lacking flags: [${PROBLEMS}]"
grep -qF "unknown label" <<<"${PROBLEMS}" && fail "scan self-test: an escaped check.sh-style hint was counted as an unknown-label literal: [${PROBLEMS}]"
[[ "${SCAN_LITERAL_COUNT}" -eq 0 ]] || fail "scan self-test: an escaped check.sh-style hint was counted as a literal (got ${SCAN_LITERAL_COUNT})"

run_scan_case no-literals 'No label commands in this skill at all.'
grep -qF "zero 'gh label create" <<<"${PROBLEMS}" || fail "scan self-test: a scan with zero literals did not fail (vacuous pass): [${PROBLEMS}]"

PROBLEMS=""
check_table ""
grep -qF "parsed to zero rows" <<<"${PROBLEMS}" || fail "table self-test: an empty table did not fail (vacuous pass): [${PROBLEMS}]"
PROBLEMS=""
check_table "${FIXTURE_TABLE}"
grep -qF "required label 'Refined' has no row" <<<"${PROBLEMS}" || fail "table self-test: a table missing required labels did not fail: [${PROBLEMS}]"
PROBLEMS=""
check_table "$(printf 'Working\037ZZZZZZ\037\037\nWorking\037FBCA04\037desc\037applier\n')"
grep -qF "non-hex color" <<<"${PROBLEMS}" || fail "table self-test: a non-hex color was not caught: [${PROBLEMS}]"
grep -qF "empty description" <<<"${PROBLEMS}" || fail "table self-test: an empty description was not caught: [${PROBLEMS}]"
grep -qF "empty 'Applied by' cell" <<<"${PROBLEMS}" || fail "table self-test: an empty 'Applied by' cell was not caught: [${PROBLEMS}]"
grep -qF "duplicate label row(s): Working" <<<"${PROBLEMS}" || fail "table self-test: a duplicate row was not caught: [${PROBLEMS}]"

# --- The contract: the real doc and the real skills tree ---------------------
if ! DOC_CONTENT="$(cat "${LABELS_DOC}")"; then
  fail "flow/docs/board-labels.md: not found/unreadable (the canonical label table must exist)"
  DOC_CONTENT=""
fi
if [[ -n "${DOC_CONTENT}" ]]; then
  grep -qxF -- "${TABLE_HEADER_ROW}" <<<"${DOC_CONTENT}" || fail "flow/docs/board-labels.md: table header must be exactly '${TABLE_HEADER_ROW}' (column order is relied on by refine-automerge-grant.test.sh row prefixes)"
  grep -qF 'How existence is ensured' <<<"${DOC_CONTENT}" || fail "flow/docs/board-labels.md: missing the 'How existence is ensured' section (skill fallbacks vs CLI vs reconciler)"
  grep -qF 'ensure-then-add' <<<"${DOC_CONTENT}" || fail "flow/docs/board-labels.md: missing the ensure-then-add procedure"
fi

REAL_TABLE="$(extract_table_rows_raw "${DOC_CONTENT}")"
PROBLEMS=""
check_table "${REAL_TABLE}"
scan_skills "${REAL_TABLE}" "${SKILLS_DIR}"
if [[ "${SCAN_LITERAL_COUNT}" -gt 0 ]]; then
  grep -qxF -- "${REFINE_CODEX_REL}" <<<"${SCAN_LITERAL_FILES}" || add_problem "scan: no literal found in skills/${REFINE_CODEX_REL} -- its hard-wrapped automerge:ok literal must be seen by the drift check"
  grep -qxF -- "${CLOSE_PARENT_REL}" <<<"${SCAN_LITERAL_FILES}" || add_problem "scan: no literal found in skills/${CLOSE_PARENT_REL} -- its Implemented self-heal must be seen by the drift check (the scan must cover *.sh)"
fi
if [[ -n "${PROBLEMS}" ]]; then
  while IFS= read -r problem; do
    [[ -n "${problem}" ]] && fail "${problem}"
  done <<<"${PROBLEMS}"
fi

# --- Single-source guards for the label sites that must stop owning a copy ---
# Step 3c (the create loop) moved to phases/guidance-settings.md in #1343, so the
# table guards scan the whole configure aggregate (SKILL.md + phases/ +
# references/): a color table or a stale pointer in any of them is a violation.
# The aggregate is built with every read checked, and the scan is non-vacuous:
# SKILL.md plus at least the 9 phase and 3 reference files must be present.
GUIDANCE_PHASE_REL="phases/guidance-settings.md"
GUIDANCE_PHASE="${SKILLS_DIR}/configure/${GUIDANCE_PHASE_REL}"
STEP3C_LITERAL='gh label create "<name>" --repo <owner>/<repo> --color "<color>" --description "<description>"'
CONFIGURE_CONTENT=""
CONFIGURE_PART_COUNT=0
if CONFIGURE_CONTENT="$(cat "${CONFIGURE_SKILL}")"; then
  for configure_part in "${SKILLS_DIR}"/configure/phases/*.md "${SKILLS_DIR}"/configure/references/*.md; do
    [[ -f "${configure_part}" ]] || continue
    if ! configure_part_content="$(cat "${configure_part}")"; then
      fail "skills/configure/${configure_part#"${SKILLS_DIR}/configure/"}: not found/unreadable"
      continue
    fi
    CONFIGURE_CONTENT+=$'\n'"${configure_part_content}"
    CONFIGURE_PART_COUNT=$((CONFIGURE_PART_COUNT + 1))
  done
  [[ "${CONFIGURE_PART_COUNT}" -ge 15 ]] || fail "skills/configure/**: aggregate is vacuous, scanned ${CONFIGURE_PART_COUNT} phases/references files (expected at least 15 after #1344: 11 phases + 4 references)"
  grep -qE '^[[:space:]]*\| `[^`]+` \| `[0-9A-Fa-f]{6}` \|' <<<"${CONFIGURE_CONTENT}" && fail "skills/configure/**: still carries a label color table row (the table lives only in docs/${DOC_REF})"
  grep -qE '^[[:space:]]*\| Label \| (Color|Applied by) \|' <<<"${CONFIGURE_CONTENT}" && fail "skills/configure/**: still carries a label table header (the 'Applied by / Meaning' and color tables live only in docs/${DOC_REF})"
  grep -qF -- "${DOC_REF}" <<<"${CONFIGURE_CONTENT}" || fail "skills/configure/**: must point at the plugin's docs/${DOC_REF}"
  grep -qF 'label table above' <<<"${CONFIGURE_CONTENT}" && fail "skills/configure/**: still points at a 'label table above' that no longer exists (point at the automerge:ok row of the plugin's docs/${DOC_REF})"
else
  fail "skills/configure/SKILL.md: not found/unreadable"
fi

# Step 3c keeps its exact `gh label create` literal and its pointer to the
# plugin's docs/board-labels.md, now in phases/guidance-settings.md (#1343). The
# literal is a `<...>` placeholder template, so the drift scan above skips it;
# these cases pin it explicitly, and require it nowhere else in the configure
# markdown (paired with the guidance-settings.md existence check).
if GUIDANCE_CONTENT="$(cat "${GUIDANCE_PHASE}")"; then
  grep -qF -- "${STEP3C_LITERAL}" <<<"${GUIDANCE_CONTENT}" || fail "skills/configure/${GUIDANCE_PHASE_REL}: step 3c must keep its exact create literal [${STEP3C_LITERAL}]"
  grep -qF -- "plugin's \`docs/${DOC_REF}\`" <<<"${GUIDANCE_CONTENT}" || fail "skills/configure/${GUIDANCE_PHASE_REL}: step 3c must point at the plugin's docs/${DOC_REF}"
  STEP3C_LITERAL_COUNT="$(grep -oF -- "${STEP3C_LITERAL}" <<<"${CONFIGURE_CONTENT}" | wc -l | tr -d '[:space:]')"
  [[ "${STEP3C_LITERAL_COUNT}" -eq 1 ]] || fail "skills/configure/**: the step 3c create literal must occur exactly once across the configure markdown (in ${GUIDANCE_PHASE_REL}), found ${STEP3C_LITERAL_COUNT}"
else
  fail "skills/configure/${GUIDANCE_PHASE_REL}: not found/unreadable (step 3c moved there in #1343)"
fi
if REFINE_CONTENT="$(cat "${REFINE_SKILL}")"; then
  grep -qF 'configure/SKILL.md:715' <<<"${REFINE_CONTENT}" && fail "skills/refine/SKILL.md: still cites the stale 'configure/SKILL.md:715' line"
  grep -qF 'lifecycle table in `/cenci:configure`' <<<"${REFINE_CONTENT}" && fail "skills/refine/SKILL.md: still sends readers to the lifecycle table in /cenci:configure instead of docs/${DOC_REF}"
else
  fail "skills/refine/SKILL.md: not found/unreadable"
fi
# The three sites that read colors/descriptions FROM the doc (no inline literal
# carrying full values) must fail closed when the doc cannot be read.
FAIL_CLOSED_PHRASE='never guess a color'
for readsite in "configure/${GUIDANCE_PHASE_REL}" "refine/SKILL.md" "${REFINE_CODEX_REL}"; do
  if READSITE_CONTENT="$(cat "${SKILLS_DIR}/${readsite}")"; then
    grep -qF -- "${FAIL_CLOSED_PHRASE}" <<<"${READSITE_CONTENT}" || fail "skills/${readsite}: reads label colors/descriptions from docs/${DOC_REF} but lacks the fail-closed wording '${FAIL_CLOSED_PHRASE}' (an unreadable doc must stop and report, not fall back to a guessed color)"
  else
    fail "skills/${readsite}: not found/unreadable"
  fi
done
if FLOW_AGENTS_CONTENT="$(cat "${FLOW_AGENTS}")"; then
  grep -qF -- "docs/${DOC_REF}" <<<"${FLOW_AGENTS_CONTENT}" || fail "flow/AGENTS.md: Reference Docs must index docs/${DOC_REF}"
else
  fail "flow/AGENTS.md: not found/unreadable"
fi

# --- #695 single-source guard: flow's README names the repo-root git-workflow doc ---
# flow no longer ships docs/git-workflow.md; the only copy is the repo-root
# doc, which flow/README.md must point at with the same `<repo-root>/` form
# flow/AGENTS.md uses. The generated-repo tree entries (templates/docs) stay.
FLOW_README="${FLOW_DIR}/README.md"
ROOT_GIT_WORKFLOW_REF='<repo-root>/docs/git-workflow.md'
if FLOW_README_CONTENT="$(cat "${FLOW_README}")"; then
  grep -qF -- "${ROOT_GIT_WORKFLOW_REF}" <<<"${FLOW_README_CONTENT}" || fail "flow/README.md: must name the repo-root doc '${ROOT_GIT_WORKFLOW_REF}' in its flow docs list"
  grep -qF -- 'flow/docs/git-workflow.md' <<<"${FLOW_README_CONTENT}" && fail "flow/README.md: must not name the removed flow/docs/git-workflow.md"
  # Anchored on the two generated-repo tree entries plus the templates/docs
  # tree entry (`└── git-workflow.md` lines), so deleting any of them fails.
  TREE_ENTRY_COUNT="$(grep -cE '^[│ ]*[├└]── git-workflow\.md' <<<"${FLOW_README_CONTENT}")"
  [[ "${TREE_ENTRY_COUNT}" -ge 3 ]] || fail "flow/README.md: lost a generated-repo git-workflow.md tree entry (expected the 2 generated-repo trees and templates/docs, found ${TREE_ENTRY_COUNT} '└── git-workflow.md' lines)"
else
  fail "flow/README.md: not found/unreadable"
fi
[[ ! -e "${FLOW_DIR}/docs/git-workflow.md" ]] || fail "flow/docs/git-workflow.md must stay deleted (the only copy is the repo-root docs/git-workflow.md)"

if (( failures > 0 )); then
  echo "board-labels-drift-contract.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "board-labels-drift-contract.test.sh: all checks passed"
