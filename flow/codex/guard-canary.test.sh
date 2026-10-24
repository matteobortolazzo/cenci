#!/usr/bin/env bash
# guard-canary.test.sh -- deterministic suite for codex/guard-canary.sh (#1213).
#
# The helper builds one unique probe per (matcher group, guard) pair of the
# Codex hook manifest, proves against the REAL guard scripts that exactly one
# guard denies each probe, judges the in-session results together with the
# filesystem, removes anything that got written, and prints the refusal or pass
# text. Everything here runs without a Codex binary: the "in-session" part is
# simulated by writing (or not writing) the probe targets ourselves.
#
#   P1  plan: a 0700 hex-named run dir outside the main worktree, one probe per
#       manifest pair (four today), absolute and unique targets across runs
#   P2  every probe is denied by exactly the expected real guard (exit 2 with
#       its message), on a shell payload and on an apply_patch payload
#   P3  fail closed: unknown guard, unknown matcher, unreadable manifest,
#       missing jq, a guard that does not deny (self-check mismatch)
#   P4  a TMPDIR inside the main worktree is never used for the run dir
#   C1  check: written targets are removed and reported; a removal that cannot
#       happen is reported by exact path and still refuses; all-denied is active
#       and removes the run dir; a "denied" claim cannot override a written file
#   C2  refusal text: pending trust, /hooks, written vs. no-guard-message
#   C3  sandbox label only with CENCI_SANDBOX=1 plus a hard-coded marker file;
#       never "trusted"; nothing changes trust (the fake HOME stays untouched)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 1
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 1
CANARY="${SCRIPT_DIR}/guard-canary.sh"
LIB="${SCRIPT_DIR}/native-hooks-lib.sh"
SANDBOX_LABEL="hook trust bypassed by launcher (explicit flag)"

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

echo "guard-canary.test.sh"

command -v jq >/dev/null 2>&1 || {
    echo "SKIP: jq not found on PATH"
    exit 0
}
command -v git >/dev/null 2>&1 || {
    echo "SKIP: git not found on PATH"
    exit 0
}

