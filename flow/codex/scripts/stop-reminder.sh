#!/bin/sh
set -eu
jq -n --arg message 'Capture only specific non-obvious lessons during implementation. Shared guidance belongs in AGENTS.md; otherwise propose the lesson without editing the main worktree.' '{systemMessage:$message}'
