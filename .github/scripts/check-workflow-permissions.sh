#!/usr/bin/env bash
# Statically validates the permission declarations around the reusable
# plugin-version-bump.yml workflow and its per-plugin caller workflows.
#
# Rules enforced:
#   1. At least one caller workflow must exist (jobs.*.uses ending in
#      plugin-version-bump.yml) — guards against a yq path typo silently
#      passing with zero workflows checked.
#   2. Every caller must declare permissions.contents == write.
#   3. Every caller's .permissions keys must be a subset of {contents,
#      actions} — least-privilege allowlist. `actions` is only permitted
#      when the caller passes a non-empty dispatch-workflow input, and when
#      present must be == write. `contents` must be write (checked by rule
#      2 above). ANY other key present under .permissions (e.g. packages,
#      id-token) fails this rule regardless of its value — this replaces an
#      older, narrower rule 4 that only inspected permissions.actions and
#      let every other key slip through silently unchecked.
#   4. The reusable plugin-version-bump.yml workflow itself must declare NO
#      top-level permissions: block (GitHub caps a reusable workflow's
#      effective permissions to whatever the caller grants, so the reusable
#      workflow should not carry its own).
#
# Known accepted residual risk: this script only runs in CI (workflow-lint.yml)
# and this repo requires 0 approvals on PRs (solo maintainer, see
# docs/release-hygiene.md §4). A single PR that simultaneously re-adds an
# over-broad permission grant to a caller workflow AND weakens/removes the
# corresponding check in this script would still pass CI and could be
# self-merged. There is no automated gate against this; the only mitigation
# is normal PR review of diffs to this file. Closing that gap (e.g. via a
# required second reviewer or a CODEOWNERS rule on this script) is out of
# scope for this ticket — noted here as documentation only.
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

FAILED=0

fail() {
  echo "FAIL: $*" >&2
  FAILED=1
}

if ! command -v yq >/dev/null 2>&1; then
  echo "FAIL: yq is required but was not found on PATH" >&2
  exit 1
fi

if [ ! -f "$REUSABLE_WORKFLOW" ]; then
  fail "reusable workflow not found at $REUSABLE_WORKFLOW"
  exit 1
fi

# --- Rule 1: discover every caller of plugin-version-bump.yml ---------------
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
  fail "no caller workflows discovered (jobs.*.uses ending in '$REUSABLE_SUFFIX' under $WORKFLOWS_DIR) — check for a yq query or path typo"
  exit 1
fi

echo "Discovered ${#callers[@]} caller workflow(s) of $REUSABLE_SUFFIX:"
printf '  - %s\n' "${callers[@]}"

# --- Rules 2-3: per-caller permission checks --------------------------------
for f in "${callers[@]}"; do
  contents_perm=$(yq '.permissions.contents // ""' "$f") || {
    fail "$f: yq failed to read .permissions.contents"
    continue
  }
  if [ "$contents_perm" != "write" ]; then
    fail "$f: rule 2 violated — permissions.contents must be 'write' (got '${contents_perm:-<absent>}')"
  fi

  dispatch_workflow=$(yq '.jobs.*.with.dispatch-workflow // ""' "$f") || {
    fail "$f: yq failed to read .jobs.*.with.dispatch-workflow"
    continue
  }

  perm_keys=$(yq '(.permissions // {}) | keys | .[]' "$f") || {
    fail "$f: yq failed to read .permissions keys"
    continue
  }

  while IFS= read -r key; do
    [ -n "$key" ] || continue
    case "$key" in
      contents)
        : # already validated by rule 2 above
        ;;
      actions)
        if [ -z "$dispatch_workflow" ]; then
          fail "$f: rule 3 violated — permissions.actions is declared but no dispatch-workflow input is passed"
        else
          actions_perm=$(yq '.permissions.actions // ""' "$f") || {
            fail "$f: yq failed to read .permissions.actions"
            continue
          }
          if [ "$actions_perm" != "write" ]; then
            fail "$f: rule 3 violated — passes dispatch-workflow ('$dispatch_workflow') but permissions.actions must be 'write' (got '${actions_perm:-<absent>}')"
          fi
        fi
        ;;
      *)
        fail "$f: rule 3 violated — permissions.$key is not permitted (only {contents, actions} are allowed under .permissions)"
        ;;
    esac
  done <<< "$perm_keys"
done

# --- Rule 4: the reusable workflow must declare no top-level permissions ----
reusable_perms=$(yq '.permissions // ""' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read .permissions"
  reusable_perms=""
}
if [ -n "$reusable_perms" ]; then
  fail "$REUSABLE_WORKFLOW: rule 4 violated — must not declare a top-level permissions: block (reusable workflows inherit the caller's grant)"
fi

if [ "$FAILED" -ne 0 ]; then
  echo "check-workflow-permissions: FAILED" >&2
  exit 1
fi

echo "check-workflow-permissions: all checks passed"
