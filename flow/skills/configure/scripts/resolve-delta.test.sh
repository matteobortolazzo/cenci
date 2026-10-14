#!/usr/bin/env bash
# resolve-delta.test.sh — runtime behavior of resolve-delta.sh, the
# deterministic "what would a /cenci:configure re-run ask?" resolver (#1245,
# parent #1137), and the contract of its table, question-keys.json.
# Modelled on hooks/scripts/check-config-staleness.test.sh (a `failures=`
# counter, small assert_* helpers, self-contained, auto-discovered by the
# flow gate's `*.test.sh` glob).
#
# Contract pinned here:
#   resolve-delta.sh <config-path|-> [--all]
#   stdout (single line): {"ask":[ids],"carried":[ids],"missingKeys":[keys],"conditional":[ids]}
#   - a question is in `ask` when every one of its keys is absent AND its ID
#     is not in the config's `configureOffered` ledger; `--all` asks every row
#   - a key is present unless it is missing or null (false, {} and "" count)
#   - `projectsAware` rows also count a key found in any projects[] entry
#   - fail closed on every error: exit 2, empty stdout, message on stderr
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESOLVER="${SCRIPT_DIR}/resolve-delta.sh"
TABLE="${SCRIPT_DIR}/../question-keys.json"
BASH_BIN="$(command -v bash)" || { echo "bash not found on PATH" >&2; exit 1; }
[[ -n "${BASH_BIN}" ]] || { echo "command -v bash returned an empty path" >&2; exit 1; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
# assert_not_contains — negative assertion, guarded against an empty needle
# (docs/shell-scripting-gotchas.md: an empty glob needle matches vacuously).
assert_not_contains() {
  [[ -n "$2" ]] || { fail "assert_not_contains: needle must not be empty ($3)"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3: expected output to NOT contain [$2], got [$1]"
}

SCRATCH="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 1; }
[[ -n "${SCRATCH}" && -d "${SCRATCH}" ]] || { echo "mktemp returned an unusable path" >&2; exit 1; }
trap 'rm -rf "${SCRATCH}"' EXIT

# The 17 question IDs, in configure-question order. They are persisted in
# users' configureOffered ledgers, so this list is a stable contract.
ALL_IDS='["stack","isMonorepo","branchPattern","mcpServers","uiConventions","playwrightCli","lspServers","autoCompactDisabled","pinSubagents200K","cicd","sandbox.enabled","sandbox.dind","sandbox.azure","lazyboards","dispatchEnroll","planning","automerge"]'
CONDITIONAL_IDS='["uiConventions","playwrightCli","lspServers","lazyboards","dispatchEnroll"]'
KEYLESS_IDS='["uiConventions","dispatchEnroll"]'

# A fully answered config: every unconditional key present (several with
# falsy values on purpose: false, {} and "" must count as present), and every
# conditional/keyless ID recorded in the ledger. The resolver must ask nothing.
COMPLETE='{
  "configVersion": "9.9.9",
  "stack": {"backend": "dotnet10"},
  "isMonorepo": false,
  "branchPattern": "",
  "mcpServers": {},
  "autoCompactDisabled": false,
  "pinSubagents200K": false,
  "cicd": {"enabled": false},
  "sandbox": {"enabled": false, "dind": false, "azure": false},
  "planning": {"autonomy": "interactive"},
  "automerge": {"enabled": false},
  "configureOffered": ["uiConventions","playwrightCli","lspServers","lazyboards","dispatchEnroll"]
}'

# write_cfg <name> <json> — writes SCRATCH/<name>.json, prints its path.
write_cfg() {
  printf '%s' "$2" > "${SCRATCH}/$1.json"
  printf '%s' "${SCRATCH}/$1.json"
}

# jv <jq-expr> — evaluates a jq expression against the last run's stdout.
jv() { printf '%s' "${OUT}" | jq -c "$1" 2>/dev/null; }

# run_resolver_at <resolver-path> <args...> — sets OUT (stdout), ERR (stderr), CODE.
# run_resolver <args...> is the same against the real resolver.
run_resolver_at() {
  local script="$1"; shift
  OUT="$("${BASH_BIN}" "${script}" "$@" 2>"${SCRATCH}/stderr")"
  CODE=$?
  ERR="$(cat "${SCRATCH}/stderr")"
}
run_resolver() { run_resolver_at "${RESOLVER}" "$@"; }

# run_resolver_stdin <json> <args...> — config piped on stdin via "-".
run_resolver_stdin() {
  local json="$1"; shift
  OUT="$(printf '%s' "${json}" | "${BASH_BIN}" "${RESOLVER}" "$@" 2>"${SCRATCH}/stderr")"
  CODE=$?
  ERR="$(cat "${SCRATCH}/stderr")"
}

# assert_fail_closed <label> — the last run must be exit 2, empty stdout, and
# carry a diagnostic on stderr (AC12).
assert_fail_closed() {
  assert_eq "${CODE}" "2" "$1 exit code"
  assert_eq "${OUT}" "" "$1 stdout must be empty on failure"
  [[ -n "${ERR}" ]] || fail "$1: expected a diagnostic on stderr, got none"
}

# make_copy <table-content|-> — copies the resolver into a fresh tree laid out
# like the plugin (skills/configure/scripts/ + skills/configure/question-keys.json)
# so a test can pair the real script with a custom table. "-" means no table.
# Sets COPY to the copied script path.
make_copy() {
  local dir
  dir="$(mktemp -d -p "${SCRATCH}")" || { echo "mktemp failed" >&2; exit 1; }
  mkdir -p "${dir}/skills/configure/scripts" || { echo "mkdir failed" >&2; exit 1; }
  cp "${RESOLVER}" "${dir}/skills/configure/scripts/resolve-delta.sh" 2>/dev/null \
    || fail "make_copy: cannot copy ${RESOLVER}"
  if [[ "$1" != "-" ]]; then
    printf '%s' "$1" > "${dir}/skills/configure/question-keys.json"
  fi
  COPY="${dir}/skills/configure/scripts/resolve-delta.sh"
}

# =====================================================================
# Table contract (question-keys.json)
# =====================================================================
tj() { jq -c "$1" "${TABLE}" 2>/dev/null; }

assert_eq "$(tj '[.questions[].id]')" "${ALL_IDS}" "table: the 17 IDs in configure-question order"
assert_eq "$(tj 'type')" '"object"' "table: top level is an object"
assert_eq "$(tj '(.description | type)')" '"string"' "table: carries a description string"
assert_eq "$(tj '[.questions[] | (.conditional | type)] | unique')" '["boolean"]' "table: every conditional flag is a boolean"
assert_eq "$(tj '[.questions[] | (.keys | type)] | unique')" '["array"]' "table: every keys value is an array"
assert_eq "$(tj '[.questions[] | select(.conditional == false)] | all(.keys == [.id])')" 'true' \
  "table: every unconditional row has exactly one key, equal to its ID"
assert_eq "$(tj '[.questions[] | select(.conditional) | .id] | sort')" \
  "$(printf '%s' "${CONDITIONAL_IDS}" | jq -c 'sort')" "table: conditional set"
assert_eq "$(tj '[.questions[] | select(.keys == []) | .id] | sort')" \
  "$(printf '%s' "${KEYLESS_IDS}" | jq -c 'sort')" "table: keyless set"
assert_eq "$(tj '[.questions[] | select(.projectsAware == true) | .id]')" '["stack","automerge"]' \
  "table: only stack and automerge look inside projects[]"
assert_eq "$(tj '[.questions[].id] | (unique | length) == length')" 'true' "table: IDs are unique"

# =====================================================================
# AC10: ask / carried / missingKeys / conditional on real fixtures
# =====================================================================

# --- 10a: complete config -> zero questions ---------------------------
CFG="$(write_cfg complete "${COMPLETE}")"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "10a exit"
assert_eq "${ERR}" "" "10a stderr silent on success"
assert_eq "$(jv '.ask')" '[]' "10a complete config asks nothing"
assert_eq "$(jv '.missingKeys')" '[]' "10a no missing keys"
assert_eq "$(jv '.conditional')" '[]' "10a no conditional questions"
assert_eq "$(jv '.carried')" "${ALL_IDS}" "10a every row is carried, in table order"
assert_eq "$(jv 'keys_unsorted')" '["ask","carried","missingKeys","conditional"]' "10a output field order"
assert_eq "$(printf '%s\n' "${OUT}" | wc -l | tr -d '[:space:]')" "1" "10a output is a single line"

# --- 10b: one key missing -> exactly that question --------------------
CFG="$(write_cfg no-planning "$(printf '%s' "${COMPLETE}" | jq 'del(.planning)')")"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "10b exit"
assert_eq "$(jv '.ask')" '["planning"]' "10b exactly the missing key's question"
assert_eq "$(jv '.missingKeys')" '["planning"]' "10b the missing key is named"
assert_eq "$(jv '.conditional')" '[]' "10b planning is not conditional"
assert_eq "$(jv '.carried | length')" "16" "10b the other 16 rows are carried"
assert_eq "$(jv '.carried | index("planning")')" 'null' "10b planning is not carried"

# --- 10c: missing key whose ID is in the ledger -> zero questions -----
CFG="$(write_cfg no-planning-ledgered "$(printf '%s' "${COMPLETE}" | jq 'del(.planning) | .configureOffered += ["planning"]')")"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "10c exit"
assert_eq "$(jv '.ask')" '[]' "10c a ledgered (declined) question is not asked again"
assert_eq "$(jv '.missingKeys')" '[]' "10c a ledgered question reports no missing key"
assert_eq "$(jv '.carried | index("planning") != null')" 'true' "10c planning moves to carried"

# --- 10d: --all -> every question -------------------------------------
CFG="$(write_cfg complete-all "${COMPLETE}")"
run_resolver "${CFG}" --all
assert_eq "${CODE}" "0" "10d exit"
assert_eq "$(jv '.ask')" "${ALL_IDS}" "10d --all asks every table ID in order"
assert_eq "$(jv '.carried')" '[]' "10d --all carries nothing"
assert_eq "$(jv '.conditional')" "${CONDITIONAL_IDS}" "10d --all flags the conditional rows"
# --all asks the conditional rows too, so their (detection-dependent) keys may
# be reported missing; but an answered unconditional key never is.
assert_eq "$(jv '[.missingKeys[] | select(IN("stack","isMonorepo","branchPattern","mcpServers","autoCompactDisabled","pinSubagents200K","cicd","sandbox.enabled","sandbox.dind","sandbox.azure","planning","automerge"))]')" '[]' \
  "10d --all does not report answered unconditional keys as missing"

run_resolver "$(write_cfg no-planning-all "$(printf '%s' "${COMPLETE}" | jq 'del(.planning)')")" --all
assert_eq "$(jv '.missingKeys | index("planning") != null')" 'true' "10d --all still reports an absent key as missing"

# --- 10e: automerge only under projects[] counts as present -----------
CFG="$(write_cfg automerge-in-projects "$(printf '%s' "${COMPLETE}" | jq 'del(.automerge) | .projects = [{"slug":"a","automerge":{"enabled":true}}]')")"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "10e exit"
assert_eq "$(jv '.ask')" '[]' "10e automerge in projects[0] is not asked"
assert_eq "$(jv '.missingKeys')" '[]' "10e automerge in projects[0] is not a missing key"
assert_eq "$(jv '.carried | index("automerge") != null')" 'true' "10e automerge is carried"

# automerge absent from every projects[] entry AND the top level is still asked
CFG="$(write_cfg automerge-nowhere "$(printf '%s' "${COMPLETE}" | jq 'del(.automerge) | .projects = [{"slug":"a"},{"slug":"b"}]')")"
run_resolver "${CFG}"
assert_eq "$(jv '.ask')" '["automerge"]' "10e automerge absent everywhere is asked"
assert_eq "$(jv '.missingKeys')" '["automerge"]' "10e automerge absent everywhere is a missing key"

# a later projects[] entry counts too, not only the first
CFG="$(write_cfg automerge-in-second-project "$(printf '%s' "${COMPLETE}" | jq 'del(.automerge) | .projects = [{"slug":"a"},{"slug":"b","automerge":{}}]')")"
run_resolver "${CFG}"
assert_eq "$(jv '.ask')" '[]' "10e automerge in any projects[] entry counts"

# --- stack is projects[]-aware too (monorepo schema) ------------------
CFG="$(write_cfg stack-in-projects "$(printf '%s' "${COMPLETE}" | jq 'del(.stack) | .projects = [{"slug":"a","stack":{"backend":"go"}}]')")"
run_resolver "${CFG}"
assert_eq "$(jv '.ask')" '[]' "stack only under projects[] is present"
assert_eq "$(jv '.missingKeys')" '[]' "stack only under projects[] is not missing"

# projectsAware is per-row: planning is NOT projects[]-aware
CFG="$(write_cfg planning-in-projects "$(printf '%s' "${COMPLETE}" | jq 'del(.planning) | .projects = [{"slug":"a","planning":{"autonomy":"lean"}}]')")"
run_resolver "${CFG}"
assert_eq "$(jv '.ask')" '["planning"]' "planning under projects[] does not count (not projectsAware)"

# --- null is absent; false / {} / "" are present ----------------------
CFG="$(write_cfg planning-null "$(printf '%s' "${COMPLETE}" | jq '.planning = null')")"
run_resolver "${CFG}"
assert_eq "$(jv '.ask')" '["planning"]' "a null key counts as absent"

# =====================================================================
# AC11: pre-ledger config -> each absent key asked exactly once
# =====================================================================
PRE_LEDGER='{
  "configVersion": "0.1.0",
  "stack": {"backend": "go"},
  "branchPattern": "",
  "autoCompactDisabled": false,
  "cicd": {"enabled": true},
  "sandbox": {"dind": false},
  "planning": {"autonomy": "lean"}
}'
CFG="$(write_cfg pre-ledger "${PRE_LEDGER}")"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "AC11 exit"
assert_eq "$(jv '.carried')" '["stack","branchPattern","autoCompactDisabled","cicd","sandbox.dind","planning"]' \
  "AC11 every present key (falsy ones included) is carried, in table order"
