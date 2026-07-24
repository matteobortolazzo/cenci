#!/usr/bin/env bash
# Tests for maintenance-issue.sh (ticket #534). Follows the repo's shell-test
# precedent (check-workflow-permissions.test.sh): plain bash, no framework,
# PASS/FAIL counters, non-zero exit on any failure.
#
# maintenance-issue.sh has no live-GitHub dependency of its own -- it shells
# out to `gh` for every side effect. This suite stubs `gh` on PATH (a fake
# executable that records its own argv as one JSON array per line -- NDJSON --
# to a log file, then returns canned output) so the create/dedup/close/label/
# body-redaction logic can be verified deterministically, with no real GitHub
# repo involved, per the plan's "stubbed-gh integration test" test-plan entry.
#
# Contract under test (pinned here for the not-yet-written implementation):
#   Usage: bash maintenance-issue.sh <report|resolve>, run with cwd at the
#   target repo root. Reads the target repo via the GITHUB_REPOSITORY env var
#   (owner/repo, as GitHub Actions sets it automatically) rather than
#   resolving it from a git remote, so tests need no real git remote.
#
#   Every invocation ensures the `maintenance` label exists via
#   `gh label create maintenance --repo <repo> ...` (best-effort; the real
#   script is expected to tolerate a non-zero exit from this call, since the
#   label may already exist -- this stub always returns 0 for it either way).
#
#   Both modes look up the single open, `maintenance`-labeled issue via
#   `gh issue list --repo <repo> --label maintenance --state open --json
#   number ...`.
#
#   report mode:
#     - builds the issue body from cwd's .cenci/maintain-report.json: one
#       line per non-pass (fail/warn) result, each redacting any
#       `<NAME>=<value>`-shaped credential-looking assignment in its message
#       to `<NAME>=[REDACTED]` before the value ever reaches the body/gh
#       argv -- pass results are never included.
#     - no open issue found -> `gh issue create --repo <repo> ... --body
#       <body> --label maintenance` (never a `gh issue edit`/`gh issue
#       close` call in this same run).
#     - an open issue found (say, number N) -> `gh issue edit N --repo
#       <repo> ... --body <body>` (dedup: never a `gh issue create` call in
#       this same run).
#
#   resolve mode:
#     - an open issue found (number N) -> `gh issue close N --repo <repo>
#       ...` (an auto-close comment is expected but this suite does not pin
#       its exact wording).
#     - no open issue found -> no `gh issue close`/`gh issue create` call at
#       all (no-op).
#
#   Every path always exits 0 (advisory/report tooling, not a hard gate) --
#   this suite additionally treats a non-zero exit as a hard failure on
#   every case since maintenance-issue.sh has no documented failure mode of
#   its own once `gh` and the report file are available.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ISSUE_SH="${SCRIPT_DIR}/maintenance-issue.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

TEST_ROOT="$(mktemp -d /var/tmp/maintenance-issue-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

REPO_SLUG="example-owner/example-repo"

