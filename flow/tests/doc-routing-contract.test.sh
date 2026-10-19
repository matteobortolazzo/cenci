#!/usr/bin/env bash
# Contract test for #1302: repository-aware documentation routing through plan,
# implement, review and Phase 8.
#
# The gap this pins down: Phase 8 and the planner assumed one documentation
# layout (append to `## Critical Rules`, let the collector create a topic doc,
# edit AGENTS.md for anything project-wide). A repository with nested
# per-project routers, a `docs/index.md`, focused owner docs and a structured
# lesson system was handled as if none of that existed. #1302 adds
# `skills/implement/scripts/doc-conventions.sh` (discover / mentions, covered
# by flow/tests/doc-conventions.test.sh) and a plan section
# `### Documentation Routing` that the planner fills from discovery and that is
# forwarded verbatim to the implementer, the Phase 6 + 7 reviewers and Phase 8.
#
# Prose-only pipeline change, so -- following flow/tests/doc-ownership-contract.test.sh
# -- the verification is a grep contract: a `failures` counter, literal
# matching, section-scoped assertions through a FENCE-AWARE extract_section
# (flow/tests/plan-persist-sections-contract.test.sh precedent, gotchas rule
# 29), every shared rule defined ONCE as a single-source-line marker and
# asserted at every restatement site (flow AGENTS.md #979, gotchas rule 13),
# occurrences counted with `grep -oF | wc -l` (rule 10(4)), and every
# procedure sentence paired with a check that the mechanism it describes is
# wired (rule 10(5)): the Phase 1 and Phase 8 bash fences really invoke
# `scripts/doc-conventions.sh`.
#
# Acceptance criteria covered: P4 (instructions route writes to discovered
# owners; no create/append/second-copy path when a convention is discovered),
# P5 (planner presents `recommend:` as a recommendation, never a Files to
# Modify step), P8 (Phase 8 mentions rule), P9 (plan-named docs written in every
# policy, `### Documentation Routing` forwarded at every consumer site), P10
# (three router rules), P20 (import-shim rule), P24 (Codex role mirrors),
# P28 (explicit requirements override minimal-mode restrictions).
#
# Non-vacuity self-tests (gotchas rules 10, 29): an unscoped fallback sentence
# injected into a mktemp copy of Phase 8 must be caught, the marker counter must
# report zero for an absent marker, and the fence-aware extractor must not
# over-capture on a fenced heading look-alike.
#
# No `read_*`-named helpers (flow/tests/read-helper-purity-contract.test.sh);
# no permission-removing chmod (flow/tests/root-safe-perms-contract.test.sh).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "doc-routing-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "doc-routing-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "doc-routing-contract.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# load_doc_raw <path-under-FLOW_DIR> -- pure extraction, no fail() side effect.
load_doc_raw() {
  cat "${FLOW_DIR}/$1" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> -- fails closed on a missing or
# unreadable file; a missing file must never masquerade as empty content.
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(load_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

# assert_contains <content> <required-substring> <label>
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

# assert_not_contains <content> <forbidden-substring> <label>
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text present: [${pattern}]"
}

# assert_nonempty <content> <label> -- a scoped extraction that came back empty
# means the heading moved or vanished; every assertion on it would be vacuous.
assert_nonempty() {
  [[ -n "$1" ]] || fail "$2: section extraction is empty (heading missing or renamed)"
}

# extract_section <content> <exact-heading-line> -- the body under a `## ` or
# `### ` heading up to the next heading of the same or a higher level.
# Fence-aware: a `## ` look-alike inside a ``` fence neither starts nor ends a
# section. Pure, safe inside $(...).
extract_section() {
  local content="$1" heading="$2"
  awk -v h="${heading}" '
    BEGIN { level = (h ~ /^### /) ? 3 : 2 }
    /^```/ { infence = !infence; if (on) print; next }
    !infence && !on && $0 == h { on = 1; next }
    on && !infence && match($0, /^#+ /) && (RLENGTH - 1) <= level { exit }
    on { print }
  ' <<<"${content}"
}

# extract_bash_fences <content> -- only the bodies of ```bash fences.
extract_bash_fences() {
  awk '
    /^```/ {
      if (infence) { infence = 0 } else if ($0 ~ /^```bash/) { infence = 1 }
      next
    }
    infence { print }
  ' <<<"$1"
}

# extract_indented_block <content> <exact-4-space-indented-heading> -- a plan
# template block inside planner.md's `## Plan Output` (indented, not fenced),
# up to the next indented `### ` / `## ` heading.
extract_indented_block() {
  local content="$1" heading="$2"
  awk -v h="${heading}" '
    $0 == h { on = 1; next }
    on && /^    #+ / { exit }
    on { print }
  ' <<<"${content}"
}

# extract_line <content> <literal-substring> -- first line containing it.
extract_line() {
  local content="$1" needle="$2"
  grep -F -m1 -e "${needle}" <<<"${content}"
}

# line_number_of <content> <exact-line> -- first line number of an exact line,
# or empty.
line_number_of() {
  local content="$1" line="$2"
  grep -nxF -m1 -e "${line}" <<<"${content}" | cut -d: -f1
}

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

# assert_marker_once <content> <marker> <label> -- the marker is present
# exactly once (a restated rule appears at the site once, never twice and
# never missing).
assert_marker_once() {
  local content="$1" marker="$2" label="$3" count
  [[ -n "${marker}" ]] || { fail "${label}: empty marker (test bug)"; return; }
  count="$(marker_count "${content}" "${marker}")"
  if [[ "${count}" == "ERROR" ]]; then
    fail "${label}: grep error while counting marker [${marker}]"
  elif [[ "${count}" -ne 1 ]]; then
    fail "${label}: expected exactly 1 occurrence of [${marker}], found ${count}"
  fi
}

# assert_marker_count <content> <marker> <expected> <label> -- the marker occurs
# exactly <expected> times (occurrences, not lines).
assert_marker_count() {
  local content="$1" marker="$2" expected="$3" label="$4" count
  [[ -n "${marker}" ]] || { fail "${label}: empty marker (test bug)"; return; }
  count="$(marker_count "${content}" "${marker}")"
  if [[ "${count}" == "ERROR" ]]; then
    fail "${label}: grep error while counting marker [${marker}]"
  elif [[ "${count}" -ne "${expected}" ]]; then
    fail "${label}: expected exactly ${expected} occurrence(s) of [${marker}], found ${count}"
  fi
}

# unscoped_lines <file> <needle> -- the `<n>:<line>` rows of <file> that contain
# <needle> but not `convention: none`; prints ERROR when grep itself failed.
# Pure, no fail().
unscoped_lines() {
  local file="$1" needle="$2" hits rc
  hits="$(grep -nF -e "${needle}" -- "${file}")"
  rc=$?
  if [[ ${rc} -gt 1 ]]; then
    echo "ERROR"
    return
  fi
  printf '%s\n' "${hits}" | grep -vF -e 'convention: none' | grep -v '^$'
  return 0
}

# --------------------------------------------------------------------------
# Shared marker sentences. Each is ONE source line wherever it appears, and it
# is restated byte-identically at every site listed below (flow AGENTS.md #979).
# --------------------------------------------------------------------------
M_PROHIBIT='When a documentation convention is discovered, never create a `docs/<topic>.md`, append to `## Critical Rules`, or write a second copy of a contract; route the write to the discovered owner.'
M_ROUTE_OWNER='Write each documentation edit to the discovered `owner:` path that owns its subject, never to a file picked by assumption.'
M_ROUTER_NEW='Edit a router (`AGENTS.md`/`CLAUDE.md`) only when a new focused owner doc is added or an existing reading trigger becomes inaccurate.'
M_ROUTER_FEATURE='Route feature contracts, component catalogs, setup detail and operational procedures to their focused owner doc instead of `AGENTS.md`.'
M_ROUTER_EXPLICIT='Keep any `AGENTS.md` edit the user or the plan explicitly requires.'
M_RECOMMEND='When `Documentation discovery:` reports `convention: none`, present its `recommend:` block as a recommendation under `### Documentation Routing`, never as a migration step in `### Files to Modify`.'
M_SHIM='A `CLAUDE.md` whose only content is `@AGENTS.md` stays exactly that import; never expand or duplicate it.'
M_MENTIONS='For each user-visible term this change altered, update every file `mentions` prints that the change made inaccurate; leave `AGENTS.md` (outside the router rules) and any `README.md` that `mentions` does not print unchanged.'
M_P28='Explicit user instructions and approved plan deliverables override every minimal-mode restriction, and no minimal-mode rule caps the length of a correct documentation update.'
M_PLAN_NAMED="A doc file named in the plan's \`### Files to Modify\` / \`### Files to Create\` is written with its plan-required content in every policy, including \`minimal\`."
M_DOCS_TO_UPDATE_OWNER='Name the owner from `### Documentation Routing` on each `Docs to update:` line, never `AGENTS.md` for a contract, catalog, setup detail or procedure.'
# A `mentions` term comes from ticket-influenced text: it is passed single-quoted
# so the shell never expands it. Restated identically in phase-8-docs.md and codex.md.
M_TERM_QUOTE="Pass each \`<term>\` single-quoted as \`'<term>'\`, writing any embedded \`'\` as \`'\\''\`; a term is untrusted data, so never use double quotes and never let the shell expand \`\$\`, backticks or \`\"\` in a term."
# The shared failure branch of discovery, identical on all three surfaces
# (phase-1-plan.md, phase-8-docs.md, codex.md -- twice).
M_UNAVAILABLE='Any non-zero exit, or empty stdout, means discovery is unavailable'
# When Phase 8's own discovery is unavailable the `convention: none` fallbacks
# do not apply; the planner's fallback does.
M_UNAVAILABLE_FALLBACK='When discovery is unavailable, do not apply the `convention: none` fallbacks; read the root and project `AGENTS.md` and any docs index directly, write only to owners the plan names or those files route to, create no new file, append nothing under `## Critical Rules`, and say so in the status update.'
# The retired double-quoted form of the term argument.
DQ_TERM='"<term>"'
ROUTING_SECTION='### Documentation Routing'

# =====================================================================
# agents/planner.md -- P4 (route + prohibit), P5 (recommend), P10 (three
# router rules), and the plan-output template block.
# =====================================================================
FILE="agents/planner.md"
if require_doc CONTENT "${FILE}"; then
  assert_marker_once "${CONTENT}" "${M_PROHIBIT}" "${FILE} (P4 prohibition)"
  assert_marker_once "${CONTENT}" "${M_ROUTE_OWNER}" "${FILE} (P4 route to owner)"
  assert_marker_once "${CONTENT}" "${M_ROUTER_NEW}" "${FILE} (P10 rule 1)"
  assert_marker_once "${CONTENT}" "${M_ROUTER_FEATURE}" "${FILE} (P10 rule 2)"
  assert_marker_once "${CONTENT}" "${M_ROUTER_EXPLICIT}" "${FILE} (P10 rule 3)"
  assert_marker_once "${CONTENT}" "${M_RECOMMEND}" "${FILE} (P5 recommend)"

  # Before Planning consumes the forwarded block, and the planner never runs
  # the plugin script itself.
  BEFORE_SECTION="$(extract_section "${CONTENT}" '## Before Planning')"
  assert_nonempty "${BEFORE_SECTION}" "${FILE} (## Before Planning)"
  assert_contains "${BEFORE_SECTION}" '`Documentation discovery:`' "${FILE} (## Before Planning)"
  assert_contains "${BEFORE_SECTION}" 'never runs `scripts/doc-conventions.sh`' "${FILE} (## Before Planning)"
  assert_contains "${BEFORE_SECTION}" 'built-in `Grep`' "${FILE} (## Before Planning)"

  # The template block exists with all seven fields, and its Recommendation
  # field is bound to `convention: none` and never a Files to Modify step.
  ROUTING_BLOCK="$(extract_indented_block "${CONTENT}" "    ${ROUTING_SECTION}")"
  assert_nonempty "${ROUTING_BLOCK}" "${FILE} (plan-output ${ROUTING_SECTION} block)"
  for FIELD in Discovery Routers Index Owners Lessons Validations Recommendation; do
    assert_contains "${ROUTING_BLOCK}" "    ${FIELD}: " "${FILE} (${ROUTING_SECTION} field ${FIELD})"
  done
  RECOMMENDATION_LINE="$(extract_line "${ROUTING_BLOCK}" '    Recommendation: ')"
  assert_contains "${RECOMMENDATION_LINE}" '`convention: none`' "${FILE} (Recommendation field)"
  assert_contains "${RECOMMENDATION_LINE}" '`### Files to Modify`' "${FILE} (Recommendation field)"
  # The template block sits inside the plan output, before ### Size Estimate.
  ROUTING_AT="$(line_number_of "${CONTENT}" "    ${ROUTING_SECTION}")"
  SIZE_AT="$(line_number_of "${CONTENT}" '    ### Size Estimate: <S|M|L>')"
  if [[ -z "${ROUTING_AT}" || -z "${SIZE_AT}" ]]; then
    fail "${FILE}: could not locate the ${ROUTING_SECTION} template block and ### Size Estimate (routing=${ROUTING_AT:-none} size=${SIZE_AT:-none})"
  elif [[ "${ROUTING_AT}" -ge "${SIZE_AT}" ]]; then
    fail "${FILE}: ${ROUTING_SECTION} template block must precede ### Size Estimate (routing=${ROUTING_AT} size=${SIZE_AT})"
  fi
fi

# =====================================================================
# agents/implementer.md -- rule 8 consumes the routing, plan-named docs are
# written in every policy (P9), Docs to update: lines name routing owners.
# The pinned carve-out sentence is asserted by doc-ownership-contract.test.sh.
# =====================================================================
FILE="agents/implementer.md"
if require_doc CONTENT "${FILE}"; then
  assert_marker_once "${CONTENT}" "${M_PLAN_NAMED}" "${FILE} (P9 plan-named doc)"
  assert_marker_once "${CONTENT}" "${M_DOCS_TO_UPDATE_OWNER}" "${FILE} (Docs to update owners)"
  assert_contains "${CONTENT}" "${ROUTING_SECTION}" "${FILE} (rule 8)"
fi

# =====================================================================
# agents/code-reviewer.md -- checklist item.
# =====================================================================
FILE="agents/code-reviewer.md"
if require_doc CONTENT "${FILE}"; then
  CHECKLIST="$(extract_section "${CONTENT}" '## Review Checklist')"
  assert_nonempty "${CHECKLIST}" "${FILE} (## Review Checklist)"
  assert_contains "${CHECKLIST}" "${ROUTING_SECTION}" "${FILE} (## Review Checklist)"
  ROUTING_ITEM="$(extract_line "${CHECKLIST}" "${ROUTING_SECTION}")"
  assert_contains "${ROUTING_ITEM}" 'second copy of a contract' "${FILE} (routing checklist item)"
  assert_contains "${ROUTING_ITEM}" 'routing change' "${FILE} (routing checklist item)"
fi

# =====================================================================
# phase-1-plan.md -- discovery runs once in Phase 1 (mechanism wired, not just
# described) and its stdout is forwarded to the planner.
# =====================================================================
FILE="skills/implement/phases/phase-1-plan.md"
if require_doc CONTENT "${FILE}"; then
  DISCOVER_HEADING='## Discover Documentation Conventions'
  DISCOVER_SECTION="$(extract_section "${CONTENT}" "${DISCOVER_HEADING}")"
  assert_nonempty "${DISCOVER_SECTION}" "${FILE} (${DISCOVER_HEADING})"

  DISCOVER_AT="$(line_number_of "${CONTENT}" "${DISCOVER_HEADING}")"
  DELEGATION_AT="$(line_number_of "${CONTENT}" '## Planner Delegation')"
  if [[ -z "${DISCOVER_AT}" || -z "${DELEGATION_AT}" ]]; then
    fail "${FILE}: could not locate both ${DISCOVER_HEADING} and ## Planner Delegation"
  elif [[ "${DISCOVER_AT}" -ge "${DELEGATION_AT}" ]]; then
    fail "${FILE}: ${DISCOVER_HEADING} must precede ## Planner Delegation (discover=${DISCOVER_AT} delegation=${DELEGATION_AT})"
  fi

  # Mechanism wired: a bash fence in the section really invokes the script
  # with the discover subcommand against the repo root, stderr captured apart.
  DISCOVER_FENCES="$(extract_bash_fences "${DISCOVER_SECTION}")"
  assert_nonempty "${DISCOVER_FENCES}" "${FILE} (${DISCOVER_HEADING} bash fence)"
  assert_contains "${DISCOVER_FENCES}" 'scripts/doc-conventions.sh" discover "<repo-root>"' "${FILE} (discover invocation)"
  assert_contains "${DISCOVER_FENCES}" ' 2>' "${FILE} (discover stderr captured separately)"
  assert_contains "${DISCOVER_SECTION}" 'unavailable (<reason>)' "${FILE} (non-blocking fallback)"
  assert_marker_once "${DISCOVER_SECTION}" "${M_UNAVAILABLE}" "${FILE} (${DISCOVER_HEADING}, unavailable branch)"
  assert_not_contains "${DISCOVER_SECTION}" '126/127' "${FILE} (${DISCOVER_HEADING}, one any-non-zero failure branch)"
  assert_contains "${DISCOVER_SECTION}" 'never a hard stop' "${FILE} (discovery is advisory)"

  DELEGATION_SECTION="$(extract_section "${CONTENT}" '## Planner Delegation')"
  assert_nonempty "${DELEGATION_SECTION}" "${FILE} (## Planner Delegation)"
  FORWARD_BULLET="$(extract_line "${DELEGATION_SECTION}" '`Documentation discovery:`')"
  assert_contains "${FORWARD_BULLET}" 'verbatim' "${FILE} (## Planner Delegation Documentation discovery bullet)"

  # ## Resume From Draft step 5: a stale/unknown re-delegation re-runs discovery.
  RESUME_SECTION="$(extract_section "${CONTENT}" '## Resume From Draft')"
  assert_nonempty "${RESUME_SECTION}" "${FILE} (## Resume From Draft)"
  STALE_BULLET="$(extract_line "${RESUME_SECTION}" '- **`stale`/`unknown`**')"
  assert_contains "${STALE_BULLET}" '`## Discover Documentation Conventions`' "${FILE} (## Resume From Draft stale/unknown re-delegation)"
fi

# =====================================================================
# phase-3 / phase-4 Delegation Context, phase-4 ## Rules -- P9 forwarding.
# =====================================================================
FILE="skills/implement/phases/phase-3-test-red.md"
if require_doc CONTENT "${FILE}"; then
  DELEGATION_CONTEXT="$(extract_section "${CONTENT}" '## Delegation Context')"
  assert_nonempty "${DELEGATION_CONTEXT}" "${FILE} (## Delegation Context)"
  ROUTING_BULLET="$(extract_line "${DELEGATION_CONTEXT}" "${ROUTING_SECTION}")"
  assert_contains "${ROUTING_BULLET}" 'verbatim' "${FILE} (## Delegation Context routing bullet)"
fi

FILE="skills/implement/phases/phase-4-implement-green.md"
if require_doc CONTENT "${FILE}"; then
  DELEGATION_CONTEXT="$(extract_section "${CONTENT}" '## Delegation Context')"
  assert_nonempty "${DELEGATION_CONTEXT}" "${FILE} (## Delegation Context)"
  ROUTING_BULLET="$(extract_line "${DELEGATION_CONTEXT}" "${ROUTING_SECTION}")"
  assert_contains "${ROUTING_BULLET}" 'verbatim' "${FILE} (## Delegation Context routing bullet)"

  RULES="$(extract_section "${CONTENT}" '## Rules')"
  assert_nonempty "${RULES}" "${FILE} (## Rules)"
  assert_marker_once "${RULES}" "${M_PLAN_NAMED}" "${FILE} (## Rules, P9 plan-named doc)"
  assert_marker_once "${RULES}" "${M_DOCS_TO_UPDATE_OWNER}" "${FILE} (## Rules, Docs to update owners)"
  # The pre-existing carve-out sentence stays (doc-ownership-contract.test.sh).
  assert_contains "${RULES}" "except a doc file explicitly named in the plan's Files to Modify/Create" "${FILE} (## Rules carve-out kept)"
fi

# =====================================================================
# phase-6-7-review.md -- every reviewer brief carries the routing verbatim.
# =====================================================================
FILE="skills/implement/phases/phase-6-7-review.md"
if require_doc CONTENT "${FILE}"; then
  EXECUTION="$(extract_section "${CONTENT}" '## Execution')"
  assert_nonempty "${EXECUTION}" "${FILE} (## Execution)"
  ROUTING_LINE="$(extract_line "${EXECUTION}" "${ROUTING_SECTION}")"
  assert_contains "${ROUTING_LINE}" 'verbatim' "${FILE} (## Execution routing sentence)"
  assert_contains "${ROUTING_LINE}" 'every reviewer brief' "${FILE} (## Execution routing sentence)"
  assert_contains "${ROUTING_LINE}" '`lite-small`' "${FILE} (## Execution routing sentence)"
fi

# =====================================================================
# phase-8-docs.md -- the routing step, mentions rule (P8), router rules (P10),
# prohibition (P4), import shim (P20), minimal-mode overrides (P9, P28).
# =====================================================================
FILE="skills/implement/phases/phase-8-docs.md"
if require_doc PHASE8 "${FILE}"; then
  # Each shared marker appears exactly once in the whole file, in its own
  # section, so a duplicate restatement or a misplaced one is caught.
  for MARKER in "${M_PROHIBIT}" "${M_ROUTE_OWNER}" "${M_ROUTER_NEW}" "${M_ROUTER_FEATURE}" "${M_ROUTER_EXPLICIT}" "${M_SHIM}" "${M_MENTIONS}" "${M_P28}" "${M_PLAN_NAMED}"; do
    assert_marker_once "${PHASE8}" "${MARKER}" "${FILE} (whole file)"
  done
  ROUTE_HEADING='## Route Documentation Writes'
  ROUTE_SECTION="$(extract_section "${PHASE8}" "${ROUTE_HEADING}")"
  assert_nonempty "${ROUTE_SECTION}" "${FILE} (${ROUTE_HEADING})"
  POLICY_AT="$(line_number_of "${PHASE8}" '## Resolve Documentation Policy')"
  ROUTE_AT="$(line_number_of "${PHASE8}" "${ROUTE_HEADING}")"
  LESSONS_AT="$(line_number_of "${PHASE8}" '## Capture Lessons')"
  if [[ -z "${POLICY_AT}" || -z "${ROUTE_AT}" || -z "${LESSONS_AT}" ]]; then
    fail "${FILE}: could not locate ## Resolve Documentation Policy, ${ROUTE_HEADING} and ## Capture Lessons"
  elif ! { [[ "${POLICY_AT}" -lt "${ROUTE_AT}" ]] && [[ "${ROUTE_AT}" -lt "${LESSONS_AT}" ]]; }; then
    fail "${FILE}: ${ROUTE_HEADING} must sit between ## Resolve Documentation Policy and ## Capture Lessons (policy=${POLICY_AT} route=${ROUTE_AT} lessons=${LESSONS_AT})"
  fi
  assert_contains "${ROUTE_SECTION}" "${ROUTING_SECTION}" "${FILE} (${ROUTE_HEADING})"
  assert_contains "${ROUTE_SECTION}" "${M_ROUTE_OWNER}" "${FILE} (${ROUTE_HEADING}, P4 route to owner)"
  assert_contains "${ROUTE_SECTION}" "${M_MENTIONS}" "${FILE} (${ROUTE_HEADING}, P8 mentions rule)"
  # Legacy plans (no routing section, or Trivial Fast Path) fall back to discover.
  assert_contains "${ROUTE_SECTION}" 'no `### Documentation Routing` section' "${FILE} (${ROUTE_HEADING} legacy fallback)"
  # Mechanism wired: the fences really invoke discover and mentions against
  # the verified worktree.
  ROUTE_FENCES="$(extract_bash_fences "${ROUTE_SECTION}")"
  assert_nonempty "${ROUTE_FENCES}" "${FILE} (${ROUTE_HEADING} bash fences)"
  assert_contains "${ROUTE_FENCES}" 'scripts/doc-conventions.sh" discover "<worktree-path>"' "${FILE} (discover invocation)"
  assert_contains "${ROUTE_FENCES}" "scripts/doc-conventions.sh\" mentions \"<worktree-path>\" '<term>'" "${FILE} (mentions invocation, single-quoted term)"
  assert_not_contains "${PHASE8}" "${DQ_TERM}" "${FILE} (no double-quoted term anywhere)"
  assert_marker_once "${PHASE8}" "${M_TERM_QUOTE}" "${FILE} (whole file, term quoting rule)"
  assert_contains "${ROUTE_SECTION}" "${M_TERM_QUOTE}" "${FILE} (${ROUTE_HEADING}, term quoting rule)"
  assert_contains "${ROUTE_SECTION}" "a term starting with \`-\` is safe" "${FILE} (${ROUTE_HEADING}, leading-dash term stays safe)"
  # Discovery failure branch: any non-zero exit, or empty stdout; when it fires
  # the `convention: none` fallbacks do not apply (the planner's fallback does).
  assert_marker_once "${PHASE8}" "${M_UNAVAILABLE}" "${FILE} (whole file, unavailable branch)"
  assert_marker_once "${PHASE8}" "${M_UNAVAILABLE_FALLBACK}" "${FILE} (whole file, unavailable fallback)"
  assert_contains "${ROUTE_SECTION}" "${M_UNAVAILABLE}" "${FILE} (${ROUTE_HEADING}, unavailable branch)"
  assert_contains "${ROUTE_SECTION}" "${M_UNAVAILABLE_FALLBACK}" "${FILE} (${ROUTE_HEADING}, unavailable fallback)"
  assert_not_contains "${ROUTE_SECTION}" '126/127' "${FILE} (${ROUTE_HEADING}, one any-non-zero failure branch)"
  assert_not_contains "${ROUTE_SECTION}" 'as for `convention: none`' "${FILE} (${ROUTE_HEADING}, unavailable discovery is not convention: none)"
  assert_contains "${ROUTE_SECTION}" 'docs-to-update.txt' "${FILE} (${ROUTE_HEADING} mentions finding recorded as correctness work)"

  # ## Update CLAUDE.md: router rules (P10 x3), prohibition (P4), shim (P20),
  # and the fallback append scoped to `convention: none`.
  CLAUDE_SECTION="$(extract_section "${PHASE8}" '## Update CLAUDE.md')"
  assert_nonempty "${CLAUDE_SECTION}" "${FILE} (## Update CLAUDE.md)"
  assert_contains "${CLAUDE_SECTION}" "${M_ROUTER_NEW}" "${FILE} (## Update CLAUDE.md, P10 rule 1)"
  assert_contains "${CLAUDE_SECTION}" "${M_ROUTER_FEATURE}" "${FILE} (## Update CLAUDE.md, P10 rule 2)"
  assert_contains "${CLAUDE_SECTION}" "${M_ROUTER_EXPLICIT}" "${FILE} (## Update CLAUDE.md, P10 rule 3)"
  assert_contains "${CLAUDE_SECTION}" "${M_PROHIBIT}" "${FILE} (## Update CLAUDE.md, P4 prohibition)"
  assert_contains "${CLAUDE_SECTION}" "${M_SHIM}" "${FILE} (## Update CLAUDE.md, P20 import shim)"
  APPEND_LINE="$(extract_line "${CLAUDE_SECTION}" 'Append under `## Critical Rules`')"
  assert_contains "${APPEND_LINE}" '`convention: none`' "${FILE} (## Update CLAUDE.md fallback append is scoped)"

  # ## Capture Lessons: routing forwarded to the collector, "new topic doc"
  # scoped to `convention: none`.
  LESSONS_SECTION="$(extract_section "${PHASE8}" '## Capture Lessons')"
  assert_nonempty "${LESSONS_SECTION}" "${FILE} (## Capture Lessons)"
  assert_contains "${LESSONS_SECTION}" "${ROUTING_SECTION}" "${FILE} (## Capture Lessons forwards the routing)"
  TOPIC_DOC_LINE="$(extract_line "${LESSONS_SECTION}" 'new topic doc')"
  assert_contains "${TOPIC_DOC_LINE}" '`convention: none`' "${FILE} (## Capture Lessons new-topic-doc sentence is scoped)"

  # ## Update Topic Docs: nested docs/**/*.md owners accepted, pinned phrase kept.
  TOPIC_SECTION="$(extract_section "${PHASE8}" '## Update Topic Docs')"
  assert_nonempty "${TOPIC_SECTION}" "${FILE} (## Update Topic Docs)"
  assert_contains "${TOPIC_SECTION}" 'nested `docs/**/*.md`' "${FILE} (## Update Topic Docs)"
  assert_contains "${TOPIC_SECTION}" 'matches `docs/*.md` or `<project-path>/docs/*.md`' "${FILE} (## Update Topic Docs pinned phrase)"
  assert_contains "${TOPIC_SECTION}" 'contains no `..` path segment' "${FILE} (## Update Topic Docs shape guard kept)"

  # ### Minimal mode: P28 override and the plan-named rule (P9).
  MINIMAL_SECTION="$(extract_section "${PHASE8}" '### Minimal mode')"
  assert_nonempty "${MINIMAL_SECTION}" "${FILE} (### Minimal mode)"
  assert_contains "${MINIMAL_SECTION}" "${M_P28}" "${FILE} (### Minimal mode, P28)"
  assert_contains "${MINIMAL_SECTION}" "${M_PLAN_NAMED}" "${FILE} (### Minimal mode, P9)"
  # No minimal-mode rule caps the length of a correct documentation update.
  grep -qiE '(at most|no more than|up to|maximum of|capped at) [0-9]+ (lines|words|sentences|characters)' <<<"${MINIMAL_SECTION}"
  CAP_RC=$?
  case "${CAP_RC}" in
    0) fail "${FILE} (### Minimal mode): a length cap on a documentation update contradicts P28" ;;
    1) ;;
    *) fail "${FILE} (### Minimal mode): grep error (rc ${CAP_RC}) while scanning for a length cap" ;;
  esac

  # Repository docs checks point at the routing's Validations: line.
  MAINTENANCE_SECTION="$(extract_section "${PHASE8}" '## Maintenance Check')"
  assert_nonempty "${MAINTENANCE_SECTION}" "${FILE} (## Maintenance Check)"
  # Several earlier lines say "continue with **Repository docs checks** below";
  # the paragraph itself opens with a colon.
  CHECKS_LINE="$(extract_line "${MAINTENANCE_SECTION}" '**Repository docs checks**: ')"
  assert_contains "${CHECKS_LINE}" '`Validations:`' "${FILE} (Repository docs checks parenthetical)"
  assert_not_contains "${CHECKS_LINE}" 'ticket #1302' "${FILE} (Repository docs checks parenthetical)"
