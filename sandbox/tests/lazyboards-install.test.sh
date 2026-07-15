#!/usr/bin/env bash
# End-to-end regressions for the installer's optional lazyboards step: the
# release-binary download (checksum-verified, opt-in only), the update-mode
# refresh of an existing install, the seed-if-absent board config, the doctor
# report, and byte-parity between flow/templates/lazyboards-config.yml and the
# config block documented in docs/orchestration.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAKE_VER="9.9.9"

# The archive name must match what install.sh derives from the host platform,
# since the mock curl serves files by basename.
case "$(uname -m)" in
x86_64 | amd64) HOST_ARCH=amd64 ;;
arm64 | aarch64) HOST_ARCH=arm64 ;;
*) HOST_ARCH="$(uname -m)" ;;
esac
case "$(uname -s)" in
Darwin) HOST_GOOS=darwin ;;
*) HOST_GOOS=linux ;;
esac
ARCHIVE="lazyboards_${FAKE_VER}_${HOST_GOOS}_${HOST_ARCH}.tar.gz"

make_common_tools() {
    local bin="$1" tool
    mkdir -p "${bin}"
    for tool in bash cat touch uname grep git mkdir dirname basename ln readlink sleep \
        pkill pgrep nohup chmod sed head rm mktemp cp mv tar gzip cut sha256sum; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
exit 0
EOF
    chmod +x "${bin}/docker"
}

# make_claude installs a claude stub that reports every cenci plugin as
# already installed, so both install and update modes run their post-install
# steps without exercising the plugin-manager paths this suite doesn't test.
make_claude() {
    local bin="$1"
    cat >"${bin}/claude" <<'EOF'
#!/bin/sh
case "$*" in
  "plugin marketplace list") echo cenci; exit 0 ;;
  "plugin list") printf 'cenci@cenci\ncenci-watch@cenci\ncenci-sandbox@cenci\n'; exit 0 ;;
esac
exit 0
EOF
    chmod +x "${bin}/claude"
}