# make_gh_stub <bin_dir> <log_file> [existing_issue_number] [fail_close] —
# writes a fake `gh` executable into <bin_dir> that:
#   - appends its own argv, encoded as one JSON array (NUL-delimited so
#     embedded newlines in e.g. a --body value can never corrupt the log),
#     to <log_file> for every invocation;
#   - answers `gh issue list ...` with either `[]` (no existing_issue_number)
#     or a single-element array carrying that issue number;
#   - answers `gh label create`, `gh issue create`, and `gh issue edit` with
#     exit 0 and no output;
#   - answers `gh issue close` with exit 0 and no output, UNLESS
#     <fail_close> is a non-empty string, in which case it exits 1 with a
#     message on stderr (simulating a persistently failing close call, e.g.
#     a bad token or rate limit);
#   - fails loudly (exit 1, message on stderr) for anything else, so an
#     unexpected invocation shape shows up as a test failure rather than a
#     silent no-op.
make_gh_stub() {
    local bin_dir="$1" log_file="$2" existing_issue_number="${3:-}" fail_close="${4:-}"
    mkdir -p "${bin_dir}"
    : > "${log_file}"
    cat > "${bin_dir}/gh" <<STUB
#!/usr/bin/env bash
set -u
ARGS_JSON="\$(printf '%s\\0' "\$@" | jq -R -s -c 'split("\\u0000")[:-1]')"
printf '%s\n' "\${ARGS_JSON}" >> "${log_file}"
sub1="\${1:-}"; sub2="\${2:-}"
case "\${sub1} \${sub2}" in
  "label create")
    exit 0
    ;;
  "issue list")
    if [[ -n "${existing_issue_number}" ]]; then
      printf '[{"number": ${existing_issue_number}}]\n'
    else
      printf '[]\n'
    fi
    exit 0
    ;;
  "issue create")
    echo "https://github.com/${REPO_SLUG}/issues/999"
    exit 0
    ;;
  "issue edit")
    exit 0
    ;;
  "issue close")
    if [[ -n "${fail_close}" ]]; then
      echo "gh stub: simulated 'gh issue close' failure" >&2
      exit 1
    fi
    exit 0
    ;;
esac
echo "gh stub: unhandled invocation: \$*" >&2
exit 1
STUB
    chmod +x "${bin_dir}/gh"
}

# write_report <dir> — a fixture .cenci/maintain-report.json with one fail,
# one warn, and one pass result, mirroring check.sh's build_report shape
# ({summary:{pass,warn,fail,skip,mode}, results:[{check,target,status,
# message,fix}]}).
write_report() {
    local dir="$1"
    mkdir -p "${dir}/.cenci"
    cat > "${dir}/.cenci/maintain-report.json" <<'EOF'
{
  "summary": {"pass": 1, "warn": 1, "fail": 1, "skip": 0, "mode": "strict"},
  "results": [
    {"check": "gate-command", "target": "flow", "status": "fail", "message": "gateCommand exited 1: build failed", "fix": "fix the build"},
    {"check": "context-budget", "target": "AGENTS.md", "status": "warn", "message": "Critical Rules section has 12 bullets (max 10)", "fix": "run /cenci:maintain rules"},
    {"check": "front-matter", "target": "flow/skills/x/SKILL.md", "status": "pass", "message": "front matter ok", "fix": ""}
  ]
}
EOF
}

# write_report_with_secret <dir> <secret> — a fixture report whose sole fail
# result's message embeds a credential-looking assignment
# (AWS_SECRET_ACCESS_KEY=<secret>), planted to prove the assembled body never
# leaks the raw value.
write_report_with_secret() {
    local dir="$1" secret="$2"
    mkdir -p "${dir}/.cenci"
    jq -n --arg secret "${secret}" '{
      summary: {pass: 0, warn: 0, fail: 1, skip: 0, mode: "strict"},
      results: [
        {check: "gate-command", target: "flow", status: "fail",
         message: ("gateCommand exited 1: request failed: AWS_SECRET_ACCESS_KEY=" + $secret + " was rejected"),
         fix: "rotate the credential and re-run"}
      ]
    }' > "${dir}/.cenci/maintain-report.json"
}

# write_report_with_traceback <dir> <token> — a fixture report whose sole
# fail result's message is a multi-line, non-`NAME=value`-shaped gate-command
# stderr dump (an embedded stack trace plus a `Bearer <token>` auth header),
# planted to prove the redaction gap the code/security reviewers found
# (only `NAME=value`-shaped strings were redacted; raw multi-line output and
# non-assignment-shaped secrets like bearer tokens passed through verbatim).
write_report_with_traceback() {
    local dir="$1" token="$2"
    mkdir -p "${dir}/.cenci"
    jq -n --arg token "${token}" '{
      summary: {pass: 0, warn: 0, fail: 1, skip: 0, mode: "strict"},
      results: [
        {check: "gate-command", target: "flow", status: "fail",
         message: ("gateCommand exited 1: Authorization: Bearer " + $token + " was rejected.\nTraceback (most recent call last):\n  File \"app.py\", line 42, in handler\n    raise RuntimeError(\"boom\")\nRuntimeError: boom\n"),
         fix: "rotate the credential and re-run"}
      ]
    }' > "${dir}/.cenci/maintain-report.json"
}

