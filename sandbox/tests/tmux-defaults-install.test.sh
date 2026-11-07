#!/usr/bin/env bash
# End-to-end regressions for the installer's tmux-defaults step (#645): the
# managed, sha256-headed copy of watch/plugin/tmux/tmux.conf at
# ${XDG_CONFIG_HOME:-$HOME/.config}/cenci/tmux.conf — install, skip, prompt,
# update refresh, uninstall, doctor, XDG, hash-tool and missing-asset failures,
# and the repo-wide "never source-file'd" grep invariants.
#
# Real install.sh under a mock PATH and a fake HOME (modelled on
# lazyboards-install.test.sh); the interactive prompt runs through a real PTY
# (script -qec, modelled on install-update.test.sh). The real-tmux isolation
# half of #645 lives in tmux-defaults-isolation.test.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)" || { echo "FAIL: could not create a temp dir" >&2; exit 1; }
[[ -n "${WORK}" && -d "${WORK}" ]] || { echo "FAIL: temp dir missing" >&2; exit 1; }
trap 'rm -rf "${WORK}"' EXIT

ASSET="${ROOT}/watch/plugin/tmux/tmux.conf"
HEADER_PREFIX='# cenci-managed tmux defaults — sha256='
[[ -f "${ASSET}" ]] || { echo "FAIL: shipped asset missing: ${ASSET}" >&2; exit 1; }

# Per-case knobs, consumed (and reset) by run_installer / run_installer_pty.
CASE_ASSET=checkout # checkout | cache | empty | none — where find_plugin_path can see the asset (empty: present but zero bytes)
CASE_NO_SHA=0       # 1: neither sha256sum nor shasum on the mock PATH
CASE_SHASUM_ONLY=0  # 1: only a shasum wrapper on the mock PATH
CASE_XDG=""         # non-empty: exported as XDG_CONFIG_HOME
CASE_CORRUPT_CAT=0  # 1: a mock cat that makes the asset's body drift after it was hashed
CASE_NO_WATCH=0     # 1: the mock claude never lists cenci-watch, so the installer prunes it from SELECTED

sha256_of() {
    local out
    if command -v sha256sum >/dev/null 2>&1; then
        out="$(sha256sum)"
    else
        out="$(shasum -a 256)"
    fi
    printf '%s\n' "${out%% *}"
}

make_common_tools() {
    local bin="$1" tool
    mkdir -p "${bin}"
    for tool in bash cat touch uname grep git mkdir dirname basename ln readlink sleep \
        pkill pgrep nohup chmod sed head tail rm rmdir mktemp cp mv tar gzip cut jq; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    if [[ "${CASE_NO_SHA}" -eq 0 ]]; then
        if [[ "${CASE_SHASUM_ONLY}" -eq 1 ]]; then
            # A shasum that ignores its "-a 256" and delegates to the real
            # sha256sum, so the shasum fallback is exercised on Linux hosts.
            local real
            real="$(command -v sha256sum)"
            printf '#!/bin/sh\nexec "%s"\n' "${real}" >"${bin}/shasum"
            chmod +x "${bin}/shasum"
        else
            ln -s "$(command -v sha256sum)" "${bin}/sha256sum"
        fi
    fi
    if [[ "${CASE_CORRUPT_CAT}" -eq 1 ]]; then
        # A cat that appends a stray line when it reads the shipped asset, so the
        # body written after the header is not the body that was hashed.
        rm -f "${bin}/cat"
        local real_cat
        real_cat="$(command -v cat)"
        cat >"${bin}/cat" <<EOF
#!/bin/sh
"${real_cat}" "\$@"
case "\${1:-}" in
*/tmux/tmux.conf) printf '# drifted after hashing\n' ;;
esac
EOF
        chmod +x "${bin}/cat"
    fi
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
exit 0
EOF
    printf '#!/bin/sh\nexit 0\n' >"${bin}/cosign"
    chmod +x "${bin}/docker" "${bin}/cosign"
}

make_claude() {
    local bin="$1"
    cat >"${bin}/claude" <<'EOF'
#!/bin/sh
case "$*" in
  "plugin marketplace list") echo cenci; exit 0 ;;
  "plugin list") printf 'cenci@cenci\ncenci-watch@cenci\ncenci-sandbox@cenci\n'; exit 0 ;;
  "plugin marketplace add "*)
    case "$4" in
      *@*) exit 0 ;;
      *) printf 'error: plugin marketplace add requires owner/repo@ref (#490)\n' >&2; exit 1 ;;
    esac
    ;;
