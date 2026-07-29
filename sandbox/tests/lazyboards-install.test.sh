#!/usr/bin/env bash
# End-to-end regressions for the installer's optional lazyboards step: the
# release-binary download (checksum-verified, opt-in only), the update-mode
# refresh of an existing install, the doctor report, and drift between the
# board-level actions flow/skills/configure/SKILL.md's 5f template emits and
# the excerpt documented in docs/orchestration.md. The installer itself no
# longer seeds any board config — .lazyboards.yml is fully self-contained and
# generated per repo by /cenci:configure (#716).
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
    # jq is linked for the same reason as the cosign mock below: doctor's jq
    # check is `required` (#784 — check-pending-plans.sh now builds its
    # SessionStart payload exclusively through jq), so without it on PATH
    # doctor hard-fails before the lazyboards step is ever reached.
    for tool in bash cat touch uname grep git mkdir dirname basename ln readlink sleep \
        pkill pgrep nohup chmod sed head rm mktemp cp mv tar gzip cut sha256sum jq; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
exit 0
EOF
    chmod +x "${bin}/docker"
    # cosign mock: doctor's cosign check is now `required` rather than
    # `optional` (#700 — cosign is a hard prerequisite for cenci update's own
    # release-verification bootstrap, not only the piped install path), so
    # every install/update/doctor invocation in this suite needs cosign on
    # PATH or doctor would hard-fail and (for install/update modes) hit the
    # "Continue anyway?" prompt this suite doesn't answer. This suite is
    # about the lazyboards step, not #626's own cosign verification
    # scenarios (installer-clients.test.sh covers those directly), so a bare
    # pass-through is enough — cosign is never actually exercised here since
    # every install.sh invocation runs from $ROOT directly (managed_checkout_dir
    # never matches), never triggering the update-mode bootstrap.
    cat >"${bin}/cosign" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${bin}/cosign"
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
  # Minimal #625/#490 interface-contract guard: this suite is about
  # lazyboards install/verify, not ref pinning, but a regression back to a
  # ref-less `marketplace add` must not pass silently just because this fake
  # never checked. See installer-clients.test.sh's make_claude for the full
  # reference pattern.
  "plugin marketplace add "*)
    case "$4" in
      *@*) exit 0 ;;
      *) printf 'error: plugin marketplace add requires owner/repo@ref (#490)\n' >&2; exit 1 ;;
    esac
    ;;
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
*/matteobortolazzo/lazyboards/releases/latest)
    printf 'https://github.com/matteobortolazzo/lazyboards/releases/tag/v%s' "${FAKE_VER}"
    exit 0
    ;;
*/matteobortolazzo/cenci/releases/latest)
    # Answers install.sh's own resolved-ref resolution (#625), distinct from
    # the lazyboards release probe above (both hit "*/releases/latest") — a
    # fixed default tag, since this suite is about lazyboards install/verify,
    # not #625's own ref-pinning scenarios (installer-clients.test.sh covers
    # those directly).
    printf 'https://github.com/matteobortolazzo/cenci/releases/tag/watch/v1.0.0'
    exit 0
    ;;
*/.claude-plugin/marketplace.json)
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

