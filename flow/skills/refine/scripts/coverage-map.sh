#!/usr/bin/env bash
# coverage-map.sh -- the mechanical half of the refine skill's split coverage
# exercise (Velka's "Splitting a ticket": every parent item has exactly one
# child owner, and the parent body gets a `### Coverage map`).
#
# Usage:
#   coverage-map.sh check --items <items.md> --child <label>=<body.md> [--child <label>=<body.md> ...]
#
# <items.md> is the parent's numbered item list, one item per line:
#   1. [acceptance criterion] <text verbatim>
#   2. [edge case] <text verbatim>
#   3. [verification step] <text>
#   4. [documentation item] <text>
#   5. [boundary] <text>
# Kinds are exactly those five. Blank lines and lines starting with `#` are
# ignored.
#
# Each --child names one child body file. An item is owned by a child when the
# item's text appears in that child's body VERBATIM (whitespace-normalized,
# case-sensitive) -- a summary, a heading, or a paraphrase is not ownership,
# which is the whole point: an edge case compressed into a heading is a lost
# edge case.
#
# Outcome:
#   exit 0  every item owned by exactly one child. stdout: a summary line
#           `items=<n> owned=<n> children=<n>` followed by the rendered
#           `### Coverage map` section (one row per item, owner = <label>).
#   exit 3  at least one item has no owner. stderr lists each as
#           `unowned: <n> [<kind>] <text>`; stdout prints the summary line
#           and nothing else. The caller fails the split -- it never drops
#           the item.
#   exit 4  at least one item is owned by more than one child. stderr lists
#           each as `duplicate: <n> [<kind>] <text> -> <label>, <label>`.
#   exit 2  usage / unreadable file / malformed item line / zero items.
#
# Pure: reads files, writes stdout/stderr, touches nothing else. Safe to run
# before any GitHub write (the refine skill's Coverage gate) and again after
# child creation with real issue numbers as labels (Pass 2's map render and
# the post-creation self-check).
set -uo pipefail

die() { echo "coverage-map.sh: $*" >&2; exit 2; }
usage() { die "usage: coverage-map.sh check --items <items.md> --child <label>=<body.md> [--child ...]"; }

[[ "${1:-}" == "check" ]] || usage
shift

ITEMS_FILE=""
CHILD_LABELS=()
CHILD_FILES=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --items) ITEMS_FILE="${2:-}"; shift 2 || usage ;;
    --child)
      spec="${2:-}"; shift 2 || usage
      [[ "${spec}" == *=* ]] || die "--child expects <label>=<body-file>, got: ${spec}"
      CHILD_LABELS+=("${spec%%=*}")
      CHILD_FILES+=("${spec#*=}")
      ;;
    *) usage ;;
  esac
done

[[ -n "${ITEMS_FILE}" ]] || usage
[[ -r "${ITEMS_FILE}" ]] || die "items file is not readable: ${ITEMS_FILE}"
[[ "${#CHILD_FILES[@]}" -gt 0 ]] || die "at least one --child is required"
for f in "${CHILD_FILES[@]}"; do
  [[ -r "${f}" ]] || die "child body file is not readable: ${f}"
done

# normalize <string> -- collapse every whitespace run to one space, trim ends.
normalize() {
  local s="$1"
  s="$(printf '%s' "${s}" | tr '\n\t' '  ' | sed -E 's/[[:space:]]+/ /g; s/^ //; s/ $//')"
  printf '%s' "${s}"
}

ITEM_NUMS=()
ITEM_KINDS=()
ITEM_TEXTS=()
while IFS= read -r line || [[ -n "${line}" ]]; do
  [[ -n "${line// /}" ]] || continue
  [[ "${line}" == \#* ]] && continue
  if [[ "${line}" =~ ^[[:space:]]*([0-9]+)\.[[:space:]]*\[(acceptance\ criterion|edge\ case|verification\ step|documentation\ item|boundary)\][[:space:]]*(.+)$ ]]; then
    ITEM_NUMS+=("${BASH_REMATCH[1]}")
    ITEM_KINDS+=("${BASH_REMATCH[2]}")
    ITEM_TEXTS+=("$(normalize "${BASH_REMATCH[3]}")")
  else
    die "malformed item line (expected '<n>. [<kind>] <text>' with kind in acceptance criterion|edge case|verification step|documentation item|boundary): ${line}"
  fi
done < "${ITEMS_FILE}"

[[ "${#ITEM_NUMS[@]}" -gt 0 ]] || die "zero items parsed from ${ITEMS_FILE} -- an empty enumeration cannot be covered"

CHILD_NORM=()
for f in "${CHILD_FILES[@]}"; do
  content="$(cat "${f}")" || die "failed to read child body file: ${f}"
  CHILD_NORM+=("$(normalize "${content}")")
done

unowned=0
duplicate=0
OWNERS=()
for i in "${!ITEM_NUMS[@]}"; do
  owners=""
  count=0
  for c in "${!CHILD_FILES[@]}"; do
    if [[ "${CHILD_NORM[$c]}" == *"${ITEM_TEXTS[$i]}"* ]]; then
      count=$((count + 1))
      if [[ -z "${owners}" ]]; then owners="${CHILD_LABELS[$c]}"; else owners="${owners}, ${CHILD_LABELS[$c]}"; fi
    fi
  done
  OWNERS+=("${owners}")
  if [[ "${count}" -eq 0 ]]; then
    unowned=$((unowned + 1))
    echo "unowned: ${ITEM_NUMS[$i]} [${ITEM_KINDS[$i]}] ${ITEM_TEXTS[$i]}" >&2
  elif [[ "${count}" -gt 1 ]]; then
    duplicate=$((duplicate + 1))
    echo "duplicate: ${ITEM_NUMS[$i]} [${ITEM_KINDS[$i]}] ${ITEM_TEXTS[$i]} -> ${owners}" >&2
  fi
done

owned=$(( ${#ITEM_NUMS[@]} - unowned ))
echo "items=${#ITEM_NUMS[@]} owned=${owned} children=${#CHILD_FILES[@]}"

if [[ "${unowned}" -gt 0 ]]; then
  echo "coverage-map.sh: ${unowned} parent item(s) have no owning child -- the split is wrong; never drop an item." >&2
  exit 3
fi
if [[ "${duplicate}" -gt 0 ]]; then
  echo "coverage-map.sh: ${duplicate} parent item(s) are owned by more than one child -- each item has exactly one owner." >&2
  exit 4
fi

echo
echo "### Coverage map"
echo "| # | Kind | Item | Owner |"
echo "|---|---|---|---|"
for i in "${!ITEM_NUMS[@]}"; do
  text="${ITEM_TEXTS[$i]//|/\\|}"
  echo "| ${ITEM_NUMS[$i]} | ${ITEM_KINDS[$i]} | ${text} | ${OWNERS[$i]} |"
done
exit 0