assert_eq "$(jv '.ask | (unique | length) == length')" 'true' "AC11 no duplicate question IDs"
assert_eq "$(jv '.ask | length')" "11" "AC11 the other 11 rows are asked"
assert_eq "$(jv '[.ask[], .carried[]] | sort')" "$(printf '%s' "${ALL_IDS}" | jq -c 'sort')" \
  "AC11 ask and carried partition the table"
assert_eq "$(jv '.ask | index("sandbox.enabled") != null and index("sandbox.azure") != null')" 'true' \
  "AC11 a partly answered sandbox object asks only its absent sub-keys"
assert_eq "$(jv '.ask | index("sandbox.dind")')" 'null' "AC11 sandbox.dind: false is present, not asked"
assert_eq "$(jv '[.missingKeys[] | select(. == "stack" or . == "branchPattern" or . == "autoCompactDisabled" or . == "cicd" or . == "sandbox.dind" or . == "planning")]')" '[]' \
  "AC11 present keys are never reported missing"
assert_eq "$(jv '[.ask[] | select(. == "isMonorepo" or . == "mcpServers" or . == "pinSubagents200K" or . == "sandbox.enabled" or . == "sandbox.azure" or . == "automerge")] | length')" "6" \
  "AC11 every absent unconditional question is asked"
