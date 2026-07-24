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
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pkill pgrep nohup chmod sed head tail cut tr rm mktemp jq mv sort cp cmp; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    # docker mock: `image inspect` fails (no cenci-sandbox:latest yet, the
    # existing convention). `info --format '{{json .Runtimes}}'` answers with
    # DOCKER_INFO_RUNTIMES when set (tests use this to script sysbox-runc
    # presence/absence for doctor_sysbox — #587, mirroring #585's
    # SysboxRegistered detection), else defaults to a runtimes map with no
    # sysbox-runc entry, so every existing case that never sets the env var
    # exercises the "absent" branch. DOCKER_INFO_FAIL (optional), when set,
    # instead makes `docker info --format ...` itself fail (exit 1, no
    # stdout) — simulating a genuine `docker info` failure (daemon down,
    # permission denied, misconfigured context) distinct from a Docker that's
    # up but genuinely has no sysbox-runc runtime registered. DOCKER_INFO_MALFORMED
    # (optional, #630), when set, instead prints unparsable non-JSON garbage
    # (exit 0) — a third distinct failure mode doctor_sysbox's 4-state parsing
    # must tell apart from both a query failure (DOCKER_INFO_FAIL) and a
    # genuinely absent sysbox-runc registration.
    cat > "${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
if [ "${1:-}" = info ] && [ "${2:-}" = --format ]; then
    if [ -n "${DOCKER_INFO_FAIL:-}" ]; then exit 1; fi
    if [ -n "${DOCKER_INFO_MALFORMED:-}" ]; then
        printf '%s\n' 'not-json-at-all{{{unterminated'
        exit 0
    fi
    runtimes=${DOCKER_INFO_RUNTIMES:-}
    if [ -z "$runtimes" ]; then
        runtimes='{"runc":{"path":"runc"}}'
    fi
    printf '%s\n' "$runtimes"
    exit 0
fi
exit 0
EOF
    chmod +x "${bin}/docker"
    # curl mock: every invocation is logged to CURL_LOG (defaulted to
    # /dev/null so existing from-file cases that never set it stay silent),
    # then branches on the requested URL:
    #   */releases/latest — the redirect probe cenci_latest_ref() (#590) will
    #     follow via `-fsSLI -o /dev/null -w '%{url_effective}'`; answers with
    #     a fake .../releases/tag/<CENCI_FAKE_RELEASE_TAG> redirect (default
    #     "watch/v1.0.0" when that env var is unset, so every from-file/
    #     update case that never sets it still resolves a ref — #625's
    #     default-pins-everywhere requirement), or prints nothing (simulating
    #     an unresolvable release — offline / no releases yet) when
    #     CENCI_FAKE_NO_RELEASE is set, which takes priority over the
    #     default. Mirrors the existing lazyboards_latest_version mock
    #     pattern in lazyboards-install.test.sh, but the tail after
    #     CENCI_FAKE_RELEASE_TAG is caller-controlled so malformed/injected
    #     values can be exercised directly (#590's two-segment tag trust
    #     boundary).
    #   */.claude-plugin/marketplace.json — the per-ref availability probe
    #     ref_available() (#625) is expected to make before trusting a
    #     resolved/explicit ref; succeeds by default (ref is "available"), or
    #     fails (curl -f style nonzero exit, no output) when
    #     CENCI_FAKE_REF_UNAVAILABLE is set — simulating a ref with no
    #     published marketplace manifest (typo'd tag, deleted branch, etc.).
    #   */install.sh — the tag's install.sh download; copies the real
    #     checkout's install.sh (via $ROOT, threaded through explicitly since
    #     piped cases run under a scrubbed env — see run_piped_case) so a
    #     re-exec genuinely continues the installer from a file, not a stub.
    #     CENCI_FAKE_EMPTY_INSTALL (optional), when set, instead leaves the
    #     destination a 0-byte file — simulating a truncated/dropped download
    #     that curl -fsSL still exits 0 for — to exercise install.sh's own
    #     empty-download guard (#590). CENCI_FAKE_CORRUPT_INSTALL (optional),
    #     when set, instead copies the real install.sh and appends a trailing
    #     tamper marker — a non-empty but corrupted/truncated-in-transit body
    #     (#626) — whose bytes no longer byte-match $ROOT/install.sh, so the
    #     cosign mock's signature-mismatch check (see make_cosign) rejects it
    #     exactly like a real signature mismatch would, without a zero-byte
    #     body (AC3: "testing is not limited to a zero-byte body").
    #   */install.sh.bundle — the tag's cosign verify-blob bundle download
    #     (#626). CENCI_FAKE_MISSING_BUNDLE (optional), when set, fails the
    #     download outright (curl exit 1, nothing written) — simulating a
    #     404/network failure, mirroring how a real `curl -fsSL` fails closed
    #     on a missing asset. CENCI_FAKE_EMPTY_BUNDLE (optional), when set,
    #     leaves the destination a 0-byte file — the bundle analogue of
    #     CENCI_FAKE_EMPTY_INSTALL. Otherwise writes a fixed non-empty
    #     placeholder bundle body (the cosign mock never parses real Sigstore
    #     bundle bytes — it only checks non-emptiness plus the invocation
    #     shape, see make_cosign).
    #   anything else — legacy generic stub retained for the cenci-installer
    #     wrapper's self-update fetch path.
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
    if [ -n "${CENCI_FAKE_NO_RELEASE:-}" ]; then
        exit 0
    fi
    printf 'https://github.com/matteobortolazzo/cenci/releases/tag/%s' "${CENCI_FAKE_RELEASE_TAG:-watch/v1.0.0}"
    exit 0
    ;;
*/.claude-plugin/marketplace.json)
    if [ -n "${CENCI_FAKE_REF_UNAVAILABLE:-}" ]; then
        exit 22
    fi
    exit 0
    ;;
*/install.sh)
    [ -n "${out}" ] || exit 1
    if [ -n "${CENCI_FAKE_EMPTY_INSTALL:-}" ]; then
        : >"${out}"
    elif [ -n "${CENCI_FAKE_CORRUPT_INSTALL:-}" ]; then
        cp "${ROOT}/install.sh" "${out}"
        printf '\n# tampered-in-transit\n' >>"${out}"
    else
        cp "${ROOT}/install.sh" "${out}"
    fi
    exit 0
    ;;
*/install.sh.bundle)
    [ -n "${out}" ] || exit 1
    if [ -n "${CENCI_FAKE_MISSING_BUNDLE:-}" ]; then
        exit 1
    elif [ -n "${CENCI_FAKE_EMPTY_BUNDLE:-}" ]; then
        : >"${out}"
    else
        printf 'fake-sigstore-bundle-body\n' >"${out}"
    fi
    exit 0
    ;;
esac
[ -n "${out}" ] || exit 1
cat >"${out}" <<'INSTALLER'
printf 'forwarded installer args: %s\n' "$*"
INSTALLER
EOF
    chmod +x "${bin}/curl"
}

# make_cosign <bin> — installs a scriptable cosign mock at <bin>/cosign for
# the installer's cosign verify-blob call (#626). Every invocation is logged
# to COSIGN_LOG (defaulted to /dev/null). COSIGN_MODE=fail (optional) makes
# the mock exit non-zero unconditionally — simulating a black-box
# verify-blob rejection unrelated to the downloaded content (the
# "cosign-nonzero" test case). Otherwise ("pass", the default) the mock
# still enforces the real cosign verify-blob interface contract rather than
# permissively accepting anything (sandbox lesson #490 / this ticket's
# Risks: "a permissive cosign mock could mask a real verify bug"): it only
# exits 0 if invoked with the verify-blob subcommand, a non-empty --bundle
# file, the pinned --certificate-identity-regexp (must reference the fully
# dot-escaped \.github/workflows/watch-release\.yml@refs/tags/watch/v, per
# the plan's locked identity string) and --certificate-oidc-issuer
# (https://token.actions.githubusercontent.com), a target file that exists,
# AND that target file's bytes exactly match the known-good reference
# $ROOT/install.sh. That last check is what makes CENCI_FAKE_CORRUPT_INSTALL
# (a genuinely tampered download) fail verification on its own, the same way
# a real signature mismatch would, without any separate opt-in from the
# caller — a real decoy, not a rubber stamp.
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
case "${identity_regexp}" in
  *'\.github/workflows/watch-release\.yml@refs/tags/watch/v'*) : ;;
  *) echo "cosign mock: unexpected --certificate-identity-regexp '${identity_regexp}'" >&2; exit 1 ;;
