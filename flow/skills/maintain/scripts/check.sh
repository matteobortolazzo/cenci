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
# Exit codes: 0 unless a "fail" result exists. --strict additionally fails on
# any "warn". "skip" never blocks.
#
# Checker scope: the flow project (slug "flow") plus repo-root instruction/
# config files — not watch/ or sandbox/ (see the ticket #530 plan). Check
# BODIES remain flow-scoped under this charter; ticket #532 only extends
# --changed relevance (is_relevant()) so a change under any configured
# sibling project (e.g. watch/, sandbox/) maps to the three cross-project
# checks (command-flags, config-examples, roadmap-status) instead of being a
# silent no-op. No existing check body gained project-generalization logic.
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

# This script's own directory -- deliberately independent of $FLOW_DIR, which
# is the *target* repo's flow project directory and, under
# flow/tests/maintain.test.sh, is a synthetic fixture repo without a full
# flow/ tree (e.g. no hooks/scripts/). check_gate_command needs the real,
# bundled flow/hooks/scripts/run-gate.sh next to this script, not whatever
# happens to exist under the target repo's resolved flow directory.
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "check.sh: failed to resolve script directory." >&2; exit 2; }
RUN_GATE_SH="${SELF_DIR}/../../../hooks/scripts/run-gate.sh"

MARKER_IDS=(skills agents workflow-deps docs-nav)

MODE_STRICT=0
MODE_CHANGED=0
MODE_WRITE=0
CHANGED_FILES=()

COUNT_PASS=0
COUNT_WARN=0
COUNT_FAIL=0
COUNT_SKIP=0
RESULTS_FILE=""
MARKERS_OK=1
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
      *)
        echo "check.sh: unknown argument: $1" >&2
        exit 2
        ;;
    esac
  done
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
  printf '%s' "$1" | paste -sd ', ' - 2>/dev/null
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
    [[ -n "$path" && "$path" != "null" ]] && FLOW_DIR="$ROOT/$path"
  else
    FLOW_DIR="$ROOT"
  fi
}

# Populate PROJECT_PATHS (repo-root-relative dir prefixes for every configured
# project) from .cenci/config.json's projects[].path. Single-repo configs yield
# ("") meaning the repo root. Used only by is_relevant()'s --changed mapping so a
# change under any project subtree maps to the cross-project checks instead of a
# silent no-op; it never widens what a check body scans (that stays flow-scoped
# per the #530 charter).
resolve_project_dirs() {
  local cfg="$ROOT/.cenci/config.json" p jq_out jq_rc
  PROJECT_PATHS=()
  if [[ -f "$cfg" && "$(config_is_monorepo "$cfg")" == "true" ]]; then
    jq_out="$(jq -r '.projects[]?.path // empty' "$cfg" 2>/dev/null)"
    jq_rc=$?
    if [[ "$jq_rc" -eq 0 ]]; then
      while IFS= read -r p; do
        [[ -n "$p" && "$p" != "null" ]] && PROJECT_PATHS+=("$p")
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
  if [[ "$status" != "pass" ]]; then
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
      shell-syntax)
        case "$f" in *.sh) return 0 ;; esac ;;
      stale-generated|capability-table|adapter-drift)
        case "$f" in flow/skills/*|flow/agents/*|flow/opencode/install-skills.sh|flow/README.md|flow/docs/*.md) return 0 ;; esac ;;
      structural-tests)
        case "$f" in flow/tests/*.test.sh) return 0 ;; esac ;;
      worktree-ignored)
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
  grep -lE "\`${agent}\`" "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/skills/*/phases/*.md 2>/dev/null \
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
  local sm="$1" self="$2" all_names tok
  all_names="$(for d in "$FLOW_DIR"/skills/*/; do basename "$d"; done | sort -u)"
  grep -oE '`[a-zA-Z0-9_-]+`' "$sm" 2>/dev/null | tr -d '`' | sort -u | while read -r tok; do
    [[ -z "$tok" || "$tok" == "$self" ]] && continue
    grep -qxF -- "$tok" <<< "$all_names" && printf '%s\n' "$tok"
  done
}

agent_refs() {
  local sm="$1" all_agents tok
  all_agents="$(for f in "$FLOW_DIR"/agents/*.md; do [[ -f "$f" ]] && fm_field "$f" name; done | sort -u)"
  grep -oE '`[a-zA-Z0-9_-]+`' "$sm" 2>/dev/null | tr -d '`' | sort -u | while read -r tok; do
    [[ -z "$tok" ]] && continue
    grep -qxF -- "$tok" <<< "$all_agents" && printf '%s\n' "$tok"
  done
}

