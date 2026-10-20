#!/usr/bin/env bash
# Parity contract test for #1303: the Claude and Codex surfaces carry the same
# minimal-policy, documentation-routing and lesson-admission marker phrases.
#
# These assertions check instruction text only; they do not prove end-to-end agent judgment.
#
# The gap this pins down: documentation policy, routing and lesson capture are
# restated across five Claude instruction files (phase-8-docs.md, phase-9-pr.md,
# agents/planner.md, agents/implementer.md, agents/lessons-collector.md, plus
# agents/code-reviewer.md for the reviewer routing phrase) and their Codex
# counterparts (skills/implement/codex.md and the planner, implementer and
# critical-reviewer role TOMLs in templates/codex/agent-roles/ with their
# .codex/agents/ copies). Each suite that owns one rule pins one site; nothing
# pinned that a rule reached BOTH surfaces. This suite does, per pair: a marker
# row lists the Claude sites that carry it and the Codex sites that must carry
# the same phrase, and every listed site is checked. Role TOMLs are condensed
# prose, so a TOML row uses a fragment that appears verbatim in both the Claude
# file and the role.
#
# Also checked: the three role template/installed pairs are byte-identical, and
# `implement` and `maintain` are absent from PORTABLE_SKILLS in
# opencode/install-skills.sh (both are Claude/Codex pipeline skills, so no
# OpenCode file changes -- docs/skill-authoring.md "Client surfaces").
#
# Single-occurrence enforcement of the individual sentences stays with
# doc-routing-contract.test.sh, docs-policy-minimal-contract.test.sh and
# lessons-routing-contract.test.sh; this suite only requires presence on both
# surfaces. Markers are single-source-line constants (gotchas rule 13), counted
# with `grep -oF | wc -l` (rule 10(4)), and the matrix evaluator has non-vacuity
# self-tests (rules 10, 29, 30): a missing marker, an unreadable site and an
# empty or comma-only site list are each caught, a marker present at one site and
# absent at another reports exactly the missing site, and an injected `implement`
# in PORTABLE_SKILLS is caught.
#
# Acceptance criteria covered: P14 (the parity matrix and the PORTABLE_SKILLS
# check), P29 (both new contract-test headers carry the instruction-text-only
# sentence and neither overclaims what the assertions establish).
#
# No `read_*`-named helpers (flow/tests/read-helper-purity-contract.test.sh);
# no permission-removing chmod (flow/tests/root-safe-perms-contract.test.sh).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "docs-policy-parity-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "docs-policy-parity-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "docs-policy-parity-contract.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# marker_count <content> <marker> -- occurrences (not lines) of a literal
# marker. Pure; prints a number, or ERROR when grep itself failed.
marker_count() {
  local content="$1" marker="$2" out rc
  out="$(grep -oF -e "${marker}" <<<"${content}")"
  rc=$?
  if [[ ${rc} -gt 1 ]]; then
    echo "ERROR"
    return
  fi
  if [[ -z "${out}" ]]; then
    echo 0
    return
  fi
  printf '%s\n' "${out}" | wc -l | tr -d '[:space:]'
}

# site_problem <repo-relative-path> <marker> -- prints why the file does not
# carry the marker, or nothing when it does. Pure, no fail().
site_problem() {
  local rel="$1" marker="$2" content count
  if [[ -z "${marker}" ]]; then
    echo "empty marker (test bug)"
    return
  fi
  if ! content="$(cat "${REPO_DIR}/${rel}" 2>/dev/null)"; then
    echo "${rel}: unreadable or missing"
    return
  fi
  count="$(marker_count "${content}" "${marker}")"
  case "${count}" in
    ERROR) echo "${rel}: grep error while counting" ;;
    0) echo "${rel}: marker missing" ;;
  esac
}

# expand_sites <comma-separated-sites> -- one repo-relative path per line. A
# `toml:<role>` token expands to the role template and its installed copy.
# Prints nothing for an empty list.
expand_sites() {
  local list="$1" token
  local -a tokens=()
  [[ -n "${list}" ]] || return 0
  IFS=',' read -r -a tokens <<<"${list}"
  for token in "${tokens[@]}"; do
    case "${token}" in
      toml:*)
        printf '%s\n' "flow/templates/codex/agent-roles/${token#toml:}.toml" ".codex/agents/${token#toml:}.toml"
        ;;
      *)
        printf '%s\n' "${token}"
        ;;
    esac
  done
}

