#!/usr/bin/env bash
# Non-blocking SessionStart maintenance advisory.
#
# Optional reminder UX is disabled only by maintenance.enabled=false and
# otherwise requires a positive remindAfterDays. Core correctness checks
# remain independent of this hook. Remote audit state is read from GitHub
# Actions metadata; no tracked marker file is created or consumed.
set -uo pipefail

finish_silent() { exit 0; }
command -v jq >/dev/null 2>&1 || finish_silent

ROOT="$(pwd -P)" || finish_silent
CONFIG_FILE="${ROOT}/.cenci/config.json"
[[ -f "$CONFIG_FILE" ]] || finish_silent

REMIND_AFTER_DAYS="$(jq -r '
  if type == "object"
    and (.maintenance? | type) == "object"
    and (if (.maintenance | has("enabled")) and (.maintenance.enabled | type) == "boolean"
         then .maintenance.enabled != false
         else true
         end)
    and (.maintenance.remindAfterDays | type) == "number"
    and .maintenance.remindAfterDays > 0
  then (.maintenance.remindAfterDays | tostring)
  else ""
  end
' "$CONFIG_FILE" 2>/dev/null)" || finish_silent
[[ -n "$REMIND_AFTER_DAYS" ]] || finish_silent
THRESHOLD_DAYS="${REMIND_AFTER_DAYS%%.*}"
[[ "$THRESHOLD_DAYS" =~ ^[0-9]+$ ]] || finish_silent

REASONS=()

# Run the target repository's own static checker from an explicit absolute
# repository CWD. The checker only exists in the cenci monorepo itself — in a
# consumer repo the flow plugin is installed but the tree has no
# flow/skills/maintain/scripts/check.sh, so this signal is skipped entirely,
# mirroring Phase 8's applicability guard (the checker's repo-scoped checks
# would otherwise report cenci-specific drift against an unrelated tree).
# Advisory mode emits JSON on stdout and skips executable/network checks, so
# it is safe for a bounded SessionStart hook and does not write a report file.
# A timeout, infrastructure exit, or malformed result contributes no local
# conclusion — like every remote degradation path below, it stays silent
# rather than turning an unfinished check into an every-session reminder.
CHECKER="${ROOT}/flow/skills/maintain/scripts/check.sh"
if [[ -f "$CHECKER" ]] && command -v timeout >/dev/null 2>&1; then
  set +e
  CHECK_JSON="$(cd "$ROOT" && timeout 2 bash "$CHECKER" --advisory 2>/dev/null)"
  CHECK_EXIT=$?
  if [[ "$CHECK_EXIT" -eq 0 || "$CHECK_EXIT" -eq 1 ]] && jq -e '
    type == "object" and
    (.summary | type) == "object" and
    (.summary.fail | type) == "number" and
    (.summary.warn | type) == "number" and
    .summary.mode == "advisory"
  ' <<<"$CHECK_JSON" >/dev/null 2>&1; then
    LOCAL_FAIL="$(jq -r '.summary.fail | floor' <<<"$CHECK_JSON")"
    LOCAL_WARN="$(jq -r '.summary.warn | floor' <<<"$CHECK_JSON")"
    if [[ "$LOCAL_FAIL" -gt 0 || "$LOCAL_WARN" -gt 0 ]]; then
      REASONS+=("The bounded local maintenance advisory found ${LOCAL_FAIL} failing and ${LOCAL_WARN} warning check result(s). Run /cenci:maintain to inspect them.")
    fi
  fi
fi

