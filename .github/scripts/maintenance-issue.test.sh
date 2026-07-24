#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ISSUE_SH="${SCRIPT_DIR}/maintenance-issue.sh"
FAILURES=0
PASSES=0
TEST_ROOT="$(mktemp -d /var/tmp/maintenance-issue-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() { echo "FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }
assert_eq() { [[ "$1" == "$2" ]] && pass || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] && pass || fail "$3: expected [$2] in [$1]"; }
assert_not_contains() { [[ "$1" != *"$2"* ]] && pass || fail "$3: did not expect [$2] in [$1]"; }

write_report() {
  local dir="$1"
  mkdir -p "${dir}/.cenci"
  cat > "${dir}/.cenci/scheduled-maintain-report.json" <<'EOF'
{
  "summary": {"pass": 1, "warn": 1, "fail": 2, "skip": 0, "mode": "strict"},
  "results": [
    {"check":"gate-command","target":"secret/path","status":"fail","message":"Bearer RAW_SECRET diagnostic","fix":"RAW_FIX"},
    {"check":"context-budget","target":"AGENTS.md","status":"warn","message":"raw warning","fix":"fix it"},
    {"check":"gate-command","target":"flow","status":"fail","message":"second raw diagnostic","fix":"fix it"},
    {"check":"front-matter","target":"flow","status":"pass","message":"ok","fix":""}
  ]
}
EOF
}

# make_gh_stub <bin> <log> <list-mode> <fail-op>
# list-mode: none, one, unrelated, ambiguous, malformed, lookup-fail
make_gh_stub() {
  local bin="$1" log="$2" list_mode="$3" fail_op="${4:-}"
  mkdir -p "$bin"
  : > "$log"
  cat > "${bin}/gh" <<STUB
#!/usr/bin/env bash
set -u
printf '%s\\0' "\$@" | jq -R -s -c 'split("\\u0000")[:-1]' >> "$log"
printf '\\n' >> "$log"
op="\${1:-} \${2:-}"
if [[ "\$op" == "$fail_op" ]]; then exit 1; fi
case "\$op" in
  "label create") exit 0 ;;
  "issue list")
    case "$list_mode" in
      none) printf '[]\\n' ;;
      one) printf '%s\\n' '[{"number":42,"body":"<!-- cenci-maintenance-audit -->\\nmanaged"}]' ;;
      unrelated) printf '%s\\n' '[{"number":9,"body":"human-authored"}]' ;;
      ambiguous) printf '%s\\n' '[{"number":42,"body":"<!-- cenci-maintenance-audit -->"},{"number":43,"body":"<!-- cenci-maintenance-audit -->"}]' ;;
      malformed) printf '%s\\n' '{"not":"an array"}' ;;
      lookup-fail) exit 1 ;;
    esac
    exit 0
    ;;
  "issue create") printf 'https://example.invalid/issues/99\\n'; exit 0 ;;
  "issue edit"|"issue close") exit 0 ;;
esac
exit 2
STUB
  chmod +x "${bin}/gh"
}

run_case() {
  local dir="$1" mode="$2" bin="$3"
  set +e
  OUT="$(cd "$dir" && PATH="${bin}:${PATH}" GITHUB_REPOSITORY=owner/repo bash "$ISSUE_SH" "$mode" 2>&1)"
  CODE=$?
  set -e
}

count_call() {
  jq -c --arg a "$2" --arg b "$3" 'select(.[0] == $a and .[1] == $b)' "$1" | wc -l | tr -d ' '
}

flag_value() {
  jq -rs --arg a "$2" --arg b "$3" --arg f "$4" '
    [.[] | select(.[0] == $a and .[1] == $b)] | last as $c |
    ($c | index($f)) as $i |
    if $i == null then empty else $c[$i + 1] end
  ' "$1"
}

echo "maintenance-issue.test.sh"

for spec in \
  "create:none::0:issue:create" \
  "edit:one::0:issue:edit" \
  "resolve:one::0:issue:close" \
  "resolve-none:none::0:issue:close" \
  "lookup-failure:lookup-fail::1:issue:create" \
  "malformed:malformed::1:issue:create" \
  "ambiguous:ambiguous::1:issue:edit" \
  "create-failure:none:issue create:1:issue:create" \
  "edit-failure:one:issue edit:1:issue:edit" \
  "close-failure:one:issue close:1:issue:close"
