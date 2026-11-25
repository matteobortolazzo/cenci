#!/usr/bin/env bash
# install.sh argv contract: usage errors exit 2 (die() stays exit 1) and the
# --only <list> plugin-selection flag (parsing, validation, mode restriction,
# help text, piped re-exec forwarding). Host-runnable: mock PATH + fake HOME.
#
# The mocks below are trimmed copies of the ones in installer-clients.test.sh
# (there is no shared mock library, see sandbox/docs/test-harness.md). Their
# interface-contract guards (marketplace `@ref`, cosign verify-blob identity
# and bundle checks, curl URL routing) are kept verbatim: a mock that accepts
# anything would mask a production regression.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)" || exit 1
[[ -n "${WORK}" && -d "${WORK}" ]] || exit 1
trap 'rm -rf "${WORK}"' EXIT

# Sentinel secret: set only in this test's own environment. The harness scrubs
# every subprocess with `env -i`, so make_claude's probe must never see it.
export OPENAI_API_KEY="sentinel-secret-1004"

VALID_NAMES="cenci, cenci-watch, cenci-sandbox"

make_common_tools() {
    local bin="$1"
    mkdir -p "${bin}"
    local tool
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pkill pgrep nohup chmod sed head tail cut tr rm mktemp jq mv sort cp cmp sha256sum; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    # docker mock: `image inspect` fails (no cenci-sandbox:latest yet); every
    # other call succeeds. Nothing here scripts sysbox presence.
    cat > "${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
if [ "${1:-}" = info ] && [ "${2:-}" = --format ]; then
    printf '%s\n' '{"runc":{"path":"runc"}}'
    exit 0
fi
exit 0
EOF
    chmod +x "${bin}/docker"
    # curl mock: every invocation is logged to CURL_LOG, then routed by URL.
    #   */releases/latest — the redirect probe cenci_latest_ref() follows;
    #     answers with a fake .../releases/tag/<CENCI_FAKE_RELEASE_TAG>
    #     redirect (default watch/v1.0.0).
    #   */.claude-plugin/marketplace.json — the per-ref availability probe
    #     ref_available() makes before trusting a ref; succeeds.
    #   */install.sh — the tag's install.sh download; copies the real
    #     checkout's install.sh (ROOT is threaded through explicitly because
    #     piped cases run under a scrubbed env) so a re-exec genuinely
    #     continues the installer from a file.
    #   */install.sh.bundle — the tag's cosign bundle download; a fixed
    #     non-empty placeholder (the cosign mock only checks non-emptiness).
    #   anything else — fails closed, so an unexpected network call is loud.
    cat > "${bin}/curl" <<'EOF'
#!/bin/sh
printf 'curl %s\n' "$*" >>"${CURL_LOG:-/dev/null}"
out= url=
while [ "$#" -gt 0 ]; do
    case "$1" in
      -o) out=$2; shift 2 ;;
      -w) shift 2 ;;
      -*) shift ;;
      *) url=$1; shift ;;
    esac
done
case "${url}" in
*/releases/latest)
    printf 'https://github.com/matteobortolazzo/cenci/releases/tag/%s' "${CENCI_FAKE_RELEASE_TAG:-watch/v1.0.0}"
    exit 0
    ;;
*/.claude-plugin/marketplace.json)
    exit 0
    ;;
*/install.sh)
    [ -n "${out}" ] || exit 1
    cp "${ROOT}/install.sh" "${out}"
    exit 0
    ;;
*/install.sh.bundle)
    [ -n "${out}" ] || exit 1
    printf 'fake-sigstore-bundle-body\n' >"${out}"
    exit 0
    ;;
esac
echo "curl mock: unexpected URL '${url}'" >&2
exit 1
EOF
    chmod +x "${bin}/curl"
    make_cosign "${bin}"
}

