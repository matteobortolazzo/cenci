#!/usr/bin/env bash
# check.sh — deterministic, LLM-free repo checker for the cenci `flow` project
# (ticket #530). Validates repo consistency across a fixed set of checks,
# generates/validates the marker-bounded documentation indexes in
# flow/README.md, and emits text plus a machine-readable JSON report.
#
# Modes:
#   check.sh                     full repo check (default)
#   check.sh --changed <f> ...   only run checks relevant to the given
#                                 repo-root-relative changed files; unrelated
#                                 context-budget breaches downgrade to "warn"
#   check.sh --strict            full check; any "warn" also fails CI
#   check.sh --advisory          repository-read-only static checks; JSON is
#                                 emitted on stdout and executable/network
#                                 checks are explicitly skipped
#   check.sh --report-file PATH  atomically write JSON to PATH instead of the
#                                 default .cenci/maintain-report.json
#   check.sh --write              regenerate marker-bounded generated sections
#                                 from canonical sources; never touches content
#                                 outside a marker pair; fails closed (no
#                                 mutation, non-zero exit) on malformed/missing/
#                                 duplicate marker pairs — see
#                                 flow/docs/skill-authoring.md
#
# Output: one text line per non-pass result:
#   "FAIL/WARN/SKIP <check> <target>: <message> -> fix: <fix>"
# plus a JSON report written to "<repo-root>/.cenci/maintain-report.json":
#   { "summary": {pass,warn,fail,skip,mode}, "results": [{check,target,status,message,fix}] }
#
# Exit codes: 0 unless a "fail" result exists, 1 for findings, and 2 for usage
# or checker/report infrastructure errors. --strict additionally fails on any
# "warn". "skip" never blocks.
#
# Checker scope: the flow project (slug "flow") plus repo-root instruction/
# config files — not watch/ or sandbox/ (see the ticket #530 plan). Check
# BODIES remain flow-scoped under this charter; ticket #532 only extends
# --changed relevance (is_relevant()) so a change under any configured
# sibling project (e.g. watch/, sandbox/) maps to the three cross-project
# checks (command-flags, config-examples, roadmap-status) instead of being a
# silent no-op. No existing check body gained project-generalization logic,
# with one deliberate, documented exception: ticket #753 widens
# check_context_budget itself into a cross-project check BODY — it now scans
# the repo root plus every configured projects[].path (via PROJECT_PATHS),
# not just $ROOT + $FLOW_DIR. Every other check body stays flow-scoped.
set -uo pipefail

# Force deterministic sort/glob ordering regardless of the invoking
# environment's locale. Bash's pathname expansion (e.g. `skills/*/`) and
# `sort` both collate per LC_COLLATE, so the same directory listing can come
# out in a different order on an author's machine (e.g. en_US.UTF-8, which
# sorts "babysit" before "babysit-attention") than on a CI runner (typically
# C/C.UTF-8, which sorts the opposite way). Since generated section content
# must byte-for-byte match between --write and validation runs, every
# ordering-sensitive operation in this script needs one fixed collation.
export LC_ALL=C

# --- Checker-owned context-budget thresholds --------------------------------
# Single source of truth for #531 ("/cenci:maintain rules"): reference these
# constants rather than copying the numbers.
CRITICAL_RULES_MAX=10
TOPIC_DOC_RULES_MAX=25
# CRITICAL_RULE_MAX_LEN (#753) — per-bullet length cap, AGENTS.md's
# "## Critical Rules" only (topic-doc "## Rules" bullets are deliberately
# uncapped). Measured on the raw source line, including the "- " prefix and
# all markdown formatting (backticks, links) as-is; hard-wrapped continuation
# lines are joined into one logical bullet first (see check_context_budget)
# so a hard-wrap can't dodge the cap. Measured in bytes, under this script's
# `export LC_ALL=C` (line 51). This repo's current longest bullet
# (watch/AGENTS.md) is 291 bytes — only 9 bytes of headroom under 300.
CRITICAL_RULE_MAX_LEN=300
# EVASION_BULLET_MIN (#753) — the >=N top-level-bullet trigger for the
# anti-evasion warn below.
EVASION_BULLET_MIN=5
# EVASION_EXEMPT_HEADINGS (#753) — newline-delimited heading texts exempt
# from the anti-evasion warn: the two rule-ledger headings, plus the
# allowlisted structural headings. Exact-match, case-sensitive.
EVASION_EXEMPT_HEADINGS="Critical Rules
Rules
Reference Docs
Rule Files
Build & Test
Project Structure
Stack
Dependency version pins
Key Conventions"

# This script's own directory -- deliberately independent of $FLOW_DIR, which
# is the *target* repo's flow project directory and, under
# flow/tests/maintain.test.sh, is a synthetic fixture repo without a full
# flow/ tree (e.g. no hooks/scripts/). check_gate_command needs the real,
# bundled flow/hooks/scripts/run-gate.sh next to this script, not whatever
# happens to exist under the target repo's resolved flow directory.
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "check.sh: failed to resolve script directory." >&2; exit 2; }
RUN_GATE_SH="${SELF_DIR}/../../../hooks/scripts/run-gate.sh"
STALENESS_SH="${SELF_DIR}/../../../hooks/scripts/check-config-staleness.sh"
AGENT_ROLE_DRIFT_SH="${SELF_DIR}/../../../hooks/scripts/check-agent-role-drift.sh"

MARKER_IDS=(skills agents workflow-deps docs-nav)

MODE_STRICT=0
MODE_CHANGED=0
MODE_WRITE=0
MODE_ADVISORY=0
REPORT_FILE=""
CHANGED_FILES=()

COUNT_PASS=0
COUNT_WARN=0
COUNT_FAIL=0
COUNT_SKIP=0
RESULTS_FILE=""
MARKERS_OK=1
PROJECT_PATHS_OK=1
declare -A MARKER_PRESENT

# ============================================================================
# Argument parsing
# ============================================================================
parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --changed)
        MODE_CHANGED=1
        shift
        while [[ $# -gt 0 && "$1" != --* ]]; do
          CHANGED_FILES+=("$1")
          shift
        done
        ;;
      --strict)
        MODE_STRICT=1
        shift
        ;;
      --write)
        MODE_WRITE=1
        shift
        ;;
      --advisory)
        MODE_ADVISORY=1
        shift
        ;;
      --report-file)
        if [[ $# -lt 2 || -z "$2" || "$2" == --* ]]; then
          echo "check.sh: --report-file requires a path" >&2
          exit 2
        fi
        REPORT_FILE="$2"
        shift 2
        ;;
      *)
        echo "check.sh: unknown argument: $1" >&2
        exit 2
        ;;
    esac
  done
  if [[ "$MODE_ADVISORY" -eq 1 && "$MODE_WRITE" -eq 1 ]]; then
    echo "check.sh: --advisory cannot be combined with --write" >&2
    exit 2
  fi
  if [[ "$MODE_ADVISORY" -eq 1 && -n "$REPORT_FILE" ]]; then
    echo "check.sh: --advisory emits JSON on stdout and cannot be combined with --report-file" >&2
    exit 2
  fi
}

# ============================================================================
# Small shared helpers
# ============================================================================
relpath() {
  local p="$1"
  case "$p" in
    "$ROOT"/*) printf '%s' "${p#"$ROOT"/}" ;;
    *) printf '%s' "$p" ;;
  esac
}

# fm_field <file> <field> — extract a single-line front-matter value (the raw
# text after "field:", including any embedded colons/quotes). Only scans the
# leading "---"-delimited block; never splits on whitespace, so a quoted,
# colon-containing description parses cleanly (Risk mitigation, plan).
fm_field() {
  local file="$1" field="$2"
  awk -v field="$field" '
    NR==1 && $0=="---" { inFM=1; next }
    inFM && $0=="---" { exit }
    inFM && $0 ~ ("^" field ":") {
      line=$0
      sub("^" field ":[ \t]*", "", line)
      gsub(/^"|"$/, "", line)
      print line
      exit
    }
  ' "$file" 2>/dev/null
}

# agent_purpose <file> — first non-empty line of the description, handling
# both single-line quoted values and block-scalar (`description: |`) values.
agent_purpose() {
  local file="$1"
  awk '
    NR==1 && $0=="---" { inFM=1; next }
    inFM && $0=="---" { exit }
    inFM && $0 ~ /^description:/ {
      rest=$0
      sub(/^description:[ \t]*/, "", rest)
      if (rest == "|" || rest == ">" || rest == "") { block=1; next }
      gsub(/^"|"$/, "", rest)
      print rest
      exit
    }
    inFM && block {
      line=$0
      sub(/^[ \t]+/, "", line)
      if (line == "") next
      gsub(/^"|"$/, "", line)
      print line
      exit
    }
  ' "$file" 2>/dev/null
}

list_join() {
  # shellcheck disable=SC2039
  printf '%s' "$1" | paste -sd ',' - 2>/dev/null | sed 's/,/, /g'
}

portable_skills_list() {
  grep '^PORTABLE_SKILLS=' "$FLOW_DIR/opencode/install-skills.sh" 2>/dev/null \
    | sed -E 's/^PORTABLE_SKILLS="([^"]*)".*/\1/'
}

is_portable_skill() {
  local name="$1" list
  list="$(portable_skills_list)"
  [[ " ${list} " == *" ${name} "* ]]
}

# config_is_monorepo <cfg> — prints "true"/"false"; shared by every call site
# that needs to branch on .cenci/config.json's top-level isMonorepo flag.
config_is_monorepo() {
  jq -r 'if (.isMonorepo // false) == true then "true" else "false" end' "$1" 2>/dev/null
}

generated_docs_enabled() {
  local cfg="$ROOT/.cenci/config.json"
  [[ -f "$cfg" ]] || return 0
  ! jq -e '.maintenance.generatedDocs? == false' "$cfg" >/dev/null 2>&1
}

# flow_project_field <cfg> <field> <default> — reads <field> off the
# .projects[] entry with slug=="flow", applying the jq empty-string
# convention (never a bare `//`, since "" is truthy for `//`) and falling
# back to <default> when the field is missing/empty/absent.
flow_project_field() {
  local cfg="$1" field="$2" default="$3"
  jq -r --arg field "$field" --arg default "$default" \
    '[.projects[]? | select(.slug=="flow")][0] // {} | .[$field] as $v | if ($v // "") != "" then $v else $default end' \
    "$cfg" 2>/dev/null
}

resolve_flow_dir() {
  local cfg="$ROOT/.cenci/config.json"
  FLOW_DIR="$ROOT/flow"
  [[ -f "$cfg" ]] || return 0
  local path
  if [[ "$(config_is_monorepo "$cfg")" == "true" ]]; then
    path="$(flow_project_field "$cfg" path flow)"
    if [[ -n "$path" && "$path" != "null" ]] && project_path_is_safe "$path"; then
      FLOW_DIR="$ROOT/$path"
    fi
  else
    FLOW_DIR="$ROOT"
  fi
}

canonicalize_dir_allow_missing() {
  local candidate="$1" tail="" segment resolved_ancestor
  while [[ ! -d "$candidate" ]]; do
    [[ ! -L "$candidate" ]] || return 1
    segment="${candidate##*/}"
    [[ -n "$segment" ]] || return 1
    if [[ -z "$tail" ]]; then
      tail="$segment"
    else
      tail="$segment/$tail"
    fi
    candidate="${candidate%/*}"
    [[ -n "$candidate" ]] || candidate="/"
  done
  resolved_ancestor="$(cd "$candidate" 2>/dev/null && pwd -P)" || return 1
  if [[ -n "$tail" ]]; then
    printf '%s/%s\n' "$resolved_ancestor" "$tail"
  else
    printf '%s\n' "$resolved_ancestor"
  fi
}

