#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #1054: a hard stop on an
# open native `blockedBy` dependency, replacing today's behavior where such a
# blocker surfaces only as a lean-planning clarifying question that parks the
# ticket on `Input Needed`. Five coordinated prose changes, no Go:
#
#   1. flow/agents/context-gatherer.md -- a new `### 5. Blocking dependencies
#      (ticket mode only)` step issuing its own read-only
#      `gh issue view <number> --repo <owner>/<repo> --json blockedBy` call
#      (never merged into §1's field list), renumbering the existing
#      `### 6. Write the bundle file` accordingly (numbering as of #1054;
#      the later design-stage removal dropped the `### 4. Design context`
#      step entirely and shifted every step below it down by one, taking
#      that step's own `(see §N)` cross-reference with it), and a mandatory
#      `blockers:` digest line with a five-form grammar (none / entry list / incomplete /
#      unsupported / unknown), same-repo `#<n>` vs cross-repo
#      `<owner>/<repo>#<n>` ref rendering, `UNKNOWN` for a non-OPEN/
#      non-CLOSED node state, a fail-closed `<unresolvable> UNKNOWN`
#      rendering for a node whose `url`/`number` does not resolve (mirroring
#      `sameRepoIssueURL`'s own unparseable-URL contract), an explicit note
#      of the deliberate cross-repo divergence from `nativeDependencies`,
#      and a placeholder-style (never literal-alternation) digest template
#      line.
#   2. flow/skills/implement/SKILL.md -- a new `## Blocked-Dependency Gate`
#      section between `## Context Gathering (Delegated)` and
#      `## Attachments` (therefore before `## Ticket Ownership`'s
#      `cenci pipeline label <id> --transition working`), with ticket/plan-
#      file/ticketless mode branches, a classification table (OPEN/UNKNOWN/
#      incomplete -> stop; unsupported -> one warning naming `gh >= 2.94.0`
#      and proceed; unknown/missing -> fallback probe), two ownership
#      branches in the stop wording (not-yet-claimed vs already-claimed,
#      the latter reporting the residual assignee + `Working`), the
#      design-ticket routing `### Design Check (hard gate)` would otherwise
#      lose to this earlier gate, both standing "do not fetch the ticket in
#      the main agent" rules amended to name this gate's one-field probe,
#      `blockers:` added to the digest store list (Phase 1 forwards it
#      verbatim), an extended `## Attachments` effective-order sentence, and
#      a fifth named `## Pipeline` session shape whose persists-nothing
#      claim is scoped around the `prepare` call that already ran.
#   3. flow/agents/planner.md -- a routing rule that an open blocker is never
#      a clarifying question in either mode, a new `### Blocked Dependencies`
#      section emitted before `## Clarifying Questions`, no new `gh` call,
#      and a legacy `Depends on #<n>` line routed to `### Open Questions`
#      instead; `### Blocked Dependencies` added to the `## Plan Output`
#      template immediately before `## Clarifying Questions`.
#   4. flow/skills/implement/phases/phase-1-plan.md -- `## Planner
#      Delegation` forwards the digest's `blockers:` line verbatim and
#      unconditionally; a new `### Blocked-Dependency Stop` immediately
#      before `### Split Gate` in `## Route Planner Output`, evaluated
#      first, persisting nothing; an extended Resume-mode note excluding
#      `## Resume From Draft` step 5's re-delegation; a third named
#      exception in `## Pipeline: Plan Stage` alongside the pinned, unchanged
#      second-exception substring.
#   5. flow/skills/implement/codex.md -- the same hard stop for the Codex
#      entrypoint, covering `/plan` *and* `apply` (which persists the plan
#      file, initializes the checkpoint, creates the worktree, and writes
#      labels -- the Claude side gates its equivalent plan-file mode with
#      its own direct probe, so apply must re-check rather than trust
#      `/plan`'s earlier verdict), positioned so `parity.test.sh`'s
#      `Stop before mutations` -> `create the worktree` ordering anchor
#      still holds, and never using `AskUserQuestion` (carried-forward
#      negative assertion).
#
# Fixture-free, grep-based idiom of flow/tests/implement-split-gate-contract.test.sh:
# `set -uo pipefail`, a `failures` counter, `grep -qF` assert helpers built on
# a pure-extractor + `require_*` nameref-wrapper split (`fail()` is never
# called inside `$(...)`), `assert_file_occurs_at_least` for marker-precision
# cases, and `assert_marker_precedes` for ordering -- plus
# `assert_heading_precedes`, a whole-line (`grep -nxF`) variant used wherever
# both ordered markers also appear as prose mentions earlier in the same
# file, where the substring variant would compare those mentions and pass
# regardless of where the real sections sit. Auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob -- no registration needed. No
# `read_*`-named helpers, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's repo-wide scan (helpers
# below are named `load_*`/`extract_*`/`require_*`/`first_*`/`assert_*`
# instead).
#
# Section extraction is fence-aware (a toggled `` ``` `` flag), mirroring
# implement-split-gate-contract.test.sh's `extract_section` -- context-
# gatherer.md's `### 1.`/`### 6.` procedure steps and the `## Digest` fenced
# template, SKILL.md's `## Blocked-Dependency Gate` and `## Pipeline`,
# planner.md's `## Clarifying Questions` and `## Plan Output` template, and
# phase-1-plan.md's `## Route Planner Output` and `## Planner Delegation`
# all contain, or sit near, fenced code blocks whose literal
# `## <heading>`/`### <heading>`-shaped lines must never be mistaken for a
# real section boundary. `extract_subsection` extends the same fence-aware
# idiom one level down, to `###`-heading-bounded procedure steps in
# context-gatherer.md, since `extract_section` only stops at a real `## `
# boundary -- needed to prove the new blockedBy call was NOT merged into
# `### 1. Fetch the ticket (ticket mode only)`'s own `--json` field list.
#
# Marker precision (docs/shell-scripting-gotchas.md rule 3): `blockedBy`
# already occurs once in SKILL.md (the Design Check's design-ticket lookup)
# -- asserted below via an increased file-wide count (>= 2) *and* a
# section-scoped assertion that the second occurrence lives inside
# `## Blocked-Dependency Gate` itself, never merely bare presence. Similarly,
# `## Pipeline: Plan Stage` in phase-1-plan.md already contains the literal
# substring "makes no `cenci pipeline` call of any kind" (the existing
# second-named-exception sentence) -- the new `### Blocked-Dependency Stop`
# reuses that exact phrase in `## Route Planner Output`, a *different*
# section that does not contain it today, so the assertion below is
# section-scoped to `## Route Planner Output` rather than a bare file-wide
# presence check, which would otherwise vacuously pass against the
# pre-existing Plan-Stage sentence alone. Every other marker below was
# verified to have zero baseline occurrences across all five target files
# before this test was written.
#
# Covered files:
#   - flow/agents/context-gatherer.md (§5 blocking-dependencies step +
#     renumbered §6 write-the-bundle step + blockers: five-form grammar +
#     same-repo/cross-repo ref rendering + UNKNOWN state)
#   - flow/skills/implement/SKILL.md (## Blocked-Dependency Gate placement,
#     mode branches, classification table, stop wording, ## Attachments
#     effective-order sentence, ## Pipeline fifth named shape)
#   - flow/agents/planner.md (never-a-clarifying-question rule,
#     ### Blocked Dependencies section + ordering in ## Plan Output,
#     no new gh call, legacy Depends-on routing)
#   - flow/skills/implement/phases/phase-1-plan.md (## Planner Delegation
#     blockers: passthrough, ### Blocked-Dependency Stop + ordering before
#     ### Split Gate, Resume-mode note extension, ## Pipeline: Plan Stage
#     third named exception)
#   - flow/skills/implement/codex.md (condensed hard-stop sentence,
#     ordering before "Stop before mutations", never AskUserQuestion)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-blocked-dependency-gate.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-blocked-dependency-gate.test.sh: failed to resolve flow directory." >&2; exit 2; }
CONTEXT_GATHERER="${FLOW_DIR}/agents/context-gatherer.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
PLANNER="${FLOW_DIR}/agents/planner.md"
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
CODEX_MD="${FLOW_DIR}/skills/implement/codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}
assert_file_lacks() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}
assert_file_occurs_at_least() {
  # $1=file $2=needle $3=min-count $4=description -- proves a *new*
  # occurrence was added beyond a pre-existing one, for markers this
  # ticket's text legitimately reuses (see the file header's marker-
  # precision note above). Counts lines containing the needle
  # (`grep -cF`), not literal substring occurrences -- a line
  # containing the needle twice still counts as one.
  [[ -n "$2" ]] || { fail "$4: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$4: file not found: $1"; return; }
  local count
  count="$(grep -cF -- "$2" "$1")"
  [[ "${count}" -ge "$3" ]] || fail "$(basename "$1") $4 (expected at least $3 matching lines, found ${count}: $2)"
}

