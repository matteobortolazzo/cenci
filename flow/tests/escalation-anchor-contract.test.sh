#!/usr/bin/env bash
# Tests cross-lane literal parity for ticket #849's persisted escalation
# anchor (a per-escalation nonce plus the immutable REST comment ID) across
# the three places that must state the same literals verbatim:
#   - flow/skills/implement/phases/phase-1-plan.md's new `## Escalation
#     Anchor` section (the producer: mints the nonce, posts via the REST
#     comments API, persists both fields into the draft's front matter)
#   - flow/skills/implement/SKILL.md's awaiting-input Plan Verification
#     branch (the manual-resume consumer -- this is the ONLY coverage of
#     that path; watch/internal/dispatch/resume_crosslane_test.go only
#     exercises the Go dispatch consumer)
#   - watch/internal/dispatch/resume.go (the dispatch consumer)
#
# Fixture-free, grep-based idiom of flow/tests/dispatch-resume-contract.test.sh:
# `set -uo pipefail`, a `failures` counter, `grep -qF`/substring helpers,
# fence-aware awk section extraction bounded to the next `## `-level heading,
# single-line markers per docs/shell-scripting-gotchas.md. No `read_*`-named
# helper calls fail() from inside a $(...) subshell, so this file is
# compliant with flow/tests/read-helper-purity-contract.test.sh's repo-wide
# scan (every extractor here is named `extract_*`, is pure, and has no
# fail() side effect). Auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob -- no registration needed.
#
# Covered files:
#   - flow/skills/implement/phases/phase-1-plan.md (new `## Escalation
#     Anchor` section: nonce regex, `openssl rand -hex 16` mint command,
#     the two-part marker prefix, the front-matter key names, the REST
#     comment-create call)
#   - flow/skills/implement/SKILL.md (awaiting-input branch: front-matter
#     key names consumed from plan-check's echoed metadata, the REST probe
#     call, the nonce format regex)
#   - watch/internal/dispatch/resume.go (escalationAnchorPrefix,
#     escalationNoncePattern, the REST probe call)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "escalation-anchor-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "escalation-anchor-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "escalation-anchor-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
RESUME_GO="${REPO_ROOT}/watch/internal/dispatch/resume.go"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

assert_file_contains() {
  # $1=file $2=needle $3=description -- pure aside from fail(), matching
  # dispatch-resume-contract.test.sh's own convention; never invoked inside
  # $(...) here.
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}

