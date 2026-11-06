#!/usr/bin/env bash
# Semantics self-test for every dorny/paths-filter step (ticket #956).
#
# check-paths-filters.sh checks the STRUCTURE of those steps; this script and
# paths-filter-cases.yml make the REAL pinned action prove what the filters
# match. workflow-lint.yml runs three jobs around it:
#
#   generate  Builds the job matrix. Discovers every dorny/paths-filter step
#             under .github/workflows/ (the self-test's own step excluded),
#             validates .github/scripts/paths-filter-cases.yml against them
#             and prints {"include":[...]} to stdout -- one entry per case.
#             Every failure goes to stderr, nothing goes to stdout, exit 1.
#             Run from the repository root.
#   setup     Matrix job step 1 (env: CASE_JSON = the matrix entry). Builds a
#             scratch git repo under $GITHUB_WORKSPACE: an empty base commit
#             and a head commit adding every listed path, plus the step's
#             filters document as a file. Writes working_directory, base_sha,
#             head_sha and filters_file to $GITHUB_OUTPUT.
#   assert    Matrix job step 3 (env: CASE_JSON, ACTUAL_JSON = the action's
#             step outputs). Fails, naming workflow, job, step, case and key,
#             when an output differs from the expectation. A key that is
#             missing or empty fails; it is never read as 'false'.
#
# Coverage rule (the executable signal for the `every`-over-a-pure-OR-list
# footgun that check-paths-filters.sh cannot detect statically): every filter
# output key of every discovered step needs at least one case expecting
# 'true' and one expecting 'false'. A gate that can never be true cannot pass
# its positive case; the #950 always-true negation gate cannot pass its
# negative one.
#
# The scripts never model picomatch. They only assemble changesets and compare
# what the real action reports with what the cases file states.
#
# GitHub Actions' default `run:` shell has errexit (-e) ON; `set +e` below
# disables it so failures accumulate and one run reports every offender.
# Every yq/jq call is guarded -- see docs/github-actions.md.
set -uo pipefail
set +e

WORKFLOWS_DIR=".github/workflows"
CASES_FILE=".github/scripts/paths-filter-cases.yml"
SELFTEST_WORKFLOW="workflow-lint.yml"
SELFTEST_JOB="paths-filter-semantics-run"
SELFTEST_STEP="semantics"
MATRIX_LIMIT=256

FAILED=0

fail() {
  echo "FAIL: $*" >&2
  FAILED=1
}

die() {
  echo "FAIL: $*" >&2
  exit 1
}

require_jq() {
  command -v jq >/dev/null 2>&1 || die "jq is required but was not found on PATH"
}

require_yq() {
  command -v yq >/dev/null 2>&1 || die "yq is required but was not found on PATH"
  local version
  version=$(yq --version 2>&1) || die "yq --version failed to execute"
  [[ "$version" == *mikefarah* ]] || die "yq must be mikefarah/yq (Go-yq); found: $version"
}

# jq definitions shared by generate (case validation) and setup (defense in
# depth before anything is written). path_problem names why a changed path is
# unsafe; it emits nothing for a safe one.
JQ_DEFS=$(cat <<'EOF'
def path_problem:
  if type != "string" then "is not a string"
  elif . == "" then "is empty"
  elif startswith("/") then "starts with '/'"
  elif endswith("/") then "ends with '/'"
  elif contains("//") then "contains '//'"
  elif contains("..") then "contains '..'"
  elif (split("/") | any(. == ".")) then "has a '.' segment"
  elif (split("/") | any(. == ".git")) then "has a .git segment"
  elif (explode | any(. < 32 or . == 127)) then "contains a control character"
  else empty end;

# A path that is also a directory of another path cannot be materialised as a
# file tree, so it is rejected up front rather than failing inside setup.
def paths_problems($label):
  if (.paths | type) != "array" or (.paths | length) == 0
  then ["\($label): paths must be a non-empty list of changed paths"]
  else
    [.paths[] | . as $p | path_problem as $why | "\($label): changed path \($p | @json) \($why)"] as $each
    | if ($each | length) > 0 then $each
      else .paths as $all
        | [$all[] | select(. as $p | $all | any(startswith($p + "/")))
           | "\($label): changed path \(@json) is also a directory of another changed path"]
      end
  end;
EOF
)

