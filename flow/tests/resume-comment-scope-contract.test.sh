#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #910 (lane 2, flow side):
# a resumed `Input Needed` session consumes ONLY the one authorized
# escalation-answer comment, carried as the `answer-comment=<id>` launch
# token. Covers AC6-AC11 of #910:
#   - flow/skills/implement/SKILL.md: the plan-file `$ARGUMENTS` token and its
#     regex, Plan Verification's `awaiting-input` probe (token path: single-
#     comment endpoints, no thread listing; no-token path: metadata-only
#     `--jq` listing projection, lowest-ID qualifying comment), the Answered/
#     Not answered sub-bullets, and Context Gathering's `omitComments: true`.
#   - flow/skills/implement/phases/phase-1-plan.md: `## Resume From Draft`
#     step 2 (both paths restated, body fetched once), the HS-R2 inventory
#     row, step 3's partial-answer note, step 6's verbatim `## Ticket Details`.
#   - flow/agents/context-gatherer.md: the `omitComments` input, its field
#     list, the skipped comment URL scan, and the comment-free bundle.
#
# Idiom per docs/shell-scripting-gotchas.md: single-line markers, fence-aware
# `extract_*` helpers scoped to ONE section (so a literal that already exists
# elsewhere in the same file cannot satisfy a check), pure `check_*` functions
# that print one problem per line and never call fail() (so they are safe in
# `$(...)` and can run on mutated in-memory copies), and a non-vacuity
# self-test per marker that removes it from its own section, plants it
# outside the section, and requires the check to fail. The projections are
# also executed with real jq against fixtures, so the quote-reply and
# null-body behavior is proven, not just spelled. Caveat: the fixtures run
# under the system `jq` (C jq), while production `gh --jq` evaluates the filter
# with the gojq engine embedded in `gh`; the filters use only constructs both
# engines share (`split`, `map`, `select`, `test`, `contains`, `join`, `//`),
# but a gojq-specific divergence would not show up here.
#
# Fix round 2 (#910 review): the gatherer's parent fetch asks for `state` only;
# the unauthorized-text claim is narrowed to what is guaranteed; the required
# positives of the shared rule are explicit; every `--paginate` and every
# token-path `--jq` is pinned.
#
# Fix round 3 (#910 review): step 6 copies the draft's bundle as ONE tail slice
# (first unfenced `## Ticket Details` line to EOF) straight into the candidate,
# then re-extracts the candidate and, freshly, the draft into a per-run private
# directory and `cmp`s those two; the awk exits non-zero on a missing/duplicate
# heading or a fence open at EOF; every scratch file is removed on every outcome.
# Both documented awk commands are executed against fixtures (nested fences, an
# unfenced `## ` heading inside Project Context, an unclosed fence, a duplicate
# heading, trailing-space/CRLF headings, no Project Context, no Ticket Details,
# tilde and indented fences) and, when present, the real plan file.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "resume-comment-scope-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "resume-comment-scope-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
SKILL_FILE="${FLOW_DIR}/skills/implement/SKILL.md"
PHASE1_FILE="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
GATHERER_FILE="${FLOW_DIR}/agents/context-gatherer.md"
RECONCILE_FILE="$(cd "${FLOW_DIR}/.." && pwd)/watch/docs/dispatch-reconcile.md" || { echo "resume-comment-scope-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
failures=0
WORK_DIR="$(mktemp -d)" || { echo "resume-comment-scope-contract.test.sh: mktemp -d failed." >&2; exit 2; }
trap 'rm -rf "${WORK_DIR}"' EXIT

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

if ! SKILL_DOC="$(cat "${SKILL_FILE}")"; then fail "SKILL.md unreadable: ${SKILL_FILE}"; SKILL_DOC=""; fi
if ! PHASE1_DOC="$(cat "${PHASE1_FILE}")"; then fail "phase-1-plan.md unreadable: ${PHASE1_FILE}"; PHASE1_DOC=""; fi
if ! GATHERER_DOC="$(cat "${GATHERER_FILE}")"; then fail "context-gatherer.md unreadable: ${GATHERER_FILE}"; GATHERER_DOC=""; fi
if ! RECONCILE_DOC="$(cat "${RECONCILE_FILE}")"; then fail "dispatch-reconcile.md unreadable: ${RECONCILE_FILE}"; RECONCILE_DOC=""; fi
if ! command -v jq >/dev/null 2>&1; then fail "jq is required to execute the projection fixtures"; fi

# ---------------------------------------------------------------------
# Load-bearing literals (each is a single source line in the docs).
# ---------------------------------------------------------------------
SQ="'"
NL=$'\n'
TOKEN_LITERAL='`answer-comment=<id>`'
TOKEN_REGEX='`^[1-9][0-9]{0,18}$`'
LISTING_JQ='.[] | ((.body // "") | split("\n") | map(select(test("^[ \t]*>") | not)) | join("\n")) as $s | {id, login: .user.login, type: .user.type, association: .author_association, cenciMarker: ($s | contains("<!-- cenci-")), anchorMarker: ($s | contains("<!-- cenci-planner-escalation:<escalationNonce> -->"))}'
SINGLE_JQ='((.body // "") | split("\n") | map(select(test("^[ \t]*>") | not)) | join("\n")) as $s | {id, login: .user.login, type: .user.type, association: .author_association, cenciMarker: ($s | contains("<!-- cenci-")), anchorMarker: ($s | contains("<!-- cenci-planner-escalation:<escalationNonce> -->")), issueUrl: .issue_url}'
LISTING_CALL="gh api \"repos/<owner>/<repo>/issues/<number>/comments?per_page=100\" --paginate --jq ${SQ}${LISTING_JQ}${SQ}"
ANCHOR_CALL="gh api repos/<owner>/<repo>/issues/comments/<escalationCommentId> --jq ${SQ}${SINGLE_JQ}${SQ}"
CARRIED_CALL="gh api repos/<owner>/<repo>/issues/comments/<answerCommentId> --jq ${SQ}${SINGLE_JQ}${SQ}"
BODY_CALL='gh api repos/<owner>/<repo>/issues/comments/<answerCommentId> --jq .body'
NO_LISTING='never runs the paginated thread listing'
ISSUE_URL_RULE='`issueUrl` must end in `/repos/<owner>/<repo>/issues/<number>` (owner and repo compared case-insensitively)'
ID_EQUALS='`id` equals `answerCommentId`'
ID_ABOVE='`id` is greater than `escalationCommentId`'
LOWEST='lowest-`id` qualifying comment'
NOT_FOUND='a 404 (deleted comment)'
OLD_PHRASE='most recent qualifying comment'
OMIT_CMD='gh issue view <number> --repo <owner>/<repo> --json number,title,body,labels,state,assignees,milestone,author'
DEFAULT_CMD='gh issue view <number> --repo <owner>/<repo> --json number,title,body,labels,state,assignees,milestone,comments,author'
# Token-path verification failures route through the Restore recovery (never Not answered).
RESTORE_HEAD='`## Restore Awaiting-Input State`'
RESTORE_AWAIT='`cenci pipeline await-input <id>`'
RESTORE_LABEL='`cenci pipeline label <id> --transition input-needed`'
TRIG_WRONG_ISSUE='a comment on a different issue (its `issueUrl` names another issue)'
TRIG_ID='an `id` not above `escalationCommentId` or not equal to `answerCommentId`'
TRIG_PERM='a failed current `admin`/`write` permission re-check'
TOKEN_ROUTE='takes none of the sub-branches below and is never **Not answered**'
TOKEN_RESTORE_ROUTE='routes through `## Restore Awaiting-Input State`'
MALFORMED_ROUTE='take the **Not answered** sub-branch below with no `gh` call at all'
OLD_TOKEN_ROUTE='These fail closed as **Not answered**'
# Fix round 2: required positives of the shared rule, restated verbatim at the
# SKILL.md probe and at step 2.
REQ_POSITIVES='**Required positives**: the answer comment'"'"'s projection must parse as one JSON object that carries `cenciMarker` equal to `false` (a boolean, never absent, `null` or a string) and non-null string values for `login`, `type` and `association`; a missing or `null` key, empty output, or unparseable output is never a pass by absence.'
TRIG_PROJ='a projection result that does not parse or lacks a required positive (`cenciMarker` equal to `false`, non-null string `login`, `type` and `association`)'
TRIG_BODY='an empty or `null` `--jq .body` result'
NOTOKEN_POSITIVES='an entry lacking a required positive does not qualify as an answer, and an empty or unparseable listing counts as a `gh` failure'
STEP2_POSITIVES_ROUTE='a projection that fails a required positive, an empty or `null` `--jq .body` result, or an empty or unparseable listing routes through the **Recovery** paragraph below'
# Fix round 2: the unauthorized-text claim is narrowed to what is guaranteed.
NARROWED_CLAIM='no comment posted after the escalation anchor other than the one authorized answer reaches the resumed session or the finalized plan'
CARRIED_CLAIM='comments already captured in the draft'"'"'s `## Ticket Details` when the draft was created depend on the planning trust gate (#847) or on a human being present at that time'
BROAD_CLAIM='no unauthorized commenter'"'"'s text reaches'
# Fix round 2: the gatherer's parent fetch asks for `state` only.
PARENT_CMD='gh issue view <parentId> --repo <owner>/<repo> --json state'
PARENT_PROSE='Fetch the parent'"'"'s `state` only, in both modes'
OLD_PARENT_PROSE='Fetch the parent with the same `gh issue view` command'
# Fix round 2: step 6 copies the draft'"'"'s sections mechanically and byte-compares them.
BT='`'
# Fix round 3 (#910 review): step 6 copies ONE tail slice (first unfenced
# `## Ticket Details` line to end of file) with an awk that exits non-zero on a
# missing/duplicate heading or a fence open at EOF; the slice is appended straight
# from the draft into the candidate; the verification re-extracts the candidate AND
# the draft into a per-run private directory and `cmp`s them; scratch files are
# removed on every outcome.
AWK_PROG='BEGIN { bad = 1 } { t = $0; sub(/^ +/, "", t); ch = substr(t, 1, 1); n = 0; while (n < length(t) && substr(t, n + 1, 1) == ch) n++; if ((ch == bt || ch == "~") && n >= 3) { if (!fence) { fence = 1; fch = ch; fn = n } else if (ch == fch && n >= fn && substr(t, n + 1) ~ /^[ \t\r]*$/) fence = 0 } if (!fence && $0 ~ /^## Ticket Details[ \t\r]*$/) { if (seen) bad = 1; else bad = 0; seen = 1; on = 1 } if (on) print } END { exit (bad || fence) }'
END_EXIT='END { exit (bad || fence) }'
CANDIDATE_REL='<repo-root>/.plans/.<id>-<slug>.candidate.md'
AWK_COPY="awk -v bt=${SQ}${BT}${SQ} ${SQ}${AWK_PROG}${SQ} \"<draft>\" >> \"${CANDIDATE_REL}\""
AWK_VERIFY="awk -v bt=${SQ}${BT}${SQ} ${SQ}${AWK_PROG}${SQ} \"<source>\" > \"<out>\""
DRAFT_TAIL='<run-dir>/draft-tail.md'
CAND_TAIL='<run-dir>/candidate-tail.md'
CMP_CMD="cmp \"${DRAFT_TAIL}\" \"${CAND_TAIL}\""
TEST_S_DRAFT="test -s \"${DRAFT_TAIL}\""
TEST_S_CAND="test -s \"${CAND_TAIL}\""
RM_CMD="rm -f \"${DRAFT_TAIL}\" \"${CAND_TAIL}\""
S6_DIR_CALL='run `flow/skills/implement/scripts/run-artifact-dir.sh` as its own call'
S6_DIR_VERIFY='verify the script exited zero and that `<run-dir>` is a non-empty, existing directory'
S6_NO_SHARED='none is ever written to the shared `${TMPDIR:-/tmp}/cenci/` directory'
S6_TAIL='from the first unfenced `## Ticket Details` line to the end of the draft'
S6_PC_CARRIED='Because the slice runs to the end of the draft, a `## Project Context` that follows `## Ticket Details` is carried with it'
S6_AWK_EXITS='exits non-zero on a missing `## Ticket Details` heading, a second unfenced `## Ticket Details` heading, or a fence still open at the end of the draft'
S6_DIRECT='appends the slice straight from the draft into the candidate with its own redirect'
S6_AGREE='the three-heading check and the candidate re-extraction must agree'
S6_ORDER='no `mv` runs until `cmp` has exited 0'
S6_EVERY='On every outcome'
S6_ALL_STOPS='an awk non-zero exit, a `test -s` failure, a disagreement between the three-heading check and the candidate re-extraction, and a `cmp` exit of 1 (mismatch) or 2 (error) are each a hard stop through `## Restore Awaiting-Input State` naming the draft as the implicated input'
HSR12_RM='run the same `rm -f` call again'
MV_CMD='mv "<repo-root>/.plans/.<id>-<slug>.candidate.md" "<repo-root>/.plans/<id>-<slug>.md"'
S6_NO_BUNDLE='never use the context bundle on this path'
S6_MECHANICAL='never re-typed through the model'
S6_NO_PC='never substitute the context bundle'"'"'s `## Project Context`'
S6_NO_TD='a draft without a `## Ticket Details` section is a hard stop through `## Restore Awaiting-Input State`, and the implicated input is the draft, not the context bundle or the planner sections'
# Retired round-2 per-section prose; none may survive anywhere in step 6.
S6_RETIRED=('draft-ticket-details' 'draft-project-context' 'candidate-project-context' 'cenci-resume-' '-v want=' '<draft-section-file>' '<candidate-section-file>' 'Project Context` is optional' 'skips its append and its `cmp` below' 'only when the Project Context scratch file is non-empty' 'mkdir -p "${TMPDIR:-/tmp}/cenci"' 'leave the candidate file and the scratch files on disk' 'before the `mv`:' 'each copied section against')
S6_OLD_WRITE_CARRIES='verbatim from the draft already read in step 1'
S6_OLD_IMPLICATED='context bundle vs. planner sections'
PERSIST_DRAFT_INPUT='On `## Resume From Draft`'"'"'s path (step 6) the implicated input for a missing `## Ticket Details` is the draft, never the context bundle'
HSR11_THREE='three-heading validation'
HSR11_COPY='the mechanical tail-slice copy of the draft'"'"'s `## Ticket Details` (and any `## Project Context` after it)'
HSR11_OLD='four-heading'

# ---------------------------------------------------------------------
# Pure extractors (fence-aware; no fail(); safe inside $(...)).
# ---------------------------------------------------------------------

# extract_h2_section <content> <exact heading line>
extract_h2_section() {
  H="$2" awk '
    $0 == ENVIRON["H"] && !on { on=1; print; next }
    /^[[:space:]]*(```|~~~)/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_awaiting_input_branch <skill content> -- the awaiting-input bullet
# through (not including) the next top-level Plan Verification bullet.
extract_awaiting_input_branch() {
  awk '
    /^- \*\*`awaiting-input`\*\*/ && !on { on=1; print; next }
    /^[[:space:]]*(```|~~~)/ { infence = !infence; if (on) print; next }
    on && !infence && /^- \*\*Unrecognized/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_probe_region <branch> -- the probe prose before the first sub-bullet.
extract_probe_region() {
  awk '/^  - \*\*/ { exit } { print }' <<<"$1"
}

# extract_subbullet_heads <branch> -- one bold sub-bullet head per line.
extract_subbullet_heads() {
  grep -o '^  - \*\*[^*]*\*\*' <<<"$1"
}

# extract_subbullet <branch> <name>
extract_subbullet() {
  N="$2" awk '
    index($0, "  - **" ENVIRON["N"] "**") == 1 && !on { on=1; print; next }
    on && /^  - \*\*/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_args_planfile_bullet <skill content> -- the plan-file-mode bullet of
# the `$ARGUMENTS` mode detection.
extract_args_planfile_bullet() {
  awk '
    index($0, "- **If the first token ends in `.md`") == 1 && !on { on=1; print; next }
    on && /^- \*\*Otherwise/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_resume_step <resume section> <n> -- one numbered step, fence-aware.
extract_resume_step() {
  N="$2" awk '
    /^[[:space:]]*(```|~~~)/ { infence = !infence; if (on) print; next }
    !infence && !on && index($0, ENVIRON["N"] ". **") == 1 { on=1; print; next }
    on && !infence && /^[0-9]+\. \*\*/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_hs_row <inventory section> <id>
extract_hs_row() {
  grep -m1 -F -- "| $2 |" <<<"$1"
}

# extract_gatherer_step <gatherer content> <n> -- "### <n>. ..." section.
extract_gatherer_step() {
  N="$2" awk '
    index($0, "### " ENVIRON["N"] ". ") == 1 && !on { on=1; print; next }
    /^[[:space:]]*(```|~~~)/ { infence = !infence; if (on) print; next }
    on && !infence && /^##+ / { exit }
    on { print }
  ' <<<"$1"
}

# extract_gatherer_inputs <gatherer content>
extract_gatherer_inputs() {
  awk '
    index($0, "## Inputs") == 1 && !on { on=1; print; next }
    /^[[:space:]]*(```|~~~)/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_jq_filter <content> <prefix ending in --jq '> -- the filter text of
# the first line carrying the prefix, up to the closing quote (the filters
# contain no single quote).
extract_jq_filter() {
  local line rest
  line="$(grep -m1 -F -- "$2" <<<"$1")" || return 0
  rest="${line#*"$2"}"
  printf '%s' "${rest%%\'*}"
}

# Section wrappers used by checks and self-tests alike.
sec_skill_args() { extract_args_planfile_bullet "$1"; }
sec_skill_branch() { extract_awaiting_input_branch "$1"; }
sec_skill_probe() { extract_probe_region "$(extract_awaiting_input_branch "$1")"; }
sec_skill_answered() { extract_subbullet "$(extract_awaiting_input_branch "$1")" "Answered"; }
sec_skill_notanswered() { extract_subbullet "$(extract_awaiting_input_branch "$1")" "Not answered"; }
sec_skill_context() { extract_h2_section "$1" "## Context Gathering (Delegated)"; }
sec_resume() { extract_h2_section "$1" "## Resume From Draft"; }
sec_step() { extract_resume_step "$(sec_resume "$2")" "$1"; }
sec_step2() { sec_step 2 "$1"; }
sec_step3() { sec_step 3 "$1"; }
sec_step6() { sec_step 6 "$1"; }
sec_hsrow() { extract_hs_row "$(extract_h2_section "$1" "## Hard-Stop Inventory")" "HS-R2"; }
sec_g_inputs() { extract_gatherer_inputs "$1"; }
sec_g_step1() { extract_gatherer_step "$1" 1; }
sec_g_step3() { extract_gatherer_step "$1" 3; }
sec_g_step6() { extract_gatherer_step "$1" 6; }

# extract_token_path <skill content> -- the probe's "**Token path**" paragraph
# only (through, not including, the "**No-token path**" paragraph).
extract_token_path() {
  awk '
    index($0, "  **Token path**") == 1 && !on { on=1; print; next }
    on && index($0, "  **No-token path**") == 1 { exit }
    on { print }
  ' <<<"$(extract_probe_region "$(extract_awaiting_input_branch "$1")")"
}

# extract_step2_recovery <step 2 content> -- the step's "**Recovery**" paragraph.
extract_step2_recovery() {
  awk '
    index($0, "   **Recovery**") == 1 && !on { on=1; print; next }
    on { print }
  ' <<<"$1"
}

# extract_reconcile_section <dispatch-reconcile content>
extract_reconcile_section() { extract_h2_section "$1" "## Resume answer-comment scoping (#910)"; }

# extract_reconcile_bullet <section> <bullet-lead> -- one two-space-indented
# bullet of the flow-side re-check, by its leading text.
extract_reconcile_bullet() {
  N="$2" awk '
    index($0, "  - " ENVIRON["N"]) == 1 && !on { on=1; print; next }
    on && /^  - / { exit }
    on && /^- / { exit }
    on { print }
  ' <<<"$1"
}

# extract_step2_token_path <step 2 content> -- step 2's "**Token path**"
# paragraph only (through, not including, the "**No-token path**" paragraph).
extract_step2_token_path() {
  awk '
    index($0, "   **Token path**") == 1 && !on { on=1; print; next }
    on && index($0, "   **No-token path**") == 1 { exit }
    on { print }
  ' <<<"$1"
}

sec_g_step2() { extract_gatherer_step "$1" 2; }
sec_persist() { extract_h2_section "$1" "## Persist the Plan"; }
sec_hsrow11() { extract_hs_row "$(extract_h2_section "$1" "## Hard-Stop Inventory")" "HS-R11"; }
sec_step2_tokenpath() { extract_step2_token_path "$(sec_step2 "$1")"; }
sec_skill_tokenpath() { extract_token_path "$1"; }
sec_step2_recovery() { extract_step2_recovery "$(sec_step2 "$1")"; }
sec_reconcile() { extract_reconcile_section "$1"; }
sec_reconcile_token() { extract_reconcile_bullet "$(sec_reconcile "$1")" "With a valid token"; }
sec_reconcile_malformed() { extract_reconcile_bullet "$(sec_reconcile "$1")" "A malformed token"; }
sec_reconcile_notoken() { extract_reconcile_bullet "$(sec_reconcile "$1")" "With no token"; }

# ---------------------------------------------------------------------
# Pure problem printers.
# ---------------------------------------------------------------------
problems_missing() {
  local content="$1" needle; shift
  for needle in "$@"; do
    if [[ -z "${needle}" ]]; then echo "empty needle"
    elif [[ "${content}" != *"${needle}"* ]]; then echo "missing: [${needle}]"
    fi
  done
}
problems_forbidden() {
  local content="$1" needle; shift
  for needle in "$@"; do
    if [[ -z "${needle}" ]]; then echo "empty needle"
    elif [[ "${content}" == *"${needle}"* ]]; then echo "forbidden text present: [${needle}]"
    fi
  done
}
# check_section <section-fn> <doc> <label> <needle>... -- section must be
# non-empty and contain every needle.
check_section() {
  local sec_fn="$1" doc="$2" label="$3" section; shift 3
  section="$("${sec_fn}" "${doc}")"
  if [[ -z "${section}" ]]; then echo "${label}: section not found"; return 0; fi
  problems_missing "${section}" "$@"
}

# Needles shared by the two probe sites (SKILL.md probe, step 2).
PATH_NEEDLES=("${ANCHOR_CALL}" "${CARRIED_CALL}" "${LISTING_CALL}" "${NO_LISTING}" "${ISSUE_URL_RULE}" "${ID_EQUALS}" "${ID_ABOVE}" "${LOWEST}" "${NOT_FOUND}")
SKILL_PROBE_NEEDLES=("${PATH_NEEDLES[@]}" '**Token path**' '**No-token path**' 'no `gh` call at all' '`answerCommentId` is a pointer, never authorization' "${REQ_POSITIVES}" "${NOTOKEN_POSITIVES}")
STEP2_NEEDLES=("${PATH_NEEDLES[@]}" "${BODY_CALL}" 're-check the blockquote-stripped body for a `<!-- cenci-` marker' 'the answer text is exactly the selected comment'"'"'s body' "${TRIG_ID}" "${REQ_POSITIVES}" "${STEP2_POSITIVES_ROUTE}")
ARGS_NEEDLES=("${TOKEN_LITERAL}" "${TOKEN_REGEX}" '`answerCommentToken`' 'after the file path is ignored, with one exception' '`absent`' '`valid`' '`invalid`' 'two or more `answer-comment=` tokens' 'Only Plan Verification'"'"'s `awaiting-input` branch reads it')
ANSWERED_NEEDLES=('record `answerCommentId`')
NOT_ANSWERED_NEEDLES=('when `answerCommentToken` is `invalid`' 'report the token as invalid')
CONTEXT_NEEDLES=('`omitComments: true` whenever `resumeFromDraft` is set' "${NARROWED_CLAIM}" "${CARRIED_CLAIM}")
HSROW_NEEDLES=('single-comment `gh api` fetch')
HSR11_NEEDLES=("${HSR11_THREE}" "${HSR11_COPY}")
PERSIST_NEEDLES=("${PERSIST_DRAFT_INPUT}" "${S6_OLD_IMPLICATED}")
STEP3_NEEDLES=('no second comment is merged in')
STEP6_NEEDLES=('the finalized plan'"'"'s `## Ticket Details` section is copied verbatim from the draft rather than from the fresh context bundle' "${S6_NO_BUNDLE}" "${S6_MECHANICAL}" 'skip the context-bundle `cat`-append' 'The three-heading check still runs against the candidate file' "${AWK_COPY}" "${AWK_VERIFY}" "${END_EXIT}" "${CMP_CMD}" "${TEST_S_DRAFT}" "${TEST_S_CAND}" "${RM_CMD}" "${S6_DIR_CALL}" "${S6_DIR_VERIFY}" "${S6_NO_SHARED}" "${S6_TAIL}" "${S6_PC_CARRIED}" "${S6_AWK_EXITS}" "${S6_DIRECT}" "${S6_AGREE}" "${S6_ORDER}" "${S6_EVERY}" "${S6_ALL_STOPS}" "${HSR12_RM}" "${MV_CMD}" "${S6_NO_PC}" "${S6_NO_TD}" "${CARRIED_CLAIM}")
G_STEP2_NEEDLES=("${PARENT_PROSE}" "${PARENT_CMD}")
G_INPUTS_NEEDLES=('Optional `omitComments`')
G_STEP1_NEEDLES=('When `omitComments` is set')
G_STEP3_NEEDLES=('When `omitComments` is set, skip the comment URL scan')
G_STEP6_NEEDLES=('When `omitComments` is set, `## Ticket Details` holds no comment text')
# Fix round 1 (#910 review rows 17-20): the four token-path verification
# failures route through the Restore recovery at BOTH sites, never Not answered.
TRIGGER_NEEDLES=("${TRIG_WRONG_ISSUE}" "${TRIG_ID}" "${NOT_FOUND}" "${TRIG_PERM}" "${TRIG_PROJ}")
TOKENPATH_NEEDLES=("${TRIGGER_NEEDLES[@]}" "${RESTORE_HEAD}" "${RESTORE_AWAIT}" "${RESTORE_LABEL}" "${TOKEN_ROUTE}" "${TOKEN_RESTORE_ROUTE}" 'draft-validity check' 'a harmless no-op' 'verify both outcomes' 'stop and report')
STEP2_RECOVERY_NEEDLES=("${TRIGGER_NEEDLES[@]}" "${TRIG_BODY}" "${RESTORE_HEAD}" "${RESTORE_AWAIT}" "${RESTORE_LABEL}")
RECONCILE_CLAIM_NEEDLES=('depend on the planning trust gate (#847)')
MALFORMED_NEEDLES=("${MALFORMED_ROUTE}")
NOQUALIFYING_NEEDLES=('no qualifying comment exists after it')
RECONCILE_TOKEN_NEEDLES=("${RESTORE_HEAD}" 'at Plan Verification and at step 2')
RECONCILE_MALFORMED_NEEDLES=('**Not answered**')
RECONCILE_NOTOKEN_NEEDLES=('**Not answered**' 'no qualifying comment')

# ---------------------------------------------------------------------
# Checks: take a full doc, print one problem per line.
# ---------------------------------------------------------------------
check_skill_args() {
  local section
  section="$(sec_skill_args "$1")"
  if [[ -z "${section}" ]]; then echo "plan-file mode bullet not found"; return 0; fi
  problems_missing "${section}" "${ARGS_NEEDLES[@]}"
  problems_forbidden "${section}" 'after the file path is ignored.'
}
# count_of <content> <literal> -- occurrences (not lines) of a fixed string.
count_of() {
  local n
  n="$(grep -oF -- "$2" <<<"$1" | wc -l | tr -d '[:space:]')"
  printf '%s' "${n:-0}"
}
# problems_paginate <section> -- every --paginate carries a --jq projection,
# and every thread-listing URL is the exact projected listing call (so no bare
# listing can hide next to the real one).
problems_paginate() {
  local section="$1" pag withjq urls calls
  pag="$(count_of "${section}" '--paginate')"
  withjq="$(count_of "${section}" "--paginate --jq ${SQ}")"
  if [[ "${pag}" -lt 1 || "${pag}" -ne "${withjq}" ]]; then
    echo "every --paginate listing must carry a --jq projection (paginate=${pag}, with --jq=${withjq})"
  fi
  urls="$(count_of "${section}" '/comments?per_page')"
  calls="$(count_of "${section}" "${LISTING_CALL}")"
  if [[ "${urls}" -lt 1 || "${urls}" -ne "${calls}" ]]; then
    echo "every thread listing must be the exact projected listing call, no bare listing (listing urls=${urls}, projected calls=${calls})"
  fi
}
# problems_tokenpath_hygiene <token-path paragraph> -- on the token path no
# thread listing at all, and every --jq is exactly the single-comment projection
# (two of them: anchor and carried comment), so none can emit a comment body.
problems_tokenpath_hygiene() {
  local section="$1" jqn single
  problems_forbidden "${section}" '/comments?per_page' '--jq .body' "--jq ${SQ}.body" 'body:' '{id, body}'
  jqn="$(count_of "${section}" '--jq')"
  single="$(count_of "${section}" "--jq ${SQ}${SINGLE_JQ}${SQ}")"
  if [[ "${jqn}" -ne 2 || "${single}" -ne 2 ]]; then
    echo "the token path must carry exactly two --jq filters, both the single-comment projection (--jq=${jqn}, projection=${single})"
  fi
}
check_skill_probe() {
  local section
  section="$(sec_skill_probe "$1")"
  if [[ -z "${section}" ]]; then echo "awaiting-input probe region not found"; return 0; fi
  problems_missing "${section}" "${SKILL_PROBE_NEEDLES[@]}"
  problems_forbidden "${section}" "${OLD_PHRASE}" '--jq .body' "--jq ${SQ}.body"
  problems_paginate "${section}"
}
EXPECTED_HEADS=$'  - **Answered**\n  - **Not answered**\n  - **Anchor incomplete or missing**'
check_skill_subbullets() {
  local heads
  heads="$(extract_subbullet_heads "$(sec_skill_branch "$1")")"
  [[ "${heads}" == "${EXPECTED_HEADS}" ]] || echo "awaiting-input sub-bullets must stay exactly Answered, Not answered, Anchor incomplete or missing (found: ${heads//${NL}/ | })"
}
check_skill_answered() { check_section sec_skill_answered "$1" "Answered sub-bullet" "${ANSWERED_NEEDLES[@]}"; }
check_skill_notanswered() { check_section sec_skill_notanswered "$1" "Not answered sub-bullet" "${NOT_ANSWERED_NEEDLES[@]}"; }
check_skill_context() {
  local section
  section="$(sec_skill_context "$1")"
  if [[ -z "${section}" ]]; then echo "Context Gathering: section not found"; return 0; fi
  problems_missing "${section}" "${CONTEXT_NEEDLES[@]}"
  problems_forbidden "${section}" "${BROAD_CLAIM}"
}
check_skill_old_phrase() { problems_forbidden "$1" "${OLD_PHRASE}"; }
check_phase_old_phrase() { problems_forbidden "$1" "${OLD_PHRASE}"; }
check_skill_tokenpath() {
  local section
  section="$(sec_skill_tokenpath "$1")"
  if [[ -z "${section}" ]]; then echo "token-path paragraph not found"; return 0; fi
  problems_missing "${section}" "${TOKENPATH_NEEDLES[@]}"
  problems_forbidden "${section}" "${OLD_TOKEN_ROUTE}"
  problems_tokenpath_hygiene "${section}"
}
check_skill_malformed() { check_section sec_skill_probe "$1" "awaiting-input probe (malformed token)" "${MALFORMED_NEEDLES[@]}"; }
check_skill_noqualifying() { check_section sec_skill_notanswered "$1" "Not answered sub-bullet (no qualifying comment)" "${NOQUALIFYING_NEEDLES[@]}"; }
check_phase_step2_recovery() {
  local section
  section="$(sec_step2_recovery "$1")"
  if [[ -z "${section}" ]]; then echo "step 2 Recovery paragraph not found"; return 0; fi
  problems_missing "${section}" "${STEP2_RECOVERY_NEEDLES[@]}"
}
# dispatch-reconcile.md: token-path verification failures go through Restore
# (at Plan Verification and at step 2); only the malformed token and the
# no-qualifying-comment case go through Not answered.
check_reconcile() {
  local doc="$1" tok
  tok="$(sec_reconcile_token "${doc}")"
  if [[ -z "${tok}" ]]; then echo "reconcile valid-token bullet not found"; else
    problems_missing "${tok}" "${RECONCILE_TOKEN_NEEDLES[@]}"
    problems_forbidden "${tok}" '**Not answered**'
  fi
  check_section sec_reconcile_malformed "${doc}" "reconcile malformed-token bullet" "${RECONCILE_MALFORMED_NEEDLES[@]}"
  check_section sec_reconcile_notoken "${doc}" "reconcile no-token bullet" "${RECONCILE_NOTOKEN_NEEDLES[@]}"
  check_section sec_reconcile "${doc}" "reconcile context-bundle claim" "${RECONCILE_CLAIM_NEEDLES[@]}"
}
check_phase_step2() {
  local section tokenpath
  section="$(sec_step2 "$1")"
  if [[ -z "${section}" ]]; then echo "step 2 not found"; return 0; fi
  problems_missing "${section}" "${STEP2_NEEDLES[@]}"
  problems_forbidden "${section}" "${OLD_PHRASE}"
  problems_paginate "${section}"
  tokenpath="$(sec_step2_tokenpath "$1")"
  if [[ -z "${tokenpath}" ]]; then echo "step 2 token-path paragraph not found"; return 0; fi
  problems_tokenpath_hygiene "${tokenpath}"
}
check_phase_hsrow() { check_section sec_hsrow "$1" "HS-R2 row" "${HSROW_NEEDLES[@]}"; }
check_phase_hsr11() {
  local section
  section="$(sec_hsrow11 "$1")"
  if [[ -z "${section}" ]]; then echo "HS-R11 row not found"; return 0; fi
  problems_missing "${section}" "${HSR11_NEEDLES[@]}"
  problems_forbidden "${section}" "${HSR11_OLD}"
}
check_phase_persist() { check_section sec_persist "$1" "Persist the Plan step 3 hard stop" "${PERSIST_NEEDLES[@]}"; }
check_phase_step3() { check_section sec_step3 "$1" "step 3" "${STEP3_NEEDLES[@]}"; }
check_phase_step6() {
  local section
  section="$(sec_step6 "$1")"
  if [[ -z "${section}" ]]; then echo "step 6 not found"; return 0; fi
  problems_missing "${section}" "${STEP6_NEEDLES[@]}"
  problems_forbidden "${section}" '(`Write` then the context-bundle `cat`-append)' "${S6_OLD_WRITE_CARRIES}" "${S6_OLD_IMPLICATED}" "${S6_RETIRED[@]}"
  problems_order "${section}" "${S6_DIR_CALL}" "${AWK_COPY}" "${AWK_VERIFY}" "${TEST_S_DRAFT}" "${TEST_S_CAND}" "${S6_AGREE}" "${CMP_CMD}" "${RM_CMD}" "${S6_ORDER}" "${MV_CMD}"
  # The ordering clause is unique in the section (a phrase that also appears
  # elsewhere could satisfy the check vacuously), and so is the cmp command.
  if [[ "$(count_of "${section}" "${S6_ORDER}")" -ne 1 ]]; then echo "the 'mv only after cmp' ordering clause must appear exactly once in step 6"; fi
  if [[ "$(count_of "${section}" "${CMP_CMD}")" -ne 1 ]]; then echo "the cmp command must appear exactly once in step 6"; fi
  if [[ "$(count_of "${section}" "${AWK_PROG}")" -ne 2 ]]; then echo "the tail-slice awk program must appear in exactly two commands (the direct copy and the re-extraction), byte for byte"; fi
}
# problems_order <section> <needle>... -- the first occurrence of each needle
# must come strictly after the previous one's (a needle that is absent is
# reported by problems_missing, not here).
problems_order() {
  local section="$1" needle prefix prev=-1 off; shift
  for needle in "$@"; do
    [[ "${section}" == *"${needle}"* ]] || continue
    prefix="${section%%"${needle}"*}"; off="${#prefix}"
    if [[ "${off}" -le "${prev}" ]]; then echo "step 6 order violated: [${needle}] must come after the previous marker"; fi
    prev="${off}"
  done
}
# step6_cmd_line <step 6 section> <fixed text> -- the first documented command
# line that starts with `awk -v bt=` and carries the fixed text, left-trimmed.
step6_cmd_line() {
  T="$2" awk 'index($0, ENVIRON["T"]) && $0 ~ /^[ \t]*awk -v bt=/ { sub(/^[ \t]+/, ""); print; exit }' <<<"$1"
}
# Real plan file for the on-disk run (gitignored, so absent in CI): the repo
# checkout's own .plans/, or the main checkout's when this is a worktree.
REAL_PLAN_NAME='910-scope-resumed-session-answer-comment.md'
REAL_PLAN_CANDIDATES=("$(cd "${FLOW_DIR}/.." && pwd)/.plans/${REAL_PLAN_NAME}" "$(cd "${FLOW_DIR}/../../.." && pwd)/.plans/${REAL_PLAN_NAME}")
DRAFT_TOKEN='"<draft>"'
CAND_TOKEN="\"${CANDIDATE_REL}\""
SRC_TOKEN='"<source>"'
OUT_TOKEN='"<out>"'
# check_awk_extract <step 6 section> -- executes BOTH documented tail-slice awk
# commands (the direct `>>` copy and the `> "<out>"` re-extraction) against
# fixtures: nested fences, an unfenced `## ` heading inside Project Context
# (the slice must keep the whole tail), an unclosed fence at EOF, a duplicate
# heading, trailing-space / CRLF headings, no Project Context, no Ticket
# Details, tilde fences, and the real plan file when it exists. It then runs the
# documented verification flow (fresh draft slice vs candidate slice, `cmp`)
# against a good, a truncated and a malformed candidate.
check_awk_extract() {
  local section="$1" copy_line verify_line out cand f rc first plan
  if [[ -z "${section}" ]]; then echo "step 6 not found (awk extraction)"; return 0; fi
  copy_line="$(step6_cmd_line "${section}" ">> \"${CANDIDATE_REL}\"")"
  verify_line="$(step6_cmd_line "${section}" '> "<out>"')"
  if [[ -z "${copy_line}" ]]; then echo "the documented direct-copy awk (>> candidate) was not found in step 6"; return 0; fi
  if [[ -z "${verify_line}" ]]; then echo "the documented re-extraction awk (> <out>) was not found in step 6"; return 0; fi
  out="${WORK_DIR}/out.md"; cand="${WORK_DIR}/cand.md"
  run_copy() { # <draft> <candidate> -- the documented copy command, appending
    local a="\"$1\"" b="\"$2\"" c
    c="${copy_line//"${DRAFT_TOKEN}"/"${a}"}"; c="${c//"${CAND_TOKEN}"/"${b}"}"
    bash -c "${c}"
  }
  run_verify() { # <source> <out> -- the documented re-extraction command
    local a="\"$1\"" b="\"$2\"" c
    c="${verify_line//"${SRC_TOKEN}"/"${a}"}"; c="${c//"${OUT_TOKEN}"/"${b}"}"
    bash -c "${c}"
  }
  expect_tail() { # <label> <fixture> <expected tail file>
    if ! run_verify "$2" "${out}"; then echo "$1: the re-extraction exited non-zero on a well-formed draft"; fi
    cmp -s "${out}" "$3" || echo "$1: the re-extraction is not the whole tail from the real Ticket Details heading: $(tr '\n' '|' <"${out}")"
    printf '%s\n' '## User Context' 'x' > "${cand}" || { echo "$1: fixture write failed"; return 0; }
    if ! run_copy "$2" "${cand}"; then echo "$1: the direct copy exited non-zero on a well-formed draft"; fi
    if ! run_verify "${cand}" "${out}"; then echo "$1: the candidate re-extraction exited non-zero after the copy"; fi
    cmp -s "${out}" "$3" || echo "$1: the copied tail differs from the draft's tail: $(tr '\n' '|' <"${out}")"
  }
  expect_failure() { # <label> <fixture>
    if run_verify "$2" "${out}"; then echo "$1: the re-extraction must exit non-zero"; fi
    printf '%s\n' '## User Context' 'x' > "${cand}" || { echo "$1: fixture write failed"; return 0; }
    if run_copy "$2" "${cand}"; then echo "$1: the direct copy must exit non-zero"; fi
  }
  # 1. Nested fences (longer outer around shorter inner, a fenced `## Ticket
  #    Details` example before the real heading and inside the outer fence) and an
  #    UNFENCED `## ` heading inside Project Context: the slice is the whole tail.
  f="${WORK_DIR}/f-nested.md"
  printf '%s\n' '## User Context' '```text' '## Ticket Details' '```' '## Q&A from Planning' 'none' '## Ticket Details' 'td 1' '````text' '## fenced heading in the body' '```bash' 'inner' '```' '## Ticket Details' '````' 'td tail' '## Project Context' 'pc 1' '## Unfenced heading inside project context' 'pc tail' > "${f}" || { echo "fixture write failed"; return 0; }
  printf '%s\n' '## Ticket Details' 'td 1' '````text' '## fenced heading in the body' '```bash' 'inner' '```' '## Ticket Details' '````' 'td tail' '## Project Context' 'pc 1' '## Unfenced heading inside project context' 'pc tail' > "${f}.want" || { echo "fixture write failed"; return 0; }
  expect_tail "nested fences + unfenced heading in Project Context" "${f}" "${f}.want"
  # 2. A fence still open at EOF.
  f="${WORK_DIR}/f-unclosed.md"
  printf '%s\n' '## Ticket Details' 'td' '```bash' 'never closed' > "${f}" || { echo "fixture write failed"; return 0; }
  expect_failure "unclosed fence at EOF" "${f}"
  # 3. A duplicate unfenced heading.
  f="${WORK_DIR}/f-dup.md"
  printf '%s\n' '## Ticket Details' 'one' '## Other' 'x' '## Ticket Details' 'two' > "${f}" || { echo "fixture write failed"; return 0; }
  expect_failure "duplicate Ticket Details heading" "${f}"
  # 4. Trailing-space and CRLF headings are still found (and bytes preserved).
  f="${WORK_DIR}/f-space.md"
  printf '%s\n' '## User Context' 'x' '## Ticket Details ' 'td' > "${f}" || { echo "fixture write failed"; return 0; }
  printf '%s\n' '## Ticket Details ' 'td' > "${f}.want" || { echo "fixture write failed"; return 0; }
  expect_tail "trailing-space heading" "${f}" "${f}.want"
  f="${WORK_DIR}/f-crlf.md"
  printf '## User Context\r\nx\r\n## Ticket Details\r\n```\r\n## in a fence\r\n```\r\ntd\r\n## Project Context\r\npc\r\n' > "${f}" || { echo "fixture write failed"; return 0; }
  printf '## Ticket Details\r\n```\r\n## in a fence\r\n```\r\ntd\r\n## Project Context\r\npc\r\n' > "${f}.want" || { echo "fixture write failed"; return 0; }
  expect_tail "CRLF heading and CRLF fence close" "${f}" "${f}.want"
  # 5. No Project Context is fine.
  f="${WORK_DIR}/f-nopc.md"
  printf '%s\n' '## User Context' 'x' '## Ticket Details' 'only td' > "${f}" || { echo "fixture write failed"; return 0; }
  printf '%s\n' '## Ticket Details' 'only td' > "${f}.want" || { echo "fixture write failed"; return 0; }
  expect_tail "no Project Context" "${f}" "${f}.want"
  # 6. No Ticket Details at all, or only inside a fence.
  f="${WORK_DIR}/f-notd.md"
  printf '%s\n' '## User Context' 'x' '## Project Context' 'pc' > "${f}" || { echo "fixture write failed"; return 0; }
  expect_failure "no Ticket Details heading" "${f}"
  f="${WORK_DIR}/f-fencedonly.md"
  printf '%s\n' '## User Context' '```' '## Ticket Details' '```' > "${f}" || { echo "fixture write failed"; return 0; }
  expect_failure "Ticket Details only inside a fence" "${f}"
  # 7. Tilde fences: a backtick run inside a tilde fence is content.
  f="${WORK_DIR}/f-tilde.md"
  printf '%s\n' '## Ticket Details' '~~~~' '```' '## Ticket Details' '~~~~' 'after' > "${f}" || { echo "fixture write failed"; return 0; }
  cp "${f}" "${f}.want" || { echo "fixture copy failed"; return 0; }
  expect_tail "tilde fence holding a backtick run" "${f}" "${f}.want"
  # 7b. A leading-space-indented fence holding a `## Ticket Details` example.
  f="${WORK_DIR}/f-indent.md"
  printf '%s\n' '## User Context' '  ```' '## Ticket Details' '  ```' '## Ticket Details' 'td' > "${f}" || { echo "fixture write failed"; return 0; }
  printf '%s\n' '## Ticket Details' 'td' > "${f}.want" || { echo "fixture write failed"; return 0; }
  expect_tail "indented fence holding a Ticket Details example" "${f}" "${f}.want"
  # 8. The verification flow, independent of the copy path: the fresh draft
  #    slice and the candidate slice are extracted separately and compared.
  f="${WORK_DIR}/f-nested.md"
  printf '%s\n' '## User Context' 'x' > "${cand}" || { echo "fixture write failed"; return 0; }
  run_copy "${f}" "${cand}" || echo "flow: the direct copy failed on the nested-fence draft"
  run_verify "${f}" "${WORK_DIR}/draft-tail.md" || echo "flow: the fresh draft re-extraction failed"
  run_verify "${cand}" "${WORK_DIR}/candidate-tail.md" || echo "flow: the candidate re-extraction failed"
  cmp -s "${WORK_DIR}/draft-tail.md" "${WORK_DIR}/candidate-tail.md" || echo "flow: a good copy must compare equal"
  head -n 2 "${f}.want" > "${WORK_DIR}/trunc-tail" || { echo "fixture write failed"; return 0; }
  { printf '%s\n' '## User Context' 'x'; cat "${WORK_DIR}/trunc-tail"; } > "${WORK_DIR}/cand-trunc.md" || { echo "fixture write failed"; return 0; }
  run_verify "${WORK_DIR}/cand-trunc.md" "${WORK_DIR}/candidate-tail.md" || echo "flow: a truncated but well-formed candidate must still extract"
  cmp -s "${WORK_DIR}/draft-tail.md" "${WORK_DIR}/candidate-tail.md" && echo "flow: a truncated copy must NOT compare equal to the fresh draft slice"
  { printf '%s\n' '## User Context' 'x'; cat "${f}.want"; printf '%s\n' '## Ticket Details' 'again'; } > "${WORK_DIR}/cand-dup.md" || { echo "fixture write failed"; return 0; }
  run_verify "${WORK_DIR}/cand-dup.md" "${WORK_DIR}/candidate-tail.md" && echo "flow: a candidate with a duplicated heading must fail its re-extraction"
  printf '%s\n' '## User Context' 'x' > "${WORK_DIR}/cand-none.md" || { echo "fixture write failed"; return 0; }
  run_verify "${WORK_DIR}/cand-none.md" "${WORK_DIR}/candidate-tail.md" && echo "flow: a candidate without the copied tail must fail its re-extraction"
  # 9. The real plan file, when present (gitignored: absent in CI).
  plan=""
  for f in "${REAL_PLAN_CANDIDATES[@]}"; do [[ -f "${f}" ]] && { plan="${f}"; break; }; done
  if [[ -z "${plan}" ]]; then
    # stderr, never stdout: stdout lines are the problem list that report() turns into failures.
    echo "SKIP: real plan file ${REAL_PLAN_NAME} not present (gitignored); awk run on it skipped" >&2
  else
    if ! run_verify "${plan}" "${out}"; then echo "real plan file: the re-extraction exited non-zero"; else
      IFS= read -r first <"${out}" || first=""
      [[ "${first}" == '## Ticket Details'* ]] || echo "real plan file: the slice does not start at its Ticket Details heading: ${first}"
      [[ -s "${out}" ]] || echo "real plan file: the slice is empty"
    fi
    printf '%s\n' '## User Context' 'x' > "${cand}" || { echo "fixture write failed"; return 0; }
    run_copy "${plan}" "${cand}" || echo "real plan file: the direct copy exited non-zero"
    rc=0; run_verify "${cand}" "${WORK_DIR}/candidate-tail.md" || rc=$?
    [[ "${rc}" -eq 0 ]] || echo "real plan file: the candidate re-extraction exited ${rc}"
    run_verify "${plan}" "${WORK_DIR}/draft-tail.md" || echo "real plan file: the fresh draft re-extraction failed"
    cmp -s "${WORK_DIR}/draft-tail.md" "${WORK_DIR}/candidate-tail.md" || echo "real plan file: the copied tail differs from the draft's tail"
  fi
}
check_gatherer() {
  local doc="$1" s1 s2
  s2="$(sec_g_step2 "${doc}")"
  if [[ -z "${s2}" ]]; then echo "gatherer §2 not found"; else
    problems_missing "${s2}" "${G_STEP2_NEEDLES[@]}"
    problems_forbidden "${s2}" 'comments' "${OLD_PARENT_PROSE}"
    grep -qxF -- "   ${PARENT_CMD}" <<<"${s2}" || echo "§2 must carry the state-only parent fetch on its own line: ${PARENT_CMD}"
  fi
  problems_missing "$(sec_g_inputs "${doc}")" "${G_INPUTS_NEEDLES[@]}"
  s1="$(sec_g_step1 "${doc}")"
  if [[ -z "${s1}" ]]; then echo "gatherer §1 not found"; return 0; fi
  problems_missing "${s1}" "${G_STEP1_NEEDLES[@]}"
  grep -qxF -- "${OMIT_CMD}" <<<"${s1}" || echo "§1 must carry the omitComments field list on its own line: ${OMIT_CMD}"
  grep -qxF -- "${DEFAULT_CMD}" <<<"${s1}" || echo "§1 must keep the default field list on its own line: ${DEFAULT_CMD}"
  problems_missing "$(sec_g_step3 "${doc}")" "${G_STEP3_NEEDLES[@]}"
  problems_missing "$(sec_g_step6 "${doc}")" "${G_STEP6_NEEDLES[@]}"
}

# check_projection <listing|single> <filter> -- executes the jq filter (after
# substituting the nonce placeholder) on fixtures; prints one problem per line.
NONCE='0123456789abcdef0123456789abcdef'
FIXTURE='[{"id":10,"issue_url":"https://api.github.com/repos/o/r/issues/5","user":{"login":"cenci-bot","type":"Bot"},"author_association":"OWNER","body":"> banner\n<!-- cenci-planner-escalation:NONCE -->"},{"id":11,"issue_url":"https://api.github.com/repos/o/r/issues/5","user":{"login":"alice","type":"User"},"author_association":"OWNER","body":"> <!-- cenci-planner-escalation:NONCE -->\nmy answer"},{"id":12,"issue_url":"https://api.github.com/repos/o/r/issues/5","user":{"login":"bob","type":"User"},"author_association":"MEMBER","body":null},{"id":13,"issue_url":"https://api.github.com/repos/o/r/issues/5","user":{"login":"carol","type":"User"},"author_association":"OWNER","body":"text\n<!-- cenci-other -->"},{"id":14,"issue_url":"https://api.github.com/repos/o/r/issues/5","user":{"login":"dave","type":"User"},"author_association":"OWNER","body":"  > <!-- cenci-x -->"},{"id":15,"issue_url":"https://api.github.com/repos/o/r/issues/5","user":{"login":"erin","type":"User"},"author_association":"OWNER","body":"\t> quote"},{"id":16,"issue_url":"https://api.github.com/repos/o/r/issues/5","user":{"login":"frank","type":"User"},"author_association":"OWNER","body":"\t> <!-- cenci-x -->"}]'
check_projection() {
  local kind="$1" filter="${2//<escalationNonce>/${NONCE}}" fixture="${FIXTURE//NONCE/${NONCE}}" prog out verdicts keys types want_keys
  if [[ -z "$2" ]]; then echo "${kind}: filter not found"; return 0; fi
  if [[ "${kind}" == listing ]]; then prog="${filter}"; want_keys='[["anchorMarker","association","cenciMarker","id","login","type"]]'
  else prog=".[] | (${filter})"; want_keys='[["anchorMarker","association","cenciMarker","id","issueUrl","login","type"]]'
  fi
  if ! out="$(jq -c "${prog}" <<<"${fixture}" 2>&1)"; then echo "${kind}: jq failed: ${out}"; return 0; fi
  if ! verdicts="$(jq -s -c '[.[] | [.id, .cenciMarker, .anchorMarker]]' <<<"${out}" 2>&1)"; then echo "${kind}: verdict read failed: ${verdicts}"; return 0; fi
  if ! keys="$(jq -s -c '[.[] | keys] | unique' <<<"${out}" 2>&1)"; then echo "${kind}: key read failed: ${keys}"; return 0; fi
  [[ "${verdicts}" == '[[10,true,true],[11,false,false],[12,false,false],[13,true,false],[14,false,false],[15,false,false],[16,false,false]]' ]] || echo "${kind}: marker verdicts wrong (anchor true/true, quote-reply false/false, null body false/false, plain marker true/false, space-indented and tab-indented quote false/false): ${verdicts}"
  if ! types="$(jq -s -c '[.[] | [(.cenciMarker | type), (.login | type), (.type | type), (.association | type)]] | unique' <<<"${out}" 2>&1)"; then echo "${kind}: type read failed: ${types}"; return 0; fi
  [[ "${types}" == '[["boolean","string","string","string"]]' ]] || echo "${kind}: every result must carry a boolean cenciMarker and string login/type/association: ${types}"
  [[ "${keys}" == "${want_keys}" ]] || echo "${kind}: output keys must be exactly ${want_keys} (no body): ${keys}"
}

# ---------------------------------------------------------------------
# Real-doc assertions.
# ---------------------------------------------------------------------
report() {
  # $1=label $2=newline-separated problems (empty means pass)
  local line
  [[ -z "$2" ]] && return 0
  while IFS= read -r line; do fail "$1: ${line}"; done <<<"$2"
}

report "SKILL.md \$ARGUMENTS plan-file token (AC6)" "$(check_skill_args "${SKILL_DOC}")"
report "SKILL.md awaiting-input probe (AC7/AC8)" "$(check_skill_probe "${SKILL_DOC}")"
report "SKILL.md awaiting-input sub-bullets" "$(check_skill_subbullets "${SKILL_DOC}")"
report "SKILL.md Answered sub-bullet" "$(check_skill_answered "${SKILL_DOC}")"
report "SKILL.md Not answered sub-bullet (invalid token)" "$(check_skill_notanswered "${SKILL_DOC}")"
report "SKILL.md Context Gathering (AC10)" "$(check_skill_context "${SKILL_DOC}")"
report "SKILL.md old answer-scan wording (AC9)" "$(check_skill_old_phrase "${SKILL_DOC}")"
report "phase-1-plan.md step 2 (AC7/AC8/AC9)" "$(check_phase_step2 "${PHASE1_DOC}")"
report "phase-1-plan.md HS-R2 inventory row" "$(check_phase_hsrow "${PHASE1_DOC}")"
report "phase-1-plan.md step 3 partial answer" "$(check_phase_step3 "${PHASE1_DOC}")"
report "phase-1-plan.md step 6 (AC11)" "$(check_phase_step6 "${PHASE1_DOC}")"
report "phase-1-plan.md old answer-scan wording (AC9)" "$(check_phase_old_phrase "${PHASE1_DOC}")"
report "context-gatherer.md omitComments (AC10)" "$(check_gatherer "${GATHERER_DOC}")"
report "SKILL.md token path restores awaiting-input (edge cases 17-20)" "$(check_skill_tokenpath "${SKILL_DOC}")"
report "SKILL.md malformed token stays Not answered" "$(check_skill_malformed "${SKILL_DOC}")"
report "SKILL.md no qualifying comment stays Not answered" "$(check_skill_noqualifying "${SKILL_DOC}")"
report "phase-1-plan.md step 2 Recovery (edge cases 17-20)" "$(check_phase_step2_recovery "${PHASE1_DOC}")"
report "dispatch-reconcile.md resume scoping routes" "$(check_reconcile "${RECONCILE_DOC}")"
report "phase-1-plan.md HS-R11 inventory row (three-heading, mechanical copy)" "$(check_phase_hsr11 "${PHASE1_DOC}")"
report "phase-1-plan.md Persist the Plan hard stop (draft is the implicated input on the resume path)" "$(check_phase_persist "${PHASE1_DOC}")"
report "phase-1-plan.md step 6 extraction awk executes" "$(check_awk_extract "$(sec_step6 "${PHASE1_DOC}")")"

# The projections as written in the docs: the same filter at both sites, and
# each executes correctly against the fixtures.
SKILL_LISTING="$(extract_jq_filter "$(sec_skill_probe "${SKILL_DOC}")" "--paginate --jq ${SQ}")"
STEP2_LISTING="$(extract_jq_filter "$(sec_step2 "${PHASE1_DOC}")" "--paginate --jq ${SQ}")"
SKILL_SINGLE="$(extract_jq_filter "$(sec_skill_probe "${SKILL_DOC}")" "issues/comments/<answerCommentId> --jq ${SQ}")"
STEP2_SINGLE="$(extract_jq_filter "$(sec_step2 "${PHASE1_DOC}")" "issues/comments/<answerCommentId> --jq ${SQ}")"
report "SKILL.md listing projection executes" "$(check_projection listing "${SKILL_LISTING}")"
report "phase-1-plan.md step 2 listing projection executes" "$(check_projection listing "${STEP2_LISTING}")"
report "SKILL.md single-comment projection executes" "$(check_projection single "${SKILL_SINGLE}")"
report "phase-1-plan.md step 2 single-comment projection executes" "$(check_projection single "${STEP2_SINGLE}")"
[[ "${SKILL_LISTING}" == "${STEP2_LISTING}" ]] || fail "listing projection differs between SKILL.md and phase-1-plan.md step 2"
[[ "${SKILL_SINGLE}" == "${STEP2_SINGLE}" ]] || fail "single-comment projection differs between SKILL.md and phase-1-plan.md step 2"

# ---------------------------------------------------------------------
# Non-vacuity self-tests: every marker removed from its own section and
# planted elsewhere in the doc must make its check fail.
# ---------------------------------------------------------------------
require_problems() {
  # $1=control label, $2=problems output of the check on the mutated copy
  [[ -n "$2" ]] || fail "self-test '$1': check passed on a mutated copy (the check is vacuous)"
}

# selftest_needles <label> <check-fn> <section-fn> <doc> <needle>...
selftest_needles() {
  local label="$1" check_fn="$2" sec_fn="$3" doc="$4" section needle stripped mutated; shift 4
  section="$("${sec_fn}" "${doc}")"
  if [[ -z "${section}" ]]; then fail "self-test ${label}: section is empty on the real doc"; return 0; fi
  for needle in "$@"; do
    if [[ "${section}" != *"${needle}"* ]]; then
      fail "self-test ${label}: needle is not inside the real section, so it cannot be mutated: [${needle}]"
      continue
    fi
    stripped="${section//"${needle}"/}"
    mutated="${doc//"${section}"/"${stripped}"}"
    mutated="${needle}${NL}${mutated}"
    require_problems "${label}: [${needle}] removed from its section and planted outside it" "$("${check_fn}" "${mutated}")"
  done
}

# selftest_forbidden <label> <check-fn> <section-fn> <doc> <phrase>
selftest_forbidden() {
  local label="$1" check_fn="$2" sec_fn="$3" doc="$4" phrase="$5" section planted mutated
  section="$("${sec_fn}" "${doc}")"
  if [[ -z "${section}" ]]; then fail "self-test ${label}: section is empty on the real doc"; return 0; fi
  planted="${section}${NL}${phrase}"
  mutated="${doc//"${section}"/"${planted}"}"
  require_problems "${label}: forbidden phrase planted in its section" "$("${check_fn}" "${mutated}")"
}

selftest_needles "SKILL.md args" check_skill_args sec_skill_args "${SKILL_DOC}" "${ARGS_NEEDLES[@]}"
selftest_forbidden "SKILL.md args" check_skill_args sec_skill_args "${SKILL_DOC}" 'The rest of `$ARGUMENTS` after the file path is ignored.'
selftest_needles "SKILL.md probe" check_skill_probe sec_skill_probe "${SKILL_DOC}" "${SKILL_PROBE_NEEDLES[@]}"
selftest_forbidden "SKILL.md probe" check_skill_probe sec_skill_probe "${SKILL_DOC}" "Take the ${OLD_PHRASE}(s) as the answer."
selftest_needles "SKILL.md Answered" check_skill_answered sec_skill_answered "${SKILL_DOC}" "${ANSWERED_NEEDLES[@]}"
selftest_needles "SKILL.md Not answered" check_skill_notanswered sec_skill_notanswered "${SKILL_DOC}" "${NOT_ANSWERED_NEEDLES[@]}"
selftest_needles "SKILL.md Context Gathering" check_skill_context sec_skill_context "${SKILL_DOC}" "${CONTEXT_NEEDLES[@]}"
selftest_needles "phase-1-plan.md step 2" check_phase_step2 sec_step2 "${PHASE1_DOC}" "${STEP2_NEEDLES[@]}"
selftest_forbidden "phase-1-plan.md step 2" check_phase_step2 sec_step2 "${PHASE1_DOC}" "Take the ${OLD_PHRASE}(s) as the answer text."
selftest_needles "phase-1-plan.md HS-R2 row" check_phase_hsrow sec_hsrow "${PHASE1_DOC}" "${HSROW_NEEDLES[@]}"
selftest_needles "phase-1-plan.md step 3" check_phase_step3 sec_step3 "${PHASE1_DOC}" "${STEP3_NEEDLES[@]}"
selftest_needles "phase-1-plan.md step 6" check_phase_step6 sec_step6 "${PHASE1_DOC}" "${STEP6_NEEDLES[@]}"
selftest_forbidden "phase-1-plan.md step 6" check_phase_step6 sec_step6 "${PHASE1_DOC}" '(`Write` then the context-bundle `cat`-append)'
selftest_needles "gatherer Inputs" check_gatherer sec_g_inputs "${GATHERER_DOC}" "${G_INPUTS_NEEDLES[@]}"
selftest_needles "gatherer §1" check_gatherer sec_g_step1 "${GATHERER_DOC}" "${G_STEP1_NEEDLES[@]}"
selftest_needles "gatherer §3" check_gatherer sec_g_step3 "${GATHERER_DOC}" "${G_STEP3_NEEDLES[@]}"
selftest_needles "gatherer §6" check_gatherer sec_g_step6 "${GATHERER_DOC}" "${G_STEP6_NEEDLES[@]}"
selftest_needles "SKILL.md token path" check_skill_tokenpath sec_skill_tokenpath "${SKILL_DOC}" "${TOKENPATH_NEEDLES[@]}"
selftest_forbidden "SKILL.md token path" check_skill_tokenpath sec_skill_tokenpath "${SKILL_DOC}" "${OLD_TOKEN_ROUTE}: a 404 (deleted comment)."
selftest_needles "SKILL.md malformed token" check_skill_malformed sec_skill_probe "${SKILL_DOC}" "${MALFORMED_NEEDLES[@]}"
selftest_needles "SKILL.md no qualifying comment" check_skill_noqualifying sec_skill_notanswered "${SKILL_DOC}" "${NOQUALIFYING_NEEDLES[@]}"
selftest_needles "phase-1-plan.md step 2 Recovery" check_phase_step2_recovery sec_step2_recovery "${PHASE1_DOC}" "${STEP2_RECOVERY_NEEDLES[@]}"
selftest_needles "dispatch-reconcile.md valid-token bullet" check_reconcile sec_reconcile_token "${RECONCILE_DOC}" "${RECONCILE_TOKEN_NEEDLES[@]}"
selftest_forbidden "dispatch-reconcile.md valid-token bullet" check_reconcile sec_reconcile_token "${RECONCILE_DOC}" 'a 404 takes the **Not answered** branch'
selftest_needles "dispatch-reconcile.md malformed-token bullet" check_reconcile sec_reconcile_malformed "${RECONCILE_DOC}" "${RECONCILE_MALFORMED_NEEDLES[@]}"
selftest_needles "dispatch-reconcile.md no-token bullet" check_reconcile sec_reconcile_notoken "${RECONCILE_DOC}" "${RECONCILE_NOTOKEN_NEEDLES[@]}"
selftest_needles "dispatch-reconcile.md context-bundle claim" check_reconcile sec_reconcile "${RECONCILE_DOC}" "${RECONCILE_CLAIM_NEEDLES[@]}"
selftest_needles "phase-1-plan.md HS-R11 row" check_phase_hsr11 sec_hsrow11 "${PHASE1_DOC}" "${HSR11_NEEDLES[@]}"
selftest_needles "phase-1-plan.md Persist the Plan hard stop" check_phase_persist sec_persist "${PHASE1_DOC}" "${PERSIST_NEEDLES[@]}"
selftest_forbidden "phase-1-plan.md step 6 old Write wording" check_phase_step6 sec_step6 "${PHASE1_DOC}" "the \`Write\` carries the draft's two sections ${S6_OLD_WRITE_CARRIES}"
selftest_forbidden "phase-1-plan.md step 6 old implicated-input wording" check_phase_step6 sec_step6 "${PHASE1_DOC}" "(${S6_OLD_IMPLICATED})"
selftest_needles "gatherer §2" check_gatherer sec_g_step2 "${GATHERER_DOC}" "${G_STEP2_NEEDLES[@]}"
selftest_forbidden "gatherer §2 parent comments" check_gatherer sec_g_step2 "${GATHERER_DOC}" 'gh issue view <parentId> --repo <owner>/<repo> --json state,comments'
selftest_forbidden "gatherer §2 old parent wording" check_gatherer sec_g_step2 "${GATHERER_DOC}" "${OLD_PARENT_PROSE}."
selftest_forbidden "SKILL.md Context Gathering broad claim" check_skill_context sec_skill_context "${SKILL_DOC}" "so ${BROAD_CLAIM} the session or the finalized plan"
selftest_needles "SKILL.md token path (fix round 2)" check_skill_tokenpath sec_skill_tokenpath "${SKILL_DOC}" "${TRIG_PROJ}"
selftest_needles "phase-1-plan.md step 2 Recovery (fix round 2)" check_phase_step2_recovery sec_step2_recovery "${PHASE1_DOC}" "${TRIG_BODY}" "${TRIG_PROJ}"

# Whole-file absence of the retired wording, in both files.
require_problems "SKILL.md retired phrase" "$(check_skill_old_phrase "${SKILL_DOC}${NL}${OLD_PHRASE}")"
require_problems "phase-1-plan.md retired phrase" "$(check_phase_old_phrase "${PHASE1_DOC}${NL}${OLD_PHRASE}")"

# Structural controls (replacement text is built in variables so no quoting
# subtlety inside ${..//../..} can hide a no-op mutation).
LAST_HEAD='  - **Anchor incomplete or missing**'
EXTRA_HEAD="  - **Extra**: x${NL}${LAST_HEAD}"
NO_JQ_LISTING="--paginate ${SQ}"
INDENTED_DEFAULT=" ${DEFAULT_CMD}"
INDENTED_OMIT=" ${OMIT_CMD}"
M_SUBBULLETS="${SKILL_DOC/"${LAST_HEAD}"/"${EXTRA_HEAD}"}"
M_PAGINATE="${SKILL_DOC/"--paginate --jq ${SQ}"/"${NO_JQ_LISTING}"}"
M_DEFAULT="${GATHERER_DOC/"${DEFAULT_CMD}"/"${INDENTED_DEFAULT}"}"
M_OMIT="${GATHERER_DOC/"${OMIT_CMD}"/"${INDENTED_OMIT}"}"
[[ "${M_SUBBULLETS}" != "${SKILL_DOC}" ]] || fail "self-test: the sub-bullet mutation changed nothing"
[[ "${M_PAGINATE}" != "${SKILL_DOC}" ]] || fail "self-test: the --paginate mutation changed nothing"
[[ "${M_DEFAULT}" != "${GATHERER_DOC}" ]] || fail "self-test: the default-field-list mutation changed nothing"
[[ "${M_OMIT}" != "${GATHERER_DOC}" ]] || fail "self-test: the omitComments-field-list mutation changed nothing"
require_problems "an extra sub-bullet after Not answered" "$(check_skill_subbullets "${M_SUBBULLETS}")"
require_problems "a --paginate listing without --jq" "$(check_skill_probe "${M_PAGINATE}")"
require_problems "gatherer default field list not on its own line" "$(check_gatherer "${M_DEFAULT}")"
require_problems "gatherer omitComments field list not on its own line" "$(check_gatherer "${M_OMIT}")"
require_problems "a doc without the plan-file bullet" "$(check_skill_args "no bullet here")"
require_problems "a doc without step 2" "$(check_phase_step2 "no resume section here")"

# Fix round 2 structural controls. Each mutation is applied INSIDE its own
# section (mutate_in_section), then proven to have changed the doc, so a
# mutation that lands elsewhere or does nothing cannot pass vacuously.
mutate_in_section() {
  # <doc> <section-fn> <from> <to> -- prints the doc with the first <from> of
  # the section replaced by <to>; prints the doc unchanged when it is absent.
  local doc="$1" sec_fn="$2" from="$3" to="$4" section mutated_section
  section="$("${sec_fn}" "${doc}")"
  if [[ -z "${section}" || "${section}" != *"${from}"* ]]; then printf '%s' "${doc}"; return 0; fi
  mutated_section="${section/"${from}"/"${to}"}"
  printf '%s' "${doc/"${section}"/"${mutated_section}"}"
}
# expect_mutation_caught <label> <original doc> <mutated doc> <check-fn>
expect_mutation_caught() {
  local label="$1" original="$2" mutated="$3" check_fn="$4"
  if [[ "${mutated}" == "${original}" ]]; then fail "self-test '${label}': the mutation changed nothing"; return 0; fi
  require_problems "${label}" "$("${check_fn}" "${mutated}")"
}
BARE_LISTING='gh api "repos/<owner>/<repo>/issues/<number>/comments?per_page=100"'
NO_BODY_NOTE='No comment body is fetched here.'
LOCATE_ANCHOR='Locate the anchor as the entry whose `id` equals the draft'"'"'s'
NO_LISTING_SKILL='this path never runs the paginated thread listing'
JQ_BODY_PLANT='--jq .body'
JQ_EXTRA_PLANT="--jq ${SQ}.user${SQ}"
expect_mutation_caught "step 2 --paginate without --jq" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step2 "--paginate --jq ${SQ}" "--paginate ${SQ}")" check_phase_step2
expect_mutation_caught "step 2 bare thread listing next to the projected one" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step2 "${LOCATE_ANCHOR}" "${BARE_LISTING} ${LOCATE_ANCHOR}")" check_phase_step2
expect_mutation_caught "SKILL.md probe bare thread listing next to the projected one" "${SKILL_DOC}" "$(mutate_in_section "${SKILL_DOC}" sec_skill_probe "${NO_BODY_NOTE}" "${NO_BODY_NOTE} ${BARE_LISTING}")" check_skill_probe
expect_mutation_caught "SKILL.md probe --jq .body" "${SKILL_DOC}" "$(mutate_in_section "${SKILL_DOC}" sec_skill_probe "${NO_BODY_NOTE}" "${NO_BODY_NOTE} ${JQ_BODY_PLANT}")" check_skill_probe
expect_mutation_caught "SKILL.md token path bare thread listing" "${SKILL_DOC}" "$(mutate_in_section "${SKILL_DOC}" sec_skill_tokenpath "${NO_LISTING_SKILL}" "${NO_LISTING_SKILL} (${BARE_LISTING})")" check_skill_tokenpath
expect_mutation_caught "SKILL.md token path --jq .body" "${SKILL_DOC}" "$(mutate_in_section "${SKILL_DOC}" sec_skill_tokenpath "${NO_LISTING_SKILL}" "${NO_LISTING_SKILL} (${JQ_BODY_PLANT})")" check_skill_tokenpath
expect_mutation_caught "SKILL.md token path extra --jq filter" "${SKILL_DOC}" "$(mutate_in_section "${SKILL_DOC}" sec_skill_tokenpath "${NO_LISTING_SKILL}" "${NO_LISTING_SKILL} (${JQ_EXTRA_PLANT})")" check_skill_tokenpath
expect_mutation_caught "step 2 token path bare thread listing" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step2_tokenpath "${NO_LISTING_SKILL}" "${NO_LISTING_SKILL} (${BARE_LISTING})")" check_phase_step2
expect_mutation_caught "step 2 token path --jq .body" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step2_tokenpath "${NO_LISTING_SKILL}" "${NO_LISTING_SKILL} (${JQ_BODY_PLANT})")" check_phase_step2
expect_mutation_caught "step 2 token path extra --jq filter" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step2_tokenpath "${NO_LISTING_SKILL}" "${NO_LISTING_SKILL} (${JQ_EXTRA_PLANT})")" check_phase_step2
expect_mutation_caught "HS-R11 row back to the four-heading wording" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_hsrow11 "${HSR11_THREE}" "${HSR11_OLD} validation")" check_phase_hsr11
require_problems "a doc without the step 2 token-path paragraph" "$(check_phase_step2 "${PHASE1_DOC/"   **Token path**"/"   **Other path**"}")"