project_path_is_safe() {
  local path="$1" resolved
  [[ -n "$path" && "$path" != /* ]] || return 1
  case "/$path/" in
    */../*|*/./*) return 1 ;;
  esac
  resolved="$(canonicalize_dir_allow_missing "$ROOT/$path")" || return 1
  case "$resolved" in
    "$ROOT"|"$ROOT"/*) ;;
    *) return 1 ;;
  esac
  return 0
}

# Populate PROJECT_PATHS (repo-root-relative dir prefixes for every configured
# project) from .cenci/config.json's projects[].path. Single-repo configs yield
# ("") meaning the repo root. Used by is_relevant()'s --changed mapping so a
# change under any project subtree maps to the cross-project checks instead of a
# silent no-op; it never widens what a check body scans (that stays flow-scoped
# per the #530 charter) -- except that PROJECT_PATHS now additionally feeds
# check_context_budget's scan list via context_budget_scan_dirs() (#753);
# every other check body remains flow-scoped.
resolve_project_dirs() {
  local cfg="$ROOT/.cenci/config.json" p slug jq_out jq_rc=0 any_fail=0 checked=0
  PROJECT_PATHS=()
  PROJECT_PATHS_OK=1
  if [[ -f "$cfg" && "$(config_is_monorepo "$cfg")" == "true" ]]; then
    jq_out="$(jq -r '.projects[]? | [(.slug // "(unknown)"), (.path // "")] | @tsv' "$cfg" 2>/dev/null)"
    jq_rc=$?
    if [[ "$jq_rc" -eq 0 ]]; then
      checked=1
      while IFS=$'\t' read -r slug p; do
        [[ -n "$slug" || -n "$p" ]] || continue
        if ! project_path_is_safe "$p"; then
          add_result config-paths "$slug" fail \
            "configured project path '${p:-<empty>}' escapes repository root or is not a safe repo-relative path" \
            "set projects[].path for '$slug' to a non-empty repository-relative path without '.' or '..' segments"
          any_fail=1
          PROJECT_PATHS_OK=0
          continue
        fi
        PROJECT_PATHS+=("$p")
      done <<< "$jq_out"
    fi
    # jq_rc != 0 here means a genuinely malformed config.json (distinct from
    # a legitimate single-repo/no-.projects[] config, which jq parses cleanly
    # and just yields no output) -- deliberately not conflated with that case
    # above; PROJECT_PATHS still falls through to the single-repo default
    # below either way. This is safe because PROJECT_PATHS only ever feeds
    # is_relevant()'s --changed relevance mapping (never a check body's scan
    # scope), and the parse failure itself is already surfaced loudly and
    # separately by check_invalid_json's own dedicated result.
  fi
  [[ "${#PROJECT_PATHS[@]}" -eq 0 ]] && PROJECT_PATHS=("")
  # Only a monorepo config whose project list was actually read earns a
  # result: a single-repo or absent config has no configured paths to
  # validate, so claiming "all paths stay within the repository" there
  # would be a pass for a check that never ran.
  if [[ "$checked" -eq 1 && "$any_fail" -eq 0 ]]; then
    add_result config-paths "(repo)" pass "all configured project paths stay within the repository" ""
  elif [[ "$jq_rc" -ne 0 ]]; then
    add_result config-paths "(repo)" skip "configured project paths could not be read from invalid JSON" ""
  fi
}

# file_under_project <repo-root-relative-path> — true if the path falls under
# any configured project's directory (or the config is single-repo, in which
# case everything is "in-project"). Consumed only by is_relevant().
file_under_project() {
  local f="$1" p
  for p in "${PROJECT_PATHS[@]}"; do
    [[ -z "$p" ]] && return 0                 # single-repo: everything is in-project
    case "$f" in "$p"/*) return 0 ;; esac
  done
  return 1
}

# ============================================================================
# Result buffer (NDJSON) + text/JSON rendering
# ============================================================================
add_result() {
  local check="$1" target="$2" status="$3" message="$4" fix="${5:-}"
  jq -nc --arg check "$check" --arg target "$target" --arg status "$status" \
    --arg message "$message" --arg fix "$fix" \
    '{check:$check, target:$target, status:$status, message:$message, fix:$fix}' >> "$RESULTS_FILE"
  case "$status" in
    pass) COUNT_PASS=$((COUNT_PASS+1)) ;;
    warn) COUNT_WARN=$((COUNT_WARN+1)) ;;
    fail) COUNT_FAIL=$((COUNT_FAIL+1)) ;;
    skip) COUNT_SKIP=$((COUNT_SKIP+1)) ;;
  esac
  if [[ "$status" != "pass" && "$MODE_ADVISORY" -eq 0 ]]; then
    local upper
    upper="$(printf '%s' "$status" | tr '[:lower:]' '[:upper:]')"
    printf '%s %s %s: %s -> fix: %s\n' "$upper" "$check" "$target" "$message" "$fix"
  fi
}

build_report() {
  local mode="$1"
  jq -s --arg mode "$mode" --argjson pass "$COUNT_PASS" --argjson warn "$COUNT_WARN" \
    --argjson fail "$COUNT_FAIL" --argjson skip "$COUNT_SKIP" \
    '{summary:{pass:$pass,warn:$warn,fail:$fail,skip:$skip,mode:$mode}, results: .}' \
    "$RESULTS_FILE"
}

write_report_atomically() {
  local destination="$1" mode="$2" parent tmp
  if [[ -d "$destination" ]]; then
    echo "check.sh: report path is a directory, expected a file: $destination" >&2
    return 1
  fi
  parent="$(dirname "$destination")"
  if [[ ! -d "$parent" ]] && ! mkdir -p "$parent" 2>/dev/null; then
    echo "check.sh: failed to create report directory: $parent" >&2
    return 1
  fi
  tmp="$(mktemp "${parent}/.maintain-report.XXXXXX")" || {
    echo "check.sh: failed to create temporary report next to: $destination" >&2
    return 1
  }
  if ! build_report "$mode" > "$tmp"; then
    rm -f "$tmp"
    echo "check.sh: failed to render report: $destination" >&2
    return 1
  fi
  if ! mv -f -- "$tmp" "$destination" 2>/dev/null; then
    rm -f "$tmp"
    echo "check.sh: failed to write report atomically: $destination" >&2
    return 1
  fi
}

# ============================================================================
# --changed narrowing
# ============================================================================
is_changed() {
  local rel="$1" f
  for f in "${CHANGED_FILES[@]:-}"; do
    [[ "$f" == "$rel" ]] && return 0
  done
  return 1
}

# is_relevant <check-id> — in full/strict mode everything is relevant; in
# --changed mode only categories whose inputs intersect the given file list
# run at all (context-budget/gate-command/github-labels always run since they
# reflect overall repo health, not a single file's domain).
is_relevant() {
  local check_id="$1"
  [[ "$MODE_CHANGED" -eq 1 ]] || return 0
  case "$check_id" in
    context-budget|gate-command|github-labels) return 0 ;;
  esac
  local f
  for f in "${CHANGED_FILES[@]:-}"; do
    case "$check_id" in
      front-matter|duplicate-names|broken-refs|orphan-files)
        case "$f" in flow/skills/*|flow/agents/*) return 0 ;; esac ;;
      instruction-docs)
        case "$f" in AGENTS.md|CLAUDE.md|flow/AGENTS.md|flow/CLAUDE.md) return 0 ;; esac ;;
      topic-docs)
        case "$f" in docs/*.md|flow/docs/*.md|AGENTS.md|flow/AGENTS.md) return 0 ;; esac ;;
      invalid-json)
        case "$f" in *.json) return 0 ;; esac ;;
      config-version)
        case "$f" in .cenci/config.json) return 0 ;; esac ;;
      agent-roles)
        case "$f" in .codex/agents/*|flow/templates/codex/agent-roles/*) return 0 ;; esac ;;
      shell-syntax)
        case "$f" in *.sh) return 0 ;; esac ;;
      stale-generated|capability-table|adapter-drift)
        case "$f" in flow/skills/*|flow/agents/*|flow/opencode/install-skills.sh|flow/README.md|flow/docs/*.md) return 0 ;; esac ;;
      structural-tests)
        case "$f" in flow/*.test.sh) return 0 ;; esac ;;
      worktree-ignored|plans-ignored|pipeline-state-ignored)
        case "$f" in .gitignore) return 0 ;; esac ;;
      claude-rules-imports)
        case "$f" in .claude/rules/*|AGENTS.md|CLAUDE.md|flow/AGENTS.md|flow/CLAUDE.md) return 0 ;; esac ;;
      legacy-lessons)
        case "$f" in .claude/rules/lessons-learned*) return 0 ;; esac ;;
      command-flags)
        case "$f" in docs/*.md|flow/docs/*.md|flow/skills/*|flow/README.md|README.md|watch/*_cmd.go|docs/cli-conventions.md) return 0 ;; esac ;;
      config-examples)
        case "$f" in docs/*.md|flow/docs/*.md|flow/README.md|README.md|.cenci/config.json) return 0 ;; esac ;;
      roadmap-status)
        case "$f" in docs/roadmap.md|flow/skills/*|flow/agents/*|watch/*_cmd.go) return 0 ;; esac
        file_under_project "$f" && return 0 ;;
    esac
  done
  return 1
}

# ============================================================================
# Marker engine
# ============================================================================
extract_marker_body() {
  local file="$1" id="$2"
  awk -v s="<!-- cenci-maintain:${id}:start -->" -v e="<!-- cenci-maintain:${id}:end -->" '
    $0==s { inside=1; next }
    $0==e { inside=0; next }
    inside { print }
  ' "$file"
}

replace_marker_body() {
  local file="$1" id="$2" newbody="$3"
  local bodyfile out
  bodyfile="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  out="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  printf '%s\n' "$newbody" > "$bodyfile"
  awk -v s="<!-- cenci-maintain:${id}:start -->" -v e="<!-- cenci-maintain:${id}:end -->" -v bf="$bodyfile" '
    $0==s { print; while ((getline line < bf) > 0) print line; close(bf); inside=1; next }
    $0==e { inside=0; print; next }
    inside { next }
    { print }
  ' "$file" > "$out"
  mv "$out" "$file"
  rm -f "$bodyfile"
}

validate_markers() {
  local file="$1" id sc ec sline eline
  MARKERS_OK=1
  for id in "${MARKER_IDS[@]}"; do
    sc="$(grep -cF "<!-- cenci-maintain:${id}:start -->" "$file" 2>/dev/null || true)"
    ec="$(grep -cF "<!-- cenci-maintain:${id}:end -->" "$file" 2>/dev/null || true)"
    sc="${sc:-0}"; ec="${ec:-0}"
    if [[ "$sc" -eq 0 && "$ec" -eq 0 ]]; then
      MARKER_PRESENT[$id]=0
      add_result stale-generated "$(relpath "$file")" fail \
        "required cenci-maintain:${id} marker pair is missing from $(relpath "$file")" \
        "add exactly one start and end marker for '${id}', then run check.sh --write"
      MARKERS_OK=0
      continue
    fi
    MARKER_PRESENT[$id]=1
    if [[ "$sc" -ne 1 || "$ec" -ne 1 ]]; then
      add_result stale-generated "$(relpath "$file")" fail \
        "malformed cenci-maintain:${id} marker pair in $(relpath "$file") (start=${sc}, end=${ec})" \
        "fix $(relpath "$file") to contain exactly one start and one end marker for '${id}', then re-run check.sh --write"
      MARKERS_OK=0
      continue
    fi
    # Counts alone are not enough: a hand-edit/merge can leave a single start
    # and a single end marker present but in reversed order. replace_marker_body's
    # awk state machine goes "inside" at whichever start marker it hits first
    # and never sees a closing end marker again if the real end line is
    # earlier in the file -- silently deleting every line through EOF on
    # --write, with no error and a zero exit. Assert start precedes end too.
    sline="$(grep -nF "<!-- cenci-maintain:${id}:start -->" "$file" 2>/dev/null | head -1 | cut -d: -f1)"
    eline="$(grep -nF "<!-- cenci-maintain:${id}:end -->" "$file" 2>/dev/null | head -1 | cut -d: -f1)"
    if [[ -z "$sline" || -z "$eline" || "$sline" -ge "$eline" ]]; then
      add_result stale-generated "$(relpath "$file")" fail \
        "cenci-maintain:${id} marker pair in $(relpath "$file") is out of order (start at line ${sline:-?}, end at line ${eline:-?})" \
        "fix $(relpath "$file") so the '${id}' start marker precedes its end marker, then re-run check.sh --write"
      MARKERS_OK=0
    fi
  done
}

generate_skills_section() {
  printf '%s\n' "<!-- Auto-generated by flow/skills/maintain/scripts/check.sh --write. Do not hand-edit. -->"
  printf '%s\n' ""
  printf '%s\n' "| Skill | Description | OpenCode | User-invocable | Claude Code | Codex |"
  printf '%s\n' "|---|---|---|---|---|---|"
  local d skillname name desc ui claude codex_col opencode_col
  for d in "$FLOW_DIR"/skills/*/; do
    [[ -f "${d}SKILL.md" ]] || continue
    skillname="$(basename "$d")"
    name="$(fm_field "${d}SKILL.md" name)"; name="${name:-$skillname}"
    desc="$(fm_field "${d}SKILL.md" description)"
    ui="$(fm_field "${d}SKILL.md" user-invocable)"
    if [[ "$ui" == "true" ]]; then ui="Yes"; else ui="No"; fi
    claude="Yes"
    if is_portable_skill "$skillname"; then opencode_col="Yes"; else opencode_col="No"; fi
    if is_portable_skill "$skillname" || [[ -f "${d}codex.md" ]]; then codex_col="Yes"; else codex_col="No"; fi
    # OpenCode is the leftmost Yes/No column: check_capability_table (and the
    # retired portability.test.sh's drift logic it replaces) hand-edits this
    # exact cell in fixtures, so it must be unambiguous to locate positionally.
    printf '| `%s` | %s | %s | %s | %s | %s |\n' "$name" "$desc" "$opencode_col" "$ui" "$claude" "$codex_col"
  done
}

referencing_skills() {
  local agent="$1"
  grep -lE "\`${agent}\`" "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/skills/*/codex.md \
    "$FLOW_DIR"/skills/*/phases/*.md "$FLOW_DIR"/skills/*/modes/*.md 2>/dev/null \
    | sed -E "s#.*/skills/([^/]+)/.*#\\1#" | sort -u | paste -sd ', ' -
}

generate_agents_section() {
  printf '%s\n' "<!-- Auto-generated by flow/skills/maintain/scripts/check.sh --write. Do not hand-edit. -->"
  printf '%s\n' ""
  printf '%s\n' "| Agent | Purpose | Referencing skills |"
  printf '%s\n' "|---|---|---|"
  local f name purpose refs
  for f in "$FLOW_DIR"/agents/*.md; do
    [[ -f "$f" ]] || continue
    name="$(fm_field "$f" name)"
    purpose="$(agent_purpose "$f")"
    refs="$(referencing_skills "$name")"
    printf '| `%s` | %s | %s |\n' "$name" "$purpose" "${refs:-—}"
  done
}

other_skill_refs() {
  local skilldir="$1" self="$2" all_names tok
  all_names="$(for d in "$FLOW_DIR"/skills/*/; do basename "$d"; done | sort -u)"
  grep -hoE '`[a-zA-Z0-9_-]+`' "$skilldir/SKILL.md" "$skilldir/codex.md" \
    "$skilldir"/phases/*.md "$skilldir"/modes/*.md 2>/dev/null | tr -d '`' | sort -u | while read -r tok; do
    [[ -z "$tok" || "$tok" == "$self" ]] && continue
    grep -qxF -- "$tok" <<< "$all_names" && printf '%s\n' "$tok"
  done
}

# portable_dependency_refs <skilldir> <self> — the skills this skill is
# *directed to consult*, as opposed to skills it merely names in prose.
#
# Direction is the whole point and a bare token match cannot see it.
# other_skill_refs (which backs README's "Reference skills" column) matches
# any backticked skill name, so it cannot tell "Read `project-core`" — an
# outbound dependency that breaks when project-core is absent — from
# "`refine` and `implement` apply exactly this rule", which is a skill
# documenting its own *callers* and depends on nothing. A descriptive column
# can tolerate that conflation; a failing gate cannot, so this narrower
# extractor keys on the consult-verb idiom already used consistently across
# flow's skills (Read/Use/Follow/Apply/Consult/Load/per/via `X`). Tokens that
# are not skill names (`--once`, `execute`) drop out on the membership test
# below, same as in other_skill_refs.
portable_dependency_refs() {
  local skilldir="$1" self="$2" all_names tok
  all_names="$(for d in "$FLOW_DIR"/skills/*/; do basename "$d"; done | sort -u)"
  grep -hoiE '(read|use|follow|apply|consult|load|per|via)[[:space:]]+(the[[:space:]]+)?`[a-zA-Z0-9_-]+`' \
    "$skilldir/SKILL.md" "$skilldir/codex.md" \
    "$skilldir"/phases/*.md "$skilldir"/modes/*.md 2>/dev/null \
    | grep -oE '`[a-zA-Z0-9_-]+`' | tr -d '`' | sort -u | while read -r tok; do
    [[ -z "$tok" || "$tok" == "$self" ]] && continue
    grep -qxF -- "$tok" <<< "$all_names" && printf '%s\n' "$tok"
  done
}

agent_refs() {
  local skilldir="$1" all_agents tok
  all_agents="$(for f in "$FLOW_DIR"/agents/*.md; do [[ -f "$f" ]] && fm_field "$f" name; done | sort -u)"
  grep -hoE '`[a-zA-Z0-9_-]+`' "$skilldir/SKILL.md" "$skilldir/codex.md" \
    "$skilldir"/phases/*.md "$skilldir"/modes/*.md 2>/dev/null | tr -d '`' | sort -u | while read -r tok; do
    [[ -z "$tok" ]] && continue
    grep -qxF -- "$tok" <<< "$all_agents" && printf '%s\n' "$tok"
  done
}

generate_workflow_deps_section() {
  printf '%s\n' "<!-- Auto-generated by flow/skills/maintain/scripts/check.sh --write. Do not hand-edit. -->"
  printf '%s\n' ""
  printf '%s\n' "| Skill | Procedure files | Reference skills | Scripts | Agents |"
  printf '%s\n' "|---|---|---|---|---|"
  local d skillname sm procedures scripts refskills agents_col
  for d in "$FLOW_DIR"/skills/*/; do
    skillname="$(basename "$d")"
    sm="${d}SKILL.md"
    [[ -f "$sm" ]] || continue
    procedures="$(list_join "$(
      {
        [[ -f "${d}codex.md" ]] && printf '%s\n' "codex.md"
        find "${d}" -maxdepth 1 -name '*.md' ! -name 'SKILL.md' ! -name 'codex.md' -printf '%f\n' 2>/dev/null
        find "${d}modes" -maxdepth 1 -name '*.md' -printf 'modes/%f\n' 2>/dev/null
        find "${d}phases" -maxdepth 1 -name '*.md' -printf 'phases/%f\n' 2>/dev/null
      } | sort
    )")"
    scripts="$(list_join "$(find "${d}scripts" -maxdepth 1 -name '*.sh' ! -name '*.test.sh' 2>/dev/null | sort | xargs -n1 basename 2>/dev/null)")"
    refskills="$(list_join "$(other_skill_refs "$d" "$skillname")")"
    agents_col="$(list_join "$(agent_refs "$d")")"
    printf '| `%s` | %s | %s | %s | %s |\n' "$skillname" "${procedures:-—}" "${refskills:-—}" "${scripts:-—}" "${agents_col:-—}"
  done
}

