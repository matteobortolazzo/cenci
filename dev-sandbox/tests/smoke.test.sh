#!/usr/bin/env bash
# Runtime smoke test for the split agent-sandbox-base + fragments image
# layout (ticket #107).
#
# Builds agent-sandbox-base:<ver> from Dockerfile.base, then builds the
# monolithic agent-sandbox:latest FROM that base image, then runs the
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
SANDBOX_DIR="${REPO_ROOT}/dev-sandbox"

FAILURES=0
PASSES=0

# fail <message>
fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# summarize_and_exit <exit-code>: prints the pass/fail summary and exits.
# Used for the fatal build-failure paths below, where there's nothing left
# worth testing once a build step has failed.
summarize_and_exit() {
    echo
    echo "passed: ${PASSES}, failed: ${FAILURES}"
    exit "$1"
}

echo "smoke.test.sh"

# ── Auto-detect container runtime (self-skip if none available) ──
# Mirrors the detection order used by dev-sandbox/agent-sand.
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
# RELATIVE (subshell cd) so the digest is identical here and in agent-sand
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
echo "case: build agent-sandbox-base:${BASE_TAG} from Dockerfile.base"
if "${RUNTIME}" build -f "${SANDBOX_DIR}/Dockerfile.base" -t "agent-sandbox-base:${BASE_TAG}" "${SANDBOX_DIR}"; then
    pass
else
    fail "failed to build agent-sandbox-base:${BASE_TAG} from Dockerfile.base"
    summarize_and_exit 1
fi

# ── Build the monolith FROM the base image ────────────────────────
echo "case: build agent-sandbox:latest (--build-arg BASE_VERSION=${BASE_TAG})"
if "${RUNTIME}" build --build-arg "BASE_VERSION=${BASE_TAG}" -t agent-sandbox:latest -f "${SANDBOX_DIR}/Dockerfile" "${SANDBOX_DIR}"; then
    pass
else
    fail "failed to build agent-sandbox:latest FROM agent-sandbox-base:${BASE_TAG}"
    summarize_and_exit 1
fi

# ── Run the built image and assert every toolchain works ──────────
# A generated per-repo image replaces the monolith whenever the repository
# contains .agent-sand/Dockerfile, so it must carry the Codex agent runtime.
echo "case: configured per-repo image includes a working Codex CLI"
EXPECTED_CODEX_VERSION="$(sed -n 's/^ARG CODEX_VERSION=//p' "${SANDBOX_DIR}/fragments/codex.dockerfile")"
# shellcheck disable=SC2016 # Expansion happens in the container, using the forwarded environment variable.
if [[ -z "${EXPECTED_CODEX_VERSION}" ]]; then
    fail "could not read the expected Codex version from fragments/codex.dockerfile"
elif "${RUNTIME}" build --build-arg "BASE_VERSION=${BASE_TAG}" \
    -t agent-sandbox-smoke-repo:latest \
    -f "${REPO_ROOT}/.agent-sand/Dockerfile" "${REPO_ROOT}/.agent-sand" \
    && "${RUNTIME}" run --rm --entrypoint /bin/bash \
        -e "EXPECTED_CODEX_VERSION=${EXPECTED_CODEX_VERSION}" \
        agent-sandbox-smoke-repo:latest -c \
        'command -v codex && test "$(codex --version)" = "codex-cli ${EXPECTED_CODEX_VERSION}"'; then
    pass
else
    fail "Codex CLI is missing, unusable, or stale in the configured per-repo image"
fi

# Regression guard for the libicu74 FailFast bug: if it regresses,
# `dotnet --version` aborts and this whole chain short-circuits.
echo "case: dotnet/node/go/uv/python3 all work inside agent-sandbox:latest"
if "${RUNTIME}" run --rm --entrypoint /bin/bash agent-sandbox:latest -c \
    'dotnet --version && node -v && go version && uv --version && python3 --version'; then
    pass
else
    fail "one or more toolchain checks failed inside agent-sandbox:latest"
fi

# ── ccline (Claude Code status line) renders from a settings payload ──
# ccline reads Claude Code's statusline JSON on stdin; a static build that
# fails here (bad extraction, wrong arch) would leave every sandbox session
# with a blank status line while the container otherwise works.
echo "case: ccline renders a status line inside agent-sandbox:latest"
if "${RUNTIME}" run --rm --entrypoint /bin/bash agent-sandbox:latest -c \
    'echo "{\"model\":{\"id\":\"claude-test\",\"display_name\":\"Test\"},\"workspace\":{\"current_dir\":\"/workspace\"},\"transcript_path\":\"/dev/null\"}" | /usr/local/bin/ccline' >/dev/null; then
    pass
