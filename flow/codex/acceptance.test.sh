#!/usr/bin/env bash
set -euo pipefail
FLOW="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

bash "$FLOW/codex/hooks.test.sh"
bash "$FLOW/codex/workflows.test.sh"
bash "$FLOW/codex/runtime.test.sh"
bash "$FLOW/scripts/migrate-project-core.test.sh"

for workflow in configure refine implement review address-review refactor sync maintain babysit design; do
  test -f "$FLOW/skills/$workflow/SKILL.md"
done

jq -e '.hooks.PreToolUse and .hooks.SessionStart and .hooks.PreCompact and .hooks.Stop' "$FLOW/codex/hooks.json" >/dev/null
echo "Codex-only workflow acceptance contracts: passed"
