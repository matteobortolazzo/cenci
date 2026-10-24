#!/usr/bin/env bash
# guard-canary.sh -- the guard canary preflight helper (#1213).
#
# Codex skips pending or untrusted plugin hooks, so an installed flow plugin does
# not mean the write guards (check-sensitive-files.sh, guard-main-worktree.sh)
# are watching a session. A mutating Codex workflow runs this helper first:
#
#   bash "${PLUGIN_ROOT}/codex/guard-canary.sh" plan
#       Builds one unique probe per (matcher group, guard) pair of
#       ${PLUGIN_ROOT}/codex/hooks.json (four with today's manifest) under a
#       fresh 0700 run dir outside the main worktree. Before printing anything it
#       runs both REAL guard scripts on a synthetic payload per probe and
#       requires that exactly the expected guard exits 2 with its own message.
#       stdout (only when the whole plan holds):
#         CANARY_PLAN=ok
#         CANARY_RUN_DIR=<dir>
#         CANARY_MAIN_ROOT=<dir>
#         PROBE<TAB><n><TAB><shell|apply_patch><TAB><guard><TAB><group><TAB><target>
#         INSTRUCTION: ...
#       A plan that cannot be made prints CANARY_STATUS=refused and exits 1.
#
#   The session then makes one tool call per PROBE line and judges each result
#   from the tool output: "denied" only when the result shows the guard's own
#   message (Refusing to write / targets the main worktree).
#
#   bash "${PLUGIN_ROOT}/codex/guard-canary.sh" check <run-dir> <n>=denied|not-denied...
#       Removes every probe target that exists (a "denied" claim cannot override
#       a file on disk), reports the removal as `REMOVED: <path>` and a removal
#       that cannot happen as `LEFTOVER: <exact path>`, then prints
#       CANARY_STATUS=active (exit 0) or CANARY_STATUS=refused (exit 1) with the
#       refusal text. active needs every probe denied, nothing written, nothing
#       left over. The run dir is removed unless a leftover sits inside it.
#
# Exit codes: 0 active/planned, 1 refused, 2 usage error. Fail closed: an
# unknown matcher or guard, an unreadable manifest, a missing jq, a self-check
# mismatch, or a run dir that is not ours all refuse.
#
# Hook trust is only ever REPORTED here, never changed. In a sandbox session
# (CENCI_SANDBOX=1 plus a hard-coded container marker, cnh_is_sandbox) the
# output says "hook trust bypassed by launcher (explicit flag)".
set -u
unset CDPATH

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)" || exit 2
PLUGIN_ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)" || exit 2
SELF="${SCRIPT_DIR}/${BASH_SOURCE[0]##*/}"
MANIFEST="${PLUGIN_ROOT_DIR}/codex/hooks.json"
GUARD_DIR="${PLUGIN_ROOT_DIR}/hooks/scripts"
SANDBOX_LABEL="hook trust bypassed by launcher (explicit flag)"
RUN_PREFIX="cenci-guard-canary-"
HEX_RE='^[0-9a-f]{16}$'
GUARD_CMD_RE='^"\$\{PLUGIN_ROOT\}/hooks/scripts/(check-sensitive-files|guard-main-worktree)\.sh"$'

CREATED_RUN_DIR=""

# shellcheck source=native-hooks-lib.sh
if ! source "${SCRIPT_DIR}/native-hooks-lib.sh"; then
    printf 'CANARY_STATUS=refused\nGuard canary refused: cannot load %s/native-hooks-lib.sh\n' "${SCRIPT_DIR}"
    exit 1
fi

IS_SANDBOX=0
if cnh_is_sandbox; then
    IS_SANDBOX=1
fi

usage() {
    echo "usage: ${SELF##*/} plan | check <run-dir> <n>=denied|not-denied..." >&2
    exit 2
}

# remove_run_dir <dir> -- remove a run dir this process created (shape-checked).
remove_run_dir() {
    local d="$1"
    [[ -n "${d}" && "${d##*/}" =~ ^${RUN_PREFIX}[0-9a-f]{16}$ && -d "${d}" && ! -L "${d}" ]] || return 1
    rm -rf -- "${d}"
}

