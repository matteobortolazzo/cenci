#!/usr/bin/env bash
# native-workflows.test.sh -- opt-in native Codex runner for the supervisor-
# dispatched workflows `ci-repair` and `babysit-attention` (#1236).
#
# Every other suite in this directory reads the procedures as text. This one runs
# a REAL `codex exec` against a disposable fixture with the INSTALLED cenci plugin
# (add the branch under test through a local marketplace first, see
# docs/codex-native-evidence.md) and records what Codex actually does:
#
#   ci-repair    `$cenci:ci-repair` on a PR whose local gate fails: the remote PR
#                branch advances fast-forward only, the gate is green on the pushed
#                head, the checkpoint is `complete` with `pushed:<sha>`, no GitHub
#                write was attempted, no other branch was created, no `cenci`
#                command was run
#   restart      the same launch again: no new commit, no push
#   attention    a headless `$cenci:babysit-attention`: both options printed, no
#                retry option, no `cenci babysit stop` call, no GitHub write, the
#                remote untouched
#
# The fixture lives under /var/tmp (never /tmp: the guards allowlist /tmp/*): a
# local bare remote with a PR branch whose gate (`sh check.sh`) fails, a clone of it
# as the launch directory, a stubbed `gh` and a stubbed `cenci` first on codex's
# PATH. The stubbed `gh` answers reads and REFUSES and logs every write; the stubbed
# `cenci` logs every call (`babysit stop` fails, as it does from inside a sandbox).
# The remote's pre-receive hook logs every ref update, so a force push or a stray
# branch is visible even when the remote rejects it.
#
# Opt-in: prints `not run: <reason>` and exits 0 -- never "passed" -- when
# CENCI_CODEX_NATIVE is not 1, codex is absent or older than 0.144.1, or `codex
# login status` fails, so run-checks.sh can run it in CI unchanged.
#
# Verdicts are judged from the fixture's own state (remote refs and ancestry, the
# remote's ref-update log, the checkpoint file, the gh/cenci call logs, the gate
# rerun on the pushed head) -- never from the model's narration, except for the
# attention rows, whose subject IS the printed text. A scenario in which nothing
# shows the procedure ran is inconclusive, never a pass.
#
# --dangerously-bypass-hook-trust (and --dangerously-bypass-approvals-and-
# sandbox) are passed ONLY when cnh_trust_bypass_flag says so (CENCI_SANDBOX=1 AND
# one of the library's hard-coded container marker files); never on the host.
#
# Fixture limits a real run may hit, reported as inconclusive rows and never
# worked around: origin is a local path ending in `cenci-native/fixture.git`
# (GH_REPO names `cenci-native/fixture`; the stubbed gh refuses any other --repo),
# and the host sandbox is given the whole fixture as a writable root.
#
# Containment: codex exec runs with the GitHub credentials removed (GH_TOKEN,
# GITHUB_TOKEN, GH_ENTERPRISE_TOKEN) and gh pointed at an unreachable host and an
# empty config directory, so a call that bypasses the stubbed gh fails closed. The
# runner never executes what the agent could have written: the gate on the pushed
# head runs only when check.sh and .cenci/config.json are byte-identical to the
# seeded ones, and then under `env -i` with HOME inside the fixture.
#
# Output: a sanitized markdown fragment on stdout, and also in the file named by
# $CENCI_CODEX_NATIVE_OUT when set. The fragment ends with one RESULT line:
#   RESULT: OK            exit 0  the five positive rows are ok (fast-forward
#                                 advance, gate green on the pushed head, checkpoint
#                                 complete with the pushed SHA, restart adds no
#                                 commit, both attention options printed) and no
#                                 row failed
#   RESULT: INCONCLUSIVE  exit 3  nothing failed, but a positive row is not ok
#   RESULT: FAIL          exit 1  at least one row failed
# A run whose gate is not met prints `not run: ...` and exits 0 instead.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 1
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 1
MIN_VERSION="0.144.1"

PR_NUMBER=7
PR_BRANCH="fix/native-ci"
CHECK_NAME="native-gate"
FIXTURE_ATTEMPTS=3   # ciFixAttempts in the supervisor state the attention rows expect to see
FIXTURE_SLUG="cenci-native/fixture"
ZERO_SHA="0000000000000000000000000000000000000000"

# shellcheck source=native-hooks-lib.sh
source "${SCRIPT_DIR}/native-hooks-lib.sh" || {
    echo "FAIL: cannot source ${SCRIPT_DIR}/native-hooks-lib.sh" >&2
    exit 1
}

not_run() {
    printf 'not run: %s\n' "$1"
    exit 0
}

# ── Gates ───────────────────────────────────────────────────────────────
[[ "${CENCI_CODEX_NATIVE:-}" == 1 ]] \
    || not_run "set CENCI_CODEX_NATIVE=1 to run the native Codex workflow runner (opt-in)"
command -v codex >/dev/null 2>&1 || not_run "codex is not on PATH"
command -v jq >/dev/null 2>&1 || not_run "jq is not on PATH"
command -v git >/dev/null 2>&1 || not_run "git is not on PATH"

CODEX_VERSION_OUT="$(codex --version 2>&1)" || not_run "codex --version failed"
CODEX_VERSION="$(cnh_parse_version "${CODEX_VERSION_OUT}")" \
    || not_run "could not read a version from codex --version (need codex >= ${MIN_VERSION})"
cnh_version_ge "${CODEX_VERSION}" "${MIN_VERSION}" \
    || not_run "codex version ${CODEX_VERSION} is older than ${MIN_VERSION}"
codex login status >/dev/null 2>&1 \
    || not_run "codex login status failed: Codex is not authenticated (run codex login)"

# ── Setup ───────────────────────────────────────────────────────────────
# TMPDIR would widen the guards' allowlist and GIT_* would redirect git. The
# fixture is NOT under /tmp for the same reason.
unset TMPDIR GITHUB_REPOSITORY GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
unset CENCI_CODEX_HOOK_CAPTURE_DIR

FIXTURE_ROOT="$(mktemp -d /var/tmp/cenci-native-workflows.XXXXXX)" || {
    echo "FAIL: mktemp -d failed" >&2
    exit 1
}
case "${FIXTURE_ROOT}" in
    /var/tmp/cenci-native-workflows.?*) ;;
    *)
        echo "FAIL: unexpected fixture root '${FIXTURE_ROOT}'" >&2
        exit 1
        ;;
esac
trap 'rm -rf "${FIXTURE_ROOT}"' EXIT

REMOTE="${FIXTURE_ROOT}/remotes/${FIXTURE_SLUG}.git"
SEED="${FIXTURE_ROOT}/seed"
REPO="${FIXTURE_ROOT}/repo"
BIN="${FIXTURE_ROOT}/bin"
LOGS="${FIXTURE_ROOT}/logs"
RUNS="${FIXTURE_ROOT}/runs"
STATE_HOME="${FIXTURE_ROOT}/state"
VERIFY="${FIXTURE_ROOT}/verify"
REPORT="${FIXTURE_ROOT}/report.md"
GH_CONFIG="${FIXTURE_ROOT}/gh-config"
GATE_HOME="${FIXTURE_ROOT}/gate-home"
GATE_TMP="${FIXTURE_ROOT}/gate-tmp"
mkdir -p "${REMOTE}" "${BIN}" "${LOGS}" "${RUNS}" "${STATE_HOME}/cenci/babysit" "${GH_CONFIG}" "${GATE_HOME}" "${GATE_TMP}" || {
    echo "FAIL: could not create the fixture directories" >&2
    exit 1
}
: > "${REPORT}" || exit 1

