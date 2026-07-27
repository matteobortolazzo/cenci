#!/usr/bin/env bash
# Tests for bump-plugin-version.sh (ticket #743). Follows the repo's
# .github/scripts/*.test.sh precedent (resolve-release-version.test.sh,
# check-version-bump-concurrency.test.sh): plain bash, no framework, PASS/FAIL
# counters, non-zero exit on any failure.
#
# These are behavioral tests, not shape assertions: every case builds a real
# bare "origin" repo plus a real working clone under one mktemp root, runs the
# script against them, and asserts on the resulting git history, the pushed
# manifest contents, and the recorded `gh` invocations. `gh` is stubbed by a
# shim on PATH that appends each invocation to $GH_LOG and exits 0, so tag
# creation and downstream dispatch are observable without any network access.
#
# bump-plugin-version.sh's contract (see #743):
#   - Reads PLUGIN_NAME, PLUGIN_JSON_PATHS, MARKETPLACE_NAME, TAG_PREFIX,
#     DISPATCH_WORKFLOW, ORIGINAL_SHA, GITHUB_REPOSITORY and the optional
#     PUSH_RETRIES from the environment only.
#   - Skips entirely when ORIGINAL_SHA's subject is already this plugin's
#     release commit.
#   - Classifies the bump (major/minor/patch) from ORIGINAL_SHA's commit
#     message, never from whatever else has landed on main since.
#   - Refreshes to the live tip of origin/main and re-derives the whole bump
#     from that tip on EVERY attempt, so a push rejected by a concurrent
#     update is retried against the new tip rather than replaying a stale
#     diff. This is the #743 fix: it replaces the lossy shared concurrency
#     queue, which silently dropped a third simultaneous run.
#   - Fails loudly (non-zero) when the marketplace entry is missing or when
#     the retry budget is exhausted — never silently.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUMP_SH="${SCRIPT_DIR}/bump-plugin-version.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

if [ ! -f "$BUMP_SH" ]; then
    echo "FAIL: bump-plugin-version.sh not found at ${BUMP_SH}" >&2
    exit 1
fi

for tool in git jq; do
    command -v "$tool" >/dev/null 2>&1 || {
        echo "FAIL: ${tool} is required but was not found on PATH" >&2
        exit 1
    }
done

TEST_ROOT="$(mktemp -d /var/tmp/bump-plugin-version-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# A `gh` shim shared by every case: records each invocation (one line per
# call, arguments space-joined) into $GH_LOG and succeeds. Placed first on
# PATH only for the script invocation itself, so it never leaks anywhere else.
GH_SHIM_DIR="${TEST_ROOT}/bin"
mkdir -p "${GH_SHIM_DIR}"
cat > "${GH_SHIM_DIR}/gh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${GH_LOG}"
exit 0
EOF
chmod +x "${GH_SHIM_DIR}/gh"

# make_fixture <case_name> [source_version] — builds
#   <TEST_ROOT>/<case_name>/origin.git  (bare remote)
#   <TEST_ROOT>/<case_name>/work        (clone, checked out on main)
# The working tree carries two stamped manifests plus a marketplace.json
# holding this plugin's entry AND an unrelated second entry. The second entry
# is what the concurrent-push cases mutate, so a naive stale-diff replay would
# clobber it visibly.
# Sets CASE_DIR, ORIGIN_DIR, WORK_DIR.
make_fixture() {
    local case_name="$1" version="${2:-1.2.3}"
    CASE_DIR="${TEST_ROOT}/${case_name}"
    ORIGIN_DIR="${CASE_DIR}/origin.git"
    WORK_DIR="${CASE_DIR}/work"

    mkdir -p "${CASE_DIR}"
    git init --bare --quiet --initial-branch=main "${ORIGIN_DIR}"

    git init --quiet --initial-branch=main "${WORK_DIR}"
    git -C "${WORK_DIR}" config user.email "test@example.com"
    git -C "${WORK_DIR}" config user.name "test"

    mkdir -p "${WORK_DIR}/plugin/.claude-plugin" \
             "${WORK_DIR}/plugin/.codex-plugin" \
             "${WORK_DIR}/.claude-plugin"
    printf '{\n  "version": "%s"\n}\n' "${version}" \
        > "${WORK_DIR}/plugin/.claude-plugin/plugin.json"
    printf '{\n  "version": "%s"\n}\n' "${version}" \
        > "${WORK_DIR}/plugin/.codex-plugin/plugin.json"
    cat > "${WORK_DIR}/.claude-plugin/marketplace.json" <<EOF
{
  "plugins": [
    { "name": "cenci-fixture", "version": "${version}" },
    { "name": "other-plugin", "version": "9.9.9" }
  ]
}
EOF

    git -C "${WORK_DIR}" add -A
    git -C "${WORK_DIR}" commit --quiet -m "chore: fixture base"
    git -C "${WORK_DIR}" remote add origin "${ORIGIN_DIR}"
    git -C "${WORK_DIR}" push --quiet -u origin main
}

