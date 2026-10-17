#!/usr/bin/env bash
# pr-size.sh -- measure a branch's final diff against the S/M/L table in
# docs/ticket-sizing.md (Velka's "Ticket sizes") and render the PR-body line
# `Size: <letter> (<files> files, <lines> lines) vs ticket <letter>`.
#
# Usage:
#   pr-size.sh <worktree-path> <base-ref> [--ticket <S|M|L>] [--plan <plan-file>]
#
# Lines are insertions plus deletions over the whole diff, generated files and
# lockfiles included (a binary file counts as one changed file and zero lines).
# The ticket's letter comes from --ticket, else from the first
# `### Size Estimate: <letter>` heading in --plan (the planner-confirmed size,
# or the ticket body appended into the plan), else `unknown`.
#
# Classification (the table, executable):
#   S  files <= 10 and lines <= 400
#   M  files <= 30 and lines <= 2000
#   L  files <= 60 and lines <= 5000
#   XL beyond L -- must have been split
#
# stdout:
#   files=<n> lines=<n> size=<letter> ticket=<letter> mismatch=none|above|below|beyond|unknown
#   Size: <letter> (<files> files, <lines> lines) vs ticket <letter>
#   [flag line -- only when mismatch is above or beyond]
# Exit 0 whenever the measurement succeeded (a mismatch is a flag, not an
# error); exit 2 on usage errors or when git cannot produce the diff.
set -uo pipefail

die() { echo "pr-size.sh: $*" >&2; exit 2; }

WORKTREE="${1:-}"; BASE="${2:-}"
[[ -n "${WORKTREE}" && -n "${BASE}" ]] || die "usage: pr-size.sh <worktree-path> <base-ref> [--ticket <S|M|L>] [--plan <plan-file>]"
shift 2
TICKET=""; PLAN=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ticket) TICKET="${2:-}"; shift 2 || die "--ticket needs a value" ;;
    --plan) PLAN="${2:-}"; shift 2 || die "--plan needs a value" ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ -d "${WORKTREE}" ]] || die "worktree is not a directory: ${WORKTREE}"

if [[ -z "${TICKET}" && -n "${PLAN}" ]]; then
  [[ -r "${PLAN}" ]] || die "plan file is not readable: ${PLAN}"
  TICKET="$(grep -m1 -oE '^### Size Estimate: *[SML]\b' "${PLAN}" | grep -oE '[SML]$')" || TICKET=""
fi
case "${TICKET}" in
  S|M|L) ;;
  "") TICKET="unknown" ;;
  *) die "--ticket must be S, M or L, got: ${TICKET}" ;;
esac

NUMSTAT="$(git -C "${WORKTREE}" diff --numstat "${BASE}...HEAD" 2>&1)" || die "git diff --numstat ${BASE}...HEAD failed in ${WORKTREE}: ${NUMSTAT}"

files=0; lines=0
while IFS=$'\t' read -r ins del _path; do
  [[ -n "${_path:-}" ]] || continue
  files=$((files + 1))
  [[ "${ins}" =~ ^[0-9]+$ ]] && lines=$((lines + ins))
  [[ "${del}" =~ ^[0-9]+$ ]] && lines=$((lines + del))
done <<< "${NUMSTAT}"

rank_of() { case "$1" in S) echo 1 ;; M) echo 2 ;; L) echo 3 ;; XL) echo 4 ;; *) echo 0 ;; esac; }

if [[ "${files}" -le 10 && "${lines}" -le 400 ]]; then size=S
elif [[ "${files}" -le 30 && "${lines}" -le 2000 ]]; then size=M
elif [[ "${files}" -le 60 && "${lines}" -le 5000 ]]; then size=L
else size=XL; fi

actual_rank="$(rank_of "${size}")"
ticket_rank="$(rank_of "${TICKET}")"
if [[ "${size}" == "XL" ]]; then mismatch=beyond
elif [[ "${ticket_rank}" -eq 0 ]]; then mismatch=unknown
elif [[ "${actual_rank}" -gt "${ticket_rank}" ]]; then mismatch=above
elif [[ "${actual_rank}" -lt "${ticket_rank}" ]]; then mismatch=below
else mismatch=none; fi

echo "files=${files} lines=${lines} size=${size} ticket=${TICKET} mismatch=${mismatch}"
if [[ "${size}" == "XL" ]]; then
  echo "Size: beyond L (${files} files, ${lines} lines) vs ticket ${TICKET}"
else
  echo "Size: ${size} (${files} files, ${lines} lines) vs ticket ${TICKET}"
fi
case "${mismatch}" in
  above)   echo "**Size mismatch**: the PR is one size above its ticket (${size} vs ${TICKET}) — founder decision required; do not merge on green checks alone." ;;
  beyond)  echo "**Size mismatch**: the PR exceeds the L bound (<= 60 changed files, <= 5,000 changed lines) — it must be split; do not merge." ;;
  unknown) echo "**Size unverified**: the ticket carries no \`### Size Estimate: <letter>\` to compare against — refine the ticket before merging." ;;
esac
exit 0
