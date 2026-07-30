#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CODEX_MANIFEST="${FLOW_DIR}/.codex-plugin/plugin.json"
CLAUDE_HOOKS="${FLOW_DIR}/hooks/hooks.json"

FAILURES=0
PASSES=0

fail() {
    echo "FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

echo "hooks.test.sh"

if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not found on PATH"
    exit 0
fi

HOOKS_PATH="$(jq -r '.hooks // empty' "${CODEX_MANIFEST}")"
if [[ -n "${HOOKS_PATH}" ]]; then
    pass
else
    fail "Codex manifest must declare an explicit hooks path"
fi

CODEX_HOOKS="${FLOW_DIR}/${HOOKS_PATH#./}"
if [[ -f "${CODEX_HOOKS}" ]]; then
    pass
else
    fail "Codex hooks file does not exist: ${CODEX_HOOKS}"
fi

if [[ -f "${CODEX_HOOKS}" ]] && jq -e '
  (([.hooks | keys[]] | sort) == ["PreCompact","PreToolUse","SessionStart","Stop"]) and
  ([.hooks | to_entries[] | .value[] | .hooks[] | .timeout] | all(. > 0 and . <= 30)) and
  ([.hooks | to_entries[] | .value[] | .hooks[] | .command] | all(contains("${PLUGIN_ROOT}")))
' "${CODEX_HOOKS}" >/dev/null; then
    pass
else
    fail "Codex hooks must cover guards/context/reminders with seconds-based timeouts"
fi

if jq -e '[.hooks | to_entries[] | .value[] | .hooks[] | keys[]] | all(. == "type" or . == "command" or . == "timeout")' "${CODEX_HOOKS}" >/dev/null; then
    pass
else
    fail "Codex hook handlers contain unsupported keys"
fi

# ── #795: a Bash PreToolUse matcher must wire both guard scripts ────
# Extends the existing Write|Edit matcher's write-guard coverage to Bash
# redirection/tee targets. Both check-sensitive-files.sh and
# guard-main-worktree.sh must be wired under a "Bash" matcher entry.
if jq -e '
  [.hooks.PreToolUse[] | select(.matcher == "Bash")] as $bash_matchers
  | ($bash_matchers | length) > 0
  and ([$bash_matchers[].hooks[].command] | any(contains("check-sensitive-files.sh")))
  and ([$bash_matchers[].hooks[].command] | any(contains("guard-main-worktree.sh")))
' "${CODEX_HOOKS}" >/dev/null; then
    pass
else
    fail "Codex hooks must wire a Bash PreToolUse matcher covering both check-sensitive-files.sh and guard-main-worktree.sh"
fi

# ── Codex hook commands must not depend on an interpreter from PATH ────
# Codex runs every hook as `$SHELL -lc "<command>"` (codex-rs/hooks/src/
# engine/command_runner.rs's default_shell_command). That is a LOGIN shell:
# it sources ~/.profile but NOT ~/.bashrc, so an interpreter installed by a
# version manager (nvm/fnm/mise/volta) is absent from PATH and a bare-word
# `node ...`/`python ...` hook command dies with exit 127 before any of
# flow's own code runs. Every command must therefore invoke a
# ${PLUGIN_ROOT}-anchored executable directly, with no interpreter prefix.
if jq -e '
  [.hooks | to_entries[] | .value[] | .hooks[] | .command]
  | all(startswith("\"${PLUGIN_ROOT}"))
' "${CODEX_HOOKS}" >/dev/null; then
    pass
else
    fail "Codex hook commands must start with a quoted \${PLUGIN_ROOT} path (no PATH-resolved interpreter prefix, which dies with exit 127 under Codex's login shell)"
fi

# ── Behavioral: each hook command must succeed with NO node on PATH ────
# Asserts the real contract (exit status + emitted JSON envelope) by running
# each hook's actual command string from hooks.json through a shell, with
# every PATH element that provides `node` removed. This is the assertion the
# previous shape-only checks could not make: it fails on exit 127 exactly the
# way a real Codex session does.
#
# A non-login `bash -c` is used deliberately: `-lc` would re-source
# /etc/profile and reinstate the interpreter this test masks, so a login
# shell here would silently defeat the node-free precondition.
#
# `node` is masked with a shim that exits 127 rather than by deleting PATH
# entries: node ships in the same /usr/bin as bash, jq, and sed, so dropping
# whole PATH elements would strip the tools the hooks legitimately need and
# turn this into a vacuous "nothing works" test. The shim reproduces exactly
# what a Codex login shell sees when node was installed by a version manager.
MASK_DIR="$(mktemp -d)" || {
    echo "FAIL: test setup: mktemp -d failed" >&2
    exit 1
}
CONTRACT_DIR="$(mktemp -d)" || {
    echo "FAIL: test setup: mktemp -d failed" >&2
    exit 1
}
CONFIGURED_DIR="$(mktemp -d)" || {
    echo "FAIL: test setup: mktemp -d failed" >&2
    exit 1
}
trap 'rm -rf "${MASK_DIR}" "${CONTRACT_DIR}" "${CONFIGURED_DIR}"' EXIT

for interpreter in node nodejs; do
    printf '#!/bin/sh\necho "%s: command not found" >&2\nexit 127\n' \
        "${interpreter}" > "${MASK_DIR}/${interpreter}"
    chmod +x "${MASK_DIR}/${interpreter}"
done
NODE_FREE_PATH="${MASK_DIR}:${PATH}"

# Prove the mask actually bites before relying on it.
if [[ "$(PATH="${NODE_FREE_PATH}" command -v node)" == "${MASK_DIR}/node" ]] \
    && ! PATH="${NODE_FREE_PATH}" node --version >/dev/null 2>&1; then
    pass
else
    fail "test setup: node mask is not in effect"
fi

# run_hook_command <event> — extract <event>'s first command from hooks.json,
# run it node-free, print stdout. Pure extractor: never calls fail() (it runs
# under command substitution, where a fail() counter bump would be lost in the
# subshell -- docs/shell-scripting-gotchas.md's read-helper purity rule).
run_hook_command() {
    local event="$1" cmd
    cmd="$(jq -r --arg e "${event}" '.hooks[$e][0].hooks[0].command' "${CODEX_HOOKS}")" || return 1
    [[ -n "${cmd}" && "${cmd}" != "null" ]] || return 1
    (
        cd "${CONTRACT_DIR}" || exit 1
        PATH="${NODE_FREE_PATH}" PLUGIN_ROOT="${FLOW_DIR}" bash -c "${cmd}"
    )
}

# CONTRACT_DIR is an empty dir with no .cenci/config.json, so
# check-lessons-file.sh emits its warning and SessionStart carries context.
if session="$(run_hook_command SessionStart)"; then
    pass
else
    fail "SessionStart hook command must exit 0 with no node on PATH (got $?)"
fi
if compact="$(run_hook_command PreCompact)"; then
    pass
else
    fail "PreCompact hook command must exit 0 with no node on PATH (got $?)"
fi
if stop="$(run_hook_command Stop)"; then
    pass
else
    fail "Stop hook command must exit 0 with no node on PATH (got $?)"
fi

jq -e '.hookSpecificOutput.hookEventName == "SessionStart" and (.hookSpecificOutput.additionalContext | type == "string")' <<<"${session:-}" >/dev/null && pass || fail "SessionStart contract"
jq -e '.hookSpecificOutput.hookEventName == "PreCompact" and (.hookSpecificOutput.additionalContext | type == "string")' <<<"${compact:-}" >/dev/null && pass || fail "PreCompact contract"
jq -e '.systemMessage | type == "string"' <<<"${stop:-}" >/dev/null && pass || fail "Stop contract"

# The emitter must survive a repo whose wrapped script emits nothing (a repo
# WITH a config) by emitting valid empty JSON, never a bare empty string.
mkdir -p "${CONFIGURED_DIR}/.cenci" && printf '{}\n' > "${CONFIGURED_DIR}/.cenci/config.json"
empty_session="$(cd "${CONFIGURED_DIR}" && PATH="${NODE_FREE_PATH}" PLUGIN_ROOT="${FLOW_DIR}" \
    bash -c "$(jq -r '.hooks.SessionStart[0].hooks[0].command' "${CODEX_HOOKS}")")"
if jq -e '. == {}' <<<"${empty_session}" >/dev/null 2>&1; then
    pass
else
    fail "SessionStart must emit {} when the wrapped script produces no context, got: ${empty_session}"
fi

if jq -e '(.hooks.Stop | length) > 0 and (.hooks.PreToolUse | length) > 0 and (.hooks.SessionStart | length) > 0' "${CLAUDE_HOOKS}" >/dev/null; then
    pass
else
    fail "Claude hooks must retain their existing lifecycle handlers"
fi

SKILLS_PATH="$(jq -r '.skills // empty' "${CODEX_MANIFEST}")"
SKILL_COUNT="$(find "${FLOW_DIR}/${SKILLS_PATH#./}" -mindepth 2 -maxdepth 2 -name SKILL.md | wc -l)"
if [[ -n "${SKILLS_PATH}" && "${SKILL_COUNT}" -gt 0 ]]; then
    pass
else
    fail "Codex manifest must keep cenci skills discoverable"
fi

echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
