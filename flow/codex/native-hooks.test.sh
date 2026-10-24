#!/usr/bin/env bash
# native-hooks.test.sh -- opt-in native Codex hook runner (#1212).
#
# Every other suite in this directory runs the guards from a shell script. This
# one runs a REAL `codex exec` against a disposable fixture repo with the
# installed cenci plugin and records what Codex actually does:
#
#   - preflight / 127 inspection: plugin root, script existence and +x for every
#     hooks.json handler, $SHELL, login-shell tool availability, and each guard
#     rerun the way Codex launches it (`$SHELL -lc`, PLUGIN_ROOT exported)
#   - deny matrix: sensitive and main-worktree targets through Bash and through
#     apply_patch, each paired with a positive control whose write must land
#   - guard canary preflight (#1213): the guard-canary.sh helper end to end --
#     plan, one `codex exec` per probe, check -- "as launched" and, in the
#     sandbox only, with --dangerously-bypass-hook-trust withheld; FAIL when the
#     preflight's result contradicts the trust state the deny-matrix canary shows
#   - cwd capture: payload cwd, hook process cwd and effective tool cwd for a
#     direct apply_patch, an exec_command with a per-call workdir, and (when the
#     installed Codex has the feature) nested code mode
#   - relative apply_patch Add/Update/Delete/Move from a root and a worktree
#     session, including the bypass shape (worktree session, root workdir)
#   - launch failure: does the tool continue when a hook exits 127 or 126?
#
# It is opt-in. It prints `not run: <reason>` and exits 0 -- never "passed" --
# when CENCI_CODEX_NATIVE is not 1, codex is absent or older than 0.144.1, or
# `codex login status` fails, so run-checks.sh can run it in CI unchanged.
#
# Verdicts are judged from filesystem state after each call, the `codex exec
# --json` tool-call events and the guards' capture tap
# (CENCI_CODEX_HOOK_CAPTURE_DIR) -- never from the model's narration or an exit
# code alone. An unwritten target with no proof the call was attempted is
# inconclusive (retried once), never a pass. A block counts only when the block
# evidence sits in the SAME tool-call item as the scenario's target, and (when
# the scenario names the guard message it expects) carries that message.
#
# Trust: the guards only mean anything if the plugin hooks are active. The
# first deny row is a canary (an absolute main-worktree write). Until it shows
# a denied call, no allow or deny row is run: an allowed write proves nothing
# when no hook is watching. Allow rows additionally need a capture-tap line.
#
# --dangerously-bypass-hook-trust (and --dangerously-bypass-approvals-and-
# sandbox) are passed ONLY when CENCI_SANDBOX=1 AND one of the library's
# hard-coded container marker files exists (cnh_trust_bypass_flag); never on the
# host, and never because of anything else in the environment. On the host,
# hooks must already be trusted through `/hooks`; if they are not the canary
# reports "not run: hooks pending/untrusted".
#
# Preflight re-runs hook handlers, i.e. executes scripts, so it is deliberately
# narrow: only plugin roots whose .codex-plugin/plugin.json names the cenci flow
# plugin, and only handler commands of the exact shape flow/codex/hooks.json
# uses. Nothing else under CODEX_HOME is executed.
#
# Output: a sanitized markdown fragment on stdout, and also in the file named by
# $CENCI_CODEX_NATIVE_OUT when set. The fragment ends with one RESULT line:
#   RESULT: OK            exit 0  at least one scenario row proved something, the
#                                 hooks were shown active, nothing failed
#   RESULT: INCONCLUSIVE  exit 3  nothing was proven (no ok scenario row, hooks
#                                 inactive, or the canary could not tell)
#   RESULT: FAIL          exit 1  at least one row failed
# A run whose gate is not met prints `not run: ...` and exits 0 instead.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 1
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 1
MIN_VERSION="0.144.1"
FLOW_PLUGIN_NAME="cenci"

# shellcheck source=native-hooks-lib.sh
source "${SCRIPT_DIR}/native-hooks-lib.sh" || {
    echo "FAIL: cannot source ${SCRIPT_DIR}/native-hooks-lib.sh" >&2
    exit 1
}

not_run() {
    printf 'not run: %s\n' "$1"
    exit 0
}

# ── Gates ───────────────────────────────────────────────────────────────
[[ "${CENCI_CODEX_NATIVE:-}" == 1 ]] \
    || not_run "set CENCI_CODEX_NATIVE=1 to run the native Codex hook runner (opt-in)"
command -v codex >/dev/null 2>&1 || not_run "codex is not on PATH"
command -v jq >/dev/null 2>&1 || not_run "jq is not on PATH"
command -v git >/dev/null 2>&1 || not_run "git is not on PATH"

CODEX_VERSION_OUT="$(codex --version 2>&1)" || not_run "codex --version failed"
CODEX_VERSION="$(cnh_parse_version "${CODEX_VERSION_OUT}")" \
    || not_run "could not read a version from codex --version (need codex >= ${MIN_VERSION})"
cnh_version_ge "${CODEX_VERSION}" "${MIN_VERSION}" \
    || not_run "codex version ${CODEX_VERSION} is older than ${MIN_VERSION}"
codex login status >/dev/null 2>&1 \
    || not_run "codex login status failed: Codex is not authenticated (run codex login)"

# ── Setup ───────────────────────────────────────────────────────────────
# TMPDIR would widen the guards' allowlist and GIT_* would redirect git. The
# fixture is NOT under /tmp for the same reason (the guards allowlist /tmp/*).
unset TMPDIR GITHUB_REPOSITORY GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
unset CENCI_CODEX_HOOK_CAPTURE_DIR

FIXTURE_ROOT="$(mktemp -d /var/tmp/cenci-native-hooks.XXXXXX)" || {
    echo "FAIL: mktemp -d failed" >&2
    exit 1
}
case "${FIXTURE_ROOT}" in
    /var/tmp/cenci-native-hooks.?*) ;;
    *)
        echo "FAIL: unexpected fixture root '${FIXTURE_ROOT}'" >&2
        exit 1
        ;;
esac
trap 'rm -rf "${FIXTURE_ROOT}"' EXIT

REPO="${FIXTURE_ROOT}/repo"
WT="${REPO}/.worktrees/1-native"
LAUNCH="${FIXTURE_ROOT}/launch"
RUNS="${FIXTURE_ROOT}/runs"
REPORT="${FIXTURE_ROOT}/report.md"
mkdir -p "${REPO}/.cenci" "${LAUNCH}" "${RUNS}" || {
    echo "FAIL: could not create the fixture directories" >&2
    exit 1
}
: > "${REPORT}" || exit 1

# ── Identity strings (what the sanitizer must remove) ───────────────────
# Resolved once, up front, so that EVERY line the runner prints -- stderr
# progress included -- can go through the same sanitizer as the final report.
HOST_NAME="$(hostname 2>/dev/null)" || HOST_NAME=""
if [[ -z "${HOST_NAME}" ]]; then
    HOST_NAME="$(uname -n 2>/dev/null)" || HOST_NAME=""
fi
# The short name (`hostname -s`) is redacted in addition to the full one.
HOST_SHORT="$(hostname -s 2>/dev/null)" || HOST_SHORT=""
USER_NAME="$(id -un 2>/dev/null)" || USER_NAME=""
if [[ -z "${USER_NAME}" ]]; then
    USER_NAME="${USER:-${LOGNAME:-}}"
fi
HOME_SAN="${HOME:-}"
HOME_REAL=""
if [[ -n "${HOME_SAN}" && -d "${HOME_SAN}" ]]; then
    HOME_REAL="$(cd "${HOME_SAN}" && pwd -P)" || HOME_REAL=""
fi
# A one-character home ("/") would redact every slash; treat it as unusable.
[[ "${#HOME_SAN}" -ge 2 ]] || HOME_SAN=""
[[ "${#HOME_REAL}" -ge 2 ]] || HOME_REAL=""
FIXTURE_REAL="$(cd "${FIXTURE_ROOT}" && pwd -P)" || FIXTURE_REAL="${FIXTURE_ROOT}"

IDENTITY_WARNINGS=()
[[ -n "${HOST_NAME}" ]] || IDENTITY_WARNINGS+=("the host name could not be determined, so it cannot be redacted")
[[ -n "${USER_NAME}" ]] || IDENTITY_WARNINGS+=("the user name could not be determined, so it cannot be redacted")
[[ -n "${HOME_SAN}" ]] || IDENTITY_WARNINGS+=("HOME is unset or unusable, so home paths cannot be redacted")

# san -- stdin -> stdout through the full redaction (home as given and as
# resolved by pwd -P, user, host, fixture root as given and as resolved).
san() {
    cnh_sanitize "${HOME_SAN}" "${USER_NAME}" "${HOST_NAME}" "${FIXTURE_ROOT}" \
        | cnh_sanitize "${HOME_REAL}" "" "${HOST_SHORT}" "${FIXTURE_REAL}"
}