fi

# =====================================================================
# codex.md -- the Codex surface mirrors every rule (docs/skill-authoring.md:
# three client surfaces). The same marker sentences, byte-identical.
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CODEX "${FILE}"; then
  for MARKER in "${M_PROHIBIT}" "${M_ROUTE_OWNER}" "${M_ROUTER_NEW}" "${M_ROUTER_FEATURE}" "${M_ROUTER_EXPLICIT}" "${M_RECOMMEND}" "${M_SHIM}" "${M_MENTIONS}" "${M_P28}" "${M_PLAN_NAMED}" "${M_DOCS_TO_UPDATE_OWNER}"; do
    assert_marker_once "${CODEX}" "${MARKER}" "${FILE}"
  done
  assert_contains "${CODEX}" "${ROUTING_SECTION}" "${FILE}"
  assert_contains "${CODEX}" 'doc-conventions.sh" discover' "${FILE} (discovery in /plan)"
  assert_contains "${CODEX}" 'doc-conventions.sh" mentions' "${FILE} (mentions check in the docs step)"
  # Term quoting and the discovery failure branch, identical to Phase 8 / Phase 1.
  assert_marker_once "${CODEX}" "${M_TERM_QUOTE}" "${FILE} (term quoting rule)"
  assert_contains "${CODEX}" "doc-conventions.sh\" mentions \"<abs-worktree-path>\" '<term>'" "${FILE} (mentions invocation, single-quoted term)"
  assert_not_contains "${CODEX}" "${DQ_TERM}" "${FILE} (no double-quoted term anywhere)"
  assert_marker_once "${CODEX}" "${M_UNAVAILABLE_FALLBACK}" "${FILE} (unavailable fallback)"
  assert_marker_count "${CODEX}" "${M_UNAVAILABLE}" 2 "${FILE} (unavailable branch at the /plan and docs discovery sites)"
  if DISCOVER_LINES="$(grep -F -e 'doc-conventions.sh" discover' <<<"${CODEX}")"; then
    assert_marker_count "${DISCOVER_LINES}" "${M_UNAVAILABLE}" 2 "${FILE} (each discovery site names the any-non-zero branch)"
    assert_not_contains "${DISCOVER_LINES}" '126/127' "${FILE} (discovery sites use one any-non-zero failure branch)"
    assert_not_contains "${DISCOVER_LINES}" 'as for `convention: none`' "${FILE} (unavailable discovery is not convention: none)"
  else
    fail "${FILE}: no line invokes doc-conventions.sh discover"
  fi
  FORWARD_LINE="$(extract_line "${CODEX}" "forward the plan's \`${ROUTING_SECTION}\`")"
  assert_contains "${FORWARD_LINE}" 'critical-reviewer' "${FILE} (routing forwarded to the reviewers)"
  assert_contains "${FORWARD_LINE}" 'implementer' "${FILE} (routing forwarded to the implementer)"
