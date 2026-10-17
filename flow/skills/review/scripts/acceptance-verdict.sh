#!/usr/bin/env bash
# acceptance-verdict.sh -- turns an acceptance reviewer's item table into a
# deterministic pass/fail verdict. Shared by the implement pipeline (Phase 6 + 7
# acceptance review, Phase 9 PR body), the review skill (PR mode) and the close
# skill (parent closure).
#
# Usage:
#   acceptance-verdict.sh <table.md> [--ui | --ui-local]
#
# <table.md> holds the reviewer's `## Acceptance Review` table. Rows have the
# shape (header and separator rows are ignored):
#   | <n> | <kind> | <item> | <status> | <proof> |
# <status> is exactly one of: covered, partial, missing, unrequested.
#
# UI evidence (Velka: a UI ticket's acceptance evidence is the gallery and the
# secret-gist screenshots, each in en-US and it-IT; a diff-only acceptance
# review of a UI ticket is a failure). The table file carries evidence lines
# of the form `Evidence: <source> <locale>: <path-or-url>`:
#   --ui        requires all four of: gallery en-US, gallery it-IT, gist en-US, gist it-IT
#   --ui-local  requires all four of: gallery en-US, gallery it-IT, capture en-US, capture it-IT
#               (the pre-gist form the pipeline uses at Phase 6 + 7, before Phase 9
#               has uploaded the gist; Phase 9 re-runs with --ui after the upload)
#
# Output (stdout, always one line first):
#   covered=<n> partial=<n> missing=<n> unrequested=<n> verdict=pass|fail
# followed, on a fail, by one `blocking: <n> [<kind>] <item> -- <status>` line
# per partial/missing row and one `ui-evidence-missing: <source> <locale>`
# line per absent evidence line.
#
# Exit codes: 0 pass; 1 fail (any partial or missing row, or missing UI
# evidence under --ui/--ui-local); 2 malformed (unreadable file, zero rows,
# or a status outside the four allowed values -- fail closed, never a pass).
set -uo pipefail

die() { echo "acceptance-verdict.sh: $*" >&2; exit 2; }

TABLE="${1:-}"
[[ -n "${TABLE}" ]] || die "usage: acceptance-verdict.sh <table.md> [--ui | --ui-local]"
[[ -r "${TABLE}" ]] || die "table file is not readable: ${TABLE}"
MODE="${2:-}"
case "${MODE}" in
  ""|--ui|--ui-local) ;;
  *) die "unknown flag: ${MODE} (expected --ui or --ui-local)" ;;
esac

covered=0; partial=0; missing=0; unrequested=0; rows=0
BLOCKING=()
trim() { local s="$1"; s="${s#"${s%%[![:space:]]*}"}"; s="${s%"${s##*[![:space:]]}"}"; printf '%s' "${s}"; }

while IFS= read -r line || [[ -n "${line}" ]]; do
  [[ "${line}" == \|* ]] || continue
  # Split on unescaped pipes: strip the leading/trailing pipe, then split.
  body="${line#|}"; body="${body%|}"
  IFS='|' read -r -a cells <<< "${body}"
  [[ "${#cells[@]}" -ge 5 ]] || continue
  n="$(trim "${cells[0]}")"
  [[ "${n}" =~ ^[0-9]+$ ]] || continue   # header / separator rows
  kind="$(trim "${cells[1]}")"
  item="$(trim "${cells[2]}")"
  status="$(trim "${cells[3]}")"
  rows=$((rows + 1))
  case "${status}" in
    covered) covered=$((covered + 1)) ;;
    partial) partial=$((partial + 1)); BLOCKING+=("blocking: ${n} [${kind}] ${item} -- partial") ;;
    missing) missing=$((missing + 1)); BLOCKING+=("blocking: ${n} [${kind}] ${item} -- missing") ;;
    unrequested) unrequested=$((unrequested + 1)) ;;
    *) die "row ${n}: status must be covered|partial|missing|unrequested, got: ${status}" ;;
  esac
done < "${TABLE}"

[[ "${rows}" -gt 0 ]] || die "zero item rows parsed from ${TABLE} -- an empty review is not a pass"

UI_MISSING=()
if [[ -n "${MODE}" ]]; then
  if [[ "${MODE}" == "--ui" ]]; then
    required=("gallery en-US" "gallery it-IT" "gist en-US" "gist it-IT")
  else
    required=("gallery en-US" "gallery it-IT" "capture en-US" "capture it-IT")
  fi
  for req in "${required[@]}"; do
    if ! grep -qE "^Evidence: ${req}: [^[:space:]]+" "${TABLE}"; then
      UI_MISSING+=("ui-evidence-missing: ${req}")
    fi
  done
fi

verdict=pass
if [[ "${partial}" -gt 0 || "${missing}" -gt 0 || "${#UI_MISSING[@]}" -gt 0 ]]; then
  verdict=fail
fi
echo "covered=${covered} partial=${partial} missing=${missing} unrequested=${unrequested} verdict=${verdict}"
for b in "${BLOCKING[@]:-}"; do [[ -n "${b}" ]] && echo "${b}"; done
for u in "${UI_MISSING[@]:-}"; do [[ -n "${u}" ]] && echo "${u}"; done
[[ "${verdict}" == "pass" ]] && exit 0
exit 1
