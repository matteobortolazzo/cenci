#!/usr/bin/env bash
# native-hooks.gate.test.sh -- deterministic, CI-safe coverage for the opt-in
# native Codex runner (native-hooks.test.sh) and its pure helpers
# (native-hooks-lib.sh) (#1212).
#
# The runner itself needs a real, authenticated Codex and a human to trust the
# plugin, so it is skipped by default and judged by the maintainer's run. What
# CAN be pinned without Codex is everything that decides whether it may run at
# all and how its evidence is handled:
#
#   G1  every "not run" path prints `not run: <reason>`, exits 0, never says
#       "passed", and never launches `codex exec`
#         (env gate unset, codex absent, version below 0.144.1, login failing)
#   G2  version edge cases (0.144.0 rejected; 0.144.1 and 0.150.0 accepted;
#       `codex-cli X.Y.Z` and bare X.Y.Z output formats)
#   G3  host mode NEVER passes --dangerously-bypass-hook-trust, not even with a
#       spoofed CENCI_SANDBOX=1 and no container marker file; sandbox mode
#       (CENCI_SANDBOX=1 AND a marker file) does
#   G4  helper tables: version parse/compare, sanitizer, verdict classifier,
#       host-vs-sandbox detection
#   G6  the guard canary preflight scenario (#1213), against the emulating
#       stub: hooks active -> the canary passes; hooks inactive -> it refuses,
#       names pending trust and /hooks and removes what was written; a result
#       that contradicts the trust state is a FAIL; the sandbox also runs a
#       trust-withheld variant that leaves out ONLY the trust flag; the host
#       never runs it
#
# A stub `codex` first on PATH records its argv and answers `--version` and
# `login status` from the environment; nothing here touches a real Codex.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="${SCRIPT_DIR}/native-hooks.test.sh"
LIB="${SCRIPT_DIR}/native-hooks-lib.sh"
BYPASS_FLAG="--dangerously-bypass-hook-trust"
MIN_VERSION="0.144.1"

FAILURES=0
PASSES=0

fail() {
    echo "FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

finish() {
    echo "passed: ${PASSES}, failed: ${FAILURES}"
    [[ "${FAILURES}" -eq 0 ]]
    exit $?
}

echo "native-hooks.gate.test.sh"

# ── Setup ───────────────────────────────────────────────────────────────
# Not under /tmp (the guards the runner exercises allowlist /tmp/*); checked
# mktemp with trap cleanup.
TEST_ROOT="$(mktemp -d /var/tmp/native-hooks-gate-test.XXXXXX)" || {
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

BASH_BIN="$(command -v bash)" || {
    echo "FAIL: test setup: bash not found on PATH" >&2
    exit 1
}

STUB_DIR="${TEST_ROOT}/stub-bin"
FARM_DIR="${TEST_ROOT}/farm-no-codex"
FAKE_HOME="${TEST_ROOT}/home"
RUN_CWD="${TEST_ROOT}/cwd"
RUNS_DIR="${TEST_ROOT}/runs"
mkdir_checked "${STUB_DIR}"
mkdir_checked "${FAKE_HOME}"
mkdir_checked "${RUN_CWD}"
mkdir_checked "${RUNS_DIR}"

if [[ ! -f "${RUNNER}" ]]; then
    fail "runner not found: ${RUNNER}"
    finish
fi
if [[ ! -f "${LIB}" ]]; then
    fail "helper library not found: ${LIB}"
    finish
fi

# The stub: record the argv of EVERY invocation (one line each), answer
# --version / login status from the environment, succeed at everything else.
#
# With STUB_EMULATE=1 `codex exec` also emulates the one tool the runner can
# drive without a patch engine: exec_command. It reads the exact command out of
# the prompt, runs the PreToolUse hooks the way Codex does (the plugin manifest
# named by STUB_PLUGIN_HOOKS with PLUGIN_ROOT=STUB_PLUGIN_ROOT, then the
# session's .codex/hooks.json), lets the tool continue unless a hook exits 2
# with stderr, and prints one `item.completed` JSONL event. apply_patch prompts
# produce no event at all (a model that never made the call).
#   STUB_CONFIG_HOOK_LIMIT=N   run at most N commands of the config-layer file
#   STUB_REDACT_BLOCK=1        replace a hook's block message with a generic one
cat > "${STUB_DIR}/codex" <<'SCRIPT'
#!/bin/sh
printf '%s\n' "$*" >> "${STUB_ARGV_LOG:?STUB_ARGV_LOG must be set}"
case "$1" in
    --version | -V)
        printf '%s\n' "${STUB_VERSION_OUTPUT-codex-cli 0.150.0}"
        exit 0
        ;;
    login)
        if [ "$2" = status ]; then
            exit "${STUB_LOGIN_RC-0}"
        fi
        exit 0
        ;;
    plugin)
        [ -z "${STUB_PLUGIN_LIST_OUTPUT:-}" ] || printf '%s\n' "${STUB_PLUGIN_LIST_OUTPUT}"
        exit 0
        ;;
    exec)
        shift
        all="$*"
        dir=""
        prompt=""
        trust_flag=0
        while [ $# -gt 0 ]; do
            if [ "$1" = "-C" ]; then
                dir="$2"
                shift 2
                continue
            fi
            if [ "$1" = "--dangerously-bypass-hook-trust" ]; then
                trust_flag=1
            fi
            prompt="$1"
            shift
        done
        # One record per `codex exec`: the flags (everything but the prompt) and
        # whether the prompt is a guard-canary probe, so a test can tell the
        # trust-withheld canary calls apart from every other call.
        tag=other
        case "$prompt" in
            *cenci-guard-canary-*) tag=canary ;;
        esac
        printf '%s\t%s\n' "$tag" "${all%"$prompt"}" >> "${STUB_ARGV_LOG}.exec"
        [ "${STUB_EMULATE:-0}" = 1 ] || exit 0
        tool_name=Bash
        match=Bash
        cmd=""
        if [ "${STUB_EMULATE_PATCH:-0}" = 1 ]; then
            case "$prompt" in
                *"call the apply_patch tool"*)
                    cmd=$(printf '%s\n' "$prompt" | sed -n '/^\*\*\* Begin Patch$/,/^\*\*\* End Patch$/p')
                    tool_name=apply_patch
                    match='Write|Edit'
                    ;;
            esac
        fi
        if [ -z "$cmd" ]; then
            cmd=$(printf '%s\n' "$prompt" | sed -n 's/^.*exactly this command: \(.*\)$/\1/p' | sed -n 1p)
        fi
        [ -n "$cmd" ] || exit 0
        work=$(mktemp -d) || exit 1
        : > "$work/block"
        payload=$(jq -n --arg cwd "$dir" --arg c "$cmd" --arg tn "$tool_name" \
            '{hook_event_name: "PreToolUse", tool_name: $tn, cwd: $cwd, tool_input: {command: $c}}')
        run_hooks() { # <manifest> <plugin-root-or-empty> <limit-or-empty>
            [ -f "$1" ] || return 0
            n=0
            jq -r --arg m "$match" '.hooks.PreToolUse[]? | select((.matcher // "") | test($m)) | .hooks[].command' "$1" > "$work/cmds" || return 0
            while IFS= read -r hc; do
                n=$((n + 1))
                if [ -n "$3" ] && [ "$n" -gt "$3" ]; then
                    break
                fi
                printf '%s' "$payload" | (cd "$dir" && PLUGIN_ROOT="$2" sh -c "$hc") > /dev/null 2> "$work/err"
                rc=$?
                if [ "$rc" = 2 ] && [ -s "$work/err" ]; then
                    if [ "${STUB_REDACT_BLOCK:-0}" = 1 ]; then
                        echo "BLOCKED: denied" >> "$work/block"
                    else
                        cat "$work/err" >> "$work/block"
                    fi
                fi
            done < "$work/cmds"
        }
        # Plugin hooks run unless the stub is told they are pending: with
        # STUB_TRUST_REQUIRED=1 they run only when the call carries
        # --dangerously-bypass-hook-trust. STUB_PATCH_HOOKS_OFF=1 leaves the
        # apply_patch tool unwatched (a Write|Edit alias that does not fire).
        if [ -n "${STUB_PLUGIN_HOOKS:-}" ] \
            && { [ "${STUB_TRUST_REQUIRED:-0}" != 1 ] || [ "$trust_flag" = 1 ]; } \
            && { [ "$tool_name" != apply_patch ] || [ "${STUB_PATCH_HOOKS_OFF:-0}" != 1 ]; }; then
            run_hooks "$STUB_PLUGIN_HOOKS" "$STUB_PLUGIN_ROOT" ""
        fi
        run_hooks "$dir/.codex/hooks.json" "" "${STUB_CONFIG_HOOK_LIMIT:-}"
        if [ -s "$work/block" ]; then
            status=declined
            output=$(cat "$work/block")
        elif [ "$tool_name" = apply_patch ]; then
            status=completed
            printf '%s\n' "$cmd" | sed -n 's/^\*\*\* Add File: //p' > "$work/targets"
            while IFS= read -r tgt; do
                [ -n "$tgt" ] || continue
                case "$tgt" in /*) ;; *) tgt="$dir/$tgt" ;; esac
                printf 'x\n' > "$tgt" 2>/dev/null
            done < "$work/targets"
            output="Done!"
        else
            status=completed
            output=$( (cd "$dir" && sh -c "$cmd") 2>&1 )
        fi
        rm -rf "$work"
        jq -n -c --arg c "$cmd" --arg o "$output" --arg s "$status" \
            '{type: "item.completed", item: {id: "item_0", type: "command_execution", command: $c, aggregated_output: $o, status: $s}}'
        exit 0
        ;;
esac
exit 0
SCRIPT
chmod +x "${STUB_DIR}/codex"

# build_farm <excluded-tool> <dir> -- a directory of symlinks to every
# executable on the current PATH (first match per name wins) except the
# excluded tool, used as the whole PATH so the tool is genuinely absent (not a
# shim that exits 127). Pure.
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

if ! build_farm codex "${FARM_DIR}"; then
    echo "FAIL: test setup: could not build a PATH without codex" >&2
    exit 1
fi
# Non-vacuity: codex is really gone from the farm while the tools a runner
# needs still resolve.
if [[ ! -e "${FARM_DIR}/codex" ]] \
    && ! PATH="${FARM_DIR}" "${BASH_BIN}" -c 'command -v codex >/dev/null 2>&1' \
    && PATH="${FARM_DIR}" "${BASH_BIN}" -c 'command -v git >/dev/null 2>&1 && command -v mktemp >/dev/null 2>&1'; then
    pass
else
    echo "FAIL: test setup: the codex-free PATH is not a faithful restriction" >&2
    exit 1
fi

STUB_PATH="${STUB_DIR}:${PATH}"
MISSING_MARKER="${TEST_ROOT}/no-such-container-marker"
MARKER_FILE="${TEST_ROOT}/container-marker"
MARKER_DIR="${TEST_ROOT}/container-marker-dir"
: > "${MARKER_FILE}"
mkdir_checked "${MARKER_DIR}"

RUN_N=0
OUT=""
R_RC=""
RUNNER_OVERRIDE=""

# run_runner <path> <env-assignment>... -- run the runner under a scrubbed
# environment (env -i: no ambient TMPDIR/CENCI_*/CI) with only the given
# variables. Sets OUT (the per-run dir) and R_RC. Pure: never calls fail().
# RUNNER_OVERRIDE, when set, names a runner copy to run instead.
run_runner() {
    local path="$1"
    shift
    RUN_N=$((RUN_N + 1))
    OUT="${RUNS_DIR}/${RUN_N}"
    mkdir -p "${OUT}" || return 1
    : > "${OUT}/argv"
    : > "${OUT}/argv.exec"
    (
        cd "${RUN_CWD}" || exit 97
        exec env -i HOME="${FAKE_HOME}" PATH="${path}" STUB_ARGV_LOG="${OUT}/argv" "$@" \
            "${BASH_BIN}" "${RUNNER_OVERRIDE:-${RUNNER}}"
    ) > "${OUT}/stdout" 2> "${OUT}/stderr"
    R_RC=$?
    printf '%s\n' "${R_RC}" > "${OUT}/rc"
}