# make_blocking_lazyboards <home> plants an installed lazyboards binary that
# simulates a build that doesn't recognize --version: instead of answering it
# behaves like the launched TUI and blocks. The sleep is finite so a probe
# regression fails the suite's duration assertion instead of hanging CI.
make_blocking_lazyboards() {
    local home="$1"
    mkdir -p "${home}/.local/bin"
    cat >"${home}/.local/bin/lazyboards" <<'EOF'
#!/bin/sh
exec sleep 300
EOF
    chmod +x "${home}/.local/bin/lazyboards"
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

make_path_lazyboards() {
    local path="$1" ver="$2"
    mkdir -p "$(dirname "${path}")"
    cat >"${path}" <<EOF
#!/bin/sh
if [ "\${1:-}" = --version ]; then
    echo "lazyboards ${ver}"
fi
exit 0
EOF
    chmod +x "${path}"
}

# prepare_checkout provisions the marketplace checkout files the installer
# resolves via find_plugin_path: the cenci CLI.
prepare_checkout() {
    local home="$1" root
    local checkout="${home}/.claude/plugins/marketplaces/cenci"
    mkdir -p "${checkout}"
    if [[ -f "${ROOT}/cenci" ]]; then
        cp "${ROOT}/cenci" "${checkout}/cenci"
        chmod +x "${checkout}/cenci"
    fi
    root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    mkdir -p "${root}/bin" "${root}/.claude-plugin"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/.claude-plugin/plugin.json"
    cat >"${root}/bin/cenci" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${root}/bin/cenci"
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
    env -i HOME="${home}" PATH="${bin}:${home}/.local/bin" \
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

echo "case: --lazyboards installs the checksum-verified binary"
run_installer opt-in --yes --no-build --lazyboards
[[ "${CASE_EXIT}" -eq 0 ]]
[[ -x "${CASE_HOME}/.local/bin/lazyboards" ]]
[[ "$("${CASE_HOME}/.local/bin/lazyboards" --version)" == "lazyboards ${FAKE_VER}" ]]
assert_contains "${CASE_OUTPUT}" "lazyboards v${FAKE_VER} installed"
# Board config is no longer a machine-global install concern (#716) — it's a
# per-repo committed .lazyboards.yml generated by /cenci:configure.
[[ ! -e "${CASE_HOME}/.config/lazyboards/config.yml" ]]
assert_not_contains "${CASE_OUTPUT}" "seeded default board config"
# The final "Try it out" hint always names lazyboards; with no tmux on PATH
# (make_common_tools installs none) it must NOT tell the user to start tmux.
assert_contains "${CASE_OUTPUT}" "kanban board wired to the workflow"
assert_not_contains "${CASE_OUTPUT}" "lazyboards dispatches into tmux windows"

echo "case: installer rejects the removed --cleanup / --no-cleanup flags"
run_installer removed-cleanup-flag --yes --no-build --lazyboards --cleanup
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "unknown option '--cleanup'"
run_installer removed-no-cleanup-flag --yes --no-build --lazyboards --no-cleanup
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "unknown option '--no-cleanup'"

echo "case: the try-it-out hint tells the user to start tmux first when tmux is present"
name=tmux-hint
mkdir -p "${WORK}/${name}/bin"
cat >"${WORK}/${name}/bin/tmux" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "${WORK}/${name}/bin/tmux"
run_installer "${name}" --yes --no-build --lazyboards
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "lazyboards dispatches into tmux windows — start it first"
assert_contains "${CASE_OUTPUT}" "kanban board wired to the workflow"

echo "case: a legacy global board config is left untouched by install"
name=keep-config
mkdir -p "${WORK}/${name}/home/.config/lazyboards"
printf 'columns: [sentinel-user-config]\n' >"${WORK}/${name}/home/.config/lazyboards/config.yml"
run_installer "${name}" --yes --no-build --lazyboards
[[ "${CASE_EXIT}" -eq 0 ]]
[[ "$(cat "${CASE_HOME}/.config/lazyboards/config.yml")" == "columns: [sentinel-user-config]" ]]

echo "case: --yes without --lazyboards never touches the lazyboards release"
run_installer default-skip --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.local/bin/lazyboards" ]]
# Scoped to the lazyboards repo specifically: install.sh's own #625
# ref-resolution now always probes matteobortolazzo/cenci's releases/latest
# (and .claude-plugin/marketplace.json) on every run, unrelated to whether
# lazyboards is selected — only the lazyboards-repo probe must be absent here.
assert_not_contains "${CASE_CURL_LOG}" "matteobortolazzo/lazyboards/releases/latest"
assert_not_contains "${CASE_CURL_LOG}" "releases/download"

echo "case: an unmanaged lazyboards found only on PATH is left alone with a reconcile hint, not silently adopted"
name="path-only-unmanaged"
make_path_lazyboards "${WORK}/${name}/bin/lazyboards" 1.0.0
run_installer "${name}" --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.local/bin/lazyboards" ]]
assert_contains "${CASE_OUTPUT}" "unmanaged lazyboards found at"
assert_contains "${CASE_OUTPUT}" "reconcile it with: cenci update --lazyboards"
# Scoped to the lazyboards repo specifically: install.sh's own #625
# ref-resolution now always probes matteobortolazzo/cenci's releases/latest
# (and .claude-plugin/marketplace.json) on every run, unrelated to whether
# lazyboards is selected — only the lazyboards-repo probe must be absent here.
assert_not_contains "${CASE_CURL_LOG}" "matteobortolazzo/lazyboards/releases/latest"
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

echo "case: rerunning install refreshes an outdated managed lazyboards"
name=install-refresh
make_installed_lazyboards "${WORK}/${name}/home" 1.0.0
run_installer "${name}" --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ "$("${CASE_HOME}/.local/bin/lazyboards" --version)" == "lazyboards ${FAKE_VER}" ]]

