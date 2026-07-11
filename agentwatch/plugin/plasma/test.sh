#!/usr/bin/env bash
#
# Dependency-free drift test for the Plasma plasmoid.
#
# The plasmoid can't run headlessly (it needs a Plasma shell), so this asserts
# the one thing that silently rots: every `class` the Go formatter can emit must
# be handled by contents/ui/main.qml. Adding a status to
# internal/frontend/status/status.go fails this until the widget maps it.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
STATUS_GO="$DIR/../../internal/frontend/status/status.go"
WIDGET="$DIR/contents/ui/main.qml"

# Every quoted class the formatter returns, minus "none" (which means hidden).
classes=$(grep -oE 'return "[a-z-]+"' "$STATUS_GO" \
    | sed -E 's/return "([a-z-]+)"/\1/' | sort -u | grep -v '^none$')

if [ -z "$classes" ]; then
    echo "FAIL - could not extract classes from $STATUS_GO"
    exit 1
fi

fail=0
for c in $classes; do
    if grep -qE "case \"${c}\"" "$WIDGET"; then
        echo "ok   - main.qml handles class '$c'"
    else
        echo "FAIL - class '$c' not handled in main.qml"
        fail=1
    fi
done

echo
if [ "$fail" -eq 0 ]; then
    echo "PASS"
else
    echo "FAIL"
fi
exit $fail
