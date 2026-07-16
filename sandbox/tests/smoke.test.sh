#!/usr/bin/env bash
# Runtime smoke test for the split cenci-sandbox-base + fragments image
# layout (ticket #107).
#
# Builds cenci-sandbox-base:<ver> from Dockerfile.base, then builds the
# monolithic cenci-sandbox:latest FROM that base image, then runs the
# monolith and asserts every baked-in toolchain works. This is the
# regression guard for the latent bug where ubuntu:24.04 lacks libicu74,
# which makes `dotnet --version` FailFast (.NET's globalization invariant
# checks need ICU).
#
# Requires docker or podman on the host. Self-skips (exit 0) rather than
# failing when neither is available, so this never hard-fails in
# environments without a container runtime (e.g. plain CI shells).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SANDBOX_DIR="${REPO_ROOT}/sandbox"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

# summarize_and_exit <exit-code>: prints the pass/fail summary and exits.
# Used for the fatal build-failure paths below, where there's nothing left
# worth testing once a build step has failed.
summarize_and_exit() {
    print_summary
    exit "$1"
}

echo "smoke.test.sh"

# ── Auto-detect container runtime (self-skip if none available) ──
# Mirrors the detection order used by the cenci launcher (podman, then docker).
if command -v podman &>/dev/null; then
    RUNTIME=podman
elif command -v docker &>/dev/null; then
    RUNTIME=docker
else
    echo "SKIP: neither podman nor docker found on PATH — smoke test requires a container runtime."
    exit 0
fi

# ── Resolve BASE_TAG via content hash ─────────────────────────────
# Portable content hash of all Dockerfile.base build inputs
# (Dockerfile.base + entrypoint.sh + everything COPYed from lib/). Rebuilds the
# base only when those inputs change — not on every plugin.json version bump.
# Prefers sha256sum (Linux); falls back to shasum -a 256 (macOS). Paths are
# RELATIVE (subshell cd) so the digest is identical here and in the Go launcher
# regardless of the absolute checkout path.
if command -v sha256sum &>/dev/null; then
    HASH_CMD=(sha256sum)
elif command -v shasum &>/dev/null; then
    HASH_CMD=(shasum -a 256)
else
    fail "neither sha256sum nor shasum on PATH — cannot resolve base tag."
    summarize_and_exit 1
fi

# ── Portable `stat` (GNU vs BSD) ───────────────────────────────────
# GNU stat (Linux) takes `-c '%u'`/`-c '%g'`; BSD stat (macOS) takes
# `-f '%u'`/`-f '%g'` instead. Mirrors the sha256sum/shasum fallback above:
# probe for the flavor once, up front, rather than at each call site.
if [[ "$(uname -s)" == "Darwin" ]]; then
    STAT_CMD=(stat -f)
else
    STAT_CMD=(stat -c)
fi

# GNU coreutils names the duration guard `timeout`; Homebrew installs it as
# `gtimeout`. Keep the suite runnable on a stock macOS host even when neither is
# present—the probes are short-lived container commands, so only the hang guard
# is skipped in that fallback.
if command -v timeout &>/dev/null; then
    TIMEOUT_BIN=timeout
elif command -v gtimeout &>/dev/null; then
    TIMEOUT_BIN=gtimeout
else
    TIMEOUT_BIN=""
    echo "  NOTE: timeout/gtimeout unavailable — running container probes without a host-side hang guard."
fi

run_with_timeout() {
    if [[ -n "${TIMEOUT_BIN}" ]]; then
        "${TIMEOUT_BIN}" 60 "$@"
    else
        "$@"
    fi
}

if ! BASE_TAG_FILES="$(cd "${SANDBOX_DIR}" && find Dockerfile.base entrypoint.sh lib -type f)"; then
    fail "failed to enumerate base image build inputs (Dockerfile.base, entrypoint.sh, lib/)."
    summarize_and_exit 1
fi
if [[ -z "${BASE_TAG_FILES}" ]]; then
    fail "no base image build inputs found (Dockerfile.base, entrypoint.sh, lib/)."
    summarize_and_exit 1
fi
BASE_TAG="$(
    cd "${SANDBOX_DIR}" &&
    LC_ALL=C sort <<<"${BASE_TAG_FILES}" |
        xargs -r "${HASH_CMD[@]}" |
        "${HASH_CMD[@]}" |
        cut -c1-12
)"
if [[ -z "${BASE_TAG}" ]]; then
    fail "failed to compute base image content hash."
    summarize_and_exit 1
