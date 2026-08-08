#!/usr/bin/env bash
# Contract test for ticket #647 — make `/cenci:design` host-only.
#
# Why this exists: design targets the desktop Pencil app and, when unreachable,
# auto-launches it with retries. Inside the cenci sandbox no desktop app can
# ever exist, so an in-sandbox invocation used to fail slowly through the
# retries with a misleading message. This test pins down the Phase 0.5 guard
# that fails fast before any probe/auto-launch, its mirror in the Codex
# adapter, and the doc pointers (`flow/README.md`, `configure/SKILL.md`,
# `sandbox/README.md`, `docs/orchestration.md`) that keep the host-only split
# discoverable.
#
# #738 addendum: because design dispatches unsandboxed on the host (#734)
# while reading untrusted GitHub issue bodies/comments, its blanket
# Bash(curl:*)/Bash(gh:*)/Bash(git:*)/Bash(pencil:*) grants would let a
# prompt-injected issue body reach curl/gh/git/pencil with no container
# boundary. This test also pins the narrowed, exact-prefix `allowed-tools`
# grant set (negative substring checks + exhaustive set equality against the
# 13-entry expected list + a bare-`Bash` rejection), the git add/commit
# compound split into two standalone Bash calls, and invocation-vs-grant
# scans that every `gh`/`git` call in the skill body falls under a granted
# prefix — so a future edit cannot silently re-widen the grant back to a
# blanket form.
#
# #749 addendum: #738 narrowed to blanket-verb prefixes; #749 narrows
# further to per-verb / exact-invocation-shape grants and closes three
# residual gaps found while doing so. `Bash(gh issue:*)` becomes five
# per-verb grants (`view`, `edit`, `comment`, `list`, `close`) so the
# invocation-vs-grant scan below (retuned to a per-verb arm) catches a newly
# ungranted verb like `create`/`transfer`/`develop`. `Bash(gh api user:*)`
# narrows to `Bash(gh api user --jq:*)` (the sole call site is `ticket-
# ownership`'s `gh api user --jq .login`). `Bash(echo:*)` is dropped — not
# because `echo` gained a new call site, but because the `Write|Edit`
# guard-hook matcher can never see `echo … > .git/hooks/pre-commit`
# redirection, which chained with design's own granted `git commit` is a
# live injection → host-code-execution path; this reverses #738's "do not
# touch echo" instruction, superseded by that redirection analysis. The
# Phase 0.5 probe's first step moves to `test "${CENCI_SANDBOX:-}" = "1"`
# under the already-granted `Bash(test:*)`, mirroring
# `configure/scripts/detect-project.sh` more closely than the old
# echo-and-eyeball form. `WebFetch` is dropped — zero invocations anywhere in
# the skill body, the same "grant with no call site" evidence that justified
# dropping `Bash(curl:*)` in #738; the L158 comment claiming a "domain-gated
# WebFetch grant" is corrected since the grant was never domain-gated.
# `Bash(mktemp -d:*)` is added (exact invocation shape, not blanket
# `Bash(mktemp:*)`) so Phase 4A's screenshot directory can be created via a
# standalone `mktemp -d` call instead of the old bare-`$TMPDIR` `mkdir -p`
# whose path was also never expanded inside the quoted Phase 4A heredoc.
#
# #957 addendum: ports `/cenci:design` off the retired `pencil`/`pencil
# interactive` binary and the old ~18-tool MCP surface onto the current `pen`
# CLI and its narrowed 5-tool MCP surface (`get_app_state`, `execute`,
# `get_screenshot`, `export_nodes`, `get_guidelines`), via a new shared,
# non-user-invocable `pencil-api` reference skill that documents the MCP
# surface, the `execute` idiom catalog, the transport table, and document
# discipline once instead of duplicating it inline. The three exact-shape
# Bash grants narrow from `Bash(pencil)` / `Bash(pencil interactive:*)` /
# `Bash(which pencil:*)` to `Bash(pen)` / `Bash(pen interactive:*)` /
# `Bash(which pen:*)` — still 17 grants total, no widening. Ten retired MCP
# tool names (`get_editor_state`, `batch_get`, `batch_design`,
# `set_variables`, `get_variables`, `snapshot_layout`, `open_document`,
# `find_empty_space_on_canvas`, `replace_all_matching_properties`,
# `search_all_unique_properties`) must never appear anywhere in
# `design/SKILL.md`, `design/codex.md`, or the new `pencil-api/SKILL.md` — the
# new `execute({ input: '<Pencil-script>' })` idiom replaces their direct
# per-tool MCP calls. The probe shape in Phase 0.5 pins the new
# `execute({ input: 'Print(1)' })` connectivity check. Step 7B's posted design
# comment gains a `### Screen nodes` subsection while keeping the existing
# `<!-- cenci-design-summary -->` marker un-blockquoted, per
# `docs/comment-attribution.md`.
#
# Follows the idiom of flow/tests/refine-skill-contract.test.sh: a
# `failures=` counter, small assert_* helpers, exact substring markers (never
# generic keywords — see docs/shell-scripting-gotchas.md), self-contained,
# auto-discovered by the flow gate's `*.test.sh` glob. It greps the real
# committed docs directly; no fixtures.
#
# CI path-filter caveat (accepted per ticket #647 Q3): this suite lives under
# `flow/tests/**` and is only run by `flow-ci.yml`'s `flow-test` job, which is
# path-filtered on `flow == 'true'`. Two of the assertions below reach outside
# `flow/**` (`sandbox/README.md`, `docs/orchestration.md`). A future PR that
# edits only one of those two files will not trigger `flow-ci` and so will not
# run this suite in CI — only the local flow gate (or the next flow-touching
# PR) would catch a regression there. This is a documented, accepted coupling
# risk, not a bug in this test.
#
# Covered files:
#   - flow/skills/design/SKILL.md
#   - flow/skills/design/codex.md
#   - flow/README.md
#   - flow/skills/configure/SKILL.md
#   - flow/skills/pencil-api/SKILL.md
#   - sandbox/README.md (cross-project)
#   - docs/orchestration.md (cross-project)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "design-sandbox-guard.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "design-sandbox-guard.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "design-sandbox-guard.test.sh: failed to resolve repo root." >&2; exit 2; }
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

