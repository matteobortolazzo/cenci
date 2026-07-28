#!/usr/bin/env bash
# Content assertions for sandbox/skills/setup/SKILL.md (#777): guards three
# accuracy/consistency gaps found in the milestone-1 gap audit —
#   1. the image-name description for `cenci sandbox build` must match the
#      real per-repo-vs-monolith selection in watch/sandbox_cmd.go:92-98 (via
#      watch/internal/sandbox/launcher/scope.go's SelectImage): the shared
#      monolith `cenci-sandbox:latest` when the repo has no
#      `.cenci/Dockerfile`, the per-repo `cenci-sandbox-<slug>:latest` when
#      it does;
#   2. no drifted copy of the one-token shortcut table (`cn xt`, `cn
#      ch`/`cs`/`co`/`cf`) may remain — its single documented home is
#      watch/README.md per docs/cli-conventions.md's "exactly one docs home"
#      rule; long-form `cenci open` examples plus a link to watch/README.md
#      replace it;
#   3. the build/launch-failure branch must point at `cenci diagnose` and the
#      failure atlas / error-codes docs shipped in milestone 1 (#478 epic).
#
# Runs on the host — plain grep/awk, no container needed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SKILL="${SANDBOX_DIR}/skills/setup/SKILL.md"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

echo "setup-skill-content.test.sh"

if [[ ! -f "${SKILL}" ]]; then
    fail "SKILL.md not found at ${SKILL}"
    print_summary
    exit 1
fi

# assert_contains <haystack> <needle> <description>
assert_contains() {
    local haystack="$1" needle="$2" desc="$3"
    if [[ "${haystack}" == *"${needle}"* ]]; then
        pass
    else
        fail "${desc}: expected to find $(printf '%q' "${needle}")"
    fi
}

# assert_not_contains <haystack> <needle> <description>
assert_not_contains() {
    local haystack="$1" needle="$2" desc="$3"
    if [[ "${haystack}" != *"${needle}"* ]]; then
        pass
    else
        fail "${desc}: did not expect to find $(printf '%q' "${needle}")"
    fi
}

# assert_matches <haystack> <glob-pattern> <description>
# Like assert_contains, but for checks that need a glob wildcard between two
# literal fragments (e.g. "cenci-sandbox-" ... ":latest" with a slug between
# them) rather than one literal substring.
assert_matches() {
    local haystack="$1" pattern="$2" desc="$3"
    # shellcheck disable=SC2053 # intentional unquoted pattern for glob match against ${pattern}
    if [[ "${haystack}" == ${pattern} ]]; then
        pass
    else
        fail "${desc}: expected to match pattern $(printf '%q' "${pattern}")"
    fi
}

SKILL_CONTENT="$(cat "${SKILL}")"

# -- (1) image-name description matches watch/sandbox_cmd.go:92-98 /
#    launcher/scope.go's SelectImage: monolith cenci-sandbox:latest without a
#    .cenci/Dockerfile, per-repo cenci-sandbox-<slug>:latest with one. Checked
#    against the whole doc, not one heading section, since the ticket's fix
#    anchor (~line 19) sits in the intro, not the "### Step 2" heading. -------

echo "case: the image-name description names the .cenci/Dockerfile condition that selects the image"
assert_contains "${SKILL_CONTENT}" ".cenci/Dockerfile" \
    "the .cenci/Dockerfile condition gating per-repo vs. monolith image selection"

echo "case: the image-name description covers the monolith (no .cenci/Dockerfile) case: cenci-sandbox:latest"
assert_contains "${SKILL_CONTENT}" "cenci-sandbox:latest" \
    "the monolith image name cenci-sandbox:latest"

echo "case: the image-name description covers the per-repo (.cenci/Dockerfile present) case: cenci-sandbox-<slug>:latest"
assert_contains "${SKILL_CONTENT}" "cenci-sandbox-" \
    "the per-repo image name prefix cenci-sandbox-<slug>:latest"

echo "case: the image-name description also covers the per-repo cenci-sandbox-<slug>:latest pattern"
assert_matches "${SKILL_CONTENT}" "*cenci-sandbox-*:latest*" \
    "the per-repo image pattern cenci-sandbox-<slug>:latest"

# -- (2) no drifted copy of the shortcut token table; long-form examples +
#    a link to the single documented home (watch/README.md) instead. -------

echo "case: no drifted shortcut-table token 'cn xt' remains"
assert_not_contains "${SKILL_CONTENT}" "cn xt" "drifted shortcut-table token 'cn xt'"

echo "case: no drifted shortcut-table token 'cn ch' remains"
assert_not_contains "${SKILL_CONTENT}" "cn ch" "drifted shortcut-table token 'cn ch'"

echo "case: no drifted shortcut-table row 'cn ch\`/\`cs\`/\`co\`/\`cf\`' remains"
assert_not_contains "${SKILL_CONTENT}" "cn ch\`/\`cs\`/\`co\`/\`cf\`" \
    "drifted shortcut-table row 'cn ch\`/\`cs\`/\`co\`/\`cf\`'"

echo "case: long-form 'cenci open --agent codex' example is present"
assert_contains "${SKILL_CONTENT}" "cenci open --agent codex" \
    "the long-form cenci open --agent codex example"

echo "case: long-form 'cenci open --model <model>' example is present"
assert_contains "${SKILL_CONTENT}" "cenci open --model" \
    "the long-form cenci open --model <model> example"

echo "case: a link to watch/README.md (the shortcut table's single documented home) is present"
assert_contains "${SKILL_CONTENT}" "watch/README.md" \
    "a link to watch/README.md"

# -- (3) the build/launch-failure branch points at cenci diagnose + the
#    failure atlas / error-codes docs. --------------------------------------

echo "case: the build-failure branch mentions cenci diagnose"
FAILURE_LINE="$(grep -F 'On failure' "${SKILL}" || true)"
if [[ -n "${FAILURE_LINE}" ]]; then
    assert_contains "${FAILURE_LINE}" "cenci diagnose" \
        "the build-failure branch pointing at cenci diagnose"
else
    fail "could not locate the 'On failure' build-failure branch line in ${SKILL}"
fi

echo "case: the setup skill points at the failure atlas doc"
assert_contains "${SKILL_CONTENT}" "docs/failure-atlas.md" \
    "a pointer to docs/failure-atlas.md"

echo "case: the setup skill points at the error-codes doc"
assert_contains "${SKILL_CONTENT}" "docs/error-codes.md" \
    "a pointer to docs/error-codes.md"

echo "case: the build-failure branch warns that cenci diagnose output may contain secrets"
assert_contains "${FAILURE_LINE}" "may include secrets" \
    "the build-failure branch's redaction caveat about cenci diagnose output"

print_summary
[[ "${FAILURES}" -eq 0 ]]
