#!/usr/bin/env bash
# Execute the shipped delta resolver and marker-bounded configure overlays.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW="$(cd "${HERE}/.." && pwd)" || exit 2
SKILL="${FLOW}/skills/configure/SKILL.md"
FLEET_PHASE="${FLOW}/skills/configure/phases/fleet-enrollment.md"
AUTONOMY_PHASE="${FLOW}/skills/configure/phases/autonomy-settings.md"
CODEX="${FLOW}/skills/configure/codex.md"
RESOLVER="${FLOW}/skills/configure/scripts/resolve-delta.sh"
SCRATCH="$(mktemp -d)" || exit 2
trap 'rm -rf "$SCRATCH"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
# extract <file> <marker-name> -- the marker-bounded program from <file>.
extract() {
  awk -v start="<!-- cenci:configure-$2:begin -->" -v end="<!-- cenci:configure-$2:end -->" '
    $0 == start { inside=1; next } $0 == end { inside=0; found=1; exit }
    inside && $0 !~ /^```/ { print }
    END { if (!found) exit 1 }
  ' "$1"
}
# The controls are wired across the router and the two conditionally read
# phase files (#1342); every file must exist and be non-empty so a missing one
# cannot satisfy a grep vacuously.
for file in "$SKILL" "$FLEET_PHASE" "$AUTONOMY_PHASE"; do
  [[ -s "$file" ]] || fail "$file is missing or empty"
done
# step6_region <file> -- the router's "Independent refinement leaves wiring"
# paragraph, bounded by the next bold lead-in; it is the only router text that
# wires the leaf answers into the step 6 write.
step6_region() {
  awk '
    index($0, "**Independent refinement leaves wiring.**") { inside=1 }
    inside && index($0, "**Overlay order and validation.**") { found=1; exit }
    inside { print }
    END { if (!found) exit 1 }
  ' "$1"
}
STEP6="${SCRATCH}/step6.md"
step6_region "$SKILL" > "$STEP6" || fail 'SKILL.md step 6 "Independent refinement leaves wiring" paragraph is missing'
[[ -s "$STEP6" ]] || fail 'SKILL.md step 6 "Independent refinement leaves wiring" paragraph is empty'
assert_json() { jq -e "$2" <<<"$1" >/dev/null || fail "$3"; }
legacy='{"refinement":{"autonomy":"lean","note":"keep"},"planning":{"autonomy":"interactive"},"automerge":{"maxChangedFiles":10,"maxDiffLines":400},"projects":[{"path":"api","automerge":{"maxChangedFiles":5,"maxDiffLines":200}}],"configureOffered":["refinement","planning","automerge"]}'
printf '%s\n' "$legacy" > "${SCRATCH}/config.json"
delta="$(bash "$RESOLVER" "${SCRATCH}/config.json")"
assert_json "$delta" '.ask | index("refinement.confirmBeforeApply") != null and index("refinement.automerge") != null' 'legacy parent keys/ledger must not suppress new leaf questions'
confirm="$(extract "$AUTONOMY_PHASE" confirm-before-apply)" || fail 'missing confirmation overlay'
grant="$(extract "$AUTONOMY_PHASE" refinement-automerge)" || fail 'missing automerge overlay'
autonomy="$(extract "$AUTONOMY_PHASE" q15-switch)" || fail 'missing autonomy overlay'
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
ledger="$(extract "$SKILL" offered-ledger)" || fail 'missing ledger overlay'
out="$(jq -c --argjson offered '["refinement.confirmBeforeApply","refinement.automerge","dispatch.planRefined","automerge.enabled","planning.attended"]' "$ledger" <<<"$out")"
printf '%s\n' "$out" > "${SCRATCH}/config.json"
delta="$(bash "$RESOLVER" "${SCRATCH}/config.json")"
assert_json "$delta" '.ask | all(. != "dispatch.planRefined" and . != "automerge.enabled" and . != "planning.attended")' 'successful fleet choices are tracked separately'
delta="$(bash "$RESOLVER" "${SCRATCH}/config.json" --all)"
assert_json "$delta" '.ask | index("refinement.confirmBeforeApply") != null and index("refinement.automerge") != null and index("dispatch.planRefined") != null and index("automerge.enabled") != null and index("planning.attended") != null' '--all reopens every independent control'
for required in 'confirmBeforeApplyAnswer' 'refinementAutomergeAnswer' 'cenci:configure-confirm-before-apply' 'cenci:configure-refinement-automerge' '--argjson confirmBeforeApply' '--arg automerge' 'dispatch.planRefined' 'automerge.enabled' 'planning.attended'; do
  grep -Fq -- "$required" "$STEP6" || fail "SKILL.md step 6 'Independent refinement leaves wiring' must wire $required"
done
for required in 'confirmBeforeApplyAnswer' 'refinementAutomergeAnswer'; do
  grep -Fq -- "$required" "$AUTONOMY_PHASE" || fail "$AUTONOMY_PHASE must store $required"
done
for required in 'dispatch.planRefined' 'automerge.enabled' 'planning.attended' 'cenci dispatch plan-refined' 'cenci automerge' 'cenci planning attended'; do
  grep -Fq -- "$required" "$FLEET_PHASE" || fail "$FLEET_PHASE must wire $required"
done
for doc in "$FLEET_PHASE" "$CODEX"; do
  grep -Fq 'never claim deferred fleet choices were applied' "$doc" || fail "$doc must report sandbox deferral honestly"
  grep -Fq 'may already have persisted' "$doc" || fail "$doc must report potentially partial fleet changes after a CLI error"
done
for required in 'refinement.confirmBeforeApply' 'refinement.automerge' 'dispatch.planRefined' 'automerge.enabled' 'planning.attended' 'cenci dispatch plan-refined' 'cenci automerge' 'cenci planning attended' 'confirmBeforeApplyAnswer' 'refinementAutomergeAnswer'; do
  grep -Fq -- "$required" "$CODEX" || fail "$CODEX must wire $required"
done
for required in 'refinement.confirmBeforeApply' 'refinement.automerge'; do
  grep -Fq -- "$required" "$AUTONOMY_PHASE" || fail "$AUTONOMY_PHASE must name $required"
done
order='then the question 13/14/15 overlays (`cenci:configure-q13-switch`, `cenci:configure-q14-remove`, `cenci:configure-q15-switch`), then the independent leaf overlays (`cenci:configure-confirm-before-apply`, `cenci:configure-refinement-automerge`), then the `configureOffered` ledger (`cenci:configure-offered-ledger`)'
grep -Fq "$order" "$SKILL" || fail 'canonical write order must apply leaf overlays before ledger'
grep -Fq 'when this leaf is absent and, on a delta run, its ID is in `delta.ask`' "$AUTONOMY_PHASE" || fail 'first and full runs must offer absent leaves without undefined delta'
for grant in 'Bash(cenci dispatch plan-refined on)' 'Bash(cenci dispatch plan-refined off)' 'Bash(cenci dispatch plan-refined status)' 'Bash(cenci automerge on)' 'Bash(cenci automerge off)' 'Bash(cenci automerge status)' 'Bash(cenci planning attended on)' 'Bash(cenci planning attended off)' 'Bash(cenci planning attended status --json)'; do
  grep -Fq "$grant" "$SKILL" || fail "host CLI commands need their scoped grant: $grant"
done
echo 'configure-independent-controls.test.sh: all checks passed.'
