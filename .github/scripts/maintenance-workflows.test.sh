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
lacks_re() { ! grep -qE -- "$2" "$1" || fail "$3"; }
contains_re() { grep -qE -- "$2" "$1" || fail "$3"; }

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
contains "$FLOW" 'maintenance: ${{ steps.extra.outputs.maintenance }}' "flow-ci missing maintenance output"
contains "$FLOW" "name: flow-maintenance" "flow-ci missing dedicated maintenance job"
contains "$FLOW" "Run maintain checker (changed files)" "flow-ci missing changed checker"
contains "$FLOW" "Run maintain checker (full report-only)" "flow-ci missing push-to-main full checker"
lacks "$FLOW" "needs.changes.outputs.maintenance == 'true' && github.event_name == 'pull_request'" "flow-ci maintenance job is incorrectly limited to pull requests"
lacks "$FLOW" "run: bash flow/skills/maintain/scripts/check.sh" "flow-ci has a checker invocation without an explicit CWD block"
contains "$FLOW" 'git -C "$CHECKOUT_ROOT" diff --name-only "${BASE_SHA}"...HEAD > "$CHANGED_FILE"' "flow-ci must check changed-file discovery before mapfile"
lacks "$FLOW" 'mapfile -t CHANGED < <(' "flow-ci must not hide git diff failure behind process substitution"

contains "$FLOW" "bash flow/scripts/run-checks.sh" "flow-ci must run the shared flow test discovery script"
lacks_re "$FLOW" 'run: bash flow/.*\.test\.sh' "flow-ci must not enumerate per-suite test steps (ticket #720 replaces them with run-checks.sh discovery)"
lacks "$FLOW" "Validate flow JSON" "flow-ci must not have a standalone Validate flow JSON step (run-checks.sh owns JSON validation)"

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
# Assert a full commit-SHA pin, not one specific SHA: Dependabot bumps the pin
# (#1205/#1206) and a literal-SHA match would fail every legitimate bump.
contains_re "$SCHEDULED" 'uses: actions/upload-artifact@[0-9a-f]{40}( |$)' "scheduled workflow must pin report upload to a full commit SHA"
lacks_re "$SCHEDULED" 'uses: actions/upload-artifact@([^0-9a-f]|[0-9a-f]{0,39}([^0-9a-f]|$))' "scheduled workflow must not use a tag or short-SHA report upload"
contains_re "$SCHEDULED" 'uses: actions/download-artifact@[0-9a-f]{40}( |$)' "scheduled workflow must pin report download to a full commit SHA"
lacks_re "$SCHEDULED" 'uses: actions/download-artifact@([^0-9a-f]|[0-9a-f]{0,39}([^0-9a-f]|$))' "scheduled workflow must not use a tag or short-SHA report download"
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

# The audit runs the flow gate, whose hooks-launch.test.sh fails (not skips) under CI=true
# without zsh and fish. Prints the failure reason for a block, or nothing when it is sound.
INSTALL_NEEDLE='apt-get install -y zsh fish'
STRICT_NEEDLE='check.sh --strict'
REASON_MISSING="scheduled audit job must install zsh and fish (hooks-launch.test.sh fails under CI=true without them)"
REASON_ORDER="scheduled audit job must install zsh and fish before check.sh --strict"
REASON_NO_STRICT="scheduled audit job must run check.sh --strict (install ordering cannot be checked)"
audit_shell_install_problem() {
  local code install_line strict_line
  # A commented-out line is not an install or a strict run.
  code="$(printf '%s\n' "$1" | grep -vE -- '^[[:space:]]*#')"
  install_line="$(printf '%s\n' "$code" | grep -nF -- "$INSTALL_NEEDLE" | head -n 1 | cut -d: -f1)"
  strict_line="$(printf '%s\n' "$code" | grep -nF -- "$STRICT_NEEDLE" | head -n 1 | cut -d: -f1)"
  if [[ -z "$install_line" ]]; then
    printf '%s\n' "$REASON_MISSING"
  elif [[ -z "$strict_line" ]]; then
    printf '%s\n' "$REASON_NO_STRICT"
  elif [[ "$install_line" -ge "$strict_line" ]]; then
    printf '%s\n' "$REASON_ORDER"
  fi
}

