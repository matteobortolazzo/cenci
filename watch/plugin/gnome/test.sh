#!/usr/bin/env bash
#
# Dependency-free drift test for the GNOME Shell extension.
#
# The extension can't be exercised headlessly (it needs a running GNOME Shell),
# so this asserts the one thing that silently rots: every `class` the Go
# formatter can emit must be handled by extension.js. If someone adds a status
# to internal/frontend/status/status.go, this fails until the widget maps it.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
STATUS_GO="$DIR/../../internal/frontend/status/status.go"
WIDGET="$DIR/extension.js"

# Every quoted class the formatter returns, minus "none" (which means hidden).
classes=$(grep -oE 'return "[a-z-]+"' "$STATUS_GO" \
    | sed -E 's/return "([a-z-]+)"/\1/' | sort -u | grep -v '^none$')

if [ -z "$classes" ]; then
    echo "FAIL - could not extract classes from $STATUS_GO"
    exit 1
fi

fail=0
for c in $classes; do
    if grep -qE "['\"]${c}['\"]" "$WIDGET"; then
        echo "ok   - extension.js handles class '$c'"
    else
        echo "FAIL - class '$c' not handled in extension.js"
        fail=1
    fi
done

echo

# Hide/show is gated on `alt`, not `class` (status.go can emit `class: "none"`
# with `alt: "dispatch-only"` when zero sessions but the fleet dispatch loop
# is enabled — the module must stay visible then). Assert extension.js reads
# `alt` from the poll JSON and hides on `alt === 'none'`, not on `cls ===
# 'none'` (a regression to the old class-only gate would re-hide dispatch-only
# output).
if grep -qE "alt\s*=\s*j\['alt'\]" "$WIDGET" && grep -qE "alt\s*===\s*'none'" "$WIDGET"; then
    echo "ok   - extension.js hides on alt === 'none' (not class === 'none')"
else
    echo "FAIL - extension.js does not gate hide/show on 'alt'"
    fail=1
fi

if grep -qE "cls\s*===\s*'none'" "$WIDGET"; then
    echo "FAIL - extension.js still hides on cls === 'none' (dispatch-only would be hidden)"
    fail=1
else
    echo "ok   - extension.js does not hide on cls === 'none'"
fi

echo

# Headroom rows are colored via dedicated .agentwatch-headroom-<class>
# selectors (extension.js builds the class name dynamically via a template
# literal, so it can't be grepped as a quoted string like the status classes
# above — assert the stylesheet side directly instead). A JS class reference
# with no matching stylesheet rule would otherwise silently render with no
# color.
STYLESHEET="$DIR/stylesheet.css"
headroom_classes="normal warning critical"

for c in $headroom_classes; do
    if grep -qE '\.agentwatch-headroom-'"$c"'([^a-z-]|$)' "$STYLESHEET"; then
        echo "ok   - stylesheet.css defines .agentwatch-headroom-$c"
    else
        echo "FAIL - stylesheet.css missing .agentwatch-headroom-$c selector"
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