# refuse_plan <reason> -- the plan could not be made: print the refusal, remove
# any run dir already created, and exit 1.
refuse_plan() {
    if [[ -n "${CREATED_RUN_DIR}" ]]; then
        remove_run_dir "${CREATED_RUN_DIR}" || true
    fi
    printf 'CANARY_STATUS=refused\n'
    printf 'Guard canary refused: %s\n' "$1"
    printf 'The cenci write guards are not confirmed active, so do not start any mutation.\n'
    printf 'If the hooks are pending or untrusted in Codex, review them with /hooks and rerun.\n'
    exit 1
}

# has_control <text> -- 0 when the text carries a control character.
has_control() {
    local LC_ALL=C
    [[ "$1" == *[[:cntrl:]]* ]]
}

# canon_dir <dir> -- echo the canonical (pwd -P) form of an existing directory.
canon_dir() {
    (cd "$1" 2>/dev/null && pwd -P)
}

# inside_or_equal <path> <root> -- 0 when <path> is <root> or below it.
inside_or_equal() {
    [[ "$1" == "$2" || "$1" == "$2"/* ]]
}

# main_worktree_root -- echo the main worktree root of the repository holding the
# current directory (the parent of the common `.git` directory; for anything
# else the toplevel, mirroring guard-main-worktree.sh's SCOPE_ROOT fallback).
main_worktree_root() {
    local top common abs
    top="$(git rev-parse --show-toplevel 2>/dev/null)" || return 1
    top="$(canon_dir "${top}")" || return 1
    common="$(git rev-parse --git-common-dir 2>/dev/null)" || return 1
    case "${common}" in
        /*) ;;
        *) common="${PWD}/${common}" ;;
    esac
    abs="$(canon_dir "${common}")" || return 1
    if [[ "${abs##*/}" == .git && "${top}" == "${abs%/*}"/* ]]; then
        printf '%s\n' "${abs%/*}"
    else
        printf '%s\n' "${top}"
    fi
}

# probe_payload <tool: shell|apply_patch> <cwd> <target> -- the synthetic
# PreToolUse payload for a probe, shaped like the ones Codex sends.
probe_payload() {
    local tool="$1" cwd="$2" target="$3" quoted cmd
    if [[ "${tool}" == shell ]]; then
        quoted="${target//\'/\'\\\'\'}"
        jq -n --arg cwd "${cwd}" --arg cmd "printf x > '${quoted}'" \
            '{hook_event_name: "PreToolUse", tool_name: "Bash", cwd: $cwd, tool_input: {command: $cmd}}'
    else
        cmd="$(printf '*** Begin Patch\n*** Add File: %s\n+x\n*** End Patch' "${target}")" || return 1
        jq -n --arg cwd "${cwd}" --arg cmd "${cmd}" \
            '{hook_event_name: "PreToolUse", tool_name: "apply_patch", cwd: $cwd, tool_input: {command: $cmd}}'
    fi
}

# guard_message <guard> -- the text the guard's own refusal carries.
guard_message() {
    case "$1" in
        check-sensitive-files) printf '%s' 'Refusing to write' ;;
        guard-main-worktree) printf '%s' 'targets the main worktree' ;;
    esac
}

# self_check_probe <n> <tool> <guard> <target> -- run both real guards on the
# probe's payload: exactly <guard> must exit 2 with its message, the other must
# allow. Echoes the mismatch description and returns 1 on any doubt.
self_check_probe() {
    local n="$1" tool="$2" guard="$3" target="$4" payload g script rc err msg want
    payload="$(probe_payload "${tool}" "${PWD}" "${target}")" || {
        echo "probe ${n}: could not build the self-check payload"
        return 1
    }
    for g in check-sensitive-files guard-main-worktree; do
        script="${GUARD_DIR}/${g}.sh"
        if [[ ! -f "${script}" || ! -x "${script}" ]]; then
            echo "probe ${n}: guard script ${script} is missing or not executable"
            return 1
        fi
        err="$(printf '%s' "${payload}" | env -u CENCI_CODEX_HOOK_CAPTURE_DIR "${script}" 2>&1 >/dev/null)"
        rc=$?
        msg="$(guard_message "${g}")"
        if [[ "${g}" == "${guard}" ]]; then
            want="exit 2 with '${msg}'"
            if [[ "${rc}" -ne 2 || "${err}" != *"${msg}"* ]]; then
                echo "probe ${n} (${tool}): ${g} should deny its own probe (${want}) but exited ${rc}"
                return 1
            fi
        elif [[ "${rc}" -ne 0 ]]; then
            echo "probe ${n} (${tool}): ${g} must allow a probe meant for ${guard}, but exited ${rc}, so the probe is not single-guard"
            return 1
        fi
    done
    return 0
}

do_plan() {
    local pairs group matcher cmd guard slug tool n=0 main_root base cand canon hex run_dir target msg i
    local -a p_n=() p_tool=() p_guard=() p_group=() p_target=() skipped=()

    command -v jq >/dev/null 2>&1 || refuse_plan "jq is required to read the hook manifest and was not found on PATH"
    command -v git >/dev/null 2>&1 || refuse_plan "git is required and was not found on PATH"
    [[ -r "${MANIFEST}" ]] || refuse_plan "the hook manifest ${MANIFEST} is missing or unreadable"

    pairs="$(jq -r '
        (.hooks.PreToolUse // [])
        | if type == "array" then . else error("PreToolUse is not an array") end
        | to_entries[]
        | (.value.matcher // "") as $m
        | (.value.hooks // [])[]
        | [$m, (.command // "")] | @tsv' "${MANIFEST}" 2>/dev/null)" \
        || refuse_plan "the hook manifest ${MANIFEST} is unreadable or not the expected shape"
    [[ -n "${pairs}" ]] || refuse_plan "the hook manifest ${MANIFEST} lists no PreToolUse guard, so there is nothing to probe"

    main_root="$(main_worktree_root)" || refuse_plan "the current directory is not inside a git repository"
    has_control "${main_root}" && refuse_plan "the main worktree path contains a control character"

    while IFS=$'\t' read -r matcher cmd; do
        if [[ ! "${cmd}" =~ ${GUARD_CMD_RE} ]]; then
            refuse_plan "unrecognized guard command in the manifest (only check-sensitive-files.sh and guard-main-worktree.sh are known): ${cmd//[[:cntrl:]]/?}"
        fi
        guard="${BASH_REMATCH[1]}"
        case "${matcher}" in
            "Write|Edit") group="Write|Edit" slug="write-edit" tool="apply_patch" ;;
            Bash) group="Bash" slug="bash" tool="shell" ;;
            *) refuse_plan "unrecognized matcher '${matcher//[[:cntrl:]]/?}' in the manifest (only 'Write|Edit' and 'Bash' are known)" ;;
        esac
        n=$((n + 1))
        p_n+=("${n}")
        p_tool+=("${tool}")
        p_guard+=("${guard}")
        p_group+=("${group}")
        p_target+=("${slug}")  # replaced by the real target once the run dir exists
    done <<< "${pairs}"

    # Run dir: a fresh 0700 directory under $TMPDIR (else /tmp), never inside the
    # main worktree.
    base=""
    for cand in "${TMPDIR:-}" /tmp; do
        [[ -n "${cand}" ]] || continue
        if [[ "${cand}" != /* ]] || ! canon="$(canon_dir "${cand}")" || has_control "${canon}"; then
            skipped+=("${cand}")
            continue
        fi
        if inside_or_equal "${canon}" "${main_root}"; then
            skipped+=("${cand}")
            continue
        fi
        base="${canon}"
        break
    done
    [[ -n "${base}" ]] || refuse_plan "no temp directory outside the main worktree is available for the run dir"

    hex="$(od -An -N8 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')" || hex=""
    [[ "${hex}" =~ ${HEX_RE} ]] || refuse_plan "could not generate a run id"
    run_dir="${base}/${RUN_PREFIX}${hex}"
    mkdir -m 700 "${run_dir}" 2>/dev/null || refuse_plan "could not create the run dir ${run_dir}"
    CREATED_RUN_DIR="${run_dir}"
    if [[ -L "${run_dir}" || ! -d "${run_dir}" || ! -O "${run_dir}" ]]; then
        refuse_plan "the run dir ${run_dir} is not a directory owned by the current user"
    fi
    if inside_or_equal "$(canon_dir "${run_dir}")" "${main_root}"; then
        refuse_plan "the run dir resolves inside the main worktree"
    fi

    for i in "${!p_n[@]}"; do
        n="${p_n[i]}"
        if [[ "${p_guard[i]}" == check-sensitive-files ]]; then
            target="${run_dir}/${p_target[i]}-${n}.env"
        else
            target="${main_root}/${RUN_PREFIX}${hex}-${n}.txt"
        fi
        p_target[i]="${target}"
        if ! msg="$(self_check_probe "${n}" "${p_tool[i]}" "${p_guard[i]}" "${target}")"; then
            refuse_plan "the probe self-check failed against the real guard scripts: ${msg}. This is expected when the repository is not configured for cenci, lives under /tmp, or a guard script is broken."
        fi
    done

    {
        printf 'V1\n'
        printf 'HEX\t%s\n' "${hex}"
        printf 'MAIN\t%s\n' "${main_root}"
        for i in "${!p_n[@]}"; do
            printf 'PROBE\t%s\t%s\t%s\t%s\t%s\n' "${p_n[i]}" "${p_tool[i]}" "${p_guard[i]}" "${p_group[i]}" "${p_target[i]}"
        done
    } > "${run_dir}/probes.tsv" || refuse_plan "could not record the probes in ${run_dir}"

    {
        printf 'CANARY_PLAN=ok\n'
        printf 'CANARY_RUN_DIR=%s\n' "${run_dir}"
        printf 'CANARY_MAIN_ROOT=%s\n' "${main_root}"
        if [[ "${#skipped[@]}" -gt 0 ]]; then
            printf 'NOTE: TMPDIR (%s) is unusable for the run dir (relative, missing, or inside the main worktree); using %s.\n' "${skipped[0]}" "${base}"
        fi
        for i in "${!p_n[@]}"; do
            printf 'PROBE\t%s\t%s\t%s\t%s\t%s\n' "${p_n[i]}" "${p_tool[i]}" "${p_guard[i]}" "${p_group[i]}" "${p_target[i]}"
        done
        printf 'INSTRUCTION: for each PROBE line make exactly one call of its tool that writes the single line x to its target: tool shell runs printf x > <target> with the shell tool; tool apply_patch adds that file with apply_patch. Make no other call, never retry a probe, and never work around a refusal.\n'
        printf 'INSTRUCTION: judge each probe from its own tool result: denied only when the result shows the guard message (Refusing to write ... or ... targets the main worktree); anything else is not-denied.\n'
        printf 'INSTRUCTION: then run: bash "%s" check "%s" 1=<denied|not-denied> 2=<denied|not-denied> ... (one entry per PROBE line). Proceed only on CANARY_STATUS=active.\n' "${SELF}" "${run_dir}"
    }
    CREATED_RUN_DIR=""
    exit 0
}

# trust_note -- the one sandbox-only trust line (empty on the host).
trust_note() {
    if [[ "${IS_SANDBOX}" -eq 1 ]]; then
        printf 'Hook trust: %s. The launcher passed the bypass flag; cenci never changes hook trust.\n' "${SANDBOX_LABEL}"
    fi
}

do_check() {
    [[ "$#" -ge 1 ]] || usage
    local run_dir="$1" canon base_name hex main_root pf line kind a b c d e f
    local -a p_n=() p_tool=() p_guard=() p_group=() p_target=() bad=() lines=()
    local -a keys=()
    local verdicts=$'\n' arg key found reported i n t written status active=1 keep_run_dir=0 leftover_any=0

    shift
    # Argument tokens: n=denied|not-denied (bash 3.2 has no associative arrays,
    # so the results live in one newline-delimited string). Anything else, or a
    # repeated probe number, is recorded and refuses, but the cleanup below
    # still runs.
    for arg in "$@"; do
        if [[ "${arg}" =~ ^([0-9]+)=(denied|not-denied)$ ]]; then
            key="${BASH_REMATCH[1]}"
            if [[ "${verdicts}" == *$'\n'"${key}="* ]]; then
                bad+=("${arg} (repeated probe number)")
            else
                verdicts="${verdicts}${key}=${BASH_REMATCH[2]}"$'\n'
                keys+=("${key}")
            fi
        else
            bad+=("${arg//[[:cntrl:]]/?}")
        fi
    done

    refuse_check() {
        printf 'CANARY_STATUS=refused\n'
        printf 'Guard canary refused: %s\n' "$1"
        printf 'The cenci write guards are not confirmed active; do not start any mutation. Nothing was removed.\n'
        exit 1
    }

    [[ "${run_dir}" == /* ]] || refuse_check "the run dir '${run_dir//[[:cntrl:]]/?}' is not an absolute path"
    base_name="${run_dir##*/}"
    [[ "${base_name}" =~ ^${RUN_PREFIX}([0-9a-f]{16})$ ]] || refuse_check "'${run_dir//[[:cntrl:]]/?}' is not a guard canary run dir"
    hex="${BASH_REMATCH[1]}"
    [[ -d "${run_dir}" && ! -L "${run_dir}" && -O "${run_dir}" ]] || refuse_check "the run dir ${run_dir} does not exist or is not a directory owned by the current user"
    canon="$(canon_dir "${run_dir}")" || refuse_check "the run dir ${run_dir} cannot be resolved"
    [[ "${canon}" == "${run_dir}" ]] || refuse_check "the run dir ${run_dir} is not in canonical form"
    pf="${run_dir}/probes.tsv"
    [[ -f "${pf}" && ! -L "${pf}" ]] || refuse_check "the run dir ${run_dir} has no probe record"

    main_root=""
    while IFS=$'\t' read -r kind a b c d e f; do
        case "${kind}" in
            HEX) [[ "${a}" == "${hex}" ]] || refuse_check "the probe record belongs to a different run" ;;
            MAIN) main_root="${a}" ;;
            PROBE)
                p_n+=("${a}") p_tool+=("${b}") p_guard+=("${c}") p_group+=("${d}") p_target+=("${e}")
                ;;
        esac
    done < "${pf}"
    [[ "${main_root}" == /* && "${#p_n[@]}" -gt 0 ]] || refuse_check "the probe record in ${run_dir} is incomplete"
    if inside_or_equal "${canon}" "${main_root}"; then
        refuse_check "the run dir ${run_dir} is inside the main worktree"
    fi

    # Strict target shapes: the only paths this helper ever removes.
    for i in "${!p_n[@]}"; do
        n="${p_n[i]}"
        t="${p_target[i]}"
        [[ "${n}" =~ ^[0-9]+$ ]] || refuse_check "the probe record in ${run_dir} is malformed"
        case "${p_tool[i]}:${p_guard[i]}" in
            shell:check-sensitive-files | shell:guard-main-worktree | apply_patch:check-sensitive-files | apply_patch:guard-main-worktree) ;;
            *) refuse_check "the probe record in ${run_dir} is malformed" ;;
        esac
        if [[ "${p_guard[i]}" == check-sensitive-files ]]; then
            [[ "${t}" == "${run_dir}"/*.env && "${t#"${run_dir}"/}" != */* ]] || refuse_check "probe ${n} has an unexpected target path"
        else
            [[ "${t}" == "${main_root}/${RUN_PREFIX}${hex}-${n}.txt" ]] || refuse_check "probe ${n} has an unexpected target path"
        fi
    done
    for key in ${keys[@]+"${keys[@]}"}; do
        found=0
        for n in "${p_n[@]}"; do
            [[ "${n}" == "${key}" ]] && found=1
        done
        [[ "${found}" -eq 1 ]] || bad+=("${key} (no such probe)")
    done

    # Cleanup first, judge second: whatever got written is removed no matter what
    # the session reported.
    for i in "${!p_n[@]}"; do
        n="${p_n[i]}"
        t="${p_target[i]}"
        written=0
        if [[ -e "${t}" || -L "${t}" ]]; then
            written=1
            rm -f -- "${t}" 2>/dev/null
            if [[ -e "${t}" || -L "${t}" ]]; then
                lines+=("LEFTOVER: ${t}")
                leftover_any=1
                case "${t}" in
                    "${run_dir}"/*) keep_run_dir=1 ;;
                esac
            else
                lines+=("REMOVED: ${t}")
            fi
        fi
        reported=none
        case "${verdicts}" in
            *$'\n'"${n}=denied"$'\n'*) reported=denied ;;
            *$'\n'"${n}=not-denied"$'\n'*) reported=not-denied ;;
        esac
        if [[ "${written}" -eq 1 ]]; then
            status="WRITTEN -- no guard stopped this probe, so the guard is inactive"
            if [[ "${reported}" == denied ]]; then
                status="WRITTEN although reported denied -- the file on disk wins, so the guard is inactive"
            fi
            active=0
        elif [[ "${reported}" == denied ]]; then
            status="denied by ${p_guard[i]}"
        elif [[ "${reported}" == not-denied ]]; then
            status="NO GUARD MESSAGE -- not written, but not denied by the guard either (inconclusive)"
            active=0
        else
            status="NO RESULT -- the session reported nothing for this probe (inconclusive)"
            active=0
        fi
        lines+=("PROBE_RESULT: ${n} (${p_tool[i]}, ${p_guard[i]}, group ${p_group[i]}): ${status}")
    done
    [[ "${#bad[@]}" -eq 0 ]] || active=0
    [[ "${leftover_any}" -eq 0 ]] || active=0

    if [[ "${keep_run_dir}" -eq 0 ]]; then
        if ! remove_run_dir "${run_dir}" && [[ -e "${run_dir}" || -L "${run_dir}" ]]; then
            lines+=("LEFTOVER: ${run_dir}")
            active=0
        fi
    fi

    {
        for line in "${lines[@]}"; do
            printf '%s\n' "${line}"
        done
        for line in "${bad[@]+"${bad[@]}"}"; do
            printf 'BAD_RESULT: unrecognized result token %s\n' "${line}"
        done
        if [[ "${active}" -eq 1 ]]; then
            printf 'CANARY_STATUS=active\n'
            printf 'Every probe was denied by its own guard and nothing was written: the cenci write guards are active for this session.\n'
            trust_note
        else
            printf 'CANARY_STATUS=refused\n'
            printf 'GUARD CANARY REFUSED: the cenci write guards are not confirmed active, so protection is inactive and no mutation may start.\n'
            printf 'Likely cause: the cenci hooks are pending or untrusted in Codex, and Codex skips pending hooks. Review and enable them with /hooks, then rerun this workflow.\n'
            printf 'Reading the probe results: WRITTEN means a guard let the write through (hooks inactive); NO GUARD MESSAGE means nothing was written but the guard never answered (inconclusive: an approval prompt, Codex'"'"'s own sandbox, or a call that was never made).\n'
            if [[ "${leftover_any}" -eq 1 ]]; then
                printf 'Remove each LEFTOVER path above by hand; the canary could not.\n'
            fi
            trust_note
        fi
    }
    if [[ "${active}" -eq 1 ]]; then
        exit 0
    fi
    exit 1
}

case "${1:-}" in
    plan)
        [[ "$#" -eq 1 ]] || usage
        do_plan
        ;;
    check)
        shift
        do_check "$@"
        ;;
    *) usage ;;
esac
