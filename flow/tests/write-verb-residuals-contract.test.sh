#!/usr/bin/env bash
# Contract test for ticket #808 -- pin the corrected threat-model wording for
# the five direct-mutating write-verb residuals (`cp`, `mv`, `dd of=`,
# `sed -i`, `truncate`) at the three sites that document them:
#   1. flow/hooks/scripts/lib/bash-write-targets.sh's "Named residuals"
#      bullet inside its "Threat model and accepted residuals" header block.
#   2. flow/docs/adapter-contract.md's `worktree-isolation` row.
#   3. flow/docs/adapter-contract.md's `sensitive-file-refusal` row.
#
# #795 originally excluded these verbs on the theory that they were "already
# constrained by per-verb allow grants" -- false under
# --dangerously-skip-permissions, which disables that permission surface
# entirely. #810 already removed that retired rationale from the repo; what
# remains (and what this test pins) is the corrected replacement: the verbs
# are outside these hooks' detection surface entirely, their targets are NOT
# protected by these hooks under --dangerously-skip-permissions, the
# parser-grammar-complexity/false-positive/maintenance-cost tradeoff that
# justifies the residual, and the distinction between environments where
# normal client permission rules are active (defense in depth) and this
# sandbox's actual boundary (the container itself).
#
# Structure, modelled on flow/tests/subagent-cwd-contract.test.sh:
#   - `set -uo pipefail`, a `failures=` counter, small assert_* helpers.
#   - read_doc_raw / read_sh_header_raw / read_md_row_raw are pure
#     extractors (no fail() side effect, safe inside $(...)); require_doc is
#     the impure nameref wrapper that calls fail() in the parent shell --
#     the naming/purity convention flow/tests/read-helper-purity-contract.
#     test.sh sweeps every tracked *.test.sh file for.
#   - A local `_contains_ws_insensitive` copy (flow/tests/parity/
#     contract-lib.sh:116-128) with the mandatory empty-needle guard, so a
#     Markdown/comment line-wrap between words in the pinned sentences can
#     never produce a false negative.
#   - Existence proofs run first and gate every dependent assertion (a
#     deletion/rename must fail loudly, never let a "must not contain"
#     assertion pass vacuously against nothing).
#   - Scoped extraction only: the .sh header block is read from the start of
#     the file through the line before the first truly empty line (never a
#     whole-file scan + tail-style filtering), then each line's leading `#`
#     marker is stripped and whitespace collapsed; the .md rows are read via
#     `grep -m1 -F` on the row's fixed leading marker, one row at a time.
#   - Negative assertions (the retired "per-verb allow grant" /
#     "already constrained by" phrasing) run only after the existence
#     proofs pass, plus a flow-wide sweep. This file necessarily embeds
#     those retired phrases as literal string data (constants + fixture
#     sed patterns), so the sweep excludes this file by exact absolute path
#     -- the same carve-out convention flow/tests/gh-title-payload-encoding.
#     test.sh documents; a comment alone is not a sufficient exclusion.
#   - Non-vacuity injections against mktemp -d fixture copies of a
#     synthetic "golden" doc shape (not the real files, which do not carry
#     the fixed wording yet in this red phase): (a) deleting the residuals
#     block/row makes the existence predicate fail; (b) corrupting one word
#     of an anchored sentence makes the positive assertion fail; (c)
#     injecting a retired phrase makes the negative assertion fire.
#   - No chmod-based unwritability anywhere (flow/tests/
#     root-safe-perms-contract.test.sh sweeps every tracked *.test.sh).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "write-verb-residuals-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "write-verb-residuals-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

_self_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "write-verb-residuals-contract.test.sh: failed to resolve self directory." >&2; exit 2; }
SELF_ABS="${_self_dir}/$(basename "${BASH_SOURCE[0]}")"

SH_RELPATH="hooks/scripts/lib/bash-write-targets.sh"
MD_RELPATH="docs/adapter-contract.md"

# =====================================================================
# Pure extractors -- no fail() side effect anywhere in this section;
# deliberately safe to invoke inside $(...).
# =====================================================================

