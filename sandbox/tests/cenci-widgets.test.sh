#!/usr/bin/env bash
# End-to-end regressions for the installer's GUI bar-widget wiring: detect each
# present desktop bar, delegate to its self-contained install.sh (install +
# reload), and prove `cenci update` re-runs the reload so widget changes
# become visible. Uses the mock-PATH + fake-HOME pattern (no real desktop).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

GNOME_UUID="cenci@matteobortolazzo.github.io"
PLASMA_ID="com.github.matteobortolazzo.cenci"

# link_tools <bin> — real coreutils the installer + widget scripts need, plus
# neutralized docker/curl/sudo and a no-op logging pkill (so widget reloads that
# kill+respawn take the respawn path instead of failing on "nothing to kill").
link_tools() {
    local bin="$1" tool
    mkdir -p "${bin}"
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pgrep nohup chmod sed head rm mktemp cp; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
exit 0
EOF
    cat >"${bin}/curl" <<'EOF'
#!/bin/sh
exit 0
EOF
    cat >"${bin}/sudo" <<'EOF'
#!/bin/sh
exit 0
EOF
    cat >"${bin}/pkill" <<'EOF'
#!/bin/sh
printf 'pkill %s\n' "$*" >>"${WIDGET_CALLS}"
exit 0
EOF
    chmod +x "${bin}/docker" "${bin}/curl" "${bin}/sudo" "${bin}/pkill"
}

# make_claude reports cenci-watch (only) installed and the marketplace registered,
# so the installer reaches step_cenci_watch_setup with cenci-watch selected.
make_claude() {
    local bin="$1"
    cat >"${bin}/claude" <<'EOF'
#!/bin/sh
# Regression probe (#353): if a host secret survives into this subprocess's
# environment, surface it in the captured calls so the sentinel-secret case
# can prove this test harness's env -i scrub keeps host secrets out.
[ -n "${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "${OPENAI_API_KEY}" >>"${WIDGET_CALLS}"
[ -n "${CONTEXT7_API_KEY:-}" ] && printf 'env-leak CONTEXT7_API_KEY=%s\n' "${CONTEXT7_API_KEY}" >>"${WIDGET_CALLS}"
case "$*" in
  "plugin marketplace list") echo cenci ;;
  "plugin list") echo 'cenci-watch@cenci' ;;
esac
exit 0
EOF
    chmod +x "${bin}/claude"
}

# make_logging_stub creates a PATH command that appends its name + argv to the
# call log and succeeds — used for every bar-detection and reload tool.
make_logging_stub() {
    local bin="$1" name="$2"
    cat >"${bin}/${name}" <<EOF
#!/bin/sh
printf '%s %s\n' "${name}" "\$*" >>"\${WIDGET_CALLS}"
exit 0
EOF
    chmod +x "${bin}/${name}"
}

# prepare_checkout builds a fake marketplace checkout holding the real widget
# dirs + their install.sh scripts, where find_plugin_path resolves them.
prepare_checkout() {
    local home="$1" checkout de
    checkout="${home}/.claude/plugins/marketplaces/cenci"
    mkdir -p "${checkout}/watch/plugin"
    for de in gnome plasma dms noctalia; do
        cp -R "${ROOT}/watch/plugin/${de}" "${checkout}/watch/plugin/${de}"
    done
    # step_cli_setup hard-fails when the cenci CLI is missing from the
    # checkout, so provision it like the real marketplace clone does.
    cp "${ROOT}/cenci" "${ROOT}/install.sh" "${checkout}/"
    chmod +x "${checkout}/cenci" "${checkout}/install.sh"
}

run_install() {
    local home="$1" bin="$2" calls="$3" out="$4"
    shift 4
    set +e
    env -i HOME="${home}" PATH="${bin}" WIDGET_CALLS="${calls}" \
        bash "${ROOT}/install.sh" "$@" >"${out}" 2>&1
    local rc=$?
    set -e
    return "${rc}"
}

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_log() { grep -q -- "$2" "$1" || fail "expected '$2' in $1"; }
assert_no_log() { ! grep -q -- "$2" "$1" || fail "did not expect '$2' in $1"; }
assert_out() { grep -Fq -- "$2" "$1" || { sed -n '1,200p' "$1" >&2; fail "expected '$2' in output"; }; }

