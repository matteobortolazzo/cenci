#!/usr/bin/env bash
# dispatch-target-invocable.test.sh — contract test for ticket #1155:
# "babysit's ci-repair/babysit-attention dispatch fails: both skills are
# user-invocable: false".
#
# `cenci run` (and therefore `cenci babysit`) dispatches work by launching the
# agent CLI with a slash/`$`-prefixed skill invocation built from the workflow
# name in watch/internal/run/template.go's builtinConfig(). A skill marked
# `user-invocable: false` is never exposed under that invocation shape, so the
# launched session silently receives an unknown command: the supervisor sees a
# clean process spawn, advances its retry counters, and no repair happens.
# That is exactly what #1155 observed for ci-repair on PR #1154.
#
# This test closes the gap by cross-checking the two sides against each other
# rather than restating either: it parses the workflow names out of
# template.go's real committed builtinConfig() and asserts that every one of
# them that is a flow skill (flow/skills/<name>/SKILL.md exists) does not
# carry `user-invocable: false`. Workflow names with no flow skill (a
# Codex-only adapter, or a future non-skill workflow) are reported as skipped,
# never silently dropped.
#
# Follows the fixture-free idiom of babysit-host-supervisor-contract.test.sh
# (REPO_ROOT resolved from FLOW_DIR/.., a `failures=` counter, failing closed
# on an unreadable source) — it greps the real committed sources, never a copy.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "dispatch-target-invocable.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "dispatch-target-invocable.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "dispatch-target-invocable.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

TEMPLATE="${REPO_ROOT}/watch/internal/run/template.go"

# =====================================================================
# 1. Extract the dispatch targets from template.go's builtinConfig().
#
# Every built-in workflow entry is written as `"<name>": <agent>WF("<name>")`
# (claudeWF/codexWF/openCodeWF). The helper-call argument is the token that
# actually becomes the invoked skill name, so that is what is parsed — the map
# key is only a lookup label. An empty parse fails closed: a silent zero-target
# sweep would let this test pass vacuously after a refactor of template.go.
# =====================================================================
if [[ ! -r "${TEMPLATE}" ]]; then
  fail "watch/internal/run/template.go: file not found/unreadable (dispatch targets cannot be verified)"
  echo "dispatch-target-invocable.test.sh: failures=${failures}"
  exit 1
fi

# Recognized per-agent template helpers. A future agent added via a helper
# not listed here would otherwise have its dispatch targets silently skipped
# by the extraction below, so the set is asserted rather than assumed: any
# `<word>WF(...)`-shaped call with an unrecognized prefix fails loudly.
KNOWN_HELPERS='claudeWF codexWF openCodeWF'
mapfile -t SEEN_HELPERS < <(grep -oE '\b[A-Za-z]+WF\("[a-z-]+"\)' "${TEMPLATE}" \
  | sed -E 's/^([A-Za-z]+WF)\(.*/\1/' | sort -u)
for helper in "${SEEN_HELPERS[@]:-}"; do
  [[ -n "${helper}" ]] || continue
  [[ " ${KNOWN_HELPERS} " == *" ${helper} "* ]] || fail "watch/internal/run/template.go: unrecognized workflow-template helper [${helper}()] — add it to this test's KNOWN_HELPERS so its dispatch targets are checked too (a new agent's targets must not slip past unchecked)"
done

mapfile -t TARGETS < <(grep -oE '\b[A-Za-z]+WF\("[a-z-]+"\)' "${TEMPLATE}" \
  | sed -E 's/.*\("([a-z-]+)"\)/\1/' | sort -u)

if [[ "${#TARGETS[@]}" -eq 0 ]]; then
  fail "watch/internal/run/template.go: no <agent>WF(\"<name>\") dispatch targets parsed — the built-in workflow shape changed and this test needs updating (failing closed rather than passing vacuously)"
fi

# Sanity floor: the two launcher-driven targets #1155 is about must be among
# the parsed set, so a parser that silently narrows can never hide them.
for required in ci-repair babysit-attention; do
  found=0
  for t in "${TARGETS[@]:-}"; do
    [[ "${t}" == "${required}" ]] && found=1
  done
  [[ "${found}" -eq 1 ]] || fail "watch/internal/run/template.go: expected dispatch target [${required}] not found among parsed targets [${TARGETS[*]:-}]"
done

# =====================================================================
# 2. Every parsed target that is a flow skill must be user-invocable.
# =====================================================================
skipped=()
for wf in "${TARGETS[@]:-}"; do
  [[ -n "${wf}" ]] || continue
  skill="${FLOW_DIR}/skills/${wf}/SKILL.md"
  if [[ ! -r "${skill}" ]]; then
    skipped+=("${wf}")
    continue
  fi
  # Frontmatter field only: the check is anchored to a bare `user-invocable:`
  # line so a mention of the field inside prose or a fenced example can
  # neither satisfy nor trip it.
  if grep -qE '^user-invocable:[[:space:]]*false[[:space:]]*$' "${skill}"; then
    fail "flow/skills/${wf}/SKILL.md: 'user-invocable: false' but the skill is dispatched as a slash/\$-prefixed invocation by watch/internal/run/template.go — the launched session cannot call it (#1155)"
  fi
done

if [[ "${#skipped[@]}" -gt 0 ]]; then
  echo "dispatch-target-invocable.test.sh: note — dispatch targets with no flow skill (not checked): ${skipped[*]}"
fi

echo "dispatch-target-invocable.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