# make_runner_copy <name> <markers> -- copy the runner and its helper library
# into a throwaway tree whose HARD-CODED container-marker list is <markers>
# (space-separated). This is the only way a test can stage a "real container"
# or a "real host" independent of the machine the suite runs on: the runner
# deliberately has no environment seam for the markers (S2), so a host run can
# never be made to look like a sandbox run from its environment. Echoes the
# copied runner path; non-zero when the substitution did not take.
make_runner_copy() {
    local name="$1" markers="$2" dir="${TEST_ROOT}/runner-copy-${1}/codex"
    mkdir -p "${dir}" || return 1
    cp "${RUNNER}" "${dir}/native-hooks.test.sh" || return 1
    sed "s|^CNH_DEFAULT_CONTAINER_MARKERS=.*|CNH_DEFAULT_CONTAINER_MARKERS=\"${markers}\"|" "${LIB}" > "${dir}/native-hooks-lib.sh" || return 1
    grep -qxF "CNH_DEFAULT_CONTAINER_MARKERS=\"${markers}\"" "${dir}/native-hooks-lib.sh" || return 1
    # The runner's guard canary preflight runs guard-canary.sh from its own
    # directory (it finds the manifest and the guard scripts relative to it and
    # reads the container markers from the library copy beside it).
    cp "${SCRIPT_DIR}/guard-canary.sh" "${dir}/guard-canary.sh" || return 1
    cp "${SCRIPT_DIR}/hooks.json" "${dir}/hooks.json" || return 1
    [[ -e "${dir}/../hooks" ]] || ln -s "${SCRIPT_DIR}/../hooks" "${dir}/../hooks" || return 1
    printf '%s\n' "${dir}/native-hooks.test.sh"
}

# exec_recorded <argv-file> -- did the stub see a `codex exec ...` call?
exec_recorded() {
    grep -Eq '(^|[[:space:]])exec([[:space:]]|$)' "$1"
}

# count_calls <argv.exec-file> <tag: canary|other|any> <mode: with|without|any|missing-approvals>
# -- how many recorded `codex exec` calls (see the stub's .exec log: one
# tag<TAB>flags record per call) match the tag and carry/lack the trust flag, or
# lack the approvals/sandbox bypass.
count_calls() {
    awk -F'\t' -v tag="$2" -v mode="$3" -v tf="${BYPASS_FLAG}" -v af="--dangerously-bypass-approvals-and-sandbox" '
        tag != "any" && $1 != tag { next }
        {
            has = index($2, tf) > 0
            if (mode == "any" || (mode == "with" && has) || (mode == "without" && !has) \
                || (mode == "missing-approvals" && index($2, af) == 0)) c++
        }
        END { print c + 0 }' "$1"
}

# bypass_on_exec <argv-file> -- did any `codex exec` call carry the flag?
bypass_on_exec() {
    grep -E '(^|[[:space:]])exec([[:space:]]|$)' "$1" | grep -qF -- "${BYPASS_FLAG}"
}

# any_bypass <argv-file> -- does ANY recorded invocation carry the flag?
any_bypass() {
    grep -qF -- "${BYPASS_FLAG}" "$1"
}

# Stub self-test: argv recording and the configured answers work, so the
# assertions below cannot pass vacuously on a broken stub.
STUB_SELF_LOG="${TEST_ROOT}/stub-self.argv"
: > "${STUB_SELF_LOG}"
STUB_VERSION_SELF="$(PATH="${STUB_PATH}" STUB_ARGV_LOG="${STUB_SELF_LOG}" STUB_VERSION_OUTPUT='codex-cli 9.9.9' codex --version)"
PATH="${STUB_PATH}" STUB_ARGV_LOG="${STUB_SELF_LOG}" STUB_LOGIN_RC=1 codex login status
STUB_LOGIN_SELF=$?
if [[ "${STUB_VERSION_SELF}" == 'codex-cli 9.9.9' && "${STUB_LOGIN_SELF}" -eq 1 ]] \
    && grep -qx -- '--version' "${STUB_SELF_LOG}" && grep -qx -- 'login status' "${STUB_SELF_LOG}"; then
    pass
