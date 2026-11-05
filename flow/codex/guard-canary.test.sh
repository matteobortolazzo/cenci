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
#   P5  (#1411) single-quoted COMMAND lines, INSTRUCTION text with all four
#       verdict tokens, a quote / metacharacter repository path, and a receipt
#       present before CANARY_PLAN=ok refuses the plan
#   R   (#1411) a probe counts as denied only with a valid guard receipt:
#       receipts are staged through the REAL guards (deny_probes), never
#       hand-written on an active path; missing / wrong-hex / wrong-guard /
#       wrong-tool / wrong-probe / symlinked / malformed receipts refuse with
#       BAD_RECEIPT; blocked and not-attempted get their own result lines
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

# command_of <out-file> <n> -- the ready-to-run shell command plan printed for
# probe <n> (the COMMAND<TAB><n><TAB><command> line), empty when there is none.
command_of() {
    awk -F'\t' -v n="$2" '$1 == "COMMAND" && $2 == n { print $3; exit }' "$1"
}

# make_fixture_repo <dir> -- a cenci-configured repository with a seed commit.
make_fixture_repo() {
    mkdir_checked "$1/.cenci"
    printf '{}\n' > "$1/.cenci/config.json" || exit 1
    git -C "$1" init -q || exit 1
    git -C "$1" add -A || exit 1
    git -C "$1" -c user.name=canary-test -c user.email=canary-test@localhost \
        -c commit.gpgsign=false commit -q -m seed || exit 1
}

# tree_of <dir> -- sorted listing of every entry below <dir> outside .git.
tree_of() {
    find "$1" -mindepth 1 -not -path '*/.git' -not -path '*/.git/*' | LC_ALL=C sort
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

# deny_probes <plan-out> <session-cwd> [n...] -- what a session with live hooks
# produces: each probe's call (the shell probe's exact COMMAND line, the
# apply_patch probe's Add File envelope) is piped through BOTH real guard
# scripts with TMPDIR=TMP_BASE, so the denying guard leaves its own receipt in
# the run dir. Default: every PROBE line. Nothing is hand-written here.
deny_probes() {
    local plan_out="$1" session="$2" n tool guard t payload g want cmd
    shift 2
    local -a ns=("$@")
    if [[ "${#ns[@]}" -eq 0 ]]; then
        while IFS=$'\t' read -r kind n _; do
            [[ "${kind}" == PROBE ]] && ns+=("${n}")
        done < "${plan_out}"
    fi
    for n in "${ns[@]}"; do
        tool="$(probe_field "${plan_out}" "${n}" 3)"
        guard="$(probe_field "${plan_out}" "${n}" 4)"
        t="$(probe_field "${plan_out}" "${n}" 6)"
        if [[ "${tool}" == shell ]]; then
            cmd="$(command_of "${plan_out}" "${n}")"
            [[ -n "${cmd}" ]] || fail "deny_probes: plan printed no COMMAND line for shell probe ${n}"
            payload="$(jq -n --arg cwd "${session}" --arg cmd "${cmd}" \
                '{hook_event_name: "PreToolUse", tool_name: "Bash", cwd: $cwd, tool_input: {command: $cmd}}')"
        else
            payload="$(payload_patch "${session}" "${t}")"
        fi
        for g in check-sensitive-files guard-main-worktree; do
            G_ERR="$( (cd "${session}" && printf '%s' "${payload}" \
                | env -u CENCI_CODEX_HOOK_CAPTURE_DIR TMPDIR="${TMP_BASE}" "${FLOW_DIR}/hooks/scripts/${g}.sh" 2>&1 >/dev/null) )"
            G_RC=$?
            want=0
            [[ "${g}" == "${guard}" ]] && want=2
            if [[ "${G_RC}" -ne "${want}" ]]; then
                fail "deny_probes: probe ${n} (${tool}): ${g} exited ${G_RC}, want ${want}: ${G_ERR:0:160}"
            fi
        done
    done
}

# receipts_in <run-dir> -- how many receipt-* entries the run dir holds.
receipts_in() {
    local c=0 e
    for e in "$1"/receipt-*; do
        [[ -e "${e}" || -L "${e}" ]] && c=$((c + 1))
    done
    echo "${c}"
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
# assert_plan_refused <label> <reason-fragment> -- the last run_helper call
# refused to plan: rc 1, CANARY_STATUS=refused, a "Guard canary refused:" line
# carrying the expected reason (so a broken fixture cannot pass the case by
# refusing for some other reason), no PROBE lines, no run dir left in the temp
# base.
assert_plan_refused() {
    local label="$1" reason="${2:-}"
    if [[ -z "${reason}" ]]; then
        fail "${label}: test bug: assert_plan_refused needs a reason fragment"
    elif grep -F 'Guard canary refused:' "${OUT}" | grep -qF -- "${reason}"; then
        pass
    else
        fail "${label}: the refusal must give the reason '${reason}' (stdout: $(head -c 400 "${OUT}"))"
    fi
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
    if grep -q '^LEFTOVER' "${OUT}"; then
        fail "${label}: a refused plan whose run dir was removed must not report a LEFTOVER"
    else
        pass
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
assert_plan_refused "unknown guard" "unrecognized guard command in the manifest"

echo "case: an unknown matcher in the manifest fails closed (P3)"
write_manifest "${TEST_ROOT}/m-unknown-matcher.json" "Read" "${SENS_CMD}"
H="$(make_plugin unknown-matcher "" "${TEST_ROOT}/m-unknown-matcher.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "unknown matcher" "unrecognized matcher 'Read'"

echo "case: a guard command that is not the exact PLUGIN_ROOT-anchored script fails closed (P3)"
write_manifest "${TEST_ROOT}/m-odd-command.json" "Bash" '"${PLUGIN_ROOT}/hooks/scripts/check-sensitive-files.sh" ; touch /x'
H="$(make_plugin odd-command "" "${TEST_ROOT}/m-odd-command.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "odd guard command" "unrecognized guard command in the manifest"

echo "case: a manifest with no PreToolUse guard fails closed (P3)"
printf '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}\n' > "${TEST_ROOT}/m-no-pretool.json"
H="$(make_plugin no-pretool "" "${TEST_ROOT}/m-no-pretool.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "no PreToolUse guard" "lists no PreToolUse guard"

echo "case: an unreadable manifest fails closed (P3)"
printf 'not json at all\n' > "${TEST_ROOT}/m-garbage.json"
H="$(make_plugin garbage "" "${TEST_ROOT}/m-garbage.json")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "garbage manifest" "unreadable or not the expected shape"
H="$(make_plugin missing-manifest "")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
rm -f "${H%/*}/hooks.json"
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "missing manifest" "is missing or unreadable"

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
assert_plan_refused "missing jq" "jq is required"

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
assert_plan_refused "guards that allow everything" "the probe self-check failed against the real guard scripts"
if grep -F 'Guard canary refused:' "${OUT}" | grep -qF 'should deny its own probe'; then
    pass
else
    fail "guards that allow everything: the self-check reason must say the guard should deny its own probe (stdout: $(head -c 400 "${OUT}"))"
fi
H="$(make_plugin swapped-guards "")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
SW_ROOT="${H%/codex/guard-canary.sh}"
rm -f "${SW_ROOT}/hooks"
mkdir_checked "${SW_ROOT}/hooks/scripts"
cp -R "${FLOW_DIR}/hooks/scripts/." "${SW_ROOT}/hooks/scripts/" || exit 1
# Both guards deny the same way: the probe would be denied by BOTH, which breaks
# the "exactly one guard" design.
cp "${FLOW_DIR}/hooks/scripts/check-sensitive-files.sh" "${SW_ROOT}/hooks/scripts/guard-main-worktree.sh" || exit 1
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "a probe denied by both guards" "so the probe is not single-guard"

echo "case: a refused plan whose run dir cannot be removed reports LEFTOVER: <run dir> (P3)"
# The run dir is swapped for a symlink by a fake check-sensitive-files guard that
# runs during the self-check (a stand-in for a tamper or race between mkdir and
# cleanup). remove_run_dir refuses to delete a symlink, so the removal cannot
# happen; no permission lever is involved, so this holds for root too.
H="$(make_plugin leftover-plan "")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
LO_ROOT="${H%/codex/guard-canary.sh}"
rm -f "${LO_ROOT}/hooks"
mkdir_checked "${LO_ROOT}/hooks/scripts"
cp -R "${FLOW_DIR}/hooks/scripts/." "${LO_ROOT}/hooks/scripts/" || exit 1
cat > "${LO_ROOT}/hooks/scripts/check-sensitive-files.sh" <<'SWAP_GUARD'
#!/bin/sh
t=$(cat | jq -r '.tool_input.command' | sed -n 's|^\*\*\* Add File: ||p' | sed -n 1p)
p=$(dirname "$t")
case "${p##*/}" in
    cenci-guard-canary-????????????????) ;;
    *) exit 0 ;;
esac
[ -d "$p" ] && [ ! -L "$p" ] || exit 0
mv "$p" "$p.real" && ln -s "$p.real" "$p"
exit 0
SWAP_GUARD
chmod +x "${LO_ROOT}/hooks/scripts/check-sensitive-files.sh" || exit 1
TMP_BASE_CANON="$(cd "${TMP_BASE}" && pwd -P)" || { echo "FAIL: test setup: canonical TMP_BASE" >&2; exit 1; }
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
LO_LINK=""
for e in "${TMP_BASE_CANON}"/cenci-guard-canary-*; do
    [[ -L "${e}" ]] && LO_LINK="${e}"
done
if [[ -n "${LO_LINK}" ]]; then
    pass
else
    fail "leftover plan non-vacuity: the fake guard must have swapped the run dir for a symlink in ${TMP_BASE_CANON} (stdout: $(head -c 300 "${OUT}"))"
fi
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}" && [[ "$(probe_count "${OUT}")" -eq 0 ]]; then
    pass
