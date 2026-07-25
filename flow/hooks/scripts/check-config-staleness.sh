#!/usr/bin/env bash
# check-config-staleness.sh — SessionStart advisory: nudge when the repo's
# .cenci/config.json was written by an older /cenci:configure than the
# installed cenci flow plugin, so users learn a re-run would pick up new
# configure features without having to watch the plugin changelog.
#
# Modes:
#   (default)  Claude Code SessionStart hook — emits hookSpecificOutput JSON
#              with an additionalContext advisory when the config is stale or
#              unstamped; silent (no output, exit 0) otherwise. Never blocks.
#   --plain    machine-readable one-liner for scripted consumers (maintain's
#              check.sh config-version check, and this script's own tests):
#                current <configVersion> <pluginVersion>
#                stale <configVersion> <pluginVersion>
#                unstamped - <pluginVersion>
#                absent
#                unknown <reason>
#              Always exits 0 — staleness is advisory, never a gate.
#
# Staleness rule (single source of truth — check.sh delegates to this script
# so the hook and the maintain check can never disagree): the config is stale
# when its configVersion is behind the installed plugin version AND the two
# differ in major.minor. Patch-only differences stay silent — configure
# features that warrant a re-run land in minor (feat) or major bumps per the
# plugin-version-bump workflow. A config *ahead* of the plugin (plugin
# downgrade) is treated as current. A config with no configVersion at all
# predates stamping and gets the same nudge as a stale one.
#
# Plugin version source: ${CLAUDE_PLUGIN_ROOT}/.claude-plugin/plugin.json,
# falling back to this script's own bundle (../../.claude-plugin/plugin.json)
# when the env var is unset (e.g. invoked by check.sh or tests).
set -uo pipefail

MODE="hook"
if [[ "${1:-}" == "--plain" ]]; then
  MODE="plain"
fi

# Terminal no-advisory states: print the plain token in --plain mode, stay
# silent in hook mode, always exit 0.
finish_quiet() {
  if [[ "$MODE" == "plain" ]]; then
    printf '%s\n' "$*"
  fi
  exit 0
}

CONFIG_FILE=".cenci/config.json"

command -v jq >/dev/null 2>&1 || finish_quiet "unknown no-jq"
[[ -f "$CONFIG_FILE" ]] || finish_quiet "absent"

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || finish_quiet "unknown self-dir-unresolvable"
PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-${SELF_DIR}/../..}"
MANIFEST="${PLUGIN_ROOT}/.claude-plugin/plugin.json"
[[ -f "$MANIFEST" ]] || finish_quiet "unknown no-plugin-manifest"

PLUGIN_VERSION="$(jq -r 'if (.version | type) == "string" then .version else "" end' "$MANIFEST" 2>/dev/null)" \
  || finish_quiet "unknown plugin-manifest-unreadable"
[[ -n "$PLUGIN_VERSION" ]] || finish_quiet "unknown no-plugin-version"

# Validate the config parses before reading fields, so a malformed file stays
# a maintain/invalid-json problem instead of a duplicate nag here.
jq -e 'type == "object"' "$CONFIG_FILE" >/dev/null 2>&1 || finish_quiet "unknown invalid-config"

CONFIG_VERSION="$(jq -r 'if (.configVersion | type) == "string" then .configVersion else "" end' "$CONFIG_FILE" 2>/dev/null)" \
  || finish_quiet "unknown config-unreadable"

STATE="current"
if [[ -z "$CONFIG_VERSION" ]]; then
  STATE="unstamped"
else
  cfg_mm="$(printf '%s' "$CONFIG_VERSION" | cut -d. -f1,2)" || finish_quiet "unknown compare-failed"
  plug_mm="$(printf '%s' "$PLUGIN_VERSION" | cut -d. -f1,2)" || finish_quiet "unknown compare-failed"
  if [[ "$cfg_mm" != "$plug_mm" && "$CONFIG_VERSION" != "$PLUGIN_VERSION" ]]; then
    lowest="$(printf '%s\n%s\n' "$CONFIG_VERSION" "$PLUGIN_VERSION" | sort -V | head -n1)" \
      || finish_quiet "unknown compare-failed"
    if [[ "$lowest" == "$CONFIG_VERSION" ]]; then
      STATE="stale"
    fi
  fi
fi

case "$STATE" in
  current)
    finish_quiet "current ${CONFIG_VERSION} ${PLUGIN_VERSION}"
    ;;
  stale)
    if [[ "$MODE" == "plain" ]]; then
      printf 'stale %s %s\n' "$CONFIG_VERSION" "$PLUGIN_VERSION"
      exit 0
    fi
    CTX="This repo's .cenci/config.json was written by /cenci:configure v${CONFIG_VERSION}, but the installed cenci flow plugin is v${PLUGIN_VERSION}. Re-running /cenci:configure would pick up configure features added since — existing answers are offered back as defaults. Surface this to the user at a natural moment; do not run /cenci:configure unprompted."
    ;;
  unstamped)
    if [[ "$MODE" == "plain" ]]; then
      printf 'unstamped - %s\n' "$PLUGIN_VERSION"
      exit 0
    fi
    CTX="This repo's .cenci/config.json has no configVersion stamp — it was written by a /cenci:configure older than the installed cenci flow plugin (v${PLUGIN_VERSION}). Re-running /cenci:configure would pick up configure features added since and stamp configVersion — existing answers are offered back as defaults. Surface this to the user at a natural moment; do not run /cenci:configure unprompted."
    ;;
esac

jq -n --arg ctx "$CTX" \
  '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
exit 0
