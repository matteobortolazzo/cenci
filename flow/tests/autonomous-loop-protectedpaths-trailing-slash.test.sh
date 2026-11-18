#!/usr/bin/env bash
# autonomous-loop-protectedpaths-trailing-slash.test.sh — doc-contract test
# for ticket #967: docs/autonomous-loop.md's `protectedPaths` bullet (§3,
# "Declare what a merge is allowed to touch") describes protectedPaths as
# globs where `*` matches any character including `/`, but omits the
# trailing-slash directory-prefix rule that `matchesProtected` actually
# implements (watch/internal/babysit/automerge.go: a pattern ending in "/"
# with no trailing "*" matches that directory and everything under it).
# Read literally, the doc makes this repo's own committed directory-prefix
# entries (flow/skills/, watch/internal/, ...) look like dead literal-match
# strings.
#
# This test asserts, by grepping the real committed doc content (not by
# re-implementing matchesProtected), that:
#   1. docs/autonomous-loop.md's protectedPaths bullet documents the
#      trailing-slash / directory-prefix rule.
#   2. flow/skills/configure/references/config-schema.md's automerge.protectedPaths field
#      description states or links that same rule.
#
# Ticket #841 extends the same contract in three ways:
#   3. Both docs state the no-wildcard rule: a pattern with neither `*` nor a
#      trailing `/` matches that exact path, or that path followed by `/` and
#      anything after it (the same rule fileOwnedByProject uses), and a `*`
#      pattern gets no directory extension. The windows below cover the WHOLE
#      rewritten bullet/field, not a fixed number of lines after its anchor.
#   4. docs/autonomous-loop.md §3 (scoped between the literal `### 3.` and
#      `### 4.` headings) says policy evaluation ignores `configVersion`,
#      interpretation changes only add protection, and a lagging stamp is
#      reported only by the SessionStart nudge and maintain's `config-version`
#      check.
#   5. config-schema.md's `configVersion` consumer paragraph says the
#      automerge policy engine (`cenci babysit`) does not read it.
# Every asserted phrase is one source line in the docs, so line-wrapping
# cannot split a marker (flow/docs/shell-scripting-gotchas.md).
#
# Follows the fixture-driven idiom of automerge-config-schema.test.sh: a
# `failures=` counter, small assert_* helpers, self-contained, auto-
# discovered by the flow gate's `*.test.sh` glob.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# read_file_raw <absolute-path> — pure extraction, no fail() side effect
# here: it is deliberately safe to call inside a $(...) command substitution
# (flow/docs/shell-scripting-gotchas.md rule on fail()-in-command-substitution).
read_file_raw() {
  cat "$1" 2>/dev/null
}

# require_file <result-var> <absolute-path> — nameref wrapper that assigns
# the real committed file's content into <result-var>, or fails closed with
# a distinct "not found" message and assigns "" if not found. Must NOT be
# invoked via $(...).
require_file() {
  local -n _result="$1"
  local _path="$2"
  local _content
  if ! _content="$(read_file_raw "${_path}")"; then
    fail "${_path}: doc not found/unreadable"
    _result=""
    return 1
  fi
  _result="${_content}"
}

assert_contains_any() {
  # assert_contains_any <content> <label> <pattern...> — pass if any
  # <pattern> is a substring of <content>. An empty pattern is a caller bug
  # (it would match vacuously), so it fails loudly.
  local content="$1" label="$2"
  shift 2
  local pattern
  for pattern in "$@"; do
    if [[ -z "${pattern}" ]]; then
      fail "${label}: empty pattern passed to assert_contains_any"
      return 1
    fi
    [[ "${content}" == *"${pattern}"* ]] && return 0
  done
  fail "${label}: expected one of [$*] not found"
  return 1
}

# extract_block <anchor> <end-regex> — reads a document on stdin and prints
# the first line containing the literal <anchor> through the line before the
# next line matching <end-regex> (or the next heading). Pure extraction with
# no fail() side effect, so it is safe inside $(...).
extract_block() {
  awk -v anchor="$1" -v endre="$2" '
    !on && index($0, anchor) { on = 1; print; next }
    on && $0 ~ endre { exit }
    on && /^#/ { exit }
    on { print }
  '
}

