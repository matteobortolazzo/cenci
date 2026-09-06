#!/usr/bin/env bash
# Contract test for ticket #1088 — Phase 1 resolves planning autonomy from the
# attended override, not the repo's `.cenci/config.json` alone.
#
# `#1086` added the fleet-wide `planning.attended` switch and
# `cenci planning attended on|off|status`; `#1087` forwards the resolved flag
# into sandboxed sessions as `CENCI_ATTENDED=1`/`0` (and pins `0` on every
# `cenci dispatch`-launched session). This ticket is the *consumer*: it
# replaces phase-1-plan.md's single-source `planning.autonomy` read with one
# documented resolution point (`## Resolve Planning Autonomy`) that every
# autonomy-dependent branch in the file reads, so a human sitting at the
# keyboard in a `"lean"` repo is asked a question instead of having it posted
# to the ticket.
#
# Follows the exact idiom of flow/tests/configure-autonomy-questions.test.sh
# (the direct precedent for "new authored section + a cross-project
# docs/autonomous-loop.md claim"): pinned exact authored substrings as
# constants — never derived from the doc under test, so the red phase fails
# for the right reason and green has an unambiguous authoring target —
# `read_doc_raw`/`require_doc` nameref helpers for in-project docs,
# `read_repo_doc_raw`/`require_repo_doc` for the cross-project
# `docs/autonomous-loop.md`, `assert_contains_ws` for prose that may be
# line-wrapped in the source markdown (flow/docs/shell-scripting-gotchas.md),
# `assert_absent_paired_ws` for non-vacuous absence checks, a `failures=`
# counter, no fixtures. Never calls `fail()` inside `$(...)` — every
# extractor below is a pure function per
# flow/docs/shell-scripting-gotchas.md's read-helper-purity rule (so this
# file is compliant with flow/tests/read-helper-purity-contract.test.sh's
# repo-wide scan). Auto-discovered by scripts/run-checks.sh's `*.test.sh`
# glob — no registration needed.
#
# Marker precision (flow/docs/shell-scripting-gotchas.md rule 3 — assert the
# specific replacement text at the edit site, never a generic marker that may
# already match unrelated prose): the consumer-site assertions below are
# scoped to the owning section's extracted body, because `resolvedAutonomy`,
# `lean` and `interactive` all recur throughout phase-1-plan.md; a whole-file
# grep for any of them would pass vacuously without proving the specific
# consumer was rewired.
#
# CI path-filter caveat (same accepted coupling as
# configure-autonomy-questions.test.sh): this suite lives under
# `flow/tests/**` and is only run by `flow-ci.yml`'s `flow-test` job. Several
# assertions below reach outside `flow/**` (`docs/autonomous-loop.md`); that
# path is already registered in `flow-ci.yml`'s `extra` filter (the gap
# `#965` closed), so a PR touching only that doc still triggers this suite.
#
# Covered files:
#   - flow/skills/implement/phases/phase-1-plan.md (the new
#     `## Resolve Planning Autonomy` section and every rewired consumer)
#   - flow/skills/implement/SKILL.md (the Cost Controls restatement)
#   - docs/autonomous-loop.md (cross-project doc-of-record)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "attended-autonomy-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "attended-autonomy-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "attended-autonomy-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, no fail() side
  # effect: deliberately safe to call inside a $(...) command substitution.
  local _relpath="$1"
  cat "${FLOW_DIR}/${_relpath}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper; a missing