fi

# =====================================================================
# Codex roles (P24): the template and the installed copy name the routing and
# stay byte-identical. validate-agent-roles.sh covers the schema separately.
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
  if ! TOML_CONTENT="$(cat "${TEMPLATE_TOML}")"; then
    fail "${ROLE}.toml: unreadable"
    continue
  fi
  assert_contains "${TOML_CONTENT}" "${ROUTING_SECTION}" "${ROLE}.toml (developer_instructions)"
  case "${ROLE}" in
    planner)
      assert_contains "${TOML_CONTENT}" '`Documentation discovery:`' "${ROLE}.toml"
      assert_contains "${TOML_CONTENT}" '`convention: none`' "${ROLE}.toml"
      ;;
    implementer)
      assert_contains "${TOML_CONTENT}" 'plan-required content in every policy' "${ROLE}.toml"
      ;;
    critical-reviewer)
      assert_contains "${TOML_CONTENT}" 'second copy of a contract' "${ROLE}.toml"
      ;;
  esac
done

# =====================================================================
# P4: the fallback paths exist only under `convention: none`. Scan every
# routing instruction file: any line naming "Append under `## Critical Rules`"
# or a "new topic doc" must also name `convention: none`.
# =====================================================================
SCOPED_FILES=(
  "agents/planner.md"
  "agents/implementer.md"
  "agents/code-reviewer.md"
  "skills/implement/phases/phase-1-plan.md"
  "skills/implement/phases/phase-3-test-red.md"
  "skills/implement/phases/phase-4-implement-green.md"
  "skills/implement/phases/phase-6-7-review.md"
  "skills/implement/phases/phase-8-docs.md"
  "skills/implement/codex.md"
)
FALLBACK_NEEDLES=('Append under `## Critical Rules`' 'new topic doc')
for REL in "${SCOPED_FILES[@]}"; do
  if [[ ! -f "${FLOW_DIR}/${REL}" ]]; then
    fail "${REL}: file missing for the fallback-scoping scan"
    continue
  fi
  for NEEDLE in "${FALLBACK_NEEDLES[@]}"; do
    OFFENDERS="$(unscoped_lines "${FLOW_DIR}/${REL}" "${NEEDLE}")"
    if [[ "${OFFENDERS}" == "ERROR" ]]; then
      fail "${REL}: grep error while scanning for [${NEEDLE}]"
    elif [[ -n "${OFFENDERS}" ]]; then
      fail "${REL}: [${NEEDLE}] appears on a line that does not name \`convention: none\` (P4): ${OFFENDERS}"
    fi
  done
