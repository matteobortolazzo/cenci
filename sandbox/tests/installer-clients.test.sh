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
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pkill pgrep nohup chmod sed head tail cut tr rm mktemp jq mv sort cp cmp sha256sum; do
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
    # cosign mock: ambient like git/curl for every doctor-relevant and
    # update-mode setup path (#700), since promoting doctor's cosign check
    # from optional to required, plus the new update-mode bootstrap's own
    # hard cosign requirement, would otherwise fail nearly every existing
    # doctor/update case — none of which previously planted a cosign mock.
    # Default "pass" mode (make_cosign, defined below — forward reference is
    # fine since this only runs once every function in the file is defined).
    # A case that needs cosign genuinely absent must `rm -f "${bin}/cosign"`
    # after calling make_common_tools, not merely skip a call that no longer
    # needs skipping.
    make_cosign "${bin}"
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
# file, a --certificate-oidc-issuer of https://token.actions.githubusercontent.com,
# a target file that exists, AND that target file's bytes exactly match the
# known-good reference $ROOT/install.sh. That last check is what makes
# CENCI_FAKE_CORRUPT_INSTALL (a genuinely tampered download) fail
# verification on its own, the same way a real signature mismatch would,
# without any separate opt-in from the caller — a real decoy, not a rubber
# stamp.
#
# --certificate-identity-regexp gets a *behavioral* contract (#736) instead
# of the old literal-string match against one fixed pattern: rather than
# asserting what the regexp's source text looks like, the mock takes the
# regexp actually passed by the caller and, via `grep -E` (bash `[[ =~ ]]`
# and POSIX ERE agree on this pattern's syntax), proves it behaves the way
# the real Sigstore certificate-identity matcher must against real-world
# sample identity strings: it must match BOTH documented canonical trigger
# identities — the tag-push identity
# (".../watch-release.yml@refs/tags/watch/v1.2.3") and the
# workflow_dispatch-from-main identity
# (".../watch-release.yml@refs/heads/main", what watch-release.yml actually
# signs with today per plugin-version-bump.yml's `--ref main` dispatch) — and
# must reject six decoys: a different workflow file, a different ref off
# the right workflow, the right workflow/ref off a different repo, a ref
# that merely starts with an allowed ref (`refs/heads/main-attacker`, `refs
# /tags/watch/v1.2.3-evil` — proving the regexp's trailing `$` anchor is
# intact), and a variant with the escaped literal dots swapped for a
# different character (proving they aren't accidentally acting as `.`
# any-char wildcards). Any failure exits non-zero with a message naming
# which check failed, so a regression to an overly narrow OR overly
# permissive regexp fails loudly instead of being silently masked.
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
record_marketplace() {
    printf '%s' "${1:-default-branch}" >"${CLAUDE_MARKETPLACE_FILE}"
    # Modern Claude versions persist the added marketplace in user settings.
    # Opt in for the dedicated compatibility case without changing unrelated
    # legacy-client fixtures in this shared mock.
    if [ "${CLAUDE_ADD_WRITES_SETTINGS:-}" = 1 ]; then
        mkdir -p "${HOME}/.claude"
        settings="${HOME}/.claude/settings.json"
        [ -f "${settings}" ] || printf '{}\n' >"${settings}"
        jq --arg ref "$1" '.extraKnownMarketplaces.cenci = {source: {source: "github", repo: "matteobortolazzo/cenci", ref: $ref}}' \
            "${settings}" >"${settings}.mock-staged"
        mv "${settings}.mock-staged" "${settings}"
    fi
}
case "$*" in
  "plugin marketplace list") [ -f "${CLAUDE_MARKETPLACE_FILE}" ] && echo cenci; exit 0 ;;
  "plugin marketplace add "*)
    # Managed declarations take precedence over user declarations. Any add
    # whose ref differs from the effective entry is refused.
    settings="${HOME}/.claude/settings.json"
    if [ -f "${CENCI_CLAUDE_MANAGED_SETTINGS:-}" ] && jq -e '.extraKnownMarketplaces.cenci' "${CENCI_CLAUDE_MANAGED_SETTINGS}" >/dev/null 2>&1; then
      settings="${CENCI_CLAUDE_MANAGED_SETTINGS}"
    fi
    if [ -f "${settings}" ] && jq -e '.extraKnownMarketplaces.cenci' "${settings}" >/dev/null 2>&1; then
      declared="$(jq -r '.extraKnownMarketplaces.cenci.source.ref // ""' "${settings}")"
      case "$4" in *@*) requested="${4#*@}" ;; *) requested="" ;; esac
      if [ "${requested}" != "${declared}" ]; then
        printf 'Failed to add marketplace: Cannot add marketplace "cenci": its source does not match its extraKnownMarketplaces entry in user or managed settings\n' >&2
        exit 1
      fi
      record_marketplace "${requested}"
      exit 0
    fi
    case "$4" in
      *@*)
        record_marketplace "${4#*@}"
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
# HAS_OPENCODE / doctor_opencode_support resolve via `have opencode` +
# `opencode --version`. Unlike make_claude/make_codex, OpenCode skill
# registration never goes through the `opencode` binary itself (no marketplace
# CLI) — a legacy installer symlinked skills via install-skills.sh and merged
# opencode.json directly, and host setup is now paused — so this mock only
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
    # The default-yes tmux-defaults step (#645) resolves its shipped asset
    # through find_plugin_path, so the fake checkout must carry it.
    mkdir -p "${checkout}/watch/plugin/tmux"
    cp "${ROOT}/watch/plugin/tmux/tmux.conf" "${checkout}/watch/plugin/tmux/tmux.conf"
}

# prepare_git_tagged_checkout <checkout-dir> <tag> — turns an existing
# checkout directory (already populated by prepare_checkout) into a git repo
# with a single commit tagged <tag> and checked out at that tag in detached
# HEAD (mirrors the real `claude`/`codex plugin marketplace add` git
# checkout mechanism, the #625/#678 remove+re-add contract), so `git -C
# <checkout-dir> describe --tags --exact-match HEAD` resolves to exactly
# <tag> — the same primitive both the update-mode bootstrap gate
# (managed_checkout_dir, #700) and the doctor stale-pin check use to
# determine "already current". A local user/email is configured so the
# commit succeeds regardless of the host's own git config.
prepare_git_tagged_checkout() {
    local dir="$1" tag="$2"
    git -C "${dir}" init -q
    git -C "${dir}" config user.email "cenci-test@example.com"
    git -C "${dir}" config user.name "cenci-test"
    git -C "${dir}" add -A
    git -C "${dir}" commit -q -m "checkout at ${tag}"
    git -C "${dir}" tag "${tag}"
    git -C "${dir}" -c advice.detachedHead=false checkout -q "${tag}"
}

# prepare_opencode_checkout_assets <home> <client> — stages the real,
# already-shipped OpenCode assets (flow/opencode/install-skills.sh,
# flow/skills/*, watch/plugin/opencode) into the marketplace checkout at the
# exact relative paths find_plugin_path resolves first (marketplace-checkout-
# first, no cache-fallback stripping needed), so the paused host
# step_opencode_setup has something genuine to leave untouched — mirroring how
# prepare_checkout stages the real cenci wrapper + install.sh rather than a
# fake stand-in. The container-only sandbox/lib/opencode-config.sh is
# deliberately not staged: the host installer never resolves it.
prepare_opencode_checkout_assets() {
    local home="$1" client="$2" checkout
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}/flow/opencode" "${checkout}/watch/plugin"
    cp "${ROOT}/flow/opencode/install-skills.sh" "${checkout}/flow/opencode/install-skills.sh"
    chmod +x "${checkout}/flow/opencode/install-skills.sh"
    cp -r "${ROOT}/flow/skills" "${checkout}/flow/skills"
    cp -r "${ROOT}/watch/plugin/opencode" "${checkout}/watch/plugin/opencode"
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
    mkdir -p "${checkout}/flow/opencode"
    cp "${ROOT}/flow/opencode/install-skills.sh" "${checkout}/flow/opencode/install-skills.sh"
    chmod +x "${checkout}/flow/opencode/install-skills.sh"
    cp -r "${ROOT}/flow/skills" "${checkout}/flow/skills"
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
    # STALE_NEWEST_HH_MODE / STALE_NEWEST_HH_VERSION (optional, prefix
    # assignments, #1419): swap the older binary for a tagged hook-health fake
    # (write_hook_health_fake, tag `cache`) so a test can prove the post-setup
    # checks run against this fallback binary.
    if [[ -n "${STALE_NEWEST_HH_MODE:-}" ]]; then
        write_hook_health_fake "${old_root}/bin/cenci" "${STALE_NEWEST_HH_MODE}" "${STALE_NEWEST_HH_VERSION:-empty}" cache
    fi
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
      # make_common_tools now plants cosign ambiently (#700); genuine
      # absence must remove it again after that setup ran.
      *COSIGN_MODE=absent*) rm -f "${bin}/cosign" ;;
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

# run_update_bootstrap_case <name> <clients> <env_extra> <checkout_tag>
# [install.sh args...] — simulates `cenci update` running straight out of an
# installed, managed marketplace checkout, the way the `cenci` wrapper execs
# it (`installer=${source_dir}/install.sh`, cenci:44): BASH_SOURCE[0]
# resolves inside ~/.claude or ~/.codex's plugins/marketplaces/cenci
# checkout, so the update-mode bootstrap gate (managed_checkout_dir, #700)
# can fire. Unlike run_piped_case (stdin, no on-disk path), this is a
# genuine from-file invocation of the checkout's own install.sh. <env_extra>
# follows run_piped_case's convention: a single word-split string of
# NAME=value pairs layered onto the env -i scrub (pass "" for none); the
# literal token "COSIGN_MODE=absent" additionally removes the ambient
# cosign mock (make_common_tools, #700) so `command -v cosign` genuinely
# fails. <checkout_tag> — pass "" to leave the checkout a plain (non-git)
# directory, which drives the bootstrap gate's fail-closed "can't prove
# currency" branch (`git describe` fails outright); pass a tag (e.g.
# "watch/v0.27.7") to git-tag the checkout via prepare_git_tagged_checkout
# so the gate instead exercises its actual `update_current_ref !=
# RESOLVED_REF` stale-tag comparison. Sets CASE_CURL_LOG / CASE_COSIGN_LOG
# (argv-only invocation logs) alongside the usual run_case CASE_* outputs so
# callers can assert whether/how the bootstrap's releases/latest probe,
# install.sh + install.sh.bundle download, and cosign verify-blob call
# happened.
run_update_bootstrap_case() {
    local name="$1" clients="$2" env_extra="$3" checkout_tag="$4"
    shift 4
    local install_args=("$@")
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    local curl_log="${WORK}/${name}/curl-log" cosign_log="${WORK}/${name}/cosign-log"
    local checkout
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    : >"${curl_log}"
    : >"${cosign_log}"
    make_common_tools "${bin}"
    case "${env_extra}" in
      *COSIGN_MODE=absent*) rm -f "${bin}/cosign" ;;
    esac
    case "${clients}" in
      claude)
        make_claude "${bin}"
        prepare_checkout "${home}" claude
        prepare_bootstrap_cenci "${home}" claude
        checkout="${home}/.claude/plugins/marketplaces/cenci"
        ;;
      codex)
        make_codex "${bin}"
        prepare_checkout "${home}" codex
        prepare_bootstrap_cenci "${home}" codex
        checkout="${home}/.codex/plugins/marketplaces/cenci"
        ;;
    esac
    if [[ -n "${checkout_tag}" ]]; then
        prepare_git_tagged_checkout "${checkout}" "${checkout_tag}"
    fi

    set +e
    # shellcheck disable=SC2086 # env_extra is intentionally word-split into
    # zero or more separate NAME=value tokens for env -i (see run_piped_case's
    # identical convention).
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        ROOT="${ROOT}" CURL_LOG="${curl_log}" COSIGN_LOG="${cosign_log}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        ${env_extra} \
        bash "${checkout}/install.sh" --yes --no-build update ${install_args[@]+"${install_args[@]}"} >"${output}" 2>&1
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
    invoke_doctor "${name}" "$@"
}

# invoke_doctor <name> [NAME=value env assignments...] — runs `install.sh
# doctor` for an already-prepared case (run_case) under `env -i`, setting
# DOCTOR_EXIT and DOCTOR_OUTPUT.
invoke_doctor() {
    local name="$1"
    shift
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

# write_service_fake <path> <service-mode> <daemon-exit> <install-exit> <tag> —
# writes a fake `cenci` at <path> for the login-service flows (#1168). Every call
# appends `<tag> <argv>` to CALLS_FILE. `daemon status` exits <daemon-exit>;
# `daemon install-service` exits <install-exit> (printing a reason on stderr when
# non-zero). <service-mode> selects the `daemon service-status` answer:
# enabled|disabled|not-installed|unsupported print `service: <mode>` and exit 0;
# `unknown-subcommand` is an older binary (stderr message, exit 2); `garbage`
# exits 0 with a first line that is not a service token. Every other verb
# exits 0 with no output.
write_service_fake() {
    local path="$1" mode="$2" daemon_exit="$3" install_exit="$4" tag="$5"
    mkdir -p "$(dirname "${path}")"
    cat > "${path}" <<EOF
#!/bin/sh
printf '%s %s\n' '${tag}' "\$*" >>"\${CALLS_FILE:?CALLS_FILE not set}"
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = status ]; then
    exit ${daemon_exit}
fi
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = service-status ]; then
    case '${mode}' in
        unknown-subcommand)
            echo "cenci: unknown subcommand 'service-status'" >&2
            exit 2
            ;;
        garbage)
            echo "something else entirely"
            exit 0
            ;;
        *)
            echo "service: ${mode}"
            exit 0
            ;;
    esac
fi
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = install-service ]; then
    if [ ${install_exit} -ne 0 ]; then
        echo "systemctl --user enable --now cenci-watch.service: exit status 1" >&2
    fi
    exit ${install_exit}
fi
exit 0
EOF
    chmod +x "${path}"
}

# run_doctor_case_with_service <name> <service-mode> [daemon-exit] — a doctor run
# whose PATH `cenci` answers `daemon service-status` per write_service_fake
# (`daemon status` exits <daemon-exit>, default 0 = running).
run_doctor_case_with_service() {
    local name="$1" mode="$2" daemon_exit="${3:-0}"
    mkdir -p "${WORK}/${name}/bin"
    write_service_fake "${WORK}/${name}/bin/cenci" "${mode}" "${daemon_exit}" 0 path
    run_doctor_case "${name}" claude
}

# run_install_case_with_service <name> <service-mode> <install-exit> [install.sh
# args...] — an install run (run_case, so --yes --no-build plus the extra args)
# whose plugin-cache binary (tag `cache`, the one step_cenci_watch_setup
# provisions) is a write_service_fake. Pre-seeding the cache manifest makes
# prepare_bootstrap_cenci return early, so this fake is the binary the installer
# resolves.
run_install_case_with_service() {
    local name="$1" mode="$2" install_exit="$3"
    shift 3
    local root="${WORK}/${name}/home/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    mkdir -p "${root}/.claude-plugin"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/.claude-plugin/plugin.json"
    write_service_fake "${root}/bin/cenci" "${mode}" 1 "${install_exit}" cache
    run_case "${name}" claude --no-build "$@"
}

# seed_hook_health_record <state_root> <record_name> <content> — plants a
# plain-text record at <state_root>/cenci/hook-health/<record_name> with the
# store's modes (dirs 0700, file 0600). <state_root> is a per-case temp dir, so
# the real hook-health store is never touched. Content is written verbatim
# (printf %b, so callers control the trailing newline).
seed_hook_health_record() {
    local root="$1" record="$2" content="$3"
    mkdir -p "${root}"
    mkdir -p "${root}/cenci/hook-health"
    chmod 700 "${root}/cenci" "${root}/cenci/hook-health"
    printf '%b' "${content}" >"${root}/cenci/hook-health/${record}"
    chmod 600 "${root}/cenci/hook-health/${record}"
}

