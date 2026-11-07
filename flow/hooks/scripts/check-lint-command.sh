#!/usr/bin/env bash
# check-lint-command.sh — detect a missing `lintCommand` for stacks that have a
# Lint row in the Stack-to-CI table (#1200). "No linter" (a stack with no Lint
# row) and "forgot to configure the linter" look identical to the implement
# pipeline, which then skips its lint checks without saying so; this script
# resolves each target to pass, fail or skip so the difference gets reported.
#
# Modes:
#   (default)  Claude Code SessionStart hook — emits hookSpecificOutput JSON
#              whose additionalContext carries exactly one line per failing
#              project (slug, stack token, suggested command); silent (no
#              output, exit 0) when nothing fails. Never blocks.
#   --plain    machine-readable lines for scripted consumers (maintain's
#              check.sh lint-command check, and this script's own tests), one
#              tab-separated line per target:
#                <status>\t<target>\t<stack>\t<detail>
#              status  pass | fail | skip
#              target  the project slug, (unknown) for a project without one,
#                      or (repo) for a single-repo config
#              stack   the comma-joined matched stack tokens (every token for
#                      a skip), or "-" when there are none
#              detail  a reason code for pass/skip (lint-set, no-lint-row,
#                      no-stack, no-projects, no-config, invalid-config,
#                      no-jq); for a fail, `token:command` pairs joined by `|`
#              A degraded state (no jq, no config, unparseable config, no
#              projects) prints exactly one `skip (repo)` line.
#              Always exits 0 — detection is advisory, never a gate.
#
# Resolution:
#   - Monorepo (isMonorepo true): one result per projects[] entry, from
#     projects[].stack.framework and projects[].lintCommand only. A top-level
#     lintCommand is NOT inherited by a project.
#   - Single-repo: one (repo) result, from stack.framework, stack.backend and
#     stack.frontend against the top-level lintCommand; every matching token
#     contributes its command to the one fail.
#   - lintCommand counts as set only when it is a string with a non-space
#     character; absent, "", whitespace-only and non-string values are unset.
#     The configured value is never printed.
#
# Matching: `dotnet*` and `angular*` are globs. Every other stem matches the
# bare stem or the stem followed by a version suffix of digits and dots only
# (go, go1.25, react19 — not gopher). Case-sensitive. Anything else is a skip.
#
# Hardening: slugs and tokens come from committed config but become
# SessionStart context, so anything outside ^[A-Za-z0-9._-]+$ is printed as
# (redacted) — after matching on the raw token, so the status never changes.
# The only emission is a single `jq -n --arg` call (never heredoc
# interpolation), matching check-pending-plans.sh.

set -uo pipefail
export LC_ALL=C

MODE="hook"
if [[ "${1:-}" == "--plain" ]]; then
  MODE="plain"
fi

# Terminal degraded states: one plain skip line, silent in hook mode, exit 0.
finish_skip() {
  if [[ "$MODE" == "plain" ]]; then
    printf 'skip\t(repo)\t-\t%s\n' "$1"
  fi
  exit 0
}

# --- Stack-to-Lint mapping ---------------------------------------------------
# SOURCE OF TRUTH: the Stack-to-CI table's Lint column in
# flow/skills/configure/phases/runtime-ci-sandbox-questions.md. This block is a
# copy, so the two must stay in sync: change both together (a `react` / `next`
# table cell becomes one line per stack here). check-lint-command.test.sh
# parses that table and fails when its Lint column and this mapping differ in
# either direction.
# cenci-lint-map:start
LINT_MAP='dotnet*|dotnet format --verify-no-changes
go|golangci-lint run ./...
python|ruff check .
rust|cargo clippy -- -D warnings
angular*|ng lint
react|npx eslint .
next|npx eslint .
vue|npx eslint .'
# cenci-lint-map:end

CONFIG_FILE=".cenci/config.json"

command -v jq >/dev/null 2>&1 || finish_skip "no-jq"
[[ -f "$CONFIG_FILE" ]] || finish_skip "no-config"
jq -e 'type == "object"' "$CONFIG_FILE" >/dev/null 2>&1 || finish_skip "invalid-config"

# One jq call, explicit type checks (never a bare `//` fallback: "" is truthy
# for it). Rows, tab-separated by @tsv (which escapes tab/newline/backslash, so
# a value can never forge a row):
#   P <slug> <set|unset>   a target
#   T <token>              a stack token of the preceding P
#   N                      a monorepo config with no usable projects[]
ROWS="$(jq -r '
  def lint($o):
    if ($o.lintCommand | type) == "string" and ($o.lintCommand | test("[^ \t\r\n]"))
    then "set" else "unset" end;
  def toks($o; $keys):
    ($o.stack | if type == "object" then . else {} end) as $s
    | $keys[] as $k
    | $s[$k]
    | select(type == "string" and . != "")
    | ["T", .];
  (
    if .isMonorepo == true then
      if (.projects | type) == "array" and (.projects | length) > 0 then
        .projects[]
        | (if type == "object" then . else {} end) as $o
        | ["P",
           (if ($o.slug | type) == "string" and $o.slug != "" and ($o.slug | IN("(repo)", "(unknown)") | not)
            then $o.slug else "(unknown)" end),
           lint($o)],
          toks($o; ["framework"])
      else
        ["N"]
      end
    else
      ["P", "(repo)", lint(.)], toks(.; ["framework", "backend", "frontend"])
    end
  ) | @tsv