# set_trigger_commit <work_dir> <subject> — appends an empty commit carrying
# <subject> and pushes it, then echoes its SHA. That SHA is what each case
# passes as ORIGINAL_SHA (the commit that "triggered" the run), which is what
# the skip guard and the bump-type classification must both read.
set_trigger_commit() {
    local work_dir="$1" subject="$2"
    git -C "${work_dir}" commit --quiet --allow-empty -m "${subject}"
    git -C "${work_dir}" push --quiet origin main
    git -C "${work_dir}" rev-parse HEAD
}

# install_competing_push_hook <work_dir> <origin_dir> <case_dir> <times> —
# installs a pre-push hook that, for its first <times> invocations, lands an
# unrelated commit on origin/main before letting the push proceed. The push
# then loses the fast-forward check server-side, which is exactly the race the
# retry loop must survive: another plugin's bump landing between our refresh
# and our push. Each competing commit bumps the unrelated "other-plugin"
# marketplace entry, so a stale-diff replay would silently revert it.
install_competing_push_hook() {
    local work_dir="$1" origin_dir="$2" case_dir="$3" times="$4"
    local hook="${work_dir}/.git/hooks/pre-push"
    mkdir -p "${work_dir}/.git/hooks"
    cat > "${hook}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
# Git exports GIT_DIR (and friends) into hook processes. Left set, they would
# make the rival clone's git commands operate on the OUTER repository instead
# of the clone. Clear them so the rival is a genuinely independent pusher.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX \\
      GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_QUARANTINE_PATH

COUNT_FILE="${case_dir}/competing-pushes"
COUNT=\$(cat "\${COUNT_FILE}" 2>/dev/null || echo 0)
if [ "\${COUNT}" -ge ${times} ]; then
  exit 0
fi
echo \$((COUNT + 1)) > "\${COUNT_FILE}"

RIVAL_VERSION="9.9.\$((COUNT + 10))"
RIVAL="${case_dir}/rival-\${COUNT}"
git clone --quiet "${origin_dir}" "\${RIVAL}"
git -C "\${RIVAL}" config user.email "rival@example.com"
git -C "\${RIVAL}" config user.name "rival"
jq --arg v "\${RIVAL_VERSION}" \\
  '(.plugins[] | select(.name == "other-plugin")).version = \$v' \\
  "\${RIVAL}/.claude-plugin/marketplace.json" > "\${RIVAL}/tmp.json"
mv "\${RIVAL}/tmp.json" "\${RIVAL}/.claude-plugin/marketplace.json"
git -C "\${RIVAL}" add -A
git -C "\${RIVAL}" commit --quiet -m "chore(release): other-plugin/v\${RIVAL_VERSION}"
git -C "\${RIVAL}" push --quiet origin main
exit 0
EOF
    chmod +x "${hook}"
}

# run_bump <work_dir> <original_sha> [VAR=VALUE ...] — runs the script with
# cwd set to <work_dir> and the gh shim first on PATH. Extra VAR=VALUE
# arguments override or add environment entries for this invocation only.
# Sets BUMP_EXIT, BUMP_OUTPUT and GH_LOG_FILE.
run_bump() {
    local work_dir="$1" original_sha="$2"
    shift 2
    GH_LOG_FILE="${work_dir}/../gh.log"
    : > "${GH_LOG_FILE}"
    BUMP_OUTPUT="$(
        cd "${work_dir}" &&
        env PATH="${GH_SHIM_DIR}:${PATH}" \
            GH_LOG="${GH_LOG_FILE}" \
            GH_TOKEN="fake-token" \
            GITHUB_REPOSITORY="matteobortolazzo/cenci" \
            PLUGIN_NAME="cenci-fixture" \
            PLUGIN_JSON_PATHS="plugin/.claude-plugin/plugin.json plugin/.codex-plugin/plugin.json" \
            MARKETPLACE_NAME="cenci-fixture" \
            TAG_PREFIX="fixture" \
            DISPATCH_WORKFLOW="" \
            ORIGINAL_SHA="${original_sha}" \
            "$@" \
            bash "${BUMP_SH}" 2>&1
    )"
    BUMP_EXIT=$?
}