else
    echo "FAIL: test setup: the codex stub does not record argv / honour STUB_* (version='${STUB_VERSION_SELF}', login rc=${STUB_LOGIN_SELF})" >&2
    exit 1
fi

# assert_not_run <label> <out-dir> <reason-regex> -- the shared contract of
# every gate: exit 0, a `not run: <reason>` line naming the cause, the word
# "passed" nowhere, and no `codex exec`.
assert_not_run() {
    local label="$1" out="$2" reason_re="$3" rc
    read -r rc < "${out}/rc" || rc="missing"
    if [[ "${rc}" == 0 ]]; then
        pass
    else
        fail "${label}: a not-run gate must exit 0 (rc=${rc})"
    fi
    if grep -E '^not run: ' "${out}/stdout" | grep -qiE -- "${reason_re}"; then
        pass
    else
        fail "${label}: stdout must carry a 'not run: <reason>' line matching /${reason_re}/, got: $(head -c 300 "${out}/stdout")"
    fi
    if cat "${out}/stdout" "${out}/stderr" | grep -qiw 'passed'; then
        fail "${label}: a not-run gate must never print \"passed\""
    else
        pass
    fi
    if exec_recorded "${out}/argv"; then
        fail "${label}: a not-run gate must not launch codex exec"
    else
        pass
    fi
}

# ── G1: every "not run" path ────────────────────────────────────────────
echo "case: env gate unset -> not run (G1)"
run_runner "${STUB_PATH}"
assert_not_run "CENCI_CODEX_NATIVE unset" "${OUT}" 'CENCI_CODEX_NATIVE'

echo "case: env gate set to something other than 1 -> not run (G1)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=0
assert_not_run "CENCI_CODEX_NATIVE=0" "${OUT}" 'CENCI_CODEX_NATIVE'

echo "case: codex absent from PATH -> not run (G1)"
run_runner "${FARM_DIR}" CENCI_CODEX_NATIVE=1
assert_not_run "codex absent" "${OUT}" 'codex'

echo "case: codex older than ${MIN_VERSION} -> not run, in every output format (G1, G2)"
for v in "codex-cli 0.144.0" "codex-cli 0.143.9" "codex-cli 0.9.0" "0.144.0"; do
    run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 "STUB_VERSION_OUTPUT=${v}"
    assert_not_run "version '${v}'" "${OUT}" "version|${MIN_VERSION//./\\.}"
done

echo "case: unparseable codex --version -> not run (fail closed, G1)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 "STUB_VERSION_OUTPUT=banana"
assert_not_run "unparseable version" "${OUT}" "version|${MIN_VERSION//./\\.}"

echo "case: codex login status failing -> not run (G1)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 STUB_LOGIN_RC=1
assert_not_run "login failing" "${OUT}" 'login|auth'
if grep -qE '^login status$' "${OUT}/argv"; then
    pass
else
    fail "login failing: the runner must have asked codex login status (argv: $(head -c 200 "${OUT}/argv"))"
fi

# ── G2: accepted versions reach `codex exec` ─────────────────────────────
echo "case: ${MIN_VERSION} and newer are accepted, in both output formats (G2)"
for v in "codex-cli 0.144.1" "codex-cli 0.150.0" "codex-cli 0.144.10" "0.144.1" "0.150.0"; do
    run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 "STUB_VERSION_OUTPUT=${v}"
    if exec_recorded "${OUT}/argv"; then
        pass
    else
        fail "version '${v}' must pass the gates and reach codex exec (stdout: $(head -c 300 "${OUT}/stdout"))"
    fi
done

# ── G3: the trust-bypass flag ───────────────────────────────────────────
# The runner's container markers are HARD-CODED (S2): an environment variable
# must never be able to make a host run look like a sandbox run, because
# sandbox mode adds --dangerously-bypass-hook-trust AND
# --dangerously-bypass-approvals-and-sandbox. The cases below therefore stage
# "a real container" / "a real host" by running a copy of the runner whose
# hard-coded marker list names a file that exists / does not exist.
APPROVAL_BYPASS_FLAG="--dangerously-bypass-approvals-and-sandbox"
HOST_RUNNER="$(make_runner_copy host "${MISSING_MARKER}")" || {
    echo "FAIL: test setup: could not build the host-staged runner copy" >&2
    exit 1
}
SANDBOX_RUNNER="$(make_runner_copy sandbox "${MARKER_FILE}")" || {
    echo "FAIL: test setup: could not build the sandbox-staged runner copy" >&2
    exit 1
}

echo "case: host mode never passes ${BYPASS_FLAG}, even with a spoofed CENCI_SANDBOX=1 and no marker file (G3)"
RUNNER_OVERRIDE="${HOST_RUNNER}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 CENCI_SANDBOX=1
RUNNER_OVERRIDE=""
if exec_recorded "${OUT}/argv"; then
    pass
else
    fail "spoofed CENCI_SANDBOX=1: the runner must still reach codex exec (non-vacuity), stdout: $(head -c 300 "${OUT}/stdout")"
fi
if any_bypass "${OUT}/argv"; then
    fail "spoofed CENCI_SANDBOX=1 without a container marker must be host mode: no codex call may carry ${BYPASS_FLAG}"
else
    pass
fi

echo "case: CNH_CONTAINER_MARKERS in the environment cannot turn a host run into a sandbox run (S2)"
RUNNER_OVERRIDE="${HOST_RUNNER}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 CENCI_SANDBOX=1 "CNH_CONTAINER_MARKERS=${MARKER_FILE}"
RUNNER_OVERRIDE=""
if exec_recorded "${OUT}/argv"; then
    pass
else
    fail "env-spoofed markers: the runner must still reach codex exec (non-vacuity), stdout: $(head -c 300 "${OUT}/stdout")"
fi
if any_bypass "${OUT}/argv" || grep -qF -- "${APPROVAL_BYPASS_FLAG}" "${OUT}/argv"; then
    fail "CENCI_SANDBOX=1 + CNH_CONTAINER_MARKERS=<existing file> on a host must get NEITHER ${BYPASS_FLAG} NOR ${APPROVAL_BYPASS_FLAG} (argv: $(head -c 400 "${OUT}/argv"))"
else
    pass
fi
if grep -rF 'CNH_CONTAINER_MARKERS' "${RUNNER}" "${LIB}" | grep -vE '^[^:]+:[[:space:]]*#' >/dev/null; then
    fail "the runner and its library must not read CNH_CONTAINER_MARKERS from the environment (only comments may name it)"
else
    pass
fi

echo "case: a container marker without CENCI_SANDBOX=1 is still host mode (G3)"
RUNNER_OVERRIDE="${SANDBOX_RUNNER}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1
RUNNER_OVERRIDE=""
if exec_recorded "${OUT}/argv" && ! any_bypass "${OUT}/argv"; then
    pass
else
    fail "marker file without CENCI_SANDBOX=1 must reach codex exec without ${BYPASS_FLAG}"
fi

echo "case: CENCI_SANDBOX=0 plus a marker file is host mode (G3)"
RUNNER_OVERRIDE="${SANDBOX_RUNNER}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 CENCI_SANDBOX=0
RUNNER_OVERRIDE=""
if exec_recorded "${OUT}/argv" && ! any_bypass "${OUT}/argv"; then
    pass
else
    fail "CENCI_SANDBOX=0 with a marker file must reach codex exec without ${BYPASS_FLAG}"
fi

echo "case: sandbox mode (CENCI_SANDBOX=1 AND a marker file) passes both bypass flags on codex exec, except the labelled trust-withheld canary calls (G3, Q4)"
RUNNER_OVERRIDE="${SANDBOX_RUNNER}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 CENCI_SANDBOX=1
RUNNER_OVERRIDE=""
if bypass_on_exec "${OUT}/argv" && grep -E '(^|[[:space:]])exec([[:space:]]|$)' "${OUT}/argv" | grep -qF -- "${APPROVAL_BYPASS_FLAG}"; then
    pass