else
    fail "ccline failed to render inside agent-sandbox:latest"
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
# The synthetic remap UID (1234) intentionally differs from the test runner's
# real UID, unlike production where HOST_UID owns the bind-mount root.
chmod 0777 "${REMAP_TMP}"
trap 'rm -rf "${REMAP_TMP}"' EXIT
REMAP_OUT="$(timeout 60 "${RUNTIME}" run --rm --user root \
    -e HOST_UID=1234 -e HOST_GID=1234 -e WORKSPACE_SCOPE=repo \
    -v "${REMAP_TMP}:/workspace" \
    agent-sandbox:latest \
    -c 'id -u; id -g; : > /workspace/remap-probe')"
REMAP_STATUS=$?
if [[ "${REMAP_STATUS}" -eq 124 ]]; then
    fail "container run timed out after 60s while exercising the remap path — likely a hang/regression in the root->dev remap"
elif [[ "${REMAP_STATUS}" -eq 0 ]]; then
    readarray -t REMAP_LINES <<<"${REMAP_OUT}"
    if [[ "${REMAP_LINES[0]:-}" == "1234" && "${REMAP_LINES[1]:-}" == "1234" ]]; then
        pass
    else
        fail "expected dev to report uid=1234/gid=1234 inside the container after remap, got: ${REMAP_OUT}"
    fi
else
    fail "container run failed while exercising the remap path (exit ${REMAP_STATUS}): ${REMAP_OUT}"
fi

echo "case: entrypoint reuses an existing image group matching HOST_GID"
GROUP_COLLISION_OUT="$(timeout 60 "${RUNTIME}" run --rm --user root \
    -e HOST_UID=1234 -e HOST_GID=20 \
    agent-sandbox:latest \
    -c 'id -u; id -g')"
GROUP_COLLISION_STATUS=$?
if [[ "${GROUP_COLLISION_STATUS}" -eq 124 ]]; then
    fail "container run timed out while exercising an existing HOST_GID"
elif [[ "${GROUP_COLLISION_STATUS}" -eq 0 ]]; then
    readarray -t GROUP_COLLISION_LINES <<<"${GROUP_COLLISION_OUT}"
    if [[ "${GROUP_COLLISION_LINES[0]:-}" == "1234" && "${GROUP_COLLISION_LINES[1]:-}" == "20" ]]; then
        pass
    else
        fail "expected dev to reuse existing gid=20, got: ${GROUP_COLLISION_OUT}"
    fi
else
    fail "container run failed for existing HOST_GID (exit ${GROUP_COLLISION_STATUS}): ${GROUP_COLLISION_OUT}"
fi

echo "case: remap-probe file ownership on the host (best-effort, docker only)"
if [[ "${RUNTIME}" == "docker" ]]; then
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
    echo "  SKIP: host-side ownership check under podman — rootless subuid mapping means the" \
        "host UID won't literally be 1234; the in-container id check above is authoritative."
fi

# ── No-op regression: unset HOST_UID/HOST_GID must not remap dev (#154) ──
# Same real-entrypoint path as above, but without HOST_UID/HOST_GID set, so
# the usermod/groupmod/chown remap itself must stay a no-op for the existing
# UID-1000 common case. The container still starts as root and unconditionally
# drops to dev before running the workload (an unavoidable extra re-exec now
# that every start begins as root — see entrypoint.sh) — dev keeps reporting
# uid=1000/gid=1000 either way.
echo "case: entrypoint leaves dev at uid=1000/gid=1000 when HOST_UID/HOST_GID are unset"
NOOP_OUT="$(timeout 60 "${RUNTIME}" run --rm --user root agent-sandbox:latest -c 'id -u; id -g')"
NOOP_STATUS=$?
if [[ "${NOOP_STATUS}" -eq 124 ]]; then
    fail "container run timed out after 60s while exercising the no-HOST_UID no-op path — likely a hang/regression in the root->dev remap"
elif [[ "${NOOP_STATUS}" -eq 0 ]]; then
    readarray -t NOOP_LINES <<<"${NOOP_OUT}"
    if [[ "${NOOP_LINES[0]:-}" == "1000" && "${NOOP_LINES[1]:-}" == "1000" ]]; then
        pass
    else
        fail "expected dev to remain uid=1000/gid=1000 when HOST_UID/HOST_GID are unset, got: ${NOOP_OUT}"
    fi
else
    fail "container run failed while exercising the no-HOST_UID no-op path (exit ${NOOP_STATUS}): ${NOOP_OUT}"
fi

# ── Summary ────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