# file must never masquerade as empty content (which would make every
# absence assertion trivially pass). Must NOT be invoked via $(...).
require_doc() {
  local -n _result="$1"
  local _relpath="$2" _content
  if ! _content="$(read_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

read_repo_doc_raw() {
  # read_repo_doc_raw <repo-relative-path> — same pure-extraction contract,
  # resolved from REPO_ROOT for docs/ files that live outside flow/**.
  local _relpath="$1"
  cat "${REPO_ROOT}/${_relpath}" 2>/dev/null
}

# require_repo_doc <result-var> <repo-relative-path>. Must NOT be invoked via $(...).
require_repo_doc() {
  local -n _result="$1"
  local _relpath="$2" _content
  if ! _content="$(read_repo_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${REPO_ROOT}/${_relpath}"
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

normalize_ws() {
  # normalize_ws <content> — collapses newlines and repeated whitespace to a
  # single space so a markdown-wrapped sentence matches as one substring, per
  # flow/docs/shell-scripting-gotchas.md's line-wrapping pitfall.
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws <content> <required-substring> <label>
assert_contains_ws() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# assert_absent_paired_ws <content> <existence-marker> <forbidden> <label> —
# a bare absence check would pass vacuously while the section it is scoped to
# is still unauthored, proving nothing about this ticket. Requiring the
# marker's own presence first makes the assertion fail loudly, for the right
# reason, during the red phase.
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  local norm
  norm="$(normalize_ws "${content}")"
  if [[ "${norm}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- existence marker missing (whitespace-normalized): [${marker}]"
    return
  fi
  [[ "${norm}" != *"${forbidden}"* ]] || fail "${label}: forbidden text present (whitespace-normalized): [${forbidden}]"
}

# extract_h2_section <content> <exact-heading-line> — fence-aware awk
# extraction bounded to the next real (unfenced) `## ` heading, so a
# section-scoped marker cannot be vacuously satisfied by unrelated prose
# elsewhere in the file. `### ` subheadings do not terminate the section
# (`^## ` requires a space in column 3). Pure: no fail() side effect, safe
# inside $(...).
extract_h2_section() {
  local content="$1" heading="$2"
  awk -v heading="${heading}" '
    $0 == heading && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"${content}"
}

# extract_bullet <content> <bullet-prefix> — pure extractor returning the one
# top-level `- ` bullet whose text starts with <bullet-prefix>, up to (not
# including) the next top-level `- ` bullet or the next heading. Used to
# scope an assertion to a single routing bullet in `## Route Planner Output`,
# where `lean`/`interactive` recur in every bullet.
extract_bullet() {
  local content="$1" prefix="$2"
  awk -v prefix="${prefix}" '
    index($0, prefix) == 1 { on=1; print; next }
    on && /^- / { exit }
    on && /^#/ { exit }
    on { print }
  ' <<<"${content}"
}

# =====================================================================
# flow/skills/implement/phases/phase-1-plan.md
# =====================================================================

PHASE1_REL="skills/implement/phases/phase-1-plan.md"

RESOLVE_HEADING='## Resolve Planning Autonomy'

# --- The single resolution point (Fix step 1 + step 4) ---------------------
SINGLE_POINT_MARKER='Resolve `resolvedAutonomy` **exactly once per session**'
NO_REREAD_MARKER='a single resolution point, never a re-read per section'
# Step A -- the repo value, reusing the shipped fail-safe sentence verbatim
# (lean-planning-contract.test.sh owns this literal against this same file,
# so keeping it here proves the read moved rather than being dropped).
STEP_A_MARKER='anything other than the exact string `"lean"`, including a missing key or a missing `planning` block, is `interactive`'
# Step B -- the four-case resolution order, first match wins.
FIRST_MATCH_MARKER='The **first** matching case wins; later cases are not evaluated.'
CASE_1_MARKER='**`CENCI_ATTENDED=1`** in the environment → `attended` is **true**.'
CASE_1_NO_READ_MARKER='so Step B ends here: run no query and attempt no host config read'
CASE_2_MARKER='**`CENCI_ATTENDED=0`** in the environment → `attended` is **false**.'
CASE_2_NO_READ_MARKER='The launcher resolved the flag and it is off, so again run no query and attempt no host config read.'
CASE_2_DISPATCH_PIN_MARKER='Sessions `cenci dispatch` launches carry a pinned `CENCI_ATTENDED=0` regardless of the host flag'
CASE_3_MARKER='**`CENCI_ATTENDED` is absent** — a host run with no sandbox launcher to set it — ask the CLI'
CASE_3_CMD_MARKER='cenci planning attended status --json'
CASE_3_FIELD_MARKER='carries a top-level `"attended"` key whose value is the JSON boolean `true`'
CASE_4_MARKER='**`CENCI_ATTENDED` is set to anything else** — any value that is not exactly `1` or `0`, an empty string included → `attended` is **false**, and no query is run.'
CASE_4_RATIONALE_MARKER='An unrecognized value is not the same as absent'
# Delegation only -- never read the fleet config file directly.
DELEGATION_ONLY_MARKER='**Delegation only — never read `~/.config/cenci/config.json` (or `$XDG_CONFIG_HOME/cenci/config.json`) directly**'
# The status payload's other fields are not substitutes for `attended`.
AUTHORIZED_NOT_SUBSTITUTE_MARKER='`authorized` is *not* a substitute: it is dispatch'"'"'s own three-factor pickup verdict, which is `false` precisely **because** attended is on'
# Fail to the repo value (Fix step 3).
FAILSAFE_HEADING_MARKER='**Fail to the repo value.**'
FAILSAFE_TRIGGERS_MARKER='cannot be run, exits non-zero, prints nothing, prints unparseable JSON, omits `attended`, or returns an `attended` that is not a JSON boolean'
FAILSAFE_ONE_LINE_MARKER='print exactly one line before continuing: `` Attended check failed (<reason>) — using this repo'"'"'s `planning.autonomy` instead. ``'
FAILSAFE_NEVER_INTERACTIVE_MARKER='it never fails to `interactive` instead'
# Step C -- narrowing only (Fix step 2).
NARROWING_HEADING_MARKER='**Narrowing only.** Attended turns `lean` into `interactive` and nothing else'
NARROWING_NEVER_GRANTS_MARKER='it never turns an `interactive` repo into `lean`, and it grants no autonomy a repo has not itself committed'
NARROWING_TABLE_LEAN_ATTENDED_ROW='| `lean` | true | `interactive` |'
NARROWING_TABLE_LEAN_UNATTENDED_ROW='| `lean` | false | `lean` |'
NARROWING_TABLE_INTERACTIVE_ATTENDED_ROW='| `interactive` | true | `interactive` |'
OVERRIDE_FIRED_LINE_MARKER='Attended: this session plans interactively — the repo'"'"'s lean planning autonomy is suspended while a human is at the keyboard.'
NO_HARD_STOP_MARKER='This section performs no mutation, makes no `cenci pipeline` call, and has no hard stop of its own, so it contributes no row to `## Hard-Stop Inventory` above.'
# Consumers (Fix step 4) and the three explicit non-consumers.
CONSUMER_DELEGATION_MARKER='`## Planner Delegation`'"'"'s `Planning autonomy: lean | interactive` bullet below.'
CONSUMER_ROUTE_MARKER='`## Route Planner Output`'"'"'s question-routing bullets below.'
CONSUMER_LEAN_APPROVAL_MARKER='`## Lean Approval Path`'"'"'s entry conditions above.'
CONSUMER_UNATTENDED_MARKER='`## Unattended Escalation Path`'"'"'s Entry above.'
CONSUMER_SPLIT_GATE_MARKER='`### Split Gate`'"'"'s **Lean ticket mode** branch below.'
NONCONSUMER_TRIVIAL_MARKER='**`## Trivial Fast Path`** never consults autonomy in either mode'
NONCONSUMER_RESUME_MARKER='still finalizes to `approval: lean-resumed` even when the resuming session is attended'
NONCONSUMER_TICKETLESS_MARKER='**Ticketless mode**, which already behaves as interactive for any question the planner cannot self-answer'

if require_doc PHASE1 "${PHASE1_REL}"; then
  assert_contains "${PHASE1}" "${RESOLVE_HEADING}" \
    "phase-1-plan.md must add the ${RESOLVE_HEADING} section"

  RESOLVE_SECTION="$(extract_h2_section "${PHASE1}" "${RESOLVE_HEADING}")"
  if [[ -z "${RESOLVE_SECTION}" ]]; then
    fail "phase-1-plan.md: could not locate the '${RESOLVE_HEADING}' section (extract_h2_section returned empty)"
  else
    L="phase-1-plan.md (${RESOLVE_HEADING})"
    assert_contains_ws "${RESOLVE_SECTION}" "${SINGLE_POINT_MARKER}" "${L} must resolve exactly once per session"
    assert_contains_ws "${RESOLVE_SECTION}" "${NO_REREAD_MARKER}" "${L} must state it is a single resolution point, not a per-section re-read"
    assert_contains_ws "${RESOLVE_SECTION}" "${STEP_A_MARKER}" "${L} Step A must keep the exact-lean-only repo read"
    assert_contains_ws "${RESOLVE_SECTION}" "${FIRST_MATCH_MARKER}" "${L} Step B must state first-match-wins ordering"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_1_MARKER}" "${L} case 1: CENCI_ATTENDED=1 means attended"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_1_NO_READ_MARKER}" "${L} case 1 must attempt no host config read"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_2_MARKER}" "${L} case 2: CENCI_ATTENDED=0 means not attended"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_2_NO_READ_MARKER}" "${L} case 2 must attempt no host config read"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_2_DISPATCH_PIN_MARKER}" "${L} case 2 must name the dispatch-pinned CENCI_ATTENDED=0"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_3_MARKER}" "${L} case 3: an absent variable delegates to the CLI"
    assert_contains "${RESOLVE_SECTION}" "${CASE_3_CMD_MARKER}" "${L} case 3 must name the exact status command"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_3_FIELD_MARKER}" "${L} case 3 must read the JSON boolean attended field"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_4_MARKER}" "${L} case 4: an unrecognized value is not attended and runs no query"
    assert_contains_ws "${RESOLVE_SECTION}" "${CASE_4_RATIONALE_MARKER}" "${L} case 4 must distinguish unrecognized from absent"
    assert_contains_ws "${RESOLVE_SECTION}" "${DELEGATION_ONLY_MARKER}" "${L} must forbid reading the fleet config file directly"
    assert_contains_ws "${RESOLVE_SECTION}" "${AUTHORIZED_NOT_SUBSTITUTE_MARKER}" "${L} must forbid substituting the payload's authorized verdict for attended"
    assert_contains_ws "${RESOLVE_SECTION}" "${FAILSAFE_HEADING_MARKER}" "${L} must state the fail-to-the-repo-value rule"
    assert_contains_ws "${RESOLVE_SECTION}" "${FAILSAFE_TRIGGERS_MARKER}" "${L} must enumerate the query-failure triggers"
    assert_contains_ws "${RESOLVE_SECTION}" "${FAILSAFE_ONE_LINE_MARKER}" "${L} must print exactly one line on fallback"
    assert_contains_ws "${RESOLVE_SECTION}" "${FAILSAFE_NEVER_INTERACTIVE_MARKER}" "${L} must state the fallback is never interactive"
    assert_contains_ws "${RESOLVE_SECTION}" "${NARROWING_HEADING_MARKER}" "${L} Step C must state narrowing-only"
    assert_contains_ws "${RESOLVE_SECTION}" "${NARROWING_NEVER_GRANTS_MARKER}" "${L} Step C must state attended never grants lean"
    assert_contains "${RESOLVE_SECTION}" "${NARROWING_TABLE_LEAN_ATTENDED_ROW}" "${L} Step C table: lean + attended = interactive"
    assert_contains "${RESOLVE_SECTION}" "${NARROWING_TABLE_LEAN_UNATTENDED_ROW}" "${L} Step C table: lean + unattended = lean"
    assert_contains "${RESOLVE_SECTION}" "${NARROWING_TABLE_INTERACTIVE_ATTENDED_ROW}" "${L} Step C table: interactive + attended = interactive"
    assert_contains_ws "${RESOLVE_SECTION}" "${OVERRIDE_FIRED_LINE_MARKER}" "${L} must print one line when the override actually fires"
    assert_contains_ws "${RESOLVE_SECTION}" "${NO_HARD_STOP_MARKER}" "${L} must state it adds no Hard-Stop Inventory row"
    assert_contains_ws "${RESOLVE_SECTION}" "${CONSUMER_DELEGATION_MARKER}" "${L} consumer list must name the Planner Delegation bullet"
    assert_contains_ws "${RESOLVE_SECTION}" "${CONSUMER_ROUTE_MARKER}" "${L} consumer list must name Route Planner Output"
    assert_contains_ws "${RESOLVE_SECTION}" "${CONSUMER_LEAN_APPROVAL_MARKER}" "${L} consumer list must name the Lean Approval Path entry conditions"
    assert_contains_ws "${RESOLVE_SECTION}" "${CONSUMER_UNATTENDED_MARKER}" "${L} consumer list must name the Unattended Escalation Path entry"
    assert_contains_ws "${RESOLVE_SECTION}" "${CONSUMER_SPLIT_GATE_MARKER}" "${L} consumer list must name the Split Gate lean-ticket branch"
    assert_contains_ws "${RESOLVE_SECTION}" "${NONCONSUMER_TRIVIAL_MARKER}" "${L} must exempt the Trivial Fast Path"
    assert_contains_ws "${RESOLVE_SECTION}" "${NONCONSUMER_RESUME_MARKER}" "${L} must exempt ## Resume From Draft (approval: lean-resumed still applies)"
    assert_contains_ws "${RESOLVE_SECTION}" "${NONCONSUMER_TICKETLESS_MARKER}" "${L} must exempt ticketless mode"
  fi

  # --- Consumer 1: ## Planner Delegation ----------------------------------
  DELEGATION_SECTION="$(extract_h2_section "${PHASE1}" "## Planner Delegation")"
  if [[ -z "${DELEGATION_SECTION}" ]]; then
    fail "phase-1-plan.md: could not locate the '## Planner Delegation' section"
  else
    assert_contains "${DELEGATION_SECTION}" 'Planning autonomy: lean' \
      "phase-1-plan.md (## Planner Delegation) must keep the literal Planning autonomy: lean delegation value"
    assert_contains "${DELEGATION_SECTION}" 'Planning autonomy: interactive' \
      "phase-1-plan.md (## Planner Delegation) must keep the literal Planning autonomy: interactive delegation value"
    assert_contains_ws "${DELEGATION_SECTION}" '`resolvedAutonomy` from `## Resolve Planning Autonomy` above, verbatim' \
      "phase-1-plan.md (## Planner Delegation) must forward resolvedAutonomy rather than re-reading planning.autonomy"
    assert_contains_ws "${DELEGATION_SECTION}" 'an attended session in a `"lean"` repo states `Planning autonomy: interactive`, which is what stands `agents/planner.md`'"'"'s `## Self-Answer Policy` down' \
      "phase-1-plan.md (## Planner Delegation) must say the attended session stands the Self-Answer Policy down"
    # Non-vacuous absence: the delegation bullet must no longer derive the
    # value from the raw config key itself (that read now lives in Step A).
    assert_absent_paired_ws "${DELEGATION_SECTION}" \
      '`resolvedAutonomy` from `## Resolve Planning Autonomy` above, verbatim' \
      'resolved from the config'"'"'s top-level `planning.autonomy` key' \
      "phase-1-plan.md (## Planner Delegation) must not re-derive autonomy from the config key"
  fi

  # --- Consumer 2: ## Lean Approval Path entry conditions ------------------
  LEAN_SECTION="$(extract_h2_section "${PHASE1}" "## Lean Approval Path")"
  if [[ -z "${LEAN_SECTION}" ]]; then
    fail "phase-1-plan.md: could not locate the '## Lean Approval Path' section"
  else
    assert_contains_ws "${LEAN_SECTION}" '`resolvedAutonomy` is `lean` (see `## Resolve Planning Autonomy` below — an attended session'"'"'s `resolvedAutonomy` is `interactive`, so it never reaches this path)' \
      "phase-1-plan.md (## Lean Approval Path) entry conditions must gate on resolvedAutonomy, not planning.autonomy"
  fi

  # --- Consumer 3: ## Unattended Escalation Path Entry ---------------------
  UNATTENDED_SECTION="$(extract_h2_section "${PHASE1}" "## Unattended Escalation Path")"
  if [[ -z "${UNATTENDED_SECTION}" ]]; then
    fail "phase-1-plan.md: could not locate the '## Unattended Escalation Path' section"
  else
    assert_contains_ws "${UNATTENDED_SECTION}" 'whenever `resolvedAutonomy` is `lean` (see `## Resolve Planning Autonomy` below) and this is ticket mode' \
      "phase-1-plan.md (## Unattended Escalation Path) Entry must gate on resolvedAutonomy"
    # AC1, stated at the path it protects: an attended session posts no
    # comment, applies no label, and writes no awaiting-input draft.
    assert_contains_ws "${UNATTENDED_SECTION}" 'An attended session never enters this path: the override already resolved `resolvedAutonomy` to `interactive`, so its questions go to `AskUserQuestion` instead — no ticket comment is posted, the `Input Needed` label is not applied, and no `status: awaiting-input` draft is written.' \
      "phase-1-plan.md (## Unattended Escalation Path) Entry must state the attended session's no-comment/no-label/no-draft outcome"
  fi

  # --- Consumers 4 + 5: ## Route Planner Output routing bullets ------------
  ROUTE_SECTION="$(extract_h2_section "${PHASE1}" "## Route Planner Output")"
  if [[ -z "${ROUTE_SECTION}" ]]; then
    fail "phase-1-plan.md: could not locate the '## Route Planner Output' section"
  else
    assert_contains_ws "${ROUTE_SECTION}" '**Lean mode, ticket mode** (`resolvedAutonomy` is `lean` — the repo'"'"'s `planning.autonomy` is exactly `"lean"` and no attended override narrowed it — **and** this is ticket mode)' \
      "phase-1-plan.md (## Route Planner Output) questions-exist bullet must gate on resolvedAutonomy while keeping the exact-lean repo condition"
    # AC2: lean repo + attended + no questions -> ## New Plan, approval: human,
    # stop for plan review. Scoped to the no-questions bullet.
    NO_QUESTIONS_BULLET="$(extract_bullet "${ROUTE_SECTION}" '- If no questions (or none remain):')"
    if [[ -z "${NO_QUESTIONS_BULLET}" ]]; then
      fail "phase-1-plan.md (## Route Planner Output): could not locate the no-questions routing bullet"
    else
      assert_contains_ws "${NO_QUESTIONS_BULLET}" 'An attended session in a `"lean"` repo lands here: the plan is persisted via `## New Plan`, its front matter records `approval: human`, and the session stops for plan review — it never takes `## Lean Approval Path` and never writes `approval: lean`.' \
        "phase-1-plan.md (## Route Planner Output, no-questions bullet) must route an attended lean session to ## New Plan with approval: human"
    fi
    assert_contains_ws "${ROUTE_SECTION}" '**Lean mode (`resolvedAutonomy` is `lean`), `## Clarifying Questions` is `None`, and `escalated` was never set this session**' \
      "phase-1-plan.md (## Route Planner Output) Lean-Approval routing bullet must gate on resolvedAutonomy"
    assert_contains_ws "${ROUTE_SECTION}" '**Lean ticket mode** (`resolvedAutonomy` is `lean` and this is ticket mode)' \
      "phase-1-plan.md (### Split Gate) lean-ticket branch must gate on resolvedAutonomy"
  fi

  # --- ## Persist the Plan: the approval table's `human` row ---------------
  PERSIST_SECTION="$(extract_h2_section "${PHASE1}" "## Persist the Plan")"
  if [[ -z "${PERSIST_SECTION}" ]]; then
    fail "phase-1-plan.md: could not locate the '## Persist the Plan' section"
  else
    assert_contains_ws "${PERSIST_SECTION}" 'including an attended session in a `"lean"` repo, where the attended override suspended lean planning for the session' \
      "phase-1-plan.md (## Persist the Plan) approval table's human row must cover the attended lean session"
  fi