else
    fail "sandbox mode must pass ${BYPASS_FLAG} on codex exec (argv: $(head -c 400 "${OUT}/argv"))"
fi
# The only exec calls without the trust flag are the guard canary's labelled
# trust-withheld probes (the prompt names a cenci-guard-canary- target); every
# call, withheld or not, still carries the approvals/sandbox bypass.
if [[ "$(count_calls "${OUT}/argv.exec" other without)" -eq 0 ]]; then
    pass
else
    fail "sandbox mode: every codex exec outside the labelled trust-withheld canary variant must carry ${BYPASS_FLAG} ($(count_calls "${OUT}/argv.exec" other without) did not)"
fi
if [[ "$(count_calls "${OUT}/argv.exec" any missing-approvals)" -eq 0 && "$(count_calls "${OUT}/argv.exec" any any)" -gt 0 ]]; then
    pass
else
    fail "sandbox mode: every codex exec, trust-withheld or not, must carry ${APPROVAL_BYPASS_FLAG}"
fi

# ── S1: only the cenci flow plugin's handlers are re-run ────────────────
echo "case: preflight re-runs only the cenci flow plugin's well-formed handlers (S1)"
S1_HOME="${TEST_ROOT}/s1-codex-home"
S1_MARK="${TEST_ROOT}/s1-marks"
mkdir_checked "${S1_MARK}"
# mk_plugin <dir-name> <plugin-name> <hook-command> <script-name-or-empty> <mark>
mk_plugin() {
    local dir="${S1_HOME}/plugins/$1/codex" root="${S1_HOME}/plugins/$1"
    mkdir_checked "${dir}"
    mkdir_checked "${root}/.codex-plugin"
    mkdir_checked "${root}/hooks/scripts"
    jq -n --arg n "$2" '{name: $n, version: "0.0.0"}' > "${root}/.codex-plugin/plugin.json"
    jq -n --arg c "$3" '{hooks: {SessionStart: [{hooks: [{type: "command", command: $c}]}]}}' > "${dir}/hooks.json"
    if [[ -n "$4" ]]; then
        printf '#!/bin/sh\n: > "%s"\nexit 0\n' "${S1_MARK}/$5" > "${root}/hooks/scripts/$4"
        chmod +x "${root}/hooks/scripts/$4"
    fi
}
SHAPED='"${PLUGIN_ROOT}/hooks/scripts/ok.sh"'
mk_plugin evil-named evil "${SHAPED}" ok.sh evil-named
mk_plugin cenci-shaped cenci "${SHAPED}" ok.sh cenci-shaped
mk_plugin cenci-injected cenci "touch ${S1_MARK}/cenci-injected; ${SHAPED}" ok.sh cenci-injected-script
mk_plugin cenci-metachar cenci "\"\${PLUGIN_ROOT}/hooks/scripts/ok.sh\" ; touch ${S1_MARK}/cenci-metachar" ok.sh cenci-metachar-script
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 "CODEX_HOME=${S1_HOME}"
if [[ -e "${S1_MARK}/cenci-shaped" ]]; then
    pass
else
    fail "S1 non-vacuity: a cenci-named plugin with a well-formed handler must be re-run (stdout: $(head -c 300 "${OUT}/stdout"))"
fi
for forbidden in evil-named cenci-injected cenci-injected-script cenci-metachar cenci-metachar-script; do
    if [[ -e "${S1_MARK}/${forbidden}" ]]; then
        fail "S1: '${forbidden}' was executed; only handlers of a plugin named cenci that match the strict command shape may be re-run"
    else
        pass
    fi
done

# ── L2: a symlinked handler is never executed ───────────────────────────
# The handler's DIRECTORY is inside the plugin root, but the script file is a
# link to a file outside it. A MISSING script must still be launched (that is
# how a 127 is reproduced), so the same run carries a root with no script.
echo "case: preflight refuses a handler script that is a symlink out of the plugin root, but still launches a missing one (L2)"
S1_HOME="${TEST_ROOT}/l2-codex-home"
S1_MARK="${TEST_ROOT}/l2-marks"
mkdir_checked "${S1_MARK}"
mk_plugin cenci-symlinked cenci "${SHAPED}" "" ""
mk_plugin cenci-missing cenci "${SHAPED}" "" ""
printf '#!/bin/sh\n: > "%s"\nexit 0\n' "${S1_MARK}/symlink-target-ran" > "${TEST_ROOT}/l2-outside.sh"
chmod +x "${TEST_ROOT}/l2-outside.sh"
ln -s "${TEST_ROOT}/l2-outside.sh" "${S1_HOME}/plugins/cenci-symlinked/hooks/scripts/ok.sh"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 "CODEX_HOME=${S1_HOME}"
if [[ -L "${S1_HOME}/plugins/cenci-symlinked/hooks/scripts/ok.sh" && -x "${TEST_ROOT}/l2-outside.sh" ]]; then
    pass
else
    fail "L2 non-vacuity: the fixture must hold a symlinked handler that would be runnable"
fi
if [[ -e "${S1_MARK}/symlink-target-ran" ]]; then
    fail "L2: the symlink target outside the plugin root was executed"
else
    pass
fi
if grep -E 'SessionStart hooks/scripts/ok.sh \(cenci-symlinked\) \| inconclusive \|' "${OUT}/stdout" >/dev/null; then
    pass
else
    fail "L2: a symlinked handler must be recorded as inconclusive (stdout: $(grep -F 'cenci-symlinked' "${OUT}/stdout" | head -n 3))"
fi
if grep -E 'ok.sh \[event\] \(cenci-missing\) \| FAIL \|.*script missing.*rc=127' "${OUT}/stdout" >/dev/null; then
    pass
else
    fail "L2: a missing handler script must still be launched and reproduce rc=127 (stdout: $(grep -F 'cenci-missing' "${OUT}/stdout" | head -n 3))"
fi

# ── L1: the strict identity check is independent of the sanitizer ───────
# `id -un` is Probeuser; the sanitizer matches a user name case-sensitively and
# so leaves the lowercase `probeuser` that the stubbed `codex plugin list`
# prints. The post-sanitize check searches case-insensitively and must refuse
# to print the report; a longer word containing the name must not trip it.
echo "case: a user name that survived sanitization (case differs) makes the runner refuse to print the report (L1)"
ID_DIR="${TEST_ROOT}/id-stub-bin"
mkdir_checked "${ID_DIR}"
printf '#!/bin/sh\nprintf "Probeuser\\n"\n' > "${ID_DIR}/id"
chmod +x "${ID_DIR}/id"
run_runner "${ID_DIR}:${STUB_PATH}" CENCI_CODEX_NATIVE=1 "STUB_PLUGIN_LIST_OUTPUT=owner probeuserless and probeuserx"
if [[ "$(< "${OUT}/rc")" == 3 ]] && grep -qF 'probeuserless' "${OUT}/stdout"; then
    pass
else
    fail "L1 non-vacuity: a name only inside longer words must not be refused (rc=$(< "${OUT}/rc"), stderr: $(head -c 300 "${OUT}/stderr"))"
fi
run_runner "${ID_DIR}:${STUB_PATH}" CENCI_CODEX_NATIVE=1 "STUB_PLUGIN_LIST_OUTPUT=owner [probeuser]. done"
if [[ "$(< "${OUT}/rc")" == 1 ]] && grep -qF 'still contains the user name' "${OUT}/stderr"; then
    pass
else
    fail "L1: a surviving case-variant user token must fail the runner (rc=$(< "${OUT}/rc"), stderr: $(head -c 300 "${OUT}/stderr"))"
fi
if grep -qi 'probeuser' "${OUT}/stdout"; then
    fail "L1: the refused report must not be printed"
else
    pass
fi

