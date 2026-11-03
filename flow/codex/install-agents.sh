#!/bin/sh
# Template source lives at templates/codex/agent-roles, not .../agents: an
# unscoped directory literally named "agents" inside this Codex plugin's own
# root is suspected of being auto-discovered by Codex's plugin loader as a
# second source of the same role names, producing "duplicate agent role name
# ... declared in the same config layer" warnings (#1040). Renaming removes
# the collision even though the exact loader mechanism isn't confirmed.
#
# Usage (PLUGIN_ROOT is required; <root> defaults to "."):
#   install-agents.sh [<root>] [--role-override <role>=<model>[:<effort>]]...
#   install-agents.sh [<root>] --migrate-preview
#   install-agents.sh [<root>] --migrate-apply <role>...
#
# Install copies each template into <root>/.codex/agents unless a file of that name
# already exists (an existing file is never rewritten). Normal roles carry no
# model/model_reasoning_effort, so they inherit the parent session's (#1191).
#
# --role-override writes `model` (and `model_reasoning_effort` when given) into a
# NEWLY created role file, right after `description`, replacing a template pin of the
# same key. An override for a role whose file already exists is skipped with one
# stderr notice (exit 0). The role must be a known role name, the model must match
# [A-Za-z0-9._-]+ and the effort [a-z]+; anything else, a duplicate role, or an
# option mix is refused (exit 2) before any directory or file is written, which keeps
# injected TOML out of the generated files.
#
# --migrate-preview is read-only: for each role file whose model AND effort exactly
# match a default that role's template once shipped, it prints a `diff -u` that
# deletes those two lines. Nothing is printed when no file qualifies.
# --migrate-apply deletes exactly those two lines from the named roles, through a
# temp file in the same directory followed by mv. Eligibility is re-checked at apply
# time for every named role first; if any is not eligible, nothing is written (exit 2).
#
# Exit codes: 0 done, 1 operational failure (mktemp, awk, mv), 2 refused or usage.
set -eu
LC_ALL=C
export LC_ALL

KNOWN_ROLES="planner implementer critical-reviewer acceptance-reviewer context-gatherer structure-analyzer domain-specialist"
MODE=install
ROOT=""
OVERRIDES=""   # one "role model effort" line per override; effort is "-" when not given
APPLY_ROLES="" # newline-separated role names for --migrate-apply
TMP_CURRENT=""

cleanup() {
  if [ -n "$TMP_CURRENT" ]; then
    rm -f "$TMP_CURRENT"
  fi
}
trap cleanup EXIT

die() { # die <message> -- usage or refusal, exit 2
  printf 'install-agents.sh: %s\n' "$1" >&2
  exit 2
}

failed() { # failed <message> -- operational failure, exit 1
  printf 'install-agents.sh: %s\n' "$1" >&2
  exit 1
}

is_known_role() {
  case "$1" in
    planner | implementer | critical-reviewer | acceptance-reviewer | context-gatherer | structure-analyzer | domain-specialist) return 0 ;;
  esac
  return 1
}

# ── Shipped defaults: every model/effort pair a role's template once pinned ──
# Eligibility for migration is an exact match of BOTH values against this table.
shipped_pairs() {
  case "$1" in
    planner | implementer | critical-reviewer)
      printf '%s\n' 'gpt-5.6 high' 'gpt-6.1-sol high'
      ;;
    acceptance-reviewer)
      printf '%s\n' 'gpt-6.1-sol high'
      ;;
    context-gatherer | structure-analyzer)
      printf '%s\n' 'gpt-5.6-terra medium'
      ;;
    *) : ;;
  esac
}

