#!/usr/bin/env bash
# Contract test for ticket #773 -- finish the least-privilege allowed-tools
# sweep milestone #735/#738/#740/#749 started for design/refine/maintain,
# extending it to the six remaining user-invocable flow skills (babysit,
# sync, review, refactor, address-review, configure) and migrating the last
# body command-substitution posting sites onto file-based flags.
#
# Follows the idiom of flow/tests/design-sandbox-guard.test.sh and
# flow/tests/refine-skill-contract.test.sh: a `failures=` counter, small
# assert_* helpers, exact substring markers (never generic keywords -- see
# docs/shell-scripting-gotchas.md), self-contained, auto-discovered by the
# flow gate's `*.test.sh` glob. It greps the real committed docs directly;
# no fixtures.
#
# None of the production edits this test pins down exist yet at RED-phase
# time (a later implementation phase's job) -- every assertion below is
# expected to fail until then.
#
# Invocation-vs-grant scan scoping: scans below are restricted to fenced
# ```bash blocks (via extract_bash_fences), not the whole file. A whole-file
# scan false-positives on prose that merely contains "git"/"gh"/"cenci"
# followed by an English word that happens to look like a subcommand (e.g.
# refactor/SKILL.md's "no recent git changes found", babysit/SKILL.md's
# "launches the selected client through `cenci run`" describing the
# *supervisor's* own action, not this skill's). Fenced-block scoping avoids
# both false positives while still catching every real invocation this
# skill's own agent turn issues. One consequence: an invocation referenced
# only as inline single-backtick code in prose (e.g. refactor/SKILL.md's
# Prerequisites paragraph naming `git remote get-url origin`) is not scanned
# here -- the exhaustive EXPECTED_BASH_GRANTS set-equality check below is the
# assertion of record for grant *existence*; the fenced-block scan is the
# supplementary guardrail against a future *widening* of what a fenced,
# actually-executed invocation is allowed to be.
#
# configure/SKILL.md is exempted from the invocation-vs-grant scan entirely
# (see docs/shell-scripting-gotchas.md's carve-out convention): its
# `permissions.deny` section documents dozens of `git ...` strings as data
# (the deny-list entries themselves, e.g. "git push --force *", "git -C
# <path>", "git rebase", "git branch -D", "git worktree remove", "git config
# --get/--list"), which a naive scan would misread as ungranted invocations.
# configure gets exhaustive grant set-equality plus explicit retired-pattern
# negative assertions instead.
#
# Covered files:
#   - skills/babysit/SKILL.md
#   - skills/sync/SKILL.md
#   - skills/review/SKILL.md
#   - skills/refactor/SKILL.md
#   - skills/address-review/SKILL.md
#   - skills/configure/SKILL.md
#   - skills/implement/SKILL.md (context7 grant style)
#   - agents/planner.md, agents/refiner.md, agents/implementer.md (context7 grant style)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "allowed-tools-sweep.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "allowed-tools-sweep.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc() {
  # read_doc <flow-relative-path> -- prints the real committed file's content,
  # or fails closed with a distinct "not found" message (a missing file must
  # never masquerade as empty content, which would make assert_not_contains
  # trivially pass).
  local path="${FLOW_DIR}/$1"
  local content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "$1: doc not found/unreadable: ${path}"
    printf ''
    return 1
  fi
  printf '%s' "${content}"
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

# extract_bash_fences <file> -- prints the concatenated content of every
# fenced ```bash ... ``` block in <file>, tolerating list-item indentation
# (the fence marker is not always at column 0 -- e.g. sync/SKILL.md's Step 3
# sub-bullets indent their fences by 3 spaces). See docs/shell-scripting-
# gotchas.md's fence-awareness entry (#782) for why naive `^```bash` anchors
# under-match indented fences.
extract_bash_fences() {
  awk '/^[[:space:]]*```bash[[:space:]]*$/{f=1;next} /^[[:space:]]*```[[:space:]]*$/{f=0;next} f' "$1"
}

