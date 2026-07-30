#!/bin/sh
# hook-output.sh — emits the Codex hook JSON envelope for flow's SessionStart,
# PreCompact, and Stop events. Replaces the former codex/hook-output.mjs.
#
# Why this is shell and not node
# ------------------------------
# Codex runs every hook command as `$SHELL -lc "<command>"` (see
# codex-rs/hooks/src/engine/command_runner.rs's default_shell_command). That is
# a LOGIN shell: it sources ~/.profile but NOT ~/.bashrc. An interpreter
# installed by a version manager (nvm/fnm/mise/volta) initializes in ~/.bashrc
# only, so it is absent from the hook's PATH and a `node ...` hook command dies
# with exit 127 — before any of flow's own code runs, making the failure look
# like a cenci bug rather than a PATH one. That killed SessionStart, PreCompact,
# and Stop for Codex users while the PreToolUse guards (which invoke their
# scripts directly) kept working.
#
# node is not a declared cenci prerequisite anywhere, and flow describes itself
# as "Markdown skills, JSON config, shell hooks", so the envelope emitter now
# carries no interpreter dependency beyond /bin/sh. Its Claude Code counterpart
# (hooks/hooks.json) already invoked these same scripts with no interpreter.
#
# Contract (unchanged from the .mjs it replaces)
# ---------------------------------------------
#   session -> runs hooks/scripts/check-lessons-file.sh, wraps its stdout as
#              {"hookSpecificOutput":{"hookEventName":"SessionStart",...}}
#   compact -> runs hooks/scripts/preserve-context.sh, wraps its stdout as
#              {"hookSpecificOutput":{"hookEventName":"PreCompact",...}}
#   stop    -> emits a static {"systemMessage":...} reminder
# Empty wrapped output emits `{}`. Exit 2 on a missing PLUGIN_ROOT or an
# unknown mode (programmer error); otherwise the wrapped script's exit status.
#
# JSON is built with a single `jq -n --arg` call, never string interpolation:
# preserve-context.sh's output includes a `ls docs/*.md` listing, so filenames
# outside this script's control reach the payload and a `"`, `\`, or newline
# would otherwise produce malformed JSON or let that content masquerade as
# trusted context. See docs/shell-scripting-gotchas.md's SessionStart-payload
# rule, and check-pending-plans.sh for the same pattern.

set -u

ROOT="${PLUGIN_ROOT:-}"
MODE="${1:-}"

case "${MODE}" in
    session | compact | stop) ;;
    *) exit 2 ;;
esac

[ -n "${ROOT}" ] || exit 2

# Stop needs no wrapped script and no jq: a static literal keeps the event that
# runs on every single agent turn free of any external dependency at all.
if [ "${MODE}" = stop ]; then
    printf '%s\n' '{"systemMessage":"Capture only specific non-obvious lessons during implementation. Shared guidance belongs in AGENTS.md; otherwise propose the lesson without editing main."}'
    exit 0
fi

if [ "${MODE}" = session ]; then
    SCRIPT="check-lessons-file.sh"
    EVENT="SessionStart"
else
    SCRIPT="preserve-context.sh"
    EVENT="PreCompact"
fi

SCRIPT_PATH="${ROOT}/hooks/scripts/${SCRIPT}"
if [ ! -x "${SCRIPT_PATH}" ]; then
    # Reported, never swallowed: a missing wrapped script is a broken install,
    # not an empty-context session.
    printf 'hook-output.sh: missing or non-executable: %s\n' "${SCRIPT_PATH}" >&2
    exit 1
fi

# Command substitution strips trailing newlines, which is the trim the former
# .mjs applied via String.trim() before its emptiness check.
CONTEXT="$("${SCRIPT_PATH}")"
STATUS=$?
if [ "${STATUS}" -ne 0 ]; then
    exit "${STATUS}"
fi

if [ -z "${CONTEXT}" ]; then
    printf '%s\n' '{}'
    exit 0
fi

# jq absence degrades to a valid empty envelope with a visible warning rather
# than a broken session: this hook is advisory, so it must never be the reason
# a Codex session fails to start or compact.
if ! command -v jq >/dev/null 2>&1; then
    printf 'hook-output.sh: jq not found on PATH; emitting no additionalContext.\n' >&2
    printf '%s\n' '{}'
    exit 0
fi

jq -n --arg event "${EVENT}" --arg ctx "${CONTEXT}" \
    '{hookSpecificOutput: {hookEventName: $event, additionalContext: $ctx}}'
