#!/usr/bin/env bash
#
# Dependency-free drift test for the noctalia-shell bar widget.
#
# The widget can't be exercised headlessly (it needs a running noctalia-shell
# / Quickshell), so this asserts the two things that silently rot:
#   1. every session-status `class` the Go formatter's highestClass() can emit
#      is handled by BarWidget.qml's icon/color mapping
#   2. Main.qml gates visibility on `alt`, not `class` (status.go can emit
#      `class: "none"` with `alt: "dispatch-only"` when zero sessions but the
#      fleet dispatch loop is enabled — the widget must stay visible then)
#
# Mirrors the pattern used by plugin/gnome/test.sh and plugin/plasma/test.sh.
# Scoped to highestClass()'s classes only (not headroomClass's threshold
# bands) since BarWidget.qml renders headroom via a separate percent-badge
# path (colorForHeadroom), not iconForClass/colorForClass.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
STATUS_GO="$DIR/../../internal/frontend/status/status.go"
BAR_WIDGET="$DIR/BarWidget.qml"
MAIN_QML="$DIR/Main.qml"

# Every quoted class highestClass() returns, minus "none" (which means hidden).
classes=$(sed -n '/^func highestClass/,/^}/p' "$STATUS_GO" \
    | grep -oE 'return "[a-z-]+"' | sed -E 's/return "([a-z-]+)"/\1/' | sort -u | grep -v '^none$')

if [ -z "$classes" ]; then
    echo "FAIL - could not extract classes from highestClass() in $STATUS_GO"
    exit 1
fi

fail=0
for c in $classes; do
    if grep -qE "case \"${c}\"" "$BAR_WIDGET"; then
        echo "ok   - BarWidget.qml handles class '$c'"
    else
        echo "FAIL - class '$c' not handled in BarWidget.qml"
        fail=1
    fi
done

echo

# Hide/show is gated on `alt`, not `class`. Main.qml must read `alt` from the
# poll JSON and derive hasOutput from it, not from cssClass alone.
if grep -qE 'cssAlt\s*=\s*j\["alt"\]' "$MAIN_QML"; then
    echo "ok   - Main.qml reads the alt field from poll JSON"
else
    echo "FAIL - Main.qml does not read the alt field from poll JSON"
    fail=1
fi

if grep -qE 'hasOutput\s*=\s*root\.cssAlt\s*!==\s*"none"' "$MAIN_QML"; then
    echo "ok   - Main.qml derives hasOutput from cssAlt (not cssClass)"
else
    echo "FAIL - Main.qml does not gate hasOutput on cssAlt"
    fail=1
fi

if grep -qE 'hasOutput\s*=\s*root\.cssClass\s*!==\s*"none"' "$MAIN_QML"; then
    echo "FAIL - Main.qml still gates hasOutput on cssClass (dispatch-only would be hidden)"
    fail=1
else
    echo "ok   - Main.qml does not gate hasOutput on cssClass"
fi

echo
if [ "$fail" -eq 0 ]; then
    echo "PASS"
else
    echo "FAIL"
fi
exit $fail