# ── Setup ───────────────────────────────────────────────────────────────
# Not under /tmp: guard-main-worktree allowlists /tmp/*, which would make every
# main-worktree probe legitimately allowed.
TEST_ROOT="$(mktemp -d /var/tmp/guard-canary-test.XXXXXX)" || {
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

REPO="${TEST_ROOT}/repo"
WT="${REPO}/.worktrees/1-canary"
TMP_BASE="${TEST_ROOT}/tmp"
FAKE_HOME="${TEST_ROOT}/home"
RUNS="${TEST_ROOT}/runs"
mkdir_checked "${REPO}/.cenci"
mkdir_checked "${TMP_BASE}"
mkdir_checked "${FAKE_HOME}"
mkdir_checked "${RUNS}"
printf '{}\n' > "${REPO}/.cenci/config.json" || exit 1
printf '# fixture\n' > "${REPO}/AGENTS.md" || exit 1
git -C "${REPO}" init -q || exit 1
git -C "${REPO}" symbolic-ref HEAD refs/heads/main || exit 1
git -C "${REPO}" add -A || exit 1
git -C "${REPO}" -c user.name=canary-test -c user.email=canary-test@localhost \
    -c commit.gpgsign=false commit -q -m seed || exit 1
git -C "${REPO}" worktree add -q "${WT}" -b canary-1 || exit 1

if [[ ! -x "${CANARY}" ]]; then
    fail "guard-canary.sh must exist and be executable: ${CANARY}"
    finish
fi
if [[ ! -f "${LIB}" ]]; then
    fail "helper library not found: ${LIB}"
    finish
fi

MARKER_FILE="${TEST_ROOT}/container-marker"
MISSING_MARKER="${TEST_ROOT}/no-such-container-marker"
: > "${MARKER_FILE}"

# build_farm <excluded-tool> <dir> -- a directory of symlinks to every
# executable on PATH except the excluded tool (so the tool is genuinely absent).
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

# make_plugin <name> <markers-or-empty> [manifest-file] -- a throwaway plugin
# root holding a copy of the helper (and of the library, with its HARD-CODED
# container-marker list swapped when <markers> is given -- the library has no
# environment seam for it), a manifest (the real one unless given) and a
# symlink to the real hook scripts. Echoes the copied helper path.
make_plugin() {
    local name="$1" markers="$2" manifest="${3:-${SCRIPT_DIR}/hooks.json}"
    local root="${TEST_ROOT}/plugin-${name}" dir
    dir="${root}/codex"
    mkdir -p "${dir}" || return 1
    cp "${CANARY}" "${dir}/guard-canary.sh" || return 1
    if [[ -n "${markers}" ]]; then
        sed "s|^CNH_DEFAULT_CONTAINER_MARKERS=.*|CNH_DEFAULT_CONTAINER_MARKERS=\"${markers}\"|" "${LIB}" > "${dir}/native-hooks-lib.sh" || return 1
        grep -qxF "CNH_DEFAULT_CONTAINER_MARKERS=\"${markers}\"" "${dir}/native-hooks-lib.sh" || return 1
    else
        cp "${LIB}" "${dir}/native-hooks-lib.sh" || return 1
    fi
    cp "${manifest}" "${dir}/hooks.json" || return 1
    ln -s "${FLOW_DIR}/hooks" "${root}/hooks" || return 1
    printf '%s\n' "${dir}/guard-canary.sh"
}

RUN_N=0
OUT=""
R_RC=""

# run_helper <helper> <cwd> <path> <env-assignment>... -- <args>... -- run the
# helper under a scrubbed environment (env -i) with only the given variables.
# Sets OUT (per-run stdout file) and R_RC. Pure: never calls fail().
run_helper() {
    local helper="$1" cwd="$2" path="$3"
    shift 3
    local -a envs=()
    while [[ "$#" -gt 0 && "$1" != -- ]]; do
        envs+=("$1")
        shift
    done
    [[ "$#" -gt 0 ]] && shift
    RUN_N=$((RUN_N + 1))
    OUT="${RUNS}/${RUN_N}"
    (
        cd "${cwd}" || exit 97
        exec env -i HOME="${FAKE_HOME}" PATH="${path}" ${envs[@]+"${envs[@]}"} \
            "${BASH_BIN}" "${helper}" "$@"
    ) > "${OUT}" 2> "${OUT}.err"
    R_RC=$?
}

# plan_in <cwd> [env-assignment...] -- plan with the real helper, TMPDIR=TMP_BASE
# unless an env assignment overrides it.
plan_in() {
    local cwd="$1"
    shift
    run_helper "${CANARY}" "${cwd}" "${PATH}" "TMPDIR=${TMP_BASE}" "$@" -- plan
}

# probe_field <out-file> <n> <field-index> -- tab-separated PROBE line field.
probe_field() {
    awk -F'\t' -v n="$2" -v i="$3" '$1 == "PROBE" && $2 == n { print $i }' "$1"
}

# kv <out-file> <KEY> -- value of the first KEY=value line.
kv() {
    awk -v k="$2" 'index($0, k "=") == 1 { print substr($0, length(k) + 2); exit }' "$1"
}

probe_count() {
    awk -F'\t' '$1 == "PROBE" { c++ } END { print c + 0 }' "$1"
}

# count_run_dirs_in <dir> -- echo how many cenci-guard-canary-* entries exist.
count_run_dirs_in() {
    local c=0 e
    for e in "$1"/cenci-guard-canary-*; do
        [[ -e "${e}" || -L "${e}" ]] && c=$((c + 1))
    done
    echo "${c}"
}

mode_of() {
    stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1" 2>/dev/null
}

TAB=$'\t'
HEX_RE='^[0-9a-f]{16}$'

# ── P1: plan ────────────────────────────────────────────────────────────
echo "case: plan from the main worktree lists one probe per manifest pair (P1)"
plan_in "${REPO}"
PLAN1="${OUT}"
if [[ "${R_RC}" -eq 0 ]]; then
    pass
else
    fail "plan must succeed on the real manifest (rc=${R_RC}): $(head -c 400 "${PLAN1}") $(head -c 300 "${PLAN1}.err")"
    finish
fi
RUN_DIR1="$(kv "${PLAN1}" CANARY_RUN_DIR)"
if [[ "$(probe_count "${PLAN1}")" -eq 4 ]]; then
    pass
else
    fail "the real manifest has four (matcher group, guard) pairs: expected 4 PROBE lines, got $(probe_count "${PLAN1}")"
fi
# matcher group, guard -> expected tool; every pair exactly once.
for pair in "Write|Edit${TAB}check-sensitive-files${TAB}apply_patch" \
    "Write|Edit${TAB}guard-main-worktree${TAB}apply_patch" \
    "Bash${TAB}check-sensitive-files${TAB}shell" \
    "Bash${TAB}guard-main-worktree${TAB}shell"; do
    want_group="${pair%%"${TAB}"*}"
    rest="${pair#*"${TAB}"}"
    want_guard="${rest%%"${TAB}"*}"
    want_tool="${rest##*"${TAB}"}"
    got="$(awk -F'\t' -v g="${want_group}" -v u="${want_guard}" -v t="${want_tool}" \
        '$1 == "PROBE" && $5 == g && $4 == u && $3 == t { c++ } END { print c + 0 }' "${PLAN1}")"
    if [[ "${got}" -eq 1 ]]; then
        pass
    else
        fail "expected exactly one probe for group '${want_group}' guard '${want_guard}' using tool '${want_tool}', got ${got}"
    fi
done

if [[ "${RUN_DIR1}" == /* && -d "${RUN_DIR1}" ]]; then
    pass
else
    fail "CANARY_RUN_DIR must be an absolute existing directory, got '${RUN_DIR1}'"
fi
if [[ "$(mode_of "${RUN_DIR1}")" == 700 ]]; then
    pass
else
    fail "the run dir must be mode 0700, got '$(mode_of "${RUN_DIR1}")'"
fi
if [[ "${RUN_DIR1##*/}" =~ ^cenci-guard-canary-([0-9a-f]+)$ ]] && [[ "${BASH_REMATCH[1]}" =~ ${HEX_RE} ]]; then
    pass
else
    fail "the run dir name must be cenci-guard-canary-<16 hex>, got '${RUN_DIR1##*/}'"
fi
case "${RUN_DIR1}" in
    "${REPO}" | "${REPO}"/*) fail "the run dir must be outside the main worktree: ${RUN_DIR1}" ;;
    *) pass ;;
esac

declare -a TARGETS1=()
for n in 1 2 3 4; do
    t="$(probe_field "${PLAN1}" "${n}" 6)"
    TARGETS1+=("${t}")
    guard="$(probe_field "${PLAN1}" "${n}" 4)"
    if [[ "${t}" == /* ]]; then
        pass
    else
        fail "probe ${n} target must be absolute, got '${t}'"
    fi
    case "${guard}" in
        check-sensitive-files)
            if [[ "${t}" == "${RUN_DIR1}"/*.env ]]; then
                pass
            else
                fail "probe ${n} (check-sensitive-files) must target <run-dir>/*.env, got '${t}'"
            fi
            ;;
        guard-main-worktree)
            if [[ "${t}" =~ ^${REPO}/cenci-guard-canary-[0-9a-f]{16}-${n}\.txt$ ]]; then
                pass
            else
                fail "probe ${n} (guard-main-worktree) must target <main-root>/cenci-guard-canary-<hex>-${n}.txt, got '${t}'"
            fi
            ;;
        *) fail "probe ${n}: unexpected guard '${guard}'" ;;
    esac
    if [[ -e "${t}" ]]; then
        fail "plan must not create probe target ${t}"
    else
        pass
    fi
done

echo "case: plan from a feature worktree session still targets the MAIN worktree (P1)"
plan_in "${WT}"
PLAN_WT="${OUT}"
if [[ "${R_RC}" -eq 0 ]]; then
    pass
else
    fail "plan from a feature worktree must succeed (rc=${R_RC}): $(head -c 400 "${PLAN_WT}")"
fi
for n in 1 2 3 4; do
    if [[ "$(probe_field "${PLAN_WT}" "${n}" 4)" == guard-main-worktree ]]; then
        t="$(probe_field "${PLAN_WT}" "${n}" 6)"
        if [[ "${t}" == "${REPO}/cenci-guard-canary-"* ]]; then
            pass
        else
            fail "worktree-session probe ${n} must target the main worktree (${REPO}), got '${t}'"
        fi
    fi
done

echo "case: targets and run ids are unique across runs (P1)"
plan_in "${REPO}"
PLAN2="${OUT}"
RUN_DIR2="$(kv "${PLAN2}" CANARY_RUN_DIR)"
if [[ "${R_RC}" -eq 0 && -n "${RUN_DIR2}" && "${RUN_DIR2}" != "${RUN_DIR1}" ]]; then
    pass
else
    fail "a second plan must succeed with a different run dir (rc=${R_RC}, '${RUN_DIR1}' vs '${RUN_DIR2}')"
fi
UNIQ="$( { for n in 1 2 3 4; do
    probe_field "${PLAN1}" "${n}" 6
    probe_field "${PLAN2}" "${n}" 6
done; } | sort | uniq | wc -l | tr -d '[:space:]')"
if [[ "${UNIQ}" -eq 8 ]]; then
    pass
else
    fail "eight probe targets across two runs must be eight distinct paths, got ${UNIQ}"
fi

# ── P2: the real guards deny exactly the expected probe ─────────────────
# A payload built here, independent of the helper, run through BOTH real guard
# scripts the way Codex launches them (the script directly, payload on stdin,
# cwd = the session cwd). Exactly the expected guard may exit 2, with its own
# message; the other must allow.
G_RC=""
G_ERR=""
# run_guard <script> <cwd> <payload>
run_guard() {
    G_ERR="$( (cd "$2" && printf '%s' "$3" | env -u CENCI_CODEX_HOOK_CAPTURE_DIR "$1" 2>&1 >/dev/null) )"
    G_RC=$?
}

payload_shell() { # <cwd> <target>
    jq -n --arg cwd "$1" --arg cmd "printf x > '$2'" \
        '{hook_event_name: "PreToolUse", tool_name: "Bash", cwd: $cwd, tool_input: {command: $cmd}}'
}

payload_patch() { # <cwd> <target>
    jq -n --arg cwd "$1" --arg cmd "$(printf '*** Begin Patch\n*** Add File: %s\n+x\n*** End Patch' "$2")" \
        '{hook_event_name: "PreToolUse", tool_name: "apply_patch", cwd: $cwd, tool_input: {command: $cmd}}'
}

echo "case: for each probe exactly the expected real guard exits 2 with its message, on shell and apply_patch payloads (P2)"
SENS="${FLOW_DIR}/hooks/scripts/check-sensitive-files.sh"
MAINW="${FLOW_DIR}/hooks/scripts/guard-main-worktree.sh"
for session in "${REPO}" "${WT}"; do
    for n in 1 2 3 4; do
        t="$(probe_field "${PLAN1}" "${n}" 6)"
        guard="$(probe_field "${PLAN1}" "${n}" 4)"
        for shape in shell patch; do
            if [[ "${shape}" == shell ]]; then
                payload="$(payload_shell "${session}" "${t}")"
            else
                payload="$(payload_patch "${session}" "${t}")"
            fi
            run_guard "${SENS}" "${session}" "${payload}"
            sens_rc="${G_RC}" sens_err="${G_ERR}"
            run_guard "${MAINW}" "${session}" "${payload}"
            main_rc="${G_RC}" main_err="${G_ERR}"
            if [[ "${guard}" == check-sensitive-files ]]; then
                want_sens=2 want_main=0
            else
                want_sens=0 want_main=2
            fi
            if [[ "${sens_rc}" -eq "${want_sens}" && "${main_rc}" -eq "${want_main}" ]]; then
                pass
            else
                fail "probe ${n} (${guard}, ${shape}, session ${session##*/}): check-sensitive-files rc=${sens_rc} (want ${want_sens}), guard-main-worktree rc=${main_rc} (want ${want_main})"
            fi
            if [[ "${guard}" == check-sensitive-files ]]; then
                msg_ok=0
                [[ "${sens_err}" == *"Refusing to write"* ]] && msg_ok=1
            else
                msg_ok=0
                [[ "${main_err}" == *"targets the main worktree"* ]] && msg_ok=1
            fi
            if [[ "${msg_ok}" -eq 1 ]]; then
                pass
            else
                fail "probe ${n} (${guard}, ${shape}): the denying guard's own message is missing (sens: '${sens_err:0:120}', main: '${main_err:0:120}')"
            fi
        done
    done
