#!/usr/bin/env bash
# Contract test for ticket #650 — migrate refine's split child tickets to
# native GitHub sub-issues.
#
# Why this exists: the parent→child enumeration used to live in a `### Child
# Tickets` markdown checklist appended to the parent body (refine/SKILL.md
# Pass 2), read back by context-gatherer.md. GitHub's first-class sub-issue
# feature (gh `--parent` / `--add-sub-issue`, reads via `--json parent,
# subIssues,subIssuesSummary`) is now the source of truth. This test pins the
# new behavior down so a future edit can't quietly re-introduce the checklist
# or drop the native linking.
#
# Follows the idiom of flow/tests/refiner-agent-contract.test.sh: a
# `failures=` counter, small assert_* helpers, exact substring markers (never
# generic keywords — see docs/shell-scripting-gotchas.md), self-contained,
# auto-discovered by the flow gate's `*.test.sh` glob. It greps the real
# committed docs directly; no fixtures.
#
# Covered files:
#   - skills/refine/SKILL.md
#   - agents/context-gatherer.md
#   - skills/refine/codex.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-skill-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-skill-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
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

# assert_not_contains <content> <forbidden-substring> <label>
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden stale text still present: [${pattern}]"
}

# --- skills/refine/SKILL.md — native sub-issue linking replaces the checklist ---

require_doc skill "skills/refine/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  # Pass 1 links each child as a native sub-issue of the parent. Accept either
  # gh spelling of the primitive (child-side `--parent` or parent-side
  # `--add-sub-issue`); require at least one.
  if [[ "${skill}" != *"--parent"* && "${skill}" != *"--add-sub-issue"* ]]; then
    fail "skills/refine/SKILL.md: no native sub-issue link primitive (--parent or --add-sub-issue) found"
  fi
  # Verification reads the native sub-issue graph from the parent side.
  assert_contains "${skill}" "--json subIssues" "skills/refine/SKILL.md"
  assert_contains "${skill}" ".subIssues.nodes" "skills/refine/SKILL.md"

  # The old markdown checklist is gone — no `### Child Tickets` section, and no
  # `- [ ] #` checkbox enumeration of children in the parent body.
  assert_not_contains "${skill}" "### Child Tickets" "skills/refine/SKILL.md"
  assert_not_contains "${skill}" "- [ ] #" "skills/refine/SKILL.md"

  # Ordering, when it exists, is prose under `### Execution Order`, not a checklist.
  assert_contains "${skill}" "### Execution Order" "skills/refine/SKILL.md"

  # The child-body backlinks are retained (hierarchy != dependency ordering).
  assert_contains "${skill}" "Related to #" "skills/refine/SKILL.md"
  assert_contains "${skill}" "Depends on #" "skills/refine/SKILL.md"
fi

# --- agents/context-gatherer.md — detection via the native sub-issue graph ---

require_doc gatherer "agents/context-gatherer.md" || true
if [[ -n "${gatherer}" ]]; then
  # parentId primary source is the native parent field.
  assert_contains "${gatherer}" "--json parent" "agents/context-gatherer.md"
  # siblings primary source is the native subIssues node list.
  assert_contains "${gatherer}" "--json subIssues" "agents/context-gatherer.md"
  assert_contains "${gatherer}" ".subIssues.nodes" "agents/context-gatherer.md"
  # The stale checklist-parsing instruction is gone.
  assert_not_contains "${gatherer}" "### Child Tickets" "agents/context-gatherer.md"
  # The `Related to #` search fallback is retained.
  assert_contains "${gatherer}" "Related to #" "agents/context-gatherer.md"
fi

# --- skills/refine/codex.md — native behavior is portable ---

require_doc codex "skills/refine/codex.md" || true
if [[ -n "${codex}" ]]; then
  assert_contains "${codex}" "sub-issue" "skills/refine/codex.md"
  assert_contains "${codex}" "--parent" "skills/refine/codex.md"
  assert_not_contains "${codex}" "### Child Tickets" "skills/refine/codex.md"
fi