# extract_section_raw <content> <heading-line> -- pure, fence-aware
# extraction bounded to the next "## " heading, mirroring
# dispatch-resume-contract.test.sh's extract_resume_section /
# unattended-escalation-contract.test.sh's extract_section_raw. Safe to call
# inside $(...): no fail() side effect.
extract_section_raw() {
  awk -v want="$2" '
    $0 == want { on=1; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

if ! PHASE1_CONTENT="$(cat "${PHASE1_PLAN}" 2>/dev/null)"; then
  fail "phase-1-plan.md: doc not found/unreadable: ${PHASE1_PLAN}"
  PHASE1_CONTENT=""
fi
if ! IMPLEMENT_SKILL_CONTENT="$(cat "${IMPLEMENT_SKILL}" 2>/dev/null)"; then
  fail "SKILL.md: doc not found/unreadable: ${IMPLEMENT_SKILL}"
  IMPLEMENT_SKILL_CONTENT=""
fi
if [[ ! -f "${RESUME_GO}" ]]; then
  fail "resume.go: file not found: ${RESUME_GO}"
fi

# Pinned literals every lane that carries them must state verbatim, per the
# plan's Integration points: resume.go's doc comment, SKILL.md,
# phase-1-plan.md, and this contract test are the four places that must
# stay in sync.
PIN_NONCE_REGEX='^[0-9a-f]{32}$'
PIN_MARKER_PREFIX='<!-- cenci-planner-escalation:'
PIN_FM_NONCE_KEY='escalationNonce'
PIN_FM_COMMENTID_KEY='escalationCommentId'
PIN_MINT_CMD='openssl rand -hex 16'
PIN_CREATE_JQ='--jq .id'
PIN_REST_PAGINATE='--paginate'
PIN_REST_PER_PAGE='per_page=100'
PIN_READBACK="gh api repos/<owner>/<repo>/issues/comments/<id> --jq '{id, body}'"
# #880: persist the nonce (and clear any stale escalationCommentId) before
# ever posting -- the literal every escalating path's own persist-nonce
# sub-step restates at its call site (never merely referenced), so a
# post-then-crash never leaves an unrecorded replacement anchor.
PIN_PERSIST_BEFORE_POST='persist it into the draft'"'"'s front matter before posting anything'

# =====================================================================
# flow/skills/implement/phases/phase-1-plan.md -- ## Escalation Anchor
# (the producer: mint, validate, marker literal, front-matter keys, the
# REST comment-create call)
# =====================================================================

ESCALATION_ANCHOR_SECTION="$(extract_section_raw "${PHASE1_CONTENT}" "## Escalation Anchor")"

if [[ -z "${ESCALATION_ANCHOR_SECTION}" ]]; then
  fail "phase-1-plan.md: could not locate '## Escalation Anchor' section (extract_section_raw returned empty) -- expected as of #849 Implementation Order step 4"
else
  assert_section_contains() {
    # $1=needle $2=label
    [[ -n "$1" ]] || { fail "$2: empty needle"; return; }
    [[ "${ESCALATION_ANCHOR_SECTION}" == *"$1"* ]] || fail "phase-1-plan.md (## Escalation Anchor) $2 (expected to contain: $1)"
  }
  assert_section_contains "${PIN_NONCE_REGEX}" \
    "must state the nonce format regex verbatim"
  assert_section_contains "${PIN_MARKER_PREFIX}" \
    "must state the two-part marker prefix verbatim"
  assert_section_contains "${PIN_FM_NONCE_KEY}" \
    "must name the escalationNonce front-matter key"
  assert_section_contains "${PIN_FM_COMMENTID_KEY}" \
    "must name the escalationCommentId front-matter key"
  assert_section_contains "${PIN_MINT_CMD}" \
    "must name the openssl rand -hex 16 mint command verbatim"
  assert_section_contains "${PIN_CREATE_JQ}" \
    "must name the REST comment-create call's --jq .id verbatim"
  assert_section_contains "${PIN_READBACK}" \
    "must name the comment body read-back verification call verbatim"
  assert_section_contains "${PIN_PERSIST_BEFORE_POST}" \
    "must state the persist-nonce-before-POST ordering rule verbatim (#880)"
fi

# =====================================================================
# flow/skills/implement/SKILL.md -- awaiting-input Plan Verification branch
# (the manual-resume consumer: consumes plan-check's echoed metadata,
# issues the REST probe call)
# =====================================================================

assert_file_contains "${IMPLEMENT_SKILL}" "${PIN_FM_NONCE_KEY}" \
  "awaiting-input branch must consume plan.escalationNonce from plan-check's echoed metadata"
assert_file_contains "${IMPLEMENT_SKILL}" "${PIN_FM_COMMENTID_KEY}" \
  "awaiting-input branch must consume plan.escalationCommentId from plan-check's echoed metadata"
assert_file_contains "${IMPLEMENT_SKILL}" "${PIN_REST_PAGINATE}" \
  "awaiting-input branch must name the REST --paginate probe call"
assert_file_contains "${IMPLEMENT_SKILL}" "${PIN_REST_PER_PAGE}" \
  "awaiting-input branch must name the REST probe's per_page=100 verbatim"
assert_file_contains "${IMPLEMENT_SKILL}" "${PIN_NONCE_REGEX}" \
  "awaiting-input branch must restate the nonce format regex verbatim"

# =====================================================================
# watch/internal/dispatch/resume.go -- the dispatch consumer
# =====================================================================

if [[ -f "${RESUME_GO}" ]]; then
  assert_file_contains "${RESUME_GO}" "${PIN_MARKER_PREFIX}" \
    "must declare the two-part marker prefix literal verbatim"
  assert_file_contains "${RESUME_GO}" "${PIN_NONCE_REGEX}" \
    "must declare the nonce format regex literal verbatim"
  assert_file_contains "${RESUME_GO}" "${PIN_REST_PAGINATE}" \
    "must issue the REST probe call with --paginate"
  assert_file_contains "${RESUME_GO}" "${PIN_REST_PER_PAGE}" \
    "must issue the REST probe call with per_page=100"
fi

echo "escalation-anchor-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
