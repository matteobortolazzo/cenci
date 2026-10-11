#!/usr/bin/env bash
# hooks-launch.test.sh -- manifest-driven launch verification of every handler
# in codex/hooks.json (#1211).
#
# hooks.test.sh checks the manifest's SHAPE and runs hooks[0] of each lifecycle
# event through a node-masked non-login shell. This suite RUNS every handler:
#
#   AC1  PreToolUse guards x real Bash and apply_patch payloads
#   AC2  every entry of every lifecycle group against its output contract
#   AC3  launch-failure conditions (unset/empty PLUGIN_ROOT, missing script,
#        non-executable script, missing bash/jq/awk) with a truthful verdict
#   AC4  the same handlers under `bash -lc`, `zsh -lc`, `fish -lc` (the way
#        Codex really launches a hook), SKIPPED when a shell is not installed
#   AC5  flow/docs/codex.md's `## Hook launch failures` table has a row for
#        every exit code the fixtures observed
#
# Every result is classified as exactly one of:
#   allowed            exit 0
#   denied-policy      exit 2 + a policy marker on stderr (guard ran and refused)
#   denied-fail-closed exit 2 + a reason on stderr (guard ran, a precondition
#                      such as jq/awk was missing, and it failed closed)
#   failed-no-reason   exit 2 + EMPTY stderr (Codex shows a failed hook with no
#                      explanation; nothing is known to have run)
#   not-running        any other exit code (126/127/...): the hook did not launch
#
# Fixture expectations are asserted exactly as observed. If a platform differs
# the fixture fails loudly rather than being weakened.
set -uo pipefail