assert_eq "$(jv '["isMonorepo","mcpServers","pinSubagents200K","sandbox.enabled","sandbox.azure","automerge"] - .missingKeys')" '[]' \
  "AC11 every absent unconditional key is reported missing"
assert_eq "$(jv '.ask | index("uiConventions") != null and index("dispatchEnroll") != null')" 'true' \
  "AC11 keyless questions are asked until they enter the ledger"
assert_eq "$(jv '[.missingKeys[] | select(. == "uiConventions" or . == "dispatchEnroll")]')" '[]' \
  "AC11 keyless questions contribute no missing key"
assert_eq "$(jv '.missingKeys | (unique | length) == length')" 'true' "AC11 no duplicate missing keys"

# --- conditional contains only conditional IDs, only from ask ---------
CFG="$(write_cfg empty-config '{}')"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "empty config exit"
assert_eq "$(jv '.ask')" "${ALL_IDS}" "empty config asks every question"
assert_eq "$(jv '.carried')" '[]' "empty config carries nothing"
assert_eq "$(jv '.conditional')" "${CONDITIONAL_IDS}" "empty config: conditional is the conditional rows, in table order"

CFG="$(write_cfg partly-ledgered '{"configureOffered":["uiConventions","dispatchEnroll"]}')"
run_resolver "${CFG}"
assert_eq "$(jv '.conditional')" '["playwrightCli","lspServers","lazyboards"]' \
  "conditional lists only ask IDs (ledgered conditional rows drop out)"