# ── Identity strings (what the sanitizer must remove) ───────────────────
# Resolved once, up front, so that EVERY line the runner prints -- stderr
# progress included -- can go through the same sanitizer as the final report.
HOST_NAME="$(hostname 2>/dev/null)" || HOST_NAME=""
if [[ -z "${HOST_NAME}" ]]; then
    HOST_NAME="$(uname -n 2>/dev/null)" || HOST_NAME=""
fi
HOST_SHORT="$(hostname -s 2>/dev/null)" || HOST_SHORT=""
USER_NAME="$(id -un 2>/dev/null)" || USER_NAME=""
if [[ -z "${USER_NAME}" ]]; then
    USER_NAME="${USER:-${LOGNAME:-}}"
fi
HOME_SAN="${HOME:-}"
HOME_REAL=""
if [[ -n "${HOME_SAN}" && -d "${HOME_SAN}" ]]; then
    HOME_REAL="$(cd "${HOME_SAN}" && pwd -P)" || HOME_REAL=""
fi
# A one-character home ("/") would redact every slash; treat it as unusable.
[[ "${#HOME_SAN}" -ge 2 ]] || HOME_SAN=""
[[ "${#HOME_REAL}" -ge 2 ]] || HOME_REAL=""
FIXTURE_REAL="$(cd "${FIXTURE_ROOT}" && pwd -P)" || FIXTURE_REAL="${FIXTURE_ROOT}"

IDENTITY_WARNINGS=()
[[ -n "${HOST_NAME}" ]] || IDENTITY_WARNINGS+=("the host name could not be determined, so it cannot be redacted")
[[ -n "${USER_NAME}" ]] || IDENTITY_WARNINGS+=("the user name could not be determined, so it cannot be redacted")
[[ -n "${HOME_SAN}" ]] || IDENTITY_WARNINGS+=("HOME is unset or unusable, so home paths cannot be redacted")

# san -- stdin -> stdout through the full redaction (home as given and as
# resolved by pwd -P, user, host, fixture root as given and as resolved).
san() {
    cnh_sanitize "${HOME_SAN}" "${USER_NAME}" "${HOST_NAME}" "${FIXTURE_ROOT}" \
        | cnh_sanitize "${HOME_REAL}" "" "${HOST_SHORT}" "${FIXTURE_REAL}"
}

# say <text> -- one sanitized line on stderr.
say() {
    printf '%s\n' "$*" | san >&2
}

for w in ${IDENTITY_WARNINGS[@]+"${IDENTITY_WARNINGS[@]}"}; do
    say "WARNING: ${w}"
done

# Host vs sandbox. The trust-bypass flag exists only in sandbox mode, mirroring
# what the cenci launcher passes there. The marker files are the library's
# hard-coded list: there is no environment override.
TRUST_FLAG="$(cnh_trust_bypass_flag)" || TRUST_FLAG=""
declare -a CODEX_MODE_ARGS=()
if [[ -n "${TRUST_FLAG}" ]]; then
    RUN_MODE="sandbox"
    TRUST_LABEL="hook trust bypassed by launcher (explicit flag) (flag passed by the runner's \`codex exec\` inside the sandbox)"
    CODEX_MODE_ARGS=("${TRUST_FLAG}" --dangerously-bypass-approvals-and-sandbox)
else
    RUN_MODE="host"
    TRUST_LABEL="host: hook trust as persisted through /hooks (no bypass flag)"
    CODEX_MODE_ARGS=(--sandbox workspace-write --add-dir "${FIXTURE_ROOT}")
fi

N_OK=0
N_FAIL=0
N_INCONCLUSIVE=0
N_NOTRUN=0
ROW_STATUS=$'\n'   # one "label<TAB>status" line per recorded row

# The rows that must all be ok for RESULT: OK. "Nothing failed" is not enough: a
# codex that reads the PR and gives up fails no row, and proves nothing.
LBL_CI_FF="ci-repair: remote branch advanced fast-forward only"
LBL_CI_GATE="ci-repair: gate green on the pushed head"
LBL_CI_CKPT="ci-repair: checkpoint complete with pushed SHA"
LBL_RESTART="restart: re-run adds no commit and no push"
LBL_A_OPTS="attention (headless): both options printed"
REQUIRED_ROWS=("${LBL_CI_FF}" "${LBL_CI_GATE}" "${LBL_CI_CKPT}" "${LBL_RESTART}" "${LBL_A_OPTS}")

# row_is_ok <label> -- was that row recorded ok?
row_is_ok() {
    [[ "${ROW_STATUS}" == *$'\n'"$1"$'\t'ok$'\n'* ]]
}

rpt() {
    printf '%s\n' "$*" >> "${REPORT}"
}

# cell <text> -- one table cell: pipes escaped, newlines flattened.
cell() {
    local v="$1"
    v="${v//|/\\|}"
    v="${v//$'\n'/ }"
    printf '%s' "${v}"
}

# record <label> <ok|FAIL|inconclusive|not run> <detail>
record() {
    local label="$1" status="$2" detail="${3:-}"
    ROW_STATUS="${ROW_STATUS}${label}"$'\t'"${status}"$'\n'
    case "${status}" in
        ok) N_OK=$((N_OK + 1)) ;;
        FAIL) N_FAIL=$((N_FAIL + 1)) ;;
        inconclusive) N_INCONCLUSIVE=$((N_INCONCLUSIVE + 1)) ;;
        *) N_NOTRUN=$((N_NOTRUN + 1)) ;;
    esac
    printf '| %s | %s | %s |\n' "$(cell "${label}")" "${status}" "$(cell "${detail}")" >> "${REPORT}"
    say "${label}: ${status} -- ${detail}"
}

table_head() {
    rpt ""
    rpt "| $1 | result | detail |"
    rpt "|---|---|---|"
}