# ── Argument parsing and validation (nothing is written in this section) ─────
add_override() {
  spec=$1
  case "$spec" in
    *=*) ;;
    *) die "--role-override expects <role>=<model>[:<effort>]" ;;
  esac
  role=${spec%%=*}
  rest=${spec#*=}
  is_known_role "$role" || die "--role-override: unknown role (known roles: $KNOWN_ROLES)"
  case "$rest" in
    *:*)
      model=${rest%%:*}
      effort=${rest#*:}
      case "$effort" in
        '' | *[!a-z]*) die "--role-override: effort must match [a-z]+" ;;
      esac
      ;;
    *)
      model=$rest
      effort="-"
      ;;
  esac
  case "$model" in
    '' | *[!A-Za-z0-9._-]*) die "--role-override: model must match [A-Za-z0-9._-]+" ;;
  esac
  case "
$OVERRIDES
" in
    *"
$role "*) die "--role-override: role given more than once: $role" ;;
  esac
  OVERRIDES="${OVERRIDES}${OVERRIDES:+
}$role $model $effort"
}

set_mode() {
  [ "$MODE" = install ] || die "--migrate-preview and --migrate-apply cannot be combined"
  MODE=$1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --role-override)
      [ "$#" -ge 2 ] || die "--role-override requires <role>=<model>[:<effort>]"
      add_override "$2"
      shift 2
      ;;
    --migrate-preview)
      set_mode preview
      shift
      ;;
    --migrate-apply)
      set_mode apply
      shift
      while [ "$#" -gt 0 ]; do
        case "$1" in
          -*) die "--migrate-apply takes role names only; put options before it" ;;
        esac
        is_known_role "$1" || die "--migrate-apply: unknown role (known roles: $KNOWN_ROLES)"
        case "
$APPLY_ROLES
" in
          *"
$1
"*) die "--migrate-apply: role given more than once: $1" ;;
        esac
        APPLY_ROLES="${APPLY_ROLES}${APPLY_ROLES:+
}$1"
        shift
      done
      [ -n "$APPLY_ROLES" ] || die "--migrate-apply requires at least one role"
      ;;
    -*)
      die "unknown option (see the usage header of install-agents.sh)"
      ;;
    *)
      [ -z "$ROOT" ] || die "unexpected extra argument"
      ROOT=$1
      shift
      ;;
  esac
done

if [ "$MODE" != install ] && [ -n "$OVERRIDES" ]; then
  die "--role-override applies to an install and cannot be combined with a migration mode"
fi
ROOT=${ROOT:-.}
DEST="$ROOT/.codex/agents"