# `declare -A` (associative arrays) below needs bash 4+.
(( BASH_VERSINFO[0] >= 4 )) || { echo "FAIL: bash >= 4 required" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CODEX_MANIFEST="${FLOW_DIR}/.codex-plugin/plugin.json"
CODEX_DOC="${FLOW_DIR}/docs/codex.md"
DOC_HEADING="## Hook launch failures"

FAILURES=0
PASSES=0
SKIPS=0

fail() {
    echo "FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

skip() {
    echo "SKIP: $1"
    SKIPS=$((SKIPS + 1))
}

finish() {
    echo "passed: ${PASSES}, failed: ${FAILURES}, skipped: ${SKIPS}"
    [[ "${FAILURES}" -eq 0 ]]
    exit $?
}

# skip_or_ci_fail <message> -- a missing prerequisite is a SKIP locally but a
# FAIL in CI (CI=true), where a silent skip would hide a job that no longer
# verifies anything.
skip_or_ci_fail() {
    if [[ "${CI:-}" == true ]]; then
        fail "$1 -- the CI job must install it"
    else
        skip "$1"
    fi
}

echo "hooks-launch.test.sh"

if ! command -v jq >/dev/null 2>&1; then
    skip_or_ci_fail "jq not found on PATH"
    finish
fi

# ── Setup ───────────────────────────────────────────────────────────────
# TMPDIR would widen the guards' allowlist and GIT_* would redirect git; the
# suite must run with neither. GITHUB_REPOSITORY is cleared for parity with the
# other hook suites.
unset TMPDIR GITHUB_REPOSITORY GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
unset BASH_ENV ENV ZDOTDIR CLAUDE_PLUGIN_ROOT

# Not under /tmp: the guards allowlist /tmp/* paths, which would make every
# deny case exit 0. /var/tmp is not allowlisted.
TEST_ROOT="$(mktemp -d /var/tmp/hooks-launch-test.XXXXXX)" || {
    echo "FAIL: test setup: mktemp -d failed" >&2
    exit 1
}
trap 'rm -rf "${TEST_ROOT}"' EXIT

mkdir_checked() {
    mkdir -p "$1" || {
        echo "FAIL: test setup: mkdir -p $1 failed" >&2
        exit 1
    }
}

RUNS_DIR="${TEST_ROOT}/runs"
FARMS_DIR="${TEST_ROOT}/farms"
LOGIN_HOME="${TEST_ROOT}/login-home"
MASK_DIR="${TEST_ROOT}/mask"
PAYLOAD_DIR="${TEST_ROOT}/payloads"
mkdir_checked "${RUNS_DIR}"
mkdir_checked "${FARMS_DIR}"
mkdir_checked "${LOGIN_HOME}"
mkdir_checked "${LOGIN_HOME}/.config"
mkdir_checked "${LOGIN_HOME}/.local/share"
mkdir_checked "${LOGIN_HOME}/.local/state"
mkdir_checked "${LOGIN_HOME}/.cache"
mkdir_checked "${MASK_DIR}"
mkdir_checked "${PAYLOAD_DIR}"

HOOKS_PATH="$(jq -r '.hooks // empty' "${CODEX_MANIFEST}")" || {
    echo "FAIL: test setup: cannot read ${CODEX_MANIFEST}" >&2
    exit 1
}
if [[ -n "${HOOKS_PATH}" ]]; then
    pass
else
    fail "Codex manifest must declare an explicit hooks path"
    finish
fi
CODEX_HOOKS="${FLOW_DIR}/${HOOKS_PATH#./}"
if [[ -f "${CODEX_HOOKS}" ]]; then
    pass
else
    fail "Codex hooks file does not exist: ${CODEX_HOOKS}"
    finish
fi

# Same node mask as hooks.test.sh: the baseline launcher is a NON-login
# `bash -c`, because a login shell would re-source /etc/profile and could
# reinstate what the mask removes.
for interpreter in node nodejs; do
    printf '#!/bin/sh\necho "%s: command not found" >&2\nexit 127\n' \
        "${interpreter}" > "${MASK_DIR}/${interpreter}"
    chmod +x "${MASK_DIR}/${interpreter}"
done
NODE_FREE_PATH="${MASK_DIR}:${PATH}"

# Fixtures: a configured repo (guards enforce there), an unconfigured empty
# dir (guards are no-ops, SessionStart carries the "not configured" warning).
# .worktrees/1-x is an ordinary directory, not a linked worktree: the guards
# only care about the path shape.
CONF="${TEST_ROOT}/conf"
UNCONF="${TEST_ROOT}/unconf"
WT="${CONF}/.worktrees/1-x"
mkdir_checked "${CONF}"
mkdir_checked "${UNCONF}"
mkdir_checked "${WT}"
mkdir_checked "${CONF}/.cenci"
mkdir_checked "${CONF}/.plans"
if git -C "${CONF}" init -q 2>/dev/null \
    && printf '{}\n' > "${CONF}/.cenci/config.json" \
    && printf '# project\n' > "${CONF}/AGENTS.md" \
    && printf '# plan\n' > "${CONF}/.plans/resume-me.md"; then
    pass
else
    echo "FAIL: test setup: could not build the configured git fixture" >&2
    exit 1
fi

# Plugin-root copy for the destructive launch-failure fixtures; the real tree
# is never mutated. Only what a handler can reach is copied.
BASE_ROOT="${TEST_ROOT}/plugin-root-base"
mkdir_checked "${BASE_ROOT}/hooks"
mkdir_checked "${BASE_ROOT}/codex"
if cp -R "${FLOW_DIR}/hooks/scripts" "${BASE_ROOT}/hooks/scripts" \
    && cp "${FLOW_DIR}/codex/hook-output.sh" "${BASE_ROOT}/codex/hook-output.sh"; then
    pass
else
    echo "FAIL: test setup: could not copy the plugin-root fixture" >&2
    exit 1
fi

# ── Manifest enumeration (nothing hardcoded) ────────────────────────────
# extract_handlers <manifest> <outfile> -- one TSV row per handler:
#   event  groupIdx  handlerIdx  matcher  command
# Pure: no fail(), exit status only. The matcher defaults to "*" (match all)
# so no field is ever empty (an empty field would collapse under a tab IFS).
extract_handlers() {
    jq -r '
        .hooks | to_entries[] | .key as $e | .value | to_entries[]
        | .key as $g | (.value.matcher // "*") as $m
        | .value.hooks | to_entries[]
        | [$e, ($g | tostring), (.key | tostring), $m, .value.command] | @tsv
    ' "$1" > "$2"
}

HANDLERS_TSV="${TEST_ROOT}/handlers.tsv"
if ! extract_handlers "${CODEX_HOOKS}" "${HANDLERS_TSV}"; then
    echo "FAIL: test setup: could not enumerate handlers from ${CODEX_HOOKS}" >&2
    exit 1
fi

SCRIPT_RE='^"\$\{?PLUGIN_ROOT\}?/([^"]+)"'
DOTDOT_RE='(^|/)\.\.(/|$)'
PLUGIN_ROOT_TOKEN='${PLUGIN_ROOT}'

declare -a H_EVENT=() H_CMD=() H_SCRIPT=() H_LABEL=() H_BASH=()
declare -a PRE_IDX=() LIFE_IDX=() PRE_GROUPS=() LIFE_EVENTS=()
declare -A PRE_GROUP_MATCHER=() PRE_GROUP_HANDLERS=() EVENT_SEEN=() EVENT_COUNT=()
H_N=0
while IFS=$'\t' read -r ev gi hi mt cm; do
    n="${H_N}"
    H_EVENT[n]="${ev}"
    H_CMD[n]="${cm}"
    H_N=$((H_N + 1))
    EVENT_COUNT["${ev}"]=$(( ${EVENT_COUNT["${ev}"]:-0} + 1 ))
    if [[ "${cm}" =~ ${SCRIPT_RE} ]]; then
        script="${BASH_REMATCH[1]}"
        rest="${cm#"${BASH_REMATCH[0]}"}"
        # The script path is joined onto fixture roots and the real tree: an
        # absolute path or a `..` segment would escape them.
        if [[ "${script}" == /* || "${script}" =~ ${DOTDOT_RE} ]]; then
            script=""
            H_SCRIPT[n]=""
            H_LABEL[n]="${ev}[${gi}.${hi}] ${cm}"
            fail "unknown command form (script path must be relative and free of '..' segments): ${cm}"
        else
            H_SCRIPT[n]="${script}"
            H_LABEL[n]="${ev}[${gi}.${hi}] ${script##*/}${rest}"
            pass
        fi
    else
        script=""
        H_SCRIPT[n]=""
        H_LABEL[n]="${ev}[${gi}.${hi}] ${cm}"
        fail "unknown command form (expected a quoted \${PLUGIN_ROOT}/<script> path): ${cm}"
    fi
    # The interpreter is read from the real script's shebang: bash-shebang
    # handlers depend on `bash` being on the login PATH, sh ones do not.
    first=""
    H_BASH[n]=0
    if [[ -n "${script}" && -x "${FLOW_DIR}/${script}" ]]; then
        IFS= read -r first < "${FLOW_DIR}/${script}" || true
        case "${first}" in
            '#!'*)
                pass
                [[ "${first}" == *bash* ]] && H_BASH[n]=1
                ;;
            *) fail "${H_LABEL[n]}: script has no shebang line" ;;
        esac
    elif [[ -n "${script}" ]]; then
        fail "${H_LABEL[n]}: script is missing or not executable in the real tree: ${FLOW_DIR}/${script}"
    fi
    if [[ "${ev}" == "PreToolUse" ]]; then
        PRE_IDX+=("${n}")
        PRE_GROUP_MATCHER["${gi}"]="${mt}"
        PRE_GROUP_HANDLERS["${gi}"]="${PRE_GROUP_HANDLERS["${gi}"]:-} ${n}"
    else
        LIFE_IDX+=("${n}")
    fi
    if [[ -z "${EVENT_SEEN["${ev}"]:-}" ]]; then
        EVENT_SEEN["${ev}"]=1
        [[ "${ev}" == "PreToolUse" ]] || LIFE_EVENTS+=("${ev}")
    fi
done < "${HANDLERS_TSV}"
for g in "${!PRE_GROUP_MATCHER[@]}"; do
    PRE_GROUPS+=("${g}")
done

# Non-vacuity: an empty or truncated enumeration would turn every loop below
# into a silent no-op.
if [[ "${EVENT_COUNT["PreToolUse"]:-0}" -ge 1 ]]; then
    pass
else
    fail "manifest has no PreToolUse handler to run"
fi
if [[ "${#LIFE_EVENTS[@]}" -ge 1 ]]; then
    pass
else
    fail "manifest has no lifecycle (non-PreToolUse) event"
fi
for ev in "${LIFE_EVENTS[@]}"; do
    if [[ "${EVENT_COUNT["${ev}"]:-0}" -ge 1 ]]; then
        pass
    else
        fail "manifest event ${ev} has no handler"
    fi
done
# The extractor must agree with an independent query of the manifest: it must
# neither drop an event nor a handler, and no group may be an empty shell
# (`hooks: []`) that contributes no row yet looks present.
EXPECT_HANDLERS="$(jq '[.hooks[][].hooks[]] | length' "${CODEX_HOOKS}")" || {
    echo "FAIL: test setup: cannot count handlers in ${CODEX_HOOKS}" >&2
    exit 1
}
EXPECT_EVENTS="$(jq -r '.hooks | keys | join(" ")' "${CODEX_HOOKS}")" || {
    echo "FAIL: test setup: cannot list events in ${CODEX_HOOKS}" >&2
    exit 1
}
EMPTY_GROUPS="$(jq '[.hooks[][] | select((.hooks | length) == 0)] | length' "${CODEX_HOOKS}")" || {
    echo "FAIL: test setup: cannot count empty groups in ${CODEX_HOOKS}" >&2
    exit 1
}
ACTUAL_EVENTS="$(printf '%s\n' "${!EVENT_COUNT[@]}" | LC_ALL=C sort | tr '\n' ' ')"
ACTUAL_EVENTS="${ACTUAL_EVENTS% }"
if [[ "${H_N}" -eq "${EXPECT_HANDLERS}" ]]; then
    pass
else
    fail "extractor saw ${H_N} handlers but the manifest declares ${EXPECT_HANDLERS}"
fi
if [[ "${ACTUAL_EVENTS}" == "${EXPECT_EVENTS}" ]]; then
    pass
else
    fail "extractor saw events [${ACTUAL_EVENTS}] but the manifest declares [${EXPECT_EVENTS}]"
fi
if [[ "${EMPTY_GROUPS}" -eq 0 ]]; then
    pass
else
    fail "manifest has ${EMPTY_GROUPS} hook group(s) with no handlers"
fi
if [[ "${H_N}" -ge 1 && "${#PRE_IDX[@]}" -ge 1 ]]; then
    pass
else
    fail "handler enumeration is empty"
    finish
fi

# ── Runner and classifier ───────────────────────────────────────────────
declare -A OBSERVED=()
RUN_N=0
OUT=""
R_RC=""
R_CLASS=""
R_ERR1=""

# new_out -- allocate the next per-run output dir into OUT. Must run in the
# parent shell (never under command substitution), or the counter is lost.
new_out() {
    RUN_N=$((RUN_N + 1))
    OUT="${RUNS_DIR}/${RUN_N}"
}

# run_fixture <launcher> <cmd> <cwd> <stdin-file> <outdir> <envmode> <root>
# Runs one hook command string and records rc/stdout/stderr into <outdir>.
# Pure: it never calls fail(), so it is safe anywhere.
#   launcher  baseline            node-masked non-login `bash -c`
#             login:<abs-shell>   `<shell> -lc` with an isolated HOME
#             restricted:<farm>   absolute `/bin/sh -c` with PATH=<farm dir>
#   envmode   export | empty | unset   state of PLUGIN_ROOT for the hook
run_fixture() {
    local launcher="$1" cmd="$2" cwd="$3" stdin_file="$4" outdir="$5" envmode="$6" root="$7"
    mkdir -p "${outdir}" || return 1
    (
        cd "${cwd}" || exit 97
        case "${envmode}" in
            export) export PLUGIN_ROOT="${root}" ;;
            empty) export PLUGIN_ROOT="" ;;
            unset) unset PLUGIN_ROOT ;;
        esac
        case "${launcher}" in
            baseline)
                export PATH="${NODE_FREE_PATH}"
                exec bash -c "${cmd}"
                ;;
            login:*)
                export HOME="${LOGIN_HOME}" XDG_CONFIG_HOME="${LOGIN_HOME}/.config"
                export XDG_DATA_HOME="${LOGIN_HOME}/.local/share"
                export XDG_STATE_HOME="${LOGIN_HOME}/.local/state"
                export XDG_CACHE_HOME="${LOGIN_HOME}/.cache"
                exec "${launcher#login:}" -lc "${cmd}"
                ;;
            restricted:*)
                export PATH="${launcher#restricted:}"
                exec /bin/sh -c "${cmd}"
                ;;
            *) exit 98 ;;
        esac
    ) < "${stdin_file}" > "${outdir}/stdout" 2> "${outdir}/stderr"
    printf '%s\n' "$?" > "${outdir}/rc"
}

