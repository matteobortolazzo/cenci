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
for toml in "$FLOW"/templates/codex/agents/*.toml; do
  grep -Eq '^description = "[^"]+"' "$toml"
  expected_name="$(basename "$toml" .toml)"
  grep -Eq "^name = \"$expected_name\"\$" "$toml"
done
REPO_ROOT="$(cd "$FLOW/.." && pwd)"
for toml in "$REPO_ROOT"/.codex/agents/*.toml; do
  expected_name="$(basename "$toml" .toml)"
  grep -Eq "^name = \"$expected_name\"\$" "$toml"
done
printf 'user-owned\n' > "$ROOT/.codex/agents/planner.toml"
PLUGIN_ROOT="$FLOW" sh "$FLOW/codex/install-agents.sh" "$ROOT"
grep -q 'user-owned' "$ROOT/.codex/agents/planner.toml"
node "$FLOW/codex/checkpoint.mjs" clear "$path"
test ! -e "$path"
echo "runtime.test.sh: passed"