# write_hook_health_fake <path> <mode> <version-mode> <tag> — writes a fake
# `cenci` executable at <path> (#1419). Every call appends `<tag> <argv>` to
# CALLS_FILE, so a test can tell WHICH binary ran a verb (`path` for a fake on
# the case PATH, `cache` for the plugin-cache binary). <mode> selects the
# `cenci daemon hook-health` answer (healthy|recovered|no-evidence|
# current-failure|…|unknown-subcommand|garbage|nonzero, see
# run_doctor_case_with_hook_health). <version-mode> selects the `cenci version`
# answer: `fail` exits 1 with no output, `empty` exits 0 with no output,
# `noisy` prints a first line carrying non-printable bytes (0x01, BEL) plus a
# second line `second-line-marker`, and anything else is printed verbatim as
# the single first line (e.g. `cenci 2.43.1`). Every other verb exits 0.
write_hook_health_fake() {
    local path="$1" mode="$2" version_mode="$3" tag="$4"
    mkdir -p "$(dirname "${path}")"
    cat > "${path}" <<EOF
#!/bin/sh
printf '%s %s\n' '${tag}' "\$*" >>"\${CALLS_FILE:?CALLS_FILE not set}"
if [ "\${1:-}" = version ]; then
    case '${version_mode}' in
        fail) exit 1 ;;
        empty) exit 0 ;;
        noisy)
            printf 'cenci\001 2.40.0\a\nsecond-line-marker\n'
            exit 0
            ;;
        *)
            printf '%s\n' '${version_mode}'
            exit 0
            ;;
    esac
fi
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = hook-health ]; then
    case '${mode}' in
        unknown-subcommand)
            echo "cenci: unknown subcommand 'hook-health'" >&2
            exit 2
            ;;
        garbage)
            echo "something else entirely"
            exit 0
            ;;
        nonzero)
            echo "hook health: healthy"
            exit 1
            ;;
        current-failure)
            echo "hook health: current-failure"
            echo "  delivery-failure (claude, 2026-01-01T00:00:00Z): daemon unreachable"
            exit 0
            ;;
        no-evidence-untrusted)
            echo "hook health: no-evidence"
            echo "  untrusted store: stubbed reporter reason line"
            exit 0
            ;;
        no-evidence-generic)
            echo "hook health: no-evidence"
            echo "  no recent records: this is not proof of health (hooks may not have run, or records are older than 24h)"
            exit 0
            ;;
        healthy-untrusted)
            echo "hook health: healthy"
            echo "  untrusted store: stubbed reporter reason line"
            exit 0
            ;;
        current-failure-untrusted)
            echo "hook health: current-failure"
            echo "  untrusted store: stubbed reporter reason line"
            exit 0
            ;;
        *)
            echo "hook health: ${mode}"
            exit 0
            ;;
    esac
fi
exit 0
EOF
    chmod +x "${path}"
}

# cache_hook_health_root <home> <client> — echoes the version-pinned plugin
# cache root prepare_cache_hook_health_cenci provisions.
cache_hook_health_root() {
    local home="$1" client="$2"
    if [[ "${client}" == claude ]]; then
        printf '%s\n' "${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    else
        printf '%s\n' "${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
    fi
}

# prepare_cache_hook_health_cenci <home> <client> <mode> <version-line> —
# pre-seeds the plugin-cache binary (tag `cache`) BEFORE run_case: with the
# manifest already present, prepare_bootstrap_cenci returns early, so the
# installer's current_cenci_binary / cached_cenci_binary resolve this fake
# instead of the generic bootstrapped one (#1419).
prepare_cache_hook_health_cenci() {
    local home="$1" client="$2" mode="$3" version_line="$4" root manifest_dir
    root="$(cache_hook_health_root "${home}" "${client}")"
    if [[ "${client}" == claude ]]; then
        manifest_dir=.claude-plugin
    else
        manifest_dir=.codex-plugin
    fi
    mkdir -p "${root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/${manifest_dir}/plugin.json"
    write_hook_health_fake "${root}/bin/cenci" "${mode}" "${version_line}" cache
}

# run_update_again <name> — re-runs the installer in update mode for an
# already-installed run_case <name> (the opencode-idempotent update pattern),
# setting UPDATE_EXIT and UPDATE_OUTPUT.
run_update_again() {
    local name="$1"
    local case_dir="${WORK}/${name}"
    UPDATE_OUTPUT="${case_dir}/update-output"
    set +e
    env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        bash "${ROOT}/install.sh" update --yes --no-build >"${UPDATE_OUTPUT}" 2>&1
    UPDATE_EXIT=$?
    set -e
}

# run_shadow_doctor_case <name> <shape> <path-version-mode> [NAME=value env
# assignments for the doctor run...] — a `--doctor` run with a seeded healthy
# cache binary (`cenci 2.43.1`) and a stale `cenci` earlier on the doctor PATH
# (#1419). The install (run_case) happens first with no PATH `cenci`, then the
# shadow is arranged, then invoke_doctor runs. <shape>:
#   file       — a regular file at <bin>/cenci;
#   symlink    — <bin>/cenci -> <case>/stale/cenci (a link into an old cache);
#   chain      — <bin>/cenci -> ~/.local/bin/cenci -> the cache binary (not a
#                shadow at all: it IS the cache binary);
#   local-link — ~/.local/bin/cenci -> <case>/stale/cenci, with the doctor
#                PATH forced to ~/.local/bin first (forward PATH= yourself).
# <path-version-mode> is write_hook_health_fake's version mode for the stale
# fake. Sets STALE_BIN (the stale fake) and CACHE_BIN (the cache binary).
run_shadow_doctor_case() {
    local name="$1" shape="$2" version_mode="$3"
    shift 3
    local home="${WORK}/${name}/home" bin="${WORK}/${name}/bin"
    local stale_fake="${WORK}/${name}/stale/cenci"
    prepare_cache_hook_health_cenci "${home}" claude healthy "cenci 2.43.1"
    CACHE_BIN="$(cache_hook_health_root "${home}" claude)/bin/cenci"
    run_case "${name}" claude
    mkdir -p "${home}/.local/bin"
    case "${shape}" in
        file)
            STALE_BIN="${bin}/cenci"
            write_hook_health_fake "${STALE_BIN}" unknown-subcommand "${version_mode}" path
            ;;
        symlink)
            STALE_BIN="${stale_fake}"
            write_hook_health_fake "${stale_fake}" unknown-subcommand "${version_mode}" path
            ln -sf "${stale_fake}" "${bin}/cenci"
            ;;
        chain)
            STALE_BIN="${CACHE_BIN}"
            ln -sf "${CACHE_BIN}" "${home}/.local/bin/cenci"
            ln -sf "${home}/.local/bin/cenci" "${bin}/cenci"
            ;;
        local-link)
            STALE_BIN="${stale_fake}"
            write_hook_health_fake "${stale_fake}" unknown-subcommand "${version_mode}" path
            ln -sf "${stale_fake}" "${home}/.local/bin/cenci"
            ;;
        *)
            echo "FAIL: unknown shadow shape '${shape}'" >&2
            exit 1
            ;;
    esac
    invoke_doctor "${name}" "$@"
}

# run_doctor_case_with_hook_health <name> <clients> <mode> [NAME=value env
# assignments for the doctor run...] — drives doctor_hook_health (#1239). <mode>
# selects the fake `cenci` on the doctor-run PATH:
#   healthy|recovered|no-evidence|current-failure — `cenci daemon hook-health`
#       prints that state's stable first line (`hook health: <token>`) and
#       exits 0, as the real reporter does (plus a detail line for
#       current-failure);
#   no-evidence-untrusted — first line `hook health: no-evidence`, then the
#       reporter's `  untrusted store: …` reason line (#1416);
#   no-evidence-generic — first line `hook health: no-evidence`, then today's
#       `  no recent records: …` line, as an older binary prints it (#1416);
#   healthy-untrusted|current-failure-untrusted — a first line that is NOT
#       no-evidence followed by an `  untrusted store: …` line 2: only the
#       no-evidence arm may react to that line (#1416);
#   unknown-subcommand — an older binary without the subverb: prints "unknown
#       subcommand" on stderr and exits 2;
#   garbage — exits 0 but prints a first line that is not a known token;
#   nonzero — prints a valid healthy first line but exits 1;
#   none — no cenci binary at all: the bootstrapped cache binary and any PATH
#       entry are removed after the install, so doctor must read the
#       missing-binary and bootstrap-failure records directly. Seed them first with
#       seed_hook_health_record into "${WORK}/<name>/home/.local/state" (the
#       default state root, since the doctor env has only HOME and PATH) or
#       into another root selected through a forwarded XDG_STATE_HOME=...
run_doctor_case_with_hook_health() {
    local name="$1" clients="$2" mode="$3"
    shift 3
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/doctor-output"
    if [[ "${mode}" != none ]]; then
        # HH_PATH_VERSION (optional, a prefix assignment like HH_SNAPSHOT_ROOT)
        # selects what the PATH fake answers to `cenci version` (see
        # write_hook_health_fake's version modes); default `empty` keeps the
        # fake as silent about its version as it was before #1419.
        write_hook_health_fake "${bin}/cenci" "${mode}" "${HH_PATH_VERSION:-empty}" path
        run_doctor_case "${name}" "${clients}" "$@"
        return 0
    fi

    run_case "${name}" "${clients}"
    rm -f "${bin}/cenci" \
        "${CASE_HOME}"/.claude/plugins/cache/cenci/cenci-watch/*/bin/cenci \
        "${CASE_HOME}"/.codex/plugins/cache/cenci/cenci-watch/*/bin/cenci
    # The no-binary doctor path needs the clock and find(1); the minimal case
    # PATH carries neither.
    ln -sf "$(command -v date)" "${bin}/date"
    ln -sf "$(command -v find)" "${bin}/find"
    # HH_SNAPSHOT_ROOT (optional, set by the caller as a prefix assignment):
    # the state root whose whole tree must come out of doctor exactly as it went
    # in. The snapshot is taken after the install and before the doctor run, so
    # only doctor's own effect is compared (#1416: doctor never creates, chmods,
    # chowns or removes anything in the store).
    local before="" after=""
    if [[ -n "${HH_SNAPSHOT_ROOT:-}" ]]; then
        before="${WORK}/${name}/store-before"
        after="${WORK}/${name}/store-after"
        snapshot_tree "${HH_SNAPSHOT_ROOT}" >"${before}"
    fi
    invoke_doctor "${name}" "$@"
    if [[ -n "${HH_SNAPSHOT_ROOT:-}" ]]; then
        snapshot_tree "${HH_SNAPSHOT_ROOT}" >"${after}"
        if ! cmp -s "${before}" "${after}"; then
            echo "FAIL: doctor changed the hook-health store tree under ${HH_SNAPSHOT_ROOT}" >&2
            diff "${before}" "${after}" >&2 || true
            exit 1
        fi
    fi
}

# snapshot_tree <dir> — prints a stable description of everything under <dir>
# (including <dir> itself): per entry its path, type, mode, owner, group, size
# and mtime, a symlink's target, and a regular file's exact bytes. Two
# snapshots are equal only when nothing was created, removed, chmod'd, chown'd,
# rewritten or retargeted in between. Fails the whole suite loudly when <dir> is
# missing, when find fails, or when the listing is empty or lacks <dir> itself,
# so a vacuous snapshot can never pass a comparison. Uses GNU stat or BSD stat,
# detected once.
SNAPSHOT_STAT_FLAVOR=""
snapshot_tree() {
    local dir="$1" entry listing
    if [[ ! -e "${dir}" && ! -L "${dir}" ]]; then
        echo "FAIL: snapshot root does not exist: ${dir}" >&2
        exit 1
    fi
    if [[ -z "${SNAPSHOT_STAT_FLAVOR}" ]]; then
        if stat -c '%n' / >/dev/null 2>&1; then
            SNAPSHOT_STAT_FLAVOR="gnu"
        else
            SNAPSHOT_STAT_FLAVOR="bsd"
        fi
    fi
    listing="$(find "${dir}")" || {
        echo "FAIL: find failed while snapshotting ${dir}" >&2
        exit 1
    }
    listing="$(printf '%s\n' "${listing}" | sort)" || {
        echo "FAIL: sort failed while snapshotting ${dir}" >&2
        exit 1
    }
    if [[ -z "${listing}" || $'\n'"${listing}"$'\n' != *$'\n'"${dir}"$'\n'* ]]; then
        echo "FAIL: snapshot of ${dir} is empty or lacks the root entry" >&2
        exit 1
    fi
    while IFS= read -r entry; do
        if [[ "${SNAPSHOT_STAT_FLAVOR}" == "gnu" ]]; then
            stat -c '%n|%F|%a|%u|%g|%s|%Y' "${entry}" || exit 1
        else
            stat -f '%N|%HT|%Lp|%u|%g|%z|%m' "${entry}" || exit 1
        fi
        if [[ -L "${entry}" ]]; then
            readlink "${entry}"
        elif [[ -f "${entry}" ]]; then
            cat "${entry}"
        fi
    done <<<"${listing}"
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
# run_doctor_case, but explicitly controls cosign's presence for the doctor
# run specifically. make_common_tools now plants a "pass"-mode cosign mock
# ambiently (like git/curl) for every setup path (#700), so this can no
# longer simply skip planting one to model "absent" — run_case's own
# make_common_tools call (inside the inlined run_doctor_case body below)
# would recreate it before the doctor invocation ran. Instead this inlines
# run_doctor_case's body with an `rm -f "${bin}/cosign"` interposed between
# run_case's setup and the doctor invocation itself, so <cosign_present>=
# "absent" still genuinely exercises cosign's absence (#626); any other
# value (e.g. "present") leaves the ambient mock as-is. Exercises doctor's
# cosign check, promoted from optional to required (#700) since cosign is
# now also a hard prerequisite for the update-mode bootstrap, not only the
# piped install path.
run_doctor_case_with_cosign() {
    local name="$1" clients="$2" cosign_present="$3"
    run_case "${name}" "${clients}"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/doctor-output"
    [ "${cosign_present}" = present ] || rm -f "${bin}/cosign"
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

# run_doctor_case_with_node <name> <clients> <node-version|absent> — like
# run_doctor_case, but plants a fake `node` printing <node-version> for
# `node --version` on the doctor-run PATH (or none for "absent"), exercising
# doctor_codex_support's Codex workflow runtime line (#1233). The fake is
# planted after run_case's setup so make_common_tools cannot interfere.
run_doctor_case_with_node() {
    local name="$1" clients="$2" node_version="$3"
    run_case "${name}" "${clients}"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/doctor-output"
    rm -f "${bin}/node"
    if [ "${node_version}" != absent ]; then
        printf '#!/bin/sh\nprintf '"'"'%%s\\n'"'"' "%s"\n' "${node_version}" >"${bin}/node"
        chmod +x "${bin}/node"
    fi
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

# run_doctor_case_with_stale_pin_checkout <name> <clients> <checkout-tag>
# <release-env> — like run_doctor_case, but pre-tags the managed marketplace
# checkout at <checkout-tag> (prepare_git_tagged_checkout) before the doctor
# run, and layers <release-env> (a single NAME=value token, e.g.
# "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9" or "CENCI_FAKE_NO_RELEASE=1") onto
# the doctor invocation's env. Exercises check_stale_marketplace_pin's
# comparison between the checkout's actual pinned ref and cenci_latest_ref
# (#700) — AC7/AC8.
run_doctor_case_with_stale_pin_checkout() {
    local name="$1" clients="$2" checkout_tag="$3" release_env="$4"
    local home="${WORK}/${name}/home" bin="${WORK}/${name}/bin"
    local calls="${WORK}/${name}/calls" output="${WORK}/${name}/doctor-output"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    case "${clients}" in
      claude)
        make_claude "${bin}"
        prepare_checkout "${home}" claude
        prepare_git_tagged_checkout "${home}/.claude/plugins/marketplaces/cenci" "${checkout_tag}"
        prepare_bootstrap_cenci "${home}" claude
        ;;
      codex)
        make_codex "${bin}"
        prepare_checkout "${home}" codex
        prepare_git_tagged_checkout "${home}/.codex/plugins/marketplaces/cenci" "${checkout_tag}"
        prepare_bootstrap_cenci "${home}" codex
        ;;
    esac
    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        "${release_env}" \
        CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${WORK}/${name}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/${name}/codex-installed" \
        bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
    DOCTOR_EXIT=$?
    set -e
    DOCTOR_OUTPUT="${output}"
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

# opencode_doctor_with_fixture <name> <json> [NAME=value env assignments...] —
# for a case prepared by run_case_opencode: writes <json> as the host
# ~/.config/opencode/opencode.json, runs doctor (invoke_doctor), and proves
# doctor never modified the file (cmp against a copy taken beforehand).
opencode_doctor_with_fixture() {
    local name="$1" json="$2"
    shift 2
    local config="${CASE_HOME}/.config/opencode/opencode.json"
    mkdir -p "${CASE_HOME}/.config/opencode"
    printf '%s\n' "${json}" >"${config}"
    cp "${config}" "${WORK}/${name}/fixture-original"
    invoke_doctor "${name}" "$@"
    cmp "${WORK}/${name}/fixture-original" "${config}"
}

# assert_allow_all_line <output-file> <config-path> — <output-file> holds
# exactly one allow-all finding, it is a `warn` line (install.sh's warn prints
# `  ! ` when colors are off, as under env -i without a tty), and that ONE line
# carries the finding text, the config path, the remediation framing and the
# manual jq command.
assert_allow_all_line() {
    local file="$1" config="$2" count line needle rc=0
    count="$(grep -cF -- "grants global allow-all permissions" "${file}")" || rc=$?
    if [ "${rc}" -gt 1 ]; then
        echo "FAIL: grep errored (rc ${rc}) counting allow-all lines in ${file}" >&2
        exit 1
    fi
    [ "${rc}" -eq 1 ] && count=0
    if ! [[ "${count}" =~ ^[0-9]+$ ]]; then
        echo "FAIL: non-numeric allow-all line count '${count}' for ${file}" >&2
        exit 1
    fi
    if [ "${count}" -ne 1 ]; then
        echo "FAIL: expected exactly one allow-all line in ${file}, got ${count}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
    line="$(grep -F -- "grants global allow-all permissions" "${file}")"
    case "${line}" in
    "  ! "*) ;;
    *)
        echo "FAIL: allow-all finding is not a warn line ('  ! ' marker): ${line}" >&2
        exit 1
        ;;
    esac
    for needle in \
        "grants global allow-all permissions" \
        "${config}" \
        "older cenci versions may have added" \
        "if you did not set these yourself" \
        "jq 'del(.permission, .autoupdate)'" \
        "cat \"${config}.tmp\" > \"${config}\" && rm \"${config}.tmp\""; do
        case "${line}" in
        *"${needle}"*) ;;
        *)
            echo "FAIL: allow-all line lacks '${needle}': ${line}" >&2
            exit 1
            ;;
        esac
    done
}