do
  IFS=: read -r name list_mode fail_op expected call_a call_b <<<"$spec"
  dir="${TEST_ROOT}/${name}"
  bin="${dir}/bin"
  log="${dir}/calls.ndjson"
  mkdir -p "$dir"
  write_report "$dir"
  make_gh_stub "$bin" "$log" "$list_mode" "$fail_op"
  mode=report
  [[ "$name" == resolve* || "$name" == close-failure ]] && mode=resolve
  run_case "$dir" "$mode" "$bin"
  if [[ "$expected" == 0 ]]; then
    assert_eq "$CODE" "0" "$name exit"
  else
    [[ "$CODE" -ne 0 ]] && pass || fail "$name: expected nonzero exit"
  fi
  calls="$(count_call "$log" "$call_a" "$call_b")"
  if [[ "$name" == "resolve-none" || "$name" == "lookup-failure" || "$name" == "malformed" || "$name" == "ambiguous" ]]; then
    assert_eq "$calls" "0" "$name mutation count"
  else
    assert_eq "$calls" "1" "$name mutation count"
  fi
done

# Label reconciliation is also fail-closed.
dir="${TEST_ROOT}/label-failure"
mkdir -p "$dir"
write_report "$dir"
make_gh_stub "${dir}/bin" "${dir}/calls.ndjson" none "label create"
run_case "$dir" report "${dir}/bin"
[[ "$CODE" -ne 0 ]] && pass || fail "label-failure: expected nonzero exit"
assert_eq "$(count_call "${dir}/calls.ndjson" issue list)" "0" "label failure stops before lookup"

# A same-label human issue is not the bot-managed issue; report creates a new one.
dir="${TEST_ROOT}/unrelated"
mkdir -p "$dir"
write_report "$dir"
make_gh_stub "${dir}/bin" "${dir}/calls.ndjson" unrelated
run_case "$dir" report "${dir}/bin"
assert_eq "$CODE" "0" "unrelated issue exit"
assert_eq "$(count_call "${dir}/calls.ndjson" issue create)" "1" "unrelated issue creates managed issue"

# The label and hidden marker are both stable deduplication keys.
CREATE_BODY="$(flag_value "${TEST_ROOT}/create/calls.ndjson" issue create --body)"
CREATE_LABEL="$(flag_value "${TEST_ROOT}/create/calls.ndjson" issue create --label)"
assert_eq "$CREATE_LABEL" "cenci-maintenance-audit" "dedicated label"
assert_contains "$CREATE_BODY" "<!-- cenci-maintenance-audit -->" "hidden marker"

# Published content is bounded to check IDs/statuses, not report diagnostics.
assert_contains "$CREATE_BODY" "| gate-command | fail |" "structured fail row"
assert_contains "$CREATE_BODY" "| context-budget | warn |" "structured warn row"
assert_not_contains "$CREATE_BODY" "RAW_SECRET" "raw secret omitted"
assert_not_contains "$CREATE_BODY" "RAW_FIX" "raw fix omitted"
assert_not_contains "$CREATE_BODY" "secret/path" "raw target omitted"
assert_not_contains "$CREATE_BODY" "second raw diagnostic" "raw message omitted"

# A malformed check identifier cannot smuggle arbitrary report text into the
# otherwise structured public body.
dir="${TEST_ROOT}/malformed-report"
mkdir -p "$dir"
write_report "$dir"
jq '.results[0].check = "gate-command RAW_SECRET"' \
  "${dir}/.cenci/scheduled-maintain-report.json" > "${dir}/report.tmp"
mv "${dir}/report.tmp" "${dir}/.cenci/scheduled-maintain-report.json"
make_gh_stub "${dir}/bin" "${dir}/calls.ndjson" none
run_case "$dir" report "${dir}/bin"
[[ "$CODE" -ne 0 ]] && pass || fail "malformed-report: expected nonzero exit"
assert_eq "$(count_call "${dir}/calls.ndjson" issue create)" "0" "malformed report is not published"

echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "$FAILURES" -eq 0 ]]
