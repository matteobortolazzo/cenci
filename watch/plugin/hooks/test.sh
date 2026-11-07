#!/usr/bin/env bash
#
# Dependency-free gate test for the Claude Code bootstrap script.
#
# Shared assertions live in ../lib/test-common.sh (sourced below); this file
# only points it at the Claude Code-specific bootstrap.sh, plugin.json,
# manifest dir, and root-resolution env var.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
BOOTSTRAP="$DIR/bootstrap.sh"
PLUGIN_JSON="$DIR/../.claude-plugin/plugin.json"
MANIFEST_DIR=".claude-plugin"
ROOT_VAR_NAME="CLAUDE_PLUGIN_ROOT"
# Hook-health cases (#1239) run this client's real hooks.json commands.
HOOKS_JSON="$DIR/hooks.json"
NOTIFY_REL="hooks/notify.sh"
CLIENT="claude"

# client_extra_cases runs from test-common.sh just before its summary, with
# every hh_* helper available. Claude Code only: the hooks.json contract (the
# settings hooks stay byte-for-byte what they were, beside one `modules` entry
# for the /cenci-status mods module) and the status.sh wrapper behind
# /cenci-status (#1183).
client_extra_cases() {
  local notify_cmd session_cmd events ev expected actual

  # shellcheck disable=SC2016 # single-quoted: ${CLAUDE_PLUGIN_ROOT} is expanded by the hook's shell, not here
  notify_cmd='"${CLAUDE_PLUGIN_ROOT}/hooks/notify.sh" 2>/dev/null || true'
  # shellcheck disable=SC2016 # same: literal hook command text
  session_cmd='nohup "${CLAUDE_PLUGIN_ROOT}/hooks/bootstrap.sh" </dev/null >/dev/null 2>&1 & "${CLAUDE_PLUGIN_ROOT}/hooks/notify.sh" 2>/dev/null || true'
  events="SessionStart UserPromptSubmit Notification PreToolUse PermissionRequest PermissionDenied PostToolUse PostToolUseFailure Stop StopFailure PostModelSwitch SessionEnd"

  # --- hooks.json contract ----------------------------------------------------
  hh_assert "hooks.json top-level keys are exactly hooks and modules" \
    test "$(jq -c 'keys' "$HOOKS_JSON")" = '["hooks","modules"]'
  hh_assert "hooks.json declares exactly one module, ./register.js" \
    test "$(jq -c '.modules' "$HOOKS_JSON")" = '["./register.js"]'
  # shellcheck disable=SC2086 # $events is a deliberate word list
  hh_assert "hooks.json declares exactly the 12 settings-hook events" \
    test "$(jq -r '.hooks | keys | sort | join(" ")' "$HOOKS_JSON")" = \
    "$(jq -nr '$ARGS.positional | sort | join(" ")' --args $events)"

  for ev in $events; do
    if [ "$ev" = "SessionStart" ]; then
      expected="$(jq -nc --arg cmd "$session_cmd" \
        '[{matcher: "startup|resume|clear", hooks: [{type: "command", command: $cmd, timeout: 5}]}]')"
    else
      expected="$(jq -nc --arg cmd "$notify_cmd" \
        '[{hooks: [{type: "command", command: $cmd, timeout: 5}]}]')"
    fi
    actual="$(jq -c --arg ev "$ev" '.hooks[$ev]' "$HOOKS_JSON")"
    hh_assert "hooks.json $ev hook keeps its command, matcher and timeout 5" \
      test "$actual" = "$expected"
  done

  # --- status.sh (the /cenci-status wrapper) ----------------------------------
  local shell tag calls stub_out
  for shell in bash dash; do
    if ! command -v "$shell" >/dev/null 2>&1; then
      echo "SKIP - $shell not found; skipping its status.sh cases"
      continue
    fi
    tag="[$shell][status.sh]"

    hh_assert "$tag the wrapper is shipped next to notify.sh and executable" \
      test -x "$DIR/status.sh"

    # No binary anywhere: exit 127, the reason on stderr, nothing on stdout, and
    # no hook-health record (status.sh is not a hook).
    hh_setup "$shell" status-nobin
    hh_env "$HH_ROOT/hooks/status.sh" </dev/null >"$HH_OUT" 2>"$HH_ERR"
    HH_RC=$?
    hh_assert "$tag exits 127 when no binary resolves" test "$HH_RC" -eq 127
    hh_assert "$tag says 'cenci binary not found' on stderr when no binary resolves" \
      grep -q "cenci binary not found" "$HH_ERR"
    hh_assert "$tag prints nothing on stdout when no binary resolves" test ! -s "$HH_OUT"
    hh_assert "$tag writes no hook-health record when no binary resolves" \
      test ! -e "$HH_STATE/cenci"

    # SessionStart race: no plugin-local bin/, CENCI_BIN reaches a working
    # binary. It must run `status` as its single argument and pass stdout on.
    hh_setup "$shell" status-envbin
    calls="$HH_TMPD/env-calls"
    stub_out="daemon: running (pid 42)"
    cat >"$HH_STUBS/env-cenci" <<STUB
#!/bin/sh
[ "\$1" = "--version" ] && exit 0
printf 'argc=%s\n' "\$#" >>"$calls"
for a in "\$@"; do printf '%s\n' "\$a" >>"$calls"; done
printf '%s\n' "$stub_out"
printf 'sessions: none\n'
STUB
    chmod +x "$HH_STUBS/env-cenci"
    hh_env CENCI_BIN="$HH_STUBS/env-cenci" "$HH_ROOT/hooks/status.sh" </dev/null >"$HH_OUT" 2>"$HH_ERR"
    HH_RC=$?
    hh_assert "$tag exits 0 when a CENCI_BIN stub answers" test "$HH_RC" -eq 0
    hh_assert "$tag runs the CENCI_BIN stub with the single argument 'status'" \
      test "$(cat "$calls" 2>/dev/null)" = "$(printf 'argc=1\nstatus')"
    hh_assert "$tag passes the stub's stdout through unchanged" \
      test "$(cat "$HH_OUT")" = "$(printf '%s\nsessions: none' "$stub_out")"
    hh_assert "$tag stays silent on stderr when a CENCI_BIN stub answers" test ! -s "$HH_ERR"

    # The plugin-local bin/cenci wins over CENCI_BIN.
    hh_setup "$shell" status-localbin
    mkdir -p "$HH_ROOT/bin"
    calls="$HH_TMPD/local-calls"
    cat >"$HH_ROOT/bin/cenci" <<STUB
#!/bin/sh
[ "\$1" = "--version" ] && exit 0
printf 'argc=%s\n' "\$#" >>"$calls"
for a in "\$@"; do printf '%s\n' "\$a" >>"$calls"; done
printf 'daemon: local\n'
STUB
    chmod +x "$HH_ROOT/bin/cenci"
    cat >"$HH_STUBS/env-cenci" <<STUB
#!/bin/sh
[ "\$1" = "--version" ] && exit 0
printf 'daemon: env\n'
printf 'called\n' >>"$HH_TMPD/env-called"
STUB
    chmod +x "$HH_STUBS/env-cenci"
    hh_env CENCI_BIN="$HH_STUBS/env-cenci" "$HH_ROOT/hooks/status.sh" </dev/null >"$HH_OUT" 2>"$HH_ERR"
    HH_RC=$?
    hh_assert "$tag runs the plugin-local bin/cenci with 'status' ahead of CENCI_BIN" \
      test "$HH_RC" -eq 0 -a "$(cat "$HH_OUT")" = "daemon: local" -a \
      "$(cat "$calls" 2>/dev/null)" = "$(printf 'argc=1\nstatus')"
    hh_assert "$tag never runs the CENCI_BIN stub when bin/cenci exists" \
      test ! -e "$HH_TMPD/env-called"
  done
}

# shellcheck source=../lib/test-common.sh
source "$DIR/../lib/test-common.sh"
