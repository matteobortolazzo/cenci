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
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pgrep nohup chmod sed head rm mktemp cp \
        rmdir tail cut tr id basename; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
exit 0
EOF
    # curl mock: install.sh's resolved-ref resolution (#625) runs on every
    # dispatch path now, so it needs a curl that answers both ref-resolution
    # probes as available (this suite is about widget wiring, not ref
    # pinning — installer-clients.test.sh covers #625's own scenarios).
    cat >"${bin}/curl" <<'EOF'
#!/bin/sh
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
    printf 'https://github.com/matteobortolazzo/cenci/releases/tag/watch/v1.0.0'
    exit 0
    ;;
*/.claude-plugin/marketplace.json)
    exit 0
    ;;
esac
[ -n "${out}" ] && : >"${out}"
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
  # Minimal #625/#490 interface-contract guard: this suite isn't testing ref
  # pinning, but a regression back to a ref-less `marketplace add` must not
  # pass silently just because this fake never checked. See
  # installer-clients.test.sh's make_claude for the full reference pattern.
  "plugin marketplace add "*)
    case "$4" in
      *@*) : ;;
      *) printf 'error: plugin marketplace add requires owner/repo@ref (#490)\n' >&2; exit 1 ;;
    esac
    ;;
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

# run_widget_script <script> <home> <bin> <calls> <out> <err> — invokes a
# widget's own install.sh/uninstall.sh directly (bypassing root install.sh),
# with stdout and stderr captured to separate files so reload-failure tests
# can assert the actionable message lands on stderr specifically, and the
# success echo (stdout) is absent — a merged-stream capture (like run_install
# uses for the root script) can't distinguish the two.
run_widget_script() {
    local script="$1" home="$2" bin="$3" calls="$4" out="$5" err="$6"
    set +e
    env -i HOME="${home}" PATH="${bin}" WIDGET_CALLS="${calls}" \
        bash "${script}" >"${out}" 2>"${err}"
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

# ---------------------------------------------------------------------------
echo "case: uninstall with no bar detected and no widget installed is a no-op — no reload calls, no errors"
CASE5="${WORK}/uninstall-noop"; H5="${CASE5}/home"; B5="${CASE5}/bin"
C5="${CASE5}/calls"; O5="${CASE5}/out"
mkdir -p "${H5}" "${B5}"; : >"${C5}"
link_tools "${B5}"
make_claude "${B5}"
prepare_checkout "${H5}"
# Deliberately stub none of the bar-detection/reload tools and create none of
# the widget config dirs, so every de_detected check is false.
run_install "${H5}" "${B5}" "${C5}" "${O5}" uninstall --yes ||
    fail "uninstall (no bars detected) exited non-zero"

assert_no_log "${C5}" "systemctl --user restart dms"
assert_no_log "${C5}" "kstart plasmashell"
assert_no_log "${C5}" "qs -c noctalia-shell"
assert_no_log "${C5}" "gnome-extensions disable"
[[ ! -e "${H5}/.local/share/gnome-shell/extensions/${GNOME_UUID}" ]] ||
    fail "gnome extension dir unexpectedly present"
[[ ! -e "${H5}/.local/share/plasma/plasmoids/${PLASMA_ID}" ]] ||
    fail "plasma plasmoid unexpectedly present"
[[ ! -e "${H5}/.config/DankMaterialShell/plugins/cenci" ]] ||
    fail "dms plugin symlink unexpectedly present"
[[ ! -e "${H5}/.config/noctalia/plugins/cenci" ]] ||
    fail "noctalia plugin symlink unexpectedly present"
echo "  ok: uninstalling never-installed widgets was a clean no-op"

# ---------------------------------------------------------------------------
echo "case: uninstall leaves a non-symlink DEST untouched (mirrors each install's own guard)"
CASE6="${WORK}/uninstall-guard"; H6="${CASE6}/home"; B6="${CASE6}/bin"
C6="${CASE6}/calls"; O6="${CASE6}/out"
mkdir -p "${H6}" "${B6}"; : >"${C6}"
link_tools "${B6}"
make_claude "${B6}"
prepare_checkout "${H6}"
make_logging_stub "${B6}" systemctl
DMS_DEST6="${H6}/.config/DankMaterialShell/plugins/cenci"
mkdir -p "$(dirname "${DMS_DEST6}")"
printf 'user-owned file, not managed by cenci\n' >"${DMS_DEST6}"
# de_detected dms is satisfied by the ~/.config/DankMaterialShell dir existing
# (no `dms` binary stubbed), matching install.sh's own detection.
run_install "${H6}" "${B6}" "${C6}" "${O6}" uninstall --yes ||
    fail "uninstall (non-symlink guard) exited non-zero"

[[ ! -L "${DMS_DEST6}" ]] ||
    fail "expected the pre-existing DEST to remain a real file, not a symlink"
[[ -f "${DMS_DEST6}" ]] ||
    fail "expected the pre-existing DEST file to survive uninstall"
[[ "$(cat "${DMS_DEST6}")" == "user-owned file, not managed by cenci" ]] ||
    fail "expected the pre-existing DEST file's contents to survive uninstall untouched"
echo "  ok: uninstall refused to remove a non-symlink DEST it doesn't own"

# ---------------------------------------------------------------------------
echo "case: host secrets in the parent env never reach captured calls or output during uninstall (regression, #353)"
CASE7="${WORK}/uninstall-sentinel"; H7="${CASE7}/home"; B7="${CASE7}/bin"
C7="${CASE7}/calls"; O7="${CASE7}/out"
mkdir -p "${H7}" "${B7}"; : >"${C7}"
link_tools "${B7}"
make_claude "${B7}"
prepare_checkout "${H7}"
for s in dms systemctl; do make_logging_stub "${B7}" "${s}"; done
export OPENAI_API_KEY="sk-test-sentinel-should-not-leak"
export CONTEXT7_API_KEY="ctx7-test-sentinel-should-not-leak"
run_install "${H7}" "${B7}" "${C7}" "${O7}" uninstall --yes ||
    fail "uninstall (sentinel) exited non-zero"
unset OPENAI_API_KEY CONTEXT7_API_KEY
assert_no_log "${C7}" "sk-test-sentinel-should-not-leak"
assert_no_log "${C7}" "ctx7-test-sentinel-should-not-leak"
assert_no_log "${O7}" "sk-test-sentinel-should-not-leak"
assert_no_log "${O7}" "ctx7-test-sentinel-should-not-leak"
echo "  ok: sentinel secrets never leaked into captured uninstall calls or output"

# ---------------------------------------------------------------------------
echo "case: install then uninstall removes each Linux widget and reloads its bar again on teardown"
CASE8="${WORK}/roundtrip"; H8="${CASE8}/home"; B8="${CASE8}/bin"
C8="${CASE8}/calls"; O8="${CASE8}/out"
mkdir -p "${H8}" "${B8}"; : >"${C8}"
link_tools "${B8}"
make_claude "${B8}"
prepare_checkout "${H8}"
for s in gnome-shell gnome-extensions glib-compile-schemas plasmashell kquitapp6 \
    kstart dms systemctl noctalia-shell qs; do
    make_logging_stub "${B8}" "${s}"
done
# uninstall_stop_daemon/uninstall_plugins need a resolvable cenci binary — give
# the fake HOME the version-pinned plugin cache an installed system would have,
# the same way the `update` case above does.
CACHE_ROOT8="${H8}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
mkdir -p "${CACHE_ROOT8}/bin" "${CACHE_ROOT8}/.claude-plugin"
printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${CACHE_ROOT8}/.claude-plugin/plugin.json"
printf '#!/bin/sh\nexit 0\n' >"${CACHE_ROOT8}/bin/cenci"
chmod +x "${CACHE_ROOT8}/bin/cenci"

run_install "${H8}" "${B8}" "${C8}" "${O8}" --yes --no-build ||
    fail "install (round trip setup) exited non-zero"
sleep 0.5

GNOME_DEST8="${H8}/.local/share/gnome-shell/extensions/${GNOME_UUID}"
PLASMA_DEST8="${H8}/.local/share/plasma/plasmoids/${PLASMA_ID}"
DMS_DEST8="${H8}/.config/DankMaterialShell/plugins/cenci"
NOCTALIA_DEST8="${H8}/.config/noctalia/plugins/cenci"
[[ -e "${GNOME_DEST8}" && ! -L "${GNOME_DEST8}" ]] ||
    fail "round trip setup: gnome extension was not installed"
[[ -L "${PLASMA_DEST8}" ]] || fail "round trip setup: plasma plasmoid symlink missing"
[[ -L "${DMS_DEST8}" ]] || fail "round trip setup: dms plugin symlink missing"
[[ -L "${NOCTALIA_DEST8}" ]] || fail "round trip setup: noctalia plugin symlink missing"

UC8="${CASE8}/uninstall-calls"; UO8="${CASE8}/uninstall-out"; : >"${UC8}"
run_install "${H8}" "${B8}" "${UC8}" "${UO8}" uninstall --yes ||
    fail "uninstall exited non-zero"
sleep 0.5

[[ ! -e "${GNOME_DEST8}" ]] || fail "gnome extension dir still present after uninstall"
[[ ! -e "${PLASMA_DEST8}" ]] || fail "plasma plasmoid symlink still present after uninstall"
[[ ! -e "${DMS_DEST8}" ]] || fail "dms plugin symlink still present after uninstall"
[[ ! -e "${NOCTALIA_DEST8}" ]] || fail "noctalia plugin symlink still present after uninstall"

assert_log "${UC8}" "systemctl --user restart dms"
assert_log "${UC8}" "kstart plasmashell"
assert_log "${UC8}" "qs -c noctalia-shell"
assert_log "${UC8}" "gnome-extensions disable"
echo "  ok: uninstall removed every widget and reloaded each bar again"

# ---------------------------------------------------------------------------
echo "case: dms install warns instead of failing when systemctl restart fails, and skips the success message (#471)"
CASE9="${WORK}/dms-systemctl-fail"; H9="${CASE9}/home"; B9="${CASE9}/bin"
C9="${CASE9}/calls"; O9="${CASE9}/out"; E9="${CASE9}/err"
mkdir -p "${H9}/.config/DankMaterialShell" "${B9}"; : >"${C9}"
link_tools "${B9}"
# systemctl detection ("status dms") succeeds so the clean systemd path is
# taken, but the actual reload ("restart dms") fails — the branch under test.
cat >"${B9}/systemctl" <<'EOF'
#!/bin/sh
printf 'systemctl %s\n' "$*" >>"${WIDGET_CALLS}"
case "$*" in
  *"status dms"*) exit 0 ;;
  *"restart dms"*) exit 1 ;;
