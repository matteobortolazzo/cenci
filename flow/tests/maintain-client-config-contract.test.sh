#!/usr/bin/env bash
# Contract coverage for ticket #666's maintain client/configuration semantics.
# The procedures are Markdown, so this suite checks the real committed adapter
# documents at their behavioral control points and verifies ordering where the
# safety contract depends on sequencing.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "maintain-client-config-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "maintain-client-config-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "maintain-client-config-contract.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <abs-path> — pure extraction, no fail() side effect here: it
  # is deliberately safe to call inside a $(...) command substitution.
  local _path="$1"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <abs-path> — nameref wrapper around read_doc_raw
# that assigns the file's content into <result-var>, or fails closed with a
# distinct "not found" message and assigns "" if not found. Must NOT be
# invoked via $(...).
require_doc() {
  local -n _result="$1"
  local _path="$2"
  local _content
  if ! _content="$(read_doc_raw "${_path}")"; then
    fail "doc not found/unreadable: ${_path}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

assert_contains() {
  local content="$1" phrase="$2" label="$3"
  [[ -n "${phrase}" ]] || { fail "${label}: empty required phrase (test bug)"; return; }
  [[ "${content}" == *"${phrase}"* ]] || fail "${label}: required text missing: [${phrase}]"
}

assert_not_contains() {
  local content="$1" phrase="$2" label="$3"
  [[ -n "${phrase}" ]] || { fail "${label}: empty forbidden phrase (test bug)"; return; }
  [[ "${content}" != *"${phrase}"* ]] || fail "${label}: stale/forbidden text remains: [${phrase}]"
}

assert_before() {
  local content="$1" first="$2" second="$3" label="$4"
  local before_first before_second
  [[ -n "${first}" && -n "${second}" ]] || { fail "${label}: empty ordering marker (test bug)"; return; }
  [[ "${content}" == *"${first}"* ]] || { fail "${label}: first marker missing: [${first}]"; return; }
  [[ "${content}" == *"${second}"* ]] || { fail "${label}: second marker missing: [${second}]"; return; }
  before_first="${content%%"${first}"*}"
  before_second="${content%%"${second}"*}"
  [[ "${#before_first}" -lt "${#before_second}" ]] || fail "${label}: markers are out of order"
}

require_doc codex "${FLOW_DIR}/skills/maintain/codex.md" || true
require_doc maintain_skill "${FLOW_DIR}/skills/maintain/SKILL.md" || true
require_doc phase8 "${FLOW_DIR}/skills/implement/phases/phase-8-docs.md" || true
require_doc implement_codex "${FLOW_DIR}/skills/implement/codex.md" || true
require_doc configure "${FLOW_DIR}/skills/configure/SKILL.md" || true
require_doc readme "${FLOW_DIR}/README.md" || true
require_doc getting_started "${REPO_ROOT}/docs/getting-started.md" || true
require_doc gotchas "${FLOW_DIR}/docs/shell-scripting-gotchas.md" || true

# Codex maintain is a complete six-phase native workflow.
for phase in \
  "## Phase 1 — Scope" \
  "## Phase 2 — Deterministic check" \
  "## Phase 3 — Worker audit" \
  "## Phase 4 — Report" \
  "## Phase 5 — Approval" \
  "## Phase 6 — Apply"; do
  assert_contains "${codex}" "${phase}" "Codex maintain phase contract"
done

assert_contains "${codex}" 'Mode `structure` launches only `structure-maintainer`.' "Codex structure worker dispatch"
assert_contains "${codex}" 'Mode `docs` launches only `docs-maintainer`.' "Codex docs worker dispatch"
assert_contains "${codex}" 'Mode `clients` launches only `portability-maintainer`.' "Codex clients worker dispatch"
assert_contains "${codex}" 'Mode `rules` launches only `rules-maintainer`.' "Codex rules worker dispatch"
assert_contains "${codex}" 'Mode `all` launches all four workers in parallel.' "Codex all-mode worker dispatch"
assert_contains "${codex}" 'Mode `backlog` launches only `backlog-maintainer`;' "Codex backlog worker dispatch"
assert_contains "${codex}" 'it is excluded from `all` and must be requested explicitly.' "Codex backlog excluded from all"
assert_contains "${codex}" 'Mode `backlog` uses a different apply path:' "Codex backlog GitHub-issue apply-path redirect"
assert_contains "${codex}" 'If mode is `structure`, `docs`, `clients`, or `all` and scope is `watch` or `sandbox`, report `not yet covered` and stop before Phase 2.' "Codex scope no-op"
assert_contains "${codex}" 'Mode `rules` continues normally for scope `watch` or `sandbox` because the `context-budget` check in `check.sh` and `rules-maintainer` are both project-generic.' "Codex rules mode exempt from the watch/sandbox no-op (#753)"
assert_contains "${codex}" 'Treat an errored, timed-out, or malformed worker result as an incomplete run' "Codex worker failure remains incomplete"

for option in \
  "all deterministic repairs" \
  "critical+high findings" \
  "docs+indexes only" \
  "rules only" \
  "let me select findings" \
  "report only"; do
  assert_contains "${codex}" "${option}" "Codex approval option ${option}"
done
assert_contains "${codex}" 'Report only is terminal: do not create a worktree, mutate files or GitHub, commit, push, or open a PR.' "Codex report-only terminal behavior"

assert_contains "${codex}" 'Run `bash flow/skills/maintain/scripts/check.sh --advisory` with the shell tool working directory set to the verified absolute `<repo-root>`.' "Codex advisory absolute CWD"
assert_contains "${codex}" 'Every checker invocation in this phase uses the shell tool working directory set to the verified absolute `<worktree-path>`.' "Codex worktree checker CWD"
assert_before "${codex}" 'check.sh --advisory' '## Phase 5 — Approval' "Codex advisory check precedes approval"
assert_before "${codex}" '## Phase 5 — Approval' 'Run the executable/default checker' "Codex executable check follows approval"
assert_before "${codex}" 'Create the dedicated worktree' 'Run the executable/default checker' "Codex executable check follows worktree creation"

# The shared Claude procedure must honor the same advisory/read-only split.
assert_contains "${maintain_skill}" 'check.sh --advisory' "Claude maintain uses advisory before approval"
assert_contains "${maintain_skill}" 'verified absolute `<repo-root>` working directory' "Claude advisory checker absolute CWD"
assert_contains "${maintain_skill}" 'verified absolute `<worktree-path>` working directory' "Claude apply checker absolute CWD"
assert_before "${maintain_skill}" 'check.sh --advisory' '## Phase 5 — Approval' "Claude advisory check precedes approval"
assert_before "${maintain_skill}" '## Phase 5 — Approval' 'Run the executable/default checker' "Claude executable check follows approval"

# backlog mode parity: both clients name backlog's sole analyzer, exclude it
# from `all`, and route it to the GitHub-issue apply path (no worktree/PR).
assert_contains "${maintain_skill}" 'launch only `backlog-maintainer`' "Claude backlog mode dispatch"
assert_contains "${maintain_skill}" 'is excluded from `all` and must be' "Claude backlog excluded from all"
assert_contains "${maintain_skill}" '`backlog` mode uses a different apply path.' "Claude backlog GitHub-issue apply-path redirect"

# Phase 8 always checks correctness; configuration only controls repair/UX.
assert_contains "${phase8}" 'Core correctness checks always run in Phase 8.' "Phase 8 core checks are unconditional"
assert_contains "${phase8}" '`maintenance.enabled` does not gate this sub-step' "Phase 8 ignores optional UX master switch"
assert_contains "${phase8}" '`maintenance.checkDuringImplement` is explicitly `false`' "Phase 8 report-only config branch"
assert_contains "${phase8}" 'all non-pass results are report-only' "Phase 8 false means report-only"
assert_contains "${phase8}" '`maintenance.generatedDocs` is explicitly `false`' "Phase 8 generated-doc config branch"
assert_contains "${phase8}" 'do not run `check.sh --write`' "Phase 8 generated-doc repair disabled"
assert_contains "${phase8}" 'shell tool working directory to the verified absolute `<worktree-path>` on every checker invocation' "Phase 8 checker absolute CWD"
assert_contains "${phase8}" 'checking that the file read itself succeeds' "Phase 8 validates shared changed-file acquisition"
assert_contains "${phase8}" 'checking the `git diff` exit status before reading it' "Phase 8 validates fallback changed-file discovery"
assert_contains "${phase8}" 'Never use process substitution for changed-file discovery' "Phase 8 forbids hidden changed-file failures"
assert_contains "${phase8}" 'maintenance: error (changed-file discovery failed)' "Phase 8 records changed-file discovery failure"
assert_contains "${implement_codex}" 'shell tool working directory set to the verified absolute worktree path' "Codex implement checker absolute CWD"

# Configure and user docs expose the live semantics, not the old reserved contract.
assert_contains "${configure}" '`maintenance.enabled` — gates optional scheduled-maintenance and reminder UX only' "Configure enabled semantics"
assert_contains "${configure}" '`maintenance.checkDuringImplement` — whether Phase 8 may repair findings' "Configure checkDuringImplement semantics"
assert_contains "${configure}" '`maintenance.generatedDocs` — whether marker-bounded generated documentation sections are checked and regenerated' "Configure generatedDocs semantics"
assert_contains "${configure}" 'Core correctness checks always run' "Configure core checks unconditional"
assert_not_contains "${configure}" 'master switch for maintenance checks' "Configure removes stale master-switch claim"
assert_not_contains "${configure}" 'advisory/reserved this release' "Configure removes stale reserved claim"

assert_contains "${readme}" '| `maintain` | Yes | Yes | No |' "README marks Codex maintain supported"
assert_contains "${readme}" '`checkDuringImplement: false` keeps the Phase 8 check but makes every repair report-only' "README report-only semantics"
assert_contains "${readme}" '`generatedDocs` controls marker-bounded generated-section maintenance' "README generatedDocs semantics"
assert_not_contains "${readme}" 'those fields are advisory this release' "README removes stale reserved claim"

assert_contains "${getting_started}" '`checkDuringImplement: false` keeps the check report-only' "Getting started report-only semantics"
assert_contains "${getting_started}" '`maintenance.enabled` controls only scheduled/reminder UX' "Getting started enabled semantics"
assert_contains "${gotchas}" 'set the shell tool working directory to the verified absolute repository or worktree path on every invocation' "Checker gotcha requires explicit absolute CWD"
assert_not_contains "${gotchas}" 'cd <repo-root> && bash flow/skills/maintain/scripts/check.sh' "Checker gotcha removes relative compound alternative"

echo "maintain-client-config-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
