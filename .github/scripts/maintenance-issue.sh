#!/usr/bin/env bash
# maintenance-issue.sh — testable issue lifecycle for the weekly scheduled
# maintenance audit workflow (.github/workflows/cenci-maintenance.yml,
# ticket #534). See maintenance-issue.test.sh (stubbed-gh integration test)
# for the pinned contract this script implements.
#
# Usage: bash maintenance-issue.sh <report|resolve>, run with cwd at the
# target repo root. Targets the repo named by the GITHUB_REPOSITORY env var
# (owner/repo, set automatically by GitHub Actions) rather than resolving it
# from a git remote, so this script (and its tests) need no real git remote.
#
# report mode  — dedup-create-or-edit the single open `maintenance`-labeled
#                issue with a FAIL/WARN summary built from cwd's
#                .cenci/maintain-report.json (see build_body/redact_body for
#                the AGENTS.md no-secrets-in-user-facing-output redaction).
# resolve mode — close that issue (with an auto-close comment) once the
#                repository is clean; a no-op when there is no open issue.
#
# Every `gh` side-effect call is best-effort (does not abort the script) --
# this is report/advisory tooling invoked from a scheduled CI workflow, never
# a hard gate — check.sh --strict's own exit code is the actual pass/fail
# signal the calling workflow acts on. `report` mode always exits 0 once
# gh/jq are available (and 0 even when they are not — there is simply
# nothing it can do without them): a failed `gh issue create`/`gh issue
# edit` only means the notification issue itself didn't update, which is
# not something the calling workflow should fail its run over.
#
# `resolve` mode is the one exception: it exits non-zero specifically when
# an open maintenance issue was found AND the `gh issue close` call for it
# failed. That distinct failure signal lets cenci-maintenance.yml refuse to
# record its "clean audit" .cenci/last-audit.json marker when the issue
# meant to prove the audit is clean never actually got closed. No open
# issue to close is a legitimate no-op and still exits 0.
set -uo pipefail

MODE="${1:-}"
case "$MODE" in
  report|resolve) ;;
  *)
    echo "maintenance-issue.sh: usage: maintenance-issue.sh <report|resolve>" >&2
    exit 0
    ;;
esac

REPO="${GITHUB_REPOSITORY:-}"
if [[ -z "$REPO" ]]; then
  echo "maintenance-issue.sh: GITHUB_REPOSITORY is not set; nothing to do" >&2
  exit 0
fi

command -v gh >/dev/null 2>&1 || { echo "maintenance-issue.sh: gh CLI not found; nothing to do" >&2; exit 0; }
command -v jq >/dev/null 2>&1 || { echo "maintenance-issue.sh: jq not found; nothing to do" >&2; exit 0; }

LABEL="maintenance"
REPORT_FILE=".cenci/maintain-report.json"

# Ensure the dedup label exists. Best-effort: `gh label create` fails
# (non-zero) once the label already exists, which is the common case after
# the first run — that is not an error condition here.
gh label create "$LABEL" --repo "$REPO" \
  --color "5319e7" \
  --description "Weekly cenci maintenance audit findings (cenci-maintenance.yml)" \
  >/dev/null 2>&1 || true

# Find the single open, maintenance-labeled issue (the dedup key). Empty
# when none exists, or when the query itself fails — treated the same as
# "none" so a transient gh hiccup degrades to opening a fresh issue rather
# than silently doing nothing.
ISSUES_JSON="$(gh issue list --repo "$REPO" --label "$LABEL" --state open --json number 2>/dev/null)" || ISSUES_JSON="[]"
ISSUE_NUMBER="$(jq -r '(. // [])[0].number // empty' <<<"${ISSUES_JSON:-[]}" 2>/dev/null || true)"