REAL_PROBLEM="$(audit_shell_install_problem "$AUDIT_BLOCK")"
[[ -z "$REAL_PROBLEM" ]] || fail "$REAL_PROBLEM"

# Non-vacuity: a fixture must be non-empty, differ from the real block and still hold what
# it must keep (else the checker reports the expected reason for the wrong cause); then the
# checker must report the expected reason. Runs in the parent shell so fail() counts.
expect_problem() { # <checker fn> <fixture> <expected reason> <message> [text the fixture must still contain]
  local fn="$1" fixture="$2" expected="$3" msg="$4" keep="${5:-}"
  if [[ -z "$fixture" || "$fixture" == "$AUDIT_BLOCK" || ( -n "$keep" && "$fixture" != *"$keep"* ) ]]; then
    fail "self-test: fixture was not derived from the audit block (${msg})"
  elif [[ "$("$fn" "$fixture")" != "$expected" ]]; then
    fail "self-test: ${msg}"
  fi
}

MISSING_BLOCK="$(printf '%s\n' "$AUDIT_BLOCK" | grep -vF -- "$INSTALL_NEEDLE")"
REORDERED_BLOCK="${MISSING_BLOCK}"$'\n'"      sudo ${INSTALL_NEEDLE}"
NO_STRICT_BLOCK="$(printf '%s\n' "$AUDIT_BLOCK" | grep -vF -- "$STRICT_NEEDLE")"
COMMENTED_BLOCK="$(printf '%s\n' "$AUDIT_BLOCK" | sed "s|^\( *\)\(.*${INSTALL_NEEDLE}\)|\1# \2|")"
expect_problem audit_shell_install_problem "$MISSING_BLOCK" "$REASON_MISSING" \
  "block without the install line was not reported as missing" "$STRICT_NEEDLE"
expect_problem audit_shell_install_problem "$REORDERED_BLOCK" "$REASON_ORDER" \
  "install line after check.sh --strict was not reported as misordered" "$STRICT_NEEDLE"
expect_problem audit_shell_install_problem "$NO_STRICT_BLOCK" "$REASON_NO_STRICT" \
  "block without the check.sh --strict line was not reported" "$INSTALL_NEEDLE"
expect_problem audit_shell_install_problem "$COMMENTED_BLOCK" "$REASON_MISSING" \
  "commented-out install line was not reported as missing" "$INSTALL_NEEDLE"