# assert_allow_all_warning <config-path> — the last doctor run flagged
# <config-path> as granting global allow-all permissions, with the manual
# remediation, all on one warn line.
assert_allow_all_warning() {
    local config="$1"
    assert_contains "${DOCTOR_OUTPUT}" "grants global allow-all permissions"
    assert_contains "${DOCTOR_OUTPUT}" "${config}"
    assert_contains "${DOCTOR_OUTPUT}" "older cenci versions may have added"
    assert_contains "${DOCTOR_OUTPUT}" "if you did not set these yourself"
    assert_contains "${DOCTOR_OUTPUT}" "jq 'del(.permission, .autoupdate)'"
    assert_allow_all_line "${DOCTOR_OUTPUT}" "${config}"
}

# assert_no_allow_all_warning — the last doctor run reached the OpenCode block
# (so silence is not vacuous) yet printed no allow-all finding and no jq error.
assert_no_allow_all_warning() {
    assert_contains "${DOCTOR_OUTPUT}" "OpenCode skills"
    assert_not_contains "${DOCTOR_OUTPUT}" "grants global allow-all permissions"
    assert_not_contains "${DOCTOR_OUTPUT}" "older cenci versions may have added"
    assert_not_contains "${DOCTOR_OUTPUT}" "jq: error"
}

# invoke_uninstall <name> [NAME=value env assignments...] — runs `install.sh
# uninstall --yes` for a case prepared by run_case_opencode under `env -i`,
# setting UNINSTALL_EXIT and UNINSTALL_OUTPUT.
invoke_uninstall() {
    local name="$1"
    shift
    local output="${WORK}/${name}/uninstall-output"
    set +e
    env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
        CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
        "$@" \
        bash "${ROOT}/install.sh" uninstall --yes >"${output}" 2>&1
    UNINSTALL_EXIT=$?
    set -e
    UNINSTALL_OUTPUT="${output}"
}

# assert_allow_all_hint <config-path> — the last uninstall printed exactly one
# legacy allow-all hint line naming <config-path> with the manual remediation.
assert_allow_all_hint() {
    local config="$1" hint_lines rc=0
    assert_contains "${UNINSTALL_OUTPUT}" "older cenci versions may have added"
    assert_contains "${UNINSTALL_OUTPUT}" "if you did not set these yourself"
    assert_contains "${UNINSTALL_OUTPUT}" "${config}"
    assert_contains "${UNINSTALL_OUTPUT}" "jq 'del(.permission, .autoupdate)'"
    assert_allow_all_line "${UNINSTALL_OUTPUT}" "${config}"
    hint_lines="$(grep -cF -- "older cenci versions may have added" "${UNINSTALL_OUTPUT}")" || rc=$?
    if [ "${rc}" -gt 1 ]; then
        echo "FAIL: grep errored (rc ${rc}) counting hint lines in ${UNINSTALL_OUTPUT}" >&2
        exit 1
    fi
    [ "${rc}" -eq 1 ] && hint_lines=0
    if ! [[ "${hint_lines}" =~ ^[0-9]+$ ]]; then
        echo "FAIL: non-numeric hint line count '${hint_lines}' in ${UNINSTALL_OUTPUT}" >&2
        exit 1
    fi
    if [ "${hint_lines}" -ne 1 ]; then
        echo "FAIL: expected exactly one allow-all hint line in ${UNINSTALL_OUTPUT}, got ${hint_lines}" >&2
        sed -n '1,240p' "${UNINSTALL_OUTPUT}" >&2
        exit 1
    fi
}

# references_container_seeder <file> — true when a CODE line of <file> mentions
# the container-only seeding helper (seed_opencode_config or the basename
# opencode-config.sh, which also catches relative sources such as
# "${SCRIPT_DIR}/lib/opencode-config.sh"). Full-line `#` comments are skipped so the
# pause-stub comment may name the helper it forbids; a trailing comment after
# code still counts. grep rc 1 means "no match"; rc >= 2 is an error and fails
# the suite instead of reading as "clean" (flow/docs/shell-scripting-gotchas.md).
references_container_seeder() {
    local file="$1" hits="" code="" rc=0
    hits="$(grep -nF -e seed_opencode_config -e opencode-config.sh -- "${file}")" || rc=$?
    if [ "${rc}" -gt 1 ]; then
        echo "FAIL: grep errored (rc ${rc}) scanning ${file}" >&2
        exit 1
    fi
    [ "${rc}" -eq 0 ] || return 1
    rc=0
    code="$(printf '%s\n' "${hits}" | grep -vE '^[0-9]+:[[:space:]]*#')" || rc=$?
    if [ "${rc}" -gt 1 ]; then
        echo "FAIL: grep errored (rc ${rc}) filtering comment lines of ${file}" >&2
        exit 1
    fi
    [ -n "${code}" ]
}

# assert_detects_seeder_reference <file> — the contract detector must flag
# <file> (an injected copy of install.sh); a silent miss would make the
# contract case vacuous.
assert_detects_seeder_reference() {
    if ! references_container_seeder "$1"; then
        echo "FAIL: references_container_seeder did not flag the injected reference in $1" >&2
        exit 1
    fi
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

# collect_lines_with_both <file> <first> <second> — sets BOTH_HITS to the lines
# of <file> containing both fixed strings. grep exit 1 (no match) is an empty
# result; exit >=2 (missing/unreadable file, bad usage) fails loudly instead of
# reading as "no match".
collect_lines_with_both() {
    local file="$1" first="$2" second="$3" first_hits rc
    first_hits="$(grep -F -- "${first}" "${file}")" && rc=0 || rc=$?
    if [[ "${rc}" -ge 2 ]]; then
        echo "FAIL: grep could not read ${file} (exit ${rc})" >&2
        exit 1
    fi
    BOTH_HITS=""
    if [[ -n "${first_hits}" ]]; then
        BOTH_HITS="$(printf '%s\n' "${first_hits}" | grep -F -- "${second}")" && rc=0 || rc=$?
        if [[ "${rc}" -ge 2 ]]; then
            echo "FAIL: grep failed filtering ${file} (exit ${rc})" >&2
            exit 1
        fi
    fi
}

# assert_line_with_both <file> <first> <second> — asserts at least one single
# line of <file> contains both fixed strings (so a hint is proven to sit on the
# same warning line as its anchor, not merely somewhere in the output). The
# match is captured instead of piped into `grep -q`, which could SIGPIPE the
# first grep and read as "no match" under pipefail.
assert_line_with_both() {
    local file="$1" first="$2" second="$3" hits
    collect_lines_with_both "${file}" "${first}" "${second}"
    hits="${BOTH_HITS}"
    if [[ -z "${hits}" ]]; then
        echo "FAIL: expected one line in ${file} containing both '${first}' and '${second}'" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
}

# assert_no_line_with_both <file> <first> <second> — the negative twin of
# assert_line_with_both: no single line may contain both fixed strings.
assert_no_line_with_both() {
    local file="$1" first="$2" second="$3" hits
    collect_lines_with_both "${file}" "${first}" "${second}"
    hits="${BOTH_HITS}"
    if [[ -n "${hits}" ]]; then
        echo "FAIL: did not expect a line in ${file} containing both '${first}' and '${second}':" >&2
        printf '%s\n' "${hits}" >&2
        exit 1
    fi
}

# assert_line_count <file> <needle> <count> — asserts exactly <count> lines of
# <file> contain the fixed string <needle>.
assert_line_count() {
    local file="$1" needle="$2" want="$3" got rc
    got="$(grep -Fc -- "${needle}" "${file}")" && rc=0 || rc=$?
    if [[ "${rc}" -ge 2 ]]; then
        echo "FAIL: grep could not read ${file} (exit ${rc})" >&2
        exit 1
    fi
    if [[ "${got}" -ne "${want}" ]]; then
        echo "FAIL: expected ${want} line(s) containing '${needle}' in ${file}, found ${got}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
}

# step_section <file> <title> <dest> — writes the installer output between the
# `==> <title>…` step heading and the next step heading to <dest> (heading
# included in neither), so an assertion can be pinned to ONE phase of the run
# (e.g. the preflight `Checking your system`, or `Setting up cenci`, where the
# post-setup hook-health check prints). Fails loudly when the heading is absent.
step_section() {
    local file="$1" title="$2" dest="$3"
    if ! grep -Fq -- "==> ${title}" "${file}"; then
        echo "FAIL: no '==> ${title}' step heading in ${file}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
    awk -v t="==> ${title}" 'index($0, t) == 1 { f = 1; next } f && /^==> / { exit } f' "${file}" >"${dest}"
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
assert_not_contains "${CASE_OUTPUT}" "cn xl|xt|xa|xs"
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
assert_contains "${CASE_OUTPUT}" "cn xl|xt|xa|xs"
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

# extraKnownMarketplaces: Claude refuses `marketplace add` from any source
# other than the one a settings entry declares (the cenci sandbox seeds a
# ref-less entry). Repair must remove only a plain unpinned user entry before
# re-registering at the selected release, preserving intentional declarations.
prepare_declared_case() {
    local name="$1" registered="$2" entry="$3"
    case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}/.claude" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    make_claude "${bin}"
    prepare_checkout "${home}" claude
    prepare_bootstrap_cenci "${home}" claude
    printf '{"permissions":{"deny":["Read(.env)"]},"enabledPlugins":{"cenci@cenci":true},"extraKnownMarketplaces":{"other":{"source":{"source":"github","repo":"example/other"}},"cenci":{"source":%s}}}\n' "${entry}" >"${home}/.claude/settings.json"
    cp "${home}/.claude/settings.json" "${case_dir}/original-settings.json"
    if [ "${registered}" = yes ]; then printf 'default-branch' >"${case_dir}/claude-marketplace"; fi
}

execute_declared_case() {
    local ref="${1:-watch/v9.9.9}"
    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CLAUDE_ADD_WRITES_SETTINGS="${2:-}" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        CENCI_CLAUDE_MANAGED_SETTINGS="${case_dir}/managed-settings.json" \
        bash "${ROOT}/install.sh" --yes --no-build --ref "${ref}" >"${output}" 2>&1
    declared_exit=$?
    set -e
}

run_declared_case() {
    prepare_declared_case "$@"
    execute_declared_case
}

file_mode() {
    stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"
}

assert_declared_repaired() {
    local backup
    [[ "${declared_exit}" -eq 0 ]]
    assert_contains "${case_dir}/claude-marketplace" "watch/v9.9.9"
    for plugin in cenci cenci-watch cenci-sandbox; do
        assert_contains "${case_dir}/claude-installed-${plugin}" "watch/v9.9.9"
    done
    jq -e --slurpfile original "${case_dir}/original-settings.json" \
        '. == ($original[0] | del(.extraKnownMarketplaces.cenci))' "${home}/.claude/settings.json" >/dev/null
    local backups=("${home}/.claude/settings.json.cenci-backup."*)
    [[ "${#backups[@]}" -eq 1 && -f "${backups[0]}" ]]
    backup="${backups[0]}"
    cmp "${case_dir}/original-settings.json" "${backup}"
    [[ "$(file_mode "${backup}")" = 600 ]]
    assert_contains "${output}" "${backup}"
    assert_not_contains "${output}" "so it tracks that source instead of"
}

echo "case: a registered unpinned user declaration is backed up and repaired to the selected release"
run_declared_case declared-registered yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
assert_declared_repaired
assert_before "${calls}" "claude plugin marketplace remove cenci" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"

echo "case: repairing settings preserves their original permissions and keeps the backup private"
prepare_declared_case declared-permissions yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
chmod 640 "${home}/.claude/settings.json"
execute_declared_case
assert_declared_repaired
[[ "$(file_mode "${home}/.claude/settings.json")" = 640 ]]

echo "case: repaired settings do not freeze the next update at the previous release"
execute_declared_case watch/v9.9.10
[[ "${declared_exit}" -eq 0 ]]
assert_contains "${case_dir}/claude-marketplace" "watch/v9.9.10"
assert_contains "${case_dir}/claude-installed-cenci" "watch/v9.9.10"
backups=("${home}/.claude/settings.json.cenci-backup."*)
[[ "${#backups[@]}" -eq 1 ]]

echo "case: an unregistered unpinned user declaration is repaired before registration"
run_declared_case declared-unregistered no '{"source":"github","repo":"matteobortolazzo/cenci"}'
assert_declared_repaired
assert_not_contains "${calls}" "claude plugin marketplace remove"

echo "case: declarations created by modern Claude add do not freeze subsequent updates"
prepare_declared_case declared-modern yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
execute_declared_case watch/v9.9.9 1
[[ "${declared_exit}" -eq 0 ]]
jq -e --slurpfile original "${case_dir}/original-settings.json" \
    '. == ($original[0] | del(.extraKnownMarketplaces.cenci))' "${home}/.claude/settings.json" >/dev/null
assert_contains "${case_dir}/claude-installed-cenci" "watch/v9.9.9"
backups=("${home}/.claude/settings.json.cenci-backup."*)
[[ "${#backups[@]}" -eq 2 ]]
for backup in "${backups[@]}"; do assert_contains "${output}" "${backup}"; done
execute_declared_case watch/v9.9.10 1
[[ "${declared_exit}" -eq 0 ]]
assert_contains "${case_dir}/claude-installed-cenci" "watch/v9.9.10"
jq -e '(.extraKnownMarketplaces | has("cenci")) | not' "${home}/.claude/settings.json" >/dev/null
backups=("${home}/.claude/settings.json.cenci-backup."*)
[[ "${#backups[@]}" -eq 3 ]]

for empty_ref in null '""'; do
    echo "case: a null or empty ref is still an unpinned user declaration"
    run_declared_case "declared-empty-${empty_ref//\"/empty}" yes "{\"source\":\"github\",\"repo\":\"matteobortolazzo/cenci\",\"ref\":${empty_ref}}"
    assert_declared_repaired
done

echo "case: an unregistered marketplace declared with a ref in extraKnownMarketplaces is added at the declared ref, not the resolved one"
run_declared_case declared-ref no '{"source":"github","repo":"matteobortolazzo/cenci","ref":"watch/v1.0.0"}'
[[ "${declared_exit}" -eq 0 ]]
assert_contains "${calls}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v1.0.0"
assert_not_contains "${calls}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"
assert_contains "${output}" "watch/v9.9.9"
cmp "${case_dir}/original-settings.json" "${home}/.claude/settings.json"
assert_contains "${output}" "extraKnownMarketplaces.cenci"
assert_contains "${output}" "cenci update"

echo "case: managed declarations take precedence and preserve the unpinned user entry"
prepare_declared_case declared-managed no '{"source":"github","repo":"matteobortolazzo/cenci"}'
printf '%s\n' '{"extraKnownMarketplaces":{"cenci":{"source":{"source":"github","repo":"matteobortolazzo/cenci","ref":"watch/v2.0.0"}}}}' >"${case_dir}/managed-settings.json"
cp "${case_dir}/managed-settings.json" "${case_dir}/original-managed.json"
execute_declared_case
[[ "${declared_exit}" -eq 0 ]]
cmp "${case_dir}/original-settings.json" "${home}/.claude/settings.json"
cmp "${case_dir}/original-managed.json" "${case_dir}/managed-settings.json"
assert_contains "${case_dir}/claude-marketplace" "watch/v2.0.0"
assert_contains "${output}" "${case_dir}/managed-settings.json"
assert_contains "${output}" "administrator"

echo "case: invalid managed JSON prevents automatic user settings repair"
prepare_declared_case declared-invalid-managed yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
printf '{invalid json\n' >"${case_dir}/managed-settings.json"
execute_declared_case
cmp "${case_dir}/original-settings.json" "${home}/.claude/settings.json"
assert_not_contains "${calls}" "claude plugin marketplace remove"
assert_contains "${output}" "${case_dir}/managed-settings.json"

echo "case: custom sources and entry options are preserved"
for variant in custom-repo custom-source custom-entry invalid-ref; do
    prepare_declared_case "declared-${variant}" yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
    case "${variant}" in
        custom-repo) filter='.extraKnownMarketplaces.cenci.source.repo = "example/cenci"' ;;
        custom-source) filter='.extraKnownMarketplaces.cenci.source.path = "custom/marketplace.json"' ;;
        custom-entry) filter='.extraKnownMarketplaces.cenci.autoUpdate = true' ;;
        invalid-ref) filter='.extraKnownMarketplaces.cenci.source.ref = false' ;;
    esac
    jq "${filter}" "${home}/.claude/settings.json" >"${case_dir}/original-settings.json"
    cp "${case_dir}/original-settings.json" "${home}/.claude/settings.json"
    execute_declared_case
    [[ "${declared_exit}" -eq 0 ]]
    cmp "${case_dir}/original-settings.json" "${home}/.claude/settings.json"
    assert_contains "${case_dir}/claude-marketplace" "default-branch"
    assert_not_contains "${calls}" "claude plugin marketplace remove"
    assert_contains "${output}" "extraKnownMarketplaces.cenci"