esac
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
# (#625) enforces the real CLI's interface contract (#490): `plugin
# marketplace add` is only accepted in the `owner/repo@ref` form and is
# rejected (nonzero, no state change) when the ref is missing, so a
# production regression back to a ref-less `marketplace add
# matteobortolazzo/cenci` call is caught instead of silently masked.
# CLAUDE_MARKETPLACE_FILE doubles as the recorded active ref: `add` writes
# the ref into it, `remove` deletes it (modeling the Q4 remove+re-add
# contract — `marketplace_registered` only checks the file's existence, so
# content changes are invisible to that check). `plugin install`/`update`
# stamp the resulting CLAUDE_INSTALLED_FILE-<plugin> marker with the
# currently-registered ref's content, so a test can prove which ref actually
# supplied the installed plugin content, not just which ref was requested.
make_claude() {
    local bin="$1"
    cat > "${bin}/claude" <<'EOF'
#!/bin/sh
printf 'claude %s\n' "$*" >>"${CALLS_FILE}"
# Regression probe (#353): if a host secret survives into this subprocess's
# environment, surface it in the captured calls so the sentinel-secret case
# can prove this test harness's env -i scrub keeps host secrets out.
[ -n "${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "${OPENAI_API_KEY}" >>"${CALLS_FILE}"
[ -n "${CONTEXT7_API_KEY:-}" ] && printf 'env-leak CONTEXT7_API_KEY=%s\n' "${CONTEXT7_API_KEY}" >>"${CALLS_FILE}"
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
  "plugin marketplace remove "*)
    # CLAUDE_REFRESH_FAIL_FILE (optional), when present, fails the remove
    # step of the Q4 remove+re-add contract — modeling a client that can't
    # tear down its existing registration, which must abort before any
    # re-add is attempted (destructive-prerequisite convention, #625).
    if [ -f "${CLAUDE_REFRESH_FAIL_FILE}" ]; then
      exit 1
    fi
    rm -f "${CLAUDE_MARKETPLACE_FILE}"
    exit 0
    ;;
  "plugin list")
    [ ! -f "${CLAUDE_LIST_FAIL_FILE}" ] || exit 1
    for p in cenci cenci-watch cenci-sandbox; do
      [ -f "${CLAUDE_INSTALLED_FILE}-${p}" ] && printf '%s@cenci\n' "${p}"
    done
    exit 0
    ;;
  "plugin install "*)
    p=${3%%@*}
    # CLAUDE_SKIP_INSTALL_PLUGIN (optional) simulates a specific plugin never
    # actually landing despite the install command reporting success — e.g. a
    # marketplace that doesn't (yet) carry it — so plugin_installed keeps
    # reporting it absent and prune_selected_to_installed prunes it from
    # SELECTED, the same as a real never-installed plugin.
    if [ "${p}" != "${CLAUDE_SKIP_INSTALL_PLUGIN:-}" ]; then
      [ -f "${CLAUDE_MARKETPLACE_FILE}" ] && cp "${CLAUDE_MARKETPLACE_FILE}" "${CLAUDE_INSTALLED_FILE}-${p}" || touch "${CLAUDE_INSTALLED_FILE}-${p}"
    fi
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

# make_codex <bin> — fake `codex` executable. Mirrors make_claude's ref
# recording/enforcement (#625, #490) for Codex's distinct `--ref <value>`
# flag form: `plugin marketplace add <repo> --ref <ref>` is only accepted
# with a non-empty --ref value, rejected otherwise. CODEX_MARKETPLACE_FILE
# doubles as the recorded active ref (add writes it, remove deletes it —
# same Q4 remove+re-add modeling as Claude). `plugin add` (used for both
# fresh installs and updates — see step_reconcile_plugins) stamps
# CODEX_INSTALLED_FILE-<plugin> with the currently-registered ref's content.
make_codex() {
    local bin="$1"
    cat > "${bin}/codex" <<'EOF'
#!/bin/sh
printf 'codex %s\n' "$*" >>"${CALLS_FILE}"
case "$*" in
  "plugin marketplace list") [ -f "${CODEX_MARKETPLACE_FILE}" ] && echo 'cenci local'; exit 0 ;;
  "plugin marketplace add "*)
    if [ "${5:-}" = "--ref" ] && [ -n "${6:-}" ]; then
      printf '%s' "$6" >"${CODEX_MARKETPLACE_FILE}"
      exit 0
    else
      printf 'error: plugin marketplace add requires --ref <value> (#490)\n' >&2
      exit 1
    fi
    ;;
  "plugin marketplace remove "*) rm -f "${CODEX_MARKETPLACE_FILE}"; exit 0 ;;
  "plugin list")
    for p in cenci cenci-watch cenci-sandbox; do
      [ -f "${CODEX_INSTALLED_FILE}-${p}" ] && printf '%s@cenci installed\n' "${p}"
    done
    exit 0
    ;;
  "plugin add "*)
    p=${3%%@*}
    [ -f "${CODEX_MARKETPLACE_FILE}" ] && cp "${CODEX_MARKETPLACE_FILE}" "${CODEX_INSTALLED_FILE}-${p}" || touch "${CODEX_INSTALLED_FILE}-${p}"
    exit 0
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/codex"
}

# make_opencode <bin> [version] — mocks the `opencode` executable that
# HAS_OPENCODE / doctor_opencode_support (#491, not yet implemented) resolve
# via `have opencode` + `opencode --version`. Unlike make_claude/make_codex,
# OpenCode plugin/skill registration never goes through the `opencode` binary
# itself (no marketplace CLI) — it's done by install-skills.sh symlinking and
# seed_opencode_config merging opencode.json directly — so this mock only
# needs to report a version and log invocations, following the same
# CALLS_FILE-logging convention as the other client mocks.
make_opencode() {
    local bin="$1" version="${2:-1.18.3}"
    cat > "${bin}/opencode" <<EOF
#!/bin/sh
printf 'opencode %s\n' "\$*" >>"\${CALLS_FILE}"
case "\$*" in
  --version) printf '%s\n' "${version}" ;;
esac
exit 0
EOF
    chmod +x "${bin}/opencode"
}

prepare_checkout() {
    local home="$1" client="$2" checkout
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}"
    if [[ -f "${ROOT}/cenci" ]]; then
        cp "${ROOT}/cenci" "${checkout}/cenci"
        chmod +x "${checkout}/cenci"
    fi
    cp "${ROOT}/install.sh" "${checkout}/install.sh"
}

# prepare_opencode_checkout_assets <home> <client> — stages the real,
# already-shipped OpenCode assets (flow/opencode/install-skills.sh,
# flow/skills/*, watch/plugin/opencode, sandbox/lib/opencode-config.sh) into
# the marketplace checkout at the exact relative paths find_plugin_path
# resolves first (marketplace-checkout-first, no cache-fallback stripping
# needed), so a real step_opencode_setup (#491, not yet implemented) has
# something genuine to symlink/merge — mirroring how prepare_checkout stages
# the real cenci wrapper + install.sh rather than a fake stand-in.
prepare_opencode_checkout_assets() {
    local home="$1" client="$2" checkout
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}/flow/opencode" "${checkout}/watch/plugin" "${checkout}/sandbox/lib"
    cp "${ROOT}/flow/opencode/install-skills.sh" "${checkout}/flow/opencode/install-skills.sh"
    chmod +x "${checkout}/flow/opencode/install-skills.sh"
    cp -r "${ROOT}/flow/skills" "${checkout}/flow/skills"
    cp -r "${ROOT}/watch/plugin/opencode" "${checkout}/watch/plugin/opencode"
    cp "${ROOT}/sandbox/lib/opencode-config.sh" "${checkout}/sandbox/lib/opencode-config.sh"
}

# prepare_opencode_cache_fallback_assets <home> <client> — like
# prepare_opencode_checkout_assets, but intentionally omits watch/plugin/opencode
# from the marketplace checkout, staging it only in the version-pinned plugin
# cache (mirroring prepare_bootstrap_cenci's cache root) so find_plugin_path
# falls through to its cache-fallback branch for that one asset: that branch
# strips the watch/plugin/ prefix before searching, producing a resolved path
# like .../cenci-watch/1.0.0/opencode with no watch/plugin/opencode substring
# in it at all — the exact shape that exposed the #491 review substring-
# matching bug in opencode_plugin_registered / uninstall_opencode_cleanup.
prepare_opencode_cache_fallback_assets() {
    local home="$1" client="$2" checkout cache_root
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}/flow/opencode" "${checkout}/sandbox/lib"
    cp "${ROOT}/flow/opencode/install-skills.sh" "${checkout}/flow/opencode/install-skills.sh"
    chmod +x "${checkout}/flow/opencode/install-skills.sh"
    cp -r "${ROOT}/flow/skills" "${checkout}/flow/skills"
    cp "${ROOT}/sandbox/lib/opencode-config.sh" "${checkout}/sandbox/lib/opencode-config.sh"
    if [[ "${client}" == claude ]]; then
        cache_root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    else
        cache_root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
    fi
    mkdir -p "${cache_root}"
    cp -r "${ROOT}/watch/plugin/opencode" "${cache_root}/opencode"
}

