#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #635, as moved by #1340:
# follow-up ticket creation inherits the original ticket's milestone and
# non-lifecycle labels, in ticket mode only, with a graceful degrade on fetch
# failure. That inheritance logic now lives in the `cenci pipeline followup`
# operation (watch/internal/pipeline/followup.go) and is covered by its Go
# tests (watch/internal/pipeline/followup_test.go). What the skills still own
# is the call: they hand the operation the items, the title and the repo slug
# through files and flags, and render its `warnings` (the inheritance warnings)
# instead of building the create payload themselves.
# Follows the fixture-free, grep-based idiom of
# flow/tests/implement-review-artifacts.test.sh: `failures` counter,
# `assert_*`/`grep -qF` helpers, plain `#!/usr/bin/env bash`.
#
# Both call sites are asserted identically:
#   - flow/skills/implement/phases/phase-9-pr.md -> ## Followup Ticket
#   - flow/skills/address-review/SKILL.md -> ## Followup Ticket for
#     Acknowledged Comments
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PHASE_9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
ADDRESS_REVIEW_SKILL="${FLOW_DIR}/skills/address-review/SKILL.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_file_lacks() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}

# extract_section <file> <exact-heading-line> -- the body under a `## ` heading
# up to the next `## ` heading (`### ` subheadings stay inside). Fence-aware: a
# `## ` look-alike inside a ``` fence neither starts nor ends a section. Pure,
# no fail() side effect, safe inside $(...).
extract_section() {
  awk -v h="$2" '
    /^```/ { infence = !infence; if (on) print; next }
    !infence && !on && $0 == h { on = 1; next }
    on && !infence && /^## / { exit }
    on { print }
  ' "$1"
}

# require_section <result-var> <file> <heading> -- fails when the file is
# unreadable or the section comes back empty (heading missing or renamed):
# every assertion on an empty section would be vacuous.
require_section() {
  local -n _section="$1"
  local _file="$2" _heading="$3" _body
  if [[ ! -f "${_file}" ]]; then
    fail "$(basename "${_file}"): file not found: ${_file}"
    _section=""
    return 1
  fi
  _body="$(extract_section "${_file}" "${_heading}")"
  if [[ -z "${_body}" ]]; then
    fail "$(basename "${_file}"): section extraction is empty (heading missing or renamed): ${_heading}"
    _section=""
    return 1
  fi
  _section="${_body}"
}

# section_contains / section_lacks <section-text> <needle> <description>
section_contains() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -n "$1" ]] || { fail "$3: section is empty (cannot contain: $2)"; return; }
  [[ "$1" == *"$2"* ]] || fail "$3 (expected to contain: $2)"
}
section_lacks() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -n "$1" ]] || { fail "$3: section is empty (a must-not-contain claim would be vacuous)"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3 (expected NOT to contain: $2)"
}

# =====================================================================
# Anchors (single source line each, per docs/shell-scripting-gotchas.md:
# keep contract-test markers on one source line so re-wrapping can't
# split the grep).
# =====================================================================
ITEMS_FILE_FLAG='--items-file'
TITLE_FILE_FLAG='--title-file'
REPO_SLUG_FLAG='--repo-slug <owner>/<repo>'
RENDER_WARNINGS='every entry of `warnings`'
# Legacy create-path text that must stay gone (inheritance moved to the operation).
HAND_ROLLED_EXCLUSION='"Refined","Working","Planned","In Review","Implemented","Design","Designed"'

# check_site <display-name> <file> <section-text>
check_site() {
  local NAME="$1" FILE="$2" SECTION="$3"

  section_contains "${SECTION}" "${ITEMS_FILE_FLAG}" \
    "${NAME} must hand the items to the operation through --items-file"
  section_contains "${SECTION}" "${TITLE_FILE_FLAG}" \
    "${NAME} must hand the title to the operation through --title-file"
  section_contains "${SECTION}" "${REPO_SLUG_FLAG}" \
    "${NAME} must pass the repository through --repo-slug <owner>/<repo>"
  section_contains "${SECTION}" "${RENDER_WARNINGS}" \
    "${NAME} must render the operation's warnings (the inheritance warnings)"
  section_lacks "${SECTION}" "${HAND_ROLLED_EXCLUSION}" \
    "${NAME} must not hand-roll the lifecycle-label exclusion list (it lives in the operation)"

  assert_file_lacks "${FILE}" "gh issue create" \
    "must not use gh issue create (the operation creates the followup)"
  assert_file_lacks "${FILE}" 'TITLE=$(cat' \
    "must not read the title back via TITLE=\$(cat ...)"
  assert_file_lacks "${FILE}" "mapfile" \
    "must not use mapfile (forbidden by shell-rules for Codex portability)"
  assert_file_lacks "${FILE}" "--slurpfile meta" \
    "must not build a create payload from a fetched meta file (the operation inherits milestone and labels)"
}

PHASE_9_SECTION=""
ADDRESS_REVIEW_SECTION=""
require_section PHASE_9_SECTION "${PHASE_9}" '## Followup Ticket'
require_section ADDRESS_REVIEW_SECTION "${ADDRESS_REVIEW_SKILL}" '## Followup Ticket for Acknowledged Comments'
check_site phase-9-pr.md "${PHASE_9}" "${PHASE_9_SECTION}"
check_site address-review/SKILL.md "${ADDRESS_REVIEW_SKILL}" "${ADDRESS_REVIEW_SECTION}"

echo "followup-ticket-inheritance.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
