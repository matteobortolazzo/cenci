#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #838 item 2: the context
# bundle temp file is scoped per session, not keyed only by ticket ID or slug.
#
# Before #838 the bundle path was `${TMPDIR:-/tmp}/cenci/cenci-context-<ticket-id>.md`
# (ticketless: `cenci-context-<slug>.md`). Two repos implementing the same issue
# number, or two sessions on the same ticket, wrote the same path, and one run's
# Phase 9 cleanup could delete the other run's live bundle -- the collision
# flow/AGENTS.md's shared-temp-file rule and flow/docs/pipeline-safety.md forbid.
# The path now carries the `<session-uuid>` suffix every other implement staging
# file already uses (for example `cenci-decision-<id>-<session-uuid>.md`).
#
# Scans exactly the four sites named by the ticket's acceptance criteria:
#   - flow/skills/implement/SKILL.md (## Context Gathering (Delegated))
#   - flow/skills/implement/phases/phase-1-plan.md (## Trivial Fast Path step 3,
#     ## Lean Approval Path step 2, ## Persist the Plan step 2)
#   - flow/skills/implement/phases/phase-9-pr.md (## Cleanup)
#   - flow/agents/context-gatherer.md (## Inputs bundle-path example)
#
# Absence is paired with presence (the case-3 idiom of
# implement-review-artifacts.test.sh): an absence check alone passes against an
# emptied or renamed file, so every site must also carry its scoped marker,
# scoped to the one section (or step) that owns it rather than file-wide.
#
# Marker discipline (flow/docs/shell-scripting-gotchas.md): every marker sits on
# one source line, matching is `grep -F` / bash literal substring (never a
# regex), assertions are section-scoped, hard-wrapped prose is compared
# whitespace-normalized, and the absence scanner carries a non-vacuity
# self-test. No `read_*`-named helper, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh. Auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob -- no registration needed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "context-bundle-scoping-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "context-bundle-scoping-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
SKILL_FILE="${FLOW_DIR}/skills/implement/SKILL.md"
PHASE1_FILE="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
PHASE9_FILE="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
GATHERER_FILE="${FLOW_DIR}/agents/context-gatherer.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# --- helpers ------------------------------------------------------------------

# load_content <result-var> <file> <label> -- nameref wrapper; fails closed on an
# unreadable or empty file. Must NOT be invoked via $(...) (it calls fail()).
load_content() {
  local -n _res="$1"
  local _c
  if [[ ! -f "$2" ]]; then
    fail "$3: file not found: $2"
    _res=""
    return 1
  fi
  if ! _c="$(cat "$2")"; then
    fail "$3: could not read file: $2"
    _res=""
    return 1
  fi
  if [[ -z "${_c}" ]]; then
    fail "$3: file is empty: $2"
    _res=""
    return 1
  fi
  _res="${_c}"
}

# extract_section <content> <exact-heading-line> -- pure, fence-aware: the body of
# the named "## <heading>" section through the next real (unfenced) "## " heading,
# or EOF. A literal "## " inside a fenced code block never ends the section.
extract_section() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_block <content> <start-needle> -- pure: from the first line containing
# the needle through the line before the next blank line, numbered-step line
# ("N. ") or bullet line ("- "). Keeps one wrapped step or bullet together and
# scopes a marker to it.
extract_block() {
  awk -v want="$2" '
    !on && index($0, want) { on=1; print; next }
    on && (/^[[:space:]]*$/ || /^[0-9]+\. / || /^- /) { exit }
    on { print }
  ' <<<"$1"
}

# require_section / require_block <result-var> <content> <anchor> <label> --
# nameref wrappers; fail closed (never an empty-but-present masquerade).
require_section() {
  local -n _res="$1"
  local _body
  _body="$(extract_section "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section"
    _res=""
    return 1
  fi
  _res="${_body}"
}
require_block() {
  local -n _res="$1"
  local _body
  _body="$(extract_block "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate block starting at '$3'"
    _res=""
    return 1
  fi
  _res="${_body}"
}