echo "cenci-widgets.test.sh"

# ---------------------------------------------------------------------------
echo "case: install detects every bar, installs each widget, and reloads it"
CASE="${WORK}/all"; HOME_DIR="${CASE}/home"; BIN="${CASE}/bin"
CALLS="${CASE}/calls"; OUT="${CASE}/out"
mkdir -p "${HOME_DIR}" "${BIN}"; : >"${CALLS}"
link_tools "${BIN}"
make_claude "${BIN}"
prepare_checkout "${HOME_DIR}"
for s in gnome-shell gnome-extensions glib-compile-schemas plasmashell kquitapp6 \
    kstart dms systemctl noctalia-shell qs; do
    make_logging_stub "${BIN}" "${s}"
done
run_install "${HOME_DIR}" "${BIN}" "${CALLS}" "${OUT}" --yes --no-build ||
    fail "install exited non-zero"
sleep 0.5

# gnome: copied into the extensions dir (a copy, not a symlink), schema compiled,
# extension live-reloaded via disable→enable.
GNOME_DEST="${HOME_DIR}/.local/share/gnome-shell/extensions/${GNOME_UUID}"
[[ -f "${GNOME_DEST}/extension.js" && ! -L "${GNOME_DEST}" ]] ||
    fail "gnome extension was not copied into place"
assert_log "${CALLS}" "glib-compile-schemas "
assert_log "${CALLS}" "gnome-extensions enable"
# plasma: symlinked to the checkout, plasmashell reloaded.
[[ -L "${HOME_DIR}/.local/share/plasma/plasmoids/${PLASMA_ID}" ]] ||
    fail "plasma plasmoid symlink missing"
assert_log "${CALLS}" "kstart plasmashell"
# dms: symlinked, service restarted.
[[ -L "${HOME_DIR}/.config/DankMaterialShell/plugins/cenci" ]] ||
    fail "dms plugin symlink missing"
assert_log "${CALLS}" "systemctl --user restart dms"
# noctalia: symlinked, shell respawned.
[[ -L "${HOME_DIR}/.config/noctalia/plugins/cenci" ]] ||
    fail "noctalia plugin symlink missing"
assert_log "${CALLS}" "qs -c noctalia-shell"
echo "  ok: all four widgets installed and reloaded"

# ---------------------------------------------------------------------------
echo "case: update re-runs each reload (widget changes become visible)"
# `update` (unlike install) hard-fails when no cenci binary can be
# provisioned, so give the fake HOME the version-pinned plugin cache an
# installed system would have; its fake binary's `daemon restart` succeeds.
CACHE_ROOT="${HOME_DIR}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
mkdir -p "${CACHE_ROOT}/bin" "${CACHE_ROOT}/.claude-plugin"
printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${CACHE_ROOT}/.claude-plugin/plugin.json"
printf '#!/bin/sh\nexit 0\n' >"${CACHE_ROOT}/bin/cenci"
chmod +x "${CACHE_ROOT}/bin/cenci"
UPCALLS="${CASE}/upcalls"; UPOUT="${CASE}/upout"; : >"${UPCALLS}"
run_install "${HOME_DIR}" "${BIN}" "${UPCALLS}" "${UPOUT}" update --yes ||
    fail "update exited non-zero"
sleep 0.5
assert_log "${UPCALLS}" "gnome-extensions enable"
assert_log "${UPCALLS}" "kstart plasmashell"
assert_log "${UPCALLS}" "systemctl --user restart dms"
assert_log "${UPCALLS}" "qs -c noctalia-shell"
echo "  ok: update reloaded every bar again"

