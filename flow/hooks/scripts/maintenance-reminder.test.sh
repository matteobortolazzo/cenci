#!/usr/bin/env bash
# maintenance-reminder.test.sh — runtime behavior of the dormant SessionStart
# maintenance-reminder advisory (ticket #534). Modelled on
# check-config-staleness.test.sh (a `failures=` counter, small assert_*
# helpers, self-contained, auto-discovered by the flow gate's `*.test.sh`
# glob) and guard-main-worktree.test.sh (`git -C <dir> init -q` git-repo
# fixtures), since this hook is the only one that needs a real git history
# (condition (b) below diffs against the marker's recorded SHA).
#
# Contract under test (pinned here for the not-yet-written implementation):
#   No stdin, no args. Reads, from cwd:
#     - .cenci/config.json's `.maintenance.remindAfterDays` (a positive
#       number) -- absent file/key/non-positive/malformed/missing jq means
#       the hook is dormant: silent, exit 0, no output at all.
#     - .cenci/last-audit.json ({generatedAt: ISO8601, sha: <commit>}),
#       written by the scheduled cenci-maintenance.yml workflow on its last
#       clean run.
#     - .cenci/maintain-report.json (the local, possibly-gitignored, last
#       maintain run's machine report), if present.
#   When active (a positive remindAfterDays is configured), the hook warns
#   (emits {hookSpecificOutput:{hookEventName:"SessionStart",
#   additionalContext:<non-empty text>}} and exits 0) when ANY of:
#     (a) the marker's generatedAt is older than remindAfterDays;
#     (b) a maintenance-sensitive file (AGENTS.md is in-allowlist per the
#         plan's Assumptions) changed between the marker's sha and HEAD
#         (`git diff --name-only <sha>...HEAD`);
#     (c) a local .cenci/maintain-report.json exists with summary.fail > 0;
#     (d) the marker file itself does not exist at all (a gentler advisory
#         than (a) -- there has simply never been a recorded clean audit).
#   Otherwise (config active, marker fresh, no sensitive-file diff, no
#   local report failures) the hook is silent and exits 0.
#   The hook never blocks: every one of the above paths exits 0.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK="${SCRIPT_DIR}/maintenance-reminder.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain [$2], got [$1]"; }

TEST_ROOT="$(mktemp -d /var/tmp/maintenance-reminder-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# make_repo <dir> — inits a git repo with one committed file so HEAD/rev-parse
# resolve. Uses `-c user.*` (never global config) to stay isolated.
make_repo() {
    local dir="$1"
    mkdir -p "${dir}"
    git -C "${dir}" init -q -b main
    echo "seed" > "${dir}/seed.txt"
    git -C "${dir}" add seed.txt
    git -C "${dir}" -c user.email=test@example.com -c user.name=Test commit -q -m "seed"
}

write_config() {  # write_config <dir> <remindAfterDays>
    local dir="$1" days="$2"
    mkdir -p "${dir}/.cenci"
    printf '{"maintenance":{"remindAfterDays":%s}}' "${days}" > "${dir}/.cenci/config.json"
}

write_marker() {  # write_marker <dir> <generatedAt-iso8601> <sha>
    local dir="$1" generated_at="$2" sha="$3"
    mkdir -p "${dir}/.cenci"
    jq -n --arg g "${generated_at}" --arg s "${sha}" '{generatedAt: $g, sha: $s}' > "${dir}/.cenci/last-audit.json"
}

write_local_report() {  # write_local_report <dir> <fail-count>
    local dir="$1" fail_count="$2"
    mkdir -p "${dir}/.cenci"
    jq -n --argjson f "${fail_count}" \
        '{summary: {pass: 0, warn: 0, fail: $f, skip: 0, mode: "strict"}, results: []}' \
        > "${dir}/.cenci/maintain-report.json"
}

run_hook() {  # run_hook <dir>
    local dir="$1"
    OUT="$(cd "${dir}" && bash "${HOOK}" 2>&1)"
    CODE=$?
}

assert_silent_zero() {  # assert_silent_zero <label>
    local label="$1"
    assert_eq "${CODE}" "0" "${label} exit"
    assert_eq "${OUT}" "" "${label} silent"
}

assert_advisory() {  # assert_advisory <label> — hookSpecificOutput JSON w/ non-empty context, exit 0
    local label="$1"
    assert_eq "${CODE}" "0" "${label} exit"
    echo "${OUT}" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null 2>&1 \
        || fail "${label}: expected SessionStart hookSpecificOutput JSON (got: ${OUT})"
    CTX="$(echo "${OUT}" | jq -r '.hookSpecificOutput.additionalContext // empty' 2>/dev/null)"
    [[ -n "${CTX}" ]] || fail "${label}: expected a non-empty additionalContext advisory (got: ${OUT})"
}

NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
FRESH="$(date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)"
STALE="$(date -u -d '-30 days' +%Y-%m-%dT%H:%M:%SZ)"

echo "maintenance-reminder.test.sh"

# --- Case 1: no maintenance.remindAfterDays key at all -> silent, exit 0 ---
REPO="${TEST_ROOT}/case1-no-config-key"
make_repo "${REPO}"
run_hook "${REPO}"
assert_silent_zero "case1 (no config key)"

# --- Case 2: config active + fresh marker + no sensitive-file changes ------
# -> silent, exit 0. Marker sha == HEAD (no diff at all), generatedAt is
# recent (well under the 7-day threshold), no local report present.
REPO="${TEST_ROOT}/case2-active-fresh-no-changes"
make_repo "${REPO}"
write_config "${REPO}" 7
HEAD_SHA="$(git -C "${REPO}" rev-parse HEAD)"
write_marker "${REPO}" "${FRESH}" "${HEAD_SHA}"
run_hook "${REPO}"
assert_silent_zero "case2 (active, fresh marker, no changes)"

# --- Case 3: stale marker (generatedAt older than remindAfterDays) --------
# -> advisory. sha == HEAD still (no sensitive-file diff), no local report,
# so staleness is the only possible trigger.
REPO="${TEST_ROOT}/case3-stale-marker"
make_repo "${REPO}"
write_config "${REPO}" 7
HEAD_SHA="$(git -C "${REPO}" rev-parse HEAD)"
write_marker "${REPO}" "${STALE}" "${HEAD_SHA}"
run_hook "${REPO}"
assert_advisory "case3 (stale marker)"

# --- Case 4: a maintenance-sensitive file changed since marker's sha ------
# -> advisory. Marker sha is recorded BEFORE a commit that touches AGENTS.md
# (in the plan's Assumptions allowlist); generatedAt is fresh and there is no
# local report, so the file-change condition is the only possible trigger.
REPO="${TEST_ROOT}/case4-sensitive-file-changed"
make_repo "${REPO}"
write_config "${REPO}" 7
MARKER_SHA="$(git -C "${REPO}" rev-parse HEAD)"
echo "## Critical Rules" >> "${REPO}/AGENTS.md"
git -C "${REPO}" add AGENTS.md
git -C "${REPO}" -c user.email=test@example.com -c user.name=Test commit -q -m "touch AGENTS.md"
write_marker "${REPO}" "${FRESH}" "${MARKER_SHA}"
run_hook "${REPO}"
assert_advisory "case4 (sensitive file changed since marker sha)"

# --- Case 5: local .cenci/maintain-report.json with summary.fail > 0 ------
# -> advisory. Marker is fresh and sha == HEAD (no other trigger), so the
# local report's fail count is the only possible trigger.
REPO="${TEST_ROOT}/case5-local-report-fail"
make_repo "${REPO}"
write_config "${REPO}" 7
HEAD_SHA="$(git -C "${REPO}" rev-parse HEAD)"
write_marker "${REPO}" "${FRESH}" "${HEAD_SHA}"
write_local_report "${REPO}" 2
run_hook "${REPO}"
assert_advisory "case5 (local report has fail > 0)"

# --- Case 6: marker file absent entirely -> gentle advisory ---------------
REPO="${TEST_ROOT}/case6-marker-absent"
make_repo "${REPO}"
write_config "${REPO}" 7
run_hook "${REPO}"
assert_advisory "case6 (marker absent)"

# --- Case 7: remindAfterDays == 0 (non-positive) -> dormant, silent exit 0
REPO="${TEST_ROOT}/case7-zero-remind-after-days"
make_repo "${REPO}"
write_config "${REPO}" 0
run_hook "${REPO}"
assert_silent_zero "case7 (remindAfterDays == 0)"

# --- Case 8: remindAfterDays is a non-numeric string -> dormant, silent
# exit 0 ---------------------------------------------------------------
REPO="${TEST_ROOT}/case8-non-numeric-remind-after-days"
make_repo "${REPO}"
mkdir -p "${REPO}/.cenci"
printf '{"maintenance":{"remindAfterDays":"soon"}}' > "${REPO}/.cenci/config.json"
run_hook "${REPO}"
assert_silent_zero "case8 (remindAfterDays is a non-numeric string)"

# --- Case 9: every case above must exit 0 (never blocks) ------------------
# Folded into assert_silent_zero/assert_advisory above (each asserts
# CODE == 0); this final check re-asserts it holds for the last-run case as
# a canary against a future change accidentally introducing a non-zero exit.
assert_eq "${CODE}" "0" "case9 (never blocks)"

echo "maintenance-reminder.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
