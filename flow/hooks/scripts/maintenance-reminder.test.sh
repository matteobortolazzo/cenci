#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK="${SCRIPT_DIR}/maintenance-reminder.sh"
REAL_GIT="$(command -v git)"
failures=0
TEST_ROOT="$(mktemp -d /var/tmp/maintenance-reminder-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected [$2] in [$1]"; }

make_repo() {
  local dir="$1"
  mkdir -p "${dir}/.cenci" "${dir}/flow/hooks/scripts"
  cp "$HOOK" "${dir}/flow/hooks/scripts/maintenance-reminder.sh"
  chmod +x "${dir}/flow/hooks/scripts/maintenance-reminder.sh"
  git -C "$dir" init -q -b main
  printf seed > "${dir}/seed.txt"
  git -C "$dir" add seed.txt
  git -C "$dir" -c user.email=test@example.com -c user.name=Test commit -q -m seed
  printf '{"maintenance":{"enabled":true,"remindAfterDays":7}}' > "${dir}/.cenci/config.json"
  git -C "$dir" remote add origin https://github.com/owner/repo.git
}

make_checker() {
  local root="$1" summary="$2" exit_code="${3:-0}"
  mkdir -p "${root}/flow/skills/maintain/scripts"
  cat > "${root}/flow/skills/maintain/scripts/check.sh" <<STUB
#!/usr/bin/env bash
printf '%s|%s\\n' "\$(pwd -P)" "\$*" >> "\${CHECK_LOG}"
printf '%s\\n' '$summary'
exit $exit_code
STUB
  chmod +x "${root}/flow/skills/maintain/scripts/check.sh"
}