# say <text> -- one sanitized line on stderr.
say() {
    printf '%s\n' "$*" | san >&2
}

for w in ${IDENTITY_WARNINGS[@]+"${IDENTITY_WARNINGS[@]}"}; do
    say "WARNING: ${w}"
done

# Host vs sandbox. The trust-bypass flag exists only in sandbox mode, mirroring
# what the cenci launcher passes there. The marker files are the library's
# hard-coded list: there is no environment override.
TRUST_FLAG="$(cnh_trust_bypass_flag)" || TRUST_FLAG=""
declare -a CODEX_MODE_ARGS=()
if [[ -n "${TRUST_FLAG}" ]]; then
    RUN_MODE="sandbox"
    TRUST_LABEL="hook trust bypassed by launcher (explicit flag) (flag passed by the runner's \`codex exec\` inside the sandbox)"
    CODEX_MODE_ARGS=("${TRUST_FLAG}" --dangerously-bypass-approvals-and-sandbox)
else
    RUN_MODE="host"
    TRUST_LABEL="host: hook trust as persisted through /hooks (no bypass flag)"
    CODEX_MODE_ARGS=(--sandbox workspace-write)
fi

N_OK=0
N_SCEN_OK=0
N_FAIL=0
N_INCONCLUSIVE=0
N_NOTRUN=0
SECTION="preflight"
HOOKS_INACTIVE=0
CANARY_STATE="not-run"
CANARY_DETAIL=""
REPRODUCED_127=0

rpt() {
    printf '%s\n' "$*" >> "${REPORT}"
}

# cell <text> -- one table cell: pipes escaped, newlines flattened.
cell() {
    local v="$1"
    v="${v//|/\\|}"
    v="${v//$'\n'/ }"
    printf '%s' "${v}"
}

# count_status <ok|FAIL|inconclusive|anything else = not run>
count_status() {
    case "$1" in
        ok)
            N_OK=$((N_OK + 1))
            if [[ "${SECTION}" == scenario ]]; then
                N_SCEN_OK=$((N_SCEN_OK + 1))
            fi
            ;;
        FAIL) N_FAIL=$((N_FAIL + 1)) ;;
        inconclusive) N_INCONCLUSIVE=$((N_INCONCLUSIVE + 1)) ;;
        *) N_NOTRUN=$((N_NOTRUN + 1)) ;;
    esac
    return 0
}

# record <label> <ok|FAIL|inconclusive|not run> <detail>
record() {
    local label="$1" status="$2" detail="${3:-}"
    count_status "${status}"
    printf '| %s | %s | %s |\n' "$(cell "${label}")" "${status}" "$(cell "${detail}")" >> "${REPORT}"
    say "${label}: ${status} -- ${detail}"
}

table_head() {
    rpt ""
    rpt "| $1 | result | detail |"
    rpt "|---|---|---|"
}

# ── Fixture ─────────────────────────────────────────────────────────────
SEED_AGENTS="# fixture"
SEED_MOVE="seed-move"

# reset_tree <dir> -- known seed state for every file a scenario touches.
reset_tree() {
    local d="$1"
    printf '%s\n' "${SEED_AGENTS}" > "${d}/AGENTS.md" || return 1
    printf 'seed-delete\n' > "${d}/rel-delete.txt" || return 1
    printf '%s\n' "${SEED_MOVE}" > "${d}/rel-move-a.txt" || return 1
    rm -f "${d}/rel-add.txt" "${d}/rel-move-b.txt"
}

fixture_init() {
    printf '{}\n' > "${REPO}/.cenci/config.json" || return 1
    reset_tree "${REPO}" || return 1
    git -C "${REPO}" init -q || return 1
    git -C "${REPO}" symbolic-ref HEAD refs/heads/main || return 1
    git -C "${REPO}" add -A || return 1
    git -C "${REPO}" -c user.name=cenci-native -c user.email=cenci-native@localhost \
        -c commit.gpgsign=false commit -q -m "seed" || return 1
    git -C "${REPO}" worktree add -q "${WT}" -b native-1 || return 1
    git -C "${LAUNCH}" init -q || return 1
}

# effect_landed <add|update|delete|move|file> <dir|path> -- did the change the
# scenario asked for land on disk? Echoes written (it landed) or absent.
effect_landed() {
    local kind="$1" d="$2" got=absent
    case "${kind}" in
        file) [[ -e "${d}" ]] && got=written ;;
        add) [[ -e "${d}/rel-add.txt" ]] && got=written ;;
        update) [[ "$(cat "${d}/AGENTS.md" 2>/dev/null)" != "${SEED_AGENTS}" ]] && got=written ;;
        delete) [[ ! -e "${d}/rel-delete.txt" ]] && got=written ;;
        move) [[ ! -e "${d}/rel-move-a.txt" && -e "${d}/rel-move-b.txt" ]] && got=written ;;
    esac
    echo "${got}"
}

# ── Codex runs and evidence ─────────────────────────────────────────────
SCEN_N=0
SCEN_DIR=""
EVENTS=""
TAP_DIR=""
CODEX_RC=0
ITEMS=""     # tool-call items of the current run, one JSON object per line
TAP_ALL=""   # every capture-tap line of the current run

