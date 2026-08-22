#!/usr/bin/env bash
set -euo pipefail
FLOW="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(mktemp -d)"; trap 'rm -rf "$ROOT"' EXIT
path="$ROOT/.cenci/checkpoints/implement-42.json"
node "$FLOW/codex/checkpoint.mjs" init "$path" implement 42 planned >/dev/null
node "$FLOW/codex/checkpoint.mjs" advance "$path" implement 42 worktree >/dev/null
jq -e '.schemaVersion == 1 and .phase == "worktree" and .status == "running"' "$path" >/dev/null
node "$FLOW/codex/checkpoint.mjs" block "$path" implement 42 review >/dev/null
jq -e '.status == "needs-input"' "$path" >/dev/null
PLUGIN_ROOT="$FLOW" sh "$FLOW/codex/install-agents.sh" "$ROOT"
test "$(find "$ROOT/.codex/agents" -name '*.toml' | wc -l)" -ge 5
# Schema validation (#1040): each directory validated on its own, not
# combined — .codex/agents/ is populated by copying the templates verbatim,
# so the same role `name`s legitimately appear in both; combining them would
# make every clean checkout report a false-positive duplicate.
bash "$FLOW/codex/validate-agent-roles.sh" "$FLOW/templates/codex/agent-roles"
REPO_ROOT="$(cd "$FLOW/.." && pwd)"
bash "$FLOW/codex/validate-agent-roles.sh" "$REPO_ROOT/.codex/agents"
printf 'user-owned\n' > "$ROOT/.codex/agents/planner.toml"
PLUGIN_ROOT="$FLOW" sh "$FLOW/codex/install-agents.sh" "$ROOT"
grep -q 'user-owned' "$ROOT/.codex/agents/planner.toml"
node "$FLOW/codex/checkpoint.mjs" clear "$path"
test ! -e "$path"
echo "runtime.test.sh: passed"