done

# Positive control: Phase 8 really keeps both fallbacks (scoped), so the scan
# above is not passing because the fallbacks were deleted or reworded away.
for NEEDLE in "${FALLBACK_NEEDLES[@]}"; do
  FALLBACK_COUNT="$(marker_count "${PHASE8}" "${NEEDLE}")"
  if [[ ! "${FALLBACK_COUNT}" =~ ^[0-9]+$ ]] || [[ "${FALLBACK_COUNT}" -eq 0 ]]; then
    fail "phase-8-docs.md: expected the scoped fallback [${NEEDLE}] to remain under \`convention: none\` (count: ${FALLBACK_COUNT})"
  fi
done

# =====================================================================
# Non-vacuity self-tests.
# =====================================================================
SELFTEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/doc-routing-contract.XXXXXX")" || { echo "doc-routing-contract.test.sh: mktemp failed." >&2; exit 2; }
[[ -n "${SELFTEST_DIR}" && -d "${SELFTEST_DIR}" ]] || { echo "doc-routing-contract.test.sh: mktemp produced no directory." >&2; exit 2; }
trap 'rm -rf -- "${SELFTEST_DIR}"' EXIT

# (1) Inject unscoped copies of both fallback sentences into a mktemp copy of
# Phase 8 and prove the scoping scan reports them; the unmodified copy must
# report nothing.
INJECTED="${SELFTEST_DIR}/phase-8-injected.md"
CLEAN="${SELFTEST_DIR}/phase-8-clean.md"
if printf '%s\n' "${PHASE8}" > "${CLEAN}"; then
  if ! { printf '%s\n' "${PHASE8}"; printf '%s\n' 'Append under `## Critical Rules`; do not rewrite existing content.'; printf '%s\n' 'The collector may create a new topic doc when findings cluster.'; } > "${INJECTED}"; then
    fail "self-test: could not write the injected Phase 8 copy"
  fi
  for NEEDLE in "${FALLBACK_NEEDLES[@]}"; do
    CLEAN_HITS="$(unscoped_lines "${CLEAN}" "${NEEDLE}")"
    if [[ -n "${CLEAN_HITS}" ]]; then
      fail "self-test: the unmodified Phase 8 copy reports unscoped [${NEEDLE}]: ${CLEAN_HITS}"
    fi
    INJECTED_HITS="$(unscoped_lines "${INJECTED}" "${NEEDLE}")"
    if [[ -z "${INJECTED_HITS}" || "${INJECTED_HITS}" == "ERROR" ]]; then
      fail "self-test: the scoping scan failed to catch an injected unscoped [${NEEDLE}] (vacuous)"
    fi
  done