else
    fail "a plan refused after the run dir was swapped must still refuse with no PROBE line (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if [[ -n "${LO_LINK}" ]] && grep -qxF "LEFTOVER: ${LO_LINK}" "${OUT}"; then
    pass
else
    fail "a refused plan that cannot remove its run dir must print 'LEFTOVER: <run dir>' with the exact path ${LO_LINK:-<none found>} (stdout: $(head -c 500 "${OUT}"))"
fi
if [[ -n "${LO_LINK}" ]]; then
    rm -rf "${LO_LINK}.real"
    rm -f "${LO_LINK}"
fi
if [[ "$(count_run_dirs_in "${TMP_BASE}")" -eq "${BASELINE_RUN_DIRS}" ]]; then
    pass
else
    fail "leftover plan: test cleanup left entries in ${TMP_BASE}"
fi

echo "case: an unconfigured repo (guards are a no-op there) refuses to plan (P3)"
UNCONF="${TEST_ROOT}/unconfigured"
mkdir_checked "${UNCONF}"
git -C "${UNCONF}" init -q || exit 1
run_helper "${CANARY}" "${UNCONF}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "unconfigured repo" "the probe self-check failed against the real guard scripts"

echo "case: outside any git repository refuses to plan (P3)"
run_helper "${CANARY}" "${TEST_ROOT}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "no git repository" "not inside a git repository"

# ── P4: TMPDIR inside the main worktree ─────────────────────────────────
echo "case: a TMPDIR inside the main worktree is never used for the run dir (P4)"
mkdir_checked "${REPO}/inside-tmp"
plan_in "${REPO}" "TMPDIR=${REPO}/inside-tmp"
INSIDE="${OUT}"
RUN_DIR_IN="$(kv "${INSIDE}" CANARY_RUN_DIR)"
SYS_TMP="$(cd /tmp && pwd -P)" || { echo "FAIL: test setup: cannot resolve /tmp" >&2; exit 1; }
# /tmp is available and outside the main worktree here, so the helper must fall
# back to it and say so; the run dir may land nowhere else.
if [[ "${R_RC}" -eq 0 && -n "${RUN_DIR_IN}" && "${RUN_DIR_IN%/*}" == "${SYS_TMP}" ]]; then
    pass
else
    fail "a TMPDIR inside the repo must fall back to ${SYS_TMP} (rc=${R_RC}, run dir '${RUN_DIR_IN}'): $(head -c 300 "${INSIDE}")"
fi
if grep -qxF "NOTE: TMPDIR (${REPO}/inside-tmp) is unusable for the run dir (relative, missing, or inside the main worktree); using ${SYS_TMP}." "${INSIDE}"; then
    pass
else
    fail "the fallback must be announced with the NOTE line naming the skipped TMPDIR and ${SYS_TMP}: $(head -c 400 "${INSIDE}")"
fi
if [[ "$(count_run_dirs_in "${REPO}/inside-tmp")" -eq 0 && "$(count_run_dirs_in "${REPO}")" -eq 0 ]]; then
    pass
else
    fail "no run dir may be created inside the main worktree"
fi

# ── P5: COMMAND lines, instructions, receipts before the plan is ok (#1411) ──
echo "case: plan prints one single-quoted COMMAND line per shell probe and none for apply_patch probes (P5)"
N_SHELL=0
for n in 1 2 3 4; do
    t="$(probe_field "${PLAN1}" "${n}" 6)"
    if [[ "$(probe_field "${PLAN1}" "${n}" 3)" == shell ]]; then
        N_SHELL=$((N_SHELL + 1))
        if grep -qxF "COMMAND${TAB}${n}${TAB}printf x > '${t}'" "${PLAN1}"; then
            pass
        else
            fail "shell probe ${n} needs the line 'COMMAND<TAB>${n}<TAB>printf x > '<target>'' (got: $(grep "^COMMAND${TAB}${n}${TAB}" "${PLAN1}"))"
        fi
    elif grep -q "^COMMAND${TAB}${n}${TAB}" "${PLAN1}"; then
        fail "apply_patch probe ${n} must get no COMMAND line"
    else
        pass
    fi
done
if [[ "${N_SHELL}" -eq 2 && "$(grep -c "^COMMAND${TAB}" "${PLAN1}")" -eq 2 ]]; then
    pass
