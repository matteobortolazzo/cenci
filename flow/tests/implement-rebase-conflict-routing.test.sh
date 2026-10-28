#!/usr/bin/env bash
# Tests documentation-contract coverage for Phase 9's rebase-conflict
# handling: a conflict is resolved inside the run by an Opus delegate, and
# only a genuine product decision leaves the run -- routed to the CLI or the
# ticket by `resolvedAutonomy`, never a bare "report and stop".
#
# Background (the bug this pins): a lean run stopped at Phase 9 on a
# docs-only conflict (two dated amendments in one section) and handed the
# merge back to the human with no PR, because the pre-approval preamble
# listed "rebase conflicts" as a stop exception and the product-decision
# branch only said "report that concrete ambiguity and stop".
#
# Fixture-free, grep-based idiom of
# flow/tests/implement-maintenance-fail-gate.test.sh: markers kept on a
# single source line (docs/shell-scripting-gotchas.md rule 13), section
# extraction for scoped assertions, auto-discovered by scripts/run-checks.sh.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-rebase-conflict-routing.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-rebase-conflict-routing.test.sh: failed to resolve flow directory." >&2; exit 2; }

PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
PHASE9_PR="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
CODEX_MD="${FLOW_DIR}/skills/implement/codex.md"
ATTRIBUTION_DOC="${FLOW_DIR}/docs/comment-attribution.md"

failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

assert_contains() {
  # $1=haystack $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -n "$1" ]] || { fail "$3: empty haystack"; return; }
  grep -qF -- "$2" <<<"$1" || fail "$3 (expected to contain: $2)"
}

assert_lacks() {
  # $1=haystack $2=needle $3=description -- an empty haystack never passes.
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -n "$1" ]] || { fail "$3: empty haystack"; return; }
  grep -qF -- "$2" <<<"$1" && fail "$3 (expected to NOT contain: $2)"
  return 0
}

extract_section() {
  # $1=file $2=exact `## ` heading -- prints the heading's body up to the
  # next `## ` heading. Prints nothing when the file or heading is missing.
  [[ -f "$1" ]] || return 0
  awk -v h="$2" '$0 == h {on=1; next} on && /^## / {exit} on {print}' "$1"
}

require_file() {
  # $1=var name $2=file
  [[ -f "$2" ]] || { fail "file not found: $2"; printf -v "$1" '%s' ""; return 1; }
  printf -v "$1" '%s' "$(cat "$2")"
}

require_file PHASE9_CONTENT "${PHASE9_PR}"
CONFLICTS="$(extract_section "${PHASE9_PR}" '## Rebase Conflicts')"
REBASE="$(extract_section "${PHASE9_PR}" '## Rebase')"

# 1. A conflict by itself is never a pre-approval stop.
assert_lacks "${PHASE9_CONTENT}" 'defined below (rebase conflicts, build/test/lint' \
  "phase-9-pr.md preamble must not list every rebase conflict as a stop exception"
assert_contains "${PHASE9_CONTENT}" 'A rebase conflict by itself is never a stop' \
  "phase-9-pr.md preamble must say a conflict alone never stops the run"
assert_lacks "${REBASE}" 'report that concrete ambiguity and stop' \
  "## Rebase must drop the bare report-and-stop product-decision branch"
assert_contains "${REBASE}" 'go to `## Rebase Conflicts` below' \
  "## Rebase must route conflicts to ## Rebase Conflicts"

# 2. Mechanical conflicts, docs overlaps included, are kept on both sides.
assert_contains "${CONFLICTS}" 'or amendments in one section, lists both sides appended to, renames, moves,' \
  "## Rebase Conflicts must classify dated docs amendments as mechanical"
assert_contains "${CONFLICTS}" 'landed on main is not a stop either' \
  "## Rebase Conflicts must not stop on scope overlap with upstream"

# 3. Resolution is always delegated to Opus, with a verdict line.
assert_contains "${CONFLICTS}" 'to the `cenci:implementer` agent with `model: "opus"` in this same' \
  "## Rebase Conflicts must delegate to an Opus implementer"
assert_contains "${CONFLICTS}" '`Conflict verdict: resolved` or `Conflict verdict: product-decision`.' \
  "## Rebase Conflicts delegate must report a closed-set verdict"
assert_contains "${CONFLICTS}" 'never by blanket ours/theirs selection' \
  "## Rebase Conflicts must forbid blanket ours/theirs"

# 4. Product decisions route by resolvedAutonomy: CLI or ticket comment.
assert_contains "${CONFLICTS}" 'question by `resolvedAutonomy`' \
  "## Rebase Conflicts must route the question by resolvedAutonomy"
assert_contains "${CONFLICTS}" '**`interactive`, or ticketless mode** — ask via `AskUserQuestion`' \
  "## Rebase Conflicts interactive/ticketless branch must ask in the CLI"
assert_contains "${CONFLICTS}" 'delegate as the decision. The run continues.' \
  "## Rebase Conflicts interactive branch must continue the run after the answer"
assert_contains "${CONFLICTS}" '**`lean`, ticket mode** — post the question on the ticket and stop.' \
  "## Rebase Conflicts lean ticket branch must post to the ticket"
assert_contains "${CONFLICTS}" '`cenci pipeline label <id> --transition input-needed`' \
  "## Rebase Conflicts lean ticket branch must swap to Input Needed"
assert_contains "${CONFLICTS}" '> 🤖 **cenci** — rebase-conflict question posted by `/cenci:implement` (Phase 9).' \
  "## Rebase Conflicts comment must open with the attribution banner"
assert_contains "${CONFLICTS}" 'line, and `<!-- cenci-rebase-conflict -->` on its own non-blockquoted' \
  "## Rebase Conflicts comment must carry its non-blockquoted marker"
assert_contains "${CONFLICTS}" 'takes precedence over posting the options verbatim' \
  "## Rebase Conflicts must state secrecy precedence over verbatim posting"

# 5. The single resolution point and the other client surfaces agree.
require_file PHASE1_CONTENT "${PHASE1_PLAN}"
assert_contains "${PHASE1_CONTENT}" '`phases/phase-9-pr.md`'"'"'s `## Rebase Conflicts` step 3' \
  "phase-1-plan.md Consumers must list Phase 9's conflict routing"
require_file CODEX_CONTENT "${CODEX_MD}"
assert_contains "${CODEX_CONTENT}" 'autonomy or ticketless mode asks through the client'"'"'s available user-input' \
  "codex.md must carry the portable interactive branch"
assert_contains "${CODEX_CONTENT}" '`<!-- cenci-rebase-conflict -->` question comment, runs' \
  "codex.md must carry the lean ticket branch"
assert_lacks "${CODEX_CONTENT}" 'AskUserQuestion' \
  "codex.md must not name the Claude-only AskUserQuestion tool"
require_file ATTR_CONTENT "${ATTRIBUTION_DOC}"
assert_contains "${ATTR_CONTENT}" '| `rebase-conflict` | `skills/implement/phases/phase-9-pr.md`' \
  "comment-attribution.md registry must record the rebase-conflict kind"

echo "implement-rebase-conflict-routing.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