esac
exit 0
EOF
    if [[ "${CASE_NO_WATCH}" -eq 1 ]]; then
        # Drop cenci-watch from the mock's `plugin list` output: the installer's
        # prune_selected_to_installed then removes it from SELECTED, exactly as
        # for a user whose cenci-watch install did not take.
        sed 's/cenci-watch@cenci\\n//' "${bin}/claude" >"${bin}/claude.nowatch"
        mv "${bin}/claude.nowatch" "${bin}/claude"
    fi
    chmod +x "${bin}/claude"
}

make_curl() {
    local bin="$1"
    cat >"${bin}/curl" <<'EOF'
#!/bin/sh
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
    -o | -w) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
    esac
done
case "${url}" in
*/matteobortolazzo/cenci/releases/latest)
    printf 'https://github.com/matteobortolazzo/cenci/releases/tag/watch/v1.0.0'
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/curl"
}

# prepare_checkout provisions the marketplace checkout and plugin cache the
# installer resolves via find_plugin_path. CASE_ASSET decides which branch of
# find_plugin_path can see the shipped tmux asset.
prepare_checkout() {
    local home="$1" checkout root
    checkout="${home}/.claude/plugins/marketplaces/cenci"
    root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    mkdir -p "${checkout}" "${root}/bin" "${root}/.claude-plugin"
    if [[ -f "${ROOT}/cenci" ]]; then
        cp "${ROOT}/cenci" "${checkout}/cenci"
        chmod +x "${checkout}/cenci"
    fi
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/.claude-plugin/plugin.json"
    printf '#!/bin/sh\nexit 0\n' >"${root}/bin/cenci"
    chmod +x "${root}/bin/cenci"
    case "${CASE_ASSET}" in
    checkout)
        mkdir -p "${checkout}/watch/plugin/tmux"
        cp "${ASSET}" "${checkout}/watch/plugin/tmux/tmux.conf"
        ;;
    cache)
        mkdir -p "${root}/tmux"
        cp "${ASSET}" "${root}/tmux/tmux.conf"
        ;;
    empty)
        mkdir -p "${checkout}/watch/plugin/tmux"
        : >"${checkout}/watch/plugin/tmux/tmux.conf"
        ;;
    none) ;;
    esac
}

# setup_case <name> — fresh mock PATH, fake HOME (kept if pre-seeded), checkout.
setup_case() {
    local name="$1"
    CASE_HOME="${WORK}/${name}/home"
    CASE_BIN="${WORK}/${name}/bin"
    CASE_OUTPUT="${WORK}/${name}/output"
    CASE_CONF="${CASE_HOME}/.config/cenci/tmux.conf"
    rm -rf "${CASE_BIN}"
    mkdir -p "${CASE_HOME}" "${CASE_BIN}"
    make_common_tools "${CASE_BIN}"
    make_claude "${CASE_BIN}"
    make_curl "${CASE_BIN}"
    prepare_checkout "${CASE_HOME}"
}

reset_case_knobs() {
    CASE_ASSET=checkout
    CASE_NO_SHA=0
    CASE_SHASUM_ONLY=0
    CASE_XDG=""
    CASE_CORRUPT_CAT=0
    CASE_NO_WATCH=0
}

# run_installer <name> <mode-and-flags...> — scrubbed env, captured output.
run_installer() {
    local name="$1"
    shift
    setup_case "${name}"
    local -a extra=()
    [[ -z "${CASE_XDG}" ]] || extra=("XDG_CONFIG_HOME=${CASE_XDG}")
    set +e
    env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}:${CASE_HOME}/.local/bin" \
        ${extra[@]+"${extra[@]}"} \
        bash "${ROOT}/install.sh" "$@" >"${CASE_OUTPUT}" 2>&1
    CASE_EXIT=$?
    set -e
    reset_case_knobs
}

# run_installer_pty <name> <feed> <args...> — the same, through a real
# controlling terminal. <feed> is interpreted with printf '%b'.
run_installer_pty() {
    local name="$1" feed="$2"
    shift 2
    setup_case "${name}"
    local wrapper="${WORK}/${name}/pty-wrapper.sh" args
    printf -v args '%q ' "$@"
    cat >"${wrapper}" <<EOF
#!/bin/sh
exec env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}:${CASE_HOME}/.local/bin" \\
    bash "${ROOT}/install.sh" ${args}
EOF
    chmod +x "${wrapper}"
    set +e
    printf '%b' "${feed}" | timeout 30 script -qec "${wrapper}" /dev/null >"${CASE_OUTPUT}" 2>&1
    CASE_EXIT=$?
    set -e
    reset_case_knobs
}

expect_exit_ok() {
    if [[ "${CASE_EXIT}" -ne 0 ]]; then
        echo "FAIL: installer exited ${CASE_EXIT}, expected 0; output:" >&2
        sed -n '1,200p' "${CASE_OUTPUT}" >&2
        exit 1
    fi
}