else
  fail "self-test: could not write the clean Phase 8 copy"
fi

# (2) The marker counter reports zero for an absent marker and one for a
# present one, so assert_marker_once cannot pass on a missing sentence.
if [[ "$(marker_count 'nothing relevant here' "${M_SHIM}")" != "0" ]]; then
  fail "self-test: marker_count must report 0 for an absent marker"
fi
if [[ "$(marker_count "prefix ${M_SHIM} suffix" "${M_SHIM}")" != "1" ]]; then
  fail "self-test: marker_count must report 1 for a present marker"
fi
if [[ "$(marker_count "${M_SHIM} ${M_SHIM}" "${M_SHIM}")" != "2" ]]; then
  fail "self-test: marker_count must count two occurrences on one line as 2 (grep -oF, not -cF)"
fi

# (3) The fence-aware extractor does not end a section on, or start one from,
# a heading look-alike inside a fence.
FENCE_DOC=$'## Alpha\nbefore\n```\n## Not A Heading\n```\nafter\n## Beta\nbeta body\n'
ALPHA_BODY="$(extract_section "${FENCE_DOC}" '## Alpha')"
assert_contains "${ALPHA_BODY}" 'after' "self-test: extract_section survives a fenced heading look-alike"
assert_not_contains "${ALPHA_BODY}" 'beta body' "self-test: extract_section stops at the next real heading"
FENCED_START="$(extract_section $'```\n## Alpha\ninside\n```\n## Alpha\nreal\n' '## Alpha')"
assert_not_contains "${FENCED_START}" 'inside' "self-test: extract_section ignores a fenced heading as a start"
assert_contains "${FENCED_START}" 'real' "self-test: extract_section starts at the real heading"

