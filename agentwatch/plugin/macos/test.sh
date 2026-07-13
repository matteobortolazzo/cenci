#!/usr/bin/env bash
#
# Dependency-free smoke test for the SwiftBar plugin. Points AGENTWATCH_BIN at a
# stub that prints canned `agentwatch status` JSON and asserts the SwiftBar output.
# macOS-only: the plugin formats via JXA (/usr/bin/osascript).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
PLUGIN="$DIR/agentwatch.5s.sh"

if ! command -v osascript >/dev/null 2>&1; then
  echo "SKIP - osascript not found (macOS-only test)"
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0

check() {
  local name="$1" haystack="$2" needle="$3"
  if printf '%s' "$haystack" | grep -qF -- "$needle"; then
    echo "ok   - $name"
    pass=$((pass + 1))
  else
    echo "FAIL - $name"
    echo "         wanted substring: $needle"
    echo "         in output:"
    printf '%s\n' "$haystack" | sed 's/^/           /'
    fail=$((fail + 1))
  fi
}

check_empty() {
  local name="$1" haystack="$2"
  if [ -z "$haystack" ]; then
    echo "ok   - $name"
    pass=$((pass + 1))
  else
    echo "FAIL - $name (expected empty output)"
    printf '%s\n' "$haystack" | sed 's/^/           /'
    fail=$((fail + 1))
  fi
}

check_not_empty() {
  local name="$1" haystack="$2"
  if [ -n "$haystack" ]; then
    echo "ok   - $name"
    pass=$((pass + 1))
  else
    echo "FAIL - $name (expected non-empty output)"
    fail=$((fail + 1))
  fi
}

# check_not <name> <haystack> <needle-that-must-be-absent>
check_not() {
  local name="$1" haystack="$2" needle="$3"
  if printf '%s' "$haystack" | grep -qF -- "$needle"; then
    echo "FAIL - $name (expected substring absent: $needle)"
    printf '%s\n' "$haystack" | sed 's/^/           /'
    fail=$((fail + 1))
  else
    echo "ok   - $name"
    pass=$((pass + 1))
  fi
}

# check_order <name> <haystack> <first-substring> <second-substring>
# Asserts <first-substring> appears on an earlier line than <second-substring>.
check_order() {
  local name="$1" haystack="$2" first="$3" second="$4"
  local n1 n2
  n1="$(printf '%s\n' "$haystack" | grep -nF -- "$first" | head -1 | cut -d: -f1)"
  n2="$(printf '%s\n' "$haystack" | grep -nF -- "$second" | head -1 | cut -d: -f1)"
  if [ -n "$n1" ] && [ -n "$n2" ] && [ "$n1" -lt "$n2" ]; then
    echo "ok   - $name"
    pass=$((pass + 1))
  else
    echo "FAIL - $name (expected '$first' before '$second')"
    printf '%s\n' "$haystack" | sed 's/^/           /'
    fail=$((fail + 1))
  fi
}

# run_plugin <fixture-file-or-empty> <exit-code>
# Builds a stub that prints the fixture (if any) and exits with the given code,
# then runs the plugin with AGENTWATCH_BIN pointed at it.
run_plugin() {
  local fixture="$1" code="${2:-0}"
  cat > "$TMP/stub" <<STUB
#!/usr/bin/env bash
[ -f "$fixture" ] && cat "$fixture"
exit $code
STUB
  chmod +x "$TMP/stub"
  AGENTWATCH_BIN="$TMP/stub" "$PLUGIN"
}

# --- Case 1: need-input present -------------------------------------------
cat > "$TMP/need-input.json" <<'JSON'
{"text":"▶ 1  ! 1","tooltip":"work:1 - build (running)\nwork:2 - deploy (need-input)","class":"need-input","alt":"active"}
JSON
out="$(run_plugin "$TMP/need-input.json" 0)"
check "menu bar line loud on need-input" "$out" "sfimage=exclamationmark.triangle.fill sfcolor=red"
check "need-input row is red/alert"      "$out" "work:2 - deploy | sfimage=exclamationmark.triangle.fill sfcolor=red"
check "running row is blue/brain"         "$out" "work:1 - build | sfimage=brain.head.profile.fill sfcolor=blue"

# --- Case 2a: daemon down / no output -> hidden ---------------------------
out="$(run_plugin "" 1)"
check_empty "hidden when status exits non-zero with no output" "$out"

# --- Case 2b: class=none -> hidden ----------------------------------------
cat > "$TMP/none.json" <<'JSON'
{"text":"","tooltip":"no agent sessions","class":"none","alt":"none"}
JSON
out="$(run_plugin "$TMP/none.json" 0)"
check_empty "hidden when class is none" "$out"