# Validates the whole cases file against the discovered steps. Input: the
# cases file as JSON. Output: {errors: [...], include: [matrix entries]}.
JQ_CASES=$(cat <<'EOF'
def case_label:
  if (.c | type) == "object" and (.c.name | type) == "string" and (.c.name | test("^[A-Za-z0-9._-]+$"))
  then "case '\(.c.name)'" else "case #\(.i)" end;

def evaluate($steps; $st):
  . as $x
  | ($x | case_label) as $L
  | $x.c as $c
  | if ($c | type) != "object" then {errors: ["\($L): must be a mapping"], entry: null, target: null}
    else
      ( [ if ($c.name | type) != "string" or (($c.name | test("^[A-Za-z0-9._-]+$")) | not)
          then "\($L): name must match ^[A-Za-z0-9._-]+$" else empty end ]
        + [ ("workflow", "job", "step") as $k
            | if ($c[$k] | type) != "string" or $c[$k] == ""
              then "\($L): \($k) must be a non-empty string" else empty end ] ) as $basic
      | if ($basic | length) > 0 then {errors: $basic, entry: null, target: null}
        elif ($c.workflow == $st.workflow and $c.job == $st.job and $c.step == $st.step)
        then {errors: ["\($L): targets the self-test step (\($st.workflow) job \($st.job) step \($st.step)), which is excluded from discovery and takes no cases"], entry: null, target: null}
        else
          ($steps | map(select(.workflow == $c.workflow and .job == $c.job and .step == $c.step)) | .[0]) as $s
          | if $s == null then
              { errors: ["\($L): stale case, "
                  + (if ($steps | any(.workflow == $c.workflow) | not)
                     then "workflow '\($c.workflow)' does not exist or defines no dorny/paths-filter step"
                     elif ($steps | any(.workflow == $c.workflow and .job == $c.job) | not)
                     then "job '\($c.job)' of workflow '\($c.workflow)' has no dorny/paths-filter step"
                     else "step id '\($c.step)' not found in job '\($c.job)' of workflow '\($c.workflow)'" end)],
                entry: null, target: null }
            else
              ($c | paths_problems($L)) as $pp
              | ( if ($c.expect | type) != "object"
                  then ["\($L): expect must be a mapping of filter key to 'true' or 'false'"]
                  else
                    [ $c.expect | to_entries[]
                      | select((.value | type) != "string" or (.value != "true" and .value != "false"))
                      | "\($L): expectation for key '\(.key)' must be the quoted string 'true' or 'false' (quote it in YAML; got \(.value | @json))" ]
                    + [ (($c.expect | keys_unsorted) - $s.keys)[]
                        | "\($L): expects key '\(.)' which step '\($c.step)' does not define" ]
                    + [ ($s.keys - ($c.expect | keys_unsorted))[]
                        | "\($L): missing expectation for key '\(.)' of step '\($c.step)'" ]
                  end ) as $ep
              | ($pp + $ep) as $errs
              | { errors: $errs,
                  target: {workflow: $c.workflow, job: $c.job, step: $c.step},
                  entry: (if ($errs | length) == 0
                          then {case: $c.name, workflow: $c.workflow, job: $c.job, step: $c.step,
                                quantifier: $s.quantifier, filters: $s.filters,
                                paths: $c.paths, expected: $c.expect}
                          else null end) }
            end
        end
    end;

.cases as $cases
| if ($cases | type) != "array" or ($cases | length) == 0
  then {errors: ["cases: the top-level 'cases' key must be a non-empty list"], include: []}
  else
    [ range(0; $cases | length) as $i | {i: $i, c: $cases[$i]} | evaluate($steps; $st) ] as $res
    | [ $res[].entry | select(. != null) ] as $valid
    | [ $res[].target | select(. != null) ] as $targets
    | ( [ $res[].errors[] ]
        + [ $cases | map(select(type == "object" and (.name | type) == "string") | .name)
            | group_by(.)[] | select(length > 1) | "case '\(.[0])': duplicate case name" ]
        + [ $steps[] | . as $s
            | select(($targets | any(.workflow == $s.workflow and .job == $s.job and .step == $s.step)) | not)
            | "\($s.workflow): job '\($s.job)' step '\($s.step)': no case covers this step" ]
        + [ $steps[] | . as $s
            | select($targets | any(.workflow == $s.workflow and .job == $s.job and .step == $s.step))
            | $s.keys[] as $k
            | ("true", "false") as $v
            | select(([$valid[] | select(.workflow == $s.workflow and .job == $s.job and .step == $s.step and .expected[$k] == $v)] | length) == 0)
            | "\($s.workflow): job '\($s.job)' step '\($s.step)': key '\($k)' has no case expecting '\($v)'" ]
        + [ if ($cases | length) > $limit
            then "matrix would have \($cases | length) entries, exceeding GitHub's limit of \($limit) jobs per matrix"
            else empty end ] ) as $errors
    | {errors: $errors, include: $valid}
  end
EOF
)

