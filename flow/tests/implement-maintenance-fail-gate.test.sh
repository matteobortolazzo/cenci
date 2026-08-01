#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #892: the implement
# pipeline must treat a maintain-checker `fail` as a hard gate, not as
# advice.
#
# Background (the bug this pins): the pipeline runs
# `flow/skills/maintain/scripts/check.sh --changed <files>` twice against the
# same tree with two different verdicts. Phase 8 filed a `fail` under its
# **Report-only default** (no halt); CI's `flow-maintenance` job runs the
# identical command and exits 1. Phase 9's hard gates were build/test/lint
# only, so the pipeline detected the exact condition that would redden CI and
# pushed anyway -- PRs #888 and #890 both landed red on nothing but
# `flow-maintenance`.
#
# The contract asserted here:
#   - a `fail` routes to must-stop-and-ask; report-only is narrowed to
#     `warn`/`skip`
#   - an "Allowed (auto-repair)" finding whose re-run does not clear is
#     re-classified by status, never blanket-downgraded to report-only
#   - `maintenance.checkDuringImplement: false` remains a full opt-out
#   - Phase 9 refuses to commit/push/PR on an unresolved `fail`, and renders
#     an explicit override honestly (never as a pass)
#   - codex.md carries the portable equivalent, without naming
#     `AskUserQuestion`
#
# Fixture-free, grep-based idiom of flow/tests/implement-split-gate-contract.test.sh
# and flow/tests/lean-planning-contract.test.sh: `set -uo pipefail`, a
# `failures` counter, `assert_file_contains`/`assert_file_lacks` helpers built
# on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md rule 13), auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob -- no registration needed. No
# `read_*`-named helpers, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's repo-wide scan (the
# extractor below is named `extract_*`, its wrapper `require_*`).
#
# Marker precision (docs/shell-scripting-gotchas.md rule 3): the anti-pattern
# assertions below target the *exact* superseded sentence fragments
# ("downgrade it to report-only", "any finding that is neither clearly
# Allowed"), not a generic keyword -- a bare "report-only" negative would
# false-trigger on the `checkDuringImplement: false` opt-out prose that must
# survive, and a bare "fail" positive would pass vacuously against the
# unfixed file.
#
# Covered files:
#   - flow/skills/implement/phases/phase-8-docs.md (## Maintenance Check's
#     CI-parity rule, the three rewritten repair-policy bullets, the
#     fail-question option set, the extended status-tag vocabulary)
#   - flow/skills/implement/phases/phase-9-pr.md (pre-approval exception,
#     ## Maintenance Gate, the `overridden` checklist rendering)
#   - flow/skills/implement/codex.md (portable equivalent)
#   - flow/README.md, docs/getting-started.md (checkDuringImplement now also
#     disables a blocking gate -- users must be told)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-maintenance-fail-gate.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-maintenance-fail-gate.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "implement-maintenance-fail-gate.test.sh: failed to resolve repo root." >&2; exit 2; }

PHASE8_DOCS="${FLOW_DIR}/skills/implement/phases/phase-8-docs.md"
PHASE9_PR="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
CODEX_MD="${FLOW_DIR}/skills/implement/codex.md"
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
  # $1=file $2=needle $3=description -- the [[ -f ]] guard is mandatory: a
  # "must NOT contain" claim about a nonexistent file is meaningless and must
  # never pass (docs/shell-scripting-gotchas.md rule 37).
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}

assert_contains() {
  # $1=haystack $2=needle $3=description -- for section-scoped assertions,
  # where the marker must appear inside a specific extracted section rather
  # than anywhere in the file.
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" == *"$2"* ]] || fail "$3 (expected section to contain: $2)"
}