# ── F1: a run that proved nothing never reports success ────────────────
echo "case: a full run against a codex stub that does nothing proves nothing and says so (F1, F2)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 "CENCI_CODEX_NATIVE_OUT=${TEST_ROOT}/f1-report.md"
if exec_recorded "${OUT}/argv"; then
    pass
else
    fail "F1 non-vacuity: the stub run must reach codex exec (stdout: $(head -c 300 "${OUT}/stdout"))"
fi
# Everything from the first scenario section on: preflight rows prove only that
# a handler launches, not what Codex does.
SCENARIO_ROWS="$(sed -n '/^### Deny matrix/,$p' "${OUT}/stdout")"
if [[ -n "${SCENARIO_ROWS}" ]]; then
    pass
else
    fail "F1 non-vacuity: the report must contain a '### Deny matrix' section"
fi
if grep -E '\| ok \|' <<< "${SCENARIO_ROWS}" >/dev/null; then
    fail "F1: no scenario row may be 'ok' when codex did nothing: $(grep -E '\| ok \|' <<< "${SCENARIO_ROWS}" | head -n 3)"
else
    pass
fi
LAST_LINE="$(grep -v '^[[:space:]]*$' "${OUT}/stdout" | tail -n 1)"
if [[ "${LAST_LINE}" == "RESULT: INCONCLUSIVE"* ]]; then
    pass
else
    fail "F1: the final output line must be 'RESULT: INCONCLUSIVE ...' when nothing was proven, got: '${LAST_LINE}'"
fi
read -r RESULT_RC < "${OUT}/rc"
if [[ "${RESULT_RC}" == 3 ]]; then
    pass
else
    fail "F1: a run that proved nothing must exit 3 (distinct from failure 1 and from a not-run gate 0), got ${RESULT_RC}"
fi
if grep -qF 'inferred from the canary' "${OUT}/stdout"; then
    fail "F2: the trust-state summary must print the canary's real status, not an unconditional 'inferred from the canary' claim"
else
    pass
fi
if grep -E '^- Trust state: .*UNKNOWN' "${OUT}/stdout" >/dev/null; then
    pass
else
    fail "F2: an inconclusive canary must record trust state UNKNOWN (summary: $(grep -F 'Trust state' "${OUT}/stdout"))"
fi

# ── G5: the judge, against an emulated exec_command ─────────────────────
# The stub emulates exec_command and the hooks (see its header), so the
# runner's verdict logic runs against the REAL guards without a Codex binary.
FLOW_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)" || {
    echo "FAIL: test setup: cannot resolve the flow directory" >&2
    exit 1
}
EMU_ENV=(CENCI_CODEX_NATIVE=1 STUB_EMULATE=1 "STUB_PLUGIN_HOOKS=${FLOW_ROOT}/codex/hooks.json" "STUB_PLUGIN_ROOT=${FLOW_ROOT}")
# The same, with the stub also emulating the dedicated apply_patch tool. The
# guard canary preflight probes apply_patch, so a run that expects it to pass
# needs this; without it every apply_patch probe is "no guard message" and the
# preflight (correctly) refuses.
EMU_PATCH_ENV=("${EMU_ENV[@]}" STUB_EMULATE_PATCH=1)

# row_status <stdout-file> <label-fragment> -- the result cell of the first
# table row whose label contains the fragment ("" when there is no such row).
row_status() {
    grep -F -- "$2" "$1" | sed -n 1p | awk -F'|' '{ s = $3; gsub(/^[ \t]+|[ \t]+$/, "", s); print s }'
}

# assert_row <label> <stdout-file> <fragment> <expected-status>
assert_row() {
    local got
    got="$(row_status "$2" "$3")"
    if [[ "${got}" == "$4" ]]; then
        pass
    else
        fail "$1: row '$3' expected '$4', got '${got}'"
    fi
}

echo "case: hooks active -> deny rows denied, allow row allowed with a tap line, launch finding needs the broken hook invoked (G5, F2, F3, F6)"
run_runner "${STUB_PATH}" "${EMU_PATCH_ENV[@]}"
assert_row "active" "${OUT}/stdout" "canary: bash write to the main worktree" ok
assert_row "active" "${OUT}/stdout" "bash -> main worktree file (deny, guard-main-worktree)" ok
assert_row "active" "${OUT}/stdout" "bash -> sensitive .env in a worktree" ok
assert_row "active" "${OUT}/stdout" "bash -> worktree file (positive control, allow)" ok
assert_row "active" "${OUT}/stdout" "apply_patch -> main worktree file (deny" ok
assert_row "active" "${OUT}/stdout" "launch failure: missing-script" ok
assert_row "active" "${OUT}/stdout" "launch failure: not-executable" ok
if grep -F 'FINDING: the tool CONTINUED after the hook failed to launch' "${OUT}/stdout" | grep -F 'broken hook was invoked' >/dev/null; then
    pass
else
    fail "active: a CONTINUED finding must cite proof that the broken hook was invoked"
fi
if grep -F -- '- Trust state: hooks ACTIVE' "${OUT}/stdout" >/dev/null; then
    pass
else
    fail "active: the summary must say the hooks are ACTIVE"
fi
read -r EMU_RC < "${OUT}/rc"
LAST_LINE="$(grep -v '^[[:space:]]*$' "${OUT}/stdout" | tail -n 1)"
if [[ "${EMU_RC}" == 0 && "${LAST_LINE}" == "RESULT: OK"* ]]; then
    pass
else
    fail "active: a run with proven rows and no failure must exit 0 with RESULT: OK, got rc=${EMU_RC}, last line '${LAST_LINE}'"
fi
if grep -E '^\| (exec_command, per-call workdir|exec_command, no per-call workdir)' "${OUT}/stdout" | grep -F 'payload cwd=' >/dev/null; then
    pass
else
    fail "active: the cwd rows must carry the tap's payload cwd / hook pwd"
fi

echo "case: hooks not active -> no allow or deny row proves anything and the result is inconclusive (G5, F1, F2)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 STUB_EMULATE=1
assert_row "inactive" "${OUT}/stdout" "canary: bash write to the main worktree" "not run"
assert_row "inactive" "${OUT}/stdout" "bash -> worktree file (positive control, allow)" "not run"
assert_row "inactive" "${OUT}/stdout" "bash -> main worktree file (deny, guard-main-worktree)" "not run"
assert_row "inactive" "${OUT}/stdout" "worktree session, relative add inside the worktree (allow)" "not run"
if grep -F -- '- Trust state: hooks INACTIVE' "${OUT}/stdout" >/dev/null; then
    pass
else
    fail "inactive: the summary must say the hooks are INACTIVE"
fi
read -r EMU_RC < "${OUT}/rc"
LAST_LINE="$(grep -v '^[[:space:]]*$' "${OUT}/stdout" | tail -n 1)"
if [[ "${EMU_RC}" == 3 && "${LAST_LINE}" == "RESULT: INCONCLUSIVE"* ]]; then
    pass
else
    fail "inactive: hooks that are not active prove nothing: expected rc 3 and RESULT: INCONCLUSIVE, got rc=${EMU_RC}, last line '${LAST_LINE}'"
fi

echo "case: a block whose guard message is not in the target's tool-call item is not attributed to the guard (G5, F3)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_REDACT_BLOCK=1
assert_row "redacted block" "${OUT}/stdout" "canary: bash write to the main worktree" inconclusive
assert_row "redacted block" "${OUT}/stdout" "bash -> worktree file (positive control, allow)" "not run"
if grep -F -- '- Trust state: UNKNOWN' "${OUT}/stdout" >/dev/null; then
    pass
else
    fail "redacted block: an inconclusive canary must record trust state UNKNOWN"
fi

echo "case: a launch-failure finding is inconclusive when the broken hook was never invoked (G5, F6)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_CONFIG_HOOK_LIMIT=1
assert_row "limit 1" "${OUT}/stdout" "launch failure: control" ok
assert_row "limit 1" "${OUT}/stdout" "launch failure: missing-script" inconclusive
assert_row "limit 1" "${OUT}/stdout" "launch failure: not-executable" inconclusive
if grep -F 'tool CONTINUED' "${OUT}/stdout" >/dev/null; then
    fail "limit 1: no CONTINUED finding may be reported when the broken hook never ran"