# classify <rc> <stderr-file> -- print the verdict. Pure.
classify() {
    case "$1" in
        0) echo allowed ;;
        2)
            if [[ ! -s "$2" ]]; then
                echo failed-no-reason
            elif grep -qE 'Refusing to write|targets the main worktree' "$2"; then
                echo denied-policy
            else
                echo denied-fail-closed
            fi
            ;;
        *) echo not-running ;;
    esac
}

# load_result <outdir> -- read a finished run into R_RC / R_CLASS / R_ERR1 and
# add its exit code to the observed set (doc-sync input).
load_result() {
    local out="$1"
    R_RC=""
    R_ERR1=""
    read -r R_RC < "${out}/rc" || R_RC="missing"
    IFS= read -r R_ERR1 < "${out}/stderr" || true
    R_CLASS="$(classify "${R_RC}" "${out}/stderr")"
    OBSERVED["${R_RC}"]=1
}

observe() {
    echo "observed: $1 rc=${R_RC} class=${R_CLASS} stderr=${R_ERR1:0:110}"
}

# exec_handler <idx> <launcher> <textmode> <cwd> <stdin> <envmode> <root>
# Runs manifest handler <idx>. textmode `codex` substitutes the literal
# ${PLUGIN_ROOT} text with <root> (what codex-rs engine/discovery.rs does);
# `raw` hands the command over untouched.
exec_handler() {
    local i="$1" launcher="$2" textmode="$3" cwd="$4" stdin="$5" envmode="$6" root="$7" cmd
    cmd="${H_CMD[i]}"
    if [[ "${textmode}" == codex ]]; then
        cmd="${cmd//${PLUGIN_ROOT_TOKEN}/${root}}"
    fi
    new_out
    run_fixture "${launcher}" "${cmd}" "${cwd}" "${stdin}" "${OUT}" "${envmode}" "${root}" || {
        fail "${H_LABEL[i]}: could not prepare run dir ${OUT}"
        R_RC="missing"
        R_CLASS="not-running"
        R_ERR1=""
        return 1
    }
    load_result "${OUT}"
}