# make_cosign <bin> — scriptable cosign mock for the installer's cosign
# verify-blob call. It enforces the real interface contract rather than
# accepting anything: the verify-blob subcommand, a non-empty --bundle file, a
# --certificate-oidc-issuer of https://token.actions.githubusercontent.com, an
# existing target file whose bytes match the known-good $ROOT/install.sh, and a
# --certificate-identity-regexp that matches both canonical watch-release.yml
# identities (tag push, workflow_dispatch from main) while rejecting six decoys.
make_cosign() {
    local bin="$1"
    mkdir -p "${bin}"
    cat > "${bin}/cosign" <<'EOF'
#!/bin/sh
printf 'cosign %s\n' "$*" >>"${COSIGN_LOG:-/dev/null}"
if [ "${COSIGN_MODE:-pass}" = "fail" ]; then
    echo "cosign mock: verify-blob rejected (COSIGN_MODE=fail)" >&2
    exit 1
fi
bundle= identity_regexp= oidc_issuer= subcommand= target=
while [ "$#" -gt 0 ]; do
    case "$1" in
      verify-blob) subcommand=verify-blob; shift ;;
      --bundle) bundle=$2; shift 2 ;;
      --certificate-identity-regexp) identity_regexp=$2; shift 2 ;;
      --certificate-oidc-issuer) oidc_issuer=$2; shift 2 ;;
      -*) shift ;;
      *) target=$1; shift ;;
    esac
done
if [ "${subcommand}" != verify-blob ]; then
    echo "cosign mock: expected verify-blob subcommand, got '${subcommand}'" >&2
    exit 1
fi
if [ -z "${bundle}" ] || [ ! -s "${bundle}" ]; then
    echo "cosign mock: --bundle missing or empty" >&2
    exit 1
fi
if [ -z "${identity_regexp}" ]; then
    echo "cosign mock: --certificate-identity-regexp missing" >&2
    exit 1
fi
if ! printf '%s' "https://github.com/matteobortolazzo/cenci/.github/workflows/watch-release.yml@refs/tags/watch/v1.2.3" | grep -Eq -- "${identity_regexp}"; then
    echo "cosign mock: --certificate-identity-regexp '${identity_regexp}' does not match the tag-push identity (.../watch-release.yml@refs/tags/watch/v1.2.3)" >&2
    exit 1
fi
if ! printf '%s' "https://github.com/matteobortolazzo/cenci/.github/workflows/watch-release.yml@refs/heads/main" | grep -Eq -- "${identity_regexp}"; then
    echo "cosign mock: --certificate-identity-regexp '${identity_regexp}' does not match the workflow_dispatch-from-main identity (.../watch-release.yml@refs/heads/main)" >&2
    exit 1
fi
for decoy in \
    "https://github.com/matteobortolazzo/cenci/.github/workflows/evil.yml@refs/heads/main" \
    "https://github.com/matteobortolazzo/cenci/.github/workflows/watch-release.yml@refs/heads/attacker" \
    "https://github.com/someone-else/other-repo/.github/workflows/watch-release.yml@refs/heads/main" \
    "https://github.com/matteobortolazzo/cenci/.github/workflows/watch-release.yml@refs/heads/main-attacker" \
    "https://github.com/matteobortolazzo/cenci/.github/workflows/watch-release.yml@refs/tags/watch/v1.2.3-evil" \
    "https://github.com/matteobortolazzo/cenci/Xgithub/workflows/watch-releaseXyml@refs/heads/main"; do
    if printf '%s' "${decoy}" | grep -Eq -- "${identity_regexp}"; then
        echo "cosign mock: --certificate-identity-regexp '${identity_regexp}' incorrectly matches decoy identity '${decoy}'" >&2
        exit 1
    fi
done
if [ "${oidc_issuer}" != "https://token.actions.githubusercontent.com" ]; then
    echo "cosign mock: unexpected --certificate-oidc-issuer '${oidc_issuer}'" >&2
    exit 1
fi
if [ -z "${target}" ] || [ ! -f "${target}" ]; then
    echo "cosign mock: target file missing" >&2
    exit 1
fi
if [ -z "${ROOT:-}" ] || ! cmp -s "${target}" "${ROOT}/install.sh"; then
    echo "cosign mock: signature mismatch" >&2
    exit 1
fi
exit 0
EOF
    chmod +x "${bin}/cosign"
}