else
    pass
fi

# ── G6: the guard canary preflight scenario (#1213) ─────────────────────
# The runner's "### Guard canary preflight" section runs guard-canary.sh plan,
# one `codex exec` per probe, then check. The stub emulates apply_patch too
# (STUB_EMULATE_PATCH=1), and can model pending hook trust (STUB_TRUST_REQUIRED=1:
# plugin hooks run only when the call carries the trust-bypass flag).
CANARY_AL="guard canary preflight (as launched)"
CANARY_WH="guard canary preflight (trust withheld)"

# row_line <stdout-file> <label-fragment> -- the first table row containing it.
row_line() {
    grep -F -- "$2" "$1" | sed -n 1p
}

# assert_row_has <label> <stdout-file> <fragment> <needle>
assert_row_has() {
    if row_line "$2" "$3" | grep -qF -- "$4"; then
        pass
    else
        fail "$1: row '$3' must contain '$4', got: $(row_line "$2" "$3")"
    fi
}

# last_line_of <stdout-file> -- the final non-blank line.
last_line_of() {
    grep -v '^[[:space:]]*$' "$1" | tail -n 1
}

echo "case: host, hooks active -> the as-launched canary passes; the withheld variant is not run and records the persisted state (G6)"
RUNNER_OVERRIDE="${HOST_RUNNER}"
run_runner "${STUB_PATH}" "${EMU_PATCH_ENV[@]}"
RUNNER_OVERRIDE=""
if grep -qxF '### Guard canary preflight' "${OUT}/stdout"; then
    pass
else
    fail "the report must carry a '### Guard canary preflight' section"
fi
assert_row "host active" "${OUT}/stdout" "${CANARY_AL}" ok
assert_row_has "host active" "${OUT}/stdout" "${CANARY_AL}" "CANARY_STATUS=active"
assert_row "host active" "${OUT}/stdout" "${CANARY_WH}" "not run"
assert_row_has "host active" "${OUT}/stdout" "${CANARY_WH}" "sandbox"
assert_row_has "host active" "${OUT}/stdout" "${CANARY_WH}" "persisted"
if [[ "$(count_calls "${OUT}/argv.exec" canary any)" -eq 4 && "$(count_calls "${OUT}/argv.exec" canary with)" -eq 0 ]]; then
    pass
else
    fail "host: the canary is one codex exec per probe (4), only the as-launched variant, never with ${BYPASS_FLAG} (got $(count_calls "${OUT}/argv.exec" canary any) calls, $(count_calls "${OUT}/argv.exec" canary with) with the flag)"
fi
DENY_AT="$(grep -nF '### Deny matrix' "${OUT}/stdout" | sed -n 1p | cut -d: -f1)"
CANARY_AT="$(grep -nxF '### Guard canary preflight' "${OUT}/stdout" | sed -n 1p | cut -d: -f1)"
if [[ -n "${DENY_AT}" && -n "${CANARY_AT}" && "${CANARY_AT}" -gt "${DENY_AT}" ]]; then
    pass
else
    fail "the canary section must come after the deny matrix (deny at '${DENY_AT}', canary at '${CANARY_AT}')"
fi

echo "case: hooks inactive -> the canary refuses, names pending trust and /hooks, removes what was written, and agrees with the INACTIVE trust state (G6)"
RUNNER_OVERRIDE="${HOST_RUNNER}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 STUB_EMULATE=1 STUB_EMULATE_PATCH=1
RUNNER_OVERRIDE=""
assert_row "inactive" "${OUT}/stdout" "${CANARY_AL}" ok
assert_row_has "inactive" "${OUT}/stdout" "${CANARY_AL}" "CANARY_STATUS=refused"
assert_row_has "inactive" "${OUT}/stdout" "${CANARY_AL}" "pending"
assert_row_has "inactive" "${OUT}/stdout" "${CANARY_AL}" "/hooks"
assert_row_has "inactive" "${OUT}/stdout" "${CANARY_AL}" "removed 4 written target(s)"
assert_row_has "inactive" "${OUT}/stdout" "${CANARY_AL}" "0 leftover(s)"
read -r EMU_RC < "${OUT}/rc"
if [[ "${EMU_RC}" == 3 && "$(last_line_of "${OUT}/stdout")" == "RESULT: INCONCLUSIVE"* ]]; then
    pass
else
    fail "inactive: a refusing canary under inactive hooks is consistent but proves nothing about the guards: expected rc 3 and RESULT: INCONCLUSIVE, got rc=${EMU_RC}"
fi

echo "case: a canary result that contradicts the trust state is a FAIL (G6)"
# Bash hooks fire (the deny-matrix canary shows ACTIVE) but apply_patch is not
# watched, so two probes are written and the preflight refuses.
RUNNER_OVERRIDE="${HOST_RUNNER}"
run_runner "${STUB_PATH}" "${EMU_PATCH_ENV[@]}" STUB_PATCH_HOOKS_OFF=1
RUNNER_OVERRIDE=""
assert_row "contradiction" "${OUT}/stdout" "canary: bash write to the main worktree" ok
assert_row "contradiction" "${OUT}/stdout" "${CANARY_AL}" FAIL
assert_row_has "contradiction" "${OUT}/stdout" "${CANARY_AL}" "removed 2 written target(s)"
read -r EMU_RC < "${OUT}/rc"
if [[ "${EMU_RC}" == 1 && "$(last_line_of "${OUT}/stdout")" == "RESULT: FAIL"* ]]; then
    pass
else
    fail "contradiction: expected rc 1 and RESULT: FAIL, got rc=${EMU_RC}, last line '$(last_line_of "${OUT}/stdout")'"
fi

echo "case: an unproven trust state never turns the canary row into an ok (G6)"
RUNNER_OVERRIDE="${HOST_RUNNER}"
run_runner "${STUB_PATH}" "${EMU_PATCH_ENV[@]}" STUB_REDACT_BLOCK=1
RUNNER_OVERRIDE=""
assert_row "unknown state" "${OUT}/stdout" "${CANARY_AL}" inconclusive

echo "case: sandbox runs the withheld variant, leaving out ONLY the trust flag, and labels the trust (G6)"
RUNNER_OVERRIDE="${SANDBOX_RUNNER}"
run_runner "${STUB_PATH}" "${EMU_PATCH_ENV[@]}" CENCI_SANDBOX=1 STUB_TRUST_REQUIRED=1
RUNNER_OVERRIDE=""
assert_row "sandbox" "${OUT}/stdout" "${CANARY_AL}" ok
assert_row_has "sandbox" "${OUT}/stdout" "${CANARY_AL}" "CANARY_STATUS=active"
assert_row_has "sandbox" "${OUT}/stdout" "${CANARY_AL}" "hook trust bypassed by launcher (explicit flag)"
assert_row "sandbox" "${OUT}/stdout" "${CANARY_WH}" ok
assert_row_has "sandbox" "${OUT}/stdout" "${CANARY_WH}" "CANARY_STATUS=refused"
assert_row_has "sandbox" "${OUT}/stdout" "${CANARY_WH}" "pending"
assert_row_has "sandbox" "${OUT}/stdout" "${CANARY_WH}" "/hooks"
if [[ "$(count_calls "${OUT}/argv.exec" canary with)" -eq 4 && "$(count_calls "${OUT}/argv.exec" canary without)" -eq 4 ]]; then
    pass
else
    fail "sandbox: expected 4 as-launched and 4 trust-withheld canary calls, got $(count_calls "${OUT}/argv.exec" canary with) with and $(count_calls "${OUT}/argv.exec" canary without) without ${BYPASS_FLAG}"
fi
if [[ "$(count_calls "${OUT}/argv.exec" other without)" -eq 0 && "$(count_calls "${OUT}/argv.exec" any missing-approvals)" -eq 0 ]]; then
    pass
else
    fail "sandbox: only canary calls may lack ${BYPASS_FLAG}, and every call must carry ${APPROVAL_BYPASS_FLAG}"