# load_content <file> -- pure read, no fail() side effect: safe to call
# inside $(...). Returns empty (and a non-zero exit, per the unchecked-cat
# gotcha) on a missing/unreadable file.
load_content() {
  cat "$1" 2>/dev/null
}

# require_content <result-var> <file> <label> -- nameref wrapper; fails
# closed with a distinct message and assigns "" on a read failure. Must NOT
# be invoked via $(...).
require_content() {
  local -n _res="$1"
  local _c
  if ! _c="$(load_content "$2")"; then
    fail "$3: could not read file: $2"
    _res=""
    return 1
  fi
  _res="${_c}"
}

# extract_section <content> <exact-heading-line> -- pure, fence-aware
# extractor: returns the body of the named "## <heading>" section through
# the next real (unfenced) "## "-level heading, or EOF. A line that merely
# *looks like* a "## " heading while inside a fenced (```) code block is
# never treated as a section boundary. Safe inside $(...): no fail() side
# effect.
extract_section() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# require_section <result-var> <content> <heading-line> <label> -- nameref
# wrapper: assigns the extracted section body, or fails closed and assigns
# "" on a missing section. Must NOT be invoked via $(...).
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

# extract_subsection <content> <exact-heading-line> -- pure, fence-aware
# extractor one level down from extract_section: returns the body of the
# named "### <heading>" (or "## <heading>") through the next real
# (unfenced) "### " or "## "-level heading, or EOF. Needed for context-
# gatherer.md's `### 1.`/`### 6.` numbered procedure steps, where
# extract_section's "## "-only boundary would over-capture through every
# later `### N.` step up to the next real "## " heading. Safe inside
# $(...): no fail() side effect.
extract_subsection() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && (/^### / || /^## /) { exit }
    on { print }
  ' <<<"$1"
}

