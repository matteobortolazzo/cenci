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
#         COMMAND<TAB><n><TAB>printf x > '<target>'   (one per shell probe; an
#                                                     embedded ' is written '\'')
#         INSTRUCTION: ...
#       The COMMAND text is byte-identical to the command the self-check ran
#       through the guards. The run dir's probes.tsv is written only after the
#       self-check, and a receipt-* entry already in the run dir at that point
#       refuses the plan (a receipt written during plan proves nothing).
#       A plan that cannot be made prints CANARY_STATUS=refused and exits 1; if
#       its already-created run dir cannot be removed it also prints
#       `LEFTOVER: <run dir>`.
#
#   The session then makes one tool call per PROBE line (a shell probe runs its
#   COMMAND verbatim) and judges each result from the tool output: "denied" only
#   when the result shows the guard's own message (Refusing to write / targets
#   the main worktree), "blocked" when Codex's sandbox or approval policy
#   refused the call before any guard saw it, "not-attempted" when the call was
#   never made, "not-denied" for anything else.
#
#   Guard receipts (#1411): when a guard denies a probe target that its run
#   dir's probes.tsv lists for that guard, the guard itself writes
#   <run-dir>/receipt-<guard>-<n>.XXXXXX holding one line
#   V1<TAB><guard><TAB><hex><TAB><n><TAB><tool_name> (see canary_receipt in
#   both guard scripts). A receipt catches a session that misreports; it cannot
#   stop one that deliberately forges a receipt, so active stays a readiness
#   check, not proof.
#
#   bash "${PLUGIN_ROOT}/codex/guard-canary.sh" check <run-dir> <n>=denied|not-denied|blocked|not-attempted...
#       Removes every probe target that exists (a "denied" claim cannot override
#       a file on disk), reports the removal as `REMOVED: <path>` and a removal
#       that cannot happen as `LEFTOVER: <exact path>`, validates every
#       receipt-* entry (regular file, not a symlink, owned by the current user,
#       one V1 line whose hex, probe number, guard and tool match probes.tsv;
#       anything else prints `BAD_RECEIPT: <name>: <reason>` and refuses), then
#       prints CANARY_STATUS=active (exit 0) or CANARY_STATUS=refused (exit 1)
#       with the refusal text. active needs every probe reported denied AND
#       backed by a valid receipt from its own guard, nothing written, nothing
#       left over. Precedence per probe: WRITTEN > denied with a receipt >
#       REPORTED DENIED WITHOUT GUARD RECEIPT > BLOCKED BEFORE THE GUARD /
#       NOT ATTEMPTED / NO GUARD MESSAGE / NO RESULT. A valid receipt on a
#       probe reported blocked, not-attempted or not-denied prefixes that line
#       with GUARD RECEIPT PRESENT BUT REPORTED <verdict>; a valid receipt on a
#       probe with no reported result still prints plain NO RESULT. The run
#       dir (with its receipts) is removed unless a leftover sits inside it.
#       A refusal before the cleanup loop (bad run dir argument, untrusted
#       record) removes nothing and names the glob to delete by hand.
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
TAB=$'\t'
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
    echo "usage: ${SELF##*/} plan | check <run-dir> <n>=denied|not-denied|blocked|not-attempted..." >&2
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
    local leftover=0
    if [[ -n "${CREATED_RUN_DIR}" ]]; then
        # A failed removal is never hidden: it is reported with its exact path,
        # the same LEFTOVER line `check` prints.
        if ! remove_run_dir "${CREATED_RUN_DIR}" && [[ -e "${CREATED_RUN_DIR}" || -L "${CREATED_RUN_DIR}" ]]; then
            printf 'LEFTOVER: %s\n' "${CREATED_RUN_DIR}"
            leftover=1
        fi
    fi
    printf 'CANARY_STATUS=refused\n'
    printf 'Guard canary refused: %s\n' "$1"
    printf 'The cenci write guards are not confirmed active, so do not start any mutation.\n'
    if [[ "${leftover}" -eq 1 ]]; then
        printf 'Remove each LEFTOVER path above by hand; the canary could not.\n'
    fi
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