expect_exit_fail() {
    if [[ "${CASE_EXIT}" -eq 0 ]]; then
        echo "FAIL: installer exited 0, expected a non-zero exit; output:" >&2
        sed -n '1,200p' "${CASE_OUTPUT}" >&2
        exit 1
    fi
}

# write_managed <path> <body> — a copy the installer considers managed: valid
# header over <body> (hash of every byte after the first line).
write_managed() {
    local dest="$1" body="$2" hash
    mkdir -p "$(dirname "${dest}")"
    hash="$(printf '%s' "${body}" | sha256_of)"
    {
        printf '%s%s\n' "${HEADER_PREFIX}" "${hash}"
        printf '%s' "${body}"
    } >"${dest}"
}

assert_contains() {
    local file="$1" needle="$2"
    if ! grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: expected '${needle}' in ${file}" >&2
        sed -n '1,200p' "${file}" >&2
        exit 1
    fi
}

assert_not_contains() {
    local file="$1" needle="$2"
    if grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: did not expect '${needle}' in ${file}" >&2
        sed -n '1,200p' "${file}" >&2
        exit 1
    fi
}

assert_same() {
    local want="$1" got="$2" what="$3"
    if ! cmp -s "${want}" "${got}"; then
        echo "FAIL: ${what}: ${got} differs from ${want}" >&2
        exit 1
    fi
}

assert_managed_current() {
    local file="$1" first hash body_hash
    if [[ ! -f "${file}" ]]; then
        echo "FAIL: expected a managed copy at ${file}; last installer output:" >&2
        sed -n '1,200p' "${CASE_OUTPUT}" >&2
        exit 1
    fi
    IFS= read -r first <"${file}"
    if [[ "${first}" != "${HEADER_PREFIX}"* ]] || ! [[ "${first#"${HEADER_PREFIX}"}" =~ ^[0-9a-f]{64}$ ]]; then
        echo "FAIL: ${file}: first line is not a managed header: ${first}" >&2
        exit 1
    fi
    hash="${first#"${HEADER_PREFIX}"}"
    body_hash="$(tail -n +2 "${file}" | sha256_of)"
    if [[ "${hash}" != "${body_hash}" ]]; then
        echo "FAIL: ${file}: header hash ${hash} != body hash ${body_hash}" >&2
        exit 1
    fi
    tail -n +2 "${file}" >"${WORK}/body-compare"
    assert_same "${ASSET}" "${WORK}/body-compare" "installed body vs shipped asset"
}

# path_link_feed prints the (printf '%b') answer for install.sh's earlier Linux
# GUI-bar PATH-link prompt — "n" so the run can never reach sudo — or nothing
# when that prompt does not appear: off Linux, or when a real file (not a
# symlink) already sits at /usr/local/bin/cenci, which the installer leaves
# alone without asking.
path_link_feed() {
    [[ "$(uname -s)" == Linux ]] || return 0
    if [[ -e /usr/local/bin/cenci && ! -L /usr/local/bin/cenci ]]; then
        return 0
    fi
    printf '%s' 'n\n'
}

# keep_copy <file> <name> — snapshot a file's bytes for a later byte-for-byte check.
keep_copy() { cp -P "$1" "${WORK}/snap-$2"; }

echo "tmux-defaults-install.test.sh"

echo "case: fresh non-interactive --tmux-defaults install writes a managed copy whose header hash matches its body (AC1)"
run_installer fresh --yes --no-build --tmux-defaults
expect_exit_ok
[[ -f "${CASE_CONF}" && ! -L "${CASE_CONF}" ]]
assert_managed_current "${CASE_CONF}"
first_line="$(head -n 1 "${CASE_CONF}")"
[[ "${first_line}" =~ ^'# cenci-managed tmux defaults — sha256='[0-9a-f]{64}$ ]]
assert_contains "${CASE_OUTPUT}" "tmux defaults"
# The final summary names the start command when the copy is installed.
assert_contains "${CASE_OUTPUT}" "tmux -L cenci -f \"${CASE_CONF}\" new-session -A -s cenci"
# The installer leaves no staging temp file behind in the config dir.
[[ "$(find "${CASE_HOME}/.config/cenci" -type f | wc -l)" -eq 1 ]]

