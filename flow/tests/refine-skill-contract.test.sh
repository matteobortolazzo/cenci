#!/usr/bin/env bash
# Contract test for ticket #650 — migrate refine's split child tickets to
# native GitHub sub-issues.
#
# Why this exists: the parent→child enumeration used to live in a `### Child
# Tickets` markdown checklist appended to the parent body (refine/SKILL.md
# Pass 2), read back by context-gatherer.md. GitHub's first-class sub-issue
# feature (gh `--parent` / `--add-sub-issue`, reads via `--json parent,
# subIssues,subIssuesSummary`) is now the source of truth. This test pins the
# new behavior down so a future edit can't quietly re-introduce the checklist
# or drop the native linking.
#
# Follows the idiom of flow/tests/refiner-agent-contract.test.sh: a
# `failures=` counter, small assert_* helpers, exact substring markers (never
# generic keywords — see docs/shell-scripting-gotchas.md), self-contained,
# auto-discovered by the flow gate's `*.test.sh` glob. It greps the real
# committed docs directly; no fixtures.
#
# Covered files:
#   - skills/refine/SKILL.md
#   - agents/context-gatherer.md
#   - skills/refine/codex.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-skill-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-skill-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc() {
  # read_doc <flow-relative-path> — prints the real committed file's content,
  # or fails closed with a distinct "not found" message (a missing file must
  # never masquerade as empty content, which would make assert_not_contains
  # trivially pass).
  local path="${FLOW_DIR}/$1"
  local content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "$1: doc not found/unreadable: ${path}"
    printf ''
    return 1
  fi
  printf '%s' "${content}"
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
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden stale text still present: [${pattern}]"
}

# --- skills/refine/SKILL.md — native sub-issue linking replaces the checklist ---

skill="$(read_doc "skills/refine/SKILL.md")" || true
if [[ -n "${skill}" ]]; then
  # Pass 1 links each child as a native sub-issue of the parent. Accept either
  # gh spelling of the primitive (child-side `--parent` or parent-side
  # `--add-sub-issue`); require at least one.
  if [[ "${skill}" != *"--parent"* && "${skill}" != *"--add-sub-issue"* ]]; then
    fail "skills/refine/SKILL.md: no native sub-issue link primitive (--parent or --add-sub-issue) found"
  fi
  # Verification reads the native sub-issue graph from the parent side.
  assert_contains "${skill}" "--json subIssues" "skills/refine/SKILL.md"
  assert_contains "${skill}" ".subIssues.nodes" "skills/refine/SKILL.md"

  # The old markdown checklist is gone — no `### Child Tickets` section, and no
  # `- [ ] #` checkbox enumeration of children in the parent body.
  assert_not_contains "${skill}" "### Child Tickets" "skills/refine/SKILL.md"
  assert_not_contains "${skill}" "- [ ] #" "skills/refine/SKILL.md"

  # Ordering, when it exists, is prose under `### Execution Order`, not a checklist.
  assert_contains "${skill}" "### Execution Order" "skills/refine/SKILL.md"

  # The child-body backlinks are retained (hierarchy != dependency ordering).
  assert_contains "${skill}" "Related to #" "skills/refine/SKILL.md"
  assert_contains "${skill}" "Depends on #" "skills/refine/SKILL.md"
fi

# --- agents/context-gatherer.md — detection via the native sub-issue graph ---

gatherer="$(read_doc "agents/context-gatherer.md")" || true
if [[ -n "${gatherer}" ]]; then
  # parentId primary source is the native parent field.
  assert_contains "${gatherer}" "--json parent" "agents/context-gatherer.md"
  # siblings primary source is the native subIssues node list.
  assert_contains "${gatherer}" "--json subIssues" "agents/context-gatherer.md"
  assert_contains "${gatherer}" ".subIssues.nodes" "agents/context-gatherer.md"
  # The stale checklist-parsing instruction is gone.
  assert_not_contains "${gatherer}" "### Child Tickets" "agents/context-gatherer.md"
  # The `Related to #` search fallback is retained.
  assert_contains "${gatherer}" "Related to #" "agents/context-gatherer.md"
fi

# --- skills/refine/codex.md — native behavior is portable ---

codex="$(read_doc "skills/refine/codex.md")" || true
if [[ -n "${codex}" ]]; then
  assert_contains "${codex}" "sub-issue" "skills/refine/codex.md"
  assert_contains "${codex}" "--parent" "skills/refine/codex.md"
  assert_not_contains "${codex}" "### Child Tickets" "skills/refine/codex.md"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "refine-skill-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "refine-skill-contract.test.sh: all checks passed."
