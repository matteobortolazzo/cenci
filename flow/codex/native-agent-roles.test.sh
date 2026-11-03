#!/usr/bin/env bash
# native-agent-roles.test.sh -- opt-in native Codex runner for generated agent roles
# (#1191). It spawns REAL custom-agent roles through `codex exec` and records what
# Codex resolves, because the rest of the suite can only read role files as text:
#
#   inherit      planner, installed with no override: the spawned role resolves the
#                parent session's model and effort
#   override     implementer, installed with --role-override from
#                CENCI_CODEX_NATIVE_OVERRIDE_MODEL (and _EFFORT, optional): the
#                spawned role resolves exactly that model (and effort)
#   unavailable  critical-reviewer, installed with a deliberately unavailable model:
#                the run is stopped by an error Codex reports, naming the model;
#                the role is never silently run on another model
#
# The fixture is built by flow/codex/install-agents.sh itself, under /var/tmp (never
# /tmp: the guards allowlist /tmp/*), and Codex runs read-only against it.
#
# Resolved model and effort are read from the session records ($CODEX_HOME/sessions
# rollout `turn_context` records of the parent thread and the spawned role's own
# thread) and failures from the `codex exec --json` event stream -- never from the
# model's narration. A scenario whose evidence is not visible is reported as
# `inconclusive`; the runner never infers a value.
#
# Opt-in: prints `not run: <reason>` and exits 0 -- never "passed" -- when
# CENCI_CODEX_NATIVE is not 1, codex is absent or older than 0.144.1, jq or git is
# absent, or `codex login status` fails, so run-checks.sh can run it in CI unchanged.
#
# Inputs (all optional):
#   CENCI_CODEX_NATIVE_OVERRIDE_MODEL   model for the override role, [A-Za-z0-9._-]+;
#                                       unset: the override row is `not run`
#   CENCI_CODEX_NATIVE_OVERRIDE_EFFORT  effort for the override role, [a-z]+
#   CNAR_RUN_TIMEOUT                    seconds per codex run (default 600)
#   CENCI_CODEX_NATIVE_OUT              also write the sanitized report to this file
#
# Output: a sanitized markdown fragment on stdout, ending with one RESULT line:
#   RESULT: OK            exit 0  inherit, override and unavailable rows are all ok
#   RESULT: INCONCLUSIVE  exit 3  nothing failed, but a required row is not ok
#   RESULT: FAIL          exit 1  at least one row failed
# Home, user, host, fixture paths, UUIDs and token-shaped strings are redacted with
# the sanitizer in native-hooks-lib.sh before anything is printed.
set -uo pipefail

