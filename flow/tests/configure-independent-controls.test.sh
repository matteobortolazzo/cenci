#!/usr/bin/env bash
# Execute the shipped delta resolver and marker-bounded configure overlays.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW="$(cd "${HERE}/.." && pwd)" || exit 2
SKILL="${FLOW}/skills/configure/SKILL.md"
CODEX="${FLOW}/skills/configure/codex.md"
RESOLVER="${FLOW}/skills/configure/scripts/resolve-delta.sh"
SCRATCH="$(mktemp -d)" || exit 2
trap 'rm -rf "$SCRATCH"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
extract() {
  awk -v start="<!-- cenci:configure-$1:begin -->" -v end="<!-- cenci:configure-$1:end -->" '
    $0 == start { inside=1; next } $0 == end { inside=0; found=1; exit }
    inside && $0 !~ /^```/ { print }
    END { if (!found) exit 1 }
  ' "$SKILL"
}
assert_json() { jq -e "$2" <<<"$1" >/dev/null || fail "$3"; }
legacy='{"refinement":{"autonomy":"lean","note":"keep"},"planning":{"autonomy":"interactive"},"automerge":{"maxChangedFiles":10,"maxDiffLines":400},"projects":[{"path":"api","automerge":{"maxChangedFiles":5,"maxDiffLines":200}}],"configureOffered":["refinement","planning","automerge"]}'
printf '%s\n' "$legacy" > "${SCRATCH}/config.json"
delta="$(bash "$RESOLVER" "${SCRATCH}/config.json")"
assert_json "$delta" '.ask | index("refinement.confirmBeforeApply") != null and index("refinement.automerge") != null' 'legacy parent keys/ledger must not suppress new leaf questions'
confirm="$(extract confirm-before-apply)" || fail 'missing confirmation overlay'
grant="$(extract refinement-automerge)" || fail 'missing automerge overlay'
autonomy="$(extract q15-switch)" || fail 'missing autonomy overlay'
out="$(jq -c --arg autonomy interactive "$autonomy" <<<"$legacy")"
out="$(jq -c --argjson confirmBeforeApply false "$confirm" <<<"$out")"
out="$(jq -c --arg automerge eligible "$grant" <<<"$out")"
assert_json "$out" '.refinement.autonomy == "interactive" and .refinement.confirmBeforeApply == false and .refinement.automerge == "eligible"' 'all three refinement answers compose independently'
out="$(jq -c --argjson confirmBeforeApply false "$confirm" <<<"$legacy")"
out="$(jq -c --arg automerge eligible "$grant" <<<"$out")"
assert_json "$out" '.refinement == {autonomy:"lean",note:"keep",confirmBeforeApply:false,automerge:"eligible"}' 'independent leaves preserve siblings and explicit false'
assert_json "$out" '.planning.autonomy == "interactive" and .automerge.maxChangedFiles == 10' 'merge grant never changes planning or risk policy'
printf '%s\n' "$out" > "${SCRATCH}/config.json"
delta="$(bash "$RESOLVER" "${SCRATCH}/config.json")"
assert_json "$delta" '.carried | index("refinement.confirmBeforeApply") != null and index("refinement.automerge") != null' 'explicit false and eligible count as answered'
out="$(jq -c --arg automerge off "$grant" <<<"$out")"
assert_json "$out" '.refinement.automerge == "off" and .refinement.confirmBeforeApply == false and .automerge.maxDiffLines == 400 and .projects[0].automerge == {maxChangedFiles:5,maxDiffLines:200}' 'disabling grants preserves confirmation and unrelated top-level/per-project risk policies'
out="$(jq -c --argjson confirmBeforeApply true "$confirm" <<<"$out")"
assert_json "$out" '.refinement.confirmBeforeApply == true and .refinement.automerge == "off"' 'opt-in confirmation writes true independently'
ledger="$(extract offered-ledger)" || fail 'missing ledger overlay'
out="$(jq -c --argjson offered '["refinement.confirmBeforeApply","refinement.automerge","dispatch.planRefined","automerge.enabled","planning.attended"]' "$ledger" <<<"$out")"
printf '%s\n' "$out" > "${SCRATCH}/config.json"
delta="$(bash "$RESOLVER" "${SCRATCH}/config.json")"
assert_json "$delta" '.ask | all(. != "dispatch.planRefined" and . != "automerge.enabled" and . != "planning.attended")' 'successful fleet choices are tracked separately'
delta="$(bash "$RESOLVER" "${SCRATCH}/config.json" --all)"
assert_json "$delta" '.ask | index("refinement.confirmBeforeApply") != null and index("refinement.automerge") != null and index("dispatch.planRefined") != null and index("automerge.enabled") != null and index("planning.attended") != null' '--all reopens every independent control'
for doc in "$SKILL" "$CODEX"; do
  for required in 'refinement.confirmBeforeApply' 'refinement.automerge' 'dispatch.planRefined' 'automerge.enabled' 'planning.attended' 'cenci dispatch plan-refined' 'cenci automerge' 'cenci planning attended' 'confirmBeforeApplyAnswer' 'refinementAutomergeAnswer'; do
    rg -Fq "$required" "$doc" || fail "$doc must wire $required"
  done
  rg -Fq 'never claim deferred fleet choices were applied' "$doc" || fail "$doc must report sandbox deferral honestly"
  rg -Fq 'may already have persisted' "$doc" || fail "$doc must report potentially partial fleet changes after a CLI error"
done
order='then the question 13/14/15 overlays (`cenci:configure-q13-switch`, `cenci:configure-q14-remove`, `cenci:configure-q15-switch`), then the independent leaf overlays (`cenci:configure-confirm-before-apply`, `cenci:configure-refinement-automerge`), then the `configureOffered` ledger (`cenci:configure-offered-ledger`)'
rg -Fq "$order" "$SKILL" || fail 'canonical write order must apply leaf overlays before ledger'
rg -Fq 'when this leaf is absent and, on a delta run, its ID is in `delta.ask`' "$SKILL" || fail 'first and full runs must offer absent leaves without undefined delta'
for grant in 'Bash(cenci dispatch plan-refined on)' 'Bash(cenci dispatch plan-refined off)' 'Bash(cenci dispatch plan-refined status)' 'Bash(cenci automerge on)' 'Bash(cenci automerge off)' 'Bash(cenci automerge status)' 'Bash(cenci planning attended on)' 'Bash(cenci planning attended off)' 'Bash(cenci planning attended status --json)'; do
  rg -Fq "$grant" "$SKILL" || fail "host CLI commands need their scoped grant: $grant"
done
echo 'configure-independent-controls.test.sh: all checks passed.'
