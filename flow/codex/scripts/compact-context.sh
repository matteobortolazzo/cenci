#!/bin/sh
set -eu
context=$("${PLUGIN_ROOT}/hooks/scripts/preserve-context.sh")
printf '%s' "$context" | jq -Rs '{systemMessage:.}'