# With maintenance.enabled: false the install step must be skipped like the audit step is.
INSTALL_STEP='Install login shells for hook launch tests'
GATE_NEEDLE="if: \${{ steps.maintenance-enabled.outputs.enabled == 'true' }}"
REASON_UNGATED="scheduled audit install step must be gated on steps.maintenance-enabled.outputs.enabled == 'true' like Run maintenance audit"
REASON_AUDIT_UNGATED="scheduled Run maintenance audit step must be gated on steps.maintenance-enabled.outputs.enabled == 'true'"
# Whole-line comments are dropped first: a commented-out if: is not a gate, and a commented-out
# "# - name:" line must not win grep -m 1 ahead of the real step.
step_gated() { printf '%s\n' "$1" | grep -vE -- '^[[:space:]]*#' | grep -A2 -m 1 -F -- "name: $2" | grep -qF -- "$GATE_NEEDLE"; }
audit_install_gate_problem() {
  step_gated "$1" "$INSTALL_STEP" || { printf '%s\n' "$REASON_UNGATED"; return; }
  step_gated "$1" "Run maintenance audit" || printf '%s\n' "$REASON_AUDIT_UNGATED"
}
GATE_PROBLEM="$(audit_install_gate_problem "$AUDIT_BLOCK")"
[[ -z "$GATE_PROBLEM" ]] || fail "$GATE_PROBLEM"
# Non-vacuity: dropping or commenting out one step's own if: line from the real block must be
# reported. The flag resets at every step start (any "- key:"), so a step without an if: never
# costs the next step its own.
edit_step_if() { # <block> <step name> <drop|comment>
  printf '%s\n' "$1" | awk -v n="- name: $2" -v m="$3" '/^ *- [A-Za-z_-]+:/ { f = index($0, n) > 0 } f && /^ *if:/ { if (m == "comment") { sub(/^ */, "&# "); print }; next } { print }'
}
HYPHEN_IN=$'      - name: a\n        if: x\n      - timeout-minutes: 5\n        if: y'
HYPHEN_OUT=$'      - name: a\n      - timeout-minutes: 5\n        if: y'
[[ "$(edit_step_if "$HYPHEN_IN" a drop)" == "$HYPHEN_OUT" ]] || fail "self-test: if: removal leaked past a hyphenated step-start key"
# Each fixture must differ from the real block by exactly the gate line (drop: 1 diff line,
# comment: 2) and the other step must keep its own gate, or the reason could come from a leak.
expect_only_gate_edited() { # <fixture> <changed line count> <other step name> <message>
  local delta
  delta="$(diff <(printf '%s\n' "$AUDIT_BLOCK") <(printf '%s\n' "$1") | grep '^[<>]')"
  [[ "$(printf '%s\n' "$delta" | grep -c .)" -eq "$2" && "$(printf '%s\n' "$delta" | grep -cF -- "$GATE_NEEDLE")" -eq "$2" ]] \
    || fail "self-test: fixture changed more than the one gate line (${4})"
  step_gated "$1" "$3" || fail "self-test: fixture also removed the other step's gate (${4})"
}
UNGATED_BLOCK="$(edit_step_if "$AUDIT_BLOCK" "$INSTALL_STEP" drop)"
UNGATED_AUDIT_BLOCK="$(edit_step_if "$AUDIT_BLOCK" "Run maintenance audit" drop)"
COMMENTED_GATE_BLOCK="$(edit_step_if "$AUDIT_BLOCK" "$INSTALL_STEP" comment)"
COMMENTED_AUDIT_GATE_BLOCK="$(edit_step_if "$AUDIT_BLOCK" "Run maintenance audit" comment)"
expect_only_gate_edited "$UNGATED_BLOCK" 1 "Run maintenance audit" "install if: dropped"
expect_only_gate_edited "$UNGATED_AUDIT_BLOCK" 1 "$INSTALL_STEP" "audit if: dropped"
expect_only_gate_edited "$COMMENTED_GATE_BLOCK" 2 "Run maintenance audit" "install if: commented out"
expect_only_gate_edited "$COMMENTED_AUDIT_GATE_BLOCK" 2 "$INSTALL_STEP" "audit if: commented out"
expect_problem audit_install_gate_problem "$UNGATED_BLOCK" "$REASON_UNGATED" \
  "install step without its if: gate was not reported as ungated" "name: ${INSTALL_STEP}"
expect_problem audit_install_gate_problem "$UNGATED_AUDIT_BLOCK" "$REASON_AUDIT_UNGATED" \
  "Run maintenance audit step without its if: gate was not reported as ungated" "name: Run maintenance audit"
expect_problem audit_install_gate_problem "$COMMENTED_GATE_BLOCK" "$REASON_UNGATED" \
  "install step with its if: gate commented out was not reported as ungated" "name: ${INSTALL_STEP}"
expect_problem audit_install_gate_problem "$COMMENTED_AUDIT_GATE_BLOCK" "$REASON_AUDIT_UNGATED" \
  "Run maintenance audit step with its if: gate commented out was not reported as ungated" "name: Run maintenance audit"

echo "maintenance-workflows.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