# ── Migration: eligibility, rewrite, preview, apply ─────────────────────────
# top_pins <file> -- the model / model_reasoning_effort lines of the file's top-level
# region (before the first [table] header or multi-line string), in file order.
top_pins() {
  awk -v "sq='''" '
    /^\[/ || index($0, "\"\"\"") || index($0, sq) { exit }
    /^model[ \t]*=/ || /^model_reasoning_effort[ \t]*=/ { print }
  ' "$1"
}

# migration_pair <role> -- prints "<model> <effort>" and returns 0 when the role file
# exists as a regular file and its pin lines are exactly one shipped pair.
migration_pair() {
  file="$DEST/$1.toml"
  if [ ! -f "$file" ] || [ -L "$file" ]; then
    return 1
  fi
  pairs=$(shipped_pairs "$1")
  [ -n "$pairs" ] || return 1
  pins=$(top_pins "$file") || return 1
  while read -r m e; do
    want="model = \"$m\"
model_reasoning_effort = \"$e\""
    if [ "$pins" = "$want" ]; then
      printf '%s %s\n' "$m" "$e"
      return 0
    fi
  done <<EOF
$pairs
EOF
  return 1
}

# strip_pins <file> <model> <effort> -- the file without exactly those two top-level lines.
strip_pins() {
  awk -v "sq='''" -v "l1=model = \"$2\"" -v "l2=model_reasoning_effort = \"$3\"" '
    !stop && (/^\[/ || index($0, "\"\"\"") || index($0, sq)) { stop = 1 }
    !stop && ($0 == l1 || $0 == l2) { next }
    { print }
  ' "$1"
}

if [ "$MODE" = preview ]; then
  for role in $KNOWN_ROLES; do
    pair=$(migration_pair "$role") || continue
    TMP_CURRENT=$(mktemp "${TMPDIR:-/tmp}/cenci-install-agents.XXXXXX") || failed "mktemp failed; nothing was changed"
    strip_pins "$DEST/$role.toml" "${pair% *}" "${pair#* }" >"$TMP_CURRENT" || failed "could not render the preview for $role"
    rc=0
    diff -u -L "a/$role.toml" -L "b/$role.toml" "$DEST/$role.toml" "$TMP_CURRENT" || rc=$?
    [ "$rc" -le 1 ] || failed "diff failed for $role"
    rm -f "$TMP_CURRENT"
    TMP_CURRENT=""
  done
  exit 0
fi

if [ "$MODE" = apply ]; then
  # Re-check every named role before writing anything: all or nothing.
  for role in $APPLY_ROLES; do
    migration_pair "$role" >/dev/null || die "$role: not eligible for migration (its model and effort do not exactly match a default this role once shipped); nothing was written"
  done
  for role in $APPLY_ROLES; do
    pair=$(migration_pair "$role") || die "$role: no longer eligible for migration; stopping"
    file="$DEST/$role.toml"
    TMP_CURRENT=$(mktemp "$DEST/.install-agents.XXXXXX") || failed "mktemp failed in the agents directory; $role was not changed"
    cp -p "$file" "$TMP_CURRENT" || failed "could not prepare $role; it was not changed"
    strip_pins "$file" "${pair% *}" "${pair#* }" >"$TMP_CURRENT" || failed "could not rewrite $role; it was not changed"
    mv "$TMP_CURRENT" "$file" || failed "could not replace $role; it was not changed"
    TMP_CURRENT=""
    printf 'install-agents.sh: %s: removed the shipped model and effort pin\n' "$role"
  done
  exit 0
fi

# ── Install ─────────────────────────────────────────────────────────────────
SOURCE=${PLUGIN_ROOT:?PLUGIN_ROOT is required}/templates/codex/agent-roles

override_for() { # override_for <role> -- prints "<model> <effort>" when one was given
  printf '%s\n' "$OVERRIDES" | while read -r r m e; do
    if [ "$r" = "$1" ]; then
      printf '%s %s\n' "$m" "$e"
    fi
  done
}

# Every override must have a template to render into, checked before the first write.
for ov_role in $(printf '%s\n' "$OVERRIDES" | while read -r r m e; do printf '%s\n' "$r"; done); do
  [ -f "$SOURCE/$ov_role.toml" ] || die "$ov_role: no template found to apply an override to"
done

mkdir -p "$DEST"
for source in "$SOURCE"/*.toml; do
  role=$(basename "$source" .toml)
  target="$DEST/$role.toml"
  ov=$(override_for "$role")
  if [ -e "$target" ] || [ -L "$target" ]; then
    if [ -n "$ov" ]; then
      printf 'install-agents.sh: %s: role file already exists; override not applied (existing role files are never rewritten)\n' "$role" >&2
    fi
    continue
  fi
  if [ -z "$ov" ]; then
    cp "$source" "$target"
    continue
  fi
  model=${ov% *}
  effort=${ov#* }
  [ "$effort" != "-" ] || effort=""
  TMP_CURRENT=$(mktemp "$DEST/.install-agents.XXXXXX") || failed "mktemp failed in the agents directory; $role was not created"
  awk -v "model=$model" -v "effort=$effort" '
    /^model = / { next }
    /^model_reasoning_effort = / && effort != "" { next }
    { print }
    /^description = / && !done {
      printf "model = \"%s\"\n", model
      if (effort != "") printf "model_reasoning_effort = \"%s\"\n", effort
      done = 1
    }
    END { if (!done) exit 3 }
  ' "$source" >"$TMP_CURRENT" || failed "could not render the override for $role; it was not created"
  chmod 0644 "$TMP_CURRENT" || failed "could not set the mode of $role; it was not created"
  mv "$TMP_CURRENT" "$target" || failed "could not create $role"
  TMP_CURRENT=""
done