SCRIPT_DIR="${BASH_SOURCE[0]%/*}"
[[ "${BASH_SOURCE[0]}" == */* ]] || SCRIPT_DIR="."
SCRIPT_DIR="$(cd "${SCRIPT_DIR}" && pwd)" || exit 1
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 1
MIN_VERSION="0.144.1"

INHERIT_ROLE="planner"
OVERRIDE_ROLE="implementer"
UNAVAILABLE_ROLE="critical-reviewer"
UNAVAILABLE_MODEL="cenci-unavailable-model-1191"

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
    || not_run "set CENCI_CODEX_NATIVE=1 to run the native Codex agent-role runner (opt-in)"
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

# ── Inputs ──────────────────────────────────────────────────────────────
OV_MODEL="${CENCI_CODEX_NATIVE_OVERRIDE_MODEL:-}"
OV_EFFORT="${CENCI_CODEX_NATIVE_OVERRIDE_EFFORT:-}"
if [[ -n "${OV_MODEL}" && ! "${OV_MODEL}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "FAIL: CENCI_CODEX_NATIVE_OVERRIDE_MODEL must match [A-Za-z0-9._-]+" >&2
    exit 2
fi
if [[ -n "${OV_EFFORT}" && ! "${OV_EFFORT}" =~ ^[a-z]+$ ]]; then
    echo "FAIL: CENCI_CODEX_NATIVE_OVERRIDE_EFFORT must match [a-z]+" >&2
    exit 2
fi
if [[ -n "${OV_EFFORT}" && -z "${OV_MODEL}" ]]; then
    echo "FAIL: CENCI_CODEX_NATIVE_OVERRIDE_EFFORT needs CENCI_CODEX_NATIVE_OVERRIDE_MODEL" >&2
    exit 2
fi
RUN_TIMEOUT="${CNAR_RUN_TIMEOUT:-600}"
[[ "${RUN_TIMEOUT}" =~ ^[1-9][0-9]*$ ]] || {
    echo "FAIL: CNAR_RUN_TIMEOUT must be a positive integer" >&2
    exit 2
}

# ── Setup ───────────────────────────────────────────────────────────────
unset TMPDIR GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
FIXTURE_ROOT="$(mktemp -d /var/tmp/cenci-native-agent-roles.XXXXXX)" || {
    echo "FAIL: mktemp -d failed" >&2
    exit 1
}
case "${FIXTURE_ROOT}" in
    /var/tmp/cenci-native-agent-roles.?*) ;;
    *)
        echo "FAIL: unexpected fixture root '${FIXTURE_ROOT}'" >&2
        exit 1
        ;;
esac
trap 'rm -rf "${FIXTURE_ROOT}"' EXIT
FIXTURE_REAL="$(cd "${FIXTURE_ROOT}" && pwd -P)" || FIXTURE_REAL="${FIXTURE_ROOT}"
REPO="${FIXTURE_REAL}/repo"
RUNS="${FIXTURE_REAL}/runs"
REPORT="${FIXTURE_REAL}/report.md"
mkdir -p "${REPO}" "${RUNS}" || {
    echo "FAIL: could not create the fixture directories" >&2
    exit 1
}
: > "${REPORT}" || exit 1

# Identity strings, resolved once so every printed line goes through the sanitizer.
HOST_NAME="$(hostname 2>/dev/null)" || HOST_NAME=""
HOST_SHORT="$(hostname -s 2>/dev/null)" || HOST_SHORT=""
USER_NAME="$(id -un 2>/dev/null)" || USER_NAME=""
HOME_SAN="${HOME:-}"
HOME_REAL=""
if [[ -n "${HOME_SAN}" && -d "${HOME_SAN}" ]]; then
    HOME_REAL="$(cd "${HOME_SAN}" && pwd -P)" || HOME_REAL=""
fi
[[ "${#HOME_SAN}" -ge 2 ]] || HOME_SAN=""
[[ "${#HOME_REAL}" -ge 2 ]] || HOME_REAL=""

san() {
    cnh_sanitize "${HOME_SAN}" "${USER_NAME}" "${HOST_NAME}" "${FIXTURE_ROOT}" \
        | cnh_sanitize "${HOME_REAL}" "" "${HOST_SHORT}" "${FIXTURE_REAL}"
}
say() { printf '%s\n' "$*" | san >&2; }
rpt() { printf '%s\n' "$*" >> "${REPORT}"; }
cell() {
    local v="$1"
    v="${v//|/\\|}"
    v="${v//$'\n'/ }"
    printf '%s' "${v}"
}

N_FAIL=0
ROW_STATUS=$'\n'
LBL_INHERIT="inherit (${INHERIT_ROLE}): spawned role resolves the parent's model and effort"
LBL_OVERRIDE="override (${OVERRIDE_ROLE}): spawned role resolves the overridden model and effort"
LBL_UNAVAILABLE="unavailable model (${UNAVAILABLE_ROLE}): Codex reports it and nothing is substituted"

# record <label> <ok|FAIL|inconclusive|not run> <detail>
record() {
    local label="$1" status="$2" detail="${3:-}"
    ROW_STATUS="${ROW_STATUS}${label}"$'\t'"${status}"$'\n'
    [[ "${status}" != FAIL ]] || N_FAIL=$((N_FAIL + 1))
    printf '| %s | %s | %s |\n' "$(cell "${label}")" "${status}" "$(cell "${detail}")" >> "${REPORT}"
    say "${label}: ${status} -- ${detail}"
}

row_is_ok() { [[ "${ROW_STATUS}" == *$'\n'"$1"$'\t'ok$'\n'* ]]; }

# ── Fixture: the roles come from install-agents.sh, never hand-written ──
git init -q "${REPO}" || { echo "FAIL: git init failed" >&2; exit 1; }
declare -a OVERRIDE_ARGS=(--role-override "${UNAVAILABLE_ROLE}=${UNAVAILABLE_MODEL}")
if [[ -n "${OV_MODEL}" ]]; then
    OVERRIDE_ARGS+=(--role-override "${OVERRIDE_ROLE}=${OV_MODEL}${OV_EFFORT:+:${OV_EFFORT}}")
fi
PLUGIN_ROOT="${FLOW_DIR}" sh "${FLOW_DIR}/codex/install-agents.sh" "${REPO}" "${OVERRIDE_ARGS[@]}" >/dev/null || {
    echo "FAIL: install-agents.sh failed" >&2
    exit 1
}
AGENTS_DIR="${REPO}/.codex/agents"
# grep exits 2 for an unreadable file, which must never read as "no pin".
[[ -f "${AGENTS_DIR}/${INHERIT_ROLE}.toml" && -r "${AGENTS_DIR}/${INHERIT_ROLE}.toml" ]] || {
    echo "FAIL: the inherit role file ${INHERIT_ROLE}.toml was not installed or is unreadable" >&2
    exit 1
}
if grep -qE '^model(_reasoning_effort)?[[:space:]]*=' "${AGENTS_DIR}/${INHERIT_ROLE}.toml"; then
    echo "FAIL: the inherit role ${INHERIT_ROLE} was installed with a model pin" >&2
    exit 1
fi
if [[ -n "${OV_MODEL}" ]]; then
    [[ -f "${AGENTS_DIR}/${OVERRIDE_ROLE}.toml" && -r "${AGENTS_DIR}/${OVERRIDE_ROLE}.toml" ]] || {
        echo "FAIL: the override role file ${OVERRIDE_ROLE}.toml was not installed or is unreadable" >&2
        exit 1
    }
    grep -qxF "model = \"${OV_MODEL}\"" "${AGENTS_DIR}/${OVERRIDE_ROLE}.toml" || {
        echo "FAIL: the override role ${OVERRIDE_ROLE} was installed without the requested model" >&2
        exit 1
    }
fi

PLUGIN_COMMIT="$(git -C "${FLOW_DIR}" rev-parse HEAD 2>/dev/null)" || PLUGIN_COMMIT="unknown"
rpt "## Native agent roles run"
rpt ""
rpt "- Codex version: ${CODEX_VERSION}"
rpt "- Plugin commit: ${PLUGIN_COMMIT}"
rpt "- Override requested: ${OV_MODEL:-none}${OV_EFFORT:+ / ${OV_EFFORT}}"
rpt "- Unavailable model used: ${UNAVAILABLE_MODEL}"
rpt ""
rpt "| scenario | result | detail |"
rpt "|---|---|---|"

# ── Running Codex and reading evidence ──────────────────────────────────
CODEX_HOME_DIR="${CODEX_HOME:-${HOME:-}/.codex}"
SCEN_N=0
SCEN_DIR=""
EVENTS=""
CODEX_RC=0
THREAD_ID=""
declare -a TIMEOUT_CMD=()
if command -v timeout >/dev/null 2>&1; then
    TIMEOUT_CMD=(timeout "${RUN_TIMEOUT}")
else
    say "notice: timeout is not on PATH, so CNAR_RUN_TIMEOUT is ignored and a hung codex run is not bounded"
fi

# run_codex <role> -- one read-only `codex exec --json` run that asks for one spawn
# of <role>. Sets SCEN_DIR, EVENTS, CODEX_RC and THREAD_ID. Returns 1 when codex
# could not be started.
run_codex() {
    local role="$1" prompt
    SCEN_N=$((SCEN_N + 1))
    SCEN_DIR="${RUNS}/${SCEN_N}"
    EVENTS="${SCEN_DIR}/events.jsonl"
    THREAD_ID=""
    mkdir -p "${SCEN_DIR}" || return 1
    : > "${SCEN_DIR}/start.marker" || return 1
    prompt="Use the spawn_agent tool exactly once with agent_type \"${role}\" and the message \"Reply with the single word done.\" Wait for that agent to finish, then reply with the single word finished. Do not use any other tool."
    ${TIMEOUT_CMD[@]+"${TIMEOUT_CMD[@]}"} \
        codex exec --json --skip-git-repo-check -C "${REPO}" --sandbox read-only \
        -c "projects.\"${REPO}\".trust_level=\"trusted\"" "${prompt}" \
        > "${EVENTS}" 2> "${SCEN_DIR}/stderr" < /dev/null
    CODEX_RC=$?
    case "${CODEX_RC}" in
        126 | 127) return 1 ;;
    esac
    THREAD_ID="$(jq -R -r 'fromjson?
        | select(type == "object" and .type == "thread.started" and ((.thread_id // null) | type) == "string")
        | .thread_id' "${EVENTS}" 2>/dev/null | sed -n '1p')" || THREAD_ID=""
    [[ "${THREAD_ID}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || THREAD_ID=""
    return 0
}

run_note() {
    case "${CODEX_RC}" in
        0) printf 'codex rc=0' ;;
        124) printf 'codex timed out after %ss (rc=124)' "${RUN_TIMEOUT}" ;;
        *) printf 'codex rc=%s' "${CODEX_RC}" ;;
    esac
}

# Session facts from one rollout: "role<TAB><agent_role>" and the FIRST turn_context
# as "turn<TAB><model><TAB><effort>". Rollout shape: Codex protocol (session_meta
# with a subagent source, turn_context with model and effort).
rollout_facts() {
    jq -R -r 'fromjson? | select(type == "object")
        | if .type == "session_meta" then
            "role\t" + (try (.payload.source.subagent.thread_spawn.agent_role // .payload.source.subagent.agent_role // .payload.agent_role // "") catch "")
          elif .type == "turn_context" then
            "turn\t" + ((.payload.model // "") | tostring) + "\t" + ((.payload.effort // .payload.reasoning_effort // "") | tostring)
          else empty end' "$1" 2>/dev/null
}

PARENT_MODEL=""
PARENT_EFFORT=""
CHILD_FOUND=0
CHILD_MODEL=""
CHILD_EFFORT=""

# collect_facts <role> -- fill PARENT_* from the rollout of this run's thread and
# CHILD_* from a rollout written during this run whose session_meta names <role>.
collect_facts() {
    local role="$1" list="${SCEN_DIR}/rollouts.list" f facts kind a b child_role first_turn a_model b_effort
    PARENT_MODEL="" PARENT_EFFORT="" CHILD_FOUND=0 CHILD_MODEL="" CHILD_EFFORT=""
    [[ -d "${CODEX_HOME_DIR}/sessions" ]] || return 1
    find "${CODEX_HOME_DIR}/sessions" -type f -name 'rollout-*.jsonl' -newer "${SCEN_DIR}/start.marker" -print0 > "${list}" || return 1
    while IFS= read -r -d '' f; do
        facts="$(rollout_facts "${f}")" || continue
        child_role=""
        first_turn=""
        a_model=""
        b_effort=""
        while IFS=$'\t' read -r kind a b; do
            case "${kind}" in
                role) [[ -n "${child_role}" ]] || child_role="${a}" ;;
                turn) [[ -n "${first_turn}" ]] || { first_turn="yes"; a_model="${a}"; b_effort="${b}"; } ;;
            esac
        done <<< "${facts}"
        [[ -n "${first_turn}" ]] || continue
        if [[ -n "${THREAD_ID}" && "${f}" == *"${THREAD_ID}"* ]]; then
            PARENT_MODEL="${a_model}"
            PARENT_EFFORT="${b_effort}"
        elif [[ "${child_role}" == "${role}" && "${CHILD_FOUND}" -eq 0 ]]; then
            CHILD_FOUND=1
            CHILD_MODEL="${a_model}"
            CHILD_EFFORT="${b_effort}"
        fi
    done < "${list}"
    return 0
}

# failure_texts -- error messages Codex reported in this run's event stream.
failure_texts() {
    jq -R -r 'fromjson? | select(type == "object")
        | if .type == "error" then (.message // empty)
          elif .type == "turn.failed" then (.error.message // empty)
          elif ((.item // null) != null and (.item.type // "") == "error") then (.item.message // empty)
          elif ((.item // null) != null and (.item.type // "") == "collab_tool_call" and (.item.status // "") == "failed") then ("spawn failed: " + (.item | tostring))
          else empty end' "${EVENTS}" 2>/dev/null
}

# spawn_failure_texts -- only the failed spawn (collab tool call) events of this run.
spawn_failure_texts() {
    jq -R -r 'fromjson? | select(type == "object")
        | select((.item // null) != null and (.item.type // "") == "collab_tool_call" and (.item.status // "") == "failed")
        | "spawn failed: " + (.item | tostring)' "${EVENTS}" 2>/dev/null
}

trim200() {
    local v="$1"
    v="${v//$'\n'/ }"
    printf '%s' "${v:0:200}"
}

# ── Scenarios ───────────────────────────────────────────────────────────
scenario_inherit() {
    if ! run_codex "${INHERIT_ROLE}"; then
        record "${LBL_INHERIT}" "inconclusive" "could not start codex exec ($(run_note))"
        return 0
    fi
    local facts_rc=0
    collect_facts "${INHERIT_ROLE}" || facts_rc=$?
    if [[ "${facts_rc}" -ne 0 ]]; then
        record "${LBL_INHERIT}" "inconclusive" "session records unreadable (sessions directory missing or not searchable), so the resolved model could not be read ($(run_note))"
    elif [[ "${CHILD_FOUND}" -eq 0 || -z "${CHILD_MODEL}" ]]; then
        record "${LBL_INHERIT}" "inconclusive" "no session record shows a spawned ${INHERIT_ROLE} role with a resolved model ($(run_note)); is the cenci plugin or project trust active?"
    elif [[ -z "${PARENT_MODEL}" ]]; then
        record "${LBL_INHERIT}" "inconclusive" "the role resolved ${CHILD_MODEL}/${CHILD_EFFORT:-unset}, but the parent session's model could not be read to compare"
    elif [[ "${CHILD_MODEL}" != "${PARENT_MODEL}" ]] \
        || [[ -n "${CHILD_EFFORT}" && -n "${PARENT_EFFORT}" && "${CHILD_EFFORT}" != "${PARENT_EFFORT}" ]]; then
        record "${LBL_INHERIT}" "FAIL" "role resolved ${CHILD_MODEL}/${CHILD_EFFORT:-unset}, parent is ${PARENT_MODEL}/${PARENT_EFFORT:-unset}"
    elif [[ -z "${CHILD_EFFORT}" || -z "${PARENT_EFFORT}" ]]; then
        # Two unreadable efforts are not equal efforts: never claim an effort match.
        record "${LBL_INHERIT}" "inconclusive" "model matches (${CHILD_MODEL}) but the effort could not be read for both sessions (role ${CHILD_EFFORT:-unset}, parent ${PARENT_EFFORT:-unset})"
    elif [[ "${CODEX_RC}" -ne 0 ]]; then
        record "${LBL_INHERIT}" "inconclusive" "role ${CHILD_MODEL}/${CHILD_EFFORT} equals parent, but codex did not exit cleanly ($(run_note))"
    else
        record "${LBL_INHERIT}" "ok" "role ${CHILD_MODEL}/${CHILD_EFFORT} equals parent ${PARENT_MODEL}/${PARENT_EFFORT} ($(run_note))"
    fi
}

scenario_override() {
    if [[ -z "${OV_MODEL}" ]]; then
        record "${LBL_OVERRIDE}" "not run" "set CENCI_CODEX_NATIVE_OVERRIDE_MODEL (and optionally _EFFORT) to a model your account can use"
        return 0
    fi
    if ! run_codex "${OVERRIDE_ROLE}"; then
        record "${LBL_OVERRIDE}" "inconclusive" "could not start codex exec ($(run_note))"
        return 0
    fi
    local facts_rc=0
    collect_facts "${OVERRIDE_ROLE}" || facts_rc=$?
    if [[ "${facts_rc}" -ne 0 ]]; then
        record "${LBL_OVERRIDE}" "inconclusive" "session records unreadable (sessions directory missing or not searchable), so the resolved model could not be read ($(run_note))"
    elif [[ "${CHILD_FOUND}" -eq 0 || -z "${CHILD_MODEL}" ]]; then
        record "${LBL_OVERRIDE}" "inconclusive" "no session record shows a spawned ${OVERRIDE_ROLE} role with a resolved model ($(run_note))"
    elif [[ "${CHILD_MODEL}" != "${OV_MODEL}" ]]; then
        record "${LBL_OVERRIDE}" "FAIL" "role resolved ${CHILD_MODEL}/${CHILD_EFFORT:-unset}, override asked for ${OV_MODEL}${OV_EFFORT:+/${OV_EFFORT}}"
    elif [[ -n "${OV_EFFORT}" && -z "${CHILD_EFFORT}" ]]; then
        record "${LBL_OVERRIDE}" "inconclusive" "role resolved model ${CHILD_MODEL}, but its effort could not be read to compare with ${OV_EFFORT}"
    elif [[ -n "${OV_EFFORT}" && "${CHILD_EFFORT}" != "${OV_EFFORT}" ]]; then
        record "${LBL_OVERRIDE}" "FAIL" "role resolved model ${CHILD_MODEL} but effort ${CHILD_EFFORT}, override asked for ${OV_EFFORT}"
    elif [[ "${CODEX_RC}" -ne 0 ]]; then
        record "${LBL_OVERRIDE}" "inconclusive" "role resolved ${CHILD_MODEL}/${CHILD_EFFORT:-unset}, but codex did not exit cleanly ($(run_note))"
    else
        record "${LBL_OVERRIDE}" "ok" "role resolved ${CHILD_MODEL}/${CHILD_EFFORT:-unset}${OV_EFFORT:+ as overridden} ($(run_note))"
    fi
}

scenario_unavailable() {
    local texts spawn_texts first
    if ! run_codex "${UNAVAILABLE_ROLE}"; then
        record "${LBL_UNAVAILABLE}" "inconclusive" "could not start codex exec ($(run_note))"
        return 0
    fi
    local facts_rc=0
    collect_facts "${UNAVAILABLE_ROLE}" || facts_rc=$?
    texts="$(failure_texts)" || texts=""
    spawn_texts="$(spawn_failure_texts)" || spawn_texts=""
    first="$(trim200 "$(sed -n '1p' <<< "${texts}")")"
    # Substitution is checked first: an error beside a role that ran on another model
    # is still a substituted run, never an ok. An ok needs readable session records,
    # because "no child rollout" only means "no substitution" when the records could
    # be read at all.
    if [[ "${CHILD_FOUND}" -eq 1 && -n "${CHILD_MODEL}" && "${CHILD_MODEL}" != "${UNAVAILABLE_MODEL}" ]]; then
        record "${LBL_UNAVAILABLE}" "FAIL" "the role ran on ${CHILD_MODEL} instead of ${UNAVAILABLE_MODEL} (substitution; $(run_note))"
    elif [[ "${facts_rc}" -ne 0 ]]; then
        record "${LBL_UNAVAILABLE}" "inconclusive" "session records unreadable, so a substituted run cannot be ruled out ($(run_note))"
    elif [[ "${CHILD_FOUND}" -eq 1 && -z "${CHILD_MODEL}" ]]; then
        record "${LBL_UNAVAILABLE}" "inconclusive" "a spawned ${UNAVAILABLE_ROLE} session record was found but its model is empty, so substitution cannot be ruled out ($(run_note))"
    elif [[ -n "${spawn_texts}" ]] && grep -qiF -- "${UNAVAILABLE_MODEL}" <<< "${spawn_texts}"; then
        record "${LBL_UNAVAILABLE}" "ok" "Codex reported the failed spawn: $(trim200 "$(sed -n '1p' <<< "${spawn_texts}")") ($(run_note))"
    elif [[ -n "${spawn_texts}" ]] && grep -qiF -- "${UNAVAILABLE_MODEL}" <<< "${texts}"; then
        record "${LBL_UNAVAILABLE}" "inconclusive" "the model name appears only in a separate error event, not in the failed spawn event: ${first} ($(run_note))"
    elif [[ -n "${texts}" ]]; then
        record "${LBL_UNAVAILABLE}" "inconclusive" "Codex reported a failure, but no failed spawn event naming the model was seen: ${first} ($(run_note))"
    else
        record "${LBL_UNAVAILABLE}" "inconclusive" "no error and no spawned-role record were seen ($(run_note))"
    fi
}

scenario_inherit
scenario_override
scenario_unavailable

# ── Result ──────────────────────────────────────────────────────────────
if [[ "${N_FAIL}" -gt 0 ]]; then
    RESULT="FAIL"
    RC=1
elif row_is_ok "${LBL_INHERIT}" && row_is_ok "${LBL_OVERRIDE}" && row_is_ok "${LBL_UNAVAILABLE}"; then
    RESULT="OK"
    RC=0
else
    RESULT="INCONCLUSIVE"
    RC=3
fi
rpt ""
rpt "RESULT: ${RESULT}"

# Sanitize into a checked file (the pipeline's own status, pipefail is on), and
# print nothing -- in particular no RESULT line -- unless it succeeded and kept the
# RESULT line intact.
SANITIZED_FILE="${FIXTURE_REAL}/report.sanitized.md"
san < "${REPORT}" > "${SANITIZED_FILE}" || {
    echo "FAIL: could not sanitize the report" >&2
    exit 1
}
grep -qxF "RESULT: ${RESULT}" "${SANITIZED_FILE}" || {
    echo "FAIL: the sanitized report lacks its RESULT line" >&2
    exit 1
}
cat "${SANITIZED_FILE}" || {
    echo "FAIL: could not print the sanitized report" >&2
    exit 1
}
if [[ -n "${CENCI_CODEX_NATIVE_OUT:-}" ]]; then
    cp "${SANITIZED_FILE}" "${CENCI_CODEX_NATIVE_OUT}" || echo "WARNING: could not write ${CENCI_CODEX_NATIVE_OUT}" >&2
fi
exit "${RC}"
