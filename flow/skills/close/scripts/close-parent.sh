#!/usr/bin/env bash
# close-parent.sh -- the mechanical half of the `close` skill's "close parent"
# step (Velka's "Closing a split parent requires a coverage review"). The
# acceptance reviewer produces the item table; this script refuses, posts,
# creates gap children, or closes -- deterministically, with every GitHub
# write verified.
#
# Usage:
#   close-parent.sh --repo <owner>/<repo> --parent <n> --review <table.md> [--dry-run]
#
# <table.md> is the acceptance reviewer's parent-closure table, in the shape
# acceptance-verdict.sh parses:
#   | <n> | <kind> | <item> | covered|partial|missing|unrequested | <PR + proof> |
# compared item by item against the MERGED child PRs and their tests and
# docs (never against the child issue bodies).
#
# Behavior, in order (every step fails closed):
#   1. Read the parent (`gh issue view --json state,labels,milestone,subIssues,
#      subIssuesSummary`). A read failure, a missing/null sub-issue graph, or a
#      truncated graph exits 2 with no write.
#   2. Zero sub-issues -> exit 5: not a split parent; close it the ordinary way.
#   3. Any sub-issue not CLOSED -> exit 3, naming every open child. No write.
#   4. Verdict via acceptance-verdict.sh (sibling script under skills/review/).
#      A malformed table exits 2 with no write.
#   5. Post the review as a comment on the parent (banner + table verbatim +
#      `<!-- cenci-parent-close-review -->` marker), verified by the created
#      comment's own id -- never by scanning the thread.
#   6. On a fail verdict: for every partial/missing row, create a new child
#      under the same parent -- the parent's milestone, the parent's
#      non-lifecycle labels, the item verbatim as its acceptance criterion,
#      linked as a native sub-issue and verified from the parent side -- and
#      exit 4 WITHOUT closing. If any child create/link fails, stop and
#      report partial state; the parent stays open.
#   7. On a pass verdict (zero partial/missing): close the parent as
#      completed, swap `In Review` -> `Implemented`, verify `state == CLOSED`,
#      exit 0.
#
# --dry-run performs steps 1-4 and prints the planned writes; nothing is
# posted, created, edited or closed.
#
# stdout: `parent=<n> children=<n> open=<n> covered=<n> partial=<n> missing=<n> unrequested=<n> outcome=<...>`
#         then `created: #<n>` per gap child, then `review-comment: <id>`.
# Exit codes: 0 closed; 3 refused (open children); 4 refused (gaps -> new
# children created, parent left open); 5 not a split parent; 2 read/verdict
# failure or a failed write (partial state is printed before exit).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "close-parent.sh: failed to resolve script directory." >&2; exit 2; }
VERDICT_SH="${SCRIPT_DIR}/../../review/scripts/acceptance-verdict.sh"

die() { echo "close-parent.sh: $*" >&2; exit 2; }
usage() { die "usage: close-parent.sh --repo <owner>/<repo> --parent <n> --review <table.md> [--dry-run]"; }

REPO=""; PARENT=""; REVIEW=""; DRY_RUN=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="${2:-}"; shift 2 || usage ;;
    --parent) PARENT="${2:-}"; shift 2 || usage ;;
    --review) REVIEW="${2:-}"; shift 2 || usage ;;
    --dry-run) DRY_RUN=1; shift ;;
    *) usage ;;
  esac