# lifecycle_contract_ok <event> <stdout-file> -- stdout is empty or exactly one
# JSON object; hookSpecificOutput (if present) names this event and carries a
# string additionalContext; systemMessage (if present) is a string. Pure.
lifecycle_contract_ok() {
    [[ ! -s "$2" ]] && return 0
    jq -e -s --arg ev "$1" '
        length == 1 and (.[0] | type == "object")
        and ((.[0] | has("hookSpecificOutput") | not)
             or (.[0].hookSpecificOutput.hookEventName == $ev
                 and (.[0].hookSpecificOutput.additionalContext | type == "string")))
        and ((.[0] | has("systemMessage") | not) or (.[0].systemMessage | type == "string"))
    ' "$2" >/dev/null 2>&1
}

# build_farm <excluded-tool> <dir> -- a directory of symlinks to every
# executable on the current PATH (first match per name wins) except
# <excluded-tool>. Used as the whole PATH so the tool is genuinely absent:
# `command -v <tool>` fails exactly as on a machine without it. Pure.
build_farm() {
    local tool="$1" dir="$2" d f name oldifs
    local -a path_dirs=() targets=()
    local seen=$'\n'
    mkdir -p "${dir}" || return 1
    oldifs="${IFS}"
    IFS=: read -r -a path_dirs <<< "${PATH}"
    IFS="${oldifs}"
    for d in "${path_dirs[@]}"; do
        [[ -d "${d}" ]] || continue
        for f in "${d}"/*; do
            [[ -f "${f}" && -x "${f}" ]] || continue
            name="${f##*/}"
            [[ "${name}" == "${tool}" ]] && continue
            case "${seen}" in
                *$'\n'"${name}"$'\n'*) continue ;;
            esac
            seen="${seen}${name}"$'\n'
            targets+=("${f}")
        done
    done
    [[ "${#targets[@]}" -gt 0 ]] || return 1
    ln -s -- "${targets[@]}" "${dir}/"
}

# ── Payloads ────────────────────────────────────────────────────────────
# make_payload <file> <tool_name> <command> -- a PreToolUse payload shaped like
# the one Codex sends (guards read tool_input.command; see
# guard-main-worktree.sh's input contract).
make_payload() {
    jq -n --arg cwd "${CONF}" --arg tool "$2" --arg cmd "$3" \
        '{cwd: $cwd, hook_event_name: "PreToolUse", tool_name: $tool, tool_input: {command: $cmd}}' \
        > "$1"
}

declare -a PL_NAME=() PL_FILE=() PL_NAMES=() PL_EXPECT=()
PL_N=0
# add_payload <name> <expect: allow|sensitive|main> <tool_name> <routing-names-json> <command>
add_payload() {
    local name="$1" expect="$2" tool="$3" names="$4" cmd="$5" file
    file="${PAYLOAD_DIR}/${PL_N}-${name}.json"
    if ! make_payload "${file}" "${tool}" "${cmd}"; then
        fail "test setup: could not build payload ${name}"
        return 1
    fi
    PL_NAME[PL_N]="${name}"
    PL_FILE[PL_N]="${file}"
    PL_NAMES[PL_N]="${names}"
    PL_EXPECT[PL_N]="${expect}"
    PL_N=$((PL_N + 1))
}

# A body full of the shell-like characters the ticket names: $, backticks,
# parentheses, ;, &&, |, > -- a legitimate in-worktree patch must not be
# mistaken for a shell command writing somewhere.
SHELL_CHAR_BODY='+cost is $5, run `date` (twice); a && b | c > d'

BASH_NAMES='["Bash"]'
PATCH_NAMES='["apply_patch","Write","Edit"]'

printf -v CMD_BASH_SENSITIVE '%s' "printf x > ${WT}/.env"
printf -v CMD_BASH_MAIN '%s' "printf x > ${CONF}/AGENTS.md"
printf -v CMD_BASH_ALLOW '%s' "printf x > ${WT}/notes.txt"
printf -v CMD_BASH_HEREDOC '%s\n' \
    "apply_patch <<'EOF'" \
    '*** Begin Patch' \
    "*** Add File: ${WT}/notes.md" \
    "${SHELL_CHAR_BODY}" \
    '*** End Patch' \
    'EOF'
printf -v CMD_PATCH_SENSITIVE '%s\n' '*** Begin Patch' '*** Add File: .worktrees/1-x/.env' '+X=1' '*** End Patch'
printf -v CMD_PATCH_MAIN '%s\n' '*** Begin Patch' '*** Update File: AGENTS.md' '@@' '-old' '+new' '*** End Patch'
printf -v CMD_PATCH_ALLOW '%s\n' '*** Begin Patch' '*** Add File: .worktrees/1-x/notes.txt' '+hello' '*** End Patch'
printf -v CMD_PATCH_CHARS '%s\n' '*** Begin Patch' '*** Add File: .worktrees/1-x/notes.md' "${SHELL_CHAR_BODY}" '*** End Patch'

add_payload bash-sensitive sensitive Bash "${BASH_NAMES}" "${CMD_BASH_SENSITIVE}"
add_payload bash-main main Bash "${BASH_NAMES}" "${CMD_BASH_MAIN}"
add_payload bash-allow allow Bash "${BASH_NAMES}" "${CMD_BASH_ALLOW}"
add_payload bash-heredoc-patch-chars allow Bash "${BASH_NAMES}" "${CMD_BASH_HEREDOC}"
add_payload patch-sensitive sensitive apply_patch "${PATCH_NAMES}" "${CMD_PATCH_SENSITIVE}"
add_payload patch-main main apply_patch "${PATCH_NAMES}" "${CMD_PATCH_MAIN}"
add_payload patch-allow allow apply_patch "${PATCH_NAMES}" "${CMD_PATCH_ALLOW}"
add_payload patch-chars allow apply_patch "${PATCH_NAMES}" "${CMD_PATCH_CHARS}"

if [[ "${PL_N}" -eq 8 ]]; then
    pass
else
    fail "expected 8 guard payloads, built ${PL_N}"
fi