# extract_section3 — reads a document on stdin and prints the lines strictly
# between the literal `### 3.` and `### 4.` headings, ignoring heading-shaped
# lines inside code fences. Pure extraction, safe inside $(...).
extract_section3() {
  awk '
    /^```/ { infence = !infence }
    !infence && /^### 3\./ { on = 1; next }
    !infence && /^### 4\./ { on = 0 }
    on { print }
  '
}

# The no-wildcard rule markers, shared by both docs. Each is a substring of
# ONE source line in the doc.
NOWILD_RULE='with neither `*` nor a trailing `/` matches that exact path and everything under it'
NOWILD_STAR='pattern containing `*` gets no directory extension'

# Non-vacuity self-test for extract_section3: a marker that lives only under
# `### 4.` must NOT be found in the §3 window. fail() is called only on the
# regression branch.
SELFTEST_DOC=$'### 3. Declare\nnothing here\n### 4. Arm\nPolicy evaluation ignores `configVersion`'
SELFTEST_SECTION="$(printf '%s\n' "${SELFTEST_DOC}" | extract_section3)"
if [[ "${SELFTEST_SECTION}" == *'ignores `configVersion`'* ]]; then
  fail "self-test: extract_section3 leaked text from the ### 4. section"
fi
if [[ "${SELFTEST_SECTION}" != *'nothing here'* ]]; then
  fail "self-test: extract_section3 dropped text from the ### 3. section"
fi

# =====================================================================
# docs/autonomous-loop.md §3 — the protectedPaths bullet must document the
# trailing-slash directory-prefix rule, not just the `*` glob rule.
# =====================================================================
FILE="${REPO_ROOT}/docs/autonomous-loop.md"
if require_file CONTENT "${FILE}"; then
  # The whole bullet: from its anchor line to the next top-level bullet.
  BULLET_BLOCK="$(printf '%s\n' "${CONTENT}" | extract_block '`protectedPaths` are globs' '^- ')"
  if [[ -z "${BULLET_BLOCK}" ]]; then
    fail "docs/autonomous-loop.md: protectedPaths bullet not found"
  else
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet documents trailing-slash directory-prefix rule)" \
      "ending in \`/\`" "ending in \"/\"" "ends in \`/\`"
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet states directory-and-everything-under semantics)" \
      "everything under"
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet states the no-wildcard exact-or-directory rule)" \
      "${NOWILD_RULE}"
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet uses flow/skills as the no-wildcard directory example)" \
      "(e.g. \`flow/skills\` covers the whole directory, while"
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet says a * pattern gets no directory extension)" \
      "${NOWILD_STAR}"
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet keeps the root-anchoring sentence)" \
      "a bare pattern with no leading \`*\` (e.g. \`install.sh\`) only matches a file literally"
  fi

  # The configVersion paragraph, scoped to §3 so a mention elsewhere in the
  # page cannot satisfy it.
  SECTION3="$(printf '%s\n' "${CONTENT}" | extract_section3)"
  if [[ -z "${SECTION3}" ]]; then
    fail "docs/autonomous-loop.md: section ### 3. not found between the ### 3. and ### 4. headings"
  else
    assert_contains_any "${SECTION3}" \
      "docs/autonomous-loop.md §3 (policy evaluation ignores configVersion)" \
      "Policy evaluation ignores \`configVersion\`"
    assert_contains_any "${SECTION3}" \
      "docs/autonomous-loop.md §3 (interpretation changes only add protection; removal needs a new explicit key)" \
      "only ever add protection, and removing protection requires a new explicit key"
    assert_contains_any "${SECTION3}" \
      "docs/autonomous-loop.md §3 (a lagging stamp is reported only by the SessionStart nudge)" \
      "A lagging stamp is reported only by the SessionStart nudge"
    assert_contains_any "${SECTION3}" \
      "docs/autonomous-loop.md §3 (a lagging stamp is reported by maintain's config-version check)" \
      "\`/cenci:maintain\`'s \`config-version\` check"
  fi
fi

# =====================================================================
# flow/skills/configure/references/config-schema.md — the
# automerge.protectedPaths field description (moved out of SKILL.md in #1344)
# must state or link the same trailing-slash rule.
# =====================================================================
FILE="${FLOW_DIR}/skills/configure/references/config-schema.md"
if require_file CONTENT "${FILE}"; then
  # The whole field description: from its anchor line to the next field.
  FIELD_BLOCK="$(printf '%s\n' "${CONTENT}" | extract_block '`automerge.protectedPaths`' '^  - ')"
  if [[ -z "${FIELD_BLOCK}" ]]; then
    fail "flow/skills/configure/references/config-schema.md: automerge.protectedPaths field description not found"
  else
    assert_contains_any "${FIELD_BLOCK}" \
      "flow/skills/configure/references/config-schema.md (automerge.protectedPaths field description states or links the trailing-slash rule)" \
      "ending in \`/\`" "ending in \"/\"" "ends in \`/\`" "everything under" "docs/autonomous-loop.md"
    assert_contains_any "${FIELD_BLOCK}" \
      "flow/skills/configure/references/config-schema.md (automerge.protectedPaths field description states the no-wildcard exact-or-directory rule)" \
      "${NOWILD_RULE}"
    assert_contains_any "${FIELD_BLOCK}" \
      "flow/skills/configure/references/config-schema.md (automerge.protectedPaths field description says a * pattern gets no directory extension)" \
      "${NOWILD_STAR}"
  fi

  # The configVersion consumer paragraph: from its anchor to the next
  # paragraph, so a mention elsewhere in the file cannot satisfy it.
  CV_BLOCK="$(printf '%s\n' "${CONTENT}" | extract_block '`configVersion` records the flow plugin version' '^Patch-only drift is deliberately silent')"
  if [[ -z "${CV_BLOCK}" ]]; then
    fail "flow/skills/configure/references/config-schema.md: configVersion consumer paragraph not found"
  else
    assert_contains_any "${CV_BLOCK}" \
      "flow/skills/configure/references/config-schema.md (configVersion paragraph says the automerge policy engine does not read it)" \
      "The automerge policy engine (\`cenci babysit\`) does not read \`configVersion\`"
  fi
fi

echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