done
[[ "${REPO}" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || die "--repo must look like owner/repo, got: ${REPO}"
[[ "${PARENT}" =~ ^[0-9]+$ ]] || die "--parent must be numeric, got: ${PARENT}"
[[ -r "${REVIEW}" ]] || die "--review table is not readable: ${REVIEW}"
[[ -r "${VERDICT_SH}" ]] || die "acceptance-verdict.sh not found next to this script's review sibling: ${VERDICT_SH}"
command -v jq >/dev/null 2>&1 || die "jq is required but was not found on PATH"
command -v gh >/dev/null 2>&1 || die "gh is required but was not found on PATH"

TMP_ROOT="${TMPDIR:-/tmp}/cenci"
mkdir -p "${TMP_ROOT}" || die "cannot create ${TMP_ROOT}"
WORK="$(mktemp -d "${TMP_ROOT}/close-parent-${PARENT}-XXXXXX")" || die "mktemp -d failed under ${TMP_ROOT}"
[[ -n "${WORK}" && -d "${WORK}" ]] || die "mktemp produced no usable directory"
trap 'rm -rf "${WORK}"' EXIT

# --- 1. Read the parent ------------------------------------------------------
PARENT_JSON="${WORK}/parent.json"
if ! gh issue view "${PARENT}" --repo "${REPO}" --json number,state,labels,milestone,subIssues,subIssuesSummary > "${PARENT_JSON}" 2>"${WORK}/parent.err"; then
  die "could not read parent #${PARENT}: $(cat "${WORK}/parent.err" 2>/dev/null)"
fi
jq -e 'type == "object" and has("subIssues")' "${PARENT_JSON}" >/dev/null 2>&1 || die "parent #${PARENT} read returned no usable sub-issue graph (fail closed)"

TOTAL="$(jq -r '.subIssues.totalCount // .subIssuesSummary.total // empty' "${PARENT_JSON}")" || die "could not read the sub-issue count"
NODES="$(jq -r '(.subIssues.nodes // []) | length' "${PARENT_JSON}")" || die "could not read the sub-issue nodes"
[[ "${TOTAL}" =~ ^[0-9]+$ ]] || TOTAL="${NODES}"
# --- 2. Not a split parent ---------------------------------------------------
if [[ "${TOTAL}" -eq 0 && "${NODES}" -eq 0 ]]; then
  echo "parent=${PARENT} children=0 open=0 outcome=not-a-split-parent"
  echo "close-parent.sh: #${PARENT} has no sub-issues; this step only closes split parents -- close it the ordinary way." >&2
  exit 5
fi
[[ "${NODES}" -eq "${TOTAL}" ]] || die "sub-issue graph truncated for #${PARENT} (${NODES} nodes of ${TOTAL}) -- fail closed"

# --- 3. Every child must be closed -------------------------------------------
OPEN_LIST="$(jq -r '[.subIssues.nodes[] | select((.state // "") != "CLOSED") | "#\(.number)"] | join(" ")' "${PARENT_JSON}")" || die "could not evaluate child states"
OPEN_COUNT="$(jq -r '[.subIssues.nodes[] | select((.state // "") != "CLOSED")] | length' "${PARENT_JSON}")" || die "could not count open children"
if [[ "${OPEN_COUNT}" -gt 0 ]]; then
  echo "parent=${PARENT} children=${NODES} open=${OPEN_COUNT} outcome=refused-open-children"
  echo "close-parent.sh: refusing to close #${PARENT}: open children ${OPEN_LIST}" >&2
  exit 3
fi
PARENT_STATE="$(jq -r '.state // ""' "${PARENT_JSON}")"

# --- 4. Verdict --------------------------------------------------------------
VERDICT_OUT="${WORK}/verdict.txt"
bash "${VERDICT_SH}" "${REVIEW}" > "${VERDICT_OUT}" 2>"${WORK}/verdict.err"
VERDICT_RC=$?
if [[ "${VERDICT_RC}" -eq 2 ]]; then
  die "the review table is malformed: $(cat "${WORK}/verdict.err" 2>/dev/null)"
fi
SUMMARY="$(head -1 "${VERDICT_OUT}")"
covered="$(sed -nE 's/.*covered=([0-9]+).*/\1/p' <<< "${SUMMARY}")"
partial="$(sed -nE 's/.*partial=([0-9]+).*/\1/p' <<< "${SUMMARY}")"
missing="$(sed -nE 's/.*missing=([0-9]+).*/\1/p' <<< "${SUMMARY}")"
unrequested="$(sed -nE 's/.*unrequested=([0-9]+).*/\1/p' <<< "${SUMMARY}")"
[[ "${covered}" =~ ^[0-9]+$ && "${partial}" =~ ^[0-9]+$ && "${missing}" =~ ^[0-9]+$ ]] || die "could not parse the verdict summary: ${SUMMARY}"

if [[ "${VERDICT_RC}" -eq 0 ]]; then outcome_plan=close; else outcome_plan=gaps; fi

if [[ "${DRY_RUN}" -eq 1 ]]; then
  echo "parent=${PARENT} children=${NODES} open=0 covered=${covered} partial=${partial} missing=${missing} unrequested=${unrequested:-0} outcome=dry-run-${outcome_plan}"
  grep '^blocking:' "${VERDICT_OUT}" || true
  exit 0
fi

# --- 5. Post the review comment on the parent ---------------------------------
COMMENT_BODY="${WORK}/comment.md"
{
  printf '%s\n\n' "> 🤖 **cenci** — split-parent coverage review posted by \`/cenci:close\` (parent closure)."
  if [[ "${outcome_plan}" == "close" ]]; then
    printf '%s\n\n' "Every original acceptance criterion and edge case of #${PARENT} is covered by a merged child PR (compared against the merged PRs, their tests and docs — not the child issue bodies). Closing the parent."
  else
    printf '%s\n\n' "#${PARENT} is **not** closed: ${partial} partial and ${missing} missing item(s) remain after comparing the parent's original acceptance criteria and edge cases against the merged child PRs, their tests and docs. Each becomes a new child under this parent (listed below)."
  fi
  cat "${REVIEW}"
  printf '\n\n%s\n' "<!-- cenci-parent-close-review -->"
} > "${COMMENT_BODY}" || die "could not stage the review comment"

COMMENT_ID="$(gh api "repos/${REPO}/issues/${PARENT}/comments" -F body=@"${COMMENT_BODY}" --jq .id 2>"${WORK}/comment.err")" || die "posting the review comment on #${PARENT} failed: $(cat "${WORK}/comment.err" 2>/dev/null)"
[[ "${COMMENT_ID}" =~ ^[0-9]+$ ]] || die "the comments API returned no numeric id for the review comment"
READBACK="$(gh api "repos/${REPO}/issues/comments/${COMMENT_ID}" --jq .body 2>/dev/null)" || die "could not read back review comment ${COMMENT_ID}"
[[ "${READBACK}" == *"<!-- cenci-parent-close-review -->"* ]] || die "review comment ${COMMENT_ID} does not carry the parent-close-review marker after read-back"

echo "parent=${PARENT} children=${NODES} open=0 covered=${covered} partial=${partial} missing=${missing} unrequested=${unrequested:-0} outcome=${outcome_plan}"
echo "review-comment: ${COMMENT_ID}"

# --- 6. Gaps -> new children, parent stays open ------------------------------
if [[ "${outcome_plan}" == "gaps" ]]; then
  LABELS_JSON="$(jq -c '[.labels[]?.name | select(. as $n | (["Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check","Followup"] | index($n)) | not)]' "${PARENT_JSON}")" || die "could not derive inheritable labels"
  MILESTONE_NUM="$(jq -r '.milestone.number // empty' "${PARENT_JSON}")" || die "could not read the parent milestone"
  created=()
  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    # line shape: `| <n> | <kind> | <item> | partial|missing | <proof> |`
    body="${line#|}"; body="${body%|}"
    IFS='|' read -r -a cells <<< "${body}"
    [[ "${#cells[@]}" -ge 5 ]] || continue
    n="$(sed -E 's/^[[:space:]]+|[[:space:]]+$//g' <<< "${cells[0]}")"
    [[ "${n}" =~ ^[0-9]+$ ]] || continue
    kind="$(sed -E 's/^[[:space:]]+|[[:space:]]+$//g' <<< "${cells[1]}")"
    item="$(sed -E 's/^[[:space:]]+|[[:space:]]+$//g' <<< "${cells[2]}")"
    status="$(sed -E 's/^[[:space:]]+|[[:space:]]+$//g' <<< "${cells[3]}")"
    proof="$(sed -E 's/^[[:space:]]+|[[:space:]]+$//g' <<< "${cells[4]}")"
    [[ "${status}" == "partial" || "${status}" == "missing" ]] || continue

    short="${item:0:60}"
    printf '%s' "Coverage gap: ${short} (parent #${PARENT}, item ${n})" > "${WORK}/title-${n}.txt"
    {
      printf 'Related to #%s\n\n' "${PARENT}"
      printf '### Goal\nDeliver parent #%s item %s, found **%s** by the parent-closure coverage review (review comment %s).\n\n' "${PARENT}" "${n}" "${status}" "${COMMENT_ID}"
      printf '### Acceptance Criteria\n- [ ] %s\n\n' "${item}"
      printf '### Coverage review\n- Kind: %s\n- Verdict: %s\n- Evidence found: %s\n' "${kind}" "${status}" "${proof:-none}"
    } > "${WORK}/body-${n}.md"
    if [[ -n "${MILESTONE_NUM}" ]]; then
      jq -n --rawfile title "${WORK}/title-${n}.txt" --rawfile body "${WORK}/body-${n}.md" --argjson labels "${LABELS_JSON}" --argjson milestone "${MILESTONE_NUM}" \
        '{title: ($title | rtrimstr("\n")), body: $body, labels: $labels, milestone: $milestone}' > "${WORK}/payload-${n}.json" || die "could not compose the payload for item ${n}"
    else
      jq -n --rawfile title "${WORK}/title-${n}.txt" --rawfile body "${WORK}/body-${n}.md" --argjson labels "${LABELS_JSON}" \
        '{title: ($title | rtrimstr("\n")), body: $body, labels: $labels}' > "${WORK}/payload-${n}.json" || die "could not compose the payload for item ${n}"
    fi
    NEW="$(gh api "repos/${REPO}/issues" -X POST --input "${WORK}/payload-${n}.json" --jq .number 2>"${WORK}/create-${n}.err")" || { echo "created so far: ${created[*]:-none}" >&2; die "creating the gap child for item ${n} failed: $(cat "${WORK}/create-${n}.err" 2>/dev/null); parent #${PARENT} left open"; }
    [[ "${NEW}" =~ ^[0-9]+$ ]] || { echo "created so far: ${created[*]:-none}" >&2; die "the issues API returned no numeric number for item ${n}'s gap child; parent #${PARENT} left open"; }
    if ! gh issue edit "${NEW}" --repo "${REPO}" --parent "${PARENT}" >/dev/null 2>"${WORK}/link-${n}.err"; then
      echo "created so far: ${created[*]:-none} #${NEW}" >&2
      die "linking #${NEW} as a sub-issue of #${PARENT} failed: $(cat "${WORK}/link-${n}.err" 2>/dev/null); link it by hand with: gh issue edit ${NEW} --repo ${REPO} --parent ${PARENT}"
    fi
    if ! gh issue view "${PARENT}" --repo "${REPO}" --json subIssues --jq '.subIssues.nodes[].number' 2>/dev/null | grep -qx "${NEW}"; then
      echo "created so far: ${created[*]:-none} #${NEW}" >&2
      die "#${NEW} is not present in #${PARENT}'s sub-issue list after linking -- fail closed; verify by hand"
    fi
    created+=("#${NEW}")
    echo "created: #${NEW} (item ${n}, ${status})"
  done < "${REVIEW}"
  echo "close-parent.sh: #${PARENT} left open -- ${#created[@]} new child(ren) created for partial/missing items: ${created[*]:-none}. Re-run /cenci:close ${PARENT} once they are merged." >&2
  exit 4
fi

# --- 7. Full coverage -> close ------------------------------------------------
if [[ "${PARENT_STATE}" == "CLOSED" ]]; then
  echo "close-parent.sh: #${PARENT} was already closed; labels reconciled only." >&2
else
  gh issue close "${PARENT}" --repo "${REPO}" --reason completed >/dev/null 2>"${WORK}/close.err" || die "closing #${PARENT} failed: $(cat "${WORK}/close.err" 2>/dev/null)"
fi
# Create-only self-heal: color and description come from the plugin's
# docs/board-labels.md (`## Labels` table); the drift contract test keeps this
# literal identical to the `Implemented` row. Failure is suppressed.
gh label create "Implemented" --repo "${REPO}" --color "6F42C1" --description "PR merged — done" >/dev/null 2>&1 || true
if ! gh issue edit "${PARENT}" --repo "${REPO}" --add-label "Implemented" --remove-label "In Review" >/dev/null 2>"${WORK}/label.err"; then
  gh issue edit "${PARENT}" --repo "${REPO}" --add-label "Implemented" >/dev/null 2>"${WORK}/label.err" || die "relabeling #${PARENT} failed: $(cat "${WORK}/label.err" 2>/dev/null)"
fi
FINAL_STATE="$(gh issue view "${PARENT}" --repo "${REPO}" --json state --jq .state 2>/dev/null)" || die "could not re-read #${PARENT} after closing"
[[ "${FINAL_STATE}" == "CLOSED" ]] || die "#${PARENT} does not read CLOSED after the close call (got: ${FINAL_STATE}) -- fail closed"
echo "closed: #${PARENT}"
exit 0