# tool_items <events-file> -- one JSON object per tool-call item, narration
# (agent_message, reasoning) excluded: a model quoting a marker proves nothing.
tool_items() {
    jq -R -r 'fromjson?
        | select((.item // null) != null and ((.item.type // "") | IN("agent_message", "reasoning") | not))
        | .item | tojson' "$1" 2>/dev/null
}

# tap_cat -- every capture-tap line of the current run. Each guard call writes
# its own regular file (guard-main-worktree.XXXXXX / check-sensitive-files.XXXXXX).
tap_cat() {
    local f
    for f in "${TAP_DIR}"/guard-main-worktree.* "${TAP_DIR}"/check-sensitive-files.*; do
        if [[ -f "${f}" && ! -L "${f}" ]]; then
            cat -- "${f}"
        fi
    done
    return 0
}

# run_codex <session-cwd> <prompt> [extra codex exec args...] -- one `codex exec
# --json` run with the capture tap pointed at a fresh owner-only directory.
# TMPDIR is already unset for this whole process. ITEMS and TAP_ALL are
# captured once, here, so that no later check pipes into `grep -q` (under
# pipefail an early-exiting grep SIGPIPEs the producer and flips the status).
run_codex() {
    local cwd="$1" prompt="$2"
    shift 2
    local -a timeout_cmd=()
    SCEN_N=$((SCEN_N + 1))
    SCEN_DIR="${RUNS}/${SCEN_N}"
    TAP_DIR="${SCEN_DIR}/tap"
    EVENTS="${SCEN_DIR}/events.jsonl"
    ITEMS=""
    TAP_ALL=""
    mkdir -p "${TAP_DIR}" || return 1
    # The guards skip the tap in a group/world-writable directory.
    chmod 700 "${TAP_DIR}" || return 1
    if command -v timeout >/dev/null 2>&1; then
        timeout_cmd=(timeout "${CNH_RUN_TIMEOUT:-300}")
    fi
    CENCI_CODEX_HOOK_CAPTURE_DIR="${TAP_DIR}" \
        ${timeout_cmd[@]+"${timeout_cmd[@]}"} \
        codex exec --json --skip-git-repo-check -C "${cwd}" \
        "${CODEX_MODE_ARGS[@]}" "$@" "${prompt}" \
        > "${EVENTS}" 2> "${SCEN_DIR}/stderr" < /dev/null
    CODEX_RC=$?
    ITEMS="$(tool_items "${EVENTS}")" || ITEMS=""
    TAP_ALL="$(tap_cat)" || TAP_ALL=""
    return 0
}

# item_has <needle> -- does some single tool-call item contain <needle>?
item_has() {
    [[ -n "$1" ]] || return 1
    grep -F -- "$1" <<< "${ITEMS}" >/dev/null
}

# tap_seen_yes_no <tool_name> -- did a guard's capture tap see a PreToolUse
# payload for <tool_name> (a hook ran for it)?
tap_seen_yes_no() {
    if [[ -n "${TAP_ALL}" ]] \
        && jq -e -s --arg t "$1" 'any(.[]; .tool_name == $t)' <<< "${TAP_ALL}" >/dev/null 2>&1; then
        echo yes
    else
        echo no
    fi
}

# attempted_yes_no <needle> <tool_name> -- yes when a tool-call event carries
# the scenario's unique needle, or the tap saw a PreToolUse payload for the
# expected tool.
attempted_yes_no() {
    if item_has "$1"; then
        echo yes
    else
        tap_seen_yes_no "$2"
    fi
}

# Hook-block evidence. "status":"declined" is NOT in this list: a declined item
# says the tool did not run, not why, so it cannot be told apart from a user or
# policy decline.
BLOCK_RE='BLOCKED:|Refusing to write|targets the main worktree|[Bb]locked by.*[Hh]ook|[Hh]ook.*[Bb]locked'

# blocked_yes_no <needle> -- did the SAME tool-call item that carries the
# scenario's target report a hook block? A block message in some other item
# (another command, an unrelated error) proves nothing about this call.
blocked_yes_no() {
    if [[ -n "$1" ]] && grep -F -- "$1" <<< "${ITEMS}" | grep -E -- "${BLOCK_RE}" >/dev/null; then
        echo yes
    else
        echo no
    fi
}

# message_seen_yes_no <needle> <message> -- is <message> in the same item as
# <needle>?
message_seen_yes_no() {
    if [[ -n "$1" && -n "$2" ]] && grep -F -- "$1" <<< "${ITEMS}" | grep -F -- "$2" >/dev/null; then
        echo yes
    else
        echo no
    fi
}

# tap_summary <tool_name> -- "payload cwd=..., hook pwd=..., workdir in
# tool_input=..." from the last tap line for that tool, or "no tap line".
tap_summary() {
    local line
    line="$(jq -c -s --arg t "$1" '[.[] | select(.tool_name == $t)] | last // empty' <<< "${TAP_ALL}" 2>/dev/null)" || line=""
    if [[ -z "${line}" ]]; then
        echo "no tap line"
        return 0
    fi
    jq -r '"payload cwd=" + (.payload_cwd | tostring) + ", hook pwd=" + .hook_pwd + ", workdir in tool_input=" + (.tool_input_has_workdir | tostring)' <<< "${line}" 2>/dev/null \
        || echo "unreadable tap line"
}

# ── Prompts ─────────────────────────────────────────────────────────────
STRICT="Do not retry, do not run any other command, and do not explain."

prompt_bash() { # <command> [workdir]
    if [[ -n "${2:-}" ]]; then
        printf 'Make exactly one tool call and nothing else: call the shell tool (exec_command) once with workdir %s and exactly this command: %s\n%s' "$2" "$1" "${STRICT}"
    else
        printf 'Make exactly one tool call and nothing else: call the shell tool (exec_command) once with exactly this command: %s\n%s' "$1" "${STRICT}"
    fi
}

prompt_patch() { # <patch text>
    printf 'Make exactly one tool call and nothing else: call the apply_patch tool (the dedicated tool, not the shell) once with exactly this patch as its input:\n%s\n%s' "$1" "${STRICT}"
}

prompt_heredoc() { # <patch text> <workdir>
    printf 'Make exactly one tool call and nothing else: call the shell tool (exec_command) once with workdir %s and exactly this command:\napply_patch <<'"'"'EOF'"'"'\n%s\nEOF\n%s' "$2" "$1" "${STRICT}"
}

# make_patch <add|update|delete|move|file-add> <prefix-or-path> -- an apply_patch
# envelope whose declared targets are <prefix>rel-*.txt / <prefix>AGENTS.md
# (<prefix> is empty, a relative dir ending in "/", or an absolute dir ending
# in "/"). file-add takes the full target path instead.
make_patch() {
    local kind="$1" p="$2"
    case "${kind}" in
        file-add) printf '*** Begin Patch\n*** Add File: %s\n+native\n*** End Patch' "${p}" ;;
        add) printf '*** Begin Patch\n*** Add File: %srel-add.txt\n+native\n*** End Patch' "${p}" ;;
        update) printf '*** Begin Patch\n*** Update File: %sAGENTS.md\n@@\n-%s\n+%s changed\n*** End Patch' "${p}" "${SEED_AGENTS}" "${SEED_AGENTS}" ;;
        delete) printf '*** Begin Patch\n*** Delete File: %srel-delete.txt\n*** End Patch' "${p}" ;;
        move) printf '*** Begin Patch\n*** Update File: %srel-move-a.txt\n*** Move to: %srel-move-b.txt\n@@\n-%s\n+%s moved\n*** End Patch' "${p}" "${p}" "${SEED_MOVE}" "${SEED_MOVE}" ;;
    esac
}

# ── Scenario judge ──────────────────────────────────────────────────────
# Scenario spec, set by the caller before run_scenario:
S_LABEL=""
S_EXPECT=""     # deny | allow
S_KIND=""       # effect_landed kind
S_WHERE=""      # effect_landed dir / path
S_SESSION=""    # codex -C directory
S_PROMPT=""
S_NEEDLE=""     # unique text a tool-call event for this scenario carries
S_TOOL=""       # tool_name the PreToolUse payload should carry
S_MESSAGE=""    # optional text the guard's block message must carry
declare -a S_EXTRA=()

# extra_for_worktree -- worktree sessions need the repo root writable under the
# host sandbox so that only a hook can stop a main-worktree write.
extra_for_worktree() {
    if [[ "${RUN_MODE}" == host ]]; then
        S_EXTRA=(--add-dir "${REPO}")
    else
        S_EXTRA=()
    fi
}

extra_for_root() { S_EXTRA=(); }

# run_scenario -- run the current spec (at most twice) and record one verdict.
run_scenario() {
    local attempt verdict="inconclusive" attempted="no" landed="absent" blocked="no" detail status
    for attempt in 1 2; do
        reset_tree "${REPO}" && reset_tree "${WT}" || {
            record "${S_LABEL}" "inconclusive" "could not reset the fixture"
            return 0
        }
        rm -rf "${REPO}"/main-*.txt "${WT}"/control-*.txt "${WT}"/bash-sens "${WT}"/patch-sens
        if ! mkdir -p "${WT}/bash-sens"; then
            record "${S_LABEL}" "inconclusive" "could not prepare the fixture directory bash-sens"
            return 0
        fi
        run_codex "${S_SESSION}" "${S_PROMPT}" ${S_EXTRA[@]+"${S_EXTRA[@]}"} || {
            record "${S_LABEL}" "inconclusive" "could not start codex exec"
            return 0
        }
        attempted="$(attempted_yes_no "${S_NEEDLE}" "${S_TOOL}")"
        landed="$(effect_landed "${S_KIND}" "${S_WHERE}")"
        blocked="$(blocked_yes_no "${S_NEEDLE}")"
        verdict="$(cnh_classify_verdict "${S_EXPECT}" "${attempted}" "${landed}" "${blocked}")"
        [[ "${verdict}" != inconclusive ]] && break
    done
    detail="verdict=${verdict} (attempted=${attempted}, effect=${landed}, hook-block=${blocked}, codex rc=${CODEX_RC}); tap: $(tap_summary "${S_TOOL}")"
    status="inconclusive"
    case "${S_EXPECT}:${verdict}" in
        deny:denied)
            if [[ -n "${S_MESSAGE}" && "$(message_seen_yes_no "${S_NEEDLE}" "${S_MESSAGE}")" != yes ]]; then
                detail="a block was reported but the guard message '${S_MESSAGE}' is not in the tool-call item for this target, so it cannot be attributed to the guard (${detail})"
            else
                status="ok"
            fi
            ;;
        allow:allowed)
            if [[ "$(tap_seen_yes_no "${S_TOOL}")" == yes ]]; then
                status="ok"
            else
                detail="the write landed but no capture-tap line shows a hook ran for it, so the allow is not evidence of anything (${detail})"
            fi
            ;;
        deny:hooks-inactive)
            if [[ "${HOOKS_INACTIVE_PROBE:-0}" == 1 ]]; then
                HOOKS_INACTIVE=1
                status="not run"
                detail="hooks pending/untrusted -- run /hooks (${detail})"
            else
                status="FAIL"
                detail="a denied call's target was written with no hook block (${detail})"
            fi
            ;;
        allow:denied) status="FAIL"; detail="a call that must be allowed was blocked (${detail})" ;;
    esac
    if [[ "${HOOKS_INACTIVE_PROBE:-0}" == 1 ]]; then
        case "${status}" in
            ok) CANARY_STATE="active" ;;
            "not run") CANARY_STATE="inactive" ;;
            *) CANARY_STATE="unknown" ;;
        esac
        CANARY_DETAIL="${detail}"
    fi
    record "${S_LABEL}" "${status}" "${detail}"
}

# skip_unproven <label> -- when the canary has not shown the hooks active,
# record a "not run" row and return 0 (the caller must skip the scenario).
# Neither a deny nor an allow outcome proves anything with no hook watching.
skip_unproven() {
    case "${CANARY_STATE}" in
        active) return 1 ;;
        inactive) record "$1" "not run" "hooks pending/untrusted -- run /hooks" ;;
        *) record "$1" "not run" "trust state ${CANARY_STATE}: the canary did not show the hooks are active, so a deny or an allow outcome here would prove nothing" ;;
    esac
    return 0
}

# ── Header ──────────────────────────────────────────────────────────────
PLUGIN_COMMIT="$(git -C "${FLOW_DIR}" rev-parse HEAD 2>/dev/null)" || PLUGIN_COMMIT="unknown"
PLUGIN_VERSION="$(jq -r '.version // "unknown"' "${FLOW_DIR}/.codex-plugin/plugin.json" 2>/dev/null)" || PLUGIN_VERSION="unknown"
LOGIN_SHELL="${SHELL:-}"