read_doc_raw() {
  # read_doc_raw <base-dir> <relpath> -- prints <base-dir>/<relpath>'s raw
  # content, or nothing on a read failure (missing/unreadable file).
  local base="$1" relpath="$2"
  cat "${base}/${relpath}" 2>/dev/null
}

read_sh_header_raw() {
  # read_sh_header_raw <base-dir> -- prints the leading comment-header block
  # of <base-dir>/hooks/scripts/lib/bash-write-targets.sh: start of file
  # through the line before the first truly empty line (scoped extraction,
  # never a whole-file scan + tail-style filtering), with each line's
  # leading `#` marker stripped and every whitespace run collapsed to a
  # single space, so a future re-wrap of the comment cannot break a
  # ws-insensitive containment match. Empty output on a missing/unreadable
  # file.
  local base="$1"
  local path="${base}/${SH_RELPATH}"
  awk '/^$/{exit} {print}' "${path}" 2>/dev/null \
    | sed -E 's/^[[:space:]]*#[[:space:]]?//' \
    | tr '\n' ' ' \
    | tr -s '[:space:]' ' '
}

read_md_row_raw() {
  # read_md_row_raw <base-dir> <property-token> -- prints the first physical
  # source line of <base-dir>/docs/adapter-contract.md whose row begins with
  # "| `<property-token>` |" (grep -m1 -F, scoped to that one row -- never a
  # whole-file grep piped to tail). Empty output if the row/file is missing.
  local base="$1" property="$2"
  local path="${base}/${MD_RELPATH}"
  grep -m1 -F -- "| \`${property}\` |" "${path}" 2>/dev/null
}

# =====================================================================
# Impure nameref wrapper -- calls fail() in the parent shell. Must NOT be
# invoked via $(...).
# =====================================================================

require_doc() {
  # require_doc <result-var> <base-dir> <relpath> -- assigns the file's
  # content into <result-var>, or fails closed with a "not found" message
  # and assigns "" (a missing/unreadable file must never silently
  # masquerade as empty content, which would make a later "must not
  # contain" assertion vacuously pass).
  local -n _result="$1"
  local base="$2" relpath="$3"
  local content
  if ! content="$(read_doc_raw "${base}" "${relpath}")"; then
    fail "${relpath}: doc not found/unreadable: ${base}/${relpath}"
    _result=""
    return 1
  fi
  _result="${content}"
}

# =====================================================================
# Existence predicates (plain helpers, not read_*-named -- calling no
# fail() themselves, but not subject to the read_* purity convention
# either way; reused for both the real repo check and the non-vacuity
# fixture self-tests below).
# =====================================================================

sh_header_markers_present() {
  local base="$1"
  local content
  content="$(read_doc_raw "${base}" "${SH_RELPATH}")"
  [[ -n "${content}" ]] || return 1
  [[ "${content}" == *"${SH_HEADER_MARKER_1}"* && "${content}" == *"${SH_HEADER_MARKER_2}"* ]]
}

md_row_marker_present() {
  local base="$1" token="$2"
  local content
  content="$(read_doc_raw "${base}" "${MD_RELPATH}")"
  [[ -n "${content}" ]] || return 1
  [[ "${content}" == *"| \`${token}\` |"* ]]
}

# =====================================================================
# Local ws-insensitive matcher (flow/tests/parity/contract-lib.sh:116-128).
# Kept local so this suite doesn't need to source the parity harness for a
# two-file doc check.
# =====================================================================

_contains_ws_insensitive() {
  local content normalized_content phrase normalized_phrase
  content="$1"
  phrase="$2"
  # An empty phrase would otherwise vacuously match any content (including
  # empty content) via the *""* glob below -- reject it explicitly.
  [[ -n "${phrase}" ]] || return 1
  normalized_content="$(printf '%s' "${content}" | tr -s '[:space:]' ' ')" || return 1
  normalized_phrase="$(printf '%s' "${phrase}" | tr -s '[:space:]' ' ')" || return 1
  [[ "${normalized_content}" == *"${normalized_phrase}"* ]]
}

assert_ws_contains() {
  local content="$1" phrase="$2" label="$3"
  _contains_ws_insensitive "${content}" "${phrase}" || fail "${label}: expected corrected text not found: [${phrase}]"
}

