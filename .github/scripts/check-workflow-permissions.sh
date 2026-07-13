#!/usr/bin/env bash
# Statically validates the permission declarations around the reusable
# plugin-version-bump.yml workflow and its per-plugin caller workflows.
#
# Rules enforced:
#   1. At least one caller workflow must exist (jobs.*.uses ending in
#      plugin-version-bump.yml) — guards against a yq path typo silently
#      passing with zero workflows checked.
#   2. Every caller must declare permissions.contents == write.
#   3. Every caller that passes a non-empty dispatch-workflow input must
#      declare permissions.actions == write.
#   4. Every caller that does NOT pass dispatch-workflow must NOT declare
#      permissions.actions at all (least-privilege — catches a
#      re-introduced stopgap).
#   5. The reusable plugin-version-bump.yml workflow itself must declare NO
#      top-level permissions: block (GitHub caps a reusable workflow's
#      effective permissions to whatever the caller grants, so the reusable
#      workflow should not carry its own).
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

# --- Rules 2-4: per-caller permission checks --------------------------------
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
  actions_perm=$(yq '.permissions.actions // ""' "$f") || {
    fail "$f: yq failed to read .permissions.actions"
    continue
  }

  if [ -n "$dispatch_workflow" ]; then
    if [ "$actions_perm" != "write" ]; then
      fail "$f: rule 3 violated — passes dispatch-workflow ('$dispatch_workflow') but permissions.actions must be 'write' (got '${actions_perm:-<absent>}')"
    fi
  else
    if [ -n "$actions_perm" ]; then
      fail "$f: rule 4 violated — does not pass dispatch-workflow, so permissions.actions must be absent (got '$actions_perm')"
    fi
  fi
done

# --- Rule 5: the reusable workflow must declare no top-level permissions ----
reusable_perms=$(yq '.permissions // ""' "$REUSABLE_WORKFLOW") || {
  fail "$REUSABLE_WORKFLOW: yq failed to read .permissions"
  reusable_perms=""
}
if [ -n "$reusable_perms" ]; then
  fail "$REUSABLE_WORKFLOW: rule 5 violated — must not declare a top-level permissions: block (reusable workflows inherit the caller's grant)"
fi

if [ "$FAILED" -ne 0 ]; then
  echo "check-workflow-permissions: FAILED" >&2
  exit 1
fi

echo "check-workflow-permissions: all checks passed"