fi
echo "resolved BASE_TAG=${BASE_TAG}"

# ── Build the base image ──────────────────────────────────────────
echo "case: build cenci-sandbox-base:${BASE_TAG} from Dockerfile.base"
if "${RUNTIME}" build -f "${SANDBOX_DIR}/Dockerfile.base" -t "cenci-sandbox-base:${BASE_TAG}" "${SANDBOX_DIR}"; then
    pass
else
    fail "failed to build cenci-sandbox-base:${BASE_TAG} from Dockerfile.base"
    summarize_and_exit 1
fi

# ── Build the monolith FROM the base image ────────────────────────
echo "case: build cenci-sandbox:latest (--build-arg BASE_VERSION=${BASE_TAG})"
if "${RUNTIME}" build --build-arg "BASE_VERSION=${BASE_TAG}" -t cenci-sandbox:latest -f "${SANDBOX_DIR}/Dockerfile" "${SANDBOX_DIR}"; then
    pass
else
    fail "failed to build cenci-sandbox:latest FROM cenci-sandbox-base:${BASE_TAG}"
    summarize_and_exit 1
fi

# ── Run the built image and assert every toolchain works ──────────
# A generated per-repo image replaces the monolith whenever the repository
# contains .cenci/Dockerfile. It must carry Node, but no root-owned agent CLI.
echo "case: monolith and configured per-repo images contain no baked agent CLIs"
if "${RUNTIME}" run --rm --entrypoint /bin/bash cenci-sandbox:latest -c \
        'test ! -e /usr/local/bin/codex && test ! -e /usr/local/bin/claude \
            && test ! -e /usr/lib/node_modules/@openai/codex \
            && test ! -e /usr/lib/node_modules/@anthropic-ai/claude-code' \
    && "${RUNTIME}" build --build-arg "BASE_VERSION=${BASE_TAG}" \
    -t cenci-sandbox-smoke-repo:latest \
    -f "${REPO_ROOT}/.cenci/Dockerfile" "${REPO_ROOT}/.cenci" \
    && "${RUNTIME}" run --rm --entrypoint /bin/bash \
        cenci-sandbox-smoke-repo:latest -c \
        'node --version && test ! -e /usr/local/bin/codex && test ! -e /usr/local/bin/claude \
            && test ! -e /usr/lib/node_modules/@openai/codex \
            && test ! -e /usr/lib/node_modules/@anthropic-ai/claude-code'; then
    pass
else
    fail "monolith or configured per-repo image contains a baked agent CLI, or the repo image lacks Node"
fi

echo "case: both agents install into isolated shared CLI volumes"
SMOKE_VOLUME_PREFIX="cenci-agent-cli-smoke-$$"
SMOKE_AGENT_VOLUMES=("${SMOKE_VOLUME_PREFIX}-claude" "${SMOKE_VOLUME_PREFIX}-codex")
REMAP_TMP=""
cleanup_smoke() {
    "${RUNTIME}" volume rm "${SMOKE_AGENT_VOLUMES[@]}" >/dev/null 2>&1 || true
    [[ -z "${REMAP_TMP}" ]] || rm -rf "${REMAP_TMP}"
}
trap cleanup_smoke EXIT
AGENT_INSTALL_OK=1
for agent in claude codex; do
    volume="${SMOKE_VOLUME_PREFIX}-${agent}"
    if ! run_with_timeout "${RUNTIME}" run --rm --user root --entrypoint /bin/bash \
        -v "${volume}:/opt/cenci-agent" cenci-sandbox:latest \
        /usr/local/bin/lib/agent-cli.sh update "${agent}" \
        || ! run_with_timeout "${RUNTIME}" run --rm --entrypoint /bin/bash \
        -v "${volume}:/opt/cenci-agent:ro" cenci-sandbox:latest -c \
        "test -x /opt/cenci-agent/current/node_modules/.bin/${agent} \
            && /opt/cenci-agent/current/node_modules/.bin/${agent} --version"; then
        AGENT_INSTALL_OK=0
    fi
done
if [[ "${AGENT_INSTALL_OK}" -eq 1 ]]; then
    pass
else
    fail "Claude Code or Codex did not install and execute from a shared volume"
fi