# check_bash_grant_set <skill_path> <newline-separated-expected-grants> <label>
# Exhaustive set equality: parse the frontmatter's Bash(...) entries and
# compare against the exact expected least-privilege list, both LC_ALL=C
# sorted so the comparison is order- and locale-independent. Also rejects a
# bare, unscoped `Bash` entry via a comma-split of the raw frontmatter value
# (the set-equality grep above is blind to a parenthesis-less field).
check_bash_grant_set() {
  local skill_path="$1" expected="$2" label="$3"
  local allowed_line actual_bash_grants expected_bash_grants
  allowed_line="$(grep -m1 '^allowed-tools:' "${skill_path}" 2>/dev/null)"
  if [[ -z "${allowed_line}" ]]; then
    fail "${label}: no 'allowed-tools:' frontmatter line found in ${skill_path}"
    return
  fi
  actual_bash_grants="$(printf '%s\n' "${allowed_line}" | grep -o 'Bash([^)]*)' | LC_ALL=C sort -u)"
  expected_bash_grants="$(printf '%s\n' "${expected}" | LC_ALL=C sort -u)"
  if [[ "${actual_bash_grants}" != "${expected_bash_grants}" ]]; then
    fail "${label} allowed-tools Bash grant set mismatch:
--- expected ---
${expected_bash_grants}
--- actual ---
${actual_bash_grants}"
  fi

  local allowed_value allowed_fields field
  allowed_value="${allowed_line#allowed-tools:}"
  IFS=',' read -r -a allowed_fields <<< "${allowed_value}"
  for field in "${allowed_fields[@]}"; do
    field="${field#"${field%%[![:space:]]*}"}"
    field="${field%"${field##*[![:space:]]}"}"
    if [[ "${field}" == "Bash" ]]; then
      fail "${label} allowed-tools: bare, unscoped 'Bash' entry grants every shell command"
    fi
  done
}

# assert_grant_field <skill_path> <exact-field> <label> -- asserts a plain
# (non-Bash) tool name is present as its own comma-split field in
# allowed-tools (e.g. babysit's new Read grant, review/refactor's new Write
# grant) -- a plain substring check would false-pass on e.g. "Read" matching
# inside a longer, unrelated token.
assert_grant_field() {
  local skill_path="$1" want="$2" label="$3"
  local allowed_line allowed_value allowed_fields field found=0
  allowed_line="$(grep -m1 '^allowed-tools:' "${skill_path}" 2>/dev/null)"
  allowed_value="${allowed_line#allowed-tools:}"
  IFS=',' read -r -a allowed_fields <<< "${allowed_value}"
  for field in "${allowed_fields[@]}"; do
    field="${field#"${field%%[![:space:]]*}"}"
    field="${field%"${field##*[![:space:]]}"}"
    [[ "${field}" == "${want}" ]] && found=1
  done
  [[ "${found}" -eq 1 ]] || fail "${label}: allowed-tools missing exact '${want}' grant"
}

# assert_dropped_blanket_grants <skill_path> <content> <label> -- every
# blanket grant #738/#740/#749/#773 have collectively retired must never
# reappear in any of the six swept skills.
assert_dropped_blanket_grants() {
  local content="$1" label="$2"
  assert_not_contains "${content}" "Bash(gh:*)" "${label} blanket gh grant"
  assert_not_contains "${content}" "Bash(git:*)" "${label} blanket git grant"
  assert_not_contains "${content}" "Bash(echo:*)" "${label} echo grant"
}

# =====================================================================
# skills/babysit/SKILL.md
# =====================================================================

babysit_path="${FLOW_DIR}/skills/babysit/SKILL.md"
babysit="$(read_doc "skills/babysit/SKILL.md")" || true
if [[ -n "${babysit}" ]]; then
  BABYSIT_EXPECTED_GRANTS='Bash(cenci babysit:*)
Bash(sh "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh":*)'
  check_bash_grant_set "${babysit_path}" "${BABYSIT_EXPECTED_GRANTS}" "skills/babysit/SKILL.md"
  assert_dropped_blanket_grants "${babysit}" "skills/babysit/SKILL.md"
  assert_grant_field "${babysit_path}" "Read" "skills/babysit/SKILL.md"

  # Frontmatter consistency: the only user-invocable flow skill missing all
  # three of these today (flow/README.md:554 already claims babysit "pins
  # model: sonnet", which is only true once this lands).
  if ! grep -q '^disable-model-invocation: true$' "${babysit_path}" 2>/dev/null; then
    fail "skills/babysit/SKILL.md: missing 'disable-model-invocation: true' frontmatter line"
  fi
  if ! grep -q '^compatibility:' "${babysit_path}" 2>/dev/null; then
    fail "skills/babysit/SKILL.md: missing 'compatibility:' frontmatter line"
  fi
  if ! grep -q '^model: sonnet$' "${babysit_path}" 2>/dev/null; then
    fail "skills/babysit/SKILL.md: missing 'model: sonnet' frontmatter line"
  fi

  # The resolver's invocation shape must be pinned as a fenced `bash` block
  # matching the granted prefix exactly -- today it is prose-only (no `sh
  # "..."` invocation shape anywhere in the file), so no body line maps to
  # the grant yet.
  assert_contains "${babysit}" 'sh "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh"' \
    "skills/babysit/SKILL.md resolver invocation shape"

  # Invocation-vs-grant scan (fenced blocks only -- see header comment):
  # every `cenci ...` invocation actually present must fall under the
  # granted `cenci babysit` prefix. Catches a future widening (e.g. a
  # fenced example that runs `cenci sandbox build` or similar) without
  # false-positiving on prose like "launches the selected client through
  # `cenci run`", which describes the *supervisor's* own action and is
  # never in a fenced block.
  #
  # Guard against a vacuous pass: if extract_bash_fences ever stops matching
  # this file's actual fenced blocks (format drift, wrong language tag), the
  # scan loop below would iterate zero times and silently report full
  # coverage with zero checks performed.
  [[ -n "$(extract_bash_fences "${babysit_path}")" ]] || fail "skills/babysit/SKILL.md: no fenced bash blocks found -- invocation-vs-grant scan produced zero coverage"
  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "cenci babysit"*) ;;
      *) fail "skills/babysit/SKILL.md: ungranted cenci invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${babysit_path}" | grep -oE '\bcenci [a-z]+( [a-z]+)?' | LC_ALL=C sort -u)
