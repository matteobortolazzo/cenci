#!/usr/bin/env bash
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failures=0
fail(){ echo "FAIL: $1" >&2; failures=$((failures+1)); }
for workflow in configure refine implement review address-review refactor sync maintain; do
  skill="${ROOT}/skills/${workflow}/SKILL.md"
  procedure="${ROOT}/skills/${workflow}/codex.md"
  grep -q 'Client dispatch.*Codex' "$skill" || fail "$workflow dispatcher"
  test -s "$procedure" || fail "$workflow Codex procedure"
done
runtime="${ROOT}/skills/codex-runtime/SKILL.md"
grep -q 'Require.*`/plan`\|must begin in `/plan`' "$runtime" || fail "Plan-mode contract"
grep -q '.cenci/checkpoints' "$runtime" || fail "checkpoint contract"
grep -q 'Arm a native Codex goal only after' "$runtime" || fail "goal arm contract"
grep -q 'Clear it before human input' "$runtime" || fail "goal clear contract"
grep -q 'built-in' "$runtime" && grep -q 'same bounded role prompt' "$runtime" || fail "agent fallback contract"
count="$(find "${ROOT}/templates/codex/agent-roles" -name '*.toml' | wc -l)"
test "$count" -ge 5 || fail "Codex agent templates"

# --- Guard canary preflight contract (#1213) -----------------------------------
# Codex skips pending or untrusted plugin hooks, so every MUTATING Codex surface
# runs codex/guard-canary.sh after project-core's configured-repo check and
# before its first mutation. #1156 and later mutating companions extend this list.
MUTATING_CODEX=(implement address-review refactor)
CANARY_REF='codex/guard-canary.sh'
REPO_ROOT="$(cd "${ROOT}/.." && pwd)"

# line_of <file> <fixed string> -- first matching line number, empty when absent.
line_of(){ grep -nF -- "$2" "$1" | head -1 | cut -d: -f1; }

# assert_before <file> <label> <earlier-needle> <later-needle> -- both present, earlier first.
assert_before(){
  local file="$1" label="$2" a b
  a="$(line_of "$file" "$3")"; b="$(line_of "$file" "$4")"
  if [[ -z "$a" ]]; then fail "$label: missing anchor '$3'"; return; fi
  if [[ -z "$b" ]]; then fail "$label: missing anchor '$4'"; return; fi
  [[ "$a" -lt "$b" ]] || fail "$label: '$3' (line $a) must precede '$4' (line $b)"
}

for workflow in "${MUTATING_CODEX[@]}"; do
  procedure="${ROOT}/skills/${workflow}/codex.md"
  grep -qF -- "$CANARY_REF" "$procedure" || fail "$workflow codex.md must run the guard canary preflight ($CANARY_REF)"
  assert_before "$procedure" "$workflow canary after configured-repo check" 'Read `project-core`' "$CANARY_REF"
  # The Claude Code SKILL.md is excluded: Claude Code has no pending-trust state for plugin hooks.
  if grep -qF -- 'guard-canary' "${ROOT}/skills/${workflow}/SKILL.md"; then
    fail "$workflow Claude SKILL.md must not reference the guard canary (no pending-trust state on Claude Code)"
  fi
done

# implement: after the blocked-dependency re-check and the apply-time plan-check read,
# before the first mutation (new-plan reset/prepare, plan persist, any transition,
# checkpoint, worktree or label write).
impl="${ROOT}/skills/implement/codex.md"
assert_before "$impl" "implement canary after blocked-dependency re-check" '`apply` runs the identical check again' "$CANARY_REF"
assert_before "$impl" "implement canary after apply-time plan-check read" 'never make a second `plan-check` call' "$CANARY_REF"
assert_before "$impl" "implement canary before new-plan reset/prepare" "$CANARY_REF" 'A new-plan apply (decision `none` or `replan`)'
assert_before "$impl" "implement canary before plan persist" "$CANARY_REF" 'In normal apply mode persist'
assert_before "$impl" "implement canary before the pipeline lifecycle" "$CANARY_REF" '**Pipeline lifecycle (ticket mode).**'
assert_before "$impl" "implement canary before checkpoint init" "$CANARY_REF" 'checkpoint.mjs init'
grep -q 'refus' <(sed -n "$(line_of "$impl" "$CANARY_REF")p" "$impl") || fail "implement canary step must state the refusal outcome"