# make_claude <bin> — fake `claude` executable. Marketplace registration
# enforces the real CLI's interface contract (#490, #625): `plugin marketplace
# add` is only accepted in the `owner/repo@ref` form and is rejected (nonzero,
# no state change) when the ref is missing. CLAUDE_MARKETPLACE_FILE records the
# active ref (`add` writes it, `remove` deletes it); `plugin install`/`update`
# stamp CLAUDE_INSTALLED_FILE-<plugin> with it. Every call is logged to
# CALLS_FILE, plus a leak probe for the sentinel host secret.
make_claude() {
    local bin="$1"
    cat > "${bin}/claude" <<'EOF'
#!/bin/sh
printf 'claude %s\n' "$*" >>"${CALLS_FILE}"
[ -n "${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "${OPENAI_API_KEY}" >>"${CALLS_FILE}"
case "$*" in
  "plugin marketplace list") [ -f "${CLAUDE_MARKETPLACE_FILE}" ] && echo cenci; exit 0 ;;
  "plugin marketplace add "*)
    case "$4" in
      *@*)
        printf '%s' "${4#*@}" >"${CLAUDE_MARKETPLACE_FILE}"
        exit 0
        ;;
      *)
        printf 'error: plugin marketplace add requires owner/repo@ref (#490)\n' >&2
        exit 1
        ;;
    esac
    ;;
  "plugin marketplace update cenci") exit 0 ;;
  "plugin marketplace remove "*)
    rm -f "${CLAUDE_MARKETPLACE_FILE}"
    exit 0
    ;;
  "plugin list")
    for p in cenci cenci-watch cenci-sandbox; do
      [ -f "${CLAUDE_INSTALLED_FILE}-${p}" ] && printf '%s@cenci\n' "${p}"
    done
    exit 0
    ;;
  "plugin install "*)
    p=${3%%@*}
    [ -f "${CLAUDE_MARKETPLACE_FILE}" ] && cp "${CLAUDE_MARKETPLACE_FILE}" "${CLAUDE_INSTALLED_FILE}-${p}" || touch "${CLAUDE_INSTALLED_FILE}-${p}"
    exit 0
    ;;
  "plugin uninstall "*)
    p=${3%%@*}
    rm -f "${CLAUDE_INSTALLED_FILE}-${p}"
    exit 0
    ;;
  "plugin update "*)
    p=${3%%@*}
    [ -f "${CLAUDE_MARKETPLACE_FILE}" ] && cp "${CLAUDE_MARKETPLACE_FILE}" "${CLAUDE_INSTALLED_FILE}-${p}" || touch "${CLAUDE_INSTALLED_FILE}-${p}"
    exit 0
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/claude"
}

prepare_checkout() {
    local home="$1" checkout
    checkout="${home}/.claude/plugins/marketplaces/cenci"
    mkdir -p "${checkout}"
    if [[ -f "${ROOT}/cenci" ]]; then
        cp "${ROOT}/cenci" "${checkout}/cenci"
        chmod +x "${checkout}/cenci"
    fi
    cp "${ROOT}/install.sh" "${checkout}/install.sh"
    # The default-yes tmux-defaults step resolves its shipped asset through
    # find_plugin_path, so the fake checkout must carry it.
    mkdir -p "${checkout}/watch/plugin/tmux"
    cp "${ROOT}/watch/plugin/tmux/tmux.conf" "${checkout}/watch/plugin/tmux/tmux.conf"
}

# prepare_bootstrap_cenci provisions the installed plugin cache without a
# binary. Its bootstrap creates the binary, proving a fresh installer run does
# not depend on a prior agent session or image build to make `cenci` runnable.
prepare_bootstrap_cenci() {
    local home="$1" root bootstrap
    root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    bootstrap="${root}/hooks/bootstrap.sh"
    [[ -e "${root}/.claude-plugin/plugin.json" ]] && return 0
    mkdir -p "${root}/.claude-plugin" "$(dirname "${bootstrap}")"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/.claude-plugin/plugin.json"
    cat >"${bootstrap}" <<EOF
#!/bin/sh
root=\${CLAUDE_PLUGIN_ROOT}
mkdir -p "\${root}/bin"
cat >"\${root}/bin/cenci" <<'BIN'
#!/bin/sh
printf 'cenci %s\n' "\$*" >>"\${CALLS_FILE}"
exit 0
BIN
chmod +x "\${root}/bin/cenci"
EOF
    chmod +x "${bootstrap}"
}

