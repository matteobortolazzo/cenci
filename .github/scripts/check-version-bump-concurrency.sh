#!/usr/bin/env bash
# Statically validates the concurrency/push-race guards around the reusable
# plugin-version-bump.yml workflow and its per-plugin caller workflows
# (tickets #342, #743).
#
# Rules enforced:
#   1. The reusable workflow (plugin-version-bump.yml) must declare a
#      top-level concurrency: block whose .group is keyed PER PLUGIN —
#      exactly "version-bump-main-${{ inputs.tag-prefix }}" — with
#      .cancel-in-progress == false.
#
#      #743: this replaced a single shared "version-bump-main" queue. GitHub
#      keeps at most ONE pending run per concurrency group and cancels any
#      previously pending run, so the shared queue silently dropped a third
#      simultaneous bump — which is how the #742 installer fix missed its
#      release, reported as `cancelled` rather than `failure`. Keying on
#      tag-prefix means different plugins never contend, while two runs for
#      the same plugin still serialize. Reverting to a constant group
#      reintroduces the dropped-release bug, so this rule pins the exact
#      templated string.
#   2. The reusable workflow's "Checkout main" step must declare
#      .with.ref == "main" and .with.fetch-depth == 0 — the
#      checked-out tree (and push base) must be the live tip of main at run
#      start, not whatever commit triggered the run.
#   3. The reusable workflow's "Bump version" step must declare
#      .env.ORIGINAL_SHA == "${{ github.sha }}" — the triggering commit,
#      used for the skip guard and bump-type classification, kept separate
#      from the live main tree checked out by rule 2.
#   4. Every caller workflow (jobs.*.uses ending in plugin-version-bump.yml)
#      must NOT declare its own top-level concurrency.group beginning with
#      "version-bump-main" — GitHub's documented gotcha is that a caller
#      declaring the same concurrency group as a reusable workflow it
#      invokes gets cancelled when the reusable workflow enters that group.
#      The check is a prefix match rather than an equality test because the
#      reusable workflow's group is now templated per plugin (#743), so the
#      exact string a caller would have to collide with varies.
#      At least one caller must be discovered — guards against a yq path
#      typo silently passing with zero workflows checked.
#   5. The reusable workflow's "Bump version" step must delegate to
#      .github/scripts/bump-plugin-version.sh, and that script must exist.
#      The push-retry loop that replaced queue-based serialization lives in
#      that script precisely so it is unit-testable (#743); re-inlining the
#      bump logic into the workflow YAML would drop it back out of test
#      coverage, where the original race went unnoticed.
#
# Failures are accumulated rather than exiting on the first one, so a single
# run reports every offending file. GitHub Actions' default `run:` shell has
# errexit (-e) ON, which would otherwise abort this script at the first
# failed assertion before the rest of the files are checked — `set +e` below
# disables that. This also makes the script safe to run manually as a plain
# bash script outside of Actions.
set -uo pipefail
set +e

WORKFLOWS_DIR=".github/workflows"
REUSABLE_WORKFLOW="${WORKFLOWS_DIR}/plugin-version-bump.yml"
REUSABLE_SUFFIX="plugin-version-bump.yml"
BUMP_SCRIPT=".github/scripts/bump-plugin-version.sh"
# The exact per-plugin concurrency group required by rule 1. Single-quoted
# throughout: this is the literal GitHub Actions expression as written in the
# YAML, not something the shell should expand.
# shellcheck disable=SC2016
EXPECTED_GROUP='version-bump-main-${{ inputs.tag-prefix }}'
# The prefix no caller may collide with (rule 4).
CALLER_FORBIDDEN_GROUP_PREFIX="version-bump-main"

FAILED=0

fail() {
  echo "FAIL: $*" >&2
  FAILED=1
}

if ! command -v yq >/dev/null 2>&1; then
  echo "FAIL: yq is required but was not found on PATH" >&2
  exit 1
fi

yq_version=$(yq --version 2>&1) || {
  echo "FAIL: yq --version failed to execute" >&2
  exit 1
}
if [[ "$yq_version" != *mikefarah* ]]; then
  echo "FAIL: yq must be mikefarah/yq (Go-yq); found: $yq_version" >&2
  exit 1
fi

if [ ! -f "$REUSABLE_WORKFLOW" ]; then
  fail "reusable workflow not found at $REUSABLE_WORKFLOW"
  exit 1
fi

# --- Rule 1: reusable workflow's top-level concurrency: block --------------
concurrency_group=$(yq '.concurrency.group // ""' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read .concurrency.group"
  concurrency_group=""
}
if [ "$concurrency_group" != "$EXPECTED_GROUP" ]; then
  fail "$REUSABLE_WORKFLOW: rule 1 violated — .concurrency.group must be per-plugin, exactly '${EXPECTED_GROUP}' (got '${concurrency_group:-<absent>}'); a constant group silently drops a third simultaneous bump"
fi