fi

# =====================================================================
# skills/sync/SKILL.md
# =====================================================================

sync_path="${FLOW_DIR}/skills/sync/SKILL.md"
sync="$(read_doc "skills/sync/SKILL.md")" || true
if [[ -n "${sync}" ]]; then
  SYNC_EXPECTED_GRANTS='Bash(git status:*)
Bash(git stash:*)
Bash(git add:*)
Bash(git commit:*)
Bash(git fetch:*)
Bash(git checkout:*)
Bash(git pull --ff-only:*)
Bash(git branch -v:*)
Bash(git branch -D:*)
Bash(git worktree list:*)
Bash(git worktree prune:*)
Bash(git worktree remove:*)
Bash(git -C:*)'
  check_bash_grant_set "${sync_path}" "${SYNC_EXPECTED_GRANTS}" "skills/sync/SKILL.md"
  assert_dropped_blanket_grants "${sync}" "skills/sync/SKILL.md"

  # Invocation-vs-grant scan (fenced blocks only).
  #
  # Guard against a vacuous pass: if extract_bash_fences ever stops matching
  # this file's actual fenced blocks (format drift, wrong language tag), the
  # scan loop below would iterate zero times and silently report full
  # coverage with zero checks performed.
  [[ -n "$(extract_bash_fences "${sync_path}")" ]] || fail "skills/sync/SKILL.md: no fenced bash blocks found -- invocation-vs-grant scan produced zero coverage"
  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "git status"*|"git stash"*|"git add"*|"git commit"*|"git fetch"*|"git checkout"*|"git pull --ff-only"*|"git branch -v"*|"git branch -D"*|"git worktree list"*|"git worktree prune"*|"git worktree remove"*|"git -C"*) ;;
      *) fail "skills/sync/SKILL.md: ungranted git invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${sync_path}" | grep -oE '\bgit [A-Za-z-]+( [A-Za-z-]+){0,2}' | LC_ALL=C sort -u)
fi

# =====================================================================
# skills/review/SKILL.md
# =====================================================================

review_path="${FLOW_DIR}/skills/review/SKILL.md"
review="$(read_doc "skills/review/SKILL.md")" || true
if [[ -n "${review}" ]]; then
  REVIEW_EXPECTED_GRANTS='Bash(git diff:*)
