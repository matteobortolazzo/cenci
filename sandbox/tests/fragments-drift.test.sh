#!/usr/bin/env bash
# Guards the byte-identity invariant documented in sandbox/AGENTS.md:
# every fragments/*.dockerfile block must appear verbatim inside Dockerfile
# (the monolith), including the Codex runtime required by per-repo images.
#
# Two fragment classes exist, and this suite distinguishes them:
#   * monolith-backed — the fragment has a corresponding block in Dockerfile
#     and must match it byte-for-byte (dotnet, node, playwright, go, docker).
#   * monolith-less — the fragment is config- or stack-selected only and this
#     repo's own stack does not use it, so Dockerfile deliberately carries no
#     such block (see MONOLITH_LESS below). These are asserted to be absent
#     from Dockerfile, so silently adding a block without also adding it to
#     this list is caught too.
#
# Matching is a true multi-line fixed-string containment via bash's own
# `[[ haystack == *needle* ]]`. It deliberately does NOT use
# `grep -zqF "${content}"`: with a multi-line pattern, `grep -F` treats each
# pattern LINE as a separate alternative, so a fragment "matched" as soon as
# any single one of its lines (e.g. a bare `USER root`) appeared anywhere in
# the monolith. Under that bug this suite reported 7/7 passing while three
# fragments had no monolith block at all — the invariant it exists to guard
# was never actually enforced (#831).
#
# Runs on the host — no Docker required, pure bash string comparison.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCKERFILE="${SANDBOX_DIR}/Dockerfile"

# Fragments that intentionally have no block in the monolith Dockerfile,
# because this repo's own stack does not use them. Kept in sync with the
# "Image architecture: base + fragments" section of sandbox/AGENTS.md.
MONOLITH_LESS=(
    python.dockerfile
    rust.dockerfile
    azure.dockerfile
)

FAILURES=0
PASSES=0

fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }

is_monolith_less() {
    local candidate="$1" entry
    for entry in "${MONOLITH_LESS[@]}"; do
        [[ "${entry}" == "${candidate}" ]] && return 0
    done
    return 1
}

echo "fragments-drift.test.sh"

if ! DOCKERFILE_CONTENT="$(cat "${DOCKERFILE}")"; then
    fail "could not read ${DOCKERFILE}"
    echo
    echo "passed: ${PASSES}, failed: ${FAILURES}"
    exit 1
fi

shopt -s nullglob
FRAGMENTS=("${SANDBOX_DIR}"/fragments/*.dockerfile)
shopt -u nullglob

if [[ "${#FRAGMENTS[@]}" -eq 0 ]]; then
    fail "no fragments found under ${SANDBOX_DIR}/fragments — expected at least one"
fi

for fragment in "${FRAGMENTS[@]}"; do
    name="$(basename "${fragment}")"
    if ! content="$(cat "${fragment}")"; then
        echo "case: ${name} is readable"
        fail "${name} could not be read"
        continue
    fi
    if [[ -z "${content}" ]]; then
        echo "case: ${name} is non-empty"
        fail "${name} is empty"
        continue
    fi

    if is_monolith_less "${name}"; then
        echo "case: ${name} is deliberately absent from Dockerfile (monolith-less fragment)"
        if [[ "${DOCKERFILE_CONTENT}" == *"${content}"* ]]; then
            fail "${name} is listed as monolith-less but its content IS present in ${DOCKERFILE} — remove it from MONOLITH_LESS so drift is enforced, and update sandbox/AGENTS.md"
        else
            pass
        fi
        continue
    fi

    echo "case: ${name} is byte-identical to its block in Dockerfile"
    if [[ "${DOCKERFILE_CONTENT}" == *"${content}"* ]]; then
        pass
    else
        fail "${name} is not verbatim inside ${DOCKERFILE} — hand-duplicate the edit (see sandbox/AGENTS.md)"
    fi
done

# The matcher itself must reject a near-miss, not just accept exact copies.
# Without this, a regression back to the line-wise `grep -F` behaviour would
# leave every case above passing and go unnoticed.
echo "case: the matcher rejects a fragment whose content is only partially present"
PROBE=$'USER root\n\n# ── Not A Real Block ─────────────────────────────────────────────\nRUN false-command-that-is-not-in-the-monolith\n\nUSER dev'
if [[ "${DOCKERFILE_CONTENT}" == *"${PROBE}"* ]]; then
    fail "the containment check matched a probe block that is not in ${DOCKERFILE} — the byte-identity invariant is not being enforced"
else
    pass
fi

# Banner-anchor invariant (#1048): watch/internal/sandbox/launcher's
# fragment-drift detector identifies a marker-less repo block's selected
# fragments by scanning for each installed fragment's `# ── <title> ───…`
# banner line (the legacy fallback that keeps working on an already-committed
# block with no per-fragment markers, e.g. velka's). That anchor is only
# trustworthy if every fragment carries exactly one such banner line, and no
# two fragments share one — otherwise "banner present" cannot map back to a
# single fragment unambiguously. This is an invariant check on the fragments
# that exist today, not new functionality, so it is expected to pass as-is;
# a failure here is a real pre-existing violation, not something to silently
# work around.
declare -A BANNER_OWNER=()
for fragment in "${FRAGMENTS[@]}"; do
    name="$(basename "${fragment}")"
    if ! content="$(cat "${fragment}")"; then
        continue
    fi

    banner_lines="$(grep -c '^# ── ' <<<"${content}")"
    echo "case: ${name} has exactly one # ── banner line"
    if [[ "${banner_lines}" -eq 1 ]]; then
        pass
    else
        fail "${name} has ${banner_lines} '# ── ' banner line(s), want exactly 1 — the fragment-drift detector's legacy banner anchor requires exactly one per fragment"
        continue
    fi

    banner_line="$(grep '^# ── ' <<<"${content}")"
    echo "case: ${name}'s banner line is unique across fragments"
    if [[ -n "${BANNER_OWNER[${banner_line}]+set}" ]]; then
        fail "${name}'s banner line [${banner_line}] is not unique — also used by ${BANNER_OWNER[${banner_line}]}; the fragment-drift detector's banner anchor cannot distinguish the two fragments"
    else
        BANNER_OWNER["${banner_line}"]="${name}"
        pass
    fi
done

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