generate_docs_nav_section() {
  printf '%s\n' "<!-- Auto-generated by flow/skills/maintain/scripts/check.sh --write. Do not hand-edit. -->"
  printf '%s\n' ""
  printf '%s\n' "**Optional integrations**"
  local f base
  for f in "$FLOW_DIR"/docs/codex.md "$FLOW_DIR"/docs/opencode.md; do
    [[ -f "$f" ]] && printf -- '- `docs/%s`\n' "$(basename "$f")"
  done
  printf '%s\n' ""
  printf '%s\n' "**Core workflow docs**"
  for f in "$FLOW_DIR"/docs/*.md; do
    [[ -f "$f" ]] || continue
    base="$(basename "$f")"
    case "$base" in codex.md|opencode.md) continue ;; esac
    printf -- '- `docs/%s`\n' "$base"
  done
  printf '%s\n' ""
  printf '%s\n' "**Maintainer operations**"
  # No doc currently qualifies for this bucket (the checker's own docs live
  # under flow/skills/maintain/, not flow/docs/) -- the taxonomy is emitted
  # unconditionally so a future maintainer-facing doc (e.g. a /cenci:maintain
  # runbook) has a defined home instead of inventing a fourth ad hoc heading.
}

generate_section() {
  case "$1" in
    skills) generate_skills_section ;;
    agents) generate_agents_section ;;
    workflow-deps) generate_workflow_deps_section ;;
    docs-nav) generate_docs_nav_section ;;
  esac
}

process_markers() {
  local file="$1" write="$2" do_compare="$3"
  validate_markers "$file"
  [[ "$MARKERS_OK" -eq 1 ]] || return 1
  [[ "$write" -eq 1 || "$do_compare" -eq 1 ]] || return 0

  local id expected actual tmp any_drift=0
  tmp="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  cp "$file" "$tmp"
  for id in "${MARKER_IDS[@]}"; do
    [[ "${MARKER_PRESENT[$id]}" -eq 1 ]] || continue
    expected="$(generate_section "$id")"
    actual="$(extract_marker_body "$file" "$id")"
    if [[ "$expected" != "$actual" ]]; then
      any_drift=1
      if [[ "$write" -eq 1 ]]; then
        replace_marker_body "$tmp" "$id" "$expected"
      else
        add_result stale-generated "$(relpath "$file")" fail \
          "the '${id}' generated section in $(relpath "$file") is out of date relative to its canonical source" \
          "run 'bash flow/skills/maintain/scripts/check.sh --write' to regenerate the '${id}' section"
      fi
    fi
  done
  if [[ "$write" -eq 1 ]]; then
    mv "$tmp" "$file"
  else
    rm -f "$tmp"
  fi
  if [[ "$any_drift" -eq 0 ]]; then
    add_result stale-generated "$(relpath "$file")" pass "all required generated sections match their canonical sources" ""
  elif [[ "$write" -eq 1 ]]; then
    add_result stale-generated "$(relpath "$file")" pass "generated sections were refreshed from canonical sources" ""
  fi
  return 0
}

# ============================================================================
# Check categories
# ============================================================================
_check_fm_file() {
  local f="$1" name desc had_fail=0
  name="$(fm_field "$f" name)"
  desc="$(fm_field "$f" description)"
  if [[ -z "$name" ]]; then
    add_result front-matter "$(relpath "$f")" fail "missing required 'name' in front matter" \
      "add a 'name:' field to $(relpath "$f")'s front matter"
    had_fail=1
  fi
  if [[ -z "$desc" ]]; then
    add_result front-matter "$(relpath "$f")" fail "missing required 'description' in front matter" \
      "add a 'description:' field to $(relpath "$f")'s front matter"
    had_fail=1
  fi
  return "$had_fail"
}

check_front_matter() {
  local any_fail=0 f
  for f in "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/agents/*.md; do
    [[ -f "$f" ]] || continue
    _check_fm_file "$f" || any_fail=1
  done
  [[ "$any_fail" -eq 0 ]] && add_result front-matter "(repo)" pass "all front matter blocks are valid" ""
  return 0
}

check_duplicate_names() {
  local tmp f name
  tmp="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  for f in "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/agents/*.md; do
    [[ -f "$f" ]] || continue
    name="$(fm_field "$f" name)"
    [[ -n "$name" ]] || continue
    printf '%s\t%s\n' "$name" "$(relpath "$f")" >> "$tmp"
  done
  local dups n
  dups="$(cut -f1 "$tmp" | sort | uniq -d)"
  if [[ -n "$dups" ]]; then
    while IFS= read -r n; do
      [[ -n "$n" ]] || continue
      local files
      files="$(awk -F'\t' -v nm="$n" '$1==nm{print $2}' "$tmp" | paste -sd ', ' -)"
      add_result duplicate-names "$n" fail "name '$n' is declared by multiple files: $files" \
        "rename one of the duplicate '$n' front-matter names to a unique value"
    done <<< "$dups"
  else
    add_result duplicate-names "(repo)" pass "no duplicate skill/agent names" ""
  fi
  rm -f "$tmp"
}

check_broken_refs() {
  local d f refs r any_fail=0
  for d in "$FLOW_DIR"/skills/*/; do
    [[ -f "${d}SKILL.md" ]] || continue
    for f in "${d}SKILL.md" "${d}codex.md" "${d}"phases/*.md "${d}"modes/*.md; do
      [[ -f "$f" ]] || continue
      refs="$(grep -oE '`scripts/[A-Za-z0-9_.-]+\.sh`' "$f" 2>/dev/null | tr -d '`' | sort -u)"
      [[ -z "$refs" ]] && continue
      while IFS= read -r r; do
        [[ -n "$r" ]] || continue
        if [[ ! -f "$d/$r" ]]; then
          add_result broken-refs "$(relpath "$f")" fail \
            "referenced script '$r' does not exist relative to its owning skill $(relpath "$d")" \
            "create $r under $(relpath "$d") or fix the reference in $(relpath "$f")"
          any_fail=1
        fi
      done <<< "$refs"
    done
  done
  [[ "$any_fail" -eq 0 ]] && add_result broken-refs "(repo)" pass "no broken scripts/ references found" ""
}

check_orphan_files() {
  local any_orphan=0 f name skilldir owner source referenced
  while IFS= read -r -d '' f; do
    name="$(basename "$f")"
    skilldir="$(dirname "$(dirname "$f")")"
    owner="$(basename "$skilldir")"
    referenced=0
    for source in "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/skills/*/codex.md \
      "$FLOW_DIR"/skills/*/phases/*.md "$FLOW_DIR"/skills/*/modes/*.md; do
      [[ -f "$source" ]] || continue
      grep -qE "\`scripts/${name}\`" "$source" 2>/dev/null || continue
      if [[ "$source" == "$skilldir/"* ]] || grep -qE "\`${owner}\`" "$source" 2>/dev/null; then
        referenced=1
        break
      fi
    done
    if [[ "$referenced" -eq 0 ]]; then
      add_result orphan-files "$(relpath "$f")" fail "script $(relpath "$f") is not referenced by any skill" \
        "reference it from its owning skill (e.g. \`scripts/${name}\`) or delete it if unused"
      any_orphan=1
    fi
  done < <(find "$FLOW_DIR"/skills -path '*/scripts/*.sh' ! -name '*.test.sh' \
    ! -path '*/maintain/scripts/*' -print0 2>/dev/null)
  # flow/skills/maintain/scripts/*.sh (this checker) is intentionally excluded:
  # the /cenci:maintain skill (and its SKILL.md, which would reference
  # check.sh) is out of scope for ticket #530 and lands in a later ticket.

  while IFS= read -r -d '' f; do
    name="$(basename "$f")"
    skilldir="$(dirname "$(dirname "$f")")"
    if ! grep -qE "\`phases/${name}\`" "$skilldir/SKILL.md" 2>/dev/null; then
      add_result orphan-files "$(relpath "$f")" fail \
        "phase file $(relpath "$f") is not referenced by its skill's SKILL.md" \
        "reference it from $(relpath "$skilldir/SKILL.md") (e.g. \`phases/${name}\`) or delete it if unused"
      any_orphan=1
    fi
  done < <(find "$FLOW_DIR"/skills -path '*/phases/*.md' -print0 2>/dev/null)

  [[ "$any_orphan" -eq 0 ]] && add_result orphan-files "(repo)" pass "no orphan scripts or phase files" ""
}

check_instruction_docs() {
  local any_fail=0 guidance cfg="$ROOT/.cenci/config.json"
  guidance="$(jq -r 'if (.guidanceLocation // "") != "" then .guidanceLocation else "AGENTS.md" end' "$cfg" 2>/dev/null)"
  [[ -z "$guidance" || "$guidance" == "null" ]] && guidance="AGENTS.md"
  if [[ ! -f "$ROOT/$guidance" ]]; then
    add_result instruction-docs "$guidance" fail "missing root instruction file $guidance" \
      "create $guidance at the repo root (see /cenci:configure)"
    any_fail=1
  fi
  if [[ ! -f "$ROOT/CLAUDE.md" ]]; then
    add_result instruction-docs "CLAUDE.md" fail "missing root CLAUDE.md adapter" \
      "create CLAUDE.md at the repo root importing $guidance (e.g. '@$guidance')"
    any_fail=1
  fi
  if [[ ! -f "$FLOW_DIR/AGENTS.md" ]]; then
    add_result instruction-docs "flow/AGENTS.md" fail "missing flow project instruction file AGENTS.md" \
      "create flow/AGENTS.md with project-specific guidance"
    any_fail=1
  fi
  if [[ ! -f "$FLOW_DIR/CLAUDE.md" ]]; then
    add_result instruction-docs "flow/CLAUDE.md" fail "missing flow project CLAUDE.md adapter" \
      "create flow/CLAUDE.md importing flow/AGENTS.md"
    any_fail=1
  fi
  [[ "$any_fail" -eq 0 ]] && add_result instruction-docs "(repo)" pass "all configured instruction files are present" ""
}

# _topic_docs_for <agents_md> <base> [--check-orphans] — the [--check-orphans]
# direction (doc exists under <base>/docs/ but is never referenced by
# <agents_md>'s Reference Docs list) is opt-in and only ever passed for
# flow/AGENTS.md + $FLOW_DIR. The repo-root docs/ directory intentionally
# mixes AGENTS.md-referenced topic docs with broader README/installer-facing
# docs (getting-started.md, roadmap.md, etc.) that were never meant to be
# exhaustively enumerated in AGENTS.md's Reference Docs list, so applying
# orphan-detection there would flag long-standing, intentional docs as
# false-positive drift. flow/docs/ has no such mix: every file is accounted
# for by either the Reference Docs list or generate_docs_nav_section's
# "Optional integrations" bucket (codex.md, opencode.md), so it is the one
# directory where "referenced or generated-nav'd" is a complete invariant.
_topic_docs_for() {
  local agents_md="$1" base="$2" check_orphans="${3:-}"
  local lines l doc any_fail=0 any_checked=0 referenced=$'\n'
  [[ -f "$agents_md" ]] || return 0
  lines="$(awk '/^## Reference Docs/{f=1;next} /^## /{f=0} f' "$agents_md" 2>/dev/null \
    | grep -oE '`docs/[A-Za-z0-9_./-]+\.md`')"
  if [[ -n "$lines" ]]; then
    while IFS= read -r l; do
      [[ -n "$l" ]] || continue
      any_checked=1
      doc="$(tr -d '`' <<< "$l")"
      referenced="${referenced}${doc}"$'\n'
      if [[ ! -f "$base/$doc" ]]; then
        add_result topic-docs "$(relpath "$agents_md")" fail \
          "dangling reference to $doc in $(relpath "$agents_md")'s Reference Docs list" \
          "create $doc under $(relpath "$base") or remove the reference from $(relpath "$agents_md")"
        any_fail=1
      fi
    done <<< "$lines"
  fi

  if [[ "$check_orphans" == "--check-orphans" ]]; then
    # Optional-integration docs (codex.md, opencode.md) are deliberately
    # excluded from the Reference Docs convention -- generate_docs_nav_section
    # buckets them separately, and flow/README.md links them directly -- so
    # they are not "orphaned" by this convention's own design.
    local d rel
    for d in "$base"/docs/*.md; do
      [[ -f "$d" ]] || continue
      rel="docs/$(basename "$d")"
      case "$rel" in docs/codex.md|docs/opencode.md) continue ;; esac
      any_checked=1
      if ! grep -qxF -- "$rel" <<< "$referenced"; then
        add_result topic-docs "$(relpath "$d")" fail \
          "$rel exists under $(relpath "$base") but is not referenced by $(relpath "$agents_md")'s Reference Docs list" \
          "add a Reference Docs bullet for \`$rel\` to $(relpath "$agents_md"), or delete $rel if it is unused"
        any_fail=1
      fi
    done
  fi

  [[ "$any_checked" -eq 1 && "$any_fail" -eq 0 ]] && \
    add_result topic-docs "$(relpath "$agents_md")" pass "all referenced topic docs exist" ""
}

check_topic_docs() {
  _topic_docs_for "$ROOT/AGENTS.md" "$ROOT"
  _topic_docs_for "$FLOW_DIR/AGENTS.md" "$FLOW_DIR" --check-orphans
}

json_targets() {
  [[ -f "$ROOT/.cenci/config.json" ]] && printf '%s\0' "$ROOT/.cenci/config.json"
  find "$FLOW_DIR" -name '*.json' -print0 2>/dev/null
}

check_invalid_json() {
  local any_fail=0 f err
  while IFS= read -r -d '' f; do
    if ! err="$(jq empty "$f" 2>&1 1>/dev/null)"; then
      add_result invalid-json "$(relpath "$f")" fail "invalid JSON: ${err}" \
        "fix the JSON syntax error in $(relpath "$f")"
      any_fail=1
    fi
  done < <(json_targets)
  [[ "$any_fail" -eq 0 ]] && add_result invalid-json "(repo)" pass "all configured JSON files parse cleanly" ""
}

# check_config_version — is .cenci/config.json's configVersion stamp current
# for the bundled flow plugin? The staleness decision is delegated to
# hooks/scripts/check-config-staleness.sh --plain (the same script the
# SessionStart hook runs), so this check and the session-start advisory can
# never disagree. Staleness is advisory: stale/unstamped is a warn, never a
# fail; every unreadable input degrades to skip.
check_config_version() {
  local cfg="$ROOT/.cenci/config.json"
  if [[ ! -f "$cfg" ]]; then
    add_result config-version "(repo)" skip "no .cenci/config.json — repo is not configured" ""
    return
  fi
  if [[ ! -f "$STALENESS_SH" ]]; then
    add_result config-version "(repo)" skip "bundled check-config-staleness.sh not found next to this checker" ""
    return
  fi
  local plugin_dir out
  plugin_dir="$(cd "${SELF_DIR}/../../.." && pwd)" || {
    add_result config-version "(repo)" skip "could not resolve the bundled plugin directory" ""
    return
  }
  out="$(cd "$ROOT" && CLAUDE_PLUGIN_ROOT="$plugin_dir" bash "$STALENESS_SH" --plain 2>/dev/null)" || {
    add_result config-version "(repo)" skip "config-staleness resolver failed" ""
    return
  }
  local state cfg_v plug_v
  read -r state cfg_v plug_v <<< "$out"
  case "$state" in
    current)
      add_result config-version "(repo)" pass "configVersion ${cfg_v} is current for the installed flow plugin v${plug_v}" ""
      ;;
    stale)
      add_result config-version "(repo)" warn \
        ".cenci/config.json was written by /cenci:configure v${cfg_v}; the installed flow plugin is v${plug_v}" \
        "re-run /cenci:configure to pick up configure features added since (existing answers become defaults) and refresh configVersion"
      ;;
    unstamped)
      add_result config-version "(repo)" warn \
        ".cenci/config.json has no configVersion stamp (written by a pre-stamping /cenci:configure)" \
        "re-run /cenci:configure to stamp configVersion (existing answers become defaults)"
      ;;
    absent)
      add_result config-version "(repo)" skip "no .cenci/config.json — repo is not configured" ""
      ;;
    *)
      add_result config-version "(repo)" skip "could not determine config staleness (resolver said: ${out:-nothing})" ""
      ;;
  esac
}

# check_agent_roles — do this repo's installed .codex/agents/*.toml files
# still satisfy the Codex agent-role schema? (#1040) Delegated to
# hooks/scripts/check-agent-role-drift.sh --plain (the same script the
# SessionStart hook runs), so this check and the session-start advisory can
# never disagree. Drift is advisory: a warn, never a fail; every unreadable
# input degrades to skip.
check_agent_roles() {
  local agents_dir="$ROOT/.codex/agents"
  if [[ ! -d "$agents_dir" ]]; then
    add_result agent-roles "(repo)" skip "no .codex/agents/ — repo has no installed Codex agent roles" ""
    return
  fi
  if [[ ! -f "$AGENT_ROLE_DRIFT_SH" ]]; then
    add_result agent-roles "(repo)" skip "bundled check-agent-role-drift.sh not found next to this checker" ""
    return
  fi
  local out
  out="$(cd "$ROOT" && bash "$AGENT_ROLE_DRIFT_SH" --plain 2>/dev/null)" || {
    add_result agent-roles "(repo)" skip "agent-role-drift resolver failed" ""
    return
  }
  local state rest
  read -r state rest <<< "$out"
  case "$state" in
    clean)
      add_result agent-roles "(repo)" pass "installed .codex/agents/*.toml satisfy the agent-role schema" ""
      ;;
    drift)
      add_result agent-roles "(repo)" warn \
        "installed .codex/agents/*.toml no longer satisfy the agent-role schema: ${rest}" \
        "review a diff against flow/templates/codex/agent-roles/ and repair by hand — install-agents.sh never overwrites an existing agent file"
      ;;
    absent)
      add_result agent-roles "(repo)" skip "no .codex/agents/ — repo has no installed Codex agent roles" ""
      ;;
    *)
      add_result agent-roles "(repo)" skip "could not determine agent-role drift (resolver said: ${out:-nothing})" ""
      ;;
  esac
}

check_shell_syntax() {
  local any_fail=0 f err
  while IFS= read -r -d '' f; do
    if ! err="$(bash -n "$f" 2>&1 1>/dev/null)"; then
      add_result shell-syntax "$(relpath "$f")" fail "bash -n reports a syntax error: ${err}" \
        "fix the shell syntax error in $(relpath "$f")"
      any_fail=1
    fi
  done < <(find "$FLOW_DIR" -name '*.sh' -print0 2>/dev/null)
  [[ "$any_fail" -eq 0 ]] && add_result shell-syntax "(repo)" pass "all shell scripts under flow/ parse cleanly" ""
}

check_capability_table() {
  local readme="$FLOW_DIR/README.md" body hc_lines portable any_fail=0 checked=0
  [[ -f "$readme" ]] || { add_result capability-table "(repo)" skip "flow/README.md not found" ""; return; }
  portable="$(portable_skills_list)"
  local col_skill col_desc col_opencode col_ui col_claude col_codex col_notes _junk name ocval expect

  body="$(extract_marker_body "$readme" skills)"
  if [[ -n "$body" ]]; then
    checked=1
    while IFS='|' read -r _junk col_skill col_desc col_opencode col_ui col_claude col_codex _junk; do
      [[ "$col_skill" == *'`'* ]] || continue
      name="$(printf '%s' "$col_skill" | tr -d ' `')"
      ocval="$(printf '%s' "$col_opencode" | xargs 2>/dev/null)"
      expect="No"
      [[ " ${portable} " == *" ${name} "* ]] && expect="Yes"
      if [[ "$ocval" != "$expect" ]]; then
        add_result capability-table "$name" fail \
          "README skill inventory lists OpenCode='${ocval}' for '${name}' but install-skills.sh PORTABLE_SKILLS says '${expect}'" \
          "regenerate flow/README.md's skill inventory (check.sh --write) or fix flow/opencode/install-skills.sh's PORTABLE_SKILLS"
        any_fail=1
      fi
    done <<< "$body"
  fi

  # The hand-curated "## Skill portability" table (kept per Assumption 1's
  # fallback, since Codex/OpenCode support state there isn't mechanically
  # derivable) is a second source of truth for the same OpenCode fact as the
  # generated table above. It must not drift from PORTABLE_SKILLS either --
  # this closes the two-sources-of-truth gap left by retiring
  # flow/opencode/portability.test.sh. Stop at the next "## " heading or the
  # generated marker block, whichever comes first, so the generated tables
  # below it are never mistaken for hand-curated rows.
  hc_lines="$(awk '/^## Skill portability/{f=1;next} /^## /{f=0} /<!-- cenci-maintain:[a-zA-Z0-9_-]+:start -->/{f=0} f' "$readme" 2>/dev/null)"
  if [[ -n "$hc_lines" ]]; then
    checked=1
    local expected_rows actual_rows row count
    expected_rows="$(
      for name in $portable; do printf '%s\n' "$name"; done
      for f in "$FLOW_DIR"/skills/*/SKILL.md; do
        [[ -f "$f" ]] || continue
        [[ "$(fm_field "$f" user-invocable)" == "true" ]] && basename "$(dirname "$f")"
      done
    )"
    expected_rows="$(printf '%s\n' "$expected_rows" | sed '/^$/d' | sort -u)"
    actual_rows="$(printf '%s\n' "$hc_lines" | awk -F'`' '/^[|][[:space:]]*`[A-Za-z0-9_-]+`/{print $2}' | sort)"
    while IFS= read -r row; do
      [[ -n "$row" ]] || continue
      count="$(grep -xcF -- "$row" <<< "$actual_rows")"
      if [[ "$count" -eq 0 ]]; then
        add_result capability-table "$row" fail \
          "README hand-curated Skill portability table is missing expected skill row '$row'" \
          "add the '$row' row to flow/README.md's Skill portability table"
        any_fail=1
      elif [[ "$count" -gt 1 ]]; then
        add_result capability-table "$row" fail \
          "README hand-curated Skill portability table contains duplicate rows for '$row'" \
          "keep exactly one '$row' row in flow/README.md's Skill portability table"
        any_fail=1
      fi
    done <<< "$expected_rows"
    while IFS= read -r row; do
      [[ -n "$row" ]] || continue
      if ! grep -qxF -- "$row" <<< "$expected_rows"; then
        add_result capability-table "$row" fail \
          "README hand-curated Skill portability table contains unexpected or renamed skill row '$row'" \
          "remove '$row' or rename it to the current portable/user-invocable skill name"
        any_fail=1
      fi
    done < <(printf '%s\n' "$actual_rows" | sort -u)
    while IFS='|' read -r _junk col_skill col_claude col_codex col_opencode col_notes _junk; do
      [[ "$col_skill" == *'`'* ]] || continue
      name="$(printf '%s' "$col_skill" | tr -d ' `')"
      ocval="$(printf '%s' "$col_opencode" | xargs 2>/dev/null)"
      expect="No"
      [[ " ${portable} " == *" ${name} "* ]] && expect="Yes"
      if [[ "$ocval" != "$expect" ]]; then
        add_result capability-table "$name" fail \
          "README hand-curated Skill portability table lists OpenCode='${ocval}' for '${name}' but install-skills.sh PORTABLE_SKILLS says '${expect}'" \
          "fix flow/README.md's Skill portability table's OpenCode column for '${name}' or update flow/opencode/install-skills.sh's PORTABLE_SKILLS"
        any_fail=1
      fi
    done <<< "$hc_lines"
  fi

  if [[ "$checked" -eq 0 ]]; then
    add_result capability-table "(repo)" pass "no generated or hand-curated skill inventory to check yet" ""
    return
  fi
  [[ "$any_fail" -eq 0 ]] && add_result capability-table "(repo)" pass "README OpenCode column matches install-skills.sh PORTABLE_SKILLS" ""
}

