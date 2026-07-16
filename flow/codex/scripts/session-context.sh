#!/bin/sh
set -eu
context=$("${PLUGIN_ROOT}/hooks/scripts/check-lessons-file.sh")
if [ -z "$context" ]; then printf '{}\n'; exit 0; fi
printf '%s' "$context" | jq -Rs '{hookSpecificOutput:{hookEventName:"SessionStart",additionalContext:.}}'