echo "case: the install never touches ~/.tmux.conf or ~/.config/tmux/tmux.conf (AC2, present and absent)"
name=personal-present
mkdir -p "${WORK}/${name}/home/.config/tmux"
printf 'set -g status-style bg=red\n' >"${WORK}/${name}/home/.tmux.conf"
printf 'set -g mouse off\n' >"${WORK}/${name}/home/.config/tmux/tmux.conf"
keep_copy "${WORK}/${name}/home/.tmux.conf" dot-tmux-conf
keep_copy "${WORK}/${name}/home/.config/tmux/tmux.conf" xdg-tmux-conf
run_installer "${name}" --yes --no-build --tmux-defaults
expect_exit_ok
[[ -f "${CASE_CONF}" ]]
assert_same "${WORK}/snap-dot-tmux-conf" "${CASE_HOME}/.tmux.conf" "dot tmux.conf after install"
assert_same "${WORK}/snap-xdg-tmux-conf" "${CASE_HOME}/.config/tmux/tmux.conf" "XDG tmux.conf after install"
run_installer personal-absent --yes --no-build --tmux-defaults
expect_exit_ok
[[ ! -e "${CASE_HOME}/.tmux.conf" && ! -e "${CASE_HOME}/.config/tmux/tmux.conf" && ! -e "${CASE_HOME}/.config/tmux" ]]

echo "case: --no-tmux-defaults leaves no copy; a non-interactive install with neither flag writes one (AC9)"
run_installer opt-out --yes --no-build --no-tmux-defaults
expect_exit_ok
[[ ! -e "${CASE_CONF}" ]]
assert_not_contains "${CASE_OUTPUT}" "tmux -L cenci"
run_installer default-yes --yes --no-build
expect_exit_ok
assert_managed_current "${CASE_CONF}"

echo "case: the last flag wins — --tmux-defaults --no-tmux-defaults skips, --no-tmux-defaults --tmux-defaults installs"
run_installer last-no --yes --no-build --tmux-defaults --no-tmux-defaults
expect_exit_ok
[[ ! -e "${CASE_CONF}" ]]
run_installer last-yes --yes --no-build --no-tmux-defaults --tmux-defaults
expect_exit_ok
assert_managed_current "${CASE_CONF}"

echo "case: an interactive install shows the tmux-defaults prompt with default y; the default answer writes the copy (AC10)"
pty_feed="$(path_link_feed)\n"
run_installer_pty pty-default "${pty_feed}" --no-build --no-lazyboards
expect_exit_ok
assert_contains "${CASE_OUTPUT}" 'Install cenci'"'"'s tmux defaults (dedicated "cenci" tmux server; your own tmux config is never touched)? [Y/n]'
assert_managed_current "${CASE_CONF}"

echo "case: an interactive install answering n writes no file (AC10)"
pty_feed="$(path_link_feed)n\n"
run_installer_pty pty-decline "${pty_feed}" --no-build --no-lazyboards
expect_exit_ok
assert_contains "${CASE_OUTPUT}" "your own tmux config is never touched)? [Y/n]"
[[ ! -e "${CASE_CONF}" ]]
assert_not_contains "${CASE_OUTPUT}" "tmux -L cenci"

echo "case: the step is skipped when cenci-watch is not among the installed components, even with an explicit --tmux-defaults (boundary)"
CASE_NO_WATCH=1
run_installer no-watch --yes --no-build --tmux-defaults
expect_exit_ok
# Guard against a vacuous pass: the mock must really have hidden cenci-watch
# while leaving the rest of the install running.
assert_not_contains "${CASE_BIN}/claude" "cenci-watch@cenci"
assert_contains "${CASE_OUTPUT}" "Reconciling plugins"
[[ ! -e "${CASE_CONF}" && ! -e "${CASE_HOME}/.config/cenci" ]]
assert_not_contains "${CASE_OUTPUT}" "Setting up tmux defaults"
assert_not_contains "${CASE_OUTPUT}" "tmux defaults:"
assert_not_contains "${CASE_OUTPUT}" "tmux -L cenci"
assert_not_contains "${CASE_OUTPUT}" "skipped — install later with: cenci-installer --tmux-defaults"

echo "case: the shipped asset also resolves through the cenci-watch plugin cache (find_plugin_path cache branch)"
CASE_ASSET=cache
run_installer from-cache --yes --no-build --tmux-defaults
expect_exit_ok
assert_managed_current "${CASE_CONF}"

echo "case: shasum is used when sha256sum is absent"
CASE_SHASUM_ONLY=1
run_installer shasum-only --yes --no-build --tmux-defaults
expect_exit_ok
assert_managed_current "${CASE_CONF}"

echo "case: install refreshes an existing managed, unmodified copy without prompting; a current one is reported up to date"
name=install-refresh
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# stale shipped body\nset -g mouse off\n'
run_installer "${name}" --yes --no-build
expect_exit_ok
assert_managed_current "${CASE_CONF}"
# A second run over the now-current copy (same fake HOME) changes nothing.
keep_copy "${CASE_CONF}" uptodate
run_installer "${name}" --yes --no-build
expect_exit_ok
assert_contains "${CASE_OUTPUT}" "already up to date"
assert_same "${WORK}/snap-uptodate" "${CASE_CONF}" "current managed copy after a second install"