read_repo_doc_raw() {
  # read_repo_doc_raw <repo-relative-path> — same pure-extraction contract as
  # read_doc_raw, but resolved from REPO_ROOT for the two cross-project docs
  # (sandbox/README.md, docs/orchestration.md) that live outside flow/**. No
  # fail() side effect here: it is deliberately safe to call inside a $(...)
  # command substitution.
  local _relpath="$1"
  local _path="${REPO_ROOT}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_repo_doc <result-var> <repo-relative-path> — nameref wrapper
# around read_repo_doc_raw. Must NOT be invoked via $(...).
require_repo_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_repo_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${REPO_ROOT}/${_relpath}"
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

# assert_no_stale_pencil_binary <content> <label> — the five negative-literal
# checks for the retired `pencil`/`pencil interactive` binary (957 AC #5),
# shared across design/SKILL.md, design/codex.md, and configure/SKILL.md so a
# stale reference can't linger in any of the three. These are scoped
# literals, never a bare "pencil" (which would false-fail on
# mcp__pencil__*, pencil.mode, pencil.enabled, $PENCIL_MODE — all
# still-valid product-name references this ticket does not touch).
assert_no_stale_pencil_binary() {
  local content="$1" label="$2"
  assert_not_contains "${content}" "Bash(pencil" "${label} stale pencil grant"
  assert_not_contains "${content}" "pencil interactive" "${label} stale pencil interactive invocation"
  assert_not_contains "${content}" "which pencil" "${label} stale which pencil invocation"
  assert_not_contains "${content}" '`pencil`' "${label} stale pencil literal invocation"
  assert_not_contains "${content}" "pencil &" "${label} stale pencil background launch"
}

# The ticket's exact four-sentence stop message (verbatim, do not paraphrase).
STOP_MESSAGE='`/cenci:design` must run from a host session: the Pencil desktop app is not reachable from inside the cenci sandbox. Exit the container and re-run `/cenci:design <args>` on the host. Sandboxed sessions get design access through headless reads only (`/cenci:implement`, `verify-ui`).'

# The reason-establishing clause shared verbatim between SKILL.md and the
# Codex mirror (the "same load-bearing sentence" the plan calls for).
STOP_MESSAGE_CORE='the Pencil desktop app is not reachable from inside the cenci sandbox'

# --- skills/design/SKILL.md — the host-only guard ---------------------------

require_doc skill "skills/design/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  # 1. Exact stop-message sentence, whitespace-normalized (markdown wraps lines).
  assert_contains_ws "${skill}" "${STOP_MESSAGE}" "skills/design/SKILL.md stop message"

  # 3. Both detection anchors present (mirrors detect-project.sh:74-79).
  assert_contains "${skill}" "CENCI_SANDBOX" "skills/design/SKILL.md CENCI_SANDBOX anchor"
  assert_contains "${skill}" "/.dockerenv" "skills/design/SKILL.md /.dockerenv anchor"

  # 4. Frontmatter grants the Bash primitive the guard's host-path probe
  #    needs (#749: Bash(test:*) now backs BOTH legs of the Phase 0.5 probe
  #    — `test "${CENCI_SANDBOX:-}" = "1"` and `test -f /.dockerenv` — plus
  #    Phase 4A's `test -d <screenshot-dir>` verification. Bash(echo:*) is
  #    gone: the Phase 0.5 probe no longer uses echo at all (see the #749
  #    addendum above).
  assert_contains "${skill}" "Bash(test:*)" "skills/design/SKILL.md allowed-tools test grant"

  # skill_path is shared by both the least-privilege block below and the
  # placement block further down (hoisted from its original placement-block
  # assignment so both blocks can use it).
  skill_path="${FLOW_DIR}/skills/design/SKILL.md"

  # --- #738: least-privilege allowed-tools ---------------------------------
  # design dispatches unsandboxed on the host (#734) while reading untrusted
  # GitHub issue bodies/comments; blanket Bash(curl:*)/Bash(gh:*)/Bash(git:*)
  # grants would let a prompt-injected issue body reach curl/gh/git with no
  # container boundary. These assertions pin the narrowed, exact-prefix grant
  # set so a future edit cannot silently re-widen it back to a blanket form.

  # AC #1/#2/#3: the four old blanket grants must never reappear, and no
  # `curl` reference (grant or invocation) may exist anywhere in the skill.
  # #749: WebFetch is dropped entirely — it was never domain-gated, and had
  # zero invocations anywhere in the skill body (the same "grant with no
  # call site" evidence that justified dropping Bash(curl:*) in #738).
  assert_not_contains "${skill}" "Bash(curl:*)" "skills/design/SKILL.md blanket curl grant"
  assert_not_contains "${skill}" "Bash(gh:*)"   "skills/design/SKILL.md blanket gh grant"
  assert_not_contains "${skill}" "Bash(git:*)"  "skills/design/SKILL.md blanket git grant"
  assert_not_contains "${skill}" "curl" "skills/design/SKILL.md any curl reference"

  # #957: the retired `pencil`/`pencil interactive` binary invocations and
  # grants must be gone, ported onto `pen`.
  assert_no_stale_pencil_binary "${skill}" "957 skills/design/SKILL.md"

  # AC #2: design/SKILL.md must instruct reading `pencil-api` before any
  # Pencil call. Pins the actual pointer sentence's load-bearing substring,
  # not just the bare word "pencil-api".
  assert_contains "${skill}" "read the \`pencil-api\` reference skill" "957 skills/design/SKILL.md pencil-api read pointer"

  # #749: negative assertions for every grant narrowed or dropped this
  # ticket — the blanket gh issue/api user grants, echo, blanket mktemp, and
  # WebFetch must all be absent.
  assert_not_contains "${skill}" "Bash(gh issue:*)" "749 skills/design/SKILL.md blanket gh issue grant"
  assert_not_contains "${skill}" "Bash(gh api user:*)" "749 skills/design/SKILL.md blanket gh api user grant"
  assert_not_contains "${skill}" "Bash(echo:*)" "749 skills/design/SKILL.md echo grant"
  assert_not_contains "${skill}" "Bash(mktemp:*)" "749 skills/design/SKILL.md blanket mktemp grant"
  assert_not_contains "${skill}" "WebFetch" "749 skills/design/SKILL.md WebFetch grant"

  # Exhaustive set equality: parse the frontmatter's Bash(...) entries and
  # compare against the exact expected 17-entry least-privilege list (#749),
  # both sorted so the comparison is order- and locale-independent.
  EXPECTED_BASH_GRANTS='Bash(pen)
Bash(pen interactive:*)
Bash(which pen:*)
Bash(gh issue view:*)
Bash(gh issue edit:*)
Bash(gh issue comment:*)
Bash(gh issue list:*)
Bash(gh issue close:*)
Bash(gh label create:*)
Bash(gh api user --jq:*)
Bash(git remote get-url:*)
Bash(git add:*)
Bash(git commit:*)
Bash(git rev-parse:*)
Bash(mkdir:*)
Bash(mktemp -d:*)
Bash(test:*)'
  allowed_line="$(grep -m1 '^allowed-tools:' "${skill_path}")"
  actual_bash_grants="$(printf '%s\n' "${allowed_line}" | grep -o 'Bash([^)]*)' | LC_ALL=C sort -u)"
  expected_bash_grants="$(printf '%s\n' "${EXPECTED_BASH_GRANTS}" | LC_ALL=C sort -u)"
  if [[ "${actual_bash_grants}" != "${expected_bash_grants}" ]]; then
    fail "skills/design/SKILL.md allowed-tools Bash grant set mismatch:
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
      fail "skills/design/SKILL.md allowed-tools: bare, unscoped 'Bash' entry grants every shell command"
    fi
  done

  # #957: exact MCP tool-surface set equality — parse the frontmatter's
  # `mcp__pencil__*` tool entries off the `allowed-tools:` line and compare
  # against the exact expected 5-tool least-privilege surface, both sorted so
  # the comparison is order- and locale-independent.
  EXPECTED_PENCIL_MCP_TOOLS='mcp__pencil__get_app_state
mcp__pencil__execute
mcp__pencil__get_screenshot
mcp__pencil__export_nodes
mcp__pencil__get_guidelines'
  actual_pencil_mcp_tools="$(printf '%s\n' "${allowed_line}" | grep -o 'mcp__pencil__[a-z_]*' | LC_ALL=C sort -u)"
  expected_pencil_mcp_tools="$(printf '%s\n' "${EXPECTED_PENCIL_MCP_TOOLS}" | LC_ALL=C sort -u)"
  if [[ "${actual_pencil_mcp_tools}" != "${expected_pencil_mcp_tools}" ]]; then
    fail "957 skills/design/SKILL.md allowed-tools mcp__pencil__ tool-surface set mismatch:
--- expected ---
${expected_pencil_mcp_tools}
--- actual ---
${actual_pencil_mcp_tools}"
  fi

  # Compound-split: the git add/commit must be two standalone Bash calls,
  # never a `&&` compound (shell-rules: Claude Code evaluates every segment
  # of a compound, so a compound can prompt even when both halves are
  # granted).
  assert_not_contains "${skill}" "&& git commit" "skills/design/SKILL.md git add/commit compound"
  assert_contains "${skill}" 'git add <designPath>/*.pen <designPath>/DESIGN.md' "skills/design/SKILL.md standalone git add"
  assert_contains "${skill}" 'git commit -m "feat(design): ' "skills/design/SKILL.md standalone git commit"

  # Invocation-vs-grant scans (guardrails against future widening): every
  # `gh`/`git` invocation token pair actually present in the file must fall
  # under one of the granted prefixes above. #749: the gh arm is per-verb
  # (mirroring the per-verb frontmatter grants) so a newly-introduced
  # ungranted verb (e.g. `gh issue create`, `gh issue transfer`) fails here.
  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "gh issue view"*|"gh issue edit"*|"gh issue comment"*|"gh issue list"*|"gh issue close"*|"gh label create"*|"gh api user --jq"*) ;;
      *) fail "skills/design/SKILL.md: ungranted gh invocation: [${cmd}]" ;;
    esac
  done < <(grep -oE '\bgh api user --jq|\bgh [a-z]+( [a-z]+)?' "${skill_path}" | LC_ALL=C sort -u)

  while IFS= read -r cmd; do
    [[ -n "${cmd}" ]] || continue
    case "${cmd}" in
      "git add"*|"git commit"*|"git rev-parse"*|"git remote get-url"*) ;;
      *) fail "skills/design/SKILL.md: ungranted git invocation: [${cmd}]" ;;
    esac
  done < <(grep -oE '\bgit [a-z-]+( [a-z-]+){0,2}' "${skill_path}" | LC_ALL=C sort -u)

  # 2. Placement: the guard precedes both the `pen interactive -a desktop`
  #    probe and the `pen &` auto-launch in either mode, and follows the
  #    `## Phase 0.5` heading (guard lives in Phase 0.5, not Phase 0 — Phase
  #    0's config/pencil.enabled gates keep precedence).
  #
  #    The probe/launch searches are scoped to start at the `## Phase 0.5`
  #    heading (via `tail -n +${line_phase}`, per
  #    docs/shell-scripting-gotchas.md) rather than scanning the whole file.
  #    The Convention section (architecturally before Phase 0.5) legitimately
  #    mentions `pen interactive -a desktop` in prose/example as the
  #    documented fallback pattern for calls that don't show their own code
  #    block elsewhere in the skill; scanning the whole file would mistake
  #    that earlier, correctly-unguarded mention for the guarded
  #    probe/auto-launch invocation.
  line_phase="$(grep -n '^## Phase 0\.5' "${skill_path}" | head -1 | cut -d: -f1)"
  line_guard="$(grep -n 'CENCI_SANDBOX' "${skill_path}" | head -1 | cut -d: -f1)"
  if [[ -n "${line_phase}" ]]; then
    rel_pencil_probe="$(tail -n +"${line_phase}" "${skill_path}" | grep -n 'pen interactive -a desktop' | head -1 | cut -d: -f1)"
    rel_pencil_launch="$(tail -n +"${line_phase}" "${skill_path}" | grep -n 'pen &' | head -1 | cut -d: -f1)"
    if [[ -n "${rel_pencil_probe}" ]]; then
      line_pencil_probe=$(( line_phase + rel_pencil_probe - 1 ))
    else
      line_pencil_probe=""
    fi
    if [[ -n "${rel_pencil_launch}" ]]; then
      line_pencil_launch=$(( line_phase + rel_pencil_launch - 1 ))
    else
      line_pencil_launch=""
    fi
  else
    line_pencil_probe=""
    line_pencil_launch=""
  fi

  if [[ -z "${line_phase}" ]]; then
    fail "skills/design/SKILL.md placement: no '## Phase 0.5' heading found"
  elif [[ -z "${line_guard}" ]]; then
    fail "skills/design/SKILL.md placement: no CENCI_SANDBOX guard marker found"
  elif [[ -z "${line_pencil_probe}" ]]; then
    fail "skills/design/SKILL.md placement: no 'pen interactive -a desktop' probe found"
  elif [[ -z "${line_pencil_launch}" ]]; then
    fail "skills/design/SKILL.md placement: no 'pen &' auto-launch found"
  else
    if (( line_guard <= line_phase )); then
      fail "skills/design/SKILL.md placement: guard (line ${line_guard}) does not follow the '## Phase 0.5' heading (line ${line_phase})"
    fi
    if (( line_guard >= line_pencil_probe )); then
      fail "skills/design/SKILL.md placement: guard (line ${line_guard}) does not precede the 'pen interactive -a desktop' probe (line ${line_pencil_probe})"
    fi
    if (( line_guard >= line_pencil_launch )); then
      fail "skills/design/SKILL.md placement: guard (line ${line_guard}) does not precede the 'pen &' auto-launch (line ${line_pencil_launch})"
    fi
  fi

  # --- #749: body-AC pins (cheap, pin the body ACs behaviorally) -----------

  # Phase 0.5 step 1 is now a `test` check under the already-granted
  # Bash(test:*), not an echo-and-eyeball probe.
  assert_contains "${skill}" 'test "${CENCI_SANDBOX:-}" = "1"' "749 skills/design/SKILL.md Phase 0.5 test check"
  # No echo invocation remains anywhere in the skill body.
  assert_not_contains "${skill}" "echo" "749 skills/design/SKILL.md no echo reference"
  # Phase 4A creates its screenshot directory via a standalone `mktemp -d`
  # call whose literal invocation matches the granted Bash(mktemp -d:*)
  # prefix — never an assignment wrapper or $(...) command substitution.
  assert_contains "${skill}" 'mktemp -d "${TMPDIR:-/tmp}/cenci-design-' "749 skills/design/SKILL.md standalone mktemp -d invocation"
  assert_not_contains "${skill}" "SCREENSHOT_DIR=" "749 skills/design/SKILL.md no SCREENSHOT_DIR= assignment wrapper"
  assert_not_contains "${skill}" '$(mktemp' "749 skills/design/SKILL.md no \$(mktemp command substitution"
  # The stale predictable-path mkdir is gone.
  assert_not_contains "${skill}" 'mkdir -p "$TMPDIR/cenci-design/screenshots"' "749 skills/design/SKILL.md stale predictable mkdir"
  # No bare $TMPDIR (unqualified by :-) remains anywhere in the skill —
  # every reference must use the ${TMPDIR:-/tmp} fallback form.
  bare_tmpdir="$(grep -nE '\$\{?TMPDIR' "${skill_path}" | grep -v '\${TMPDIR:-' || true)"
  if [[ -n "${bare_tmpdir}" ]]; then
    fail "749 skills/design/SKILL.md: bare \$TMPDIR (unqualified by :-) found:
${bare_tmpdir}"
  fi

  # --- #957: body-AC pins ---------------------------------------------------

  # Probe-shape pin: the Phase 0.5 connectivity probe now uses the current
  # `execute` idiom, not a retired per-tool MCP call. Scoped to the Phase 0.5
  # region (from the `## Phase 0.5` heading to the next `## ` heading) so a
  # coincidental match elsewhere in the file cannot pass this check.
  phase_0_5_content="$(awk '/^## Phase 0\.5/{f=1;next} /^## /{f=0} f' "${skill_path}")"
  assert_contains "${phase_0_5_content}" "execute({ input: 'Print(1)' })" "957 skills/design/SKILL.md Phase 0.5 execute probe shape"

  # Step 7B pins: the posted design comment gains a `### Screen nodes`
  # subsection, scoped to the Step 7B comment-body section (from its `###
  # Step 7B` heading to the next `### ` heading) so a coincidental match
  # elsewhere in the file cannot pass this check. The existing
  # `<!-- cenci-design-summary -->` marker must remain present on its own
  # non-blockquoted line (never prefixed with `> `), per
  # docs/comment-attribution.md.
  step_7b_content="$(awk '/^### Step 7B/{f=1;next} /^### /{f=0} f' "${skill_path}")"
  assert_contains "${step_7b_content}" "### Screen nodes" "957 skills/design/SKILL.md Step 7B Screen nodes subsection"

  # Scoped to step_7b_content (computed above) rather than the whole file --
  # a whole-file grep's `tail -1` picks the *last* occurrence, which is Step
  # 7C's prose sentence mentioning the marker, not the actual marker inside
  # the Step 7B comment body -- making the check vacuous for its stated
  # purpose (mutation-tested: blockquoting the real Step 7B marker left a
  # whole-file `tail -1` scan green).
  marker_found=0
  while IFS= read -r marker_candidate_line; do
    marker_candidate_trimmed="${marker_candidate_line#"${marker_candidate_line%%[![:space:]]*}"}"
    if [[ "${marker_candidate_trimmed}" == '<!-- cenci-design-summary -->' ]]; then
      marker_found=1
    fi
  done <<< "${step_7b_content}"
  if [[ "${marker_found}" -eq 0 ]]; then
    fail "957 skills/design/SKILL.md: <!-- cenci-design-summary --> marker not found on its own non-blockquoted line within the Step 7B region"
  fi
  if grep -qE '^[[:space:]]*>.*cenci-design-summary' <<< "${step_7b_content}"; then
    fail "957 skills/design/SKILL.md: <!-- cenci-design-summary --> marker must not be blockquoted within the Step 7B region"
  fi

  # Step 7B: the posted-comment content (screen names) is also subject to the
  # document-derived-value validation rule (round-2 fix #8) — pin the
  # pointer sentence within the Step 7B region so it can't regress silently.
  assert_contains_ws "${step_7b_content}" "The same document-derived-value validation rule from" "957r2 skills/design/SKILL.md Step 7B validation-rule note"

  # --- #957 round 2: per-batch get_app_state re-verification pins ----------
  #
  # Round 1 added a re-ask-once-then-Stop re-verification sentence at the
  # head of Phase 4A, Phase 5 Step A, Step 7.0, and (round 2) both Step 4B
  # branches, but had no assertions pinning it -- exactly the vacuity class
  # the original blockquote-marker Must-Fix already burned this ticket on
  # once. Each region is scoped independently (never a whole-file substring
  # match) so a sentence present in only one location can't false-positive
  # the others.
  REVERIFY_PHRASE="Apply the same re-ask-once-then-Stop handling as Step 3A"

  step_4a_content="$(awk '/^### Step 4A/{f=1;next} /^### /{f=0} f' "${skill_path}")"
  assert_contains "${step_4a_content}" "${REVERIFY_PHRASE}" "957r2 skills/design/SKILL.md Step 4A re-verification"

  step_4b_content="$(awk '/^### Step 4B/{f=1;next} /^## /{f=0} f' "${skill_path}")"
  # Both "Request Changes" and "Start Over" branches must each carry their
  # own re-verification sentence -- count occurrences within the region
  # rather than a single assert_contains, since a single occurrence would
  # only prove one branch was fixed and leave the other silently uncovered.
  step_4b_reverify_count="$(grep -o -F "${REVERIFY_PHRASE}" <<< "${step_4b_content}" | wc -l | tr -d ' ')"
  if [[ "${step_4b_reverify_count}" -lt 2 ]]; then
    fail "957r2 skills/design/SKILL.md Step 4B re-verification: expected the re-verification sentence in both the Request Changes and Start Over branches, found ${step_4b_reverify_count}"
  fi

  step_5a_content="$(awk '/^### Step A: Extract data from \.pen file/{f=1;next} /^### /{f=0} f' "${skill_path}")"
  assert_contains "${step_5a_content}" "${REVERIFY_PHRASE}" "957r2 skills/design/SKILL.md Phase 5 Step A re-verification"

  step_70_content="$(awk '/^### Step 7\.0/{f=1;next} /^### /{f=0} f' "${skill_path}")"
  assert_contains "${step_70_content}" "${REVERIFY_PHRASE}" "957r2 skills/design/SKILL.md Step 7.0 re-verification"

  # --- #957 round 2: from-scratch "exactly one new file" + baseline pins ---

  step_3a_content="$(awk '/^### Step 3A/{f=1;next} /^### /{f=0} f' "${skill_path}")"
  assert_contains "${step_3a_content}" "Exactly one new file" "957r2 skills/design/SKILL.md Step 3A exactly-one-new-file branch"
  assert_contains_ws "${step_3a_content}" "run \`Glob\` (\`<designPath>/*.pen\`) now, before prompting the human to create the new document" "957r2 skills/design/SKILL.md Step 3A baseline-Glob-if-missing fallback"