# prepare_case <name> <clients> — sets CASE_DIR/CASE_HOME/CASE_BIN/CASE_CALLS/
# CASE_CURL_LOG/CASE_OUTPUT/CASE_ERR for a fresh case. <clients> is `claude`
# (a mocked Claude Code client on PATH) or `none` (no client at all).
prepare_case() {
    local name="$1" clients="$2"
    CASE_DIR="${WORK}/${name}"
    CASE_HOME="${CASE_DIR}/home"
    CASE_BIN="${CASE_DIR}/bin"
    CASE_CALLS="${CASE_DIR}/calls"
    CASE_CURL_LOG="${CASE_DIR}/curl-log"
    CASE_OUTPUT="${CASE_DIR}/output"
    CASE_ERR="${CASE_DIR}/stderr"
    mkdir -p "${CASE_HOME}" "${CASE_BIN}"
    : >"${CASE_CALLS}"
    : >"${CASE_CURL_LOG}"
    make_common_tools "${CASE_BIN}"
    if [[ "${clients}" == claude ]]; then
        make_claude "${CASE_BIN}"
        prepare_checkout "${CASE_HOME}"
        prepare_bootstrap_cenci "${CASE_HOME}"
    fi
}

# preinstall_plugins <name> <plugin...> — marks the marketplace and each listed
# plugin as already installed for the mocked Claude client.
preinstall_plugins() {
    local name="$1" p
    shift
    mkdir -p "${WORK}/${name}"
    : >"${WORK}/${name}/claude-marketplace"
    for p in "$@"; do
        : >"${WORK}/${name}/claude-installed-${p}"
    done
}

# run_case <name> <clients> [install.sh args...] — one from-file install.sh run
# (`--yes --no-build` always prepended) under a scrubbed `env -i`. Sets
# CASE_EXIT plus the CASE_* paths; stdout and stderr are captured separately.
run_case() {
    local name="$1" clients="$2"
    shift 2
    prepare_case "${name}" "${clients}"
    set +e
    env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
        ROOT="${ROOT}" CURL_LOG="${CASE_CURL_LOG}" \
        CLAUDE_MARKETPLACE_FILE="${CASE_DIR}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${CASE_DIR}/claude-installed" \
        bash "${ROOT}/install.sh" --yes --no-build "$@" >"${CASE_OUTPUT}" 2>"${CASE_ERR}"
    CASE_EXIT=$?
    set -e
}

# run_piped_case <name> <env_extra> [install.sh args...] — simulates
# `curl -fsSL .../main/install.sh | bash -s -- ...`: install.sh is read from
# stdin (no on-disk script path), the shape the piped release-tag pin and its
# re-exec must detect. <env_extra> is a single word-split string of NAME=value
# pairs layered onto the env -i scrub ("" for none). The CWD holds a decoy
# regular file named "bash" so a regressed CWD-relative piped detection would be
# fooled and skip the pin.
run_piped_case() {
    local name="$1" env_extra="$2"
    shift 2
    prepare_case "${name}" claude
    printf 'not a real shell\n' >"${CASE_DIR}/bash"
    set +e
    # shellcheck disable=SC2086 # env_extra is intentionally word-split into
    # zero or more separate NAME=value tokens for env -i.
    (
        cd "${CASE_DIR}" && \
        env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
            ROOT="${ROOT}" CURL_LOG="${CASE_CURL_LOG}" COSIGN_LOG="${CASE_DIR}/cosign-log" \
            CLAUDE_MARKETPLACE_FILE="${CASE_DIR}/claude-marketplace" \
            CLAUDE_INSTALLED_FILE="${CASE_DIR}/claude-installed" \
            ${env_extra} \
            bash -s -- --yes --no-build "$@" <"${ROOT}/install.sh" >"${CASE_OUTPUT}" 2>"${CASE_ERR}"
    )
    CASE_EXIT=$?
    set -e
}

show_file() {
    echo "--- $1 ---" >&2
    sed -n '1,120p' "$1" >&2
}

