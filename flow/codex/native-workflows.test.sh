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
#                retry option, the attempt count read through the stubbed
#                `cenci babysit status`, no `cenci babysit stop` call, no GitHub write, the
#                remote untouched
#
# and, for the Plan-mode, handoff and goal rows (#1235), on a ticketless implement
# fixture of its own per scenario (a bare remote, a clone, a runner-persisted plan
# artifact and `planned` checkpoint; the gate is green or red):
#
#   probe        `codex --help`, `codex exec --help` and `codex features list`
#                scanned for a Plan/collaboration-mode option; the result is recorded
#                (a version-verified control is a Followup, not part of the ticket)
#   plan mode    the planning request in Default mode (`codex exec` has no Plan
#                mode): the exact refusal line, zero tool calls, nothing written
#   persist      `persist` in a fresh process: the exact refusal line, zero tool
#                calls, nothing written
#   apply        a new process on the runner-persisted artifact and checkpoint:
#                `verify` passes and the checkpoint advances past `planned`; with
#                the artifact edited after approval, the digest-mismatch line and
#                zero writes
#   goal create  green gate, a stubbed `gh pr create` that succeeds: one
#                create_goal with the approved-plan objective, checkpoint goal
#                `created`, update_goal complete after `gh pr create` and before
#                the stubbed `cenci babysit`
#   goal skip    a goal seeded in turn 1, then `apply` resumed (`codex exec resume`)
#                against a red gate: no create_goal, no update_goal, checkpoint goal
#                `skipped-active` and status `needs-input`
#
# The goal rows are judged from the tool calls in the session rollout
# ($CODEX_HOME/sessions) or the event stream, never from what the model says; a
# scenario in which no goal call is visible is inconclusive.
#
# The fixture lives under /var/tmp (never /tmp: the guards allowlist /tmp/*): a
# local bare remote with a PR branch whose gate (`sh check.sh`) fails, a clone of it
# as the launch directory, a stubbed `gh` and a stubbed `cenci` first on codex's
# PATH. The stubbed `gh` answers reads and REFUSES and logs every write; the stubbed
# `cenci` logs every call (`babysit status` answers with the attempt count, `babysit stop`
# fails like a host nack, as a forwarded stop does when the host daemon rejects it).
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
# Timeouts: CNW_RUN_TIMEOUT (default 900 s) bounds each run; CNW_APPLY_TIMEOUT
# (default CNW_RUN_TIMEOUT when set, else 2400 s) bounds the long `apply` runs. A
# timeout is reported as an inconclusive row with its return code (124).
#
# Output: a sanitized markdown fragment on stdout, and also in the file named by
# $CENCI_CODEX_NATIVE_OUT when set. The fragment ends with one RESULT line:
#   RESULT: OK            exit 0  the five positive ci-repair/attention rows and
#                                 every Plan-mode, handoff and goal row are ok
#                                 (fast-forward advance, gate green on the pushed
#                                 head, checkpoint complete with the pushed SHA,
#                                 restart adds no commit, both attention options
#                                 printed; the probe recorded, both refusal lines
#                                 with zero tool calls and writes, apply verifies,
#                                 the tampered artifact refused with zero writes,
#                                 the goal created then completed in order, the
#                                 goal skipped when one exists) and no row failed
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
FIXTURE_ATTEMPTS=3   # ciFixAttempts the stubbed `cenci babysit status` reports and the attention rows expect to see
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
HO="${FIXTURE_ROOT}/handoff"
mkdir -p "${REMOTE}" "${BIN}" "${LOGS}" "${RUNS}" "${STATE_HOME}" "${GH_CONFIG}" "${GATE_HOME}" "${GATE_TMP}" "${HO}" || {
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
# The Plan-mode, handoff and goal rows (#1235) are required too.
LBL_PROBE="startup-control probe: Plan/collaboration-mode option recorded"
LBL_PM_LINE="plan-mode self-check: exact refusal line printed"
LBL_PM_CALLS="plan-mode self-check: zero tool calls"
LBL_PM_WRITE="plan-mode self-check: nothing written"
LBL_PS_LINE="persist (fresh process): exact refusal line printed"
LBL_PS_CALLS="persist (fresh process): zero tool calls"
LBL_PS_WRITE="persist (fresh process): nothing written"
LBL_TP_LINE="apply (tampered artifact): digest-mismatch line printed"
LBL_TP_WRITE="apply (tampered artifact): zero writes"
LBL_AV="apply (new process): verify passes and the checkpoint advances past planned"
LBL_GC="goal create: create_goal once, with the approved-plan objective"
LBL_GK="goal create: checkpoint records goal=created"
LBL_GP="goal complete: update_goal complete only after a successful gh pr create"
LBL_GB="goal complete: update_goal complete before cenci babysit"
LBL_SC="goal skip: no create_goal while a goal exists"
LBL_SU="goal skip: no update_goal"
LBL_SK="goal skip: checkpoint records skipped-active and needs-input"
REQUIRED_ROWS=("${LBL_CI_FF}" "${LBL_CI_GATE}" "${LBL_CI_CKPT}" "${LBL_RESTART}" "${LBL_A_OPTS}"
    "${LBL_PROBE}" "${LBL_PM_LINE}" "${LBL_PM_CALLS}" "${LBL_PM_WRITE}" "${LBL_PS_LINE}" "${LBL_PS_CALLS}" "${LBL_PS_WRITE}"
    "${LBL_TP_LINE}" "${LBL_TP_WRITE}" "${LBL_AV}" "${LBL_GC}" "${LBL_GK}" "${LBL_GP}" "${LBL_GB}" "${LBL_SC}" "${LBL_SU}" "${LBL_SK}")

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

    write_stubs || return 1
}

# write_stubs -- `gh` and `cenci` for codex's PATH, configured through a sidecar
# env file so the scripts themselves are fixed text.
write_stubs() {
    printf 'NW_REMOTE=%q\nNW_LOGS=%q\nNW_BRANCH=%q\nNW_SLUG=%q\nNW_ATTEMPTS=%q\n' \
        "${REMOTE}" "${LOGS}" "${PR_BRANCH}" "${FIXTURE_SLUG}" "${FIXTURE_ATTEMPTS}" > "${BIN}/fixture.env" || return 1
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
    "pr create")
        # A help request creates nothing: it is answered with usage text and is
        # neither a write (refused) nor a successful creation (logged).
        for a in "$@"; do
            case "${a}" in
                --help | -h)
                    echo "Usage: gh pr create [flags] (fixture stub: help only, no pull request was created)"
                    exit 0
                    ;;
            esac
        done
        # The one write the goal scenarios allow, and only while the runner has
        # switched the ok mode on (the sentinel file): every other scenario keeps
        # refusing it. The ok mode logs the call and answers with a PR URL.
        [ -f "${NW_LOGS}/pr-create-ok" ] || refuse "$@"
        printf '%s\n' "$*" >> "${NW_LOGS}/gh-pr-create.log"
        printf 'https://github.com/%s/pull/8\n' "${NW_SLUG}"
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
# Fixture cenci: every call is logged; `babysit status` answers the way a forwarded
# status does (one JSON line, no path); `babysit stop` fails like a host nack.
. "${0%/*}/fixture.env"
printf '%s\n' "$*" >> "${NW_LOGS}/cenci.log"
case "$1 $2" in
    "babysit status")
        printf '{"pr":%s,"repo":"%s","status":"running","ciFixAttempts":%s,"conflictFixAttempts":0,"fixCap":3,"supervisorLive":true}\n' \
            "${3:-0}" "${NW_SLUG}" "${NW_ATTEMPTS}"
        ;;
    "babysit stop")
        echo "not stopped: the host supervisor could not be stopped (fixture stub)" >&2
        exit 1
        ;;
    "babysit "[0-9]*)
        echo "cenci: babysit armed for PR $2 (fixture stub: no supervisor was started)"
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