# mode: active-success, active-failure, skipped-then-success, audit-skipped,
# disabled, offline, malformed-workflow, malformed-runs, malformed-jobs, slow
make_gh_stub() {
  local bin="$1" mode="$2" sha="$3" timestamp="$4"
  mkdir -p "$bin"
  cat > "${bin}/gh" <<STUB
#!/usr/bin/env bash
set -u
endpoint="\${2:-}"
case "$mode:\$endpoint" in
  offline:*) exit 1 ;;
  slow:*/actions/workflows/cenci-maintenance.yml) sleep 2; printf '{"state":"active"}\\n'; exit 0 ;;
  malformed-workflow:*/actions/workflows/cenci-maintenance.yml) printf '[]\\n'; exit 0 ;;
  malformed-runs:*/workflows/cenci-maintenance.yml/runs*) printf '{"workflow_runs":"bad"}\\n'; exit 0 ;;
  disabled:*/actions/workflows/cenci-maintenance.yml) printf '{"state":"disabled_manually"}\\n'; exit 0 ;;
  *:*/actions/workflows/cenci-maintenance.yml) printf '{"state":"active"}\\n'; exit 0 ;;
  active-success:*/workflows/cenci-maintenance.yml/runs*) printf '{"workflow_runs":[{"id":1,"conclusion":"success","updated_at":"$timestamp","head_sha":"$sha"}]}\\n'; exit 0 ;;
  active-failure:*/workflows/cenci-maintenance.yml/runs*) printf '{"workflow_runs":[{"id":1,"conclusion":"failure","updated_at":"$timestamp","head_sha":"$sha"}]}\\n'; exit 0 ;;
  skipped-then-success:*/workflows/cenci-maintenance.yml/runs*) printf '{"workflow_runs":[{"id":1,"conclusion":"success","updated_at":"2099-01-01T00:00:00Z","head_sha":"bad"},{"id":2,"conclusion":"success","updated_at":"$timestamp","head_sha":"$sha"}]}\\n'; exit 0 ;;
  audit-skipped:*/workflows/cenci-maintenance.yml/runs*) printf '{"workflow_runs":[{"id":1,"conclusion":"success","updated_at":"$timestamp","head_sha":"$sha"}]}\\n'; exit 0 ;;
  malformed-jobs:*/workflows/cenci-maintenance.yml/runs*) printf '{"workflow_runs":[{"id":1,"conclusion":"success","updated_at":"$timestamp","head_sha":"$sha"}]}\\n'; exit 0 ;;
  malformed-jobs:*/actions/runs/*/jobs) printf '{"jobs":"bad"}\\n'; exit 0 ;;
  skipped-then-success:*/actions/runs/1/jobs) printf '{"jobs":[{"steps":[{"name":"Run maintenance audit","conclusion":"skipped"}]}]}\\n'; exit 0 ;;
  skipped-then-success:*/actions/runs/2/jobs) printf '{"jobs":[{"steps":[{"name":"Run maintenance audit","conclusion":"success"}]}]}\\n'; exit 0 ;;
  audit-skipped:*/actions/runs/*/jobs) printf '{"jobs":[{"steps":[{"name":"Run maintenance audit","conclusion":"skipped"}]}]}\\n'; exit 0 ;;
  active-success:*/actions/runs/*/jobs) printf '{"jobs":[{"steps":[{"name":"Run maintenance audit","conclusion":"success"}]}]}\\n'; exit 0 ;;
  active-failure:*/actions/runs/*/jobs) printf '{"jobs":[{"steps":[{"name":"Run maintenance audit","conclusion":"failure"}]}]}\\n'; exit 0 ;;
esac
exit 2
STUB
  chmod +x "${bin}/gh"
}

run_hook() {
  local repo="$1" bin="$2" check_log="$3"
  set +e
  OUT="$(cd "$repo" && PATH="${bin}:${PATH}" CHECK_LOG="$check_log" bash "${repo}/flow/hooks/scripts/maintenance-reminder.sh" 2>&1)"
  CODE=$?
  set -e
}

assert_silent() { assert_eq "$CODE" 0 "$1 exit"; assert_eq "$OUT" "" "$1 output"; }
assert_advisory() {
  assert_eq "$CODE" 0 "$1 exit"
  jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' <<<"$OUT" >/dev/null 2>&1 || fail "$1: invalid hook JSON [$OUT]"
}

NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
STALE="$(date -u -d '-30 days' +%Y-%m-%dT%H:%M:%SZ)"

echo "maintenance-reminder.test.sh"

# Fresh successful metadata + clean local advisory check is silent.
repo="${TEST_ROOT}/fresh"
make_repo "$repo"
sha="$(git -C "$repo" rev-parse HEAD)"
make_checker "$repo" '{"summary":{"pass":2,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
make_gh_stub "${repo}/bin" active-success "$sha" "$NOW"
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
assert_silent fresh
assert_eq "$(cat "${repo}/check.log")" "${repo}|--advisory" "fresh checker cwd/args"
[[ ! -e "${repo}/.cenci/maintain-report.json" ]] || fail "fresh: advisory checker wrote default report"

# Stale success, latest failure, and local advisory findings each warn.
for mode in stale remote-failure local-failure skipped; do
  repo="${TEST_ROOT}/${mode}"
  make_repo "$repo"
  sha="$(git -C "$repo" rev-parse HEAD)"
  timestamp="$NOW"
  gh_mode=active-success
  summary='{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
  case "$mode" in
    stale) timestamp="$STALE" ;;
    remote-failure) gh_mode=active-failure ;;
    local-failure) summary='{"summary":{"pass":0,"warn":1,"fail":1,"skip":2,"mode":"advisory"},"results":[]}' ;;
    skipped) gh_mode=skipped-then-success ;;
  esac
  make_checker "$repo" "$summary"
  make_gh_stub "${repo}/bin" "$gh_mode" "$sha" "$timestamp"
  run_hook "$repo" "${repo}/bin" "${repo}/check.log"
  if [[ "$mode" == skipped ]]; then
    assert_silent "$mode"
  else
    assert_advisory "$mode"
    case "$mode" in
      stale) assert_contains "$OUT" "more than the configured" "stale reason" ;;
      remote-failure) assert_contains "$OUT" "concluded 'failure'" "remote failure reason" ;;
      local-failure) assert_contains "$OUT" "bounded local maintenance advisory" "local failure reason" ;;
    esac
  fi
done

# A sensitive change since Actions' head_sha warns.
repo="${TEST_ROOT}/changed"
make_repo "$repo"
sha="$(git -C "$repo" rev-parse HEAD)"
printf changed > "${repo}/README.md"
git -C "$repo" add README.md
git -C "$repo" -c user.email=test@example.com -c user.name=Test commit -q -m changed
make_checker "$repo" '{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
make_gh_stub "${repo}/bin" active-success "$sha" "$NOW"
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
assert_advisory changed
assert_contains "$OUT" "README.md" "changed-path reason"

# A comparison failure is reported as unknown instead of becoming an empty
# changed-file list.
repo="${TEST_ROOT}/diff-failure"
make_repo "$repo"
sha="$(git -C "$repo" rev-parse HEAD)"
make_checker "$repo" '{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
make_gh_stub "${repo}/bin" active-success "$sha" "$NOW"
cat > "${repo}/bin/git" <<EOF
#!/usr/bin/env bash
for arg in "\$@"; do
  [[ "\$arg" == "diff" ]] && exit 1
done
exec "${REAL_GIT}" "\$@"
EOF
chmod +x "${repo}/bin/git"
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
assert_advisory diff-failure
assert_contains "$OUT" "change status is unknown" "diff-failure reason"

# Disabled, offline, and malformed remote metadata do not invent remote state;
# the local checker still runs and the hook remains non-blocking.
for mode in disabled offline malformed-workflow malformed-runs malformed-jobs audit-skipped; do
  repo="${TEST_ROOT}/${mode}"
  make_repo "$repo"
  sha="$(git -C "$repo" rev-parse HEAD)"
  make_checker "$repo" '{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
  make_gh_stub "${repo}/bin" "$mode" "$sha" "$NOW"
  run_hook "$repo" "${repo}/bin" "${repo}/check.log"
  assert_silent "$mode"
  assert_eq "$(wc -l < "${repo}/check.log" | tr -d ' ')" 1 "$mode checker invocation"
done

# Slow local and remote commands remain within the host hook's five-second
# deadline and still produce a useful local status-unknown result.
repo="${TEST_ROOT}/slow-local"
make_repo "$repo"
sha="$(git -C "$repo" rev-parse HEAD)"
mkdir -p "${repo}/flow/skills/maintain/scripts"
cat > "${repo}/flow/skills/maintain/scripts/check.sh" <<'EOF'
#!/usr/bin/env bash
sleep 4
printf '%s\n' '{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
EOF
chmod +x "${repo}/flow/skills/maintain/scripts/check.sh"
make_gh_stub "${repo}/bin" offline "$sha" "$NOW"
SECONDS=0
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
elapsed="$SECONDS"
assert_advisory slow-local
assert_contains "$OUT" "maintenance status is unknown" "slow-local reason"
[[ "$elapsed" -lt 5 ]] || fail "slow-local exceeded host hook budget (${elapsed}s)"

repo="${TEST_ROOT}/slow-remote"
make_repo "$repo"
sha="$(git -C "$repo" rev-parse HEAD)"
make_checker "$repo" '{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
make_gh_stub "${repo}/bin" slow "$sha" "$NOW"
SECONDS=0
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
elapsed="$SECONDS"
assert_silent slow-remote
[[ "$elapsed" -lt 5 ]] || fail "slow-remote exceeded host hook budget (${elapsed}s)"

# Checker timeout, infrastructure exit, and malformed output produce a bounded
# status-unknown reminder without exposing raw diagnostics.
for mode in checker-timeout checker-exit-two checker-malformed; do
  repo="${TEST_ROOT}/${mode}"
  make_repo "$repo"
  sha="$(git -C "$repo" rev-parse HEAD)"
  case "$mode" in
    checker-timeout)
      make_checker "$repo" '{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
      mkdir -p "${repo}/bin"
      cat > "${repo}/bin/timeout" <<'EOF'
#!/usr/bin/env bash
exit 124
EOF
      chmod +x "${repo}/bin/timeout"
      ;;
    checker-exit-two)
      make_checker "$repo" '{"summary":{"pass":1,"warn":0,"fail":0,"skip":2,"mode":"advisory"},"results":[]}' 2
      ;;
    checker-malformed)
      make_checker "$repo" 'not json' 1
      ;;
  esac
  make_gh_stub "${repo}/bin" active-success "$sha" "$NOW"
  run_hook "$repo" "${repo}/bin" "${repo}/check.log"
  assert_advisory "$mode"
  assert_contains "$OUT" "maintenance status is unknown" "$mode reason"
  [[ "$OUT" != *"not json"* ]] || fail "$mode: raw checker output leaked"
done

repo="${TEST_ROOT}/enabled-default"
make_repo "$repo"
printf '{"maintenance":{"remindAfterDays":7}}' > "${repo}/.cenci/config.json"
sha="$(git -C "$repo" rev-parse HEAD)"
make_checker "$repo" '{"summary":{"pass":0,"warn":1,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
make_gh_stub "${repo}/bin" active-success "$sha" "$NOW"
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
assert_advisory enabled-default
assert_contains "$OUT" "bounded local maintenance advisory" "enabled-default reason"

# Only an explicit boolean false disables the optional UX. Wrong-typed values
# degrade to the enabled default, matching the scheduled workflow.
repo="${TEST_ROOT}/enabled-wrong-type"
make_repo "$repo"
printf '{"maintenance":{"enabled":"false","remindAfterDays":7}}' > "${repo}/.cenci/config.json"
sha="$(git -C "$repo" rev-parse HEAD)"
make_checker "$repo" '{"summary":{"pass":0,"warn":1,"fail":0,"skip":2,"mode":"advisory"},"results":[]}'
make_gh_stub "${repo}/bin" active-success "$sha" "$NOW"
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
assert_advisory enabled-wrong-type
assert_contains "$OUT" "bounded local maintenance advisory" "enabled-wrong-type reason"

repo="${TEST_ROOT}/disabled-config"
make_repo "$repo"
printf '{"maintenance":{"enabled":false,"remindAfterDays":7}}' > "${repo}/.cenci/config.json"
sha="$(git -C "$repo" rev-parse HEAD)"
make_checker "$repo" '{"summary":{"pass":0,"warn":1,"fail":1,"skip":2,"mode":"advisory"},"results":[]}'
make_gh_stub "${repo}/bin" active-failure "$sha" "$STALE"
run_hook "$repo" "${repo}/bin" "${repo}/check.log"
assert_silent disabled-config
[[ ! -e "${repo}/check.log" ]] || fail "disabled-config: checker should not run"

echo "maintenance-reminder.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