# Index of the plain Bash allow payload: reused by the launch-failure fixtures
# as "a write the guard has no reason to refuse".
PL_BASH_ALLOW=2
if [[ "${PL_NAME[PL_BASH_ALLOW]:-}" == bash-allow ]]; then
    pass
else
    fail "PL_BASH_ALLOW=${PL_BASH_ALLOW} does not index the bash-allow payload (found '${PL_NAME[PL_BASH_ALLOW]:-}')"
    finish
fi

# Lifecycle stdin: Codex sends the event name and cwd as JSON.
declare -A LIFE_STDIN=()
for ev in "${LIFE_EVENTS[@]}"; do
    LIFE_STDIN["${ev}"]="${PAYLOAD_DIR}/life-${ev}.json"
    if ! jq -n --arg e "${ev}" --arg c "${CONF}" '{hook_event_name: $e, cwd: $c}' > "${LIFE_STDIN["${ev}"]}"; then
        fail "test setup: could not build ${ev} stdin"
    fi
done

# group_matches <matcher> <names-json> -- does a PreToolUse matcher (anchored
# regex; `*`/empty match everything) select any of the tool names? Pure: it
# returns jq's status (0 match, 1 no match, >1 invalid matcher/jq error) and
# never calls fail(); the call site decides what >1 means.
group_matches() {
    jq -n -e --arg m "$1" --argjson names "$2" '
        if $m == "" or $m == "*" then true
        else $names | any(. as $n | $n | test("^(" + $m + ")$"))
        end
    ' >/dev/null 2>&1
}

# ── AC1: guard matrix ───────────────────────────────────────────────────
# Every PreToolUse handler x every payload, baseline launcher, from the
# configured repo root. Results are kept per (payload, handler).
declare -A RES_RC=() RES_CLASS=() RES_OUT=()
for p in "${!PL_NAME[@]}"; do
    for i in "${PRE_IDX[@]}"; do
        exec_handler "${i}" baseline codex "${CONF}" "${PL_FILE[p]}" export "${FLOW_DIR}"
        RES_RC["${p}:${i}"]="${R_RC}"
        RES_CLASS["${p}:${i}"]="${R_CLASS}"
        RES_OUT["${p}:${i}"]="${OUT}"
        if [[ "${R_CLASS}" == not-running ]]; then
            fail "guard matrix: ${H_LABEL[i]} did not run on payload ${PL_NAME[p]} (rc=${R_RC}): ${R_ERR1}"
        else
            pass
        fi
        if [[ "${PL_EXPECT[p]}" == allow ]]; then
            if [[ "${R_RC}" == 0 ]]; then
                pass
            else
                fail "guard matrix: ${H_LABEL[i]} must allow ${PL_NAME[p]} (rc=${R_RC}): ${R_ERR1}"
            fi
        fi
    done
done

# Deny payloads: route the payload to its matcher groups; in EVERY matched
# group at least one handler must deny with the expected policy marker (any
# handler in a Codex group can block). No per-script expectation list: a new
# guard in the group is covered automatically.
for p in "${!PL_NAME[@]}"; do
    case "${PL_EXPECT[p]}" in
        sensitive) marker="Refusing to write" ;;
        main) marker="targets the main worktree" ;;
        *) continue ;;
    esac
    matched=0
    for g in "${PRE_GROUPS[@]}"; do
        group_matches "${PRE_GROUP_MATCHER["${g}"]}" "${PL_NAMES[p]}"
        gm_rc=$?
        if [[ "${gm_rc}" -gt 1 ]]; then
            fail "guard matrix: PreToolUse group ${g} has an invalid matcher '${PRE_GROUP_MATCHER["${g}"]}' (jq rc=${gm_rc})"
            continue
        elif [[ "${gm_rc}" -eq 1 ]]; then
            continue
        fi
        matched=$((matched + 1))
        read -r -a members <<< "${PRE_GROUP_HANDLERS["${g}"]}"
        denied=0
        for i in "${members[@]}"; do
            if [[ "${RES_CLASS["${p}:${i}"]}" == denied-policy ]] \
                && grep -qF -- "${marker}" "${RES_OUT["${p}:${i}"]}/stderr"; then
                denied=1
            fi
        done
        if [[ "${denied}" -eq 1 ]]; then
            pass
        else
            fail "guard matrix: PreToolUse group ${g} (matcher ${PRE_GROUP_MATCHER["${g}"]}) routed payload ${PL_NAME[p]} but no handler denied it with '${marker}'"
        fi
    done
    if [[ "${matched}" -ge 1 ]]; then
        pass
    else
        fail "guard matrix: no PreToolUse matcher routes payload ${PL_NAME[p]} (tools: ${PL_NAMES[p]})"
    fi
done

# ── AC2: lifecycle matrix ───────────────────────────────────────────────
# Every entry of every non-PreToolUse group, in an unconfigured dir and in a
# configured repo with a ticketless pending plan.
declare -A LIFE_RC=() LIFE_NONEMPTY=()
for dirname in UNCONF CONF; do
    if [[ "${dirname}" == UNCONF ]]; then cwd="${UNCONF}"; else cwd="${CONF}"; fi
    for i in "${LIFE_IDX[@]}"; do
        ev="${H_EVENT[i]}"
        exec_handler "${i}" baseline codex "${cwd}" "${LIFE_STDIN["${ev}"]}" export "${FLOW_DIR}"
        LIFE_RC["${dirname}:${i}"]="${R_RC}"
        if [[ "${R_RC}" == 0 ]]; then
            pass
        else
            fail "lifecycle: ${H_LABEL[i]} must exit 0 in ${dirname} (rc=${R_RC}, ${R_CLASS}): ${R_ERR1}"
        fi
        if lifecycle_contract_ok "${ev}" "${OUT}/stdout"; then
            pass
            [[ -s "${OUT}/stdout" ]] && LIFE_NONEMPTY["${ev}"]=1
        else
            fail "lifecycle: ${H_LABEL[i]} violates the ${ev} output contract in ${dirname}"
        fi
    done
done
for ev in "${LIFE_EVENTS[@]}"; do
    if [[ -n "${LIFE_NONEMPTY["${ev}"]:-}" ]]; then
        pass
    else
        fail "lifecycle: no handler produced non-empty valid output for ${ev} (contract check would be vacuous)"
    fi
done

