#!/bin/bash
# Check for pending plan files and inject as context on session start.
PLANS_DIR=".plans"

if [ ! -d "$PLANS_DIR" ]; then
  exit 0
fi

# Find plan files
PLANS=$(find "$PLANS_DIR" -maxdepth 1 -name "*.md" -type f 2>/dev/null)

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
    "additionalContext": "Pending implementation plan found: $FILENAME\nIf the user invokes /cenci:implement with an explicit ticket number or plan file, honor that argument and do not ask about unrelated pending plans. Otherwise, offer to resume by invoking: /cenci:implement $PLAN_FILE"
  }
}
EOF
elif [ "$COUNT" -gt 1 ]; then
  FILE_LIST=$(echo "$PLANS" | while read f; do basename "$f"; done | tr '\n' ', ' | sed 's/, $//')
  cat <<EOF
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "Multiple pending plans found: $FILE_LIST\nIf the user invokes /cenci:implement with an explicit ticket number or plan file, honor that argument and ignore every unrelated pending plan; ticket mode may only auto-detect .plans/<ticket-number>-*.md. Do not ask the user to choose among unrelated plans. If no explicit implement target was provided, ask which plan to resume using the AskUserQuestion tool, then invoke: /cenci:implement .plans/<filename>"
  }
}
EOF
fi

exit 0