# The documented step-6 tail-slice awk (the first occurrence in the section, the
# direct copy), broken nine ways. Each mutation is applied INSIDE step 6 and
# proven to change the doc; each must be caught by executing the awk on fixtures.
expect_awk_caught() {
  local label="$1" from="$2" to="$3" mutated
  mutated="$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${from}" "${to}")"
  if [[ "${mutated}" == "${PHASE1_DOC}" ]]; then fail "self-test '${label}': the mutation changed nothing"; return 0; fi
  require_problems "${label}" "$(check_awk_extract "$(sec_step6 "${mutated}")")"
}
expect_awk_caught "slice awk matching a fenced heading" '!fence && $0 ~ /^## Ticket Details' '$0 ~ /^## Ticket Details'
expect_awk_caught "slice awk without the duplicate-heading refusal" 'if (seen) bad = 1; else bad = 0; ' 'bad = 0; '
expect_awk_caught "slice awk whose closing fence ignores the opener length" ' && n >= fn' ''
expect_awk_caught "slice awk that ignores a fence open at EOF" ' || fence' ''
expect_awk_caught "slice awk that exits zero when no heading exists" 'BEGIN { bad = 1 } ' ''
expect_awk_caught "slice awk without trailing-space and CR tolerance on the heading" '/^## Ticket Details[ \t\r]*$/' '/^## Ticket Details$/'
expect_awk_caught "slice awk whose fence close is not CRLF-tolerant" '/^[ \t\r]*$/) fence = 0' '/^[ \t]*$/) fence = 0'
expect_awk_caught "slice awk without tilde fences" '(ch == bt || ch == "~")' '(ch == bt)'
expect_awk_caught "slice awk without leading-space fence tolerance" 'sub(/^ +/, "", t); ' ''

