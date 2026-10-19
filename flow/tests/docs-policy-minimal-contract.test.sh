#!/usr/bin/env bash
# Contract test for #1301: the optional `docs.policy: "minimal"` setting, its
# minimal-mode Phase 8 gating, the single Phase 9 docs status line, and the
# honest `unavailable` / `skipped (<reason>)` maintenance status.
#
# Prose-only pipeline change (Markdown) plus one read-only resolver, so this
# follows the grep-contract idiom of flow/tests/doc-ownership-contract.test.sh
# and flow/tests/implement-maintenance-fail-gate.test.sh: a `failures` counter,
# `assert_*` helpers built on literal matching, every negative assertion
# guarded by a file-existence check (docs/shell-scripting-gotchas.md rule 28),
# markers kept on one source line (rule 13), and a fence-aware section
# extractor (rule 29) paired with its own non-vacuity self-test below. The
# three `case_policy_*` cases also run the real resolver
# (skills/implement/scripts/docs-policy.sh) against `mktemp -d` repos, so the
# prose contract and the script it cites can never disagree silently.
#
# Named cases required by AC P13: case_policy_absent, case_policy_unknown,
# case_policy_minimal, case_maintenance_pass, case_maintenance_warn,
# case_maintenance_fail, case_check_during_implement_false,
# case_checker_unavailable. Plus P2, P3, P6, P7, P12, P15 and P18, and the
# review-hardening mechanisms: the read-and-error step for docs-to-update.txt
# (P18), untracked files in changed-file discovery, the sanitized Notes
# excerpt for a failing repository docs command, and stdout-only resolver
# output with every non-clean result treated as unresolved. Final review fixes:
# re-entry carries forward only the persistence-failed error, default-mode
# docs errors reach Notes in full, a malformed docs-status.txt is never
# trusted, the sanitized excerpt is recorded once as an `excerpt:` line, and
# the resolver's header comment matches its real warning text.
#
# No `read_*`-named helpers (flow/tests/read-helper-purity-contract.test.sh);
# no permission-removing chmod (root-safe-perms-contract.test.sh).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "docs-policy-minimal-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "docs-policy-minimal-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "docs-policy-minimal-contract.test.sh: failed to resolve repo root." >&2; exit 2; }

RESOLVER="${FLOW_DIR}/skills/implement/scripts/docs-policy.sh"
PHASE8_DOCS="${FLOW_DIR}/skills/implement/phases/phase-8-docs.md"
PHASE9_PR="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
CODEX_MD="${FLOW_DIR}/skills/implement/codex.md"
CONFIGURE_SKILL="${FLOW_DIR}/skills/configure/SKILL.md"
CONFIGURE_CODEX="${FLOW_DIR}/skills/configure/codex.md"
FLOW_README="${FLOW_DIR}/README.md"
GETTING_STARTED="${REPO_ROOT}/docs/getting-started.md"

failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}

assert_file_lacks() {
  # $1=file $2=needle $3=description -- the [[ -f ]] / [[ -r ]] guards are
  # mandatory: a "must NOT contain" claim about a missing or unreadable file
  # must never pass. grep exit 1 is the only "absent" result; exit 2 (a scan
  # error) fails loudly instead of reading as absence.
  local rc
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  [[ -r "$1" ]] || { fail "$3: file not readable: $1"; return; }
  grep -qF -- "$2" "$1"
  rc=$?
  case "${rc}" in
    0) fail "$(basename "$1") $3 (expected to NOT contain: $2)" ;;
    1) ;;
    *) fail "$3: grep scan error (exit ${rc}) on $1" ;;
  esac
  return 0
}

assert_contains() {
  # $1=haystack $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" == *"$2"* ]] || fail "$3 (expected to contain: $2)"
}

assert_not_contains() {
  # $1=haystack $2=needle $3=description -- callers pass a haystack obtained
  # through require_section/require_line, which already fail closed on a
  # missing file or section, so an empty haystack here is never vacuous.
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3 (expected to NOT contain: $2)"
}

assert_eq() {
  # $1=actual $2=expected $3=description
  [[ "$1" == "$2" ]] || fail "$3 (expected [$2], got [$1])"
}