# prepare_bootstrap_cenci provisions the installed plugin cache without a
# binary. Its bootstrap creates the binary, proving a fresh installer run does
# not depend on a prior agent session or image build to make `cenci` runnable.
prepare_bootstrap_cenci() {
    local home="$1" client="$2" root manifest_dir bootstrap root_var
    if [[ "${client}" == claude ]]; then
        root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.claude-plugin
        bootstrap="${root}/hooks/bootstrap.sh"
        root_var=CLAUDE_PLUGIN_ROOT
    else
        root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.codex-plugin
        bootstrap="${root}/codex/bootstrap.sh"
        root_var=PLUGIN_ROOT
    fi
    [[ -e "${root}/${manifest_dir}/plugin.json" ]] && return 0
    mkdir -p "${root}/${manifest_dir}" "$(dirname "${bootstrap}")"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/${manifest_dir}/plugin.json"
    cat >"${bootstrap}" <<EOF
#!/bin/sh
root=\${${root_var}}
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

# prepare_broken_cenci_cache <home> <client> — provisions only the
# version-pinned plugin cache manifest, with no bootstrap script and no
# binary. Simulates an offline machine, unpublished release assets, or an
# otherwise-unpopulated plugin cache: current_cenci_binary can't provision
# anything from this cache.
prepare_broken_cenci_cache() {
    local home="$1" client="$2" root manifest_dir
    if [[ "${client}" == claude ]]; then
        root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.claude-plugin
    else
        root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.codex-plugin
    fi
    mkdir -p "${root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/${manifest_dir}/plugin.json"
}

# run_case_broken_cenci_cache <name> <mode-and-flags...> — like run_case, but
# provisions a plugin cache with no bootstrap script (prepare_broken_cenci_cache)
# instead of a working one, so current_cenci_binary can't provision the cenci
# binary at all.
run_case_broken_cenci_cache() {
    local name="$1"
    shift
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    make_claude "${bin}"
    prepare_checkout "${home}" claude
    prepare_broken_cenci_cache "${home}" claude

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_REFRESH_FAIL_FILE="${case_dir}/claude-refresh-fail" \
        CLAUDE_LIST_FAIL_FILE="${case_dir}/claude-list-fail" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        bash "${ROOT}/install.sh" --yes --no-build "$@" >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
}

# prepare_stale_newest_cenci_cache <home> <client> — provisions TWO version-
# pinned plugin cache roots: an older one (1.0.0) with a working bootstrapped
# binary, and a newer one (2.0.0) with only a manifest — no bootstrap, no binary.
# Simulates the manifest-ahead-of-release race: the plugin manifest bumped to a
# version whose release isn't published yet, so current_cenci_binary (which
# resolves the newest root) can't provision it, while the previous version's
# binary is still installed and usable. The newer manifest is stamped with a
# strictly newer mtime so newest_cenci_root resolves 2.0.0.
prepare_stale_newest_cenci_cache() {
    local home="$1" client="$2" old_root new_root manifest_dir
    if [[ "${client}" == claude ]]; then
        old_root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
        new_root="${home}/.claude/plugins/cache/cenci/cenci-watch/2.0.0"
        manifest_dir=.claude-plugin
    else
        old_root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
        new_root="${home}/.codex/plugins/cache/cenci/cenci-watch/2.0.0"
        manifest_dir=.codex-plugin
    fi
    # Older version: working binary that logs its invocations.
    mkdir -p "${old_root}/bin" "${old_root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${old_root}/${manifest_dir}/plugin.json"
    cat > "${old_root}/bin/cenci" <<'EOF'
#!/bin/sh
printf 'cenci %s\n' "$*" >>"${CALLS_FILE}"
exit 0
EOF
    chmod +x "${old_root}/bin/cenci"
    # Newer version: manifest only — release assets not published yet.
    mkdir -p "${new_root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"2.0.0"}\n' >"${new_root}/${manifest_dir}/plugin.json"
    # Portable (GNU + BSD touch) mtime stamps so the newer root sorts newest.
    touch -t 202001010000 "${old_root}/${manifest_dir}/plugin.json"
    touch -t 202001020000 "${new_root}/${manifest_dir}/plugin.json"
}

# run_case_stale_newest_cenci_cache <name> <mode-and-flags...> — like
# run_case_broken_cenci_cache, but the newest plugin version has no binary while
# a previous version's binary is present (prepare_stale_newest_cenci_cache).
run_case_stale_newest_cenci_cache() {
    local name="$1"
    shift
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    make_claude "${bin}"
    prepare_checkout "${home}" claude
    prepare_stale_newest_cenci_cache "${home}" claude

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_REFRESH_FAIL_FILE="${case_dir}/claude-refresh-fail" \
        CLAUDE_LIST_FAIL_FILE="${case_dir}/claude-list-fail" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        bash "${ROOT}/install.sh" --yes --no-build "$@" >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
}

# prepare_cache_cenci <home> <client> — provision a fake cenci binary in the
# version-pinned plugin cache (what current_cenci_binary resolves), logging
# every invocation to CALLS_FILE, so the `cenci sandbox build` step can run.
prepare_cache_cenci() {
    local home="$1" client="$2" root manifest_dir
    if [[ "${client}" == claude ]]; then
        root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.claude-plugin
    else
        root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.codex-plugin
    fi
    mkdir -p "${root}/bin" "${root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/${manifest_dir}/plugin.json"
    cat > "${root}/bin/cenci" <<'EOF'
#!/bin/sh
printf 'cenci %s\n' "$*" >>"${CALLS_FILE}"
exit 0
EOF
    chmod +x "${root}/bin/cenci"
}

run_case() {
    local name="$1" clients="$2"
    local build_flag="${3:---no-build}"
    local extra=()
    if [ "$#" -gt 3 ]; then extra=("${@:4}"); fi
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    case "${clients}" in
      claude) make_claude "${bin}"; prepare_checkout "${home}" claude; prepare_bootstrap_cenci "${home}" claude ;;
      codex) make_codex "${bin}"; prepare_checkout "${home}" codex; prepare_bootstrap_cenci "${home}" codex ;;
      dual) make_claude "${bin}"; make_codex "${bin}"; prepare_checkout "${home}" claude; prepare_bootstrap_cenci "${home}" claude ;;
    esac

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_REFRESH_FAIL_FILE="${case_dir}/claude-refresh-fail" \
        CLAUDE_LIST_FAIL_FILE="${case_dir}/claude-list-fail" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        bash "${ROOT}/install.sh" --yes "${build_flag}" ${extra[@]+"${extra[@]}"} >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
    CASE_BIN="${bin}"
}

# run_piped_case <name> <clients> <env_extra> [install.sh args...] — simulates
# the documented `curl -fsSL .../main/install.sh | bash` one-liner: install.sh
# is read from stdin (no on-disk script path for $0/BASH_SOURCE to resolve),
# the exact shape the new piped-only release-tag resolution + re-exec guard
# (#590) must detect. `bash -s -- <args>` is the standard way to forward
# arguments into a piped script's "$@" (see the plan's Architectural Context /
# Assumptions). <env_extra> is a single word-split string of NAME=value pairs
# (e.g. "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_REF=main") layered onto the
# env -i scrub — pass "" for none. Sets CASE_CURL_LOG (every curl-mock
# invocation, argv only — never the resolved redirect body) and
# CASE_COSIGN_LOG (every cosign-mock invocation, argv only — #626) alongside
# the usual run_case CASE_* outputs, so callers can assert whether/how the
# /releases/latest probe, the tag's install.sh + install.sh.bundle download,
# and the cosign verify-blob call happened.
#
# A cosign mock (make_cosign, "pass" mode) is placed on this case's PATH by
# default — cosign is a new mandatory prerequisite for the default piped
# install path (#626), so every case that doesn't specifically test cosign's
# absence needs it present to reach a genuine verified success. Passing the
# literal token "COSIGN_MODE=absent" anywhere in <env_extra> both skips
# planting the mock (so `command -v cosign` genuinely fails, exercising the
# cosign-missing die path) and is otherwise inert once forwarded into the
# env -i call below (no mock exists to read it).
#
# Every case's CWD-for-the-invocation is case_dir, and case_dir always
# contains a readable regular file literally named "bash" (a decoy) planted
# before the run. In a real piped invocation $0 is the bare word "bash" (no
# path separator), so a naive from-file check (`[ -f "$pin_self" ]` alone)
# would resolve that -f/-r against CWD and be fooled into thinking this is a
# from-file run whenever the caller's CWD happens to contain a file named
# "bash" — silently skipping the release-tag pin. Planting the decoy in
# every piped case's CWD (not just one dedicated case) proves the guard's
# bare-word/no-path-separator detection (install.sh) isn't fooled by it,
# across every piped scenario this suite exercises.
run_piped_case() {
    local name="$1" clients="$2" env_extra="$3"
    shift 3
    local install_args=("$@")
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    local curl_log="${WORK}/${name}/curl-log" cosign_log="${WORK}/${name}/cosign-log"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    : >"${curl_log}"
    : >"${cosign_log}"
    # Decoy: a readable regular file literally named "bash" in the CWD the
    # piped invocation below runs from (see doc comment above).
    printf 'not a real shell\n' >"${case_dir}/bash"
    make_common_tools "${bin}"
    case "${env_extra}" in
      *COSIGN_MODE=absent*) : ;; # cosign intentionally left off PATH
      *) make_cosign "${bin}" ;;
    esac
    case "${clients}" in
      claude) make_claude "${bin}"; prepare_checkout "${home}" claude; prepare_bootstrap_cenci "${home}" claude ;;
      codex) make_codex "${bin}"; prepare_checkout "${home}" codex; prepare_bootstrap_cenci "${home}" codex ;;
    esac

    set +e
    # shellcheck disable=SC2086 # env_extra is intentionally word-split into
    # zero or more separate NAME=value tokens for env -i (see doc comment).
    # Run from case_dir (which holds the "bash" decoy file) so $0's bare-word
    # "bash" would resolve -f/-r false-positive against it if the guard
    # regressed to CWD-relative detection alone.
    (
        cd "${case_dir}" && \
        env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
            ROOT="${ROOT}" CURL_LOG="${curl_log}" COSIGN_LOG="${cosign_log}" \
            CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
            CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
            CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
            CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
            ${env_extra} \
            bash -s -- --yes --no-build ${install_args[@]+"${install_args[@]}"} <"${ROOT}/install.sh" >"${output}" 2>&1
    )
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_CURL_LOG="${curl_log}"
    CASE_COSIGN_LOG="${cosign_log}"
    CASE_HOME="${home}"
    CASE_BIN="${bin}"
}

# run_doctor_case <name> <clients> [extra NAME=value env assignments for the
# doctor run...] — extra args are forwarded to `env -i` ahead of the doctor
# invocation, so callers can script additional env vars the mocked tools on
# PATH read (e.g. DOCKER_INFO_RUNTIMES for the docker mock).
run_doctor_case() {
    local name="$1" clients="$2"
    shift 2
    run_case "${name}" "${clients}"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/doctor-output"
    set +e
    env -i HOME="${CASE_HOME}" PATH="${bin}" CALLS_FILE="${CASE_CALLS}" \
        CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${WORK}/${name}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/${name}/codex-installed" \
        "$@" \
        bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
    DOCTOR_EXIT=$?
    set -e
    DOCTOR_OUTPUT="${output}"
}

# run_doctor_case_with_daemon_status is run_doctor_case plus a fake
# `cenci` on the doctor-run PATH that exits daemon_exit on `daemon
# status` (0 = running, 1 = not running — mirrors runDaemonStatus in
# watch/main.go). Pre-creating "${WORK}/${name}/bin/cenci" before
# run_case's `mkdir -p` (idempotent) and make_common_tools (which only
# symlinks its own fixed tool list) leaves this fake in place for the doctor
# run that follows.
run_doctor_case_with_daemon_status() {
    local name="$1" clients="$2" daemon_exit="$3"
    local bin="${WORK}/${name}/bin"
    mkdir -p "${bin}"
    cat > "${bin}/cenci" <<EOF
#!/bin/sh
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = status ]; then
    exit ${daemon_exit}