assert_ws_not_contains() {
  local content="$1" phrase="$2" label="$3"
  # Negative-assertion existence guard: a "must not contain" claim against
  # empty/missing content is meaningless and must never pass.
  if [[ -z "${content}" ]]; then
    fail "${label}: cannot assert absence of [${phrase}] against empty/missing content (guard against a vacuous negative assertion)"
    return
  fi
  _contains_ws_insensitive "${content}" "${phrase}" && fail "${label}: forbidden retired phrase still present: [${phrase}]"
}

# =====================================================================
# Pinned replacement wording (what Phase 4 must write, verbatim modulo
# whitespace, at all three sites) and the retired phrases that must never
# reappear.
# =====================================================================

SH_HEADER_MARKER_1='Threat model and accepted residuals'
SH_HEADER_MARKER_2='Named residuals, accepted by settled #795 refinement decisions:'
MD_ROW_TOKEN_WORKTREE='worktree-isolation'
MD_ROW_TOKEN_SENSITIVE='sensitive-file-refusal'

# AC2: names the five verb families + states the hooks do not inspect their
# write targets at all.
VERB_SURFACE_MARKER='`cp`, `mv`, `dd of=`, `sed -i`, and `truncate` sit entirely outside this detection surface: these hooks do not inspect their write targets at all'
# AC5 (first half): explicit non-protection statement under
# --dangerously-skip-permissions.
SKIP_PERMISSIONS_MARKER='not protected by these hooks under --dangerously-skip-permissions'
# AC4: parser/grammar complexity + false-positive/maintenance-cost rationale.
RATIONALE_MARKER='Each verb has a distinct target grammar with ambiguous target positions, and supporting them safely would materially expand parser complexity and false-positive risk without justifying the added maintenance cost.'
# AC5 (second half): the environment distinction -- normal permission rules
# may be defense in depth elsewhere, but are never the sandbox boundary.
ENV_DISTINCTION_MARKER='Where normal client permission rules are active they may add defense in depth, but they are never the sandbox boundary -- the container itself is.'

RETIRED_PHRASE_1='per-verb allow grant'
RETIRED_PHRASE_2='already constrained by'

# =====================================================================
# Existence proofs -- run first and gate everything else (AC7). A deletion
# or rename of either file/section must fail loudly here, never let a later
# "must not contain" assertion pass vacuously against nothing.
# =====================================================================

SH_OK=1
if ! require_doc SH_CONTENT "${FLOW_DIR}" "${SH_RELPATH}"; then
  SH_OK=0
elif ! sh_header_markers_present "${FLOW_DIR}"; then
  fail "${SH_RELPATH}: header missing required marker(s): [${SH_HEADER_MARKER_1}] / [${SH_HEADER_MARKER_2}]"
  SH_OK=0
fi

MD_OK=1
if ! require_doc MD_CONTENT "${FLOW_DIR}" "${MD_RELPATH}"; then
  MD_OK=0
else
  if ! md_row_marker_present "${FLOW_DIR}" "${MD_ROW_TOKEN_WORKTREE}"; then
    fail "${MD_RELPATH}: missing row start: [| \`${MD_ROW_TOKEN_WORKTREE}\` |]"
    MD_OK=0
  fi
  if ! md_row_marker_present "${FLOW_DIR}" "${MD_ROW_TOKEN_SENSITIVE}"; then
    fail "${MD_RELPATH}: missing row start: [| \`${MD_ROW_TOKEN_SENSITIVE}\` |]"
    MD_OK=0
  fi
fi

# =====================================================================
# Site 1: bash-write-targets.sh's Named residuals header block.
# =====================================================================
if [[ "${SH_OK}" -eq 1 ]]; then
  SH_HEADER_NORM="$(read_sh_header_raw "${FLOW_DIR}")"
  LABEL="${SH_RELPATH} (Named residuals header)"
  assert_ws_contains "${SH_HEADER_NORM}" "${VERB_SURFACE_MARKER}" "${LABEL}"
  assert_ws_contains "${SH_HEADER_NORM}" "${SKIP_PERMISSIONS_MARKER}" "${LABEL}"
  assert_ws_contains "${SH_HEADER_NORM}" "${RATIONALE_MARKER}" "${LABEL}"
  assert_ws_contains "${SH_HEADER_NORM}" "${ENV_DISTINCTION_MARKER}" "${LABEL}"
  assert_ws_not_contains "${SH_HEADER_NORM}" "${RETIRED_PHRASE_1}" "${LABEL}"
  assert_ws_not_contains "${SH_HEADER_NORM}" "${RETIRED_PHRASE_2}" "${LABEL}"