# --- Case 3: running + done mix (incl. paneless row) ----------------------
cat > "$TMP/mix.json" <<'JSON'
{"text":"▶ 1  ✓ 1","tooltip":"work:1 - build (running)\nsolo (done)","class":"running","alt":"active"}
JSON
out="$(run_plugin "$TMP/mix.json" 0)"
check "menu bar line tinted by highest class (running)" "$out" "▶ 1  ✓ 1 | sfimage=brain.head.profile.fill sfcolor=blue"
check "running row is blue/brain"                        "$out" "work:1 - build | sfimage=brain.head.profile.fill sfcolor=blue"
check "done paneless row is green/check"                "$out" "solo | sfimage=checkmark.circle.fill sfcolor=green"

# --- Case 4: budget headroom present, multiple agents, mixed thresholds ---
#
# claude=73% (>25% -> normal/green), codex=15% (10-25% inclusive -> warning/
# orange), gpt=5% (<10% -> critical/red). Keys are intentionally out of
# alphabetical order in the fixture to assert the plugin sorts by agent name
# rather than relying on JS object key iteration order.
cat > "$TMP/headroom-mixed.json" <<'JSON'
{"text":"▶ 1  claude 73%  codex 15%  gpt 5%","tooltip":"work:1 - build (running)\nclaude 73%  codex 15%  gpt 5%","class":"running","alt":"active","headroom":{"gpt":0.05,"claude":0.73,"codex":0.15}}
JSON
out="$(run_plugin "$TMP/headroom-mixed.json" 0)"
check "normal-band headroom row (>25%) is green"   "$out" "claude 73% | sfcolor=green"
check "warning-band headroom row (10-25%) is orange" "$out" "codex 15% | sfcolor=orange"
check "critical-band headroom row (<10%) is red"    "$out" "gpt 5% | sfcolor=red"
check_order "headroom rows sorted by agent name (claude before codex)" "$out" "claude 73% | sfcolor=green" "codex 15% | sfcolor=orange"
check_order "headroom rows sorted by agent name (codex before gpt)"    "$out" "codex 15% | sfcolor=orange" "gpt 5% | sfcolor=red"
check "session status row still colored by its own status, unaffected by headroom" \
  "$out" "work:1 - build | sfimage=brain.head.profile.fill sfcolor=blue"
check_not "headroom summary tooltip line not rendered as a garbled session row" \
  "$out" "claude 73%  codex 15%  gpt 5% |"

# --- Case 5: budget headroom absent -> no headroom rows, parity with waybar --
#
# No "headroom" key in the JSON at all (loop disabled/unconfigured, per #169).
# The dropdown must render exactly as it does for a status payload with no
# budget data: no headroom rows, and no bare percentage text anywhere in the
# output (percentages only ever appear via headroom rendering).
cat > "$TMP/no-headroom.json" <<'JSON'
{"text":"▶ 1","tooltip":"work:1 - build (running)","class":"running","alt":"active"}
JSON
out="$(run_plugin "$TMP/no-headroom.json" 0)"
check "session row still renders normally when headroom is absent" "$out" "work:1 - build | sfimage=brain.head.profile.fill sfcolor=blue"
check_not "no headroom percentage text when field is absent" "$out" "%"

# --- Case 6: non-matching, non-headroom tooltip line -> 'none' fallback ---
#
# A tooltip line with no trailing "(status)" suffix that is also NOT the
# compact headroom summary line (no "headroom" field here) must still render
# — with the 'none' status fallback (no sfimage/sfcolor params) — rather than
# being silently dropped. Pins the narrowed skip condition against future
# regressions of the old "drop any non-matching line" behavior.
cat > "$TMP/garbled-line.json" <<'JSON'
{"text":"▶ 1","tooltip":"work:1 - build (running)\ngarbled line with no status suffix","class":"running","alt":"active"}
JSON
out="$(run_plugin "$TMP/garbled-line.json" 0)"
check "non-matching, non-headroom line still renders via 'none' fallback (not silently dropped)" \
  "$out" "garbled line with no status suffix |"
check "session row still renders normally alongside the garbled line" \
  "$out" "work:1 - build | sfimage=brain.head.profile.fill sfcolor=blue"