else
    fail "expected exactly two COMMAND lines for the two shell probes (shell probes ${N_SHELL}, COMMAND lines $(grep -c "^COMMAND${TAB}" "${PLAN1}"))"
fi

echo "case: the INSTRUCTION lines say to run COMMAND verbatim and list all four verdict tokens (P5)"
if grep '^INSTRUCTION:' "${PLAN1}" | grep -F 'COMMAND' | grep -qF 'verbatim'; then
    pass
else
    fail "an INSTRUCTION line must tell the session to run the COMMAND line verbatim: $(grep '^INSTRUCTION:' "${PLAN1}")"
fi
if grep '^INSTRUCTION:' "${PLAN1}" | grep -qF '=<denied|not-denied|blocked|not-attempted>'; then
    pass
else
    fail "the check INSTRUCTION must offer all four tokens <denied|not-denied|blocked|not-attempted>: $(grep '^INSTRUCTION:' "${PLAN1}")"
fi
for tok in blocked not-attempted not-denied; do
    if grep '^INSTRUCTION: judge' "${PLAN1}" | grep -qF -- "${tok} "; then
        pass
    else
        fail "the judging INSTRUCTION must say when to report ${tok}: $(grep '^INSTRUCTION: judge' "${PLAN1}")"
    fi
done

echo "case: a main-worktree path with a single quote is quoted as '\\'' in COMMAND, and the command writes exactly its target (P5)"
QUOTE_REPO="${TEST_ROOT}/quote'repo dir"
make_fixture_repo "${QUOTE_REPO}"
plan_in "${QUOTE_REPO}"
QUOTE_PLAN="${OUT}"
QUOTE_RUN="$(kv "${QUOTE_PLAN}" CANARY_RUN_DIR)"
if [[ "${R_RC}" -eq 0 && -n "${QUOTE_RUN}" ]]; then
    pass
else
    fail "plan in a repository whose path holds a single quote must succeed (rc=${R_RC}): $(head -c 500 "${QUOTE_PLAN}")"
fi
QUOTE_SHELL=0
for n in 1 2 3 4; do
    [[ "$(probe_field "${QUOTE_PLAN}" "${n}" 3)" == shell ]] || continue
    t="$(probe_field "${QUOTE_PLAN}" "${n}" 6)"
    [[ "$(probe_field "${QUOTE_PLAN}" "${n}" 4)" == guard-main-worktree ]] || continue
    QUOTE_SHELL=$((QUOTE_SHELL + 1))
    want_q="$(printf '%s' "${t}" | sed "s/'/'\\\\''/g")"
    want_cmd="printf x > '${want_q}'"
    if [[ "$(command_of "${QUOTE_PLAN}" "${n}")" == "${want_cmd}" && "${want_cmd}" == *"'\\''"* ]]; then
        pass
    else
        fail "probe ${n}: COMMAND must single-quote the target with an embedded ' written as '\\'' (want: ${want_cmd}; got: $(command_of "${QUOTE_PLAN}" "${n}"))"
    fi
    QUOTE_CWD="${TEST_ROOT}/quote-cwd"
    mkdir_checked "${QUOTE_CWD}"
    before_root="$(tree_of "${QUOTE_REPO}")"
    (cd "${QUOTE_CWD}" && "${BASH_BIN}" -c "$(command_of "${QUOTE_PLAN}" "${n}")")
    if [[ -f "${t}" && "$(< "${t}")" == x && -z "$(tree_of "${QUOTE_CWD}")" ]] \
        && [[ "$(tree_of "${QUOTE_REPO}")" == "$(printf '%s\n%s' "${before_root}" "${t}" | LC_ALL=C sort | sed '/^$/d')" ]]; then
        pass
    else
        fail "probe ${n}: running COMMAND must create exactly ${t} and nothing else (cwd: $(tree_of "${QUOTE_CWD}"))"
    fi
    rm -f "${t}"
done
# The filters above must leave at least one shell main-worktree probe, or every
# quoting assertion in the loop was skipped.
[[ "${QUOTE_SHELL}" -ge 1 ]] || fail "quoted repo path: expected at least one shell guard-main-worktree probe, got ${QUOTE_SHELL}"
[[ -n "${QUOTE_RUN}" ]] && rm -rf "${QUOTE_RUN}"

echo "case: a repository path with a space, ', \$, ; and a backtick either plans a COMMAND that writes only its target, or refuses cleanly (P5)"
META_REPO="${TEST_ROOT}/meta dir'q\$x;y\`z/repo"
make_fixture_repo "${META_REPO}"
META_CWD="${TEST_ROOT}/meta-cwd"
mkdir_checked "${META_CWD}"
plan_in "${META_REPO}"
META_PLAN="${OUT}"
META_RUN="$(kv "${META_PLAN}" CANARY_RUN_DIR)"
if [[ "${R_RC}" -eq 0 ]] && grep -qxF 'CANARY_PLAN=ok' "${META_PLAN}"; then
    echo "  (metacharacter repo: plan succeeded; checking each COMMAND)"
    META_SHELL=0
    for n in 1 2 3 4; do
        [[ "$(probe_field "${META_PLAN}" "${n}" 3)" == shell ]] || continue
        META_SHELL=$((META_SHELL + 1))
        t="$(probe_field "${META_PLAN}" "${n}" 6)"
        before_root="$(tree_of "${META_REPO}")"
        before_run="$(tree_of "${META_RUN}")"
        (cd "${META_CWD}" && "${BASH_BIN}" -c "$(command_of "${META_PLAN}" "${n}")")
        root_now="$(tree_of "${META_REPO}")"
        run_now="$(tree_of "${META_RUN}")"
        case "${t}" in
            "${META_RUN}"/*) want_root="${before_root}" want_run="$(printf '%s\n%s' "${before_run}" "${t}" | LC_ALL=C sort | sed '/^$/d')" ;;
            *) want_root="$(printf '%s\n%s' "${before_root}" "${t}" | LC_ALL=C sort | sed '/^$/d')" want_run="${before_run}" ;;
        esac
        if [[ -f "${t}" && "${root_now}" == "${want_root}" && "${run_now}" == "${want_run}" && -z "$(tree_of "${META_CWD}")" ]]; then
            pass
        else
            fail "metacharacter repo: COMMAND for probe ${n} must create exactly ${t} and no other entry (root: ${root_now}; run: ${run_now}; cwd: $(tree_of "${META_CWD}"))"
        fi
        rm -f "${t}"
    done
    [[ "${META_SHELL}" -eq 2 ]] || fail "metacharacter repo: expected two shell probes, got ${META_SHELL}"
    rm -rf "${META_RUN}"
else
    echo "  (metacharacter repo: plan refused, $(grep -m 1 -F 'Guard canary refused:' "${META_PLAN}" | cut -c 1-160); checking the refusal left nothing)"
    if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${META_PLAN}" && [[ "$(probe_count "${META_PLAN}")" -eq 0 ]]; then
        pass
    else
        fail "metacharacter repo: a plan that is not ok must refuse with exit 1 and no PROBE line (rc=${R_RC}): $(head -c 400 "${META_PLAN}")"
    fi
    if [[ "$(count_run_dirs_in "${TMP_BASE}")" -eq "${BASELINE_RUN_DIRS}" && "$(count_run_dirs_in "${META_REPO}")" -eq 0 && -z "$(tree_of "${META_CWD}")" ]]; then
        pass
    else
        fail "metacharacter repo: a refused plan must leave no run dir and no probe file"
    fi
fi

echo "case: a receipt already in the run dir before CANARY_PLAN=ok refuses the plan and removes the run dir (P5)"
# A fake check-sensitive-files guard plants a receipt in the run dir during the
# self-check, then hands the call to the real guard, so the self-check itself
# still passes. No real guard can do this (probes.tsv does not exist yet).
H="$(make_plugin planted-receipt "")" || { echo "FAIL: test setup: plugin copy" >&2; exit 1; }
PR_ROOT="${H%/codex/guard-canary.sh}"
rm -f "${PR_ROOT}/hooks"
mkdir_checked "${PR_ROOT}/hooks/scripts"
cp -R "${FLOW_DIR}/hooks/scripts/." "${PR_ROOT}/hooks/scripts/" || exit 1
mv "${PR_ROOT}/hooks/scripts/check-sensitive-files.sh" "${PR_ROOT}/hooks/scripts/real-check-sensitive-files.sh" || exit 1
cat > "${PR_ROOT}/hooks/scripts/check-sensitive-files.sh" <<'PLANT_GUARD'
#!/bin/sh
in=$(cat)
t=$(printf '%s' "$in" | jq -r '.tool_input.command' | sed -n -e 's|^\*\*\* Add File: ||p' -e "s|^printf x > '\(.*\)'\$|\1|p" | sed -n 1p)
p=${t%/*}
case "${p##*/}" in
    cenci-guard-canary-????????????????) [ -d "$p" ] && : > "$p/receipt-check-sensitive-files-1.planted" ;;