# row_problems <class> <marker-variable-name> <claude-sites> <codex-sites> --
# one problem per line; nothing when every listed site carries the marker. An
# unset marker, an empty site list on either surface, or an unreadable site is
# a problem, never a silent pass. Pure.
row_problems() {
  local class="$1" var="$2" claude="$3" codex="$4" marker site problem claude_sites codex_sites
  marker="${MARKER[${var}]:-}"
  if [[ -z "${marker}" ]]; then
    echo "${class}/${var}: marker variable is unset or empty (test bug)"
    return
  fi
  # Judge each surface by its EXPANDED site list: a list such as "," is non-empty
  # as a string but names no site, and must not pass with nothing checked.
  claude_sites="$(expand_sites "${claude}")"
  codex_sites="$(expand_sites "${codex}")"
  if ! grep -q '[^[:space:]]' <<<"${claude_sites}"; then
    echo "${class}/${var}: no Claude site listed"
  fi
  if ! grep -q '[^[:space:]]' <<<"${codex_sites}"; then
    echo "${class}/${var}: no Codex site listed"
  fi
  while IFS= read -r site; do
    [[ -n "${site}" ]] || continue
    problem="$(site_problem "${site}" "${marker}")"
    if [[ -n "${problem}" ]]; then
      echo "${class}/${var}: ${problem}"
    fi
  done <<<"${claude_sites}"$'\n'"${codex_sites}"
}

# header_problem <file> -- prints why the file's header does not carry the P29
# sentence or overclaims, else nothing. Only the leading comment block counts.
P29_SENTENCE='These assertions check instruction text only; they do not prove end-to-end agent judgment.'
header_problem() {
  local file="$1" header stripped
  if ! header="$(awk 'NR == 1 && /^#!/ { next } /^#/ { print; next } { exit }' "${file}" 2>/dev/null)"; then
    echo "${file}: unreadable"
    return
  fi
  if [[ -z "${header}" ]]; then
    echo "${file}: no leading comment block"
    return
  fi
  if [[ "$(marker_count "${header}" "${P29_SENTENCE}")" != "1" ]]; then
    echo "${file}: the header must carry the instruction-text-only sentence exactly once"
    return
  fi
  stripped="${header//"${P29_SENTENCE}"/}"
  if grep -qiE '(proves?|guarantees?|demonstrates?|verifies) .*agent (judg|behavio)' <<<"${stripped}"; then
    echo "${file}: the header claims to prove agent judgment or behavior"
  fi
}

# portable_skills_problem <install-skills-content> -- prints why the
# PORTABLE_SKILLS declaration is wrong, else nothing. Pure.
portable_skills_problem() {
  local content="$1" decl lines list word
  local -a words=()
  lines="$(grep -c '^PORTABLE_SKILLS="' <<<"${content}")"
  if [[ "${lines}" != "1" ]]; then
    echo "expected exactly one PORTABLE_SKILLS=\" line, found ${lines}"
    return
  fi
  decl="$(grep '^PORTABLE_SKILLS="' <<<"${content}")"
  list="${decl#PORTABLE_SKILLS=\"}"
  list="${list%\"*}"
  read -r -a words <<<"${list}"
  if [[ "${#words[@]}" -eq 0 ]]; then
    echo "PORTABLE_SKILLS is empty"
    return
  fi
  for word in "${words[@]}"; do
    case "${word}" in
      implement | maintain) echo "PORTABLE_SKILLS lists ${word}, a Claude/Codex pipeline skill" ;;
    esac
  done
}

