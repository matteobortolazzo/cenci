#!/usr/bin/env bash
# End-to-end regressions for client detection and client-aware installer output.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

make_common_tools() {
    local bin="$1"
    mkdir -p "${bin}"
    local tool
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pkill pgrep nohup chmod sed head rm mktemp; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat > "${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
exit 0
EOF
    chmod +x "${bin}/docker"
    cat > "${bin}/curl" <<'EOF'
#!/bin/sh
out=
while [ "$#" -gt 0 ]; do
    case "$1" in
      -o) out=$2; shift 2 ;;
      *) shift ;;
    esac
done
[ -n "${out}" ] || exit 1
cat >"${out}" <<'INSTALLER'
printf 'forwarded installer args: %s\n' "$*"
INSTALLER
EOF
    chmod +x "${bin}/curl"
}

make_claude() {
    local bin="$1"
    cat > "${bin}/claude" <<'EOF'
#!/bin/sh
printf 'claude %s\n' "$*" >>"${CALLS_FILE}"
case "$*" in
  "plugin marketplace list") [ -f "${CLAUDE_MARKETPLACE_FILE}" ] && echo agent-stack; exit 0 ;;
  "plugin marketplace add "*) touch "${CLAUDE_MARKETPLACE_FILE}"; exit 0 ;;
  "plugin list")
    for p in agentflow agentwatch agent-sandbox; do
      [ -f "${CLAUDE_INSTALLED_FILE}-${p}" ] && printf '%s@agent-stack\n' "${p}"
    done
    exit 0
    ;;
  "plugin install "*) p=${3%%@*}; touch "${CLAUDE_INSTALLED_FILE}-${p}"; exit 0 ;;
esac
exit 0
EOF
    chmod +x "${bin}/claude"
}

make_codex() {
    local bin="$1"
    cat > "${bin}/codex" <<'EOF'
#!/bin/sh
printf 'codex %s\n' "$*" >>"${CALLS_FILE}"
case "$*" in
  "plugin marketplace list") [ -f "${CODEX_MARKETPLACE_FILE}" ] && echo 'agent-stack local'; exit 0 ;;
  "plugin marketplace add "*) touch "${CODEX_MARKETPLACE_FILE}"; exit 0 ;;
  "plugin list")
    for p in agentflow agentwatch agent-sandbox; do
      [ -f "${CODEX_INSTALLED_FILE}-${p}" ] && printf '%s@agent-stack installed\n' "${p}"
    done
    exit 0
    ;;
  "plugin add "*) p=${3%%@*}; touch "${CODEX_INSTALLED_FILE}-${p}"; exit 0 ;;
esac
exit 0
EOF
    chmod +x "${bin}/codex"
}

prepare_checkout() {
    local home="$1" client="$2" checkout
    checkout="${home}/.${client}/plugins/marketplaces/agent-stack"
    mkdir -p "${checkout}/dev-sandbox"
    cat > "${checkout}/dev-sandbox/agent-sand" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${checkout}/dev-sandbox/agent-sand"
    if [[ -f "${ROOT}/agent-stack" ]]; then
        cp "${ROOT}/agent-stack" "${checkout}/agent-stack"
        chmod +x "${checkout}/agent-stack"
    fi
}

run_case() {
    local name="$1" clients="$2"
    local build_flag="${3:---no-build}"
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    case "${clients}" in
      claude) make_claude "${bin}"; prepare_checkout "${home}" claude ;;
      codex) make_codex "${bin}"; prepare_checkout "${home}" codex ;;
      dual) make_claude "${bin}"; make_codex "${bin}"; prepare_checkout "${home}" claude ;;
    esac

    set +e
    HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        bash "${ROOT}/install.sh" --yes "${build_flag}" >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
    CASE_BIN="${bin}"
}

run_doctor_case() {
    local name="$1" clients="$2"
    run_case "${name}" "${clients}"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/doctor-output"
    set +e
    HOME="${CASE_HOME}" PATH="${bin}" CALLS_FILE="${CASE_CALLS}" \
        CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${WORK}/${name}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/${name}/codex-installed" \
        bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
    DOCTOR_EXIT=$?
    set -e
    DOCTOR_OUTPUT="${output}"
}

assert_contains() {
    local file="$1" needle="$2"
    if ! grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: expected '${needle}' in ${file}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
}

assert_not_contains() {
    local file="$1" needle="$2"
    if grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: did not expect '${needle}' in ${file}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
}

assert_agent_stack_utility() {
    local output="${CASE_HOME}/agent-stack-output"
    [[ -L "${CASE_HOME}/.local/bin/agent-stack" ]]
    HOME="${CASE_HOME}" PATH="${CASE_BIN}" \
        "${CASE_HOME}/.local/bin/agent-stack" update --yes >"${output}"
    assert_contains "${output}" "forwarded installer args: update --yes"
}

echo "installer-clients.test.sh"

echo "case: Claude-only installs every component and prints only Claude launch guidance"
run_case claude claude
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install agentflow@agent-stack"
assert_contains "${CASE_CALLS}" "claude plugin install agentwatch@agent-stack"
assert_contains "${CASE_CALLS}" "claude plugin install agent-sandbox@agent-stack"
assert_contains "${CASE_OUTPUT}" "agent-sand"
assert_not_contains "${CASE_OUTPUT}" "codex-sand"
assert_agent_stack_utility

echo "case: Codex-only installs every component without invoking or recommending Claude"
run_case codex codex
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "codex plugin add agentflow@agent-stack"
assert_contains "${CASE_CALLS}" "codex plugin add agentwatch@agent-stack"
assert_contains "${CASE_CALLS}" "codex plugin add agent-sandbox@agent-stack"
assert_contains "${CASE_OUTPUT}" "codex-sand"
assert_not_contains "${CASE_OUTPUT}" "agent-sand                # Claude"
assert_not_contains "${CASE_OUTPUT}" "/agentflow:configure"
[[ -L "${CASE_HOME}/.local/bin/codex-sand" ]]
[[ ! -e "${CASE_HOME}/.local/bin/agent-sand" ]]
assert_agent_stack_utility

echo "case: Codex-only image build uses the Codex launcher"
run_case codex-build codex --build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "sandbox image built"
assert_not_contains "${CASE_OUTPUT}" "agent-sand: No such file"

echo "case: dual-client installs independently and exposes both launchers"
run_case dual dual
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install agentflow@agent-stack"
assert_contains "${CASE_CALLS}" "codex plugin add agentflow@agent-stack"
[[ -L "${CASE_HOME}/.local/bin/agent-sand" ]]
[[ -L "${CASE_HOME}/.local/bin/codex-sand" ]]
assert_agent_stack_utility

echo "case: no supported client fails with a client-specific diagnostic"
run_case none none
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "Install Claude Code, Codex, or both"

echo "case: doctor separates clients, components, launchers, and image readiness"
run_doctor_case doctor-codex codex
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Required platform dependencies"
assert_contains "${DOCTOR_OUTPUT}" "Supported clients (at least one required)"
assert_contains "${DOCTOR_OUTPUT}" "Installed stack components"
assert_contains "${DOCTOR_OUTPUT}" "Launchers and container image"
assert_contains "${DOCTOR_OUTPUT}" "Codex: agentflow"
assert_contains "${DOCTOR_OUTPUT}" "agent-stack utility"

echo "case: doctor fails when no supported client is available"
run_doctor_case doctor-none none
[[ "${DOCTOR_EXIT}" -ne 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "no supported client"

echo "passed: client detection, installation, launchers, and summaries"
