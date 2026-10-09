#!/usr/bin/env bash
# The workflow is prose; pin its execution boundaries and shared rebase helper.
set -euo pipefail
TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW_DIR="$(cd "$TEST_DIR/.." && pwd)" || exit 2
for PROCEDURE in skills/implement/codex.md skills/implement/phases/phase-9-pr.md skills/merge-repair/SKILL.md; do
  FILE="$FLOW_DIR/$PROCEDURE"
  [[ -s "$FILE" ]] || { echo "missing procedure: $PROCEDURE" >&2; exit 1; }
  grep -qF '/hooks/scripts/rebase-remote-base.sh"' "$FILE"
  grep -qF 'REBASE_STATUS=ready' "$FILE"
  grep -qF -- '--force-with-lease=refs/heads/' "$FILE"
done
SKILL="$FLOW_DIR/skills/merge-repair/SKILL.md"
grep -qxF 'model: opus' "$SKILL"
grep -qF -- '--force-with-lease=refs/heads/<head-branch>:<expected-head-sha>' "$SKILL"
grep -qF 'to `<abs-worktree-path>`:' "$SKILL"
grep -qF '/hooks/scripts/run-gate.sh" "<slug>"' "$SKILL"
grep -qF '.plans/.merge-repair-<pr>.json' "$SKILL"
grep -qF 'recovery record before rebasing' "$SKILL"
grep -qF 'phase: prepared' "$SKILL"
grep -qF 'phase: verified' "$SKILL"
grep -qF 'Never use bare force' "$SKILL"
echo 'merge-repair workflow contracts passed'