Bash(git remote get-url:*)
Bash(gh pr diff:*)
Bash(gh pr view:*)
Bash(gh pr comment:*)'
  check_bash_grant_set "${review_path}" "${REVIEW_EXPECTED_GRANTS}" "skills/review/SKILL.md"
  assert_dropped_blanket_grants "${review}" "skills/review/SKILL.md"
  assert_grant_field "${review_path}" "Write" "skills/review/SKILL.md"

  # Invocation-vs-grant scan (fenced blocks only).
  #
  # Guard against a vacuous pass: if extract_bash_fences ever stops matching
  # this file's actual fenced blocks (format drift, wrong language tag), the
  # scan loops below would iterate zero times and silently report full
  # coverage with zero checks performed.
  [[ -n "$(extract_bash_fences "${review_path}")" ]] || fail "skills/review/SKILL.md: no fenced bash blocks found -- invocation-vs-grant scan produced zero coverage"
  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "git diff"*|"git remote get-url"*) ;;
      *) fail "skills/review/SKILL.md: ungranted git invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${review_path}" | grep -oE '\bgit [A-Za-z-]+( [A-Za-z-]+){0,2}' | LC_ALL=C sort -u)

  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "gh pr diff"*|"gh pr view"*|"gh pr comment"*) ;;
      *) fail "skills/review/SKILL.md: ungranted gh invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${review_path}" | grep -oE '\bgh [a-z]+( [a-z]+)?' | LC_ALL=C sort -u)
fi

# =====================================================================
# skills/refactor/SKILL.md
# =====================================================================

refactor_path="${FLOW_DIR}/skills/refactor/SKILL.md"
refactor="$(read_doc "skills/refactor/SKILL.md")" || true
if [[ -n "${refactor}" ]]; then
  REFACTOR_EXPECTED_GRANTS='Bash(git diff --name-only:*)
Bash(git log:*)
Bash(git remote get-url:*)
Bash(wc -l:*)
Bash(jq -n:*)
Bash(gh api repos/:*)'
  check_bash_grant_set "${refactor_path}" "${REFACTOR_EXPECTED_GRANTS}" "skills/refactor/SKILL.md"
  assert_dropped_blanket_grants "${refactor}" "skills/refactor/SKILL.md"
  assert_grant_field "${refactor_path}" "Write" "skills/refactor/SKILL.md"

  # Closes the last live `gh issue create --title` in flow (Q1): the ticket
  # creation site migrates to `gh api repos/... -X POST --input ... --jq
  # .number`.
  assert_not_contains "${refactor}" "gh issue create" "skills/refactor/SKILL.md retired gh issue create"

  # Invocation-vs-grant scan (fenced blocks only). Today's fenced Phase 6
  # block still runs `gh issue create --repo ... --title ...`, which falls
  # under none of the granted `gh api repos/` prefix -- this is the
  # assertion that must fail today for the ticket's headline reason.
  #
  # Guard against a vacuous pass: if extract_bash_fences ever stops matching
  # this file's actual fenced blocks (format drift, wrong language tag), the
  # scan loops below would iterate zero times and silently report full
  # coverage with zero checks performed.
  [[ -n "$(extract_bash_fences "${refactor_path}")" ]] || fail "skills/refactor/SKILL.md: no fenced bash blocks found -- invocation-vs-grant scan produced zero coverage"
  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "git diff --name-only"*|"git log"*|"git remote get-url"*) ;;
      *) fail "skills/refactor/SKILL.md: ungranted git invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${refactor_path}" | grep -oE '\bgit [A-Za-z-]+( [A-Za-z-]+){0,2}' | LC_ALL=C sort -u)

  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "gh api repos"*) ;;
      *) fail "skills/refactor/SKILL.md: ungranted gh invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${refactor_path}" | grep -oE '\bgh [a-z]+( [a-z]+)?' | LC_ALL=C sort -u)
fi

# =====================================================================
# skills/address-review/SKILL.md
# =====================================================================

address_review_path="${FLOW_DIR}/skills/address-review/SKILL.md"
address_review="$(read_doc "skills/address-review/SKILL.md")" || true
if [[ -n "${address_review}" ]]; then
  ADDRESS_REVIEW_EXPECTED_GRANTS='Bash(gh pr view:*)