# require_subsection <result-var> <content> <heading-line> <label> --
# nameref wrapper for extract_subsection, same fail-closed contract as
# require_section. Must NOT be invoked via $(...).
require_subsection() {
  local -n _res="$1"
  local _body
  _body="$(extract_subsection "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' subsection"
    _res=""
    return 1
  fi
  _res="${_body}"
}

assert_section_contains() {
  # $1=section-body $2=needle $3=label
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" == *"$2"* ]] || fail "$3 (expected section to contain: $2)"
}
assert_section_lacks() {
  # $1=section-body $2=needle $3=label
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3 (expected section to NOT contain: $2)"
}

# first_match_line_in_file <file> <needle> -- pure: prints the 1-based line
# number of the needle's first literal match in the file, or nothing if
# absent. Safe inside $(...): no fail() side effect.
first_match_line_in_file() {
  grep -nF -m1 -- "$2" "$1" 2>/dev/null | cut -d: -f1
}

# assert_marker_precedes <file> <needle-before> <needle-after> <label> --
# ordering assertion via a local grep -n first-match offset helper. Calls
# fail() directly in the parent shell -- must NOT be invoked via $(...).
assert_marker_precedes() {
  local file="$1" before="$2" after="$3" label="$4"
  local line_before line_after
  line_before="$(first_match_line_in_file "${file}" "${before}")"
  line_after="$(first_match_line_in_file "${file}" "${after}")"
  if [[ -z "${line_before}" || -z "${line_after}" ]]; then
    fail "${label}: could not locate both markers to compare ordering (before='${before}' line='${line_before:-<missing>}'; after='${after}' line='${line_after:-<missing>}')"
    return
  fi
  if [[ "${line_before}" -ge "${line_after}" ]]; then
    fail "${label}: expected '${before}' (line ${line_before}) to precede '${after}' (line ${line_after})"
  fi
}

# first_heading_line_in_file <file> <exact-heading-line> -- pure: prints the
# 1-based line number of the first line that equals the argument *in full*
# (`grep -nxF`), or nothing if absent. Distinct from
# first_match_line_in_file's substring match, and the distinction is
# load-bearing: both `### Blocked-Dependency Stop` and `### Split Gate`
# appear in phase-1-plan.md as backtick-quoted prose mentions long before
# their real headings (the `## Pipeline: Plan Stage` third-exception
# sentence, the `## Lean Approval Path` Split-Gate backstop bullet, and
# `### Blocked-Dependency Stop`'s own body, which names `### Split Gate`).
# A substring-based ordering assertion between the two therefore compares
# prose positions and passes no matter where the real subsections sit --
# vacuous. Safe inside $(...): no fail() side effect.
first_heading_line_in_file() {
  grep -nxF -m1 -- "$2" "$1" 2>/dev/null | cut -d: -f1
}

# assert_heading_precedes <file> <heading-before> <heading-after> <label> --
# ordering assertion over whole-line heading matches, so a prose mention of
# either heading can never satisfy it. Calls fail() directly in the parent
# shell -- must NOT be invoked via $(...).
assert_heading_precedes() {
  local file="$1" before="$2" after="$3" label="$4"
  local line_before line_after
  line_before="$(first_heading_line_in_file "${file}" "${before}")"
  line_after="$(first_heading_line_in_file "${file}" "${after}")"
  if [[ -z "${line_before}" || -z "${line_after}" ]]; then
    fail "${label}: could not locate both headings as whole lines to compare ordering (before='${before}' line='${line_before:-<missing>}'; after='${after}' line='${line_after:-<missing>}')"
    return
  fi
  if [[ "${line_before}" -ge "${line_after}" ]]; then
    fail "${label}: expected heading '${before}' (line ${line_before}) to precede heading '${after}' (line ${line_after})"
  fi
}

# first_match_line_in_content <content> <needle> -- content-based sibling of
# first_match_line_in_file, for ordering assertions scoped to an already-
# extracted section body (e.g. planner.md's ## Plan Output template, where a
# file-wide first-match would instead hit the file's real, earlier
# ## Clarifying Questions heading). Safe inside $(...): no fail() side
# effect.
first_match_line_in_content() {
  grep -nF -m1 -- "$2" <<<"$1" 2>/dev/null | cut -d: -f1
}