assert_exit() {
    local want="$1"
    if [[ "${CASE_EXIT}" -ne "${want}" ]]; then
        echo "FAIL: expected exit ${want}, got ${CASE_EXIT}" >&2
        show_file "${CASE_OUTPUT}"
        show_file "${CASE_ERR}"
        exit 1
    fi
}

assert_contains() {
    local file="$1" needle="$2"
    if ! grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: expected '${needle}' in ${file}" >&2
        show_file "${file}"
        exit 1
    fi
}

assert_not_contains() {
    local file="$1" needle="$2"
    [[ -f "${file}" ]] || { echo "FAIL: ${file} does not exist" >&2; exit 1; }
    if grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: did not expect '${needle}' in ${file}" >&2
        show_file "${file}"
        exit 1
    fi
}

# assert_has_line <file> <line> — an entire line of <file> equals <line>.
assert_has_line() {
    local file="$1" line="$2"
    if ! grep -Fxq -- "${line}" "${file}"; then
        echo "FAIL: expected the exact line '${line}' in ${file}" >&2
        show_file "${file}"
        exit 1
    fi
}

# assert_has_line_matching <file> <ERE> — one single line of <file> matches.
assert_has_line_matching() {
    local file="$1" pattern="$2"
    if ! grep -Eq -- "${pattern}" "${file}"; then
        echo "FAIL: expected a line matching /${pattern}/ in ${file}" >&2
        show_file "${file}"
        exit 1
    fi
}

assert_line_count() {
    local file="$1" needle="$2" want="$3" got rc
    got="$(grep -Fc -- "${needle}" "${file}")" && rc=0 || rc=$?
    if [[ "${rc}" -ge 2 ]]; then
        echo "FAIL: grep could not read ${file} (exit ${rc})" >&2
        exit 1
    fi
    if [[ "${got}" -ne "${want}" ]]; then
        echo "FAIL: expected ${want} line(s) containing '${needle}' in ${file}, found ${got}" >&2
        show_file "${file}"
        exit 1
    fi
}

assert_empty() {
    local file="$1"
    [[ -f "${file}" ]] || { echo "FAIL: ${file} does not exist" >&2; exit 1; }
    if [[ -s "${file}" ]]; then
        echo "FAIL: expected ${file} to be empty" >&2
        show_file "${file}"
        exit 1
    fi
}

# expect_usage_error <name> <stderr-needle> <install.sh args...> — a run with a
# mocked Claude client that must be rejected as a usage error: exit 2, the
# message on stderr in die()'s `Error:` format, and no client call made.
expect_usage_error() {
    local name="$1" needle="$2"
    shift 2
    run_case "${name}" claude "$@"
    assert_exit 2
    assert_contains "${CASE_ERR}" "Error: "
    assert_contains "${CASE_ERR}" "${needle}"
    assert_empty "${CASE_CALLS}"
}

# assert_installs_only <calls> <plugin...> — the calls log shows a
# `claude plugin install <p>@cenci` for exactly the listed plugins.
assert_installs_only() {
    local calls="$1" p listed want
    shift
    for p in cenci cenci-watch cenci-sandbox; do
        want=0
        for listed in "$@"; do
            [[ "${listed}" == "${p}" ]] && want=1
        done
        if [[ "${want}" -eq 1 ]]; then
            assert_contains "${calls}" "claude plugin install ${p}@cenci"
        else
            assert_not_contains "${calls}" "claude plugin install ${p}@cenci"
            assert_not_contains "${calls}" "claude plugin update ${p}@cenci"
        fi
    done
}

echo "installer-selection.test.sh"

echo "case 1: an unknown option is a usage error (exit 2) in die()'s stderr format"
expect_usage_error unknown-option "unknown option '--bogus'" --bogus

echo "case 2: --ref without a value is a usage error"
expect_usage_error ref-missing-value "--ref requires a value" --ref

echo "case 3: conflicting host flags are usage errors"
expect_usage_error conflict-ccline "conflicting host ccline flags" --host-ccline --no-host-ccline
expect_usage_error conflict-permissions "conflicting host permissions flags" --host-permissions --no-host-permissions