# jget <json> <jq filter> <context> -- result in JGET; failure is reported
# in the current shell so FAILED survives.
jget() {
  JGET=$(jq -r "$2" <<<"$1") || {
    fail "$3: jq failed evaluating '$2'"
    return 1
  }
}

# ── generate ─────────────────────────────────────────────────────────────

# Resolves the filters: value of one step to its YAML document, mirroring
# check-paths-filters.sh rule 3: a single-line value ending in .yml/.yaml is a
# repo-root-relative external file. Result in FILTERS_DOC.
resolve_filters() {
  local raw="$1" where="$2"
  FILTERS_DOC=""
  if [ -z "$raw" ]; then
    fail "$where: the step has no filters: value"
    return 1
  fi
  if [[ "$raw" != *$'\n'* ]] && [[ "$raw" =~ \.(yml|yaml)$ ]]; then
    case "$raw" in
      /*|*..*)
        fail "$where: filters: path '${raw}' must be repo-root-relative (no leading '/' and no '..')"
        return 1
        ;;
    esac
    if [ ! -r "$raw" ]; then
      fail "$where: filters: references external file '${raw}' which is missing or unreadable"
      return 1
    fi
    FILTERS_DOC=$(cat "$raw") || {
      fail "$where: failed to read external filters file '${raw}'"
      return 1
    }
  else
    FILTERS_DOC="$raw"
  fi
}

# discover_file <file> -- appends the file's steps to STEPS_JSON.
discover_file() {
  local f="$1" base steps rows row where
  base=$(basename "$f")

  steps=$(yq -o=json -I=0 '[(.jobs // {}) | to_entries[] | .key as $job | ((.value.steps // []) | to_entries[]) | select((.value.uses // "") | test("^dorny/paths-filter(@|$)")) | {"job": $job, "index": .key, "id": (.value.id // ""), "uses": .value.uses, "quantifier": (.value.with."predicate-quantifier" // "some"), "filters": (.value.with.filters // "")}]' "$f") || {
    fail "$f: yq failed to discover dorny/paths-filter steps"
    return
  }
  rows=$(jq -c '.[]' <<<"$steps") || {
    fail "$f: jq failed to read the discovered steps"
    return
  }
  [ -n "$rows" ] || return

  while IFS= read -r row; do
    [ -n "$row" ] || continue
    local job index id uses quantifier filters_raw
    jget "$row" '.job' "$f" || continue
    job="$JGET"
    jget "$row" '.index' "$f job '$job'" || continue
    index="$JGET"
    where="$f: job '$job' step $index"
    jget "$row" '.id | tostring' "$where" || continue
    id="$JGET"

    if [ -z "$id" ]; then
      fail "$where: dorny/paths-filter step has no id: -- every step needs an id so cases can name it"
      continue
    fi
    if [ "$base" = "$SELFTEST_WORKFLOW" ] && [ "$job" = "$SELFTEST_JOB" ] && [ "$id" = "$SELFTEST_STEP" ]; then
      continue
    fi
    DISCOVERED=$((DISCOVERED + 1))
    where="$f: job '$job' step '$id'"

    jget "$row" '.uses' "$where" || continue
    uses="$JGET"
    jget "$row" '.quantifier | tostring' "$where" || continue
    quantifier="$JGET"
    jget "$row" 'if (.filters | type) == "string" then .filters else error("filters must be a string") end' "$where" || continue
    filters_raw="$JGET"

    if [ -n "$PINNED" ] && [ "$uses" != "$PINNED" ]; then
      fail "$where: pin drift -- uses '${uses}' but the self-test step in ${WORKFLOWS_DIR}/${SELFTEST_WORKFLOW} pins '${PINNED}' (workflow ${base})"
    fi

    resolve_filters "$filters_raw" "$where" || continue

    local doc_json keys_json rec
    doc_json=$(printf '%s' "$FILTERS_DOC" | yq -o=json -I=0 '.' -) || {
      fail "$where: filters: value failed to parse as YAML"
      continue
    }
    keys_json=$(jq -c 'if type == "object" and length > 0 then keys_unsorted else error("filters must be a non-empty mapping") end' <<<"$doc_json") || {
      fail "$where: filters: must be a non-empty mapping of filter name to patterns"
      continue
    }
    rec=$(jq -nc --arg w "$base" --arg j "$job" --arg s "$id" --arg q "$quantifier" --arg f "$FILTERS_DOC" --argjson k "$keys_json" \
      '{workflow: $w, job: $j, step: $s, quantifier: $q, filters: $f, keys: $k}') || {
      fail "$where: jq failed to record the step"
      continue
    }
    STEPS_JSON=$(jq -c --argjson s "$rec" '. + [$s]' <<<"$STEPS_JSON") || {
      fail "$where: jq failed to append the step"
      continue
    }
  done <<<"$rows"
}

generate() {
  require_jq
  require_yq

  if [ ! -d "$WORKFLOWS_DIR" ]; then
    die "$WORKFLOWS_DIR does not exist; no dorny/paths-filter steps discovered"
  fi

  DISCOVERED=0
  STEPS_JSON='[]'
  PINNED=""

  local selftest_file="${WORKFLOWS_DIR}/${SELFTEST_WORKFLOW}"
  if [ ! -r "$selftest_file" ]; then
    fail "$selftest_file is missing or unreadable; cannot read the pinned ref of the self-test step"
  else
    PINNED=$(job="$SELFTEST_JOB" sid="$SELFTEST_STEP" yq '[((.jobs[strenv(job)].steps // [])[] | select(.id == strenv(sid)) | .uses)] | .[0] // ""' "$selftest_file") || {
      fail "$selftest_file: yq failed to read the self-test step"
      PINNED=""
    }
    if [ -z "$PINNED" ]; then
      fail "$selftest_file: self-test step (job '${SELFTEST_JOB}', id '${SELFTEST_STEP}') not found; its uses: ref is the pin every other step is compared with"
    fi
  fi

  local f
  for f in "$WORKFLOWS_DIR"/*.yml "$WORKFLOWS_DIR"/*.yaml; do
    [ -f "$f" ] || continue
    discover_file "$f"
  done

  if [ "$DISCOVERED" -eq 0 ]; then
    fail "no dorny/paths-filter steps discovered across ${WORKFLOWS_DIR} -- check for a yq query or path typo"
  fi

  local cases_json="" cases_failed=0
  if [ ! -r "$CASES_FILE" ]; then
    fail "$CASES_FILE is missing or unreadable"
  else
    cases_json=$(yq -o=json -I=0 '.' "$CASES_FILE") || {
      fail "$CASES_FILE: yq failed to parse the cases file"
      cases_json=""
      cases_failed=1
    }
    # An empty or comments-only file makes yq print `null` (or, depending on
    # the yq version, nothing) and exit 0; that must not read as "nothing to
    # validate".
    if { [ -z "$cases_json" ] || [ "$cases_json" = "null" ]; } && [ "$cases_failed" -eq 0 ]; then
      fail "$CASES_FILE: parsed to empty output (the file is empty or holds only comments)"
      cases_json=""
    fi
  fi

  local matrix_printed=0
  if [ -n "$cases_json" ]; then
    local selftest_json result line
    selftest_json=$(jq -nc --arg w "$SELFTEST_WORKFLOW" --arg j "$SELFTEST_JOB" --arg s "$SELFTEST_STEP" '{workflow: $w, job: $j, step: $s}') || die "jq failed to build the self-test coordinates"
    result=$(jq -c --argjson steps "$STEPS_JSON" --argjson st "$selftest_json" --argjson limit "$MATRIX_LIMIT" "${JQ_DEFS}"$'\n'"${JQ_CASES}" <<<"$cases_json") || {
      fail "$CASES_FILE: jq failed to validate the cases"
      result=""
    }
    if [ -n "$result" ]; then
      local errors
      errors=$(jq -r '.errors[]' <<<"$result") || fail "$CASES_FILE: jq failed to read the validation result"
      while IFS= read -r line; do
        [ -n "$line" ] || continue
        fail "$line"
      done <<<"$errors"

      if [ "$FAILED" -eq 0 ]; then
        if jq -c '{include: .include}' <<<"$result"; then
          matrix_printed=1
        else
          fail "jq failed to print the matrix"
        fi
      fi
    fi
  fi

  # Never exit 0 without a matrix on stdout: an empty $GITHUB_OUTPUT value
  # would otherwise reach fromJSON as a green generator.
  if [ "$FAILED" -eq 0 ] && [ "$matrix_printed" -eq 0 ]; then
    fail "generate produced no matrix"
  fi

  if [ "$FAILED" -ne 0 ]; then
    echo "paths-filter-semantics generate: FAILED" >&2
    exit 1
  fi
}

# ── setup ────────────────────────────────────────────────────────────────

setup() {
  require_jq
  command -v git >/dev/null 2>&1 || die "git is required but was not found on PATH"

  [ -n "${CASE_JSON:-}" ] || die "CASE_JSON (the matrix entry) is not set"
  [ -n "${GITHUB_OUTPUT:-}" ] || die "GITHUB_OUTPUT is not set"
  [ -n "${GITHUB_WORKSPACE:-}" ] && [ -d "$GITHUB_WORKSPACE" ] || die "GITHUB_WORKSPACE is not set to an existing directory"

  # Re-validate before creating anything: defense in depth behind generate.
  local problems line
  problems=$(jq -r "${JQ_DEFS}"$'\n''paths_problems("CASE_JSON") | .[]' <<<"$CASE_JSON") || die "CASE_JSON is not a valid matrix entry"
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    fail "$line"
  done <<<"$problems"
  [ "$FAILED" -eq 0 ] || exit 1

  local filters count i p full
  filters=$(jq -r 'if (.filters | type) == "string" and .filters != "" then .filters else error("missing filters") end' <<<"$CASE_JSON") || die "CASE_JSON has no filters document"
  count=$(jq -r '.paths | length' <<<"$CASE_JSON") || die "CASE_JSON paths could not be counted"

  local root repo
  root=$(mktemp -d "${GITHUB_WORKSPACE}/.paths-filter-semantics.XXXXXX") || die "mktemp -d failed under ${GITHUB_WORKSPACE}"
  [ -n "$root" ] && [ -d "$root" ] || die "mktemp -d did not produce a directory"
  repo="${root}/repo"
  mkdir "$repo" || die "could not create ${repo}"

  local -a git_cmd=(git -C "$repo" -c user.name=paths-filter-semantics -c user.email=paths-filter-semantics@example.invalid -c commit.gpgsign=false)
  git -C "$repo" init -q -b pfs-head || die "git init failed in ${repo}"
  "${git_cmd[@]}" commit -q --allow-empty -m "paths-filter-semantics base" || die "base commit failed"

  local base_sha head_sha
  base_sha=$(git -C "$repo" rev-parse HEAD) || die "could not resolve the base commit"

  for ((i = 0; i < count; i++)); do
    p=$(jq -j ".paths[$i], \"x\"" <<<"$CASE_JSON") || die "could not read path ${i}"
    p="${p%x}"
    full="${repo}/${p}"
    mkdir -p "${full%/*}" || die "could not create the directory for '${p}'"
    printf '%s\n' "$p" > "$full" || die "could not write '${p}'"
  done
  git -C "$repo" add -A || die "git add failed"
  "${git_cmd[@]}" commit -q -m "paths-filter-semantics head" || die "head commit failed"
  head_sha=$(git -C "$repo" rev-parse HEAD) || die "could not resolve the head commit"

  # `git add -A` silently skips ignored paths; a changeset that lost a declared
  # path would let a negative case pass vacuously. Fail closed unless the
  # committed changeset is exactly the declared paths.
  local declared committed only_declared only_committed
  declared=$(jq -r '.paths[]' <<<"$CASE_JSON" | LC_ALL=C sort -u) || die "could not list the declared paths"
  committed=$(git -C "$repo" diff --name-only -z "$base_sha" "$head_sha" | tr '\0' '\n' | LC_ALL=C sort -u) || die "could not list the committed paths"
  if [ "$declared" != "$committed" ]; then
    only_declared=$(LC_ALL=C comm -23 <(printf '%s\n' "$declared") <(printf '%s\n' "$committed") | tr '\n' ' ')
    only_committed=$(LC_ALL=C comm -13 <(printf '%s\n' "$declared") <(printf '%s\n' "$committed") | tr '\n' ' ')
    die "the scratch repo's committed changeset differs from the declared paths (declared but not committed: [${only_declared% }]; committed but not declared: [${only_committed% }])"
  fi

  # Backstop: if the action fetches before resolving the local SHAs, the
  # fetch succeeds against the scratch repo itself.
  git -C "$repo" remote add origin "$repo" || die "could not add the origin remote"

  printf '%s\n' "$filters" > "${root}/filters.yml" || die "could not write the filters file"

  {
    printf 'working_directory=%s\n' "${root#"${GITHUB_WORKSPACE}"/}/repo"
    printf 'base_sha=%s\n' "$base_sha"
    printf 'head_sha=%s\n' "$head_sha"
    printf 'filters_file=%s\n' "${root}/filters.yml"
  } >> "$GITHUB_OUTPUT" || die "could not write to GITHUB_OUTPUT"
}

# ── assert ───────────────────────────────────────────────────────────────

assert_outputs() {
  require_jq
  [ -n "${CASE_JSON:-}" ] || die "CASE_JSON (the matrix entry) is not set"
  [ -n "${ACTUAL_JSON:-}" ] || die "ACTUAL_JSON (the action's step outputs) is not set"

  local workflow job step case_name keys key where state expected actual
  jget "$CASE_JSON" '.workflow' "CASE_JSON" || exit 1
  workflow="$JGET"
  jget "$CASE_JSON" '.job' "CASE_JSON" || exit 1
  job="$JGET"
  jget "$CASE_JSON" '.step' "CASE_JSON" || exit 1
  step="$JGET"
  jget "$CASE_JSON" '.case' "CASE_JSON" || exit 1
  case_name="$JGET"
  jget "$CASE_JSON" '.expected | keys_unsorted[]' "CASE_JSON" || exit 1
  keys="$JGET"
  jq -e 'type == "object"' >/dev/null <<<"$ACTUAL_JSON" || die "ACTUAL_JSON is not a JSON object"

  if [ -z "$keys" ]; then
    die "workflow '${workflow}' job '${job}' step '${step}' case '${case_name}': the case expects no filter keys"
  fi

  while IFS= read -r key; do
    [ -n "$key" ] || continue
    where="workflow '${workflow}' job '${job}' step '${step}' case '${case_name}' key '${key}'"

    state=$(jq -r --arg k "$key" 'if has($k) | not then "missing" elif (.[$k] == null or .[$k] == "") then "empty" else "present" end' <<<"$ACTUAL_JSON") || {
      fail "$where: jq failed to read the action output"
      continue
    }
    case "$state" in
      missing)
        fail "$where: output is missing from the action's outputs (a missing output is never treated as 'false')"
        continue
        ;;
      empty)
        fail "$where: output is empty (an empty output is never treated as 'false')"
        continue
        ;;
    esac

    actual=$(jq -r --arg k "$key" '.[$k] | tostring' <<<"$ACTUAL_JSON") || {
      fail "$where: jq failed to read the action output"
      continue
    }
    expected=$(jq -r --arg k "$key" '.expected[$k]' <<<"$CASE_JSON") || {
      fail "$where: jq failed to read the expectation"
      continue
    }
    if [ "$actual" != "$expected" ]; then
      fail "$where: expected '${expected}', got '${actual}'"
    fi
  done <<<"$keys"

  if [ "$FAILED" -ne 0 ]; then
    echo "paths-filter-semantics assert: FAILED" >&2
    exit 1
  fi
  echo "paths-filter-semantics assert: case '${case_name}' matched every expectation"
}

case "${1:-}" in
  generate) generate ;;
  setup) setup ;;
  assert) assert_outputs ;;
  *)
    echo "usage: paths-filter-semantics.sh generate|setup|assert" >&2
    exit 2
    ;;
esac