echo "case: two images see the same CLI and workload writes fail"
SHARED_VOLUME="${SMOKE_VOLUME_PREFIX}-codex"
MONOLITH_VERSION="$(run_with_timeout "${RUNTIME}" run --rm --entrypoint /bin/bash \
    -v "${SHARED_VOLUME}:/opt/cenci-agent:ro" cenci-sandbox:latest \
    -c 'cat /opt/cenci-agent/current/VERSION')"
REPO_VERSION="$(run_with_timeout "${RUNTIME}" run --rm --entrypoint /bin/bash \
    -v "${SHARED_VOLUME}:/opt/cenci-agent:ro" cenci-sandbox-smoke-repo:latest \
    -c 'cat /opt/cenci-agent/current/VERSION')"
if [[ -n "${MONOLITH_VERSION}" && "${MONOLITH_VERSION}" == "${REPO_VERSION}" ]] \
    && ! run_with_timeout "${RUNTIME}" run --rm --entrypoint /bin/bash \
        -v "${SHARED_VOLUME}:/opt/cenci-agent:ro" cenci-sandbox:latest \
        -c 'touch /opt/cenci-agent/current/tamper'; then
    pass
else
    fail "shared CLI differed between images or its workload mount was writable"
fi

echo "case: update atomically changes current and a failed update retains it"
BEFORE_TARGET="$(run_with_timeout "${RUNTIME}" run --rm --entrypoint /bin/bash \
    -v "${SHARED_VOLUME}:/opt/cenci-agent:ro" cenci-sandbox:latest \
    -c 'readlink /opt/cenci-agent/current')"
if run_with_timeout "${RUNTIME}" run --rm --user root --entrypoint /bin/bash \
    -v "${SHARED_VOLUME}:/opt/cenci-agent" cenci-sandbox:latest \
    /usr/local/bin/lib/agent-cli.sh update codex "${MONOLITH_VERSION}"; then
    AFTER_TARGET="$(run_with_timeout "${RUNTIME}" run --rm --entrypoint /bin/bash \
        -v "${SHARED_VOLUME}:/opt/cenci-agent:ro" cenci-sandbox:latest \
        -c 'readlink /opt/cenci-agent/current')"
    if [[ "${AFTER_TARGET}" != "${BEFORE_TARGET}" ]] \
        && ! run_with_timeout "${RUNTIME}" run --rm --user root --entrypoint /bin/bash \
            -v "${SHARED_VOLUME}:/opt/cenci-agent" cenci-sandbox:latest \
            /usr/local/bin/lib/agent-cli.sh update codex 999999.0.0 \
        && [[ "$(run_with_timeout "${RUNTIME}" run --rm --entrypoint /bin/bash \
            -v "${SHARED_VOLUME}:/opt/cenci-agent:ro" cenci-sandbox:latest \
            -c 'readlink /opt/cenci-agent/current')" == "${AFTER_TARGET}" ]]; then
        pass
    else
        fail "atomic update or failed-update rollback contract did not hold"
    fi
else
    fail "same-version atomic update failed"
fi

# Regression guard for the libicu74 FailFast bug: if it regresses,
# `dotnet --version` aborts and this whole chain short-circuits.
echo "case: dotnet/node/go/uv/python3 all work inside cenci-sandbox:latest"
if "${RUNTIME}" run --rm --entrypoint /bin/bash cenci-sandbox:latest -c \
    'dotnet --version && node -v && go version && uv --version && python3 --version'; then
    pass
else
    fail "one or more toolchain checks failed inside cenci-sandbox:latest"
fi

# Regression guard for the gap this fragment closes: Chromium and its OS-level
# deps (fonts, libnss3, etc.) must actually launch and render, not just have
# `playwright --version` report a binary is present. Runs as the image's
# default non-root `dev` user (no --user override) — the same user verify-ui
# runs as at runtime, and the configuration Chromium's own sandbox expects.
echo "case: Playwright launches Chromium and renders a screenshot inside cenci-sandbox:latest"
EXPECTED_PLAYWRIGHT_VERSION="$(sed -n 's/^ARG PLAYWRIGHT_VERSION=//p' "${SANDBOX_DIR}/fragments/playwright.dockerfile")"
# shellcheck disable=SC2016 # Expansion happens in the container, using the forwarded environment variable.
if [[ -z "${EXPECTED_PLAYWRIGHT_VERSION}" ]]; then
    fail "could not read the expected Playwright version from fragments/playwright.dockerfile"
