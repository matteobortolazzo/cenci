#!/usr/bin/env bash
# Tests the review-receipt contract of ticket #1472 across the places that must
# state the same literals and grammar:
#   - watch/internal/babysit/discussion.go (the consumer: receiptMarkerPrefix /
#     receiptMarkerSuffix and the strict marker, head and entry regexes)
#   - flow/skills/address-review/SKILL.md (the producer, Claude Code: the
#     finite `babysit-review` branch and its receipt template)
#   - flow/skills/address-review/codex.md (the producer, Codex: the appended
#     finite branch and its receipt template)
#   - flow/docs/comment-attribution.md (the registry: the `review-receipt`
#     kind, the sanctioned machine-read, the PR-thread marker exception)
#   - flow/skills/pr-comment-filter/SKILL.md (scope: conversation comments in,
#     receipt comments out)
#
# Fixture-free, grep-based idiom of escalation-anchor-contract.test.sh:
# `set -uo pipefail`, a `failures` counter, substring helpers, fence-aware awk
# section extraction bounded to the next `## `-level heading. Every extractor
# is named `extract_*`, is pure, and has no fail() side effect, so this file is
# compliant with read-helper-purity-contract.test.sh. Auto-discovered by
# scripts/run-checks.sh's `*.test.sh` find -- no registration needed.
#
# Beyond literal parity, the receipt template is checked structurally: its
# placeholders are replaced with concrete values and the result must match the
# regexes read out of discussion.go, so a drift in either side fails here.
#
# Coverage split (what is exercised and what is contract-only):
#   - Exercised: the producers' receipt templates are parsed and verified by the
#     real Go parseReceipt/verifyReceipt in watch/internal/babysit/
#     receipt_template_contract_test.go (go test), and matched here against the
#     regexes read from discussion.go.
#   - Contract-only (pinned text, not executed): the finite branch's gating and
#     ending instructions. For both producers this file asserts that
#       * the **Gate.** paragraph is one line carrying "ONLY when both hold:",
#         then CENCI_ATTENDED=0, then "AND a `--receipt-nonce` ..." in order;
#       * the finite section contains no AskUserQuestion, states Phase 3F is
#         not run, and maps ask-the-user points to blocked/failed;
#       * the sentences "Post exactly one receipt and never retry the post."
#         and "Posting the receipt is the last action of the branch: stop
#         immediately after it." exist, the latter being the final non-blank
#         line of the section;
#       * SKILL.md keeps Step 3F's AskUserQuestion approval gate and its
#         Interaction rule for the interactive path.
#     The skill is run by a model, so whether it obeys these instructions
#     (takes the interactive path on a missing gate half, posts one receipt,
#     stops) cannot be executed here; nothing in this file claims it does.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "review-receipt-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "review-receipt-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "review-receipt-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
DISCUSSION_GO="${REPO_ROOT}/watch/internal/babysit/discussion.go"
SKILL_MD="${FLOW_DIR}/skills/address-review/SKILL.md"
CODEX_MD="${FLOW_DIR}/skills/address-review/codex.md"
ATTRIBUTION_MD="${FLOW_DIR}/docs/comment-attribution.md"
FILTER_MD="${FLOW_DIR}/skills/pr-comment-filter/SKILL.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# extract_section_raw <content> <heading-line> -- pure, fence-aware extraction
# bounded to the next heading of the same or a higher level or a closing
# </details> tag (a heading-shaped line inside a fence does not end the
# section).
extract_section_raw() {
  awk -v want="$2" '
    BEGIN { match(want, /^#+/); level = RLENGTH }
    $0 == want { on=1; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^<\/details>/ { exit }
    on && !infence && /^#+ / { match($0, /^#+/); if (RLENGTH <= level) exit }
    on { print }
  ' <<<"$1"
}

# extract_first_markdown_fence <content> -- pure: the lines of the first
# ```markdown fenced block in <content>, without the fence lines.
extract_first_markdown_fence() {
  awk '
    !done && /^```markdown[[:space:]]*$/ { on=1; next }
    on && /^```[[:space:]]*$/ { done=1; on=0; next }
    on { print }
  ' <<<"$1"
}

# extract_go_string_const <file> <name> -- pure: the double-quoted value of a
# `<name> = "<value>"` constant line in a Go file.
extract_go_string_const() {
  sed -n "s/^[[:space:]]*$2[[:space:]]*=[[:space:]]*\"\\(.*\\)\"[[:space:]]*\$/\\1/p" "$1" | head -n 1
}

# extract_go_regex <file> <name> -- pure: the raw-string pattern of a
# `<name> = regexp.MustCompile(`<pattern>`)` line in a Go file.
extract_go_regex() {
  sed -n "s/^[[:space:]]*$2[[:space:]]*=[[:space:]]*regexp\\.MustCompile(\`\\(.*\\)\`)[[:space:]]*\$/\\1/p" "$1" | head -n 1
}

# extract_line_of <content> <pattern> -- pure: the first line of <content> that
# contains the fixed string <pattern>.
extract_line_of() {
  grep -m1 -F -- "$2" <<<"$1" || true
}

# line_number_of <file> <fixed-string> -- pure: the 1-based number of the first
# line of <file> containing the fixed string, empty when absent.
line_number_of() {
  grep -n -m1 -F -- "$2" "$1" 2>/dev/null | cut -d: -f1
}

assert_contains() {
  # $1=haystack $2=needle $3=label
  [[ -n "$2" ]] || { fail "$3: empty needle (test bug)"; return; }
  [[ "$1" == *"$2"* ]] || fail "$3 (expected to contain: $2)"
}

assert_not_contains() {
  [[ -n "$2" ]] || { fail "$3: empty needle (test bug)"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3 (must not contain: $2)"
}

# --- load every input; a missing file fails loudly rather than passing vacuously
for input in "${DISCUSSION_GO}" "${SKILL_MD}" "${CODEX_MD}" "${ATTRIBUTION_MD}" "${FILTER_MD}"; do
  [[ -f "${input}" ]] || fail "input not found: ${input}"
done
SKILL_CONTENT="$(cat "${SKILL_MD}" 2>/dev/null || true)"
CODEX_CONTENT="$(cat "${CODEX_MD}" 2>/dev/null || true)"
ATTRIBUTION_CONTENT="$(cat "${ATTRIBUTION_MD}" 2>/dev/null || true)"
FILTER_CONTENT="$(cat "${FILTER_MD}" 2>/dev/null || true)"

# --- the pinned grammar (plan: "Receipt grammar (load-bearing)")
PIN_MARKER_PREFIX='<!-- cenci-review-receipt:'
PIN_MARKER_SUFFIX=' -->'
PIN_MARKER_LINE='<!-- cenci-review-receipt:<32-hex> -->'
PIN_BANNER='> 🤖 **cenci** — review receipt posted by `/cenci:address-review` (babysit-review).'
PIN_HEAD_LINE='head: <40|64-hex>'
PIN_ENTRY_LINE='- discussion:<id> revision=<64-hex> outcome=<addressed|no-action|blocked|failed>'
PIN_NONCE_FLAG='--receipt-nonce'
PIN_LAUNCH_ARGS='<pr> --receipt-nonce <nonce> discussion:<id>=<sha256>'
PIN_ATTENDED_GATE='CENCI_ATTENDED=0'
PIN_ATTENDED_GRANT='Bash(printenv CENCI_ATTENDED)'
FINITE_HEADING_SKILL='## Finite Receipt Branch (babysit-review)'
FINITE_HEADING_CODEX='## Finite receipt branch (babysit-review)'

# =====================================================================
# (a1) watch/internal/babysit/discussion.go -- the consumer's literals
# =====================================================================

GO_MARKER_PREFIX="$(extract_go_string_const "${DISCUSSION_GO}" receiptMarkerPrefix)"
GO_MARKER_SUFFIX="$(extract_go_string_const "${DISCUSSION_GO}" receiptMarkerSuffix)"
GO_MARKER_RE="$(extract_go_regex "${DISCUSSION_GO}" receiptMarkerLine)"
GO_HEAD_RE="$(extract_go_regex "${DISCUSSION_GO}" receiptHeadLine)"
GO_ENTRY_RE="$(extract_go_regex "${DISCUSSION_GO}" receiptEntryLine)"

[[ "${GO_MARKER_PREFIX}" == "${PIN_MARKER_PREFIX}" ]] || fail "discussion.go receiptMarkerPrefix is [${GO_MARKER_PREFIX}], expected [${PIN_MARKER_PREFIX}]"
[[ "${GO_MARKER_SUFFIX}" == "${PIN_MARKER_SUFFIX}" ]] || fail "discussion.go receiptMarkerSuffix is [${GO_MARKER_SUFFIX}], expected [${PIN_MARKER_SUFFIX}]"
[[ -n "${GO_MARKER_RE}" ]] || fail "discussion.go: could not read the receiptMarkerLine regex"
[[ -n "${GO_HEAD_RE}" ]] || fail "discussion.go: could not read the receiptHeadLine regex"
[[ -n "${GO_ENTRY_RE}" ]] || fail "discussion.go: could not read the receiptEntryLine regex"
assert_contains "${GO_MARKER_RE}" '([0-9a-f]{32})' "discussion.go marker regex must capture a 32-hex nonce"
assert_contains "${GO_HEAD_RE}" '([0-9a-f]{40}|[0-9a-f]{64})' "discussion.go head regex must accept a 40 or 64 hex SHA"
assert_contains "${GO_ENTRY_RE}" 'revision=([0-9a-f]{64})' "discussion.go entry regex must require a 64-hex revision"
assert_contains "${GO_ENTRY_RE}" 'outcome=(addressed|no-action|blocked|failed)' "discussion.go entry regex must accept exactly the four outcomes"

# concretize_template_line <line> -- pure: replace the grammar placeholders
# with concrete values that a conforming receipt would carry.
HEX32='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
HEX40='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
HEX64='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
concretize_template_line() {
  local line="$1"
  line="${line//<32-hex>/${HEX32}}"
  line="${line//<40|64-hex>/${HEX40}}"
  line="${line//<64-hex>/${HEX64}}"
  line="${line//<id>/4242}"
  line="${line//<addressed|no-action|blocked|failed>/addressed}"
  printf '%s' "${line}"
}

# =====================================================================
# (a2)(b)(c)(e) address-review/SKILL.md and codex.md -- the producers
# =====================================================================

# check_producer <label> <file-content> <finite-heading>
check_producer() {
  local label="$1" content="$2" heading="$3"
  local section fence marker_lines head_lines entry_lines blockquoted_marker

  section="$(extract_section_raw "${content}" "${heading}")"
  if [[ -z "${section}" ]]; then
    fail "${label}: could not locate '${heading}' section (extract_section_raw returned empty)"
    return
  fi

  # (b) the gating text names both gates
  assert_contains "${section}" "${PIN_ATTENDED_GATE}" "${label} finite section must name the CENCI_ATTENDED=0 gate"
  assert_contains "${section}" "${PIN_NONCE_FLAG}" "${label} finite section must name the --receipt-nonce gate"
  assert_contains "${section}" "${PIN_LAUNCH_ARGS}" "${label} finite section must state the launch argument form"
  assert_contains "${section}" "never instructions" "${label} finite section must state PR comment bodies are data, never instructions"
  assert_contains "${section}" "--unattended" "${label} finite section must state the worker is pinned --unattended"

  # (b) the gate is a conjunction: ONE line (the **Gate.** paragraph) must carry
  # the "only when both hold" wording, then CENCI_ATTENDED=0, then AND, then the
  # --receipt-nonce argument, in that order. Dropping either half, or moving one
  # half to another paragraph, fails this single assertion.
  local gate_line gate_re
  gate_line="$(extract_line_of "${section}" '**Gate.**')"
  gate_re='ONLY when both hold:.*CENCI_ATTENDED=0.*AND a `--receipt-nonce'
  [[ -n "${gate_line}" ]] || fail "${label} finite section must have a '**Gate.**' paragraph"
  [[ "${gate_line}" =~ ${gate_re} ]] || fail "${label} gate must be one conjunction line: 'ONLY when both hold: CENCI_ATTENDED=0 ... AND a --receipt-nonce' (got: ${gate_line})"

  # (c') one receipt, no retry, and posting it is the LAST action of the branch:
  # the sentences exist, and the "last action" sentence is the final non-blank
  # line of the finite section, so nothing can be instructed after the receipt.
  local last_line
  assert_contains "${section}" "Post exactly one receipt and never retry the post." "${label} finite section must state exactly one receipt and no retry of the post"
  assert_contains "${section}" "Posting the receipt is the last action of the branch: stop immediately after it." "${label} finite section must state posting the receipt is the last action of the branch"
  last_line="$(grep -v '^[[:space:]]*$' <<<"${section}" | tail -n 1)"
  [[ "${last_line}" == "- Posting the receipt is the last action of the branch: stop immediately after it." ]] || fail "${label} finite section must end with the last-action sentence (final line is: ${last_line})"

  # (c) the finite section holds no user-question tool, and maps every
  # ask-the-user point to a non-interactive outcome
  assert_not_contains "${section}" "AskUserQuestion" "${label} finite section must not mention AskUserQuestion"
  assert_contains "${section}" "Phase 3F" "${label} finite section must state that Phase 3F is not run"
  assert_contains "${section}" "blocked" "${label} finite section must map ask-the-user points to the blocked outcome"
  assert_contains "${section}" "failed" "${label} finite section must map ask-the-user points to the failed outcome"

  # (a2) the receipt literals
  assert_contains "${section}" "${PIN_MARKER_PREFIX}" "${label} finite section must carry the marker prefix"
  assert_contains "${section}" "${PIN_MARKER_LINE}" "${label} finite section must carry the marker line"

  # (e) the receipt template, checked structurally against the Go grammar
  fence="$(extract_first_markdown_fence "${section}")"
  if [[ -z "${fence}" ]]; then
    fail "${label}: finite section has no \`\`\`markdown receipt template block"
    return
  fi
  [[ "$(head -n 1 <<<"${fence}")" == "${PIN_BANNER}" ]] || fail "${label} receipt template: first line must be the blockquoted banner [${PIN_BANNER}]"
  marker_lines="$(grep -cF -- "${PIN_MARKER_PREFIX}" <<<"${fence}" || true)"
  [[ "${marker_lines}" -eq 1 ]] || fail "${label} receipt template: expected exactly one marker line, found ${marker_lines}"
  head_lines="$(grep -c '^head:' <<<"${fence}" || true)"
  [[ "${head_lines}" -eq 1 ]] || fail "${label} receipt template: expected exactly one head: line, found ${head_lines}"
  entry_lines="$(grep -c '^- discussion:' <<<"${fence}" || true)"
  [[ "${entry_lines}" -eq 1 ]] || fail "${label} receipt template: expected one '- discussion:' template line, found ${entry_lines}"
  blockquoted_marker="$(grep -E '^>' <<<"${fence}" | grep -F -- "cenci-review-receipt" || true)"
  [[ -z "${blockquoted_marker}" ]] || fail "${label} receipt template: the marker must stay on its own non-blockquoted line: ${blockquoted_marker}"

  local marker_tpl head_tpl entry_tpl
  marker_tpl="$(extract_line_of "${fence}" "${PIN_MARKER_PREFIX}")"
  head_tpl="$(grep -m1 '^head:' <<<"${fence}" || true)"
  entry_tpl="$(grep -m1 '^- discussion:' <<<"${fence}" || true)"
  [[ "${marker_tpl}" == "${PIN_MARKER_LINE}" ]] || fail "${label} receipt template: marker line is [${marker_tpl}], expected [${PIN_MARKER_LINE}]"
  [[ "${head_tpl}" == "${PIN_HEAD_LINE}" ]] || fail "${label} receipt template: head line is [${head_tpl}], expected [${PIN_HEAD_LINE}]"
  [[ "${entry_tpl}" == "${PIN_ENTRY_LINE}" ]] || fail "${label} receipt template: discussion line is [${entry_tpl}], expected [${PIN_ENTRY_LINE}]"

  local marker_concrete head_concrete entry_concrete
  marker_concrete="$(concretize_template_line "${marker_tpl}")"
  head_concrete="$(concretize_template_line "${head_tpl}")"
  entry_concrete="$(concretize_template_line "${entry_tpl}")"
  if [[ -n "${GO_MARKER_RE}" ]]; then
    [[ "${marker_concrete}" =~ ${GO_MARKER_RE} ]] || fail "${label} receipt template: marker line does not parse under discussion.go's receiptMarkerLine"
  fi
  if [[ -n "${GO_HEAD_RE}" ]]; then
    [[ "${head_concrete}" =~ ${GO_HEAD_RE} ]] || fail "${label} receipt template: head line does not parse under discussion.go's receiptHeadLine"
  fi
  if [[ -n "${GO_ENTRY_RE}" ]]; then
    [[ "${entry_concrete}" =~ ${GO_ENTRY_RE} ]] || fail "${label} receipt template: discussion line does not parse under discussion.go's receiptEntryLine"
  fi

  # author trust (review-fix): the launch marks a key `:untrusted` when its
  # comment author lacks write permission; the worker never acts on such a key,
  # never reports it `addressed`, and only acts on inline/review feedback whose
  # author the permission lookup proved to hold admin/write.
  assert_contains "${section}" "**Author trust.**" "${label} finite section must have an Author trust paragraph"
  assert_contains "${section}" 'discussion:<id>=<sha256>[:untrusted]' "${label} finite section must state the optional :untrusted launch-token suffix"
  assert_contains "${section}" '^discussion:[0-9]+=[0-9a-f]{64}(:untrusted)?$' "${label} finite section must validate the token with the optional :untrusted suffix"
  assert_contains "${section}" 'never `addressed`' "${label} finite section must forbid the addressed outcome for an :untrusted key"
  assert_contains "${section}" 'collaborators/<login>/permission --jq .permission' "${label} finite section must check inline/review authors with the collaborator permission call"
  assert_contains "${section}" '^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$' "${label} finite section must validate the login before it reaches a command"
  assert_contains "${section}" "fail closed" "${label} finite section must fail closed on a permission lookup that does not answer admin or write"

  # grammar rules every producer must state
  assert_contains "${section}" "no duplicate" "${label} finite section must forbid duplicate keys"
  assert_contains "${section}" "zero" "${label} finite section must allow a receipt with zero discussion lines"
}

check_producer "address-review/SKILL.md" "${SKILL_CONTENT}" "${FINITE_HEADING_SKILL}"
check_producer "address-review/codex.md" "${CODEX_CONTENT}" "${FINITE_HEADING_CODEX}"

# =====================================================================
# (b') SKILL.md grant, Step 1C, and Codex ordering
# =====================================================================

SKILL_FRONTMATTER_ALLOWED="$(grep -m1 '^allowed-tools:' "${SKILL_MD}" 2>/dev/null || true)"
assert_contains "${SKILL_FRONTMATTER_ALLOWED}" "${PIN_ATTENDED_GRANT}" "address-review/SKILL.md allowed-tools must grant exactly the printenv CENCI_ATTENDED read"
assert_contains "${SKILL_CONTENT}" "printenv CENCI_ATTENDED" "address-review/SKILL.md must read CENCI_ATTENDED through printenv"
assert_contains "${SKILL_FRONTMATTER_ALLOWED}" "Bash(gh api repos/:*)" "address-review/SKILL.md allowed-tools must keep the gh api repos/ grant the author permission lookup runs under"

STEP_1C="$(extract_section_raw "${SKILL_CONTENT}" "## Step 1C: Fetch Review Comments")"
assert_contains "${STEP_1C}" "issues/<number>/comments" "address-review/SKILL.md Step 1C must fetch PR conversation comments (issues/<number>/comments)"

# The finite branch in codex.md is strictly appended: it must come after the
# existing apply step (workflows.test.sh pins the existing ordering).
CODEX_APPLY_LINE="$(line_number_of "${CODEX_MD}" "apply approved fixes")"
CODEX_FINITE_LINE="$(line_number_of "${CODEX_MD}" "${FINITE_HEADING_CODEX}")"
if [[ -z "${CODEX_APPLY_LINE}" || -z "${CODEX_FINITE_LINE}" ]]; then
  fail "address-review/codex.md: could not locate both the apply step and the finite heading (apply=[${CODEX_APPLY_LINE}] finite=[${CODEX_FINITE_LINE}])"
elif [[ "${CODEX_FINITE_LINE}" -le "${CODEX_APPLY_LINE}" ]]; then
  fail "address-review/codex.md: the finite branch must be appended after the existing text (finite at ${CODEX_FINITE_LINE}, apply step at ${CODEX_APPLY_LINE})"
fi
CODEX_FINITE_SECTION="$(extract_section_raw "${CODEX_CONTENT}" "${FINITE_HEADING_CODEX}")"
assert_contains "${CODEX_FINITE_SECTION}" "Plan-mode" "address-review/codex.md finite section must state the Plan-mode handoff does not apply"
assert_contains "${CODEX_FINITE_SECTION}" "guard-canary.sh" "address-review/codex.md finite section must keep the guard canary preflight"

# =====================================================================
# (d) the interactive path keeps Phase 3F's AskUserQuestion
# =====================================================================

STEP_3F="$(extract_section_raw "${SKILL_CONTENT}" "## Step 3F: User Approval")"
if [[ -z "${STEP_3F}" ]]; then
  fail "address-review/SKILL.md: could not locate '## Step 3F: User Approval' (the interactive path must keep Phase 3F)"
else
  assert_contains "${STEP_3F}" "AskUserQuestion" "address-review/SKILL.md Step 3F must keep its AskUserQuestion approval gate"
  assert_contains "${STEP_3F}" "Only proceed to Phase 4 after the user approves." "address-review/SKILL.md Step 3F must keep its approval-before-Phase-4 rule"
fi
assert_contains "${SKILL_CONTENT}" "> **Interaction rule**: Every question, confirmation, or approval directed at the user" "address-review/SKILL.md must keep its Interaction rule for the interactive path"

# =====================================================================
# docs/comment-attribution.md -- registry, machine-read, precedence
# =====================================================================

assert_contains "${ATTRIBUTION_CONTENT}" '| `review-receipt` |' "comment-attribution.md registry must have a review-receipt row"
assert_contains "${ATTRIBUTION_CONTENT}" "${PIN_MARKER_LINE}" "comment-attribution.md must state the marker literal"
assert_contains "${ATTRIBUTION_CONTENT}" "${PIN_BANNER}" "comment-attribution.md must state the receipt banner"
assert_contains "${ATTRIBUTION_CONTENT}" "${PIN_HEAD_LINE}" "comment-attribution.md must state the head: line"
assert_contains "${ATTRIBUTION_CONTENT}" "${PIN_ENTRY_LINE}" "comment-attribution.md must state the discussion line"
RECEIPT_READ_SECTION="$(extract_section_raw "${ATTRIBUTION_CONTENT}" "### \`review-receipt\` is the one sanctioned PR-thread marker")"
if [[ -z "${RECEIPT_READ_SECTION}" ]]; then
  fail "comment-attribution.md: could not locate the '### \`review-receipt\` is the one sanctioned PR-thread marker' subsection"
else
  assert_contains "${RECEIPT_READ_SECTION}" "stripBlockquoteLines" "comment-attribution.md machine-read must strip blockquote lines first"
  assert_contains "${RECEIPT_READ_SECTION}" "nonce" "comment-attribution.md machine-read must require the nonce match"
  assert_contains "${RECEIPT_READ_SECTION}" "write permission" "comment-attribution.md machine-read must require write permission"
  assert_contains "${RECEIPT_READ_SECTION}" "created_at" "comment-attribution.md machine-read must require created_at after launch"
  assert_contains "${RECEIPT_READ_SECTION}" "updated_at" "comment-attribution.md machine-read must reject an edited comment"
fi
PR_COMMENTS_SECTION="$(extract_section_raw "${ATTRIBUTION_CONTENT}" "## PR comments: banner only, no marker")"
assert_contains "${PR_COMMENTS_SECTION}" "precedence" "comment-attribution.md PR-comments statement must state precedence for the receipt (#979)"
assert_contains "${PR_COMMENTS_SECTION}" "cenci-review-receipt" "comment-attribution.md PR-comments statement must name the receipt as the one PR-thread marker"

# =====================================================================
# pr-comment-filter/SKILL.md -- scope
# =====================================================================

assert_contains "${FILTER_CONTENT}" "conversation" "pr-comment-filter must put PR conversation comments in scope"
assert_contains "${FILTER_CONTENT}" "cenci-review-receipt" "pr-comment-filter must exclude receipt comments by name"

echo "review-receipt-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