done

echo "case: symlinked user settings are preserved with actionable instructions"
prepare_declared_case declared-symlink yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
mv "${home}/.claude/settings.json" "${case_dir}/linked-settings.json"
ln -s "${case_dir}/linked-settings.json" "${home}/.claude/settings.json"
execute_declared_case
[[ "${declared_exit}" -eq 0 && -L "${home}/.claude/settings.json" ]]
cmp "${case_dir}/original-settings.json" "${case_dir}/linked-settings.json"
assert_not_contains "${calls}" "claude plugin marketplace remove"
assert_contains "${output}" "extraKnownMarketplaces.cenci"
assert_contains "${output}" "cenci update"

for failure in backup-mktemp backup-cp stage-mktemp stage-cp transform-jq rename-mv; do
    operation="${failure##*-}"
    case "${failure}" in
        backup-*) fail_pattern='*settings.json.cenci-backup.*' ;;
        stage-*) fail_pattern='*settings.json.cenci-tmp.*' ;;
        transform-jq) fail_pattern='*settings.json.cenci-backup.*' ;;
        rename-mv) fail_pattern='*settings.json' ;;
    esac
    echo "case: settings repair ${failure} failure preserves settings and registration, and skips Claude plugins"
    prepare_declared_case "declared-failed-${failure}" yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
    make_codex "${bin}"
    prepare_checkout "${home}" codex
    # Rename the symlink before writing the wrapper, so the host tool itself
    # is never overwritten. Match the backup/staged path so a failure at a
    # later stage cannot pass by accidentally failing the first prerequisite.
    mv "${bin}/${operation}" "${bin}/${operation}-real"
    cat >"${bin}/${operation}" <<EOF
#!/bin/sh
for arg do
    case "\$arg" in ${fail_pattern}) exit 1 ;; esac
done
exec "${bin}/${operation}-real" "\$@"
EOF
    chmod +x "${bin}/${operation}"
    execute_declared_case
    [[ "${declared_exit}" -ne 0 ]]
    cmp "${case_dir}/original-settings.json" "${home}/.claude/settings.json"
    assert_contains "${case_dir}/claude-marketplace" "default-branch"
    assert_not_contains "${calls}" "claude plugin marketplace remove"
    assert_not_contains "${calls}" "claude plugin marketplace add"
    assert_not_contains "${calls}" "claude plugin install"
    assert_not_contains "${calls}" "claude plugin update"
    assert_contains "${case_dir}/codex-installed-cenci" "watch/v9.9.9"
done

echo "case: a concurrent settings edit is preserved and aborts marketplace and plugin changes"
prepare_declared_case declared-concurrent yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
jq '.permissions.deny += ["Read(concurrent-secret)"]' "${home}/.claude/settings.json" >"${case_dir}/concurrent-settings.json"
mv "${bin}/cmp" "${bin}/cmp-real"
cat >"${bin}/cmp" <<EOF
#!/bin/sh
case "\$*" in
    *settings.json.cenci-backup.*) cp "${case_dir}/concurrent-settings.json" "${home}/.claude/settings.json" ;;
esac
exec "${bin}/cmp-real" "\$@"
EOF
chmod +x "${bin}/cmp"
execute_declared_case
[[ "${declared_exit}" -ne 0 ]]
cmp "${case_dir}/concurrent-settings.json" "${home}/.claude/settings.json"
assert_contains "${case_dir}/claude-marketplace" "default-branch"
assert_not_contains "${calls}" "claude plugin marketplace remove"
assert_not_contains "${calls}" "claude plugin marketplace add"
assert_not_contains "${calls}" "claude plugin install"
assert_not_contains "${calls}" "claude plugin update"

echo "case: modern Claude declaration cleanup failure is reported before plugin installation"
prepare_declared_case declared-modern-cleanup-failure yes '{"source":"github","repo":"matteobortolazzo/cenci"}'
mv "${bin}/mv" "${bin}/mv-real"
cat >"${bin}/mv" <<EOF
#!/bin/sh
case "\$*" in
    *settings.json.cenci-tmp.*)
        if grep -Fxq 'watch/v9.9.9' "${case_dir}/claude-marketplace"; then exit 1; fi
        ;;
esac
exec "${bin}/mv-real" "\$@"
EOF
chmod +x "${bin}/mv"
execute_declared_case watch/v9.9.9 1
[[ "${declared_exit}" -ne 0 ]]
assert_contains "${case_dir}/claude-marketplace" "watch/v9.9.9"
jq -e --slurpfile original "${case_dir}/original-settings.json" \
    '. == ($original[0] | .extraKnownMarketplaces.cenci.source.ref = "watch/v9.9.9")' "${home}/.claude/settings.json" >/dev/null
assert_not_contains "${calls}" "claude plugin install"
assert_not_contains "${calls}" "claude plugin update"
assert_contains "${output}" "generated settings declaration could not be cleaned up"
assert_contains "${output}" "extraKnownMarketplaces.cenci"
assert_contains "${output}" "cenci update"

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

echo "case: an incomplete update keeps the previously provisioned binary and returns failure"
run_case_stale_newest_cenci_cache bootstrap-stale-newest-update update
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "cenci binary update incomplete"
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
# #1213: doctor reports Codex hook protection as unverified and never as active.
assert_contains "${DOCTOR_OUTPUT}" "hook protection is not verified by doctor"
assert_contains "${DOCTOR_OUTPUT}" "inactive protection"
assert_contains "${DOCTOR_OUTPUT}" "review them with /hooks"
assert_contains "${DOCTOR_OUTPUT}" "implement, address-review, refactor, ci-repair, babysit-attention, refine, configure, maintain, sync and babysit's arm step run a guard canary"
assert_not_contains "${DOCTOR_OUTPUT}" "not yet covered"
assert_not_contains "${DOCTOR_OUTPUT}" "mutating Codex workflows run a guard canary"
assert_contains "${DOCTOR_OUTPUT}" "hook trust bypassed by launcher (explicit flag)"
assert_contains "${DOCTOR_OUTPUT}" "cenci never changes trust"
assert_not_contains "${DOCTOR_OUTPUT}" "supports cenci hooks"
# The "Neutral project config: <path>" line echoes the checkout path, which can
# itself contain "trusted" (e.g. a worktree named after an untrusted-store ticket),
# so the wording check must not see it.
DOCTOR_OUTPUT_NO_PATHS="${DOCTOR_OUTPUT}.no-paths"
# grep exits 1 when no line is left, which is fine; exit 2 is a read error.
grep -v 'Neutral project config:' "${DOCTOR_OUTPUT}" > "${DOCTOR_OUTPUT_NO_PATHS}" || [[ $? -eq 1 ]]
assert_not_contains "${DOCTOR_OUTPUT_NO_PATHS}" "trusted"
assert_not_contains "${DOCTOR_OUTPUT}" "protection active"

echo "case: doctor reports the Codex workflow runtime separately from plugin installation, ok with Node 18+ on PATH (#1233)"
run_doctor_case_with_node doctor-codex-node-ok codex v20.11.0
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime: Node 20.11.0 on PATH"
assert_contains "${DOCTOR_OUTPUT}" "Codex: cenci"
assert_not_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime not executable"

echo "case: doctor warns, without failing, when Node is absent from PATH (plugin installed, runtime not executable) (#1233)"
run_doctor_case_with_node doctor-codex-node-absent codex absent
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime not executable: Node.js 18+ not found on PATH (found: none)"
assert_contains "${DOCTOR_OUTPUT}" "install Node 18+"
assert_contains "${DOCTOR_OUTPUT}" "Codex: cenci"
assert_not_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime: Node"

echo "case: doctor warns, without failing, when Node on PATH is older than 18 and names the version found (#1233)"
run_doctor_case_with_node doctor-codex-node-old codex v16.20.0
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime not executable: Node.js 18+ not found on PATH (found: 16.20.0)"
assert_not_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime: Node"

echo "case: doctor applies require-runtime.sh's version grammar: unparseable output warns, the 18 boundary is exact (#1233)"
for bad in banana v20.1.2garbage v20.1 v20.1.2.3 v1234567.0.0; do
    run_doctor_case_with_node "doctor-codex-node-bad-${bad}" codex "${bad}"
    [[ "${DOCTOR_EXIT}" -eq 0 ]]
    assert_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime not executable: Node.js 18+ not found on PATH (found: unparseable)"
    assert_not_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime: Node"
done
run_doctor_case_with_node doctor-codex-node-min codex v18.0.0
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime: Node 18.0.0 on PATH"
assert_not_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime not executable"
run_doctor_case_with_node doctor-codex-node-below-min codex v17.9.1
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime not executable: Node.js 18+ not found on PATH (found: 17.9.1)"
assert_not_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime: Node"
run_doctor_case_with_node doctor-codex-node-prerelease codex v20.0.0-rc.1
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime: Node 20.0.0 on PATH"

echo "case: doctor skips the Codex workflow runtime line when Codex is not detected (#1233)"
run_doctor_case_with_node doctor-claude-node-skip claude absent
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_not_contains "${DOCTOR_OUTPUT}" "Codex workflow runtime"

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

echo "case: doctor's not-running text names the login service and no longer reads only 'first agent session' (#1168)"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci daemon: not running" "run: cenci daemon start"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci daemon: not running" "next agent session"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci daemon: not running" "at login with the login service"

echo "case: doctor maps service 'enabled' to an ok line after the running line, without failing doctor (#1168)"
run_doctor_case_with_service doctor-service-enabled enabled
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "✓ cenci daemon service: enabled (starts at login)"
assert_before "${DOCTOR_OUTPUT}" "cenci daemon: running" "cenci daemon service: enabled"
assert_not_contains "${DOCTOR_OUTPUT}" "! cenci daemon service"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ cenci daemon service"

echo "case: doctor maps service 'not-installed' to a warn naming install-service, without failing doctor (#1168)"
run_doctor_case_with_service doctor-service-not-installed not-installed
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! cenci daemon service: not installed — to start at login run: cenci daemon install-service"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ cenci daemon service"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ cenci daemon service"

echo "case: doctor maps service 'disabled' to a warn naming install-service, without failing doctor (#1168)"
run_doctor_case_with_service doctor-service-disabled disabled
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! cenci daemon service: installed but disabled — run: cenci daemon install-service"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ cenci daemon service"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ cenci daemon service"

echo "case: doctor maps service 'unsupported' to a plain info line (no ok/warn/fail mark), without failing doctor (#1168)"
run_doctor_case_with_service doctor-service-unsupported unsupported
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon service: unsupported on this host"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ cenci daemon service"
assert_not_contains "${DOCTOR_OUTPUT}" "! cenci daemon service"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ cenci daemon service"

echo "case: the service line also prints when the daemon is not running (#1168)"
run_doctor_case_with_service doctor-service-enabled-daemon-down enabled 1
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon: not running"
assert_contains "${DOCTOR_OUTPUT}" "✓ cenci daemon service: enabled (starts at login)"

echo "case: an older binary whose daemon service-status exits 2 yields only the running line, no service line and no failure (#1168)"
run_doctor_case_with_service doctor-service-older-binary unknown-subcommand
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon: running"
assert_not_contains "${DOCTOR_OUTPUT}" "cenci daemon service"
assert_not_contains "${DOCTOR_OUTPUT}" "unknown subcommand"
assert_not_contains "${DOCTOR_OUTPUT}" "✗"

