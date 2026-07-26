#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FLOW="${ROOT}/.github/workflows/flow-ci.yml"
SCHEDULED="${ROOT}/.github/workflows/cenci-maintenance.yml"
LINT="${ROOT}/.github/workflows/workflow-lint.yml"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }
contains() { grep -qF -- "$2" "$1" || fail "$3"; }
lacks() { ! grep -qF -- "$2" "$1" || fail "$3"; }

echo "maintenance-workflows.test.sh"

for path in \
  "flow/skills/**" "flow/agents/**" "flow/hooks/**" "flow/docs/**" \
  "docs/**" "README.md" "flow/README.md" ".cenci/config.json" \
  "install.sh" "sandbox/**" "watch/**" ".claude-plugin/**" \
  "flow/codex/**" "flow/opencode/**" ".github/scripts/maintenance-*" \
  ".github/workflows/cenci-maintenance.yml" ".github/workflows/workflow-lint.yml"
do
  contains "$FLOW" "$path" "flow-ci maintenance filter missing $path"
done
contains "$FLOW" 'maintenance: ${{ steps.filter.outputs.maintenance }}' "flow-ci missing maintenance output"
contains "$FLOW" "name: flow-maintenance" "flow-ci missing dedicated maintenance job"
contains "$FLOW" "Run maintain checker (changed files)" "flow-ci missing changed checker"
contains "$FLOW" "Run maintain checker (full report-only)" "flow-ci missing push-to-main full checker"
lacks "$FLOW" "needs.changes.outputs.maintenance == 'true' && github.event_name == 'pull_request'" "flow-ci maintenance job is incorrectly limited to pull requests"
lacks "$FLOW" "run: bash flow/skills/maintain/scripts/check.sh" "flow-ci has a checker invocation without an explicit CWD block"
contains "$FLOW" 'git -C "$CHECKOUT_ROOT" diff --name-only "${BASE_SHA}"...HEAD > "$CHANGED_FILE"' "flow-ci must check changed-file discovery before mapfile"
lacks "$FLOW" 'mapfile -t CHANGED < <(' "flow-ci must not hide git diff failure behind process substitution"

contains "$SCHEDULED" "contents: read" "scheduled workflow must use contents: read"
contains "$SCHEDULED" "issues: write" "scheduled workflow must use issues: write"
lacks "$SCHEDULED" "contents: write" "scheduled workflow must not use contents: write"
lacks "$SCHEDULED" "git push" "scheduled workflow must not push"
lacks "$SCHEDULED" "git commit" "scheduled workflow must not commit"
lacks "$SCHEDULED" "last-audit.json" "scheduled workflow must not write audit markers"
contains "$SCHEDULED" "--report-file" "scheduled checker must use explicit report file"
contains "$SCHEDULED" "Reconcile maintenance issue" "scheduled workflow missing isolated reconciliation step"
contains "$SCHEDULED" "reconcile:" "scheduled workflow must isolate reconciliation in its own job"
contains "$SCHEDULED" "Read maintenance UX setting" "scheduled workflow must resolve maintenance.enabled"
contains "$SCHEDULED" "needs.audit.outputs.enabled == 'true'" "scheduled reconciliation must honor maintenance.enabled"
contains "$SCHEDULED" "persist-credentials: false" "scheduled checkouts must not persist GitHub credentials"
contains "$SCHEDULED" 'group: cenci-maintenance-${{ github.repository }}' "scheduled workflow must serialize repository audits"
contains "$SCHEDULED" "cancel-in-progress: false" "scheduled workflow must not cancel an active reconciliation"
contains "$SCHEDULED" 'runner.temp' "scheduled report must live in runner-local temporary storage"
contains "$SCHEDULED" 'github.run_attempt' "scheduled report artifact must be unique to the workflow attempt"
contains "$SCHEDULED" "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" "scheduled workflow must pin report upload"
contains "$SCHEDULED" "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093" "scheduled workflow must pin report download"
contains "$SCHEDULED" "if-no-files-found: error" "scheduled workflow must fail instead of reconciling a missing/stale report"
contains "$LINT" "docker://rhysd/actionlint:1.7.7" "workflow lint must run a pinned actionlint image"
lacks "$LINT" "docker://rhysd/actionlint:latest" "workflow lint must not use an unpinned actionlint image"

AUDIT_BLOCK="$(sed -n '/^  audit:/,/^  reconcile:/p' "$SCHEDULED")"
RECONCILE_BLOCK="$(sed -n '/^  reconcile:/,$p' "$SCHEDULED")"
[[ "$AUDIT_BLOCK" != *GH_TOKEN* ]] || fail "scheduled audit step exposes GH_TOKEN"
[[ "$AUDIT_BLOCK" != *"issues: write"* ]] || fail "scheduled audit job has issue-write permission"
[[ "$RECONCILE_BLOCK" == *GH_TOKEN* ]] || fail "scheduled reconciliation step lacks GH_TOKEN"
[[ "$RECONCILE_BLOCK" == *"issues: write"* ]] || fail "scheduled reconciliation job lacks issue-write permission"
lacks "$SCHEDULED" 'scheduled-maintain-report.json' "scheduled report must not use a reusable checkout path"

echo "maintenance-workflows.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