echo "case 4: --only without a value is a usage error naming the three valid names"
expect_usage_error only-missing-value "--only requires a value" --only
assert_contains "${CASE_ERR}" "${VALID_NAMES}"

echo "case 5: an empty --only value is a usage error naming the three valid names"
expect_usage_error only-equals-empty "empty plugin name" --only=
assert_contains "${CASE_ERR}" "${VALID_NAMES}"
expect_usage_error only-empty-arg "empty plugin name" --only ""
assert_contains "${CASE_ERR}" "${VALID_NAMES}"

echo "case 6: an unknown plugin name is a usage error naming the three valid names"
expect_usage_error only-unknown-name "unknown plugin 'bogus'" --only cenci-watch,bogus
assert_contains "${CASE_ERR}" "${VALID_NAMES}"
expect_usage_error only-unknown-name-equals "unknown plugin 'bogus'" --only=bogus,cenci-watch
assert_contains "${CASE_ERR}" "${VALID_NAMES}"

echo "case 7: empty list elements and spaces around names are usage errors"
expect_usage_error only-trailing-comma "empty plugin name" --only cenci-watch,
assert_contains "${CASE_ERR}" "${VALID_NAMES}"
expect_usage_error only-leading-comma "empty plugin name" --only ,cenci-watch
assert_contains "${CASE_ERR}" "${VALID_NAMES}"
expect_usage_error only-empty-middle "empty plugin name" --only cenci-watch,,cenci
assert_contains "${CASE_ERR}" "${VALID_NAMES}"
expect_usage_error only-space-after-comma "unknown plugin ' cenci'" --only "cenci-watch, cenci"
assert_contains "${CASE_ERR}" "${VALID_NAMES}"

echo "case 8: a list omitting cenci-watch is a usage error that names the reason"
expect_usage_error only-without-watch "ships in cenci-watch" --only cenci,cenci-sandbox
expect_usage_error only-sandbox-alone "ships in cenci-watch" --only=cenci-sandbox

echo "case 9: a repeated --only is a usage error, never silent precedence"
expect_usage_error only-repeated "--only given more than once" --only cenci-watch --only cenci-sandbox,cenci-watch
expect_usage_error only-repeated-forms "--only given more than once" --only=cenci-watch --only cenci-watch

echo "case 10: --only is a usage error in doctor and uninstall, and nothing runs or is removed"
for spec in "doctor-flag-after|doctor|doctor --only cenci-watch" \
            "doctor-flag-before|doctor|--only cenci-watch doctor" \
            "uninstall-equals|uninstall|uninstall --only=cenci-watch"; do
    IFS='|' read -r name mode args <<<"${spec}"
    preinstall_plugins "${name}" cenci cenci-watch cenci-sandbox
    # shellcheck disable=SC2086 # args is intentionally word-split into argv tokens.
    run_case "${name}" claude ${args}
    assert_exit 2
    assert_contains "${CASE_ERR}" "--only is not supported in ${mode} mode"
    assert_empty "${CASE_CALLS}"
    # The claude mock deletes the marker on `plugin uninstall` and the
    # marketplace file on `marketplace remove`, so these also catch a removal
    # that the empty calls log alone would already have flagged.
    for p in cenci cenci-watch cenci-sandbox; do
        [[ -f "${WORK}/${name}/claude-installed-${p}" ]] || { echo "FAIL: ${name} removed ${p}" >&2; exit 1; }
    done
    [[ -f "${WORK}/${name}/claude-marketplace" ]] || { echo "FAIL: ${name} removed the marketplace" >&2; exit 1; }
done

echo "case 11: every valid subset installs exactly the listed plugins, in ALL_PLUGINS order"
run_case subset-watch claude --only cenci-watch
assert_exit 0
assert_installs_only "${CASE_CALLS}" cenci-watch
assert_has_line "${CASE_OUTPUT}" "  Reconciled: cenci-watch"
assert_not_contains "${CASE_CALLS}" "env-leak"

run_case subset-core-watch claude --only=cenci-watch,cenci
assert_exit 0
assert_installs_only "${CASE_CALLS}" cenci cenci-watch
assert_has_line "${CASE_OUTPUT}" "  Reconciled: cenci cenci-watch"