rpt "## Native Codex run: ${RUN_MODE}"
rpt ""
rpt "| Field | Value |"
rpt "|---|---|"
rpt "| Codex version | $(cell "${CODEX_VERSION_OUT}") |"
rpt "| Plugin version / commit (runner checkout) | ${PLUGIN_VERSION} / ${PLUGIN_COMMIT} |"
rpt "| Mode | ${RUN_MODE} |"
rpt "| Hook trust | $(cell "${TRUST_LABEL}") |"
rpt "| \$SHELL | $(cell "${LOGIN_SHELL:-<unset>}") |"
rpt "| Fixture | ${FIXTURE_ROOT} |"
for w in ${IDENTITY_WARNINGS[@]+"${IDENTITY_WARNINGS[@]}"}; do
    rpt "| Redaction warning | ${w} |"
done

if ! fixture_init; then
    echo "FAIL: could not build the fixture repo" >&2
    exit 1
fi

# ── Preflight / 127 inspection ──────────────────────────────────────────
rpt ""
rpt "### Preflight and 127 inspection"
rpt ""

case "${LOGIN_SHELL##*/}" in
    fish) rpt "- \$SHELL is fish: hook commands using \`\${PLUGIN_ROOT}\` are parsed by fish, not a POSIX shell (flagged)." ;;
    "") rpt "- \$SHELL is unset: the login shell Codex falls back to is not known to this runner (flagged)." ;;
esac
PROBE_SHELL="${LOGIN_SHELL:-/bin/sh}"
for tool in bash jq awk; do
    if tool_path="$("${PROBE_SHELL}" -lc "command -v ${tool}" 2>/dev/null)" && [[ -n "${tool_path}" ]]; then
        rpt "- \`${PROBE_SHELL} -lc 'command -v ${tool}'\`: ${tool_path}"
    else
        rpt "- \`${PROBE_SHELL} -lc 'command -v ${tool}'\`: NOT FOUND on the login-shell PATH"
    fi
done

# plugin_root_canonical <root> -- echo the canonical root and return 0 only when
# it is a directory whose .codex-plugin/plugin.json names the cenci flow plugin
# and which carries a codex/hooks.json. Anything else under CODEX_HOME is some
# other plugin's code and is never executed. Sets ROOT_REJECT_REASON.
ROOT_REJECT_REASON=""
plugin_root_canonical() {
    local root="$1" canon name
    ROOT_REJECT_REASON=""
    if [[ ! -d "${root}" ]]; then
        ROOT_REJECT_REASON="not a directory"
        return 1
    fi
    if ! canon="$(cd "${root}" && pwd -P)"; then
        ROOT_REJECT_REASON="cannot be canonicalized"
        return 1
    fi
    if [[ ! -f "${canon}/.codex-plugin/plugin.json" ]]; then
        ROOT_REJECT_REASON="no .codex-plugin/plugin.json"
        return 1
    fi
    if ! name="$(jq -r '.name // ""' "${canon}/.codex-plugin/plugin.json" 2>/dev/null)"; then
        ROOT_REJECT_REASON="unreadable .codex-plugin/plugin.json"
        return 1
    fi
    if [[ "${name}" != "${FLOW_PLUGIN_NAME}" ]]; then
        ROOT_REJECT_REASON="plugin name is not ${FLOW_PLUGIN_NAME}"
        return 1
    fi
    if [[ ! -f "${canon}/codex/hooks.json" ]]; then
        ROOT_REJECT_REASON="no codex/hooks.json"
        return 1
    fi
    printf '%s\n' "${canon}"
}

CODEX_HOME_DIR="${CODEX_HOME:-${HOME:-}/.codex}"
declare -a PLUGIN_ROOTS=()
declare -a ROOT_NOTES=()
OWN_ROOT_USED=0

# add_root <label> <candidate> -- accept <candidate> as a handler root when it
# is the cenci flow plugin; otherwise note why not. Duplicates are ignored.
add_root() {
    local canon r
    if ! canon="$(plugin_root_canonical "$2")"; then
        plugin_root_canonical "$2" >/dev/null 2>&1
        ROOT_NOTES+=("$1: not re-run (${ROOT_REJECT_REASON})")
        return 0
    fi
    for r in ${PLUGIN_ROOTS[@]+"${PLUGIN_ROOTS[@]}"}; do
        [[ "${r}" == "${canon}" ]] && return 0
    done
    PLUGIN_ROOTS+=("${canon}")
    return 0
}

add_root "runner checkout" "${FLOW_DIR}"
OWN_ROOT="${PLUGIN_ROOTS[0]:-}"
FOUND_LIST=""
FIND_RC=0
if [[ -d "${CODEX_HOME_DIR}" ]]; then
    FOUND_LIST="$(find "${CODEX_HOME_DIR}" -maxdepth 8 -path '*/codex/hooks.json' 2>/dev/null)"
    FIND_RC=$?
fi
N_FOUND=0
while IFS= read -r found; do
    [[ -n "${found}" ]] || continue
    N_FOUND=$((N_FOUND + 1))
    [[ "${N_FOUND}" -le 20 ]] || break
    add_root "${found%/codex/hooks.json}" "${found%/codex/hooks.json}"
done <<< "${FOUND_LIST}"
[[ "${FIND_RC}" -eq 0 ]] || ROOT_NOTES+=("scanning CODEX_HOME for plugin hook manifests hit errors (find exited ${FIND_RC}); the list may be partial")

PLUGIN_LIST_RAW="$(codex plugin list 2>&1)"
PLUGIN_LIST_RC=$?
PLUGIN_LIST="$(printf '%s\n' "${PLUGIN_LIST_RAW}" | sed -n '1,20p')"
if [[ "${PLUGIN_LIST_RC}" -ne 0 ]]; then
    PLUGIN_LIST="(codex plugin list exited ${PLUGIN_LIST_RC})"$'\n'"${PLUGIN_LIST}"
fi
rpt "- \`codex plugin list\`:"
rpt '```text'
rpt "${PLUGIN_LIST}"
rpt '```'
for note in ${ROOT_NOTES[@]+"${ROOT_NOTES[@]}"}; do
    rpt "- plugin root ${note}"
done

PRE_ALLOW_CMD="printf x > ${WT}/preflight-allow.txt"
PRE_DENY_CMD="printf x > ${REPO}/.env"
payload_for() { # <event> [command]
    jq -n --arg cwd "${REPO}" --arg ev "$1" --arg cmd "${2:-}" \
        'if $ev == "PreToolUse"
         then {cwd: $cwd, hook_event_name: $ev, tool_name: "Bash", tool_input: {command: $cmd}}
         else {cwd: $cwd, hook_event_name: $ev} end'
}

# rerun_handler <root> <command> <payload> -- run one handler the way Codex
# launches it: `$SHELL -lc "<command>"` with PLUGIN_ROOT exported (the mechanism
# plugin-bundled hooks get). The command text is NOT rewritten: <command> has
# already been matched against HANDLER_RE, and the login shell expands
# ${PLUGIN_ROOT} itself. The payload arrives on stdin through a here-string, so
# a handler that exits without reading cannot SIGPIPE the writer. Echoes
# "rc|first stderr line".
rerun_handler() {
    local root="$1" cmd="$2" payload="$3" err rc first
    err="${SCEN_DIR:-${RUNS}}/preflight.err"
    (
        cd "${REPO}" || exit 97
        export PLUGIN_ROOT="${root}"
        "${PROBE_SHELL}" -lc "${cmd}" <<< "${payload}" > /dev/null 2> "${err}"
    )
    rc=$?
    first=""
    IFS= read -r first < "${err}" || true
    printf '%s|%s' "${rc}" "${first}"
}

# The only handler shape re-run: a quoted ${PLUGIN_ROOT}-anchored script under
# hooks/scripts or codex, optionally followed by plain words. Every command in
# flow/codex/hooks.json has this shape; anything else is reported, not executed.
HANDLER_RE='^"\$\{PLUGIN_ROOT\}/(hooks/scripts|codex)/([A-Za-z0-9._-]+\.sh)"( [A-Za-z0-9._-]+)*$'
DENY_FIRST_LINE_RE='^BLOCKED: .*(Refusing to write|targets the main worktree)'
HANDLERS_RUN=0
MANIFEST_ERRORS=0