done

# ── P3: fail closed ─────────────────────────────────────────────────────
# assert_plan_refused <label> -- the last run_helper call refused to plan: rc 1,
# CANARY_STATUS=refused, no PROBE lines, no run dir left in the temp base.
assert_plan_refused() {
    local label="$1"
    if [[ "${R_RC}" -eq 1 ]]; then
        pass
    else
        fail "${label}: plan must exit 1 (rc=${R_RC}): $(head -c 300 "${OUT}") $(head -c 200 "${OUT}.err")"
    fi
    if grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
        pass
    else
        fail "${label}: plan must print CANARY_STATUS=refused (stdout: $(head -c 300 "${OUT}"))"
    fi
    if [[ "$(probe_count "${OUT}")" -eq 0 ]]; then
        pass
    else
        fail "${label}: a refused plan must print no PROBE line"
    fi
    if [[ "$(count_run_dirs_in "${TMP_BASE}")" -eq "${BASELINE_RUN_DIRS}" ]]; then
        pass
    else
        fail "${label}: a refused plan must not leave its run dir behind"
    fi
}

# Two successful plans above left their run dirs (never checked) in TMP_BASE.
BASELINE_RUN_DIRS="$(count_run_dirs_in "${TMP_BASE}")"

write_manifest() { # <file> <matcher> <command>
    jq -n --arg m "$2" --arg c "$3" \
        '{hooks: {PreToolUse: [{matcher: $m, hooks: [{type: "command", command: $c, timeout: 10}]}]}}' > "$1"
}

