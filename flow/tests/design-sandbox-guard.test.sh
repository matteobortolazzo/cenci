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
#   - sandbox/README.md (cross-project)
#   - docs/orchestration.md (cross-project)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "design-sandbox-guard.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "design-sandbox-guard.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "design-sandbox-guard.test.sh: failed to resolve repo root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc() {
  # read_doc <flow-relative-path> — prints the real committed file's content,
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

read_repo_doc() {
  # read_repo_doc <repo-relative-path> — same fail-closed contract as
  # read_doc, but resolved from REPO_ROOT for the two cross-project docs
  # (sandbox/README.md, docs/orchestration.md) that live outside flow/**.
  local path="${REPO_ROOT}/$1"
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

# The ticket's exact four-sentence stop message (verbatim, do not paraphrase).
STOP_MESSAGE='`/cenci:design` must run from a host session: the Pencil desktop app is not reachable from inside the cenci sandbox. Exit the container and re-run `/cenci:design <args>` on the host. Sandboxed sessions get design access through headless reads only (`/cenci:implement`, `verify-ui`).'

# The reason-establishing clause shared verbatim between SKILL.md and the
# Codex mirror (the "same load-bearing sentence" the plan calls for).
STOP_MESSAGE_CORE='the Pencil desktop app is not reachable from inside the cenci sandbox'

# --- skills/design/SKILL.md — the host-only guard ---------------------------

skill="$(read_doc "skills/design/SKILL.md")" || true
if [[ -n "${skill}" ]]; then
  # 1. Exact stop-message sentence, whitespace-normalized (markdown wraps lines).
  assert_contains_ws "${skill}" "${STOP_MESSAGE}" "skills/design/SKILL.md stop message"

  # 3. Both detection anchors present (mirrors detect-project.sh:74-79).
  assert_contains "${skill}" "CENCI_SANDBOX" "skills/design/SKILL.md CENCI_SANDBOX anchor"
  assert_contains "${skill}" "/.dockerenv" "skills/design/SKILL.md /.dockerenv anchor"

  # 4. Frontmatter grants the two Bash primitives the guard's host-path probe
  #    needs (Bash(echo:*) for the CENCI_SANDBOX check, Bash(test:*) for the
  #    /.dockerenv check) — a future edit stripping them would silently make
  #    the guard prompt-blocked on the host.
  assert_contains "${skill}" "Bash(echo:*)" "skills/design/SKILL.md allowed-tools echo grant"
  assert_contains "${skill}" "Bash(test:*)" "skills/design/SKILL.md allowed-tools test grant"

  # 2. Placement: the guard precedes both the `pencil interactive -a desktop`
  #    probe and the `pencil &` auto-launch in either mode, and follows the
  #    `## Phase 0.5` heading (guard lives in Phase 0.5, not Phase 0 — Phase
  #    0's config/pencil.enabled gates keep precedence).
  #
  #    The probe/launch searches are scoped to start at the `## Phase 0.5`
  #    heading (via `tail -n +${line_phase}`, per
  #    docs/shell-scripting-gotchas.md) rather than scanning the whole file.
  #    The Convention section (architecturally before Phase 0.5) legitimately
  #    mentions `pencil interactive -a desktop` in prose/example as the
  #    documented fallback pattern for calls that don't show their own code
  #    block elsewhere in the skill; scanning the whole file would mistake
  #    that earlier, correctly-unguarded mention for the guarded
  #    probe/auto-launch invocation.
  skill_path="${FLOW_DIR}/skills/design/SKILL.md"
  line_phase="$(grep -n '^## Phase 0\.5' "${skill_path}" | head -1 | cut -d: -f1)"
  line_guard="$(grep -n 'CENCI_SANDBOX' "${skill_path}" | head -1 | cut -d: -f1)"
  if [[ -n "${line_phase}" ]]; then
    rel_pencil_probe="$(tail -n +"${line_phase}" "${skill_path}" | grep -n 'pencil interactive -a desktop' | head -1 | cut -d: -f1)"
    rel_pencil_launch="$(tail -n +"${line_phase}" "${skill_path}" | grep -n 'pencil &' | head -1 | cut -d: -f1)"
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
    fail "skills/design/SKILL.md placement: no 'pencil interactive -a desktop' probe found"
  elif [[ -z "${line_pencil_launch}" ]]; then
    fail "skills/design/SKILL.md placement: no 'pencil &' auto-launch found"
  else
    if (( line_guard <= line_phase )); then
      fail "skills/design/SKILL.md placement: guard (line ${line_guard}) does not follow the '## Phase 0.5' heading (line ${line_phase})"
    fi
    if (( line_guard >= line_pencil_probe )); then
      fail "skills/design/SKILL.md placement: guard (line ${line_guard}) does not precede the 'pencil interactive -a desktop' probe (line ${line_pencil_probe})"
    fi
    if (( line_guard >= line_pencil_launch )); then
      fail "skills/design/SKILL.md placement: guard (line ${line_guard}) does not precede the 'pencil &' auto-launch (line ${line_pencil_launch})"
    fi
  fi
fi

# --- skills/design/codex.md — the guard mirrored for Codex ------------------

codex="$(read_doc "skills/design/codex.md")" || true
if [[ -n "${codex}" ]]; then
  # 5. Same detection order, same load-bearing sentence.
  assert_contains "${codex}" "CENCI_SANDBOX" "skills/design/codex.md CENCI_SANDBOX anchor"
  assert_contains "${codex}" "/.dockerenv" "skills/design/codex.md /.dockerenv anchor"
  assert_contains_ws "${codex}" "${STOP_MESSAGE_CORE}" "skills/design/codex.md load-bearing sentence"
fi

# --- flow/README.md — design row marked host-only ----------------------------

readme="$(read_doc "README.md")" || true
if [[ -n "${readme}" ]]; then
  design_row="$(grep -F '/cenci:design' "${FLOW_DIR}/README.md" | head -1)"
  if [[ -z "${design_row}" ]]; then
    fail "README.md: no /cenci:design row found"
  else
    assert_contains "${design_row}" "host-only" "README.md design row host-only marker"
  fi
fi

# --- skills/configure/SKILL.md — generated D action + sandbox note ----------

configure="$(read_doc "skills/configure/SKILL.md")" || true
if [[ -n "${configure}" ]]; then
  # 7. Generated D action carries --no-sandbox.
  assert_contains "${configure}" 'command: "cenci run design {number} --no-sandbox"' "skills/configure/SKILL.md generated D action"
  # 7. Sandbox note's host-only clause for design itself.
  assert_contains_ws "${configure}" "design itself is host-only" "skills/configure/SKILL.md sandbox-note host-only clause"
  # The old bare command must not linger as the recommended/generated string.
  assert_not_contains "${configure}" 'command: "cenci run design {number}"' "skills/configure/SKILL.md stale bare D action"
fi

# --- Cross-project: sandbox/README.md ----------------------------------------

sandbox_readme="$(read_repo_doc "sandbox/README.md")" || true
if [[ -n "${sandbox_readme}" ]]; then
  # 8. /cenci:design never runs in-container, fails fast with host-session guidance.
  assert_contains "${sandbox_readme}" "/cenci:design" "sandbox/README.md design cross-reference"
  assert_contains_ws "${sandbox_readme}" "never runs in-container" "sandbox/README.md never-runs-in-container clause"
fi

# --- Cross-project: docs/orchestration.md ------------------------------------

orchestration="$(read_repo_doc "docs/orchestration.md")" || true
if [[ -n "${orchestration}" ]]; then
  # 8. design dispatches on the host.
  assert_contains_ws "${orchestration}" "design dispatches on the host" "docs/orchestration.md design-dispatches-on-host clause"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "design-sandbox-guard.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "design-sandbox-guard.test.sh: all checks passed."
