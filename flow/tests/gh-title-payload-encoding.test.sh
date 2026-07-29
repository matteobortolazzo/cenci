#!/usr/bin/env bash
# Contract test for ticket #756 -- encode title-carrying `gh api` payloads
# with `jq -n --rawfile` instead of hand-escaped JSON. #740 moved refine's
# and maintain's title-carrying GitHub writes onto `gh api ... --input` with
# a Write-authored JSON literal, but the literal still required the agent to
# hand-escape ticket-controlled text ("Escape every ..."). A well-formed
# injection into that hand-escaping does not fail loudly the way malformed
# JSON does -- this ticket closes that gap by replacing hand-escaping with
# mechanical `jq -n --rawfile` composition, documented once in
# flow/skills/shell-rules/SKILL.md as the single source of truth and reused
# at all six title-carrying payload sites.
#
# Follows the fixture-free grep idiom of
# flow/tests/followup-ticket-inheritance.test.sh: `failures` counter,
# `assert_*`/`grep -qF` helpers, plain `#!/usr/bin/env bash`,
# `set -uo pipefail`. No fixtures -- greps the real committed docs directly.
# Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob; no
# registration needed.
#
# None of the production edits this test pins down exist yet at RED-phase
# time (later implementation-phase work) -- every assertion below is
# expected to fail until then, except the explicitly-noted regression
# assertion for the untouched `gh pr create --title` example, which is
# already true today and must stay true.
#
# Covered files:
#   - skills/shell-rules/SKILL.md (canonical pattern, single source of truth)
#   - skills/refine/SKILL.md (3 sites: retitle PATCH, Pass 1 child create,
#     companion design create)
#   - skills/maintain/modes/backlog.md (1 site: batch polish-ticket create)
#   - skills/implement/phases/phase-9-pr.md (1 site, 2 jq forms: with-meta,
#     no-meta)
#   - skills/address-review/SKILL.md (2 sites: followup-ticket create with 2
#     jq forms (with-meta, no-meta), plus #773's new inline-reply payload)
#   - skills/refactor/SKILL.md (1 site, #773: ticket-creation payload,
#     replacing the last live `gh issue create --title` in flow)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "gh-title-payload-encoding.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "gh-title-payload-encoding.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# assert_file_contains <file> <needle> <description>
assert_file_contains() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}

# assert_file_lacks <file> <needle> <description>
assert_file_lacks() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}

# assert_min_occurrences <file> <needle> <min-count> <description>
assert_min_occurrences() {
  local file="$1" needle="$2" min="$3" desc="$4" count
  [[ -n "${needle}" ]] || { fail "${desc}: empty needle"; return; }
  [[ -f "${file}" ]] || { fail "${desc}: file not found: ${file}"; return; }
  count="$(grep -oF -- "${needle}" "${file}" | wc -l | tr -d '[:space:]')"
  if (( count < min )); then
    fail "$(basename "${file}") ${desc} (expected >= ${min} occurrence(s) of [${needle}], found ${count})"
  fi
}

SHELL_RULES="${FLOW_DIR}/skills/shell-rules/SKILL.md"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
MAINTAIN_BACKLOG="${FLOW_DIR}/skills/maintain/modes/backlog.md"
PHASE_9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
ADDRESS_REVIEW_SKILL="${FLOW_DIR}/skills/address-review/SKILL.md"
REFACTOR_SKILL="${FLOW_DIR}/skills/refactor/SKILL.md"
SHELL_GOTCHAS_DOC="${FLOW_DIR}/docs/shell-scripting-gotchas.md"

# =====================================================================
# Canonical pattern -- single source of truth in shell-rules/SKILL.md's
# "Body Files and Heredocs" section (docs/shell-scripting-gotchas.md:10 --
# exact replacement text, not a generic marker).
# =====================================================================

CANONICAL_JQ_MARKER='jq -n --rawfile title'
CANONICAL_RAWFILE_BODY_MARKER='--rawfile body'
RTRIMSTR_MARKER='rtrimstr("\n")'
PAYLOAD_REDIRECT_MARKER='> <payload>.json'
RUN_SCOPING_MARKER='the raw title/body files and the payload file are all run-scoped by the existing'
SEPARATE_BASH_CALLS_MARKER='separate Bash calls (no pipe, no `&&`)'
PR_CREATE_CARVEOUT_MARKER='has no `--input`/`--title-file` equivalent'
PR_CREATE_EXAMPLE_MARKER='gh pr create --title'

assert_file_contains "${SHELL_RULES}" "${CANONICAL_JQ_MARKER}" \
  "must document the canonical jq -n --rawfile title invocation"
assert_file_contains "${SHELL_RULES}" "${CANONICAL_RAWFILE_BODY_MARKER}" \
  "must document --rawfile body in the canonical snippet"
assert_file_contains "${SHELL_RULES}" "${RTRIMSTR_MARKER}" \
  'must strip the file tool trailing newline from the title via rtrimstr("\n")'
assert_file_contains "${SHELL_RULES}" "${PAYLOAD_REDIRECT_MARKER}" \
  "must redirect the jq output to a <payload>.json file"
assert_file_contains "${SHELL_RULES}" "${RUN_SCOPING_MARKER}" \
  "must state the raw title/body files and the payload file are all run-scoped by the existing <scope> rule"
assert_file_contains "${SHELL_RULES}" "${SEPARATE_BASH_CALLS_MARKER}" \
  "must state the jq call and the gh api call are separate Bash calls (no pipe, no &&)"
assert_file_contains "${SHELL_RULES}" "${PR_CREATE_CARVEOUT_MARKER}" \
  "must note gh pr create has no --input/--title-file equivalent"
