#!/bin/bash
# Check for approved plan files and inject as context on session start.
PLANS_DIR=".plans"

if [ ! -d "$PLANS_DIR" ]; then
  exit 0
fi

# Find plan files
PLANS=$(find "$PLANS_DIR" -name "*.md" -type f 2>/dev/null)

if [ -z "$PLANS" ]; then
  exit 0
fi

# Count plans
COUNT=$(echo "$PLANS" | wc -l | tr -d ' ')

if [ "$COUNT" -eq 1 ]; then
  PLAN_FILE=$(echo "$PLANS" | head -1)
  FILENAME=$(basename "$PLAN_FILE")
  cat <<EOF
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "Pending implementation plan found: $FILENAME\nResume implementation by invoking: /agentflow:implement $PLAN_FILE"
  }
}
EOF
elif [ "$COUNT" -gt 1 ]; then
  FILE_LIST=$(echo "$PLANS" | while read f; do basename "$f"; done | tr '\n' ', ' | sed 's/, $//')
  cat <<EOF
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "Multiple pending plans found: $FILE_LIST\nAsk the user which plan to resume, then invoke: /agentflow:implement .plans/<filename>"
  }
}
EOF
fi

exit 0