# Non-vacuity of the new step-6 prose pins: the retired round-2 text, and each
# structural element, mutated INSIDE step 6 (proven to change the doc).
for retired in "${S6_RETIRED[@]}"; do
  selftest_forbidden "phase-1-plan.md step 6 retired text [${retired}]" check_phase_step6 sec_step6 "${PHASE1_DOC}" "${retired}"
done
expect_mutation_caught "step 6 direct copy turned into a scratch hop" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "\" >> \"${CANDIDATE_REL}\"" "\" > \"<run-dir>/copy.md\"")" check_phase_step6
expect_mutation_caught "step 6 awk END without the open-fence exit" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${END_EXIT}" 'END { exit bad }')" check_phase_step6
expect_mutation_caught "step 6 cmp compares the candidate slice with itself" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${CMP_CMD}" "cmp \"${CAND_TAIL}\" \"${CAND_TAIL}\"")" check_phase_step6
expect_mutation_caught "step 6 test -s dropped" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${TEST_S_DRAFT}" 'true')" check_phase_step6
expect_mutation_caught "step 6 cleanup dropped" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${RM_CMD}" 'true')" check_phase_step6
expect_mutation_caught "step 6 ordering clause weakened" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${S6_ORDER}" 'the replace follows')" check_phase_step6
expect_mutation_caught "step 6 ordering clause duplicated" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${S6_ORDER}" "${S6_ORDER}; ${S6_ORDER}")" check_phase_step6
expect_mutation_caught "step 6 mv before the cmp" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${S6_DIR_CALL}" "${MV_CMD} ${S6_DIR_CALL}")" check_phase_step6
expect_mutation_caught "step 6 private directory back to the shared temp dir" "${PHASE1_DOC}" "$(mutate_in_section "${PHASE1_DOC}" sec_step6 "${S6_NO_SHARED}" 'every file goes to mkdir -p "${TMPDIR:-/tmp}/cenci"')" check_phase_step6