generate_workflow_deps_section() {
  printf '%s\n' "<!-- Auto-generated by flow/skills/maintain/scripts/check.sh --write. Do not hand-edit. -->"
  printf '%s\n' ""
  printf '%s\n' "| Skill | Phases | Reference skills | Scripts | Agents |"
  printf '%s\n' "|---|---|---|---|---|"
  local d skillname sm phases scripts refskills agents_col
  for d in "$FLOW_DIR"/skills/*/; do
    skillname="$(basename "$d")"
    sm="${d}SKILL.md"
    [[ -f "$sm" ]] || continue
    phases="$(list_join "$(find "${d}phases" -maxdepth 1 -name '*.md' 2>/dev/null | sort | xargs -n1 basename 2>/dev/null)")"
    scripts="$(list_join "$(find "${d}scripts" -maxdepth 1 -name '*.sh' ! -name '*.test.sh' 2>/dev/null | sort | xargs -n1 basename 2>/dev/null)")"
    refskills="$(list_join "$(other_skill_refs "$sm" "$skillname")")"
    agents_col="$(list_join "$(agent_refs "$sm")")"
    printf '| `%s` | %s | %s | %s | %s |\n' "$skillname" "${phases:-—}" "${refskills:-—}" "${scripts:-—}" "${agents_col:-—}"
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

  local id expected actual tmp
  tmp="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  cp "$file" "$tmp"
  for id in "${MARKER_IDS[@]}"; do
    [[ "${MARKER_PRESENT[$id]}" -eq 1 ]] || continue
    expected="$(generate_section "$id")"
    actual="$(extract_marker_body "$file" "$id")"
    if [[ "$expected" != "$actual" ]]; then
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
  local all_scripts f refs r any_fail=0
  all_scripts="$(find "$FLOW_DIR" -path '*/scripts/*.sh' -printf '%f\n' 2>/dev/null | sort -u)"
  for f in "$FLOW_DIR"/skills/*/SKILL.md; do
    [[ -f "$f" ]] || continue
    refs="$(grep -oE '`scripts/[A-Za-z0-9_.-]+\.sh`' "$f" 2>/dev/null | tr -d '`' | sed 's#^scripts/##' | sort -u)"
    [[ -z "$refs" ]] && continue
    while IFS= read -r r; do
      [[ -n "$r" ]] || continue
      if ! grep -qxF -- "$r" <<< "$all_scripts"; then
        add_result broken-refs "$(relpath "$f")" fail \
          "referenced script 'scripts/$r' does not exist anywhere under flow/" \
          "create scripts/$r or fix the reference in $(relpath "$f")"
        any_fail=1
      fi
    done <<< "$refs"
  done
  [[ "$any_fail" -eq 0 ]] && add_result broken-refs "(repo)" pass "no broken scripts/ references found" ""
}