elif "${RUNTIME}" run --rm --entrypoint /bin/bash \
    -e "EXPECTED_PLAYWRIGHT_VERSION=${EXPECTED_PLAYWRIGHT_VERSION}" \
    cenci-sandbox:latest -c \
    'test "$(playwright --version)" = "Version ${EXPECTED_PLAYWRIGHT_VERSION}" \
        && playwright screenshot --browser=chromium about:blank /tmp/playwright-smoke.png \
        && test -s /tmp/playwright-smoke.png'; then
    pass
else
    fail "Playwright CLI or Chromium is missing, unusable, or stale in cenci-sandbox:latest"
fi

# ── ccline (Claude Code status line) renders from a settings payload ──
# ccline reads Claude Code's statusline JSON on stdin; a static build that
# fails here (bad extraction, wrong arch) would leave every sandbox session
# with a blank status line while the container otherwise works.
echo "case: ccline renders a status line inside cenci-sandbox:latest"
if "${RUNTIME}" run --rm --entrypoint /bin/bash cenci-sandbox:latest -c \
    'echo "{\"model\":{\"id\":\"claude-test\",\"display_name\":\"Test\"},\"workspace\":{\"current_dir\":\"/workspace\"},\"transcript_path\":\"/dev/null\"}" | /usr/local/bin/ccline' >/dev/null; then
    pass
else
    fail "ccline failed to render inside cenci-sandbox:latest"
fi

# ── UID/GID remap via HOST_UID/HOST_GID (#154) ─────────────────────
# Runs the REAL entrypoint (no --entrypoint override) so the remap block
# actually executes: a HOST_UID/HOST_GID mismatch must usermod/groupmod
# `dev` and re-exec, landing the final `bash -c ...` process at the
# requested UID/GID instead of the image's baked-in 1000/1000 `dev`
# account. The in-container `id -u`/`id -g` output is the primary,
# runtime-agnostic assertion. The host-side ownership check on the probe
# file written into the per-repo `/workspace` mount is best-effort:
# rootless podman remaps container UID 1234 into a host subuid range, so
# the host-side `stat` won't literally show 1234 there — that assertion
# only runs (and only fails the suite) under RUNTIME=docker.
echo "case: entrypoint remaps dev to HOST_UID/HOST_GID for a per-repo mount"
REMAP_TMP="$(mktemp -d)"
SMOKE_NPM_FAKE="${REMAP_TMP}/npm"
cat >"${SMOKE_NPM_FAKE}" <<'EOF'
#!/bin/sh
case "$*" in *codex*) agent=codex ;; *) agent=claude ;; esac
mkdir -p "${NPM_CONFIG_PREFIX}/bin"
printf '#!/bin/sh\nexit 0\n' >"${NPM_CONFIG_PREFIX}/bin/${agent}"
chmod +x "${NPM_CONFIG_PREFIX}/bin/${agent}"
EOF
chmod +x "${SMOKE_NPM_FAKE}"
# The synthetic remap UID (1234) intentionally differs from the test runner's
# real UID, unlike production where HOST_UID owns the bind-mount root.
chmod 0777 "${REMAP_TMP}"
REMAP_OUT="$(run_with_timeout "${RUNTIME}" run --rm --user root \
    -e HOST_UID=1234 -e HOST_GID=1234 -e WORKSPACE_SCOPE=repo \
    -v "${REMAP_TMP}:/workspace" \
    -v "${SMOKE_NPM_FAKE}:/usr/bin/npm:ro" \
    cenci-sandbox:latest \
    -c 'id -u; id -g; : > /workspace/remap-probe')"
REMAP_STATUS=$?
if [[ "${REMAP_STATUS}" -eq 124 ]]; then
    fail "container run timed out after 60s while exercising the remap path — likely a hang/regression in the root->dev remap"
elif [[ "${REMAP_STATUS}" -eq 0 ]]; then
    REMAP_UID="$(printf '%s\n' "${REMAP_OUT}" | tail -n 2 | head -n 1)"
    REMAP_GID="$(printf '%s\n' "${REMAP_OUT}" | tail -n 1)"
    if [[ "${REMAP_UID}" == "1234" && "${REMAP_GID}" == "1234" ]]; then
        pass
    else
        fail "expected dev to report uid=1234/gid=1234 inside the container after remap, got: ${REMAP_OUT}"
    fi
else
    fail "container run failed while exercising the remap path (exit ${REMAP_STATUS}): ${REMAP_OUT}"