table_head "handler (root)"
for root in ${PLUGIN_ROOTS[@]+"${PLUGIN_ROOTS[@]}"}; do
    manifest="${root}/codex/hooks.json"
    if ! handlers="$(jq -r '.hooks | to_entries[] | .key as $e | .value[].hooks[] | [$e, .command] | @tsv' "${manifest}" 2>/dev/null)"; then
        MANIFEST_ERRORS=$((MANIFEST_ERRORS + 1))
        record "${root}: codex/hooks.json" "inconclusive" "manifest unreadable or not the expected shape (jq failed); no handler of this root was run"
        continue
    fi
    handlers="$(printf '%s\n' "${handlers}" | awk '!seen[$0]++')"
    while IFS=$'\t' read -r ev cmd; do
        [[ -n "${ev}" ]] || continue
        if ! [[ "${cmd}" =~ ${HANDLER_RE} ]]; then
            record "${ev} handler (${root##*/})" "inconclusive" "command shape not recognized, so it was not executed: ${cmd//[[:cntrl:]]/?}"
            continue
        fi
        script="${BASH_REMATCH[1]}/${BASH_REMATCH[2]}"
        exists="missing"
        [[ -f "${root}/${script}" ]] && exists="present"
        execbit="not executable"
        [[ -x "${root}/${script}" ]] && execbit="executable"
        # A symlinked handler is refused outright: its target is not covered by
        # the root check, and reading or running it could reach any file the
        # user can. This runs before the shebang read, so a link out of the root
        # is never opened. A MISSING script is still launched (that is how a 127
        # is reproduced).
        if [[ -L "${root}/${script}" ]]; then
            record "${ev} ${script} (${root##*/})" "inconclusive" "the script is a symbolic link, so it was not executed"
            continue
        fi
        if [[ "${exists}" == present ]]; then
            script_dir="$(cd "${root}/${script%/*}" && pwd -P)" || script_dir=""
            if [[ "${script_dir}" != "${root}"/* ]]; then
                record "${ev} ${script} (${root##*/})" "inconclusive" "the script resolves outside the plugin root, so it was not executed"
                continue
            fi
        fi
        shebang=""
        [[ "${exists}" == present ]] && { IFS= read -r shebang < "${root}/${script}" || true; }
        if [[ "${ev}" == PreToolUse ]]; then
            payloads=("allow|$(payload_for PreToolUse "${PRE_ALLOW_CMD}")" "deny|$(payload_for PreToolUse "${PRE_DENY_CMD}")")
        else
            payloads=("event|$(payload_for "${ev}")")
        fi
        for entry in "${payloads[@]}"; do
            res="$(rerun_handler "${root}" "${cmd}" "${entry#*|}")"
            HANDLERS_RUN=$((HANDLERS_RUN + 1))
            rc="${res%%|*}"
            first_line="${res#*|}"
            status="ok"
            if [[ "${rc}" == 127 ]]; then
                REPRODUCED_127=1
                status="FAIL"
            elif [[ "${entry%%|*}" == deny ]]; then
                # Exit 2 alone is not a denial: a missing jq exits 2 as well.
                if [[ "${rc}" != 2 ]] || ! [[ "${first_line}" =~ ${DENY_FIRST_LINE_RE} ]]; then
                    status="FAIL"
                fi
            elif [[ "${rc}" != 0 ]]; then
                status="FAIL"
            fi
            record "${ev} ${script} [${entry%%|*}] (${root##*/})" "${status}" "script ${exists}, ${execbit}, shebang '${shebang}', rc=${rc}, stderr: ${first_line}"
        done
    done <<< "${handlers}"
done

ROOT_LIST=""
for root in ${PLUGIN_ROOTS[@]+"${PLUGIN_ROOTS[@]}"}; do
    ROOT_LIST="${ROOT_LIST:+${ROOT_LIST}, }\`${root}\`"
done
ROOT_NOTE=""
if [[ "${#PLUGIN_ROOTS[@]}" -eq 1 && "${PLUGIN_ROOTS[0]}" == "${OWN_ROOT}" && -n "${OWN_ROOT}" ]]; then
    ROOT_NOTE=" The only root inspected is the runner's own checkout, not an installed plugin copy: it says nothing about the installed plugin's launch."
fi
rpt ""
if [[ "${REPRODUCED_127}" == 1 ]]; then
    rpt "- 127 REPRODUCED by the preflight (see the FAIL rows above for the resolved root, script presence, exec bit and stderr). Roots inspected: ${ROOT_LIST}.${ROOT_NOTE}"
elif [[ "${HANDLERS_RUN}" -eq 0 || "${MANIFEST_ERRORS}" -gt 0 ]]; then
    N_INCONCLUSIVE=$((N_INCONCLUSIVE + 1))
    rpt "- 127 INCONCLUSIVE: no handlers executed (handlers run: ${HANDLERS_RUN}, unreadable manifests: ${MANIFEST_ERRORS}). Roots inspected: ${ROOT_LIST:-none}.${ROOT_NOTE} The preflight neither reproduced nor ruled out the report."
else
    rpt "- 127 NOT reproduced by the preflight: none of ${HANDLERS_RUN} handler runs through \`${PROBE_SHELL} -lc\` exited 127. Roots inspected: ${ROOT_LIST}.${ROOT_NOTE} That is not evidence the report is fixed."
fi

# ── Deny matrix (the first row is the canary) ───────────────────────────
SECTION="scenario"
rpt ""
rpt "### Deny matrix"
table_head "scenario"

# Canary: an absolute main-worktree write through Bash. A write that lands
# with no hook block means the plugin hooks are not active (typically pending
# /hooks trust on the host). Until it shows a denied call, nothing else below
# that depends on the guards runs.
extra_for_root
S_LABEL="canary: bash write to the main worktree (deny)"
S_EXPECT="deny" S_KIND="file" S_WHERE="${REPO}/main-canary.txt" S_SESSION="${REPO}"
S_NEEDLE="main-canary.txt" S_TOOL="Bash" S_MESSAGE="targets the main worktree"
S_PROMPT="$(prompt_bash "printf x > ${S_WHERE}")"
HOOKS_INACTIVE_PROBE=1
run_scenario
HOOKS_INACTIVE_PROBE=0

# deny_row <label> <expect> <tool> <target> <session> <message> <prompt>
deny_row() {
    if skip_unproven "$1"; then
        return 0
    fi
    S_LABEL="$1" S_EXPECT="$2" S_KIND="file" S_WHERE="$4" S_SESSION="$5"
    S_NEEDLE="${4##*/}" S_TOOL="$3" S_MESSAGE="$6" S_PROMPT="$7"
    extra_for_root
    run_scenario
}

BASH_SENS="${WT}/bash-sens/.env"
PATCH_SENS="${WT}/patch-sens/.env"
deny_row "bash -> sensitive .env in a worktree (deny, check-sensitive-files)" deny Bash "${BASH_SENS}" "${REPO}" "Refusing to write" \
    "$(prompt_bash "printf x > ${BASH_SENS}")"
deny_row "bash -> main worktree file (deny, guard-main-worktree)" deny Bash "${REPO}/main-bash.txt" "${REPO}" "targets the main worktree" \
    "$(prompt_bash "printf x > ${REPO}/main-bash.txt")"
deny_row "bash -> worktree file (positive control, allow)" allow Bash "${WT}/control-bash.txt" "${REPO}" "" \
    "$(prompt_bash "printf x > ${WT}/control-bash.txt")"
deny_row "apply_patch -> sensitive .env in a worktree (deny, check-sensitive-files)" deny apply_patch "${PATCH_SENS}" "${REPO}" "Refusing to write" \
    "$(prompt_patch "$(make_patch file-add "${PATCH_SENS}")")"
deny_row "apply_patch -> main worktree file (deny, guard-main-worktree)" deny apply_patch "${REPO}/main-patch.txt" "${REPO}" "targets the main worktree" \
    "$(prompt_patch "$(make_patch file-add "${REPO}/main-patch.txt")")"
deny_row "apply_patch -> worktree file (positive control, allow)" allow apply_patch "${WT}/control-patch.txt" "${REPO}" "" \
    "$(prompt_patch "$(make_patch file-add "${WT}/control-patch.txt")")"

# ── Guard canary preflight (#1213) ──────────────────────────────────────
# What the mutating Codex workflows run before their first mutation:
# guard-canary.sh plan -> one `codex exec` per probe from a feature-worktree
# session -> guard-canary.sh check. Each probe is judged here exactly like a
# deny-matrix row (same-item block evidence + the guard's own message + the
# filesystem), and the helper prints active or refused. The result is compared
# with the trust state the deny-matrix canary established (CANARY_STATE):
#   active   -> the preflight must pass; a refusal is a FAIL
#   inactive -> the preflight must refuse; a pass is a FAIL
#   unknown  -> nothing to compare against: inconclusive
# Variants: "as launched" (host: persisted /hooks trust; sandbox: with the
# launcher's trust-bypass flag) and "trust withheld" (sandbox only: the same
# calls without --dangerously-bypass-hook-trust, inside the disposable
# container). The host never withholds trust (cenci never changes it): the
# variant is recorded as not run, with the trust state observed above.
SECTION="canary"
rpt ""
rpt "### Guard canary preflight"
table_head "variant"

CANARY_SH="${SCRIPT_DIR}/guard-canary.sh"
CANARY_AL_RESULT="not run"
CANARY_WH_RESULT="not run"
CANARY_RESULT=""   # result of the last canary_variant: ok | FAIL | inconclusive

# canary_guard_message <guard> -- the text that guard's own refusal carries.
canary_guard_message() {
    case "$1" in
        check-sensitive-files) printf '%s' 'Refusing to write' ;;
        guard-main-worktree) printf '%s' 'targets the main worktree' ;;
    esac
}