echo "case: update refreshes a managed, unmodified copy whose body differs from the shipped asset (AC11)"
name=update-refresh
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# old shipped body\nset -g mouse off\n'
run_installer "${name}" update --yes --no-build
expect_exit_ok
assert_managed_current "${CASE_CONF}"

echo "case: update with no existing copy and no flag creates no file; update --tmux-defaults creates it (edge)"
run_installer update-none update --yes --no-build
expect_exit_ok
[[ ! -e "${CASE_CONF}" ]]
run_installer update-create update --yes --no-build --tmux-defaults
expect_exit_ok
assert_managed_current "${CASE_CONF}"

echo "case: update --no-tmux-defaults skips the step and keeps an existing managed copy byte-identical"
name=update-optout
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# old shipped body\n'
keep_copy "${WORK}/${name}/home/.config/cenci/tmux.conf" update-optout
run_installer "${name}" update --yes --no-build --no-tmux-defaults
expect_exit_ok
assert_same "${WORK}/snap-update-optout" "${CASE_CONF}" "managed copy after update --no-tmux-defaults"

echo "case: an existing copy without the managed header stays byte-identical across install, update and uninstall, each warning with the path (edge)"
name=unmanaged
mkdir -p "${WORK}/${name}/home/.config/cenci"
printf 'set -g status-style bg=blue\n' >"${WORK}/${name}/home/.config/cenci/tmux.conf"
keep_copy "${WORK}/${name}/home/.config/cenci/tmux.conf" unmanaged
run_installer "${name}" --yes --no-build
expect_exit_ok
assert_same "${WORK}/snap-unmanaged" "${CASE_CONF}" "unmanaged copy after install"
assert_contains "${CASE_OUTPUT}" "${CASE_CONF}"
assert_contains "${CASE_OUTPUT}" "not cenci-managed"
run_installer "${name}" update --yes --no-build
expect_exit_ok
assert_same "${WORK}/snap-unmanaged" "${CASE_CONF}" "unmanaged copy after update"
assert_contains "${CASE_OUTPUT}" "${CASE_CONF}"
assert_contains "${CASE_OUTPUT}" "not cenci-managed"
run_installer "${name}" uninstall --yes
expect_exit_ok
assert_same "${WORK}/snap-unmanaged" "${CASE_CONF}" "unmanaged copy after uninstall"
assert_contains "${CASE_OUTPUT}" "kept ${CASE_CONF}"
assert_not_contains "${CASE_OUTPUT}" "removed ${CASE_CONF}"

echo "case: a managed copy whose body was edited is kept byte-identical by update and uninstall (edge)"
name=modified
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
printf '# my own edit\n' >>"${WORK}/${name}/home/.config/cenci/tmux.conf"
keep_copy "${WORK}/${name}/home/.config/cenci/tmux.conf" modified
run_installer "${name}" update --yes --no-build
expect_exit_ok
assert_same "${WORK}/snap-modified" "${CASE_CONF}" "modified copy after update"
assert_contains "${CASE_OUTPUT}" "modified locally — not refreshed"
assert_contains "${CASE_OUTPUT}" "${CASE_CONF}"
run_installer "${name}" uninstall --yes
expect_exit_ok
assert_same "${WORK}/snap-modified" "${CASE_CONF}" "modified copy after uninstall"
assert_contains "${CASE_OUTPUT}" "kept ${CASE_CONF}"

echo "case: an explicit --tmux-defaults still never overwrites a modified or unmanaged copy, and says to remove it and re-run (edge)"
name=explicit-modified
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
printf '# my own edit\n' >>"${WORK}/${name}/home/.config/cenci/tmux.conf"
keep_copy "${WORK}/${name}/home/.config/cenci/tmux.conf" explicit-modified
run_installer "${name}" --yes --no-build --tmux-defaults
expect_exit_ok
assert_same "${WORK}/snap-explicit-modified" "${CASE_CONF}" "modified copy after explicit --tmux-defaults"
assert_contains "${CASE_OUTPUT}" "remove ${CASE_CONF} and re-run: cenci-installer --tmux-defaults"
name=explicit-unmanaged
mkdir -p "${WORK}/${name}/home/.config/cenci"
printf 'set -g mouse off\n' >"${WORK}/${name}/home/.config/cenci/tmux.conf"
keep_copy "${WORK}/${name}/home/.config/cenci/tmux.conf" explicit-unmanaged
run_installer "${name}" --yes --no-build --tmux-defaults
expect_exit_ok
assert_same "${WORK}/snap-explicit-unmanaged" "${CASE_CONF}" "unmanaged copy after explicit --tmux-defaults"
assert_contains "${CASE_OUTPUT}" "remove ${CASE_CONF} and re-run: cenci-installer --tmux-defaults"