# --- Case 7: dispatch enabled, no runs yet, zero sessions ------------------
#
# Zero sessions but the fleet dispatch loop is enabled: class stays "none"
# (session-status semantics unchanged) but alt is "dispatch-only", so the item
# must NOT hide (this is the one case in this file where "class":"none" must
# NOT produce empty output — it's the narrow exception to Case 2b above,
# distinguished by "alt":"dispatch-only" rather than "alt":"none"). Also
# asserts the dedicated dispatch dropdown row renders (no last-run summary
# yet, since last_run_at is absent).
cat > "$TMP/dispatch-only.json" <<'JSON'
{"text":"⟳","tooltip":"dispatch: on (5m)","class":"none","alt":"dispatch-only","dispatch":{"enabled":true,"daemon_running":true,"interval":"5m","pass_running":false,"last_dispatched":0,"last_skipped":0}}
JSON
out="$(run_plugin "$TMP/dispatch-only.json" 0)"
check_not_empty "not hidden when alt is dispatch-only (class stays none)" "$out"
check "dispatch-only menu bar line shows the glyph" "$out" "⟳ |"
check "dispatch-only dropdown row shown, no runs yet" "$out" "dispatch: on (5m)"

# --- Case 8: dispatch enabled, success counts ------------------------------
#
# Note: the exact HH:MM in the dedicated dropdown row depends on the local
# timezone the JXA interpreter runs under (mirrors formatLastRunTime's
# t.Local() in status.go), so this only asserts the timezone-independent
# parts of the summary, not a specific clock time.
cat > "$TMP/dispatch-success.json" <<'JSON'
{"text":"▶ 1  ⟳","tooltip":"work:1 - build (running)\ndispatch: on (5m) — last run 12:04, 2 dispatched / 3 skipped","class":"running","alt":"active","dispatch":{"enabled":true,"daemon_running":true,"interval":"5m","pass_running":false,"last_run_at":"2026-07-13T12:04:00Z","last_dispatched":2,"last_skipped":3}}
JSON
out="$(run_plugin "$TMP/dispatch-success.json" 0)"
check "dispatch success dropdown row (dedicated, not generic fallback)" "$out" "dispatch: on (5m) — last run"
check "dispatch success dropdown row shows the counts" "$out" "2 dispatched / 3 skipped"
check "session row still renders normally alongside the dispatch row" \
  "$out" "work:1 - build | sfimage=brain.head.profile.fill sfcolor=blue"

# --- Case 9: dispatch enabled, last run failed -----------------------------
cat > "$TMP/dispatch-error.json" <<'JSON'
{"text":"▶ 1  ⟳","tooltip":"work:1 - build (running)\ndispatch: on (5m) — last run failed: boom","class":"running","alt":"active","dispatch":{"enabled":true,"daemon_running":true,"interval":"5m","pass_running":false,"last_dispatched":0,"last_skipped":0,"last_error":"boom"}}
JSON
out="$(run_plugin "$TMP/dispatch-error.json" 0)"
check "dispatch error dropdown row shows the failure" "$out" "dispatch: on (5m) — last run failed: boom"

# --- Case 10: dispatch success with malformed last_run_at ------------------
#
# A malformed/unparseable last_run_at must not silently degrade to "NaN:NaN"
# (new Date() never throws). The JXA mirror must fall back to rendering the
# raw string, mirroring formatLastRunTime's raw-string fallback in status.go.
cat > "$TMP/dispatch-malformed-timestamp.json" <<'JSON'
{"text":"▶ 1  ⟳","tooltip":"work:1 - build (running)\ndispatch: on (5m) — last run not-a-timestamp, 2 dispatched / 3 skipped","class":"running","alt":"active","dispatch":{"enabled":true,"daemon_running":true,"interval":"5m","pass_running":false,"last_run_at":"not-a-timestamp","last_dispatched":2,"last_skipped":3}}
JSON
out="$(run_plugin "$TMP/dispatch-malformed-timestamp.json" 0)"
check "malformed last_run_at falls back to the raw string" "$out" "last run not-a-timestamp,"
check_not "malformed last_run_at never renders NaN:NaN" "$out" "NaN:NaN"

# --- Case 11: dispatch enabled with empty Interval --------------------------
#
# Enabled=true with an empty Interval is a reachable config state (LoopEnabled
# can be explicitly true while DaemonInterval is unset/0). The dedicated
# dropdown row must omit the parenthetical ("dispatch: on") instead of
# rendering the cosmetically broken "dispatch: on ()".
cat > "$TMP/dispatch-empty-interval.json" <<'JSON'
{"text":"⟳","tooltip":"dispatch: on","class":"none","alt":"dispatch-only","dispatch":{"enabled":true,"daemon_running":true,"interval":"","pass_running":false,"last_dispatched":0,"last_skipped":0}}
JSON
out="$(run_plugin "$TMP/dispatch-empty-interval.json" 0)"
check "empty interval renders without empty parens" "$out" "dispatch: on"
check_not "empty interval never renders empty parens" "$out" "dispatch: on ()"

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
