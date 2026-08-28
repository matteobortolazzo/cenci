#!/usr/bin/env bash
# Contract test for ticket #1115 — retire the sandbox `baseVersion` semver
# resolution scheme in /cenci:configure and always emit a fixed
# `ARG BASE_VERSION=latest`.
#
# Step 5e used to resolve `baseVersion` via a dogfooding path (a), an
# installed-plugin path (b) reading `~/.claude/plugins/installed_plugins.json`
# (which can never succeed inside a `cenci open` session — the sandbox
# plugin set is the closed set {cenci, cenci-watch}, #1002), and a semver
# **Validation** step against `^[0-9]+\.[0-9]+\.[0-9]+$`, falling back to an
# "unresolved" path (c) with a "No cenci-sandbox plugin version detected"
# inline comment. None of that ever produced a real base-image tag —
# `buildBase` (watch/internal/sandbox/launcher/engine.go:337-349) only ever
# tags `cenci-sandbox-base:<12-hex content hash>` and
# `cenci-sandbox-base:latest`; no semver tag exists in any environment. This
# suite pins that step 5e now always emits the literal
# `ARG BASE_VERSION=latest` with no resolution algorithm, that
# `installed_plugins.json` and the semver validation pattern and unresolved
# comment are gone, and that neither client adapter's
# `merge-sandbox-config.sh` invocation still passes `--base-version`.
#
# Follows the exact idiom of flow/tests/configure-fragment-markers.test.sh:
# pinned exact authored substrings as constants (the red phase fails for the
# right reason, and green has an unambiguous authoring target — never derive
# strings from the doc under test), a `require_doc` nameref helper, `fail()`
# counter, no fixtures, `assert_absent_paired_ws` for non-vacuous absence
# checks (a bare `assert_not_contains` on `installed_plugins.json` would pass
# vacuously if step 5e itself vanished or was renumbered) per
# flow/docs/shell-scripting-gotchas.md's read-helper-purity rule. Never calls
# `fail()` inside `$(...)` — every extractor is a pure function, invoked in
# `$(...)` only for its stdout, with the caller checking the result and
# calling `fail()` in the parent shell.
#
# Covered files:
#   - flow/skills/configure/SKILL.md
#   - flow/skills/configure/codex.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "configure-baseversion-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "configure-baseversion-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
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
# A bare assert_not_contains on installed_plugins.json, the semver validation
# pattern, or the unresolved-value comment would pass vacuously before step
# 5e exists at all — it would prove nothing about this ticket. Requiring the
# marker's own existence first means this only proves the intended thing
# (step 5e exists AND deliberately no longer carries the retired resolution
# algorithm), and fails loudly — for the right reason — while the fix is
# still unauthored (this red phase). Mirrors
# configure-fragment-markers.test.sh's helper of the same name.
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

# --- Exact authored substrings Phase 4 (green) must add/remove verbatim ----
#
# These are pinned here, not derived, so the red phase fails for the right
# reason and the green phase has an unambiguous authoring target.

# Step 5e existence marker — scopes every check below so nothing passes
# vacuously if step 5e itself is missing or renumbered.
STEP5E_MARKER='5e. **Generate `.cenci/Dockerfile`**'

# The fixed literal step 5e must emit, with no resolution algorithm. Paired
# with the immediately-following FROM line (not a bare `ARG
# BASE_VERSION=latest` substring) because that exact literal already occurs
# elsewhere in the unfixed doc's path-(c) fallback prose ("write `ARG
# BASE_VERSION=latest`, then a comment line immediately after") — a bare
# substring match would pass vacuously both before and after this ticket's
# fix. Pairing with the FROM line pins the check to the generated-file-format
# code fence specifically, the one site this ticket actually rewrites.
BASE_VERSION_LATEST_FROM_PAIR='ARG BASE_VERSION=latest FROM cenci-sandbox-base:${BASE_VERSION}'

# Retired resolution-algorithm artifacts that must no longer appear anywhere
# in step 5e's scope once the algorithm is deleted.
INSTALLED_PLUGINS_JSON='installed_plugins.json'
SEMVER_VALIDATION_PATTERN='^[0-9]+\.[0-9]+\.[0-9]+$'
UNRESOLVED_COMMENT='No cenci-sandbox plugin version detected'

# The shared client-neutral merge script — both adapters must still invoke
# it (#632), but neither may still pass the retired flag.
MERGE_SCRIPT_MARKER='merge-sandbox-config.sh'
BASE_VERSION_FLAG='--base-version'

# --- skills/configure/SKILL.md ----------------------------------------------

require_doc skill "skills/configure/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  # Step 5e must exist at all — every check below is scoped/paired against it.
  assert_contains "${skill}" "${STEP5E_MARKER}" "SKILL.md step 5e (Generate .cenci/Dockerfile) present"

  # The fixed literal replacement for the ARG default, pinned to the
  # generated-file-format code fence via the FROM pairing (see comment on
  # BASE_VERSION_LATEST_FROM_PAIR above).
  assert_contains_ws "${skill}" "${BASE_VERSION_LATEST_FROM_PAIR}" "SKILL.md step 5e's generated Dockerfile fence emits the literal ARG BASE_VERSION=latest"

  # Retired resolution-algorithm artifacts, paired against step 5e's own
  # existence so the absence checks cannot pass vacuously.
  assert_absent_paired_ws "${skill}" "${STEP5E_MARKER}" "${INSTALLED_PLUGINS_JSON}" "SKILL.md no longer reads installed_plugins.json"
  assert_absent_paired_ws "${skill}" "${STEP5E_MARKER}" "${SEMVER_VALIDATION_PATTERN}" "SKILL.md no longer validates a semver baseVersion pattern"
  assert_absent_paired_ws "${skill}" "${STEP5E_MARKER}" "${UNRESOLVED_COMMENT}" "SKILL.md no longer emits the unresolved-baseVersion inline comment"

  # Shared merge script still invoked, but without the retired flag.
  assert_contains "${skill}" "${MERGE_SCRIPT_MARKER}" "SKILL.md still invokes merge-sandbox-config.sh"
  assert_absent_paired_ws "${skill}" "${MERGE_SCRIPT_MARKER}" "${BASE_VERSION_FLAG}" "SKILL.md merge-sandbox-config.sh invocation drops --base-version"
else
  fail "SKILL.md: skipped step 5e assertions -- doc empty/unreadable"
fi

# --- skills/configure/codex.md ----------------------------------------------

require_doc codex "skills/configure/codex.md" || true
if [[ -n "${codex}" ]]; then
  assert_contains "${codex}" "${MERGE_SCRIPT_MARKER}" "codex.md still invokes merge-sandbox-config.sh"
  assert_absent_paired_ws "${codex}" "${MERGE_SCRIPT_MARKER}" "${BASE_VERSION_FLAG}" "codex.md merge-sandbox-config.sh invocation drops --base-version"
else
  fail "codex.md: skipped merge-sandbox-config.sh assertions -- doc empty/unreadable"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-baseversion-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-baseversion-contract.test.sh: all checks passed."