# ---------------------------------------------------------------------------
echo "case: only the detected bar is touched (no false-positive installs)"
CASE2="${WORK}/gate"; H2="${CASE2}/home"; B2="${CASE2}/bin"
C2="${CASE2}/calls"; O2="${CASE2}/out"
mkdir -p "${H2}" "${B2}"; : >"${C2}"
link_tools "${B2}"
make_claude "${B2}"
prepare_checkout "${H2}"
for s in plasmashell kquitapp6 kstart; do make_logging_stub "${B2}" "${s}"; done
run_install "${H2}" "${B2}" "${C2}" "${O2}" --yes --no-build ||
    fail "install (gated) exited non-zero"
sleep 0.2
[[ -L "${H2}/.local/share/plasma/plasmoids/${PLASMA_ID}" ]] ||
    fail "plasma widget should have been installed"
[[ ! -e "${H2}/.local/share/gnome-shell/extensions/${GNOME_UUID}" ]] ||
    fail "gnome widget installed without gnome present"
[[ ! -e "${H2}/.config/DankMaterialShell/plugins/cenci" ]] ||
    fail "dms widget installed without dms present"
[[ ! -e "${H2}/.config/noctalia/plugins/cenci" ]] ||
    fail "noctalia widget installed without noctalia present"
assert_no_log "${C2}" "gnome-extensions"
assert_no_log "${C2}" "systemctl --user restart dms"
assert_no_log "${C2}" "qs -c noctalia-shell"
echo "  ok: undetected bars neither installed nor reloaded"

# ---------------------------------------------------------------------------
echo "case: waybar prints guidance only (no widget files written)"
CASE3="${WORK}/waybar"; H3="${CASE3}/home"; B3="${CASE3}/bin"
C3="${CASE3}/calls"; O3="${CASE3}/out"
mkdir -p "${H3}" "${B3}"; : >"${C3}"
link_tools "${B3}"
make_claude "${B3}"
prepare_checkout "${H3}"
make_logging_stub "${B3}" waybar
run_install "${H3}" "${B3}" "${C3}" "${O3}" --yes --no-build ||
    fail "install (waybar) exited non-zero"
assert_out "${O3}" "waybar detected"
assert_out "${O3}" "pkill -SIGUSR2 waybar"
[[ ! -e "${H3}/.local/share/gnome-shell/extensions/${GNOME_UUID}" ]] ||
    fail "waybar path wrote a gnome widget"
[[ ! -e "${H3}/.local/share/plasma/plasmoids/${PLASMA_ID}" ]] ||
    fail "waybar path wrote a plasma widget"
[[ ! -e "${H3}/.config/DankMaterialShell/plugins/cenci" ]] ||
    fail "waybar path wrote a dms widget"
[[ ! -e "${H3}/.config/noctalia/plugins/cenci" ]] ||
    fail "waybar path wrote a noctalia widget"
assert_no_log "${C3}" "waybar reload"
echo "  ok: waybar guidance printed, nothing installed"

# ---------------------------------------------------------------------------
echo "case: host secrets in the parent env never reach captured calls or output (regression, #353)"
CASE4="${WORK}/sentinel"; H4="${CASE4}/home"; B4="${CASE4}/bin"
C4="${CASE4}/calls"; O4="${CASE4}/out"
mkdir -p "${H4}" "${B4}"; : >"${C4}"
link_tools "${B4}"
make_claude "${B4}"
prepare_checkout "${H4}"
export OPENAI_API_KEY="sk-test-sentinel-should-not-leak"
export CONTEXT7_API_KEY="ctx7-test-sentinel-should-not-leak"
run_install "${H4}" "${B4}" "${C4}" "${O4}" --yes --no-build ||
    fail "install (sentinel) exited non-zero"
unset OPENAI_API_KEY CONTEXT7_API_KEY
assert_no_log "${C4}" "sk-test-sentinel-should-not-leak"
assert_no_log "${C4}" "ctx7-test-sentinel-should-not-leak"
assert_no_log "${O4}" "sk-test-sentinel-should-not-leak"
assert_no_log "${O4}" "ctx7-test-sentinel-should-not-leak"
echo "  ok: sentinel secrets never leaked into captured calls or output"

echo "passed: GUI bar-widget detection, install, reload, and update"