# canary_variant <label> <with|without> <expected: active|refused|refused-soft|unknown>
# -- one full preflight. "without" leaves out only --dangerously-bypass-hook-trust
# (the approvals/sandbox bypass stays). Records one row and sets CANARY_RESULT.
canary_variant() {
    local label="$1" trust="$2" expected="$3"
    local plan_out plan_rc run_dir kind pn ptool pguard pgroup ptarget needle tool_name
    local attempted landed blocked seen verdict check_out check_rc got status detail
    local likely n_removed n_left first_line line probes=""
    local -a saved_mode=("${CODEX_MODE_ARGS[@]}") results=()
    CANARY_RESULT="inconclusive"

    if [[ "${trust}" == without ]]; then
        CODEX_MODE_ARGS=(--dangerously-bypass-approvals-and-sandbox)
    fi

    plan_out="$(cd "${WT}" && bash "${CANARY_SH}" plan 2>&1)"
    plan_rc=$?
    run_dir=""
    if [[ "${plan_rc}" -eq 0 ]]; then
        while IFS= read -r line; do
            case "${line}" in
                CANARY_RUN_DIR=*) run_dir="${line#CANARY_RUN_DIR=}" ;;
            esac
        done <<< "${plan_out}"
    fi

    if [[ "${plan_rc}" -eq 0 && -n "${run_dir}" ]]; then
        while IFS=$'\t' read -r kind pn ptool pguard pgroup ptarget; do
            [[ "${kind}" == PROBE ]] || continue
            needle="${ptarget##*/}"
            if [[ "${ptool}" == shell ]]; then
                tool_name="Bash"
                S_PROMPT="$(prompt_bash "printf x > ${ptarget}")"
            else
                tool_name="apply_patch"
                S_PROMPT="$(prompt_patch "$(make_patch file-add "${ptarget}")")"
            fi
            extra_for_worktree
            run_codex "${WT}" "${S_PROMPT}" ${S_EXTRA[@]+"${S_EXTRA[@]}"} || {
                results+=("${pn}=not-denied")
                probes="${probes:+${probes}, }${pn}=not-denied (could not start codex exec)"
                continue
            }
            attempted="$(attempted_yes_no "${needle}" "${tool_name}")"
            landed="$(effect_landed file "${ptarget}")"
            blocked="$(blocked_yes_no "${needle}")"
            seen="$(message_seen_yes_no "${needle}" "$(canary_guard_message "${pguard}")")"
            verdict="$(cnh_classify_verdict deny "${attempted}" "${landed}" "${blocked}")"
            if [[ "${verdict}" == denied && "${seen}" == yes ]]; then
                results+=("${pn}=denied")
                probes="${probes:+${probes}, }${pn}=denied (${ptool}, ${pguard})"
            else
                results+=("${pn}=not-denied")
                probes="${probes:+${probes}, }${pn}=not-denied (${ptool}, ${pguard}; attempted=${attempted}, effect=${landed}, hook-block=${blocked}, guard-message=${seen})"
            fi
        done <<< "${plan_out}"
        check_out="$(cd "${WT}" && bash "${CANARY_SH}" check "${run_dir}" ${results[@]+"${results[@]}"} 2>&1)"
        check_rc=$?
    else
        # A plan that cannot be made is itself the canary's refusal.
        check_out="${plan_out}"
        check_rc=1
        probes="no probe was run: the plan was refused"
    fi
    CODEX_MODE_ARGS=("${saved_mode[@]}")

    got="unknown"
    while IFS= read -r line; do
        case "${line}" in
            CANARY_STATUS=active) got="active" ;;
            CANARY_STATUS=refused) got="refused" ;;
        esac
    done <<< "${check_out}"
    n_removed="$(grep -c '^REMOVED: ' <<< "${check_out}")"
    n_left="$(grep -c '^LEFTOVER: ' <<< "${check_out}")"
    likely="$(grep -m 1 '^Likely cause:' <<< "${check_out}")" || likely=""
    first_line="$(grep -m 1 -E '^(Guard canary refused|GUARD CANARY REFUSED)' <<< "${check_out}")" || first_line=""

    status="inconclusive"
    case "${expected}:${got}" in
        active:active | refused:refused | refused-soft:refused) status="ok" ;;
        active:refused | refused:active) status="FAIL" ;;
    esac
    detail="CANARY_STATUS=${got} (helper rc ${check_rc}, expected ${expected}); probes: ${probes}; cleanup: removed ${n_removed} written target(s), ${n_left} leftover(s)"
    if [[ -n "${first_line}" ]]; then detail="${detail}; ${first_line}"; fi
    if [[ -n "${likely}" ]]; then detail="${detail}; ${likely}"; fi
    if [[ "${RUN_MODE}" == sandbox ]]; then detail="${detail}; hook trust bypassed by launcher (explicit flag)"; fi
    case "${status}" in
        FAIL) detail="the result contradicts the trust state the deny-matrix canary established (${detail})" ;;
        inconclusive)
            if [[ "${expected}" == refused-soft ]]; then
                detail="the hooks were active even with the trust flag left out (trust is already persisted in this container), so the refusal path was not exercised (${detail})"
            else
                detail="no comparison is possible against trust state ${CANARY_STATE} (${detail})"
            fi
            ;;
    esac
    CANARY_RESULT="${status}"
    record "${label}" "${status}" "${detail}"
    rpt ""
    rpt "- ${label}: helper output"
    rpt '```text'
    rpt "${check_out}"
    rpt '```'
    return 0
}

case "${CANARY_STATE}" in
    active) EXPECT_AL="active" ;;
    inactive) EXPECT_AL="refused" ;;
    *) EXPECT_AL="unknown" ;;
esac
canary_variant "guard canary preflight (as launched)" with "${EXPECT_AL}"
CANARY_AL_RESULT="${CANARY_RESULT}"
if [[ "${RUN_MODE}" == sandbox ]]; then
    # Trust withheld inside the container: with the flag left out the plugin
    # hooks are pending, so the preflight must refuse. An active result here
    # means the container already had the hooks trusted persistently, which
    # leaves the refusal path unexercised.
    canary_variant "guard canary preflight (trust withheld)" without refused-soft
    CANARY_WH_RESULT="${CANARY_RESULT}"
else
    record "guard canary preflight (trust withheld)" "not run" "sandbox-only: the host variant would have to change persisted hook trust, which cenci never does; persisted trust state as observed by the as-launched run above: ${CANARY_STATE}"
fi
SECTION="scenario"

# ── cwd capture ─────────────────────────────────────────────────────────
rpt ""
rpt "### cwd capture (payload cwd / hook pwd / effective cwd)"
rpt ""
rpt "| way | payload cwd, hook pwd, workdir in tool_input | effective cwd | result |"
rpt "|---|---|---|---|"

declare -a CWD_ROW_EXTRA=()

# cwd_row <label> <session> <prompt> <probe-file-name> <tool> <content|location>
# The probe file lands in the worktree (absolute `pwd > ...` redirect, which the
# guards allow) or wherever a relative apply_patch Add resolved. The EFFECTIVE
# cwd is derived from the filesystem, never from file content that the call
# itself chose:
#   content   the probe holds `pwd` as run inside the tool call
#   location  the effective cwd is the directory the probe was found in
# A row is `ok` only with a capture-tap line for the tool: without one no hook
# ran, so the payload cwd and hook pwd columns would be empty.
cwd_row() {
    local label="$1" session="$2" prompt="$3" probe_name="$4" tool="$5" how="$6"
    local effective="not observed" found="" d attempt tapsum tap_seen status="inconclusive" note=""
    if [[ "${HOOKS_INACTIVE}" == 1 ]]; then
        printf '| %s | - | - | not run: hooks pending/untrusted -- run /hooks |\n' "$(cell "${label}")" >> "${REPORT}"
        count_status "not run"
        return 0
    fi
    for attempt in 1 2; do
        rm -f "${WT}/${probe_name}" "${REPO}/${probe_name}"
        extra_for_worktree
        run_codex "${session}" "${prompt}" ${S_EXTRA[@]+"${S_EXTRA[@]}"} ${CWD_ROW_EXTRA[@]+"${CWD_ROW_EXTRA[@]}"} || break
        found=""
        for d in "${WT}" "${REPO}"; do
            if [[ -f "${d}/${probe_name}" ]]; then
                found="${d}/${probe_name}"
                break
            fi
        done
        [[ -n "${found}" ]] && break
    done
    if [[ -n "${found}" ]]; then
        case "${how}" in
            content) IFS= read -r effective < "${found}" || effective="not observed" ;;
            location) effective="${found%/*}" ;;
        esac
    fi
    tapsum="$(tap_summary "${tool}")"
    tap_seen="$(tap_seen_yes_no "${tool}")"
    if [[ "${effective}" != "not observed" && "${tap_seen}" == yes ]]; then
        status="ok"
    elif [[ "${effective}" != "not observed" ]]; then
        note="effective cwd observed but no capture-tap line: no hook ran for this call, so payload cwd and hook pwd are unknown"
    fi
    count_status "${status}"
    printf '| %s | %s | %s | %s |\n' "$(cell "${label}")" "$(cell "${tapsum}")" "$(cell "${effective}")" "${status}" >> "${REPORT}"
    [[ -z "${note}" ]] || rpt "- ${label}: ${note}."
    say "${label}: ${status}${note:+ -- ${note}}"
}