fi
WITH_FLAGS="$(awk -F'\t' -v f="${BYPASS_FLAG}" '$1 == "canary" && index($2, f) { print $2; exit }' "${OUT}/argv.exec")"
WITHOUT_FLAGS="$(awk -F'\t' -v f="${BYPASS_FLAG}" '$1 == "canary" && !index($2, f) { print $2; exit }' "${OUT}/argv.exec")"
norm_flags() { printf '%s' "$1" | sed "s/${BYPASS_FLAG}//" | tr -s ' ' | sed -e 's/^ //' -e 's/ $//'; }
if [[ -n "${WITH_FLAGS}" && -n "${WITHOUT_FLAGS}" && "$(norm_flags "${WITH_FLAGS}")" == "$(norm_flags "${WITHOUT_FLAGS}")" ]]; then
    pass
else
    fail "sandbox: the withheld variant must differ from the as-launched one ONLY by ${BYPASS_FLAG} (with: '${WITH_FLAGS}', without: '${WITHOUT_FLAGS}')"
fi
if grep -qiE '(^|[^n])trusted' "${OUT}/stdout"; then
    fail "sandbox: no line of the report may call anything 'trusted': $(grep -iE '(^|[^n])trusted' "${OUT}/stdout" | head -n 2)"
else
    pass
fi

# ── G4: helper tables ───────────────────────────────────────────────────
# shellcheck source=native-hooks-lib.sh
if ! source "${LIB}"; then
    fail "could not source ${LIB}"
    finish
fi
for fn in cnh_parse_version cnh_version_ge cnh_sanitize cnh_classify_verdict cnh_is_sandbox cnh_trust_bypass_flag; do
    if declare -F "${fn}" >/dev/null; then
        pass
    else
        fail "${LIB##*/} must define ${fn}"
    fi
done

echo "case: cnh_parse_version extracts X.Y.Z from codex --version output (G4)"
# input|expected ("" = must fail with empty stdout)
parse_cases=(
    "codex-cli 0.144.1|0.144.1"
    "codex-cli 0.150.0|0.150.0"
    "0.144.1|0.144.1"
    "codex-cli 1.20.300|1.20.300"
    "no version here|"
    "|"
)
for c in "${parse_cases[@]}"; do
    input="${c%%|*}"
    want="${c#*|}"
    got="$(cnh_parse_version "${input}" 2>/dev/null)"
    rc=$?
    if [[ -n "${want}" ]]; then
        if [[ "${rc}" -eq 0 && "${got}" == "${want}" ]]; then
            pass
        else
            fail "cnh_parse_version '${input}': expected ${want} (rc 0), got '${got}' (rc ${rc})"
        fi
    elif [[ "${rc}" -ne 0 && -z "${got}" ]]; then
        pass
    else
        fail "cnh_parse_version '${input}': must fail with empty stdout, got '${got}' (rc ${rc})"
    fi
done

echo "case: cnh_version_ge compares numerically per component and fails closed on garbage (G4)"
# version|min|expected (yes = rc 0, no = rc != 0)
ge_cases=(
    "0.144.1|${MIN_VERSION}|yes"
    "0.144.0|${MIN_VERSION}|no"
    "0.150.0|${MIN_VERSION}|yes"
    "0.143.99|${MIN_VERSION}|no"
    "0.9.0|${MIN_VERSION}|no"
    "0.144.10|${MIN_VERSION}|yes"
    "0.145.0|${MIN_VERSION}|yes"
    "1.0.0|${MIN_VERSION}|yes"
    "0.144|${MIN_VERSION}|no"
    "abc|${MIN_VERSION}|no"
    "|${MIN_VERSION}|no"
)
for c in "${ge_cases[@]}"; do
    v="${c%%|*}"
    rest="${c#*|}"
    min="${rest%%|*}"
    want="${rest##*|}"
    cnh_version_ge "${v}" "${min}" 2>/dev/null
    rc=$?
    if [[ "${want}" == yes && "${rc}" -eq 0 ]] || [[ "${want}" == no && "${rc}" -ne 0 ]]; then
        pass
    else
        fail "cnh_version_ge '${v}' '${min}': expected ${want}, rc=${rc}"
    fi
done

# check_sanitize <label> <input> <expected> [home user host fixture]
SAN_HOME="/home/alice"
SAN_USER="alice"
SAN_HOST="devbox-01"
SAN_FIXTURE="/var/tmp/native-hooks.AbC123"
check_sanitize() {
    local label="$1" input="$2" want="$3"
    local home="${4-${SAN_HOME}}" user="${5-${SAN_USER}}" host="${6-${SAN_HOST}}" fixture="${7-${SAN_FIXTURE}}"
    local got rc
    got="$(printf '%s\n' "${input}" | cnh_sanitize "${home}" "${user}" "${host}" "${fixture}" 2>/dev/null)"
    rc=$?
    if [[ "${rc}" -eq 0 && "${got}" == "${want}" ]]; then
        pass
    else
        fail "cnh_sanitize ${label}: expected '${want}' (rc 0), got '${got}' (rc ${rc})"
    fi
}

echo "case: cnh_sanitize replaces home, user, host and the fixture root (G4)"
check_sanitize "home" "/home/alice/.codex/plugins/cenci" "<HOME>/.codex/plugins/cenci"
check_sanitize "user and host" "alice@devbox-01:~\$ codex" "<USER>@<HOST>:~\$ codex"
check_sanitize "fixture root" "cwd=/var/tmp/native-hooks.AbC123/repo/.worktrees/1-native" "cwd=<FIXTURE>/repo/.worktrees/1-native"

echo "case: cnh_sanitize replaces UUID / session ids (G4)"
check_sanitize "uuid" "session 019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b started" "session <UUID> started"
check_sanitize "uppercase uuid in json" '{"thread_id":"019A1B2C-3D4E-7F80-9A1B-2C3D4E5F6A7B"}' '{"thread_id":"<UUID>"}'
check_sanitize "two uuids on one line" "a 019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b b 11111111-2222-3333-4444-555555555555" "a <UUID> b <UUID>"

echo "case: cnh_sanitize replaces token-shaped strings (G4)"
check_sanitize "sk- key" "OPENAI_API_KEY=sk-proj-AbCdEf1234567890abcdefGHIJ" "OPENAI_API_KEY=<TOKEN>"
check_sanitize "ghp_ token" "token ghp_abcdefghijklmnopqrstuvwxyz0123456789" "token <TOKEN>"
check_sanitize "github_pat_ token" "github_pat_11ABCDEFG0abcdefghijklmnop_qrstuvwxyz0123456789ABCDEFGHIJ" "<TOKEN>"
check_sanitize "slack token" "xoxb-1234567890-abcdefghijkl" "<TOKEN>"
check_sanitize "aws access key" "AKIAIOSFODNN7EXAMPLE" "<TOKEN>"
check_sanitize "jwt" "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r" "<TOKEN>"
check_sanitize "bearer header" "Authorization: Bearer abc123DEF456ghi789" "Authorization: Bearer <TOKEN>"

echo "case: cnh_sanitize leaves evidence-bearing text alone (G4)"
check_sanitize "git commit sha is kept" "plugin commit f8643bd3a89990802fc42024c1f6ab4a1a655418" "plugin commit f8643bd3a89990802fc42024c1f6ab4a1a655418"
check_sanitize "plain evidence text is kept" "hook exited with code 127 on codex-cli 0.144.1" "hook exited with code 127 on codex-cli 0.144.1"
check_sanitize "all-empty sources redact nothing" "/etc/passwd /home/alice devbox-01" "/etc/passwd /home/alice devbox-01" "" "" "" ""
check_sanitize "an empty home never redacts the stream" "/etc/passwd and /home/alice" "/etc/passwd and /home/<USER>" ""
check_sanitize "sources are literal, not regex" "/home/aXb/x /home/a.b/x" "/home/aXb/x <HOME>/x" "/home/a.b" "nobody" "nohost" "/nowhere"