# --------------------------------------------------------------------------
# Marker sentences. Each is ONE source line wherever it appears. Existing
# markers are byte-identical to doc-routing-contract.test.sh and
# docs-policy-minimal-contract.test.sh; the lesson markers to
# lessons-routing-contract.test.sh. M_* are whole sentences restated
# byte-identically on both surfaces; F_* are fragments that also appear
# verbatim in the condensed role TOMLs.
# --------------------------------------------------------------------------
declare -A MARKER=()
# documentation routing
MARKER[M_PROHIBIT]='When a documentation convention is discovered, never create a `docs/<topic>.md`, append to `## Critical Rules`, or write a second copy of a contract; route the write to the discovered owner.'
MARKER[M_ROUTE_OWNER]='Write each documentation edit to the discovered `owner:` path that owns its subject, never to a file picked by assumption.'
MARKER[M_ROUTER_NEW]='Edit a router (`AGENTS.md`/`CLAUDE.md`) only when a new focused owner doc is added or an existing reading trigger becomes inaccurate.'
MARKER[M_ROUTER_FEATURE]='Route feature contracts, component catalogs, setup detail and operational procedures to their focused owner doc instead of `AGENTS.md`.'
MARKER[M_ROUTER_EXPLICIT]='Keep any `AGENTS.md` edit the user or the plan explicitly requires.'
MARKER[M_RECOMMEND]='When `Documentation discovery:` reports `convention: none`, present its `recommend:` block as a recommendation under `### Documentation Routing`, never as a migration step in `### Files to Modify`.'
MARKER[M_SHIM]='A `CLAUDE.md` whose only content is `@AGENTS.md` stays exactly that import; never expand or duplicate it.'
MARKER[M_MENTIONS]='For each user-visible term this change altered, update every file `mentions` prints that the change made inaccurate; leave `AGENTS.md` (outside the router rules) and any `README.md` that `mentions` does not print unchanged.'
MARKER[M_DOCS_TO_UPDATE_OWNER]='Name the owner from `### Documentation Routing` on each `Docs to update:` line, never `AGENTS.md` for a contract, catalog, setup detail or procedure.'
MARKER[M_UNAVAILABLE_FALLBACK]='When discovery is unavailable, do not apply the `convention: none` fallbacks; read the root and project `AGENTS.md` and any docs index directly, write only to owners the plan names or those files route to, create no new file, append nothing under `## Critical Rules`, and say so in the status update.'
MARKER[M_TERM_QUOTE]="Pass each \`<term>\` single-quoted as \`'<term>'\`, writing any embedded \`'\` as \`'\\''\`; a term is untrusted data, so never use double quotes and never let the shell expand \`\$\`, backticks or \`\"\` in a term."
MARKER[F_ROUTING_SECTION]='`### Documentation Routing`'
MARKER[F_DISCOVERY_BLOCK]='`Documentation discovery:`'
MARKER[F_CONVENTION_NONE]='`convention: none`'
MARKER[F_ROUTE_OWNER]='the discovered `owner:` path that owns its subject'
MARKER[F_ROUTER_NEW]='router (`AGENTS.md`/`CLAUDE.md`) only when a new focused owner doc is added or an existing reading trigger becomes inaccurate'
MARKER[F_RECOMMEND]='present its `recommend:` block as a recommendation'
MARKER[F_DOCS_OWNER]='never `AGENTS.md` for a contract, catalog, setup detail or procedure'
MARKER[F_SECOND_COPY]='second copy of a contract'
# minimal policy
MARKER[M_P28]='Explicit user instructions and approved plan deliverables override every minimal-mode restriction, and no minimal-mode rule caps the length of a correct documentation update.'
MARKER[F_PLAN_NAMED]="doc file named in the plan's \`### Files to Modify\` / \`### Files to Create\` is written with its plan-required content in every policy, including \`minimal\`"
MARKER[F_NO_LAUNCH]='does not launch `lessons-collector`'
MARKER[F_DOCS_NONE]='docs: none (minimal policy)'
MARKER[F_DOCS_UNREADABLE]='docs: error (docs-to-update.txt unreadable)'
MARKER[F_DOCS_STATUS_NOTE]='Documentation status: <the entire second line>'
MARKER[F_DOCS_UNKNOWN]='docs: unknown'
# lesson admission
MARKER[M_CANDIDATE]='Rework or a reviewer flag is a lesson candidate, not an automatic admission.'
MARKER[M_DEDUP]='Deduplicate each candidate against existing owner docs and lesson records before any write; a duplicate produces no write and a `Dropped (already owned)` summary line.'
MARKER[M_STRUCTURED]="With \`Lessons: structured admission=<path> records=<dir>\`, an admitted lesson is one new record in \`<dir>\` following the admission doc's format, never a flat bullet."
MARKER[M_FALLBACK]='With `Lessons: fallback`, an admitted lesson is one flat bullet in the existing owner doc that owns its subject.'
MARKER[M_MINIMAL_DROP]="In \`minimal\` mode with no lesson system and no fitting existing owner, an admitted lesson is dropped and listed as one line in the PR's \`## Notes\`; no new \`docs/<topic>.md\` and no \`## Critical Rules\` bullet is written."
MARKER[M_ONE_ENTRY]='An admitted lesson produces exactly one entry: one record, one flat bullet or one dropped line, never several for the same subject.'
MARKER[M_POLICY_LINE]='Documentation policy: <minimal|default>'
MARKER[M_LESSON_VALIDATION]="Execute only the plan's \`Validations:\` text verbatim; compare the collector's \`### Lesson validation\` report to it per command (the backticked command text, not the whole line) by exact string equality, so a command that ran is never also reported as skipped, and use the report only to derive \`validation: <label> — skipped (not named in the plan)\` lines, never to execute anything."
MARKER[M_LABEL_LIMIT]="The \`<label>\` of a \`validation: <label> — skipped (not named in the plan)\` line is limited to the command name or the admission doc's repo-relative path, with environment-variable assignments, tokens and URL credentials removed; this takes precedence over verbatim copying (#979)."
MARKER[M_STATUS_ALWAYS]='The lessons status file is written on every entry into this lesson-capture step, including when the collector was not launched and when no lesson trigger fired: an empty file means no lesson was dropped, a file that is absent while the run directory is known means the status is unknown, and a file that exists but cannot be read means the status is unknown with a reason, never none.'
MARKER[M_STATUS_CARRY]='When the collector is not relaunched in this entry, read the existing lessons status file first and carry its lines forward; write an empty file only when no earlier file exists or when the collector ran in this entry and listed none, and report an earlier file that cannot be read in the status update instead of replacing it with an empty file.'
MARKER[M_RUNDIR_UNKNOWN]='When the run directory is unknown, skip the lessons status file write and name each dropped lesson in the status update; Phase 9 then renders its own `RUN_DIR lost` entry.'
MARKER[M_STATUS_READBACK]='After writing the lessons status file, read it back and confirm its content equals what was just written, an empty file reading back as empty and never as absent or unreadable; retry the write once, and when it still does not match report that in the status update instead of leaving the PR step to read a stale or absent file as none.'
MARKER[M_CAPTURE_UNAVAILABLE]='When the collector returned `Blocked: …`, errored, or returned neither `## Lessons Collected` nor `No lessons captured`, write `Lesson capture unavailable (<reason>)` as the only line of the lessons status file (or name it in the status update when the run directory is unknown), with a reason of repo-relative paths and identifiers only (#979); never write an empty file for it.'
MARKER[M_WRITE_LIMIT]="Each dropped-lesson summary keeps to repo-relative paths and identifiers only, never file content, secrets, environment values, URL credentials or command output, because Phase 9 copies it into a PR body that can reach a public repository; this takes precedence over copying the collector's line verbatim (#979)."
MARKER[M_RENDER_CUT]="A dropped-lesson line that carries anything beyond a repo-relative path or an identifier (file content, a command's output, a secret, an environment value, a URL credential) is cut down to its identifier-level part before it is rendered: this secrecy rule takes precedence over copying the line verbatim (#979), because a PR body can reach a possibly-public repository."
MARKER[M_UNAVAILABLE_RENDER]='Render a `Lesson capture unavailable (<reason>)` line as one unchecked `- [ ]` entry under `## Notes` → `### Considered and discarded` in every policy, never as "none" and never as a dropped lesson, with the reason cut down to repo-relative paths and identifiers only (#979).'
MARKER[M_ABSENT_RENDER]='When the run directory is unknown, or the lessons status file is absent or cannot be read, render one unchecked `- [ ] Lesson status unavailable (<reason>)` entry under `## Notes` → `### Considered and discarded` in every policy, never as "none", with the reason limited to `RUN_DIR lost`, `file absent` or `file unreadable` and never a path or a raw tool error (#979); the lesson-capture step always writes the file, so an absent file means the status is unknown, not that no lesson was dropped.'
MARKER[M_UNRECOGNISED_LINE]='Any non-empty lessons status file that contains a line matching neither `Lesson dropped (minimal policy):` nor `Lesson capture unavailable (` is treated as unreadable: render `- [ ] Lesson status unavailable (file unreadable)` and copy none of the unrecognised text into the PR body (#979).'
MARKER[M_EMPTY_STATUS_READ]="An empty lessons status file counts as empty (none), not unreadable: when the client's read tool returns no content or only a system reminder for an empty file, that counts as empty, and the client's shell (\`test -e\` / \`test -s\`, or \`cat\` and its exit status) confirms it, rather than a reminder-only read being treated as unreadable."
MARKER[M_LESSON_FAIL]='A lesson validation that fails after a record was created is recorded as a `fail` `validation:` line in the maintenance status file and enters the Fix now / Stop / Push anyway question, never as a pass.'
MARKER[M_NOTES_RENDER]='Render each `Lesson dropped (minimal policy):` line as one entry under `## Notes` → `### Considered and discarded`, using repo-relative paths and identifiers only, never file content or command output.'
MARKER[M_ONLY_MINIMAL_REACHES]="Only \`### Dropped (minimal policy)\` entries reach the lessons status file and the PR's \`## Notes\`; every other dropped entry stays in the collector's summary."
MARKER[M_SKIPPED_WIRING]='Write each `validation: <label> — skipped (not named in the plan)` line for a collector-reported command into the maintenance status file with the other `validation:` lines, never as `pass`; Phase 9 renders it as an unchecked `Docs check skipped` line.'
MARKER[M_SKIPPED_REDERIVE]="Re-derive that skipped line on every entry from the collector's \`### Lesson validation\` report; when the collector was not relaunched or reported none, read the earlier status file before the first-line rewrite and carry its \`skipped (not named in the plan)\` lines forward."
MARKER[F_DROPPED_LINE]='Lesson dropped (minimal policy):'
MARKER[F_PLANNER_VALIDATION]='with a structured `Lessons:` line, list the validation command its admission doc names, if any'