cwd_row "exec_command, per-call workdir = repo root, session cwd = worktree" "${WT}" \
    "$(prompt_bash "pwd > ${WT}/cwd-exec.txt" "${REPO}")" "cwd-exec.txt" Bash content
cwd_row "exec_command, no per-call workdir, session cwd = worktree" "${WT}" \
    "$(prompt_bash "pwd > ${WT}/cwd-exec-same.txt")" "cwd-exec-same.txt" Bash content

# Direct apply_patch: the effective directory is where a RELATIVE Add lands.
cwd_row "direct apply_patch (relative Add), session cwd = worktree" "${WT}" \
    "$(prompt_patch "$(make_patch file-add cwd-direct.txt)")" "cwd-direct.txt" apply_patch location
if [[ -f "${WT}/cwd-direct.txt" ]]; then
    rpt ""
    rpt "- direct apply_patch landed in the session cwd (relative Add found at \`${WT}/cwd-direct.txt\`)."
fi

# Nested code mode. `code_mode` is an under-development Codex feature (key
# code_mode, default off). Distinguish three states: the probe failed, the
# feature is absent, and the feature is listed.
FEATURES_OUT="$(codex features list 2>&1)"
FEATURES_RC=$?
if [[ "${FEATURES_RC}" -ne 0 ]]; then
    rpt ""
    rpt "- nested code mode: NOT PROBED -- \`codex features list\` failed (exit ${FEATURES_RC}), so whether codex ${CODEX_VERSION} has code mode is unknown. This is neither \"unsupported\" nor evidence that nested calls are safe."
elif grep -E '^code_mode[[:space:]]' >/dev/null <<< "${FEATURES_OUT}"; then
    PROBE_NESTED="cwd-nested.txt"
    NESTED_PROMPT="Make exactly one tool call and nothing else: use code mode (the exec tool) to run JavaScript that calls the nested exec_command tool once with workdir ${REPO} and cmd 'pwd > ${WT}/${PROBE_NESTED}'. ${STRICT}"
    CWD_ROW_EXTRA=(-c features.code_mode=true)
    cwd_row "nested code mode exec_command, per-call workdir = repo root" "${WT}" "${NESTED_PROMPT}" "${PROBE_NESTED}" Bash content
    CWD_ROW_EXTRA=()
    rpt ""
    rpt "- nested code mode: the feature is listed and was requested with \`-c features.code_mode=true\` (the registry key, not verified against this build). Whether it was actually enabled is shown only by the row above: a probe file without a capture-tap line means a nested call can reach the tool without any hook running."
else
    rpt ""
    rpt "- nested code mode: unsupported in codex ${CODEX_VERSION} (\`codex features list\` has no code_mode feature). This is not evidence that nested calls are safe."
fi

# ── Relative apply_patch ────────────────────────────────────────────────
rpt ""
rpt "### Relative apply_patch (native Add / Update / Delete / Move)"
table_head "scenario"

rel_row() { # <label> <expect> <kind> <session> <effect-dir> <prefix>
    if skip_unproven "$1"; then
        return 0
    fi
    S_LABEL="$1" S_EXPECT="$2" S_KIND="$3" S_SESSION="$4" S_WHERE="$5"
    S_TOOL="apply_patch" S_MESSAGE=""
    case "$3" in
        add) S_NEEDLE="rel-add.txt" ;;
        update) S_NEEDLE="AGENTS.md" ;;
        delete) S_NEEDLE="rel-delete.txt" ;;
        move) S_NEEDLE="rel-move-b.txt" ;;
    esac
    S_PROMPT="$(prompt_patch "$(make_patch "$3" "$6")")"
    if [[ "$4" == "${WT}" ]]; then extra_for_worktree; else extra_for_root; fi
    run_scenario
}

for kind in add update delete move; do
    rel_row "root session, relative ${kind} resolving to the main worktree (deny)" deny "${kind}" "${REPO}" "${REPO}" ""
    rel_row "worktree session, relative ${kind} inside the worktree (allow)" allow "${kind}" "${WT}" "${WT}" ""
done
rel_row "root session, relative add under .worktrees/ (allow)" allow add "${REPO}" "${WT}" ".worktrees/1-native/"

# The bypass shape: a worktree session whose exec_command runs apply_patch
# with the repo root as workdir. Without the predicate the relative AGENTS.md
# joins the hook's (worktree) cwd and is allowed while the tool writes the main
# worktree. Both rows require the guard's own "effective working directory
# cannot be determined" message: a block for some other reason is not this.
BYPASS_PATCH="$(make_patch update "")"
if skip_unproven "bypass shape: worktree session, root workdir, relative AGENTS.md (deny)"; then
    skip_unproven "Bash-routed heredoc, workdir = session cwd, relative add (deny: over-block by design)"
else
    S_LABEL="bypass shape: worktree session, root workdir, relative AGENTS.md (deny)"
    S_EXPECT="deny" S_KIND="update" S_WHERE="${REPO}" S_SESSION="${WT}"
    S_NEEDLE="AGENTS.md" S_TOOL="Bash" S_MESSAGE="effective working directory cannot be determined"
    S_PROMPT="$(prompt_heredoc "${BYPASS_PATCH}" "${REPO}")"
    extra_for_worktree
    run_scenario
    S_LABEL="Bash-routed heredoc, workdir = session cwd, relative add (deny: over-block by design)"
    S_EXPECT="deny" S_KIND="add" S_WHERE="${WT}" S_SESSION="${WT}"
    S_NEEDLE="rel-add.txt" S_TOOL="Bash" S_MESSAGE="effective working directory cannot be determined"
    S_PROMPT="$(prompt_heredoc "$(make_patch add "")" "${WT}")"
    extra_for_worktree
    run_scenario
fi

# ── Launch failure ──────────────────────────────────────────────────────
rpt ""
rpt "### Launch failure (does the tool continue when a hook cannot start?)"
table_head "scenario"

LAUNCH_MARKER="no"
LAUNCH_WRITTEN="absent"
LAUNCH_ATTEMPTED="no"
LAUNCH_FAIL_RAN="no"

# launch_variant <name> <failing-script-or-empty> -- a config-layer hook group
# with a marker hook (proves the layer is active) and, optionally, a hook that
# cannot launch. The failing hook is a wrapper that first appends to its own
# evidence file and THEN invokes the unlaunchable script, so the shell exits 127
# (missing) or 126 (not executable) exactly as Codex's `$SHELL -lc` would, and
# the evidence file proves the broken hook was actually invoked. The tool call
# is an otherwise-allowed write. Returns non-zero when the variant could not be
# set up or run; the LAUNCH_* results are reset on every call so a failed
# variant can never reuse the previous one's values.
launch_variant() {
    local name="$1" script="$2" target="${LAUNCH}/launch-${1}.txt" marker="${LAUNCH}/marker.log"
    local evid="${LAUNCH}/failing-${1}.log" attempt hooks_json failing=""
    LAUNCH_MARKER="no"
    LAUNCH_WRITTEN="absent"
    LAUNCH_ATTEMPTED="no"
    LAUNCH_FAIL_RAN="no"
    mkdir -p "${LAUNCH}/.codex" || return 1
    printf '#!/bin/sh\nprintf "ran\\n" >> "%s"\nexit 0\n' "${marker}" > "${LAUNCH}/marker.sh" || return 1
    chmod +x "${LAUNCH}/marker.sh" || return 1
    printf '#!/bin/sh\nexit 0\n' > "${LAUNCH}/not-executable.sh" || return 1
    if [[ -n "${script}" ]]; then
        failing="printf 'ran\\n' >> '${evid}'; '${script}'"
    fi
    hooks_json="$(jq -n --arg marker "${LAUNCH}/marker.sh" --arg failing "${failing}" '
        {hooks: {PreToolUse: [{matcher: "Bash",
          hooks: ([{type: "command", command: $marker, timeout: 10}]
                  + (if $failing == "" then [] else [{type: "command", command: $failing, timeout: 10}] end))}]}}')" || return 1
    printf '%s\n' "${hooks_json}" > "${LAUNCH}/.codex/hooks.json" || return 1
    for attempt in 1 2; do
        rm -f "${target}" "${marker}" "${evid}"
        run_codex "${LAUNCH}" "$(prompt_bash "printf x > ${target}")" || return 1
        [[ -f "${marker}" ]] && break
    done
    if [[ -f "${marker}" ]]; then LAUNCH_MARKER="yes"; fi
    LAUNCH_WRITTEN="$(effect_landed file "${target}")"
    LAUNCH_ATTEMPTED="$(attempted_yes_no "launch-${name}.txt" Bash)"
    if [[ -f "${evid}" ]]; then LAUNCH_FAIL_RAN="yes"; fi
    return 0
}