esac
exit 0
EOF
chmod +x "${B9}/systemctl"
run_widget_script "${ROOT}/watch/plugin/dms/install.sh" "${H9}" "${B9}" "${C9}" "${O9}" "${E9}" ||
    fail "dms install.sh should still exit 0 when systemctl restart fails (warn, don't die)"
assert_out "${E9}" "Could not restart the dms user service — reload it manually: systemctl --user restart dms"
assert_no_log "${O9}" "Restarted the dms user service."
echo "  ok: dms systemctl-restart failure warns on stderr, exits 0, and skips the success message"

# ---------------------------------------------------------------------------
echo "case: dms install warns instead of failing when the pkill fallback fails, and skips the success message (#471)"
CASE10="${WORK}/dms-pkill-fail"; H10="${CASE10}/home"; B10="${CASE10}/bin"
C10="${CASE10}/calls"; O10="${CASE10}/out"; E10="${CASE10}/err"
mkdir -p "${H10}/.config/DankMaterialShell" "${B10}"; : >"${C10}"
link_tools "${B10}"
# No systemctl on PATH, so install.sh takes the pkill fallback branch; make
# that pkill call itself fail (unlike link_tools' always-succeeding stub).
cat >"${B10}/pkill" <<'EOF'
#!/bin/sh
printf 'pkill %s\n' "$*" >>"${WIDGET_CALLS}"
exit 1
EOF
chmod +x "${B10}/pkill"
run_widget_script "${ROOT}/watch/plugin/dms/install.sh" "${H10}" "${B10}" "${C10}" "${O10}" "${E10}" ||
    fail "dms install.sh should still exit 0 when the pkill fallback fails (warn, don't die)"