# assert_marker_precedes_in_content <content> <needle-before> <needle-after>
# <label> -- content-scoped sibling of assert_marker_precedes. Calls fail()
# directly in the parent shell -- must NOT be invoked via $(...).
assert_marker_precedes_in_content() {
  local content="$1" before="$2" after="$3" label="$4"
  local line_before line_after
  line_before="$(first_match_line_in_content "${content}" "${before}")"
  line_after="$(first_match_line_in_content "${content}" "${after}")"
  if [[ -z "${line_before}" || -z "${line_after}" ]]; then
    fail "${label}: could not locate both markers to compare ordering (before='${before}' line='${line_before:-<missing>}'; after='${after}' line='${line_after:-<missing>}')"
    return
  fi
  if [[ "${line_before}" -ge "${line_after}" ]]; then
    fail "${label}: expected '${before}' (line ${line_before}) to precede '${after}' (line ${line_after})"
  fi
}

# =====================================================================
# flow/agents/context-gatherer.md
# =====================================================================

require_content CTX_CONTENT "${CONTEXT_GATHERER}" "context-gatherer.md"

# --- ### 5. Blocking dependencies step + renumbered ### 6. -----------------
#
# Numbering note: skills/design/ and agents/context-gatherer.md's own
# `### 4. Design context (if a design path was provided)` step were removed
# entirely by the design-stage removal, which renumbered every later step
# down by one (old §5 Blocking dependencies -> §4 Project context stays
# §4... see the file itself: the surviving order is 1 Fetch the ticket,
# 2 Parent-child detection, 3 Discover attachments, 4 Project context,
# 5 Blocking dependencies, 6 Write the bundle file). This test's numbered
# markers below were shifted down by one to match; the design-context
# subsection and its now-nonexistent "(see §N)" cross-reference no longer
# exist, so both are dropped rather than renumbered.

assert_file_contains "${CONTEXT_GATHERER}" "### 5. Blocking dependencies (ticket mode only)" \
  "must add a new ### 5. Blocking dependencies (ticket mode only) procedure step"
assert_file_contains "${CONTEXT_GATHERER}" "### 6. Write the bundle file" \
  "must renumber the existing ### 6. Write the bundle file step correctly"

CTX_GH_CALL='gh issue view <number> --repo <owner>/<repo> --json blockedBy'
assert_file_contains "${CONTEXT_GATHERER}" "${CTX_GH_CALL}" \
  "must issue its own dedicated read-only gh issue view ... --json blockedBy call"

if require_subsection CTX_S1_CONTENT "${CTX_CONTENT}" "### 1. Fetch the ticket (ticket mode only)" "context-gatherer.md"; then
  assert_section_lacks "${CTX_S1_CONTENT}" "blockedBy" \
    "context-gatherer.md (### 1. Fetch the ticket) must NOT merge the blockedBy field into §1's own --json field list"
fi

# --- blockers: five-form digest grammar -----------------------------------

assert_file_contains "${CONTEXT_GATHERER}" "blockers: none" \
  "must define the blockers: none grammar form (no blocking dependencies)"
assert_file_contains "${CONTEXT_GATHERER}" "incomplete <k>/<totalCount>" \
  "must define the blockers: incomplete <k>/<totalCount> grammar form (truncated blockedBy list)"
assert_file_contains "${CONTEXT_GATHERER}" "unsupported — <exact gh stderr>" \
  "must define the blockers: unsupported — <exact gh stderr> grammar form (capability gap)"
assert_file_contains "${CONTEXT_GATHERER}" "unknown — <exact gh stderr>" \
  "must define the blockers: unknown — <exact gh stderr> grammar form (unresolvable failure)"
assert_file_contains "${CONTEXT_GATHERER}" "unknown json field" \
  "must detect the capability-gap case by matching gh stderr containing 'unknown json field' (case-insensitively)"

# --- Same-repo / cross-repo ref rendering + UNKNOWN state -----------------

assert_file_contains "${CONTEXT_GATHERER}" "/<owner>/<repo>/issues/<n>" \
  "must define the same-repo URL-path shape used to render a blocker as #<n>"
assert_file_contains "${CONTEXT_GATHERER}" "<owner>/<repo>#<n>" \
  "must define the cross-repo blocker ref rendering as <owner>/<repo>#<n>"
assert_file_contains "${CONTEXT_GATHERER}" "sameRepoIssueURL" \
  "must name sameRepoIssueURL as the semantic mirror for same-repo URL-path validation"
assert_file_contains "${CONTEXT_GATHERER}" "its own inline node state" \
  "must classify a cross-repo blocker from its own inline node state, not treat it as unresolvable"
assert_file_contains "${CONTEXT_GATHERER}" "neither \`OPEN\` nor \`CLOSED\`" \
  "must render UNKNOWN for a node state that is neither OPEN nor CLOSED"
assert_file_contains "${CONTEXT_GATHERER}" "nativeDependencyState" \
  "must name nativeDependencyState as the semantic mirror for the UNKNOWN fail-closed default"

# --- Unparseable/missing url fails closed, and the cross-repo divergence
#     from the Go dispatch gate is documented rather than silent ----------

assert_file_contains "${CONTEXT_GATHERER}" "Unresolvable \`url\` fails closed" \
  "must define what happens when a blockedBy node's url is absent, unparseable, or not an issue path"
assert_file_contains "${CONTEXT_GATHERER}" "<unresolvable> UNKNOWN" \
  "must render an unresolvable-url blocker as <unresolvable> UNKNOWN so the gate's UNKNOWN -> STOP row catches it"