is_sensitive_path() {
  case "$1" in
    flow/skills/*|flow/agents/*|flow/hooks/*|flow/docs/*|flow/codex/*|flow/opencode/*| \
    docs/*|sandbox/*|watch/*|.claude-plugin/*|*/.claude-plugin/*| \
    README.md|flow/README.md|flow/AGENTS.md|flow/CLAUDE.md|AGENTS.md|CLAUDE.md| \
    .cenci/config.json|install.sh)
      return 0
      ;;
    *) return 1 ;;
  esac
}

resolve_repo_slug() {
  if [[ -n "${GITHUB_REPOSITORY:-}" ]]; then
    printf '%s' "$GITHUB_REPOSITORY"
    return
  fi
  command -v git >/dev/null 2>&1 || return 1
  local remote
  remote="$(git -C "$ROOT" remote get-url origin 2>/dev/null)" || return 1
  case "$remote" in
    https://github.com/*) remote="${remote#https://github.com/}" ;;
    git@github.com:*) remote="${remote#git@github.com:}" ;;
    *) return 1 ;;
  esac
  remote="${remote%.git}"
  [[ "$remote" == */* && "$remote" != */*/* ]] || return 1
  printf '%s' "$remote"
}

# GitHub metadata is optional. Offline, unauthenticated, disabled, and
# malformed responses contribute no remote conclusion; local advisory results
# above remain useful and this hook always exits zero.
if command -v gh >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1 && REPO_SLUG="$(resolve_repo_slug)"; then
  if WORKFLOW_JSON="$(timeout 0.5 gh api "repos/${REPO_SLUG}/actions/workflows/cenci-maintenance.yml" 2>/dev/null)" \
    && jq -e 'type == "object" and .state == "active"' <<<"$WORKFLOW_JSON" >/dev/null 2>&1 \
    && RUNS_JSON="$(timeout 0.5 gh api "repos/${REPO_SLUG}/actions/workflows/cenci-maintenance.yml/runs?status=completed&per_page=20" 2>/dev/null)" \
    && jq -e 'type == "object" and (.workflow_runs | type) == "array"' <<<"$RUNS_JSON" >/dev/null 2>&1; then

    RUN_JSON=""
    AUDIT_OUTCOME=""
    while IFS= read -r CANDIDATE; do
      RUN_ID="$(jq -r '.id' <<<"$CANDIDATE" 2>/dev/null)"
      [[ "$RUN_ID" =~ ^[0-9]+$ ]] || continue
      if ! JOBS_JSON="$(timeout 0.5 gh api "repos/${REPO_SLUG}/actions/runs/${RUN_ID}/jobs" 2>/dev/null)" \
        || ! jq -e 'type == "object" and (.jobs | type) == "array"' <<<"$JOBS_JSON" >/dev/null 2>&1; then
        continue
      fi
      CANDIDATE_OUTCOME="$(jq -r '
        [.jobs[]?.steps[]?
          | select(.name == "Run maintenance audit")
          | .conclusion
          | select(type == "string" and . != "skipped")]
        | first // empty
      ' <<<"$JOBS_JSON" 2>/dev/null)"
      [[ -n "$CANDIDATE_OUTCOME" ]] || continue
      RUN_JSON="$CANDIDATE"
      AUDIT_OUTCOME="$CANDIDATE_OUTCOME"
      break
    done < <(jq -c '
      [.workflow_runs[]
        | select((.id | type) == "number")
        | select((.conclusion | type) == "string")
        | select(.conclusion != "skipped")
        | select((.updated_at | type) == "string" and (.updated_at | length) > 0)
        | select((.head_sha | type) == "string" and (.head_sha | length) > 0)][0:2][]
    ' <<<"$RUNS_JSON" 2>/dev/null)

    if [[ -n "$RUN_JSON" ]]; then
      AUDIT_AT="$(jq -r '.updated_at' <<<"$RUN_JSON")"
      AUDIT_SHA="$(jq -r '.head_sha' <<<"$RUN_JSON")"

      if [[ "$AUDIT_OUTCOME" != "success" ]]; then
        REASONS+=("The latest usable scheduled maintenance audit concluded '${AUDIT_OUTCOME}'. Run /cenci:maintain to inspect current drift.")
      fi

      AUDIT_EPOCH="$(date -u -d "$AUDIT_AT" +%s 2>/dev/null || true)"
      NOW_EPOCH="$(date -u +%s 2>/dev/null || true)"
      if [[ -n "$AUDIT_EPOCH" && -n "$NOW_EPOCH" ]]; then
        AGE_DAYS=$(( (NOW_EPOCH - AUDIT_EPOCH) / 86400 ))
        if [[ "$AGE_DAYS" -gt "$THRESHOLD_DAYS" ]]; then
          REASONS+=("The latest usable scheduled maintenance audit completed on ${AUDIT_AT}, more than the configured ${REMIND_AFTER_DAYS}-day threshold ago.")
        fi
      fi

      if command -v git >/dev/null 2>&1 \
        && git -C "$ROOT" cat-file -e "${AUDIT_SHA}^{commit}" 2>/dev/null; then
        if CHANGED_FILES="$(git -C "$ROOT" diff --name-only "${AUDIT_SHA}...HEAD" 2>/dev/null)"; then
          if [[ -n "$CHANGED_FILES" ]]; then
            while IFS= read -r changed; do
              [[ -n "$changed" ]] || continue
              if is_sensitive_path "$changed"; then
                REASONS+=("A maintenance-sensitive file (${changed}) changed since the latest usable scheduled audit (${AUDIT_SHA:0:12}).")
                break
              fi
            done <<<"$CHANGED_FILES"
          fi
        else
          REASONS+=("The latest scheduled maintenance audit could not be compared with the current checkout, so change status is unknown. Run /cenci:maintain to inspect it.")
        fi
      fi
    fi
  fi
fi

[[ "${#REASONS[@]}" -gt 0 ]] || finish_silent
CTX="$(printf '%s\n' "${REASONS[@]}")"
jq -n --arg ctx "$CTX" \
  '{hookSpecificOutput:{hookEventName:"SessionStart",additionalContext:$ctx}}'
exit 0