assert_out "${E10}" "DMS does not appear to be running — start it or check its status manually."
assert_no_log "${O10}" "Signalled DMS to reload (pkill 'qs -c dms')."
echo "  ok: dms pkill-fallback failure warns on stderr, exits 0, and skips the success message"

# ---------------------------------------------------------------------------
echo "case: plasma install warns instead of failing when kstart fails to relaunch plasmashell, and skips the success message (#471)"
CASE11="${WORK}/plasma-kstart-fail"; H11="${CASE11}/home"; B11="${CASE11}/bin"
C11="${CASE11}/calls"; O11="${CASE11}/out"; E11="${CASE11}/err"
mkdir -p "${H11}" "${B11}"; : >"${C11}"
link_tools "${B11}"
make_logging_stub "${B11}" plasmashell
make_logging_stub "${B11}" kquitapp6
# kquitapp6 stays best-effort (`|| true`, not under test); kstart is the
# synchronously-checkable branch — make it fail.
cat >"${B11}/kstart" <<'EOF'
#!/bin/sh
printf 'kstart %s\n' "$*" >>"${WIDGET_CALLS}"
exit 1
EOF
chmod +x "${B11}/kstart"
run_widget_script "${ROOT}/watch/plugin/plasma/install.sh" "${H11}" "${B11}" "${C11}" "${O11}" "${E11}" ||
    fail "plasma install.sh should still exit 0 when kstart fails (warn, don't die)"