esac
printf '%s' "$in" | "${0%/*}/real-check-sensitive-files.sh"
PLANT_GUARD
chmod +x "${PR_ROOT}/hooks/scripts/check-sensitive-files.sh" || exit 1
run_helper "${H}" "${REPO}" "${PATH}" "TMPDIR=${TMP_BASE}" -- plan
assert_plan_refused "receipt before the plan is ok" "already in the run dir before the plan is complete"
if grep -qxF 'CANARY_PLAN=ok' "${OUT}"; then
    fail "receipt before the plan is ok: CANARY_PLAN=ok must never print"
else
    pass
fi

echo "case: a main-worktree path with a control character is refused by plan, leaving no probe file and no run dir (P5)"
# Outside /tmp like every fixture (guard-main-worktree allowlists /tmp/*), so the
# only possible reason to refuse is the control character itself.
for ctl_name in "ctl${TAB}repo" "ctl"$'\n'"repo"; do
    CTL_REPO="${TEST_ROOT}/${ctl_name}"
    make_fixture_repo "${CTL_REPO}"
    ctl_label="control-character path (${ctl_name//[[:cntrl:]]/<ctl>})"
    ctl_before="$(tree_of "${CTL_REPO}")"
    plan_in "${CTL_REPO}"
    assert_plan_refused "${ctl_label}" "the main worktree path contains a control character"
    if grep -qxF 'CANARY_PLAN=ok' "${OUT}"; then
        fail "${ctl_label}: CANARY_PLAN=ok must never print"
    else
        pass
    fi
    if [[ "$(tree_of "${CTL_REPO}")" == "${ctl_before}" && "$(count_run_dirs_in "${CTL_REPO}")" -eq 0 ]]; then
        pass
    else
        fail "${ctl_label}: a refused plan must leave no probe file in the main worktree"
    fi
done

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

# fresh_denied_plan [cwd] -- fresh_plan, then every probe denied by the real
# guards (receipts staged), so a case that expects a refusal refuses for its own
# reason and not merely because no receipt exists.
fresh_denied_plan() {
    fresh_plan "${1:-${REPO}}"
    deny_probes "${CUR_PLAN}" "${1:-${REPO}}"
}

echo "case: all probes denied by the real guards (receipts present) and nothing written -> active, run dir removed (C1)"
fresh_plan
deny_probes "${CUR_PLAN}" "${REPO}"
if [[ "$(receipts_in "${CUR_DIR}")" -eq 4 ]]; then
    pass
else
    fail "deny_probes non-vacuity: the four real guard denials must leave four receipts, found $(receipts_in "${CUR_DIR}")"
fi
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

echo "case: a written target is removed, reported, and refuses even when claimed denied and its receipt is present (C1)"
fresh_plan
# Every probe gets its real receipt first: WRITTEN must still win.
deny_probes "${CUR_PLAN}" "${REPO}"
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
for n in 1 2; do
    if grep -E "^PROBE_RESULT: ${n} " "${OUT}" | grep -qF 'WRITTEN' && ! grep -E "^PROBE_RESULT: ${n} " "${OUT}" | grep -qF 'denied by'; then
        pass
    else
        fail "written probe ${n} with a receipt: WRITTEN must win over the receipt: $(grep -E "^PROBE_RESULT: ${n} " "${OUT}")"
    fi
done
if [[ ! -e "${CUR_DIR}" ]]; then
    pass
else
    fail "a refused check must remove the run dir and its receipts (${CUR_DIR})"
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

# probe_label <plan-out> <n> -- "<n> (<tool>, <guard>, group <group>)", the
# prefix of probe <n>'s PROBE_RESULT line.
probe_label() {
    printf '%s (%s, %s, group %s)' "$2" "$(probe_field "$1" "$2" 3)" "$(probe_field "$1" "$2" 4)" "$(probe_field "$1" "$2" 5)"
}

echo "case: no guard message and nothing written is inconclusive, not 'written', with the line unchanged (C2)"
fresh_plan
deny_probes "${CUR_PLAN}" "${REPO}" 1 2 4
check_run "${CUR_DIR}" 1=denied 2=denied 3=not-denied 4=denied
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
    pass
else
    fail "a not-denied probe must refuse (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if grep -qxF "PROBE_RESULT: $(probe_label "${CUR_PLAN}" 3): NO GUARD MESSAGE -- not written, but not denied by the guard either (inconclusive)" "${OUT}"; then
    pass
else
    fail "an unwritten not-denied probe with no receipt keeps the exact NO GUARD MESSAGE line: $(grep '^PROBE_RESULT: 3 ' "${OUT}")"
fi
if grep -qE '^(REMOVED|LEFTOVER)' "${OUT}"; then
    fail "nothing was written, so nothing may be reported removed or left over"
else
    pass
fi

echo "case: a missing probe result refuses with the NO RESULT line unchanged (C1)"
fresh_plan
deny_probes "${CUR_PLAN}" "${REPO}" 1 2 3
check_run "${CUR_DIR}" 1=denied 2=denied 3=denied
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
    pass
else
    fail "a probe with no reported result must refuse (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if grep -qxF "PROBE_RESULT: $(probe_label "${CUR_PLAN}" 4): NO RESULT -- the session reported nothing for this probe (inconclusive)" "${OUT}"; then
    pass