assert_eq "$(jv '.ask | length')" "15" "partly ledgered: 15 questions remain"

# --- ledger robustness -------------------------------------------------
CFG="$(write_cfg unknown-ledger-ids "$(printf '%s' "${COMPLETE}" | jq 'del(.planning) | .configureOffered += ["notAQuestion","planning"]')")"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "unknown ledger IDs exit"
assert_eq "$(jv '.ask')" '[]' "unknown ledger IDs are ignored, known ones still honoured"
assert_not_contains "${OUT}" "notAQuestion" "unknown ledger IDs never echoed"

# --- output carries identifiers only, never config values -------------
CFG="$(write_cfg secret-values "$(printf '%s' "${COMPLETE}" | jq '.branchPattern = "SECRET-VALUE-9f2c" | del(.planning)')")"
run_resolver "${CFG}"
assert_eq "${CODE}" "0" "secret-values exit"
assert_eq "$(jv 'any(.ask[]; . == "planning")')" "true" "secret-values: output names the missing planning id (non-vacuous)"
assert_not_contains "${OUT}" "SECRET-VALUE-9f2c" "config values never appear in the output"

# --- "-" reads the config from stdin, same result as a path -----------
NO_PLANNING_JSON="$(printf '%s' "${COMPLETE}" | jq 'del(.planning)')"
run_resolver "$(write_cfg stdin-equivalence "${NO_PLANNING_JSON}")"
FROM_PATH="${OUT}"
run_resolver_stdin "${NO_PLANNING_JSON}" -
assert_eq "${CODE}" "0" "stdin exit"
assert_eq "${OUT}" "${FROM_PATH}" "stdin output equals the path output"
assert_eq "$(jv '.ask')" '["planning"]' "stdin: the missing planning key is asked"
run_resolver_stdin '{}' - --all
assert_eq "$(jv '.ask')" "${ALL_IDS}" "stdin first-run '{}' with --all asks everything"