fi

# --- skills/design/codex.md — the guard mirrored for Codex ------------------

require_doc codex "skills/design/codex.md" || true
if [[ -n "${codex}" ]]; then
  # 5. Same detection order, same load-bearing sentence.
  assert_contains "${codex}" "CENCI_SANDBOX" "skills/design/codex.md CENCI_SANDBOX anchor"
  assert_contains "${codex}" "/.dockerenv" "skills/design/codex.md /.dockerenv anchor"
  assert_contains_ws "${codex}" "${STOP_MESSAGE_CORE}" "skills/design/codex.md load-bearing sentence"

  # --- #749: command-surface paragraph rewrite ------------------------------
  assert_contains "${codex}" "gh api user --jq" "749 skills/design/codex.md gh api user --jq positive"
  assert_contains "${codex}" "mktemp -d" "749 skills/design/codex.md mktemp -d positive"
  assert_not_contains "${codex}" "goes through the client's own web-fetch capability" "749 skills/design/codex.md stale web-fetch clause"

  # --- #957: pencil-api pointer + stale-binary negatives --------------------

  # AC #2: design/codex.md must instruct reading `pencil-api` before any
  # Pencil call, same as the Claude procedure.
  assert_contains_ws "${codex}" "read the \`pencil-api\` reference skill before any Pencil call" "957 skills/design/codex.md pencil-api read pointer"

  # AC #5: the retired `pencil`/`pencil interactive` binary invocations and
  # grants must be gone from the Codex mirror too.
  assert_no_stale_pencil_binary "${codex}" "957 skills/design/codex.md"