check_adapter_drift() {
  local list name any_fail=0
  list="$(portable_skills_list)"
  for name in $list; do
    if [[ ! -d "$FLOW_DIR/skills/$name" ]]; then
      add_result adapter-drift "$name" fail \
        "install-skills.sh PORTABLE_SKILLS references '$name' but flow/skills/$name/ does not exist" \
        "remove '$name' from PORTABLE_SKILLS in flow/opencode/install-skills.sh or create the missing skill directory"
      any_fail=1
    fi
  done

  # A portable skill must be self-contained on the OpenCode client. OpenCode
  # delivery is a symlink of the skill directory alone (opencode/
  # install-skills.sh) -- there is no dependency resolution -- so a skill in
  # PORTABLE_SKILLS that references a skill outside it reaches OpenCode users
  # pointing at a skill they do not have. This is the mechanical half of
  # docs/skill-authoring.md's "Client surfaces" rule (#1042).
  #
  # Only outbound dependencies count -- see portable_dependency_refs for why
  # this deliberately does not reuse other_skill_refs.
  local refname
  for name in $list; do
    [[ -d "$FLOW_DIR/skills/$name" ]] || continue
    while read -r refname; do
      [[ -n "$refname" ]] || continue
      [[ " ${list} " == *" ${refname} "* ]] && continue
      add_result adapter-drift "$name" fail \
        "portable skill '$name' is directed to consult '$refname', which is not in PORTABLE_SKILLS — OpenCode installs '$name' without it" \
        "add '$refname' to PORTABLE_SKILLS in flow/opencode/install-skills.sh, or remove the '$refname' dependency from flow/skills/$name/"
      any_fail=1
    done < <(portable_dependency_refs "$FLOW_DIR/skills/$name/" "$name")
  done

  [[ "$any_fail" -eq 0 ]] && add_result adapter-drift "(repo)" pass "every PORTABLE_SKILLS entry has a matching flow/skills/ directory and references only portable skills" ""
}