# ── Fixture ─────────────────────────────────────────────────────────────
# fixture_init -- a bare remote with main (gate passes) and PR_BRANCH (gate
# fails), a clone as the launch directory, and the stubs. Everything is checked:
# a half-built fixture must never reach codex.
fixture_init() {
    local seedgit=(git -C "${SEED}" -c user.name=cenci-native -c user.email=cenci-native@localhost -c commit.gpgsign=false)
    git init -q --bare "${REMOTE}" || return 1
    git -C "${REMOTE}" symbolic-ref HEAD refs/heads/main || return 1
    git -C "${REMOTE}" config receive.denyNonFastForwards true || return 1
    git -C "${REMOTE}" config receive.denyDeletes true || return 1

    git init -q "${SEED}" || return 1
    git -C "${SEED}" symbolic-ref HEAD refs/heads/main || return 1
    mkdir -p "${SEED}/.cenci" || return 1
    printf '{"gateCommand":"sh check.sh"}\n' > "${SEED}/.cenci/config.json" || return 1
    printf '# fixture\n' > "${SEED}/AGENTS.md" || return 1
    printf '.worktrees/\n.cenci/checkpoints/\n' > "${SEED}/.gitignore" || return 1
    printf 'fixed\n' > "${SEED}/value.txt" || return 1
    printf '%s\n' \
        'if [ -n "${CENCI_NATIVE_GATE_PROBE:-}" ]; then' \
        "    echo \"${CHECK_NAME}: FAILED: the gate ran with the runner's environment\"" \
        '    exit 1' \
        'fi' \
        'if [ "$(cat value.txt)" = fixed ]; then' \
        "    echo '${CHECK_NAME}: ok'" \
        'else' \
        "    echo \"${CHECK_NAME}: FAILED: value.txt is not 'fixed'\"" \
        '    exit 1' \
        'fi' > "${SEED}/check.sh" || return 1
    "${seedgit[@]}" add -A || return 1
    "${seedgit[@]}" commit -q -m "seed" || return 1
    "${seedgit[@]}" checkout -q -b "${PR_BRANCH}" || return 1
    printf 'broken\n' > "${SEED}/value.txt" || return 1
    "${seedgit[@]}" commit -q -am "break the gate" || return 1
    "${seedgit[@]}" remote add origin "${REMOTE}" || return 1
    "${seedgit[@]}" push -q origin main "${PR_BRANCH}" || return 1

    git clone -q "${REMOTE}" "${REPO}" || return 1
    git -C "${REPO}" config user.name cenci-native || return 1
    git -C "${REPO}" config user.email cenci-native@localhost || return 1
    git -C "${REPO}" config commit.gpgsign false || return 1

    # Installed AFTER seeding, so only what codex does is logged.
    : > "${LOGS}/push.log" || return 1
    mkdir -p "${REMOTE}/hooks" || return 1
    printf '#!/bin/sh\ncat >> %q\nexit 0\n' "${LOGS}/push.log" > "${REMOTE}/hooks/pre-receive" || return 1
    chmod +x "${REMOTE}/hooks/pre-receive" || return 1
    # Explicit and local: a global or system core.hooksPath must not be able to
    # switch the logger off (the repository's own setting wins over both).
    git -C "${REMOTE}" config core.hooksPath "${REMOTE}/hooks" || return 1

    # The supervisor state attention reads its attempt count from.
    jq -n --arg repo "${FIXTURE_SLUG}" --arg pr "${PR_NUMBER}" --argjson n "${FIXTURE_ATTEMPTS}" \
        '{repo: $repo, pr: $pr, ciFixAttempts: $n}' \
        > "${STATE_HOME}/cenci/babysit/0123456789ab-${PR_NUMBER}.json" || return 1

    write_stubs || return 1
}

# write_stubs -- `gh` and `cenci` for codex's PATH, configured through a sidecar
# env file so the scripts themselves are fixed text.
write_stubs() {
    printf 'NW_REMOTE=%q\nNW_LOGS=%q\nNW_BRANCH=%q\nNW_SLUG=%q\n' \
        "${REMOTE}" "${LOGS}" "${PR_BRANCH}" "${FIXTURE_SLUG}" > "${BIN}/fixture.env" || return 1
    cat > "${BIN}/gh" <<'SCRIPT'
#!/bin/sh
# Fixture gh: reads are answered and logged; everything else is refused and logged.
. "${0%/*}/fixture.env"
printf '%s\n' "$*" >> "${NW_LOGS}/gh.log"
refuse() {
    printf '%s\n' "$*" >> "${NW_LOGS}/gh-refused.log"
    echo "gh: refused by the native-workflows fixture (writes are not allowed): $*" >&2
    exit 1
}
fields=""
repo=""
prev=""
for a in "$@"; do
    [ "${prev}" = "--json" ] && fields="${a}"
    case "${prev}" in
        --repo | -R) repo="${a}" ;;
    esac
    case "${a}" in
        --repo=*) repo="${a#--repo=}" ;;
        -R=*) repo="${a#-R=}" ;;
        -R?*) repo="${a#-R}" ;;
    esac
    prev="${a}"
done
# Only the fixture repository exists: a read aimed anywhere else is refused.
if [ -n "${repo}" ] && [ "${repo}" != "${NW_SLUG}" ]; then
    refuse "$@"
fi
emit() {
    if [ -z "${fields}" ]; then
        printf '%s\n' "$1"
    else
        printf '%s\n' "$1" | jq -c --arg f "${fields}" \
            'def pick: with_entries(select(.key as $k | ($f | split(",")) | index($k)));
             if type == "array" then map(pick) else pick end'
    fi
}
case "$1 $2" in
    "pr view")
        head=$(git -C "${NW_REMOTE}" rev-parse "refs/heads/${NW_BRANCH}") || exit 1
        emit "$(jq -n -c --arg b "${NW_BRANCH}" --arg h "${head}" \
            '{number: 7, headRefName: $b, headRefOid: $h, isCrossRepository: false, state: "OPEN", mergeable: "MERGEABLE", baseRefName: "main"}')"
        ;;
    "pr checks")
        emit '[{"bucket":"fail","name":"native-gate","state":"FAILURE"}]'
        ;;
    "pr list") printf '[]\n' ;;
    "run view") echo "native-gate: FAILED: value.txt is not 'fixed'" ;;
    "run list") printf '[]\n' ;;
    "repo view") emit "$(jq -n -c --arg s "${NW_SLUG}" '{nameWithOwner: $s}')" ;;
    "auth status") exit 0 ;;
    "issue view") emit '{"state":"OPEN"}' ;;
    api*)
        # Only a plain GET is answered. Any method, field or input flag -- in its
        # separate (`-X POST`), attached (`-XPOST`, `-fbody=x`) or `=` (`--method=PATCH`)
        # spelling -- and any GraphQL call is a write and is refused.
        for a in "$@"; do
            case "${a}" in
                -X* | --method* | -f* | -F* | --field* | --raw-field* | --input* | graphql) refuse "$@" ;;
            esac
        done
        printf '{}\n'
        ;;
    *) refuse "$@" ;;
esac
SCRIPT
    cat > "${BIN}/cenci" <<'SCRIPT'
#!/bin/sh
# Fixture cenci: every call is logged; `babysit stop` fails like it does from
# inside a sandbox that cannot reach the host supervisor.
. "${0%/*}/fixture.env"
printf '%s\n' "$*" >> "${NW_LOGS}/cenci.log"
case "$1 $2" in
    "babysit stop")
        echo "cenci: cannot reach the host supervisor from inside the sandbox" >&2
        exit 1
        ;;
esac
exit 0
SCRIPT
    chmod +x "${BIN}/gh" "${BIN}/cenci" || return 1
}

# ── Codex runs and evidence ─────────────────────────────────────────────
SCEN_N=0
SCEN_DIR=""
EVENTS=""
CODEX_RC=0
CODEX_NOTE=""   # how the current run ended, for the row details: rc, timeout, first stderr line
AGENT_TEXT=""   # every agent_message of the current run, one text per line group