# Extractor fence-awareness: a fenced "## " line must not end a section, and a
# fenced numbered line must not end a step.
FENCE_DOC=$'## A\nbefore\n```\n## B\n1. **fenced**\n```\nafter\n## C\nlater'
[[ "$(extract_h2_section "${FENCE_DOC}" "## A")" == *after* ]] || fail "self-test: extract_h2_section stopped at a fenced heading"
[[ "$(extract_h2_section "${FENCE_DOC}" "## A")" != *later* ]] || fail "self-test: extract_h2_section ran past the next real heading"
STEP_DOC=$'1. **one**\n```\n2. **fenced**\n```\ntail\n2. **two**\nx'
[[ "$(extract_resume_step "${STEP_DOC}" 1)" == *tail* ]] || fail "self-test: extract_resume_step stopped at a fenced numbered line"
[[ "$(extract_resume_step "${STEP_DOC}" 1)" != *'**two**'* ]] || fail "self-test: extract_resume_step ran past the next real step"

# Projection behavior controls (real jq on broken filters).
NULL_SAFE_BODY='(.body // "")'
RAW_BODY='.body'
NOT_STRIP='| not'
BODY_KEY='body: .body, anchorMarker:'
ISSUE_URL_KEY=', issueUrl: .issue_url'
EMPTY=''
M_NOSTRIP="${LISTING_JQ//"${NOT_STRIP}"/"${EMPTY}"}"
M_BODYKEY="${LISTING_JQ//"anchorMarker:"/"${BODY_KEY}"}"
M_NULLBODY="${LISTING_JQ//"${NULL_SAFE_BODY}"/"${RAW_BODY}"}"
M_NOURL="${SINGLE_JQ//"${ISSUE_URL_KEY}"/"${EMPTY}"}"
[[ "${M_NOSTRIP}" != "${LISTING_JQ}" && "${M_BODYKEY}" != "${LISTING_JQ}" && "${M_NULLBODY}" != "${LISTING_JQ}" && "${M_NOURL}" != "${SINGLE_JQ}" ]] || fail "self-test: a projection mutation changed nothing"
require_problems "listing without blockquote stripping" "$(check_projection listing "${M_NOSTRIP}")"
require_problems "listing that emits a body key" "$(check_projection listing "${M_BODYKEY}")"
require_problems "listing that breaks on a null body" "$(check_projection listing "${M_NULLBODY}")"
require_problems "single projection without issueUrl" "$(check_projection single "${M_NOURL}")"
# Leading-whitespace quote lines: the strip must accept spaces AND tabs before
# the `>` (the `  > <!-- cenci-x -->` and `\t> <!-- cenci-x -->` fixture rows).
WS_PREFIX='[ \t]*'
SPACE_ONLY_PREFIX='[ ]*'
COL0_PREFIX=''
M_COL0="${LISTING_JQ//"${WS_PREFIX}"/"${COL0_PREFIX}"}"
M_SPACEONLY="${LISTING_JQ//"${WS_PREFIX}"/"${SPACE_ONLY_PREFIX}"}"
[[ "${M_COL0}" != "${LISTING_JQ}" && "${M_SPACEONLY}" != "${LISTING_JQ}" ]] || fail "self-test: a whitespace-prefix mutation changed nothing"
require_problems "listing that strips quote lines only at column 0" "$(check_projection listing "${M_COL0}")"
require_problems "listing whose quote strip ignores a leading tab" "$(check_projection listing "${M_SPACEONLY}")"
MARK_FROM='cenciMarker: ($s | contains("<!-- cenci-"))'
MARK_TO='cenciMarker: ($s | contains("<!-- cenci-") | tostring)'
M_STRINGMARKER="${LISTING_JQ//"${MARK_FROM}"/"${MARK_TO}"}"
[[ "${M_STRINGMARKER}" != "${LISTING_JQ}" ]] || fail "self-test: the string-typed marker mutation changed nothing"
require_problems "listing whose cenciMarker is not a boolean" "$(check_projection listing "${M_STRINGMARKER}")"

echo "resume-comment-scope-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