assert_file_contains "${CONTEXT_GATHERER}" "never omit the node, and never render it \`CLOSED\`" \
  "must forbid omitting an unresolvable-url blocker or rendering it CLOSED (the two ways a blocked ticket would slip through)"
assert_file_contains "${CONTEXT_GATHERER}" "Known divergence from the Go gate on cross-repo links" \
  "must document, not leave silent, that this grammar is laxer than nativeDependencies on cross-repo blockers"

# --- Digest template renders one concrete form, never the placeholder -----

assert_file_contains "${CONTEXT_GATHERER}" "blockers: <exactly one of §5's five forms, rendered — never this placeholder text>" \
  "digest template's blockers: line must be a placeholder like every other template line, not a literal alternation of all five forms"
assert_file_contains "${CONTEXT_GATHERER}" "The \`blockers:\` line is rendered, never echoed" \
  "must tell the gatherer to emit one concrete form, since a template-shaped line reads as unparseable to the gate"
assert_file_contains "${CONTEXT_GATHERER}" "In ticketless mode there is no ticket to check, so omit the line entirely" \
  "must state the ticketless-mode behavior as an omission rather than a placeholder or n/a value"

# =====================================================================
# flow/skills/implement/SKILL.md -- ## Blocked-Dependency Gate
# =====================================================================

require_content SKILL_CONTENT "${IMPLEMENT_SKILL}" "SKILL.md"

assert_file_contains "${IMPLEMENT_SKILL}" "## Blocked-Dependency Gate" \
  "must add a new ## Blocked-Dependency Gate section"

# --- The gate's direct probe is a main-agent `gh` call, so both standing
#     "do not fetch the ticket in the main agent" rules must be amended to
#     name it -- otherwise the gate is simultaneously required and
#     forbidden, and a malformed digest silently skips the check ----------

assert_file_contains "${IMPLEMENT_SKILL}" "There are exactly two exceptions, both named and bounded: \`cenci pipeline plan-check\`" \
  "SKILL.md's ticket-mode no-fetch rule must name the gate's blockedBy probe alongside cenci pipeline plan-check as its second exception"
assert_file_contains "${IMPLEMENT_SKILL}" "The single exception is \`## Blocked-Dependency Gate\`'s \`gh issue view <number> --repo <owner>/<repo> --json blockedBy\` fallback probe" \
  "SKILL.md's post-digest 'do not re-fetch the ticket in the main agent' rule must name the gate's fallback probe as its exception"
# The former "not the precedent for this probe" correction (### Design Check
# already issuing this idiom) no longer applies -- `### Design Check (hard
# gate)` was removed entirely by the design-stage removal, so there is no
# stale claim left to correct.

# --- The digest store list must retain the blockers: line, since Phase 1's
#     ## Planner Delegation forwards it verbatim and unconditionally -------

if require_section GATHERING_SECTION "${SKILL_CONTENT}" "## Context Gathering (Delegated)" "SKILL.md"; then
  assert_section_contains "${GATHERING_SECTION}" "\`blockers:\` — the raw digest line, stored **verbatim** and retained for the whole session" \
    "SKILL.md (## Context Gathering) 'From the digest, store:' list must include the blockers: line, which Phase 1 forwards verbatim"
  assert_section_contains "${GATHERING_SECTION}" "Do not summarize it, normalize it, or drop it once the gate has passed" \
    "SKILL.md (## Context Gathering) must forbid dropping the blockers: line after the gate passes -- the planner backstop still needs it at Phase 1"
fi

# --- Placement: after ## Context Gathering (Delegated), before
#     ## Attachments, therefore before ## Ticket Ownership's
#     cenci pipeline label <id> --transition working call --------------

SKILL_LABEL_WORKING_CMD='cenci pipeline label <id> --transition working'
assert_marker_precedes "${IMPLEMENT_SKILL}" "## Blocked-Dependency Gate" "## Attachments" \
  "SKILL.md ## Blocked-Dependency Gate must precede ## Attachments"
assert_marker_precedes "${IMPLEMENT_SKILL}" "## Blocked-Dependency Gate" "${SKILL_LABEL_WORKING_CMD}" \
  "SKILL.md ## Blocked-Dependency Gate must precede ## Ticket Ownership's cenci pipeline label <id> --transition working call"

# --- blockedBy occurrence count: must be >= 2, and one occurrence must
#     live inside the gate itself (marker-precision: a bare file-wide
#     presence check would vacuously pass against a mention anywhere else
#     in the file) -----------------------------------------------------

assert_file_occurs_at_least "${IMPLEMENT_SKILL}" "blockedBy" 2 \
  "must reference blockedBy at least twice, with one occurrence inside the new gate"

