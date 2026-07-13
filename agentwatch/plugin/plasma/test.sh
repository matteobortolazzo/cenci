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
# NOTE: this now also picks up the headroom threshold bands ("normal",
# "warning", "critical") returned by headroomClass in status.go, alongside the
# session-status classes ("failed", "running", ...) — the extraction is
# greedy and can't tell the two namespaces apart, so main.qml must have a
# `case "..."` for every class from both switches.
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

# check_widget asserts a pattern is present in main.qml, printing an ok/FAIL
# line and setting $fail on a miss.
check_widget() {
    local pattern="$1" ok_msg="$2" fail_msg="$3"
    if grep -qE "$pattern" "$WIDGET"; then
        echo "ok   - $ok_msg"
    else
        echo "FAIL - $fail_msg"
        fail=1
    fi
}

# main.qml must parse the `headroom` map from the poll JSON (mirrors the JXA
# script reading `data.headroom`).
check_widget 'headroom\s*=\s*j\.headroom' \
    "main.qml reads the headroom field from poll JSON" \
    "main.qml does not read the headroom field from poll JSON"

# The Go-appended headroom summary line must be excluded from tooltipLines by
# EXACT match only (see the narrow-skip lesson in .claude/rules/lessons-learned.md)
# so unrelated lines still fall through to the visible 'none' fallback.
check_widget 'headroomSummaryLine' \
    "main.qml derives a headroomSummaryLine" \
    "main.qml has no headroomSummaryLine derivation"

check_widget '===\s*summary\b' \
    "main.qml skips the summary line by exact match (=== summary)" \
    "main.qml does not exact-match-skip the summary line"

# Hide/show is gated on `alt`, not `class` (status.go can emit `class: "none"`
# with `alt: "dispatch-only"` when zero sessions but the fleet dispatch loop is
# enabled — the panel must stay visible then). main.qml must read `alt` from
# the poll JSON and derive hasOutput from it, not from cssClass alone.
check_widget 'cssAlt\s*=\s*j\["alt"\]' \
    "main.qml reads the alt field from poll JSON" \
    "main.qml does not read the alt field from poll JSON"

check_widget 'hasOutput\s*=\s*root\.cssAlt\s*!==\s*"none"' \
    "main.qml derives hasOutput from cssAlt (not cssClass)" \
    "main.qml does not gate hasOutput on cssAlt"

if grep -qE 'hasOutput\s*=\s*root\.cssClass\s*!==\s*"none"' "$WIDGET"; then
    echo "FAIL - main.qml still gates hasOutput on cssClass (dispatch-only would be hidden)"
    fail=1
else
    echo "ok   - main.qml does not gate hasOutput on cssClass"
fi

echo
if [ "$fail" -eq 0 ]; then
    echo "PASS"
else
    echo "FAIL"
fi
exit $fail
