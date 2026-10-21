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
#   - flow/skills/configure/SKILL.md (step 5e)
#   - flow/skills/configure/phases/runtime-ci-sandbox-questions.md (Q9b, #1341)
#   - flow/skills/configure/phases/autonomy-settings.md (the five jq marker
#     blocks and the automerge scaffold, byte for byte, #1342 P5)
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

fi

# --- skills/configure/phases/runtime-ci-sandbox-questions.md ----------------
# Question 9b moved out of SKILL.md into this phase file (#1341); step 5e
# stays inline above.

require_doc runtime_phase "skills/configure/phases/runtime-ci-sandbox-questions.md" || true
if [[ -n "${runtime_phase}" ]]; then
  # Q9b existence + corrected remedy wording, paired so the absence check
  # can't pass vacuously before the question itself exists.
  assert_contains "${runtime_phase}" "${Q9B_MARKER}" "runtime-ci-sandbox-questions.md question 9b (Nested Docker / dind) present"
  assert_absent_paired_ws "${runtime_phase}" "${Q9B_MARKER}" "${OLD_REMEDY}" "runtime-ci-sandbox-questions.md stale cenci-sandbox-build-alone remedy removed"
  assert_contains_ws "${runtime_phase}" "${NEW_REMEDY}" "runtime-ci-sandbox-questions.md corrected remedy names /cenci:configure before cenci sandbox build"
else
  fail "runtime-ci-sandbox-questions.md: empty, assertions skipped"
fi

# --- (P5) The five jq marker blocks and the scaffold, byte for byte ----------
#
# #1342 moves question 13-17's marker-bounded programs (and the
# `cenci:automerge-scaffold` block that moves with question 14) out of
# SKILL.md into phases/autonomy-settings.md. Step 6 and codex.md consume them
# by marker name, so a single changed byte silently changes what every future
# configure run writes. Each block below is pinned from the pre-move SKILL.md
# INCLUDING its fence lines and both marker comments. Each must occur exactly
# once across every configure markdown file, live in phases/autonomy-settings.md
# and nowhere in SKILL.md, and each program is extracted and RUN with jq.

IFS= read -r -d '' P5_Q13_BLOCK <<'PINNED_BLOCK'
<!-- cenci:configure-q13-switch:begin -->
```jq
.planning = ((.planning | if type == "object" then . else {} end) + {autonomy: $autonomy})
```
<!-- cenci:configure-q13-switch:end -->
PINNED_BLOCK
P5_Q13_BLOCK="${P5_Q13_BLOCK%$'\n'}"

IFS= read -r -d '' P5_Q14_BLOCK <<'PINNED_BLOCK'
<!-- cenci:configure-q14-remove:begin -->
```jq
del(.automerge) | if (.projects | type) == "array" then .projects |= map(if type == "object" then del(.automerge) else . end) else . end
```
<!-- cenci:configure-q14-remove:end -->
PINNED_BLOCK
P5_Q14_BLOCK="${P5_Q14_BLOCK%$'\n'}"

IFS= read -r -d '' P5_Q15_BLOCK <<'PINNED_BLOCK'
<!-- cenci:configure-q15-switch:begin -->
```jq
.refinement = ((.refinement | if type == "object" then . else {} end) + {autonomy: $autonomy})
```
<!-- cenci:configure-q15-switch:end -->
PINNED_BLOCK
P5_Q15_BLOCK="${P5_Q15_BLOCK%$'\n'}"

IFS= read -r -d '' P5_CONFIRM_BLOCK <<'PINNED_BLOCK'
<!-- cenci:configure-confirm-before-apply:begin -->
```jq
.refinement = ((.refinement | if type == "object" then . else {} end) + {confirmBeforeApply: $confirmBeforeApply})
```
<!-- cenci:configure-confirm-before-apply:end -->
PINNED_BLOCK
P5_CONFIRM_BLOCK="${P5_CONFIRM_BLOCK%$'\n'}"

IFS= read -r -d '' P5_GRANT_BLOCK <<'PINNED_BLOCK'
<!-- cenci:configure-refinement-automerge:begin -->
```jq
.refinement = ((.refinement | if type == "object" then . else {} end) + {automerge: $automerge})
```
<!-- cenci:configure-refinement-automerge:end -->
PINNED_BLOCK
P5_GRANT_BLOCK="${P5_GRANT_BLOCK%$'\n'}"