echo "case: a symlinked cenci/tmux.conf is unmanaged — never followed, replaced or deleted"
name=symlink
mkdir -p "${WORK}/${name}/home/.config/cenci"
write_managed "${WORK}/${name}/user-real.conf" $'# a managed-looking body\n'
keep_copy "${WORK}/${name}/user-real.conf" symlink-target
ln -s "${WORK}/${name}/user-real.conf" "${WORK}/${name}/home/.config/cenci/tmux.conf"
run_installer "${name}" --yes --no-build --tmux-defaults
expect_exit_ok
[[ -L "${CASE_CONF}" && "$(readlink "${CASE_CONF}")" == "${WORK}/${name}/user-real.conf" ]]
assert_same "${WORK}/snap-symlink-target" "${WORK}/${name}/user-real.conf" "symlink target after install"
run_installer "${name}" update --yes --no-build
expect_exit_ok
assert_contains "${CASE_OUTPUT}" "${CASE_CONF} is not cenci-managed"
[[ -L "${CASE_CONF}" ]]
assert_same "${WORK}/snap-symlink-target" "${WORK}/${name}/user-real.conf" "symlink target after update"
run_installer "${name}" uninstall --yes
expect_exit_ok
assert_contains "${CASE_OUTPUT}" "kept ${CASE_CONF} (not cenci-managed)"
[[ -L "${CASE_CONF}" ]]
assert_same "${WORK}/snap-symlink-target" "${WORK}/${name}/user-real.conf" "symlink target after uninstall"

echo "case: uninstall --yes removes a managed, unmodified copy, prints 'removed <path>', and previews the path (AC12)"
name=uninstall
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
run_installer "${name}" uninstall --yes
expect_exit_ok
[[ ! -e "${CASE_CONF}" ]]
assert_contains "${CASE_OUTPUT}" "removed ${CASE_CONF}"
assert_contains "${CASE_OUTPUT}" "This will remove:"
assert_contains "${CASE_OUTPUT}" "• ${CASE_CONF} (cenci-managed tmux defaults)"

echo "case: the uninstall preview lists neither an unmanaged copy nor an absent one"
run_installer uninstall-absent uninstall --yes
expect_exit_ok
# Positive control: the uninstall ran through to its end (past the tmux-defaults
# step), so the missing bullet and report are real omissions.
assert_contains "${CASE_OUTPUT}" "This will remove:"
assert_contains "${CASE_OUTPUT}" "cenci has been uninstalled."
assert_not_contains "${CASE_OUTPUT}" "removed ${CASE_CONF}"
assert_not_contains "${CASE_OUTPUT}" "kept ${CASE_CONF}"
assert_not_contains "${CASE_OUTPUT}" "cenci-managed tmux defaults"
run_installer "unmanaged" uninstall --yes
expect_exit_ok
# Positive control: this run reached the tmux-defaults uninstall step and kept
# the file, so the missing preview bullet is a real omission.
assert_contains "${CASE_OUTPUT}" "kept ${CASE_CONF} (not cenci-managed)"
assert_not_contains "${CASE_OUTPUT}" "cenci-managed tmux defaults"

echo "case: uninstall --yes --no-tmux-defaults keeps a managed, unmodified copy byte-identical (edge)"
name=uninstall-keep
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
keep_copy "${WORK}/${name}/home/.config/cenci/tmux.conf" uninstall-keep
run_installer "${name}" uninstall --yes --no-tmux-defaults
expect_exit_ok
assert_same "${WORK}/snap-uninstall-keep" "${CASE_CONF}" "managed copy after uninstall --no-tmux-defaults"
assert_contains "${CASE_OUTPUT}" "kept ${CASE_CONF}"
assert_not_contains "${CASE_OUTPUT}" "cenci-managed tmux defaults)"

echo "case: uninstall removes the cenci config dir when tmux.conf and config.json were its only files, and keeps it when a kept tmux.conf remains (edge)"
name=uninstall-dir
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
printf '{}\n' >"${WORK}/${name}/home/.config/cenci/config.json"
run_installer "${name}" uninstall --yes
expect_exit_ok
[[ ! -e "${CASE_HOME}/.config/cenci" ]]
name=uninstall-dir-kept
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
printf '# my own edit\n' >>"${WORK}/${name}/home/.config/cenci/tmux.conf"
printf '{}\n' >"${WORK}/${name}/home/.config/cenci/config.json"
run_installer "${name}" uninstall --yes
expect_exit_ok
[[ -f "${CASE_CONF}" && ! -e "${CASE_HOME}/.config/cenci/config.json" ]]