# shell_probe_command <target> -- the one shell command a shell probe runs:
# printf x > '<target>', single-quoted with an embedded ' written as '\''. Both
# the self-check payload and the printed COMMAND line come from here, so the
# session runs exactly the command the real guards were checked against.
shell_probe_command() {
    local quoted="${1//\'/\'\\\'\'}"
    printf "printf x > '%s'" "${quoted}"
}

# probe_payload <tool: shell|apply_patch> <cwd> <target> -- the synthetic
# PreToolUse payload for a probe, shaped like the ones Codex sends.
probe_payload() {
    local tool="$1" cwd="$2" target="$3" cmd
    if [[ "${tool}" == shell ]]; then
        cmd="$(shell_probe_command "${target}")" || return 1
        jq -n --arg cwd "${cwd}" --arg cmd "${cmd}" \
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
    local pairs group matcher cmd guard slug tool n=0 main_root base cand canon hex run_dir run_canon target msg i e
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
    # Checked substitution: an empty canonical path would make inside_or_equal
    # false and let a run dir inside the main worktree through.
    run_canon="$(canon_dir "${run_dir}")" || refuse_plan "the run dir ${run_dir} cannot be resolved"
    [[ -n "${run_canon}" ]] || refuse_plan "the run dir ${run_dir} cannot be resolved"
    if inside_or_equal "${run_canon}" "${main_root}"; then
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

    # No receipt may exist yet: the guards only write one once probes.tsv lists
    # the probe, so anything here was planted while the plan ran and would
    # pre-satisfy check without the live session's hooks ever answering.
    for e in "${run_dir}"/receipt-*; do
        if [[ -e "${e}" || -L "${e}" ]]; then
            e="${e##*/}"
            refuse_plan "a guard receipt (${e//[[:cntrl:]]/?}) is already in the run dir before the plan is complete, so the run dir cannot be used as evidence"
        fi
    done

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
        for i in "${!p_n[@]}"; do
            if [[ "${p_tool[i]}" == shell ]]; then
                printf 'COMMAND\t%s\t%s\n' "${p_n[i]}" "$(shell_probe_command "${p_target[i]}")"
            fi
        done
        printf 'INSTRUCTION: for each PROBE line make exactly one call of its tool that writes the single line x to its target: tool shell runs that probe'"'"'s COMMAND line verbatim (the text after COMMAND<TAB><n><TAB>, already single-quoted) with the shell tool; tool apply_patch adds that file with apply_patch. Make no other call, never retry a probe, and never work around a refusal.\n'
        printf 'INSTRUCTION: judge each probe from its own tool result: denied only when the result shows the guard message (Refusing to write ... or ... targets the main worktree); blocked when Codex'"'"'s sandbox or approval policy refused the call before any guard answered; not-attempted when the call was never made; not-denied for anything else.\n'
        printf 'INSTRUCTION: then run: bash "%s" check "%s" 1=<denied|not-denied|blocked|not-attempted> 2=<denied|not-denied|blocked|not-attempted> ... (one entry per PROBE line). Proceed only on CANARY_STATUS=active.\n' "${SELF}" "${run_dir}"
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
    local -a keys=() p_receipt=() bad_receipts=()
    local verdicts=$'\n' arg key found reported i j n t written status active=1 keep_run_dir=0 leftover_any=0
    local hint_hex hint_root hint_trimmed
    local r rname reason size rcount rline rdata want_tool receipt_note
    local receipt_re="^V1${TAB}(check-sensitive-files|guard-main-worktree)${TAB}([0-9a-f]{16})${TAB}([0-9]+)${TAB}([A-Za-z0-9_]+)\$"

    shift
    # Argument tokens: n=denied|not-denied|blocked|not-attempted (bash 3.2 has
    # no associative arrays, so the results live in one newline-delimited
    # string). Anything else, or a repeated probe number, is recorded and
    # refuses, but the cleanup below still runs.
    for arg in "$@"; do
        if [[ "${arg}" =~ ^([0-9]+)=(denied|not-denied|blocked|not-attempted)$ ]]; then
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

    # An early refusal happens before the cleanup loop and the record is not
    # trusted there, so nothing is removed -- yet canary files may already sit in
    # the main worktree. The refusal therefore says how to remove them by hand:
    # the exact glob when the hex id (from the run dir NAME the session passed)
    # and the main root (from the session's own cwd, never from the record) are
    # both known, else a pointer to look for cenci-guard-canary-* files.
    hint_hex=""
    hint_root=""
    hint_trimmed="${run_dir}"
    while [[ "${#hint_trimmed}" -gt 1 && "${hint_trimmed}" == */ ]]; do
        hint_trimmed="${hint_trimmed%/}"
    done
    if [[ "${hint_trimmed##*/}" =~ ^${RUN_PREFIX}([0-9a-f]{16})$ ]]; then
        hint_hex="${BASH_REMATCH[1]}"
        if hint_root="$(main_worktree_root)" && [[ "${hint_root}" == /* ]] && ! has_control "${hint_root}"; then
            :
        else
            hint_hex=""
            hint_root=""
        fi
    fi

    refuse_check() {
        printf 'CANARY_STATUS=refused\n'
        printf 'Guard canary refused: %s\n' "$1"
        printf 'The cenci write guards are not confirmed active; do not start any mutation. Nothing was removed.\n'
        if [[ -n "${hint_hex}" ]]; then
            printf 'Canary files may still sit in the main worktree: delete any file matching %s/%s%s-*.txt by hand. The .env probe lives in the run dir %s; remove that directory by hand too.\n' \
                "${hint_root}" "${RUN_PREFIX}" "${hint_hex}" "${run_dir//[[:cntrl:]]/?}"
        else
            printf 'Canary files may still sit in the main worktree: look for files named %s* there (and for a %s* run dir in the temp directory) and delete them by hand.\n' \
                "${RUN_PREFIX}" "${RUN_PREFIX}"
        fi
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

    # Guard receipts: only a valid one counts as the guard's own evidence, and
    # every invalid receipt-* entry refuses. A symlink is tested first and never
    # read, so check never follows it.
    for i in "${!p_n[@]}"; do
        p_receipt[i]=0
    done
    for r in "${run_dir}"/receipt-*; do
        [[ -e "${r}" || -L "${r}" ]] || continue
        rname="${r##*/}"
        rname="${rname//[[:cntrl:]]/?}"
        reason=""
        if [[ -L "${r}" ]]; then
            reason="is a symlink, which is never followed"
        elif [[ ! -f "${r}" ]]; then
            reason="is not a regular file"
        elif [[ ! -O "${r}" ]]; then
            reason="is not owned by the current user"
        else
            size="$(wc -c < "${r}" 2>/dev/null | tr -d '[:space:]')" || size=""
            if [[ ! "${size}" =~ ^[0-9]+$ || "${size}" -gt 512 ]]; then
                reason="is unreadable or too large to be a receipt"
            else
                # Bounded read: at most 513 bytes, so a file grown or swapped
                # after the size check above still cannot stall check (the
                # trailing x survives command substitution's newline stripping
                # and proves head succeeded). Residual: a FIFO swapped in after
                # the -f test blocks the open itself; that needs the same user
                # and only hangs this run.
                rdata=""
                { rdata="$(head -c 513 -- "${r}" && printf x)"; } 2>/dev/null || rdata=""
                rcount=0
                rline=""
                if [[ "${rdata}" != *x ]]; then
                    rcount=-1
                else
                    rdata="${rdata%x}"
                    if [[ -n "${rdata}" ]]; then
                        rline="${rdata%$'\n'}"
                        rcount=1
                        [[ "${rline}" == *$'\n'* ]] && rcount=2
                    fi
                fi
                if [[ "${rcount}" -lt 0 || "${#rdata}" -gt 512 ]]; then
                    reason="is unreadable or too large to be a receipt"
                elif [[ "${rcount}" -ne 1 ]]; then
                    reason="does not hold exactly one line"
                elif [[ ! "${rline}" =~ ${receipt_re} ]]; then
                    reason="is not a V1 receipt line"
                elif [[ "${BASH_REMATCH[2]}" != "${hex}" ]]; then
                    reason="names another run (${BASH_REMATCH[2]})"
                else
                    key="${BASH_REMATCH[3]}"
                    found=""
                    for j in "${!p_n[@]}"; do
                        [[ "${p_n[j]}" == "${key}" ]] && found="${j}"
                    done
                    if [[ -z "${found}" ]]; then
                        reason="names probe ${key}, which is not in the probe record"
                    else
                        if [[ "${p_tool[found]}" == shell ]]; then want_tool=Bash; else want_tool=apply_patch; fi
                        if [[ "${BASH_REMATCH[1]}" != "${p_guard[found]}" ]]; then
                            reason="names guard ${BASH_REMATCH[1]}, but probe ${key} is for ${p_guard[found]}"
                        elif [[ "${BASH_REMATCH[4]}" != "${want_tool}" ]]; then
                            reason="names tool ${BASH_REMATCH[4]}, but probe ${key} is a ${p_tool[found]} probe (tool ${want_tool})"
                        else
                            p_receipt[found]=1
                        fi
                    fi
                fi
            fi
        fi
        if [[ -n "${reason}" ]]; then
            bad_receipts+=("${rname}: ${reason}")
        fi
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
            *$'\n'"${n}=blocked"$'\n'*) reported=blocked ;;
            *$'\n'"${n}=not-attempted"$'\n'*) reported=not-attempted ;;
        esac
        receipt_note=""
        if [[ "${p_receipt[i]}" -eq 1 ]]; then
            receipt_note="GUARD RECEIPT PRESENT BUT REPORTED ${reported} -- "
        fi
        if [[ "${written}" -eq 1 ]]; then
            status="WRITTEN -- no guard stopped this probe, so the guard is inactive"
            if [[ "${reported}" == denied ]]; then
                status="WRITTEN although reported denied -- the file on disk wins, so the guard is inactive"
            fi
            active=0
        elif [[ "${reported}" == denied && "${p_receipt[i]}" -eq 1 ]]; then
            status="denied by ${p_guard[i]}"
        elif [[ "${reported}" == denied ]]; then
            status="REPORTED DENIED WITHOUT GUARD RECEIPT -- the session reported a denial, but ${p_guard[i]} left no valid receipt, so no guard is shown to have answered (a sandbox or approval refusal, or a misreport)"
            active=0
        elif [[ "${reported}" == blocked ]]; then
            status="${receipt_note}BLOCKED BEFORE THE GUARD (Codex sandbox or approval refusal) -- not written, and the guard never saw the call (inconclusive)"
            active=0
        elif [[ "${reported}" == not-attempted ]]; then
            status="${receipt_note}NOT ATTEMPTED -- the session never made this probe's call (inconclusive)"
            active=0
        elif [[ "${reported}" == not-denied ]]; then
            status="${receipt_note}NO GUARD MESSAGE -- not written, but not denied by the guard either (inconclusive)"
            active=0
        else
            status="NO RESULT -- the session reported nothing for this probe (inconclusive)"
            active=0
        fi
        lines+=("PROBE_RESULT: ${n} (${p_tool[i]}, ${p_guard[i]}, group ${p_group[i]}): ${status}")
    done
    [[ "${#bad[@]}" -eq 0 ]] || active=0
    [[ "${#bad_receipts[@]}" -eq 0 ]] || active=0
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
        for line in "${bad_receipts[@]+"${bad_receipts[@]}"}"; do
            printf 'BAD_RECEIPT: %s\n' "${line}"
        done
        if [[ "${active}" -eq 1 ]]; then
            printf 'CANARY_STATUS=active\n'
            printf 'Every probe was denied by its own guard, with that guard'"'"'s receipt, and nothing was written: the cenci write guards are active for this session.\n'
            trust_note
        else
            printf 'CANARY_STATUS=refused\n'
            printf 'GUARD CANARY REFUSED: the cenci write guards are not confirmed active, so protection is inactive and no mutation may start.\n'
            printf 'Likely cause: the cenci hooks are pending or untrusted in Codex, and Codex skips pending hooks. Review and enable them with /hooks, then rerun this workflow.\n'
            printf 'Reading the probe results: WRITTEN means a guard let the write through (hooks inactive); REPORTED DENIED WITHOUT GUARD RECEIPT means the session reported a denial but the guard left no valid receipt, so no guard is shown to have answered; BLOCKED BEFORE THE GUARD means Codex'"'"'s sandbox or approval policy refused the call before any guard saw it (inconclusive); NOT ATTEMPTED means the probe call was never made (inconclusive); NO GUARD MESSAGE means nothing was written but the guard never answered (inconclusive: an approval prompt, Codex'"'"'s own sandbox, or a call that was never made); NO RESULT means the session reported nothing for that probe (inconclusive). A BAD_RECEIPT line names a receipt that does not match this run'"'"'s probe record.\n'
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
