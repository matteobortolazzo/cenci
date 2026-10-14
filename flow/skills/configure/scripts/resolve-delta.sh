#!/usr/bin/env bash
# resolve-delta.sh — deterministic answer to "which /cenci:configure questions
# would a re-run ask?" for a given .cenci/config.json (#1245, parent #1137).
# The question list and the config keys that record each answer live in
# ../question-keys.json; this script holds no per-question knowledge.
#
# Usage:
#   resolve-delta.sh <config-path|-> [--all]
#
# <config-path|-> is the current .cenci/config.json: a file path, or "-" to
# read it from stdin (a first run passes "{}"). Prints one JSON line:
#   {"ask":[ids],"carried":[ids],"missingKeys":[keys],"conditional":[ids]}
# with every array in table order.
#
# Rules:
#   - A key is present unless it is missing or null (false, {} and "" count).
#     A projectsAware row also counts a key found inside any projects[] entry.
#   - A row is asked when every one of its keys is absent AND its ID is not in
#     the config's `configureOffered` ledger (the questions already put to the
#     user). Keyless rows (empty keys) are therefore tracked by the ledger only.
#     A config without a ledger is treated as having an empty one.
#   - --all asks every row and carries none.
#   - missingKeys: the absent keys of the asked rows.
#   - conditional: the asked IDs flagged conditional (detection-dependent).
#   - Output carries identifiers from the table only, never config values.
#
# Fails closed on every error — no jq, bad arguments, an unreadable config or
# table, invalid JSON, a config that is not an object, a malformed ledger or
# table: a message on stderr, exit 2, and empty stdout. Stdout is written only
# after every step has succeeded.
set -uo pipefail

usage() {
  echo "usage: resolve-delta.sh <config-path|-> [--all]" >&2
  exit 2
}

die() {
  echo "resolve-delta.sh: $1" >&2
  exit 2
}

command -v jq >/dev/null 2>&1 || die "jq is required but was not found on PATH."

CONFIG_SRC=""
ALL="false"
for arg in "$@"; do
  case "$arg" in
    --all)
      ALL="true"
      ;;
    --*)
      die "unknown argument: ${arg}"
      ;;
    *)
      [[ -z "$CONFIG_SRC" ]] || die "unexpected extra argument: ${arg}"
      CONFIG_SRC="$arg"
      ;;
  esac
done
[[ -n "$CONFIG_SRC" ]] || usage

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || die "cannot resolve the script directory"
TABLE_FILE="${SELF_DIR}/../question-keys.json"

[[ -f "$TABLE_FILE" ]] || die "question table not found: ${TABLE_FILE}"
if ! TABLE_RAW="$(cat "$TABLE_FILE")"; then
  die "unreadable question table: ${TABLE_FILE}"
fi

if [[ "$CONFIG_SRC" == "-" ]]; then
  if ! CONFIG_RAW="$(cat)"; then
    die "unreadable stdin"
  fi
else
  [[ -f "$CONFIG_SRC" ]] || die "config is not a readable file: ${CONFIG_SRC}"
  if ! CONFIG_RAW="$(cat "$CONFIG_SRC")"; then
    die "unreadable config: ${CONFIG_SRC}"
  fi
fi
[[ -n "$CONFIG_RAW" ]] || die "config is empty"

# Exactly one JSON value, and it must be an object.
OBJECT_ONLY='if length == 1 and (.[0] | type) == "object" then .[0] else empty end'

CONFIG="$(jq -s -c -e "$OBJECT_ONLY" <<<"$CONFIG_RAW" 2>/dev/null)" \
  || die "config is not a single valid JSON object"
TABLE="$(jq -s -c -e "$OBJECT_ONLY" <<<"$TABLE_RAW" 2>/dev/null)" \
  || die "question table is not a single valid JSON object"

jq -e '
  (.questions | type) == "array"
  and (.questions | length) > 0
  and all(.questions[];
        type == "object"
        and (.id | type) == "string" and (.id | length) > 0
        and (.keys | type) == "array"
        and all(.keys[]; type == "string" and length > 0)
        and (.conditional | type) == "boolean"
        and ((has("projectsAware") | not) or (.projectsAware | type) == "boolean"))
  and ([.questions[].id] | unique | length) == (.questions | length)
' <<<"$TABLE" >/dev/null 2>&1 \
  || die "question table is invalid (need a unique string id, keys array, boolean conditional per row)"

jq -e '
  (has("configureOffered") | not)
  or ((.configureOffered | type) == "array" and all(.configureOffered[]; type == "string"))
' <<<"$CONFIG" >/dev/null 2>&1 \
  || die "configureOffered must be an array of strings"

if ! RESULT="$(jq -c -n \
  --argjson cfg "$CONFIG" \
  --argjson table "$TABLE" \
  --argjson all "$ALL" '
  def present($obj; $path): (try ($obj | getpath($path | split("."))) catch null) != null;
  ($cfg | if has("configureOffered") then .configureOffered else [] end) as $ledger
  | ($cfg | if has("projects") and (.projects | type) == "array" then .projects else [] end) as $projects
  | [ $table.questions[]
      | . as $row
      | ($row.projectsAware // false) as $aware
      | ([ $row.keys[]
           | . as $key
           | select(
               (present($cfg; $key) or ($aware and any($projects[]; present(.; $key)))) | not
             ) ]) as $absent
      | { id: $row.id,
          conditional: $row.conditional,
          absent: $absent,
          ask: ($all or ( ($absent | length) == ($row.keys | length)
                          and (($ledger | index($row.id)) == null) )) }
    ] as $rows
  | ($rows | map(select(.ask))) as $asked
  | { ask: [$asked[].id],
      carried: [$rows[] | select(.ask | not) | .id],
      missingKeys: [$asked[].absent[]],
      conditional: [$asked[] | select(.conditional) | .id] }
' 2>/dev/null)"; then
  die "could not compute the question delta"
fi
[[ -n "$RESULT" ]] || die "could not compute the question delta"

printf '%s\n' "$RESULT"