# normalize_ws <content> -- collapse newlines and repeated whitespace so a
# markdown-wrapped sentence matches as one substring.
normalize_ws() {
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws <content> <needle> <label> -- literal, whitespace-normalized.
assert_contains_ws() {
  local content="$1" needle="$2" label="$3"
  [[ -n "${needle}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${needle}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${needle}]"
}

UNSCOPED_LITERALS=(
  'cenci-context-<id>.md'
  'cenci-context-<ticket-id>.md'
  'cenci-context-<slug>.md'
  'cenci-context-<id|slug>.md'
  'cenci-context-<ticket-id-or-slug>.md'
)

# unscoped_hit <file> -- pure: prints the first unscoped bundle literal the file
# contains and returns 0; returns 1 when none is present; returns 2 when the file
# cannot be scanned (missing or a grep error), so a scan failure is never read as
# "absent". `grep -F --` keeps `<`, `|` and `-` literal.
unscoped_hit() {
  local lit rc
  [[ -f "$1" ]] || return 2
  for lit in "${UNSCOPED_LITERALS[@]}"; do
    grep -qF -- "${lit}" "$1"
    rc=$?
    if [[ "${rc}" -eq 0 ]]; then
      printf '%s' "${lit}"
      return 0
    fi
    [[ "${rc}" -eq 1 ]] || return 2
  done
  return 1
}

# =====================================================================
# Non-vacuity self-test for the absence scanner: it must flag each of the five
# unscoped literals and must NOT flag the session-scoped forms (a scoped path
# never contains an unscoped literal as a substring). Without this, a broken
# scanner would report every file clean.
# =====================================================================

SELFTEST_DIR="$(mktemp -d)" || { echo "context-bundle-scoping-contract.test.sh: failed to create self-test directory." >&2; exit 2; }
trap 'rm -rf "${SELFTEST_DIR}"' EXIT

for lit in "${UNSCOPED_LITERALS[@]}"; do
  printf 'path: ${TMPDIR:-/tmp}/cenci/%s\n' "${lit}" > "${SELFTEST_DIR}/injected.md" \
    || { fail "self-test: could not write injected fixture for ${lit}"; continue; }
  hit="$(unscoped_hit "${SELFTEST_DIR}/injected.md")"
  rc=$?
  [[ "${rc}" -eq 0 && "${hit}" == "${lit}" ]] \
    || fail "self-test: the absence scanner must flag the injected unscoped literal [${lit}] (rc=${rc}, hit=[${hit}])"
done

printf '%s\n' \
  '${TMPDIR:-/tmp}/cenci/cenci-context-<ticket-id>-<session-uuid>.md' \
  '(ticketless: cenci-context-<slug>-<session-uuid>.md)' \
  '${TMPDIR:-/tmp}/cenci/cenci-context-<ticket-id-or-slug>-<session-uuid>.md' > "${SELFTEST_DIR}/scoped.md" \
  || fail "self-test: could not write scoped fixture"
unscoped_hit "${SELFTEST_DIR}/scoped.md" >/dev/null
scoped_rc=$?
[[ "${scoped_rc}" -eq 1 ]] \
  || fail "self-test: the absence scanner must not flag the session-scoped forms (rc=${scoped_rc})"

unscoped_hit "${SELFTEST_DIR}/does-not-exist.md" >/dev/null
missing_rc=$?
[[ "${missing_rc}" -eq 2 ]] \
  || fail "self-test: a missing file must be a scan error (rc 2), never read as clean (rc=${missing_rc})"

# Fence-awareness self-test for extract_section: a literal "## " line inside a
# fenced block must not end the section early.
FENCE_FIXTURE=$'## Target\nbefore\n```\n## Not A Heading\n```\nafter\n## Next\noutside'
fence_body="$(extract_section "${FENCE_FIXTURE}" '## Target')"
[[ "${fence_body}" == *"after"* && "${fence_body}" != *"outside"* ]] \
  || fail "self-test: extract_section must stay fence-aware (a fenced '## ' line must not end the section)"

# =====================================================================
# Absence: none of the four files may contain an unscoped bundle literal. Each
# file's own presence check below keeps this from passing against an emptied or
# renamed file.
# =====================================================================

for site in "${SKILL_FILE}" "${PHASE1_FILE}" "${PHASE9_FILE}" "${GATHERER_FILE}"; do
  hit="$(unscoped_hit "${site}")"
  rc=$?
  case "${rc}" in
    0) fail "$(basename "${site}") still contains the unscoped context-bundle literal [${hit}] (#838: the path must carry <session-uuid>)" ;;
    1) ;;
    *) fail "$(basename "${site}"): could not scan for unscoped context-bundle literals (file missing or grep error): ${site}" ;;
  esac
done

# =====================================================================
# Presence, scoped per site. Ticket-only sites (Trivial Fast Path and Lean
# Approval Path are ticket mode only) name `<ticket-id>`; sites serving both modes
# name both literal forms; Phase 9's rm -f entry mirrors the sibling
# `followup-tracked-<session-uuid>` entry's `<ticket-id-or-slug>` placeholder.
# =====================================================================

TICKET_PATH='${TMPDIR:-/tmp}/cenci/cenci-context-<ticket-id>-<session-uuid>.md'
SLUG_PATH='${TMPDIR:-/tmp}/cenci/cenci-context-<slug>-<session-uuid>.md'
TICKETLESS_PAREN='(ticketless: cenci-context-<slug>-<session-uuid>.md)'
TICKET_APPEND='cat ${TMPDIR:-/tmp}/cenci/cenci-context-<ticket-id>-<session-uuid>.md >> "<repo-root>/.plans/<filename>"'
RM_ENTRY='${TMPDIR:-/tmp}/cenci/cenci-context-<ticket-id-or-slug>-<session-uuid>.md'

# --- SKILL.md: ## Context Gathering (Delegated) --------------------------------

