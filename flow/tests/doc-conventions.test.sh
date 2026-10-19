#!/usr/bin/env bash
# Behavioral suite for skills/implement/scripts/doc-conventions.sh -- the
# read-only documentation-convention resolver (#1302). `discover <repo-root>`
# prints only paths and classification lines; `mentions <repo-root> <term>`
# prints the Markdown files that contain a fixed-string term. Every scenario
# builds a throwaway repo under `mktemp -d`; no git, no gh, no network.
#
# Scenarios:
#   P4  nested per-project AGENTS.md files, docs/index.md, nested owner docs and
#       a structured lesson system -> agents/index/owner/lessons lines; a
#       CLAUDE.md next to an AGENTS.md is not listed; lessons fallback variants
#   P5  README-only repo -> `convention: none` then a recommend:/evidence: block;
#       a partial convention (AGENTS.md but no docs/) prints no recommend block
#   P8  `mentions` for a flag documented only in README.md, only in
#       docs/cli/commands.md, and in both; a `-`-leading term is safe
#   P20 the script never writes: AGENTS.md plus an `@AGENTS.md` CLAUDE.md shim
#       stay byte-identical (whole-tree diff) after discover and mentions
#   P21 symlinks that resolve outside the repository (relative, absolute,
#       chained, sibling-prefix, directory, FIFO) are skipped and never read;
#       dangling links and loops are named; inside links are not skipped
#   P30 every fixture body carries a sentinel token that must never reach
#       stdout/stderr, and every discover line carries an allowed prefix
#   Plus usage-error exits and control-character / odd file names.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "doc-conventions.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "doc-conventions.test.sh: failed to resolve flow directory." >&2; exit 2; }
SCRIPT="${FLOW_DIR}/skills/implement/scripts/doc-conventions.sh"
failures=0
checks=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { checks=$((checks+1)); [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { checks=$((checks+1)); [[ -n "$2" ]] || { fail "$3: empty needle"; return; }; [[ "$1" == *"$2"* ]] || fail "$3: expected to contain [$2], got [$1]"; }
assert_not_contains() { checks=$((checks+1)); [[ -n "$2" ]] || { fail "$3: empty needle"; return; }; [[ "$1" != *"$2"* ]] || fail "$3: expected NOT to contain [$2], got [$1]"; }
lines() { printf '%s\n' "$@"; }

[[ -r "${SCRIPT}" ]] || fail "script missing: ${SCRIPT}"
T="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 2; }
[[ -n "${T}" && -d "${T}" ]] || { echo "mktemp -d returned no directory" >&2; exit 2; }
trap 'rm -rf "${T}"' EXIT

SENTINEL="body-sentinel-7f3c9a21"
LINE_RE='^(agents|index|owner|lessons|convention|recommend|evidence|skipped): '
ALL_OUTPUT="${T}/all-output"
: > "${ALL_OUTPUT}" || { echo "could not create ${ALL_OUTPUT}" >&2; exit 2; }

# run_dc [args...] -- runs the script; sets OUT, ERR, ERR_LINES, RC. A hang
# (for example a read of a FIFO the script must never open) is cut off by
# `timeout` and surfaces as RC=124. Optional per-call context, passed as
# prefix assignments: RUN_CWD (directory to run in) and RUN_SHIM (a directory
# of stub commands put first on PATH for the script only).
run_dc() {
  local dir="${RUN_CWD:-.}" shim="${RUN_SHIM:-}"
  if command -v timeout >/dev/null 2>&1; then
    ( cd "${dir}" && { [[ -z "${shim}" ]] || export PATH="${shim}:${PATH}"; } && timeout 30 bash "${SCRIPT}" "$@" >"${T}/out" 2>"${T}/err" ); RC=$?
  else
    ( cd "${dir}" && { [[ -z "${shim}" ]] || export PATH="${shim}:${PATH}"; } && bash "${SCRIPT}" "$@" >"${T}/out" 2>"${T}/err" ); RC=$?
  fi
  if ! OUT="$(cat "${T}/out")"; then fail "run_dc: could not read captured stdout"; OUT=""; fi
  if ! ERR="$(cat "${T}/err")"; then fail "run_dc: could not read captured stderr"; ERR=""; fi
  if ! ERR_LINES="$(wc -l < "${T}/err" | tr -d '[:space:]')"; then fail "run_dc: could not count stderr lines"; ERR_LINES="ERROR"; fi
  printf '%s\n%s\n' "${OUT}" "${ERR}" >> "${ALL_OUTPUT}" || fail "run_dc: could not record output"
}

# check_line_prefixes <label> -- every non-empty line of OUT carries an allowed prefix (P30).
check_line_prefixes() {
  local line
  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    checks=$((checks+1))
    [[ "${line}" =~ ${LINE_RE} ]] || fail "$1: line without an allowed prefix: [${line}]"
  done <<< "${OUT}"
}

# expect_discover <label> <repo-root> <expected stdout> -- exit 0, silent stderr, exact stdout.
expect_discover() {
  run_dc discover "$2"
  assert_eq "${RC}" "0" "$1 exit"
  assert_eq "${OUT}" "$3" "$1 stdout"
  assert_eq "${ERR}" "" "$1 stderr"
  check_line_prefixes "$1"
}

# expect_mentions <label> <repo-root> <term> <expected stdout> -- exit 0, silent stderr, exact stdout.
expect_mentions() {
  run_dc mentions "$2" "$3"
  assert_eq "${RC}" "0" "$1 exit"
  assert_eq "${OUT}" "$4" "$1 stdout"
  assert_eq "${ERR}" "" "$1 stderr"
}

# expect_usage_error <label> [args...] -- exit 2, nothing on stdout, exactly one stderr line.
expect_usage_error() {
  local label="$1"; shift
  run_dc "$@"
  assert_eq "${RC}" "2" "${label} exit"
  assert_eq "${OUT}" "" "${label} stdout"
  assert_eq "${ERR_LINES}" "1" "${label} one stderr line"
}

# Checked fixture helpers.
mkfile() { # <root> <rel> [extra body line]
  local path="$1/$2"
  mkdir -p "${path%/*}" || { fail "could not create directory for $path"; return 1; }
  printf '# %s\n%s\n%s\n' "$2" "${3:-}" "${SENTINEL}" > "${path}" || { fail "could not write $path"; return 1; }
}
mklink() { # <target> <link path>
  mkdir -p "${2%/*}" || { fail "could not create directory for link $2"; return 1; }
  ln -s "$1" "$2" || { fail "could not create symlink $2"; return 1; }
}
mkdirs() { mkdir -p "$1" || { fail "could not create directory $1"; return 1; }; }

# ---------------------------------------------------------------- fixtures --

# P4: nested per-project AGENTS.md, index, nested owners, structured lessons.
N="${T}/nested"
mkfile "$N" README.md
mkfile "$N" AGENTS.md
mkfile "$N" flow/AGENTS.md
mkfile "$N" watch/AGENTS.md
printf '@AGENTS.md\n' > "$N/CLAUDE.md" || fail "could not write nested CLAUDE.md"
printf '@AGENTS.md\n' > "$N/flow/CLAUDE.md" || fail "could not write nested flow/CLAUDE.md"
mkfile "$N" docs/index.md "[cli](cli.md) [setup](setup.md) [api](api/endpoints.md)"
mkfile "$N" docs/cli.md
mkfile "$N" docs/setup.md
mkfile "$N" docs/api/endpoints.md
mkfile "$N" flow/docs/README.md
mkfile "$N" flow/docs/pipeline.md
mkfile "$N" docs/lessons/README.md "admission rules"
mkfile "$N" docs/lessons/2026-01-first-record.md
mkfile "$N" .git/NOTES.md
mkfile "$N" .worktrees/wt/AGENTS.md
mkfile "$N" .worktrees/wt/docs/hidden.md
mkfile "$N" node_modules/pkg/docs/readme.md
mkfile "$N" .plans/1-plan.md
mkfile "$N" docs/notes.txt

# A CLAUDE.md with no sibling AGENTS.md is the legacy router and is listed.
LEG="${T}/legacy"
mkfile "$LEG" CLAUDE.md
mkfile "$LEG" docs/guide.md

# Lesson-system variants.
L_ONLY_ADMISSION="${T}/lessons-admission-only"
mkfile "$L_ONLY_ADMISSION" docs/guide.md
mkfile "$L_ONLY_ADMISSION" docs/lessons/README.md
L_NO_ADMISSION="${T}/lessons-no-admission"
mkfile "$L_NO_ADMISSION" docs/guide.md
mkfile "$L_NO_ADMISSION" docs/lessons/2026-01-record.md
L_LEARNINGS="${T}/lessons-learnings"
mkfile "$L_LEARNINGS" docs/guide.md
mkfile "$L_LEARNINGS" docs/learnings/index.md
mkfile "$L_LEARNINGS" docs/learnings/one.md
L_NO_DOCS="${T}/lessons-outside-docs"
mkfile "$L_NO_DOCS" docs/guide.md
mkfile "$L_NO_DOCS" lessons/README.md
mkfile "$L_NO_DOCS" lessons/one.md

# P5: README-only, empty, and partial-convention repos.
R5="${T}/readme-only"
mkfile "$R5" README.md
EMPTY="${T}/empty"
mkdirs "$EMPTY"
PARTIAL="${T}/partial"
mkfile "$PARTIAL" README.md
mkfile "$PARTIAL" AGENTS.md

# P8: a CLI flag documented in README only, in the guide only, and in both.
FLAG='--frobnicate'
P8_README="${T}/p8-readme"
mkfile "$P8_README" README.md "Use ${FLAG} to enable frobnication."
mkfile "$P8_README" AGENTS.md "Router."
mkfile "$P8_README" docs/cli/commands.md "Commands reference without the flag."
mkfile "$P8_README" node_modules/dep/README.md "${FLAG} inside a pruned tree"
printf 'Use %s here, but this is not Markdown.\n' "${FLAG}" > "${P8_README}/notes.txt" || fail "could not write notes.txt"
P8_GUIDE="${T}/p8-guide"
mkfile "$P8_GUIDE" README.md "See [the CLI guide](docs/cli/commands.md) for every option."
mkfile "$P8_GUIDE" AGENTS.md "Router."
mkfile "$P8_GUIDE" docs/cli/commands.md "Pass ${FLAG} to enable frobnication."
P8_BOTH="${T}/p8-both"
mkfile "$P8_BOTH" README.md "Use ${FLAG} to enable frobnication."
mkfile "$P8_BOTH" AGENTS.md "Router."
mkfile "$P8_BOTH" docs/cli/commands.md "Pass ${FLAG} to enable frobnication."
P8_FIXED="${T}/p8-fixed-string"
mkfile "$P8_FIXED" docs/regex.md "literal a.c here"
mkfile "$P8_FIXED" docs/other.md "abc and a*c here"

# P20: AGENTS.md plus the @AGENTS.md shim.
P20="${T}/p20"
mkfile "$P20" AGENTS.md "Rules with the term shim-term."
printf '@AGENTS.md' > "$P20/CLAUDE.md" || fail "could not write p20 CLAUDE.md"
mkfile "$P20" docs/index.md
mkfile "$P20" docs/topic.md "shim-term documented here"

# P21: symlinks. repo/ is the repository; outside/ and repo-evil/ are not.
LK="${T}/links"
LKR="${LK}/repo"
LKO="${LK}/outside"
LKE="${LK}/repo-evil"
mkfile "$LKR" README.md "readme-unique-term"
mkfile "$LKR" docs/in-target.md "inner-unique-term"
mkfile "$LKO" secret.md "outside-unique-term"
mkfile "$LKO" docs-out/d.md "outside-dir-unique-term"
mkfile "$LKE" evil.md "sibling-unique-term"
mkfifo "$LKO/fifo.md" || fail "could not create outside FIFO"
mkfifo "$LKR/pipe-target" || fail "could not create inside FIFO"
mklink in-target.md "$LKR/docs/in-link.md"
mklink ../../outside/secret.md "$LKR/docs/out-link.md"
mklink "$LKO/secret.md" "$LKR/abs-out.md"
mklink ../repo-evil/evil.md "$LKR/evil-sibling.md"
mklink chain-b.md "$LKR/chain-a.md"
mklink "$LKO/secret.md" "$LKR/chain-b.md"
mklink ok-chain-b.md "$LKR/ok-chain-a.md"
mklink docs/in-target.md "$LKR/ok-chain-b.md"
mklink nonexistent.md "$LKR/dangling.md"
mklink loop-b.md "$LKR/loop-a.md"
mklink loop-a.md "$LKR/loop-b.md"
mklink self.md "$LKR/self.md"
mklink ../../outside/docs-out "$LKR/docs/linked-docs"
mklink ../outside/docs-out "$LKR/outdir.md"
mklink .. "$LKR/parent-link"
mklink docs "$LKR/alias-docs"
mklink . "$LKR/self-root"
mklink . "$LKR/rootlink.md"
mklink "$LKR/README.md" "$LKR/abs-in.md"
mklink ../outside/fifo.md "$LKR/fifo-link.md"
mklink pipe-target "$LKR/pipe.md"

# Control characters and spaces in file names.
CTRL="${T}/ctrl"
mkfile "$CTRL" docs/ok.md
mkfile "$CTRL" "docs/with space.md"
mkfile "$CTRL" "docs/bad"$'\n'"owner: forged.md"
mkfile "$CTRL" "docs/tab"$'\t'"name.md"

# ---------------------------------------------------------------- P4 -------

expect_discover "P4 nested repo" "$N" "$(lines \
  'agents: AGENTS.md' \
  'agents: flow/AGENTS.md' \
  'agents: watch/AGENTS.md' \
  'index: docs/index.md' \
  'index: flow/docs/README.md' \
  'owner: docs/api/endpoints.md' \
  'owner: docs/cli.md' \
  'owner: docs/setup.md' \
  'owner: flow/docs/pipeline.md' \
  'lessons: structured admission=docs/lessons/README.md records=docs/lessons/')"

run_dc discover "$N"
assert_not_contains "${OUT}" "CLAUDE.md" "P4 CLAUDE.md next to AGENTS.md is not listed"
assert_not_contains "${OUT}" "convention: none" "P4 no convention: none when a convention exists"
assert_not_contains "${OUT}" "recommend:" "P4 no recommend block when a convention exists"
assert_not_contains "${OUT}" ".git/" "P4 .git is pruned"
assert_not_contains "${OUT}" ".worktrees" "P4 .worktrees is pruned"
assert_not_contains "${OUT}" "node_modules" "P4 node_modules is pruned"
assert_not_contains "${OUT}" ".plans" "P4 .plans is pruned"
assert_not_contains "${OUT}" "2026-01-first-record" "P4 lesson records are not owners"
assert_not_contains "${OUT}" "notes.txt" "P4 non-Markdown files are ignored"
assert_eq "$(printf '%s\n' "${OUT}" | grep -c '^lessons: ')" "1" "P4 exactly one lessons line"

expect_discover "P4 legacy CLAUDE.md router" "$LEG" "$(lines \
  'agents: CLAUDE.md' \
  'owner: docs/guide.md' \
  'lessons: fallback')"

expect_discover "P4 lessons dir with an admission doc but no record" "$L_ONLY_ADMISSION" "$(lines \
  'owner: docs/guide.md' \
  'lessons: fallback')"
expect_discover "P4 lessons dir with a record but no admission doc" "$L_NO_ADMISSION" "$(lines \
  'owner: docs/guide.md' \
  'lessons: fallback')"
expect_discover "P4 learnings dir with index.md admission" "$L_LEARNINGS" "$(lines \
  'owner: docs/guide.md' \
  'lessons: structured admission=docs/learnings/index.md records=docs/learnings/')"
expect_discover "P4 lessons dir outside the repository docs" "$L_NO_DOCS" "$(lines \
  'owner: docs/guide.md' \
  'lessons: fallback')"

# A trailing slash, a relative root and a symlinked root resolve to the same listing.
NESTED_EXPECTED="$(bash "${SCRIPT}" discover "$N" 2>/dev/null)" || fail "P4 reference discover failed"
run_dc discover "$N/"
assert_eq "${OUT}" "${NESTED_EXPECTED}" "P4 trailing slash root"
if RELATIVE_OUT="$(cd "${T}" && bash "${SCRIPT}" discover nested 2>/dev/null)"; then
  assert_eq "${RELATIVE_OUT}" "${NESTED_EXPECTED}" "P4 relative root"
else
  fail "P4 relative root: script failed"
fi
mklink "$N" "${T}/nested-link"
run_dc discover "${T}/nested-link"
assert_eq "${OUT}" "${NESTED_EXPECTED}" "P4 symlinked root"

# ---------------------------------------------------------------- P5 -------

expect_discover "P5 README-only repo" "$R5" "$(lines \
  'lessons: fallback' \
  'convention: none' \
  'recommend: root AGENTS.md as a short router that points to focused owner docs' \
  'evidence: no AGENTS.md router was found in the repository' \
  'recommend: docs/<topic>.md focused owner docs for contracts, catalogs, setup and procedures' \
  'evidence: no Markdown owner docs were found under a docs/ directory')"

run_dc discover "$R5"
assert_eq "$(printf '%s\n' "${OUT}" | grep -c '^recommend: ')" "2" "P5 recommend count"
assert_eq "$(printf '%s\n' "${OUT}" | grep -c '^evidence: ')" "2" "P5 one evidence line per recommend"
assert_contains "${OUT}" "recommend: root AGENTS.md" "P5 names a root AGENTS.md router"
assert_contains "${OUT}" "docs/<topic>.md" "P5 names docs/<topic>.md owners"
assert_eq "$(printf '%s\n' "${OUT}" | grep -c '^convention: none$')" "1" "P5 one convention: none line"

run_dc discover "$EMPTY"
assert_eq "${RC}" "0" "P5 empty repo exit"
assert_contains "${OUT}" "convention: none" "P5 empty repo has no convention"

expect_discover "P5 partial convention (AGENTS.md, no docs/)" "$PARTIAL" "$(lines \
  'agents: AGENTS.md' \
  'lessons: fallback')"

# ---------------------------------------------------------------- P8 -------

expect_mentions "P8 flag only in README.md" "$P8_README" "${FLAG}" "README.md"
expect_mentions "P8 flag only in the guide" "$P8_GUIDE" "${FLAG}" "docs/cli/commands.md"
expect_mentions "P8 flag in both" "$P8_BOTH" "${FLAG}" "$(lines 'README.md' 'docs/cli/commands.md')"
expect_mentions "P8 no match is empty and exit 0" "$P8_BOTH" "--no-such-flag" ""
expect_mentions "P8 matching is case-sensitive" "$P8_BOTH" "--FROBNICATE" ""
expect_mentions "P8 term is a fixed string, not a regex" "$P8_FIXED" "a.c" "docs/regex.md"
expect_mentions "P8 AGENTS.md without the term is not printed" "$P8_BOTH" "Router." "AGENTS.md"
expect_mentions "P8 term with spaces" "$P8_BOTH" "enable frobnication" "$(lines 'README.md' 'docs/cli/commands.md')"

# ---------------------------------------------------------------- P20 ------

cp -R "$P20" "${T}/p20-before" || fail "P20 could not snapshot the fixture"
run_dc discover "$P20"
assert_eq "${RC}" "0" "P20 discover exit"
assert_eq "$(printf '%s\n' "${OUT}" | grep -c '^agents: ')" "1" "P20 one agents line"
assert_contains "${OUT}" "agents: AGENTS.md" "P20 AGENTS.md is listed"
assert_not_contains "${OUT}" "CLAUDE.md" "P20 the @AGENTS.md shim is not listed"
run_dc mentions "$P20" "shim-term"
assert_eq "${OUT}" "$(lines 'AGENTS.md' 'docs/topic.md')" "P20 mentions stdout"
if diff -r "${T}/p20-before" "$P20" > /dev/null 2>&1; then
  checks=$((checks+1))
else
  fail "P20 the repository tree changed after discover and mentions"
fi
if CLAUDE_AFTER="$(cat "$P20/CLAUDE.md")"; then
  assert_eq "${CLAUDE_AFTER}" "@AGENTS.md" "P20 CLAUDE.md is still exactly the import"
else
  fail "P20 could not read CLAUDE.md"
fi

# ---------------------------------------------------------------- P21 ------

expect_discover "P21 symlink repo" "$LKR" "$(lines \
  'owner: docs/in-link.md' \
  'owner: docs/in-target.md' \
  'lessons: fallback' \
  'skipped: abs-out.md (outside repository)' \
  'skipped: chain-a.md (outside repository)' \
  'skipped: chain-b.md (outside repository)' \
  'skipped: dangling.md (dangling symlink)' \
  'skipped: docs/linked-docs (outside repository)' \
  'skipped: docs/out-link.md (outside repository)' \
  'skipped: evil-sibling.md (outside repository)' \
  'skipped: fifo-link.md (outside repository)' \
  'skipped: loop-a.md (symlink loop)' \
  'skipped: loop-b.md (symlink loop)' \
  'skipped: outdir.md (outside repository)' \
  'skipped: parent-link (outside repository)' \
  'skipped: pipe.md (not a regular file)' \
  'skipped: self.md (symlink loop)')"

run_dc discover "$LKR"
assert_not_contains "${OUT}" "alias-docs" "P21 an inside directory symlink is neither listed nor descended"
assert_not_contains "${OUT}" "self-root" "P21 a directory symlink to the root is not listed"
assert_not_contains "${OUT}" "rootlink.md" "P21 an .md symlink to the root is not listed"
assert_not_contains "${OUT}" "d.md" "P21 a skipped directory symlink is never descended"
assert_not_contains "${OUT}" "outside-unique-term" "P21 no outside content on stdout"

expect_mentions "P21 outside file term" "$LKR" "outside-unique-term" ""
expect_mentions "P21 outside directory term" "$LKR" "outside-dir-unique-term" ""
expect_mentions "P21 sibling-prefix repo term" "$LKR" "sibling-unique-term" ""
expect_mentions "P21 inside links are read" "$LKR" "inner-unique-term" "$(lines \
  'docs/in-link.md' \
  'docs/in-target.md' \
  'ok-chain-a.md' \
  'ok-chain-b.md')"
expect_mentions "P21 absolute inside link (target parent equals root)" "$LKR" "readme-unique-term" "$(lines 'README.md' 'abs-in.md')"

# ---------------------------------------------------------------- P30 ------

run_dc mentions "$N" "${SENTINEL}"
assert_eq "${RC}" "0" "P30 mentions of the sentinel exit"
assert_contains "${OUT}" "docs/cli.md" "P30 sentinel term finds the files"
assert_not_contains "${OUT}" "${SENTINEL}" "P30 mentions prints paths, not bodies"
assert_not_contains "${OUT}" "# " "P30 mentions prints no Markdown heading from a body"

# ------------------------------------------------- odd names & usage errors -

expect_discover "control characters in names" "$CTRL" "$(lines \
  'owner: docs/ok.md' \
  'owner: docs/with space.md' \
  'lessons: fallback')"
expect_mentions "control-character names are not mentioned either" "$CTRL" "${SENTINEL}" "$(lines 'docs/ok.md' 'docs/with space.md')"

expect_usage_error "no arguments"
expect_usage_error "bad subcommand" frobnicate "$N"
expect_usage_error "discover without a root" discover
expect_usage_error "discover with an extra argument" discover "$N" extra
expect_usage_error "discover with an empty root" discover ""
expect_usage_error "discover on a missing directory" discover "${T}/does-not-exist"
expect_usage_error "discover on a regular file" discover "$N/README.md"
expect_usage_error "mentions without a term" mentions "$N"
expect_usage_error "mentions with an empty term" mentions "$N" ""
expect_usage_error "mentions with a multi-line term" mentions "$N" $'one\ntwo'
expect_usage_error "mentions with an extra argument" mentions "$N" term extra
expect_usage_error "mentions on a missing directory" mentions "${T}/does-not-exist" term
expect_usage_error "mentions on a regular file" mentions "$N/README.md" term

# ----------------------------------------------- fail-closed & stub commands -
# Stub commands live in a directory put first on PATH for the script only (no
# permission-removing chmod: flow/tests/root-safe-perms-contract.test.sh).

make_shim() { # <dir> <name> <body lines>
  mkdirs "$1" || return 1
  printf '#!/bin/sh\n%s\n' "$3" > "$1/$2" || { fail "could not write shim $2"; return 1; }
  chmod +x "$1/$2" || fail "could not make shim $2 executable"
}
REAL_GREP="$(command -v grep)" || fail "grep not found"
REAL_SORT="$(command -v sort)" || fail "sort not found"
REAL_MKTEMP="$(command -v mktemp)" || fail "mktemp not found"
[[ "${REAL_GREP}" == /* && "${REAL_SORT}" == /* && "${REAL_MKTEMP}" == /* ]] || fail "the stubbed commands must resolve to absolute paths"

SHIM_FIND="${T}/shim-find"
make_shim "${SHIM_FIND}" find 'exit 2'
SHIM_GREP_ONCE="${T}/shim-grep-once"
GREP_MARK="${T}/grep-mark"
make_shim "${SHIM_GREP_ONCE}" grep "if [ -e \"${GREP_MARK}\" ]; then exit 2; fi
: > \"${GREP_MARK}\"
exec \"${REAL_GREP}\" \"\$@\""
SHIM_GREP_ALWAYS="${T}/shim-grep-always"
make_shim "${SHIM_GREP_ALWAYS}" grep 'exit 2'
SHIM_SORT_LDIRS="${T}/shim-sort-ldirs"
make_shim "${SHIM_SORT_LDIRS}" sort "case \"\$*\" in *ldirs*) exit 2 ;; esac
exec \"${REAL_SORT}\" \"\$@\""
SHIM_MKTEMP="${T}/shim-mktemp"
MKTEMP_LOG="${T}/mktemp-log"
make_shim "${SHIM_MKTEMP}" mktemp "printf '%s\\n' \"\$@\" >> \"${MKTEMP_LOG}\"
exec \"${REAL_MKTEMP}\" \"\$@\""

# C: a failure after output has started must leave stdout empty (buffered).
RUN_SHIM="${SHIM_FIND}" expect_usage_error "find failure in discover" discover "$N"
RUN_SHIM="${SHIM_FIND}" expect_usage_error "find failure in mentions" mentions "$N" "${SENTINEL}"
RUN_SHIM="${SHIM_GREP_ALWAYS}" expect_usage_error "grep failure after agents/index/owner lines in discover" discover "$N"
rm -f "${GREP_MARK}" || fail "could not reset the grep shim marker"
RUN_SHIM="${SHIM_GREP_ONCE}" expect_usage_error "grep failure after a first match in mentions" mentions "$N" "${SENTINEL}"
RUN_SHIM="${SHIM_SORT_LDIRS}" expect_usage_error "sort failure after agents/index/owner lines in discover" discover "$N"
TMPDIR="${T}/no-such-tmpdir" expect_usage_error "mktemp failure in discover" discover "$N"
TMPDIR="${T}/no-such-tmpdir" expect_usage_error "mktemp failure in mentions" mentions "$N" "${SENTINEL}"

# B: CDPATH must not redirect `cd` (and print a stray stdout line); a relative
# TMPDIR must not put the work directory inside the repository.
CDP="${T}/cdpath"
mkfile "$CDP" repo/AGENTS.md
mkfile "$CDP" repo/docs/real.md
mkfile "$CDP" decoy/repo/AGENTS.md
mkfile "$CDP" decoy/repo/docs/decoy.md
CDPATH="${CDP}/decoy" RUN_CWD="${CDP}" expect_discover "CDPATH with a relative root analyses the right tree" repo "$(lines \
  'agents: AGENTS.md' \
  'owner: docs/real.md' \
  'lessons: fallback')"
CDPATH="${CDP}/decoy" RUN_CWD="${CDP}" expect_mentions "CDPATH with a relative root mentions the right tree" repo "${SENTINEL}" "$(lines 'AGENTS.md' 'docs/real.md')"

: > "${MKTEMP_LOG}" || fail "could not reset the mktemp log"
TMPDIR="relative-tmp" RUN_CWD="${CDP}" RUN_SHIM="${SHIM_MKTEMP}" run_dc discover repo
assert_eq "${RC}" "0" "relative TMPDIR falls back to an absolute temp directory (exit)"
assert_eq "${ERR}" "" "relative TMPDIR falls back to an absolute temp directory (stderr)"
if MKTEMP_ARGS="$(cat "${MKTEMP_LOG}")"; then
  assert_contains "${MKTEMP_ARGS}" "/cenci-doc-conventions." "relative TMPDIR: mktemp was invoked"
  assert_not_contains "${MKTEMP_ARGS}" "relative-tmp" "relative TMPDIR is never used for the work directory"
  case "${MKTEMP_ARGS}" in
    *$'\n'/*) checks=$((checks+1)) ;;
    *) fail "relative TMPDIR: the mktemp template is not absolute: [${MKTEMP_ARGS}]" ;;
  esac
else
  fail "could not read the mktemp log"
fi
: > "${MKTEMP_LOG}" || fail "could not reset the mktemp log"
TMPDIR="." RUN_CWD="${CDP}/repo" RUN_SHIM="${SHIM_MKTEMP}" run_dc discover "${CDP}/repo"
assert_eq "${RC}" "0" "TMPDIR=. is not used as the work directory base (exit)"
if MKTEMP_ARGS="$(cat "${MKTEMP_LOG}")"; then
  assert_not_contains "${MKTEMP_ARGS}" $'\n./' "TMPDIR=. is never used for the work directory"
else
  fail "could not read the mktemp log"
fi

# F: a CLAUDE.md is a legacy router unless the AGENTS.md beside it was KEPT.
SIB="${T}/siblings"
mkfile "$SIB" outside/a.md
mkfile "$SIB/dangling" CLAUDE.md
mklink nope.md "$SIB/dangling/AGENTS.md"
mkfile "$SIB/outlink" CLAUDE.md
mklink ../outside/a.md "$SIB/outlink/AGENTS.md"
mkfile "$SIB/kept-link" CLAUDE.md
mkfile "$SIB/kept-link" real.md
mklink real.md "$SIB/kept-link/AGENTS.md"
mkfile "$SIB/only-skipped" README.md
mklink nope.md "$SIB/only-skipped/AGENTS.md"
expect_discover "F dangling sibling AGENTS.md still lists the regular CLAUDE.md" "$SIB/dangling" "$(lines \
  'agents: CLAUDE.md' \
  'lessons: fallback' \
  'skipped: AGENTS.md (dangling symlink)')"
expect_discover "F outside-repo sibling AGENTS.md still lists the regular CLAUDE.md" "$SIB/outlink" "$(lines \
  'agents: CLAUDE.md' \
  'lessons: fallback' \
  'skipped: AGENTS.md (outside repository)')"
expect_discover "F a kept sibling AGENTS.md symlink still hides CLAUDE.md" "$SIB/kept-link" "$(lines \
  'agents: AGENTS.md' \
  'lessons: fallback')"
expect_discover "F convention: none with only skipped entries does not claim no router exists" "$SIB/only-skipped" "$(lines \
  'lessons: fallback' \
  'skipped: AGENTS.md (dangling symlink)' \
  'convention: none' \
  'recommend: root AGENTS.md as a short router that points to focused owner docs' \
  'evidence: no usable AGENTS.md router was found in the repository (skipped: entries exist and were not used)' \
  'recommend: docs/<topic>.md focused owner docs for contracts, catalogs, setup and procedures' \
  'evidence: no usable Markdown owner docs were found under a docs/ directory (skipped: entries exist and were not used)')"

# G: a symlink resolving into a pruned directory is skipped, never read.
PR="${T}/pruned-targets"
mkfile "$PR" README.md
mkfile "$PR" docs/guide.md "kept-guide-term"
mkfile "$PR" .git/config "pruned-secret-term"
mkfile "$PR" .worktrees/wt/docs/hidden.md "pruned-secret-term"
mkfile "$PR" node_modules/pkg/n.md "pruned-secret-term"
mkfile "$PR" .plans/1-plan.md "pruned-secret-term"
mkfile "$PR" sub/node_modules/deep.md "pruned-secret-term"
mklink ../.git/config "$PR/docs/x.md"
mklink ../.worktrees/wt/docs/hidden.md "$PR/docs/wt.md"
mklink ../node_modules/pkg/n.md "$PR/docs/nm.md"
mklink ../.plans/1-plan.md "$PR/docs/plan.md"
mklink ../sub/node_modules/deep.md "$PR/docs/deep.md"
mklink guide.md "$PR/docs/ok.md"
expect_discover "G symlinks into pruned directories are skipped" "$PR" "$(lines \
  'owner: docs/guide.md' \
  'owner: docs/ok.md' \
  'lessons: fallback' \
  'skipped: docs/deep.md (pruned target)' \
  'skipped: docs/nm.md (pruned target)' \
  'skipped: docs/plan.md (pruned target)' \
  'skipped: docs/wt.md (pruned target)' \
  'skipped: docs/x.md (pruned target)')"
expect_mentions "G a pruned-directory term is never found through a link" "$PR" "pruned-secret-term" ""
expect_mentions "G an ordinary in-repo link is still read" "$PR" "kept-guide-term" "$(lines 'docs/guide.md' 'docs/ok.md')"

# ------------------------------------------------------------ P30 sweep ----

if SWEEP="$(cat "${ALL_OUTPUT}")"; then
  assert_not_contains "${SWEEP}" "${SENTINEL}" "P30 sentinel never reaches stdout or stderr in any run"
  assert_not_contains "${SWEEP}" "outside-unique-term" "P21 outside-repository content never reaches any output"
  assert_not_contains "${SWEEP}" "sibling-unique-term" "P21 sibling-prefix content never reaches any output"
else
  fail "P30 could not read the recorded output"
fi

# The script must not use bash-reserved BASH_* variable names.
if [[ -r "${SCRIPT}" ]]; then
  checks=$((checks+1))
  if grep -q 'BASH_' "${SCRIPT}"; then fail "script uses a BASH_* variable name"; fi
fi

if [[ "${failures}" -eq 0 ]]; then
  echo "doc-conventions.test.sh: failures=0 checks=${checks}"
  exit 0
fi
echo "doc-conventions.test.sh: failures=${failures} checks=${checks}"
exit 1