echo "case: cnh_sanitize replaces user and host only as whole tokens, never inside other words (G4, C3)"
# A short or common-word user (root, dev) must not corrupt unrelated text.
check_sanitize "user root: whole token only" \
    "root@devbox-01 rootless uid=root /dev/root-fs a root b 'root' (root) root:x,root;root" \
    "<USER>@<HOST> rootless uid=<USER> /dev/<USER>-fs a <USER> b '<USER>' (<USER>) <USER>:x,<USER>;<USER>" \
    "" "root" "devbox-01" ""
check_sanitize "user dev: whole token only" \
    "dev@devbox-01 developer device dev=1 \"dev\" nodev" \
    "<USER>@<HOST> developer device <USER>=1 \"<USER>\" nodev" \
    "" "dev" "devbox-01" ""
check_sanitize "host inside a longer word is kept" \
    "devbox-01 devbox-012 xdevbox-01 ssh devbox-01:22" \
    "<HOST> devbox-012 xdevbox-01 ssh <HOST>:22" \
    "" "alice" "devbox-01" ""
check_sanitize "user at string edges and after a slash" \
    "root" "<USER>" "" "root" "" ""
check_sanitize "adjacent occurrences are not whole tokens" \
    "rootroot rootroot2 root_root" "rootroot rootroot2 <USER>_<USER>" "" "root" "" ""

echo "case: cnh_sanitize treats any non-alphanumeric neighbour as a word edge, and the host case-insensitively (G4, #1212)"
# Any character other than [A-Za-z0-9] bounds a token: dots, backticks,
# brackets, angle brackets, pipes, dashes and underscores all count.
check_sanitize "host followed by a dotted domain (box.lan)" \
    "ssh box.lan and box.example.org" "ssh <HOST>.lan and <HOST>.example.org" "" "alice" "box" ""
check_sanitize "full host name and its short form are both redacted" \
    "ssh box.lan.example and box up" "ssh <HOST> and <HOST> up" "" "alice" "box.lan.example" ""
check_sanitize "backtick-quoted user" \
    '`dev` and `dev`.' '`<USER>` and `<USER>`.' "" "dev" "" ""
check_sanitize "bracketed, angle-bracketed and piped user" \
    '[dev] <dev> |dev| {dev}' '[<USER>] <<USER>> |<USER>| {<USER>}' "" "dev" "" ""
check_sanitize "user at a sentence end" \
    "logged in as dev. Then dev." "logged in as <USER>. Then <USER>." "" "dev" "" ""
check_sanitize "user next to a dash or underscore edge" \
    "dev-box dev_x x-dev" "<USER>-box <USER>_x x-<USER>" "" "dev" "" ""
check_sanitize "a letter or digit neighbour keeps the word" \
    "developer devbox-012 dev2 2dev nodev" "developer devbox-012 dev2 2dev nodev" "" "dev" "" ""
check_sanitize "host match ignores case" \
    "DEVBOX-01 Devbox-01 devbox-01 DevBox-012" "<HOST> <HOST> <HOST> DevBox-012" "" "alice" "devbox-01" ""
check_sanitize "uppercase host in a short and a full form" \
    "BOX.LAN Box" "<HOST> <HOST>" "" "alice" "box.lan" ""
check_sanitize "user match stays case-sensitive" \
    "Dev dev DEV" "Dev <USER> DEV" "" "dev" "" ""
check_sanitize "regex and glob metacharacters in a name stay literal" \
    "a.b axb a*b a?b" "<USER> axb a*b a?b" "" "a.b" "" ""

echo "case: cnh_sanitize handles every kind on one line, home before user, and is idempotent (G4)"
COMPOSITE_IN="alice@devbox-01 /home/alice/x 019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b sk-abcdefghijklmnopqrstuv"
COMPOSITE_WANT="<USER>@<HOST> <HOME>/x <UUID> <TOKEN>"
check_sanitize "composite line" "${COMPOSITE_IN}" "${COMPOSITE_WANT}"
check_sanitize "second pass changes nothing" "${COMPOSITE_WANT}" "${COMPOSITE_WANT}"

echo "case: cnh_classify_verdict (G4)"
# expect attempted target hook-block -> verdict
#   denied        a deny case: attempted, target absent, a hook block event seen
#   allowed       an allow (positive-control) case: attempted and the write landed
#   hooks-inactive a deny case whose target WAS written with no block event
#   inconclusive  everything else -- never a pass without proof the call ran
classify_cases=(
    "deny yes absent yes|denied"
    "deny yes absent no|inconclusive"
    "deny yes written no|hooks-inactive"
    "deny yes written yes|inconclusive"
    "deny no absent no|inconclusive"
    "deny no absent yes|inconclusive"
    "deny no written no|inconclusive"
    "allow yes written no|allowed"
    "allow yes written yes|inconclusive"
    "allow yes absent yes|denied"
    "allow yes absent no|inconclusive"
    "allow no absent no|inconclusive"
    "allow no written no|inconclusive"
)
for c in "${classify_cases[@]}"; do
    args="${c%%|*}"
    want="${c##*|}"
    read -r -a argv <<< "${args}"
    got="$(cnh_classify_verdict "${argv[@]}" 2>/dev/null)"
    rc=$?
    if [[ "${rc}" -eq 0 && "${got}" == "${want}" ]]; then
        pass
    else
        fail "cnh_classify_verdict ${args}: expected ${want} (rc 0), got '${got}' (rc ${rc})"
    fi
done

echo "case: cnh_is_sandbox needs CENCI_SANDBOX=1 AND a marker file (G4, Q4)"
# sandbox-env|markers|expected
sandbox_cases=(
    "1|${MARKER_FILE}|yes"
    "1|${MISSING_MARKER} ${MARKER_FILE}|yes"
    "1|${MISSING_MARKER}|no"
    "1|${MARKER_DIR}|no"
    "unset|${MARKER_FILE}|no"
    "0|${MARKER_FILE}|no"
    "true|${MARKER_FILE}|no"
    "|${MARKER_FILE}|no"
)
for c in "${sandbox_cases[@]}"; do
    envv="${c%%|*}"
    rest="${c#*|}"
    markers="${rest%|*}"
    want="${rest##*|}"
    read -r -a marker_args <<< "${markers}"
    (
        if [[ "${envv}" == unset ]]; then
            unset CENCI_SANDBOX
        else
            export CENCI_SANDBOX="${envv}"
        fi
        cnh_is_sandbox "${marker_args[@]}"
    ) 2>/dev/null
    rc=$?
    if [[ "${want}" == yes && "${rc}" -eq 0 ]] || [[ "${want}" == no && "${rc}" -ne 0 ]]; then
        pass
    else
        fail "cnh_is_sandbox CENCI_SANDBOX='${envv}' markers='${markers}': expected ${want}, rc=${rc}"
    fi
done

echo "case: cnh_trust_bypass_flag prints the flag only in sandbox mode (G3, G4)"
got="$( (export CENCI_SANDBOX=1; cnh_trust_bypass_flag "${MARKER_FILE}") 2>/dev/null)"
if [[ "${got}" == "${BYPASS_FLAG}" ]]; then
    pass
else
    fail "cnh_trust_bypass_flag in sandbox mode: expected '${BYPASS_FLAG}', got '${got}'"
fi
got="$( (export CENCI_SANDBOX=1; cnh_trust_bypass_flag "${MISSING_MARKER}"; echo "rc=$?") 2>/dev/null)"
if [[ "${got}" == "rc=0" ]]; then
    pass
else
    fail "cnh_trust_bypass_flag with a spoofed CENCI_SANDBOX=1 and no marker: expected no output and rc 0, got '${got}'"
fi
got="$( (unset CENCI_SANDBOX; cnh_trust_bypass_flag "${MARKER_FILE}"; echo "rc=$?") 2>/dev/null)"
if [[ "${got}" == "rc=0" ]]; then
    pass
else
    fail "cnh_trust_bypass_flag on a host with a marker but no CENCI_SANDBOX: expected no output and rc 0, got '${got}'"
fi

finish