Bash(gh pr checkout:*)
Bash(gh pr comment:*)
Bash(gh pr edit:*)
Bash(gh api repos/:*)
Bash(gh issue view:*)
Bash(gh issue edit:*)
Bash(gh issue list:*)
Bash(gh label create:*)
Bash(git remote get-url:*)
Bash(git worktree list:*)
Bash(git pull --rebase:*)
Bash(git diff --name-only:*)
Bash(git add:*)
Bash(git commit:*)
Bash(git push origin:*)
Bash(jq -n:*)
Bash(rm -f ${TMPDIR:-/tmp}/cenci/:*)'
  check_bash_grant_set "${address_review_path}" "${ADDRESS_REVIEW_EXPECTED_GRANTS}" "skills/address-review/SKILL.md"
  assert_dropped_blanket_grants "${address_review}" "skills/address-review/SKILL.md"

  # Q2: inline-reply and general-reply posting sites migrate off
  # `-f body="$REPLY"` / `--body "$COMMENT"` read-back onto `jq -n --rawfile`
  # + `gh api ... --input` / `--body-file`.
  assert_not_contains "${address_review}" '-f body="$REPLY"' "skills/address-review/SKILL.md retired -f body=\$REPLY inline reply"

  # Invocation-vs-grant scan (fenced blocks only).
  #
  # Guard against a vacuous pass: if extract_bash_fences ever stops matching
  # this file's actual fenced blocks (format drift, wrong language tag), the
  # scan loops below would iterate zero times and silently report full
  # coverage with zero checks performed.
  [[ -n "$(extract_bash_fences "${address_review_path}")" ]] || fail "skills/address-review/SKILL.md: no fenced bash blocks found -- invocation-vs-grant scan produced zero coverage"
  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "git remote get-url"*|"git worktree list"*|"git pull --rebase"*|"git diff --name-only"*|"git add"*|"git commit"*|"git push origin"*) ;;
      *) fail "skills/address-review/SKILL.md: ungranted git invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${address_review_path}" | grep -oE '\bgit [A-Za-z-]+( [A-Za-z-]+){0,2}' | LC_ALL=C sort -u)

  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "gh pr view"*|"gh pr checkout"*|"gh pr comment"*|"gh pr edit"*|"gh api repos"*|"gh issue view"*|"gh issue edit"*|"gh issue list"*|"gh label create"*) ;;
      *) fail "skills/address-review/SKILL.md: ungranted gh invocation: [${cmd}]" ;;
    esac
  done < <(extract_bash_fences "${address_review_path}" | grep -oE '\bgh [a-z]+( [a-z]+)?' | LC_ALL=C sort -u)
fi

# =====================================================================
# skills/configure/SKILL.md -- exempted from the invocation-vs-grant scan
# (permissions.deny prose enumerates dozens of git strings as data -- see
# header comment); grant-presence (via exhaustive set equality) plus
# retired-pattern negatives instead.
# =====================================================================

configure_path="${FLOW_DIR}/skills/configure/SKILL.md"
configure="$(read_doc "skills/configure/SKILL.md")" || true
if [[ -n "${configure}" ]]; then
  CONFIGURE_EXPECTED_GRANTS='Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/detect-project.sh")
Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/merge-sandbox-config.sh":*)
Bash(bash "${CLAUDE_PLUGIN_ROOT}/scripts/migrate-project-core.sh":*)
Bash(test:*)
Bash(which:*)
Bash(jq:*)
Bash(mv ~/.claude/settings.json.tmp ~/.claude/settings.json)
Bash(mkdir -p:*)
Bash(rm -f .claude/hooks/check-pending-plans.sh)
Bash(rmdir .claude/hooks:*)
Bash(gh auth status:*)
Bash(gh label list:*)
Bash(gh label create:*)
Bash(gh pr create:*)
Bash(gh pr view:*)
Bash(git rev-parse:*)
Bash(git add:*)
Bash(git commit:*)
Bash(git worktree add:*)
Bash(git -C:*)
Bash(git diff --no-index:*)
Bash(rm -f ${TMPDIR:-/tmp}/cenci/cenci-configure-:*)'
  check_bash_grant_set "${configure_path}" "${CONFIGURE_EXPECTED_GRANTS}" "skills/configure/SKILL.md"
  # Test-bug fix (#773, narrow): unlike the other five swept skills,
  # assert_dropped_blanket_grants is NOT called for configure here. Per the
  # plan's own Test Strategy table ("configure grant set ... grant-presence +
  # retired-pattern negatives only -- a whole-file token scan false-positives
  # on the permissions.deny command list, which is data, not invocations"),
  # configure is deliberately exempted from whole-file scans. This isn't only
  # the permissions.deny list: configure's settings.json-template callout
  # legitimately documents that template's own base permissions as literal
  # `Bash(git:*)`/`Bash(gh:*)` entries (see flow/templates/settings.json) --
  # a different artifact's grants, not this skill's own. check_bash_grant_set
  # above already exhaustively pins configure's actual frontmatter grant set
  # (and independently rejects a bare, unscoped `Bash` via its comma-split
  # check), so configure's own grants can never silently re-widen to a
  # blanket form even without the generic whole-file negative scan.

  # Retired-pattern negatives: the #749 echo-redirection hole class, closed
  # here for configure's two settings.json fallback chains and its
  # container-detection probe (Q3/#749 precedent).
  assert_not_contains "${configure}" 'echo "${CENCI_SANDBOX:-}"' "skills/configure/SKILL.md retired echo CENCI_SANDBOX probe"
  assert_not_contains "${configure}" '|| echo' "skills/configure/SKILL.md retired echo-fallback settings.json write"

  # Step 1 of Create Worktree splits into two standalone Bash calls (no
  # `&&` compound -- shell-rules: every segment of a compound is evaluated
  # independently by the approval system).
  assert_not_contains "${configure}" 'git add -A && git commit' "skills/configure/SKILL.md git add/commit compound"