fi

# --- #957: retired MCP tool names must not linger anywhere -------------------

RETIRED_PENCIL_TOOLS=(
  "get_editor_state"
  "batch_get"
  "batch_design"
  "set_variables"
  "get_variables"
  "snapshot_layout"
  "open_document"
  "find_empty_space_on_canvas"
  "replace_all_matching_properties"
  "search_all_unique_properties"
)

require_doc pencil_api_content "skills/pencil-api/SKILL.md" || true

for retired_tool in "${RETIRED_PENCIL_TOOLS[@]}"; do
  assert_not_contains "${skill}" "${retired_tool}" "957 skills/design/SKILL.md retired tool: ${retired_tool}"
  assert_not_contains "${codex}" "${retired_tool}" "957 skills/design/codex.md retired tool: ${retired_tool}"
  assert_not_contains "${pencil_api_content}" "${retired_tool}" "957 skills/pencil-api/SKILL.md retired tool: ${retired_tool}"
done

# --- #957: skills/pencil-api/SKILL.md — shared reference skill --------------

pencil_api_path="${FLOW_DIR}/skills/pencil-api/SKILL.md"
if [[ ! -f "${pencil_api_path}" ]]; then
  fail "957 skills/pencil-api/SKILL.md: file not found: ${pencil_api_path}"
