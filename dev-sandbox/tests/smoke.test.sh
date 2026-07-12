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

# ── Summary ────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