fi

# =====================================================================
# Sites 2 & 3: adapter-contract.md's worktree-isolation and
# sensitive-file-refusal rows -- identical shared boundary clause.
# =====================================================================
if [[ "${MD_OK}" -eq 1 ]]; then
  MD_ROW_WORKTREE="$(read_md_row_raw "${FLOW_DIR}" "${MD_ROW_TOKEN_WORKTREE}")"
  LABEL="${MD_RELPATH} (${MD_ROW_TOKEN_WORKTREE} row)"
  assert_ws_contains "${MD_ROW_WORKTREE}" "${VERB_SURFACE_MARKER}" "${LABEL}"
  assert_ws_contains "${MD_ROW_WORKTREE}" "${SKIP_PERMISSIONS_MARKER}" "${LABEL}"
  assert_ws_contains "${MD_ROW_WORKTREE}" "${RATIONALE_MARKER}" "${LABEL}"
  assert_ws_contains "${MD_ROW_WORKTREE}" "${ENV_DISTINCTION_MARKER}" "${LABEL}"
  assert_ws_not_contains "${MD_ROW_WORKTREE}" "${RETIRED_PHRASE_1}" "${LABEL}"
  assert_ws_not_contains "${MD_ROW_WORKTREE}" "${RETIRED_PHRASE_2}" "${LABEL}"

  MD_ROW_SENSITIVE="$(read_md_row_raw "${FLOW_DIR}" "${MD_ROW_TOKEN_SENSITIVE}")"
  LABEL="${MD_RELPATH} (${MD_ROW_TOKEN_SENSITIVE} row)"
  assert_ws_contains "${MD_ROW_SENSITIVE}" "${VERB_SURFACE_MARKER}" "${LABEL}"
  assert_ws_contains "${MD_ROW_SENSITIVE}" "${SKIP_PERMISSIONS_MARKER}" "${LABEL}"
  assert_ws_contains "${MD_ROW_SENSITIVE}" "${RATIONALE_MARKER}" "${LABEL}"
  assert_ws_contains "${MD_ROW_SENSITIVE}" "${ENV_DISTINCTION_MARKER}" "${LABEL}"
  assert_ws_not_contains "${MD_ROW_SENSITIVE}" "${RETIRED_PHRASE_1}" "${LABEL}"
  assert_ws_not_contains "${MD_ROW_SENSITIVE}" "${RETIRED_PHRASE_2}" "${LABEL}"
fi

# =====================================================================
# Flow-wide sweep -- the retired allow-grant rationale must not survive
# anywhere under flow/, not just at the three sites above (AC1, AC3). This
# file itself names the retired phrases as literal string data (the
# RETIRED_PHRASE_* constants and the fixture sed patterns below), so the
# sweep excludes this file by exact absolute path -- a comment alone is not
# a sufficient carve-out (flow/tests/gh-title-payload-encoding.test.sh
# documents the same convention).
# =====================================================================
for phrase in "${RETIRED_PHRASE_1}" "${RETIRED_PHRASE_2}"; do
  RAW_HITS="$(grep -rlF --exclude-dir=.git -- "${phrase}" "${FLOW_DIR}")"
  GREP_STATUS=$?
  if [[ "${GREP_STATUS}" -eq 2 ]]; then
    fail "flow-wide sweep: grep scan error (exit 2, e.g. I/O error) while searching for retired phrase [${phrase}] under ${FLOW_DIR} -- treating as a genuine scan failure, not a confirmed absence"
    continue
  fi
  HITS="$(printf '%s\n' "${RAW_HITS}" | grep -vxF -- "${SELF_ABS}")"
  if [[ -n "${HITS}" ]]; then
    fail "flow-wide sweep: retired phrase [${phrase}] still present outside this test file: ${HITS}"
  fi