echo "case: an unrecognized service-status first line prints no service line and no failure (#1168)"
run_doctor_case_with_service doctor-service-garbage garbage
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon: running"
assert_not_contains "${DOCTOR_OUTPUT}" "cenci daemon service"
assert_not_contains "${DOCTOR_OUTPUT}" "✗"

echo "case: install --yes with no login service installed runs install-service on the provisioned binary and reports ok (#1168)"
run_install_case_with_service install-service-yes not-installed 0
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "cache daemon service-status"
assert_contains "${CASE_CALLS}" "cache daemon install-service"
assert_not_contains "${CASE_CALLS}" "--force"
assert_before "${CASE_CALLS}" "cache daemon service-status" "cache daemon install-service"
assert_contains "${CASE_OUTPUT}" "✓ cenci daemon login service installed"
assert_not_contains "${CASE_OUTPUT}" "✗"

echo "case: install --yes --no-login-service never calls install-service (#1168)"
run_install_case_with_service install-service-declined not-installed 0 --no-login-service
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CALLS}" "install-service"
assert_not_contains "${CASE_OUTPUT}" "login service installed"
assert_not_contains "${CASE_OUTPUT}" "✗"

echo "case: a failing install-service warns without failing the install (#1168)"
run_install_case_with_service install-service-failing not-installed 1
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "cache daemon install-service"
assert_contains "${CASE_OUTPUT}" "! could not install the cenci daemon login service"
assert_contains "${CASE_OUTPUT}" "cenci daemon install-service"
assert_not_contains "${CASE_OUTPUT}" "login service installed"
assert_not_contains "${CASE_OUTPUT}" "✗"

for service_mode in enabled disabled unsupported unknown-subcommand garbage; do
    echo "case: install --yes with service-status '${service_mode}' never calls install-service (#1168)"
    run_install_case_with_service "install-service-skip-${service_mode}" "${service_mode}" 0
    [[ "${CASE_EXIT}" -eq 0 ]]
    assert_contains "${CASE_CALLS}" "cache daemon service-status"
    assert_not_contains "${CASE_CALLS}" "install-service"
    assert_not_contains "${CASE_OUTPUT}" "login service installed"
done

echo "case: update mode never reads the service state, prompts for, or installs the login service (#1168)"
run_install_case_with_service install-service-update not-installed 0 update
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CALLS}" "service-status"
assert_not_contains "${CASE_CALLS}" "install-service"
assert_not_contains "${CASE_OUTPUT}" "login service"

echo "case: doctor maps hook-health 'healthy' to ok without failing doctor (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-healthy claude healthy
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "✓ hook health: healthy"
assert_not_contains "${DOCTOR_OUTPUT}" "! hook health"

echo "case: doctor maps hook-health 'recovered' to ok (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-recovered claude recovered
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "✓ hook health: recovered"
assert_not_contains "${DOCTOR_OUTPUT}" "! hook health"

echo "case: doctor maps hook-health 'no-evidence' to warn, never a failure (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-no-evidence claude no-evidence
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: no evidence"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ hook health"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: doctor maps hook-health 'current-failure' to warn naming the failure, never a failure (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-current-failure claude current-failure
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: current failure"
assert_contains "${DOCTOR_OUTPUT}" "delivery-failure"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ hook health"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: doctor warns to update when the binary predates the hook-health subverb (exit 2, unknown subcommand) (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-older-binary claude unknown-subcommand
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable"
assert_contains "${DOCTOR_OUTPUT}" "cenci update"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ hook health"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: doctor warns on an unrecognized hook-health first line (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-garbage claude garbage
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ hook health"

echo "case: doctor warns on a non-zero reporter exit even when its first line says healthy (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-nonzero claude nonzero
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ hook health"

hh_now="$(date +%s)"
# The exact no-binary, no-usable-record warning: asserting it (not just the
# absence of the "was missing when" text) proves which branch ran.
hh_no_record="hook health: no evidence yet — cenci binary not found and no recent missing-binary or bootstrap-failure record found (not proof hooks work)"
# The fixed untrusted-store warning, identical for the binary-present
# no-evidence arm and the no-binary path (#1416). Doctor prints its own text and
# never echoes the reporter's `untrusted store:` line.
hh_untrusted="hook health: no evidence — the hook-health store is untrusted (symlinked, foreign-owned or group/other-writable), so hooks cannot record; fix its owner and permissions"
# The exact no-binary bootstrap-failure warning for a 120 s old claude record.
hh_bootstrap_claude="hook health: installing the cenci binary failed when a claude hook last ran (2m ago) — check the bootstrap log and network access, then restart the session; if it persists, run: cenci update"
# The exact binary-present no-evidence warning with no untrusted line (#1239).
hh_generic_no_evidence="hook health: no evidence yet (absent or stale records are not proof hooks work) — details: cenci daemon hook-health"

echo "case: doctor warns that the store is untrusted when the reporter's no-evidence output carries an untrusted store line, never a failure (#1416)"
run_doctor_case_with_hook_health doctor-hook-health-no-evidence-untrusted claude no-evidence-untrusted
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_untrusted}"
assert_not_contains "${DOCTOR_OUTPUT}" "hook health: no evidence yet"
assert_not_contains "${DOCTOR_OUTPUT}" "stubbed reporter reason line"
assert_not_contains "${DOCTOR_OUTPUT}" "✓ hook health"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: doctor keeps the generic no-evidence warning when line 2 is not an untrusted store line, as an older binary prints it (#1416)"
run_doctor_case_with_hook_health doctor-hook-health-no-evidence-generic claude no-evidence-generic
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_generic_no_evidence}"
assert_not_contains "${DOCTOR_OUTPUT}" "hook-health store is untrusted"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: doctor keeps the generic no-evidence warning when the reporter prints a single no-evidence line (#1416)"
run_doctor_case_with_hook_health doctor-hook-health-no-evidence-single-line claude no-evidence
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_generic_no_evidence}"
assert_not_contains "${DOCTOR_OUTPUT}" "hook-health store is untrusted"

echo "case: an untrusted store line under a healthy first line does not trigger the untrusted warning (#1416)"
run_doctor_case_with_hook_health doctor-hook-health-healthy-untrusted claude healthy-untrusted
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "✓ hook health: healthy"
assert_not_contains "${DOCTOR_OUTPUT}" "hook-health store is untrusted"
assert_not_contains "${DOCTOR_OUTPUT}" "! hook health"