# ── AC3: launch-failure fixtures ────────────────────────────────────────
# cwd/stdin/baseline-rc for handler i: guards run in the configured repo on
# the plain allow payload, lifecycle handlers in the unconfigured dir.
h_cwd() {
    if [[ "${H_EVENT[$1]}" == PreToolUse ]]; then echo "${CONF}"; else echo "${UNCONF}"; fi
}
h_stdin() {
    if [[ "${H_EVENT[$1]}" == PreToolUse ]]; then
        echo "${PL_FILE[PL_BASH_ALLOW]}"
    else
        echo "${LIFE_STDIN["${H_EVENT[$1]}"]}"
    fi
}
h_base_rc() {
    if [[ "${H_EVENT[$1]}" == PreToolUse ]]; then
        echo "${RES_RC["${PL_BASH_ALLOW}:$1"]}"
    else
        echo "${LIFE_RC["UNCONF:$1"]}"
    fi
}

# Restricted PATHs: genuinely missing tools, not exit-127 shims (a shim leaves
# `command -v jq` succeeding, so the guard would take its JSON-parse-failure
# path instead of its real "jq is required" path).
FARM_BASH="${FARMS_DIR}/no-bash"
FARM_JQ="${FARMS_DIR}/no-jq"
FARM_AWK="${FARMS_DIR}/no-awk"
farms_ok=1
for pair in "bash:${FARM_BASH}" "jq:${FARM_JQ}" "awk:${FARM_AWK}"; do
    tool="${pair%%:*}"
    farm="${pair#*:}"
    if ! build_farm "${tool}" "${farm}"; then
        fail "test setup: could not build the PATH without ${tool}"
        farms_ok=0
        continue
    fi
    # Non-vacuity: the tool is really gone and the rest still resolves.
    if [[ ! -e "${farm}/${tool}" ]] \
        && PATH="${farm}" /bin/sh -c 'command -v git >/dev/null 2>&1 && command -v env >/dev/null 2>&1' \
        && ! PATH="${farm}" /bin/sh -c "command -v ${tool} >/dev/null 2>&1"; then
        pass
    else
        fail "test setup: PATH without ${tool} is not a faithful restriction"
        farms_ok=0
    fi
done

# Real copy of the plugin root with one script removed / de-executed. A new
# copy per case keeps the cases independent.
copy_root() {
    local dest="$1"
    cp -R "${BASE_ROOT}" "${dest}"
}

for i in "${!H_CMD[@]}"; do
    label="${H_LABEL[i]}"
    script="${H_SCRIPT[i]}"
    cwd="$(h_cwd "${i}")"
    stdin="$(h_stdin "${i}")"
    base_rc="$(h_base_rc "${i}")"
    is_hook_output=0
    [[ "${script##*/}" == hook-output.sh ]] && is_hook_output=1

    # (a) unset PLUGIN_ROOT, command untouched: the shell expands the empty
    # variable and runs /hooks/... or /codex/...
    exec_handler "${i}" baseline raw "${cwd}" "${stdin}" unset ""
    observe "(a) PLUGIN_ROOT unset, raw command: ${label}"
    if [[ "${R_RC}" == 127 && "${R_CLASS}" == not-running && -n "${R_ERR1}" ]]; then
        pass
    else
        fail "(a) ${label}: unset PLUGIN_ROOT must be 127/not-running with stderr (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
    fi
    if [[ "${R_ERR1}" == *"${PLUGIN_ROOT_TOKEN}"* || "${R_ERR1}" != *"/${script%%/*}/"* ]]; then
        fail "(a) ${label}: stderr should show the broken path /${script%%/*}/... (got '${R_ERR1}')"
    else
        pass
    fi

    # (a') path substituted but PLUGIN_ROOT not exported: guards resolve their
    # library from $0 so they still run; hook-output.sh needs the variable.
    exec_handler "${i}" baseline codex "${cwd}" "${stdin}" unset "${FLOW_DIR}"
    observe "(a') path substituted, PLUGIN_ROOT not exported: ${label}"
    if [[ "${is_hook_output}" -eq 1 ]]; then
        if [[ "${R_RC}" == 2 && "${R_CLASS}" == failed-no-reason ]]; then
            pass
        else
            fail "(a') ${label}: hook-output.sh without PLUGIN_ROOT must exit 2 with empty stderr (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
        fi
    elif [[ "${R_RC}" == "${base_rc}" ]]; then
        pass
    else
        fail "(a') ${label}: handler does not use PLUGIN_ROOT; expected baseline rc ${base_rc}, got ${R_RC} (${R_CLASS}): ${R_ERR1}"
    fi

    # (b) empty PLUGIN_ROOT
    exec_handler "${i}" baseline raw "${cwd}" "${stdin}" empty ""
    observe "(b) PLUGIN_ROOT empty, raw command: ${label}"
    if [[ "${R_RC}" == 127 && "${R_CLASS}" == not-running && -n "${R_ERR1}" ]]; then
        pass
    else
        fail "(b) ${label}: empty PLUGIN_ROOT must be 127/not-running with stderr (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
    fi

    # (c) script missing from the plugin root
    root_c="${TEST_ROOT}/root-missing-${i}"
    if copy_root "${root_c}" \
        && [[ -f "${root_c}/${script}" ]] \
        && rm -f "${root_c}/${script}" \
        && [[ ! -e "${root_c}/${script}" ]]; then
        exec_handler "${i}" baseline codex "${cwd}" "${stdin}" export "${root_c}"
        observe "(c) script missing: ${label}"
        if [[ "${R_RC}" == 127 && "${R_CLASS}" == not-running ]] \
            && grep -qF -- "${root_c}/${script}" "${OUT}/stderr"; then
            pass
        else
            fail "(c) ${label}: missing script must be 127/not-running naming the full path (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
        fi
    else
        fail "(c) ${label}: invalid fixture: the plugin-root copy lacked the script before removal, or it survived removal (${root_c}/${script})"
    fi

    # (d) script present with no execute bit. Recreated via redirection from
    # the real file, so the copy has no x bit and no permission-removing chmod
    # is needed (execve refuses it even for root).
    root_d="${TEST_ROOT}/root-nonexec-${i}"
    if copy_root "${root_d}" && rm -f "${root_d}/${script}" && cat "${FLOW_DIR}/${script}" > "${root_d}/${script}" \
        && [[ ! -x "${root_d}/${script}" ]]; then
        exec_handler "${i}" baseline codex "${cwd}" "${stdin}" export "${root_d}"
        observe "(d) script not executable: ${label}"
        if [[ "${R_RC}" == 126 && "${R_CLASS}" == not-running ]] \
            && grep -qF -- "${root_d}/${script}" "${OUT}/stderr"; then
            pass
        else
            fail "(d) ${label}: non-executable script must be 126/not-running naming the full path (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
        fi
    else
        fail "(d) ${label}: could not build the non-executable fixture"
    fi

    [[ "${farms_ok}" -eq 1 ]] || continue

    # (e) no bash on PATH: #!/bin/sh handlers must be unaffected, bash-shebang
    # handlers die in `env`.
    exec_handler "${i}" "restricted:${FARM_BASH}" codex "${cwd}" "${stdin}" export "${FLOW_DIR}"
    observe "(e) bash missing from PATH: ${label}"
    if [[ "${H_BASH[i]}" -eq 1 ]]; then
        if [[ "${R_RC}" == 127 && "${R_CLASS}" == not-running ]] && grep -qE "bash[^:]*: No such file" "${OUT}/stderr"; then
            pass
        else
            fail "(e) ${label}: bash-shebang handler without bash must be 127/not-running naming bash (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
        fi
    elif [[ "${R_RC}" == "${base_rc}" ]]; then
        pass
    else
        fail "(e) ${label}: sh-shebang handler must not need bash; expected rc ${base_rc}, got ${R_RC} (${R_CLASS}): ${R_ERR1}"
    fi

    # (f) no jq / (g) no awk
    for pair in "f:jq:${FARM_JQ}" "g:awk:${FARM_AWK}"; do
        tag="${pair%%:*}"
        rest="${pair#*:}"
        tool="${rest%%:*}"
        farm="${rest#*:}"
        exec_handler "${i}" "restricted:${farm}" codex "${cwd}" "${stdin}" export "${FLOW_DIR}"
        observe "(${tag}) ${tool} missing from PATH: ${label}"
        case "${tag}" in
            f) tool_sig='jq is required' ;;
            *) tool_sig='requires awk' ;;
        esac
        if [[ "${H_EVENT[i]}" == PreToolUse ]]; then
            if [[ "${R_RC}" == 2 && "${R_CLASS}" == denied-fail-closed ]] && grep -qF -- "${tool_sig}" "${OUT}/stderr"; then
                pass
            else
                fail "(${tag}) ${label}: guard without ${tool} must fail closed (exit 2, reason '${tool_sig}') (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
            fi
        elif [[ "${is_hook_output}" -eq 1 ]]; then
            if [[ "${R_RC}" == 0 ]] && lifecycle_contract_ok "${H_EVENT[i]}" "${OUT}/stdout"; then
                pass
            else
                fail "(${tag}) ${label}: hook-output.sh without ${tool} must degrade to a valid payload and exit 0 (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
            fi
            # Without jq the session and compact modes emit exactly `{}` and say
            # why on stderr; stop mode has no payload to degrade, so only the
            # contract check above applies to it.
            if [[ "${tag}" == f && "${H_CMD[i]}" != *" stop" ]]; then
                nojq_stdout=""
                nojq_stdout="$(<"${OUT}/stdout")"
                if [[ "${nojq_stdout}" == '{}' ]] && grep -qF 'jq not found' "${OUT}/stderr"; then
                    pass
                else
                    fail "(f) ${label}: hook-output.sh without jq must print exactly {} and report 'jq not found' on stderr (stdout='${nojq_stdout}', stderr='${R_ERR1}')"
                fi
            fi
        elif [[ "${R_CLASS}" != not-running ]]; then
            # The other lifecycle handlers need neither jq nor awk: they must
            # still launch (observed rc=0); no output expectation beyond that.
            pass
        else
            fail "(${tag}) ${label}: handler must still launch without ${tool} (rc=${R_RC}, ${R_CLASS}, stderr='${R_ERR1}')"
        fi
    done