# thread_id_of <events-file> -- the thread id the run reported in its first
# `thread.started` event. Non-zero, nothing on stdout, when there is none or it is
# not a plain identifier (it is later passed to `codex exec resume` as an argument).
thread_id_of() {
    local id
    id="$(jq -R -r 'fromjson?
        | select(type == "object" and .type == "thread.started" and ((.thread_id // null) | type) == "string")
        | .thread_id' "$1" 2>/dev/null)" || return 1
    id="${id%%$'\n'*}"
    [[ "${id}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || return 1
    printf '%s' "${id}"
}

# run_codex <session-cwd> <prompt> -- one `codex exec --json` run with the
# fixture's stubs first on PATH and the supervisor-state directory exported. The
# GitHub credentials are removed and gh is pointed at an unreachable host with an
# empty config directory, so a call that bypasses the stubbed gh fails closed.
# Sets CODEX_RC and CODEX_NOTE. Returns 1 when the run could not be set up or codex
# could not be started (rc 126/127); any other rc still gets judged from the
# fixture's state, with CODEX_NOTE as the reason a row is inconclusive.
RUN_KIND="exec"   # exec | resume (`codex exec resume <thread-id>`, run from inside the directory)
RESUME_THREAD=""  # the thread id a resume run continues (the seed turn's thread.started id)
RUN_LIMIT=""      # seconds; empty means CNW_RUN_TIMEOUT, else 900
declare -a CODEX_RESUME_ARGS=()
if [[ -n "${TRUST_FLAG}" ]]; then
    CODEX_RESUME_ARGS=("${TRUST_FLAG}" --dangerously-bypass-approvals-and-sandbox)
else
    CODEX_RESUME_ARGS=(-c 'sandbox_mode="workspace-write"' -c "sandbox_workspace_write.writable_roots=[\"${FIXTURE_ROOT}\"]")
fi

run_codex() {
    local cwd="$1" prompt="$2" first="" limit="${RUN_LIMIT:-${CNW_RUN_TIMEOUT:-900}}"
    local -a timeout_cmd=()
    SCEN_N=$((SCEN_N + 1))
    SCEN_DIR="${RUNS}/${SCEN_N}"
    EVENTS="${SCEN_DIR}/events.jsonl"
    AGENT_TEXT=""
    CODEX_RC=0
    CODEX_NOTE="codex did not run"
    mkdir -p "${SCEN_DIR}" || return 1
    : > "${SCEN_DIR}/start.marker" || return 1
    if command -v timeout >/dev/null 2>&1; then
        timeout_cmd=(timeout "${limit}")
    fi
    if [[ "${RUN_KIND}" == resume ]]; then
        if [[ ! "${RESUME_THREAD}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
            CODEX_NOTE="no valid thread id to resume (seed thread id not captured)"
            return 1
        fi
        # `codex exec resume` takes no -C: it is started from inside the directory and
        # continues the thread it is given by id (never `--last`, which could pick up
        # another thread), whose id the caller read from the seed turn.
        (
            cd "${cwd}" || exit 126
            exec env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN \
                GH_HOST=fixture.invalid GH_CONFIG_DIR="${GH_CONFIG}" \
                PATH="${BIN}:${PATH}" GH_REPO="${FIXTURE_SLUG}" XDG_STATE_HOME="${STATE_HOME}" \
                ${timeout_cmd[@]+"${timeout_cmd[@]}"} \
                codex exec resume "${RESUME_THREAD}" --json --skip-git-repo-check \
                "${CODEX_RESUME_ARGS[@]}" "${prompt}"
        ) > "${EVENTS}" 2> "${SCEN_DIR}/stderr" < /dev/null
    else
        env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN \
            GH_HOST=fixture.invalid GH_CONFIG_DIR="${GH_CONFIG}" \
            PATH="${BIN}:${PATH}" GH_REPO="${FIXTURE_SLUG}" XDG_STATE_HOME="${STATE_HOME}" \
            ${timeout_cmd[@]+"${timeout_cmd[@]}"} \
            codex exec --json --skip-git-repo-check -C "${cwd}" \
            "${CODEX_MODE_ARGS[@]}" "${prompt}" \
            > "${EVENTS}" 2> "${SCEN_DIR}/stderr" < /dev/null
    fi
    CODEX_RC=$?
    AGENT_TEXT="$(agent_texts "${EVENTS}")" || AGENT_TEXT=""
    case "${CODEX_RC}" in
        0) CODEX_NOTE="codex rc=0" ;;
        124) CODEX_NOTE="codex timed out after ${limit}s (rc=124)" ;;
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
    : > "${LOGS}/gh-pr-create.log" || return 1
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
    printf '$cenci:babysit-attention %s CI retry cap reached' "${PR_NUMBER}"
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
        # The stubbed `cenci babysit status` answers with the count, so it must be
        # stated: "not readable" is a failure here, not an alternative.
        if grep -qiE "(^|[^0-9])(${FIXTURE_ATTEMPTS}[[:space:]]+(attempt|fix|retr)|attempts?[^0-9]{0,20}${FIXTURE_ATTEMPTS}([^0-9]|\$))" <<< "${text_nohex}"; then
            record "${prefix}: attempt count" "ok" "the summary states the attempt count from cenci babysit status (${FIXTURE_ATTEMPTS})"
        else
            record "${prefix}: attempt count" "FAIL" "the summary does not state the attempt count (${FIXTURE_ATTEMPTS}) the stubbed cenci babysit status reports"
        fi
    fi

    if ! cenci_calls="$(cat "${LOGS}/cenci.log" 2>/dev/null)"; then
        record "${prefix}: no babysit stop call" "FAIL" "the stubbed cenci's call log is unreadable, so a babysit stop call cannot be ruled out"
    else
        stop_calls="$(grep -E '^babysit stop' <<< "${cenci_calls}")"
        if [[ -n "${stop_calls}" ]]; then
            record "${prefix}: no babysit stop call" "FAIL" "cenci babysit stop was called: ${stop_calls//$'\n'/; }"
        elif [[ "${attempted}" == yes ]]; then
            record "${prefix}: no babysit stop call" "ok" "no cenci babysit stop call"
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

# ── Plan mode, handoff and goal lifecycle (#1235) ───────────────────────
# Each scenario below has a ticketless implement fixture of its own (a bare remote,
# a clone, and for the apply scenarios a runner-persisted plan artifact and a
# `planned` checkpoint), so none of them can change what another one judges.
HO_SLUG="native-note"
HO_PLAN=".plans/${HO_SLUG}.md"
HO_CKPT=".cenci/checkpoints/implement-${HO_SLUG}.json"
HO_REPO=""
HO_REMOTE=""
SEED_OBJECTIVE="cenci-native seeded goal (unrelated to any plan)"
PLAN_MODE_LINE="cenci: \$cenci:implement plans only in Plan mode — type /plan and resend: /plan \$cenci:implement ${HO_SLUG}"
PERSIST_LINE="cenci: no approved plan in this session — type /plan and resend: /plan \$cenci:implement ${HO_SLUG}"
DIGEST_TEXT="plan artifact changed since approval (digest mismatch)"
PROBE_RE='collaboration|plan[ _-]?mode|(^|[^[:alnum:]])--plan([^[:alnum:]]|$)'

# The goal tools as the call list names them. A line of the call list is
# "tool<TAB>arguments". A completion is not matched by text: finish_calls parses
# the arguments of each update_goal and marks only `status == "complete"`.
T_GET=$'^get_goal\t'
T_CREATE=$'^create_goal\t'
T_UPDATE=$'^update_goal\t'
# The commands the order rows look for. They are matched only at the START of a
# command of a shell tool call (first_command_call), never as a mention.
CMD_PR='gh pr create'
CMD_BABYSIT='cenci babysit'
CMD_VERIFY='node +[^;&|]*checkpoint\.mjs[^;&| ]* +verify'
# The note of every row that rests on a call list that could not be extracted.
CALLS_UNREADABLE_NOTE="the tool-call lists could not be read (an extraction failed), so no row that rests on them can be judged"

prompt_plan() { printf '$cenci:implement %s' "${HO_SLUG}"; }
prompt_persist() { printf '$cenci:implement persist %s' "${HO_SLUG}"; }
prompt_apply() { printf '$cenci:implement apply %s' "${HO_SLUG}"; }
prompt_seed() {
    printf 'Call the create_goal tool once with the objective "%s" and then stop. Make no other tool call and edit nothing.' "${SEED_OBJECTIVE}"
}

# rows_inconclusive <reason> <label>... -- one inconclusive row per label.
rows_inconclusive() {
    local reason="$1" lbl
    shift
    for lbl in "$@"; do
        record "${lbl}" "inconclusive" "${reason}"
    done
}

# run_apply <cwd> <prompt> [exec|resume [thread-id]] -- a long `apply` run with its
# own timeout. A resume run continues the thread <thread-id>.
run_apply() {
    local rc
    RUN_KIND="${3:-exec}"
    RESUME_THREAD="${4:-}"
    RUN_LIMIT="${CNW_APPLY_TIMEOUT:-${CNW_RUN_TIMEOUT:-2400}}"
    run_codex "$1" "$2"
    rc=$?
    RUN_KIND="exec"
    RESUME_THREAD=""
    RUN_LIMIT=""
    return "${rc}"
}

# ho_persist -- the artifact and the `planned` checkpoint the Persist step would
# write, written by the runner with the plugin's own checkpoint helper, so the
# apply under test starts in a new process on exactly what persist leaves.
ho_persist() {
    local digest
    mkdir -p "${HO_REPO}/.plans" || return 1
    {
        printf '%s\n' '---' 'version: 1' 'mode: ticketless' "slug: ${HO_SLUG}" 'status: planned' 'approval: human' '---' ''
        printf '%s\n' '## Ticket Details' 'Ticketless task: append the line `native` to NOTES.md.' ''
        printf '%s\n' '## Implementation Plan' '### Summary' 'Append one line, `native`, to NOTES.md.' ''
        printf '%s\n' '### Files to Modify' '- `NOTES.md`: append the line `native`.' ''
        printf '%s\n' '### Test Strategy' 'The project gate (`sh check.sh`) must stay green.' ''
        printf '%s\n' '## Architectural Context' 'A one-file fixture; no conventions apply.' ''
        printf '%s\n' '## Choices' 'No question was asked; every default was accepted.'
    } > "${HO_REPO}/${HO_PLAN}" || return 1
    digest="$(cd "${HO_REPO}" && node "${FLOW_DIR}/codex/checkpoint.mjs" digest "${HO_PLAN}")" || return 1
    [[ "${digest}" =~ ^[0-9a-f]{64}$ ]] || return 1
    (cd "${HO_REPO}" && node "${FLOW_DIR}/codex/checkpoint.mjs" init "${HO_CKPT}" implement "${HO_SLUG}" planned \
        --plan-path "${HO_PLAN}" --plan-digest "${digest}" > /dev/null) || return 1
}

# ho_fixture <name> <green|red> [persist] -- a bare remote (origin path ends in
# cenci-native/fixture.git, like the ci-repair fixture's) and a clone of it as
# HO_REPO. The gate is `sh check.sh`; with `persist` the artifact and checkpoint
# are written too.
ho_fixture() {
    local name="$1" gate="$2" persist="${3:-}" root seed
    root="${HO}/${name}"
    seed="${root}/seed"
    HO_REMOTE="${root}/remotes/${FIXTURE_SLUG}.git"
    HO_REPO="${root}/repo"
    local seedgit=(git -C "${seed}" -c user.name=cenci-native -c user.email=cenci-native@localhost -c commit.gpgsign=false)
    mkdir -p "${root}/remotes/cenci-native" "${seed}/.cenci" || return 1
    git init -q "${seed}" || return 1
    git -C "${seed}" symbolic-ref HEAD refs/heads/main || return 1
    printf '{"gateCommand":"sh check.sh"}\n' > "${seed}/.cenci/config.json" || return 1
    printf '# fixture\n' > "${seed}/AGENTS.md" || return 1
    printf '.worktrees/\n.cenci/checkpoints/\n' > "${seed}/.gitignore" || return 1
    printf 'notes\n' > "${seed}/NOTES.md" || return 1
    if [[ "${gate}" == green ]]; then
        printf '%s\n' "echo '${CHECK_NAME}: ok'" > "${seed}/check.sh" || return 1
    else
        printf '%s\n' "echo '${CHECK_NAME}: FAILED: the baseline is red'" 'exit 1' > "${seed}/check.sh" || return 1
    fi
    "${seedgit[@]}" add -A || return 1
    "${seedgit[@]}" commit -q -m "seed" || return 1
    git clone -q --bare "${seed}" "${HO_REMOTE}" || return 1
    git clone -q "${HO_REMOTE}" "${HO_REPO}" || return 1
    git -C "${HO_REPO}" config user.name cenci-native || return 1
    git -C "${HO_REPO}" config user.email cenci-native@localhost || return 1
    git -C "${HO_REPO}" config commit.gpgsign false || return 1
    if [[ -n "${persist}" ]]; then
        ho_persist || return 1
    fi
}

# repo_snapshot <repo> -- refs and `git status --porcelain --ignored`: a clean
# repository still yields a non-empty reading (the refs), so two failed reads can
# never compare equal.
repo_snapshot() {
    local refs st
    refs="$(git -C "$1" for-each-ref --format='%(refname) %(objectname)' 2>/dev/null)" || return 1
    [[ -n "${refs}" ]] || return 1
    st="$(git -C "$1" status --porcelain --ignored 2>/dev/null)" || return 1
    printf 'refs:\n%s\nstatus:\n%s\n' "${refs}" "${st}"
}

# ho_state -- everything a run that must write nothing is judged against: the
# launch repository, the artifact and checkpoint contents and the remote's refs.
ho_state() {
    local snap art ck rrefs
    snap="$(repo_snapshot "${HO_REPO}")" || return 1
    art="$(git hash-object "${HO_REPO}/${HO_PLAN}" 2>/dev/null)" || return 1
    ck="$(git hash-object "${HO_REPO}/${HO_CKPT}" 2>/dev/null)" || return 1
    rrefs="$(git -C "${HO_REMOTE}" for-each-ref --format='%(refname) %(objectname)' 2>/dev/null)" || return 1
    printf '%s\nartifact:%s\ncheckpoint:%s\nremote:\n%s\n' "${snap}" "${art}" "${ck}" "${rrefs}"
}

# state_changes <before> <after> -- the first changed lines, for a row's detail.
state_changes() {
    local f1="${SCEN_DIR}/state.before" f2="${SCEN_DIR}/state.after"
    if ! printf '%s\n' "$1" > "${f1}" || ! printf '%s\n' "$2" > "${f2}"; then
        printf 'unreadable'
        return 0
    fi
    diff "${f1}" "${f2}" | grep -E '^[<>]' | sed -n '1,4p' | tr '\n' ';'
}

# ho_ckpt_state -- "status|phase|goal" of the persisted checkpoint; non-zero when
# it cannot be read.
ho_ckpt_state() {
    local f="${HO_REPO}/${HO_CKPT}"
    [[ -f "${f}" ]] || return 1
    jq -r '[(.status // ""), (.phase // ""), (.goal // "")] | join("|")' "${f}" 2>/dev/null
}

# ── Tool calls: the session rollout, or the event stream ─────────────────
# Goal calls are judged from what the tools were asked, never from what the model
# says. Two sources are read, whichever shows the call: the session rollout
# ($CODEX_HOME/sessions/**/rollout-*.jsonl, the files of THIS run's directory
# written since the run started) and the `codex exec --json` event stream. Both
# are reduced to one list, one line per tool call in order, each call once (an
# item reported as started and completed counts once): "id<TAB>tool<TAB>arguments".
# The tool is get_goal, create_goal or update_goal for a goal call (whatever the
# namespace in front of it) and the call's own name otherwise ("shell" for a command).
# The arguments of a shell call are reduced to the plain command text: the JSON
# wrapper ({"cmd": ...} or {"command": [...]}) and a `bash -lc '<script>'` wrapper
# are removed and a line break becomes " ; ", so a command start can be recognised
# whatever the client's spelling. An extraction failure is the caller's to see:
# the jq exits non-zero (a tool call it cannot read is never skipped silently).
# A line that cannot be parsed is never skipped silently. objs($mode) parses the
# lines of a stream: in "rollout" mode every non-empty line must be a JSON object;
# in "events" mode a non-empty line that starts with `{` must parse (any other line
# is client noise, not an event, and is ignored). The jq exits non-zero on a line
# that breaks the rule, so the caller sets CALLS_EXTRACT_FAILED.
PARSE_JQ='
  def objs($mode):
    split("\n") | map(select(test("\\S")))
    | map(. as $l
          | (try {v: ($l | fromjson)} catch {bad: true}) as $r
          | if $r.bad then (if $mode == "rollout" or ($l | test("^\\s*\\{")) then error("unparseable line") else empty end)
            elif ($r.v | type) != "object" then (if $mode == "rollout" then error("line is not a JSON object") else empty end)
            else $r.v end);
'
CALLS_JQ="${PARSE_JQ}"'
  def isshell: . as $n | ["shell", "exec_command", "shell_command", "local_shell", "container.exec"] | index($n) != null;
  def shelltext:
    (if type == "string" and test("^\\s*[\\[{]") then (. as $orig | try fromjson catch $orig) else . end) as $a
    | (if ($a | type) == "object" then ($a.cmd // $a.command // $a.input // "") else $a end) as $c
    | (if ($c | type) == "array"
         then (if ($c | length) >= 3 and ($c[1] | tostring | test("^-[a-z]*c$")) then ($c[-1] | tostring) else ($c | map(tostring) | join(" ")) end)
         else ($c | if type == "string" then . else tojson end) end)
    | sub("^(?:/[^ ]*/)?(?:ba|z)?sh +-[a-z]*c +\\x27(?<s>[\\s\\S]*)\\x27\\s*$"; .s)
    | sub("^(?:/[^ ]*/)?(?:ba|z)?sh +-[a-z]*c +\"(?<s>[\\s\\S]*)\"\\s*$"; .s)
    | gsub("\\s*\\n\\s*"; " ; ");
  objs($mode)
  | map((.payload // .item // .) as $p
        | select(($p | type) == "object"
                 and (["function_call", "custom_tool_call", "mcp_tool_call", "command_execution", "local_shell_call", "tool_call", "web_search"]
                      | index($p.type // "")))
        | ($p.name // $p.tool // "shell") as $n
        | (([$n | capture("(?<g>(get|create|update)_goal)$").g][0]) // $n) as $t
        | (($p.arguments // $p.input // $p.command // $p.action // "")) as $raw
        | {id: ($p.id // $p.call_id // ""),
           tool: $t,
           args: (if ((($p.type // "") | IN("command_execution", "local_shell_call")) or ($t | isshell))
                  then ($raw | shelltext)
                  else ($raw | if type == "string" then . else tojson end) end)})
  | reduce .[] as $r ([]; if $r.id != "" and (map(.id) | index($r.id)) != null then . else . + [$r] end)
  | .[] | [.id, .tool, (.args | gsub("[\t\r\n]"; " "))] | join("\t")
'

#
# extract_calls <rollout|events> <file>... -- the call list of the files, in order.
# Non-zero when a file cannot be read, a rollout file does not end in a newline (its
# last line may be cut short, and read together with the next file it would fuse
# with that file's first line), or a line breaks the objs() rule above.
extract_calls() {
    local mode="$1" f last
    shift
    for f in "$@"; do
        if [[ "${mode}" == rollout ]]; then
            last="$(tail -c 1 -- "${f}")" || return 1
            [[ -z "${last}" ]] || return 1
        fi
    done
    jq -R -s -r --arg mode "${mode}" "${CALLS_JQ}" "$@"
}

# count_events <file> -- how many lines of the event stream are typed JSON events
# (every `codex exec --json` event carries a string `type`). Non-zero when the
# stream cannot be read or a line that starts with `{` is not JSON.
count_events() {
    jq -R -s -r --arg mode events "${PARSE_JQ}"'
        objs($mode) | map(select(((.type? // null) | type) == "string")) | length' "$1"
}

codex_home() {
    printf '%s' "${CODEX_HOME:-${HOME:-}/.codex}"
}

CALLS_SRC=""      # rollout | events: where the goal calls were read
CALLS_ROLL_N=0    # rollout files read for this run
CALLS_FILE=""     # id/tool/arguments of the chosen source
CALLS_ANY=""      # tool/arguments of both sources together (for "zero tool calls")
CALLS_TSV=""      # tool/arguments of the chosen source without an earlier turn's calls
CALLS_CMP=""      # one line per CALLS_TSV line: 1 for an update_goal whose status is exactly "complete"
CALLS_EXTRACT_FAILED=0  # 1 when reading either source failed: the lists are then unreadable, not empty
CALLS_EVENTS_N=0        # typed events in the event stream

# collect_calls <cwd> [rollout|events] -- read the current run's tool calls. The
# source is the rollout when it shows a goal call, else the event stream, unless
# one is forced. Returns non-zero when the lists could not be written. An
# extraction (jq) failure does not return non-zero: it sets CALLS_EXTRACT_FAILED,
# and every row that rests on the lists must then be inconclusive.
collect_calls() {
    local real f first root out="${SCEN_DIR}" forced="${2:-}"
    local -a rolls=()
    real="$(cd "$1" && pwd -P)" || real="$1"
    CALLS_SRC="events"
    CALLS_ROLL_N=0
    CALLS_EXTRACT_FAILED=0
    CALLS_EVENTS_N=0
    : > "${out}/calls.roll" || return 1
    root="$(codex_home)/sessions"
    : > "${out}/rollouts.list" || return 1
    if [[ -d "${root}" ]]; then
        find "${root}" -type f -name 'rollout-*.jsonl' -newer "${out}/start.marker" -print 2> /dev/null | sort > "${out}/rollouts.list"
        # A rollout belongs to this run when its FIRST line (the session_meta) names
        # this directory; a later unparseable line must not make the file look like
        # another run's. Such a file is then read in full below and, when it cannot
        # be parsed, sets CALLS_EXTRACT_FAILED.
        while IFS= read -r f; do
            [[ -n "${f}" ]] || continue
            first="$(head -n 1 -- "${f}")" || first=""
            if [[ -n "${first}" ]] && jq -e --arg c "${real}" --arg d "$1" \
                'select(.type == "session_meta") | select(.payload.cwd == $c or .payload.cwd == $d)' <<< "${first}" > /dev/null 2>&1; then
                rolls+=("${f}")
            fi
        done < "${out}/rollouts.list"
    fi
    CALLS_ROLL_N="${#rolls[@]}"
    if [[ "${CALLS_ROLL_N}" -gt 0 ]]; then
        if ! extract_calls rollout "${rolls[@]}" > "${out}/calls.roll" 2> /dev/null; then
            : > "${out}/calls.roll" || return 1
            CALLS_EXTRACT_FAILED=1
        fi
    fi
    if ! extract_calls events "${EVENTS}" > "${out}/calls.events" 2> /dev/null; then
        : > "${out}/calls.events" || return 1
        CALLS_EXTRACT_FAILED=1
    fi
    if ! CALLS_EVENTS_N="$(count_events "${EVENTS}" 2> /dev/null)" || [[ ! "${CALLS_EVENTS_N}" =~ ^[0-9]+$ ]]; then
        CALLS_EVENTS_N=0
        CALLS_EXTRACT_FAILED=1
    fi
    CALLS_FILE="${out}/calls.events"
    if [[ "${forced}" == rollout ]] || { [[ -z "${forced}" ]] && grep -qE $'\t(get|create|update)_goal\t' "${out}/calls.roll"; }; then
        CALLS_SRC="rollout"
        CALLS_FILE="${out}/calls.roll"
    fi
    cat "${out}/calls.roll" "${out}/calls.events" | cut -f2- > "${out}/calls.any" || return 1
    CALLS_ANY="${out}/calls.any"
}

# finish_calls <ids-file> -- CALLS_TSV: the chosen source's calls minus those whose
# id is in <ids-file> (an earlier turn of the same thread), as "tool<TAB>arguments".
finish_calls() {
    local ids="$1"
    if [[ -z "${ids}" ]]; then
        ids="${SCEN_DIR}/none.ids"
        : > "${ids}" || return 1
    fi
    CALLS_TSV="${SCEN_DIR}/calls.tsv"
    awk -F'\t' -v ids="${ids}" \
        'FILENAME == ids { if ($1 != "") skip[$1] = 1; next } !($1 != "" && ($1 in skip)) { print $2 "\t" $3 }' \
        "${ids}" "${CALLS_FILE}" > "${CALLS_TSV}" || return 1
    # A completion is an update_goal whose arguments parse to status == "complete"
    # exactly: "incomplete", "completed" or a paused status that mentions the word
    # in a note are not one.
    CALLS_CMP="${SCEN_DIR}/calls.cmp"
    jq -R -r 'split("\t") as $f
        | if $f[0] == "update_goal"
          then (try (($f[1:] | join("\t")) | fromjson | (type == "object" and .status == "complete")) catch false) | if . then "1" else "0" end
          else "0" end' "${CALLS_TSV}" > "${CALLS_CMP}" || return 1
}

n_complete_calls() { # how many calls of CALLS_TSV complete the goal
    local n rc
    n="$(grep -c '^1$' "${CALLS_CMP}")"
    rc=$?
    [[ "${rc}" -le 1 ]] || return 1
    printf '%s' "${n}"
}

first_complete_call() { # position of the first completing call, empty when none
    local out rc
    out="$(grep -n -m1 '^1$' "${CALLS_CMP}")"
    rc=$?
    [[ "${rc}" -le 1 ]] || return 1
    printf '%s' "${out%%:*}"
}

# first_command_call <ERE> -- position of the first call of a SHELL tool whose
# command STARTS with <ERE> (at the start of the command text or after ; & | or
# an opening parenthesis or brace, past any VAR=value prefix) and does not ask for help
# (--help, -h). A call that only mentions the command (`rg 'gh pr create'`, `echo`,
# `cat`, a comment) is not that command. Empty when none; non-zero on a failure.
first_command_call() {
    local out
    out="$(CMD_RE="$1" awk -F'\t' '
        BEGIN { re = "[;&|({][ ]*([A-Za-z_][A-Za-z0-9_]*=[^ ]*[ ]+)*" ENVIRON["CMD_RE"] "([[:space:]]|$)" }
        $1 !~ /^(shell|exec_command|shell_command|local_shell|container\.exec)$/ { next }
        {
            s = ";" $2
            while (match(s, re)) {
                rest = substr(s, RSTART + RLENGTH)
                cut = rest
                if (match(cut, /[;&|]/)) { cut = substr(cut, 1, RSTART - 1) }
                if ((" " cut " ") !~ / (--help|-h) /) { print NR; exit }
                s = ";" rest
            }
        }' "${CALLS_TSV}")" || return 1
    printf '%s' "${out}"
}

n_calls_matching() { # <ERE> -- how many calls of CALLS_TSV match "tool<TAB>arguments"
    local n rc
    n="$(grep -cE -e "$1" "${CALLS_TSV}")"
    rc=$?
    [[ "${rc}" -le 1 ]] || return 1
    printf '%s' "${n}"
}

first_call_matching() { # <ERE> -- position of the first match, empty when none
    local out rc
    out="$(grep -nE -e "$1" "${CALLS_TSV}")"
    rc=$?
    [[ "${rc}" -le 1 ]] || return 1
    printf '%s' "${out%%:*}"
}

# calls_preview <file> -- the first calls, one short line each, for a row's detail.
calls_preview() {
    sed -n '1,3p' "$1" | cut -c1-90 | tr '\t' ' ' | tr '\n' ';'
}

# calls_sequence <file> -- the call list as "n. tool arguments", capped.
calls_sequence() {
    sed -n '1,60p' "$1" | cut -c1-140 | tr '\t' ' ' | awk '{ printf "%d. %s\n", NR, $0 }'
}

# ── Scenario: startup-control probe ─────────────────────────────────────
PROBE_DIR="${RUNS}/probe"

probe_one() { # <name> <codex-args>...
    local name="$1"
    local -a tcmd=()
    shift
    if command -v timeout > /dev/null 2>&1; then
        tcmd=(timeout 60)
    fi
    env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN ${tcmd[@]+"${tcmd[@]}"} \
        codex "$@" > "${PROBE_DIR}/${name}.out" 2>&1 < /dev/null
    printf '%s\n' "$?" > "${PROBE_DIR}/${name}.rc"
}

scenario_probe() {
    local name text hits="" line frc goalfeat="" note="" nonempty=0
    mkdir -p "${PROBE_DIR}" || {
        record "${LBL_PROBE}" "inconclusive" "could not create the probe directory"
        return 0
    }
    probe_one help --help
    probe_one exec-help exec --help
    probe_one features features list
    for name in help exec-help; do
        text="$(tr -d '[:space:]' < "${PROBE_DIR}/${name}.out" 2> /dev/null)" || text=""
        [[ -z "${text}" ]] || nonempty=$((nonempty + 1))
    done
    if [[ "${nonempty}" -ne 2 ]]; then
        record "${LBL_PROBE}" "inconclusive" "codex --help or codex exec --help printed nothing (rc $(cat "${PROBE_DIR}/help.rc" 2> /dev/null)/$(cat "${PROBE_DIR}/exec-help.rc" 2> /dev/null)), so no option could be scanned"
        return 0
    fi
    for name in help exec-help features; do
        line="$(grep -iE -e "${PROBE_RE}" "${PROBE_DIR}/${name}.out" 2> /dev/null | sed -n '1,2p' | tr -s '[:space:]' ' ')" || line=""
        if [[ -n "${line// /}" ]]; then
            case "${name}" in
                help) note="codex --help" ;;
                exec-help) note="codex exec --help" ;;
                *) note="codex features list" ;;
            esac
            hits="${hits}${hits:+; }${note}: ${line:0:160}"
        fi
    done
    frc="$(cat "${PROBE_DIR}/features.rc" 2> /dev/null)" || frc="?"
    goalfeat="$(grep -iE -e 'goal' "${PROBE_DIR}/features.out" 2> /dev/null | sed -n '1,2p' | tr -s '[:space:]' ' ')" || goalfeat=""
    [[ -z "${goalfeat// /}" ]] || goalfeat=" Goal-related features listed: ${goalfeat:0:160}."
    if [[ -n "${hits}" ]]; then
        record "${LBL_PROBE}" "ok" "found a candidate Plan/collaboration-mode option: ${hits}. A version-verified startup control is filed as a Followup, not added to this ticket.${goalfeat}"
    else
        record "${LBL_PROBE}" "ok" "none found in codex --help, codex exec --help or codex features list (features list rc=${frc}).${goalfeat}"
    fi
}

# ── Scenarios: the refusals (Plan mode and a fresh-process persist) ──────
# scenario_refusal <fixture-name> <prompt> <expected-line> <line-label> <calls-label> <write-label>
# The run must print exactly the refusal line, make no tool call at all and leave
# the launch repository exactly as it was.
scenario_refusal() {
    local name="$1" prompt="$2" want="$3" l_line="$4" l_calls="$5" l_write="$6"
    local before="" after="" text="" n="" changes=""
    if ! ho_fixture "${name}" green; then
        rows_inconclusive "could not build the ${name} fixture" "${l_line}" "${l_calls}" "${l_write}"
        return 0
    fi
    if ! reset_call_logs; then
        rows_inconclusive "could not reset the stub call logs, so this scenario cannot be judged" "${l_line}" "${l_calls}" "${l_write}"
        return 0
    fi
    before="$(repo_snapshot "${HO_REPO}")" || before=""
    if [[ -z "${before}" ]]; then
        rows_inconclusive "could not read the fixture repository before the run" "${l_line}" "${l_calls}" "${l_write}"
        return 0
    fi
    if ! run_codex "${HO_REPO}" "${prompt}"; then
        rows_inconclusive "${CODEX_NOTE}" "${l_line}" "${l_calls}" "${l_write}"
        return 0
    fi
    after="$(repo_snapshot "${HO_REPO}")" || after=""
    if ! collect_calls "${HO_REPO}"; then
        rows_inconclusive "the tool-call lists could not be written" "${l_line}" "${l_calls}" "${l_write}"
        return 0
    fi
    text="$(sed -E 's/[[:space:]]+$//' <<< "${AGENT_TEXT}" | grep -v '^$')"
    if [[ -z "${text}" ]]; then
        rows_inconclusive "no agent message was captured, so the run is not shown to have refused (is the cenci plugin installed? ${CODEX_NOTE})" "${l_line}" "${l_calls}"
    else
        if [[ "${text}" == "${want}" ]]; then
            record "${l_line}" "ok" "the one printed line is the contract's refusal line (${CODEX_NOTE})"
        elif grep -qxF -- "${want}" <<< "${text}"; then
            record "${l_line}" "FAIL" "the refusal line was printed with other output after or before it: '${text:0:200}'"
        else
            record "${l_line}" "FAIL" "the printed text is not the refusal line: '${text:0:200}'"
        fi
        n="$(count_lines "${CALLS_ANY}")" || n=""
        if [[ "${CALLS_EXTRACT_FAILED}" -eq 1 ]]; then
            record "${l_calls}" "inconclusive" "${CALLS_UNREADABLE_NOTE}"
        elif [[ -z "${n}" ]]; then
            record "${l_calls}" "inconclusive" "the tool-call lists could not be counted"
        elif [[ "${n}" -gt 0 ]]; then
            record "${l_calls}" "FAIL" "the run made ${n} tool call(s) before refusing: $(calls_preview "${CALLS_ANY}")"
        elif [[ "${CALLS_ROLL_N}" -eq 0 && "${CALLS_EVENTS_N}" -eq 0 ]]; then
            # An empty list proves nothing unless a list was actually read.
            record "${l_calls}" "inconclusive" "rollout not read, event stream empty: zero tool calls is not shown (${CODEX_NOTE})"
        else
            record "${l_calls}" "ok" "no tool call in the event stream or in the ${CALLS_ROLL_N} rollout file(s) read"
        fi
    fi
    if [[ -z "${after}" ]]; then
        record "${l_write}" "inconclusive" "could not read the fixture repository after the run"
    elif [[ "${after}" != "${before}" ]]; then
        changes="$(state_changes "${before}" "${after}")"
        record "${l_write}" "FAIL" "the run changed the launch repository (refs or git status --ignored): ${changes}"
    elif [[ -z "${text}" ]]; then
        record "${l_write}" "inconclusive" "nothing changed, but no agent message shows the run refused"
    else
        record "${l_write}" "ok" "git status --ignored and the refs are unchanged: no .cenci, .plans or worktree was written"
    fi
}

# ── Scenario: apply on a tampered artifact ──────────────────────────────
scenario_tamper() {
    local before="" after="" changes="" pr_writes="" refused="" babysat="" text
    if ! ho_fixture tamper green persist; then
        rows_inconclusive "could not build the tampered-artifact fixture (it needs node to persist the plan)" "${LBL_TP_LINE}" "${LBL_TP_WRITE}"
        return 0
    fi
    # The artifact changes after approval: its sha256 no longer matches the digest
    # the checkpoint recorded.
    if ! printf '\nAppended after approval.\n' >> "${HO_REPO}/${HO_PLAN}"; then
        rows_inconclusive "could not edit the persisted artifact" "${LBL_TP_LINE}" "${LBL_TP_WRITE}"
        return 0
    fi
    if ! reset_call_logs; then
        rows_inconclusive "could not reset the stub call logs, so this scenario cannot be judged" "${LBL_TP_LINE}" "${LBL_TP_WRITE}"
        return 0
    fi
    before="$(ho_state)" || before=""
    if [[ -z "${before}" ]]; then
        rows_inconclusive "could not read the fixture before the run" "${LBL_TP_LINE}" "${LBL_TP_WRITE}"
        return 0
    fi
    if ! run_apply "${HO_REPO}" "$(prompt_apply)"; then
        rows_inconclusive "${CODEX_NOTE}" "${LBL_TP_LINE}" "${LBL_TP_WRITE}"
        return 0
    fi
    after="$(ho_state)" || after=""
    pr_writes="$(cat "${LOGS}/gh-pr-create.log" 2> /dev/null)" || pr_writes="unreadable"
    refused="$(cat "${LOGS}/gh-refused.log" 2> /dev/null)" || refused="unreadable"
    babysat="$(grep -E '^babysit' "${LOGS}/cenci.log" 2> /dev/null)"
    text="$(sed -E 's/[[:space:]]+$//' <<< "${AGENT_TEXT}" | grep -v '^$')"

    if [[ -z "${text}" ]]; then
        record "${LBL_TP_LINE}" "inconclusive" "no agent message was captured, so the apply is not shown to have refused (is the cenci plugin installed? ${CODEX_NOTE})"
    elif grep -qF -- "${DIGEST_TEXT}" <<< "${text}"; then
        record "${LBL_TP_LINE}" "ok" "the printed stop carries '${DIGEST_TEXT}' (${CODEX_NOTE})"
    else
        record "${LBL_TP_LINE}" "FAIL" "the apply on a tampered artifact did not report the digest mismatch: '${text:0:200}'"
    fi
    if [[ -z "${after}" ]]; then
        record "${LBL_TP_WRITE}" "inconclusive" "could not read the fixture after the run"
    elif [[ "${after}" != "${before}" ]]; then
        changes="$(state_changes "${before}" "${after}")"
        record "${LBL_TP_WRITE}" "FAIL" "the apply wrote although verify must stop before any write: ${changes}"
    elif [[ -n "${pr_writes}" || -n "${refused}" || -n "${babysat}" ]]; then
        record "${LBL_TP_WRITE}" "FAIL" "the apply reached a GitHub write or babysit: ${pr_writes}${refused}${babysat}"
    elif ! grep -qF -- "${DIGEST_TEXT}" <<< "${text}"; then
        record "${LBL_TP_WRITE}" "inconclusive" "nothing changed, but nothing shows the apply refused"
    else
        record "${LBL_TP_WRITE}" "ok" "artifact, checkpoint, launch repository and remote refs are unchanged; no PR, babysit or worktree"
    fi
}

# ── Scenario: goal create, then complete ────────────────────────────────
GOAL_SEQ_CREATE=""
GOAL_SEQ_SKIP=""

scenario_goal_create() {
    local state="" status="" rest="" phase="" goal="" n_get n_create n_upd n_cmp n_pr cenci_log babysat="no"
    local idx_pr idx_cmp idx_bab idx_ver idx_create args no_goal advanced="no" detail want_obj obj_problem
    local -a all=("${LBL_AV}" "${LBL_GC}" "${LBL_GK}" "${LBL_GP}" "${LBL_GB}")
    if ! ho_fixture create green persist; then
        rows_inconclusive "could not build the goal-create fixture (it needs node to persist the plan)" "${all[@]}"
        return 0
    fi
    if ! reset_call_logs || ! : > "${LOGS}/pr-create-ok"; then
        rows_inconclusive "could not prepare the stub call logs, so this scenario cannot be judged" "${all[@]}"
        return 0
    fi
    run_apply "${HO_REPO}" "$(prompt_apply)"
    local started=$?
    rm -f "${LOGS}/pr-create-ok"
    if [[ "${started}" -ne 0 ]]; then
        rows_inconclusive "${CODEX_NOTE}" "${all[@]}"
        return 0
    fi
    if ! collect_calls "${HO_REPO}" || ! finish_calls ""; then
        rows_inconclusive "the tool-call lists could not be written" "${all[@]}"
        return 0
    fi
    GOAL_SEQ_CREATE="$(calls_sequence "${CALLS_TSV}")"
    state="$(ho_ckpt_state)" || state=""
    status="${state%%|*}"
    rest="${state#*|}"
    phase="${rest%%|*}"
    goal="${rest#*|}"
    [[ -n "${state}" && "${phase}" != planned ]] && advanced="yes"

    # Row: the apply verified and started.
    if [[ -z "${state}" ]]; then
        record "${LBL_AV}" "inconclusive" "the checkpoint could not be read after the apply (${CODEX_NOTE})"
    elif [[ "${advanced}" == no ]]; then
        record "${LBL_AV}" "inconclusive" "the checkpoint is still planned after the apply: verify did not pass, or the installed plugin's apply did not run (is the cenci plugin installed? ${CODEX_NOTE})"
    else
        record "${LBL_AV}" "ok" "new-process apply advanced the runner-persisted checkpoint: status=${status}, phase=${phase}, goal=${goal:-none} (${CODEX_NOTE})"
    fi
    if [[ "${advanced}" == no ]]; then
        rows_inconclusive "the apply did not advance the checkpoint, so no goal step is shown to have run (${CODEX_NOTE})" "${LBL_GC}" "${LBL_GK}" "${LBL_GP}" "${LBL_GB}"
        return 0
    fi

    if [[ "${CALLS_EXTRACT_FAILED}" -eq 1 ]]; then
        rows_inconclusive "${CALLS_UNREADABLE_NOTE}" "${LBL_GC}" "${LBL_GK}" "${LBL_GP}" "${LBL_GB}"
        return 0
    fi

    if ! n_get="$(n_calls_matching "${T_GET}")" || ! n_create="$(n_calls_matching "${T_CREATE}")" \
        || ! n_upd="$(n_calls_matching "${T_UPDATE}")" || ! n_cmp="$(n_complete_calls)" \
        || ! idx_pr="$(first_command_call "${CMD_PR}")" || ! idx_cmp="$(first_complete_call)" \
        || ! idx_bab="$(first_command_call "${CMD_BABYSIT}")" || ! idx_ver="$(first_command_call "${CMD_VERIFY}")" \
        || ! idx_create="$(first_call_matching "${T_CREATE}")"; then
        rows_inconclusive "the tool-call list could not be searched" "${LBL_GC}" "${LBL_GK}" "${LBL_GP}" "${LBL_GB}"
        return 0
    fi
    if ! n_pr="$(count_lines "${LOGS}/gh-pr-create.log")"; then
        rows_inconclusive "the stubbed gh's pr create log could not be read" "${LBL_GC}" "${LBL_GK}" "${LBL_GP}" "${LBL_GB}"
        return 0
    fi
    if ! cenci_log="$(cat "${LOGS}/cenci.log")"; then
        rows_inconclusive "the stubbed cenci's call log could not be read" "${LBL_GC}" "${LBL_GK}" "${LBL_GP}" "${LBL_GB}"
        return 0
    fi
    grep -qE '^babysit [0-9]' <<< "${cenci_log}" && babysat="yes"
    no_goal="no goal events (no get_goal, create_goal or update_goal call in the ${CALLS_ROLL_N} rollout file(s) or the event stream): the goal tools may not be exposed by codex exec at this version, or the rollout could not be read"

    # Row: one create_goal with the approved-plan objective, after verify.
    if [[ $((n_get + n_create + n_upd)) -eq 0 ]]; then
        record "${LBL_GC}" "inconclusive" "${no_goal}"
    elif [[ "${n_create}" -eq 0 ]]; then
        record "${LBL_GC}" "FAIL" "get_goal ran but create_goal was never called, although no goal existed in the new thread"
    elif [[ "${n_create}" -gt 1 ]]; then
        record "${LBL_GC}" "FAIL" "create_goal was called ${n_create} times"
    else
        args="$(grep -E -e "${T_CREATE}" "${CALLS_TSV}" | sed -n '1p' | cut -f2-)"
        # The arguments are parsed: the objective must be exactly the contract's
        # string and no token_budget may be set. A substring match would accept
        # `{"objective": "...", "token_budget": 5000}` and any longer objective.
        want_obj="Complete approved plan ${HO_PLAN} through an open PR"
        if [[ -z "${args}" ]]; then
            record "${LBL_GC}" "FAIL" "create_goal was called without arguments, so it has no objective"
        elif ! obj_problem="$(jq -r --arg o "${want_obj}" '
            if type != "object" then "the arguments are not a JSON object"
            elif .objective != $o then "the objective is not exactly \"" + $o + "\""
            elif has("token_budget") then "a token_budget is set (" + (.token_budget | tojson) + "); the goal never sets one"
            else "" end' <<< "${args}" 2> /dev/null)"; then
            record "${LBL_GC}" "inconclusive" "the create_goal arguments are not JSON, so the objective cannot be checked: ${args:0:200}"
        elif [[ -n "${obj_problem}" ]]; then
            record "${LBL_GC}" "FAIL" "${obj_problem}: ${args:0:200}"
        elif [[ -n "${idx_ver}" && "${idx_create}" -lt "${idx_ver}" ]]; then
            record "${LBL_GC}" "FAIL" "create_goal (call ${idx_create}) came before the checkpoint verify (call ${idx_ver})"
        else
            detail="one create_goal with ${args:0:160}"
            [[ -n "${idx_ver}" ]] || detail="${detail} (the verify call is not visible, so its order is not shown)"
            record "${LBL_GC}" "ok" "${detail}"
        fi
    fi

    # Row: the checkpoint records the outcome.
    if [[ $((n_get + n_create + n_upd)) -eq 0 ]]; then
        record "${LBL_GK}" "inconclusive" "${no_goal} (the checkpoint records goal=${goal:-none})"
    elif [[ "${n_create}" -eq 0 ]]; then
        record "${LBL_GK}" "inconclusive" "no create_goal call, so a created goal is not shown (the checkpoint records goal=${goal:-none})"
    elif [[ "${goal}" == created ]]; then
        record "${LBL_GK}" "ok" "create_goal ran and the checkpoint records goal=created"
    else
        record "${LBL_GK}" "FAIL" "create_goal ran but the checkpoint records goal='${goal:-none}', expected created"
    fi

    # Row: complete only after a successful gh pr create.
    if [[ $((n_get + n_create + n_upd)) -eq 0 ]]; then
        record "${LBL_GP}" "inconclusive" "${no_goal}"
    elif [[ "${n_cmp}" -eq 0 ]]; then
        if [[ "${n_pr}" -gt 0 ]]; then
            record "${LBL_GP}" "FAIL" "gh pr create succeeded (${n_pr}) but update_goal complete was never called"
        else
            record "${LBL_GP}" "inconclusive" "the run did not reach a successful gh pr create, so no completion is due (${CODEX_NOTE}; checkpoint status=${status}, phase=${phase})"
        fi
    elif [[ "${n_pr}" -eq 0 ]]; then
        record "${LBL_GP}" "FAIL" "update_goal complete was called without a successful gh pr create"
    elif [[ -z "${idx_pr}" ]]; then
        record "${LBL_GP}" "inconclusive" "gh pr create succeeded but is not visible in the call list, so the order is not shown"
    elif [[ "${idx_cmp}" -lt "${idx_pr}" ]]; then
        record "${LBL_GP}" "FAIL" "update_goal complete (call ${idx_cmp}) came before gh pr create (call ${idx_pr})"
    else
        record "${LBL_GP}" "ok" "gh pr create (call ${idx_pr}) succeeded before update_goal complete (call ${idx_cmp})"
    fi

    # Row: complete before babysit is armed.
    if [[ $((n_get + n_create + n_upd)) -eq 0 ]]; then
        record "${LBL_GB}" "inconclusive" "${no_goal}"
    elif [[ "${babysat}" == no ]]; then
        record "${LBL_GB}" "inconclusive" "cenci babysit was never run, so the order against update_goal complete is not shown (${CODEX_NOTE})"
    elif [[ "${n_cmp}" -eq 0 ]]; then
        record "${LBL_GB}" "FAIL" "cenci babysit was armed but update_goal complete was never called"
    elif [[ -z "${idx_bab}" ]]; then
        record "${LBL_GB}" "inconclusive" "cenci babysit ran but is not visible in the call list, so the order is not shown"
    elif [[ "${idx_cmp}" -gt "${idx_bab}" ]]; then
        record "${LBL_GB}" "FAIL" "cenci babysit (call ${idx_bab}) was run before update_goal complete (call ${idx_cmp})"
    else
        record "${LBL_GB}" "ok" "update_goal complete (call ${idx_cmp}) came before cenci babysit (call ${idx_bab})"
    fi
}

# ── Scenario: goal skip ─────────────────────────────────────────────────
# Turn 1 seeds a goal in a thread; turn 2 resumes that thread with `apply` against
# a RED gate. The apply must find the goal, create nothing, complete nothing and
# stop at the baseline with the checkpoint needs-input and goal skipped-active.
scenario_goal_skip() {
    local state="" status="" rest="" phase="" goal="" n_get="" n_create="" n_upd="" seed_src="" seed_n seed_dir seed_thread="" resumed_thread=""
    local -a all=("${LBL_SC}" "${LBL_SU}" "${LBL_SK}")
    if ! ho_fixture skip red persist; then
        rows_inconclusive "could not build the goal-skip fixture (it needs node to persist the plan)" "${all[@]}"
        return 0
    fi
    seed_dir="${HO}/skip"
    if ! reset_call_logs; then
        rows_inconclusive "could not reset the stub call logs, so this scenario cannot be judged" "${all[@]}"
        return 0
    fi
    # Turn 1: seed the goal.
    if ! run_codex "${HO_REPO}" "$(prompt_seed)"; then
        rows_inconclusive "the seeding turn could not run: ${CODEX_NOTE}" "${all[@]}"
        return 0
    fi
    if ! collect_calls "${HO_REPO}" || ! finish_calls ""; then
        rows_inconclusive "the tool-call lists of the seeding turn could not be written" "${all[@]}"
        return 0
    fi
    if [[ "${CALLS_EXTRACT_FAILED}" -eq 1 ]]; then
        rows_inconclusive "the seeding turn: ${CALLS_UNREADABLE_NOTE}" "${all[@]}"
        return 0
    fi
    seed_src="${CALLS_SRC}"
    seed_thread="$(thread_id_of "${EVENTS}")" || seed_thread=""
    seed_n="$(n_calls_matching "${T_CREATE}")" || seed_n=""
    if [[ -z "${seed_n}" ]] || [[ "${seed_n}" -eq 0 ]]; then
        rows_inconclusive "no goal events: no create_goal call was seen in the seeding turn (${CODEX_NOTE}), so no goal exists to skip; the goal tools may not be exposed by codex exec at this version" "${all[@]}"
        return 0
    fi
    if [[ -z "${seed_thread}" ]]; then
        rows_inconclusive "seed thread id not captured: the seeding turn's event stream has no thread.started event with a thread id, so the resumed apply cannot be shown to continue the seeded thread (${CODEX_NOTE})" "${all[@]}"
        return 0
    fi
    cut -f1 "${CALLS_FILE}" > "${seed_dir}.ids" || {
        rows_inconclusive "could not record the seeding turn's calls" "${all[@]}"
        return 0
    }
    # Turn 2: the resumed apply, in a new process on the same thread.
    if ! run_apply "${HO_REPO}" "$(prompt_apply)" resume "${seed_thread}"; then
        rows_inconclusive "the resumed apply could not run: ${CODEX_NOTE}" "${all[@]}"
        return 0
    fi
    resumed_thread="$(thread_id_of "${EVENTS}")" || resumed_thread=""
    if [[ "${resumed_thread}" != "${seed_thread}" ]]; then
        rows_inconclusive "resumed a different thread: the seed turn's thread is '${seed_thread}' but the resumed apply reports '${resumed_thread:-no thread id}', so a goal seeded there is not shown to be the one the apply saw (${CODEX_NOTE})" "${all[@]}"
        return 0
    fi
    if ! collect_calls "${HO_REPO}" "${seed_src}" || ! finish_calls "${seed_dir}.ids"; then
        rows_inconclusive "the tool-call lists of the resumed apply could not be written" "${all[@]}"
        return 0
    fi
    GOAL_SEQ_SKIP="$(calls_sequence "${CALLS_TSV}")"
    if [[ "${CALLS_EXTRACT_FAILED}" -eq 0 ]] && { ! n_get="$(n_calls_matching "${T_GET}")" || ! n_create="$(n_calls_matching "${T_CREATE}")" \
        || ! n_upd="$(n_calls_matching "${T_UPDATE}")"; }; then
        rows_inconclusive "the tool-call list could not be searched" "${all[@]}"
        return 0
    fi
    state="$(ho_ckpt_state)" || state=""
    status="${state%%|*}"
    rest="${state#*|}"
    phase="${rest%%|*}"
    goal="${rest#*|}"

    if [[ "${CALLS_EXTRACT_FAILED}" -eq 1 ]]; then
        # The checkpoint row below does not rest on the call lists and is still judged.
        rows_inconclusive "${CALLS_UNREADABLE_NOTE}" "${LBL_SC}" "${LBL_SU}"
    else
        if [[ "${n_create}" -gt 0 ]]; then
            record "${LBL_SC}" "FAIL" "create_goal was called ${n_create} time(s) in the resumed apply although the seeded goal was active"
        elif [[ "${n_get}" -eq 0 ]]; then
            record "${LBL_SC}" "inconclusive" "no get_goal call in the resumed apply, so it is not shown to have checked for a goal (${CODEX_NOTE})"
        else
            record "${LBL_SC}" "ok" "get_goal ran and no create_goal followed: the seeded objective was left as it was (${CODEX_NOTE})"
        fi
        if [[ "${n_upd}" -gt 0 ]]; then
            record "${LBL_SU}" "FAIL" "update_goal was called ${n_upd} time(s) on a goal this run did not create"
        elif [[ "${n_get}" -eq 0 ]]; then
            record "${LBL_SU}" "inconclusive" "no get_goal call in the resumed apply, so it is not shown to have reached the goal step (${CODEX_NOTE})"
        else
            record "${LBL_SU}" "ok" "no update_goal call on the skipped goal"
        fi
    fi
    if [[ -z "${state}" ]]; then
        record "${LBL_SK}" "inconclusive" "the checkpoint could not be read after the resumed apply (${CODEX_NOTE})"
    elif [[ "${phase}" == planned ]]; then
        record "${LBL_SK}" "inconclusive" "the checkpoint is still planned after the resumed apply: verify did not pass, or the apply did not run (${CODEX_NOTE})"
    elif [[ "${goal}" == skipped-active && "${status}" == needs-input ]]; then
        record "${LBL_SK}" "ok" "red baseline: status=needs-input, phase=${phase}, goal=skipped-active"
    else
        record "${LBL_SK}" "FAIL" "expected status=needs-input and goal=skipped-active after a red baseline on an active goal, got status='${status}', phase='${phase}', goal='${goal:-none}'"
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

rpt ""
rpt "### Plan mode, handoff and goal lifecycle"
table_head "scenario"
scenario_probe
scenario_refusal plan-mode "$(prompt_plan)" "${PLAN_MODE_LINE}" "${LBL_PM_LINE}" "${LBL_PM_CALLS}" "${LBL_PM_WRITE}"
scenario_refusal persist "$(prompt_persist)" "${PERSIST_LINE}" "${LBL_PS_LINE}" "${LBL_PS_CALLS}" "${LBL_PS_WRITE}"
scenario_tamper
scenario_goal_create
scenario_goal_skip
if [[ -n "${GOAL_SEQ_CREATE}" ]]; then
    rpt ""
    rpt "Tool calls of the goal create-then-complete apply, in order (tool and the start of its arguments):"
    rpt '```text'
    rpt "${GOAL_SEQ_CREATE}"
    rpt '```'
fi
if [[ -n "${GOAL_SEQ_SKIP}" ]]; then
    rpt ""
    rpt "Tool calls of the resumed goal-skip apply, in order (the seeding turn's calls removed):"
    rpt '```text'
    rpt "${GOAL_SEQ_SKIP}"
    rpt '```'
fi

# ── Summary and output ──────────────────────────────────────────────────
rpt ""
rpt "### Summary"
rpt ""
rpt "- ok=${N_OK} failed=${N_FAIL} inconclusive=${N_INCONCLUSIVE} not-run=${N_NOTRUN}"
rpt "- An inconclusive row is never a success: the procedure was not shown to have run, or nothing observable resulted. A row is judged from the fixture's state, not from the model's narration (the attention rows and the two refusal-line rows judge the printed text itself; the goal rows judge the tool calls in the rollout or event stream)."
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