# make_curl installs a curl stub that serves the fake GitHub release: the
# /releases/latest redirect resolve (printed via -w url_effective) and the
# archive/checksums downloads (-o <file> <url>, served by basename from
# RELEASE_DIR). Every invocation's argv is appended to CURL_LOG.
make_curl() {
    local bin="$1"
    cat >"${bin}/curl" <<'EOF'
#!/bin/sh
printf 'curl %s\n' "$*" >>"${CURL_LOG}"
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
    printf 'https://github.com/matteobortolazzo/lazyboards/releases/tag/v%s' "${FAKE_VER}"
    exit 0
    ;;
*/releases/download/*)
    [ -n "${out}" ] || exit 1
    f="${RELEASE_DIR}/$(basename "${url}")"
    [ -f "${f}" ] || exit 22
    cp "${f}" "${out}"
    exit 0
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/curl"
}

# make_release builds the fake GoReleaser release assets: a lazyboards
# "binary" (a script that answers --version with the given version), the
# platform archive, and a checksums.txt covering it.
make_release() {
    local dir="$1" stage
    stage="${WORK}/release-stage"
    rm -rf "${stage}" "${dir}"
    mkdir -p "${stage}" "${dir}"
    cat >"${stage}/lazyboards" <<EOF
#!/bin/sh
if [ "\${1:-}" = --version ] || [ "\${1:-}" = -v ] || [ "\${1:-}" = version ]; then
    echo "lazyboards ${FAKE_VER}"
fi
exit 0
EOF
    chmod +x "${stage}/lazyboards"
    tar -czf "${dir}/${ARCHIVE}" -C "${stage}" lazyboards
    (cd "${dir}" && sha256sum "${ARCHIVE}" >checksums.txt)
}

# make_installed_lazyboards <home> <version> plants an already-installed
# lazyboards binary reporting the given version.
make_installed_lazyboards() {
    local home="$1" ver="$2"
    mkdir -p "${home}/.local/bin"
    cat >"${home}/.local/bin/lazyboards" <<EOF
#!/bin/sh
if [ "\${1:-}" = --version ] || [ "\${1:-}" = -v ] || [ "\${1:-}" = version ]; then
    echo "lazyboards ${ver}"
fi
exit 0
EOF
    chmod +x "${home}/.local/bin/lazyboards"
}

# prepare_checkout provisions the marketplace checkout files the installer
# resolves via find_plugin_path: the cenci CLI and the board config template.
prepare_checkout() {
    local home="$1"
    local checkout="${home}/.claude/plugins/marketplaces/cenci"
    mkdir -p "${checkout}/flow/templates"
    cp "${ROOT}/flow/templates/lazyboards-config.yml" "${checkout}/flow/templates/"
    if [[ -f "${ROOT}/cenci" ]]; then
        cp "${ROOT}/cenci" "${checkout}/cenci"
        chmod +x "${checkout}/cenci"
    fi
}

# run_installer <name> <mode-and-flags...> — fresh HOME + mock PATH, then run
# install.sh with the given arguments under a scrubbed environment.
run_installer() {
    local name="$1"
    shift
    local home="${WORK}/${name}/home" bin="${WORK}/${name}/bin"
    CASE_HOME="${home}"
    CASE_OUTPUT="${WORK}/${name}/output"
    CASE_CURL_LOG="${WORK}/${name}/curl-calls"
    if [[ ! -d "${home}" ]]; then mkdir -p "${home}"; fi
    mkdir -p "${bin}"
    : >"${CASE_CURL_LOG}"
    make_common_tools "${bin}"
    make_claude "${bin}"
    make_curl "${bin}"
    prepare_checkout "${home}"

    set +e
    env -i HOME="${home}" PATH="${bin}" \
        CURL_LOG="${CASE_CURL_LOG}" RELEASE_DIR="${RELEASE_DIR}" FAKE_VER="${FAKE_VER}" \
        bash "${ROOT}/install.sh" "$@" >"${CASE_OUTPUT}" 2>&1
    CASE_EXIT=$?
    set -e
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

RELEASE_DIR="${WORK}/release"
make_release "${RELEASE_DIR}"

echo "lazyboards-install.test.sh"

echo "case: --lazyboards installs the checksum-verified binary and seeds the board config"
run_installer opt-in --yes --no-build --lazyboards
[[ "${CASE_EXIT}" -eq 0 ]]
[[ -x "${CASE_HOME}/.local/bin/lazyboards" ]]
[[ "$("${CASE_HOME}/.local/bin/lazyboards" --version)" == "lazyboards ${FAKE_VER}" ]]
assert_contains "${CASE_OUTPUT}" "lazyboards v${FAKE_VER} installed"
[[ -f "${CASE_HOME}/.config/lazyboards/config.yml" ]]
if ! diff -q "${ROOT}/flow/templates/lazyboards-config.yml" \
    "${CASE_HOME}/.config/lazyboards/config.yml" >/dev/null; then
    echo "FAIL: seeded config differs from flow/templates/lazyboards-config.yml" >&2
    exit 1
fi
assert_contains "${CASE_OUTPUT}" "seeded default board config"

echo "case: an existing board config is never overwritten"
name=keep-config
mkdir -p "${WORK}/${name}/home/.config/lazyboards"
printf 'columns: [sentinel-user-config]\n' >"${WORK}/${name}/home/.config/lazyboards/config.yml"
run_installer "${name}" --yes --no-build --lazyboards
[[ "${CASE_EXIT}" -eq 0 ]]
[[ "$(cat "${CASE_HOME}/.config/lazyboards/config.yml")" == "columns: [sentinel-user-config]" ]]
assert_contains "${CASE_OUTPUT}" "left untouched"

echo "case: --yes without --lazyboards never touches the lazyboards release"
run_installer default-skip --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.local/bin/lazyboards" ]]
assert_not_contains "${CASE_CURL_LOG}" "releases/latest"
assert_not_contains "${CASE_CURL_LOG}" "releases/download"

echo "case: a checksum mismatch fails the step and installs nothing"
sed 's/^[0-9a-f]\{8\}/deadbeef/' "${RELEASE_DIR}/checksums.txt" >"${RELEASE_DIR}/checksums.txt.tmp"
mv "${RELEASE_DIR}/checksums.txt.tmp" "${RELEASE_DIR}/checksums.txt"
run_installer bad-checksum --yes --no-build --lazyboards
[[ "${CASE_EXIT}" -ne 0 ]]
[[ ! -e "${CASE_HOME}/.local/bin/lazyboards" ]]
assert_contains "${CASE_OUTPUT}" "checksum verification failed"
make_release "${RELEASE_DIR}"

echo "case: update refreshes an outdated installed lazyboards"
name=update-refresh
make_installed_lazyboards "${WORK}/${name}/home" 1.0.0
run_installer "${name}" update --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ "$("${CASE_HOME}/.local/bin/lazyboards" --version)" == "lazyboards ${FAKE_VER}" ]]
assert_contains "${CASE_OUTPUT}" "lazyboards v${FAKE_VER} installed"

echo "case: update leaves an up-to-date lazyboards alone"
name=update-current
make_installed_lazyboards "${WORK}/${name}/home" "${FAKE_VER}"
run_installer "${name}" update --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "already up to date"
assert_not_contains "${CASE_CURL_LOG}" "releases/download"

echo "case: update without lazyboards installed skips the step entirely"
run_installer update-absent update --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.local/bin/lazyboards" ]]
assert_not_contains "${CASE_CURL_LOG}" "releases/latest"

echo "case: doctor reports lazyboards state without failing when absent"
run_installer doctor-absent doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "lazyboards not installed"

echo "case: doctor reports an installed lazyboards with its version"
name=doctor-present
make_installed_lazyboards "${WORK}/${name}/home" "${FAKE_VER}"
mkdir -p "${WORK}/${name}/home/.config/lazyboards"
touch "${WORK}/${name}/home/.config/lazyboards/config.yml"
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "lazyboards installed (v${FAKE_VER})"
assert_contains "${CASE_OUTPUT}" "board config present"

echo "case: docs/orchestration.md board config block matches flow/templates/lazyboards-config.yml byte-for-byte"
EXTRACTED="${WORK}/orchestration-config.yml"
awk '/^## The board config/{s=1} s && /^```yaml$/{f=1; next} f && /^```$/{exit} f{print}' \
    "${ROOT}/docs/orchestration.md" >"${EXTRACTED}"
if ! diff -u "${ROOT}/flow/templates/lazyboards-config.yml" "${EXTRACTED}"; then
    echo "FAIL: docs/orchestration.md board config drifted from flow/templates/lazyboards-config.yml" >&2
    exit 1
fi

echo "passed: opt-in install, checksum gate, update refresh, config seeding, doctor, template parity"