# write_report_with_json_secrets <dir> — a fixture report whose sole fail
# result's message is a JSON-quoted-key/value blob (e.g.
# {"password":"abc123","token":"xyz789","apiKey":"zzz111"}), planted to
# prove the JSON-string-value redaction gap the code reviewer reproduced by
# hand-tracing the sed pipeline (ticket #534 round-2 review): the plain
# `key: value` pattern's match breaks on the closing `"` right after the
# key name, so JSON-quoted secrets previously passed through unredacted.
write_report_with_json_secrets() {
    local dir="$1"
    mkdir -p "${dir}/.cenci"
    cat > "${dir}/.cenci/maintain-report.json" <<'EOF'
{
  "summary": {"pass": 0, "warn": 0, "fail": 1, "skip": 0, "mode": "strict"},
  "results": [
    {"check": "gate-command", "target": "flow", "status": "fail",
     "message": "gateCommand exited 1: request body was {\"password\":\"abc123\",\"token\":\"xyz789\",\"apiKey\":\"zzz111\"}",
     "fix": "rotate the credentials and re-run"}
  ]
}
EOF
}

# write_report_with_padded_secrets <dir> — a fixture report whose sole fail
# result's message embeds a space-padded `NAME = "value"`-shaped assignment
# and an underscore-prefixed `key:`-shaped pair (api_key:), planted to prove
# the space-padding/prefix redaction gaps the security reviewer found: the
# `NAME=value` pattern previously required no spaces around `=`, and the
# secret-keyword colon pattern's `\b` boundary previously missed
# underscore-prefixed keys like `api_key:` (word-char `_` blocks `\b`).
write_report_with_padded_secrets() {
    local dir="$1"
    mkdir -p "${dir}/.cenci"
    cat > "${dir}/.cenci/maintain-report.json" <<'EOF'
{
  "summary": {"pass": 0, "warn": 0, "fail": 1, "skip": 0, "mode": "strict"},
  "results": [
    {"check": "gate-command", "target": "flow", "status": "fail",
     "message": "gateCommand exited 1: config dump: password = \"hunter2border\", api_key: zzz222yyy",
     "fix": "rotate the credentials and re-run"}
  ]
}
EOF
}

# write_report_with_truncation_boundary_secret <dir> — a fixture report
# whose sole fail result's message is engineered so a URL-userinfo secret's
# password characters fall entirely within the first MAX_MESSAGE_LEN (200)
# characters of the raw message (`"gateCommand exited 1: " + 149 filler
# chars + "https://user:sup3rSecretPass1@host.example.com/api rejected"`),
# while the URL-userinfo pattern's trailing `@` anchor character falls
# exactly at raw offset 200 (i.e. just past the first 200 characters).
# Planted to prove the truncate-then-redact ordering gap the security
# reviewer found: if the raw message were truncated to 200 chars BEFORE
# redaction (the pre-fix order), the trailing `@` trigger would be sliced
# off, so the still-present, still-unredacted password text
# (sup3rSecretPass1, sitting entirely within the first 200 characters)
# would leak into the assembled body verbatim.
write_report_with_truncation_boundary_secret() {
    local dir="$1"
    local filler
    mkdir -p "${dir}/.cenci"
    filler="$(printf 'A%.0s' $(seq 1 149))"
    jq -n --arg msg "gateCommand exited 1: ${filler}https://user:sup3rSecretPass1@host.example.com/api rejected" '{
      summary: {pass: 0, warn: 0, fail: 1, skip: 0, mode: "strict"},
      results: [
        {check: "gate-command", target: "flow", status: "fail", message: $msg,
         fix: "rotate the credential and re-run"}
      ]
    }' > "${dir}/.cenci/maintain-report.json"
}