echo "case: a PATH-shadowing lazyboards is reported instead of false update success"
name="path-shadow"
make_installed_lazyboards "${WORK}/${name}/home" 1.0.0
make_path_lazyboards "${WORK}/${name}/bin/lazyboards" 1.0.0
run_installer "${name}" update --yes --no-build
[[ "${CASE_EXIT}" -ne 0 ]]
[[ "$("${CASE_HOME}/.local/bin/lazyboards" --version)" == "lazyboards ${FAKE_VER}" ]]
[[ "$("${WORK}/${name}/bin/lazyboards" --version)" == "lazyboards 1.0.0" ]]
assert_contains "${CASE_OUTPUT}" "shadows the managed lazyboards"

echo "case: an existing lazyboards symlink is never followed or overwritten"
name=symlink-target
mkdir -p "${WORK}/${name}/home/.local/bin"
sentinel="${WORK}/${name}/sentinel"
printf 'keep-me\n' >"${sentinel}"
ln -s "${sentinel}" "${WORK}/${name}/home/.local/bin/lazyboards"
run_installer "${name}" --yes --no-build --lazyboards
[[ "${CASE_EXIT}" -ne 0 ]]
[[ "$(cat "${sentinel}")" == keep-me ]]
[[ -L "${CASE_HOME}/.local/bin/lazyboards" ]]

# Regression (#448): a symlinked ~/.local/bin/lazyboards used to be treated as
# a managed install (lazyboards_managed_binary didn't check -L), so a plain
# `update --yes` (no --lazyboards) fell through to install_lazyboards_binary,
# which then permanently hard-failed on every future update because it
# refuses to overwrite a symlinked dest. It must instead be recognized as
# unmanaged and left alone with the same reconcile hint doctor gives.
echo "case: update leaves a symlinked ~/.local/bin/lazyboards untouched with a reconcile hint, not a permanent hard failure"
name=symlink-target-update
target="${WORK}/${name}/target/lazyboards"
make_path_lazyboards "${target}" 1.0.0
mkdir -p "${WORK}/${name}/home/.local/bin"
ln -s "${target}" "${WORK}/${name}/home/.local/bin/lazyboards"
run_installer "${name}" update --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ -L "${CASE_HOME}/.local/bin/lazyboards" ]]
[[ "$(readlink "${CASE_HOME}/.local/bin/lazyboards")" == "${target}" ]]
assert_contains "${CASE_OUTPUT}" "unmanaged lazyboards found at"
assert_contains "${CASE_OUTPUT}" "reconcile it with: cenci update --lazyboards"
# Scoped to the lazyboards repo specifically: install.sh's own #625
# ref-resolution now always probes matteobortolazzo/cenci's releases/latest
# (and .claude-plugin/marketplace.json) on every run, unrelated to whether
# lazyboards is selected — only the lazyboards-repo probe must be absent here.
assert_not_contains "${CASE_CURL_LOG}" "matteobortolazzo/lazyboards/releases/latest"
assert_not_contains "${CASE_CURL_LOG}" "releases/download"

echo "case: update leaves an up-to-date lazyboards alone"
name=update-current
make_installed_lazyboards "${WORK}/${name}/home" "${FAKE_VER}"
run_installer "${name}" update --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "already up to date"
assert_not_contains "${CASE_CURL_LOG}" "releases/download"