else
    fail "a probe with no reported result keeps the exact NO RESULT line: $(grep '^PROBE_RESULT: 4 ' "${OUT}")"
fi

# ── R: receipts and the new verdicts (#1411) ────────────────────────────
# assert_refused_exit1 <label> -- the last check refused with exit 1.
assert_refused_exit1() {
    if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}" && ! grep -qxF 'CANARY_STATUS=active' "${OUT}"; then
        pass
    else
        fail "$1: check must print CANARY_STATUS=refused and exit 1 (rc=${R_RC}): $(head -c 500 "${OUT}")"
    fi
}

# result_line <n> -- probe <n>'s PROBE_RESULT line from the last check.
result_line() {
    grep -E "^PROBE_RESULT: $1 " "${OUT}" | sed -n 1p
}

echo "case: every probe reported denied but no guard ever ran (no receipt) refuses with REPORTED DENIED WITHOUT GUARD RECEIPT (R)"
fresh_plan
check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
assert_refused_exit1 "denied without receipts"
for n in 1 2 3 4; do
    if [[ "$(result_line "${n}")" == *"REPORTED DENIED WITHOUT GUARD RECEIPT"* ]]; then
        pass
    else
        fail "probe ${n} reported denied with no receipt must say REPORTED DENIED WITHOUT GUARD RECEIPT: $(result_line "${n}")"
    fi
done
if grep -qE '^PROBE_RESULT: .*denied by ' "${OUT}"; then
    fail "denied without receipts: no probe may be accepted as 'denied by <guard>'"
else
    pass
fi

echo "case: blocked and not-attempted are accepted tokens with their own result lines, and refuse (R)"
fresh_plan
deny_probes "${CUR_PLAN}" "${REPO}" 3 4
check_run "${CUR_DIR}" 1=blocked 2=not-attempted 3=denied 4=denied
assert_refused_exit1 "blocked / not-attempted"
if [[ "$(result_line 1)" == *"BLOCKED BEFORE THE GUARD (Codex sandbox or approval refusal)"* && "$(result_line 1)" != *RECEIPT* ]]; then
    pass
else
    fail "1=blocked must print BLOCKED BEFORE THE GUARD (Codex sandbox or approval refusal): $(result_line 1)"
fi
if [[ "$(result_line 2)" == *"NOT ATTEMPTED"* && "$(result_line 2)" != *RECEIPT* ]]; then
    pass
else
    fail "2=not-attempted must print NOT ATTEMPTED: $(result_line 2)"
fi
for n in 3 4; do
    if [[ "$(result_line "${n}")" == *": denied by $(probe_field "${CUR_PLAN}" "${n}" 4)" ]]; then
        pass
    else
        fail "probe ${n} with a real receipt and denied must be accepted: $(result_line "${n}")"
    fi
done
if grep -q '^BAD_RESULT' "${OUT}"; then
    fail "blocked and not-attempted are valid tokens, never BAD_RESULT: $(grep '^BAD_RESULT' "${OUT}")"
else
    pass
fi

echo "case: a receipt with a non-denied verdict names both the receipt and the verdict, and refuses (R)"
fresh_plan
deny_probes "${CUR_PLAN}" "${REPO}"
check_run "${CUR_DIR}" 1=blocked 2=not-attempted 3=not-denied 4=denied
assert_refused_exit1 "receipt + non-denied verdict"
for spec in "1|blocked|BLOCKED BEFORE THE GUARD (Codex sandbox or approval refusal)" "2|not-attempted|NOT ATTEMPTED" "3|not-denied|NO GUARD MESSAGE"; do
    IFS='|' read -r n tok phrase <<< "${spec}"
    if [[ "$(result_line "${n}")" == *"GUARD RECEIPT PRESENT BUT REPORTED ${tok} -- ${phrase}"* ]]; then
        pass
    else
        fail "probe ${n} with a receipt but ${tok}: the line must name both the receipt and the verdict: $(result_line "${n}")"
    fi
done
if [[ ! -e "${CUR_DIR}" ]]; then
    pass
else
    fail "a refused check must remove the run dir with its receipts (${CUR_DIR})"
fi

echo "case: the refused explanation names all six outcomes (R)"
EXPLAIN="$(grep '^Reading the probe results:' "${OUT}")"
for outcome in "WRITTEN" "REPORTED DENIED WITHOUT GUARD RECEIPT" "BLOCKED BEFORE THE GUARD" "NOT ATTEMPTED" "NO GUARD MESSAGE" "NO RESULT"; do
    if [[ "${EXPLAIN}" == *"${outcome}"* ]]; then
        pass
    else
        fail "the refused explanation must name ${outcome}: ${EXPLAIN}"
    fi
done

# bad_receipt_case <label> <probe n> <receipt-body-or-@SYMLINK@> <reason-fragment>
# -- every probe but <n> gets its real receipt; <n> gets only a hand-written one,
# which check must reject with a BAD_RECEIPT line, and the canary refuses.
bad_receipt_case() {
    local label="$1" n="$2" body="$3" reason="$4" owner="${5:-}" hex guard elsewhere="" name other tool badtool m saved_bash
    fresh_plan
    hex="${CUR_DIR##*cenci-guard-canary-}"
    guard="$(probe_field "${CUR_PLAN}" "${n}" 4)"
    local -a others=()
    for m in 1 2 3 4; do
        [[ "${m}" == "${n}" ]] || others+=("${m}")
    done
    deny_probes "${CUR_PLAN}" "${REPO}" "${others[@]}"
    name="receipt-${guard}-${n}.manual"
    if [[ "${guard}" == check-sensitive-files ]]; then other=guard-main-worktree; else other=check-sensitive-files; fi
    if [[ "$(probe_field "${CUR_PLAN}" "${n}" 3)" == shell ]]; then tool=Bash badtool=apply_patch; else tool=apply_patch badtool=Bash; fi
    body="${body//@HEX@/${hex}}"
    body="${body//@GUARD@/${guard}}"
    body="${body//@OTHER@/${other}}"
    body="${body//@TOOL@/${tool}}"
    body="${body//@BADTOOL@/${badtool}}"
    if [[ "${body}" == @SYMLINK@* ]]; then
        elsewhere="${TEST_ROOT}/receipt-elsewhere-${n}"
        printf '%s\n' "${body#@SYMLINK@}" > "${elsewhere}" || exit 1
        ln -s "${elsewhere}" "${CUR_DIR}/${name}" || exit 1
    elif [[ "${body}" == @DIR@ ]]; then
        mkdir "${CUR_DIR}/${name}" || exit 1
    elif [[ "${body}" == @FIFO@ ]]; then
        mkfifo "${CUR_DIR}/${name}" || exit 1
    else
        printf '%s' "${body}" > "${CUR_DIR}/${name}" || exit 1
    fi
    if [[ -n "${owner}" ]]; then
        chown "${owner}" "${CUR_DIR}/${name}" || exit 1
    fi
    saved_bash="${BASH_BIN}"
    # A FIFO receipt must be refused without ever being read; a read would block
    # forever, so run that check under a wall-clock limit (rc 124 fails the case).
    if [[ "${body}" == @FIFO@ && -x "${TIMEOUT_BASH:-}" ]]; then
        BASH_BIN="${TIMEOUT_BASH}"
    fi
    check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
    BASH_BIN="${saved_bash}"
    assert_refused_exit1 "${label}"
    if grep -F "BAD_RECEIPT: ${name}: " "${OUT}" | grep -qF -- "${reason}"; then
        pass
    else
        fail "${label}: check must print 'BAD_RECEIPT: ${name}: ...${reason}...' (got: $(grep '^BAD_RECEIPT' "${OUT}"))"
    fi
    if [[ "$(result_line "${n}")" == *"REPORTED DENIED WITHOUT GUARD RECEIPT"* ]]; then
        pass
    else
        fail "${label}: a bad receipt must not count, so probe ${n} is denied without a receipt: $(result_line "${n}")"
    fi
    if [[ -n "${elsewhere:-}" ]]; then
        if [[ -f "${elsewhere}" && "$(< "${elsewhere}")" == "${body#@SYMLINK@}" ]]; then
            pass
        else
            fail "${label}: check must never follow or touch a symlinked receipt's destination"
        fi
        rm -f "${elsewhere}"
    fi
}