# origin_file <origin_dir> <path> — prints <path>'s content as it exists on
# origin/main (the pushed result), never the local working tree.
origin_file() {
    git -C "$1" show "main:$2"
}

assert_eq() {
    local label="$1" actual="$2" expected="$3"
    if [[ "${actual}" == "${expected}" ]]; then
        pass
    else
        fail "${label}: got '${actual}', expected '${expected}'"
    fi
}

assert_contains() {
    local label="$1" haystack="$2" needle="$3"
    if [[ "${haystack}" == *"${needle}"* ]]; then
        pass
    else
        fail "${label}: '${needle}' not found in: ${haystack}"
    fi
}

assert_not_contains() {
    local label="$1" haystack="$2" needle="$3"
    if [[ "${haystack}" != *"${needle}"* ]]; then
        pass
    else
        fail "${label}: '${needle}' unexpectedly present in: ${haystack}"
    fi
}

echo "bump-plugin-version.test.sh"

# ── Case 1: patch bump — the default classification ───────────────────
echo "case: a plain fix commit patch-bumps every manifest and the marketplace entry"
make_fixture "case1-patch"
C1_SHA="$(set_trigger_commit "${WORK_DIR}" "fix(fixture): correct a thing")"
run_bump "${WORK_DIR}" "${C1_SHA}"
assert_eq "case1 exit status" "${BUMP_EXIT}" 0
assert_eq "case1 source manifest version" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.claude-plugin/plugin.json" | jq -r .version)" "1.2.4"
assert_eq "case1 codex manifest version" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.codex-plugin/plugin.json" | jq -r .version)" "1.2.4"
assert_eq "case1 marketplace entry version" \
    "$(origin_file "${ORIGIN_DIR}" ".claude-plugin/marketplace.json" \
        | jq -r '.plugins[] | select(.name == "cenci-fixture") | .version')" "1.2.4"
assert_eq "case1 unrelated marketplace entry untouched" \
    "$(origin_file "${ORIGIN_DIR}" ".claude-plugin/marketplace.json" \
        | jq -r '.plugins[] | select(.name == "other-plugin") | .version')" "9.9.9"
assert_eq "case1 release commit subject" \
    "$(git -C "${ORIGIN_DIR}" log -1 --pretty=format:%s main)" \
    "chore(release): cenci-fixture/v1.2.4"
assert_contains "case1 creates the tag via gh api" "$(cat "${GH_LOG_FILE}")" \
    "refs/tags/fixture/v1.2.4"

# ── Case 2: feat commit → minor bump ──────────────────────────────────
echo "case: a feat commit minor-bumps and resets the patch component"
make_fixture "case2-minor"
C2_SHA="$(set_trigger_commit "${WORK_DIR}" "feat(fixture): add a thing")"
run_bump "${WORK_DIR}" "${C2_SHA}"
assert_eq "case2 exit status" "${BUMP_EXIT}" 0
assert_eq "case2 minor bump with patch reset" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.claude-plugin/plugin.json" | jq -r .version)" "1.3.0"

# ── Case 3: breaking change → major bump ──────────────────────────────
echo "case: a breaking-change commit major-bumps and resets minor and patch"
make_fixture "case3-major"
C3_SHA="$(set_trigger_commit "${WORK_DIR}" "feat(fixture)!: replace a thing")"
run_bump "${WORK_DIR}" "${C3_SHA}"
assert_eq "case3 exit status" "${BUMP_EXIT}" 0
assert_eq "case3 major bump with minor and patch reset" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.claude-plugin/plugin.json" | jq -r .version)" "2.0.0"

# ── Case 4: skip guard — trigger commit is already a release commit ────
echo "case: a trigger commit that is already this plugin's release commit is skipped"
make_fixture "case4-skip"
C4_SHA="$(set_trigger_commit "${WORK_DIR}" "chore(release): cenci-fixture/v1.2.3")"
C4_TIP_BEFORE="$(git -C "${ORIGIN_DIR}" rev-parse main)"
run_bump "${WORK_DIR}" "${C4_SHA}"
assert_eq "case4 exit status" "${BUMP_EXIT}" 0
assert_eq "case4 origin main is untouched" \
    "$(git -C "${ORIGIN_DIR}" rev-parse main)" "${C4_TIP_BEFORE}"