else
  if ! grep -q '^user-invocable: false$' "${pencil_api_path}" 2>/dev/null; then
    fail "957 skills/pencil-api/SKILL.md: missing 'user-invocable: false' frontmatter line"
  fi

  assert_contains "${pencil_api_content}" "## MCP Surface" "957 skills/pencil-api/SKILL.md MCP surface heading"
  assert_contains "${pencil_api_content}" "## \`execute\` Idiom Catalog" "957 skills/pencil-api/SKILL.md execute idiom catalog heading"
  assert_contains "${pencil_api_content}" "## Transport Table" "957 skills/pencil-api/SKILL.md transport table heading"
  assert_contains "${pencil_api_content}" "## Document Discipline" "957 skills/pencil-api/SKILL.md document discipline heading"

  # --- #957 round 2: widened interpolation-validation allowlist pins -------
  #
  # Round 1's allowlist pattern (`^[A-Za-z0-9:._-]+$`) excluded `/`, breaking
  # every real component/screen name (e.g. `Component/ExerciseCard`,
  # `Screen/training-plan`). Pin the widened pattern literal so it can't
  # silently narrow back, plus the real call-site example it must admit, plus
  # the restructured (separate, additive) EOF reject rule.
  assert_contains "${pencil_api_content}" '`^[A-Za-z0-9:/._ -]+$`' "957r2 skills/pencil-api/SKILL.md widened allowlist pattern"
  assert_contains "${pencil_api_content}" "Component/ExerciseCard" "957r2 skills/pencil-api/SKILL.md allowlist real-name example"
  assert_contains "${pencil_api_content}" "reject a value that is exactly the literal string \`EOF\`, or" "957r2 skills/pencil-api/SKILL.md EOF standalone-line reject rule"