fi

echo "case: entrypoint reuses an existing image group matching HOST_GID"
GROUP_COLLISION_OUT="$(run_with_timeout "${RUNTIME}" run --rm --user root \
    -e HOST_UID=1234 -e HOST_GID=20 \
    -v "${SMOKE_NPM_FAKE}:/usr/bin/npm:ro" \
    cenci-sandbox:latest \
    -c 'id -u; id -g')"
GROUP_COLLISION_STATUS=$?
if [[ "${GROUP_COLLISION_STATUS}" -eq 124 ]]; then
    fail "container run timed out while exercising an existing HOST_GID"
elif [[ "${GROUP_COLLISION_STATUS}" -eq 0 ]]; then
    GROUP_COLLISION_UID="$(printf '%s\n' "${GROUP_COLLISION_OUT}" | tail -n 2 | head -n 1)"
    GROUP_COLLISION_GID="$(printf '%s\n' "${GROUP_COLLISION_OUT}" | tail -n 1)"
    if [[ "${GROUP_COLLISION_UID}" == "1234" && "${GROUP_COLLISION_GID}" == "20" ]]; then
        pass
    else
        fail "expected dev to reuse existing gid=20, got: ${GROUP_COLLISION_OUT}"
    fi
else
    fail "container run failed for existing HOST_GID (exit ${GROUP_COLLISION_STATUS}): ${GROUP_COLLISION_OUT}"
fi

echo "case: remap-probe file ownership on the host (best-effort, docker only)"
if [[ "${RUNTIME}" == "docker" && "$(uname -s)" != "Darwin" ]]; then
    if [[ -f "${REMAP_TMP}/remap-probe" ]]; then
        PROBE_UID="$("${STAT_CMD[@]}" '%u' "${REMAP_TMP}/remap-probe")"
        PROBE_GID="$("${STAT_CMD[@]}" '%g' "${REMAP_TMP}/remap-probe")"
        if [[ "${PROBE_UID}" == "1234" && "${PROBE_GID}" == "1234" ]]; then
            pass
        else
            fail "expected /workspace/remap-probe to be owned by 1234:1234 on the host under docker, got ${PROBE_UID}:${PROBE_GID}"
        fi
    else
        fail "remap-probe file was not created in the per-repo mount"
    fi
else
    echo "  SKIP: host-side ownership check under podman or Docker Desktop — UID mapping means" \
        "the host UID won't literally be 1234; the in-container id check above is authoritative."
fi

# ── No-op regression: unset HOST_UID/HOST_GID must not remap dev (#154) ──
# Same real-entrypoint path as above, but without HOST_UID/HOST_GID set, so
# the usermod/groupmod/chown remap itself must stay a no-op for the existing
# UID-1000 common case. The container still starts as root and unconditionally
# drops to dev before running the workload (an unavoidable extra re-exec now
# that every start begins as root — see entrypoint.sh) — dev keeps reporting
# uid=1000/gid=1000 either way.
echo "case: entrypoint leaves dev at uid=1000/gid=1000 when HOST_UID/HOST_GID are unset"
NOOP_OUT="$(run_with_timeout "${RUNTIME}" run --rm --user root \
    -v "${SMOKE_NPM_FAKE}:/usr/bin/npm:ro" \
    cenci-sandbox:latest -c 'id -u; id -g')"
NOOP_STATUS=$?
if [[ "${NOOP_STATUS}" -eq 124 ]]; then
    fail "container run timed out after 60s while exercising the no-HOST_UID no-op path — likely a hang/regression in the root->dev remap"
elif [[ "${NOOP_STATUS}" -eq 0 ]]; then
    NOOP_UID="$(printf '%s\n' "${NOOP_OUT}" | tail -n 2 | head -n 1)"
    NOOP_GID="$(printf '%s\n' "${NOOP_OUT}" | tail -n 1)"
    if [[ "${NOOP_UID}" == "1000" && "${NOOP_GID}" == "1000" ]]; then
        pass
    else
        fail "expected dev to remain uid=1000/gid=1000 when HOST_UID/HOST_GID are unset, got: ${NOOP_OUT}"
    fi
else
    fail "container run failed while exercising the no-HOST_UID no-op path (exit ${NOOP_STATUS}): ${NOOP_OUT}"
fi

# ── Summary ────────────────────────────────────────────────────────
print_summary
[[ "${FAILURES}" -eq 0 ]]