# Note: no `// ""` fallback here — cancel-in-progress's expected value
# (false) is itself falsy under yq/jq's `//` alternative operator, which
# would otherwise mask an explicit `false` as "absent".
concurrency_cancel=$(yq '.concurrency."cancel-in-progress"' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read .concurrency.cancel-in-progress"
  concurrency_cancel="null"
}
if [ "$concurrency_cancel" != "false" ]; then
  reported="$concurrency_cancel"
  [ "$reported" == "null" ] && reported="<absent>"
  fail "$REUSABLE_WORKFLOW: rule 1 violated — .concurrency.cancel-in-progress must be 'false' (got '${reported}')"
fi

# --- Rule 2: reusable workflow's checkout step (ref + fetch-depth) ---------
checkout_ref=$(yq '[.jobs.*.steps[] | select(.name == "Checkout main")][0].with.ref // ""' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read the checkout step's .with.ref"
  checkout_ref=""
}
if [ "$checkout_ref" != "main" ]; then
  fail "$REUSABLE_WORKFLOW: rule 2 violated — checkout step's .with.ref must be 'main' (got '${checkout_ref:-<absent>}')"
fi

checkout_fetch_depth=$(yq '[.jobs.*.steps[] | select(.name == "Checkout main")][0].with."fetch-depth" // ""' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read the checkout step's .with.fetch-depth"
  checkout_fetch_depth=""
}
if [ "$checkout_fetch_depth" != "0" ]; then
  fail "$REUSABLE_WORKFLOW: rule 2 violated — checkout step's .with.fetch-depth must be 0 (got '${checkout_fetch_depth:-<absent>}')"
fi

# --- Rule 3: reusable workflow's "Bump version" step declares ORIGINAL_SHA --
bump_original_sha=$(yq '[.jobs.*.steps[] | select(.name == "Bump version")][0].env.ORIGINAL_SHA // ""' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read the Bump version step's .env.ORIGINAL_SHA"
  bump_original_sha=""
}
# shellcheck disable=SC2016  # single quotes are deliberate: the literal
# GitHub Actions expression string, not a shell expansion, is the expected value
if [ "$bump_original_sha" != '${{ github.sha }}' ]; then
  fail "$REUSABLE_WORKFLOW: rule 3 violated — Bump version step's .env.ORIGINAL_SHA must be '\${{ github.sha }}' (got '${bump_original_sha:-<absent>}')"
fi

# --- Rule 4: discover callers; none may redeclare the concurrency group ----
callers=()
for f in "$WORKFLOWS_DIR"/*.yml; do
  [ -f "$f" ] || continue

  uses_list=$(yq '.jobs.*.uses // ""' "$f") || {
    fail "$f: yq failed to read .jobs.*.uses"
    continue
  }

  while IFS= read -r uses_value; do
    [ -n "$uses_value" ] || continue
    case "$uses_value" in
      *"$REUSABLE_SUFFIX")
        callers+=("$f")
        ;;
    esac
  done <<< "$uses_list"
done

if [ "${#callers[@]}" -eq 0 ]; then
  fail "rule 4 violated — no caller workflows discovered (jobs.*.uses ending in '$REUSABLE_SUFFIX' under $WORKFLOWS_DIR) — check for a yq query or path typo"
  exit 1
fi

echo "Discovered ${#callers[@]} caller workflow(s) of $REUSABLE_SUFFIX:"
printf '  - %s\n' "${callers[@]}"

for f in "${callers[@]}"; do
  caller_group=$(yq '.concurrency.group // ""' "$f") || {
    fail "$f: yq failed to read .concurrency.group"
    continue
  }
  case "$caller_group" in
    "${CALLER_FORBIDDEN_GROUP_PREFIX}"*)
      fail "$f: rule 4 violated — caller must not declare a concurrency.group starting with '${CALLER_FORBIDDEN_GROUP_PREFIX}' (got '${caller_group}'); this cancels the caller run when the reusable workflow it invokes enters that same group"
      ;;
  esac
done

# --- Rule 5: the "Bump version" step delegates to the tested script --------
if [ ! -f "$BUMP_SCRIPT" ]; then
  fail "rule 5 violated — $BUMP_SCRIPT not found; the reusable workflow's Bump version step must delegate to it"
fi

bump_run=$(yq '[.jobs.*.steps[] | select(.name == "Bump version")][0].run // ""' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read the Bump version step's .run"
  bump_run=""
}
case "$bump_run" in
  *"$BUMP_SCRIPT"*) : ;;
  *)
    fail "$REUSABLE_WORKFLOW: rule 5 violated — the Bump version step's run: must invoke '${BUMP_SCRIPT}' (the push-retry loop lives there so it stays unit-testable), got '${bump_run:-<absent>}'"
    ;;
esac

if [ "$FAILED" -ne 0 ]; then
  echo "check-version-bump-concurrency: FAILED" >&2
  exit 1
fi

echo "check-version-bump-concurrency: all checks passed"