echo "case: an untrusted store line under a current-failure first line does not trigger the untrusted warning (#1416)"
run_doctor_case_with_hook_health doctor-hook-health-current-failure-untrusted claude current-failure-untrusted
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: current failure"
assert_not_contains "${DOCTOR_OUTPUT}" "hook-health store is untrusted"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary and a fresh missing-binary record warns naming the failure, never a failure (#1239)"
seed_hook_health_record "${WORK}/doctor-hook-health-record-fresh/home/.local/state" missing-binary "$((hh_now - 120)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-record-fresh claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "binary not found yet"
assert_contains "${DOCTOR_OUTPUT}" "! hook health: cenci binary was missing when a claude hook last ran"
assert_contains "${DOCTOR_OUTPUT}" "cenci update"
assert_not_contains "${DOCTOR_OUTPUT}" "hook health: no evidence"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary reads the record from an absolute XDG_STATE_HOME (#1239)"
seed_hook_health_record "${WORK}/doctor-hook-health-record-xdg/xdg-state" missing-binary "$((hh_now - 30)) codex\n"
run_doctor_case_with_hook_health doctor-hook-health-record-xdg codex none "XDG_STATE_HOME=${WORK}/doctor-hook-health-record-xdg/xdg-state"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: cenci binary was missing when a codex hook last ran"

echo "case: no binary treats a relative XDG_STATE_HOME as unset and falls back to \$HOME/.local/state (#1239)"
seed_hook_health_record "${WORK}/doctor-hook-health-record-relxdg/home/.local/state" missing-binary "$((hh_now - 30)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-record-relxdg claude none "XDG_STATE_HOME=relative/state"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: cenci binary was missing when a claude hook last ran"

echo "case: no binary and no record warns no-evidence, never a failure (#1239)"
run_doctor_case_with_hook_health doctor-hook-health-record-absent claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "binary not found yet"
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary treats a relative HOME as no record, even when the relative path holds one (#1239)"
hh_rel_case="doctor-hook-health-record-relhome"
seed_hook_health_record "${WORK}/${hh_rel_case}/rel-home/.local/state" missing-binary "$((hh_now - 30)) claude\n"
pushd "${WORK}/${hh_rel_case}" >/dev/null
run_doctor_case_with_hook_health "${hh_rel_case}" claude none "HOME=rel-home"
popd >/dev/null
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"

echo "case: no binary counts a stale (older than 24h) missing-binary record as absent (#1239)"
seed_hook_health_record "${WORK}/doctor-hook-health-record-stale/home/.local/state" missing-binary "$((hh_now - 90000)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-record-stale claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"

echo "case: no binary counts a future-dated missing-binary record as absent (#1239)"
seed_hook_health_record "${WORK}/doctor-hook-health-record-future/home/.local/state" missing-binary "$((hh_now + 3600)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-record-future claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"

echo "case: no binary counts malformed missing-binary and bootstrap-failure records as absent (#1239, #1416)"
hh_malformed_n=0
for hh_category in missing-binary bootstrap-failure; do
    for hh_bad in \
        "garbage\n" \
        "${hh_now} opencode\n" \
        "${hh_now} claude" \
        "${hh_now} claude\nextra\n" \
        "${hh_now} claude                          padded-beyond-32-bytes\n" \
        "\n"; do
        hh_malformed_n=$((hh_malformed_n + 1))
        hh_case="doctor-hook-health-record-malformed-${hh_malformed_n}"
        seed_hook_health_record "${WORK}/${hh_case}/home/.local/state" "${hh_category}" "${hh_bad}"
        run_doctor_case_with_hook_health "${hh_case}" claude none
        [[ "${DOCTOR_EXIT}" -eq 0 ]]
        assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
        assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"
        assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"
    done
done

echo "case: no binary counts a symlinked missing-binary record as absent (#1239)"
hh_link_state="${WORK}/doctor-hook-health-record-symlink/home/.local/state"
seed_hook_health_record "${hh_link_state}" decoy "${hh_now} claude\n"
ln -s decoy "${hh_link_state}/cenci/hook-health/missing-binary"
run_doctor_case_with_hook_health doctor-hook-health-record-symlink claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"

echo "case: no binary reports a group/other-writable store dir as untrusted and ignores its record (#1239, #1416)"
seed_hook_health_record "${WORK}/doctor-hook-health-record-wide/home/.local/state" missing-binary "$((hh_now - 30)) claude\n"
chmod 777 "${WORK}/doctor-hook-health-record-wide/home/.local/state/cenci/hook-health"
HH_SNAPSHOT_ROOT="${WORK}/doctor-hook-health-record-wide/home/.local/state" \
    run_doctor_case_with_hook_health doctor-hook-health-record-wide claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_untrusted}"
assert_not_contains "${DOCTOR_OUTPUT}" "hook health: no evidence yet"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary reports an untrusted leaf as untrusted without listing either valid fresh record (#1416)"
hh_wide_both_state="${WORK}/doctor-hook-health-untrusted-both/home/.local/state"
seed_hook_health_record "${hh_wide_both_state}" missing-binary "$((hh_now - 30)) claude\n"
seed_hook_health_record "${hh_wide_both_state}" bootstrap-failure "$((hh_now - 30)) claude\n"
chmod 777 "${hh_wide_both_state}/cenci/hook-health"
HH_SNAPSHOT_ROOT="${hh_wide_both_state}" \
    run_doctor_case_with_hook_health doctor-hook-health-untrusted-both claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_untrusted}"
assert_not_contains "${DOCTOR_OUTPUT}" "${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"
assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary reports a symlinked leaf as untrusted even when it points at a trusted dir holding fresh records (#1416)"
hh_link_leaf_state="${WORK}/doctor-hook-health-untrusted-symlink-leaf/home/.local/state"
seed_hook_health_record "${hh_link_leaf_state}" missing-binary "$((hh_now - 30)) claude\n"
seed_hook_health_record "${hh_link_leaf_state}" bootstrap-failure "$((hh_now - 30)) claude\n"
mv "${hh_link_leaf_state}/cenci/hook-health" "${hh_link_leaf_state}/cenci/real-store"
ln -s real-store "${hh_link_leaf_state}/cenci/hook-health"
HH_SNAPSHOT_ROOT="${hh_link_leaf_state}" \
    run_doctor_case_with_hook_health doctor-hook-health-untrusted-symlink-leaf claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_untrusted}"
assert_not_contains "${DOCTOR_OUTPUT}" "${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"
assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary reports a group/other-writable state root with an absent leaf as untrusted (#1416)"
hh_wide_root_state="${WORK}/doctor-hook-health-untrusted-root/home/.local/state"
mkdir -p "${hh_wide_root_state}/cenci"
chmod 777 "${hh_wide_root_state}/cenci"
HH_SNAPSHOT_ROOT="${hh_wide_root_state}" \
    run_doctor_case_with_hook_health doctor-hook-health-untrusted-root claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_untrusted}"
assert_not_contains "${DOCTOR_OUTPUT}" "${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"
assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary treats a trusted state root with an absent leaf as plain no-record, not untrusted (#1416)"
mkdir -p "${WORK}/doctor-hook-health-absent-leaf/home/.local/state/cenci"
chmod 700 "${WORK}/doctor-hook-health-absent-leaf/home/.local/state/cenci"
HH_SNAPSHOT_ROOT="${WORK}/doctor-hook-health-absent-leaf/home/.local/state" \
    run_doctor_case_with_hook_health doctor-hook-health-absent-leaf claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "hook-health store is untrusted"

echo "case: no binary and a fresh bootstrap-failure record warns naming the failure and its age, never a failure (#1416)"
# Re-read the clock right before seeding: hh_now was captured earlier, and the
# exact "(2m ago)" text drifts to "3m ago" once the suite has run for a minute.
now_fresh="$(date +%s)"
seed_hook_health_record "${WORK}/doctor-hook-health-bootstrap-fresh/home/.local/state" bootstrap-failure "$((now_fresh - 120)) claude\n"
HH_SNAPSHOT_ROOT="${WORK}/doctor-hook-health-bootstrap-fresh/home/.local/state" \
    run_doctor_case_with_hook_health doctor-hook-health-bootstrap-fresh claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "binary not found yet"
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_bootstrap_claude}"
assert_not_contains "${DOCTOR_OUTPUT}" "was missing when"
assert_not_contains "${DOCTOR_OUTPUT}" "hook health: no evidence"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

echo "case: no binary reads the bootstrap-failure record from an absolute XDG_STATE_HOME (#1416)"
seed_hook_health_record "${WORK}/doctor-hook-health-bootstrap-xdg/xdg-state" bootstrap-failure "$((hh_now - 30)) codex\n"
run_doctor_case_with_hook_health doctor-hook-health-bootstrap-xdg codex none "XDG_STATE_HOME=${WORK}/doctor-hook-health-bootstrap-xdg/xdg-state"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: installing the cenci binary failed when a codex hook last ran"

echo "case: no binary treats a relative XDG_STATE_HOME as unset for the bootstrap-failure record too (#1416)"
seed_hook_health_record "${WORK}/doctor-hook-health-bootstrap-relxdg/home/.local/state" bootstrap-failure "$((hh_now - 30)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-bootstrap-relxdg claude none "XDG_STATE_HOME=relative/state"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: installing the cenci binary failed when a claude hook last ran"

echo "case: no binary treats a relative HOME as no bootstrap-failure record, even when the relative path holds one (#1416)"
hh_bs_rel_case="doctor-hook-health-bootstrap-relhome"
seed_hook_health_record "${WORK}/${hh_bs_rel_case}/rel-home/.local/state" bootstrap-failure "$((hh_now - 30)) claude\n"
pushd "${WORK}/${hh_bs_rel_case}" >/dev/null
run_doctor_case_with_hook_health "${hh_bs_rel_case}" claude none "HOME=rel-home"
popd >/dev/null
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"

echo "case: no binary counts a stale (older than 24h) bootstrap-failure record as absent (#1416)"
seed_hook_health_record "${WORK}/doctor-hook-health-bootstrap-stale/home/.local/state" bootstrap-failure "$((hh_now - 90000)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-bootstrap-stale claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"

echo "case: no binary counts a future-dated bootstrap-failure record as absent (#1416)"
seed_hook_health_record "${WORK}/doctor-hook-health-bootstrap-future/home/.local/state" bootstrap-failure "$((hh_now + 3600)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-bootstrap-future claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"

echo "case: no binary counts a symlinked bootstrap-failure record as absent (#1416)"
hh_bs_link_state="${WORK}/doctor-hook-health-bootstrap-symlink/home/.local/state"
seed_hook_health_record "${hh_bs_link_state}" decoy "${hh_now} claude\n"
ln -s decoy "${hh_bs_link_state}/cenci/hook-health/bootstrap-failure"
run_doctor_case_with_hook_health doctor-hook-health-bootstrap-symlink claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_no_record}"
assert_not_contains "${DOCTOR_OUTPUT}" "installing the cenci binary failed"

echo "case: no binary with both fresh records prints the missing-binary warning first, then the bootstrap-failure warning (#1416)"
hh_both_state="${WORK}/doctor-hook-health-both-records/home/.local/state"
now_fresh="$(date +%s)"
seed_hook_health_record "${hh_both_state}" missing-binary "$((now_fresh - 60)) claude\n"
seed_hook_health_record "${hh_both_state}" bootstrap-failure "$((now_fresh - 120)) claude\n"
run_doctor_case_with_hook_health doctor-hook-health-both-records claude none
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: cenci binary was missing when a claude hook last ran"
assert_contains "${DOCTOR_OUTPUT}" "! ${hh_bootstrap_claude}"
assert_before "${DOCTOR_OUTPUT}" "was missing when" "installing the cenci binary failed"
assert_not_contains "${DOCTOR_OUTPUT}" "hook health: no evidence"
assert_not_contains "${DOCTOR_OUTPUT}" "✗ hook health"

# --- #1419: hook health is checked against the freshly provisioned cache
# binary after setup, and a stale `cenci` earlier on PATH is named.

# hh1419_paths <case-name> — sets the per-case paths the #1419 cases assert on:
# hh_home, hh_bin (the case PATH dir) and hh_cache (the plugin-cache binary).
hh1419_paths() {
    hh_home="${WORK}/$1/home"
    hh_bin="${WORK}/$1/bin"
    hh_cache="$(cache_hook_health_root "${hh_home}" claude)/bin/cenci"
}

# assert_install_hook_health_never_updates <output> — no output line of an
# install/update run pairs `hook health` with `cenci update`, and no PATH-shadow
# warning line mentions `cenci update` (#1419 AC8).
assert_install_hook_health_never_updates() {
    assert_no_line_with_both "$1" "hook health" "cenci update"
    assert_no_line_with_both "$1" "cenci on PATH" "cenci update"
}

echo "case: install checks hook health on the freshly provisioned cache binary, not a stale PATH cenci, and names the shadow (#1419)"
name=hh1419-install-stale-path
hh1419_paths "${name}"
write_hook_health_fake "${hh_bin}/cenci" unknown-subcommand "cenci 2.40.0" path
prepare_cache_hook_health_cenci "${hh_home}" claude healthy "cenci 2.43.1"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
step_section "${CASE_OUTPUT}" "Checking your system" "${WORK}/${name}/preflight"
step_section "${CASE_OUTPUT}" "Setting up cenci" "${WORK}/${name}/setup"
# The post-setup result comes from the cache binary, never the stale PATH one.
assert_contains "${WORK}/${name}/setup" "✓ hook health: healthy"
assert_contains "${CASE_CALLS}" "cache daemon hook-health"
assert_not_contains "${CASE_CALLS}" "path daemon hook-health"
# The preflight prints no hook-health result and exactly one neutral deferral line.
assert_not_contains "${WORK}/${name}/preflight" "✓ hook health"
assert_not_contains "${WORK}/${name}/preflight" "! hook health"
assert_not_contains "${WORK}/${name}/preflight" "✗ hook health"
assert_line_count "${WORK}/${name}/preflight" "hook health" 1
assert_line_count "${WORK}/${name}/preflight" "hook health: checked after setup later in this run" 1
# One shadow warning naming both paths and both versions.
assert_line_count "${CASE_OUTPUT}" "cenci on PATH is" 1
assert_contains "${WORK}/${name}/setup" "! cenci on PATH is ${hh_bin}/cenci (cenci 2.40.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
assert_install_hook_health_never_updates "${CASE_OUTPUT}"

echo "case: update prints the same post-setup hook-health result from the cache binary and still names the shadow (#1419)"
name=hh1419-update-stale-path
hh1419_paths "${name}"
write_hook_health_fake "${hh_bin}/cenci" unknown-subcommand "cenci 2.40.0" path
prepare_cache_hook_health_cenci "${hh_home}" claude healthy "cenci 2.43.1"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
: >"${CASE_CALLS}"
run_update_again "${name}"
[[ "${UPDATE_EXIT}" -eq 0 ]]
assert_not_contains "${UPDATE_OUTPUT}" "Checking your system"
step_section "${UPDATE_OUTPUT}" "Setting up cenci" "${WORK}/${name}/update-setup"
assert_contains "${WORK}/${name}/update-setup" "✓ hook health: healthy"
assert_contains "${CASE_CALLS}" "cache daemon hook-health"
assert_not_contains "${CASE_CALLS}" "path daemon hook-health"
assert_line_count "${UPDATE_OUTPUT}" "cenci on PATH is" 1
assert_contains "${WORK}/${name}/update-setup" "! cenci on PATH is ${hh_bin}/cenci (cenci 2.40.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
assert_install_hook_health_never_updates "${UPDATE_OUTPUT}"

echo "case: doctor's reporter-unavailable warning names the PATH binary and its version, and still says run: cenci update (#1419)"
name=hh1419-doctor-text-versioned
hh1419_paths "${name}"
HH_PATH_VERSION="cenci 2.40.0" run_doctor_case_with_hook_health "${name}" claude unknown-subcommand
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable — ${hh_bin}/cenci (cenci 2.40.0) predates it; run: cenci update"

echo "case: doctor's reporter-unavailable warning says version unknown when the binary's version verb fails (#1419)"
name=hh1419-doctor-text-version-unknown
hh1419_paths "${name}"
HH_PATH_VERSION=fail run_doctor_case_with_hook_health "${name}" claude unknown-subcommand
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable — ${hh_bin}/cenci (version unknown) predates it; run: cenci update"

echo "case: doctor's unrecognized-output warning carries the binary path and version (#1419)"
name=hh1419-doctor-text-garbage
hh1419_paths "${name}"
HH_PATH_VERSION="cenci 2.40.0" run_doctor_case_with_hook_health "${name}" claude garbage
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable — unrecognized output from ${hh_bin}/cenci (cenci 2.40.0); run: cenci update"

echo "case: unrecognized-output warning without a readable version says version unknown (#1419)"
name=hh1419-doctor-text-garbage-unknown
hh1419_paths "${name}"
HH_PATH_VERSION=empty run_doctor_case_with_hook_health "${name}" claude garbage
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable — unrecognized output from ${hh_bin}/cenci (version unknown); run: cenci update"

echo "case: a pinned cache binary without the reporter warns with its path and version, never cenci update (#1419)"
name=hh1419-install-pinned-no-reporter
hh1419_paths "${name}"
prepare_cache_hook_health_cenci "${hh_home}" claude unknown-subcommand "cenci 2.40.0"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
step_section "${CASE_OUTPUT}" "Setting up cenci" "${WORK}/${name}/setup"
assert_contains "${WORK}/${name}/setup" "! hook health: reporter unavailable — ${hh_cache} (cenci 2.40.0): the installed cenci-watch release has no hook-health reporter"
assert_not_contains "${CASE_OUTPUT}" "✓ hook health"
assert_install_hook_health_never_updates "${CASE_OUTPUT}"

echo "case: the in-sandbox shadow at the system link names the host mount and cenci sandbox stop, no link or update command (#1419)"
name=hh1419-shadow-sandbox
hh1419_paths "${name}"
run_shadow_doctor_case "${name}" file "cenci 2.40.0" CENCI_SANDBOX=1 "CENCI_INSTALLER_SYSTEM_LINK=${WORK}/${name}/bin/cenci"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Required tools are present."
assert_line_count "${DOCTOR_OUTPUT}" "cenci on PATH is" 1
assert_contains "${DOCTOR_OUTPUT}" "! cenci on PATH is ${hh_bin}/cenci (cenci 2.40.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "host binary mounted when this sandbox container was created"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "update cenci on the host"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "cenci sandbox stop"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "relaunch"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "ln -sf"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "cenci update"

echo "case: CENCI_SANDBOX=1 with the shadow somewhere other than the system link gets the regular-file hint, not the host-mount text (#1419)"
name=hh1419-shadow-sandbox-elsewhere
hh1419_paths "${name}"
run_shadow_doctor_case "${name}" file "cenci 2.40.0" CENCI_SANDBOX=1
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "remove it"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "sandbox container"

echo "case: a symlink shadow gets the ln -sf hint to ~/.local/bin/cenci, without sudo outside /usr/local/bin (#1419)"
name=hh1419-shadow-symlink
hh1419_paths "${name}"
run_shadow_doctor_case "${name}" symlink "cenci 2.40.0"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Required tools are present."
assert_line_count "${DOCTOR_OUTPUT}" "cenci on PATH is" 1
assert_contains "${DOCTOR_OUTPUT}" "! cenci on PATH is ${hh_bin}/cenci (cenci 2.40.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "ln -sf \"${hh_home}/.local/bin/cenci\" ${hh_bin}/cenci"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "sudo"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "cenci update"
[[ -L "${hh_bin}/cenci" ]]

echo "case: a symlink shadow at the system link gets the sudo ln -sf hint (#1419)"
name=hh1419-shadow-symlink-system
hh1419_paths "${name}"
run_shadow_doctor_case "${name}" symlink "cenci 2.40.0" "CENCI_INSTALLER_SYSTEM_LINK=${WORK}/${name}/bin/cenci"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "sudo ln -sf \"${hh_home}/.local/bin/cenci\" ${hh_bin}/cenci"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "cenci update"

echo "case: a stale ~/.local/bin/cenci symlink is re-pointed at the cache binary, never at itself (#1419)"
name=hh1419-shadow-local-link
hh1419_paths "${name}"
run_shadow_doctor_case "${name}" local-link "cenci 2.40.0" "PATH=${WORK}/${name}/home/.local/bin:${WORK}/${name}/bin"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! cenci on PATH is ${hh_home}/.local/bin/cenci (cenci 2.40.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "ln -sf \"${hh_cache}\" \"${hh_home}/.local/bin/cenci\""
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "ln -sf \"${hh_home}/.local/bin/cenci\" ${hh_home}/.local/bin/cenci"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "cenci update"

echo "case: a regular-file shadow gets the remove-or-link hint and is left byte-identical (#1419)"
name=hh1419-shadow-regular-file
hh1419_paths "${name}"
write_hook_health_fake "${WORK}/${name}/expected-shadow" unknown-subcommand "cenci 2.40.0" path
run_shadow_doctor_case "${name}" file "cenci 2.40.0"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Required tools are present."
assert_contains "${DOCTOR_OUTPUT}" "! cenci on PATH is ${hh_bin}/cenci (cenci 2.40.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "remove it"
assert_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "link to ~/.local/bin/cenci"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "ln -sf"
assert_no_line_with_both "${DOCTOR_OUTPUT}" "cenci on PATH is" "cenci update"
[[ -f "${hh_bin}/cenci" && ! -L "${hh_bin}/cenci" ]]
cmp "${WORK}/${name}/expected-shadow" "${hh_bin}/cenci"

echo "case: no shadow warning when PATH resolves to the cache binary through a symlink chain, doctor (#1419)"
name=hh1419-chain-doctor
hh1419_paths "${name}"
run_shadow_doctor_case "${name}" chain empty
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "✓ hook health: healthy"
assert_contains "${CASE_CALLS}" "cache daemon hook-health"
assert_not_contains "${DOCTOR_OUTPUT}" "cenci on PATH is"

echo "case: no shadow warning when the PATH entry chains through ~/.local/bin to the cache binary, install (#1419)"
name=hh1419-chain-install
hh1419_paths "${name}"
prepare_cache_hook_health_cenci "${hh_home}" claude healthy "cenci 2.43.1"
mkdir -p "${hh_bin}"
ln -sf "${hh_home}/.local/bin/cenci" "${hh_bin}/cenci"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
step_section "${CASE_OUTPUT}" "Setting up cenci" "${WORK}/${name}/setup"
assert_contains "${WORK}/${name}/setup" "✓ hook health: healthy"
assert_contains "${CASE_CALLS}" "cache daemon hook-health"
assert_not_contains "${CASE_OUTPUT}" "cenci on PATH is"

echo "case: no shadow warning when the PATH cenci is the same version, newer, or a dev build (#1419)"
for hh_version in "cenci 2.43.1" "cenci 2.50.0" "cenci dev"; do
    name="hh1419-not-older-${hh_version//[^A-Za-z0-9]/_}"
    hh1419_paths "${name}"
    run_shadow_doctor_case "${name}" file "${hh_version}"
    [[ "${DOCTOR_EXIT}" -eq 0 ]]
    # Precondition: the PATH binary really was the one doctor looked at.
    assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable — ${hh_bin}/cenci (${hh_version}) predates it"
    assert_not_contains "${DOCTOR_OUTPUT}" "cenci on PATH is"
done

echo "case: a PATH cenci whose version verb fails or prints nothing is warned about with version unknown (#1419)"
for hh_version in fail empty; do
    name="hh1419-version-unknown-${hh_version}"
    hh1419_paths "${name}"
    run_shadow_doctor_case "${name}" file "${hh_version}"
    [[ "${DOCTOR_EXIT}" -eq 0 ]]
    assert_line_count "${DOCTOR_OUTPUT}" "cenci on PATH is" 1
    assert_contains "${DOCTOR_OUTPUT}" "! cenci on PATH is ${hh_bin}/cenci (version unknown) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
done

echo "case: a sort that cannot compare versions still yields the shadow warning (#1419)"
name=hh1419-sort-cannot-compare
hh1419_paths "${name}"
# A PATH cenci NEWER than the cache binary is never stale under a working sort
# (the not-older case above); the warning here can only come from the
# sort-failed fallthrough. The stub fails only the `sort -V -C` comparison.
run_shadow_doctor_case "${name}" file "cenci 2.50.0"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_not_contains "${DOCTOR_OUTPUT}" "cenci on PATH is"
real_sort="$(command -v sort)"
rm -f "${hh_bin}/sort"
cat > "${hh_bin}/sort" <<EOF
#!/bin/sh
case " \$* " in
    *" -V "*) case " \$* " in *" -C "*) exit 2 ;; esac ;;
esac
exec "${real_sort}" "\$@"
EOF
chmod +x "${hh_bin}/sort"
invoke_doctor "${name}"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_line_count "${DOCTOR_OUTPUT}" "cenci on PATH is" 1
assert_contains "${DOCTOR_OUTPUT}" "! cenci on PATH is ${hh_bin}/cenci (cenci 2.50.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"