# --- table-driven: a custom table changes the answer ------------------
CUSTOM_TABLE="$(jq -c '.questions += [{"id":"extraQuestion","keys":["extraKey"],"conditional":false}]' "${TABLE}" 2>/dev/null)"
if [[ -z "${CUSTOM_TABLE}" ]]; then
  fail "custom-table fixture: could not derive a table from ${TABLE}"
else
  make_copy "${CUSTOM_TABLE}"
  run_resolver_at "${COPY}" "$(write_cfg complete-custom-table "${COMPLETE}")"
  assert_eq "${CODE}" "0" "custom table exit"
  assert_eq "$(jv '.ask')" '["extraQuestion"]' "a row added to the table is asked"
  assert_eq "$(jv '.missingKeys')" '["extraKey"]' "a row added to the table reports its key"
fi

# =====================================================================
# AC12: fail closed — exit 2 AND empty stdout
# =====================================================================

# --- no jq on PATH ------------------------------------------------------
STUB="${SCRATCH}/nojq-bin"
mkdir -p "${STUB}"
for tool in cat dirname basename mktemp rm; do
  ln -s "$(command -v "${tool}")" "${STUB}/${tool}"
done
if (PATH="${STUB}"; command -v jq >/dev/null 2>&1); then
  fail "AC12 no-jq fixture: jq is still reachable on the restricted PATH"
fi
CFG="$(write_cfg for-nojq "${COMPLETE}")"
OUT="$(PATH="${STUB}" "${BASH_BIN}" "${RESOLVER}" "${CFG}" 2>"${SCRATCH}/stderr")"
CODE=$?
ERR="$(cat "${SCRATCH}/stderr")"
assert_fail_closed "AC12 missing jq"
[[ "${ERR}" == *jq* ]] || fail "AC12 missing jq: diagnostic should name jq, got [${ERR}]"

# --- unreadable config: path that does not exist -----------------------
run_resolver "${SCRATCH}/does-not-exist.json"
assert_fail_closed "AC12 nonexistent config path"

# --- unreadable config: a directory (EISDIR; root cannot bypass it) ----
mkdir -p "${SCRATCH}/a-directory"
run_resolver "${SCRATCH}/a-directory"
assert_fail_closed "AC12 directory as config path"