done

# ── AC4: login shells ───────────────────────────────────────────────────
# Codex runs `$SHELL -lc "<command>"` with ${PLUGIN_ROOT} substituted into the
# text and PLUGIN_ROOT exported. HOME and XDG_CONFIG_HOME point at empty dirs
# so a developer's own profile cannot change the result; whatever the system
# profile does (printing, rewriting PATH) is deliberately NOT masked.
for sh_name in bash zsh fish; do
    if ! sh_path="$(command -v "${sh_name}")"; then
        skip_or_ci_fail "${sh_name} not installed — login-shell fixtures not run"
        continue
    fi
    launcher="login:${sh_path}"

    for p in "${!PL_NAME[@]}"; do
        for i in "${PRE_IDX[@]}"; do
            exec_handler "${i}" "${launcher}" codex "${CONF}" "${PL_FILE[p]}" export "${FLOW_DIR}"
            if [[ "${R_RC}" == "${RES_RC["${p}:${i}"]}" && "${R_CLASS}" == "${RES_CLASS["${p}:${i}"]}" ]]; then
                pass
            else
                fail "login ${sh_name}: ${H_LABEL[i]} on ${PL_NAME[p]} gave rc=${R_RC} (${R_CLASS}); baseline gave rc=${RES_RC["${p}:${i}"]} (${RES_CLASS["${p}:${i}"]}): ${R_ERR1}"
            fi
        done
    done

    for dirname in UNCONF CONF; do
        if [[ "${dirname}" == UNCONF ]]; then cwd="${UNCONF}"; else cwd="${CONF}"; fi
        for i in "${LIFE_IDX[@]}"; do
            ev="${H_EVENT[i]}"
            exec_handler "${i}" "${launcher}" codex "${cwd}" "${LIFE_STDIN["${ev}"]}" export "${FLOW_DIR}"
            if [[ "${R_RC}" == "${LIFE_RC["${dirname}:${i}"]}" ]] && lifecycle_contract_ok "${ev}" "${OUT}/stdout"; then
                pass
            else
                fail "login ${sh_name}: ${H_LABEL[i]} in ${dirname} gave rc=${R_RC} (${R_CLASS}) or broke the ${ev} contract; baseline rc=${LIFE_RC["${dirname}:${i}"]}: ${R_ERR1}"
            fi
        done
    done

    # Raw ${PLUGIN_ROOT} form with the variable unset (fixture (a)): records
    # how each shell reports it. fish parses `${...}` differently, so its code
    # is observed rather than presumed; it must never read as a pass.
    for i in "${!H_CMD[@]}"; do
        exec_handler "${i}" "${launcher}" raw "$(h_cwd "${i}")" "$(h_stdin "${i}")" unset ""
        observe "(a) login ${sh_name}, PLUGIN_ROOT unset, raw command: ${H_LABEL[i]}"
        if [[ "${sh_name}" == fish ]]; then
            if [[ "${R_RC}" != 0 && "${R_CLASS}" == not-running ]]; then
                pass
            else
                fail "(a) login fish: ${H_LABEL[i]} unset PLUGIN_ROOT must be non-zero/not-running (rc=${R_RC}, ${R_CLASS}): ${R_ERR1}"
            fi
        elif [[ "${R_RC}" == 127 && "${R_CLASS}" == not-running ]]; then
            pass
        else
            fail "(a) login ${sh_name}: ${H_LABEL[i]} unset PLUGIN_ROOT must be 127/not-running (rc=${R_RC}, ${R_CLASS}): ${R_ERR1}"
        fi
    done