# extract_section <content> <exact-heading-line> -- pure, fence-aware
# extractor: returns the body of the named "## <heading>" section through the
# next real (unfenced) "## "-level heading, or EOF. A "## "-shaped line inside
# a fenced code block (phase-9-pr.md's ## PR holds a PR-body markdown
# template full of literal "## " lines) is never a section boundary
# (docs/shell-scripting-gotchas.md rule 38). No fail() side effect, so this is
# safe inside $(...) (rule 39).
extract_section() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# require_section <result-var> <file> <heading-line> <label> -- nameref
# wrapper: assigns the extracted section body, or fails closed and assigns ""
# on an unreadable file or missing section (never masquerading as an
# empty-but-present one). Must NOT be invoked via $(...).
require_section() {
  local -n _res="$1"
  local _content _body
  if ! _content="$(cat "$2" 2>/dev/null)"; then
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

# --- Phase 8: CI-parity rule -------------------------------------------------
# The rule that makes the whole gate derivable rather than arbitrary: CI runs
# the identical `--changed` invocation, and `--changed` already downgrades
# breaches unrelated to this PR's own paths to `warn`, so a surviving `fail`
# is by construction one CI will also raise.

maintenance_section=""
require_section maintenance_section "${PHASE8_DOCS}" '## Maintenance Check' 'Phase 8 Maintenance Check section'

assert_contains "${maintenance_section}" '**CI-parity rule — a checker `fail` is a hard gate, a `warn` is not**' \
  'Phase 8 declares the CI-parity rule'
assert_contains "${maintenance_section}" 'every `fail` that survives here is one CI will raise too, so pushing it is a guaranteed red pipeline' \
  'Phase 8 states why a surviving fail is CI-blocking'
assert_contains "${maintenance_section}" '`warn` and `skip` are not CI-blocking and stay advisory' \
  'Phase 8 keeps warn/skip advisory'

# --- Phase 8: repair-policy decision flow ------------------------------------

assert_contains "${maintenance_section}" '**Must stop and ask (do not auto-repair)**: **any result whose status is `fail`**' \
  'Phase 8 routes every fail to must-stop-and-ask'
assert_contains "${maintenance_section}" '**Report-only default**: any `warn` or `skip` finding that is neither clearly Allowed nor clearly a must-stop case' \
  'Phase 8 narrows report-only to warn/skip'

# Anti-pattern: the superseded report-only wording covered *any* finding,
# which is exactly what let a `fail` through. Match the exact fragment, not a
# bare "report-only" (which legitimately survives in the opt-out prose).
assert_file_lacks "${PHASE8_DOCS}" 'any finding that is neither clearly Allowed nor clearly a must-stop case' \
  'no longer files any finding under report-only'

# The auto-repair branch's non-clearing path must re-classify by status
# instead of blanket-downgrading a still-failing finding to report-only.
assert_contains "${maintenance_section}" 're-classify it by its status: a surviving `fail` routes to the must-stop-and-ask branch below, a surviving `warn` to the report-only default' \
  'Phase 8 re-classifies a non-clearing auto-repair by status'
assert_file_lacks "${PHASE8_DOCS}" 'downgrade it to report-only' \
  'no longer blanket-downgrades a non-clearing auto-repair'

# --- Phase 8: the fail question's option set ---------------------------------
# Ordering is pinned by keeping all three labels on one source line, so a
# single grep -F proves both membership and order.

assert_contains "${maintenance_section}" 'offer exactly these options in this order: **Fix now**, **Stop**, **Push anyway**.' \
  'Phase 8 pins the fail-question option set and ordering'
assert_contains "${maintenance_section}" 'AskUserQuestion' \
  'Phase 8 routes the fail question through AskUserQuestion'
assert_contains "${maintenance_section}" 'State the checker'"'"'s exact `FAIL <check> <target>: <message>` line and its `-> fix:` hint' \
  'Phase 8 requires the exact checker line in the question'

# --- Phase 8: status-tag vocabulary ------------------------------------------

assert_contains "${maintenance_section}" '<repaired|reported|halted|overridden>' \
  'Phase 8 extends the status-tag vocabulary with overridden'
assert_contains "${maintenance_section}" 'a "Push anyway" override tags the status file `overridden`; a "Stop" choice tags it `halted` and ends the run at Phase 8' \
  'Phase 8 maps each fail-question choice to a status tag'

# --- Phase 8: checkDuringImplement opt-out survives --------------------------
# The escape hatch must keep working exactly as before -- everything
# report-only, no halt -- and must say so about `fail` explicitly, since that
# is the status whose handling changed.

assert_file_contains "${PHASE8_DOCS}" 'all non-pass results are report-only, including any `fail`' \
  'Phase 8 keeps checkDuringImplement:false a full opt-out covering fail'
assert_file_contains "${PHASE8_DOCS}" '`maintenance.checkDuringImplement` is explicitly `false`' \
  'Phase 8 retains the checkDuringImplement config branch'

# --- Phase 9: pre-approval exception and pre-push gate -----------------------

assert_file_contains "${PHASE9_PR}" 'an unresolved maintenance `fail`' \
  'Phase 9 names the maintenance fail among its pre-approval exceptions'

gate_section=""
require_section gate_section "${PHASE9_PR}" '## Maintenance Gate' 'Phase 9 Maintenance Gate section'

assert_contains "${gate_section}" 'If the tag is `halted`, stop before Commit: do not commit, do not push, and do not create the PR.' \
  'Phase 9 blocks commit/push/PR on a halted maintenance status'
assert_contains "${gate_section}" 'Run this gate before the Commit step' \
  'Phase 9 orders the maintenance gate ahead of Commit'
assert_contains "${gate_section}" '`overridden`' \
  'Phase 9 gate accounts for the explicit override tag'

# --- Phase 9: honest checklist rendering -------------------------------------

assert_file_contains "${PHASE9_PR}" '- [ ] Maintenance check: FAILING (user-approved red pipeline — see Notes)' \
  'Phase 9 renders an overridden fail as an unchecked FAILING line'
assert_file_contains "${PHASE9_PR}" 'summary line shows `— overridden`' \
  'Phase 9 has a rendering rule keyed on the overridden tag'
assert_file_contains "${PHASE9_PR}" 'never render this as a pass' \
  'Phase 9 forbids rendering a non-pass maintenance status as a pass'

# --- codex.md: portable equivalent -------------------------------------------

assert_file_contains "${CODEX_MD}" 'A checker `fail` is CI-blocking: gate on it through the client'"'"'s available user-input mechanism' \
  'codex.md carries the portable fail gate'
assert_file_contains "${CODEX_MD}" 'never push an unresolved `fail`' \
  'codex.md forbids pushing an unresolved fail'
assert_file_lacks "${CODEX_MD}" 'AskUserQuestion' \
  'codex.md stays client-portable (no AskUserQuestion)'

# --- User-facing docs: checkDuringImplement now also disables a gate ---------

assert_file_contains "${FLOW_README}" 'including CI-blocking `fail` results, which disables the Phase 9 push gate' \
  'flow/README.md documents the widened meaning of checkDuringImplement:false'
assert_file_contains "${GETTING_STARTED}" 'including CI-blocking `fail` results, which disables the Phase 9 push gate' \
  'docs/getting-started.md documents the widened meaning of checkDuringImplement:false'

if [[ "${failures}" -gt 0 ]]; then
  echo "implement-maintenance-fail-gate.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "implement-maintenance-fail-gate.test.sh: all assertions passed"