fi

# =====================================================================
# context7 grant-style standardization: bare `mcp__context7` -> enumerated
# `mcp__context7__resolve-library-id, mcp__context7__query-docs` (matching
# agents/security-analyzer.md's already-enumerated form, left untouched).
# =====================================================================

CONTEXT7_ENUMERATED='mcp__context7__resolve-library-id, mcp__context7__query-docs'
CONTEXT7_BARE_COMMA='mcp__context7,'
CONTEXT7_BARE_EOL=$'mcp__context7\n'

check_context7_enumerated() {
  local relpath="$1" content
  content="$(read_doc "${relpath}")" || return
  [[ -n "${content}" ]] || return
  assert_contains "${content}" "${CONTEXT7_ENUMERATED}" "${relpath} enumerated context7 grant"
  assert_not_contains "${content}" "${CONTEXT7_BARE_COMMA}" "${relpath} bare context7 grant (mid-list comma form)"
  assert_not_contains "${content}" "${CONTEXT7_BARE_EOL}" "${relpath} bare context7 grant (end-of-line form)"
}

check_context7_enumerated "skills/implement/SKILL.md"
check_context7_enumerated "agents/planner.md"
check_context7_enumerated "agents/refiner.md"
check_context7_enumerated "agents/implementer.md"

# =====================================================================
# Body-migration negatives: no flow skill markdown file may retain a
# `VAR=$(cat <file>)` read-back, a `--body "$VAR"` interpolation, or a
# `printf '%s' '<literal>' > /tmp/...` redirection-based temp-file write --
# the same shell-redirection/command-substitution hole class #749 closed for
# design, now migrated onto `--body-file` / `gh api --input` / the `Write`
# tool at review/refactor/address-review's three remaining sites.
# =====================================================================

SKILLS_DIR="${FLOW_DIR}/skills"
if [[ ! -d "${SKILLS_DIR}" ]]; then
  fail "flow/skills directory not found: ${SKILLS_DIR}"
else
  DOLLAR_CAT_HITS="$(grep -rlF --include='*.md' -- '$(cat' "${SKILLS_DIR}" 2>/dev/null)"
  if [[ -n "${DOLLAR_CAT_HITS}" ]]; then
    fail "no flow skill markdown file may retain a \$(cat read-back; found in: ${DOLLAR_CAT_HITS}"
  fi

  BODY_VAR_HITS="$(grep -rlF --include='*.md' -- '--body "$' "${SKILLS_DIR}" 2>/dev/null)"
  if [[ -n "${BODY_VAR_HITS}" ]]; then
    fail "no flow skill markdown file may retain a --body \"\$VAR\" interpolation; found in: ${BODY_VAR_HITS}"
  fi

  PRINTF_REDIRECT_HITS="$(grep -rlF --include='*.md' -- "printf '%s' '" "${SKILLS_DIR}" 2>/dev/null)"
  if [[ -n "${PRINTF_REDIRECT_HITS}" ]]; then
    fail "no flow skill markdown file may retain a printf '%s' '<literal>' > /tmp redirection write; found in: ${PRINTF_REDIRECT_HITS}"
  fi
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "allowed-tools-sweep.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "allowed-tools-sweep.test.sh: all checks passed."