PHASE8='flow/skills/implement/phases/phase-8-docs.md'
PHASE9='flow/skills/implement/phases/phase-9-pr.md'
CODEX='flow/skills/implement/codex.md'
PLANNER='flow/agents/planner.md'
IMPLEMENTER='flow/agents/implementer.md'
COLLECTOR='flow/agents/lessons-collector.md'
CODE_REVIEWER='flow/agents/code-reviewer.md'

# class|marker-variable|Claude sites (comma-separated)|Codex sites (comma-separated)
ROWS=(
  # --- documentation routing: phase-8 / planner.md <-> codex.md / planner role
  "routing|M_PROHIBIT|${PHASE8},${PLANNER}|${CODEX}"
  "routing|M_ROUTE_OWNER|${PHASE8},${PLANNER}|${CODEX}"
  "routing|F_ROUTE_OWNER|${PLANNER}|toml:planner"
  "routing|M_ROUTER_NEW|${PHASE8},${PLANNER}|${CODEX}"
  "routing|F_ROUTER_NEW|${PLANNER}|toml:planner"
  "routing|M_ROUTER_FEATURE|${PHASE8},${PLANNER}|${CODEX}"
  "routing|M_ROUTER_EXPLICIT|${PHASE8},${PLANNER}|${CODEX}"
  "routing|M_RECOMMEND|${PLANNER}|${CODEX}"
  "routing|F_RECOMMEND|${PLANNER}|toml:planner"
  "routing|F_DISCOVERY_BLOCK|${PLANNER}|${CODEX},toml:planner"
  "routing|F_CONVENTION_NONE|${PLANNER},${PHASE8}|${CODEX},toml:planner"
  "routing|M_SHIM|${PHASE8}|${CODEX}"
  "routing|M_MENTIONS|${PHASE8}|${CODEX}"
  "routing|M_UNAVAILABLE_FALLBACK|${PHASE8}|${CODEX}"
  "routing|M_TERM_QUOTE|${PHASE8}|${CODEX}"
  # --- documentation routing: implementer.md <-> implementer role
  "routing|M_DOCS_TO_UPDATE_OWNER|${IMPLEMENTER}|${CODEX}"
  "routing|F_DOCS_OWNER|${IMPLEMENTER}|toml:implementer"
  # --- documentation routing: code-reviewer.md <-> critical-reviewer role
  "routing|F_SECOND_COPY|${CODE_REVIEWER},${PHASE8}|toml:critical-reviewer,${CODEX}"
  # --- the routing section is named on every consuming surface
  "routing|F_ROUTING_SECTION|${PLANNER},${IMPLEMENTER},${CODE_REVIEWER},${PHASE8}|${CODEX},toml:planner,toml:implementer,toml:critical-reviewer"
  # --- minimal policy
  "minimal-policy|M_P28|${PHASE8}|${CODEX}"
  "minimal-policy|F_PLAN_NAMED|${IMPLEMENTER},${PHASE8}|${CODEX},toml:implementer"
  "minimal-policy|F_NO_LAUNCH|${PHASE8}|${CODEX}"
  "minimal-policy|F_DOCS_NONE|${PHASE8},${PHASE9}|${CODEX}"
  "minimal-policy|F_DOCS_UNREADABLE|${PHASE8}|${CODEX}"
  "minimal-policy|F_DOCS_STATUS_NOTE|${PHASE9}|${CODEX}"
  "minimal-policy|F_DOCS_UNKNOWN|${PHASE9}|${CODEX}"
  # --- lesson admission: collector / phase-8 / phase-9 <-> codex.md / planner role
  "lesson-admission|M_CANDIDATE|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_DEDUP|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_STRUCTURED|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_FALLBACK|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_MINIMAL_DROP|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_ONE_ENTRY|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_POLICY_LINE|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_LESSON_VALIDATION|${PHASE8}|${CODEX}"
  "lesson-admission|M_LABEL_LIMIT|${PHASE8},${PHASE9}|${CODEX}"
  "lesson-admission|M_STATUS_ALWAYS|${PHASE8}|${CODEX}"
  "lesson-admission|M_STATUS_CARRY|${PHASE8}|${CODEX}"
  "lesson-admission|M_RUNDIR_UNKNOWN|${PHASE8}|${CODEX}"
  "lesson-admission|M_STATUS_READBACK|${PHASE8}|${CODEX}"
  "lesson-admission|M_CAPTURE_UNAVAILABLE|${PHASE8}|${CODEX}"
  "lesson-admission|M_WRITE_LIMIT|${PHASE8}|${CODEX}"
  "lesson-admission|M_RENDER_CUT|${PHASE9}|${CODEX}"
  "lesson-admission|M_UNAVAILABLE_RENDER|${PHASE9}|${CODEX}"
  "lesson-admission|M_ABSENT_RENDER|${PHASE9}|${CODEX}"
  "lesson-admission|M_UNRECOGNISED_LINE|${PHASE9}|${CODEX}"
  "lesson-admission|M_EMPTY_STATUS_READ|${PHASE8},${PHASE9}|${CODEX}"
  "lesson-admission|M_LESSON_FAIL|${PHASE8}|${CODEX}"
  "lesson-admission|M_ONLY_MINIMAL_REACHES|${COLLECTOR},${PHASE8}|${CODEX}"
  "lesson-admission|M_SKIPPED_WIRING|${PHASE8}|${CODEX}"
  "lesson-admission|M_SKIPPED_REDERIVE|${PHASE8}|${CODEX}"
  "lesson-admission|M_NOTES_RENDER|${PHASE9}|${CODEX}"
  "lesson-admission|F_DROPPED_LINE|${PHASE8},${PHASE9}|${CODEX}"
  "lesson-admission|F_PLANNER_VALIDATION|${PLANNER}|toml:planner"
)