echo "case: --no-lazyboards skips an installed lazyboards during update"
name=update-explicit-skip
make_installed_lazyboards "${WORK}/${name}/home" 1.0.0
run_installer "${name}" update --yes --no-build --no-lazyboards
[[ "${CASE_EXIT}" -eq 0 ]]
[[ "$("${CASE_HOME}/.local/bin/lazyboards" --version)" == "lazyboards 1.0.0" ]]
# Scoped to the lazyboards repo specifically: install.sh's own #625
# ref-resolution now always probes matteobortolazzo/cenci's releases/latest
# (and .claude-plugin/marketplace.json) on every run, unrelated to whether
# lazyboards is selected — only the lazyboards-repo probe must be absent here.
assert_not_contains "${CASE_CURL_LOG}" "matteobortolazzo/lazyboards/releases/latest"
assert_not_contains "${CASE_CURL_LOG}" "releases/download"

echo "case: update without lazyboards installed skips the step entirely"
run_installer update-absent update --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.local/bin/lazyboards" ]]
# Scoped to the lazyboards repo specifically: install.sh's own #625
# ref-resolution now always probes matteobortolazzo/cenci's releases/latest
# (and .claude-plugin/marketplace.json) on every run, unrelated to whether
# lazyboards is selected — only the lazyboards-repo probe must be absent here.
assert_not_contains "${CASE_CURL_LOG}" "matteobortolazzo/lazyboards/releases/latest"

echo "case: doctor reports lazyboards state without failing when absent"
run_installer doctor-absent doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "lazyboards not installed"
assert_contains "${CASE_OUTPUT}" "install with: cenci-installer --lazyboards"
assert_contains "${CASE_OUTPUT}" "re-run the installer to create it"
assert_not_contains "${CASE_OUTPUT}" "re-run the installer to seed the default"

echo "case: doctor never hangs on a lazyboards binary that ignores --version"
name=doctor-blocking
make_blocking_lazyboards "${WORK}/${name}/home"
probe_start=${SECONDS}
run_installer "${name}" doctor
probe_elapsed=$((SECONDS - probe_start))
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "lazyboards installed"
if [[ "${probe_elapsed}" -ge 60 ]]; then
    echo "FAIL: doctor took ${probe_elapsed}s — the lazyboards version probe is unbounded again" >&2
    exit 1
fi

echo "case: install preflight labels missing launchers as created by this run, not a re-run"
run_installer preflight-hints --yes --no-build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "cenci-installer utility — created later in this run"
assert_contains "${CASE_OUTPUT}" "cn launcher (cenci open) — created later in this run"
assert_not_contains "${CASE_OUTPUT}" "re-run the installer to create it"

echo "case: doctor reports an installed lazyboards with its version"
name=doctor-present
make_installed_lazyboards "${WORK}/${name}/home" "${FAKE_VER}"
mkdir -p "${WORK}/${name}/home/.config/lazyboards"
touch "${WORK}/${name}/home/.config/lazyboards/config.yml"
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "lazyboards installed (v${FAKE_VER})"
# cenci no longer creates or expects a global board config, so doctor no
# longer announces its presence as a positive signal (#716).
assert_not_contains "${CASE_OUTPUT}" "board config"

echo "case: doctor does not warn about a missing global board config"
name=doctor-no-global-config
make_installed_lazyboards "${WORK}/${name}/home" "${FAKE_VER}"
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_OUTPUT}" "no board config"

echo "case: doctor warns (without failing) when the installed board config still has the vulnerable Checkout PR pattern"
name=doctor-vulnerable-config
make_installed_lazyboards "${WORK}/${name}/home" "${FAKE_VER}"
mkdir -p "${WORK}/${name}/home/.config/lazyboards"
cat >"${WORK}/${name}/home/.config/lazyboards/config.yml" <<'EOF'
columns:
  - name: In Review
    actions:
      W:
        name: Checkout PR
        type: shell
        scope: pr
        command: 'tmux new-window -d -n pr-{pr_number} "git fetch origin {pr_branch} && git switch {pr_branch}"'
EOF
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "Checkout PR action uses the vulnerable git fetch/git switch pattern"
assert_contains "${CASE_OUTPUT}" "update to gh pr checkout {pr_number}"

echo "case: doctor stays clean when the installed board config already uses gh pr checkout"
name=doctor-clean-config
make_installed_lazyboards "${WORK}/${name}/home" "${FAKE_VER}"
mkdir -p "${WORK}/${name}/home/.config/lazyboards"
cat >"${WORK}/${name}/home/.config/lazyboards/config.yml" <<'EOF'
columns:
  - name: In Review
    actions:
      W:
        name: Checkout PR
        type: shell
        scope: pr
        command: 'tmux new-window -d -n pr-{pr_number} "gh pr checkout {pr_number}"'