done

# =====================================================================
# Non-vacuity injections -- prove the extraction/assertion machinery above
# actually discriminates good from bad content. Built from a synthetic
# "golden" fixture shaped exactly like the corrected files once Phase 4
# lands (the real files do not carry the fixed wording yet in this red
# phase), then mutated in mktemp -d copies.
# =====================================================================
FIXTURE_DIR="$(mktemp -d)" || { echo "write-verb-residuals-contract.test.sh: failed to create fixture directory." >&2; exit 2; }
trap 'rm -rf "${FIXTURE_DIR}"' EXIT

GOLDEN_DIR="${FIXTURE_DIR}/golden"
mkdir -p "${GOLDEN_DIR}/$(dirname "${SH_RELPATH}")" "${GOLDEN_DIR}/$(dirname "${MD_RELPATH}")" \
  || { echo "write-verb-residuals-contract.test.sh: failed to create golden fixture directories." >&2; exit 2; }

cat > "${GOLDEN_DIR}/${SH_RELPATH}" <<'EOF'
#!/bin/sh
# Threat model and accepted residuals (#795):
#
#   Named residuals, accepted by settled #795 refinement decisions:
#   - `cp`, `mv`, `dd of=`, `sed -i`, and `truncate` sit entirely outside
#     this detection surface: these hooks do not inspect their write
#     targets at all, so those targets are not protected by these hooks
#     under --dangerously-skip-permissions. Each verb has a distinct
#     target grammar with ambiguous target positions, and supporting
#     them safely would materially expand parser complexity and
#     false-positive risk without justifying the added maintenance cost.
#     Where normal client permission rules are active they may add
#     defense in depth, but they are never the sandbox boundary -- the
#     container itself is.

echo "body text after the header block -- must never be included in the extracted header"
EOF

cat > "${GOLDEN_DIR}/${MD_RELPATH}" <<'EOF'
| Property | Deterministic enforcement point | Required control-point invocation | Adapters |
|---|---|---|---|
| `worktree-isolation` | Script: hooks/scripts/guard-main-worktree.sh | Coverage boundary: `cp`, `mv`, `dd of=`, `sed -i`, and `truncate` sit entirely outside this detection surface: these hooks do not inspect their write targets at all, so those targets are not protected by these hooks under --dangerously-skip-permissions. Each verb has a distinct target grammar with ambiguous target positions, and supporting them safely would materially expand parser complexity and false-positive risk without justifying the added maintenance cost. Where normal client permission rules are active they may add defense in depth, but they are never the sandbox boundary -- the container itself is. | Claude, Codex |
| `sensitive-file-refusal` | Script: hooks/scripts/check-sensitive-files.sh | Coverage boundary: `cp`, `mv`, `dd of=`, `sed -i`, and `truncate` sit entirely outside this detection surface: these hooks do not inspect their write targets at all, so those targets are not protected by these hooks under --dangerously-skip-permissions. Each verb has a distinct target grammar with ambiguous target positions, and supporting them safely would materially expand parser complexity and false-positive risk without justifying the added maintenance cost. Where normal client permission rules are active they may add defense in depth, but they are never the sandbox boundary -- the container itself is. | Claude, Codex |
EOF

# --- control: the unmodified golden fixture must satisfy every real
# assertion, or the fixture itself doesn't faithfully represent the fixed
# shape and the mutation tests below would prove nothing.
GOLDEN_SH_HEADER="$(read_sh_header_raw "${GOLDEN_DIR}")"
assert_ws_contains "${GOLDEN_SH_HEADER}" "${VERB_SURFACE_MARKER}" "non-vacuity control: golden sh fixture"
assert_ws_contains "${GOLDEN_SH_HEADER}" "${SKIP_PERMISSIONS_MARKER}" "non-vacuity control: golden sh fixture"
assert_ws_contains "${GOLDEN_SH_HEADER}" "${RATIONALE_MARKER}" "non-vacuity control: golden sh fixture"
assert_ws_contains "${GOLDEN_SH_HEADER}" "${ENV_DISTINCTION_MARKER}" "non-vacuity control: golden sh fixture"