echo "case: with XDG_CONFIG_HOME set, the copy is written to and removed from \$XDG_CONFIG_HOME/cenci, never under \$HOME/.config/cenci (edge)"
xdg="${WORK}/xdg-case/xdg"
mkdir -p "${xdg}"
CASE_XDG="${xdg}"
run_installer xdg-case --yes --no-build --tmux-defaults
expect_exit_ok
assert_managed_current "${xdg}/cenci/tmux.conf"
[[ ! -e "${CASE_HOME}/.config/cenci" ]]
assert_contains "${CASE_OUTPUT}" "tmux -L cenci -f \"${xdg}/cenci/tmux.conf\""
CASE_XDG="${xdg}"
run_installer xdg-case uninstall --yes
expect_exit_ok
[[ ! -e "${xdg}/cenci/tmux.conf" ]]
assert_contains "${CASE_OUTPUT}" "removed ${xdg}/cenci/tmux.conf"
[[ ! -e "${CASE_HOME}/.config/cenci" ]]

echo "case: neither sha256sum nor shasum — the step fails naming both tools, writes no file, and the installer exits non-zero (edge)"
CASE_NO_SHA=1
run_installer no-hash-tool --yes --no-build --tmux-defaults
expect_exit_fail
[[ ! -e "${CASE_CONF}" ]]
assert_contains "${CASE_OUTPUT}" "sha256sum"
assert_contains "${CASE_OUTPUT}" "shasum"
[[ ! -e "${CASE_HOME}/.config/cenci" || -z "$(find "${CASE_HOME}/.config/cenci" -type f)" ]]

echo "case: an unresolvable shipped asset fails the step naming watch/plugin/tmux/tmux.conf and writes no file (edge)"
CASE_ASSET=none
run_installer no-asset --yes --no-build --tmux-defaults
expect_exit_fail
[[ ! -e "${CASE_CONF}" ]]
assert_contains "${CASE_OUTPUT}" "watch/plugin/tmux/tmux.conf"

echo "case: an empty shipped asset is treated as unresolvable — the step fails naming watch/plugin/tmux/tmux.conf and writes no file (edge)"
CASE_ASSET=empty
run_installer empty-asset --yes --no-build --tmux-defaults
expect_exit_fail
[[ ! -e "${CASE_CONF}" ]]
[[ ! -e "${CASE_HOME}/.config/cenci" || -z "$(find "${CASE_HOME}/.config/cenci" -type f)" ]]
assert_contains "${CASE_OUTPUT}" "tmux defaults: could not find the shipped watch/plugin/tmux/tmux.conf"

echo "case: a staged copy whose body drifted from its header hash never lands — the step fails and leaves no file or temp file (edge)"
CASE_CORRUPT_CAT=1
run_installer drifted-asset --yes --no-build --tmux-defaults
expect_exit_fail
[[ ! -e "${CASE_CONF}" ]]
[[ ! -e "${CASE_HOME}/.config/cenci" || -z "$(find "${CASE_HOME}/.config/cenci" -type f)" ]]
assert_contains "${CASE_OUTPUT}" "staged copy failed its integrity check"

echo "case: doctor prints exactly one tmux-defaults status line per state and its exit code is unchanged (AC13)"
run_installer doctor-absent doctor
doctor_exit="${CASE_EXIT}"
# The mock environment is healthy enough that doctor exits 0, so comparing the
# other states against it proves the tmux-defaults line never changes the exit.
if [[ "${doctor_exit}" -ne 0 ]]; then
    echo "FAIL: baseline doctor run exited ${doctor_exit}, expected 0; output:" >&2
    sed -n '1,200p' "${CASE_OUTPUT}" >&2
    exit 1
fi
[[ "$(grep -c 'tmux defaults' "${CASE_OUTPUT}" || true)" -eq 1 ]]
assert_contains "${CASE_OUTPUT}" "tmux defaults not installed — install with: cenci-installer --tmux-defaults"
name=doctor-managed
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq "${doctor_exit}" ]]
[[ "$(grep -c 'tmux defaults' "${CASE_OUTPUT}" || true)" -eq 1 ]]
assert_contains "${CASE_OUTPUT}" "tmux defaults installed"
name=doctor-modified
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
printf '# my own edit\n' >>"${WORK}/${name}/home/.config/cenci/tmux.conf"
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq "${doctor_exit}" ]]
[[ "$(grep -c 'tmux defaults' "${CASE_OUTPUT}" || true)" -eq 1 ]]
assert_contains "${CASE_OUTPUT}" "tmux defaults modified locally — not refreshed by updates"
name=doctor-unmanaged
mkdir -p "${WORK}/${name}/home/.config/cenci"
printf 'set -g mouse off\n' >"${WORK}/${name}/home/.config/cenci/tmux.conf"
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq "${doctor_exit}" ]]
[[ "$(grep -c 'tmux defaults' "${CASE_OUTPUT}" || true)" -eq 1 ]]
assert_contains "${CASE_OUTPUT}" "tmux defaults present but not cenci-managed"
keep_copy "${CASE_CONF}" doctor-unmanaged
assert_same "${WORK}/snap-doctor-unmanaged" "${CASE_CONF}" "doctor must not touch the copy"