' "$CONFIG_FILE" 2>/dev/null)" || finish_skip "invalid-config"

# sanitize <value> [target] — sets SAFE to the value when it is charset-safe,
# else (redacted). Only a target may be the script-owned literal (repo) or
# (unknown); the jq stage maps a config-supplied slug of either literal to
# (unknown), so those placeholders can only originate from this script.
sanitize() {
  SAFE="(redacted)"
  if [[ "${2:-}" == "target" ]]; then
    case "$1" in '(repo)' | '(unknown)') SAFE="$1"; return 0 ;; esac
  fi
  case "$1" in
    '' | *[!A-Za-z0-9._-]*) ;;
    *) SAFE="$1" ;;
  esac
}

# match_token <raw-token> — sets MATCH_CMD to the mapped command, or "".
match_token() {
  local tok="$1" pat cmd stem rest
  MATCH_CMD=""
  while IFS='|' read -r pat cmd; do
    [[ -n "$pat" ]] || continue
    case "$pat" in
      *'*')
        stem="${pat%\*}"
        case "$tok" in
          "$stem"*) MATCH_CMD="$cmd"; return 0 ;;
        esac
        ;;
      *)
        case "$tok" in
          "$pat"*)
            rest="${tok#"$pat"}"
            case "$rest" in
              *[!0-9.]*) ;;
              *) MATCH_CMD="$cmd"; return 0 ;;
            esac
            ;;
        esac
        ;;
    esac
  done <<< "$LINT_MAP"
  return 0
}

PLAIN_OUT=""    # plain lines, printed once at the end
FAIL_LINES=""   # hook advisory lines, one per failing project
NO_PROJECTS=0
HAVE_PROJECT=0
CUR_SLUG=""
CUR_LINT=""
CUR_TOKENS=""   # newline-joined raw tokens (a raw token cannot hold a newline)

# flush_project — resolves the collected project and records its output.
flush_project() {
  [[ "$HAVE_PROJECT" -eq 1 ]] || return 0
  HAVE_PROJECT=0
  local tok safe_tok safe_slug all_list="" matched_list="" pairs="" suggest="" status stack detail line
  local seen=$'\n'   # newline-delimited raw tokens already collected, first-seen order
  while IFS= read -r tok; do
    [[ -n "$tok" ]] || continue
    [[ "$seen" != *$'\n'"$tok"$'\n'* ]] || continue
    seen="${seen}${tok}"$'\n'
    sanitize "$tok"
    safe_tok="$SAFE"
    all_list="${all_list:+${all_list},}${safe_tok}"
    match_token "$tok"
    if [[ -n "$MATCH_CMD" ]]; then
      matched_list="${matched_list:+${matched_list},}${safe_tok}"
      pairs="${pairs:+${pairs}|}${safe_tok}:${MATCH_CMD}"
      suggest="${suggest:+${suggest}; }${safe_tok}: ${MATCH_CMD}"
    fi
  done <<< "$CUR_TOKENS"
  sanitize "$CUR_SLUG" target
  safe_slug="$SAFE"

  if [[ -z "$matched_list" ]]; then
    status="skip"
    stack="${all_list:--}"
    if [[ -z "$all_list" ]]; then detail="no-stack"; else detail="no-lint-row"; fi
  elif [[ "$CUR_LINT" == "set" ]]; then
    status="pass"; stack="$matched_list"; detail="lint-set"
  else
    status="fail"; stack="$matched_list"; detail="$pairs"
    FAIL_LINES="${FAIL_LINES:+${FAIL_LINES}$'\n'}lint-command ${safe_slug}: stack ${stack} matches a Lint row but lintCommand is unset or empty; suggested command: ${suggest}. Surface this to the user; do not edit .cenci/config.json unprompted."
  fi
  printf -v line '%s\t%s\t%s\t%s\n' "$status" "$safe_slug" "$stack" "$detail"
  PLAIN_OUT="${PLAIN_OUT}${line}"
}

while IFS=$'\t' read -r kind field_a field_b; do
  case "$kind" in
    P)
      flush_project
      HAVE_PROJECT=1
      CUR_SLUG="$field_a"
      CUR_LINT="$field_b"
      CUR_TOKENS=""
      ;;
    T)
      CUR_TOKENS="${CUR_TOKENS:+${CUR_TOKENS}$'\n'}${field_a}"
      ;;
    N)
      NO_PROJECTS=1
      ;;
  esac
done <<< "$ROWS"
flush_project

if [[ "$NO_PROJECTS" -eq 1 || -z "$PLAIN_OUT" ]]; then
  finish_skip "no-projects"
fi

if [[ "$MODE" == "plain" ]]; then
  printf '%s' "$PLAIN_OUT"
  exit 0
fi

[[ -n "$FAIL_LINES" ]] || exit 0

jq -n --arg ctx "$FAIL_LINES" \
  '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
exit 0