IFS= read -r -d '' P5_SCAFFOLD_BLOCK <<'PINNED_BLOCK'
<!-- cenci:automerge-scaffold:begin -->
```json
{
  "protectedPaths": [
    "*auth*",
    "*login*",
    "*logout*",
    "*session*",
    "*password*",
    "*passwd*",
    "*credential*",
    "*secret*",
    "*secrets*",
    "*token*",
    "*jwt*",
    "*apikey*",
    "*api_key*",
    "*.pem",
    "*.key",
    "*.env*",
    "*oauth*",
    "*sso*",
    "*saml*",
    "*openid*",
    "*permission*",
    "*acl*",
    "*rbac*",
    "*role*",
    "*crypto*",
    "*encrypt*",
    "*decrypt*",
    "*sign*",
    "*hash*",
    "*payment*",
    "*billing*",
    "*invoice*",
    "*checkout*",
    "*stripe*",
    "*migrat*",
    "*schema*",
    ".github/",
    ".cenci/"
  ],
  "maxChangedFiles": 10,
  "maxDiffLines": 400,
  "mergeMethod": "squash"
}
```
<!-- cenci:automerge-scaffold:end -->
PINNED_BLOCK
P5_SCAFFOLD_BLOCK="${P5_SCAFFOLD_BLOCK%$'\n'}"

AUTONOMY_PHASE_FILE='phases/autonomy-settings.md'
CONFIGURE_DIR="${FLOW_DIR}/skills/configure"

# marker_block_raw <content> <begin-line> <end-line> -- lines from the exact
# begin marker line through the exact end marker line, inclusive (fences
# included). Pure (no fail()), safe inside $(...).
marker_block_raw() {
  local content="$1" begin="$2" end="$3"
  awk -v b="${begin}" -v e="${end}" '
    $0 == b { on=1 }
    on { print }
    on && $0 == e { exit }
  ' <<<"${content}"
}

# count_exact_lines <content> <line> -- lines equal to <line>. Pure.
count_exact_lines() {
  local content="$1" line="$2"
  grep -cxF -- "${line}" <<<"${content}" | tr -d '[:space:]'
}