done

# ── AC5: documentation table sync ───────────────────────────────────────
# extract_symptom_table <doc> -- the table rows (lines starting `|`) of the
# exact `## Hook launch failures` section. Fence-aware: a heading-shaped or
# row-shaped line inside a code fence neither starts nor ends the section nor
# contributes rows (gotchas rule 29). Pure.
extract_symptom_table() {
    awk -v heading="${DOC_HEADING}" '
        /^```/ { infence = !infence; next }
        !infence && /^## / { insec = ($0 == heading); next }
        insec && !infence && /^\|/ { print }
    ' "$1"
}

# missing_code_rows <table-file> -- print every observed exit code that has no
# row starting `| <code> |`. Pure.
missing_code_rows() {
    local code
    for code in "${!OBSERVED[@]}"; do
        grep -q -- "^| ${code} |" "$1" || echo "${code}"
    done
}

# 1. Self-test on a synthetic doc: the extractor must ignore fenced decoys and
#    other sections, and the check must catch a deleted 126 row.
SYN_DIR="$(mktemp -d "${TEST_ROOT}/syn.XXXXXX")" || {
    echo "FAIL: test setup: mktemp -d failed" >&2
    exit 1
}
SYN_DOC="${SYN_DIR}/codex.md"
{
    printf '%s\n' '# Doc' '' '```text' "${DOC_HEADING}" '| 999 | fenced decoy |' '```' ''
    printf '%s\n' "${DOC_HEADING}" '' '| Exit code | Cause |' '|---|---|'
    for code in "${!OBSERVED[@]}"; do
        printf '| %s | synthetic row |\n' "${code}"
    done
    printf '%s\n' '' '```text' '## Not a heading' '| 888 | fenced row |' '```' '' '| 127 | after the fence |' ''
    printf '%s\n' '## Next section' '| 555 | outside the section |'
} > "${SYN_DOC}"
SYN_TABLE="${SYN_DIR}/table.txt"
if extract_symptom_table "${SYN_DOC}" > "${SYN_TABLE}" \
    && grep -q '^| 127 | after the fence |' "${SYN_TABLE}" \
    && ! grep -qE '^\| (999|888|555) \|' "${SYN_TABLE}"; then
    pass
else
    fail "doc-sync self-test: extract_symptom_table must be fence-aware and stop at the next heading"
fi
if [[ -z "$(missing_code_rows "${SYN_TABLE}")" ]]; then
    pass
else
    fail "doc-sync self-test: a complete synthetic table must have no missing rows"
fi
grep -v '^| 126 |' "${SYN_TABLE}" > "${SYN_DIR}/table-no126.txt"
[[ $? -le 1 ]] || fail "doc-sync self-test: could not build the 126-less table"
if [[ -n "${OBSERVED[126]:-}" ]]; then
    synthetic_missing="$(missing_code_rows "${SYN_DIR}/table-no126.txt")"
    if grep -qx '126' <<< "${synthetic_missing}"; then
        pass
    else
        fail "doc-sync self-test: deleting the 126 row was NOT detected (regression: the check is vacuous)"
    fi
else
    fail "doc-sync self-test: fixture (d) never observed exit 126, so the injected regression cannot be exercised"
fi

# 2. The real doc.
REAL_TABLE="${SYN_DIR}/real-table.txt"
if [[ ! -f "${CODEX_DOC}" ]]; then
    fail "doc sync: ${CODEX_DOC} does not exist"
elif ! extract_symptom_table "${CODEX_DOC}" > "${REAL_TABLE}"; then
    fail "doc sync: could not read ${CODEX_DOC}"
elif [[ ! -s "${REAL_TABLE}" ]]; then
    fail "doc sync: ${CODEX_DOC} has no '${DOC_HEADING}' section with a table"
else
    pass
    for code in "${!OBSERVED[@]}"; do
        if grep -q -- "^| ${code} |" "${REAL_TABLE}"; then
            pass
        else
            fail "doc sync: ${CODEX_DOC} '${DOC_HEADING}' has no row for observed exit code ${code}"
        fi
    done
    # Injected regression on a COPY of the real doc: dropping its 126 row must
    # make the same check fail.
    DOC_COPY="${SYN_DIR}/codex-copy.md"
    grep -v '^| 126 |' "${CODEX_DOC}" > "${DOC_COPY}"
    [[ $? -le 1 ]] || fail "doc sync: could not build the 126-less doc copy"
    COPY_TABLE="${SYN_DIR}/copy-table.txt"
    if grep -q '^| 126 |' "${REAL_TABLE}"; then
        extract_symptom_table "${DOC_COPY}" > "${COPY_TABLE}"
        copy_missing="$(missing_code_rows "${COPY_TABLE}")"
        if grep -qx '126' <<< "${copy_missing}"; then
            pass
        else
            fail "doc sync: removing the 126 row from a doc copy was NOT detected"
        fi
    else
        fail "doc sync: the real table has no 126 row, so the injected-regression check cannot run"
    fi
fi

# ── Summary ─────────────────────────────────────────────────────────────
observed_codes="$(printf '%s\n' "${!OBSERVED[@]}" | sort -n | tr '\n' ' ')"
echo "observed exit codes: ${observed_codes}"
finish