# check_structural_tests — non-executing discovery assertion (ticket #720).
# Execution of *.test.sh suites is owned exclusively by flow/scripts/
# run-checks.sh (CI's flow-test job and the flow gateCommand both invoke it),
# and check_gate_command already runs that full 33+-suite gate in this same
# check.sh invocation -- executing suites here too would double-run the gate
# for zero added signal. This check instead only asserts that discovery
# itself is healthy: every *.test.sh under FLOW_DIR is readable and
# non-empty, and at least one was found.
#
# Discovery prunes .git/node_modules/vendor/.worktrees because in a
# single-project (non-monorepo) consumer repo, resolve_flow_dir sets
# FLOW_DIR="$ROOT" (check.sh's resolve_flow_dir), so unpruned discovery would
# walk the whole repository rather than just the flow tree.
check_structural_tests() {
  local any_fail=0 count=0 f
  local list
  list="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  if ! find "$FLOW_DIR" \( -name .git -o -name node_modules -o -name vendor -o -name .worktrees \) -prune \
    -o -type f -name '*.test.sh' -print0 | sort -z > "$list"; then
    add_result structural-tests "(repo)" fail "*.test.sh discovery under $(relpath "$FLOW_DIR") failed" \
      "investigate why find/sort failed while discovering *.test.sh files"
    rm -f "$list"
    return
  fi
  [[ -r "$list" ]] || { echo "check.sh: discovery list is not readable: $list" >&2; exit 2; }
  while IFS= read -r -d '' f; do
    count=$((count+1))
    if [[ ! -r "$f" ]]; then
      add_result structural-tests "$(relpath "$f")" fail "$(basename "$f") is not readable" \
        "restore read permission on $(relpath "$f")"
      any_fail=1
    elif [[ ! -s "$f" ]]; then
      add_result structural-tests "$(relpath "$f")" fail "$(basename "$f") is empty" \
        "restore the test assertions in $(relpath "$f") or remove the empty file"
      any_fail=1
    fi
  done < "$list" || { echo "check.sh: failed to read discovery list: $list" >&2; exit 2; }
  rm -f "$list"
  if [[ "$count" -eq 0 ]]; then
    add_result structural-tests "(repo)" fail "zero *.test.sh files discovered under $(relpath "$FLOW_DIR")" \
      "add at least one *.test.sh integration test under the flow tree"
  elif [[ "$any_fail" -eq 0 ]]; then
    add_result structural-tests "(repo)" pass "discovered ${count} *.test.sh scripts, all readable and non-empty" ""
  fi
}

check_worktree_ignored() {
  if [[ -f "$ROOT/.gitignore" ]] && grep -qE '^\.worktrees/?$' "$ROOT/.gitignore"; then
    add_result worktree-ignored "(repo)" pass ".worktrees/ is git-ignored" ""
  else
    add_result worktree-ignored ".gitignore" fail \
      ".worktrees/ (the configured worktree directory) is not listed in .gitignore" \
      "add '.worktrees/' to .gitignore"
  fi
}

check_plans_ignored() {
  if [[ -f "$ROOT/.gitignore" ]] && grep -qE '^\.plans/?$' "$ROOT/.gitignore"; then
    add_result plans-ignored "(repo)" pass ".plans/ is git-ignored" ""
  else
    add_result plans-ignored ".gitignore" fail \
      ".plans/ (session-specific plan files) is not listed in .gitignore" \
      "add '.plans/' to .gitignore"
  fi
}

check_pipeline_state_ignored() {
  if [[ -f "$ROOT/.gitignore" ]] && grep -qE '^\.cenci/pipeline/?$' "$ROOT/.gitignore"; then
    add_result pipeline-state-ignored "(repo)" pass ".cenci/pipeline/ is git-ignored" ""
  else
    add_result pipeline-state-ignored ".gitignore" fail \
      ".cenci/pipeline/ (transient per-run cenci pipeline state) is not listed in .gitignore" \
      "add '.cenci/pipeline/' to .gitignore"
  fi
}