EOF
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_OUTPUT}" "Checkout PR action uses the vulnerable git fetch/git switch pattern"

echo "case: doctor warns about the vulnerable legacy config even when lazyboards is unmanaged (PATH-only, not cenci-installed)"
name=doctor-vulnerable-config-unmanaged
make_path_lazyboards "${WORK}/${name}/bin/lazyboards" 1.0.0
mkdir -p "${WORK}/${name}/home/.config/lazyboards"
cat >"${WORK}/${name}/home/.config/lazyboards/config.yml" <<'EOF'
columns:
  - name: In Review
    actions:
      W:
        name: Checkout PR
        type: shell
        scope: pr
        command: 'tmux new-window -d -n pr-{pr_number} "git fetch origin {pr_branch} && git switch {pr_branch}"'
EOF
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "unmanaged lazyboards found at"
assert_contains "${CASE_OUTPUT}" "Checkout PR action uses the vulnerable git fetch/git switch pattern"
assert_contains "${CASE_OUTPUT}" "update to gh pr checkout {pr_number}"

# --- Regressions for #406: the 5f per-repo `.lazyboards.yml` template must
# give New/Refined/Planned explicit LOCAL actions instead of claiming bare
# `- name:` entries "inherit" global actions (they don't — a local `columns:`
# list replaces the global list entirely, per lazyboards' documented
# behavior). Scoped to the 5f template region only, since "Designed" and
# "Implemented" legitimately appear elsewhere in SKILL.md's label/lifecycle
# tables.
SKILL_MD="${ROOT}/flow/skills/configure/SKILL.md"
EXTRACTED_5F="${WORK}/skill-5f-template.md"
awk '/^5f\. \*\*Generate `\.lazyboards\.yml`\*\*/{f=1} f && /^6\. \*\*Write `\.cenci\/config\.json`\*\*/{exit} f{print}' \
    "${SKILL_MD}" >"${EXTRACTED_5F}"

# DEDENTED_5F holds the fenced ```yaml block from the 5f region, dedented from
# its 3-space list indent so top-level placement (e.g. the board-level
# `actions:`/`cleanup:` keys) is provable rather than inferred from a
# substring. Exits at the first closing fence, so it captures the main
# template and not the zero-runnable-projects snippet further down.
DEDENTED_5F="${WORK}/skill-5f-dedented.md"
awk '/^   ```yaml$/{f=1;next} f && /^   ```$/{exit} f{sub(/^   /,""); print}' \
    "${EXTRACTED_5F}" >"${DEDENTED_5F}"

echo "case: docs/orchestration.md's board config block matches the C/X and cleanup lines the 5f template emits"
ORCH_BLOCK="${WORK}/orchestration-board-config.yml"
awk '/^## The board config/{s=1} s && /^```yaml$/{f=1; next} f && /^```$/{exit} f{print}' \
    "${ROOT}/docs/orchestration.md" >"${ORCH_BLOCK}"
C_LINE="$(grep -F 'C: { name: Claude' "${DEDENTED_5F}")"
X_LINE="$(grep -F 'X: { name: Codex' "${DEDENTED_5F}")"
CLEANUP_LINE="$(grep -F 'cleanup: "cenci close {number}"' "${DEDENTED_5F}")"
assert_contains "${ORCH_BLOCK}" "${C_LINE}"
assert_contains "${ORCH_BLOCK}" "${X_LINE}"
assert_contains "${ORCH_BLOCK}" "${CLEANUP_LINE}"

echo "case: generated board actions pass shell-escaped worktree paths via tmux -c"
assert_contains "${ROOT}/flow/skills/configure/SKILL.md" \
    "-c {pr_worktree}/'apps/web-client'"
assert_not_contains "${ROOT}/flow/skills/configure/SKILL.md" '"cd {pr_worktree}'

# extract_column <name> — prints the lines of a single generated column's
# block (from its `- name: <name>` line up to, but excluding, the next
# `- name:` line), mirroring the fenced-block extraction already used above
# for docs/orchestration.md's board config parity check.
extract_column() {
    local col="$1"
    awk -v col="${col}" '
        index($0, "- name: " col) > 0 { f=1; print; next }
        f && index($0, "- name:") > 0 { exit }
        f { print }
    ' "${EXTRACTED_5F}"
}