# =====================================================================
# P14: the marker matrix. Every row is evaluated; each of the three marker
# classes must have rows (a class that lost all its rows is not "covered").
# =====================================================================
CLASS_ROWS_minimal_policy=0
CLASS_ROWS_routing=0
CLASS_ROWS_lesson_admission=0
for ROW in "${ROWS[@]}"; do
  IFS='|' read -r ROW_CLASS ROW_VAR ROW_CLAUDE ROW_CODEX <<<"${ROW}"
  case "${ROW_CLASS}" in
    minimal-policy) CLASS_ROWS_minimal_policy=$((CLASS_ROWS_minimal_policy+1)) ;;
    routing) CLASS_ROWS_routing=$((CLASS_ROWS_routing+1)) ;;
    lesson-admission) CLASS_ROWS_lesson_admission=$((CLASS_ROWS_lesson_admission+1)) ;;
    *) fail "row [${ROW}]: unknown marker class [${ROW_CLASS}] (test bug)" ;;
  esac
  PROBLEMS="$(row_problems "${ROW_CLASS}" "${ROW_VAR}" "${ROW_CLAUDE}" "${ROW_CODEX}")"
  if [[ -n "${PROBLEMS}" ]]; then
    while IFS= read -r PROBLEM; do
      fail "parity: ${PROBLEM}"
    done <<<"${PROBLEMS}"
  fi