# check_gate_command intentionally executes the repo-configured gateCommand
# (a test/build command from trusted, committed .cenci/config.json -- see
# docs/health-gates.md's trust-boundary note) via flow/hooks/scripts/run-gate.sh,
# the single resolver/executor every gate consumer shares. This is expected,
# not a vulnerability: .github/workflows/flow-ci.yml's `pull_request` trigger
# (never `pull_request_target`) plus its repo-wide `permissions: contents:
# read` mean a PR from a fork never runs with write credentials or secrets
# regardless of what gateCommand executes. Do not change that trigger to
# `pull_request_target` without re-deriving this reasoning.
check_gate_command() {
  local cfg="$ROOT/.cenci/config.json"
  if [[ ! -f "$cfg" ]] || ! jq -e . "$cfg" >/dev/null 2>&1; then
    add_result gate-command "(repo)" skip "no valid .cenci/config.json to resolve a gateCommand from" ""
    return
  fi

  # Resolve the gateCommand string purely to produce actionable messages
  # below -- run-gate.sh (invoked further down) is the single resolver AND
  # executor of record; this repeats none of its cd/sh -c/path-traversal-guard
  # logic, only its read-only jq field lookup.
  local gate_cmd slug_args=()
  if [[ "$(config_is_monorepo "$cfg")" == "true" ]]; then
    gate_cmd="$(flow_project_field "$cfg" gateCommand "")"
    slug_args=(flow)
  else
    gate_cmd="$(jq -r 'if (.gateCommand // "") != "" then .gateCommand else empty end' "$cfg" 2>/dev/null)"
  fi

  if [[ ! -f "$RUN_GATE_SH" ]]; then
    add_result gate-command "flow" skip "run-gate.sh not found at ${RUN_GATE_SH}; cannot resolve/execute gateCommand" ""
    return
  fi

  local out rc status
  out="$(cd "$ROOT" && sh "$RUN_GATE_SH" "${slug_args[@]}" 2>&1)"
  rc=$?
  status="$(printf '%s\n' "$out" | grep -oE '^GATE_STATUS=[a-z]+$' | tail -n1 | sed 's/^GATE_STATUS=//')"

  case "$status" in
    green)
      add_result gate-command "flow" pass "gateCommand passed for 'flow'" ""
      ;;
    unset)
      # .cenci/config.json is already known-valid at this point, so run-gate.sh
      # reporting "unset" here means no gateCommand is configured for the
      # 'flow' project -- maintain treats that as a fail (a configured
      # project should have a gate), unlike run-gate.sh's other consumers.
      add_result gate-command "flow" fail "no gateCommand configured for the flow project in .cenci/config.json" \
        "add a gateCommand for the 'flow' project in .cenci/config.json (see docs/health-gates.md)"
      ;;
    red)
      if [[ "$rc" -eq 127 ]]; then
        add_result gate-command "flow" skip "gateCommand could not be run in this environment (command not found): ${gate_cmd:-$out}" ""
      else
        add_result gate-command "flow" fail "gateCommand exited ${rc}: ${gate_cmd:-$out}" \
          "fix the failing gate for 'flow' (see docs/health-gates.md); reproduce locally: bash flow/hooks/scripts/run-gate.sh flow"
      fi
      ;;
    *)
      # No GATE_STATUS line at all -> run-gate.sh itself errored (missing
      # project directory, unresolvable/ambiguous slug, '..'-path-traversal
      # guard tripped, etc.) -- distinct from a red gate.
      if [[ "$rc" -eq 127 ]]; then
        add_result gate-command "flow" skip "gateCommand could not be run in this environment (command not found): ${gate_cmd:-$out}" ""
      else
        add_result gate-command "flow" fail "run-gate.sh could not resolve/execute the gateCommand for 'flow': ${out}" \
          "fix the failing gate for 'flow' (see docs/health-gates.md); reproduce locally: bash flow/hooks/scripts/run-gate.sh flow"
      fi
      ;;
  esac
}