fi

# =====================================================================
# flow/skills/implement/SKILL.md — Cost Controls restatement
# =====================================================================

SKILL_REL="skills/implement/SKILL.md"
SKILL_RESOLVED_MARKER='The value Phase 1 acts on is `resolvedAutonomy`, resolved once in `phases/phase-1-plan.md`'"'"'s `## Resolve Planning Autonomy` from this key **and** the fleet-wide attended override'
SKILL_NARROWING_MARKER='an attended session narrows `"lean"` to `"interactive"` for that session only, never the reverse'

if require_doc IMPLEMENT_SKILL "${SKILL_REL}"; then
  assert_contains_ws "${IMPLEMENT_SKILL}" "${SKILL_RESOLVED_MARKER}" \
    "implement/SKILL.md planning.autonomy bullet must point at the single resolution point"
  assert_contains_ws "${IMPLEMENT_SKILL}" "${SKILL_NARROWING_MARKER}" \
    "implement/SKILL.md planning.autonomy bullet must state the narrowing-only invariant"
fi

# =====================================================================
# docs/autonomous-loop.md — the doc-of-record for the autonomy switches
# =====================================================================

LOOP_REL="docs/autonomous-loop.md"
LOOP_OVERRIDE_HEADING='#### The attended override'
LOOP_NOT_A_SWITCH_MARKER='This is an override, not a sixth switch: it only ever suspends switch 1, never grants it.'
LOOP_ASKED_MARKER='a clarifying question is asked directly instead of being posted to the ticket — no `Input Needed` label, no `awaiting-input` draft, and no waiting for the next dispatch pass'
LOOP_APPROVAL_HUMAN_MARKER='a plan with no escalations is **not** self-approved: it is saved, the session stops for you to read it, and its front matter records `approval: human`, not `approval: lean`'
LOOP_SANDBOX_MARKER='the flag is forwarded at exec time as `CENCI_ATTENDED=1` or `CENCI_ATTENDED=0` — always explicitly, never "unset means off"'
LOOP_SANDBOX_REBUILD_MARKER='Toggling it on the host takes effect on the next `cenci open`, with no container rebuild.'
LOOP_DISPATCH_PIN_MARKER='Sessions `cenci dispatch` launches are pinned to `CENCI_ATTENDED=0` whatever the host flag says'
LOOP_ORDER_HEADING_MARKER='**The resolution order** a planning session follows, first match wins:'
LOOP_ORDER_1_MARKER='1. `CENCI_ATTENDED=1` → attended; plan interactively.'
LOOP_ORDER_2_MARKER='2. `CENCI_ATTENDED=0` → use the repo'"'"'s `planning.autonomy` as-is.'
LOOP_ORDER_3_MARKER='3. Variable absent (an ordinary host run) → `cenci planning attended status --json`, read its `attended` field.'
LOOP_ORDER_4_MARKER='4. Anything else, including a failed or unparseable query → use the repo'"'"'s `planning.autonomy`, and say so in one line.'
LOOP_NEVER_DIRECT_MARKER='Nothing reads `~/.config/cenci/config.json` directly — the flag is only ever resolved through the `cenci` binary or the forwarded variable.'
# Switch-table row 5 must describe both directions the override reaches, so
# the table no longer contradicts the new subsection.
LOOP_TABLE_ROW5_MARKER='and a planning session you launch yourself in a `"lean"` repo plans *interactively* — it asks you instead of posting the question to the ticket, and stops for plan review instead of self-approving'
# The Input Needed off-ramp bullet must not still read as unconditional.
LOOP_OFFRAMP_MARKER='unless attended mode is on for this machine, in which case planning asks you directly instead'
# The Turning-it-off row must name both effects.
LOOP_TURNOFF_MARKER='Next dispatch pass; and the next planning session you launch'

