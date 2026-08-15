#!/usr/bin/env bash
# Contract test for ticket #1048 — fragment fixes never reach repos with a
# committed .cenci/Dockerfile, and nothing detects the drift.
#
# Half of #1048's fix (the launcher-side detector in
# watch/internal/sandbox/launcher/fragmentdrift.go) can only ever catch a
# marker-less managed block via the legacy banner-line fallback (decision
# constraint 4). Going forward, `/cenci:configure` must wrap each fragment it
# writes into a repo's `.cenci/Dockerfile` in a per-fragment
# `# cenci:fragment-begin <name>` / `# cenci:fragment-end <name>` marker pair
# so identification becomes exact. This suite pins that step 5e instruction
# in flow/skills/configure/SKILL.md, plus the corrected remedy wording at
# SKILL.md's Q9b (`:510`), which used to tell the user that a bare
# `cenci sandbox build` would pick up a fragment fix — false for a per-repo
# image, since BuildRepoImage never reads sandbox/fragments/*.dockerfile at
# all. The remedy must name `/cenci:configure` first, then
# `cenci sandbox build`, matching the already-correct wording at
# sandbox/README.md:693.
#
# Follows the exact idiom of flow/tests/configure-autonomy-questions.test.sh:
# pinned exact authored substrings as constants (the red phase fails for the
# right reason, and green has an unambiguous authoring target — never derive
# strings from the doc under test), a `require_doc` nameref helper, `fail()`
# counter, no fixtures, `assert_absent_paired`/`assert_absent_paired_ws` for
# non-vacuous absence checks (a bare `assert_not_contains` on a stale-remedy
# sentence would pass vacuously if the surrounding question were itself
# missing or renamed) per flow/docs/shell-scripting-gotchas.md's read-helper-
# purity rule. Never calls `fail()` inside `$(...)` — every extractor is a
# pure function, invoked in `$(...)` only for its stdout, with the caller
# checking the result and calling `fail()` in the parent shell.
#
# Covered files:
#   - flow/skills/configure/SKILL.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "configure-fragment-markers.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "configure-fragment-markers.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, no fail() side
  # effect here: it is deliberately safe to call inside a $(...) command
  # substitution.
  local _relpath="$1"
  local _path="${FLOW_DIR}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing file must never masquerade as empty content, which would make
# assert_not_contains trivially pass). Must NOT be invoked via $(...).
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

# assert_contains <content> <required-substring> <label>
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

normalize_ws() {
  # normalize_ws <content> — collapses newlines and repeated whitespace to a
  # single space so a markdown-wrapped sentence can be matched as one
  # substring, per docs/shell-scripting-gotchas.md's line-wrapping pitfall.
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws <content> <required-substring> <label> — whitespace-
# normalized variant of assert_contains, for sentences that may be
# line-wrapped in the source markdown.
assert_contains_ws() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# assert_absent_paired_ws <content> <existence-marker> <forbidden-substring> <label>
#
# A bare assert_not_contains on a stale-remedy sentence would pass vacuously
# before the surrounding question exists at all — it would prove nothing
# about this ticket. Requiring the marker's own existence first means this
# only proves the intended thing (the question exists AND deliberately no
# longer carries the stale, misleading remedy), and fails loudly — for the
# right reason — while the fix is still unauthored (this red phase). Mirrors
# configure-autonomy-questions.test.sh's helper of the same name.
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  local norm
  norm="$(normalize_ws "${content}")"
  if [[ "${norm}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- existence marker missing (whitespace-normalized): [${marker}]"
    return
  fi
  [[ "${norm}" != *"${forbidden}"* ]] || fail "${label}: forbidden text present (whitespace-normalized): [${forbidden}]"
}

# --- Exact authored substrings Phase 4 (green) must add verbatim -----------
#
# These are pinned here, not derived, so the red phase fails for the right
# reason and the green phase has an unambiguous authoring target.

# Step 5e existence marker — scopes the per-fragment-marker checks below so
# they cannot pass vacuously if step 5e itself is missing or renumbered.
STEP5E_MARKER='5e. **Generate `.cenci/Dockerfile`**'

# Per-fragment markers: `# cenci:fragment-begin <name>` / `-end <name>`,
# where <name> is the fragment file's basename without `.dockerfile` (Files
# to Modify, ticket #1048 plan). These are the exact mechanism the launcher's
# fragment-drift detector prefers over the legacy banner-line fallback.
FRAGMENT_MARKER_BEGIN='`# cenci:fragment-begin <name>`'
FRAGMENT_MARKER_END='`# cenci:fragment-end <name>`'
FRAGMENT_MARKER_NAME_RULE='`<name>` is the fragment file'"'"'s basename without `.dockerfile`'
FRAGMENT_MARKER_WRAP_INSTRUCTION='wrap each fragment'"'"'s content in'

# Q9b existence marker — scopes the remedy-wording check below.
Q9B_MARKER='9b. **Nested Docker (dind)**'

# Stale remedy this ticket must remove: it implies a bare `cenci sandbox
# build` alone picks up a fragment fix, which is false for a per-repo image
# (BuildRepoImage never reads sandbox/fragments/*.dockerfile — ticket
# #1048's root cause).
OLD_REMEDY='Tell the user to run `cenci sandbox build` to pick the fragment up.'

# Corrected remedy: names `/cenci:configure` first, then `cenci sandbox
# build` — matching the already-correct wording at sandbox/README.md:693
# ("Fix it by re-running `/cenci:configure` and then `cenci sandbox
# build`.").
NEW_REMEDY='Tell the user to re-run `/cenci:configure` and then `cenci sandbox build` to pick the fragment up.'

# --- skills/configure/SKILL.md ----------------------------------------------

require_doc skill "skills/configure/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  # Step 5e must exist at all — every check below is scoped/paired against it.
  assert_contains "${skill}" "${STEP5E_MARKER}" "SKILL.md step 5e (Generate .cenci/Dockerfile) present"

  # Per-fragment marker emission instruction.
  assert_contains "${skill}" "${FRAGMENT_MARKER_BEGIN}" "SKILL.md step 5e emits # cenci:fragment-begin <name>"
  assert_contains "${skill}" "${FRAGMENT_MARKER_END}" "SKILL.md step 5e emits # cenci:fragment-end <name>"
  assert_contains_ws "${skill}" "${FRAGMENT_MARKER_NAME_RULE}" "SKILL.md step 5e states <name> is the fragment's basename without .dockerfile"
  assert_contains_ws "${skill}" "${FRAGMENT_MARKER_WRAP_INSTRUCTION}" "SKILL.md step 5e instructs wrapping each fragment's content in the markers"

  # Q9b existence + corrected remedy wording, paired so the absence check
  # can't pass vacuously before the question itself exists.
  assert_contains "${skill}" "${Q9B_MARKER}" "SKILL.md question 9b (Nested Docker / dind) present"
  assert_absent_paired_ws "${skill}" "${Q9B_MARKER}" "${OLD_REMEDY}" "SKILL.md stale cenci-sandbox-build-alone remedy removed"
  assert_contains_ws "${skill}" "${NEW_REMEDY}" "SKILL.md corrected remedy names /cenci:configure before cenci sandbox build"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-fragment-markers.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-fragment-markers.test.sh: all checks passed."