# run_issue_sh <dir> <mode> <bin_dir> — runs maintenance-issue.sh <mode> with
# cwd set to <dir> and <bin_dir> prepended to PATH. Sets ISSUE_EXIT.
run_issue_sh() {
    local dir="$1" mode="$2" bin_dir="$3"
    (cd "${dir}" && PATH="${bin_dir}:${PATH}" GITHUB_REPOSITORY="${REPO_SLUG}" bash "${ISSUE_SH}" "${mode}") \
        >/dev/null 2>"${TEST_ROOT}/last-stderr.txt"
    ISSUE_EXIT=$?
    ISSUE_STDERR="$(cat "${TEST_ROOT}/last-stderr.txt")"
}

assert_exit_zero() {
    local label="$1"
    if [[ "${ISSUE_EXIT}" -eq 0 ]]; then
        pass
    else
        fail "${label}: exit ${ISSUE_EXIT}, expected 0 (stderr: ${ISSUE_STDERR})"
    fi
}

# count_calls <log_file> <sub1> <sub2> — number of NDJSON call records whose
# first two argv elements equal <sub1>/<sub2>.
count_calls() {
    local log_file="$1" sub1="$2" sub2="$3"
    jq -c --arg s1 "${sub1}" --arg s2 "${sub2}" \
        'select((.[0] // "") == $s1 and (.[1] // "") == $s2)' \
        "${log_file}" 2>/dev/null | wc -l | tr -d ' '
}