assert_eq "case4 version is unchanged" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.claude-plugin/plugin.json" | jq -r .version)" "1.2.3"
assert_eq "case4 no gh calls were made" "$(wc -l < "${GH_LOG_FILE}" | tr -d ' ')" "0"

# ── Case 5: another plugin's release commit must NOT skip this plugin ──
echo "case: another plugin's release commit does not trip this plugin's skip guard"
make_fixture "case5-other-release"
C5_SHA="$(set_trigger_commit "${WORK_DIR}" "chore(release): other-plugin/v9.9.9")"
run_bump "${WORK_DIR}" "${C5_SHA}"
assert_eq "case5 exit status" "${BUMP_EXIT}" 0
assert_eq "case5 this plugin still bumped" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.claude-plugin/plugin.json" | jq -r .version)" "1.2.4"

# ── Case 6: missing marketplace entry fails loudly and pushes nothing ──
echo "case: a marketplace-name matching no entry fails loudly without pushing"
make_fixture "case6-bad-marketplace"
C6_SHA="$(set_trigger_commit "${WORK_DIR}" "fix(fixture): correct a thing")"
C6_TIP_BEFORE="$(git -C "${ORIGIN_DIR}" rev-parse main)"
run_bump "${WORK_DIR}" "${C6_SHA}" MARKETPLACE_NAME="cenci-typo"
if [[ "${BUMP_EXIT}" -ne 0 ]]; then pass; else fail "case6: expected non-zero exit, got 0"; fi
assert_contains "case6 names the missing entry" "${BUMP_OUTPUT}" "cenci-typo"
assert_eq "case6 origin main is untouched" \
    "$(git -C "${ORIGIN_DIR}" rev-parse main)" "${C6_TIP_BEFORE}"
assert_eq "case6 no tag was created" "$(wc -l < "${GH_LOG_FILE}" | tr -d ' ')" "0"

# ── Case 7: THE #743 REGRESSION — a concurrent push is retried, not lost ──
# A rival commit lands on origin/main between our refresh and our push, so the
# first push is rejected. The retry must re-derive the bump from the NEW tip
# and preserve the rival's change rather than replaying a stale diff over it.
echo "case: a push rejected by a concurrent update is retried against the new tip"
make_fixture "case7-race"
C7_SHA="$(set_trigger_commit "${WORK_DIR}" "fix(fixture): correct a thing")"
install_competing_push_hook "${WORK_DIR}" "${ORIGIN_DIR}" "${CASE_DIR}" 1
run_bump "${WORK_DIR}" "${C7_SHA}"
assert_eq "case7 exit status" "${BUMP_EXIT}" 0
assert_eq "case7 this plugin was bumped exactly once" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.claude-plugin/plugin.json" | jq -r .version)" "1.2.4"
assert_eq "case7 the rival's concurrent change survived" \
    "$(origin_file "${ORIGIN_DIR}" ".claude-plugin/marketplace.json" \
        | jq -r '.plugins[] | select(.name == "other-plugin") | .version')" "9.9.10"
assert_eq "case7 our marketplace entry is stamped on top of the rival's commit" \
    "$(origin_file "${ORIGIN_DIR}" ".claude-plugin/marketplace.json" \
        | jq -r '.plugins[] | select(.name == "cenci-fixture") | .version')" "1.2.4"
assert_eq "case7 exactly one release commit for this plugin" \
    "$(git -C "${ORIGIN_DIR}" log --pretty=format:%s main \
        | grep -c '^chore(release): cenci-fixture/v')" "1"
assert_eq "case7 the rival's commit is an ancestor of ours" \
    "$(git -C "${ORIGIN_DIR}" log -1 --pretty=format:%s main)" \
    "chore(release): cenci-fixture/v1.2.4"
assert_contains "case7 tags the version that was actually pushed" \
    "$(cat "${GH_LOG_FILE}")" "refs/tags/fixture/v1.2.4"

