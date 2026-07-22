#!/usr/bin/env bash
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failures=0
fail(){ echo "FAIL: $1" >&2; failures=$((failures+1)); }
for workflow in configure refine implement review address-review refactor sync maintain design; do
  skill="${ROOT}/skills/${workflow}/SKILL.md"
  procedure="${ROOT}/skills/${workflow}/codex.md"
  grep -q 'Client dispatch.*Codex' "$skill" || fail "$workflow dispatcher"
  test -s "$procedure" || fail "$workflow Codex procedure"
done
runtime="${ROOT}/skills/codex-runtime/SKILL.md"
grep -q 'Require.*`/plan`\|must begin in `/plan`' "$runtime" || fail "Plan-mode contract"
grep -q '.cenci/checkpoints' "$runtime" || fail "checkpoint contract"
grep -q 'Arm a native Codex goal only after' "$runtime" || fail "goal arm contract"
grep -q 'Clear it before human input' "$runtime" || fail "goal clear contract"
grep -q 'built-in' "$runtime" && grep -q 'same bounded role prompt' "$runtime" || fail "agent fallback contract"
count="$(find "${ROOT}/templates/codex/agents" -name '*.toml' | wc -l)"
test "$count" -ge 5 || fail "Codex agent templates"
echo "workflows.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