# redact_body — never lets raw, unbounded gate-command output reach the
# public-facing issue body verbatim. Applied to each finding's message
# individually, before build_body's own collapse+truncate step runs (see
# below) -- redacting first means a credential-shaped substring can never
# be split/dropped by truncation before this function ever gets to see it
# whole. It is then applied a second time to the whole assembled body as a
# belt-and-suspenders pass (idempotent on already-redacted text), so it
# also catches a credential-looking pattern that formatting happens to
# split across a segment boundary. Covers, per AGENTS.md's
# no-secrets/no-stack-traces-in-user-facing-output rule:
#   - `<NAME>=<value>`-shaped assignments, value optionally quoted and with
#     optional whitespace around the `=` (e.g. a leaked
#     AWS_SECRET_ACCESS_KEY=... embedded in raw gate-command stderr, or a
#     `password = "hunter2"`-shaped config dump line) ->
#     `<NAME>=[REDACTED]`;
#   - `Bearer <token>`-style auth headers -> `Bearer [REDACTED]`;
#   - `key: value`/`key:value` pairs whose key names a common secret
#     (token, key, secret, password, auth, credential; case-insensitive),
#     allowing the keyword to appear as a suffix of a longer identifier
#     (e.g. `api_key:`, `db_password:`) rather than requiring a strict word
#     boundary immediately before it -> `[REDACTED]`;
#   - the same key/value shape JSON-quoted (e.g. `"password":"abc123"`,
#     `"apiKey":"zzz111"`) -> `"[REDACTED]"`;
#   - URL userinfo (`scheme://user:pass@host`) -> `scheme://[REDACTED]@host`.
# This function's job is redacting credential-shaped substrings within
# whatever text it is given; build_body is responsible for feeding it each
# message before collapsing/truncating that message.
redact_body() {
  sed -E \
    -e 's/([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*("[^"]*"|[^[:space:]]+)/\1=[REDACTED]/g' \
    -e 's/\bBearer[[:space:]]+[^[:space:]]+/Bearer [REDACTED]/gI' \
    -e 's/[A-Za-z0-9_-]*(token|key|secret|password|auth|credential)[A-Za-z0-9_-]*[[:space:]]*:[[:space:]]*[^[:space:]]+/[REDACTED]/gI' \
    -e 's/"[A-Za-z0-9_-]*(token|key|secret|password|auth|credential)[A-Za-z0-9_-]*"[[:space:]]*:[[:space:]]*"[^"]*"/"[REDACTED]"/gI' \
    -e 's#([A-Za-z][A-Za-z0-9+.-]*://)[^/@[:space:]:]+:[^/@[:space:]]+@#\1[REDACTED]@#g'
}

# neutralize_mentions — backtick-wraps every `@name`/`@org/team`-shaped
# mention found anywhere in the assembled body. GitHub does not resolve
# mentions inside inline code spans, so this stops a `@user`/`@org/team`
# substring surviving inside raw gate-command output from triggering a
# live notification/cross-reference when the issue is created/edited.
neutralize_mentions() {
  sed -E 's#@([A-Za-z0-9][A-Za-z0-9_-]*(/[A-Za-z0-9][A-Za-z0-9_-]*)?)#`&`#g'
}

# build_body — a single-line body (gh's --body argv value must never embed
# a raw newline here, since the workflow that dispatches this script parses
# --body's value back out of a NUL-delimited argv log; see
# maintenance-issue.test.sh's call_flag_value helper) carrying one
# "[STATUS] check (target): message" segment per non-pass (fail/warn) result
# in .cenci/maintain-report.json, joined with " | ". Pass results are never
# surfaced. Falls back to a placeholder when the report is missing or
# carries no findings.
#
# Each finding's .message can be arbitrary raw check.sh/gate-command
# output (multi-line stack traces, long stderr dumps, embedded secrets)
# -- never assumed to already be a clean, structured, single-line string.
# Before it is interpolated into the body, each message is, IN THIS ORDER:
#   1. redacted via redact_body, on the full raw (un-collapsed,
#      un-truncated) message -- redaction must see the whole message,
#      since a pattern anchored on trailing context (e.g. the URL-userinfo
#      pattern's trailing `@host`) could otherwise have that context
#      sliced off by truncation before redact_body ever runs, leaking the
#      credential up to the truncation point;
#   2. collapsed -- any embedded newline (\n, \r, \r\n) is replaced with a
#      space, so a multi-line stack trace can never pass through as-is;
#   3. hard-truncated to MAX_MESSAGE_LEN characters (after collapsing).
# redact_body is then applied a second time to the whole assembled body (a
# harmless no-op on already-redacted text) as a belt-and-suspenders pass
# that also catches a credential-looking pattern formatting happens to
# split across a segment boundary.
MAX_MESSAGE_LEN=200

