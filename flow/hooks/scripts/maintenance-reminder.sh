#!/usr/bin/env bash
# maintenance-reminder.sh — SessionStart advisory (ticket #534). Dormant
# unless cwd's .cenci/config.json sets a positive
# `.maintenance.remindAfterDays` (from the separate config-schema ticket —
# this hook only reads the key defensively, never writes it). Absent file,
# absent/non-positive/malformed key, or missing jq all mean "dormant":
# silent, exit 0, no output at all — the same graceful-absence idiom as
# resolve-babysit-interval.sh. When active, follows check-config-staleness.sh's
# shared hookSpecificOutput idiom: emit JSON when there's something to say,
# stay silent otherwise, always exit 0, never block.
#
# When active, reads (from cwd):
#   - .cenci/last-audit.json ({generatedAt: ISO8601, sha: <commit>}),
#     written by the scheduled cenci-maintenance.yml workflow on its last
#     clean run;
#   - .cenci/maintain-report.json, the local (possibly gitignored) last
#     /cenci:maintain run's machine report, if present.
#
# Warns (emits hookSpecificOutput + exits 0) when ANY of:
#   (a) the marker's generatedAt is older than remindAfterDays;
#   (b) a maintenance-sensitive file changed since the marker's sha
#       (git diff --name-only <sha>...HEAD intersected with an allowlist;
#       degrades to silent/skipped if git/sha are unavailable);
#   (c) the local .cenci/maintain-report.json has summary.fail > 0;
#   (d) the marker file does not exist at all — a gentler advisory than
#       (a): there has simply never been a recorded clean audit.
# Silent otherwise. No Lazyboards coupling; every path exits 0.
set -uo pipefail

finish_silent() {
  exit 0
}

command -v jq >/dev/null 2>&1 || finish_silent

CONFIG_FILE=".cenci/config.json"
[[ -f "$CONFIG_FILE" ]] || finish_silent

jq -e 'type == "object"' "$CONFIG_FILE" >/dev/null 2>&1 || finish_silent

# A positive maintenance.remindAfterDays activates the hook; anything else
# (absent, non-number, non-positive, or a malformed .maintenance shape)
# resolves to empty here and is handled by the emptiness check below.
REMIND_AFTER_DAYS="$(jq -r '
  if (.maintenance.remindAfterDays? | type) == "number" and (.maintenance.remindAfterDays > 0)
  then (.maintenance.remindAfterDays | tostring)
  else "" end
' "$CONFIG_FILE" 2>/dev/null)" || finish_silent
[[ -n "$REMIND_AFTER_DAYS" ]] || finish_silent

# Integer-days threshold for the age comparison below (truncates a
# fractional remindAfterDays, e.g. "3.5" -> "3" — good enough for a
# day-granularity staleness nudge).
THRESHOLD_DAYS="${REMIND_AFTER_DAYS%%.*}"
[[ -n "$THRESHOLD_DAYS" ]] || THRESHOLD_DAYS=0

MARKER_FILE=".cenci/last-audit.json"
LOCAL_REPORT_FILE=".cenci/maintain-report.json"

# Maintenance-sensitive file allowlist, approximating check.sh's repo-scope
# inputs (plan Assumptions).
is_sensitive_path() {
  case "$1" in
    flow/skills/*|flow/agents/*|flow/docs/*|flow/hooks/*|flow/README.md| \
    AGENTS.md|CLAUDE.md|flow/AGENTS.md|flow/CLAUDE.md|docs/*|.cenci/config.json)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

REASONS=()

if [[ ! -f "$MARKER_FILE" ]]; then
  # (d) no marker recorded yet — gentler than a staleness warning.
  REASONS+=("No maintenance audit marker (.cenci/last-audit.json) was found — the scheduled cenci-maintenance.yml workflow has not recorded a clean run yet. Consider running /cenci:maintain to establish a baseline.")
else
  MARKER_JSON="$(cat "$MARKER_FILE" 2>/dev/null)"
  if jq -e 'type == "object"' <<<"${MARKER_JSON:-}" >/dev/null 2>&1; then
    GENERATED_AT="$(jq -r 'if (.generatedAt | type) == "string" then .generatedAt else "" end' <<<"$MARKER_JSON" 2>/dev/null)"
    MARKER_SHA="$(jq -r 'if (.sha | type) == "string" then .sha else "" end' <<<"$MARKER_JSON" 2>/dev/null)"

    # (a) staleness — the marker is older than the configured threshold.
    if [[ -n "$GENERATED_AT" ]]; then
      GENERATED_EPOCH="$(date -u -d "$GENERATED_AT" +%s 2>/dev/null || true)"
      if [[ -n "$GENERATED_EPOCH" ]]; then
        NOW_EPOCH="$(date -u +%s)"
        AGE_DAYS=$(( (NOW_EPOCH - GENERATED_EPOCH) / 86400 ))
        if [[ "$AGE_DAYS" -gt "$THRESHOLD_DAYS" ]]; then
          REASONS+=("The last recorded clean maintenance audit was generated on ${GENERATED_AT}, more than the configured ${REMIND_AFTER_DAYS}-day threshold ago. Consider running /cenci:maintain.")
        fi
      fi
    fi

    # (b) a maintenance-sensitive file changed since the marker's sha.
    # Degrades to skipped (not silent overall — other conditions still
    # apply) if git or the recorded sha are unavailable.
    if [[ -n "$MARKER_SHA" ]] \
      && command -v git >/dev/null 2>&1 \
      && git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
      && git cat-file -e "${MARKER_SHA}^{commit}" 2>/dev/null; then
      CHANGED_FILES="$(git diff --name-only "${MARKER_SHA}...HEAD" 2>/dev/null || true)"
      if [[ -n "$CHANGED_FILES" ]]; then
        SENSITIVE_HIT=""
        while IFS= read -r f; do
          [[ -n "$f" ]] || continue
          if is_sensitive_path "$f"; then
            SENSITIVE_HIT="$f"
            break
          fi
        done <<<"$CHANGED_FILES"
        if [[ -n "$SENSITIVE_HIT" ]]; then
          REASONS+=("A maintenance-sensitive file (${SENSITIVE_HIT}) changed since the last recorded clean audit (${MARKER_SHA:0:12}). Consider running /cenci:maintain.")
        fi
      fi
    fi
  fi
fi

# (c) local report shows outstanding failures.
if [[ -f "$LOCAL_REPORT_FILE" ]]; then
  FAIL_COUNT="$(jq -r 'if (.summary.fail | type) == "number" then (.summary.fail | floor) else 0 end' "$LOCAL_REPORT_FILE" 2>/dev/null)"
  [[ "$FAIL_COUNT" =~ ^[0-9]+$ ]] || FAIL_COUNT=0
  if [[ "$FAIL_COUNT" -gt 0 ]]; then
    REASONS+=("The local .cenci/maintain-report.json shows ${FAIL_COUNT} failing check(s) from the last /cenci:maintain run. Consider reviewing and repairing them.")
  fi
fi

[[ "${#REASONS[@]}" -gt 0 ]] || finish_silent

CTX="$(printf '%s\n' "${REASONS[@]}")"
jq -n --arg ctx "$CTX" \
  '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
exit 0