# extract_section <content> <exact-heading-line> -- pure, fence-aware: prints
# the named "## " (or "### ") section from its heading through the next real
# heading of the same or a higher level, or EOF. A heading-shaped line inside
# a fenced code block is never a boundary. No fail() side effect, so it is
# safe inside $(...).
extract_section() {
  awk -v want="$2" '
    BEGIN { level = 0 }
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

# require_section <result-var> <file> <heading-line> <label> -- nameref
# wrapper; fails closed (and assigns "") on an unreadable file or a missing
# section. Must NOT be invoked via $(...).
require_section() {
  local -n _res="$1"
  local _content _body
  if [[ ! -f "$2" ]] || ! _content="$(cat "$2" 2>/dev/null)"; then
    fail "$4: could not read file: $2"
    _res=""
    return 1
  fi
  _body="$(extract_section "${_content}" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section in $2"
    _res=""
    return 1
  fi
  _res="${_body}"
}

# require_line <result-var> <haystack> <needle> <label> -- binds an
# assertion to the one source line carrying <needle>; fails closed when no
# such line exists. Must NOT be invoked via $(...).
require_line() {
  local -n _line="$1"
  local _found
  _found="$(grep -F -m1 -e "$3" <<<"$2")"
  if [[ -z "${_found}" ]]; then
    fail "$4: no line containing: $3"
    _line=""
    return 1
  fi
  _line="${_found}"
}

# count_occurrences <file> <needle> -- occurrence count via grep -oF | wc -l
# (never grep -c, which counts lines; rule 10(4)). Prints ERROR when the file
# is unreadable.
count_occurrences() {
  [[ -f "$1" ]] || { echo "ERROR"; return; }
  grep -oF -- "$2" "$1" | wc -l | tr -d '[:space:]'
}

# --- Resolver harness --------------------------------------------------------

TMP_ROOT="$(mktemp -d)" || { echo "docs-policy-minimal-contract.test.sh: mktemp -d failed." >&2; exit 2; }
[[ -n "${TMP_ROOT}" && -d "${TMP_ROOT}" ]] || { echo "docs-policy-minimal-contract.test.sh: mktemp -d returned no directory." >&2; exit 2; }
trap 'rm -rf "${TMP_ROOT}"' EXIT

# run_resolver <case-name> <config-json-or-empty> -- builds a synthetic repo
# under TMP_ROOT and runs the real resolver; sets RESOLVED_OUT, RESOLVED_ERR,
# RESOLVED_RC. An empty config argument writes no config file at all.
run_resolver() {
  local repo="${TMP_ROOT}/$1"
  mkdir -p "${repo}/.cenci" || { fail "$1: could not create synthetic repo"; return 1; }
  if [[ -n "$2" ]]; then
    printf '%s\n' "$2" > "${repo}/.cenci/config.json" || { fail "$1: could not write synthetic config"; return 1; }
  fi
  RESOLVED_OUT="$(bash "${RESOLVER}" "${repo}" 2>"${repo}.stderr")"
  RESOLVED_RC=$?
  if ! RESOLVED_ERR="$(cat "${repo}.stderr")"; then
    fail "$1: could not read the resolver's captured stderr"
    RESOLVED_ERR=""
    return 1
  fi
}

# --- Extractor self-test (non-vacuity, rule 29) ------------------------------
# A fenced "## " line must not end a section; a real "## " heading must; a
# "### " subsection stays inside its "## " parent.

case_extractor_self_test() {
  local doc body
  doc=$'## Alpha\nalpha-1\n### Alpha sub\nalpha-sub\n```markdown\n## Fenced heading\n```\nalpha-after-fence\n## Beta\nbeta-1'
  body="$(extract_section "${doc}" '## Alpha')"
  assert_contains "${body}" 'alpha-sub' 'extractor keeps a ### subsection inside its ## parent'
  assert_contains "${body}" 'alpha-after-fence' 'extractor does not stop at a fenced ## line'
  assert_not_contains "${body}" 'beta-1' 'extractor stops at the next real ## heading'
  body="$(extract_section "${doc}" '### Alpha sub')"
  assert_not_contains "${body}" 'beta-1' 'extractor stops a ### section at the next ## heading'
}

# --- Shared contract phrases (single source lines in the docs) ---------------

P8_ZERO_EDIT='In `minimal` mode, a run with no plan-named doc file, no `$RUN_DIR/docs-to-update.txt` entry, no changed reading trigger and no admitted lesson makes zero documentation edits, skips the `## Update CLAUDE.md`, `## Update README.md` and `## Update Topic Docs` sub-steps, and does not launch `lessons-collector`.'
P8_MINIMAL_MAINT_ANCHOR='**Minimal mode (`docs.policy: "minimal"`)**:'
CONFIGURE_OPTIONAL_SENTENCE='`docs.policy` is an optional field that no configure prompt writes, and a configure re-run preserves it together with every unrelated key.'
CONFIGURE_SCOPE_SENTENCE='Plan-required and correctness documentation work always runs, while incidental documentation edits and lesson capture are what `minimal` suppresses.'
UNAVAILABLE_STATUS='maintenance: unavailable (no documentation checker in this repository)'
P18_SENTENCE='In `minimal` mode, an unreadable `$RUN_DIR/docs-to-update.txt` still reports `docs: error (docs-to-update.txt unreadable)`, never `docs: none (minimal policy)`'
# P18 mechanism: the read-and-error step inside `### Minimal mode`, and the
# Docs Status re-read that never relies on conversation memory.
MIN_READ_DTU='Evaluate the `$RUN_DIR/docs-to-update.txt` entry condition by reading `$RUN_DIR/docs-to-update.txt` itself with the `Read` tool'
MIN_ABSENT_DTU='an absent file (the read reports that no file exists at that path) means no entry'
MIN_UNREADABLE_DTU='a file that exists but cannot be read records `docs: error (docs-to-update.txt unreadable)` even though every sub-step is skipped'
DS_REREAD_DTU='re-read `$RUN_DIR/docs-to-update.txt` here with the `Read` tool rather than relying on this conversation'
DS_PERSIST_ERR='Phase 6 + 7'"'"'s `docs: error (docs-to-update.txt persistence failed)` is still emitted when `$RUN_DIR/docs-to-update.txt` is absent'
DS_COPY_AT_DETECTION='the status update line carrying it must be copied into `$RUN_DIR/docs-status.txt` at the point of detection'
RESOLVE_COPY_NOW='copy that line into `$RUN_DIR/docs-status.txt` now'
# Changed-file discovery: tracked diff UNION untracked files, each captured
# and exit-checked.
UNTRACKED_CMD='git -C <worktree-path> ls-files --others --exclude-standard'
DISCOVERY_TMP='(or, when `$RUN_DIR` is unknown, a `mktemp` file under `${TMPDIR:-/tmp}/cenci/`)'
DS_DISCOVERY_FAIL='a failure of either records `docs: error (changed-file discovery failed)`, never `none`'
MAINT_DISCOVERY_FAIL='If the `files.txt` read or either fallback command fails'
# PR-body secrecy for a failing repository docs command.
NOTES_EXCERPT='only the command label, its exit code and a short excerpt with secret, environment-variable, token and URL-credential values removed'
# Resolver output handling: stdout only, and every non-clean result unresolved.
STDOUT_ONLY='read the policy token from stdout only'
UNRESOLVED_CATCH='anything other than exit 0 with stdout exactly `minimal` or `default`'
# Docs Status re-entry: only the persistence failure is carried forward; every
# other reason is re-derived, so a fixed config or file clears it.
DS_CARRY_PERSIST_ONLY='re-read `$RUN_DIR/docs-status.txt` before overwriting it, and carry forward only the provisional `docs: error (docs-to-update.txt persistence failed)` line'
DS_REDERIVED='is re-derived on each entry'
CODEX_CARRY_PERSIST_ONLY='carry forward only the provisional persistence-failed line'
# Default-mode (and unresolved) docs errors reach ## Notes in full.
NOTES_DOC_STATUS='`Documentation status: <the entire second line>`'
P9_NOTES_DOC_STATUS_RULE='whenever the second line starts with `docs: error` and the first line is `policy: default` or `policy: unresolved`, add `Documentation status: <the entire second line>` to `## Notes`'
README_NOTES_DOC_STATUS='`Documentation status: docs: error (<reason>)`'
# A malformed docs-status.txt is handled like an absent one.
MALFORMED_DEF='first line not exactly one of `policy: minimal`, `policy: default` or `policy: unresolved`, or `policy: minimal` with no second line starting `docs: `'
MALFORMED_UNKNOWN='docs: unknown (status file malformed'
# A failing repository docs command's sanitized excerpt line.
EXCERPT_LINE='`excerpt: exit <code> — <sanitized excerpt>`'
P8_EXCERPT_RULE='immediately under its failing `validation:` line in `$RUN_DIR/maintain-status.txt`, write exactly one `excerpt: exit <code> — <sanitized excerpt>` line'
P9_NOTES_CITES_EXCERPT='the `excerpt:` line Phase 8 wrote under that `validation:` line'
P9_HALTED_CITES_EXCERPT='report that `validation:` line and the `excerpt:` line recorded under it'

policy_section=""
require_section policy_section "${PHASE8_DOCS}" '## Resolve Documentation Policy' 'Phase 8 Resolve Documentation Policy section'
maintenance_section=""
require_section maintenance_section "${PHASE8_DOCS}" '## Maintenance Check' 'Phase 8 Maintenance Check section'
docs_status_section=""
require_section docs_status_section "${PHASE8_DOCS}" '## Docs Status' 'Phase 8 Docs Status section'

# --- P13: policy cases (each also runs the resolver) -------------------------

case_policy_absent() {
  run_resolver policy-absent '{"projects": []}' || return
  assert_eq "${RESOLVED_RC}" '0' 'case_policy_absent: resolver exit code'
  assert_eq "${RESOLVED_OUT}" 'default' 'case_policy_absent: resolver output'
  assert_eq "${RESOLVED_ERR}" '' 'case_policy_absent: an absent docs.policy is silent'
  # Phase 8 cites the resolver by its backticked skill-relative path (the
  # check.sh orphan-files contract) and keeps today's triggers for default.
  assert_contains "${policy_section}" '`scripts/docs-policy.sh`' 'case_policy_absent: Phase 8 cites the resolver'
  assert_contains "${policy_section}" '"${CLAUDE_PLUGIN_ROOT}/skills/implement/scripts/docs-policy.sh" "<worktree-path>"' 'case_policy_absent: Phase 8 resolves against the worktree'
  assert_contains "${policy_section}" 'Exit 0 with `default` (an absent `docs` block, an absent `docs.policy`, or any value other than the exact string `"minimal"`)' 'case_policy_absent: absent policy resolves to default'
  assert_contains "${policy_section}" 'every sub-step below keeps its existing trigger, unchanged' 'case_policy_absent: default mode keeps the existing triggers (P27)'
}

case_policy_unknown() {
  run_resolver policy-unknown '{"docs": {"policy": "Minimal"}}' || return
  assert_eq "${RESOLVED_RC}" '0' 'case_policy_unknown: resolver exit code'
  assert_eq "${RESOLVED_OUT}" 'default' 'case_policy_unknown: resolver output'
  assert_contains "${RESOLVED_ERR}" 'docs.policy' 'case_policy_unknown: the warning names docs.policy'
  assert_not_contains "${RESOLVED_ERR}" 'Minimal' 'case_policy_unknown: the warning never echoes the value'
  assert_not_contains "${RESOLVED_ERR}" 'minimal' 'case_policy_unknown: the warning never carries the bare token minimal'
  assert_file_lacks "${RESOLVER}" 'is not \"minimal\"' 'case_policy_unknown: the old minimal-bearing warning text is gone'
  assert_contains "${policy_section}" 'one stderr warning naming `docs.policy` for a present but unknown value' 'case_policy_unknown: Phase 8 documents the unknown-value warning'
}

case_policy_minimal() {
  run_resolver policy-minimal '{"docs": {"policy": "minimal"}}' || return
  assert_eq "${RESOLVED_RC}" '0' 'case_policy_minimal: resolver exit code'
  assert_eq "${RESOLVED_OUT}" 'minimal' 'case_policy_minimal: resolver output'
  assert_eq "${RESOLVED_ERR}" '' 'case_policy_minimal: no warning'
  assert_contains "${policy_section}" 'Exit 0 with `minimal` → apply `### Minimal mode` below.' 'case_policy_minimal: Phase 8 routes minimal to its gating'
  assert_contains "${policy_section}" "${P8_ZERO_EDIT}" 'case_policy_minimal: Phase 8 zero-edit rule'
}

case_policy_unresolved() {
  # P15: a malformed config exits 2 with one stderr line; Phase 8 falls back
  # to default behavior and reports docs: error (policy unresolved).
  run_resolver policy-unresolved '{"docs": ' || return
  assert_eq "${RESOLVED_RC}" '2' 'case_policy_unresolved: resolver exit code'
  assert_eq "${RESOLVED_OUT}" '' 'case_policy_unresolved: nothing on stdout'
  assert_contains "${policy_section}" 'Exit 2 (an unreadable or malformed config) → apply default behavior and report `docs: error (policy unresolved)`' 'case_policy_unresolved: Phase 8 falls back to default and reports the error'
  assert_contains "${docs_status_section}" '`policy: unresolved`' 'case_policy_unresolved: the docs status file records an unresolved policy'
}

# --- P13: maintenance cases --------------------------------------------------

minimal_maint_line=""
require_line minimal_maint_line "${maintenance_section}" "${P8_MINIMAL_MAINT_ANCHOR}" 'Phase 8 minimal-mode maintenance rule'

case_maintenance_pass() {
  assert_contains "${maintenance_section}" 'a final run with `fail=0` (including a warn-only run, since `warn` is not CI-blocking) records `pass`' 'case_maintenance_pass: Phase 8 records a clean or warn-only checker as pass'
  assert_contains "${maintenance_section}" 'with **no** trailing `— <tag>` suffix' 'case_maintenance_pass: the no-tag clean pass first line survives'
  assert_file_contains "${PHASE9_PR}" '→ `- [x] Maintenance check passed`' 'case_maintenance_pass: Phase 9 still renders a clean pass checked'
  assert_file_contains "${PHASE9_PR}" '`pass` → `- [x] Docs check passed: <label>`' 'case_maintenance_pass: Phase 9 renders a passing repository docs check'
}

case_maintenance_warn() {
  assert_contains "${minimal_maint_line}" 'every `warn` and `skip` result is report-only with no `AskUserQuestion` prompt' 'case_maintenance_warn: minimal warn/skip never prompts (Phase 8)'
  assert_contains "${minimal_maint_line}" 'including the must-stop-and-ask ambiguity cases at those statuses' 'case_maintenance_warn: minimal covers the at-any-status ambiguity cases'
  assert_file_contains "${CODEX_MD}" 'In `minimal` mode, every `warn` and `skip` maintenance result is report-only with no prompt' 'case_maintenance_warn: minimal warn/skip never prompts (codex.md)'
  assert_file_lacks "${CODEX_MD}" 'AskUserQuestion' 'case_maintenance_warn: codex.md stays client-portable'
}

case_maintenance_fail() {
  assert_contains "${minimal_maint_line}" 'a `fail` still enters the Fix now / Stop / Push anyway question below' 'case_maintenance_fail: minimal keeps the fail question (Phase 8)'
  assert_contains "${maintenance_section}" 'offer exactly these options in this order: **Fix now**, **Stop**, **Push anyway**.' 'case_maintenance_fail: the fail question options are unchanged'
  assert_file_contains "${CODEX_MD}" 'a `fail` still enters the fix / stop / push-anyway choice' 'case_maintenance_fail: minimal keeps the fail gate (codex.md)'
}

case_check_during_implement_false() {
  assert_contains "${minimal_maint_line}" '`maintenance.checkDuringImplement` explicitly `false` still makes a `fail` report-only' 'case_check_during_implement_false: minimal keeps the opt-out (Phase 8)'
  assert_contains "${maintenance_section}" 'all non-pass results are report-only, including any `fail`' 'case_check_during_implement_false: the opt-out is unchanged'
  assert_file_contains "${CODEX_MD}" '`maintenance.checkDuringImplement=false` still makes a `fail` report-only' 'case_check_during_implement_false: minimal keeps the opt-out (codex.md)'
}

case_checker_unavailable() {
  assert_contains "${maintenance_section}" "write \`${UNAVAILABLE_STATUS}\` as the first line of \`\$RUN_DIR/maintain-status.txt\`" 'case_checker_unavailable: Phase 8 records unavailable'
  assert_contains "${maintenance_section}" 'an absent checker is `unavailable`, never a silent skip and never `skipped`' 'case_checker_unavailable: an absent checker is never a silent skip'
  assert_file_lacks "${PHASE8_DOCS}" 'so the file is absent and the step is a clean skip' 'case_checker_unavailable: the silent-skip wording is gone'
  assert_file_contains "${PHASE9_PR}" '→ `- [ ] Maintenance check: unavailable (no documentation checker in this repository — verify documentation manually before merge)`' 'case_checker_unavailable: Phase 9 renders unavailable unchecked'
  assert_file_contains "${PHASE9_PR}" 'never `- [x] Maintenance check: not applicable`' 'case_checker_unavailable: unavailable is never rendered as not applicable'
  assert_file_contains "${PHASE9_PR}" 'file'"'"'s first line is `maintenance: skipped (no doc-affecting changes)` → `- [x] Maintenance check: not applicable`' 'case_checker_unavailable: no-doc-affecting-changes keeps its checked rendering'
  assert_file_contains "${CODEX_MD}" 'with no `flow/skills/maintain/scripts/check.sh` in the tree, record `unavailable` and render it unchecked' 'case_checker_unavailable: codex.md records unavailable'
}

# --- P2: configure schema (both clients, identical sentences) ----------------

case_configure_schema() {
  local f
  for f in "${CONFIGURE_SKILL}" "${CONFIGURE_CODEX}"; do
    assert_file_contains "${f}" "${CONFIGURE_OPTIONAL_SENTENCE}" 'P2: docs.policy optional, never prompted, preserved on re-run'
    assert_file_contains "${f}" "${CONFIGURE_SCOPE_SENTENCE}" 'P2: plan-required/correctness vs incidental/lesson-capture'
    assert_file_contains "${f}" 'Do **not** add `docs` to the migration-removal list' 'P2: docs is not a migration-removal key'
  done
}

# --- P3: README subsection + getting-started link ----------------------------

case_readme_subsection() {
  local pipeline policy_doc readme_content maint_line policy_line section6 count
  require_section pipeline "${FLOW_README}" '## Implementation Pipeline' 'P3: README Implementation Pipeline section' || return
  assert_contains "${pipeline}" $'\n### Documentation policy\n' 'P3: README has ### Documentation policy under ## Implementation Pipeline'
  readme_content="$(cat "${FLOW_README}")"
  maint_line="$(grep -nxF '### Maintaining the project' <<<"${readme_content}" | cut -d: -f1)"
  policy_line="$(grep -nxF '### Documentation policy' <<<"${readme_content}" | cut -d: -f1)"
  if [[ -z "${maint_line}" || -z "${policy_line}" ]] || (( policy_line <= maint_line )); then
    fail 'P3: ### Documentation policy must follow ### Maintaining the project'
  fi
  require_section policy_doc "${FLOW_README}" '### Documentation policy' 'P3: README Documentation policy subsection' || return
  assert_contains "${policy_doc}" '`{"docs": {"policy": "minimal"}}`' 'P3: README shows the setting as inline code'
  assert_not_contains "${policy_doc}" '```json' 'P3: README example is not a config-shaped ```json block'
  assert_contains "${policy_doc}" 'absent, or any other value' 'P3: README documents the default values'
  assert_contains "${policy_doc}" "${CONFIGURE_SCOPE_SENTENCE}" 'P3: README states the minimal scope'
  assert_contains "${policy_doc}" 'does not launch the lessons collector' 'P3: README documents minimal gating'
  assert_contains "${policy_doc}" '`docs: none (minimal policy)`' 'P3: README documents docs: none'
  assert_contains "${policy_doc}" '`docs: updated <paths>`' 'P3: README documents docs: updated'
  assert_contains "${policy_doc}" '`docs: error (<reason>)`' 'P3: README documents docs: error'
  assert_contains "${policy_doc}" '`- [ ] Maintenance check: unavailable' 'P3: README calls out the default-mode unavailable rendering'
  require_section section6 "${GETTING_STARTED}" '## 6. Keep the project healthy' 'P3: getting-started section 6' || return
  assert_contains "${section6}" '(../flow/README.md#documentation-policy)' 'P3: getting-started section 6 links to the subsection'
  count="$(count_occurrences "${GETTING_STARTED}" 'documentation-policy')"
  assert_eq "${count}" '1' 'P3: getting-started links to the subsection exactly once'
  assert_file_lacks "${GETTING_STARTED}" 'docs: none (minimal policy)' 'P3: getting-started does not restate the status lines'
  assert_file_lacks "${GETTING_STARTED}" 'lessons collector' 'P3: getting-started does not restate minimal behavior'
}

# --- P6: four conditions + no-launch rule (phase-8 and codex.md) -------------

case_minimal_gating() {
  local f
  local -a conditions=('no plan-named doc file' 'docs-to-update.txt` entry' 'no changed reading trigger' 'no admitted lesson' 'makes zero documentation edits' 'does not launch `lessons-collector`')
  for f in "${PHASE8_DOCS}" "${CODEX_MD}"; do
    local c
    for c in "${conditions[@]}"; do
      assert_file_contains "${f}" "${c}" 'P6: minimal-mode condition'
    done
  done
  assert_contains "${policy_section}" '**Changed reading trigger** — the run added, renamed or removed a doc that an `AGENTS.md`/`CLAUDE.md` read-when pointer routes to, or made such a pointer inaccurate.' 'P6: Phase 8 defines a changed reading trigger'
  assert_contains "${policy_section}" '**Admitted lesson** — one of the four `## Capture Lessons` triggers fired' 'P6: Phase 8 defines an admitted lesson'
  assert_contains "${policy_section}" 'first record it as an entry in `$RUN_DIR/docs-to-update.txt` with the `Write` tool, then repair it' 'P6: an implementer-unlisted stale doc is recorded before repair'
  assert_contains "${policy_section}" 'Phase 8 never looks for new documentation opportunities in `minimal` mode' 'P6: minimal never hunts for new documentation'
  local lessons
  require_section lessons "${PHASE8_DOCS}" '## Capture Lessons' 'P6: Phase 8 Capture Lessons section' || return
  assert_contains "${lessons}" 'In `minimal` mode, launch `lessons-collector` only for an admitted lesson' 'P6: Capture Lessons carries the minimal gate'
}

# --- P7: Phase 9 single docs status line -------------------------------------

case_phase9_docs_line() {
  local pr_section
  require_section pr_section "${PHASE9_PR}" '## PR' 'P7: Phase 9 PR section' || return
  assert_contains "${pr_section}" '<Documentation checklist line — see below>' 'P7: the template routes the docs line through its rule'
  assert_not_contains "${pr_section}" $'\n- [ ] Documentation updated\n' 'P7: the template no longer hard-codes Documentation updated'
  assert_contains "${pr_section}" 'first line `policy: minimal` → exactly one documentation status line, from the file'"'"'s second line, in place of `- [ ] Documentation updated`' 'P7: minimal renders exactly one docs line'
  assert_contains "${pr_section}" '`- [x] docs: none (minimal policy)` when Phase 8 edited nothing' 'P7: docs: none rendering'
  assert_contains "${pr_section}" '`- [x] docs: updated <comma-separated repo-relative paths>` otherwise' 'P7: docs: updated rendering'
  assert_contains "${pr_section}" '`- [ ] docs: error (<reason>)`' 'P7: docs: error renders unchecked'
  assert_contains "${pr_section}" 'first line `policy: default` or `policy: unresolved` → `- [ ] Documentation updated`, unchanged' 'P7: default mode keeps Documentation updated'
  assert_contains "${pr_section}" 'maintenance failures, `validation:` lines and coverage gaps' 'P7: failures and gaps still render'
  assert_contains "${pr_section}" 'still render in `## Checklist` and `## Notes` exactly as above' 'P7: failures and gaps still render in Checklist and Notes'
  assert_contains "${pr_section}" 'docs: unknown (RUN_DIR lost' 'P7: a lost RUN_DIR renders an unchecked unknown line'
  assert_contains "${pr_section}" '`scripts/docs-policy.sh`' 'P7: Phase 9 re-resolves the policy on a lost RUN_DIR'
  assert_contains "${pr_section}" 'Documentation policy unresolved' 'P15: Phase 9 notes an unresolved policy'
  assert_contains "${docs_status_section}" '`docs: updated <comma-separated repo-relative paths>` listing every documentation path in the run'"'"'s final diff within this phase'"'"'s ownership classes' 'P7: Phase 8 docs: updated lists the final diff paths'
  assert_contains "${docs_status_section}" 'plus every plan-named doc file' 'P7: docs: updated includes plan-named doc files'
  assert_contains "${docs_status_section}" 'only when the PR changes no documentation at all' 'P7: docs: none only for a docs-free PR'
  assert_file_contains "${CODEX_MD}" 'In `minimal` mode, the PR body carries exactly one documentation status line in place of `Documentation updated`' 'P7: codex.md carries the docs line'
}

# --- P12: validation records -------------------------------------------------

case_validation_records() {
  assert_contains "${maintenance_section}" 'where `<status>` is exactly one of `pass`, `fail`, `skipped (<reason>)` or `unavailable`' 'P12: each validation records exactly one status'
  assert_contains "${maintenance_section}" 'write exactly one `validation: <label> — <status>[ — <tag>]` line per documentation validation this run applies' 'P12: one validation line per applied validation'
  assert_contains "${maintenance_section}" 'cenci'"'"'s checker (label `check.sh --changed`) and each repository docs check the plan names' 'P12: both validation sources are recorded'
  assert_contains "${maintenance_section}" 'through the client'"'"'s normal permission flow — add no new permission allow-list entry for it' 'P12: repository commands use the normal permission flow'
  assert_contains "${maintenance_section}" 'A non-zero exit records `fail` and enters the Fix now / Stop / Push anyway question above' 'P12: a repository-check fail enters the fail question'
  assert_contains "${maintenance_section}" 'A command the user denies at the permission prompt records `skipped (permission denied)`' 'P12: a denied command is skipped, not passed'
  assert_contains "${maintenance_section}" 'this paragraph always runs' 'P12: repository checks run even when the cenci checker stops early'
  assert_file_contains "${PHASE9_PR}" 'Every `unavailable` status and every `skipped (<reason>)` other than `skipped (no doc-affecting changes)`, on the first line or on a `validation:` line, renders as an unchecked `- [ ]` line' 'P12: Phase 9 renders unavailable/skipped unchecked'
  assert_file_contains "${PHASE9_PR}" '→ `- [ ] Maintenance check: skipped (<reason> — verify manually before merge)`' 'P12: other skipped reasons render unchecked'
  assert_contains "${maintenance_section}" 'write `maintenance: error (changed-file discovery failed)` to `$RUN_DIR/maintain-status.txt` when `$RUN_DIR` is known, otherwise report that status directly for Phase 9, followed by `validation: check.sh --changed — skipped (changed-file discovery failed)`, then continue with **Repository docs checks** below' 'P12: a changed-file discovery failure records its validation line'
  assert_contains "${maintenance_section}" 'write `maintenance: skipped (no doc-affecting changes)` to `$RUN_DIR/maintain-status.txt` (skip the write if `$RUN_DIR` is unknown), followed by `validation: check.sh --changed — skipped (no doc-affecting changes)`, then continue with **Repository docs checks** below' 'P12: no matching changed path records its validation line'
  assert_contains "${maintenance_section}" 'followed by `validation: check.sh --changed — skipped (checker execution failed)`, then continue with **Repository docs checks** below' 'P12: a checker crash records its validation line'
  assert_file_contains "${CODEX_MD}" 'a changed-file discovery failure records `validation: check.sh --changed — skipped (changed-file discovery failed)` and never enters the fail question' 'P12: codex.md records the discovery-failure validation line'
  assert_file_contains "${CODEX_MD}" 'a run with no doc-affecting changed path records `validation: check.sh --changed — skipped (no doc-affecting changes)`' 'P12: codex.md records the no-doc-affecting-changes validation line'
  assert_file_contains "${CODEX_MD}" 'A repository-declared docs command runs through the client'"'"'s normal permission flow with no new allow-list entry.' 'P12: codex.md keeps the permission-flow rule'
  assert_file_contains "${CODEX_MD}" 'as exactly one of `pass`, `fail`, `skipped (<reason>)` or `unavailable`' 'P12: codex.md records exactly one status'
}

# --- P18: unreadable docs-to-update.txt is never none ------------------------

case_unreadable_docs_to_update() {
  assert_contains "${docs_status_section}" "${P18_SENTENCE}" 'P18: Phase 8 never reports none for an unreadable docs-to-update.txt'
  assert_file_contains "${CODEX_MD}" 'an unreadable `docs-to-update.txt` reports `docs: error (docs-to-update.txt unreadable)`, never `docs: none (minimal policy)`' 'P18: codex.md carries the same rule'
  assert_file_contains "${PHASE9_PR}" 'an error is never rendered checked and never as `docs: none (minimal policy)`' 'P18: Phase 9 never renders an error as none'
  # The mechanism, not just the outcome: Minimal mode actually reads the file
  # when it evaluates condition 2 and distinguishes absent from unreadable.
  local minimal_section
  require_section minimal_section "${PHASE8_DOCS}" '### Minimal mode' 'P18: Phase 8 Minimal mode subsection' || return
  assert_contains "${minimal_section}" "${MIN_READ_DTU}" 'P18: Minimal mode reads docs-to-update.txt to evaluate condition 2'
  assert_contains "${minimal_section}" "${MIN_ABSENT_DTU}" 'P18: Minimal mode treats an absent file as no entry'
  assert_contains "${minimal_section}" "${MIN_UNREADABLE_DTU}" 'P18: Minimal mode records the unreadable error even when every sub-step is skipped'
  # Docs Status re-reads the files instead of relying on conversation memory,
  # and a Phase 6 + 7 persistence failure survives an absent file.
  assert_contains "${docs_status_section}" "${DS_REREAD_DTU}" 'P18: Docs Status re-reads docs-to-update.txt itself'
  assert_contains "${docs_status_section}" "${DS_PERSIST_ERR}" 'P18: the persistence-failure error survives an absent docs-to-update.txt'
  assert_contains "${docs_status_section}" "${DS_COPY_AT_DETECTION}" 'P18: the persistence-failure line is copied into docs-status.txt at detection'
  assert_contains "${policy_section}" "${RESOLVE_COPY_NOW}" 'P18: Phase 8 start copies a reported persistence failure into docs-status.txt'
  assert_contains "${docs_status_section}" "${DS_CARRY_PERSIST_ONLY}" 'P18: Docs Status carries forward only the provisional persistence-failed error'
  assert_contains "${docs_status_section}" "${DS_REDERIVED}" 'P18: every other docs error reason is re-derived on each entry'
  assert_not_contains "${docs_status_section}" 'carry forward every `docs: error (<reason>)` it already holds' 'P18: transient docs errors are never carried forward permanently'
  assert_file_contains "${CODEX_MD}" 'evaluate the `docs-to-update.txt` entry condition by reading `docs-to-update.txt` itself' 'P18: codex.md reads docs-to-update.txt to evaluate condition 2'
  assert_file_contains "${CODEX_MD}" "${MIN_UNREADABLE_DTU}" 'P18: codex.md records the unreadable error even when every step is skipped'
  assert_file_contains "${CODEX_MD}" 'a `docs: error (docs-to-update.txt persistence failed)` is still reported when `docs-to-update.txt` is absent' 'P18: codex.md keeps the persistence-failure error'
  assert_file_contains "${CODEX_MD}" "${CODEX_CARRY_PERSIST_ONLY}" 'P18: codex.md carries forward only the persistence-failed error'
  assert_file_contains "${CODEX_MD}" "${DS_REDERIVED}" 'P18: codex.md re-derives every other docs error reason on each entry'
}

# --- W2: a docs error reaches ## Notes in full in default mode ---------------

case_default_docs_error_notes() {
  local pr_section policy_doc
  require_section pr_section "${PHASE9_PR}" '## PR' 'W2: Phase 9 PR section' || return
  assert_contains "${pr_section}" "${P9_NOTES_DOC_STATUS_RULE}" 'W2: Phase 9 renders the whole docs error line into Notes for default and unresolved'
  assert_contains "${pr_section}" 'Documentation policy unresolved' 'W2: the explicit policy-unresolved sentence stays'
  assert_not_contains "${pr_section}" 'any other `docs: error (<reason>)` second line adds' 'W2: the single-reason Notes wording is gone'
  assert_file_contains "${CODEX_MD}" "${NOTES_DOC_STATUS}" 'W2: codex.md renders the whole docs error line into the notes'
  require_section policy_doc "${FLOW_README}" '### Documentation policy' 'W2: README Documentation policy subsection' || return
  assert_contains "${policy_doc}" "${README_NOTES_DOC_STATUS}" 'W2: README documents the default-mode Documentation status Notes line'
}

# --- W3: a malformed docs-status.txt is never trusted ------------------------

case_malformed_docs_status() {
  local pr_section
  require_section pr_section "${PHASE9_PR}" '## PR' 'W3: Phase 9 PR section' || return
  assert_contains "${pr_section}" "${MALFORMED_DEF}" 'W3: Phase 9 defines a malformed docs status file'
  assert_contains "${pr_section}" "${MALFORMED_UNKNOWN}" 'W3: Phase 9 renders a malformed file as an unchecked docs: unknown line'
  assert_contains "${pr_section}" 'Never write a docs status line from memory' 'W3: Phase 9 never writes a docs line from memory'
  assert_file_contains "${CODEX_MD}" "${MALFORMED_DEF}" 'W3: codex.md defines a malformed docs status file'
  assert_file_contains "${CODEX_MD}" "${MALFORMED_UNKNOWN}" 'W3: codex.md renders a malformed file as docs: unknown'
  assert_file_contains "${CODEX_MD}" 'a docs status line is never written from memory' 'W3: codex.md never writes a docs line from memory'
}

# --- Nit 60: the sanitized excerpt is recorded once, then cited --------------

case_validation_excerpt_line() {
  assert_contains "${maintenance_section}" "${P8_EXCERPT_RULE}" 'Nit60: Phase 8 records one sanitized excerpt line under a failing validation line'
  assert_not_contains "${maintenance_section}" 'Record a failing command'"'"'s exit code under its `validation:` line.' 'Nit60: the exit-code-only record is gone'
  assert_file_contains "${PHASE9_PR}" "${P9_NOTES_CITES_EXCERPT}" 'Nit60: Phase 9 Notes cite the recorded excerpt line'
  assert_file_contains "${PHASE9_PR}" "${P9_HALTED_CITES_EXCERPT}" 'Nit60: the halted gate cites the recorded excerpt line'
  assert_file_lacks "${PHASE9_PR}" 'the failing output recorded under it' 'Nit60: the halted gate no longer assumes raw output was recorded'
  assert_file_contains "${CODEX_MD}" "${EXCERPT_LINE}" 'Nit60: codex.md records the same excerpt line'
}

# --- Nit 55: the resolver comment matches its real warning text --------------

case_resolver_comment() {
  assert_file_contains "${RESOLVER}" 'never echoes the value and never contains `minimal`' 'Nit55: resolver comment states the warning never contains minimal'
  assert_file_lacks "${RESOLVER}" 'never contains a bare policy token' 'Nit55: the inaccurate bare-policy-token claim is gone'
}

# --- B: changed-file discovery includes untracked files ----------------------

case_changed_file_discovery() {
  assert_contains "${docs_status_section}" 'git -C <worktree-path> diff --name-only origin/main' 'B: Docs Status keeps the tracked diff'
  assert_contains "${docs_status_section}" "${UNTRACKED_CMD}" 'B: Docs Status unions untracked files into the final diff'
  assert_contains "${docs_status_section}" "${DISCOVERY_TMP}" 'B: Docs Status names the RUN_DIR-unknown capture location'
  assert_contains "${docs_status_section}" "${DS_DISCOVERY_FAIL}" 'B: a failed discovery is an error, never none'
  assert_contains "${maintenance_section}" "${UNTRACKED_CMD}" 'B: the Trigger guard recompute unions untracked files'
  assert_contains "${maintenance_section}" "${DISCOVERY_TMP}" 'B: the Trigger guard names the RUN_DIR-unknown capture location'
  assert_contains "${maintenance_section}" "${MAINT_DISCOVERY_FAIL}" 'B: either fallback failing records a discovery failure'
  assert_file_contains "${CODEX_MD}" 'the union of `git -C <abs-worktree-path> diff --name-only origin/main` and `git -C <abs-worktree-path> ls-files --others --exclude-standard`' 'B: codex.md unions untracked files'
}

# --- C: a failing repository docs command never pastes output verbatim -------

case_notes_secrecy() {
  assert_contains "${maintenance_section}" "${NOTES_EXCERPT}" 'C: Phase 8 limits the PR-body Notes entry to a sanitized excerpt'
  assert_contains "${maintenance_section}" 'showing the verbatim output in the local `AskUserQuestion` prompt stays fine' 'C: Phase 8 keeps the verbatim local prompt'
  assert_file_contains "${PHASE9_PR}" "${NOTES_EXCERPT}" 'C: Phase 9 Notes carry only a sanitized excerpt'
  assert_file_lacks "${PHASE9_PR}" 'appends its failing output to `## Notes` the same way' 'C: Phase 9 no longer pastes repository-check output into Notes'
  assert_file_contains "${CODEX_MD}" "${NOTES_EXCERPT}" 'C: codex.md limits the PR body to a sanitized excerpt'
  assert_file_contains "${CODEX_MD}" 'showing the verbatim output in the local user-input prompt stays fine' 'C: codex.md keeps the verbatim local prompt'
}

# --- E: resolver stdout only; every non-clean result is unresolved -----------

case_resolver_output_handling() {
  local pr_section
  assert_contains "${policy_section}" "${STDOUT_ONLY}" 'E: Phase 8 reads the policy token from stdout only'
  assert_contains "${policy_section}" "${UNRESOLVED_CATCH}" 'E: Phase 8 treats every non-clean result as unresolved'
  assert_contains "${policy_section}" 'exit 126/127' 'E: Phase 8 names the could-not-run exits'
  assert_contains "${policy_section}" 'empty or unexpected stdout' 'E: Phase 8 catches empty or unexpected stdout'
  require_section pr_section "${PHASE9_PR}" '## PR' 'E: Phase 9 PR section' || return
  assert_contains "${pr_section}" "${STDOUT_ONLY}" 'E: Phase 9 re-resolve reads stdout only'
  assert_contains "${pr_section}" "${UNRESOLVED_CATCH}" 'E: Phase 9 re-resolve treats every non-clean result as unknown'
  assert_file_contains "${CODEX_MD}" "${STDOUT_ONLY}" 'E: codex.md reads the policy token from stdout only'
  assert_file_contains "${CODEX_MD}" "${UNRESOLVED_CATCH}" 'E: codex.md treats every non-clean result as unresolved'
}

case_extractor_self_test
case_policy_absent
case_policy_unknown
case_policy_minimal
case_policy_unresolved
case_maintenance_pass
case_maintenance_warn
case_maintenance_fail
case_check_during_implement_false
case_checker_unavailable
case_configure_schema
case_readme_subsection
case_minimal_gating
case_phase9_docs_line
case_validation_records
case_unreadable_docs_to_update
case_changed_file_discovery
case_notes_secrecy
case_resolver_output_handling
case_default_docs_error_notes
case_malformed_docs_status
case_validation_excerpt_line
case_resolver_comment

if [[ "${failures}" -gt 0 ]]; then
  echo "docs-policy-minimal-contract.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "docs-policy-minimal-contract.test.sh: all assertions passed"