build_body() {
  local segments=() line status check target raw_msg redacted collapsed clipped upper_status

  if [[ -f "$REPORT_FILE" ]]; then
    while IFS= read -r line; do
      status="$(jq -r '.status' <<<"$line")"
      check="$(jq -r '.check' <<<"$line")"
      target="$(jq -r '.target' <<<"$line")"
      raw_msg="$(jq -r '.message // ""' <<<"$line")"

      redacted="$(printf '%s' "$raw_msg" | redact_body)"
      collapsed="$(printf '%s' "$redacted" | sed -z -E 's/\r\n|\r|\n/ /g')"

      if [[ "${#collapsed}" -gt "$MAX_MESSAGE_LEN" ]]; then
        clipped="${collapsed:0:$MAX_MESSAGE_LEN}... [truncated]"
      else
        clipped="$collapsed"
      fi

      upper_status="$(printf '%s' "$status" | tr '[:lower:]' '[:upper:]')"
      segments+=("[${upper_status}] ${check} (${target}): ${clipped}")
    done < <(jq -c '(.results // [])[] | select(.status == "fail" or .status == "warn")' "$REPORT_FILE" 2>/dev/null)
  fi

  local findings="" sep="" seg
  for seg in "${segments[@]}"; do
    findings="${findings}${sep}${seg}"
    sep=" | "
  done
  [[ -n "$findings" ]] || findings="No FAIL/WARN findings were available in .cenci/maintain-report.json."

  printf 'Scheduled maintenance audit (cenci-maintenance.yml) ran check.sh --strict. Findings: %s. Run /cenci:maintain locally to review and repair. This issue is deduplicated and auto-managed: it will be updated on the next failing run and auto-closed once the repository is clean.' "$findings"
}

case "$MODE" in
  report)
    BODY="$(build_body | redact_body | neutralize_mentions)"
    if [[ -z "$ISSUE_NUMBER" ]]; then
      gh issue create --repo "$REPO" \
        --title "Weekly maintenance audit found issues" \
        --body "$BODY" \
        --label "$LABEL" \
        >/dev/null || echo "gh issue create failed (non-fatal, advisory only)" >&2
    else
      gh issue edit "$ISSUE_NUMBER" --repo "$REPO" --body "$BODY" \
        >/dev/null || echo "gh issue edit failed (non-fatal, advisory only)" >&2
    fi
    ;;
  resolve)
    # Unlike report mode, a failed `gh issue close` here is surfaced as a
    # non-zero script exit (MODE_EXIT) -- distinct from "no open issue to
    # close", which is a legitimate no-op and must stay exit 0. The
    # calling workflow (cenci-maintenance.yml) relies on this to refuse
    # recording its "clean audit" marker when the issue meant to prove the
    # audit is clean never actually got closed.
    if [[ -n "$ISSUE_NUMBER" ]]; then
      if ! gh issue close "$ISSUE_NUMBER" --repo "$REPO" \
        --comment "Repository is clean per the latest scheduled maintenance audit (cenci-maintenance.yml). Auto-closed." \
        >/dev/null; then
        echo "gh issue close failed" >&2
        MODE_EXIT=1
      fi
    fi
    ;;
esac

exit "${MODE_EXIT:-0}"