# --- invalid JSON / empty / not an object ------------------------------
run_resolver "$(write_cfg invalid-json '{ "stack": ')"
assert_fail_closed "AC12 invalid JSON"
run_resolver "$(write_cfg empty-file '')"
assert_fail_closed "AC12 empty config file"
run_resolver "$(write_cfg json-null 'null')"
assert_fail_closed "AC12 config is null"
run_resolver "$(write_cfg json-array '[]')"
assert_fail_closed "AC12 config is an array"
run_resolver "$(write_cfg json-string '"stack"')"
assert_fail_closed "AC12 config is a string"
run_resolver "$(write_cfg json-number '3')"
assert_fail_closed "AC12 config is a number"
run_resolver_stdin '' -
assert_fail_closed "AC12 empty stdin"
run_resolver_stdin '{ not json' -
assert_fail_closed "AC12 invalid JSON on stdin"

# --- malformed configureOffered ----------------------------------------
for bad in '"planning"' '{}' '[1,2]' '["planning",3]' '[["planning"]]' '7' 'true'; do
  BAD_LEDGER_CFG="$(printf '%s' "${COMPLETE}" | jq -c --argjson v "${bad}" '.configureOffered = $v' 2>/dev/null)"
  if [[ -z "${BAD_LEDGER_CFG}" ]]; then
    fail "AC12 bad-ledger fixture [${bad}]: could not derive a config from COMPLETE"
    continue
  fi
  CFG="$(write_cfg bad-ledger "${BAD_LEDGER_CFG}")"
  run_resolver "${CFG}"
  assert_fail_closed "AC12 configureOffered=${bad}"
  [[ "${ERR}" == *configureOffered* ]] \
    || fail "AC12 configureOffered=${bad}: diagnostic should name configureOffered, got [${ERR}]"
done

# --- missing or invalid table ------------------------------------------
make_copy "-"
run_resolver_at "${COPY}" "$(write_cfg for-missing-table "${COMPLETE}")"
assert_fail_closed "AC12 missing table"

make_copy '{ "questions": [ '
run_resolver_at "${COPY}" "$(write_cfg for-truncated-table "${COMPLETE}")"
assert_fail_closed "AC12 table is invalid JSON"

make_copy '[]'
run_resolver_at "${COPY}" "$(write_cfg for-array-table "${COMPLETE}")"
assert_fail_closed "AC12 table is not an object"

make_copy '{"description":"x"}'
run_resolver_at "${COPY}" "$(write_cfg for-no-questions "${COMPLETE}")"
assert_fail_closed "AC12 table has no questions array"

BAD_TABLES=(
  '.questions[1].id = .questions[0].id'
  '.questions[0].conditional = "yes"'
  'del(.questions[0].conditional)'
  '.questions[0].keys = "stack"'
  'del(.questions[0].keys)'
  '.questions[0].keys = [1]'
  '.questions[0].id = 5'
  '.questions[0].id = ""'
)
for filter in "${BAD_TABLES[@]}"; do
  BAD="$(jq -c "${filter}" "${TABLE}" 2>/dev/null)"
  if [[ -z "${BAD}" ]]; then
    fail "AC12 invalid-table fixture [${filter}]: could not derive from ${TABLE}"
    continue
  fi
  make_copy "${BAD}"
  run_resolver_at "${COPY}" "$(write_cfg for-bad-table "${COMPLETE}")"
  assert_fail_closed "AC12 invalid table [${filter}]"
done

# Non-vacuity: the copy mechanism itself works with the untouched real table,
# so the failures above are caused by the table, not by the copy.
GOOD="$(jq -c '.' "${TABLE}" 2>/dev/null)"
if [[ -z "${GOOD}" ]]; then
  fail "AC12 non-vacuity: could not read ${TABLE}"
else
  make_copy "${GOOD}"
  run_resolver_at "${COPY}" "$(write_cfg for-good-table "${COMPLETE}")"
  assert_eq "${CODE}" "0" "AC12 non-vacuity: a copied script with the real table succeeds"
  assert_eq "$(jv '.ask')" '[]' "AC12 non-vacuity: copied script answers like the original"
fi

# --- argument errors -----------------------------------------------------
run_resolver
assert_fail_closed "AC12 no arguments"
CFG="$(write_cfg for-args "${COMPLETE}")"
run_resolver "${CFG}" --bogus
assert_fail_closed "AC12 unknown flag"
run_resolver "${CFG}" extra-positional
assert_fail_closed "AC12 extra positional argument"
run_resolver "${CFG}" --all extra-positional
assert_fail_closed "AC12 extra positional after --all"
run_resolver "${CFG}" --all --bogus
assert_fail_closed "AC12 unknown flag after --all"
run_resolver --all
assert_fail_closed "AC12 --all without a config path"

echo "resolve-delta.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