if require_section GATE_SECTION "${SKILL_CONTENT}" "## Blocked-Dependency Gate" "SKILL.md"; then
  assert_section_contains "${GATE_SECTION}" "blockedBy" \
    "SKILL.md (## Blocked-Dependency Gate) must itself reference blockedBy, not merely elsewhere in the file"

  # Mode branches
  assert_section_contains "${GATE_SECTION}" "classify the digest's \`blockers:\` line" \
    "SKILL.md (## Blocked-Dependency Gate) ticket-mode branch must classify the digest's blockers: line"
  assert_section_contains "${GATE_SECTION}" "issue the same one-field probe directly from the main agent" \
    "SKILL.md (## Blocked-Dependency Gate) plan-file-mode branch must issue the same one-field probe directly from the main agent"
  assert_section_contains "${GATE_SECTION}" "explicit documented no-op" \
    "SKILL.md (## Blocked-Dependency Gate) ticketless-mode branch must be an explicit documented no-op"

  # Classification table
  assert_section_contains "${GATE_SECTION}" "proceed, no prompt, no extra output" \
    "SKILL.md (## Blocked-Dependency Gate) classification table must proceed with no prompt/extra output when none or all-CLOSED"
  assert_section_contains "${GATE_SECTION}" "any entry \`OPEN\`" \
    "SKILL.md (## Blocked-Dependency Gate) classification table must stop on any entry OPEN"
  assert_section_contains "${GATE_SECTION}" "any entry \`UNKNOWN\`, or \`incomplete" \
    "SKILL.md (## Blocked-Dependency Gate) classification table must fail closed on any entry UNKNOWN or an incomplete list"
  assert_section_contains "${GATE_SECTION}" "if the fallback itself fails with anything other than the capability error, **STOP**" \
    "SKILL.md (## Blocked-Dependency Gate) classification table must fail closed when the fallback probe itself fails"
  assert_section_contains "${GATE_SECTION}" "prints exactly one warning naming \`gh >= 2.94.0\`" \
    "SKILL.md (## Blocked-Dependency Gate) capability-gap branch must print exactly one warning naming gh >= 2.94.0"
  assert_section_contains "${GATE_SECTION}" "explicitly not the fail-closed path" \
    "SKILL.md (## Blocked-Dependency Gate) capability-gap branch must explicitly proceed, not fail closed"
  assert_section_contains "${GATE_SECTION}" "run the direct probe as a fallback and re-classify" \
    "SKILL.md (## Blocked-Dependency Gate) must run the direct fallback probe on a missing/unknown blockers: line and re-classify"

  # Stop wording: names every ref+state, no ownership claim/Working/Input
  # Needed/comment/cenci pipeline call/subagent delegation/worktree
  assert_section_contains "${GATE_SECTION}" \
    "reports every blocking ref and its state and tells the user to re-run once the blockers close, and it takes no action of any kind — no ownership claim, no \`Working\`, no \`Input Needed\`, no ticket comment, no \`cenci pipeline\` call, no subagent delegation, no worktree" \
    "SKILL.md (## Blocked-Dependency Gate) stop must name every blocking ref+state and take no ownership/label/comment/pipeline/subagent/worktree action"

  # Ownership branches: the gate also runs in plan-file mode and on re-runs
  # of an already-claimed ticket, where "the run ended before claiming the
  # ticket" is false and leaves a stranded assignee + Working label
  # unreported.
  assert_section_contains "${GATE_SECTION}" "never assert the unclaimed case unconditionally" \
    "SKILL.md (## Blocked-Dependency Gate) must not state 'ended before claiming the ticket' unconditionally -- the gate also runs after an earlier session claimed the ticket"
  assert_section_contains "${GATE_SECTION}" "**Already claimed**" \
    "SKILL.md (## Blocked-Dependency Gate) must define an already-claimed branch (plan-file mode, or a re-run of a ticket an earlier session claimed)"
  assert_section_contains "${GATE_SECTION}" "the ticket stays assigned and labelled \`Working\` from that earlier run" \
    "SKILL.md (## Blocked-Dependency Gate) already-claimed branch must report the residual assignee and Working label"
  assert_section_contains "${GATE_SECTION}" "Never leave that residual state unreported" \
    "SKILL.md (## Blocked-Dependency Gate) already-claimed branch must forbid leaving the stranded board state unreported"

  # Design-ticket routing (removed): `### Design Check (hard gate)` and its
  # `/cenci:design` routing were removed entirely by the design-stage
  # removal, so there is no design-ticket case for this gate to name or
  # route to any more.
fi

# --- ## Attachments -- extended effective-order sentence ------------------

SKILL_EFFECTIVE_ORDER_MARKER='Context Gathering → Blocked-Dependency Gate → Attachments'
if require_section ATTACHMENTS_SECTION "${SKILL_CONTENT}" "## Attachments" "SKILL.md"; then
  assert_section_contains "${ATTACHMENTS_SECTION}" "${SKILL_EFFECTIVE_ORDER_MARKER}" \
    "SKILL.md (## Attachments) effective-order sentence must gain Blocked-Dependency Gate between Context Gathering and Attachments"
fi

# --- ## Pipeline -- fifth named session shape ------------------------------