if require_repo_doc LOOP_DOC "${LOOP_REL}"; then
  assert_contains "${LOOP_DOC}" "${LOOP_OVERRIDE_HEADING}" \
    "docs/autonomous-loop.md must add the ${LOOP_OVERRIDE_HEADING} subsection"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_NOT_A_SWITCH_MARKER}" \
    "docs/autonomous-loop.md must describe attended as an override on switch 1, not a new switch"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_ASKED_MARKER}" \
    "docs/autonomous-loop.md must state the no-comment/no-label/no-draft attended behavior"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_APPROVAL_HUMAN_MARKER}" \
    "docs/autonomous-loop.md must state the attended plan stops for review and records approval: human"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_SANDBOX_MARKER}" \
    "docs/autonomous-loop.md must document the CENCI_ATTENDED sandbox forward"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_SANDBOX_REBUILD_MARKER}" \
    "docs/autonomous-loop.md must state a host toggle needs no container rebuild"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_DISPATCH_PIN_MARKER}" \
    "docs/autonomous-loop.md must state dispatch-launched sessions are pinned to CENCI_ATTENDED=0"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_ORDER_HEADING_MARKER}" \
    "docs/autonomous-loop.md must document the resolution order"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_ORDER_1_MARKER}" "docs/autonomous-loop.md resolution order step 1"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_ORDER_2_MARKER}" "docs/autonomous-loop.md resolution order step 2"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_ORDER_3_MARKER}" "docs/autonomous-loop.md resolution order step 3"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_ORDER_4_MARKER}" "docs/autonomous-loop.md resolution order step 4"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_NEVER_DIRECT_MARKER}" \
    "docs/autonomous-loop.md must state the fleet config is never read directly"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_TABLE_ROW5_MARKER}" \
    "docs/autonomous-loop.md switch-table row 5 must also describe the in-session planning effect"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_OFFRAMP_MARKER}" \
    "docs/autonomous-loop.md Input Needed off-ramp must note the attended exception"
  assert_contains_ws "${LOOP_DOC}" "${LOOP_TURNOFF_MARKER}" \
    "docs/autonomous-loop.md Turning it off row must name both effects of cenci planning attended on"
  # No contradiction left: the old row-5 wording that scoped the switch to
  # dispatch pickups alone must be gone, checked non-vacuously against the
  # new subsection's own existence.
  assert_absent_paired_ws "${LOOP_DOC}" "${LOOP_NOT_A_SWITCH_MARKER}" \
    'The **inverse of switch 2, per machine**: suppresses unattended planning pickups/re-plans for lean repos on this machine specifically' \
    "docs/autonomous-loop.md switch-table row 5 must no longer describe the flag as dispatch-only"
fi

echo "attended-autonomy-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