assert_file_contains "${SHELL_RULES}" "${PR_CREATE_EXAMPLE_MARKER}" \
  "must retain the existing gh pr create --title example verbatim (out of scope, unchanged)"

# =====================================================================
# Per-site canonical encoding -- all title-carrying gh api payload sites
# build via jq -n --rawfile, never a Write-authored JSON literal.
# refine/SKILL.md has three sites (retitle PATCH, Pass 1 child create,
# companion design create); maintain/modes/backlog.md, phase-9-pr.md, and
# address-review/SKILL.md each have one call site -- phase-9-pr.md and
# address-review/SKILL.md each document two jq forms there (with-meta,
# no-meta), so their occurrence floors are doubled. #773 adds refactor's
# ticket-creation payload (1 site) and raises address-review's floor for its
# new inline-reply payload (Q2: `-f body="$REPLY"` migrates to `jq -n
# --rawfile body` + `gh api ... --input`).
# =====================================================================

assert_min_occurrences "${REFINE_SKILL}" "jq -n" 3 \
  "must build all three payload sites (retitle PATCH, child create, companion design create) via jq -n"
assert_min_occurrences "${REFINE_SKILL}" "--rawfile" 6 \
  "must pass raw title/body via --rawfile at all three payload sites (2 per site)"

assert_min_occurrences "${MAINTAIN_BACKLOG}" "jq -n" 1 \
  "must build the batch polish-ticket payload via jq -n"
assert_min_occurrences "${MAINTAIN_BACKLOG}" "--rawfile" 2 \
  "must pass the raw title/body via --rawfile"

assert_min_occurrences "${PHASE_9}" "jq -n" 2 \
  "must document both the with-meta and no-meta jq forms"
assert_min_occurrences "${PHASE_9}" "--rawfile" 4 \
  "must pass raw title/body via --rawfile in both jq forms"

assert_min_occurrences "${ADDRESS_REVIEW_SKILL}" "jq -n" 2 \
  "must document both the with-meta and no-meta jq forms"
assert_min_occurrences "${ADDRESS_REVIEW_SKILL}" "--rawfile" 5 \
  "must pass raw title/body via --rawfile in both followup jq forms plus the new inline-reply payload (#773)"

assert_min_occurrences "${REFACTOR_SKILL}" "jq -n" 1 \
  "must build the ticket-creation payload via jq -n (#773, replaces gh issue create --title)"
assert_min_occurrences "${REFACTOR_SKILL}" "--rawfile" 2 \
  "must pass the raw title/body via --rawfile (#773)"

# =====================================================================
# Hardening sweep -- no file under flow/skills/ or flow/docs/ may retain a
# hand-escaping instruction for a gh api --input payload. This is the
# vulnerability the ticket closes: a well-formed injection into a
# hand-escaped payload does not fail loudly the way malformed JSON does.
# =====================================================================

ESCAPE_MARKER='Escape every'
SKILLS_DIR="${FLOW_DIR}/skills"
DOCS_DIR="${FLOW_DIR}/docs"
if [[ ! -d "${SKILLS_DIR}" ]]; then
  fail "flow/skills directory not found: ${SKILLS_DIR}"
else
  ESCAPE_HITS="$(grep -rlF -- "${ESCAPE_MARKER}" "${SKILLS_DIR}" 2>/dev/null)"
  if [[ -n "${ESCAPE_HITS}" ]]; then
    fail "no file under flow/skills/ may retain a hand-escaping (\"Escape every\") instruction; found in: ${ESCAPE_HITS}"
  fi
fi
if [[ ! -d "${DOCS_DIR}" ]]; then
  fail "flow/docs directory not found: ${DOCS_DIR}"
else
  ESCAPE_HITS_DOCS="$(grep -rlF -- "${ESCAPE_MARKER}" "${DOCS_DIR}" 2>/dev/null)"
  if [[ -n "${ESCAPE_HITS_DOCS}" ]]; then
    fail "no file under flow/docs/ may retain a hand-escaping (\"Escape every\") instruction; found in: ${ESCAPE_HITS_DOCS}"
  fi
fi

# =====================================================================
# Repo-wide sweep -- no flow/**/*.md file may reintroduce the vulnerable
# `gh issue create` pattern this ticket (and #773's refactor site) moved
# off of (title interpolated directly into a command line rather than via
# jq --rawfile and gh api --input). The marker is the bare `gh issue create`
# verb pair, not `gh issue create --title` -- refactor/SKILL.md's live site
# interleaves `--repo <owner>/<repo>` between `create` and `--title`
# (`gh issue create --repo <owner>/<repo> --title "refactor: <title>" ...`),
# so the narrower `--title`-suffixed marker never caught it (#773). Two
# files are excluded, each for the same reason: their one occurrence is
# inline-code prose naming the retired anti-pattern to explain why it's
# unsafe, not a live example to copy -- the same kind of intentional,
# non-executable carve-out as PR_CREATE_EXAMPLE_MARKER above.
#   - shell-rules/SKILL.md (line ~52)
#   - docs/shell-scripting-gotchas.md (documents this exact sweep's own
#     carve-out convention, and so necessarily names the retired pattern
#     as an example)
# =====================================================================

ISSUE_CREATE_MARKER='gh issue create'
ISSUE_CREATE_HITS="$(grep -rlF -- "${ISSUE_CREATE_MARKER}" "${SKILLS_DIR}" "${DOCS_DIR}" 2>/dev/null | grep -vF -- "${SHELL_RULES}" | grep -vF -- "${SHELL_GOTCHAS_DOC}")"
if [[ -n "${ISSUE_CREATE_HITS}" ]]; then
  fail "no file under flow/skills/ or flow/docs/ may reintroduce \"gh issue create\"; found in: ${ISSUE_CREATE_HITS}"
fi

echo "gh-title-payload-encoding.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