fi
exit 0
EOF
    chmod +x "${bin}/cenci"
    run_doctor_case "${name}" "${clients}"
}

# run_doctor_case_with_docker_runtimes <name> <clients> <runtimes_json> — like
# run_doctor_case, but sets DOCKER_INFO_RUNTIMES for the doctor invocation so
# the docker mock's `docker info --format '{{json .Runtimes}}'` branch answers
# with <runtimes_json>, exercising doctor_sysbox's presence/absence branches
# (#587, mirroring #585's SysboxRegistered detection: presence of the
# "sysbox-runc" key in the Runtimes map).
run_doctor_case_with_docker_runtimes() {
    local name="$1" clients="$2" runtimes_json="$3"
    run_doctor_case "${name}" "${clients}" "DOCKER_INFO_RUNTIMES=${runtimes_json}"
}

# run_doctor_case_with_docker_info_failure <name> <clients> — like
# run_doctor_case, but sets DOCKER_INFO_FAIL for the doctor invocation so the
# docker mock's `docker info --format '{{json .Runtimes}}'` branch itself
# fails (exit 1, no stdout), exercising doctor_sysbox's distinct "could not
# query docker info" path (#587 review fix) rather than the generic
# "not registered" message a genuinely-absent sysbox-runc runtime produces.
run_doctor_case_with_docker_info_failure() {
    local name="$1" clients="$2"
    run_doctor_case "${name}" "${clients}" "DOCKER_INFO_FAIL=1"
}

# run_doctor_case_with_podman <name> <clients> — like run_doctor_case, but
# stubs a `podman` executable onto the case's PATH before the run so
# container_runtime() resolves "podman" (have podman is checked before
# docker), exercising doctor_sysbox's runtime-gated self-skip: #585's sysbox
# preflight/detection is Docker-only, so the doctor check must not run at all
# under Podman.
run_doctor_case_with_podman() {
    local name="$1" clients="$2"
    local bin="${WORK}/${name}/bin"
    mkdir -p "${bin}"
    cat > "${bin}/podman" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${bin}/podman"
    run_doctor_case "${name}" "${clients}"
}

# run_doctor_case_with_podman_preferred_and_docker_runtimes <name> <clients>
# <runtimes_json> — like run_doctor_case_with_podman, but ALSO scripts
# DOCKER_INFO_RUNTIMES for the run (make_common_tools already stubs a docker
# mock unconditionally). This is the dual-runtime host #630 fixes:
# container_runtime() still resolves "podman" first (have podman is checked
# before docker), but doctor_sysbox must now probe Docker's sysbox
# registration independently whenever `have docker`, regardless of which
# runtime is preferred for ordinary launches.
run_doctor_case_with_podman_preferred_and_docker_runtimes() {
    local name="$1" clients="$2" runtimes_json="$3"
    local bin="${WORK}/${name}/bin"
    mkdir -p "${bin}"
    cat > "${bin}/podman" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${bin}/podman"
    run_doctor_case "${name}" "${clients}" "DOCKER_INFO_RUNTIMES=${runtimes_json}"
}

# make_sysbox_runc_binary <path> <version-line> [exit-code=0] — writes an
# executable at an arbitrary absolute path (not necessarily on PATH) that
# answers `<path> --version` with <version-line>, simulating the real
# sysbox-runc binary doctor_sysbox resolves via the "path" field of the
# `docker info --format '{{json .Runtimes}}'` sysbox-runc entry (#630's Q2).
make_sysbox_runc_binary() {
    local path="$1" version_line="$2" exit_code="${3:-0}"
    mkdir -p "$(dirname "${path}")"
    cat > "${path}" <<EOF
#!/bin/sh
if [ "\${1:-}" = --version ]; then
    printf '%s\n' "${version_line}"
    exit ${exit_code}
fi
exit 0
EOF
    chmod +x "${path}"
}

# run_doctor_case_with_sysbox_path_fallback <name> <clients> <runtimes_json>
# <version-line> — like run_doctor_case_with_docker_runtimes, but pre-creates
# a `sysbox-runc` binary directly on the case's own PATH (its bin dir)
# BEFORE run_case's own `mkdir -p` (idempotent, same pre-creation order as
# run_doctor_case_with_daemon_status above) so it's in place when doctor_sysbox
# falls back to `sysbox-runc --version` on PATH — exercised when the
# runtimes_json's sysbox-runc entry has no usable "path" field (#630's Q2
# fallback chain: JSON path -> PATH -> "unknown").
run_doctor_case_with_sysbox_path_fallback() {
    local name="$1" clients="$2" runtimes_json="$3" version_line="$4"
    local bin="${WORK}/${name}/bin"
    mkdir -p "${bin}"
    cat > "${bin}/sysbox-runc" <<EOF
#!/bin/sh
if [ "\${1:-}" = --version ]; then
    printf '%s\n' "${version_line}"
    exit 0
fi
exit 0
EOF
    chmod +x "${bin}/sysbox-runc"
    run_doctor_case_with_docker_runtimes "${name}" "${clients}" "${runtimes_json}"
}

# run_doctor_case_with_cosign <name> <clients> <present|absent> — like
# run_doctor_case, but places a cosign mock on this doctor run's PATH only
# when <present|absent> is the literal string "present"; any other value
# (e.g. "absent") leaves cosign genuinely missing (#626). Exercises
# run_doctor's cosign presence check — a new mandatory prerequisite for the
# default verified piped install path — which must warn (not hard-fail) when
# cosign is absent, per the check <label> optional <hint> pattern (install.sh
# ~321).
run_doctor_case_with_cosign() {
    local name="$1" clients="$2" cosign_present="$3"
    local bin="${WORK}/${name}/bin"
    mkdir -p "${bin}"
    [ "${cosign_present}" = present ] && make_cosign "${bin}"
    run_doctor_case "${name}" "${clients}"
}

# run_case_opencode <name> <clients> <opencode-version|absent> [build_flag]
# [extra args...] — like run_case, but additionally provisions a mocked
# `opencode` executable (unless <opencode-version> is the literal string
# "absent") plus the real OpenCode assets step_opencode_setup will resolve
# via find_plugin_path (prepare_opencode_checkout_assets). step_opencode_setup
# (#491, not yet implemented) is assumed to gate its own ask_yn opt-in with a
# "y" default — mirroring the existing SwiftBar-widget precedent
# (step_cenci_watch_setup's `ask_yn "SwiftBar detected..." y`) for an
# already-detected optional integration — so plain --yes (no interactive tty
# stub) is expected to proceed with it once implemented.
run_case_opencode() {
    local name="$1" clients="$2" opencode_version="$3"
    shift 3
    local build_flag="${1:---no-build}"
    local extra=()
    if [ "$#" -gt 1 ]; then extra=("${@:2}"); fi
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    case "${clients}" in
    claude)
        make_claude "${bin}"
        prepare_checkout "${home}" claude
        prepare_bootstrap_cenci "${home}" claude
        prepare_opencode_checkout_assets "${home}" claude
        ;;
    codex)
        make_codex "${bin}"
        prepare_checkout "${home}" codex
        prepare_bootstrap_cenci "${home}" codex
        prepare_opencode_checkout_assets "${home}" codex
        ;;
    dual)
        make_claude "${bin}"
        make_codex "${bin}"
        prepare_checkout "${home}" claude
        prepare_bootstrap_cenci "${home}" claude
        prepare_opencode_checkout_assets "${home}" claude
        ;;
    esac
    if [ "${opencode_version}" != absent ]; then
        make_opencode "${bin}" "${opencode_version}"
    fi

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_REFRESH_FAIL_FILE="${case_dir}/claude-refresh-fail" \
        CLAUDE_LIST_FAIL_FILE="${case_dir}/claude-list-fail" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        bash "${ROOT}/install.sh" --yes "${build_flag}" ${extra[@]+"${extra[@]}"} >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
    CASE_BIN="${bin}"
}

