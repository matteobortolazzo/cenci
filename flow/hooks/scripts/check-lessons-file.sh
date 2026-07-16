#!/bin/sh
# Verify cenci is configured. .cenci/config.json is canonical and the legacy
# .claude/config.json remains a read-only fallback. Reference docs live under docs/
# (on-demand) and are not required for this check.
test -f .cenci/config.json || test -f .claude/config.json || echo 'WARNING: .cenci/config.json not found. Run cenci:configure to set up.'