# call_flag_value <log_file> <sub1> <sub2> <flag> — the value immediately
# following <flag> in the LAST matching call's argv (empty if no match).
call_flag_value() {
    local log_file="$1" sub1="$2" sub2="$3" flag="$4"
    jq -r --arg s1 "${sub1}" --arg s2 "${sub2}" --arg f "${flag}" '
        select((.[0] // "") == $s1 and (.[1] // "") == $s2) as $c |
        ($c | index($f)) as $i |
        if $i == null then empty else $c[$i+1] end
    ' "${log_file}" 2>/dev/null | tail -n1
}

# label_call_names <log_file> — positional label-name argument (argv[2]) of
# every `gh label create` call.
label_call_names() {
    local log_file="$1"
    jq -r 'select((.[0] // "") == "label" and (.[1] // "") == "create") | .[2] // empty' \
        "${log_file}" 2>/dev/null
}

echo "maintenance-issue.test.sh"

# ── Case 1: report, no open issue -> creates (not edits/closes) ──────────
echo "case: report mode with no open issue creates the maintenance issue"
CASE1="${TEST_ROOT}/case1-report-create"
mkdir -p "${CASE1}"
write_report "${CASE1}"
BIN1="${TEST_ROOT}/case1-bin"
LOG1="${TEST_ROOT}/case1-gh-calls.ndjson"
make_gh_stub "${BIN1}" "${LOG1}"
run_issue_sh "${CASE1}" report "${BIN1}"
assert_exit_zero "case1 report create"
if [[ "$(count_calls "${LOG1}" issue create)" -ge 1 ]]; then
    pass
else
    fail "case1: expected a 'gh issue create' call when no open issue exists"
fi
if [[ "$(count_calls "${LOG1}" issue edit)" -eq 0 ]]; then
    pass
else
    fail "case1: did not expect a 'gh issue edit' call when creating"
fi

# ── Case 2: report, an open issue already exists -> edits, never re-creates
# (dedup) ──────────────────────────────────────────────────────────────────
echo "case: report mode with an existing open issue edits it instead of re-creating"
CASE2="${TEST_ROOT}/case2-report-edit"
mkdir -p "${CASE2}"
write_report "${CASE2}"
BIN2="${TEST_ROOT}/case2-bin"
LOG2="${TEST_ROOT}/case2-gh-calls.ndjson"
make_gh_stub "${BIN2}" "${LOG2}" "42"
run_issue_sh "${CASE2}" report "${BIN2}"
assert_exit_zero "case2 report edit"
if [[ "$(count_calls "${LOG2}" issue edit)" -ge 1 ]]; then
    pass
else
    fail "case2: expected a 'gh issue edit' call when an open issue already exists"
fi
if [[ "$(count_calls "${LOG2}" issue create)" -eq 0 ]]; then
    pass
else
    fail "case2: dedup violated -- 'gh issue create' must not run when an open issue already exists"
fi

# ── Case 3: resolve, an open issue exists -> closes it ────────────────────
echo "case: resolve mode with an open issue closes it"
CASE3="${TEST_ROOT}/case3-resolve-close"
mkdir -p "${CASE3}"
BIN3="${TEST_ROOT}/case3-bin"
LOG3="${TEST_ROOT}/case3-gh-calls.ndjson"
make_gh_stub "${BIN3}" "${LOG3}" "7"
run_issue_sh "${CASE3}" resolve "${BIN3}"
assert_exit_zero "case3 resolve close"
if [[ "$(count_calls "${LOG3}" issue close)" -ge 1 ]]; then
    pass
else
    fail "case3: expected a 'gh issue close' call when an open issue exists on a clean run"
fi

# ── Case 3b: resolve, no open issue exists -> no-op (never closes/creates)
echo "case: resolve mode with no open issue is a no-op"
CASE3B="${TEST_ROOT}/case3b-resolve-noop"
mkdir -p "${CASE3B}"
BIN3B="${TEST_ROOT}/case3b-bin"
LOG3B="${TEST_ROOT}/case3b-gh-calls.ndjson"
make_gh_stub "${BIN3B}" "${LOG3B}"
run_issue_sh "${CASE3B}" resolve "${BIN3B}"
assert_exit_zero "case3b resolve no-op"
if [[ "$(count_calls "${LOG3B}" issue close)" -eq 0 && "$(count_calls "${LOG3B}" issue create)" -eq 0 ]]; then
    pass
else
    fail "case3b: expected no 'gh issue close'/'gh issue create' call when there is no open issue to resolve"
fi

# ── Case 4: every run ensures the `maintenance` label exists ──────────────
echo "case: report mode ensures the maintenance label exists"
if [[ "$(label_call_names "${LOG1}")" == *"maintenance"* ]]; then
    pass
else
    fail "case4: expected a 'gh label create maintenance ...' call (got label names: $(label_call_names "${LOG1}"))"
fi

# ── Case 5: issue body carries the FAIL/WARN summary, and only that ───────
echo "case: assembled issue body contains the FAIL/WARN summary from the report"
BODY1="$(call_flag_value "${LOG1}" issue create --body)"
if [[ "${BODY1}" == *"gate-command"* && "${BODY1}" == *"build failed"* ]]; then
    pass
else
    fail "case5: expected the body to contain the fail result (gate-command / build failed); got: ${BODY1}"
fi
if [[ "${BODY1}" == *"context-budget"* && "${BODY1}" == *"Critical Rules section has 12 bullets"* ]]; then
    pass
else
    fail "case5: expected the body to contain the warn result (context-budget / Critical Rules...); got: ${BODY1}"
fi
if [[ "${BODY1}" != *"front-matter"* ]]; then
    pass
else
    fail "case5: pass results must not be surfaced in the body (found 'front-matter'); got: ${BODY1}"
fi

# ── Case 6: a planted secret in a report message is redacted from the body -
echo "case: a planted secret in the report is redacted from the assembled body"
CASE6="${TEST_ROOT}/case6-secret-redaction"
mkdir -p "${CASE6}"
SECRET="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
write_report_with_secret "${CASE6}" "${SECRET}"
BIN6="${TEST_ROOT}/case6-bin"
LOG6="${TEST_ROOT}/case6-gh-calls.ndjson"
make_gh_stub "${BIN6}" "${LOG6}"
run_issue_sh "${CASE6}" report "${BIN6}"
assert_exit_zero "case6 report with planted secret"
BODY6="$(call_flag_value "${LOG6}" issue create --body)"
if [[ "${BODY6}" != *"${SECRET}"* ]]; then
    pass
else
    fail "case6: raw secret value leaked into the assembled issue body: ${BODY6}"
fi
if [[ "${BODY6}" == *"gate-command"* ]]; then
    pass
else
    fail "case6: expected the check identity to survive redaction (gate-command); got: ${BODY6}"
fi

# ── Case 7: a planted multi-line stack trace + bearer token (neither
# `NAME=value`-shaped) is neither embedded verbatim nor left multi-line in
# the assembled body -- the redaction-gap the code/security reviewers
# reproduced (Phase 6+7 review, ticket #534) ───────────────────────────────
echo "case: a planted multi-line traceback + bearer token is collapsed/truncated and redacted from the assembled body"
CASE7="${TEST_ROOT}/case7-traceback-redaction"
mkdir -p "${CASE7}"
TOKEN="FAKEBEARERTOKEN1234567890abcdefFAKEBEARERTOKEN"
write_report_with_traceback "${CASE7}" "${TOKEN}"
BIN7="${TEST_ROOT}/case7-bin"
LOG7="${TEST_ROOT}/case7-gh-calls.ndjson"
make_gh_stub "${BIN7}" "${LOG7}"
run_issue_sh "${CASE7}" report "${BIN7}"
assert_exit_zero "case7 report with planted traceback + bearer token"
BODY7="$(call_flag_value "${LOG7}" issue create --body)"
if [[ "${BODY7}" != *$'\n'* ]]; then
    pass
else
    fail "case7: raw multi-line stack trace leaked into the assembled issue body (embedded newline found): ${BODY7}"
fi
if [[ "${BODY7}" != *"${TOKEN}"* ]]; then
    pass
else
    fail "case7: raw bearer token leaked into the assembled issue body: ${BODY7}"
fi
if [[ "${BODY7}" == *"gate-command"* ]]; then
    pass
else
    fail "case7: expected the check identity to survive redaction (gate-command); got: ${BODY7}"
fi

# ── Case 8: a planted JSON-quoted-key/value secret is redacted from the
# body -- the redaction gap the code reviewer reproduced by hand-tracing
# the sed pipeline (round-2 review, ticket #534): the plain `key: value`
# pattern's match breaks on the closing `"` right after the key name, so
# `{"password":"abc123","token":"xyz789","apiKey":"zzz111"}` previously
# passed through the assembled body completely unredacted ────────────────
echo "case: a planted JSON-quoted secret is redacted from the assembled body"
CASE8="${TEST_ROOT}/case8-json-secret-redaction"
mkdir -p "${CASE8}"
write_report_with_json_secrets "${CASE8}"
BIN8="${TEST_ROOT}/case8-bin"
LOG8="${TEST_ROOT}/case8-gh-calls.ndjson"
make_gh_stub "${BIN8}" "${LOG8}"
run_issue_sh "${CASE8}" report "${BIN8}"
assert_exit_zero "case8 report with planted JSON-quoted secrets"
BODY8="$(call_flag_value "${LOG8}" issue create --body)"
if [[ "${BODY8}" != *"abc123"* ]]; then
    pass
else
    fail "case8: raw JSON-quoted password value leaked into the assembled issue body: ${BODY8}"
fi
if [[ "${BODY8}" != *"xyz789"* ]]; then
    pass
else
    fail "case8: raw JSON-quoted token value leaked into the assembled issue body: ${BODY8}"
fi
if [[ "${BODY8}" != *"zzz111"* ]]; then
    pass
else
    fail "case8: raw JSON-quoted apiKey value leaked into the assembled issue body: ${BODY8}"
fi
if [[ "${BODY8}" == *"gate-command"* ]]; then
    pass
else
    fail "case8: expected the check identity to survive redaction (gate-command); got: ${BODY8}"
fi

# ── Case 9: space-padded `NAME = "value"` assignments and underscore-
# prefixed `key:`-shaped pairs (api_key:) are redacted -- the pattern gaps
# the security reviewer found (strict no-space `=` requirement, and a `\b`
# boundary that misses a keyword preceded by `_`) ─────────────────────────
echo "case: space-padded and underscore-prefixed secret patterns are redacted from the assembled body"
CASE9="${TEST_ROOT}/case9-padded-secret-redaction"
mkdir -p "${CASE9}"
write_report_with_padded_secrets "${CASE9}"
BIN9="${TEST_ROOT}/case9-bin"
LOG9="${TEST_ROOT}/case9-gh-calls.ndjson"
make_gh_stub "${BIN9}" "${LOG9}"
run_issue_sh "${CASE9}" report "${BIN9}"
assert_exit_zero "case9 report with planted padded/prefixed secrets"
BODY9="$(call_flag_value "${LOG9}" issue create --body)"
if [[ "${BODY9}" != *"hunter2border"* ]]; then
    pass
else
    fail "case9: raw space-padded 'password = value' secret leaked into the assembled issue body: ${BODY9}"
fi
if [[ "${BODY9}" != *"zzz222yyy"* ]]; then
    pass
else
    fail "case9: raw underscore-prefixed 'api_key:' secret leaked into the assembled issue body: ${BODY9}"
fi
if [[ "${BODY9}" == *"gate-command"* ]]; then
    pass
else
    fail "case9: expected the check identity to survive redaction (gate-command); got: ${BODY9}"
fi

# ── Case 10: a URL-userinfo secret whose password sits entirely within the
# first MAX_MESSAGE_LEN (200) raw characters, but whose triggering `@` sits
# just past that boundary, is still redacted -- the truncate-then-redact
# ordering gap the security reviewer found (raw content was truncated
# before redaction ran, which could slice off the `@` anchor and leave the
# still-present password text unredacted) ─────────────────────────────────
echo "case: a URL-userinfo secret straddling the truncation boundary is still redacted (redact-before-truncate ordering)"
CASE10="${TEST_ROOT}/case10-truncation-boundary-redaction"
mkdir -p "${CASE10}"
write_report_with_truncation_boundary_secret "${CASE10}"
BIN10="${TEST_ROOT}/case10-bin"
LOG10="${TEST_ROOT}/case10-gh-calls.ndjson"
make_gh_stub "${BIN10}" "${LOG10}"
run_issue_sh "${CASE10}" report "${BIN10}"
assert_exit_zero "case10 report with planted truncation-boundary secret"
BODY10="$(call_flag_value "${LOG10}" issue create --body)"
if [[ "${BODY10}" != *"sup3rSecretPass1"* ]]; then
    pass
else
    fail "case10: raw URL-userinfo password leaked into the assembled issue body because truncation ran before redaction: ${BODY10}"
fi
if [[ "${BODY10}" == *"gate-command"* ]]; then
    pass
else
    fail "case10: expected the check identity to survive redaction (gate-command); got: ${BODY10}"
fi

# ── Case 11: resolve mode surfaces a non-zero exit when `gh issue close`
# itself fails on an open issue -- distinct from "no open issue to close"
# (case 3b), which must stay a no-op exit 0. Fixes the dead
# RESOLVE_EXIT check in cenci-maintenance.yml the silent-failure hunter
# found: the compound `gh issue close ... || echo ... >&2` previously
# always exited 0 regardless of whether `gh issue close` itself succeeded ─
echo "case: resolve mode exits non-zero when 'gh issue close' fails on an open issue"
CASE11="${TEST_ROOT}/case11-resolve-close-failure"
mkdir -p "${CASE11}"
BIN11="${TEST_ROOT}/case11-bin"
LOG11="${TEST_ROOT}/case11-gh-calls.ndjson"
make_gh_stub "${BIN11}" "${LOG11}" "13" "fail-close"
run_issue_sh "${CASE11}" resolve "${BIN11}"
if [[ "${ISSUE_EXIT}" -ne 0 ]]; then
    pass
else
    fail "case11: expected a non-zero exit when 'gh issue close' fails on an open issue, got exit ${ISSUE_EXIT}"
fi
if [[ "$(count_calls "${LOG11}" issue close)" -ge 1 ]]; then
    pass
else
    fail "case11: expected a 'gh issue close' call to have been attempted"
fi

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