done
for CLASS_COUNT in "${CLASS_ROWS_minimal_policy}" "${CLASS_ROWS_routing}" "${CLASS_ROWS_lesson_admission}"; do
  if [[ "${CLASS_COUNT}" -eq 0 ]]; then
    fail "parity: a marker class has no rows (minimal-policy=${CLASS_ROWS_minimal_policy} routing=${CLASS_ROWS_routing} lesson-admission=${CLASS_ROWS_lesson_admission})"
  fi
done

# =====================================================================
# P14: the Codex role templates and their installed copies are byte-identical.
# =====================================================================
for ROLE in planner implementer critical-reviewer; do
  TEMPLATE_TOML="${FLOW_DIR}/templates/codex/agent-roles/${ROLE}.toml"
  INSTALLED_TOML="${REPO_DIR}/.codex/agents/${ROLE}.toml"
  if [[ ! -f "${TEMPLATE_TOML}" || ! -f "${INSTALLED_TOML}" ]]; then
    fail "${ROLE}.toml: template or installed copy missing (${TEMPLATE_TOML}, ${INSTALLED_TOML})"
    continue
  fi
  if ! cmp -s "${TEMPLATE_TOML}" "${INSTALLED_TOML}"; then
    fail "${ROLE}.toml: flow/templates/codex/agent-roles and .codex/agents copies differ (must be byte-identical)"
  fi