if require_section PIPELINE_SECTION "${SKILL_CONTENT}" "## Pipeline" "SKILL.md"; then
  assert_section_contains "${PIPELINE_SECTION}" "one of five named shapes" \
    "SKILL.md (## Pipeline) must change 'one of four named shapes' to five"
  assert_section_contains "${PIPELINE_SECTION}" "like shape 4 it persists nothing **of its own**" \
    "SKILL.md (## Pipeline) fifth shape must scope its persists-nothing claim to the gate's own actions"
  assert_section_contains "${PIPELINE_SECTION}" "advanced the ticket's persisted stage to \`prepared\`" \
    "SKILL.md (## Pipeline) fifth shape must carve out the prepare call that already ran before the gate -- an unqualified 'persists nothing' is false"
  assert_section_contains "${PIPELINE_SECTION}" "can fire before the ticket is even claimed" \
    "SKILL.md (## Pipeline) fifth shape must note it can fire before the ticket is even claimed, unlike every other shape"
  assert_section_contains "${PIPELINE_SECTION}" "but it is not restricted to that case" \
    "SKILL.md (## Pipeline) fifth shape must not imply the stop always fires pre-claim -- plan-file mode and re-runs fire post-claim"
fi

# =====================================================================
# flow/agents/planner.md
# =====================================================================

require_content PLANNER_CONTENT "${PLANNER}" "planner.md"

if require_section CQ_SECTION "${PLANNER_CONTENT}" "## Clarifying Questions" "planner.md"; then
  assert_section_contains "${CQ_SECTION}" \
    "an open blocking dependency is never a clarifying question in either lean or interactive mode" \
    "planner.md (## Clarifying Questions) must state an open blocking dependency is never a clarifying question in either mode"
  assert_section_contains "${CQ_SECTION}" "### Blocked Dependencies" \
    "planner.md (## Clarifying Questions) must introduce the ### Blocked Dependencies output section"
  assert_section_contains "${CQ_SECTION}" "<ref> — OPEN" \
    "planner.md (## Clarifying Questions) must specify one <ref> — OPEN line per blocker"
  assert_section_contains "${CQ_SECTION}" "No new \`gh\` call from the planner" \
    "planner.md (## Clarifying Questions) must state no new gh call is made from the planner"
  assert_section_contains "${CQ_SECTION}" "goes under \`### Open Questions\` — never a stop" \
    "planner.md (## Clarifying Questions) must route an unconfirmable legacy Depends on #<n> line to ### Open Questions, never a stop"
  assert_section_contains "${CQ_SECTION}" "Depends on #" \
    "planner.md (## Clarifying Questions) must name the legacy prose Depends on #<n> form"
fi

# --- ## Plan Output template: ### Blocked Dependencies added immediately
#     before ## Clarifying Questions (ordering scoped to the template body,
#     not the file-wide first ## Clarifying Questions heading, which is the
#     real prose section read above and occurs earlier in the file) -------

if require_section PLAN_OUTPUT_SECTION "${PLANNER_CONTENT}" "## Plan Output" "planner.md"; then
  assert_section_contains "${PLAN_OUTPUT_SECTION}" "### Blocked Dependencies" \
    "planner.md (## Plan Output template) must add a ### Blocked Dependencies template section"
  assert_marker_precedes_in_content "${PLAN_OUTPUT_SECTION}" "### Blocked Dependencies" "## Clarifying Questions" \
    "planner.md (## Plan Output template) ### Blocked Dependencies must be placed immediately before ## Clarifying Questions"
fi

# =====================================================================
# flow/skills/implement/phases/phase-1-plan.md
# =====================================================================

require_content PHASE1_CONTENT "${PHASE1_PLAN}" "phase-1-plan.md"

# --- ## Planner Delegation -- forwards blockers: line verbatim, always ----

if require_section PLANNER_DELEGATION_SECTION "${PHASE1_CONTENT}" "## Planner Delegation" "phase-1-plan.md"; then
  assert_section_contains "${PLANNER_DELEGATION_SECTION}" "forward the digest's \`blockers:\` line verbatim" \
    "phase-1-plan.md (## Planner Delegation) must forward the digest's blockers: line verbatim in the ticket-mode list"
  assert_section_contains "${PLANNER_DELEGATION_SECTION}" "including \`blockers: none\`" \
    "phase-1-plan.md (## Planner Delegation) must forward blockers: unconditionally, including blockers: none"
fi

# --- ## Route Planner Output -- new ### Blocked-Dependency Stop ----------

