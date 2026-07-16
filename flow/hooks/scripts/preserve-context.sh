#!/bin/sh
# PreCompact hook: output a brief reminder of where on-demand reference docs live,
# so the post-compaction agent knows to re-read them when needed instead of guessing.

echo "=== CONTEXT PRESERVATION (PreCompact) ==="

if test -d docs; then
  echo ""
  echo "## Reference Docs"
  echo "Topic-specific reference docs live under docs/. Re-read the file matching your work area on demand."
  ls docs/*.md 2>/dev/null | sed 's|^|  - |'
fi

if test -f .cenci/config.json; then
  echo ""
  echo "## Active Config"
  echo "Config exists at .cenci/config.json — re-read it for ticket/PR system settings."
elif test -f .claude/config.json; then
  echo ""
  echo "## Active Config (legacy)"
  echo "Legacy config exists at .claude/config.json — read it for compatibility, then migrate with cenci:configure."
fi

# Backward compat: note any legacy lessons file so the agent knows to consult it.
if test -f .claude/rules/lessons-learned.md; then
  echo ""
  echo "## Legacy Lessons File"
  echo "A legacy .claude/rules/lessons-learned.md exists — read it on demand for the work area; new lessons go to docs/ or AGENTS.md."
fi

echo ""
echo "=== END CONTEXT PRESERVATION ==="