T_=$'\t'
bad_receipt_case "wrong hex" 4 "V1${T_}@GUARD@${T_}ffffffffffffffff${T_}4${T_}@TOOL@"$'\n' "another run"
bad_receipt_case "wrong guard" 4 "V1${T_}@OTHER@${T_}@HEX@${T_}4${T_}@TOOL@"$'\n' "names guard"
bad_receipt_case "wrong tool" 4 "V1${T_}@GUARD@${T_}@HEX@${T_}4${T_}@BADTOOL@"$'\n' "names tool"
bad_receipt_case "probe number not in the record" 4 "V1${T_}@GUARD@${T_}@HEX@${T_}9${T_}@TOOL@"$'\n' "not in the probe record"
bad_receipt_case "symlinked receipt" 4 "@SYMLINK@V1${T_}@GUARD@${T_}@HEX@${T_}4${T_}@TOOL@" "symlink"
bad_receipt_case "empty receipt (a write that failed after mktemp)" 4 "" "exactly one line"
bad_receipt_case "two-line receipt" 4 "V1${T_}@GUARD@${T_}@HEX@${T_}4${T_}@TOOL@"$'\n'"V1${T_}@GUARD@${T_}@HEX@${T_}4${T_}@TOOL@"$'\n' "exactly one line"
bad_receipt_case "oversized receipt" 4 "V1${T_}@GUARD@${T_}@HEX@${T_}4${T_}@TOOL@$(printf '%*s' 600 '')"$'\n' "too large to be a receipt"
# A receipt-* entry that is not a regular file is refused with BAD_RECEIPT and is
# never read: a directory, and a FIFO (whose read would block forever).
bad_receipt_case "directory named like a receipt" 4 "@DIR@" "is not a regular file"
if command -v mkfifo >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1; then
    TIMEOUT_BASH="${TEST_ROOT}/timeout-bash"
    printf '#!/bin/sh\nexec timeout 20 "%s" "$@"\n' "${BASH_BIN}" > "${TIMEOUT_BASH}" || exit 1
    chmod +x "${TIMEOUT_BASH}" || exit 1
    bad_receipt_case "FIFO named like a receipt (never read, never hangs)" 4 "@FIFO@" "is not a regular file"
else
    echo "SKIP: FIFO receipt case needs mkfifo and timeout on PATH"
fi

# A receipt owned by another user is refused (-O). Ownership cannot be faked
# without root (chown to another uid), so this runs only as root; otherwise the
# -O branch of do_check is NOT exercised by this suite.
if [[ "$(id -u)" -eq 0 ]] && command -v chown >/dev/null 2>&1; then
    bad_receipt_case "receipt owned by another user" 4 "V1${T_}@GUARD@${T_}@HEX@${T_}4${T_}@TOOL@"$'\n' "not owned by the current user" 65534
else
    echo "SKIP: receipt-ownership (-O) case needs root to chown a receipt to another uid (running as uid $(id -u))"
fi

# The positive control for the parser: the same hand-written line with the
# right fields IS accepted (so each negative above fails for its own field).
bad_receipt_control() {
    local hex guard tool
    fresh_plan
    hex="${CUR_DIR##*cenci-guard-canary-}"
    guard="$(probe_field "${CUR_PLAN}" 4 4)"
    if [[ "$(probe_field "${CUR_PLAN}" 4 3)" == shell ]]; then tool=Bash; else tool=apply_patch; fi
    deny_probes "${CUR_PLAN}" "${REPO}" 1 2 3
    printf 'V1\t%s\t%s\t4\t%s\n' "${guard}" "${hex}" "${tool}" > "${CUR_DIR}/receipt-${guard}-4.manual" || exit 1
    check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
    if [[ "${R_RC}" -eq 0 ]] && grep -qxF 'CANARY_STATUS=active' "${OUT}" && ! grep -q '^BAD_RECEIPT' "${OUT}"; then
        pass
    else
        fail "receipt parser control: a well-formed receipt line must be accepted (rc=${R_RC}): $(head -c 500 "${OUT}")"
    fi
}
bad_receipt_control

echo "case: an unrecognized result token refuses and removes nothing it should not (C1)"
fresh_denied_plan
check_run "${CUR_DIR}" 1=denied 2=denied 3=denied 4=maybe
if [[ "${R_RC}" -ne 0 ]] && ! grep -qxF 'CANARY_STATUS=active' "${OUT}" && grep -qF 'BAD_RESULT: unrecognized result token 4=maybe' "${OUT}"; then
    pass
else
    fail "an unrecognized result must never produce an active canary and must be a BAD_RESULT (rc=${R_RC}): $(head -c 300 "${OUT}")"
fi

echo "case: a removal that cannot happen is reported by exact path and still refuses (C1)"
# A directory at the main-worktree probe target: rm -f cannot remove it.
fresh_denied_plan
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
fresh_denied_plan
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

# ── C1: early refusals (untrusted record) ───────────────────────────────
# `check` refuses BEFORE its cleanup loop when the run dir argument or the probe
# record cannot be trusted. Nothing is removed on that path (the record is
# untrusted, so it must never steer a delete), which means canary files may
# still sit in the main worktree: the refusal has to say how to remove them by
# hand.

# rewrite_probe_target <run-dir> <n> <new-target> -- tamper with the record.
rewrite_probe_target() {
    awk -F'\t' -v OFS='\t' -v n="$2" -v t="$3" '$1 == "PROBE" && $2 == n { $6 = t } { print }' "$1/probes.tsv" > "$1/probes.new" \
        && mv "$1/probes.new" "$1/probes.tsv" \
        || { echo "FAIL: test setup: cannot rewrite the probe record in $1" >&2; exit 1; }
}