echo "case: 5f template gives the New column a local R (Refine) action"
NEW_COL="${WORK}/skill-5f-col-new.md"
extract_column "New" >"${NEW_COL}"
assert_contains "${NEW_COL}" "name: Refine"
assert_contains "${NEW_COL}" 'command: "cenci run refine {number}"'

echo "case: 5f template gives the Refined column local I (Implement) and gated D (Design) actions"
REFINED_COL="${WORK}/skill-5f-col-refined.md"
extract_column "Refined" >"${REFINED_COL}"
assert_contains "${REFINED_COL}" "name: Implement"
assert_contains "${REFINED_COL}" 'command: "cenci run implement {number}"'
assert_contains "${REFINED_COL}" "name: Design"
assert_contains "${REFINED_COL}" 'command: "cenci run design {number} --no-sandbox"'
assert_contains "${EXTRACTED_5F}" "pencil.enabled"

echo "case: 5f template gives the Planned column local I (Implement), E (Edit plan), and V (View plan) actions"
PLANNED_COL="${WORK}/skill-5f-col-planned.md"
extract_column "Planned" >"${PLANNED_COL}"
assert_contains "${PLANNED_COL}" "name: Implement"
assert_contains "${PLANNED_COL}" 'command: "cenci run implement {number}"'
assert_contains "${PLANNED_COL}" "name: Edit plan"
assert_contains "${PLANNED_COL}" "name: View plan"
assert_contains "${PLANNED_COL}" '.plans/{number}-*.md'

echo "case: 5f template gives In Review no Checkout PR fallback when there are zero runnable projects"
assert_not_contains "${EXTRACTED_5F}" \
    'tmux new-window -d -n pr-{pr_number} "gh pr checkout {pr_number}"'

echo "case: 5f template never re-emits Designed or Implemented as generated columns"
assert_not_contains "${EXTRACTED_5F}" "- name: Designed"
assert_not_contains "${EXTRACTED_5F}" "- name: Implemented"

echo "case: 5f template emits the C/X launch actions at top level, outside \`columns:\`"
grep -q '^actions:$' "${DEDENTED_5F}"
# Long-form `cenci open --agent <agent>`, never a one-token shortcut: the
# shortcut table has exactly one home in docs (watch/README.md's CLI
# reference) and must not be copied into a generated template — the same rule
# setup-skill-content.test.sh enforces for skill content (#790, #800).
assert_contains "${DEDENTED_5F}" 'C: { name: Claude, type: shell, command: "tmux new-window cenci open --agent claude" }'
assert_contains "${DEDENTED_5F}" 'X: { name: Codex, type: shell, command: "tmux new-window cenci open --agent codex" }'

echo "case: 5f template emits a top-level \`cleanup:\` auto-close action"
grep -q '^cleanup: "cenci close {number}"$' "${DEDENTED_5F}"

echo "case: the .lazyboards.yml file-exists conflict check fires unconditionally regardless of prior lazyboards.enabled state"
assert_contains "${EXTRACTED_5F}" \
    "**unconditionally** whenever \`.lazyboards.yml\` already exists at the repo"
assert_contains "${EXTRACTED_5F}" \
    "regardless of any prior \`lazyboards.enabled\` state recorded in"

echo "case: 5f's recommended action set includes the top-level C/X actions and cleanup"
assert_contains "${EXTRACTED_5F}" \
    "board-level \`C\`/\`X\` launch actions and \`cleanup: \"cenci close {number}\"\`"

echo "case: 5f only runs on an in-session Yes answer to question 10, not a stale lazyboards.enabled flag"
Q10_REGION="${WORK}/skill-q10-region.md"
awk '/^### Board Config \(lazyboards\)$/{f=1} f && /^### Auth Verification$/{exit} f{print}' \
    "${SKILL_MD}" >"${Q10_REGION}"
assert_contains "${Q10_REGION}" "in this session"
assert_contains "${EXTRACTED_5F}" "question 10 was asked and answered Yes"
assert_contains "${EXTRACTED_5F}" "in this session"

echo "passed: opt-in install, explicit update skip, checksum gate, doctor, board-config drift guard, safe board actions"