if require_section ROUTE_SECTION "${PHASE1_CONTENT}" "## Route Planner Output" "phase-1-plan.md"; then
  assert_section_contains "${ROUTE_SECTION}" "### Blocked-Dependency Stop" \
    "phase-1-plan.md (## Route Planner Output) must add a new ### Blocked-Dependency Stop subsection"
  assert_section_contains "${ROUTE_SECTION}" \
    "evaluated **first** — before the clarifying-questions bullets, before \`### Split Gate\`, and before any persist" \
    "phase-1-plan.md (## Route Planner Output) must state the Blocked-Dependency Stop is evaluated first, before the bullets and before the Split Gate"
  assert_section_contains "${ROUTE_SECTION}" "fires on a non-empty \`### Blocked Dependencies\`" \
    "phase-1-plan.md (### Blocked-Dependency Stop) must fire on a non-empty ### Blocked Dependencies"
  assert_section_contains "${ROUTE_SECTION}" "makes no \`cenci pipeline\` call of any kind" \
    "phase-1-plan.md (### Blocked-Dependency Stop) must make no cenci pipeline call of any kind"
  assert_section_contains "${ROUTE_SECTION}" "leaves \`Working\` and the assignee claim as found" \
    "phase-1-plan.md (### Blocked-Dependency Stop) must leave Working and the assignee claim as found"
  assert_section_contains "${ROUTE_SECTION}" "ends the turn naming the blockers" \
    "phase-1-plan.md (### Blocked-Dependency Stop) must end the turn naming the blockers"
  assert_section_contains "${ROUTE_SECTION}" "never invokes \`AskUserQuestion\`" \
    "phase-1-plan.md (### Blocked-Dependency Stop) must never invoke AskUserQuestion"
  assert_section_contains "${ROUTE_SECTION}" "never routes to \`## Unattended Escalation Path\`" \
    "phase-1-plan.md (### Blocked-Dependency Stop) must never route to ## Unattended Escalation Path"

  RESUME_EXEMPTION_MARKER='this stop governs only a fresh `## Planner Delegation` return, never `## Resume From Draft` step 5'"'"'s re-delegation'
  assert_section_contains "${ROUTE_SECTION}" "${RESUME_EXEMPTION_MARKER}" \
    "phase-1-plan.md (## Route Planner Output, Resume-mode note) must state the Blocked-Dependency Stop governs only a fresh Planner Delegation return, never Resume From Draft step 5's re-delegation"
fi

assert_heading_precedes "${PHASE1_PLAN}" "### Blocked-Dependency Stop" "### Split Gate" \
  "phase-1-plan.md ### Blocked-Dependency Stop heading must precede the ### Split Gate heading"

# --- ## Pipeline: Plan Stage -- third named exception, without rewording
#     the existing pinned second-exception substring -----------------------

PINNED_SECOND_EXCEPTION='a second named exception: the Split Gate Stop branch records nothing at all'
if require_section PLAN_STAGE_SECTION "${PHASE1_CONTENT}" "## Pipeline: Plan Stage" "phase-1-plan.md"; then
  assert_section_contains "${PLAN_STAGE_SECTION}" "${PINNED_SECOND_EXCEPTION}" \
    "phase-1-plan.md (## Pipeline: Plan Stage) must NOT reword the pinned second-named-exception substring (Split Gate Stop branch records nothing at all)"
  assert_section_contains "${PLAN_STAGE_SECTION}" "a third named exception" \
    "phase-1-plan.md (## Pipeline: Plan Stage) must append a third named exception for the Blocked-Dependency Stop branch"
  assert_section_contains "${PLAN_STAGE_SECTION}" "### Blocked-Dependency Stop" \
    "phase-1-plan.md (## Pipeline: Plan Stage) third-exception sentence must name the ### Blocked-Dependency Stop branch"
fi

# =====================================================================
# flow/skills/implement/codex.md -- condensed hard-stop sentence
# =====================================================================

CODEX_HARD_STOP_MARKER='An open native blocking dependency is a hard stop before any mutation'

assert_file_contains "${CODEX_MD}" "${CODEX_HARD_STOP_MARKER}" \
  "codex.md must carry the same hard stop for the Codex entrypoint"
assert_marker_precedes "${CODEX_MD}" "${CODEX_HARD_STOP_MARKER}" "Stop before mutations" \
  "codex.md new hard-stop sentence must precede the existing 'Stop before mutations' sentence, preserving parity.test.sh's ordering anchor"
assert_marker_precedes "${CODEX_MD}" "Stop before mutations" "create the worktree" \
  "codex.md 'Stop before mutations' must still precede 'create the worktree' (parity.test.sh's own pinned ordering anchor)"
assert_file_lacks "${CODEX_MD}" "AskUserQuestion" \
  "must never use AskUserQuestion in the cross-tool-portable codex.md (flow/AGENTS.md critical rule) -- carried-forward negative assertion, not new"

# --- apply-mode parity: the Claude side gates plan-file mode with its own
#     direct probe, so Codex's apply path (which persists the plan file,
#     initializes the checkpoint, creates the worktree, and writes labels)
#     must re-check too -- a blocker linked after /plan otherwise sails
#     into every mutation the gate exists to prevent ------------------------

assert_file_contains "${CODEX_MD}" "\`apply\` runs the identical check again as its own first step" \
  "codex.md must gate apply mode too, not only /plan -- apply mutates ticket, checkpoint, worktree, and labels"
assert_file_contains "${CODEX_MD}" "before it persists the plan file, initializes the checkpoint, creates the worktree, or writes any label" \
  "codex.md apply-mode check must run before every mutation apply performs"
assert_file_contains "${CODEX_MD}" "an approved plan is not evidence the dependency is still clear" \
  "codex.md must state why apply re-checks rather than trusting /plan's earlier verdict"
assert_file_contains "${CODEX_MD}" "rather than claiming the run stopped before the ticket was claimed" \
  "codex.md apply-mode stop must report residual assignee/labels instead of asserting the pre-claim wording"

echo "implement-blocked-dependency-gate.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
