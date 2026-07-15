#!/bin/sh
# Verify cenci is configured. The presence of .claude/config.json — created by
# /cenci:configure — is the canonical signal. Reference docs live under docs/
# (on-demand) and are not required for this check.
test -f .claude/config.json || echo 'WARNING: .claude/config.json not found. Run /cenci:configure to set up.'