SENS_CMD='"${PLUGIN_ROOT}/hooks/scripts/check-sensitive-files.sh"'

echo "case: an unknown guard in the manifest fails closed (P3)"
write_manifest "${TEST_ROOT}/m-unknown-guard.json" "Bash" '"${PLUGIN_ROOT}/hooks/scripts/some-new-guard.sh"'
H="$(make_plugin unknown-guard "" "${TEST_ROOT}/m-unknown-guard.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "unknown guard"

echo "case: an unknown matcher in the manifest fails closed (P3)"
write_manifest "${TEST_ROOT}/m-unknown-matcher.json" "Read" "${SENS_CMD}"
H="$(make_plugin unknown-matcher "" "${TEST_ROOT}/m-unknown-matcher.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "unknown matcher"

echo "case: a guard command that is not the exact PLUGIN_ROOT-anchored script fails closed (P3)"
write_manifest "${TEST_ROOT}/m-odd-command.json" "Bash" '"${PLUGIN_ROOT}/hooks/scripts/check-sensitive-files.sh" ; touch /x'
H="$(make_plugin odd-command "" "${TEST_ROOT}/m-odd-command.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "odd guard command"

echo "case: a manifest with no PreToolUse guard fails closed (P3)"
printf '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}\n' > "${TEST_ROOT}/m-no-pretool.json"
H="$(make_plugin no-pretool "" "${TEST_ROOT}/m-no-pretool.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "no PreToolUse guard"

