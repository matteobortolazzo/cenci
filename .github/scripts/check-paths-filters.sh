#!/usr/bin/env bash
# Guards against the #950 paths-filter negation footgun: dorny/paths-filter
# combines patterns with the `some` quantifier (OR) by default, under which
# a `!`-prefixed pattern is an independent predicate meaning "any path that
# is not this one" -- it matches nearly every path in the repo rather than
# excluding anything. The exclusion idiom only works under step-level
# `predicate-quantifier: 'every'` (AND).
#
# Rules enforced:
#   1. Any dorny/paths-filter step whose filters: yields a pattern beginning
#      with '!' must set .with.predicate-quantifier == 'every'. An explicit
#      'some' fails identically to an absent predicate-quantifier. Failure
#      messages name the workflow file, the job key, the step index
#      (0-based), and the offending pattern.
#   2. Discovery guard: at least one dorny/paths-filter step must be
#      discovered across .github/workflows/, else FAIL -- mirrors
#      check-version-bump-concurrency.sh's rule 4, guarding against a query
#      or path typo silently passing with zero steps checked. Discovery
#      matches the action by name (uses: dorny/paths-filter) regardless of
#      whether the reference is pinned (@ref) -- an unpinned step is still
#      discovered and checked, though this guard does not itself enforce
#      pinning (a separate concern, out of scope here).
#   3. Fail closed: if a step's filters: value is a single-line string
#      ending in .yml/.yaml, it names a repo-root-relative external filters
#      file (a documented paths-filter input form) -- resolve and scan that
#      file; an unreadable file is a FAIL. If the inline filters: value
#      fails to parse as YAML, that is also a FAIL.
#
# Out of scope (see the #950 plan's Risks section): this guard does not
# check list-files, and it does not flag the inverse footgun
# (predicate-quantifier: 'every' on a pure OR list, which silently makes a
# gate permanently false) -- no false-positive-free formulation exists,
# since it would also flag legitimate overlapping-positive-pattern filters.
#
# Re-parses the nested filters: value with yq (all string scalars, keys and
# leaves, checked for a leading '!') rather than line-regexing it, so it
# also covers the change-type object form (`- added|modified: '!x/y.md'`)
# and fails closed on malformed YAML.
#
# GitHub Actions' default `run:` shell has errexit (-e) ON, which would
# abort this script at the first failed assertion before the rest of the
# workflows are checked -- `set +e` below disables that so failures
# accumulate and one run reports every offender.
set -uo pipefail
set +e

WORKFLOWS_DIR=".github/workflows"

FAILED=0
DISCOVERED=0

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

if [ ! -d "$WORKFLOWS_DIR" ]; then
  fail "rule 2 violated — $WORKFLOWS_DIR does not exist; no dorny/paths-filter steps discovered"
  exit 1
fi

# check_patterns <file> <job> <step> <quantifier> <doc-text>
# Re-parses <doc-text> (a filters: value, already resolved from either the
# inline block or an external filters file) as YAML and reports every
# '!'-prefixed string scalar found anywhere in it (map keys and leaf list
# items alike) as a rule 1 violation, unless <quantifier> is 'every'.
check_patterns() {
  local file="$1" job="$2" step="$3" quantifier="$4" doc="$5"

  local strings
  strings=$(printf '%s' "$doc" | yq e '.. | select(tag == "!!str")' - 2>/dev/null) || {
    fail "$file: job '$job' step $step: yq failed to re-parse the filters document for negation scanning"
    return
  }

  [ -n "$strings" ] || return

  local line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
      '!'*)
        if [ "$quantifier" != "every" ]; then
          fail "$file: job '$job' step $step: rule 1 violated — pattern '$line' begins with '!' but predicate-quantifier is '${quantifier}' (must be 'every' for negation to exclude rather than widen the filter to always-true)"
        fi
        ;;
    esac
  done <<< "$strings"
}

for f in "$WORKFLOWS_DIR"/*.yml "$WORKFLOWS_DIR"/*.yaml; do
  [ -f "$f" ] || continue

  coords=$(yq -o=tsv '.jobs // {} | to_entries[] | .key as $job | (.value.steps // []) | to_entries[] | select((.value.uses // "") | test("^dorny/paths-filter(@|$)")) | [$job, .key]' "$f") || {
    fail "$f: yq failed to discover dorny/paths-filter steps"
    continue
  }

  [ -n "$coords" ] || continue

  while IFS=$'\t' read -r job step; do
    [ -n "$job" ] || continue

    DISCOVERED=$((DISCOVERED + 1))

    quantifier=$(job="$job" step="$step" yq '.jobs[strenv(job)].steps[env(step)].with."predicate-quantifier" // "some"' "$f") || {
      fail "$f: job '$job' step $step: yq failed to read .with.predicate-quantifier"
      continue
    }

    filters_raw=$(job="$job" step="$step" yq '.jobs[strenv(job)].steps[env(step)].with.filters // ""' "$f") || {
      fail "$f: job '$job' step $step: yq failed to read .with.filters"
      continue
    }

    if [ -z "$filters_raw" ]; then
      continue
    fi

    # Rule 3: a single-line value ending in .yml/.yaml names an external,
    # repo-root-relative filters file (a documented paths-filter input
    # form) rather than inline YAML.
    if [[ "$filters_raw" != *$'\n'* ]] && [[ "$filters_raw" =~ \.(yml|yaml)$ ]]; then
      # Reject an absolute or traversing path before ever touching the
      # filesystem -- filters_raw comes from PR-authored workflow YAML, so
      # an unrejected '..' or leading '/' here is a bounded arbitrary-read
      # primitive on the runner.
      case "$filters_raw" in
        /*|*..*)
          fail "$f: job '$job' step $step: rule 3 violated — filters: path '${filters_raw}' must be repo-root-relative"
          continue
          ;;
      esac

      if [ ! -r "$filters_raw" ]; then
        fail "$f: job '$job' step $step: rule 3 violated — filters: references external file '${filters_raw}' which is missing or unreadable"
        continue
      fi
      filters_doc=$(cat "$filters_raw") || {
        fail "$f: job '$job' step $step: rule 3 violated — failed to read external filters file '${filters_raw}'"
        continue
      }
    else
      filters_doc="$filters_raw"
    fi

    # Rule 3: fail closed on malformed YAML.
    printf '%s' "$filters_doc" | yq e '.' - >/dev/null 2>&1 || {
      fail "$f: job '$job' step $step: rule 3 violated — filters: value failed to parse as YAML"
      continue
    }

    check_patterns "$f" "$job" "$step" "$quantifier" "$filters_doc"
  done <<< "$coords"
done

if [ "$DISCOVERED" -eq 0 ]; then
  fail "rule 2 violated — no dorny/paths-filter steps discovered across ${WORKFLOWS_DIR} — check for a yq query or path typo"
  exit 1
fi

echo "Discovered ${DISCOVERED} dorny/paths-filter step(s) under ${WORKFLOWS_DIR}."

if [ "$FAILED" -ne 0 ]; then
  echo "check-paths-filters: FAILED" >&2
  exit 1
fi

echo "check-paths-filters: all checks passed"