GOLDEN_MD_WORKTREE="$(read_md_row_raw "${GOLDEN_DIR}" "${MD_ROW_TOKEN_WORKTREE}")"
assert_ws_contains "${GOLDEN_MD_WORKTREE}" "${VERB_SURFACE_MARKER}" "non-vacuity control: golden md worktree-isolation row"
assert_ws_contains "${GOLDEN_MD_WORKTREE}" "${SKIP_PERMISSIONS_MARKER}" "non-vacuity control: golden md worktree-isolation row"
assert_ws_contains "${GOLDEN_MD_WORKTREE}" "${RATIONALE_MARKER}" "non-vacuity control: golden md worktree-isolation row"
assert_ws_contains "${GOLDEN_MD_WORKTREE}" "${ENV_DISTINCTION_MARKER}" "non-vacuity control: golden md worktree-isolation row"

GOLDEN_MD_SENSITIVE="$(read_md_row_raw "${GOLDEN_DIR}" "${MD_ROW_TOKEN_SENSITIVE}")"
assert_ws_contains "${GOLDEN_MD_SENSITIVE}" "${VERB_SURFACE_MARKER}" "non-vacuity control: golden md sensitive-file-refusal row"
assert_ws_contains "${GOLDEN_MD_SENSITIVE}" "${SKIP_PERMISSIONS_MARKER}" "non-vacuity control: golden md sensitive-file-refusal row"
assert_ws_contains "${GOLDEN_MD_SENSITIVE}" "${RATIONALE_MARKER}" "non-vacuity control: golden md sensitive-file-refusal row"
assert_ws_contains "${GOLDEN_MD_SENSITIVE}" "${ENV_DISTINCTION_MARKER}" "non-vacuity control: golden md sensitive-file-refusal row"

# --- (a) delete the residuals block/row from a copy -> existence
# predicate must fail.
DELETED_SH_DIR="${FIXTURE_DIR}/sh-deleted-block"
mkdir -p "${DELETED_SH_DIR}/$(dirname "${SH_RELPATH}")" || { echo "write-verb-residuals-contract.test.sh: failed to create sh-deleted-block fixture directory." >&2; exit 2; }
printf '#!/bin/sh\n\necho "no header block at all"\n' > "${DELETED_SH_DIR}/${SH_RELPATH}"
if sh_header_markers_present "${DELETED_SH_DIR}"; then
  fail "non-vacuity: sh_header_markers_present incorrectly reports the residuals block present after it was deleted from the fixture copy"
fi

DELETED_MD_DIR="${FIXTURE_DIR}/md-deleted-row"
mkdir -p "${DELETED_MD_DIR}/$(dirname "${MD_RELPATH}")" || { echo "write-verb-residuals-contract.test.sh: failed to create md-deleted-row fixture directory." >&2; exit 2; }
printf '| Property | Deterministic enforcement point | Required control-point invocation | Adapters |\n|---|---|---|---|\n' > "${DELETED_MD_DIR}/${MD_RELPATH}"
if md_row_marker_present "${DELETED_MD_DIR}" "${MD_ROW_TOKEN_WORKTREE}"; then
  fail "non-vacuity: md_row_marker_present incorrectly reports the worktree-isolation row present after it was deleted from the fixture copy"
fi

# --- (b) corrupt one word of an anchored sentence in a copy -> positive
# assertion must fail. "expand" is a single word wholly on one physical
# source line regardless of surrounding wrap, so a plain per-line sed
# substitution is guaranteed to hit it.
CORRUPTED_SH_DIR="${FIXTURE_DIR}/sh-corrupted-word"
mkdir -p "${CORRUPTED_SH_DIR}/$(dirname "${SH_RELPATH}")" || { echo "write-verb-residuals-contract.test.sh: failed to create sh-corrupted-word fixture directory." >&2; exit 2; }
sed 's/\bexpand\b/shrink/' "${GOLDEN_DIR}/${SH_RELPATH}" > "${CORRUPTED_SH_DIR}/${SH_RELPATH}"
CORRUPTED_SH_HEADER="$(read_sh_header_raw "${CORRUPTED_SH_DIR}")"
if _contains_ws_insensitive "${CORRUPTED_SH_HEADER}" "${RATIONALE_MARKER}"; then
  fail "non-vacuity: positive assertion did not catch a corrupted word inside the anchored rationale sentence in the sh header fixture (expected [${RATIONALE_MARKER}] to be absent after corruption)"