echo "case: no shadow warning when the cache binary's version cannot be read but the PATH cenci's can (#1419)"
for hh_version in fail empty; do
    name="hh1419-cache-version-unknown-${hh_version}"
    hh1419_paths "${name}"
    write_hook_health_fake "${hh_bin}/cenci" unknown-subcommand "cenci 2.40.0" path
    prepare_cache_hook_health_cenci "${hh_home}" claude healthy "${hh_version}"
    run_case "${name}" claude
    [[ "${CASE_EXIT}" -eq 0 ]]
    step_section "${CASE_OUTPUT}" "Setting up cenci" "${WORK}/${name}/setup"
    # Preconditions: the post-setup checks ran against the cache binary, and
    # both binaries were asked for their version, so the absence is meaningful.
    assert_contains "${WORK}/${name}/setup" "✓ hook health: healthy"
    assert_contains "${CASE_CALLS}" "cache version"
    assert_contains "${CASE_CALLS}" "path version"
    assert_not_contains "${CASE_OUTPUT}" "cenci on PATH is"
done

echo "case: multi-line / non-printable version output is reduced to a clean first line (#1419)"
name=hh1419-version-noisy
hh1419_paths "${name}"
run_shadow_doctor_case "${name}" file noisy
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "! cenci on PATH is ${hh_bin}/cenci (cenci 2.40.0) but the active plugin binary is ${hh_cache} (cenci 2.43.1)"
assert_not_contains "${DOCTOR_OUTPUT}" "second-line-marker"
assert_not_contains "${DOCTOR_OUTPUT}" $'\001'
assert_not_contains "${DOCTOR_OUTPUT}" $'\a'

echo "case: an install where the cache binary could not be provisioned prints no post-setup hook-health or shadow line (#1419)"
name=hh1419-install-unprovisioned
hh1419_paths "${name}"
write_hook_health_fake "${hh_bin}/cenci" unknown-subcommand "cenci 2.40.0" path
run_case_broken_cenci_cache "${name}"
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "could not provision the cenci binary yet"
assert_not_contains "${CASE_OUTPUT}" "✓ hook health"
assert_not_contains "${CASE_OUTPUT}" "! hook health"
assert_not_contains "${CASE_OUTPUT}" "✗ hook health"
assert_not_contains "${CASE_OUTPUT}" "cenci on PATH is"
assert_not_contains "${CASE_CALLS}" "daemon hook-health"
assert_install_hook_health_never_updates "${CASE_OUTPUT}"

echo "case: an incomplete update runs the post-setup hook-health check against the previously provisioned fallback binary and still fails (#1419)"
name=hh1419-update-fallback-binary
hh1419_paths "${name}"
STALE_NEWEST_HH_MODE=healthy STALE_NEWEST_HH_VERSION="cenci 2.43.1" run_case_stale_newest_cenci_cache "${name}" update
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "cenci binary update incomplete"
step_section "${CASE_OUTPUT}" "Setting up cenci" "${WORK}/${name}/setup"
assert_contains "${WORK}/${name}/setup" "✓ hook health: healthy"
assert_contains "${CASE_CALLS}" "cache daemon hook-health"
assert_install_hook_health_never_updates "${CASE_OUTPUT}"

echo "case: a cenci-watch that never installs gets no post-setup hook-health or shadow check (#1419)"
name=hh1419-install-deselected
hh1419_paths "${name}"
write_hook_health_fake "${hh_bin}/cenci" unknown-subcommand "cenci 2.40.0" path
prepare_cache_hook_health_cenci "${hh_home}" claude healthy "cenci 2.43.1"
make_common_tools "${hh_bin}"
make_claude "${hh_bin}"
prepare_checkout "${hh_home}" claude
: >"${WORK}/${name}/calls"
set +e
env -i HOME="${hh_home}" PATH="${hh_bin}" CALLS_FILE="${WORK}/${name}/calls" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    CLAUDE_SKIP_INSTALL_PLUGIN="cenci-watch" \
    bash "${ROOT}/install.sh" --yes --no-build >"${WORK}/${name}/output" 2>&1
hh_deselected_exit=$?
set -e
[[ "${hh_deselected_exit}" -eq 0 ]]
assert_not_contains "${WORK}/${name}/output" "==> Setting up cenci"
assert_not_contains "${WORK}/${name}/output" "✓ hook health"
assert_not_contains "${WORK}/${name}/output" "! hook health"
assert_not_contains "${WORK}/${name}/output" "✗ hook health"
assert_not_contains "${WORK}/${name}/output" "cenci on PATH is"
assert_not_contains "${WORK}/${name}/calls" "daemon hook-health"

