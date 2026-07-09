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
check "running row is blue/gear"         "$out" "work:1 - build | sfimage=gearshape.fill sfcolor=blue"

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
check "menu bar line tinted by highest class (running)" "$out" "▶ 1  ✓ 1 | sfimage=gearshape.fill sfcolor=blue"
check "running row is blue/gear"                        "$out" "work:1 - build | sfimage=gearshape.fill sfcolor=blue"
check "done paneless row is green/check"                "$out" "solo | sfimage=checkmark.circle.fill sfcolor=green"

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