fi

CORRUPTED_MD_DIR="${FIXTURE_DIR}/md-corrupted-word"
mkdir -p "${CORRUPTED_MD_DIR}/$(dirname "${MD_RELPATH}")" || { echo "write-verb-residuals-contract.test.sh: failed to create md-corrupted-word fixture directory." >&2; exit 2; }
sed 's/\bexpand\b/shrink/' "${GOLDEN_DIR}/${MD_RELPATH}" > "${CORRUPTED_MD_DIR}/${MD_RELPATH}"
CORRUPTED_MD_ROW="$(read_md_row_raw "${CORRUPTED_MD_DIR}" "${MD_ROW_TOKEN_WORKTREE}")"
if _contains_ws_insensitive "${CORRUPTED_MD_ROW}" "${RATIONALE_MARKER}"; then
  fail "non-vacuity: positive assertion did not catch a corrupted word inside the anchored rationale sentence in the md row fixture (expected [${RATIONALE_MARKER}] to be absent after corruption)"
fi

# --- (c) inject the retired phrase into a copy -> negative assertion must
# fire. sed 'a' inserts a whole new line after a stable whole-line landmark
# rather than editing mid-sentence, so this doesn't depend on where the
# golden fixture happens to wrap.
INJECTED_SH_DIR="${FIXTURE_DIR}/sh-injected-retired-phrase"
mkdir -p "${INJECTED_SH_DIR}/$(dirname "${SH_RELPATH}")" || { echo "write-verb-residuals-contract.test.sh: failed to create sh-injected-retired-phrase fixture directory." >&2; exit 2; }
sed "/${SH_HEADER_MARKER_2}/a\\
#   (retired phrasing injected for the non-vacuity test) already constrained by per-verb allow grant" "${GOLDEN_DIR}/${SH_RELPATH}" > "${INJECTED_SH_DIR}/${SH_RELPATH}"
INJECTED_SH_HEADER="$(read_sh_header_raw "${INJECTED_SH_DIR}")"
if ! _contains_ws_insensitive "${INJECTED_SH_HEADER}" "${RETIRED_PHRASE_1}"; then
  fail "non-vacuity: negative-assertion sweep did not detect the injected retired phrase [${RETIRED_PHRASE_1}] in the sh header fixture"
fi
if ! _contains_ws_insensitive "${INJECTED_SH_HEADER}" "${RETIRED_PHRASE_2}"; then
  fail "non-vacuity: negative-assertion sweep did not detect the injected retired phrase [${RETIRED_PHRASE_2}] in the sh header fixture"
fi

INJECTED_MD_DIR="${FIXTURE_DIR}/md-injected-retired-phrase"
mkdir -p "${INJECTED_MD_DIR}/$(dirname "${MD_RELPATH}")" || { echo "write-verb-residuals-contract.test.sh: failed to create md-injected-retired-phrase fixture directory." >&2; exit 2; }
sed 's/Coverage boundary: /Coverage boundary: already constrained by per-verb allow grant; /' "${GOLDEN_DIR}/${MD_RELPATH}" > "${INJECTED_MD_DIR}/${MD_RELPATH}"
INJECTED_MD_ROW="$(read_md_row_raw "${INJECTED_MD_DIR}" "${MD_ROW_TOKEN_WORKTREE}")"
if ! _contains_ws_insensitive "${INJECTED_MD_ROW}" "${RETIRED_PHRASE_1}"; then
  fail "non-vacuity: negative-assertion sweep did not detect the injected retired phrase [${RETIRED_PHRASE_1}] in the md row fixture"
fi
if ! _contains_ws_insensitive "${INJECTED_MD_ROW}" "${RETIRED_PHRASE_2}"; then
  fail "non-vacuity: negative-assertion sweep did not detect the injected retired phrase [${RETIRED_PHRASE_2}] in the md row fixture"
fi

echo "write-verb-residuals-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