# =====================================================================
# #740 -- migrate refine's title-carrying GitHub writes from
# `TITLE=$(cat ...) && gh issue ... --title "$TITLE"` (a `&&` compound that
# can never auto-approve under any prefix rule, since both clients' approval
# systems evaluate every segment of a compound independently) to
# `gh api repos/<owner>/<repo>/... -X PATCH|POST --input <json-file>` with a
# Write-authored JSON payload, and narrow refine's allowed-tools to least
# privilege to match.
#
# Mirrors design-sandbox-guard.test.sh:149-231 (#738's own contract test):
# exhaustive Bash-grant set equality (LC_ALL=C sort -u'd) + comma-split
# bare-`Bash` rejection + negative substring checks for the dropped blanket
# grants + invocation-vs-grant `case` scans for every gh/git call actually
# present in the skill body. None of these production edits exist yet at
# RED-phase time (a later implementation phase's job) -- every assertion
# below is expected to fail until then.
#
# Grant-count note: the plan's prose calls the narrowed set "exactly 9 Bash
# grants" but its own Files to Modify bullet enumerates only 8 concrete
# `Bash(...)` entries, and every gh/git invocation site actually present in
# the skill body (cross-checked by the invocation-vs-grant scans below) maps
# onto exactly those 8 prefixes with none left over. EXPECTED_BASH_GRANTS
# below pins that self-consistent 8-entry set; if a later phase lands a
# deliberate 9th grant, this assertion is the signal to reconcile it against
# the plan rather than silently drift.
#
# #749 addendum: refine carries the identical grant defects #738/#740 left
# unaddressed. `Bash(gh issue:*)` narrows to exactly two per-verb grants
# (`view`, `edit`) — refine's body invokes no `comment`/`list`/`close`/
# `create` (issue creation goes through `gh api repos/... -X POST`, already
# covered by the `Bash(gh api repos/:*)` grant). `Bash(gh api user:*)`
# narrows to `Bash(gh api user --jq:*)` (sole call site: `ticket-ownership`'s
# `gh api user --jq .login`, read by refine). Blanket `Bash(mktemp:*)`
# narrows to `Bash(mktemp -u ${TMPDIR:-/tmp}/cenci/:*)` — refine's only
# `mktemp` call is a dry-run `-u` name generator (`mktemp -u
# ${TMPDIR:-/tmp}/cenci/issue-<n>-XXXXXX`), never `mktemp -d`; mirroring design's literal `mktemp -d` string
# would simultaneously break refine's token generation and add a grant with
# no call site. `WebFetch` is dropped — zero invocations (only prose meaning
# `gh issue view`). This grows the set from 8 to 9 entries.
# =====================================================================

skill_path="${FLOW_DIR}/skills/refine/SKILL.md"

if [[ -n "${skill}" ]]; then
  # --- Dropped blanket grants must be gone, and no curl reference may remain
  # anywhere in the skill body -- refine's only inherited curl use is via the
  # attachments reference skill (out of scope) and is expected to now prompt.
  # #749: WebFetch is dropped entirely (zero invocations -- only prose
  # meaning `gh issue view`), so there is no domain-gated (or any) WebFetch
  # grant left to cover legitimate fetches.
  assert_not_contains "${skill}" "Bash(curl:*)"  "740 skills/refine/SKILL.md blanket curl grant"
  assert_not_contains "${skill}" "Bash(gh:*)"    "740 skills/refine/SKILL.md blanket gh grant"
  assert_not_contains "${skill}" "Bash(git:*)"   "740 skills/refine/SKILL.md blanket git grant"
  assert_not_contains "${skill}" "Bash(cat:*)"   "740 skills/refine/SKILL.md blanket cat grant"
  assert_not_contains "${skill}" "Bash(rm:*)"    "740 skills/refine/SKILL.md blanket rm grant"
  assert_not_contains "${skill}" "Bash(mkdir:*)" "740 skills/refine/SKILL.md unused mkdir grant"
  assert_not_contains "${skill}" "curl" "740 skills/refine/SKILL.md any curl reference"

  # --- #749: negative assertions for every grant narrowed or dropped this
  # ticket -- the blanket gh issue/api user grants, blanket mktemp, and
  # WebFetch must all be absent.
  assert_not_contains "${skill}" "Bash(gh issue:*)" "749 skills/refine/SKILL.md blanket gh issue grant"
  assert_not_contains "${skill}" "Bash(gh api user:*)" "749 skills/refine/SKILL.md blanket gh api user grant"
  assert_not_contains "${skill}" "Bash(mktemp:*)" "749 skills/refine/SKILL.md blanket mktemp grant"
  assert_not_contains "${skill}" "WebFetch" "749 skills/refine/SKILL.md WebFetch grant"

  # --- Exhaustive set equality: parse the frontmatter's Bash(...) entries and
  # compare against the expected least-privilege list (#749: 9 entries), both
  # sorted so the comparison is order- and locale-independent.
  EXPECTED_BASH_GRANTS='Bash(gh issue view:*)
Bash(gh issue edit:*)
Bash(gh label create:*)
Bash(gh api user --jq:*)
Bash(gh api repos/:*)
Bash(git remote get-url:*)
Bash(mktemp -u ${TMPDIR:-/tmp}/cenci/:*)
Bash(cat ${TMPDIR:-/tmp}/cenci/:*)
Bash(rm -f ${TMPDIR:-/tmp}/cenci/:*)
Bash(jq -n:*)'
  allowed_line="$(grep -m1 '^allowed-tools:' "${skill_path}")"
  actual_bash_grants="$(printf '%s\n' "${allowed_line}" | grep -o 'Bash([^)]*)' | LC_ALL=C sort -u)"
  expected_bash_grants="$(printf '%s\n' "${EXPECTED_BASH_GRANTS}" | LC_ALL=C sort -u)"
  if [[ "${actual_bash_grants}" != "${expected_bash_grants}" ]]; then
    fail "740 skills/refine/SKILL.md allowed-tools Bash grant set mismatch:
--- expected ---
${expected_bash_grants}
--- actual ---
${actual_bash_grants}"
  fi

  # Bare-`Bash` rejection: the grep above is blind to a parenthesis-less
  # entry (e.g. a bare `Bash` with no scoping at all), so split the raw
  # frontmatter value on commas and check each trimmed field explicitly.
  allowed_value="${allowed_line#allowed-tools:}"
  IFS=',' read -r -a allowed_fields <<< "${allowed_value}"
  for field in "${allowed_fields[@]}"; do
    field="${field#"${field%%[![:space:]]*}"}"
    field="${field%"${field##*[![:space:]]}"}"
    if [[ "${field}" == "Bash" ]]; then
      fail "740 skills/refine/SKILL.md allowed-tools: bare, unscoped 'Bash' entry grants every shell command"
    fi
  done

  # --- Read-back pattern gone: no TITLE=$(cat read-back, no inline --title
  # interpolation anywhere in the skill body.
  assert_not_contains "${skill}" 'TITLE=$(cat' "740 skills/refine/SKILL.md TITLE=\$(cat read-back"
  assert_not_contains "${skill}" '--title "$TITLE"' "740 skills/refine/SKILL.md inline --title \"\$TITLE\""

  # --- gh api --input canonical pattern present for both edit (PATCH) and
  # create (POST), with --jq .number parsing creation success directly from
  # the API response instead of parsing a URL out of `gh issue create` output.
  assert_contains "${skill}" "gh api repos/" "740 skills/refine/SKILL.md gh api repos/ endpoint"
  assert_contains "${skill}" "/issues/" "740 skills/refine/SKILL.md issues/<number> path segment"
  assert_contains "${skill}" "-X PATCH --input" "740 skills/refine/SKILL.md -X PATCH --input (title edit)"
  assert_contains "${skill}" "-X POST --input" "740 skills/refine/SKILL.md -X POST --input (title-carrying create)"
  assert_contains "${skill}" "--jq .number" "740 skills/refine/SKILL.md --jq .number creation-success parsing"

  # --- Step 13 cleanup: rm -f reformatted onto a single line so its command
  # string actually matches the narrowed Bash(rm -f ${TMPDIR:-/tmp}/cenci/:*)
  # prefix rule (a multi-line `rm -f \` continuation makes the command string
  # start with a trailing backslash, which the prefix rule would not match).
  assert_contains "${skill}" 'rm -f ${TMPDIR:-/tmp}/cenci/issue-' "740 skills/refine/SKILL.md single-line rm -f cleanup"
  assert_not_contains "${skill}" 'rm -f \' "740 skills/refine/SKILL.md multi-line rm -f continuation"

  # --- #749: refine's only mktemp call is a dry-run `-u` name generator, path
  # -scoped to ${TMPDIR:-/tmp}/cenci/ -- pin that the actual invocation shape
  # matches the granted Bash(mktemp -u ${TMPDIR:-/tmp}/cenci/:*) prefix
  # (mirroring design's own cross-file pin for Bash(mktemp -d:*)).
  assert_contains "${skill}" 'mktemp -u ${TMPDIR:-/tmp}/cenci/issue-' "749 skills/refine/SKILL.md mktemp -u invocation shape"

  # --- Invocation-vs-grant scans (guardrails against future widening): every
  # `gh`/`git` invocation token pair actually present in the file must fall
  # under one of the granted prefixes above -- docs/shell-scripting-gotchas.md
  # names #738, #740, *and* #749 as the recurrence this guards against.
  # #749: per-verb gh arm (view/edit only -- refine posts no comments and
  # never lists/closes) so a newly-introduced ungranted verb fails here.
  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "gh issue view"*|"gh issue edit"*|"gh label create"*|"gh api user --jq"*|"gh api repos"*) ;;
      *) fail "740 skills/refine/SKILL.md: ungranted gh invocation: [${cmd}]" ;;
    esac
  done < <(grep -oE '\bgh api user --jq|\bgh [a-z]+( [a-z]+)?' "${skill_path}" | LC_ALL=C sort -u)

  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "git remote get-url"*) ;;
      *) fail "740 skills/refine/SKILL.md: ungranted git invocation: [${cmd}]" ;;
    esac
  done < <(grep -oE '\bgit [a-z-]+( [a-z-]+){0,2}' "${skill_path}" | LC_ALL=C sort -u)
fi

# --- skills/refine/codex.md -- command-surface parity note ------------------
# `codex` was already read above (native sub-issue behavior block); reuse it.
if [[ -n "${codex}" ]]; then
  assert_contains "${codex}" "gh api repos/" "740 skills/refine/codex.md gh api repos/ mention"
  assert_contains "${codex}" "Command surface (least privilege)" "740 skills/refine/codex.md least-privilege command-surface marker"
  assert_not_contains "${codex}" "goes through the client's own web-fetch capability" "749 skills/refine/codex.md stale web-fetch clause"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "refine-skill-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "refine-skill-contract.test.sh: all checks passed."