assert_out "${E11}" "Could not reload plasmashell via kstart — restart it manually: kstart plasmashell"
assert_no_log "${O11}" "Reloaded plasmashell."
echo "  ok: plasma kstart failure warns on stderr, exits 0, and skips the success message"

# ---------------------------------------------------------------------------
echo "case: gnome install warns instead of failing when gnome-extensions enable fails, and skips the success message (#475)"
CASE12="${WORK}/gnome-enable-fail"; H12="${CASE12}/home"; B12="${CASE12}/bin"
C12="${CASE12}/calls"; O12="${CASE12}/out"; E12="${CASE12}/err"
mkdir -p "${H12}" "${B12}"; : >"${C12}"
link_tools "${B12}"
# gnome-extensions detection ("list") reports the UUID already installed
# (non-fresh) and "disable" succeeds, but the actual reload ("enable") fails
# — the branch under test.
cat >"${B12}/gnome-extensions" <<'EOF'
#!/bin/sh
printf 'gnome-extensions %s\n' "$*" >>"${WIDGET_CALLS}"
case "$1" in
  list) echo "cenci@matteobortolazzo.github.io" ;;
  disable) exit 0 ;;
  enable) exit 1 ;;
esac
exit 0
EOF
chmod +x "${B12}/gnome-extensions"
run_widget_script "${ROOT}/watch/plugin/gnome/install.sh" "${H12}" "${B12}" "${C12}" "${O12}" "${E12}" ||
    fail "gnome install.sh should still exit 0 when gnome-extensions enable fails (warn, don't die)"
assert_out "${E12}" "Could not reload the Cenci extension — reload it manually: gnome-extensions enable \"${GNOME_UUID}\""
assert_no_log "${O12}" "Reloaded the Cenci extension."
echo "  ok: gnome enable failure warns on stderr, exits 0, and skips the success message"

# ---------------------------------------------------------------------------
echo "case: gnome install warns instead of unconditionally claiming reload success when gnome-extensions is missing from PATH (#475)"
CASE13="${WORK}/gnome-extensions-missing"; H13="${CASE13}/home"; B13="${CASE13}/bin"
C13="${CASE13}/calls"; O13="${CASE13}/out"; E13="${CASE13}/err"
mkdir -p "${H13}" "${B13}"; : >"${C13}"
link_tools "${B13}"
# gnome-shell is on PATH (satisfies the top-level gate) but gnome-extensions
# is deliberately not stubbed, so the disable/enable block never runs — the
# gap under test (FRESH stays 0, so the script must not fall through to the
# unconditional "Reloaded" message).
make_logging_stub "${B13}" gnome-shell
run_widget_script "${ROOT}/watch/plugin/gnome/install.sh" "${H13}" "${B13}" "${C13}" "${O13}" "${E13}" ||
    fail "gnome install.sh should still exit 0 when gnome-extensions is missing from PATH"
assert_out "${E13}" "Could not reload the Cenci extension — gnome-extensions not found on PATH; reload GNOME Shell manually to pick up changes."
assert_no_log "${O13}" "Reloaded the Cenci extension."
assert_no_log "${C13}" "gnome-extensions disable"
assert_no_log "${C13}" "gnome-extensions enable"
echo "  ok: gnome install warns when gnome-extensions is missing, exits 0, and skips the success message"

echo "passed: GUI bar-widget detection, install, reload, update, and uninstall"
