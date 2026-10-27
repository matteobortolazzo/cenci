#!/usr/bin/env bash
# native-workflows.gate.test.sh -- deterministic, CI-safe coverage for the opt-in
# native Codex workflow runner (native-workflows.test.sh, #1236) and the way it
# reuses native-hooks-lib.sh.
#
# The runner needs a real, authenticated Codex and the installed cenci plugin, so
# it is skipped by default and judged by the maintainer's run. What CAN be pinned
# without Codex is everything that decides whether it may run at all, how it
# handles the trust-bypass flag, and whether its judge catches the effects it
# exists to catch:
#
#   G1  every "not run" path prints `not run: <reason>`, exits 0, never says
#       "passed", and never launches `codex exec`
#         (env gate unset or 0, codex absent, version below 0.144.1, an
#          unparseable version, login failing)
#   G2  accepted versions reach `codex exec`
#   G3  the trust-bypass flag is the library's, unchanged: host mode never passes
#       it (not even with a spoofed CENCI_SANDBOX=1), sandbox mode does, and the
#       runner neither redefines the helpers nor reads a marker override
#   G4  the judge, against a stub `codex` that emulates what a correct run and a
#       set of misbehaving runs do on the fixture (a clean ci-repair + restart +
#       headless attention run is OK; a no-op codex proves nothing; each
#       forbidden effect turns exactly its row to FAIL; RESULT: OK needs the five
#       positive rows; a gate edited by the agent is never executed; a blind or
#       unreadable push log, an unreadable call log and an unreadable launch
#       repository are inconclusive; attached-flag gh writes are refused; the PR
#       number and attempt count must be in context, not a digit of a hash)
#   G5  a user name that survives sanitization makes the runner refuse to print
#
#   G6  `codex exec` runs without GitHub credentials and with an unreachable gh
#       host; a failing, unstartable or timed-out `codex exec` names its rc and
#       sanitized first stderr line in the rows
#
# A stub `codex` first on PATH records its argv and answers `--version` and
# `login status` from the environment; nothing here touches a real Codex.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="${SCRIPT_DIR}/native-workflows.test.sh"
LIB="${SCRIPT_DIR}/native-hooks-lib.sh"
BYPASS_FLAG="--dangerously-bypass-hook-trust"
APPROVAL_BYPASS_FLAG="--dangerously-bypass-approvals-and-sandbox"
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

echo "native-workflows.gate.test.sh"

# ── Setup ───────────────────────────────────────────────────────────────
# Not under /tmp (the guards the workflows run under allowlist /tmp/*); checked
# mktemp with trap cleanup.
TEST_ROOT="$(mktemp -d /var/tmp/native-workflows-gate-test.XXXXXX)" || {
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
for tool in git jq; do
    command -v "${tool}" >/dev/null 2>&1 || {
        echo "FAIL: test setup: ${tool} not found on PATH" >&2
        exit 1
    }
done

FLOW_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)" || {
    echo "FAIL: test setup: cannot resolve the flow directory" >&2
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

# A missing runner is one named failure here, and every case below then fails by
# its own name (running a missing file exits 127) instead of the suite stopping
# at the first one.
RUNNER_PRESENT=1
if [[ ! -f "${RUNNER}" ]]; then
    RUNNER_PRESENT=0
    fail "runner not found: ${RUNNER}"
fi
if [[ ! -f "${LIB}" ]]; then
    fail "helper library not found: ${LIB}"
    finish
fi