run_case subset-watch-sandbox claude --only cenci-watch,cenci-sandbox
assert_exit 0
assert_installs_only "${CASE_CALLS}" cenci-watch cenci-sandbox
assert_has_line "${CASE_OUTPUT}" "  Reconciled: cenci-watch cenci-sandbox"

run_case subset-all claude --only=cenci,cenci-watch,cenci-sandbox
assert_exit 0
assert_installs_only "${CASE_CALLS}" cenci cenci-watch cenci-sandbox
assert_has_line "${CASE_OUTPUT}" "  Reconciled: cenci cenci-watch cenci-sandbox"

run_case subset-shuffled-duplicate claude --only cenci-sandbox,cenci-watch,cenci-watch
assert_exit 0
assert_installs_only "${CASE_CALLS}" cenci-watch cenci-sandbox
assert_line_count "${CASE_CALLS}" "claude plugin install cenci-watch@cenci" 1
assert_has_line "${CASE_OUTPUT}" "  Reconciled: cenci-watch cenci-sandbox"

echo "case 12: update --only reconciles exactly the listed plugins and leaves unlisted ones alone"
preinstall_plugins update-only-watch cenci cenci-watch cenci-sandbox
run_case update-only-watch claude update --only cenci-watch
assert_exit 0
assert_contains "${CASE_CALLS}" "claude plugin update cenci-watch@cenci"
assert_not_contains "${CASE_CALLS}" "claude plugin update cenci@cenci"
assert_not_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_not_contains "${CASE_CALLS}" "claude plugin update cenci-sandbox@cenci"
assert_not_contains "${CASE_CALLS}" "claude plugin install cenci-sandbox@cenci"
# Update mode runs the sandbox refresh steps through the bootstrap `cenci` mock,
# which logs `cenci sandbox update-plugins --all` / `update-agent --all`; the
# cenci-sandbox gate must skip both for an unlisted cenci-sandbox.
assert_not_contains "${CASE_CALLS}" "cenci sandbox"

echo "case 13: --only survives the piped release-pin re-exec unchanged"
run_piped_case piped-only "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9" --only cenci-watch,cenci-sandbox
assert_exit 0
# The tag's install.sh was downloaded, so the re-exec happened; the parent
# exec()s into it, so every step call below comes from the child. The child
# installing only the listed plugins proves the argument was forwarded.
assert_contains "${CASE_CURL_LOG}" "releases/latest"
assert_contains "${CASE_CURL_LOG}" "/install.sh"
assert_contains "${CASE_CALLS}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"
assert_installs_only "${CASE_CALLS}" cenci-watch cenci-sandbox
assert_has_line "${CASE_OUTPUT}" "  Reconciled: cenci-watch cenci-sandbox"

echo "case 13b: a piped usage error never pays for a network lookup"
run_piped_case piped-only-bogus "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9" --only bogus
assert_exit 2
assert_contains "${CASE_ERR}" "${VALID_NAMES}"
assert_empty "${CASE_CURL_LOG}"
assert_empty "${CASE_CALLS}"

echo "case 14: die() stays the exit-1 path for environment failures"
run_case no-client none
assert_exit 1
assert_contains "${CASE_ERR}" "no supported client found"
run_case no-client-valid-only none --only cenci-watch
assert_exit 1
assert_contains "${CASE_ERR}" "no supported client found"

echo "case 15: --help and the usage header document --only and the mandatory cenci-watch rule"
run_case help claude --help
assert_exit 0
assert_contains "${CASE_OUTPUT}" "--only <list>"
assert_has_line_matching "${CASE_OUTPUT}" "--only.*cenci-watch|cenci-watch.*--only"
assert_has_line_matching "${CASE_OUTPUT}" "cenci-watch.*(mandatory|required)|(mandatory|required).*cenci-watch"
assert_empty "${CASE_CALLS}"
sed -n '1,60p' "${ROOT}/install.sh" >"${WORK}/header.txt"
assert_contains "${WORK}/header.txt" "--only <list>"
assert_has_line_matching "${WORK}/header.txt" "cenci-watch.*(mandatory|required)|(mandatory|required).*cenci-watch"

echo "installer-selection.test.sh: all cases passed"