# assert_hint_glob <label> <hex> -- the refusal names the exact glob to delete by
# hand and says the .env probe lives in the run dir, and does not claim the
# canary cleaned up.
assert_hint_glob() {
    local label="$1" hex="$2"
    if grep -qF -- "${REPO}/cenci-guard-canary-${hex}-*.txt" "${OUT}"; then
        pass
    else
        fail "${label}: the refusal must name '${REPO}/cenci-guard-canary-${hex}-*.txt' for manual removal (stdout: $(head -c 500 "${OUT}"))"
    fi
    if grep -F '.env' "${OUT}" | grep -qF 'run dir'; then
        pass
    else
        fail "${label}: the refusal must say the .env probe lives in the run dir (stdout: $(head -c 500 "${OUT}"))"
    fi
    if grep -qxF 'CANARY_STATUS=refused' "${OUT}" && ! grep -qxF 'CANARY_STATUS=active' "${OUT}" && [[ "${R_RC}" -eq 1 ]]; then
        pass
    else
        fail "${label}: an early refusal must exit 1 with CANARY_STATUS=refused (rc=${R_RC})"
    fi
}

# assert_hint_generic <label> -- hex or main root undeterminable: look for
# cenci-guard-canary-* files in the main worktree, no invented glob.
assert_hint_generic() {
    local label="$1"
    if grep -qF 'cenci-guard-canary-*' "${OUT}" && grep -qiF 'main worktree' "${OUT}"; then
        pass
    else
        fail "${label}: the refusal must say to look for cenci-guard-canary-* files in the main worktree (stdout: $(head -c 500 "${OUT}"))"
    fi
    if grep -qF -- '-*.txt' "${OUT}"; then
        fail "${label}: with the hex id or main root undetermined the refusal must not invent a specific glob (stdout: $(head -c 500 "${OUT}"))"
    else
        pass
    fi
    if grep -qxF 'CANARY_STATUS=refused' "${OUT}" && ! grep -qxF 'CANARY_STATUS=active' "${OUT}" && [[ "${R_RC}" -eq 1 ]]; then
        pass
    else
        fail "${label}: an early refusal must exit 1 with CANARY_STATUS=refused (rc=${R_RC})"
    fi
}

VICTIM_BODY="victim-must-survive"

# tamper_case <label> <probe n> <victim-path> -- a record whose probe <n> target
# is <victim-path> (@RUN@ = the run dir, @HEX@ = its hex id). The victim must
# survive, so must an honest written probe (nothing is removed on this path),
# and the status is never active.
tamper_case() {
    local label="$1" n="$2" victim="$3" hex bystander
    fresh_plan
    hex="${CUR_DIR##*cenci-guard-canary-}"
    victim="${victim//@RUN@/${CUR_DIR}}"
    victim="${victim//@HEX@/${hex}}"
    bystander="$(probe_field "${CUR_PLAN}" 4 6)"
    mkdir_checked "${victim%/*}"
    printf '%s\n' "${VICTIM_BODY}" > "${victim}" || exit 1
    printf 'x\n' > "${bystander}" || exit 1
    rewrite_probe_target "${CUR_DIR}" "${n}" "${victim}"
    check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
    if [[ "$(< "${victim}")" == "${VICTIM_BODY}" ]]; then
        pass
    else
        fail "${label}: the victim file ${victim} must survive a tampered record"
    fi
    if [[ -f "${bystander}" ]]; then
        pass
    else
        fail "${label}: an early refusal must remove nothing, but the written probe ${bystander} is gone"
    fi
    if grep -qxF 'CANARY_STATUS=active' "${OUT}"; then
        fail "${label}: a tampered record must never produce an active canary"
    else
        pass
    fi
    if grep -F 'Guard canary refused:' "${OUT}" | grep -qF "probe ${n} has an unexpected target path"; then
        pass
    else
        fail "${label}: the refusal must say probe ${n} has an unexpected target path (stdout: $(head -c 400 "${OUT}"))"
    fi
    assert_hint_glob "${label}" "${hex}"
    rm -rf "${CUR_DIR}"
    rm -f "${victim}" "${bystander}"
}

echo "case: a tampered probes.tsv whose target is a victim outside the allowed shapes deletes nothing (C1)"
tamper_case "main-worktree probe -> unrelated file in the main worktree" 2 "${REPO}/victim-main.txt"
tamper_case "main-worktree probe -> right shape, wrong hex" 2 "${REPO}/cenci-guard-canary-ffffffffffffffff-2.txt"
tamper_case "main-worktree probe -> right hex, wrong probe number" 2 "${REPO}/cenci-guard-canary-@HEX@-3.txt"
tamper_case "main-worktree probe -> file outside the repo" 2 "${TEST_ROOT}/victim-outside.txt"
tamper_case "sensitive probe -> .env outside the run dir" 1 "${TEST_ROOT}/victim-outside.env"
tamper_case "sensitive probe -> .env in a subdirectory of the run dir" 1 "@RUN@/nested/victim.env"
tamper_case "sensitive probe -> .env reached through .." 1 "@RUN@/../victim-dotdot.env"
tamper_case "sensitive probe -> non-.env file in the run dir" 1 "@RUN@/victim.txt"

echo "case: a probe record that belongs to a different run refuses and deletes nothing (C1)"
fresh_plan
HEX_A="${CUR_DIR##*cenci-guard-canary-}"
BYSTANDER="$(probe_field "${CUR_PLAN}" 4 6)"
printf 'x\n' > "${BYSTANDER}" || exit 1
sed "s/^HEX${TAB}${HEX_A}\$/HEX${TAB}ffffffffffffffff/" "${CUR_DIR}/probes.tsv" > "${CUR_DIR}/probes.new" \
    && mv "${CUR_DIR}/probes.new" "${CUR_DIR}/probes.tsv" || exit 1
if grep -qxF "HEX${TAB}ffffffffffffffff" "${CUR_DIR}/probes.tsv"; then
    pass
else
    fail "hex mismatch non-vacuity: the record's HEX line must have been replaced"
fi
check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
if grep -F 'Guard canary refused:' "${OUT}" | grep -qF 'belongs to a different run' && ! grep -qxF 'CANARY_STATUS=active' "${OUT}"; then
    pass
else
    fail "a record whose hex differs from the run dir name must refuse as 'a different run' (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
if [[ -f "${BYSTANDER}" ]]; then
    pass
else
    fail "a record from a different run must not trigger any removal (${BYSTANDER} is gone)"
fi
assert_hint_glob "different-run record" "${HEX_A}"
rm -rf "${CUR_DIR}"
rm -f "${BYSTANDER}"

echo "case: a corrupt probe record, a non-canonical path and a trailing slash all name the manual cleanup glob and delete nothing (C1)"
fresh_plan
HEX_A="${CUR_DIR##*cenci-guard-canary-}"
BYSTANDER="$(probe_field "${CUR_PLAN}" 4 6)"
printf 'x\n' > "${BYSTANDER}" || exit 1
printf 'garbage\n' > "${CUR_DIR}/probes.tsv" || exit 1
check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
if grep -F 'Guard canary refused:' "${OUT}" | grep -qF 'probe record'; then
    pass