# The stub codex: record the argv of EVERY invocation (one line each), answer
# --version / login status from the environment and succeed at everything else.
# `codex exec` does nothing unless STUB_EMULATOR names a script, which is then
# run with the session directory (-C) and the prompt (the last argument).
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
        # The environment `codex exec` was given: the runner must have removed the
        # GitHub credentials and pointed gh at an unreachable host.
        {
            printf 'GH_TOKEN=%s\n' "${GH_TOKEN-UNSET}"
            printf 'GITHUB_TOKEN=%s\n' "${GITHUB_TOKEN-UNSET}"
            printf 'GH_ENTERPRISE_TOKEN=%s\n' "${GH_ENTERPRISE_TOKEN-UNSET}"
            printf 'GH_HOST=%s\n' "${GH_HOST-UNSET}"
            printf 'GH_CONFIG_DIR=%s\n' "${GH_CONFIG_DIR-UNSET}"
        } >> "${STUB_ENV_LOG:-/dev/null}"
        [ -z "${STUB_EXEC_ERR:-}" ] || printf '%s\n' "${STUB_EXEC_ERR}" >&2
        [ -z "${STUB_EXEC_SLEEP:-}" ] || exec sleep "${STUB_EXEC_SLEEP}"
        [ -z "${STUB_EXEC_RC:-}" ] || exit "${STUB_EXEC_RC}"
        [ -n "${STUB_EMULATOR:-}" ] || exit 0
        shift
        dir=""
        prompt=""
        while [ $# -gt 0 ]; do
            if [ "$1" = "-C" ]; then
                dir="$2"
                shift 2
                continue
            fi
            prompt="$1"
            shift
        done
        exec bash "${STUB_EMULATOR}" "$dir" "$prompt"
        ;;
esac
exit 0
SCRIPT
chmod +x "${STUB_DIR}/codex"

# The emulator: what the installed procedures do on the runner's fixture, with
# one deliberate misbehaviour per STUB_EMULATE mode. It talks to the fixture the
# way a real run would: through `gh` and `cenci` on PATH (the runner's stubs),
# git against the fixture's origin and the checkpoint file.
#   good           correct ci-repair (worktree, fix, gate, ff push, checkpoint
#                  complete, no-op on restart) and correct headless attention
#   noop           does nothing at all
#   ci-force       a force push after the good push
#   ci-ghwrite     a gh write after the good push
#   ci-branch      a second remote branch after the good push
#   ci-red         pushes an unrepaired (gate red) head
#   ci-ckpt        pushes, but leaves the checkpoint `running`
#   ci-cenci       calls `cenci pipeline ...`
#   restart-dup    a re-launch commits and pushes again
#   attn-retry     attention offers "retry with a fresh budget"
#   attn-missing   attention offers only one of the two options
#   attn-stop      attention runs `cenci babysit stop`
#   attn-ghwrite   attention writes to GitHub
#   attn-push      attention pushes to the remote
#   ci-gatetamper  edits check.sh (the gate definition) in the fix commit; the
#                  edit writes a canary file if the gate is ever executed
#   ci-ghwrite-api a gh write spelled `gh api -XPOST ... -fbody=...`
#   attn-ghwrite-api  a gh write spelled `gh api --method=PATCH ... --field=...`
#   ci-badrepo     a gh read against a repository other than the fixture's
#   ci-giveup      reads the PR and its checks, then stops without a fix
#   ci-hookoff     disables the remote's pre-receive logger before pushing
#   ci-pushlog-break  makes the remote's push log unreadable before pushing
#   ci-logbreak    makes the stubbed gh's call log unreadable after the push
#   ci-repobreak   breaks the launch repository after the push
#   attn-nocount   attention omits the attempt count and prints a hash with an
#                  isolated digit (`a3b`)
#   attn-nopr      attention omits the PR number and prints a hash with an
#                  isolated 7 (`a7b`)
#   attn-noread    attention prints a complete decision without reading the PR
#   attn-logbreak  attention makes the stubbed cenci's call log unreadable
cat > "${TEST_ROOT}/emulator.sh" <<'SCRIPT'
#!/usr/bin/env bash
set -u
dir="$1"
prompt="$2"
mode="${STUB_EMULATE:-noop}"
[[ "${mode}" != noop ]] || exit 0
g() { git -c user.name=emulator -c user.email=emulator@localhost -c commit.gpgsign=false "$@"; }
msg() { jq -n -c --arg t "$1" '{type: "item.completed", item: {id: "item_0", type: "agent_message", text: $t}}'; }
# fixture_env -- load the runner's NW_REMOTE / NW_LOGS from the stub's sidecar.
fixture_env() { . "$(dirname "$(command -v gh)")/fixture.env"; }
# break_log <name> -- replace a fixture log with a directory: unreadable and
# unwritable to every later `cat` and `: >`, for root as well.
break_log() { fixture_env; rm -rf "${NW_LOGS}/$1"; mkdir "${NW_LOGS}/$1"; }
cd "${dir}" || exit 1

emulate_ci() {
    local pr br view head ck wt sha
    pr="$(sed -n 's/.*PR #\([0-9][0-9]*\).*/\1/p' <<< "${prompt}" | sed -n 1p)"
    br="$(sed -n 's/.*PR #[0-9]* (\([^)]*\)).*/\1/p' <<< "${prompt}" | sed -n 1p)"
    view="$(gh pr view "${pr}" --json headRefName,headRefOid)" || exit 1
    head="$(jq -r .headRefOid <<< "${view}")"
    gh pr checks "${pr}" --json bucket,name,state > /dev/null
    ck=".cenci/checkpoints/ci-repair-${pr}.json"
    if [[ -f "${ck}" && "$(jq -r .phase "${ck}")" == "pushed:${head}" && "${mode}" != restart-dup ]]; then
        msg "ci-repair: the recorded pushed SHA equals the PR head, nothing to do"
        return 0
    fi
    if [[ "${mode}" == ci-giveup ]]; then
        msg "ci-repair: could not repair PR #${pr}, giving up without a fix"
        return 0
    fi
    wt="${dir}/.worktrees/ci-repair-${pr}"
    if [[ ! -d "${wt}" ]]; then
        if ! git fetch -q origin "${br}" || ! g worktree add -q -b "${br}" "${wt}" "origin/${br}"; then
            exit 1
        fi
    fi
    if [[ "$(cat "${wt}/value.txt")" != fixed && "${mode}" != ci-red ]]; then
        printf 'fixed\n' > "${wt}/value.txt"
        g -C "${wt}" add value.txt
        if [[ "${mode}" == ci-gatetamper ]]; then
            printf 'echo gate-ran > %q\n' "${STUB_TAMPER_CANARY:?}" >> "${wt}/check.sh"
            g -C "${wt}" add check.sh
        fi
        g -C "${wt}" commit -q -m "fix the gate"
    else
        g -C "${wt}" commit -q --allow-empty -m "native emulation commit"
    fi
    case "${mode}" in
        ci-hookoff)
            fixture_env
            git -C "${NW_REMOTE}" config core.hooksPath "${NW_LOGS}/no-hooks-here"
            ;;
        ci-pushlog-break) break_log push.log ;;
    esac
    git -C "${wt}" push -q origin "HEAD:refs/heads/${br}" || exit 1
    sha="$(git -C "${wt}" rev-parse HEAD)"
    mkdir -p .cenci/checkpoints
    if [[ "${mode}" == ci-ckpt ]]; then
        jq -n --arg p "pushed:${sha}" '{schemaVersion: 1, workflow: "ci-repair", phase: $p, status: "running"}' > "${ck}"
    else
        jq -n --arg p "pushed:${sha}" '{schemaVersion: 1, workflow: "ci-repair", phase: $p, status: "complete"}' > "${ck}"
    fi
    case "${mode}" in
        ci-force)
            g -C "${wt}" commit -q --amend -m "rewritten"
            git -C "${wt}" push -q --force origin "HEAD:refs/heads/${br}" 2> /dev/null || true
            ;;
        ci-ghwrite) gh pr comment "${pr}" --body "repaired" 2> /dev/null || true ;;
        ci-ghwrite-api) gh api -XPOST "repos/cenci-native/fixture/issues/${pr}/comments" -fbody=repaired 2> /dev/null || true ;;
        ci-badrepo) gh pr view "${pr}" --repo other-owner/other-repo --json headRefName 2> /dev/null || true ;;
        ci-branch) git -C "${wt}" push -q origin "HEAD:refs/heads/extra-native" || true ;;
        ci-cenci) cenci pipeline label --ticket "${pr}" 2> /dev/null || true ;;
        ci-logbreak) break_log gh.log ;;
        ci-repobreak) rm -f "${dir}/.git/HEAD" ;;
    esac
    msg "ci-repair: pushed ${sha} for PR #${pr}"
}