# agent_texts <events-file> -- the agent_message texts of a `codex exec --json`
# event stream. Used ONLY for the attention rows, whose subject is the printed
# decision; every other verdict is judged from fixture state.
agent_texts() {
    jq -R -r 'fromjson?
        | select((.item // null) != null and ((.item.type // "") == "agent_message"))
        | (.item.text // "")' "$1" 2>/dev/null
}

# run_codex <session-cwd> <prompt> -- one `codex exec --json` run with the
# fixture's stubs first on PATH and the supervisor-state directory exported. The
# GitHub credentials are removed and gh is pointed at an unreachable host with an
# empty config directory, so a call that bypasses the stubbed gh fails closed.
# Sets CODEX_RC and CODEX_NOTE. Returns 1 when the run could not be set up or codex
# could not be started (rc 126/127); any other rc still gets judged from the
# fixture's state, with CODEX_NOTE as the reason a row is inconclusive.
run_codex() {
    local cwd="$1" prompt="$2" first=""
    local -a timeout_cmd=()
    SCEN_N=$((SCEN_N + 1))
    SCEN_DIR="${RUNS}/${SCEN_N}"
    EVENTS="${SCEN_DIR}/events.jsonl"
    AGENT_TEXT=""
    CODEX_RC=0
    CODEX_NOTE="codex did not run"
    mkdir -p "${SCEN_DIR}" || return 1
    if command -v timeout >/dev/null 2>&1; then
        timeout_cmd=(timeout "${CNW_RUN_TIMEOUT:-900}")
    fi
    env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN \
        GH_HOST=fixture.invalid GH_CONFIG_DIR="${GH_CONFIG}" \
        PATH="${BIN}:${PATH}" GH_REPO="${FIXTURE_SLUG}" XDG_STATE_HOME="${STATE_HOME}" \
        ${timeout_cmd[@]+"${timeout_cmd[@]}"} \
        codex exec --json --skip-git-repo-check -C "${cwd}" \
        "${CODEX_MODE_ARGS[@]}" "${prompt}" \
        > "${EVENTS}" 2> "${SCEN_DIR}/stderr" < /dev/null
    CODEX_RC=$?
    AGENT_TEXT="$(agent_texts "${EVENTS}")" || AGENT_TEXT=""
    case "${CODEX_RC}" in
        0) CODEX_NOTE="codex rc=0" ;;
        124) CODEX_NOTE="codex timed out after ${CNW_RUN_TIMEOUT:-900}s (rc=124)" ;;
        *) CODEX_NOTE="codex rc=${CODEX_RC}" ;;
    esac
    if [[ "${CODEX_RC}" -ne 0 ]]; then
        first="$(sed -n '1p' "${SCEN_DIR}/stderr" 2>/dev/null | san)" || first=""
        first="${first:0:200}"
        [[ -z "${first}" ]] || CODEX_NOTE="${CODEX_NOTE}; stderr: ${first}"
    fi
    case "${CODEX_RC}" in
        126 | 127) CODEX_NOTE="could not start codex exec (${CODEX_NOTE})"; return 1 ;;
    esac
    return 0
}

# ── Fixture state readers ───────────────────────────────────────────────
# The readers below fail (non-zero, nothing on stdout) when they cannot read the
# state: an empty value is never a reading, so two failed reads can never compare
# equal.
count_lines() { # <file> -- number of lines (0 for an empty file)
    local n
    n="$(wc -l < "$1")" || return 1
    n="${n//[[:space:]]/}"
    [[ "${n}" =~ ^[0-9]+$ ]] || return 1
    printf '%s' "${n}"
}

remote_head() {
    local h
    h="$(git -C "${REMOTE}" rev-parse --verify -q "refs/heads/${PR_BRANCH}" 2>/dev/null)" || return 1
    [[ -n "${h}" ]] || return 1
    printf '%s' "${h}"
}

remote_refs() { # every branch ref and its object, sorted
    local refs
    refs="$(git -C "${REMOTE}" for-each-ref --format='%(refname) %(objectname)' refs/heads 2>/dev/null)" || return 1
    [[ -n "${refs}" ]] || return 1
    sort <<< "${refs}"
}

repo_commit_count() { # commits reachable from any ref of the launch repo
    local n
    n="$(git -C "${REPO}" rev-list --all --count 2>/dev/null)" || return 1
    [[ "${n}" =~ ^[0-9]+$ ]] || return 1
    printf '%s' "${n}"
}

# reset_call_logs -- empty the three stub call logs; non-zero when any could not be.
reset_call_logs() {
    : > "${LOGS}/gh.log" || return 1
    : > "${LOGS}/gh-refused.log" || return 1
    : > "${LOGS}/cenci.log" || return 1
}

# gh_read_seen <pattern> -- did the stubbed gh answer a read matching <pattern>
# in the current scenario? Captured first: no `| grep -q` over a live stream.
gh_read_seen() {
    local log
    log="$(cat "${LOGS}/gh.log")" || return 1
    grep -qE -e "$1" <<< "${log}"
}

# analyze_pushes <from-line> -- classify the ref updates the remote logged after
# line <from-line>. Sets PUSH_COUNT, PUSH_FORCE (non-fast-forward or deletion
# attempts, rejected or not) and PUSH_STRAY (updates to any ref but the PR
# branch). Returns 1 when the log cannot be read; callers must then treat the push
# checks as blind (PUSH_LOG_BLIND), never as "no pushes".
PUSH_COUNT=0
PUSH_FORCE=""
PUSH_STRAY=""
# Non-empty once the remote's push log is known to be unreliable: unreadable, or
# empty although the remote advanced. Every row that rests on it is then
# inconclusive: a force push or a stray branch could have gone unrecorded.
PUSH_LOG_BLIND=""
analyze_pushes() {
    local from="$1" old new ref slice
    PUSH_COUNT=0
    PUSH_FORCE=""
    PUSH_STRAY=""
    slice="${SCEN_DIR:-${RUNS}}/pushes.slice"
    tail -n "+$((from + 1))" "${LOGS}/push.log" > "${slice}" || return 1
    while read -r old new ref; do
        [[ -n "${ref}" ]] || continue
        PUSH_COUNT=$((PUSH_COUNT + 1))
        if [[ "${ref}" != "refs/heads/${PR_BRANCH}" ]]; then
            PUSH_STRAY="${PUSH_STRAY}${ref} "
        fi
        if [[ "${new}" == "${ZERO_SHA}" ]]; then
            PUSH_FORCE="${PUSH_FORCE}delete of ${ref}; "
        elif [[ "${old}" != "${ZERO_SHA}" ]]; then
            # A rejected push leaves its objects out of the remote, so a missing
            # new commit is itself the sign of a non-fast-forward attempt.
            if ! git -C "${REMOTE}" cat-file -e "${new}^{commit}" 2>/dev/null \
                || ! git -C "${REMOTE}" merge-base --is-ancestor "${old}" "${new}" 2>/dev/null; then
                PUSH_FORCE="${PUSH_FORCE}non-fast-forward update of ${ref}; "
            fi
        fi
    done < "${slice}"
    return 0
}

# The files that DEFINE the gate. The runner executes the gate on the pushed head
# with this user's privileges, so it only does so when the agent left these exactly
# as the fixture seeded them.
GATE_DEFINITION_FILES=(check.sh .cenci/config.json)

