#!/usr/bin/env bash
# End-to-end regressions for the installer's GUI bar-widget wiring: detect each
# present desktop bar, delegate to its self-contained install.sh (install +
# reload), and prove `agent-stack update` re-runs the reload so widget changes
# become visible. Uses the mock-PATH + fake-HOME pattern (no real desktop).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

GNOME_UUID="agentwatch@matteobortolazzo.github.io"
PLASMA_ID="com.github.matteobortolazzo.agentwatch"

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

# make_claude reports agentwatch (only) installed and the marketplace registered,
# so the installer reaches step_agentwatch_setup with agentwatch selected.
make_claude() {
    local bin="$1"
    cat >"${bin}/claude" <<'EOF'
#!/bin/sh
case "$*" in
  "plugin marketplace list") echo agent-stack ;;
  "plugin list") echo 'agentwatch@agent-stack' ;;
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
    checkout="${home}/.claude/plugins/marketplaces/agent-stack"
    mkdir -p "${checkout}/agentwatch/plugin"
    for de in gnome plasma dms noctalia; do
        cp -R "${ROOT}/agentwatch/plugin/${de}" "${checkout}/agentwatch/plugin/${de}"
    done
}

run_install() {
    local home="$1" bin="$2" calls="$3" out="$4"
    shift 4
    set +e
    HOME="${home}" PATH="${bin}" WIDGET_CALLS="${calls}" \
        bash "${ROOT}/install.sh" "$@" >"${out}" 2>&1
    local rc=$?
    set -e
    return "${rc}"
}

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_log() { grep -q -- "$2" "$1" || fail "expected '$2' in $1"; }
assert_no_log() { ! grep -q -- "$2" "$1" || fail "did not expect '$2' in $1"; }
assert_out() { grep -Fq -- "$2" "$1" || { sed -n '1,200p' "$1" >&2; fail "expected '$2' in output"; }; }

echo "agentwatch-widgets.test.sh"

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
[[ -L "${HOME_DIR}/.config/DankMaterialShell/plugins/agentwatch" ]] ||
    fail "dms plugin symlink missing"
assert_log "${CALLS}" "systemctl --user restart dms"
# noctalia: symlinked, shell respawned.
[[ -L "${HOME_DIR}/.config/noctalia/plugins/agentwatch" ]] ||
    fail "noctalia plugin symlink missing"
assert_log "${CALLS}" "qs -c noctalia-shell"
echo "  ok: all four widgets installed and reloaded"

# ---------------------------------------------------------------------------
echo "case: update re-runs each reload (widget changes become visible)"
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
[[ ! -e "${H2}/.config/DankMaterialShell/plugins/agentwatch" ]] ||
    fail "dms widget installed without dms present"
[[ ! -e "${H2}/.config/noctalia/plugins/agentwatch" ]] ||
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
[[ ! -e "${H3}/.config/DankMaterialShell/plugins/agentwatch" ]] ||
    fail "waybar path wrote a dms widget"
[[ ! -e "${H3}/.config/noctalia/plugins/agentwatch" ]] ||
    fail "waybar path wrote a noctalia widget"
assert_no_log "${C3}" "waybar reload"
echo "  ok: waybar guidance printed, nothing installed"

echo "passed: GUI bar-widget detection, install, reload, and update"