if load_content SKILL_CONTENT "${SKILL_FILE}" "implement/SKILL.md"; then
  if require_section GATHER_SECTION "${SKILL_CONTENT}" '## Context Gathering (Delegated)' "implement/SKILL.md"; then
    if require_block SKILL_TICKET_BULLET "${GATHER_SECTION}" 'The bundle output path:' "implement/SKILL.md (ticket-mode bundle-path bullet)"; then
      assert_contains_ws "${SKILL_TICKET_BULLET}" "${TICKET_PATH}" \
        "implement/SKILL.md ## Context Gathering (Delegated) ticket-mode bundle path must be session-scoped"
    fi
    if require_block SKILL_TICKETLESS "${GATHER_SECTION}" '**If ticketless mode:**' "implement/SKILL.md (ticketless bundle-path paragraph)"; then
      assert_contains_ws "${SKILL_TICKETLESS}" "bundle path \`${SLUG_PATH}\`" \
        "implement/SKILL.md ## Context Gathering (Delegated) ticketless bundle path must be session-scoped"
    fi
  fi
fi

# --- phase-1-plan.md: Trivial Fast Path step 3, Lean Approval Path step 2,
#     Persist the Plan step 2 ----------------------------------------------------

if load_content PHASE1_CONTENT "${PHASE1_FILE}" "phase-1-plan.md"; then
  if require_section TRIVIAL_SECTION "${PHASE1_CONTENT}" '## Trivial Fast Path' "phase-1-plan.md"; then
    assert_contains_ws "${TRIVIAL_SECTION}" "${TICKET_APPEND}" \
      "phase-1-plan.md ## Trivial Fast Path step 3 must append the session-scoped, ticket-only bundle path"
  fi
  if require_section LEAN_SECTION "${PHASE1_CONTENT}" '## Lean Approval Path' "phase-1-plan.md"; then
    assert_contains_ws "${LEAN_SECTION}" "${TICKET_APPEND}" \
      "phase-1-plan.md ## Lean Approval Path step 2 must append the session-scoped, ticket-only bundle path"
  fi
  if require_section PERSIST_SECTION "${PHASE1_CONTENT}" '## Persist the Plan' "phase-1-plan.md" \
     && require_block PERSIST_STEP2 "${PERSIST_SECTION}" 'Append the context bundle verbatim via shell' "phase-1-plan.md (## Persist the Plan step 2)"; then
    assert_contains_ws "${PERSIST_STEP2}" "${TICKET_APPEND}" \
      "phase-1-plan.md ## Persist the Plan step 2 must append the session-scoped bundle path (ticket form)"
    assert_contains_ws "${PERSIST_STEP2}" "${TICKETLESS_PAREN}" \
      "phase-1-plan.md ## Persist the Plan step 2 serves both modes, so it must also name the session-scoped ticketless form"
  fi
fi

# --- phase-9-pr.md: ## Cleanup --------------------------------------------------

LEFTOVER_SENTENCE='A context bundle left by an earlier session (a plan-file-mode run resumed in a later session skips Context Gathering) or an unscoped bundle from an older run is not removed here; it stays as an accepted ephemeral `/tmp` leftover, like an unknown `$RUN_DIR`.'
OWN_BUNDLE_SENTENCE='This run removes only its own session-scoped bundle, `cenci-context-<ticket-id>-<session-uuid>.md` (ticketless: cenci-context-<slug>-<session-uuid>.md), never another session'"'"'s.'

if load_content PHASE9_CONTENT "${PHASE9_FILE}" "phase-9-pr.md"; then
  if require_section CLEANUP_SECTION "${PHASE9_CONTENT}" '## Cleanup' "phase-9-pr.md"; then
    assert_contains_ws "${CLEANUP_SECTION}" "${RM_ENTRY}" \
      "phase-9-pr.md ## Cleanup rm -f list must remove only the session-scoped bundle path"
    assert_contains_ws "${CLEANUP_SECTION}" "${OWN_BUNDLE_SENTENCE}" \
      "phase-9-pr.md ## Cleanup prose must say the run removes only its own session-scoped bundle, naming both literal forms"
    assert_contains_ws "${CLEANUP_SECTION}" "${LEFTOVER_SENTENCE}" \
      "phase-9-pr.md ## Cleanup must state an earlier session's or unscoped older bundle is an accepted ephemeral /tmp leftover, like an unknown \$RUN_DIR (plan-file-mode resume edge case)"
  fi
fi

# --- agents/context-gatherer.md: ## Inputs bundle-path example -----------------

if load_content GATHERER_CONTENT "${GATHERER_FILE}" "agents/context-gatherer.md"; then
  if require_section GATHERER_INPUTS "${GATHERER_CONTENT}" '## Inputs (provided by the main agent)' "agents/context-gatherer.md"; then
    if require_block GATHERER_EXAMPLE "${GATHERER_INPUTS}" 'Bundle output path' "agents/context-gatherer.md (bundle-path example)"; then
      assert_contains_ws "${GATHERER_EXAMPLE}" "\`${TICKET_PATH}\`" \
        "agents/context-gatherer.md bundle-path example must show the session-scoped ticket-mode path"
      assert_contains_ws "${GATHERER_EXAMPLE}" "${TICKETLESS_PAREN}" \
        "agents/context-gatherer.md bundle-path example serves both modes, so it must also name the session-scoped ticketless form"
    fi
  fi
fi

echo "context-bundle-scoping-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