fi

# --- flow/README.md — design row marked host-only ----------------------------

require_doc readme "README.md" || true
if [[ -n "${readme}" ]]; then
  design_row="$(grep -F '/cenci:design' "${FLOW_DIR}/README.md" | head -1)"
  if [[ -z "${design_row}" ]]; then
    fail "README.md: no /cenci:design row found"
  else
    assert_contains "${design_row}" "host-only" "README.md design row host-only marker"
  fi
fi

# --- skills/configure/SKILL.md — generated D action + sandbox note ----------

require_doc configure "skills/configure/SKILL.md" || true
if [[ -n "${configure}" ]]; then
  # 7. Generated D action carries --no-sandbox.
  assert_contains "${configure}" 'command: "cenci run design {number} --no-sandbox"' "skills/configure/SKILL.md generated D action"
  # 7. Sandbox note's host-only clause for design itself.
  assert_contains_ws "${configure}" "design itself is host-only" "skills/configure/SKILL.md sandbox-note host-only clause"
  # The old bare command must not linger as the recommended/generated string.
  assert_not_contains "${configure}" 'command: "cenci run design {number}"' "skills/configure/SKILL.md stale bare D action"

  # 957 AC #5: the retired `pencil`/`pencil interactive` binary invocations
  # and grants must be gone from configure/SKILL.md too — it already carries
  # its own `pen interactive --help` detection probe, ported from `pencil
  # interactive --help` in the same ticket. Four of the five shared negative
  # literals apply verbatim; the fifth (a bare `` `pencil` `` single-backtick
  # literal) is skipped here on purpose — configure/SKILL.md legitimately
  # inline-codes the still-valid `pencil` *config field* name (e.g.
  # `` `pencil.enabled` ``, `` `pencil` field``) many times, which the bare
  # single-backtick check can't distinguish from a stale CLI-binary
  # reference; the four narrower literals below already catch a real stale
  # binary invocation without that false-positive risk.
  assert_not_contains "${configure}" "Bash(pencil" "957 skills/configure/SKILL.md stale pencil grant"
  assert_not_contains "${configure}" "pencil interactive" "957 skills/configure/SKILL.md stale pencil interactive invocation"
  assert_not_contains "${configure}" "which pencil" "957 skills/configure/SKILL.md stale which pencil invocation"
  assert_not_contains "${configure}" "pencil &" "957 skills/configure/SKILL.md stale pencil background launch"
fi

# --- Cross-project: sandbox/README.md ----------------------------------------

require_repo_doc sandbox_readme "sandbox/README.md" || true
if [[ -n "${sandbox_readme}" ]]; then
  # 8. /cenci:design never runs in-container, fails fast with host-session guidance.
  assert_contains "${sandbox_readme}" "/cenci:design" "sandbox/README.md design cross-reference"
  assert_contains_ws "${sandbox_readme}" "never runs in-container" "sandbox/README.md never-runs-in-container clause"
fi

# --- Cross-project: docs/orchestration.md ------------------------------------

require_repo_doc orchestration "docs/orchestration.md" || true
if [[ -n "${orchestration}" ]]; then
  # 8. design dispatches on the host.
  assert_contains_ws "${orchestration}" "design dispatches on the host" "docs/orchestration.md design-dispatches-on-host clause"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "design-sandbox-guard.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "design-sandbox-guard.test.sh: all checks passed."