# (4) The term-quoting and failure-branch checks catch their regressions: a
# double-quoted term, a marker restated with a different quote form, and the
# retired 126/127 branch, each injected into a copy of the real text.
assert_not_contains "${PHASE8}" "${DQ_TERM}" "self-test baseline: Phase 8 has no double-quoted term"
INJECTED_DQ="${PHASE8}"$'\n''bash doc-conventions.sh" mentions "<worktree-path>" "<term>"'
if [[ "${INJECTED_DQ}" != *"${DQ_TERM}"* ]]; then
  fail "self-test: the double-quoted term detector missed an injected \"<term>\" (vacuous)"
fi
if [[ "$(marker_count "${INJECTED_DQ}" "${M_TERM_QUOTE}")" != "1" ]]; then
  fail "self-test: the term quoting marker must still count once in an unrelated injection"
fi
if [[ "$(marker_count 'Pass each term in double quotes.' "${M_TERM_QUOTE}")" != "0" ]]; then
  fail "self-test: a rewritten term quoting rule must no longer match the marker (vacuous)"
fi
LEGACY_BRANCH="${PHASE8}"$'\n''Exit 2, exit 126/127 or empty stdout is unavailable.'
if [[ "${LEGACY_BRANCH}" != *'126/127'* ]]; then
  fail "self-test: the retired 126/127 branch detector missed an injection (vacuous)"
fi
if [[ "$(marker_count 'x' "${M_UNAVAILABLE}")" != "0" ]]; then
  fail "self-test: marker_count must report 0 for an absent unavailable-branch marker"
fi

echo "doc-routing-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