echo "case: an unreadable manifest fails closed (P3)"
printf 'not json at all\n' > "${TEST_ROOT}/m-garbage.json"
H="$(make_plugin garbage "" "${TEST_ROOT}/m-garbage.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "garbage manifest"
H="$(make_plugin missing-manifest "")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
rm -f "${H%/*}/hooks.json"
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "missing manifest"

echo "case: a missing jq fails closed (P3)"
FARM="${TEST_ROOT}/farm-no-jq"
if ! build_farm jq "${FARM}"; then
    echo "FAIL: test setup: could not build a PATH without jq" >&2
    exit 1
fi
if PATH="${FARM}" "${BASH_BIN}" -c 'command -v jq >/dev/null 2>&1'; then
    echo "FAIL: test setup: the jq-free PATH still resolves jq" >&2
    exit 1
fi
run_helper "${CANARY}" "${REPO}" "${FARM}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "missing jq"

echo "case: a guard that does not deny its probe makes the self-check fail closed (P3)"
H="$(make_plugin lax-guards "")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
LAX_ROOT="${H%/codex/guard-canary.sh}"
rm -f "${LAX_ROOT}/hooks"
mkdir_checked "${LAX_ROOT}/hooks/scripts"
for g in check-sensitive-files guard-main-worktree; do
    printf '#!/bin/sh\nexit 0\n' > "${LAX_ROOT}/hooks/scripts/${g}.sh"
    chmod +x "${LAX_ROOT}/hooks/scripts/${g}.sh"
done
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "guards that allow everything"
H="$(make_plugin swapped-guards "")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
SW_ROOT="${H%/codex/guard-canary.sh}"
rm -f "${SW_ROOT}/hooks"
mkdir_checked "${SW_ROOT}/hooks/scripts"
cp -R "${FLOW_DIR}/hooks/scripts/." "${SW_ROOT}/hooks/scripts/" || exit 1
# Both guards deny the same way: the probe would be denied by BOTH, which breaks
# the "exactly one guard" design.
cp "${FLOW_DIR}/hooks/scripts/check-sensitive-files.sh" "${SW_ROOT}/hooks/scripts/guard-main-worktree.sh" || exit 1
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "a probe denied by both guards"

echo "case: an unconfigured repo (guards are a no-op there) refuses to plan (P3)"
UNCONF="${TEST_ROOT}/unconfigured"
mkdir_checked "${UNCONF}"
git -C "${UNCONF}" init -q || exit 1
run_helper "${CANARY}" "${UNCONF}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "unconfigured repo"

echo "case: outside any git repository refuses to plan (P3)"
run_helper "${CANARY}" "${TEST_ROOT}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "no git repository"

# ── P4: TMPDIR inside the main worktree ─────────────────────────────────
echo "case: a TMPDIR inside the main worktree is never used for the run dir (P4)"
mkdir_checked "${REPO}/inside-tmp"
plan_in "${REPO}" "TMPDIR=${REPO}/inside-tmp"
INSIDE="${OUT}"
RUN_DIR_IN="$(kv "${INSIDE}" CANARY_RUN_DIR)"
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${INSIDE}"; then
    pass