# address-review: first thing in the second (normal-mode) invocation.
assert_before "${ROOT}/skills/address-review/codex.md" "address-review canary before fixes" "$CANARY_REF" 'apply approved fixes'
# refactor: normal mode, before any ticket is created.
assert_before "${ROOT}/skills/refactor/codex.md" "refactor canary before ticket creation" "$CANARY_REF" 'create GitHub tickets'

# babysit: portable skill, so the Codex-only step is self-contained (no codex-runtime
# consult: adapter-drift) and sits before the --agent codex arm.
babysit="${ROOT}/skills/babysit/SKILL.md"
grep -qF -- 'codex/guard-canary.sh" plan' "$babysit" || fail "babysit Codex-only step must run guard-canary.sh plan"
grep -qF -- 'codex/guard-canary.sh" check' "$babysit" || fail "babysit Codex-only step must run guard-canary.sh check"
assert_before "$babysit" "babysit canary before arming" "$CANARY_REF" 'cenci babysit <pr> --agent codex'
grep -qi 'codex-only\|only under codex\|under codex only' "$babysit" || fail "babysit canary step must be labelled Codex-only"
grep -qi 'do not arm\|never arm\|not arm' "$babysit" || fail "babysit canary step must not arm on refusal"
grep -q 'codex-runtime' "$babysit" && fail "babysit must not consult codex-runtime (portable skill, adapter-drift)"

# codex-runtime: the canonical rule.
grep -qF -- 'Guard canary preflight' "$runtime" || fail "codex-runtime guard canary bullet"
grep -qF -- 'guard-canary.sh' "$runtime" || fail "codex-runtime must name the helper"
grep -qF -- '/hooks' "$runtime" || fail "codex-runtime must point at /hooks"
grep -qi 'pending' "$runtime" || fail "codex-runtime must name pending trust as a cause"
grep -qi 'leftover' "$runtime" || fail "codex-runtime must carry the leftover rule"
grep -qF -- 'hook trust bypassed by launcher (explicit flag)' "$runtime" || fail "codex-runtime sandbox label"
grep -qi 'no pending-trust state' "$runtime" || fail "codex-runtime must record why Claude Code is excluded"
grep -qi 'never changes' "$runtime" || fail "codex-runtime must say cenci never changes trust"

# Docs describe the verified behavior.
codex_doc="${ROOT}/docs/codex.md"
grep -qF -- 'Guard canary preflight' "$codex_doc" || fail "docs/codex.md Guard canary preflight section"
grep -qF -- 'guard-canary.sh' "$codex_doc" || fail "docs/codex.md must name the helper"
grep -qF -- 'hook trust bypassed by launcher (explicit flag)' "$codex_doc" || fail "docs/codex.md sandbox label"
grep -qF -- 'Codex hook-activation precondition' "${ROOT}/docs/adapter-contract.md" || fail "adapter-contract hook-activation precondition"
grep -qF -- 'Guard canary preflight' "${ROOT}/docs/codex-native-evidence.md" || fail "codex-native-evidence guard canary section"
grep -qF -- 'hook trust bypassed by launcher (explicit flag)' "${REPO_ROOT}/docs/getting-started.md" || fail "getting-started sandbox label"
grep -qF -- 'guard canary' "${ROOT}/AGENTS.md" || fail "flow/AGENTS.md evidence reference mentions the guard canary"

echo "workflows.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