check_orphan_files() {
  local any_orphan=0 f name skilldir
  while IFS= read -r -d '' f; do
    name="$(basename "$f")"
    if ! grep -rlqE "\`scripts/${name}\`" "$FLOW_DIR"/skills/*/SKILL.md 2>/dev/null; then
      add_result orphan-files "$(relpath "$f")" fail "script $(relpath "$f") is not referenced by any skill" \
        "reference it from the owning skill's SKILL.md (e.g. \`scripts/${name}\`) or delete it if unused"
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
  [[ "$any_fail" -eq 0 ]] && add_result adapter-drift "(repo)" pass "every PORTABLE_SKILLS entry has a matching flow/skills/ directory" ""
}

check_structural_tests() {
  local any_fail=0 f out rc
  for f in "$FLOW_DIR"/tests/*.test.sh; do
    [[ -f "$f" ]] || continue
    out="$(bash "$f" 2>&1)"; rc=$?
    if [[ "$rc" -ne 0 ]]; then
      add_result structural-tests "$(relpath "$f")" fail "$(basename "$f") failed (exit ${rc})" \
        "fix the failing assertions in $(relpath "$f")"
      any_fail=1
    fi
  done
  [[ "$any_fail" -eq 0 ]] && add_result structural-tests "(repo)" pass "all flow/tests/*.test.sh pass" ""
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
  for l in Refined Design Designed Planned Working "In Review" Implemented Followup \
    dispatch-failed plan-invalid reconcile-stuck; do
    if ! jq -e --arg l "$l" '[.[] | select(.name==$l)] | length > 0' <<< "$labels_json" >/dev/null 2>&1; then
      add_result github-labels "$owner_repo" fail "missing lifecycle label '$l'" \
        "run: gh label create \"$l\" --repo $owner_repo --color <hex> --description \"<description>\" (or re-run /cenci:configure to self-heal labels)"
      any_fail=1
    fi
  done
  [[ "$any_fail" -eq 0 ]] && add_result github-labels "$owner_repo" pass "all canonical lifecycle labels are present" ""
}

check_context_budget() {
  local target rel count status

  for target in "$ROOT/AGENTS.md" "$FLOW_DIR/AGENTS.md"; do
    [[ -f "$target" ]] || continue
    count="$(awk '/^## Critical Rules/{f=1;next} /^## /{f=0} f && /^- /{c++} END{print c+0}' "$target")"
    if [[ "$count" -gt "$CRITICAL_RULES_MAX" ]]; then
      rel="$(relpath "$target")"
      status=fail
      if [[ "$MODE_CHANGED" -eq 1 ]] && ! is_changed "$rel"; then status=warn; fi
      add_result context-budget "$rel" "$status" \
        "Critical Rules section has ${count} bullets (max ${CRITICAL_RULES_MAX})" \
        "run /cenci:maintain rules on ${rel} to curate the Critical Rules section back under the ${CRITICAL_RULES_MAX}-bullet threshold"
    fi
  done

  local docdir
  for docdir in "$ROOT/docs" "$FLOW_DIR/docs"; do
    [[ -d "$docdir" ]] || continue
    for target in "$docdir"/*.md; do
      [[ -f "$target" ]] || continue
      grep -q '^## Rules' "$target" || continue
      count="$(awk '/^## Rules/{f=1;next} /^## /{f=0} f && /^- /{c++} END{print c+0}' "$target")"
      if [[ "$count" -gt "$TOPIC_DOC_RULES_MAX" ]]; then
        rel="$(relpath "$target")"
        status=fail
        if [[ "$MODE_CHANGED" -eq 1 ]] && ! is_changed "$rel"; then status=warn; fi
        add_result context-budget "$rel" "$status" \
          "Rules section has ${count} bullets (max ${TOPIC_DOC_RULES_MAX})" \
          "run /cenci:maintain rules on ${rel} to curate the Rules section back under the ${TOPIC_DOC_RULES_MAX}-bullet threshold"
      fi
    done
  done
}

# ============================================================================
# command-flags / config-examples / roadmap-status (ticket #532)
# ============================================================================

# check_command_flags (id command-flags) — Q2 cross-reference. Only inspects
# backtick spans that begin with "cenci " (precise; avoids flagging git/gh/
# arbitrary flags). Each span's verb chain and any --flags must each resolve
# in at least one definition surface (watch/main.go, watch/*_cmd.go,
# docs/cli-conventions.md, skill SKILL.md/phases/codex.md files); otherwise
# the span is flagged. watch/main.go is included alongside watch/*_cmd.go
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
  local corpus_file target f span rest verb_str flag any_fail=0
  corpus_file="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }

  for f in "$ROOT/watch/main.go" "$ROOT"/watch/*_cmd.go "$ROOT/docs/cli-conventions.md" \
           "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/skills/*/phases/*.md "$FLOW_DIR"/skills/*/codex.md; do
    [[ -f "$f" ]] && cat "$f" >> "$corpus_file"
  done

  for target in "$ROOT"/docs/*.md "$FLOW_DIR"/docs/*.md "$FLOW_DIR/README.md" "$ROOT/README.md" \
                "$FLOW_DIR"/skills/*/SKILL.md "$FLOW_DIR"/skills/*/phases/*.md; do
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
      if [[ -n "$verb_str" ]] && ! grep -qF -- "$verb_str" "$corpus_file"; then
        unresolved=1
      fi
      for flag in "${flag_toks[@]:-}"; do
        [[ -n "$flag" ]] || continue
        grep -qF -- "$flag" "$corpus_file" || unresolved=1
      done
      if [[ "$unresolved" -eq 1 ]]; then
        add_result command-flags "$(relpath "$target")" fail \
          "\`cenci ${rest}\` in $(relpath "$target") references a CLI command/flag defined in no known surface (watch/main.go, watch/*_cmd.go, docs/cli-conventions.md, skills)" \
          "update or remove the stale command/flag reference in $(relpath "$target"), or add the command to its definition surface"
        any_fail=1
      fi
    done < <(grep -oE '`cenci [^`]*`' "$target" 2>/dev/null | tr -d '`')
  done

  rm -f "$corpus_file"
  [[ "$any_fail" -eq 0 ]] && add_result command-flags "(repo)" pass "no stale cenci command/flag references found" ""
}

# check_config_examples (id config-examples) — Q3. Fenced ```json blocks that
# look config-shaped (contain at least one signal key from the live
# .cenci/config.json schema) must parse as JSON and use only field names
# present in that live schema.
CONFIG_SIGNAL_KEYS=(projects isMonorepo gateCommand guidanceLocation branchPattern buildCommand testCommand lintCommand serveCommand mcpServers lspServers)

_is_config_shaped_block() {
  local block="$1" k
  for k in "${CONFIG_SIGNAL_KEYS[@]}"; do
    grep -qF "\"$k\"" <<< "$block" && return 0
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
  local cfg="$ROOT/.cenci/config.json" schema_keys any_fail=0 target rel line block k keys schema_rc
  schema_keys="$(jq -r '[.. | objects | keys[]] | unique[]' "$cfg" 2>/dev/null)"
  schema_rc=$?
  if [[ "$schema_rc" -ne 0 ]]; then
    # Missing or malformed .cenci/config.json: the live schema itself is
    # unavailable, so no config-shaped example's fields can be meaningfully
    # compared against it. Surface that explicitly and skip every per-key
    # comparison below -- otherwise every config-shaped block found
    # afterward would get ALL its keys reported as "not present in schema",
    # a misleading fail cascade whose true cause (schema unavailable) is
    # never surfaced. check_invalid_json already reports a malformed
    # config.json in its own right; this is a distinct, config-examples-
    # scoped signal that its own comparisons could not run.
    add_result config-examples "(repo)" skip \
      "live .cenci/config.json schema is unavailable (missing or does not parse), so config examples cannot be validated against it" \
      ""
    return
  fi

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
            keys="$(jq -r '[.. | objects | keys[]] | unique[]' <<< "$block" 2>/dev/null)"
            while IFS= read -r k; do
              [[ -n "$k" ]] || continue
              if ! grep -qxF -- "$k" <<< "$schema_keys"; then
                add_result config-examples "$rel" fail \
                  "config example in $rel uses field \`$k\` not present in the live .cenci/config.json schema" \
                  "update the config example to match the current .cenci/config.json schema (field renamed/removed), or fix the JSON"
                any_fail=1
              fi
            done <<< "$keys"
          fi
        fi
        block=""
        continue
      fi
      block+="$line"$'\n'
    done < <(extract_json_blocks "$target")
  done

  [[ "$any_fail" -eq 0 ]] && add_result config-examples "(repo)" pass "all config examples match the live .cenci/config.json schema" ""
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

  resolve_flow_dir
  resolve_project_dirs

  RESULTS_FILE="$(mktemp)" || { echo "check.sh: mktemp failed" >&2; exit 2; }
  trap 'rm -f "$RESULTS_FILE"' EXIT

  local readme="$FLOW_DIR/README.md"
  if [[ -f "$readme" ]]; then
    local do_compare=0
    is_relevant stale-generated && do_compare=1
    process_markers "$readme" "$MODE_WRITE" "$do_compare"
  fi

  is_relevant front-matter && check_front_matter
  is_relevant duplicate-names && check_duplicate_names
  is_relevant broken-refs && check_broken_refs
  is_relevant orphan-files && check_orphan_files
  is_relevant instruction-docs && check_instruction_docs
  is_relevant topic-docs && check_topic_docs
  is_relevant invalid-json && check_invalid_json
  is_relevant shell-syntax && check_shell_syntax
  is_relevant capability-table && check_capability_table
  is_relevant adapter-drift && check_adapter_drift
  is_relevant structural-tests && check_structural_tests
  is_relevant worktree-ignored && check_worktree_ignored
  check_gate_command
  is_relevant claude-rules-imports && check_claude_rules_imports
  is_relevant legacy-lessons && check_legacy_lessons
  check_github_labels
  check_context_budget
  is_relevant command-flags   && check_command_flags
  is_relevant config-examples && check_config_examples
  is_relevant roadmap-status  && check_roadmap_status

  local mode
  if [[ "$MODE_STRICT" -eq 1 ]]; then
    mode="strict"
  elif [[ "$MODE_CHANGED" -eq 1 ]]; then
    mode="changed"
  else
    mode="full"
  fi

  mkdir -p "$ROOT/.cenci"
  build_report "$mode" > "$ROOT/.cenci/maintain-report.json"

  echo "summary: pass=${COUNT_PASS} warn=${COUNT_WARN} fail=${COUNT_FAIL} skip=${COUNT_SKIP} mode=${mode}"

  if [[ "$COUNT_FAIL" -gt 0 ]]; then
    exit 1
  fi
  if [[ "$MODE_STRICT" -eq 1 && "$COUNT_WARN" -gt 0 ]]; then
    exit 1
  fi
  exit 0
}

main "$@"