echo "case: doctor degrades to one non-failing line when a header is present but no hash tool exists"
name=doctor-no-hash
write_managed "${WORK}/${name}/home/.config/cenci/tmux.conf" $'# shipped body\n'
CASE_NO_SHA=1
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq "${doctor_exit}" ]]
[[ "$(grep -c 'tmux defaults' "${CASE_OUTPUT}" || true)" -eq 1 ]]
assert_contains "${CASE_OUTPUT}" "tmux defaults present — integrity not verifiable (neither sha256sum nor shasum found)"

echo "case: the install-mode preflight adapts the not-installed hint to how the step will resolve"
run_installer preflight-yes --yes --no-build
expect_exit_ok
assert_contains "${CASE_OUTPUT}" "tmux defaults not installed — installed later in this run"
run_installer preflight-no --yes --no-build --no-tmux-defaults
expect_exit_ok
# Positive control: the preflight did print the tmux-defaults line, with the
# plain install hint rather than the "later in this run" wording.
assert_contains "${CASE_OUTPUT}" "tmux defaults not installed — install with: cenci-installer --tmux-defaults"
assert_not_contains "${CASE_OUTPUT}" "installed later in this run"

echo "case: doctor notes a personal tmux config that references @cenci-* variables, naming the cenci server, and never edits it (AC14)"
name=doctor-personal
mkdir -p "${WORK}/${name}/home"
printf 'set -g window-status-format "#{@cenci-symbol} #W"\n' >"${WORK}/${name}/home/.tmux.conf"
keep_copy "${WORK}/${name}/home/.tmux.conf" doctor-personal
run_installer "${name}" doctor
[[ "${CASE_EXIT}" -eq "${doctor_exit}" ]]
assert_contains "${CASE_OUTPUT}" "tmux -L cenci"
assert_contains "${CASE_OUTPUT}" "#646"
assert_same "${WORK}/snap-doctor-personal" "${CASE_HOME}/.tmux.conf" "personal ~/.tmux.conf after doctor"
run_installer doctor-no-personal doctor
[[ "${CASE_EXIT}" -eq "${doctor_exit}" ]]
# Positive control: doctor ran through the tmux section (its status line is
# there), so the missing #646 note is a real omission.
[[ "$(grep -c 'tmux defaults' "${CASE_OUTPUT}" || true)" -eq 1 ]]
assert_not_contains "${CASE_OUTPUT}" "#646"

echo "case: --help lists --tmux-defaults / --no-tmux-defaults (AC15)"
help_out="${WORK}/help-output"
bash "${ROOT}/install.sh" --help >"${help_out}" 2>&1
assert_contains "${help_out}" "--tmux-defaults / --no-tmux-defaults"

echo "case: the shipped asset contains no source-file or run-shell directive (AC8a)"
if grep -En 'source-file|run-shell' "${ASSET}"; then
    echo "FAIL: shipped asset mentions source-file or run-shell" >&2
    exit 1
fi
# No plugin-manager style non-comment directive either (run, source, tpm).
if grep -Ev '^[[:space:]]*#' "${ASSET}" | grep -Eq '^[[:space:]]*(run|run-shell|source|source-file)([[:space:]]|$)|tpm'; then
    echo "FAIL: shipped asset has a run/source/plugin-manager directive" >&2
    exit 1
fi

echo "case: no line in install.sh, docs/**, watch/README.md or watch/internal/** has source-file together with cenci/tmux.conf (AC8b)"
(
    cd "${ROOT}"
    grep -rIn 'cenci/tmux.conf' install.sh docs watch/README.md watch/internal >"${WORK}/conf-mentions" || true
)
[[ -s "${WORK}/conf-mentions" ]] # the scan must see the doc/installer mentions, or it proves nothing
if grep -F 'source-file' "${WORK}/conf-mentions"; then
    echo "FAIL: a line pairs source-file with cenci/tmux.conf" >&2
    exit 1
fi
(
    cd "${ROOT}"
    grep -rIn 'source-file' install.sh docs watch/README.md watch/internal >"${WORK}/source-file-mentions" || true
)
if grep -F 'cenci/tmux.conf' "${WORK}/source-file-mentions"; then
    echo "FAIL: a source-file line mentions cenci/tmux.conf" >&2
    exit 1
fi

echo "passed: tmux defaults install, skip, prompt, update, uninstall, doctor, XDG, failure and grep-invariant paths"