done

# =====================================================================
# P14: OpenCode is unaffected -- `implement` and `maintain` are not portable.
# =====================================================================
if INSTALL_SKILLS="$(cat "${FLOW_DIR}/opencode/install-skills.sh" 2>/dev/null)"; then
  PORTABLE_PROBLEM="$(portable_skills_problem "${INSTALL_SKILLS}")"
  if [[ -n "${PORTABLE_PROBLEM}" ]]; then
    fail "opencode/install-skills.sh: ${PORTABLE_PROBLEM}"
  fi
else
  fail "opencode/install-skills.sh: unreadable"
  INSTALL_SKILLS=""
fi

# =====================================================================
# P29: both new contract-test headers carry the instruction-text-only
# sentence and never claim to prove agent judgment.
# =====================================================================
for SUITE in docs-policy-parity-contract.test.sh lessons-routing-contract.test.sh; do
  HEADER_PROBLEM="$(header_problem "${SCRIPT_DIR}/${SUITE}")"
  if [[ -n "${HEADER_PROBLEM}" ]]; then
    fail "P29: ${HEADER_PROBLEM}"
  fi
done

# =====================================================================
# Non-vacuity self-tests.
# =====================================================================
SELFTEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/docs-policy-parity-contract.XXXXXX")" || { echo "docs-policy-parity-contract.test.sh: mktemp failed." >&2; exit 2; }
[[ -n "${SELFTEST_DIR}" && -d "${SELFTEST_DIR}" ]] || { echo "docs-policy-parity-contract.test.sh: mktemp produced no directory." >&2; exit 2; }
trap 'rm -rf -- "${SELFTEST_DIR}"' EXIT

# (1) The matrix evaluator reports a marker absent from a listed site, an
# unreadable site, an empty site list and an unset marker variable, and stays
# silent for a marker that is present.
if [[ -n "$(row_problems selftest M_P28 "${PHASE8}" "${CODEX}")" ]]; then
  fail "self-test baseline: a known-good row reports problems: $(row_problems selftest M_P28 "${PHASE8}" "${CODEX}")"
fi
MARKER[SELF_ABSENT]='this sentence is in no flow file ZZ-parity-selftest'
if [[ -z "$(row_problems selftest SELF_ABSENT "${PHASE8}" "${CODEX}")" ]]; then
  fail "self-test: a marker absent from every site was not reported (vacuous)"
fi
if [[ -z "$(row_problems selftest M_P28 "${PHASE8}" "flow/skills/implement/does-not-exist.md")" ]]; then
  fail "self-test: an unreadable Codex site was not reported (vacuous)"
fi
if [[ -z "$(row_problems selftest M_P28 "${PHASE8}" "")" ]]; then
  fail "self-test: an empty Codex site list was not reported (vacuous)"
fi
if [[ -z "$(row_problems selftest M_P28 "" "${CODEX}")" ]]; then
  fail "self-test: an empty Claude site list was not reported (vacuous)"
fi
# A list that is non-empty as a string but expands to no site (",") checks
# nothing, so it must fail rather than pass.
if [[ -z "$(row_problems selftest M_P28 "${PHASE8}" ",")" ]]; then
  fail "self-test: a Codex site list that expands to no site was not reported (vacuous)"
fi
if [[ -z "$(row_problems selftest M_P28 "," "${CODEX}")" ]]; then
  fail "self-test: a Claude site list that expands to no site was not reported (vacuous)"
fi
# Mixed case: a marker present at one site and absent at another reports exactly
# the missing site, in either direction -- not "some problem", and not the site
# that does carry it.
MARKER[SELF_ONLY_CODEX]='Lesson capture: forward the plan'
MIXED_PROBLEMS="$(row_problems selftest SELF_ONLY_CODEX "${PHASE8}" "${CODEX}")"
if [[ "$(grep -c . <<<"${MIXED_PROBLEMS}")" != "1" || "${MIXED_PROBLEMS}" != *"${PHASE8}: marker missing"* || "${MIXED_PROBLEMS}" == *"${CODEX}"* ]]; then
  fail "self-test: a marker present only in codex.md must report exactly ${PHASE8} (got: ${MIXED_PROBLEMS:-nothing})"