# run_opencode_doctor_case <name> <clients> <opencode-version|absent> — like
# run_doctor_case, but runs run_case_opencode first so the mocked `opencode`
# executable and checkout assets are in place for doctor to detect.
run_opencode_doctor_case() {
    local name="$1" clients="$2" opencode_version="$3"
    run_case_opencode "${name}" "${clients}" "${opencode_version}"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/doctor-output"
    set +e
    env -i HOME="${CASE_HOME}" PATH="${bin}" CALLS_FILE="${CASE_CALLS}" \
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

# assert_before <file> <first> <second> — asserts <first>'s earliest matching
# line number is strictly less than <second>'s in <file>, proving call
# ordering (e.g. the Q4 remove+re-add contract: `marketplace remove` must be
# recorded before the ref-carrying `marketplace add`, #625).
assert_before() {
    local file="$1" first="$2" second="$3" line1 line2
    # `|| true` on each assignment: under `set -e` + `pipefail`, grep finding
    # no match makes the whole pipeline (and thus this plain assignment)
    # exit nonzero, which would abort the script here — before the explicit
    # "expected both ... in file" diagnostic below ever runs. Suppress that
    # so a genuinely missing needle is reported with a clear message instead
    # of a silent, unexplained script exit.
    line1="$(grep -Fn -- "${first}" "${file}" | head -n1 | cut -d: -f1)" || true
    line2="$(grep -Fn -- "${second}" "${file}" | head -n1 | cut -d: -f1)" || true
    if [ -z "${line1}" ] || [ -z "${line2}" ]; then
        echo "FAIL: expected both '${first}' and '${second}' in ${file}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
    if [ "${line1}" -ge "${line2}" ]; then
        echo "FAIL: expected '${first}' before '${second}' in ${file} (got lines ${line1} and ${line2})" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
}

assert_cenci_installer_utility() {
    local output="${CASE_HOME}/cenci-installer-output"
    [[ -L "${CASE_HOME}/.local/bin/cenci-installer" ]]
    HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
        CLAUDE_MARKETPLACE_FILE="${WORK}/wrapper-claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${WORK}/wrapper-claude-installed" \
        CODEX_MARKETPLACE_FILE="${WORK}/wrapper-codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/wrapper-codex-installed" \
        "${CASE_HOME}/.local/bin/cenci-installer" doctor >"${output}"
    assert_contains "${output}" "cenci installer"
}

echo "installer-clients.test.sh"

echo "case: Claude-only installs every component and prints only Claude launch guidance"
run_case claude claude
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_contains "${CASE_CALLS}" "claude plugin install cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "claude plugin install cenci-sandbox@cenci"
assert_contains "${CASE_OUTPUT}" "cn ch|cs|co|cf"
assert_not_contains "${CASE_OUTPUT}" "cn xl|xt|xs"
[[ -L "${CASE_HOME}/.local/bin/cn" ]]
[[ -x "${CASE_HOME}/.local/bin/cenci" ]]
[[ -x "${CASE_HOME}/.local/bin/cn" ]]
[[ ! -e "${CASE_HOME}/.local/bin/cenci-sand" ]]
assert_cenci_installer_utility

echo "case: Codex-only installs every component without invoking or recommending Claude"
run_case codex codex
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "codex plugin add cenci@cenci"
assert_contains "${CASE_CALLS}" "codex plugin add cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "codex plugin add cenci-sandbox@cenci"
assert_contains "${CASE_OUTPUT}" "cn xl|xt|xs"
assert_not_contains "${CASE_OUTPUT}" "cn ch|cs|co|cf"
assert_not_contains "${CASE_OUTPUT}" "/cenci:configure"
[[ -L "${CASE_HOME}/.local/bin/cn" ]]
[[ ! -e "${CASE_HOME}/.local/bin/cenci-sand" ]]
[[ ! -e "${CASE_HOME}/.local/bin/sb" ]]
[[ ! -e "${CASE_HOME}/.local/bin/codex-sand" ]]
assert_cenci_installer_utility

echo "case: Codex-only image build runs 'cenci sandbox build' via the cached binary"
prepare_cache_cenci "${WORK}/codex-build/home" codex
run_case codex-build codex --build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "sandbox image built"
assert_contains "${CASE_CALLS}" "cenci sandbox build"
assert_not_contains "${CASE_CALLS}" "--agents"

echo "case: image builds no longer select or bake agent CLIs"
prepare_cache_cenci "${WORK}/build-default/home" claude
run_case build-default dual --build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "cenci sandbox build"
assert_not_contains "${CASE_CALLS}" "--agents"

echo "case: the removed installer --agents option is rejected"
prepare_cache_cenci "${WORK}/agents-removed/home" claude
run_case agents-removed dual --build --agents claude
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "unknown option '--agents'"
assert_not_contains "${CASE_CALLS}" "cenci sandbox build"

echo "case: a stale cenci-sand symlink is repointed at cenci, never recreated"
name=stale-sand
mkdir -p "${WORK}/${name}/home/.local/bin"
ln -s /nonexistent/old-cenci-sand "${WORK}/${name}/home/.local/bin/cenci-sand"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
[[ -L "${CASE_HOME}/.local/bin/cenci-sand" ]]
[[ "$(readlink "${CASE_HOME}/.local/bin/cenci-sand")" == "${CASE_HOME}/.local/bin/cenci" ]]
assert_contains "${CASE_OUTPUT}" "cenci-sand is deprecated"

echo "case: dual-client installs independently and exposes the cn launcher"
run_case dual dual
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_contains "${CASE_CALLS}" "codex plugin add cenci@cenci"
[[ -L "${CASE_HOME}/.local/bin/cn" ]]
[[ ! -e "${CASE_HOME}/.local/bin/cenci-sand" ]]
[[ ! -e "${CASE_HOME}/.local/bin/sb" ]]
[[ ! -e "${CASE_HOME}/.local/bin/codex-sand" ]]
assert_cenci_installer_utility

# --- Supply chain: an already-registered marketplace is removed and
# re-added at the resolved ref rather than merely refreshed in place (#625,
# Q4) — the deterministic remove+re-add contract, since neither client's
# in-place `add`/`update`/`upgrade` semantics for repointing an existing
# registration to a different ref are verified.

echo "case: an already-registered marketplace at a different ref is removed then re-added at the resolved ref, for both clients (#625, Q4)"
name=already-registered-different-ref
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
make_codex "${bin}"
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude
printf 'main' >"${case_dir}/claude-marketplace"
printf 'main' >"${case_dir}/codex-marketplace"
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
    CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
    bash "${ROOT}/install.sh" --yes --no-build --ref watch/v9.9.9 >"${output}" 2>&1
already_registered_exit=$?
set -e
[[ "${already_registered_exit}" -eq 0 ]]
assert_before "${calls}" "claude plugin marketplace remove cenci" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"
assert_before "${calls}" "codex plugin marketplace remove cenci" "codex plugin marketplace add matteobortolazzo/cenci --ref watch/v9.9.9"
assert_contains "${case_dir}/claude-marketplace" "watch/v9.9.9"
assert_contains "${case_dir}/codex-marketplace" "watch/v9.9.9"

echo "case: install repairs a missing core plugin without substring false positives"
name=repair-missing-core
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace"
touch "${WORK}/${name}/claude-installed-cenci-watch"
touch "${WORK}/${name}/claude-installed-cenci-sandbox"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-sandbox@cenci"

echo "case: rerunning install updates every existing required plugin"
name=reconcile-existing
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace"
for p in cenci cenci-watch cenci-sandbox; do
    touch "${WORK}/${name}/claude-installed-${p}"
done
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin update cenci@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-sandbox@cenci"

echo "case: marketplace re-registration is aborted when removing the existing registration fails — the destructive-prerequisite guard fires, no re-add is attempted (#625)"
name=refresh-failure
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace" "${WORK}/${name}/claude-refresh-fail"
run_case "${name}" claude
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "could not remove the existing marketplace registration"
assert_not_contains "${CASE_OUTPUT}" "Claude: cenci updated"
assert_not_contains "${CASE_CALLS}" "claude plugin marketplace add"

echo "case: a plugin-list query failure is visible and non-zero, not a false 'not installed'"
name=list-failure
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace" "${WORK}/${name}/claude-list-fail"
run_case "${name}" claude
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "could not query installed plugins"

echo "case: a fresh install degrades gracefully when the cenci-watch bootstrap can't provision a binary yet"
run_case_broken_cenci_cache bootstrap-unavailable-install
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "could not provision the cenci binary yet"
assert_not_contains "${CASE_OUTPUT}" "could not provision the cenci binary from the installed cenci-watch plugin"

echo "case: an update still hard-fails when NO cenci binary can be provisioned anywhere"
run_case_broken_cenci_cache bootstrap-unavailable-update update
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "could not provision the cenci binary from the installed cenci-watch plugin"

echo "case: an update tolerates an unpublished newest release by keeping the previously provisioned binary"
run_case_stale_newest_cenci_cache bootstrap-stale-newest-update update
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "the newest cenci-watch release isn't available yet"
assert_not_contains "${CASE_OUTPUT}" "could not provision the cenci binary from the installed cenci-watch plugin"
# Fell back to the previously provisioned (1.0.0) binary and restarted the daemon with it.
assert_contains "${CASE_CALLS}" "cenci daemon restart"

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
assert_contains "${DOCTOR_OUTPUT}" "cn launcher (cenci open)"
assert_not_contains "${DOCTOR_OUTPUT}" "cenci-sand launcher"
assert_contains "${DOCTOR_OUTPUT}" "Codex: cenci"
assert_contains "${DOCTOR_OUTPUT}" "cenci-installer utility"
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon"
assert_not_contains "${DOCTOR_OUTPUT}" "binary not found yet"

echo "case: doctor's Codex notification hint reflects the user-level config.toml, never a differing project-level override Codex itself ignores (#416)"
name=doctor-codex-config-precedence
run_case "${name}" codex
[[ "${CASE_EXIT}" -eq 0 ]]
project="${WORK}/${name}/project"
mkdir -p "${project}/.codex" "${CASE_HOME}/.codex"
git -C "${project}" init -q
printf 'notification_method = "osc9"\n' >"${CASE_HOME}/.codex/config.toml"
printf 'notification_method = "bel"\n' >"${project}/.codex/config.toml"
output="${WORK}/${name}/doctor-precedence-output"
set +e
(
    cd "${project}" &&
    env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
        CODEX_MARKETPLACE_FILE="${WORK}/${name}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/${name}/codex-installed" \
        bash "${ROOT}/install.sh" doctor
) >"${output}" 2>&1
precedence_exit=$?
set -e
[[ "${precedence_exit}" -eq 0 ]]
assert_contains "${output}" "Codex notification_method: osc9"
assert_not_contains "${output}" "Codex notification_method: bel"

echo "case: doctor reports the cenci daemon as running"
run_doctor_case_with_daemon_status doctor-daemon-up claude 0
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon: running"

echo "case: doctor reports the cenci daemon as not running, without failing doctor"
run_doctor_case_with_daemon_status doctor-daemon-down claude 1
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon: not running"

echo "case: doctor reports sysbox-runc registered and its resolved version when present under Docker, resolving the runtime path from docker info's Runtimes JSON (#587 presence + #630 version reporting, Q2)"
sysbox_present_bin="${WORK}/sysbox-binaries/doctor-sysbox-present/sysbox-runc"
make_sysbox_runc_binary "${sysbox_present_bin}" "sysbox-runc version 0.6.4"
run_doctor_case_with_docker_runtimes doctor-sysbox-present claude \
    "{\"runc\":{\"path\":\"runc\"},\"sysbox-runc\":{\"path\":\"${sysbox_present_bin}\"}}"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "sysbox-runc registered"
assert_contains "${DOCTOR_OUTPUT}" "0.6.4"

echo "case: doctor reports the sysbox-runc version as unknown when it's registered but both the JSON path and the PATH fallback fail to resolve a version (#630's Q2 fallback chain: JSON path -> PATH -> unknown)"
run_doctor_case_with_docker_runtimes doctor-sysbox-present-version-unknown claude \
    '{"runc":{"path":"runc"},"sysbox-runc":{"path":"/nonexistent/sysbox-runc-binary-630"}}'
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "sysbox-runc registered"
assert_contains "${DOCTOR_OUTPUT}" "unknown"

echo "case: doctor falls back to a bare 'sysbox-runc --version' on PATH when the JSON path field is missing, and reports the resolved version (#630's Q2 fallback chain)"
run_doctor_case_with_sysbox_path_fallback doctor-sysbox-path-fallback claude \
    '{"runc":{"path":"runc"},"sysbox-runc":{}}' "sysbox-runc version 0.5.2"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "sysbox-runc registered"
assert_contains "${DOCTOR_OUTPUT}" "0.5.2"

echo "case: doctor self-skips sysbox with a neutral line (no failure marker) when Docker has no sysbox-runc runtime, and never fails doctor"
run_doctor_case_with_docker_runtimes doctor-sysbox-absent claude '{"runc":{"path":"runc"}}'
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "sysbox-runc not registered"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ sysbox-runc"

echo "case: doctor reports a distinct 'could not query docker info' line (not 'not registered') when docker info itself fails, and never fails doctor (#587 review fix)"
run_doctor_case_with_docker_info_failure doctor-sysbox-docker-info-fail claude
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "could not query docker info — sysbox-runc status unknown"
assert_not_contains "${DOCTOR_OUTPUT}" "sysbox-runc not registered"

echo "case: doctor distinguishes malformed/unparsable docker info Runtimes JSON from both a query failure and a genuine absence (#630's 4-state parsing: query-fail / malformed / absent / present)"
run_doctor_case doctor-sysbox-malformed claude "DOCKER_INFO_MALFORMED=1"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "malformed"
assert_not_contains "${DOCTOR_OUTPUT}" "sysbox-runc not registered"
assert_not_contains "${DOCTOR_OUTPUT}" "could not query docker info — sysbox-runc status unknown"

echo "case: doctor probes Docker's sysbox registration even when Podman is the preferred runtime for ordinary launches — the exact dual-runtime bug #630 fixes: sysbox dind always requires Docker as the outer runtime regardless of container_runtime()'s podman-first launch preference"
run_doctor_case_with_podman_preferred_and_docker_runtimes doctor-sysbox-podman-preferred-docker-present claude \
    '{"runc":{"path":"runc"},"sysbox-runc":{"path":"runc"}}'
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "sysbox-runc registered"

echo "case: doctor warns when cosign is absent — a new mandatory prerequisite for the default verified piped install path (#626), without hard-failing doctor"
run_doctor_case_with_cosign doctor-cosign-absent claude absent
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cosign"
assert_contains "${DOCTOR_OUTPUT}" "install cosign"

echo "case: doctor reports cosign present without the absence warning (#626)"
run_doctor_case_with_cosign doctor-cosign-present claude present
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cosign"
assert_not_contains "${DOCTOR_OUTPUT}" "install cosign"

echo "case: doctor fails when no supported client is available"
run_doctor_case doctor-none none
[[ "${DOCTOR_EXIT}" -ne 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "no supported client"

echo "case: host secrets in the parent env never reach captured calls or output (regression, #353)"
export OPENAI_API_KEY="sk-test-sentinel-should-not-leak"
export CONTEXT7_API_KEY="ctx7-test-sentinel-should-not-leak"
run_case sentinel-secrets claude
unset OPENAI_API_KEY CONTEXT7_API_KEY
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CALLS}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${CASE_OUTPUT}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${CASE_CALLS}" "ctx7-test-sentinel-should-not-leak"
assert_not_contains "${CASE_OUTPUT}" "ctx7-test-sentinel-should-not-leak"

# --- Direct coverage of the repo-root `cenci` wrapper itself (the stable
# front door installed at ~/.local/bin/cenci-installer): it decides whether
# to refresh the owning client's marketplace checkout before exec'ing
# install.sh, and refuses to run as an updater from an unmanaged copy.

echo "case: the cenci wrapper's --help skips the marketplace refresh entirely"
name=wrapper-help
home="${WORK}/${name}/home" bin="${WORK}/${name}/bin"
output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
prepare_checkout "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    "${home}/.claude/plugins/marketplaces/cenci/cenci" --help >"${output}" 2>&1
wrapper_help_exit=$?
set -e
[[ "${wrapper_help_exit}" -eq 0 ]]
assert_contains "${output}" "Usage:"
assert_not_contains "${calls}" "claude plugin marketplace update"

echo "case: the cenci wrapper still resolves ~/.claude when it is itself a symlink, and lets install.sh's own marketplace step register cenci at the resolved ref (no wrapper-side refresh needed since #625)"
name=wrapper-symlinked-claude-home
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"

real_claude_dir="${home}/dot-claude"
checkout="${real_claude_dir}/plugins/marketplaces/cenci"
mkdir -p "${checkout}"
cp "${ROOT}/cenci" "${checkout}/cenci"
chmod +x "${checkout}/cenci"
cp "${ROOT}/install.sh" "${checkout}/install.sh"
ln -s "${real_claude_dir}" "${home}/.claude"
prepare_bootstrap_cenci "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    "${home}/.claude/plugins/marketplaces/cenci/cenci" update --yes >"${output}" 2>&1
wrapper_update_exit=$?
set -e
[[ "${wrapper_update_exit}" -eq 0 ]]
assert_contains "${calls}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v1.0.0"
assert_not_contains "${calls}" "claude plugin marketplace update cenci"

echo "case: the cenci wrapper refuses to update from an unexpected installer root, but --help still works"
name=wrapper-unexpected-root
plain="${WORK}/${name}/plain" bin="${WORK}/${name}/bin"
mkdir -p "${plain}" "${WORK}/${name}/home" "${bin}"
make_common_tools "${bin}"
cp "${ROOT}/cenci" "${plain}/cenci"
chmod +x "${plain}/cenci"
cp "${ROOT}/install.sh" "${plain}/install.sh"

update_output="${WORK}/${name}/update-output"
set +e
env -i HOME="${WORK}/${name}/home" PATH="${bin}" \
    "${plain}/cenci" update >"${update_output}" 2>&1
wrapper_root_update_exit=$?
set -e
[[ "${wrapper_root_update_exit}" -ne 0 ]]
assert_contains "${update_output}" "refusing to update from unexpected installer root"

help_output="${WORK}/${name}/help-output"
set +e
env -i HOME="${WORK}/${name}/home" PATH="${bin}" \
    "${plain}/cenci" --help >"${help_output}" 2>&1
wrapper_root_help_exit=$?
set -e
[[ "${wrapper_root_help_exit}" -eq 0 ]]
assert_contains "${help_output}" "Usage:"

# --- OpenCode wiring (#491): install.sh has no HAS_OPENCODE detection,
# doctor_opencode_support, or step_opencode_setup yet, so every case below
# currently fails (RED). It's expected to pass end-to-end once #491 lands.
#
# Assumed doctor-output contract these cases pin down for the implementation
# to satisfy (mirrors doctor_codex_support's existing wording style):
#   "OpenCode detected" / "OpenCode not detected"            — Supported clients
#   "OpenCode <ver> supports cenci integration"               — diagnostic 1 (installed+version)
#   "OpenCode <ver> is unsupported — cenci requires 1.18.3 or newer"
#   "OpenCode provider authenticated" / "... not authenticated" — diagnostic 2
#   "OpenCode skills linked" / "OpenCode skills not linked"     — diagnostic 3 (part a)
#   "OpenCode plugin registered" / "OpenCode plugin not registered" — diagnostic 3 (part b)

echo "case: OpenCode absent does not alter Claude/Codex install output (regression guard, #491)"
run_case_opencode opencode-absent-install claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_not_contains "${CASE_OUTPUT}" "OpenCode"

echo "case: OpenCode absent still separates Claude/Codex detection from an explicit 'not detected' doctor line"
run_opencode_doctor_case opencode-absent-doctor claude absent
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Claude Code detected"
assert_contains "${DOCTOR_OUTPUT}" "OpenCode not detected"

echo "case: OpenCode detected reports itself and its version in doctor output"
run_opencode_doctor_case opencode-version-ok claude 1.18.3
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "OpenCode detected"
assert_contains "${DOCTOR_OUTPUT}" "OpenCode 1.18.3 supports cenci integration"

echo "case: an OpenCode version below 1.18.3 is flagged unsupported, not a hard doctor failure"
run_opencode_doctor_case opencode-version-old claude 1.17.0
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "OpenCode 1.17.0 is unsupported — cenci requires 1.18.3 or newer"

echo "case: OpenCode provider authentication is flagged absent when neither env vars nor auth.json are present"
run_opencode_doctor_case opencode-provider-absent claude 1.18.3
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "OpenCode provider not authenticated"

echo "case: OpenCode provider authentication is reported via an API key env var — no live API call"
name=opencode-provider-env
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
output="${WORK}/${name}/doctor-env-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    ANTHROPIC_API_KEY="sk-ant-test-not-a-real-key" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
provider_env_exit=$?
set -e
[[ "${provider_env_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode provider authenticated"

echo "case: OpenCode provider authentication is also reported via ~/.local/share/opencode/auth.json"
name=opencode-provider-authjson
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
mkdir -p "${CASE_HOME}/.local/share/opencode"
printf '{}\n' >"${CASE_HOME}/.local/share/opencode/auth.json"
output="${WORK}/${name}/doctor-authjson-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
provider_authjson_exit=$?
set -e
[[ "${provider_authjson_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode provider authenticated"

echo "case: OpenCode's cenci-integration diagnostics report independently — skills linked but the plugin not yet registered"
# Provisioned with opencode "absent" during the install run itself so
# step_opencode_setup never auto-links/auto-registers (which would otherwise
# fully complete both diagnostics before this case gets a chance to build a
# partial state — see the idempotent case below, which pins that a plain
# install run really does fully link+register when opencode is present).
# The opencode mock is added only for the follow-up doctor invocation, so
# doctor still detects and reports its version.
name=opencode-skills-only
run_case_opencode "${name}" claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
make_opencode "${CASE_BIN}" 1.18.3
mkdir -p "${CASE_HOME}/.config/opencode/skills"
ln -s "${CASE_HOME}/.claude/plugins/marketplaces/cenci/flow/skills/testing" "${CASE_HOME}/.config/opencode/skills/testing"
output="${WORK}/${name}/doctor-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
skills_only_exit=$?
set -e
[[ "${skills_only_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode 1.18.3 supports cenci integration"
assert_contains "${output}" "OpenCode skills linked"
assert_contains "${output}" "OpenCode plugin not registered"

echo "case: OpenCode's cenci-integration diagnostics report independently — plugin registered but skills not yet linked"
# Same "absent" provisioning rationale as the skills-only case above: keeps
# step_opencode_setup from auto-completing both diagnostics before this case
# builds its own partial (plugin-only) state.
name=opencode-plugin-only
run_case_opencode "${name}" claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
make_opencode "${CASE_BIN}" 1.18.3
mkdir -p "${CASE_HOME}/.config/opencode"
printf '{"plugin": ["file://%s/watch/plugin/opencode"]}\n' "${CASE_HOME}/.claude/plugins/marketplaces/cenci" >"${CASE_HOME}/.config/opencode/opencode.json"
output="${WORK}/${name}/doctor-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
plugin_only_exit=$?
set -e
[[ "${plugin_only_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode plugin registered"
assert_contains "${output}" "OpenCode skills not linked"

echo "case: re-running install links OpenCode's portable skills idempotently and never duplicates the plugin entry"
name=opencode-idempotent
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "linked: 12, skipped: 0"
[[ -L "${CASE_HOME}/.config/opencode/skills/testing" ]]
[[ "$(grep -c 'watch/plugin/opencode' "${CASE_HOME}/.config/opencode/opencode.json")" -eq 1 ]]

second_output="${WORK}/${name}/second-run-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${second_output}" 2>&1
second_run_exit=$?
set -e
[[ "${second_run_exit}" -eq 0 ]]
assert_contains "${second_output}" "linked: 0, skipped: 12"
[[ "$(grep -c 'watch/plugin/opencode' "${CASE_HOME}/.config/opencode/opencode.json")" -eq 1 ]]

echo "case: uninstall removes only cenci's OpenCode skill symlinks and plugin entry, preserving the user's own config and skills"
name=opencode-uninstall
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
echo "user content" >"${CASE_HOME}/.config/opencode/skills/my-own-skill"
jq '. + {"theme": "dark"}' "${CASE_HOME}/.config/opencode/opencode.json" >"${CASE_HOME}/.config/opencode/opencode.json.tmp"
mv "${CASE_HOME}/.config/opencode/opencode.json.tmp" "${CASE_HOME}/.config/opencode/opencode.json"

uninstall_output="${WORK}/${name}/uninstall-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" uninstall --yes >"${uninstall_output}" 2>&1
opencode_uninstall_exit=$?
set -e
[[ "${opencode_uninstall_exit}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.config/opencode/skills/testing" ]]
[[ -f "${CASE_HOME}/.config/opencode/skills/my-own-skill" ]]
assert_contains "${CASE_HOME}/.config/opencode/opencode.json" "theme"
assert_not_contains "${CASE_HOME}/.config/opencode/opencode.json" "watch/plugin/opencode"

echo "case: OpenCode plugin registration is recognized (install, doctor, and uninstall) even when find_plugin_path resolves watch/plugin/opencode via the plugin-cache fallback branch, not the marketplace checkout (#491 review fix — substring-matching bug)"
name=opencode-cache-fallback
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
make_opencode "${bin}" 1.18.3
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude
prepare_opencode_cache_fallback_assets "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
cache_fallback_install_exit=$?
set -e
[[ "${cache_fallback_install_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode plugin registered"
assert_contains "${home}/.config/opencode/opencode.json" "cenci-watch/1.0.0/opencode"

doctor_output="${WORK}/${name}/doctor-output"
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${doctor_output}" 2>&1
cache_fallback_doctor_exit=$?
set -e
[[ "${cache_fallback_doctor_exit}" -eq 0 ]]
assert_contains "${doctor_output}" "OpenCode plugin registered"

uninstall_output="${WORK}/${name}/uninstall-output"
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" uninstall --yes >"${uninstall_output}" 2>&1
cache_fallback_uninstall_exit=$?
set -e
[[ "${cache_fallback_uninstall_exit}" -eq 0 ]]
assert_not_contains "${home}/.config/opencode/opencode.json" "cenci-watch/1.0.0/opencode"

echo "case: a cenci-watch that never actually installs is pruned from SELECTED, so step_opencode_setup skips OpenCode plugin registration entirely instead of registering a deselected plugin's asset (#491 review fix — missing selected gate)"
name=opencode-cenci-watch-deselected
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
make_opencode "${bin}" 1.18.3
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude
prepare_opencode_checkout_assets "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    CLAUDE_SKIP_INSTALL_PLUGIN="cenci-watch" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
deselected_exit=$?
set -e
[[ "${deselected_exit}" -eq 0 ]]
assert_not_contains "${output}" "Setting up OpenCode integration"
assert_not_contains "${output}" "OpenCode plugin registered"
[[ ! -e "${home}/.config/opencode/opencode.json" ]]

# --- Supply chain: install.sh defaults to an immutable release tag instead
# of main (#590), via cenci_latest_ref() and a piped-only re-exec guard.

echo "case: a default piped run resolves the latest release tag and re-execs install.sh from it, even with a coincidentally-named readable file called 'bash' sitting in the caller's CWD (run_piped_case always plants one — see its doc comment) — proves the guard's bare-word/no-path-separator detection isn't fooled into a false 'from-file' read (#590 review fix)"
run_piped_case piped-default claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9"
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CURL_LOG}" "releases/latest"
assert_contains "${CASE_CURL_LOG}" "watch/v9.9.9"
assert_contains "${CASE_CURL_LOG}" "install.sh"
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: CENCI_REF=main skips release-tag resolution entirely on a piped run (#590)"
run_piped_case piped-cenci-ref-main claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_REF=main"
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CURL_LOG}" "releases/latest"
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: --ref main skips release-tag resolution entirely on a piped run (#590)"
run_piped_case piped-ref-flag-main claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9" --ref main
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CURL_LOG}" "releases/latest"
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: an unresolvable release tag (offline / no releases yet) hard-fails with a clear error naming the CENCI_REF=main override, never a silent fall-through (#590, AC5)"
run_piped_case piped-resolve-failure claude "CENCI_FAKE_NO_RELEASE=1"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"

echo "case: a malformed/injected release-tag redirect (failing the ^(flow|watch|sandbox)/v[0-9]+.[0-9]+.[0-9]+\$ regex) is rejected and never flows into a download URL (#590, trust boundary)"
run_piped_case piped-malformed-tag claude "CENCI_FAKE_RELEASE_TAG=../../etc/passwd"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CURL_LOG}" "../../etc/passwd"

echo "case: a resolved release tag whose install.sh download is empty (curl -fsSL exits 0 on a truncated/dropped body) hard-fails with a clear error naming the CENCI_REF=main override, never a silent no-op (#590, AC5)"
run_piped_case piped-empty-download claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_FAKE_EMPTY_INSTALL=1"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"

# --- Supply chain: verify the release-pinned install.sh + install.sh.bundle
# with cosign before executing them (#626). Every case below reuses
# run_piped_case's default "cosign present, pass mode" mock unless
# specifically exercising cosign's own absence/failure.

echo "case: install.sh + install.sh.bundle both download successfully and cosign verify-blob succeeds — the verified installer proceeds to run (#626 AC2/AC7)"
run_piped_case piped-verify-valid claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9"
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CURL_LOG}" "install.sh.bundle"
assert_contains "${CASE_COSIGN_LOG}" "verify-blob"
assert_contains "${CASE_COSIGN_LOG}" "--certificate-identity-regexp"
assert_contains "${CASE_COSIGN_LOG}" "--certificate-oidc-issuer"
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: install.sh.bundle download failure hard-fails with a distinct message naming the bundle, never falls back to main (#626 AC2/AC4)"
run_piped_case piped-verify-missing-bundle claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_FAKE_MISSING_BUNDLE=1"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "could not download install.sh.bundle"
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: an empty (but present) install.sh.bundle hard-fails with a distinct message, never falls back to main (#626 AC3/AC4)"
run_piped_case piped-verify-empty-bundle claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_FAKE_EMPTY_BUNDLE=1"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "install.sh.bundle"
assert_contains "${CASE_OUTPUT}" "was empty"
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: a non-empty but corrupted/tampered install.sh download fails cosign verify-blob on the real byte mismatch, never falls back to main — testing is not limited to a zero-byte body (#626 AC3/AC4)"
run_piped_case piped-verify-corrupt-install claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_FAKE_CORRUPT_INSTALL=1"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "installer verification failed"
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: cosign absent from PATH hard-fails with a distinct 'cosign not found' message, never falls back to main — cosign is a new mandatory prerequisite (#626)"
run_piped_case piped-verify-cosign-missing claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 COSIGN_MODE=absent"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "cosign not found"
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: cosign present but verify-blob exits non-zero hard-fails with the same 'installer verification failed' message class as a corrupt install.sh, never falls back to main (#626 AC4)"
run_piped_case piped-verify-cosign-nonzero claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 COSIGN_MODE=fail"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "installer verification failed"
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"

echo "case: a normal from-file install.sh run never re-execs itself via the piped-only pin block (regression — proves the piped-only guard can't break existing from-file suites, #590), but — per #625's Q1 — DOES resolve and pin the marketplace + plugin content to the default release ref since CENCI_REF is unset, not live main"
name=onfile-no-reexec-but-pins-default-ref
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
curl_log="${WORK}/${name}/curl-log"
mkdir -p "${home}" "${bin}"
: >"${calls}"
: >"${curl_log}"
make_common_tools "${bin}"
make_claude "${bin}"
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" ROOT="${ROOT}" CURL_LOG="${curl_log}" \
    CENCI_FAKE_RELEASE_TAG="watch/v9.9.9" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
onfile_no_probe_exit=$?
set -e
[[ "${onfile_no_probe_exit}" -eq 0 ]]
# The piped-only re-exec pin block never fires from a file: no fresh
# install.sh download is staged and re-exec'd (#590 regression).
assert_not_contains "${curl_log}" "/install.sh"
assert_contains "${calls}" "claude plugin install cenci@cenci"
# #625, Q1 + test case 2: a from-file run with CENCI_REF unset still
# resolves the latest release tag (a *different*, new call path than the
# #590 piped pin block above) and pins the marketplace + installed plugin
# content to it — never main.
assert_contains "${curl_log}" "releases/latest"
assert_contains "${calls}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"
assert_contains "${case_dir}/claude-installed-cenci" "watch/v9.9.9"
assert_not_contains "${case_dir}/claude-installed-cenci" "@main"

echo "case: cenci-installer update (from-file, CENCI_REF unset) also resolves and pins the marketplace + plugin content to the default release ref, never main — covers the update-mode dispatch path distinctly from plain install (#625, Q1)"
name=update-mode-pins-default-ref
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_codex "${bin}"
prepare_checkout "${home}" codex
prepare_bootstrap_cenci "${home}" codex
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CENCI_FAKE_RELEASE_TAG="watch/v9.9.9" \
    CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
    CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
    bash "${ROOT}/install.sh" --yes --no-build update >"${output}" 2>&1
update_mode_exit=$?
set -e
[[ "${update_mode_exit}" -eq 0 ]]
assert_contains "${calls}" "codex plugin marketplace add matteobortolazzo/cenci --ref watch/v9.9.9"
assert_contains "${case_dir}/codex-installed-cenci" "watch/v9.9.9"

# --- Supply chain: CENCI_REF/--ref pins marketplace manifests and installed
# plugin content — not just the installer re-exec — for every entry path
# (#625). See docs/getting-started.md and README.md for the user-facing
# summary of this contract.

echo "case: a default piped run (CENCI_FAKE_RELEASE_TAG set, CENCI_REF unset) pins the marketplace registration and installed plugin content to the resolved release ref, not main, for Claude (#625, test case 1)"
run_piped_case piped-default-ref-pin-claude claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9"
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_contains "${WORK}/piped-default-ref-pin-claude/claude-installed-cenci" "watch/v9.9.9"
assert_not_contains "${WORK}/piped-default-ref-pin-claude/claude-installed-cenci" "@main"

echo "case: a default piped run (CENCI_FAKE_RELEASE_TAG set, CENCI_REF unset) pins the marketplace registration and installed plugin content to the resolved release ref, not main, for Codex (#625, test case 1)"
run_piped_case piped-default-codex codex "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9"
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "codex plugin marketplace add matteobortolazzo/cenci --ref watch/v9.9.9"
assert_contains "${CASE_CALLS}" "codex plugin add cenci@cenci"
assert_contains "${WORK}/piped-default-codex/codex-installed-cenci" "watch/v9.9.9"
assert_not_contains "${WORK}/piped-default-codex/codex-installed-cenci" "main"

echo "case: an explicit --ref <tag-or-commit> (from-file) pins the marketplace + installed plugin content to exactly that ref for Claude, no resolution involved (#625, test case 3)"
run_case explicit-ref-claude claude --no-build --ref watch/v2.3.4
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v2.3.4"
assert_contains "${WORK}/explicit-ref-claude/claude-installed-cenci" "watch/v2.3.4"

echo "case: an explicit --ref <tag-or-commit> (from-file) pins the marketplace + installed plugin content to exactly that ref for Codex, no resolution involved (#625, test case 3)"
run_case explicit-ref-codex codex --no-build --ref watch/v2.3.4
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "codex plugin marketplace add matteobortolazzo/cenci --ref watch/v2.3.4"
assert_contains "${WORK}/explicit-ref-codex/codex-installed-cenci" "watch/v2.3.4"

echo "case: --ref main is the only path that actually registers the marketplace and installs plugin content at main for Claude (#625, test case 4, AC3)"
run_case explicit-main claude --no-build --ref main
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin marketplace add matteobortolazzo/cenci@main"
assert_contains "${WORK}/explicit-main/claude-installed-cenci" "main"

echo "case: CENCI_REF=main is the only path that actually registers the marketplace and installs plugin content at main for Codex (#625, test case 4, AC3)"
name=explicit-main-env
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_codex "${bin}"
prepare_checkout "${home}" codex
prepare_bootstrap_cenci "${home}" codex
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" CENCI_REF=main \
    CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
    CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
explicit_main_env_exit=$?
set -e
[[ "${explicit_main_env_exit}" -eq 0 ]]
assert_contains "${calls}" "codex plugin marketplace add matteobortolazzo/cenci --ref main"
assert_contains "${case_dir}/codex-installed-cenci" "main"

echo "case: a malformed --ref value (space/shell metacharacter) is rejected before any marketplace mutation, never silently accepted (#625, test case 6)"
name=malformed-ref
run_case "${name}" claude --no-build --ref 'bad ref!'
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "invalid ref"
assert_not_contains "${CASE_CALLS}" "marketplace add"
assert_not_contains "${CASE_CALLS}" "marketplace remove"

echo "case: a --ref value containing a '..' path segment is rejected before any marketplace mutation — closes the gap where the ref_available manifest probe could resolve a different ref's content than what gets registered with the client CLI (#625 review fix)"
name=dotdot-ref
run_case "${name}" claude --no-build --ref '../cenci/main'
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "invalid ref"
assert_not_contains "${CASE_CALLS}" "marketplace add"
assert_not_contains "${CASE_CALLS}" "marketplace remove"

echo "case: a --ref value with a leading '-' is rejected before any marketplace mutation — closes the gap where Codex's --ref <value> invocation risks the value being misparsed as another flag (#625 review fix)"
name=leading-dash-ref
run_case "${name}" claude --no-build --ref '-x'
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "invalid ref"
assert_not_contains "${CASE_CALLS}" "marketplace add"
assert_not_contains "${CASE_CALLS}" "marketplace remove"

echo "case: a syntactically valid but unavailable ref (marketplace.json probe 404s) is rejected before any marketplace mutation, never falls through to main (#625, test case 7)"
name=unavailable-ref
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" CENCI_FAKE_REF_UNAVAILABLE=1 \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build --ref does-not-exist >"${output}" 2>&1
unavailable_ref_exit=$?
set -e
[[ "${unavailable_ref_exit}" -ne 0 ]]
assert_contains "${output}" "not available"
assert_contains "${output}" "CENCI_REF=main"
assert_not_contains "${calls}" "marketplace add"
assert_not_contains "${calls}" "@main"
assert_not_contains "${calls}" "--ref main"

echo "case: an unresolvable release (offline, no release published yet) hard-fails on a from-file cenci-installer update run with the CENCI_REF=main hint, before any marketplace mutation (#625, test case 8, Q2)"
name=unresolvable-release-from-file
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" CENCI_FAKE_NO_RELEASE=1 \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build update >"${output}" 2>&1
unresolvable_release_exit=$?
set -e
[[ "${unresolvable_release_exit}" -ne 0 ]]
assert_contains "${output}" "CENCI_REF=main"
assert_not_contains "${calls}" "marketplace add"
assert_not_contains "${calls}" "marketplace remove"

# --- doc regression assertions (#630) ----------------------------------------
# Direct content checks against the checked-out repo's own doc files (no
# install.sh run involved) — the natural home per the plan's Assumptions,
# since no dedicated docs-guidance.test.sh exists yet and these are small,
# focused regression assertions alongside the installer's own doc-content
# conventions.

echo "case: the retired --docker (DooD host-socket) guidance is no longer present in the sandbox boundary diagram"
assert_not_contains "${ROOT}/docs/assets/cenci-sandbox-boundary.svg" "--docker mounts the host socket"

echo "case: watch/README.md's canonical CLI reference includes direct cenci open --dind and --no-dind examples"
assert_contains "${ROOT}/watch/README.md" "cenci open --dind"
assert_contains "${ROOT}/watch/README.md" "cenci open --no-dind"

echo "case: SECURITY.md describes DinD volumes as potentially sensitive application/build state"
assert_contains "${ROOT}/SECURITY.md" "potentially sensitive application/build state"

echo "case: SECURITY.md explains DinD retains an inner daemon and shared-kernel attack surface rather than implying VM-grade isolation"
assert_contains "${ROOT}/SECURITY.md" "inner daemon"
assert_contains "${ROOT}/SECURITY.md" "shared-kernel attack surface"
assert_not_contains "${ROOT}/SECURITY.md" "VM-grade isolation"

echo "passed: client detection, installation, launchers, and summaries"