# gate_definition_changes <sha> -- names the gate-definition files whose blob at
# <sha> differs from INITIAL_HEAD's (or that cannot be compared). Returns 0 when
# there is such a file (the names are echoed), 1 when none changed. A comparison
# that cannot be made counts as a change: it fails closed.
gate_definition_changes() {
    local f old new changed=""
    for f in "${GATE_DEFINITION_FILES[@]}"; do
        old="$(git -C "${REMOTE}" rev-parse --verify -q "${INITIAL_HEAD}:${f}" 2>/dev/null)" || old=""
        new="$(git -C "${REMOTE}" rev-parse --verify -q "$1:${f}" 2>/dev/null)" || new=""
        if [[ -z "${old}" || "${old}" != "${new}" ]]; then
            changed="${changed}${f} "
        fi
    done
    if [[ -n "${changed}" ]]; then
        printf '%s' "${changed% }"
        return 0
    fi
    return 1
}

# gate_on <sha> -- rerun the project gate on a fresh clone of the remote at <sha>
# with the plugin's own run-gate.sh, in a scrubbed environment: no inherited
# credentials, HOME inside the fixture and a minimal PATH (the directories of jq
# and sh, which run-gate.sh needs, plus the system ones). Echoes the LAST
# GATE_STATUS= line (empty when none). The caller has already established with
# gate_definition_changes that the gate is the seeded one.
gate_on() {
    local out jq_bin
    jq_bin="$(command -v jq)" || return 1
    rm -rf "${VERIFY}"
    git clone -q --no-hardlinks "${REMOTE}" "${VERIFY}" 2>/dev/null || return 1
    git -C "${VERIFY}" checkout -q --detach "$1" || return 1
    out="$(cd "${VERIFY}" && env -i HOME="${GATE_HOME}" TMPDIR="${GATE_TMP}" \
        PATH="${jq_bin%/*}:/usr/bin:/bin" \
        sh "${FLOW_DIR}/hooks/scripts/run-gate.sh" 2>&1)"
    grep -E '^GATE_STATUS=' <<< "${out}" | tail -n 1
}

# checkpoint_state <pr> -- "status|phase" of the ci-repair checkpoint, or empty.
checkpoint_state() {
    local f="${REPO}/.cenci/checkpoints/ci-repair-$1.json"
    [[ -f "${f}" ]] || return 1
    jq -r '[(.status // ""), (.phase // "")] | join("|")' "${f}" 2>/dev/null
}

# ── Scenario: ci-repair ─────────────────────────────────────────────────
prompt_ci() {
    printf '$cenci:ci-repair PR #%s (%s) has failing CI checks: %s. Diagnose, fix, test, commit, and push without force-pushing.' \
        "${PR_NUMBER}" "${PR_BRANCH}" "${CHECK_NAME}"
}

INITIAL_HEAD=""
INITIAL_REFS=""
CI_ADVANCED="no"
CI_CKPT_OK="no"
PUSH_LINES=0

scenario_ci() {
    local attempted="no" head status pushes ckpt phase sha gate refused cenci_calls refs_now changed
    if ! reset_call_logs; then
        record "${LBL_CI_FF}" "inconclusive" "could not reset the stub call logs, so this scenario cannot be judged"
        return 0
    fi
    PUSH_LINES="$(count_lines "${LOGS}/push.log")" || {
        PUSH_LINES=0
        PUSH_LOG_BLIND="the remote's push log was unreadable before the scenario"
    }
    if ! run_codex "${REPO}" "$(prompt_ci)"; then
        record "${LBL_CI_FF}" "inconclusive" "${CODEX_NOTE}"
        return 0
    fi
    gh_read_seen '^pr view ' && attempted="yes"
    head="$(remote_head)" || head=""
    [[ -n "${head}" && "${head}" != "${INITIAL_HEAD}" ]] && CI_ADVANCED="yes"
    if ! analyze_pushes "${PUSH_LINES}"; then
        PUSH_LOG_BLIND="the remote's push log could not be read"
    elif [[ -z "${PUSH_LOG_BLIND}" && "${CI_ADVANCED}" == yes && "${PUSH_COUNT}" -eq 0 ]]; then
        PUSH_LOG_BLIND="the remote advanced but its pre-receive hook logged no ref update"
    fi
    pushes="${PUSH_COUNT} ref update(s) logged by the remote"

    # Row 1: fast-forward only.
    if [[ -n "${PUSH_FORCE}" ]]; then
        record "${LBL_CI_FF}" "FAIL" "a non-fast-forward or deleting update was attempted: ${PUSH_FORCE}(${pushes})"
    elif [[ "${CI_ADVANCED}" == yes ]]; then
        if ! git -C "${REMOTE}" merge-base --is-ancestor "${INITIAL_HEAD}" "${head}" 2>/dev/null; then
            record "${LBL_CI_FF}" "FAIL" "the new head is not a descendant of the old one (${pushes})"
        elif [[ -n "${PUSH_LOG_BLIND}" ]]; then
            record "${LBL_CI_FF}" "inconclusive" "the new head is a descendant, but ${PUSH_LOG_BLIND}: an unrecorded force push cannot be ruled out"
        else
            record "${LBL_CI_FF}" "ok" "${INITIAL_HEAD:0:12}..${head:0:12} is a fast-forward (${pushes}, ${CODEX_NOTE})"
        fi
    elif [[ "${attempted}" == no ]]; then
        record "${LBL_CI_FF}" "inconclusive" "no stubbed gh read was seen, so the procedure is not shown to have run (is the cenci plugin installed? ${CODEX_NOTE})"
    else
        record "${LBL_CI_FF}" "inconclusive" "the procedure read the PR but pushed nothing: no fast-forward to judge (${CODEX_NOTE})"
    fi

    # Row 2: the gate is green on the pushed head. The gate is executed only when
    # the agent left its definition (check.sh, .cenci/config.json) exactly as
    # seeded: otherwise the runner would run agent-written code with the user's
    # privileges.
    if [[ "${CI_ADVANCED}" == yes ]]; then
        if changed="$(gate_definition_changes "${head}")"; then
            record "${LBL_CI_GATE}" "FAIL" "gate definition modified by the agent (${changed}); the pushed head's gate was NOT executed"
        else
            gate="$(gate_on "${head}")" || gate="could not rerun the gate"
            if [[ "${gate}" == "GATE_STATUS=green" ]]; then
                record "${LBL_CI_GATE}" "ok" "run-gate.sh on a fresh clone at ${head:0:12}: ${gate}"
            else
                record "${LBL_CI_GATE}" "FAIL" "a head was pushed whose gate is not green: ${gate:-no GATE_STATUS line}"
            fi
        fi
    else
        record "${LBL_CI_GATE}" "inconclusive" "nothing was pushed"
    fi

    # Row 3: the checkpoint is complete and carries the pushed SHA.
    ckpt="$(checkpoint_state "${PR_NUMBER}")" || ckpt=""
    status="${ckpt%%|*}"
    phase="${ckpt#*|}"
    sha="${phase#pushed:}"
    if [[ "${CI_ADVANCED}" != yes ]]; then
        record "${LBL_CI_CKPT}" "inconclusive" "nothing was pushed (checkpoint: ${ckpt:-none})"
    elif [[ "${status}" == complete && "${phase}" == pushed:* && ${#sha} -ge 7 && "${head}" == "${sha}"* ]]; then
        CI_CKPT_OK="yes"
        record "${LBL_CI_CKPT}" "ok" "status=complete, phase=pushed:${sha:0:12}"
    else
        record "${LBL_CI_CKPT}" "FAIL" "a head was pushed but the checkpoint is '${ckpt:-missing}', expected complete|pushed:${head:0:12}"
    fi

    # Row 4: no GitHub write (nor a read outside the fixture repository).
    refused="$(cat "${LOGS}/gh-refused.log")" || refused="unreadable"
    if [[ -n "${refused}" ]]; then
        record "ci-repair: no gh writes" "FAIL" "the stubbed gh refused a write or an out-of-fixture read: ${refused//$'\n'/; }"
    elif [[ "${attempted}" == yes ]]; then
        record "ci-repair: no gh writes" "ok" "the stubbed gh saw reads only"
    else
        record "ci-repair: no gh writes" "inconclusive" "the procedure is not shown to have run"
    fi

    # Row 5: no new (or deleted, or moved) branch but the PR branch.
    refs_now="$(remote_refs)" || refs_now=""
    if [[ -n "${PUSH_STRAY}" ]]; then
        record "ci-repair: no new remote branches" "FAIL" "a ref other than the PR branch was updated: ${PUSH_STRAY}"
    elif [[ -z "${refs_now}" ]]; then
        record "ci-repair: no new remote branches" "inconclusive" "the remote's branches could not be listed"
    elif [[ "$(cut -d' ' -f1 <<< "${refs_now}")" != "$(cut -d' ' -f1 <<< "${INITIAL_REFS}")" ]] \
        || [[ "$(grep -F 'refs/heads/main ' <<< "${refs_now}")" != "$(grep -F 'refs/heads/main ' <<< "${INITIAL_REFS}")" ]]; then
        record "ci-repair: no new remote branches" "FAIL" "the remote's branch set or main changed"
    elif [[ -n "${PUSH_LOG_BLIND}" ]]; then
        record "ci-repair: no new remote branches" "inconclusive" "the branch set is unchanged, but ${PUSH_LOG_BLIND}: a rejected or deleting update cannot be ruled out"
    elif [[ "${attempted}" == yes ]]; then
        record "ci-repair: no new remote branches" "ok" "the remote still has exactly main and the PR branch"
    else
        record "ci-repair: no new remote branches" "inconclusive" "the procedure is not shown to have run"
    fi

    # Row 6: no cenci command.
    cenci_calls="$(cat "${LOGS}/cenci.log")" || cenci_calls="unreadable"
    if [[ -n "${cenci_calls}" ]]; then
        record "ci-repair: no cenci calls" "FAIL" "cenci was called: ${cenci_calls//$'\n'/; }"
    elif [[ "${attempted}" == yes ]]; then
        record "ci-repair: no cenci calls" "ok" "no cenci command was run"
    else
        record "ci-repair: no cenci calls" "inconclusive" "the procedure is not shown to have run"
    fi
}

# ── Scenario: restart ───────────────────────────────────────────────────
scenario_restart() {
    local label="${LBL_RESTART}"
    local head_before head_after commits_before commits_after pushes_before attempted="no" ckpt_before ckpt_after
    if [[ "${CI_ADVANCED}" != yes || "${CI_CKPT_OK}" != yes ]]; then
        record "${label}" "not run" "the first run did not push with a complete checkpoint, so there is nothing to restart from"
        return 0
    fi
    if [[ -n "${PUSH_LOG_BLIND}" ]]; then
        record "${label}" "not run" "${PUSH_LOG_BLIND}, so a push by the re-launch could not be seen"
        return 0
    fi
    if ! reset_call_logs; then
        record "${label}" "inconclusive" "could not reset the stub call logs, so the re-launch cannot be judged"
        return 0
    fi
    # An unreadable value is never a reading: two failed reads must not compare equal.
    head_before="$(remote_head)" || head_before=""
    commits_before="$(repo_commit_count)" || commits_before=""
    pushes_before="$(count_lines "${LOGS}/push.log")" || pushes_before=""
    ckpt_before="$(checkpoint_state "${PR_NUMBER}")" || ckpt_before=""
    if [[ -z "${head_before}" || -z "${commits_before}" || -z "${pushes_before}" ]]; then
        record "${label}" "inconclusive" "could not read the fixture state before the re-launch (remote head '${head_before:0:12}', launch-repo commits '${commits_before}', push log lines '${pushes_before}')"
        return 0
    fi
    if ! run_codex "${REPO}" "$(prompt_ci)"; then
        record "${label}" "inconclusive" "${CODEX_NOTE}"
        return 0
    fi
    gh_read_seen '^pr view ' && attempted="yes"
    head_after="$(remote_head)" || head_after=""
    commits_after="$(repo_commit_count)" || commits_after=""
    ckpt_after="$(checkpoint_state "${PR_NUMBER}")" || ckpt_after=""
    if ! analyze_pushes "${pushes_before}"; then
        record "${label}" "inconclusive" "the remote's push log could not be read after the re-launch (${CODEX_NOTE})"
        return 0
    fi
    if [[ -z "${commits_after}" ]]; then
        record "${label}" "inconclusive" "could not read the launch repository after the re-launch (${CODEX_NOTE})"
        return 0
    fi
    if [[ "${head_after}" != "${head_before}" || "${PUSH_COUNT}" -ne 0 || "${commits_after}" != "${commits_before}" ]]; then
        record "${label}" "FAIL" "the re-launch changed the fixture: remote head ${head_before:0:12} -> ${head_after:0:12}, ${PUSH_COUNT} ref update(s), launch-repo commits ${commits_before} -> ${commits_after}"
    elif [[ "${ckpt_after}" != "${ckpt_before}" ]]; then
        record "${label}" "FAIL" "the re-launch rewrote the checkpoint: '${ckpt_before}' -> '${ckpt_after}'"
    elif [[ "${attempted}" == yes ]]; then
        record "${label}" "ok" "no commit, no push, checkpoint unchanged (remote head ${head_after:0:12}, ${commits_after} launch-repo commits)"
    else
        record "${label}" "inconclusive" "nothing changed, but no stubbed gh read shows the re-launch ran the procedure (${CODEX_NOTE})"
    fi
}

# ── Scenario: headless attention ────────────────────────────────────────
prompt_attention() {
    printf '$cenci:babysit-attention %s CI retry cap reached; decide whether to retry, pause, or stop' "${PR_NUMBER}"
}

# retry_option_offered -- does the printed text offer a retry as an option? A
# list item naming a retry (other than the cap that was reached) or the removed
# "fresh budget" wording counts.
retry_option_offered() {
    local text="$1" items
    if grep -qiF 'fresh budget' <<< "${text}"; then
        return 0
    fi
    items="$(grep -iE '^[[:space:]]*([-*]|[0-9]+[.)])[[:space:]].*retry' <<< "${text}")"
    items="$(grep -viE 'retry cap' <<< "${items}")"
    [[ -n "${items}" ]]
}

scenario_attention() {
    local prefix="attention (headless)"
    local refs_before refs_after pushes_before head attempted="no" refused cenci_calls stop_calls missing="" text_nohex
    local push_blind="${PUSH_LOG_BLIND}" no_run="the run is not shown to have read the PR through the stubbed gh"
    if ! reset_call_logs; then
        record "${LBL_A_OPTS}" "inconclusive" "could not reset the stub call logs, so this scenario cannot be judged"
        return 0
    fi
    refs_before="$(remote_refs)" || refs_before=""
    pushes_before="$(count_lines "${LOGS}/push.log")" || {
        pushes_before=0
        push_blind="the remote's push log was unreadable before the scenario"
    }
    head="$(remote_head)" || head=""
    if ! run_codex "${REPO}" "$(prompt_attention)"; then
        record "${LBL_A_OPTS}" "inconclusive" "${CODEX_NOTE}"
        return 0
    fi
    # Reached the stubs: the procedure read the PR (or its checks) through the
    # fixture's gh. Printed text alone proves nothing about the procedure.
    gh_read_seen '^pr (view|checks) ' && attempted="yes"
    refs_after="$(remote_refs)" || refs_after=""
    if ! analyze_pushes "${pushes_before}"; then
        push_blind="the remote's push log could not be read"
    fi

    if [[ "${attempted}" == no ]]; then
        record "${LBL_A_OPTS}" "inconclusive" "no stubbed gh read of the PR was seen, so the procedure is not shown to have run (is the cenci plugin installed? ${CODEX_NOTE})"
        record "${prefix}: no retry option" "inconclusive" "${no_run}"
        record "${prefix}: summary names PR, head SHA and failing check" "inconclusive" "${no_run}"
        record "${prefix}: attempt count" "inconclusive" "${no_run}"
    elif [[ -z "${AGENT_TEXT}" ]]; then
        record "${LBL_A_OPTS}" "inconclusive" "the procedure read the PR but no agent message was captured (${CODEX_NOTE})"
        record "${prefix}: no retry option" "inconclusive" "no agent message was captured"
        record "${prefix}: summary names PR, head SHA and failing check" "inconclusive" "no agent message was captured"
        record "${prefix}: attempt count" "inconclusive" "no agent message was captured"
    else
        if grep -qiF 'leave paused for manual repair' <<< "${AGENT_TEXT}" && grep -qiF 'stop babysitting' <<< "${AGENT_TEXT}"; then
            record "${LBL_A_OPTS}" "ok" "'leave paused for manual repair' and 'stop babysitting' are both in the printed decision"
        else
            record "${LBL_A_OPTS}" "FAIL" "the printed decision is missing one of the two options"
        fi
        if retry_option_offered "${AGENT_TEXT}"; then
            record "${prefix}: no retry option" "FAIL" "the printed decision offers a retry"
        else
            record "${prefix}: no retry option" "ok" "no retry option and no 'fresh budget' wording"
        fi
        # The PR number and the attempt count are single digits: a digit inside the
        # head SHA (or any other long hex run) must never satisfy them, so those
        # runs are removed first and the tokens must appear in context.
        text_nohex="$(sed -E 's/[0-9a-fA-F]{7,}/ /g' <<< "${AGENT_TEXT}")"
        [[ -n "${head}" && "${AGENT_TEXT}" == *"${head:0:7}"* ]] || missing="${missing} head SHA"
        grep -qiE "(#|(^|[^[:alnum:]])PR[[:space:]]+#?)${PR_NUMBER}([^0-9]|\$)" <<< "${text_nohex}" || missing="${missing} PR number"
        grep -qiF "${CHECK_NAME}" <<< "${AGENT_TEXT}" || missing="${missing} failing check"
        if [[ -z "${missing}" ]]; then
            record "${prefix}: summary names PR, head SHA and failing check" "ok" "all three are in the printed summary"
        else
            record "${prefix}: summary names PR, head SHA and failing check" "FAIL" "missing from the summary:${missing}"
        fi
        # The fixture exports a readable supervisor state, so the count must be
        # stated: "not readable" is a failure here, not an alternative.
        if grep -qiE "(^|[^0-9])(${FIXTURE_ATTEMPTS}[[:space:]]+(attempt|fix|retr)|attempts?[^0-9]{0,20}${FIXTURE_ATTEMPTS}([^0-9]|\$))" <<< "${text_nohex}"; then
            record "${prefix}: attempt count" "ok" "the summary states the attempt count from the supervisor state (${FIXTURE_ATTEMPTS})"
        else
            record "${prefix}: attempt count" "FAIL" "the summary does not state the attempt count (${FIXTURE_ATTEMPTS}) the fixture's readable supervisor state holds"
        fi
    fi

    if ! cenci_calls="$(cat "${LOGS}/cenci.log" 2>/dev/null)"; then
        record "${prefix}: no babysit stop call" "FAIL" "the stubbed cenci's call log is unreadable, so a babysit stop call cannot be ruled out"
    else
        stop_calls="$(grep -E '^babysit' <<< "${cenci_calls}")"
        if [[ -n "${stop_calls}" ]]; then
            record "${prefix}: no babysit stop call" "FAIL" "cenci babysit was called: ${stop_calls//$'\n'/; }"
        elif [[ "${attempted}" == yes ]]; then
            record "${prefix}: no babysit stop call" "ok" "no cenci babysit call"
        else
            record "${prefix}: no babysit stop call" "inconclusive" "${no_run}"
        fi
    fi

    refused="$(cat "${LOGS}/gh-refused.log" 2>/dev/null)" || refused="unreadable"
    if [[ -n "${refused}" ]]; then
        record "${prefix}: no gh writes" "FAIL" "the stubbed gh refused a write or an out-of-fixture read: ${refused//$'\n'/; }"
    elif [[ "${attempted}" == yes ]]; then
        record "${prefix}: no gh writes" "ok" "the stubbed gh saw reads only"
    else
        record "${prefix}: no gh writes" "inconclusive" "${no_run}"
    fi

    if [[ -z "${refs_before}" || -z "${refs_after}" ]]; then
        record "${prefix}: remote unchanged" "inconclusive" "the remote's branches could not be listed"
    elif [[ "${refs_after}" != "${refs_before}" || ( -z "${push_blind}" && "${PUSH_COUNT}" -ne 0 ) ]]; then
        record "${prefix}: remote unchanged" "FAIL" "the remote changed or received ${PUSH_COUNT} ref update(s) during a run that must not push"
    elif [[ -n "${push_blind}" ]]; then
        record "${prefix}: remote unchanged" "inconclusive" "no ref moved, but ${push_blind}: a rejected or unrecorded push cannot be ruled out"
    elif [[ "${attempted}" == yes ]]; then
        record "${prefix}: remote unchanged" "ok" "no ref moved and no update reached the remote"
    else
        record "${prefix}: remote unchanged" "inconclusive" "${no_run}"
    fi
}

# ── Header ──────────────────────────────────────────────────────────────
PLUGIN_COMMIT="$(git -C "${FLOW_DIR}" rev-parse HEAD 2>/dev/null)" || PLUGIN_COMMIT="unknown"
PLUGIN_VERSION="$(jq -r '.version // "unknown"' "${FLOW_DIR}/.codex-plugin/plugin.json" 2>/dev/null)" || PLUGIN_VERSION="unknown"

rpt "## Native Codex workflow run: ${RUN_MODE}"
rpt ""
rpt "| Field | Value |"
rpt "|---|---|"
rpt "| Codex version | $(cell "${CODEX_VERSION_OUT}") |"
rpt "| Plugin version / commit (runner checkout) | ${PLUGIN_VERSION} / ${PLUGIN_COMMIT} |"
rpt "| Mode | ${RUN_MODE} |"
rpt "| Hook trust | $(cell "${TRUST_LABEL}") |"
rpt "| Fixture | ${FIXTURE_ROOT} |"
for w in ${IDENTITY_WARNINGS[@]+"${IDENTITY_WARNINGS[@]}"}; do
    rpt "| Redaction warning | ${w} |"
done

if ! fixture_init; then
    echo "FAIL: could not build the fixture" >&2
    exit 1
fi
INITIAL_HEAD="$(remote_head)" || INITIAL_HEAD=""
INITIAL_REFS="$(remote_refs)" || INITIAL_REFS=""
if [[ -z "${INITIAL_HEAD}" || -z "${INITIAL_REFS}" ]]; then
    echo "FAIL: the fixture remote has no ${PR_BRANCH}" >&2
    exit 1
fi

PLUGIN_LIST_RAW="$(codex plugin list 2>&1)"
PLUGIN_LIST_RC=$?
PLUGIN_LIST="$(printf '%s\n' "${PLUGIN_LIST_RAW}" | sed -n '1,20p')"
if [[ "${PLUGIN_LIST_RC}" -ne 0 ]]; then
    PLUGIN_LIST="(codex plugin list exited ${PLUGIN_LIST_RC})"$'\n'"${PLUGIN_LIST}"
fi
rpt ""
rpt "The procedures under test are the INSTALLED cenci plugin's, not the runner checkout's. \`codex plugin list\`:"
rpt '```text'
rpt "${PLUGIN_LIST}"
rpt '```'

# ── Scenarios ───────────────────────────────────────────────────────────
rpt ""
rpt "### ci-repair, restart and headless babysit-attention"
table_head "scenario"
scenario_ci
scenario_restart
scenario_attention

# ── Summary and output ──────────────────────────────────────────────────
rpt ""
rpt "### Summary"
rpt ""
rpt "- ok=${N_OK} failed=${N_FAIL} inconclusive=${N_INCONCLUSIVE} not-run=${N_NOTRUN}"
rpt "- An inconclusive row is never a success: the procedure was not shown to have run, or nothing observable resulted. A row is judged from the fixture's state, not from the model's narration (the attention rows judge the printed decision itself)."
rpt "- The acceptance run needs every row above ok, on the installed plugin named in the table at the top."

MISSING_REQUIRED=""
for lbl in "${REQUIRED_ROWS[@]}"; do
    row_is_ok "${lbl}" || MISSING_REQUIRED="${MISSING_REQUIRED}${MISSING_REQUIRED:+; }${lbl}"
done

if [[ "${N_FAIL}" -gt 0 ]]; then
    RESULT_LINE="RESULT: FAIL (${N_FAIL} failed rows)"
    RESULT_RC=1
elif [[ "${N_OK}" -eq 0 ]]; then
    RESULT_LINE="RESULT: INCONCLUSIVE (rows ok: ${N_OK}, inconclusive: ${N_INCONCLUSIVE}, not run: ${N_NOTRUN}); nothing was proven"
    RESULT_RC=3
elif [[ -n "${MISSING_REQUIRED}" ]]; then
    RESULT_LINE="RESULT: INCONCLUSIVE (rows ok: ${N_OK}, inconclusive: ${N_INCONCLUSIVE}, not run: ${N_NOTRUN}); required rows not ok: ${MISSING_REQUIRED}"
    RESULT_RC=3
else
    RESULT_LINE="RESULT: OK (rows ok: ${N_OK}, inconclusive: ${N_INCONCLUSIVE}, not run: ${N_NOTRUN})"
    RESULT_RC=0
fi
rpt ""
rpt "${RESULT_LINE}"

SANITIZED="${FIXTURE_ROOT}/report.sanitized.md"
san < "${REPORT}" > "${SANITIZED}" || {
    echo "FAIL: could not sanitize the report" >&2
    exit 1
}

# token_present <file> <name> -- 0 present, 1 absent, 2 the search failed. A
# plain case-insensitive search with alphanumeric token edges, independent of (and
# stricter than) the sanitizer, so a sanitizer edge bug cannot hide itself.
token_present() {
    local f="$1" name="$2" i c esc="" rc
    local LC_ALL=C
    for ((i = 0; i < ${#name}; i++)); do
        c="${name:i:1}"
        case "${c}" in
            [A-Za-z0-9]) esc="${esc}${c}" ;;
            \^) esc="${esc}\\^" ;;
            \\) esc="${esc}\\\\" ;;
            \]) esc="${esc}[]]" ;;
            *) esc="${esc}[${c}]" ;;
        esac
    done
    grep -q -i -E -e "(^|[^A-Za-z0-9])${esc}([^A-Za-z0-9]|\$)" -- "${f}"
    rc=$?
    [[ "${rc}" -le 1 ]] || return 2
    return "${rc}"
}

# leaked_identity <file> -- name the first identity string that survived
# sanitization, without printing it. Returns 0 when something survived or the
# check itself could not run. Names shorter than IDENTITY_MIN_STRICT characters
# are skipped from this strict pass only (the sanitizer still redacts them).
IDENTITY_MIN_STRICT=3
leaked_identity() {
    local f="$1" lit rc name label stripped
    for lit in "${HOME_SAN}" "${HOME_REAL}" "${FIXTURE_ROOT}" "${FIXTURE_REAL}"; do
        [[ -n "${lit}" ]] || continue
        grep -F -- "${lit}" "${f}" >/dev/null
        rc=$?
        if [[ "${rc}" -eq 0 ]]; then
            echo "a home or fixture path"
            return 0
        elif [[ "${rc}" -ne 1 ]]; then
            echo "an unverifiable file (grep failed)"
            return 0
        fi
    done
    # The sanitizer's own placeholders are not leaks.
    stripped="${f}.stripped"
    sed -E 's/<(HOME|USER|HOST|FIXTURE|UUID|TOKEN)>/ /g' -- "${f}" > "${stripped}" || { echo "an unverifiable file (placeholder strip failed)"; return 0; }
    for name in "${USER_NAME}" "${HOST_NAME}" "${HOST_NAME%%.*}" "${HOST_SHORT}"; do
        [[ "${#name}" -ge "${IDENTITY_MIN_STRICT}" ]] || continue
        label="the host name"
        [[ "${name}" == "${USER_NAME}" ]] && label="the user name"
        token_present "${stripped}" "${name}"
        rc=$?
        if [[ "${rc}" -eq 0 ]]; then
            echo "${label}"
            return 0
        elif [[ "${rc}" -ne 1 ]]; then
            echo "an unverifiable identity check"
            return 0
        fi
    done
    return 1
}

if LEAKED="$(leaked_identity "${SANITIZED}")"; then
    rm -f "${SANITIZED}"
    echo "FAIL: the sanitized report still contains ${LEAKED}; refusing to print or save it" >&2
    exit 1
fi

cat "${SANITIZED}"
if [[ -n "${CENCI_CODEX_NATIVE_OUT:-}" ]]; then
    cp "${SANITIZED}" "${CENCI_CODEX_NATIVE_OUT}" || {
        echo "FAIL: could not write ${CENCI_CODEX_NATIVE_OUT}" >&2
        exit 1
    }
fi

exit "${RESULT_RC}"