if ! launch_variant control ""; then
    record "launch failure: control (marker hook only)" "inconclusive" "the control variant could not be set up or run; the variants below cannot be judged"
elif [[ "${LAUNCH_MARKER}" == no ]]; then
    record "launch failure: control (marker hook only)" "not run" "config-layer hooks are not active (project untrusted or hooks pending) -- run /hooks"
elif [[ "${LAUNCH_WRITTEN}" != written ]]; then
    record "launch failure: control (marker hook only)" "inconclusive" "the allowed write did not land (attempted=${LAUNCH_ATTEMPTED}); the variants below cannot be judged"
else
    record "launch failure: control (marker hook only)" "ok" "marker hook ran and the write landed"
    for variant in "missing-script|${LAUNCH}/missing-script.sh|127" "not-executable|${LAUNCH}/not-executable.sh|126"; do
        vname="${variant%%|*}"
        vrest="${variant#*|}"
        vlabel="launch failure: ${vname} (expected exit ${vrest##*|})"
        if ! launch_variant "${vname}" "${vrest%|*}"; then
            record "${vlabel}" "inconclusive" "the variant could not be set up or run"
            continue
        fi
        if [[ "${LAUNCH_MARKER}" == no || "${LAUNCH_ATTEMPTED}" == no || "${LAUNCH_FAIL_RAN}" == no ]]; then
            record "${vlabel}" "inconclusive" "marker=${LAUNCH_MARKER}, attempted=${LAUNCH_ATTEMPTED}, broken-hook-invoked=${LAUNCH_FAIL_RAN}: without proof that the broken hook was invoked nothing can be said about what a failed launch does"
        elif [[ "${LAUNCH_WRITTEN}" == written ]]; then
            record "${vlabel}" "ok" "FINDING: the tool CONTINUED after the hook failed to launch (the broken hook was invoked and the write landed)"
        else
            record "${vlabel}" "ok" "FINDING: the tool did NOT run after the hook failed to launch (the broken hook was invoked and the write did not land)"
        fi
    done
fi

# ── Summary and output ──────────────────────────────────────────────────
rpt ""
rpt "### Summary"
rpt ""
case "${CANARY_STATE}" in
    active) rpt "- Trust state: hooks ACTIVE (the canary's main-worktree write was denied by the guard)." ;;
    inactive) rpt "- Trust state: hooks INACTIVE (canary wrote a main-worktree target with no hook block). Review them with \`/hooks\` and rerun; the guard-dependent rows above are not run." ;;
    unknown) rpt "- Trust state: UNKNOWN (the canary was inconclusive: ${CANARY_DETAIL}). No allow or deny row was run, because neither outcome proves anything while it is unknown whether a hook is watching." ;;
    *) rpt "- Trust state: UNKNOWN (the canary did not run)." ;;
esac
rpt "- Guard canary preflight: as launched ${CANARY_AL_RESULT}, trust withheld ${CANARY_WH_RESULT} (see the Guard canary preflight section for the status, refusal text and cleanup outcome)."
rpt "- ok=${N_OK} (scenario rows: ${N_SCEN_OK}) failed=${N_FAIL} inconclusive=${N_INCONCLUSIVE} not-run=${N_NOTRUN}"
rpt "- An inconclusive row is never a success: the call was not proven to have been attempted. Preflight rows prove only that a handler launches, not what Codex does."

if [[ "${N_FAIL}" -gt 0 ]]; then
    RESULT_LINE="RESULT: FAIL (${N_FAIL} failed rows)"
    RESULT_RC=1
elif [[ "${N_SCEN_OK}" -eq 0 || "${CANARY_STATE}" != active ]]; then
    RESULT_LINE="RESULT: INCONCLUSIVE (scenario rows ok: ${N_SCEN_OK}, trust state: ${CANARY_STATE}); nothing was proven"
    RESULT_RC=3
else
    RESULT_LINE="RESULT: OK (scenario rows ok: ${N_SCEN_OK}, inconclusive: ${N_INCONCLUSIVE}, not run: ${N_NOTRUN})"
    RESULT_RC=0
fi
rpt ""
rpt "${RESULT_LINE}"

SANITIZED="${FIXTURE_ROOT}/report.sanitized.md"
san < "${REPORT}" > "${SANITIZED}" || {
    echo "FAIL: could not sanitize the report" >&2
    exit 1
}

# leaked_identity <file> -- name the first identity string that survived
# sanitization, without printing it. Home paths must be gone literally; user and
# host names must be gone as whole tokens. The token check is deliberately
# independent of, and stricter than, the sanitizer: a plain case-insensitive
# search (for the user too, which the sanitizer matches case-sensitively) with
# alphanumeric token edges, so a user `root` is not "leaked" by `rootless` but
# `ROOT` or `[root]` is. It does not re-run the sanitizer, so a sanitizer edge
# bug cannot hide itself. Names shorter than IDENTITY_MIN_STRICT characters are
# skipped from this strict pass only (the sanitizer still redacts them): a
# one- or two-letter name matches ordinary report text as a whole token and
# would make a clean report fail closed. Returns 0 when something survived or
# the check itself could not run.
IDENTITY_MIN_STRICT=3

# token_present <file> <name> -- 0 present, 1 absent, 2 the search failed.
token_present() {
    local f="$1" name="$2" i c esc="" rc
    local LC_ALL=C
    for ((i = 0; i < ${#name}; i++)); do
        c="${name:i:1}"
        case "${c}" in
            [A-Za-z0-9]) esc="${esc}${c}" ;;
            \^) esc="${esc}\\^" ;;
            \\) esc="${esc}\\\\" ;;
            \]) esc="${esc}[]]" ;;
            *) esc="${esc}[${c}]" ;;
        esac
    done
    grep -q -i -E -e "(^|[^A-Za-z0-9])${esc}([^A-Za-z0-9]|\$)" -- "${f}"
    rc=$?
    [[ "${rc}" -le 1 ]] || return 2
    return "${rc}"
}

leaked_identity() {
    local f="$1" lit rc name label stripped
    for lit in "${HOME_SAN}" "${HOME_REAL}" "${FIXTURE_ROOT}" "${FIXTURE_REAL}"; do
        [[ -n "${lit}" ]] || continue
        grep -F -- "${lit}" "${f}" >/dev/null
        rc=$?
        if [[ "${rc}" -eq 0 ]]; then
            echo "a home or fixture path"
            return 0
        elif [[ "${rc}" -ne 1 ]]; then
            echo "an unverifiable file (grep failed)"
            return 0
        fi
    done
    # The sanitizer's own placeholders are not leaks: a user named `user` or a
    # host named `host` must not match the <USER> / <HOST> it was replaced by.
    stripped="${f}.stripped"
    sed -E 's/<(HOME|USER|HOST|FIXTURE|UUID|TOKEN)>/ /g' -- "${f}" > "${stripped}" || { echo "an unverifiable file (placeholder strip failed)"; return 0; }
    for name in "${USER_NAME}" "${HOST_NAME}" "${HOST_NAME%%.*}" "${HOST_SHORT}"; do
        [[ "${#name}" -ge "${IDENTITY_MIN_STRICT}" ]] || continue
        label="the host name"
        [[ "${name}" == "${USER_NAME}" ]] && label="the user name"
        token_present "${stripped}" "${name}"
        rc=$?
        if [[ "${rc}" -eq 0 ]]; then
            echo "${label}"
            return 0
        elif [[ "${rc}" -ne 1 ]]; then
            echo "an unverifiable identity check"
            return 0
        fi
    done
    return 1
}

if LEAKED="$(leaked_identity "${SANITIZED}")"; then
    rm -f "${SANITIZED}"
    echo "FAIL: the sanitized report still contains ${LEAKED}; refusing to print or save it" >&2
    exit 1
fi

cat "${SANITIZED}"
if [[ -n "${CENCI_CODEX_NATIVE_OUT:-}" ]]; then
    cp "${SANITIZED}" "${CENCI_CODEX_NATIVE_OUT}" || {
        echo "FAIL: could not write ${CENCI_CODEX_NATIVE_OUT}" >&2
        exit 1
    }
fi

exit "${RESULT_RC}"