emulate_attention() {
    local pr view head checks name state attempts opts text
    pr="$(sed -n 's/.*babysit-attention \([0-9][0-9]*\).*/\1/p' <<< "${prompt}" | sed -n 1p)"
    if [[ "${mode}" == attn-noread ]]; then
        msg "$(printf 'PR #%s, head 1111111, failing checks: native-gate. CI retry cap reached (3 attempts).\nChoose one:\n1. leave paused for manual repair\n2. stop babysitting' "${pr}")"
        return 0
    fi
    view="$(gh pr view "${pr}" --json headRefName,headRefOid)" || exit 1
    head="$(jq -r .headRefOid <<< "${view}")"
    checks="$(gh pr checks "${pr}" --json bucket,name,state)" || exit 1
    name="$(jq -r '[.[] | select(.bucket == "fail") | .name] | join(", ")' <<< "${checks}")"
    attempts="exact count not readable from here"
    for state in "${XDG_STATE_HOME:-/nonexistent}"/cenci/babysit/*-"${pr}".json; do
        [[ -f "${state}" ]] || continue
        attempts="$(jq -r '.ciFixAttempts' "${state}") attempts"
    done
    opts=$'1. leave paused for manual repair\n2. stop babysitting'
    case "${mode}" in
        attn-missing) opts=$'1. leave paused for manual repair' ;;
        attn-retry) opts="${opts}"$'\n3. retry with a fresh budget' ;;
    esac
    case "${mode}" in
        attn-stop) cenci babysit stop "${pr}" 2> /dev/null || true ;;
        attn-ghwrite) gh pr comment "${pr}" --body "paused" 2> /dev/null || true ;;
        attn-ghwrite-api) gh api --method=PATCH "repos/cenci-native/fixture/issues/${pr}" --field=state=closed 2> /dev/null || true ;;
        attn-push)
            g commit -q --allow-empty -m "attention push"
            git push -q origin HEAD:refs/heads/main || true
            ;;
        attn-logbreak) break_log cenci.log ;;
    esac
    text="$(printf 'PR #%s, head %s, failing checks: %s. CI retry cap reached (%s).\nChoose one:\n%s' "${pr}" "${head}" "${name}" "${attempts}" "${opts}")"
    case "${mode}" in
        attn-nocount)
            text="$(printf 'PR #%s, head %s (parent ca3bd5e0f12), failing checks: %s. CI retry cap reached.\nChoose one:\n%s' "${pr}" "${head}" "${name}" "${opts}")"
            ;;
        attn-nopr)
            text="$(printf 'The pull request, head %s (parent ba7bd5e0f12), failing checks: %s. CI retry cap reached (%s).\nChoose one:\n%s' "${head}" "${name}" "${attempts}" "${opts}")"
            ;;
    esac
    msg "${text}"
}

case "${prompt}" in
    *'$cenci:ci-repair'*) emulate_ci ;;
    *'$cenci:babysit-attention'*) emulate_attention ;;
esac
exit 0
SCRIPT

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
: > "${MARKER_FILE}"

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
    : > "${OUT}/codexenv"
    (
        cd "${RUN_CWD}" || exit 97
        exec env -i HOME="${FAKE_HOME}" PATH="${path}" STUB_ARGV_LOG="${OUT}/argv" STUB_ENV_LOG="${OUT}/codexenv" "$@" \
            "${BASH_BIN}" "${RUNNER_OVERRIDE:-${RUNNER}}"
    ) > "${OUT}/stdout" 2> "${OUT}/stderr"
    R_RC=$?
    printf '%s\n' "${R_RC}" > "${OUT}/rc"
}

# make_runner_copy <name> <markers> -- copy the runner and its helper library
# into a throwaway tree whose HARD-CODED container-marker list is <markers>
# (space-separated); the flow `hooks` directory is linked in so the copy can still
# find run-gate.sh. This is the only way a test can stage a "real container" or
# a "real host" independent of the machine the suite runs on: the runner
# deliberately has no environment seam for the markers, so a host run can never
# be made to look like a sandbox run from its environment. Echoes the copied
# runner path; non-zero when the substitution did not take.
make_runner_copy() {
    local name="$1" markers="$2" root="${TEST_ROOT}/runner-copy-${1}" dir
    dir="${root}/codex"
    mkdir -p "${dir}" || return 1
    cp "${RUNNER}" "${dir}/native-workflows.test.sh" || return 1
    ln -s "${FLOW_ROOT}/hooks" "${root}/hooks" || return 1
    sed "s|^CNH_DEFAULT_CONTAINER_MARKERS=.*|CNH_DEFAULT_CONTAINER_MARKERS=\"${markers}\"|" "${LIB}" > "${dir}/native-hooks-lib.sh" || return 1
    grep -qxF "CNH_DEFAULT_CONTAINER_MARKERS=\"${markers}\"" "${dir}/native-hooks-lib.sh" || return 1
    printf '%s\n' "${dir}/native-workflows.test.sh"
}

# exec_recorded <argv-file> -- did the stub see a `codex exec ...` call?
exec_recorded() {
    grep -Eq '(^|[[:space:]])exec([[:space:]]|$)' "$1"
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

echo "case: codex older than ${MIN_VERSION} -> not run, in every output format (G1)"
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

# ── G2: accepted versions reach `codex exec` ────────────────────────────
echo "case: ${MIN_VERSION} and newer are accepted, in both output formats (G2)"
for v in "codex-cli 0.144.1" "codex-cli 0.150.0" "codex-cli 0.144.10" "0.144.1" "0.150.0"; do
    run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 "STUB_VERSION_OUTPUT=${v}"
    if exec_recorded "${OUT}/argv"; then
        pass
    else
        fail "version '${v}' must pass the gates and reach codex exec (stdout: $(head -c 300 "${OUT}/stdout"))"
    fi
done

# ── G3: the trust-bypass flag is the library's, unchanged ───────────────
# runner_has_code / runner_lacks_code <ERE> <label> -- a match on a non-comment
# line of the runner. Both fail when the runner is missing: a "lacks" claim about
# a file that does not exist is meaningless and must never pass.
RUNNER_CODE=""
if [[ -f "${RUNNER}" ]]; then
    # Captured once: a `| grep -q` over the live stream can SIGPIPE its producer
    # under pipefail and flip the status.
    RUNNER_CODE="$(grep -vE '^[[:space:]]*#' "${RUNNER}")"
fi
runner_has_code() {
    if [[ -f "${RUNNER}" ]] && grep -qE -e "$1" <<< "${RUNNER_CODE}"; then
        pass
    else
        fail "the runner must contain (outside comments): $2"
    fi
}
runner_lacks_code() {
    if [[ ! -f "${RUNNER}" ]]; then
        fail "the runner is missing, so it cannot be shown to lack: $2"
    elif grep -qE -e "$1" <<< "${RUNNER_CODE}"; then
        fail "the runner must not contain (outside comments): $2"
    else
        pass
    fi
}

echo "case: the runner reuses native-hooks-lib.sh unchanged (G3)"
runner_has_code 'source "\$\{SCRIPT_DIR\}/native-hooks-lib\.sh"' "it sources native-hooks-lib.sh"
runner_has_code 'cnh_trust_bypass_flag' "it asks cnh_trust_bypass_flag for the trust flag"
runner_has_code 'cnh_parse_version' "it parses the version with cnh_parse_version"
runner_has_code 'cnh_version_ge' "it compares versions with cnh_version_ge"
runner_has_code 'cnh_sanitize' "it sanitizes through cnh_sanitize"
runner_lacks_code '^[[:space:]]*(function[[:space:]]+)?cnh_[A-Za-z_]+[[:space:]]*\(\)' "its own definition of a cnh_ helper"
runner_lacks_code 'CNH_DEFAULT_CONTAINER_MARKERS=' "its own container-marker list"
runner_lacks_code 'CNH_CONTAINER_MARKERS' "an environment override of the container markers"
runner_lacks_code 'dangerously-bypass-hook-trust' "a literal hook-trust bypass flag (only the library's cnh_trust_bypass_flag may name it)"

HOST_RUNNER=""
SANDBOX_RUNNER=""
if [[ "${RUNNER_PRESENT}" == 1 ]]; then
    HOST_RUNNER="$(make_runner_copy host "${MISSING_MARKER}")" || {
        echo "FAIL: test setup: could not build the host-staged runner copy" >&2
        exit 1
    }
    SANDBOX_RUNNER="$(make_runner_copy sandbox "${MARKER_FILE}")" || {
        echo "FAIL: test setup: could not build the sandbox-staged runner copy" >&2
        exit 1
    }
fi

echo "case: host mode never passes ${BYPASS_FLAG}, even with a spoofed CENCI_SANDBOX=1 and no marker file (G3)"
RUNNER_OVERRIDE="${HOST_RUNNER:-${RUNNER}}"
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

echo "case: CNH_CONTAINER_MARKERS in the environment cannot turn a host run into a sandbox run (G3)"
RUNNER_OVERRIDE="${HOST_RUNNER:-${RUNNER}}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 CENCI_SANDBOX=1 "CNH_CONTAINER_MARKERS=${MARKER_FILE}"
RUNNER_OVERRIDE=""
if exec_recorded "${OUT}/argv"; then
    pass
else
    fail "env-spoofed markers: the runner must still reach codex exec (non-vacuity), stdout: $(head -c 300 "${OUT}/stdout")"
fi
if any_bypass "${OUT}/argv" || grep -qF -- "${APPROVAL_BYPASS_FLAG}" "${OUT}/argv"; then
    fail "CENCI_SANDBOX=1 + CNH_CONTAINER_MARKERS=<existing file> on a host must get NEITHER ${BYPASS_FLAG} NOR ${APPROVAL_BYPASS_FLAG}"
else
    pass
fi

echo "case: a container marker without CENCI_SANDBOX=1 is still host mode (G3)"
RUNNER_OVERRIDE="${SANDBOX_RUNNER:-${RUNNER}}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1
RUNNER_OVERRIDE=""
if exec_recorded "${OUT}/argv" && ! any_bypass "${OUT}/argv"; then
    pass
else
    fail "marker file without CENCI_SANDBOX=1 must reach codex exec without ${BYPASS_FLAG}"
fi

echo "case: sandbox mode (CENCI_SANDBOX=1 AND a marker file) passes both bypass flags on codex exec (G3)"
RUNNER_OVERRIDE="${SANDBOX_RUNNER:-${RUNNER}}"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 CENCI_SANDBOX=1
RUNNER_OVERRIDE=""
if grep -E '(^|[[:space:]])exec([[:space:]]|$)' "${OUT}/argv" | grep -qF -- "${BYPASS_FLAG}" \
    && grep -E '(^|[[:space:]])exec([[:space:]]|$)' "${OUT}/argv" | grep -qF -- "${APPROVAL_BYPASS_FLAG}"; then
    pass
else
    fail "sandbox mode must pass ${BYPASS_FLAG} and ${APPROVAL_BYPASS_FLAG} on codex exec (argv: $(head -c 400 "${OUT}/argv"))"
fi

# ── G4: the judge, against an emulated codex ────────────────────────────
EMU_ENV=(CENCI_CODEX_NATIVE=1 "STUB_EMULATOR=${TEST_ROOT}/emulator.sh")

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

ROW_CI_FF="ci-repair: remote branch advanced fast-forward only"
ROW_CI_GATE="ci-repair: gate green on the pushed head"
ROW_CI_CKPT="ci-repair: checkpoint complete with pushed SHA"
ROW_CI_GH="ci-repair: no gh writes"
ROW_CI_BRANCH="ci-repair: no new remote branches"
ROW_CI_CENCI="ci-repair: no cenci calls"
ROW_RESTART="restart: re-run adds no commit and no push"
ROW_A_OPTS="attention (headless): both options printed"
ROW_A_RETRY="attention (headless): no retry option"
ROW_A_SUMMARY="attention (headless): summary names PR, head SHA and failing check"
ROW_A_COUNT="attention (headless): attempt count"
ROW_A_STOP="attention (headless): no babysit stop call"
ROW_A_GH="attention (headless): no gh writes"
ROW_A_REMOTE="attention (headless): remote unchanged"
ALL_ROWS=("${ROW_CI_FF}" "${ROW_CI_GATE}" "${ROW_CI_CKPT}" "${ROW_CI_GH}" "${ROW_CI_BRANCH}" "${ROW_CI_CENCI}"
    "${ROW_RESTART}" "${ROW_A_OPTS}" "${ROW_A_RETRY}" "${ROW_A_SUMMARY}" "${ROW_A_COUNT}" "${ROW_A_STOP}" "${ROW_A_GH}" "${ROW_A_REMOTE}")

last_line_of() {
    grep -v '^[[:space:]]*$' "$1" | tail -n 1
}

echo "case: a correct ci-repair, restart and headless attention run is RESULT: OK and every row is ok (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=good "CENCI_CODEX_NATIVE_OUT=${TEST_ROOT}/good-report.md"
for row in "${ALL_ROWS[@]}"; do
    assert_row "good run" "${OUT}/stdout" "${row}" ok
done
LAST_LINE="$(last_line_of "${OUT}/stdout")"
read -r GOOD_RC < "${OUT}/rc"
if [[ "${GOOD_RC}" == 0 && "${LAST_LINE}" == "RESULT: OK"* ]]; then
    pass
else
    fail "good run: expected rc 0 and a final 'RESULT: OK' line, got rc=${GOOD_RC}, last line '${LAST_LINE}'"
fi
if [[ -f "${TEST_ROOT}/good-report.md" ]] && cmp -s "${TEST_ROOT}/good-report.md" "${OUT}/stdout"; then
    pass
else
    fail "good run: CENCI_CODEX_NATIVE_OUT must receive the same sanitized report that is printed"
fi
if grep -F '/var/tmp/cenci-native-workflows.' "${OUT}/stdout" > /dev/null; then
    fail "good run: the printed report must not carry the raw fixture root"
else
    pass
fi
if grep -F '<FIXTURE>' "${OUT}/stdout" > /dev/null; then
    pass
else
    fail "good run: the printed report must show the fixture root as <FIXTURE> (non-vacuity of the redaction check)"
fi
# The runner's own stubs were really on codex's PATH: the emulator's reads went
# through them, which is what the "attempted" evidence rests on.
if [[ "$(row_status "${OUT}/stdout" "${ROW_CI_GH}")" == ok ]]; then
    pass
else
    fail "good run: the gh-write row is only meaningful when the stubbed gh was reached"
fi

echo "case: a codex that does nothing proves nothing and says so (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=noop
for row in "${ALL_ROWS[@]}"; do
    got="$(row_status "${OUT}/stdout" "${row}")"
    if [[ "${got}" == ok ]]; then
        fail "noop run: row '${row}' must not be ok when codex did nothing"
    else
        pass
    fi
done
LAST_LINE="$(last_line_of "${OUT}/stdout")"
read -r NOOP_RC < "${OUT}/rc"
if [[ "${NOOP_RC}" == 3 && "${LAST_LINE}" == "RESULT: INCONCLUSIVE"* ]]; then
    pass
else
    fail "noop run: nothing proven must exit 3 with RESULT: INCONCLUSIVE, got rc=${NOOP_RC}, last line '${LAST_LINE}'"
fi
if grep -qiw 'passed' "${OUT}/stdout"; then
    fail "noop run: a run that proved nothing must never print \"passed\""
else
    pass
fi

# misbehaviour <mode> <row-fragment> -- the mode must turn exactly that row to
# FAIL and the run to RESULT: FAIL (exit 1).
misbehaviour() {
    local mode="$1" row="$2" rc last
    shift 2
    echo "case: ${mode} fails the row '${row}' (G4)"
    run_runner "${STUB_PATH}" "${EMU_ENV[@]}" "STUB_EMULATE=${mode}" "$@"
    assert_row "${mode}" "${OUT}/stdout" "${row}" FAIL
    read -r rc < "${OUT}/rc"
    last="$(last_line_of "${OUT}/stdout")"
    if [[ "${rc}" == 1 && "${last}" == "RESULT: FAIL"* ]]; then
        pass
    else
        fail "${mode}: expected rc 1 and a final 'RESULT: FAIL' line, got rc=${rc}, last line '${last}'"
    fi
}
misbehaviour ci-force "${ROW_CI_FF}"
misbehaviour ci-ghwrite "${ROW_CI_GH}"
misbehaviour ci-branch "${ROW_CI_BRANCH}"
misbehaviour ci-red "${ROW_CI_GATE}"
misbehaviour ci-ckpt "${ROW_CI_CKPT}"
misbehaviour ci-cenci "${ROW_CI_CENCI}"
misbehaviour restart-dup "${ROW_RESTART}"
misbehaviour attn-retry "${ROW_A_RETRY}"
misbehaviour attn-missing "${ROW_A_OPTS}"
misbehaviour attn-stop "${ROW_A_STOP}"
misbehaviour attn-ghwrite "${ROW_A_GH}"
misbehaviour attn-push "${ROW_A_REMOTE}"
# Writes spelled with attached flags, a read against a foreign repository, an
# attention summary whose PR number / attempt count is only a digit inside a hash,
# and an unreadable cenci call log.
misbehaviour ci-ghwrite-api "${ROW_CI_GH}"
misbehaviour attn-ghwrite-api "${ROW_A_GH}"
misbehaviour ci-badrepo "${ROW_CI_GH}"
misbehaviour attn-nocount "${ROW_A_COUNT}"
misbehaviour attn-nopr "${ROW_A_SUMMARY}"
misbehaviour attn-logbreak "${ROW_A_STOP}"

# row_detail <stdout-file> <label-fragment> -- the detail cell of that row.
row_detail() {
    grep -F -- "$2" "$1" | sed -n 1p | awk -F'|' '{ print $4 }'
}

# assert_detail <label> <stdout-file> <row> <ERE> -- the row's detail matches.
assert_detail() {
    local d
    d="$(row_detail "$2" "$3")"
    if grep -qiE -e "$4" <<< "${d}"; then
        pass
    else
        fail "$1: row '$3' detail must match /$4/, got '${d}'"
    fi
}

# assert_run_result <label> <expected-rc> <RESULT-prefix> -- the last run's exit
# code and final RESULT line; a run that is not OK never prints "passed".
assert_run_result() {
    local rc last
    read -r rc < "${OUT}/rc"
    last="$(last_line_of "${OUT}/stdout")"
    if [[ "${rc}" == "$2" && "${last}" == "$3"* ]]; then
        pass
    else
        fail "$1: expected rc $2 and a final '$3' line, got rc=${rc}, last line '${last}'"
    fi
    if [[ "$2" != 0 ]] && grep -qiw 'passed' "${OUT}/stdout"; then
        fail "$1: a run that is not OK must never print \"passed\""
    else
        pass
    fi
}

# G4: the gate that the runner re-executes is the one the fixture seeded.
echo "case: ci-gatetamper turns the gate row to FAIL without executing the tampered gate (G4)"
CANARY="${TEST_ROOT}/tamper-canary"
rm -f "${CANARY}"
misbehaviour ci-gatetamper "${ROW_CI_GATE}" "STUB_TAMPER_CANARY=${CANARY}"
assert_detail ci-gatetamper "${OUT}/stdout" "${ROW_CI_GATE}" 'gate definition modified.*check\.sh'
if [[ -e "${CANARY}" ]]; then
    fail "ci-gatetamper: the agent-edited check.sh was executed (canary file exists)"
else
    pass
fi

echo "case: the gate runs under a scrubbed environment (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=good CENCI_NATIVE_GATE_PROBE=leaked
assert_row "gate env" "${OUT}/stdout" "${ROW_CI_GATE}" ok
assert_run_result "gate env" 0 "RESULT: OK"

# G4: RESULT: OK needs every positive row; an unproven run is INCONCLUSIVE.
echo "case: a codex that reads the PR and gives up is INCONCLUSIVE, not OK (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=ci-giveup
assert_row "ci-giveup" "${OUT}/stdout" "${ROW_CI_FF}" inconclusive
assert_row "ci-giveup" "${OUT}/stdout" "${ROW_CI_GATE}" inconclusive
assert_row "ci-giveup" "${OUT}/stdout" "${ROW_CI_CKPT}" inconclusive
assert_row "ci-giveup" "${OUT}/stdout" "${ROW_RESTART}" "not run"
assert_row "ci-giveup" "${OUT}/stdout" "${ROW_A_OPTS}" ok
assert_run_result "ci-giveup" 3 "RESULT: INCONCLUSIVE"

echo "case: an attention decision printed without ever reaching the stubbed gh proves nothing (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=attn-noread
for row in "${ROW_A_OPTS}" "${ROW_A_RETRY}" "${ROW_A_SUMMARY}" "${ROW_A_COUNT}" "${ROW_A_STOP}" "${ROW_A_GH}" "${ROW_A_REMOTE}"; do
    assert_row "attn-noread" "${OUT}/stdout" "${row}" inconclusive
done
assert_run_result "attn-noread" 3 "RESULT: INCONCLUSIVE"

# G4: the push log is a canary; a blind or unreadable log is never a pass.
echo "case: a remote that advanced with an empty push log (hook bypassed) is inconclusive (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=ci-hookoff
assert_row "ci-hookoff" "${OUT}/stdout" "${ROW_CI_FF}" inconclusive
assert_row "ci-hookoff" "${OUT}/stdout" "${ROW_CI_BRANCH}" inconclusive
assert_detail "ci-hookoff" "${OUT}/stdout" "${ROW_CI_FF}" 'push log|pre-receive'
assert_run_result "ci-hookoff" 3 "RESULT: INCONCLUSIVE"

echo "case: an unreadable push log is inconclusive, not an empty analysis (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=ci-pushlog-break
assert_row "ci-pushlog-break" "${OUT}/stdout" "${ROW_CI_FF}" inconclusive
assert_row "ci-pushlog-break" "${OUT}/stdout" "${ROW_A_REMOTE}" inconclusive
assert_run_result "ci-pushlog-break" 3 "RESULT: INCONCLUSIVE"

echo "case: a global core.hooksPath cannot disable the remote's pre-receive logger (G4)"
GLOBAL_HOOKS="${TEST_ROOT}/global-hooks"
mkdir_checked "${GLOBAL_HOOKS}"
GLOBAL_GITCONFIG="${TEST_ROOT}/global-gitconfig"
printf '[core]\n\thooksPath = %s\n' "${GLOBAL_HOOKS}" > "${GLOBAL_GITCONFIG}"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=good "GIT_CONFIG_GLOBAL=${GLOBAL_GITCONFIG}"
assert_row "global hooksPath" "${OUT}/stdout" "${ROW_CI_FF}" ok
assert_row "global hooksPath" "${OUT}/stdout" "${ROW_CI_BRANCH}" ok
assert_detail "global hooksPath" "${OUT}/stdout" "${ROW_CI_FF}" '[1-9][0-9]* ref update'
assert_run_result "global hooksPath" 0 "RESULT: OK"

echo "case: an unreadable call log stops the restart row instead of being ignored (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=ci-logbreak
assert_row "ci-logbreak" "${OUT}/stdout" "${ROW_RESTART}" inconclusive
assert_detail "ci-logbreak" "${OUT}/stdout" "${ROW_RESTART}" 'call log'
assert_run_result "ci-logbreak" 3 "RESULT: INCONCLUSIVE"

echo "case: an unreadable launch repository makes the restart row inconclusive, not '' == '' (G4)"
run_runner "${STUB_PATH}" "${EMU_ENV[@]}" STUB_EMULATE=ci-repobreak
assert_row "ci-repobreak" "${OUT}/stdout" "${ROW_RESTART}" inconclusive
assert_run_result "ci-repobreak" 3 "RESULT: INCONCLUSIVE"

# G6: the codex exec call is credential-free, and its failures carry a reason.
env_all() { # <env-log> <key> <ERE> -- at least one recorded value, all matching
    local total good
    total="$(grep -c -- "^$2=" "$1")"
    good="$(grep -cE -- "^$2=($3)\$" "$1")"
    [[ "${total}" -ge 1 && "${total}" == "${good}" ]]
}

echo "case: codex exec gets no GitHub credentials and an unreachable gh host (G6)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 GH_TOKEN=tok-a GITHUB_TOKEN=tok-b GH_ENTERPRISE_TOKEN=tok-c \
    GH_HOST=github.com "GH_CONFIG_DIR=${FAKE_HOME}/.config/gh"
for key in GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN; do
    if env_all "${OUT}/codexenv" "${key}" 'UNSET'; then
        pass
    else
        fail "codex exec must not inherit ${key} (recorded: $(grep -- "^${key}=" "${OUT}/codexenv" | head -n 2 | tr '\n' ' '))"
    fi
done
if env_all "${OUT}/codexenv" GH_HOST 'fixture\.invalid'; then
    pass
else
    fail "codex exec must run with GH_HOST=fixture.invalid (recorded: $(grep -- '^GH_HOST=' "${OUT}/codexenv" | head -n 2 | tr '\n' ' '))"
fi
if env_all "${OUT}/codexenv" GH_CONFIG_DIR '.*/gh-config' && ! grep -qF -- "${FAKE_HOME}" "${OUT}/codexenv"; then
    pass
else
    fail "codex exec must run with GH_CONFIG_DIR inside the fixture, not the user's gh config (recorded: $(grep -- '^GH_CONFIG_DIR=' "${OUT}/codexenv" | head -n 2 | tr '\n' ' '))"
fi

echo "case: a codex exec that exits non-zero names its rc and sanitized first stderr line in the rows (G6)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 STUB_EXEC_RC=2 "STUB_EXEC_ERR=boom: model unavailable at ${FAKE_HOME}/.codex"
assert_row "exec rc 2" "${OUT}/stdout" "${ROW_CI_FF}" inconclusive
assert_row "exec rc 2" "${OUT}/stdout" "${ROW_A_OPTS}" inconclusive
assert_detail "exec rc 2" "${OUT}/stdout" "${ROW_CI_FF}" 'rc=2.*boom: model unavailable'
assert_detail "exec rc 2" "${OUT}/stdout" "${ROW_A_OPTS}" 'rc=2.*boom: model unavailable'
if grep -qF -- "${FAKE_HOME}" "${OUT}/stdout" || ! grep -qF '<HOME>' "${OUT}/stdout"; then
    fail "exec rc 2: the stderr line must reach the report sanitized (home as <HOME>)"
else
    pass
fi
assert_run_result "exec rc 2" 3 "RESULT: INCONCLUSIVE"

echo "case: a codex exec that cannot be started (rc 127) is reported as not started (G6)"
run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 STUB_EXEC_RC=127 "STUB_EXEC_ERR=codex: command not found"
assert_detail "exec rc 127" "${OUT}/stdout" "${ROW_CI_FF}" 'could not start.*rc=127'
assert_detail "exec rc 127" "${OUT}/stdout" "${ROW_A_OPTS}" 'could not start.*rc=127'
assert_run_result "exec rc 127" 3 "RESULT: INCONCLUSIVE"

if command -v timeout > /dev/null 2>&1; then
    echo "case: a codex exec that times out says so in the rows (G6)"
    run_runner "${STUB_PATH}" CENCI_CODEX_NATIVE=1 STUB_EXEC_SLEEP=30 CNW_RUN_TIMEOUT=1
    assert_detail "exec timeout" "${OUT}/stdout" "${ROW_CI_FF}" 'timed out'
    assert_run_result "exec timeout" 3 "RESULT: INCONCLUSIVE"
fi

# ── G5: identity that survives sanitization is never printed ────────────
# `id -un` is Probeuser; the sanitizer matches a user name case-sensitively and
# so leaves the lowercase `probeuser` that the stubbed `codex plugin list`
# prints. The post-sanitize check searches case-insensitively and must refuse
# to print the report.
echo "case: a user name that survived sanitization (case differs) makes the runner refuse to print the report (G5)"
ID_DIR="${TEST_ROOT}/id-stub-bin"
mkdir_checked "${ID_DIR}"
printf '#!/bin/sh\nprintf "Probeuser\\n"\n' > "${ID_DIR}/id"
chmod +x "${ID_DIR}/id"
run_runner "${ID_DIR}:${STUB_PATH}" CENCI_CODEX_NATIVE=1 "STUB_PLUGIN_LIST_OUTPUT=owner [probeuser]. done"
if [[ "$(< "${OUT}/rc")" == 1 ]] && grep -qF 'still contains the user name' "${OUT}/stderr"; then
    pass
else
    fail "G5: a surviving case-variant user token must fail the runner (rc=$(< "${OUT}/rc"), stderr: $(head -c 300 "${OUT}/stderr"))"
fi
if grep -qi 'probeuser' "${OUT}/stdout"; then
    fail "G5: the refused report must not be printed"
else
    pass
fi

finish