echo "case: doctor with a PATH cenci and no cache binary names no shadow and checks hook health on the PATH binary (#1419)"
name=hh1419-doctor-no-cache
hh1419_paths "${name}"
write_hook_health_fake "${hh_bin}/cenci" unknown-subcommand "cenci 2.40.0" path
run_case "${name}" claude
rm -f "${hh_home}"/.claude/plugins/cache/cenci/cenci-watch/*/bin/cenci
: >"${CASE_CALLS}"
invoke_doctor "${name}"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "path daemon hook-health"
assert_contains "${DOCTOR_OUTPUT}" "! hook health: reporter unavailable — ${hh_bin}/cenci (cenci 2.40.0) predates it; run: cenci update"
assert_not_contains "${DOCTOR_OUTPUT}" "cenci on PATH is"

echo "case: with ~/.local/bin off PATH and no other cenci, the post-setup hook-health check still runs and no shadow is named (#1419)"
name=hh1419-install-no-path-cenci
hh1419_paths "${name}"
prepare_cache_hook_health_cenci "${hh_home}" claude healthy "cenci 2.43.1"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
[[ ! -e "${hh_bin}/cenci" ]]
step_section "${CASE_OUTPUT}" "Setting up cenci" "${WORK}/${name}/setup"
assert_contains "${WORK}/${name}/setup" "✓ hook health: healthy"
assert_contains "${CASE_CALLS}" "cache daemon hook-health"
assert_not_contains "${CASE_OUTPUT}" "cenci on PATH is"
assert_install_hook_health_never_updates "${CASE_OUTPUT}"

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

echo "case: doctor hard-fails when cosign is absent — cosign is a mandatory prerequisite for both the default verified piped install path (#626) and the update-mode bootstrap (#700), promoted from optional to required"
run_doctor_case_with_cosign doctor-cosign-absent claude absent
[[ "${DOCTOR_EXIT}" -ne 0 ]]
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
# make_common_tools already plants cosign ambiently (#700); this explicit
# call is kept for clarity even though it's now redundant, per the plan's
# fix for this test (this run now hits the update-mode bootstrap gate,
# which hard-requires cosign).
make_cosign "${bin}"

real_claude_dir="${home}/dot-claude"
checkout="${real_claude_dir}/plugins/marketplaces/cenci"
mkdir -p "${checkout}"
cp "${ROOT}/cenci" "${checkout}/cenci"
chmod +x "${checkout}/cenci"
cp "${ROOT}/install.sh" "${checkout}/install.sh"
ln -s "${real_claude_dir}" "${home}/.claude"
prepare_bootstrap_cenci "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" ROOT="${ROOT}" \
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
assert_contains "${DOCTOR_OUTPUT}" "OpenCode is temporarily disabled"

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
assert_contains "${output}" "OpenCode is temporarily disabled"
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

echo "case: install and rerun leave OpenCode untouched during the pause"
name=opencode-idempotent
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "OpenCode is temporarily disabled"
assert_not_contains "${CASE_OUTPUT}" "Setting up OpenCode integration"
[[ ! -e "${CASE_HOME}/.config/opencode" ]]

# Existing user configuration and skills must also survive install/update unchanged.
mkdir -p "${CASE_HOME}/.config/opencode/skills"
printf '{"theme":"dark","plugin":["user-plugin"],"permission":{"*":"ask"}}\n' >"${CASE_HOME}/.config/opencode/opencode.json"
cp "${CASE_HOME}/.config/opencode/opencode.json" "${WORK}/${name}/original-config"
printf 'user skill\n' >"${CASE_HOME}/.config/opencode/skills/my-skill"
second_output="${WORK}/${name}/second-run-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${second_output}" 2>&1
second_run_exit=$?
set -e
[[ "${second_run_exit}" -eq 0 ]]
assert_contains "${second_output}" "OpenCode is temporarily disabled"
cmp "${WORK}/${name}/original-config" "${CASE_HOME}/.config/opencode/opencode.json"
assert_contains "${CASE_HOME}/.config/opencode/skills/my-skill" "user skill"

update_output="${WORK}/${name}/update-output"
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" update --yes --no-build >"${update_output}" 2>&1
assert_contains "${update_output}" "OpenCode is temporarily disabled"
cmp "${WORK}/${name}/original-config" "${CASE_HOME}/.config/opencode/opencode.json"
assert_contains "${CASE_HOME}/.config/opencode/skills/my-skill" "user skill"

echo "case: uninstall removes only cenci's OpenCode skill symlinks and plugin entry, preserving the user's own config and skills"
name=opencode-uninstall
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
# Seed a legacy installation; the paused installer must not recreate it.
mkdir -p "${CASE_HOME}/.config/opencode/skills"
ln -s "${CASE_HOME}/.claude/plugins/marketplaces/cenci/flow/skills/testing" "${CASE_HOME}/.config/opencode/skills/testing"
printf '{"plugin":["file://%s/watch/plugin/opencode"],"permission":{"*":"allow"},"autoupdate":false}\n' "${CASE_HOME}/.claude/plugins/marketplaces/cenci" >"${CASE_HOME}/.config/opencode/opencode.json"
echo "user content" >"${CASE_HOME}/.config/opencode/skills/my-own-skill"
jq '. + {"theme": "dark"}' "${CASE_HOME}/.config/opencode/opencode.json" >"${CASE_HOME}/.config/opencode/opencode.json.tmp"
mv "${CASE_HOME}/.config/opencode/opencode.json.tmp" "${CASE_HOME}/.config/opencode/opencode.json"

invoke_uninstall "${name}"
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.config/opencode/skills/testing" ]]
[[ -f "${CASE_HOME}/.config/opencode/skills/my-own-skill" ]]
assert_contains "${CASE_HOME}/.config/opencode/opencode.json" "theme"
assert_contains "${CASE_HOME}/.config/opencode/opencode.json" "permission"
assert_contains "${CASE_HOME}/.config/opencode/opencode.json" "autoupdate"
assert_not_contains "${CASE_HOME}/.config/opencode/opencode.json" "watch/plugin/opencode"
# The legacy allow-all block survives (never auto-removed) but is flagged once.
jq -e '.permission == {"*":"allow"} and .autoupdate == false' "${CASE_HOME}/.config/opencode/opencode.json" >/dev/null
assert_allow_all_hint "${CASE_HOME}/.config/opencode/opencode.json"

# run_opencode_uninstall_case <name> <json> [NAME=value env...] — after a fresh
# install, seeds a legacy host opencode.json (the cenci-watch plugin spec is
# spliced into <json> at the __PLUGIN__ placeholder) at the path OpenCode
# resolves (XDG_CONFIG_HOME when passed), then uninstalls. Sets
# OPENCODE_CASE_CONFIG to that path plus UNINSTALL_EXIT/UNINSTALL_OUTPUT.
run_opencode_uninstall_case() {
    local name="$1" json="$2" config_dir
    shift 2
    run_case_opencode "${name}" claude 1.18.3
    [[ "${CASE_EXIT}" -eq 0 ]]
    config_dir="${CASE_HOME}/.config/opencode"
    local arg
    for arg in "$@"; do
        case "${arg}" in
        XDG_CONFIG_HOME=*) config_dir="${arg#XDG_CONFIG_HOME=}/opencode" ;;
        esac
    done
    OPENCODE_CASE_CONFIG="${config_dir}/opencode.json"
    mkdir -p "${config_dir}"
    printf '%s\n' "${json//__PLUGIN__/file://${CASE_HOME}/.claude/plugins/marketplaces/cenci/watch/plugin/opencode}" >"${OPENCODE_CASE_CONFIG}"
    invoke_uninstall "${name}" "$@"
}

echo "case: uninstall removes the plugin entry from a config holding only the plugin plus shorthand allow-all, then flags it without touching permission"
name=opencode-uninstall-shorthand
run_opencode_uninstall_case "${name}" '{"plugin":["__PLUGIN__"],"permission":"allow"}'
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${UNINSTALL_OUTPUT}" "removed cenci's OpenCode plugin registration"
jq -e '(.plugin | length) == 0 and .permission == "allow"' "${OPENCODE_CASE_CONFIG}" >/dev/null
assert_allow_all_hint "${OPENCODE_CASE_CONFIG}"

echo "case: uninstall prints no allow-all hint for a non-global permission"
name=opencode-uninstall-nonglobal
run_opencode_uninstall_case "${name}" '{"plugin":["__PLUGIN__"],"permission":{"*":"ask","bash":"allow"}}'
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${UNINSTALL_OUTPUT}" "removed cenci's OpenCode plugin registration"
assert_not_contains "${UNINSTALL_OUTPUT}" "older cenci versions may have added"
assert_not_contains "${UNINSTALL_OUTPUT}" "jq 'del(.permission, .autoupdate)'"

echo "case: uninstall prints no allow-all hint when permission is absent"
name=opencode-uninstall-no-permission
run_opencode_uninstall_case "${name}" '{"plugin":["__PLUGIN__"],"theme":"dark"}'
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${UNINSTALL_OUTPUT}" "removed cenci's OpenCode plugin registration"
assert_not_contains "${UNINSTALL_OUTPUT}" "older cenci versions may have added"

echo "case: uninstall without jq prints only the existing manual-removal warning, no allow-all hint, and leaves the file byte-identical"
name=opencode-uninstall-no-jq
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
rm "${CASE_BIN}/jq"
mkdir -p "${CASE_HOME}/.config/opencode"
printf '{"plugin":["file://%s/.claude/plugins/marketplaces/cenci/watch/plugin/opencode"],"permission":"allow"}\n' "${CASE_HOME}" >"${CASE_HOME}/.config/opencode/opencode.json"
cp "${CASE_HOME}/.config/opencode/opencode.json" "${WORK}/${name}/fixture-original"
invoke_uninstall "${name}"
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${UNINSTALL_OUTPUT}" "jq not found — remove the cenci plugin entry"
assert_not_contains "${UNINSTALL_OUTPUT}" "older cenci versions may have added"
cmp "${WORK}/${name}/fixture-original" "${CASE_HOME}/.config/opencode/opencode.json"

echo "case: uninstall still prints the allow-all hint when OpenCode is not detected (cleanup is not gated on HAS_OPENCODE)"
name=opencode-uninstall-undetected
run_case_opencode "${name}" claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
undetected_config="${CASE_HOME}/.config/opencode/opencode.json"
mkdir -p "${CASE_HOME}/.config/opencode"
printf '{"plugin":["file://%s/.claude/plugins/marketplaces/cenci/watch/plugin/opencode"],"permission":{"*":"allow"}}\n' "${CASE_HOME}" >"${undetected_config}"
invoke_uninstall "${name}"
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${UNINSTALL_OUTPUT}" "removed cenci's OpenCode plugin registration"
jq -e '(.plugin | length) == 0 and .permission == {"*":"allow"}' "${undetected_config}" >/dev/null
assert_allow_all_hint "${undetected_config}"

echo "case: with XDG_CONFIG_HOME set, uninstall cleans and flags \$XDG_CONFIG_HOME/opencode/opencode.json, not ~/.config"
name=opencode-uninstall-xdg
xdg_dir="${WORK}/${name}/xdg"
run_opencode_uninstall_case "${name}" '{"plugin":["__PLUGIN__"],"permission":{"*":"allow"}}' "XDG_CONFIG_HOME=${xdg_dir}"
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
[[ "${OPENCODE_CASE_CONFIG}" == "${xdg_dir}/opencode/opencode.json" ]]
assert_contains "${UNINSTALL_OUTPUT}" "removed cenci's OpenCode plugin registration"
assert_not_contains "${OPENCODE_CASE_CONFIG}" "watch/plugin/opencode"
assert_allow_all_hint "${xdg_dir}/opencode/opencode.json"
assert_not_contains "${UNINSTALL_OUTPUT}" "${CASE_HOME}/.config/opencode/opencode.json"

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
# Simulate the registration left by an older installer/cache layout.
mkdir -p "${home}/.config/opencode"
printf '{"plugin":["file://%s/.claude/plugins/cache/cenci/cenci-watch/1.0.0/opencode"]}\n' "${home}" >"${home}/.config/opencode/opencode.json"

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
cache_fallback_install_exit=$?
set -e
[[ "${cache_fallback_install_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode is temporarily disabled"
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

# --- Legacy host OpenCode allow-all (#1018): doctor warns, never modifies.

echo "case: doctor flags a host opencode.json granting global allow-all (shorthand, object, object with exceptions) without modifying it"
name=opencode-allow-all
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
allow_all_config="${CASE_HOME}/.config/opencode/opencode.json"
for allow_all_json in '{"permission":"allow"}' '{"permission":{"*":"allow"}}' '{"permission":{"*":"allow","bash":"ask"},"autoupdate":false}'; do
    opencode_doctor_with_fixture "${name}" "${allow_all_json}"
    assert_allow_all_warning "${allow_all_config}"
done

echo "case: doctor stays silent for non-global, absent, missing, invalid, non-object and per-agent permissions"
for silent_json in \
    '{"permission":{"*":"ask","bash":"allow"}}' \
    '{"theme":"dark"}' \
    '{"autoupdate":false}' \
    'not json {' \
    '[]' \
    '{"permission":true}' \
    '{"permission":[]}' \
    '{"agent":{"x":{"permission":{"*":"allow"}}}}'; do
    opencode_doctor_with_fixture "${name}" "${silent_json}"
    assert_no_allow_all_warning
done
rm -f "${allow_all_config}"
invoke_doctor "${name}"
assert_no_allow_all_warning
[[ ! -e "${allow_all_config}" ]]

echo "case: an allow-all finding is a warning only, so doctor's exit status matches the same config without permission"
opencode_doctor_with_fixture "${name}" '{"theme":"dark"}'
benign_doctor_exit="${DOCTOR_EXIT}"
[[ "${benign_doctor_exit}" -eq 0 ]]
opencode_doctor_with_fixture "${name}" '{"permission":{"*":"allow"}}'
assert_allow_all_warning "${allow_all_config}"
[[ "${DOCTOR_EXIT}" -eq "${benign_doctor_exit}" ]]

echo "case: with XDG_CONFIG_HOME set, doctor inspects \$XDG_CONFIG_HOME/opencode/opencode.json, not ~/.config"
xdg_dir="${WORK}/${name}/xdg"
mkdir -p "${xdg_dir}/opencode"
printf '{"permission":{"*":"allow"}}\n' >"${xdg_dir}/opencode/opencode.json"
rm -f "${allow_all_config}"
invoke_doctor "${name}" "XDG_CONFIG_HOME=${xdg_dir}"
assert_allow_all_warning "${xdg_dir}/opencode/opencode.json"
assert_not_contains "${DOCTOR_OUTPUT}" "${allow_all_config}"
printf '{"theme":"dark"}\n' >"${xdg_dir}/opencode/opencode.json"
opencode_doctor_with_fixture "${name}" '{"permission":{"*":"allow"}}' "XDG_CONFIG_HOME=${xdg_dir}"
assert_no_allow_all_warning

echo "case: doctor prints no allow-all warning when OpenCode is not detected, even with an allow-all host config"
name=opencode-allow-all-undetected
run_case_opencode "${name}" claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
opencode_doctor_with_fixture "${name}" '{"permission":{"*":"allow"}}'
assert_not_contains "${DOCTOR_OUTPUT}" "OpenCode skills"
assert_not_contains "${DOCTOR_OUTPUT}" "grants global allow-all permissions"

echo "case: without jq on PATH doctor prints no allow-all line and its exit status matches a config without permission"
name=opencode-allow-all-no-jq
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
rm "${CASE_BIN}/jq"
opencode_doctor_with_fixture "${name}" '{"theme":"dark"}'
no_jq_benign_exit="${DOCTOR_EXIT}"
# Without jq doctor's required "jq" check fails, so the benign baseline is a
# known non-zero status (1) for exactly that reason, not an unrelated failure.
assert_contains "${DOCTOR_OUTPUT}" "install jq from your package manager"
[[ "${no_jq_benign_exit}" -eq 1 ]]
opencode_doctor_with_fixture "${name}" '{"permission":{"*":"allow"}}'
assert_no_allow_all_warning
[[ "${DOCTOR_EXIT}" -eq "${no_jq_benign_exit}" ]]

echo "case: contract — install.sh never calls or sources the container-only seeding helper (code lines; full-line comments are exempt)"
if references_container_seeder "${ROOT}/install.sh"; then
    echo "FAIL: ${ROOT}/install.sh references seed_opencode_config or sandbox/lib/opencode-config.sh on a code line" >&2
    exit 1
fi
# Non-vacuity: the detector must flag injected copies, and only on code lines.
contract_dir="${WORK}/seeder-contract"
mkdir -p "${contract_dir}"
cp "${ROOT}/install.sh" "${contract_dir}/injected-call.sh"
printf '%s\n' "seed_opencode_config \"\$f\" \"\$s\"" >>"${contract_dir}/injected-call.sh"
assert_detects_seeder_reference "${contract_dir}/injected-call.sh"
cp "${ROOT}/install.sh" "${contract_dir}/injected-source.sh"
printf '%s\n' "source \"\${dir}/sandbox/lib/opencode-config.sh\"" >>"${contract_dir}/injected-source.sh"
assert_detects_seeder_reference "${contract_dir}/injected-source.sh"
cp "${ROOT}/install.sh" "${contract_dir}/injected-relative-source.sh"
printf '%s\n' "source \"\${SCRIPT_DIR}/lib/opencode-config.sh\"" >>"${contract_dir}/injected-relative-source.sh"
assert_detects_seeder_reference "${contract_dir}/injected-relative-source.sh"
cp "${ROOT}/install.sh" "${contract_dir}/injected-trailing.sh"
printf '%s\n' 'true # seed_opencode_config' >>"${contract_dir}/injected-trailing.sh"
assert_detects_seeder_reference "${contract_dir}/injected-trailing.sh"
cp "${ROOT}/install.sh" "${contract_dir}/injected-comment.sh"
printf '%s\n' '	# re-enablement must never call seed_opencode_config' >>"${contract_dir}/injected-comment.sh"
if references_container_seeder "${contract_dir}/injected-comment.sh"; then
    echo "FAIL: a full-line comment naming seed_opencode_config must not trip the contract" >&2
    exit 1
fi

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

echo "case: a resolved release tag that is not watch/* (only watch releases publish and sign install.sh — flow/sandbox version bumps create bare tags with no Release object) is rejected by exec_pinned_installer's own prefix guard before any install.sh download is attempted, with a distinct message naming the invariant, never a confusing download 404 (#736)"
run_piped_case piped-verify-non-watch-prefix claude "CENCI_FAKE_RELEASE_TAG=flow/v1.0.0"
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "only watch/* releases publish a signed install.sh"
assert_not_contains "${CASE_CURL_LOG}" "/install.sh"

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

# --- cenci update bootstraps the resolved release's installer instead of
# trusting a stale local copy (#700): on installs pinned before #678, the
# installed installer's own step_marketplace can't move the pin, so
# `cenci update` was a permanent silent no-op. Every case below invokes the
# checkout's own install.sh directly (run_update_bootstrap_case), the same
# way the `cenci` wrapper execs it, so BASH_SOURCE[0] resolves inside the
# managed marketplace checkout and the new bootstrap gate can fire.

echo "case: AC1 - cenci update from a managed checkout registered at an old ref bootstraps the target release's installer and installs the new plugin versions through it, not the on-disk installer's own step_marketplace (verified via the ref that actually supplied installed content, not merely which ref was requested)"
name=update-bootstrap-repoints-stale-pin
mkdir -p "${WORK}/${name}"
printf 'watch/v0.27.7' >"${WORK}/${name}/claude-marketplace"
run_update_bootstrap_case "${name}" claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9" "watch/v0.27.7"
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CURL_LOG}" "install.sh"
assert_contains "${CASE_COSIGN_LOG}" "verify-blob"
assert_contains "${WORK}/${name}/claude-installed-cenci" "watch/v9.9.9"
assert_not_contains "${WORK}/${name}/claude-installed-cenci" "watch/v0.27.7"

echo "case: AC2 - the update-mode bootstrap re-execs at most once — install.sh is fetched exactly one time from the resolved release, the child never re-enters the gate (#700)"
name=update-bootstrap-single-reexec
mkdir -p "${WORK}/${name}"
printf 'watch/v0.27.7' >"${WORK}/${name}/claude-marketplace"
run_update_bootstrap_case "${name}" claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9" ""
[[ "${CASE_EXIT}" -eq 0 ]]
[[ "$(grep -c 'install\.sh$' "${CASE_CURL_LOG}")" -eq 1 ]]

echo "case: AC3 - an explicit --ref suppresses the update-mode bootstrap entirely, even for a checkout whose currency can't be proven, and pins the marketplace + installed content to exactly that ref"
name=update-bootstrap-suppressed-by-ref-flag
mkdir -p "${WORK}/${name}"
printf 'watch/v0.27.7' >"${WORK}/${name}/claude-marketplace"
run_update_bootstrap_case "${name}" claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9" "" --ref watch/v2.3.4
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CURL_LOG}" "install.sh"
assert_not_contains "${CASE_COSIGN_LOG}" "verify-blob"
assert_contains "${WORK}/${name}/claude-installed-cenci" "watch/v2.3.4"

echo "case: AC3 - CENCI_REF=main suppresses the update-mode bootstrap entirely — the only path that actually pins the marketplace and installed plugin content to live main"
name=update-bootstrap-suppressed-by-cenci-ref-main
mkdir -p "${WORK}/${name}"
printf 'watch/v0.27.7' >"${WORK}/${name}/claude-marketplace"
run_update_bootstrap_case "${name}" claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_REF=main" ""
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CURL_LOG}" "install.sh"
assert_not_contains "${CASE_COSIGN_LOG}" "verify-blob"
assert_contains "${WORK}/${name}/claude-installed-cenci" "main"

echo "case: AC4 - a dev run directly from the git source checkout (\$ROOT/install.sh update) never downloads or re-execs a pinned installer — the managed-checkout gate excludes a source checkout even though CENCI_REF is unset and its currency can't be proven"
name=update-dev-checkout-no-bootstrap
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
    bash "${ROOT}/install.sh" --yes --no-build update >"${output}" 2>&1
dev_checkout_update_exit=$?
set -e
[[ "${dev_checkout_update_exit}" -eq 0 ]]
assert_not_contains "${curl_log}" "/install.sh"
assert_contains "${calls}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"

echo "case: AC5 - cosign missing hard-fails the update-mode bootstrap with the actionable install-cosign message, never falling back to the stale local installer's own marketplace mutation"
name=update-bootstrap-cosign-missing
mkdir -p "${WORK}/${name}"
printf 'watch/v0.27.7' >"${WORK}/${name}/claude-marketplace"
run_update_bootstrap_case "${name}" claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 COSIGN_MODE=absent" ""
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "cosign not found"
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CALLS}" "marketplace add"

echo "case: AC6 - a corrupted/tampered install.sh downloaded by the update-mode bootstrap fails cosign verification and never falls back to running the stale local installer's own marketplace mutation"
name=update-bootstrap-corrupt-install
mkdir -p "${WORK}/${name}"
printf 'watch/v0.27.7' >"${WORK}/${name}/claude-marketplace"
run_update_bootstrap_case "${name}" claude "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9 CENCI_FAKE_CORRUPT_INSTALL=1" ""
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "installer verification failed"
assert_contains "${CASE_OUTPUT}" "CENCI_REF=main"
assert_not_contains "${CASE_CALLS}" "marketplace add"

echo "case: AC9 - cenci update from a managed checkout already pinned at the resolved latest ref performs no installer download — the bootstrap skips its own re-exec when the checkout is provably current"
name=update-bootstrap-skips-when-current
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
curl_log="${WORK}/${name}/curl-log" cosign_log="${WORK}/${name}/cosign-log"
mkdir -p "${home}" "${bin}"
: >"${calls}"
: >"${curl_log}"
: >"${cosign_log}"
make_common_tools "${bin}"
make_claude "${bin}"
prepare_checkout "${home}" claude
prepare_git_tagged_checkout "${home}/.claude/plugins/marketplaces/cenci" "watch/v9.9.9"
prepare_bootstrap_cenci "${home}" claude
printf 'watch/v9.9.9' >"${case_dir}/claude-marketplace"
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" ROOT="${ROOT}" \
    CURL_LOG="${curl_log}" COSIGN_LOG="${cosign_log}" \
    CENCI_FAKE_RELEASE_TAG="watch/v9.9.9" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${home}/.claude/plugins/marketplaces/cenci/install.sh" --yes --no-build update >"${output}" 2>&1
already_current_exit=$?
set -e
[[ "${already_current_exit}" -eq 0 ]]
assert_contains "${curl_log}" "releases/latest"
assert_not_contains "${curl_log}" "/install.sh"

# --- cenci doctor: stale marketplace pin detection (#700). The only signal
# a stranded pre-#678 install can get — must warn (never fail doctor) on a
# stale pin, and must never conflate "couldn't determine latest" (offline)
# with "your pin is stale".

echo "case: AC7 - cenci doctor warns on a stale marketplace pin and prints the exact remove/re-add/update recovery commands, without failing doctor"
run_doctor_case_with_stale_pin_checkout doctor-stale-pin-warns claude "watch/v0.27.7" "CENCI_FAKE_RELEASE_TAG=watch/v9.9.9"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "claude plugin marketplace remove cenci"
assert_contains "${DOCTOR_OUTPUT}" "claude plugin marketplace add matteobortolazzo/cenci@watch/v9.9.9"
assert_contains "${DOCTOR_OUTPUT}" "cenci update"

echo "case: AC8 - cenci doctor degrades gracefully when it can't reach the latest release (offline) — reports that it could not determine the latest release, and never claims the pin is stale or prints the stale-pin recovery commands"
run_doctor_case_with_stale_pin_checkout doctor-stale-pin-offline claude "watch/v1.0.0" "CENCI_FAKE_NO_RELEASE=1"
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "could not determine the latest release"
assert_not_contains "${DOCTOR_OUTPUT}" "claude plugin marketplace remove cenci"

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

echo "case: optional host permissions reach both clients during install and update"
for host_mode in install update; do
    host_flags=()
    if [[ "${host_mode}" == update ]]; then host_flags=(update); fi
    run_case "host-grants-${host_mode}" dual --no-build ${host_flags[@]+"${host_flags[@]}"} --host-permissions --no-host-ccline
    [[ "${CASE_EXIT}" -eq 0 ]]
    jq -e '.permissions.allow | index("Bash(gh:*)")' "${CASE_HOME}/.claude/settings.json" >/dev/null
    assert_contains "${CASE_HOME}/.codex/rules/cenci-host.rules" 'prefix_rule(pattern=["gh"], decision="allow")'
done

echo "passed: client detection, installation, launchers, and summaries"