check_claude_rules_imports() {
  local dir="$ROOT/.claude/rules"
  [[ -d "$dir" ]] || { add_result claude-rules-imports "(repo)" pass "no .claude/rules/ directory present" ""; return; }
  local imports f rel any_fail=0
  imports="$( { cat "$ROOT/AGENTS.md" "$ROOT/CLAUDE.md" "$FLOW_DIR/AGENTS.md" "$FLOW_DIR/CLAUDE.md" 2>/dev/null; } \
    | grep -oE '@\.claude/rules/[A-Za-z0-9_.-]+\.md' | sed 's/^@//' | sort -u)"
  for f in "$dir"/*.md; do
    [[ -f "$f" ]] || continue
    case "$(basename "$f")" in
      lessons-learned.md|lessons-learned-*.md) continue ;;
    esac
    rel=".claude/rules/$(basename "$f")"
    if ! grep -qxF -- "$rel" <<< "$imports"; then
      add_result claude-rules-imports "$rel" fail \
        "$rel exists but is not @-imported by any applicable AGENTS.md/CLAUDE.md" \
        "add '@$rel' to the appropriate AGENTS.md, or delete the file if unused"
      any_fail=1
    fi
  done
  [[ "$any_fail" -eq 0 ]] && add_result claude-rules-imports "(repo)" pass "every .claude/rules/ file is explicitly imported" ""
}

check_legacy_lessons() {
  local dir="$ROOT/.claude/rules"
  [[ -d "$dir" ]] || { add_result legacy-lessons "(repo)" pass "no legacy lessons-learned files present" ""; return; }
  local any=0 f
  for f in "$dir"/lessons-learned.md "$dir"/lessons-learned-*.md; do
    [[ -f "$f" ]] || continue
    add_result legacy-lessons "$(relpath "$f")" warn "legacy lessons file $(relpath "$f") remains" \
      "migrate its rules into docs/<topic>.md or CLAUDE.md via /cenci:maintain, then delete $(relpath "$f")"
    any=1
  done
  [[ "$any" -eq 0 ]] && add_result legacy-lessons "(repo)" pass "no legacy lessons-learned files present" ""
}

check_github_labels() {
  if ! command -v gh >/dev/null 2>&1; then
    add_result github-labels "(repo)" skip "gh CLI not available" ""
    return
  fi
  local remote_url owner_repo
  if ! remote_url="$(git -C "$ROOT" remote get-url origin 2>/dev/null)"; then
    add_result github-labels "(repo)" skip "no git 'origin' remote configured; cannot query GitHub labels" ""
    return
  fi
  owner_repo="$(printf '%s' "$remote_url" | sed -E 's#^(https://github\.com/|git@github\.com:)##; s#\.git$##')"
  if [[ -z "$owner_repo" || "$owner_repo" == "$remote_url" ]]; then
    add_result github-labels "(repo)" skip "origin remote is not a github.com repository" ""
    return
  fi
  local labels_json
  if ! labels_json="$(timeout 8 gh label list --repo "$owner_repo" --limit 100 --json name 2>/dev/null)"; then
    add_result github-labels "$owner_repo" skip "unable to query GitHub labels (offline or unauthenticated)" ""
    return
  fi
  local any_fail=0 l
  for l in Refined Planned Working "In Review" Implemented Followup \
    dispatch-failed plan-invalid reconcile-stuck; do
    if ! jq -e --arg l "$l" '[.[] | select(.name==$l)] | length > 0' <<< "$labels_json" >/dev/null 2>&1; then
      add_result github-labels "$owner_repo" fail "missing lifecycle label '$l'" \
        "run: gh label create \"$l\" --repo $owner_repo --color <hex> --description \"<description>\" (or re-run /cenci:configure to self-heal labels)"
      any_fail=1
    fi
  done
  [[ "$any_fail" -eq 0 ]] && add_result github-labels "$owner_repo" pass "all canonical lifecycle labels are present" ""
}

# context_budget_scan_dirs (#753) — prints one absolute directory per line
# for check_context_budget's scan list: always $ROOT first, then $ROOT/<p>
# for every non-empty PROJECT_PATHS entry, deduped. The "" sentinel
# (resolve_project_dirs' single-repo *or* absent-config marker) yields root
# only -- this deliberately drops the implicit $FLOW_DIR fallback that older
# revisions of check_context_budget used: an absent .cenci/config.json now
# scans root AGENTS.md + root docs/ only, not flow/ (#753, Q4). This is an
# intentional behavior change for absent-config repos, not an oversight.
context_budget_scan_dirs() {
  printf '%s\n' "$ROOT"
  local p dir seen=$'\n'
  for p in "${PROJECT_PATHS[@]}"; do
    [[ -n "$p" ]] || continue
    dir="$ROOT/$p"
    case "$seen" in
      *$'\n'"$dir"$'\n'*) continue ;;
    esac
    seen="${seen}${dir}"$'\n'
    printf '%s\n' "$dir"
  done
}

# context_budget_path_safe (#753 review fix, security-reviewer LOW +
# silent-failure-hunter follow-up) —
# PROJECT_PATHS entries are already-vetted safe directory prefixes
# (project_path_is_safe()), but the individual AGENTS.md/topic-doc files
# opened under them are not: [[ -f ]] follows symlinks, so a committed
# symlink pointing outside the repo could have its target read and reported
# on. Rejects $1 outright if it is itself a symlink (mirrors the leaf-level
# check in canonicalize_dir_allow_missing()) -- resolving only the parent
# directory and re-appending basename would let a symlinked leaf file (e.g.
# AGENTS.md -> /etc/passwd) resolve back under $ROOT and be wrongly treated
# as safe. Also resolves $1's parent dir to its real path and requires it
# stays under $ROOT, catching a symlinked ancestor directory. Returns 1
# (caller must skip the candidate, not read it) if either check fails.
context_budget_path_safe() {
  local target="$1" dirreal real
  [[ ! -L "$target" ]] || return 1
  dirreal="$(cd "$(dirname "$target")" 2>/dev/null && pwd -P)" || return 1
  real="$dirreal/$(basename "$target")"
  case "$real" in
    "$ROOT"|"$ROOT"/*) return 0 ;;
    *) return 1 ;;
  esac
}

# check_context_budget (#753) — three dimensions over context_budget_scan_dirs:
#   1. count: unchanged semantics (## Critical Rules / topic-doc ## Rules
#      bullet counts vs CRITICAL_RULES_MAX/TOPIC_DOC_RULES_MAX), now run for
#      every scanned project directory instead of just $ROOT + $FLOW_DIR.
#      Note: "### " sub-headings don't match /^## /, so bullets nested under
#      a ### sub-heading already roll up into the parent ## section's count
#      -- this is existing awk behavior, not new logic (Q3).
#   2. length: new. AGENTS.md's "## Critical Rules" only -- caps each
#      logical bullet (hard-wrap continuation lines joined) at
#      CRITICAL_RULE_MAX_LEN raw bytes.
#   3. anti-evasion: new, warn-only. Any "## " section in a scanned
#      AGENTS.md with >= EVASION_BULLET_MIN top-level bullets whose heading
#      isn't on EVASION_EXEMPT_HEADINGS.
#
# Every scanned AGENTS.md/topic-doc candidate is guarded twice before it's
# read (#753 review fixes): context_budget_path_safe() requires its resolved
# real path stays under $ROOT (a symlink escaping the repo is silently
# skipped, mirroring check_structural_tests' non-crashing discovery), and
# [[ -r ]] requires it's actually readable (an unreadable file emits an
# explicit context-budget fail -- mirrors check_structural_tests' readability
# guard, ~line 1146 -- rather than silently reporting a false "pass" for a
# file it never inspected).
check_context_budget() {
  local target rel count status any_finding=0 scan_dir docdir agents_md agents_md_ok

  while IFS= read -r scan_dir; do
    agents_md="$scan_dir/AGENTS.md"
    agents_md_ok=0
    if [[ -f "$agents_md" ]] && context_budget_path_safe "$agents_md"; then
      if [[ -r "$agents_md" ]]; then
        agents_md_ok=1
      else
        rel="$(relpath "$agents_md")"
        add_result context-budget "$rel" fail "$(basename "$agents_md") is not readable" \
          "restore read permission on ${rel}"
        any_finding=1
      fi
    fi

    # --- Dimension 1: count (unchanged semantics) --------------------------
    target="$agents_md"
    if [[ "$agents_md_ok" -eq 1 ]]; then
      count="$(awk '/^## Critical Rules/{f=1;next} /^## /{f=0} f && /^- /{c++} END{print c+0}' "$target")"
      if [[ "$count" -gt "$CRITICAL_RULES_MAX" ]]; then
        rel="$(relpath "$target")"
        status=fail
        if [[ "$MODE_CHANGED" -eq 1 ]] && ! is_changed "$rel"; then status=warn; fi
        add_result context-budget "$rel" "$status" \
          "Critical Rules section has ${count} bullets (max ${CRITICAL_RULES_MAX})" \
          "run /cenci:maintain rules on ${rel} to curate the Critical Rules section back under the ${CRITICAL_RULES_MAX}-bullet threshold"
        any_finding=1
      fi
    fi

    docdir="$scan_dir/docs"
    if [[ -d "$docdir" ]]; then
      for target in "$docdir"/*.md; do
        [[ -f "$target" ]] || continue
        context_budget_path_safe "$target" || continue
        if [[ ! -r "$target" ]]; then
          rel="$(relpath "$target")"
          add_result context-budget "$rel" fail "$(basename "$target") is not readable" \
            "restore read permission on ${rel}"
          any_finding=1
          continue
        fi
        grep -q '^## Rules' "$target" || continue
        count="$(awk '/^## Rules/{f=1;next} /^## /{f=0} f && /^- /{c++} END{print c+0}' "$target")"
        if [[ "$count" -gt "$TOPIC_DOC_RULES_MAX" ]]; then
          rel="$(relpath "$target")"
          status=fail
          if [[ "$MODE_CHANGED" -eq 1 ]] && ! is_changed "$rel"; then status=warn; fi
          add_result context-budget "$rel" "$status" \
            "Rules section has ${count} bullets (max ${TOPIC_DOC_RULES_MAX})" \
            "run /cenci:maintain rules on ${rel} to curate the Rules section back under the ${TOPIC_DOC_RULES_MAX}-bullet threshold"
          any_finding=1
        fi
      done
    fi

    # --- Dimension 2: length (AGENTS.md ## Critical Rules only) ------------
    target="$agents_md"
    if [[ "$agents_md_ok" -eq 1 ]]; then
      local ord len
      while IFS=$'\t' read -r ord len; do
        [[ -n "$ord" ]] || continue
        rel="$(relpath "$target")"
        status=fail
        if [[ "$MODE_CHANGED" -eq 1 ]] && ! is_changed "$rel"; then status=warn; fi
        add_result context-budget "$rel" "$status" \
          "Critical Rules bullet ${ord} is ${len} chars (max ${CRITICAL_RULE_MAX_LEN})" \
          "run /cenci:maintain rules on ${rel} to tighten the over-length Critical Rules bullet back under the ${CRITICAL_RULE_MAX_LEN}-character cap"
        any_finding=1
      done < <(awk -v max="$CRITICAL_RULE_MAX_LEN" '
        function flush() { if (open && len > max) print n"\t"len; open=0 }
        /^## Critical Rules/ { f=1; next }
        /^## / { if (f) flush(); f=0; next }
        !f { next }
        /^- / { flush(); n++; len=length($0); open=1; next }
        /^[[:space:]]*$/ || /^#/ || /^[[:space:]]*[-*+][[:space:]]/ || /^```/ { flush(); next }
        {
          if (open) {
            stripped=$0
            sub(/^[[:space:]]+/, "", stripped)
            len += 1 + length(stripped)
          }
        }
        END { flush() }
      ' "$target")
    fi

    # --- Dimension 3: anti-evasion (AGENTS.md only, warn-only) -------------
    target="$agents_md"
    if [[ "$agents_md_ok" -eq 1 ]]; then
      local sec_count heading safe_heading
      while IFS=$'\t' read -r sec_count heading; do
        [[ -n "$heading" ]] || continue
        [[ "$sec_count" -ge "$EVASION_BULLET_MIN" ]] || continue
        # Comparison always uses the raw, unsanitized heading -- sanitizing
        # before comparison could change matching behavior against
        # EVASION_EXEMPT_HEADINGS.
        grep -qxF -- "$heading" <<< "$EVASION_EXEMPT_HEADINGS" && continue
        rel="$(relpath "$target")"
        # The heading is untrusted content from a scanned repo file, and this
        # message reaches both a terminal and a downstream LLM (#753 review
        # fix, security-reviewer LOW) -- strip control characters (terminal
        # escape-sequence spoofing) and cap length before it's reported.
        safe_heading="${heading//[[:cntrl:]]/}"
        if [[ "${#safe_heading}" -gt 80 ]]; then
          safe_heading="${safe_heading:0:80}..."
        fi
        add_result context-budget "$rel" warn \
          "section '## ${safe_heading}' has ${sec_count} top-level bullets but is not a rule ledger or a recognized structural section" \
          "run /cenci:maintain rules on ${rel} to fold these bullets into ## Critical Rules or a docs/<topic>.md, or rename the section to a recognized structural heading"
        any_finding=1
      done < <(awk '
        function flush() { if (heading != "" && count > 0) print count"\t"heading }
        /^## / {
          flush()
          heading=$0
          sub(/^## /, "", heading)
          sub(/[ \t]+$/, "", heading)
          count=0
          next
        }
        /^- / { count++ }
        END { flush() }
      ' "$target")
    fi
  done < <(context_budget_scan_dirs)

  [[ "$any_finding" -eq 0 ]] && add_result context-budget "(repo)" pass "all rule sections are within checker-owned context budgets" ""
}

# ============================================================================
# command-flags / config-examples / roadmap-status (ticket #532)
# ============================================================================

# check_command_flags (id command-flags) — Q2 cross-reference. Only inspects
# backtick spans that begin with "cenci " (precise; avoids flagging git/gh/
# arbitrary flags). Each span's verb chain and any --flags must each resolve
# in executable Go CLI registration sites (watch/main.go and watch/*_cmd.go);
# documentation being validated is never an authority. Otherwise the span is
# flagged. watch/main.go is included alongside watch/*_cmd.go
# because some top-level verbs (e.g. `doctor`, `update`, `uninstall`) dispatch
# directly from main.go and never get their own *_cmd.go file.
#
# Tokenizing a doc's `cenci ...` span: many real spans embed placeholder
# syntax (`<ticket>`, `{number}`, `[--flag]`, `a|b`) that can never appear
# verbatim in a definition surface, and shortened/bracketed doc phrasing of a
# real flag (e.g. a doc's `[--volumes]` vs. the real source's
# `[--images] [--volumes]`) is not expected to match a defining source
# byte-for-byte either. So the verb chain is only ever built from the run of
# leading tokens that are neither flag-shaped (`--x`) nor placeholder-shaped
# (containing `<`, `{`, `[`, or `|`) nor a bare `--` (an argv separator, not a
# named flag) — the first such token stops the verb chain from growing any
# further, and only that leading run needs to resolve verbatim. Well-formed
# `--flag` tokens are still validated individually (found anywhere in the
# span, even past where the verb chain stopped); anything else (bare
# placeholder tokens, or plain words trailing after a flag/placeholder, e.g.
# an example's argument value) is not itself checked.
check_command_flags() {
  local command_file flag_file target f span rest verb_str flag any_fail=0
  command_file="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  flag_file="$(mktemp)" || { rm -f "$command_file"; echo "check.sh: mktemp failed" >&2; exit 2; }

  # Only syntactic registration sites in executable Go CLI sources establish
  # command/flag truth. Exact-token registries prevent comments, prose, and
  # unrelated string literals from validating a stale documented spelling.
  for f in "$ROOT/watch/main.go" "$ROOT"/watch/*_cmd.go; do
    [[ -f "$f" ]] || continue
    sed -nE 's/^[[:space:]]*case[[:space:]]+"([A-Za-z0-9_-]+)".*/\1/p' "$f" >> "$command_file"
    sed -nE 's/^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*:=[[:space:]]*flag\.NewFlagSet\("([A-Za-z0-9_-]+)( [A-Za-z0-9_-]+)?".*/\1\2/p' "$f" \
      | tr ' ' '\n' >> "$command_file"
    sed -nE 's/^[[:space:]]*"([A-Za-z0-9_-]+)"[[:space:]]*:[[:space:]]*true,.*/\1/p' "$f" >> "$command_file"
    sed -nE 's/^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*(:=|=)[[:space:]]*[A-Za-z_][A-Za-z0-9_]*\.(Bool|Duration|Int|String)\("([A-Za-z0-9_-]+)".*/\3/p' "$f" >> "$flag_file"
    sed -nE 's/^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*\.(BoolVar|DurationVar|IntVar|StringVar|Var)\([^,]+,[[:space:]]*"([A-Za-z0-9_-]+)".*/\2/p' "$f" >> "$flag_file"
  done
  sort -u -o "$command_file" "$command_file"
  sort -u -o "$flag_file" "$flag_file"

  for target in "$ROOT"/docs/*.md "$FLOW_DIR"/docs/*.md "$FLOW_DIR/README.md" "$ROOT/README.md" \
                "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/skills/*/codex.md \
                "$FLOW_DIR"/skills/*/phases/*.md "$FLOW_DIR"/skills/*/modes/*.md; do
    [[ -f "$target" ]] || continue
    while IFS= read -r span; do
      [[ -n "$span" ]] || continue
      rest="${span#cenci }"
      local -a toks=() verb_toks=() flag_toks=()
      IFS=' ' read -r -a toks <<< "$rest"
      local t stopped=0
      for t in "${toks[@]}"; do
        case "$t" in
          *[\<\{\[\|]*)
            # placeholder/bracket/pipe/angle-bracket token: never itself
            # checked, and stops the verb chain from growing further.
            stopped=1
            continue
            ;;
        esac
        case "$t" in
          --)
            # bare argv separator, not a named flag: stops the verb chain,
            # nothing to validate.
            stopped=1
            continue
            ;;
          --?*)
            flag_toks+=("$t")
            stopped=1
            continue
            ;;
        esac
        [[ "$stopped" -eq 0 ]] && verb_toks+=("$t")
      done
      verb_str="${verb_toks[*]:-}"
      local unresolved=0
      if [[ -n "$verb_str" ]]; then
        local first_verb="${verb_toks[0]:-}"
        if ! grep -qxF -- "$first_verb" "$command_file"; then
          unresolved=1
        elif [[ "$first_verb" != "run" && "$first_verb" != "pipeline" ]]; then
          local verb_token
          for verb_token in "${verb_toks[@]}"; do
            grep -qxF -- "$verb_token" "$command_file" || unresolved=1
          done
        fi
      fi
      for flag in "${flag_toks[@]:-}"; do
        [[ -n "$flag" ]] || continue
        grep -qxF -- "${flag#--}" "$flag_file" || unresolved=1
      done
      if [[ "$unresolved" -eq 1 ]]; then
        add_result command-flags "$(relpath "$target")" fail \
          "\`cenci ${rest}\` in $(relpath "$target") references a CLI command/flag not registered by watch/main.go or watch/*_cmd.go" \
          "update or remove the stale command/flag reference in $(relpath "$target"), or register it in the Go CLI source"
        any_fail=1
      fi
    done < <(grep -oE '`cenci [^`]*`' "$target" 2>/dev/null | tr -d '`')
  done

  rm -f "$command_file" "$flag_file"
  [[ "$any_fail" -eq 0 ]] && add_result command-flags "(repo)" pass "no stale cenci command/flag references found" ""
}

# check_config_examples (id config-examples) — Q3. Fenced ```json blocks that
# look config-shaped (contain at least one signal key from the live
# .cenci/config.json schema) must parse as JSON and use only field names
# present in that live schema.
# Scope note: only the per-repo `.cenci/config.json` schema is modeled here.
# The fleet config (`~/.config/cenci/config.json`, which owns `dispatch` and
# `automerge.enabled`) is a different schema with no metadata below, so a doc
# example of it must use a non-`json` fence (```jsonc) to stay out of this
# check -- otherwise its fleet-only fields are reported as being outside the
# canonical repo schema, which is true but not the author's error.
# Canonical .cenci/config.json path/type metadata. Paths use [] for array
# elements and * for user-defined map keys. Optional fields remain valid even
# when the repository's live config does not happen to contain them.
CONFIG_SCHEMA=(
  'configVersion|string' 'branchPattern|string' 'isMonorepo|boolean'
  'guidanceLocation|string' 'babysitInterval|string'
  'buildCommand|string' 'testCommand|string' 'lintCommand|string'
  'gateCommand|string' 'serveCommand|string'
  'stack|object' 'stack.backend|string' 'stack.frontend|string'
  'stack.testing|string,array' 'stack.testing[]|string'
  'projects|array' 'projects[]|object' 'projects[].slug|string'
  'projects[].path|string' 'projects[].name|string' 'projects[].description|string'
  'projects[].stack|object' 'projects[].stack.framework|string'
  'projects[].stack.testing|string,array' 'projects[].stack.testing[]|string'
  'projects[].buildCommand|string' 'projects[].testCommand|string'
  'projects[].lintCommand|string' 'projects[].gateCommand|string'
  'projects[].serveCommand|string' 'projects[].babysitInterval|string'
  'projects[].boardKey|string' 'projects[].testKey|string'
  'mcpServers|object' 'mcpServers.*|boolean'
  'lspServers|object' 'lspServers.*|boolean'
  'playwrightCli|boolean'
  'autoCompactDisabled|boolean' 'pinSubagents200K|boolean'
  'cenci|object' 'cenci.compactImplementation|boolean'
  'cenci.reviewConcurrency|string' 'cenci.implementerConcurrency|string'
  'cenci.diffContextMode|string' 'cenci.liteReviewEnabled|boolean'
  'cenci.planComment|boolean'
  'cicd|object' 'cicd.enabled|boolean' 'cicd.platform|string'
  'sandbox|object' 'sandbox.enabled|boolean' 'sandbox.baseVersion|string,null'
  'sandbox.dind|boolean'
  'lazyboards|object' 'lazyboards.enabled|boolean'
  'lazyboards.serveCommand|string' 'lazyboards.boardKey|string'
  'lazyboards.testCommand|string' 'lazyboards.testKey|string'
  'planning|object' 'planning.autonomy|string'
  'security|object' 'security.sensitivePaths|array' 'security.sensitivePaths[]|string'
  'maintenance|object' 'maintenance.enabled|boolean'
  'maintenance.checkDuringImplement|boolean' 'maintenance.remindAfterDays|number'
  'maintenance.generatedDocs|boolean'
  'automerge|object' 'automerge.protectedPaths|array' 'automerge.protectedPaths[]|string'
  'automerge.maxChangedFiles|number' 'automerge.maxDiffLines|number' 'automerge.mergeMethod|string'
  'projects[].automerge|object' 'projects[].automerge.protectedPaths|array'
  'projects[].automerge.protectedPaths[]|string' 'projects[].automerge.maxChangedFiles|number'
  'projects[].automerge.maxDiffLines|number' 'projects[].automerge.mergeMethod|string'
)

CONFIG_SIGNAL_KEYS=()
for _config_entry in "${CONFIG_SCHEMA[@]}"; do
  _config_path="${_config_entry%%|*}"
  if [[ "$_config_path" != *.* && "$_config_path" != *"[]"* && "$_config_path" != *"*"* ]]; then
    CONFIG_SIGNAL_KEYS+=("$_config_path")
  fi
done
unset _config_entry _config_path

config_schema_types() {
  local path="$1" entry pattern types
  for entry in "${CONFIG_SCHEMA[@]}"; do
    pattern="${entry%%|*}"
    types="${entry#*|}"
    # `*` (user-defined map keys, e.g. mcpServers.*) is the only pattern
    # metacharacter honored here. Every other path must compare literally:
    # an unquoted bash pattern match would misparse the [] array markers as
    # bracket expressions (a pattern with two [] pairs, like
    # projects[].stack.testing[], can never match its own literal path).
    if [[ "$pattern" == *'*'* ]]; then
      [[ "$path" == $pattern ]] || continue
    else
      [[ "$path" == "$pattern" ]] || continue
    fi
    printf '%s' "$types"
    return 0
  done
  return 1
}

_is_config_shaped_block() {
  local block="$1" k top_keys
  if top_keys="$(jq -r 'if type == "object" then keys[] else empty end' <<< "$block" 2>/dev/null)"; then
    for k in "${CONFIG_SIGNAL_KEYS[@]}"; do
      grep -qxF -- "$k" <<< "$top_keys" && return 0
    done
    return 1
  fi
  for k in "${CONFIG_SIGNAL_KEYS[@]}"; do
    grep -qE "\"${k}\"[[:space:]]*:" <<< "$block" && return 0
  done
  return 1
}

# extract_json_blocks <file> — prints each fenced ```json ... ``` block's
# body, terminated by a sentinel line, so a caller can iterate multiple
# blocks per file safely. NUL-delimiting (the usual idiom elsewhere in this
# script) is deliberately avoided here: awk's printf "\0" is not portable
# (mawk silently drops it instead of emitting a null byte), so a text
# sentinel line is used instead -- safe because JSON content can never
# legitimately contain this exact line on its own.
_JSON_BLOCK_SENTINEL="@@CENCI-MAINTAIN-JSON-BLOCK-END@@"

extract_json_blocks() {
  local file="$1"
  awk -v sentinel="$_JSON_BLOCK_SENTINEL" '
    /^```json[ \t]*$/ { inblock=1; buf=""; next }
    inblock && /^```[ \t]*$/ { inblock=0; print buf sentinel; next }
    inblock { buf = buf $0 "\n" }
    # An opened ```json fence that is never closed before EOF must still be
    # routed through validation instead of its buffered content silently
    # vanishing (no fail/warn ever emitted for it).
    END { if (inblock) print buf sentinel }
  ' "$file"
}

check_config_examples() {
  local any_fail=0 target rel line block path node_type allowed

  for target in "$ROOT"/docs/*.md "$FLOW_DIR"/docs/*.md "$FLOW_DIR/README.md" "$ROOT/README.md"; do
    [[ -f "$target" ]] || continue
    rel="$(relpath "$target")"
    block=""
    while IFS= read -r line; do
      if [[ "$line" == "$_JSON_BLOCK_SENTINEL" ]]; then
        if _is_config_shaped_block "$block"; then
          if ! jq empty <<< "$block" >/dev/null 2>&1; then
            add_result config-examples "$rel" fail \
              "config example in $rel does not parse as JSON" \
              "fix the malformed JSON in the config example block in $rel"
            any_fail=1
          else
            while IFS=$'\t' read -r path node_type; do
              [[ -n "$path" ]] || continue
              allowed="$(config_schema_types "$path" 2>/dev/null)" || allowed=""
              if [[ -z "$allowed" ]]; then
                add_result config-examples "$rel" fail \
                  "config example in $rel uses field path \`$path\` outside the canonical .cenci/config.json schema" \
                  "move, rename, or remove \`$path\` so the example matches the canonical config nesting"
                any_fail=1
              elif ! grep -qwF -- "$node_type" <<< "${allowed//,/ }"; then
                add_result config-examples "$rel" fail \
                  "config example in $rel gives \`$path\` type $node_type; canonical type is $allowed" \
                  "change \`$path\` in the example to the canonical $allowed type"
                any_fail=1
              fi
            done < <(jq -r '
              paths as $p
              | [($p | map(if type == "number" then "[]" else tostring end) | join(".") | gsub("\\.\\[\\]"; "[]")),
                 (getpath($p) | type)]
              | @tsv
            ' <<< "$block")
          fi
        fi
        block=""
        continue
      fi
      block+="$line"$'\n'
    done < <(extract_json_blocks "$target")
  done

  [[ "$any_fail" -eq 0 ]] && add_result config-examples "(repo)" pass "all config examples match canonical path/type metadata" ""
}

# check_roadmap_status (id roadmap-status) — name-reference staleness only.
# Restricted to unambiguous backticked token classes (file-path-shaped,
# skill-invocation-shaped) so label words like `Planned`/`Working` are never
# flagged. `cenci <verb>` command spans are intentionally not revalidated here
# (command-flags already scans docs/*.md, including roadmap.md, for those).
_roadmap_token_is_path_shaped() {
  local t="$1"
  [[ "$t" == */* ]] || return 1
  # Reject any '..' path-traversal segment before ever reaching the -f test
  # against "$ROOT/$inner" below -- matches the repo's established
  # traversal-guard convention (see guard-main-worktree.sh). A token like
  # this is simply treated as not path-shaped (no result emitted either way).
  case "$t" in
    *..*) return 1 ;;
  esac
  case "$t" in
    *.md|*.sh|*.go|*.json) return 0 ;;
  esac
  file_under_project "$t"
}