else
    fail "a corrupt record must refuse naming the probe record: $(head -c 400 "${OUT}")"
fi
assert_hint_glob "corrupt record" "${HEX_A}"
rm -rf "${CUR_DIR}"
rm -f "${BYSTANDER}"
fresh_plan
HEX_A="${CUR_DIR##*cenci-guard-canary-}"
BYSTANDER="$(probe_field "${CUR_PLAN}" 4 6)"
printf 'x\n' > "${BYSTANDER}" || exit 1
check_run "${TMP_BASE}/../tmp/${CUR_DIR##*/}" "${ALL_DENIED[@]}"
if grep -F 'Guard canary refused:' "${OUT}" | grep -qF 'not in canonical form'; then
    pass
else
    fail "a non-canonical run dir must refuse as 'not in canonical form': $(head -c 400 "${OUT}")"
fi
assert_hint_glob "non-canonical path" "${HEX_A}"
check_run "${CUR_DIR}/" "${ALL_DENIED[@]}"
assert_hint_glob "trailing slash" "${HEX_A}"
if [[ -f "${BYSTANDER}" && -d "${CUR_DIR}" ]]; then
    pass
else
    fail "early refusals must remove neither the probe ${BYSTANDER} nor the run dir ${CUR_DIR}"
fi
check_run "${TMP_BASE}/cenci-guard-canary-0123456789abcdef" "${ALL_DENIED[@]}"
assert_hint_glob "typo'd run dir that does not exist" "0123456789abcdef"
rm -rf "${CUR_DIR}"
rm -f "${BYSTANDER}"

echo "case: when the hex id or the main root cannot be determined the refusal says to look for cenci-guard-canary-* in the main worktree (C1)"
mkdir_checked "${REPO}/not-a-run-dir"
check_run "${REPO}/not-a-run-dir" "${ALL_DENIED[@]}"
assert_hint_generic "run dir without a hex id"
rm -rf "${REPO}/not-a-run-dir"
check_run "relative-run-dir" "${ALL_DENIED[@]}"
assert_hint_generic "relative run dir"
# A valid-looking name, but the session is not in a git repository, so the main
# root cannot be determined (the record is untrusted, so it is never asked).
run_helper "${CANARY}" "${TEST_ROOT}" "${PATH}" "TMPDIR=${TMP_BASE}" -- check "${TMP_BASE}/cenci-guard-canary-0123456789abcdef" "${ALL_DENIED[@]}"
assert_hint_generic "main root undeterminable"

echo "case: a repeated probe number and an unknown probe number refuse, but the cleanup still runs (C1)"
fresh_denied_plan
T2="$(probe_field "${CUR_PLAN}" 2 6)"
printf 'x\n' > "${T2}" || exit 1
check_run "${CUR_DIR}" 1=denied 1=denied 2=denied 3=denied 4=denied
if [[ "${R_RC}" -eq 1 ]] && ! grep -qxF 'CANARY_STATUS=active' "${OUT}" && grep -qF 'BAD_RESULT: unrecognized result token 1=denied (repeated probe number)' "${OUT}"; then
    pass
else
    fail "a repeated probe number must refuse and be reported as BAD_RESULT (rc=${R_RC}): $(head -c 500 "${OUT}")"
fi
if [[ ! -e "${T2}" ]] && grep -qxF "REMOVED: ${T2}" "${OUT}"; then
    pass
else
    fail "the cleanup must still remove the written probe on a repeated-token refusal"
fi
fresh_denied_plan
T2="$(probe_field "${CUR_PLAN}" 2 6)"
printf 'x\n' > "${T2}" || exit 1
check_run "${CUR_DIR}" 1=denied 2=denied 3=denied 4=denied 5=denied
if [[ "${R_RC}" -eq 1 ]] && ! grep -qxF 'CANARY_STATUS=active' "${OUT}" && grep -qF 'BAD_RESULT: unrecognized result token 5 (no such probe)' "${OUT}"; then
    pass
else
    fail "an unknown probe number must refuse and be reported as BAD_RESULT (rc=${R_RC}): $(head -c 500 "${OUT}")"
fi
if [[ ! -e "${T2}" ]] && grep -qxF "REMOVED: ${T2}" "${OUT}"; then
    pass
else
    fail "the cleanup must still remove the written probe on an unknown-token refusal"
fi
# The first verdict for a probe number wins: a later denied cannot overwrite it.
fresh_denied_plan
check_run "${CUR_DIR}" 1=not-denied 1=denied 2=denied 3=denied 4=denied
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}"; then
    pass
else
    fail "a repeated number must refuse (rc=${R_RC}): $(head -c 400 "${OUT}")"
fi
rm -rf "${CUR_DIR}"

echo "case: a symlink at a probe target is removed as a symlink and never followed (C1)"
fresh_denied_plan
T2="$(probe_field "${CUR_PLAN}" 2 6)"
DANGLING_DEST="${TEST_ROOT}/dangling-dest.txt"
rm -f "${DANGLING_DEST}"
ln -s "${DANGLING_DEST}" "${T2}" || exit 1
check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
if [[ "${R_RC}" -eq 1 ]] && grep -qxF 'CANARY_STATUS=refused' "${OUT}" && grep -qxF "REMOVED: ${T2}" "${OUT}" && [[ ! -L "${T2}" ]]; then
    pass
else
    fail "a dangling symlink at a probe target counts as written, is removed and refuses (rc=${R_RC}): $(head -c 500 "${OUT}")"
fi
if [[ ! -e "${DANGLING_DEST}" && ! -L "${DANGLING_DEST}" ]]; then
    pass
else
    fail "check must not create or touch what a dangling symlink points at"
fi
if grep -qE 'WRITTEN' "${OUT}" && ! grep -q '^LEFTOVER' "${OUT}"; then
    pass
else
    fail "a removable symlink is reported WRITTEN, not as a leftover: $(head -c 500 "${OUT}")"
fi
rm -rf "${CUR_DIR}"
fresh_denied_plan
T2="$(probe_field "${CUR_PLAN}" 2 6)"
LINK_VICTIM="${TEST_ROOT}/link-victim.txt"
printf '%s\n' "${VICTIM_BODY}" > "${LINK_VICTIM}" || exit 1
ln -s "${LINK_VICTIM}" "${T2}" || exit 1
check_run "${CUR_DIR}" "${ALL_DENIED[@]}"
if [[ ! -L "${T2}" && "$(< "${LINK_VICTIM}")" == "${VICTIM_BODY}" ]]; then
    pass
else
    fail "a symlink at a probe target must be removed without touching its destination (${LINK_VICTIM})"
fi
rm -rf "${CUR_DIR}"
rm -f "${LINK_VICTIM}"

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
        deny_probes "${plan_out}" "${REPO}"
        run_helper "${helper}" "${REPO}" "${PATH}" "${envs[@]}" -- check "${dir}" "${ALL_DENIED[@]}"
        if ! grep -qxF 'CANARY_STATUS=active' "${OUT}"; then
            fail "${label} (${mode}): with every probe denied by the real guards the canary must be active: $(head -c 400 "${OUT}")"
        fi
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