elif [[ "${R_RC}" -eq 0 && -n "${RUN_DIR_IN}" ]]; then
    case "${RUN_DIR_IN}" in
        "${REPO}" | "${REPO}"/*) fail "a TMPDIR inside the repo produced a run dir inside the repo: ${RUN_DIR_IN}" ;;
        *) pass ;;
    esac
else
    fail "a TMPDIR inside the repo must be rejected or fall back (rc=${R_RC}): $(head -c 300 "${INSIDE}")"
fi
if [[ "$(count_run_dirs_in "${REPO}/inside-tmp")" -eq 0 && "$(count_run_dirs_in "${REPO}")" -eq 0 ]]; then
    pass
else
    fail "no run dir may be created inside the main worktree"
fi

# ── C1: check ───────────────────────────────────────────────────────────
# fresh_plan [cwd] -- a new plan; sets CUR_DIR and CUR_PLAN (fails the suite
# when the plan itself fails, since every check case depends on it).
CUR_DIR=""
CUR_PLAN=""
fresh_plan() {
    plan_in "${1:-${REPO}}"
    CUR_PLAN="${OUT}"
    CUR_DIR="$(kv "${CUR_PLAN}" CANARY_RUN_DIR)"
    if [[ "${R_RC}" -ne 0 || -z "${CUR_DIR}" || ! -d "${CUR_DIR}" ]]; then
        echo "FAIL: test setup: plan failed (rc=${R_RC}): $(head -c 300 "${CUR_PLAN}")" >&2
        exit 1
    fi
}

# check_run <dir> <result>... [-- env assignments before] -- run `check`.
check_run() {
    run_helper "${CANARY}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- check "$@"
}

ALL_DENIED=(1=denied 2=denied 3=denied 4=denied)

echo "case: all probes denied and nothing written -> active, run dir removed (C1)"
fresh_plan
check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
if [[ "${R_RC}" -eq 0 ]] && grep -qxF 'CANARY_STATUS=active' "${OUT}"; then
    pass
else
    fail "all-denied must exit 0 with CANARY_STATUS=active (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if [[ ! -e "${CUR_DIR}" ]]; then
    pass
else
    fail "an active canary must remove its run dir ${CUR_DIR}"
fi
if grep -q 'CANARY_STATUS=refused' "${OUT}"; then
    fail "an active canary must not print CANARY_STATUS=refused"
else
    pass
fi

echo "case: a written target is removed, reported, and refuses even when claimed denied (C1)"
fresh_plan
T1="$(probe_field "${CUR_PLAN}" 1 6)"   # .env under the run dir
T2="$(probe_field "${CUR_PLAN}" 2 6)"   # main-worktree .txt
printf 'x\n' > "${T1}"
printf 'x\n' > "${T2}"
check_run "${CUR_DIR}" 1=denied 2=not-denied 3=denied 4=denied
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
    pass
else
    fail "written targets must refuse (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if [[ ! -e "${T1}" && ! -e "${T2}" ]]; then
    pass
else
    fail "check must remove every written probe target"
fi
for t in "${T1}" "${T2}"; do
    if grep -qF -- "${t}" "${OUT}"; then
        pass
    else
        fail "check must report the written target by exact path: ${t}"
    fi
done
if grep -qiE 'written' "${OUT}"; then
    pass
else
    fail "the refusal must say a probe was written"
fi
if grep -q '^LEFTOVER' "${OUT}"; then
    fail "a removable written target must not be reported as a leftover"
else
    pass
fi

echo "case: no guard message and nothing written is inconclusive, not 'written' (C2)"
fresh_plan
check_run "${CUR_DIR}" 1=denied 2=denied 3=not-denied 4=denied
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
    pass
else
    fail "a not-denied probe must refuse (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if grep -qiE 'no guard message' "${OUT}" && grep -qiE 'inconclusive' "${OUT}"; then
    pass
else
    fail "an unwritten not-denied probe must be reported as 'no guard message' / inconclusive: $(head -c 600 "${OUT}")"
fi
if grep -qE '^(REMOVED|LEFTOVER)' "${OUT}"; then
    fail "nothing was written, so nothing may be reported removed or left over"
else
    pass
fi

echo "case: a missing probe result refuses (C1)"
fresh_plan
check_run "${CUR_DIR}" 1=denied 2=denied 3=denied
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
    pass
else
    fail "a probe with no reported result must refuse (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi

echo "case: an unrecognized result token refuses and removes nothing it should not (C1)"
fresh_plan
check_run "${CUR_DIR}" 1=denied 2=denied 3=denied 4=maybe
if [[ "${R_RC}" -ne 0 ]] && ! grep -qxF 'CANARY_STATUS=active' "${OUT}"; then
    pass
else
    fail "an unrecognized result must never produce an active canary (rc=${R_RC}): $(head -c 300 "${OUT}")"
fi

echo "case: a removal that cannot happen is reported by exact path and still refuses (C1)"
# A directory at the main-worktree probe target: rm -f cannot remove it.
fresh_plan
T2="$(probe_field "${CUR_PLAN}" 2 6)"
mkdir_checked "${T2}"
check_run "${CUR_DIR}" 1=denied 2=denied 3=denied 4=denied
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
    pass
else
    fail "an undeletable target must refuse even with every probe claimed denied (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if grep -qxF "LEFTOVER: ${T2}" "${OUT}"; then
    pass
else
    fail "the leftover must be reported as 'LEFTOVER: <exact path>' (stdout: $(head -c 500 "${OUT}"))"
fi
if [[ -d "${T2}" ]]; then
    pass
else
    fail "check must not recursively delete a directory it found at a probe target"
fi
rmdir "${T2}" 2>/dev/null
rm -rf "${CUR_DIR}"

echo "case: an undeletable target inside the run dir keeps the run dir and is reported (C1)"
fresh_plan
T1="$(probe_field "${CUR_PLAN}" 1 6)"
mkdir_checked "${T1}"
check_run "${CUR_DIR}" ${ALL_DENIED[@]+"${ALL_DENIED[@]}"}
if [[ "${R_RC}" -eq 1 ]] && grep -qxF "LEFTOVER: ${T1}" "${OUT}" && [[ -d "${T1}" ]]; then
    pass
else
    fail "a directory at a run-dir probe target must be a LEFTOVER line with the exact path and a refusal (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
rm -rf "${CUR_DIR}"

echo "case: check refuses a run dir that is not one of ours, and removes nothing (C1)"
mkdir_checked "${REPO}/not-a-run-dir"
printf 'keep\n' > "${REPO}/not-a-run-dir/keep.txt"
check_run "${REPO}/not-a-run-dir" "${ALL_DENIED[@]}"
if [[ "${R_RC}" -ne 0 ]] && ! grep -qxF 'CANARY_STATUS=active' "${OUT}" && [[ -f "${REPO}/not-a-run-dir/keep.txt" ]]; then
    pass
else
    fail "a directory that is not a canary run dir must be refused and left alone (rc=${R_RC}): $(head -c 300 "${OUT}")"
fi
check_run "${TMP_BASE}/cenci-guard-canary-0123456789abcdef" "${ALL_DENIED[@]}"
if [[ "${R_RC}" -ne 0 ]] && ! grep -qxF 'CANARY_STATUS=active' "${OUT}"; then
    pass
else
    fail "a run dir that does not exist must never produce an active canary (rc=${R_RC})"
fi
# A canary-shaped directory inside the main worktree is rejected too.
mkdir -m 700 "${REPO}/cenci-guard-canary-0123456789abcdef" || exit 1
printf 'keep\n' > "${REPO}/cenci-guard-canary-0123456789abcdef/keep.txt"
check_run "${REPO}/cenci-guard-canary-0123456789abcdef" "${ALL_DENIED[@]}"
if [[ "${R_RC}" -ne 0 ]] && [[ -f "${REPO}/cenci-guard-canary-0123456789abcdef/keep.txt" ]]; then
    pass
else
    fail "a canary-named directory inside the main worktree must be refused (rc=${R_RC})"
fi
rm -rf "${REPO}/cenci-guard-canary-0123456789abcdef" "${REPO}/not-a-run-dir"

# ── C2: refusal text ────────────────────────────────────────────────────
echo "case: the refusal names pending/untrusted hooks and points to /hooks (C2)"
fresh_plan
T2="$(probe_field "${CUR_PLAN}" 2 6)"
printf 'x\n' > "${T2}"
check_run "${CUR_DIR}" 1=not-denied 2=not-denied 3=not-denied 4=not-denied
REFUSAL="${OUT}"
if grep -qF '/hooks' "${REFUSAL}"; then
    pass
else
    fail "the refusal text must point to /hooks: $(head -c 600 "${REFUSAL}")"
fi
if grep -qi 'pending' "${REFUSAL}" && grep -qi 'untrusted' "${REFUSAL}"; then
    pass
else
    fail "the refusal text must name pending and untrusted hooks as a likely cause: $(head -c 600 "${REFUSAL}")"
fi
if grep -qi 'protection' "${REFUSAL}" && grep -qiE 'inactive|not active' "${REFUSAL}"; then
    pass
else
    fail "the refusal must say protection is inactive"
fi
if grep -qF "${SANDBOX_LABEL}" "${REFUSAL}"; then
    fail "a host refusal must not carry the sandbox label"
else
    pass
fi
if grep -qiE '(^|[^n])trusted' "${REFUSAL}"; then
    fail "no text may call anything 'trusted': $(grep -iE '(^|[^n])trusted' "${REFUSAL}" | head -n 2)"
else
    pass
fi

# ── C3: sandbox label ───────────────────────────────────────────────────
echo "case: the sandbox label appears only with CENCI_SANDBOX=1 AND a hard-coded marker file (C3)"
SB="$(make_plugin sandbox "${MARKER_FILE}")" || { echo "FAIL: test setup: sandbox plugin copy" >&2; exit 1; }
HOSTC="$(make_plugin host "${MISSING_MARKER}")" || { echo "FAIL: test setup: host plugin copy" >&2; exit 1; }

# label_case <label> <helper> <CENCI_SANDBOX value or unset> <expect: yes|no> <mode: refuse|active>
label_case() {
    local label="$1" helper="$2" sbx="$3" want="$4" mode="$5" t2 got
    local -a envs=("TMPDIR=${TMP_BASE}")
    [[ "${sbx}" == unset ]] || envs+=("CENCI_SANDBOX=${sbx}")
    run_helper "${helper}" "${REPO}" "${PATH}" "${envs[@]}" -- plan
    local plan_out="${OUT}" dir
    dir="$(kv "${plan_out}" CANARY_RUN_DIR)"
    if [[ "${R_RC}" -ne 0 || -z "${dir}" ]]; then
        fail "${label}: plan failed (rc=${R_RC}): $(head -c 300 "${plan_out}")"
        return 0
    fi
    if [[ "${mode}" == refuse ]]; then
        t2="$(probe_field "${plan_out}" 2 6)"
        printf 'x\n' > "${t2}"
        run_helper "${helper}" "${REPO}" "${PATH}" "${envs[@]}" -- check "${dir}" 1=denied 2=denied 3=denied 4=denied
    else
        run_helper "${helper}" "${REPO}" "${PATH}" "${envs[@]}" -- check "${dir}" "${ALL_DENIED[@]}"
    fi
    if grep -qF "${SANDBOX_LABEL}" "${OUT}"; then
        got=yes
    else
        got=no
    fi
    if [[ "${got}" == "${want}" ]]; then
        pass
    else
        fail "${label} (${mode}): sandbox label present=${got}, want ${want}: $(head -c 500 "${OUT}")"
    fi
    if grep -qiE '(^|[^n])trusted' "${OUT}"; then
        fail "${label} (${mode}): no text may call anything 'trusted'"
    else
        pass
    fi
    rm -rf "${dir}"
}

label_case "CENCI_SANDBOX=1 + marker" "${SB}" 1 yes refuse
label_case "CENCI_SANDBOX=1 + marker" "${SB}" 1 yes active
label_case "CENCI_SANDBOX=1 + no marker" "${HOSTC}" 1 no refuse
label_case "CENCI_SANDBOX=1 + no marker" "${HOSTC}" 1 no active
label_case "marker + CENCI_SANDBOX unset" "${SB}" unset no refuse
label_case "marker + CENCI_SANDBOX=0" "${SB}" 0 no refuse
label_case "marker + CENCI_SANDBOX=true" "${SB}" true no active

echo "case: no environment variable can name a container marker (C3)"
label_case "CNH_CONTAINER_MARKERS env" "${HOSTC}" 1 no refuse
run_helper "${HOSTC}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" CENCI_SANDBOX=1 "CNH_CONTAINER_MARKERS=${MARKER_FILE}" -- plan
SPOOF_DIR="$(kv "${OUT}" CANARY_RUN_DIR)"
run_helper "${HOSTC}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" CENCI_SANDBOX=1 "CNH_CONTAINER_MARKERS=${MARKER_FILE}" -- check "${SPOOF_DIR}" 1=denied 2=denied 3=denied 4=not-denied
if grep -qF "${SANDBOX_LABEL}" "${OUT}"; then
    fail "an environment-named marker must not turn a host run into a sandbox run"
else
    pass
fi
rm -rf "${SPOOF_DIR}"

echo "case: the helper never changes hook trust (the fake HOME stays empty) (C3)"
if [[ -z "$(find "${FAKE_HOME}" -mindepth 1 -print -quit)" ]]; then
    pass
else
    fail "the helper wrote into HOME: $(find "${FAKE_HOME}" -mindepth 1 | head -n 3)"
fi
if grep -nE 'config\.toml|\.codex|hooks trust|trust_hooks' "${CANARY}" | grep -vE '^[0-9]+:[[:space:]]*#' | grep -E 'rm |>|tee|sed -i|printf .*>' >/dev/null; then
    fail "guard-canary.sh must not write Codex configuration"
else
    pass
fi

# ── Cleanup of the plans no case checked ────────────────────────────────
rm -rf "${RUN_DIR1}" "${RUN_DIR2}" "${RUN_DIR_IN}"

finish