# ── Case 8: the retry re-reads the version from the refreshed tip ──────
# The rival bumps THIS plugin's source manifest, so a correct retry must
# re-read 2.0.0 from the new tip and produce 2.0.1 — never 1.2.4 derived from
# the stale pre-race version it first computed.
echo "case: the retry re-derives the version from the refreshed tip, not the stale one"
make_fixture "case8-rebase-version"
C8_SHA="$(set_trigger_commit "${WORK_DIR}" "fix(fixture): correct a thing")"
HOOK_CASE_DIR="${CASE_DIR}"
cat > "${WORK_DIR}/.git/hooks/pre-push" <<EOF
#!/usr/bin/env bash
set -uo pipefail
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX \\
      GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_QUARANTINE_PATH
COUNT_FILE="${HOOK_CASE_DIR}/competing-pushes"
[ -f "\${COUNT_FILE}" ] && exit 0
touch "\${COUNT_FILE}"
RIVAL="${HOOK_CASE_DIR}/rival"
git clone --quiet "${ORIGIN_DIR}" "\${RIVAL}"
git -C "\${RIVAL}" config user.email "rival@example.com"
git -C "\${RIVAL}" config user.name "rival"
jq '.version = "2.0.0"' "\${RIVAL}/plugin/.claude-plugin/plugin.json" > "\${RIVAL}/t.json"
mv "\${RIVAL}/t.json" "\${RIVAL}/plugin/.claude-plugin/plugin.json"
git -C "\${RIVAL}" add -A
git -C "\${RIVAL}" commit --quiet -m "chore: rival manual version set"
git -C "\${RIVAL}" push --quiet origin main
exit 0
EOF
chmod +x "${WORK_DIR}/.git/hooks/pre-push"
run_bump "${WORK_DIR}" "${C8_SHA}"
assert_eq "case8 exit status" "${BUMP_EXIT}" 0
assert_eq "case8 version derives from the refreshed tip" \
    "$(origin_file "${ORIGIN_DIR}" "plugin/.claude-plugin/plugin.json" | jq -r .version)" "2.0.1"
assert_contains "case8 tags the refreshed version" "$(cat "${GH_LOG_FILE}")" \
    "refs/tags/fixture/v2.0.1"
assert_not_contains "case8 never tags the stale version" "$(cat "${GH_LOG_FILE}")" \
    "refs/tags/fixture/v1.2.4"

# ── Case 9: exhausting the retry budget fails loudly ──────────────────
echo "case: exhausting the push-retry budget fails loudly instead of silently"
make_fixture "case9-exhausted"
C9_SHA="$(set_trigger_commit "${WORK_DIR}" "fix(fixture): correct a thing")"
install_competing_push_hook "${WORK_DIR}" "${ORIGIN_DIR}" "${CASE_DIR}" 99
run_bump "${WORK_DIR}" "${C9_SHA}" PUSH_RETRIES=2
if [[ "${BUMP_EXIT}" -ne 0 ]]; then pass; else fail "case9: expected non-zero exit, got 0"; fi
assert_contains "case9 explains the exhausted budget" "${BUMP_OUTPUT}" "2"
assert_eq "case9 no tag was created" "$(wc -l < "${GH_LOG_FILE}" | tr -d ' ')" "0"
assert_eq "case9 no release commit landed on origin" \
    "$(git -C "${ORIGIN_DIR}" log --pretty=format:%s main \
        | grep -c '^chore(release): cenci-fixture/v')" "0"

# ── Case 10: downstream dispatch is opt-in ────────────────────────────
echo "case: a downstream workflow is dispatched only when one is configured"
make_fixture "case10-dispatch"
C10_SHA="$(set_trigger_commit "${WORK_DIR}" "fix(fixture): correct a thing")"
run_bump "${WORK_DIR}" "${C10_SHA}" DISPATCH_WORKFLOW="watch-release.yml"
assert_eq "case10 exit status" "${BUMP_EXIT}" 0
assert_contains "case10 dispatches the configured workflow" "$(cat "${GH_LOG_FILE}")" \
    "workflow run watch-release.yml"
assert_contains "case10 dispatches the new version" "$(cat "${GH_LOG_FILE}")" \
    "version=1.2.4"

echo "case: no downstream workflow is dispatched when none is configured"
make_fixture "case10b-no-dispatch"
C10B_SHA="$(set_trigger_commit "${WORK_DIR}" "fix(fixture): correct a thing")"
run_bump "${WORK_DIR}" "${C10B_SHA}"
assert_eq "case10b exit status" "${BUMP_EXIT}" 0
assert_not_contains "case10b dispatches nothing" "$(cat "${GH_LOG_FILE}")" "workflow run"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