# P5 corpus: SKILL.md plus every phases/*.md and references/*.md on disk, so a
# stray duplicate in any configure markdown file is counted.
require_doc autonomy_phase "skills/configure/${AUTONOMY_PHASE_FILE}" || true
p5_all="${skill}"
shopt -s nullglob
for _part in "${CONFIGURE_DIR}"/phases/*.md "${CONFIGURE_DIR}"/references/*.md; do
  p5_all+=$'\n'"$(cat "${_part}")"
done
shopt -u nullglob

p5_check_block() { # <marker-name> <pinned-block> <begin> <end>
  local name="$1" pinned="$2" begin="$3" end="$4" n_begin n_end in_skill actual
  n_begin="$(count_exact_lines "${p5_all}" "${begin}")"
  n_end="$(count_exact_lines "${p5_all}" "${end}")"
  [[ "${n_begin}" -eq 1 ]] || fail "P5 ${name}: begin marker must occur exactly once across the configure markdown, found ${n_begin}"
  [[ "${n_end}" -eq 1 ]] || fail "P5 ${name}: end marker must occur exactly once across the configure markdown, found ${n_end}"
  in_skill="$(count_exact_lines "${skill}" "${begin}")"
  [[ "${in_skill}" -eq 0 ]] || fail "P5 ${name}: block must no longer be in SKILL.md, found ${in_skill} begin marker(s) there"
  if [[ -z "${autonomy_phase}" ]]; then
    fail "P5 ${name}: ${AUTONOMY_PHASE_FILE} missing or empty"
    return
  fi
  actual="$(marker_block_raw "${autonomy_phase}" "${begin}" "${end}")"
  if [[ -z "${actual}" ]]; then
    fail "P5 ${name}: block not found in ${AUTONOMY_PHASE_FILE}"
  elif [[ "${actual}" != "${pinned}" ]]; then
    fail "P5 ${name}: block in ${AUTONOMY_PHASE_FILE} is not byte-identical to the pre-move SKILL.md block (fences and marker lines included)"
  fi
}

p5_check_block q13-switch "${P5_Q13_BLOCK}" '<!-- cenci:configure-q13-switch:begin -->' '<!-- cenci:configure-q13-switch:end -->'
p5_check_block q14-remove "${P5_Q14_BLOCK}" '<!-- cenci:configure-q14-remove:begin -->' '<!-- cenci:configure-q14-remove:end -->'
p5_check_block q15-switch "${P5_Q15_BLOCK}" '<!-- cenci:configure-q15-switch:begin -->' '<!-- cenci:configure-q15-switch:end -->'
p5_check_block confirm-before-apply "${P5_CONFIRM_BLOCK}" '<!-- cenci:configure-confirm-before-apply:begin -->' '<!-- cenci:configure-confirm-before-apply:end -->'
p5_check_block refinement-automerge "${P5_GRANT_BLOCK}" '<!-- cenci:configure-refinement-automerge:begin -->' '<!-- cenci:configure-refinement-automerge:end -->'
p5_check_block automerge-scaffold "${P5_SCAFFOLD_BLOCK}" '<!-- cenci:automerge-scaffold:begin -->' '<!-- cenci:automerge-scaffold:end -->'

# The block text is pinned; now prove the extracted programs actually run.
# Each is extracted from phases/autonomy-settings.md (fence lines stripped) and
# applied with jq to a fixture, the way step 6 applies them.
p5_extract() { # <marker-name> -- the program text between the marker lines
  awk -v b="<!-- cenci:configure-$1:begin -->" -v e="<!-- cenci:configure-$1:end -->" '
    $0 == e { on=0 }
    on && index($0, "```") != 1 { print }
    $0 == b { on=1 }
  ' <<<"${autonomy_phase}"
}

p5_json_eq() { # <label> <actual> <expected-json>
  local label="$1" actual="$2" expected="$3"
  if [[ -z "${actual}" ]]; then
    fail "P5 ${label}: program produced no output (missing block or jq error)"
    return
  fi
  jq -ne --argjson a "${actual}" --argjson b "${expected}" '$a == $b' >/dev/null 2>&1 \
    || fail "P5 ${label}: expected ${expected}, got ${actual}"
}

if ! command -v jq >/dev/null 2>&1; then
  fail "P5: jq not available to run the extracted programs"
else
  p5_prog="$(p5_extract q13-switch)"
  [[ -n "${p5_prog}" ]] || fail "P5 q13-switch: no program extracted from ${AUTONOMY_PHASE_FILE}"
  p5_out="$(printf '%s' '{"planning":{"autonomy":"interactive","note":"keep"},"stack":{"backend":"go"}}' | jq -c --arg autonomy lean "${p5_prog:-.}" 2>/dev/null)"
  p5_json_eq "q13-switch switches autonomy and keeps siblings" "${p5_prog:+${p5_out}}" '{"planning":{"autonomy":"lean","note":"keep"},"stack":{"backend":"go"}}'

  p5_prog="$(p5_extract q14-remove)"
  [[ -n "${p5_prog}" ]] || fail "P5 q14-remove: no program extracted from ${AUTONOMY_PHASE_FILE}"
  p5_out="$(printf '%s' '{"automerge":{"maxDiffLines":1},"projects":[{"path":"api","automerge":{"maxDiffLines":2}},{"path":"web"}],"stack":{"backend":"go"}}' | jq -c "${p5_prog:-.}" 2>/dev/null)"
  p5_json_eq "q14-remove deletes the top-level and every projects[] automerge key" "${p5_prog:+${p5_out}}" '{"projects":[{"path":"api"},{"path":"web"}],"stack":{"backend":"go"}}'

  p5_prog="$(p5_extract q15-switch)"
  [[ -n "${p5_prog}" ]] || fail "P5 q15-switch: no program extracted from ${AUTONOMY_PHASE_FILE}"
  p5_out="$(printf '%s' '{"refinement":{"autonomy":"interactive","note":"keep"}}' | jq -c --arg autonomy lean "${p5_prog:-.}" 2>/dev/null)"
  p5_json_eq "q15-switch switches autonomy and keeps siblings" "${p5_prog:+${p5_out}}" '{"refinement":{"autonomy":"lean","note":"keep"}}'

  p5_prog="$(p5_extract confirm-before-apply)"
  [[ -n "${p5_prog}" ]] || fail "P5 confirm-before-apply: no program extracted from ${AUTONOMY_PHASE_FILE}"
  p5_out="$(printf '%s' '{"refinement":{"autonomy":"lean"}}' | jq -c --argjson confirmBeforeApply false "${p5_prog:-.}" 2>/dev/null)"
  p5_json_eq "confirm-before-apply records an explicit false and keeps siblings" "${p5_prog:+${p5_out}}" '{"refinement":{"autonomy":"lean","confirmBeforeApply":false}}'

  p5_prog="$(p5_extract refinement-automerge)"
  [[ -n "${p5_prog}" ]] || fail "P5 refinement-automerge: no program extracted from ${AUTONOMY_PHASE_FILE}"
  p5_out="$(printf '%s' '{"refinement":{"autonomy":"lean"},"automerge":{"maxDiffLines":1}}' | jq -c --arg automerge eligible "${p5_prog:-.}" 2>/dev/null)"
  p5_json_eq "refinement-automerge grants without touching the risk policy" "${p5_prog:+${p5_out}}" '{"refinement":{"autonomy":"lean","automerge":"eligible"},"automerge":{"maxDiffLines":1}}'

  p5_scaffold="$(marker_block_raw "${autonomy_phase}" '<!-- cenci:automerge-scaffold:begin -->' '<!-- cenci:automerge-scaffold:end -->' | awk 'index($0, "<!--") != 1 && index($0, "```") != 1')"
  printf '%s\n' "${p5_scaffold}" | jq -e 'type == "object" and (.protectedPaths | length) > 0' >/dev/null 2>&1 \
    || fail "P5 automerge-scaffold: the block in ${AUTONOMY_PHASE_FILE} is not a JSON object with protectedPaths"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-fragment-markers.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-fragment-markers.test.sh: all checks passed."
