#!/usr/bin/env bash
# Reconcile the single bot-managed issue for scheduled cenci maintenance.
#
# Usage: maintenance-issue.sh <report|resolve>
# The issue is identified by BOTH its dedicated label and BOT_MARKER. This
# avoids taking over human-authored issues that happen to share a label.
set -uo pipefail

MODE="${1:-}"
case "$MODE" in
  report|resolve) ;;
  *)
    echo "maintenance-issue.sh: usage: maintenance-issue.sh <report|resolve>" >&2
    exit 2
    ;;
esac

REPO="${GITHUB_REPOSITORY:-}"
[[ -n "$REPO" ]] || { echo "maintenance-issue.sh: GITHUB_REPOSITORY is required" >&2; exit 2; }
command -v gh >/dev/null 2>&1 || { echo "maintenance-issue.sh: gh is required" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "maintenance-issue.sh: jq is required" >&2; exit 2; }

LABEL="cenci-maintenance-audit"
BOT_MARKER="<!-- cenci-maintenance-audit -->"
REPORT_FILE="${MAINTENANCE_REPORT_FILE:-.cenci/scheduled-maintain-report.json}"

# --force makes an existing label a successful idempotent update while still
# surfacing authentication, permission, and transport failures.
if ! gh label create "$LABEL" --repo "$REPO" --force \
  --color "5319e7" \
  --description "Bot-managed cenci maintenance audit findings" >/dev/null; then
  echo "maintenance-issue.sh: failed to reconcile label '$LABEL'" >&2
  exit 1
fi

if ! ISSUES_JSON="$(gh issue list --repo "$REPO" --label "$LABEL" --state open \
  --limit 100 --json number,body)"; then
  echo "maintenance-issue.sh: failed to look up managed issues" >&2
  exit 1
fi

if ! jq -e '
  type == "array" and
  all(.[]; type == "object" and (.number | type) == "number" and (.body | type) == "string")
' <<<"$ISSUES_JSON" >/dev/null 2>&1; then
  echo "maintenance-issue.sh: issue lookup returned malformed JSON" >&2
  exit 1
fi

if ! MANAGED_NUMBERS="$(jq -r --arg marker "$BOT_MARKER" \
  '.[] | select(.body | contains($marker)) | .number' <<<"$ISSUES_JSON")"; then
  echo "maintenance-issue.sh: failed to parse issue lookup response" >&2
  exit 1
fi

MANAGED_COUNT="$(grep -c . <<<"$MANAGED_NUMBERS" || true)"
if [[ "$MANAGED_COUNT" -gt 1 ]]; then
  echo "maintenance-issue.sh: multiple open bot-managed maintenance issues found" >&2
  exit 1
fi
ISSUE_NUMBER=""
[[ "$MANAGED_COUNT" -eq 0 ]] || ISSUE_NUMBER="$MANAGED_NUMBERS"

build_body() {
  [[ -f "$REPORT_FILE" ]] || {
    echo "maintenance-issue.sh: report file not found: $REPORT_FILE" >&2
    return 1
  }
  jq -e '
    type == "object" and
    (.results | type) == "array" and
    all(.results[];
      (.check | type) == "string" and
      (.check | test("^[a-z0-9][a-z0-9-]*$")) and
      (.status == "pass" or .status == "warn" or .status == "fail" or .status == "skip"))
  ' "$REPORT_FILE" >/dev/null 2>&1 || {
    echo "maintenance-issue.sh: report file is malformed: $REPORT_FILE" >&2
    return 1
  }

  local rows
  rows="$(jq -r '
    [.results[]
      | select(.status == "fail" or .status == "warn")
      | {check, status}]
    | unique_by(.check, .status)
    | sort_by(.check, .status)
    | .[]
    | "| \(.check) | \(.status) |"
  ' "$REPORT_FILE")" || return 1

  printf '%s\n' "$BOT_MARKER"
  printf '%s\n\n' "The scheduled cenci maintenance audit found repository drift."
  printf '%s\n' "| Check ID | Status |"
  printf '%s\n' "| --- | --- |"
  if [[ -n "$rows" ]]; then
    printf '%s\n' "$rows"
  else
    printf '%s\n' "| report | unknown |"
  fi
  printf '\n%s\n' "Run \`/cenci:maintain\` locally to inspect diagnostics and choose repairs."
}

case "$MODE" in
  report)
    if ! BODY="$(build_body)"; then
      exit 1
    fi
    if [[ -z "$ISSUE_NUMBER" ]]; then
      if ! gh issue create --repo "$REPO" \
        --title "Scheduled cenci maintenance audit found drift" \
        --body "$BODY" --label "$LABEL" >/dev/null; then
        echo "maintenance-issue.sh: failed to create managed issue" >&2
        exit 1
      fi
    elif ! gh issue edit "$ISSUE_NUMBER" --repo "$REPO" \
      --title "Scheduled cenci maintenance audit found drift" \
      --body "$BODY" >/dev/null; then
      echo "maintenance-issue.sh: failed to edit managed issue #$ISSUE_NUMBER" >&2
      exit 1
    fi
    ;;
  resolve)
    if [[ -n "$ISSUE_NUMBER" ]] && ! gh issue close "$ISSUE_NUMBER" --repo "$REPO" \
      --comment "The latest scheduled cenci maintenance audit completed cleanly. Auto-closing." \
      >/dev/null; then
      echo "maintenance-issue.sh: failed to close managed issue #$ISSUE_NUMBER" >&2
      exit 1
    fi
    ;;
esac
