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
# `### Size Estimate: <letter>` in Implementation Plan, else `unknown`.
# Legacy plans without that section retain first-heading compatibility. The
# approval: trivial legacy one-line prose plan also inherits its original ticket
# estimate; other plans with no confirmed estimate remain unknown.
# Size Acceptance is a root-validated approval record, not authorization this
# measurement script can grant. Its structural fields are checked fail-closed.
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
TICKET=""; PLAN=""; ORIGINAL="unknown"; ACCEPTED="false"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ticket) TICKET="${2:-}"; shift 2 || die "--ticket needs a value" ;;
    --plan) PLAN="${2:-}"; shift 2 || die "--plan needs a value" ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ -d "${WORKTREE}" ]] || die "worktree is not a directory: ${WORKTREE}"

if [[ -n "${PLAN}" ]]; then
  [[ -r "${PLAN}" ]] || die "plan file is not readable: ${PLAN}"
  PLAN_SIZES="$(awk '
    function size_letter(line) {
      sub(/^### Size Estimate: */, "", line); sub(/[ \t]*$/, "", line); return line
    }
    NR == 1 && $0 == "---" { frontmatter = 1; next }
    frontmatter {
      if ($0 == "---") frontmatter = 0
      else if (/^approval:/) { approvals++; if ($0 == "approval: trivial") trivial_approval++ }
      next
    }
    section == "## Implementation Plan" && !/^## / && /[^ \t]/ {
      plan_lines++
      if ($0 ~ /^[ \t]*(#|```|~~~)/) nonprose_plan = 1
    }
    /^[ \t]*(```|~~~)/ {
      delimiter = $0; sub(/^[ \t]*/, "", delimiter)
      char = substr(delimiter, 1, 1); run = 0
      while (substr(delimiter, run + 1, 1) == char) run++
      if (!fence) { fence = 1; fence_char = char; fence_length = run }
      else if (char == fence_char && run >= fence_length && substr(delimiter, run + 1) ~ /^[ \t]*$/) { fence = 0; inner_fence = 0 }
      else if (section == "## Ticket Details") {
        if (!inner_fence) { inner_fence = 1; inner_char = char; inner_length = run }
        else if (char == inner_char && run >= inner_length && substr(delimiter, run + 1) ~ /^[ \t]*$/) inner_fence = 0
      }
      next
    }
    fence && !inner_fence && section == "## Ticket Details" && /^### Size Estimate: *[SML][ \t]*$/ {
      if (!original) original = size_letter($0)
    }
    fence { next }
    /^## / { section = $0; if ($0 == "## Implementation Plan") has_plan++; acceptance = 0 }
    /^### / { acceptance = (section == "## Implementation Plan" && $0 == "### Size Acceptance"); if (acceptance) has_acceptance++ }
    /^### Size Estimate: *[SML][ \t]*$/ {
      letter = size_letter($0)
      if (!legacy) legacy = letter
      if (section == "## Implementation Plan" && !confirmed) confirmed = letter
      if (section == "## Ticket Details" && !original) original = letter
    }
    acceptance && /^(Target|Scope|Supported estimate|Lanes|Reason to retain one PR|Human approval):/ {
      field = $0; sub(/:.*/, "", field); fields[field]++
      if ($0 !~ /:[ \t]*[^ \t]/ || fields[field] > 1) bad = 1
    }
    acceptance && /^Original size:/ { old_m++; if ($0 != "Original size: M") bad = 1 }
    acceptance && /^Accepted size:/ { new_l++; if ($0 != "Accepted size: L") bad = 1 }
    acceptance && /^Review:/ { review++; if ($0 != "Review: full founder read; no automerge") bad = 1 }
    END {
      size = has_plan ? confirmed : legacy
      if (!size && has_plan == 1 && approvals == 1 && trivial_approval == 1 && plan_lines == 1 && !nonprose_plan && !has_acceptance) size = original
      valid = !bad && has_acceptance == 1 && old_m == 1 && new_l == 1 && review == 1 && fields["Target"] && fields["Scope"] && fields["Supported estimate"] && fields["Lanes"] && fields["Reason to retain one PR"] && fields["Human approval"] && size == "L" && original == "M"
      if (has_acceptance && !valid) exit 2
      if (original == "M" && size == "L" && !valid) exit 2
      print (size ? size : "unknown"), (original ? original : "unknown"), (has_acceptance ? "true" : "false")
    }
  ' "${PLAN}")" || die "plan Size Acceptance is missing, incomplete or inconsistent; return to planning"
  read -r PLAN_TICKET ORIGINAL ACCEPTED <<< "${PLAN_SIZES}"
  if [[ "${ORIGINAL}" == "M" && "${TICKET}" == "L" && "${ACCEPTED}" != "true" ]]; then
    die "--ticket L cannot replace the original M without a valid plan Size Acceptance; return to planning"
  fi
  if [[ "${ACCEPTED}" == "true" && -n "${TICKET}" && "${TICKET}" != "L" ]]; then
    die "--ticket conflicts with the plan Size Acceptance; return to planning"
  fi
  [[ -n "${TICKET}" ]] || TICKET="${PLAN_TICKET}"
fi
case "${TICKET}" in
  S|M|L) ;;
  ""|unknown) TICKET="unknown" ;;
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

echo "files=${files} lines=${lines} size=${size} ticket=${TICKET} mismatch=${mismatch} original=${ORIGINAL} accepted=${ACCEPTED}"
comparison="ticket ${TICKET}"
if [[ "${ACCEPTED}" == "true" ]]; then comparison="accepted L (original ticket M)"; fi
if [[ "${size}" == "XL" ]]; then
  echo "Size: beyond L (${files} files, ${lines} lines) vs ${comparison}"
else
  echo "Size: ${size} (${files} files, ${lines} lines) vs ${comparison}"
fi
if [[ "${ACCEPTED}" == "true" ]]; then
  echo "**Accepted size correction**: original ticket M; accepted L — full founder read required before merge; no automerge. Preserve the complete Size Acceptance record in the PR."
fi
case "${mismatch}" in
  above)   echo "**Size mismatch**: the PR is one size above its ticket (${size} vs ${TICKET}) — founder decision required; do not merge on green checks alone." ;;
  beyond)  echo "**Size mismatch**: the PR exceeds the L bound (<= 60 changed files, <= 5,000 changed lines) — it must be split; do not merge." ;;
  unknown) echo "**Size unverified**: the ticket carries no \`### Size Estimate: <letter>\` to compare against — refine the ticket before merging." ;;
esac
exit 0
