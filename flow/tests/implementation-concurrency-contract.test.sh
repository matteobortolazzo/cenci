#!/usr/bin/env bash
# Agent procedures implement these behaviors; assert dispatch sites and their gates.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 2
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || exit 2
failures=0
contains() {
  [[ -n "$2" && -f "$1" ]] || { echo "FAIL: missing file/needle: $1"; failures=$((failures+1)); return; }
  if ! grep -qF -- "$2" "$1"; then
    echo "FAIL: $1: $2"
    failures=$((failures+1))
  fi
}
contains_between() {
  local content
  content="$(awk -v start="$2" -v end="$3" '
    {
      line=$0; sub(/^[[:space:]]+/, "", line)
      if (start ~ /^#/ ? line == start : index(line, start) == 1) active=1
      if (active && (end ~ /^#/ ? line == end : index(line, end) == 1)) exit
      if (active) print
    }
  ' "$1")" || { echo "FAIL: cannot read $1"; failures=$((failures+1)); return; }
  if [[ -z "$4" || "$content" != *"$4"* ]]; then
    echo "FAIL: $1 section $2: $4"
    failures=$((failures+1))
  fi
}
PLANNER="${FLOW_DIR}/agents/planner.md"
PHASE3="${FLOW_DIR}/skills/implement/phases/phase-3-test-red.md"
PHASE4="${FLOW_DIR}/skills/implement/phases/phase-4-implement-green.md"
CODEX="${FLOW_DIR}/skills/implement/codex.md"

# Explicit decision, dependency order and exclusive ownership in the plan.
for phrase in '### Parallelism Assessment (required)' 'Decision: parallel | sequential' 'concrete sequential reason' 'shared foundation in an earlier wave' 'read-only in later waves' '- Wave: <positive integer' '- Depends on: <earlier-wave lane IDs or None>' 'test commands must not race'; do
  contains "$PLANNER" "$phrase"
done
if grep -qF -- 'optional — omit by default' "$PLANNER"; then
  echo 'FAIL: planner still silently omits concurrency assessment'
  failures=$((failures+1))
fi

# Both clients consume the configuration at their implementation dispatch sites.
for file in "$PHASE3" "$CODEX"; do
  for phrase in 'cenci.compactImplementation' 'exactly one delegated lane' 'cenci.implementerConcurrency' 'cenci.implementerMaxConcurrency' 'default 2' 'positive integer' 'available worker slots' '### Parallelism Assessment' 'Wave:' 'isUiTicket' 'sensitive-path' 'red-before-green' 'Lane Verification Barrier'; do
    contains "$file" "$phrase"
  done
done
for phrase in 'missing Wave fields mean wave 1' 'dependency on the same or a later wave' 'only after every lane in the current wave is green' 'stop launching queued lanes and later waves' 'Launch at most the resolved cap' 'Never run the full build or full test suite' 'On resume, re-run completed lanes' 'follow Phase 4’s Visual Verification'; do
  contains "$PHASE3" "$phrase"
done
for phrase in 'Read Phase 3’s `## Parallel Lanes`' 'native `implementer` subagents' 'Phase 4’s `## Visual Verification`'; do
  contains "$CODEX" "$phrase"
done
contains "$PHASE4" 'parallel or sequential lanes, including dependency waves'
contains "$PHASE4" 'then run `## Visual Verification` for a UI ticket'

# Scope the decisive checks to the execution sites; unrelated mentions cannot pass.
for phrase in 'default false' 'small (S under' 'Run full build, tests and configured lint' 'Phase 5' 'Visual Verification'; do
  contains_between "$PHASE3" '## Compact Implementation' '## Parallel Lanes' "$phrase"
done
# Compact step 6 is the single Completed-Change Verification (#1103), not an extra full group.
contains_between "$PHASE3" '## Compact Implementation' '## Parallel Lanes' 'This step is its Completed-Change Verification'
for phrase in 'a malformed preflight: remain read-only and return to planning' 'Root-retained actions are excluded' 'dependency on the same or a later wave' 'sequential decision/mode further limits it to 1' 'stop and report the capability blocker' 'Launch at most the resolved cap' 'only after every lane in the current wave is green' 'stop launching queued lanes and later waves' 'On resume, re-run completed lanes'; do
  contains_between "$PHASE3" '## Parallel Lanes' '## Delegation Context' "$phrase"
done
for phrase in 'Resolve `isUiTicket` using the `frontend-classification`' 'Recompute it on every apply' 'never defaults to false' 'Use this resolved flag for both fan-out eligibility' 'native `implementer` subagents' 'cenci.implementerMaxConcurrency' 'Read Phase 3’s `## Parallel Lanes`' 'read-only for later consumers' 'Phase 4’s `## Visual Verification`'; do
  contains_between "$CODEX" '**Implementation dispatch (apply only).**' 'Then, before the reviews' "$phrase"
done
for phrase in 'Scope | Wave | Depends on' 'even for a sequential' 'Owned files maps to `Files:`, Verification to `Tests:`'; do
  contains_between "$PLANNER" '### Delegation Preflight' '### Parallelism Assessment' "$phrase"
done
contains_between "$PLANNER" '### Parallelism Assessment (required)' '### Parallel Lanes (when eligible)' 'concrete sequential reason'
contains_between "$PHASE3" '## Delegation Preflight Re-check' '## Context Budget' 'before choosing any execution mode'
for phrase in 'complete Delegation Preflight schema' 'verification, Scope, Wave and Depends on' 'required `### Parallelism Assessment`' 'never omit the assessment on a fresh'; do
  contains_between "$CODEX" 'The plan also carries' 'Then return an approved plan' "$phrase"
done
contains_between "$CODEX" 'Read `project-core`' 'An open native blocking dependency' 'Before delegating the planner, use `frontend-classification`'
contains_between "$CODEX" 'Read `project-core`' 'An open native blocking dependency' 'resolve `isUiTicket` and pass that flag to the planner'

# configure's `cenci` schema lives in references/config-schema.md since #1344.
for file in "${FLOW_DIR}/skills/implement/SKILL.md" "${FLOW_DIR}/skills/configure/references/config-schema.md" "${FLOW_DIR}/skills/configure/codex.md" "${FLOW_DIR}/README.md"; do
  contains "$file" 'implementerMaxConcurrency'
done
contains "${FLOW_DIR}/skills/maintain/scripts/check.sh" 'cenci.implementerMaxConcurrency|number'
if ! jq -e '.cenci.compactImplementation == true and .cenci.implementerMaxConcurrency == 2' "${REPO_DIR}/.cenci/config.json" >/dev/null; then
  echo 'FAIL: this repo must opt in to compact implementation with a cap of two'
  failures=$((failures+1))
fi
printf 'implementation concurrency contracts: failures=%s\n' "$failures"
[[ "$failures" -eq 0 ]]