fi
MARKER[SELF_ONLY_PHASE8]='Pass that exact verified `<worktree-path>` as `<project-root>`'
MIXED_PROBLEMS="$(row_problems selftest SELF_ONLY_PHASE8 "${PHASE8}" "${CODEX}")"
if [[ "$(grep -c . <<<"${MIXED_PROBLEMS}")" != "1" || "${MIXED_PROBLEMS}" != *"${CODEX}: marker missing"* || "${MIXED_PROBLEMS}" == *"${PHASE8}"* ]]; then
  fail "self-test: a marker present only in phase-8-docs.md must report exactly ${CODEX} (got: ${MIXED_PROBLEMS:-nothing})"
fi
if [[ -z "$(row_problems selftest NO_SUCH_MARKER_VARIABLE "${PHASE8}" "${CODEX}")" ]]; then
  fail "self-test: an unset marker variable was not reported (vacuous)"
fi
if [[ -z "$(site_problem 'flow/does-not-exist.md' "${MARKER[M_P28]}")" ]]; then
  fail "self-test: a missing file was not reported by site_problem (vacuous)"
fi

# (2) expand_sites doubles a TOML token and drops an empty list.
if [[ "$(expand_sites 'toml:planner' | wc -l | tr -d '[:space:]')" != "2" ]]; then
  fail "self-test: a toml:<role> token must expand to the template and the installed copy"
fi
if [[ -n "$(expand_sites '')" ]]; then
  fail "self-test: an empty site list must expand to nothing"
fi

# (3) The PORTABLE_SKILLS check holds on the real file, has a positive control,
# and catches an injected `implement` or `maintain`.
if [[ "$(marker_count "${INSTALL_SKILLS}" ' babysit ')" == "0" ]]; then
  fail "self-test: positive control failed -- babysit is not in PORTABLE_SKILLS, so the check may be reading the wrong line"
fi
INJECTED_IMPLEMENT="${INSTALL_SKILLS/PORTABLE_SKILLS=\"/PORTABLE_SKILLS=\"implement }"
if [[ -z "$(portable_skills_problem "${INJECTED_IMPLEMENT}")" ]]; then
  fail "self-test: an injected implement in PORTABLE_SKILLS was not caught (vacuous)"
fi
INJECTED_MAINTAIN="${INSTALL_SKILLS/PORTABLE_SKILLS=\"/PORTABLE_SKILLS=\"maintain }"
if [[ -z "$(portable_skills_problem "${INJECTED_MAINTAIN}")" ]]; then
  fail "self-test: an injected maintain in PORTABLE_SKILLS was not caught (vacuous)"
fi
if [[ -z "$(portable_skills_problem 'no declaration here')" ]]; then
  fail "self-test: a missing PORTABLE_SKILLS declaration was not caught (vacuous)"
fi

# (4) The P29 header check accepts the real headers and catches a missing
# sentence and an overclaim.
printf '%s\n' '#!/usr/bin/env bash' '# A header without the required sentence.' 'set -u' > "${SELFTEST_DIR}/no-sentence.sh" || fail "self-test: could not write the no-sentence fixture"
if [[ -z "$(header_problem "${SELFTEST_DIR}/no-sentence.sh")" ]]; then
  fail "self-test: a header without the P29 sentence was not caught (vacuous)"
fi
printf '%s\n' '#!/usr/bin/env bash' "# ${P29_SENTENCE}" '# This suite proves agent judgment end to end.' 'set -u' > "${SELFTEST_DIR}/overclaim.sh" || fail "self-test: could not write the overclaim fixture"
if [[ -z "$(header_problem "${SELFTEST_DIR}/overclaim.sh")" ]]; then
  fail "self-test: a header claiming to prove agent judgment was not caught (vacuous)"
fi
printf '%s\n' '#!/usr/bin/env bash' "# ${P29_SENTENCE}" 'set -u' > "${SELFTEST_DIR}/good.sh" || fail "self-test: could not write the good-header fixture"
if [[ -n "$(header_problem "${SELFTEST_DIR}/good.sh")" ]]; then
  fail "self-test: a correct header was reported: $(header_problem "${SELFTEST_DIR}/good.sh")"
fi

echo "docs-policy-parity-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