check_roadmap_status() {
  local file="$ROOT/docs/roadmap.md"
  if [[ ! -f "$file" ]]; then
    add_result roadmap-status "(repo)" skip "docs/roadmap.md not found" ""
    return
  fi

  local any_fail=0 span inner name
  while IFS= read -r span; do
    [[ -n "$span" ]] || continue
    inner="$(tr -d '`' <<< "$span")"
    if [[ "$inner" =~ ^/?cenci:([A-Za-z0-9_-]+)$ ]]; then
      name="${BASH_REMATCH[1]}"
      if [[ ! -d "$FLOW_DIR/skills/$name" ]]; then
        add_result roadmap-status "$inner" fail \
          "stale reference \`$inner\` in docs/roadmap.md names a skill that no longer exists" \
          "update the stale reference \`$inner\` in docs/roadmap.md -- the named skill/file no longer exists (renamed or deleted); fix or restore the target"
        any_fail=1
      fi
      continue
    fi
    if _roadmap_token_is_path_shaped "$inner"; then
      if [[ ! -f "$ROOT/$inner" ]]; then
        add_result roadmap-status "$inner" fail \
          "stale reference \`$inner\` in docs/roadmap.md names a file that no longer exists" \
          "update the stale reference \`$inner\` in docs/roadmap.md -- the named skill/file no longer exists (renamed or deleted); fix or restore the target"
        any_fail=1
      fi
    fi
  done < <(grep -oE '`[^`]+`' "$file" 2>/dev/null)

  [[ "$any_fail" -eq 0 ]] && add_result roadmap-status "(repo)" pass "no stale name references found in docs/roadmap.md" ""
}

# ============================================================================
# Main
# ============================================================================
main() {
  parse_args "$@"

  ROOT="$(pwd -P)" || { echo "check.sh: failed to resolve current working directory." >&2; exit 2; }
  command -v jq >/dev/null 2>&1 || { echo "check.sh: jq is required but was not found on PATH." >&2; exit 2; }

  RESULTS_FILE="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  trap 'rm -f "$RESULTS_FILE"' EXIT

  resolve_project_dirs
  resolve_flow_dir

  local readme="$FLOW_DIR/README.md"
  if is_relevant stale-generated && ! generated_docs_enabled; then
    add_result stale-generated "$(relpath "$readme")" skip \
      "maintenance.generatedDocs=false disables generated-section maintenance" ""
  elif [[ -f "$readme" ]]; then
    local do_compare=0
    is_relevant stale-generated && do_compare=1
    process_markers "$readme" "$MODE_WRITE" "$do_compare"
  elif is_relevant stale-generated; then
    add_result stale-generated "$(relpath "$readme")" fail \
      "flow README containing the required generated marker pairs is missing" \
      "restore $(relpath "$readme") with all required cenci-maintain marker pairs"
  fi

  is_relevant front-matter && check_front_matter
  is_relevant duplicate-names && check_duplicate_names
  is_relevant broken-refs && check_broken_refs
  is_relevant orphan-files && check_orphan_files
  is_relevant instruction-docs && check_instruction_docs
  is_relevant topic-docs && check_topic_docs
  is_relevant invalid-json && check_invalid_json
  is_relevant config-version && check_config_version
  is_relevant agent-roles && check_agent_roles
  is_relevant shell-syntax && check_shell_syntax
  is_relevant capability-table && check_capability_table
  is_relevant adapter-drift && check_adapter_drift
  is_relevant structural-tests && check_structural_tests
  is_relevant worktree-ignored && check_worktree_ignored
  is_relevant plans-ignored && check_plans_ignored
  is_relevant pipeline-state-ignored && check_pipeline_state_ignored
  if [[ "$MODE_ADVISORY" -eq 1 ]]; then
    add_result gate-command "(repo)" skip "advisory mode skips executable gate commands" ""
  elif [[ "$PROJECT_PATHS_OK" -ne 1 ]]; then
    add_result gate-command "(repo)" skip "unsafe configured project paths prevent gate execution" ""
  else
    check_gate_command
  fi
  is_relevant claude-rules-imports && check_claude_rules_imports
  is_relevant legacy-lessons && check_legacy_lessons
  if [[ "$MODE_ADVISORY" -eq 1 ]]; then
    add_result github-labels "(repo)" skip "advisory mode skips network-backed GitHub label checks" ""
  else
    check_github_labels
  fi
  check_context_budget
  is_relevant command-flags   && check_command_flags
  is_relevant config-examples && check_config_examples
  is_relevant roadmap-status  && check_roadmap_status

  local mode
  if [[ "$MODE_ADVISORY" -eq 1 ]]; then
    mode="advisory"
  elif [[ "$MODE_STRICT" -eq 1 ]]; then
    mode="strict"
  elif [[ "$MODE_CHANGED" -eq 1 ]]; then
    mode="changed"
  else
    mode="full"
  fi

  if [[ "$MODE_ADVISORY" -eq 1 ]]; then
    build_report "$mode" || { echo "check.sh: failed to render advisory report" >&2; exit 2; }
  else
    local destination="${REPORT_FILE:-$ROOT/.cenci/maintain-report.json}"
    write_report_atomically "$destination" "$mode" || exit 2
    echo "summary: pass=${COUNT_PASS} warn=${COUNT_WARN} fail=${COUNT_FAIL} skip=${COUNT_SKIP} mode=${mode}"
  fi

  if [[ "$COUNT_FAIL" -gt 0 ]]; then
    exit 1
  fi
  if [[ "$MODE_STRICT" -eq 1 && "$COUNT_WARN" -gt 0 ]]; then
    exit 1
  fi
  exit 0
}

main "$@"
