#!/usr/bin/env bash
# run-checks.sh — shared, cwd-independent executor for flow's JSON validation
# plus *.test.sh discovery/execution (ticket #720). Both flow-ci.yml's
# flow-test job and .cenci/config.json's flow gateCommand invoke this single
# script, so "which tests run for flow" is written down in exactly one place
# instead of drifting across CI, the gate, and the maintain checker.
#
# Usage: run-checks.sh [flow-root]
#   flow-root defaults to the parent directory of this script (i.e. flow/,
#   when this script lives at flow/scripts/run-checks.sh). The optional
#   override argument exists ONLY for flow/scripts/run-checks.test.sh, which
#   must never invoke this script bare against the real flow tree -- see the
#   RECURSION WARNING at the top of that file. It always passes an explicit
#   mktemp -d fixture root instead.
#
# Behavior, in order:
#   1. JSON validation across the flow tree -- fails (non-zero exit, no
#      suite header printed) before any suite runs. Only regular files are
#      matched (find -type f), so a *.json symlink is not dereferenced.
#   1.5. Coverage-map sync check (ticket #916) -- only when this invocation's
#      resolved FLOW_ROOT is this script's own real flow root (never a
#      run-checks.test.sh fixture root), it verifies docs/pipeline-coverage-
#      map.md at the repo root: every mapped Go-test/flow-suite token
#      resolves (forward), every adversarial-suite Go test and both flow
#      adversarial suites are named somewhere in the map (backward), no
#      suite named in the map's "## Adversarial suite bounds" section is in
#      this script's own EXCLUDE array (AC4), and flow-ci.yml's flow filter
#      registers the map path and a watch/** test glob. Runs as its own
#      pseudo-suite ("=== coverage-map sync check ===" header) contributing
#      to failed/FAILED_SUITES but never to run, so the zero-suites
#      false-green guard below keeps its original meaning.
#   2. Discovery of every *.test.sh regular file (find -type f, so a
#      *.test.sh symlink pointing at an arbitrary script is never
#      discovered/executed) under the flow tree, in deterministic order
#      (sort -z), materialized into an array before executing anything.
#   3. Aggregate execution: every discovered, non-excluded suite runs; the
#      script never stops at the first failure. Before executing a suite,
#      it must be readable and non-empty ([[ -r && -s ]], mirroring
#      check_structural_tests) -- an unreadable or empty suite is counted
#      as a failed suite instead of silently passing as `bash <empty>`
#      would. A "=== <relative path> ===" header delimits each suite's
#      output. The run ends with a summary line and exits non-zero if any
#      suite failed, or if zero suites actually ran (false-green guard per
#      docs/health-gates.md's discovery-loop rule -- covers both "nothing
#      discovered" and "everything excluded").
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || {
  echo "run-checks.sh: failed to resolve script directory." >&2
  exit 2
}

RAW_FLOW_ROOT="${1:-}"
if [[ -z "${RAW_FLOW_ROOT}" ]]; then
  RAW_FLOW_ROOT="${SCRIPT_DIR}/.."
fi
FLOW_ROOT="$(cd "${RAW_FLOW_ROOT}" && pwd)" || {
  echo "run-checks.sh: failed to resolve flow root: ${RAW_FLOW_ROOT}" >&2
  exit 2
}

if ! command -v jq >/dev/null 2>&1; then
  echo "run-checks.sh: jq is required but was not found on PATH." >&2
  exit 2
fi

# --- 1. JSON validation ------------------------------------------------------
# Must fail before any suite header is printed (run-checks.test.sh case 4).
if ! find "${FLOW_ROOT}" -type f -name '*.json' -print0 | xargs -0 -r -n1 jq empty; then
  echo "run-checks.sh: JSON validation failed under ${FLOW_ROOT}." >&2
  exit 1
fi

# --- Exclude allowlist --------------------------------------------------------
# Stays empty. Add an entry ONLY for a suite that is environment-dependent
# (e.g. requires a container runtime unavailable here) or prohibitively slow
# for a fast local/CI gate -- mirroring the sandbox gate's tests/smoke.test.sh
# carve-out (that suite triggers a full container image build). Every future
# entry needs its own one-line rationale comment; excluded paths are reported
# as skipped below, never silently dropped. Defined before the coverage-map
# sync check below (AC4 needs is_excluded) and before discovery.
EXCLUDE=()

is_excluded() {
  local rel="$1" x
  for x in "${EXCLUDE[@]:-}"; do
    [[ -n "${x}" && "${rel}" == "${x}" ]] && return 0
  done
  return 1
}

# Aggregate counters -- defined before the coverage-map sync check so it can
# accumulate into failed/FAILED_SUITES (never into run; see the check's own
# comment below).
run=0
failed=0
skipped=0
FAILED_SUITES=()

# --- 1.5. Coverage-map sync check (ticket #916) ------------------------------
# Guarded on the real flow root: a run-checks.test.sh fixture invocation
# passes an explicit mktemp -d root whose resolved FLOW_ROOT never equals
# this script's own SCRIPT_DIR/.. UNLESS the fixture is a copy of this
# script placed at <fixture-root>/flow/scripts/run-checks.sh and invoked
# with <fixture-root>/flow as the explicit root -- exactly the idiom
# run-checks.test.sh cases 9-17 use, deliberately, to exercise this check
# against a repo-shaped fixture tree.
REAL_FLOW_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)" || {
  echo "run-checks.sh: failed to resolve real flow root for coverage-map sync check." >&2
  exit 2
}

# Always defined regardless of which branch below runs, so the EXIT trap's
# `rm -rf "${COV_TMPDIR:-}"` never acts on an inherited environment value.
COV_TMPDIR=""

if [[ "${FLOW_ROOT}" == "${REAL_FLOW_ROOT}" ]]; then
  echo "=== coverage-map sync check ==="
  cov_failed=0
  cov_fail() {
    echo "run-checks.sh: coverage-map sync check: $1" >&2
    cov_failed=$((cov_failed + 1))
  }

  COV_TMPDIR="$(mktemp -d)" || {
    echo "run-checks.sh: failed to create coverage-map sync check temp directory." >&2
    exit 2
  }
  # Merge into the existing LIST trap once LIST exists (below); until then,
  # an EXIT trap scoped to just this directory is safe to set now and will
  # be superseded by the combined trap set after LIST is created.
  trap 'rm -rf "${COV_TMPDIR}"' EXIT

  COV_REPO_ROOT="$(cd "${FLOW_ROOT}/.." && pwd)" || {
    cov_fail "failed to resolve repo root above flow root ${FLOW_ROOT}"
    COV_REPO_ROOT=""
  }

  if [[ -n "${COV_REPO_ROOT}" ]]; then
    MAP="${COV_REPO_ROOT}/docs/pipeline-coverage-map.md"

    if [[ ! -r "${MAP}" ]]; then
      cov_fail "coverage map is missing or unreadable: docs/pipeline-coverage-map.md"
    else
      # --- Path-component validation for extracted tokens ---------------------
      # Applied to every path component (from .test.sh tokens, .test.sh::literal
      # tokens, and .go-glob tokens) before it is ever treated as a file/glob to
      # read: narrow safe charset only, no ".." path segment (rejects traversal
      # regardless of charset shape), never absolute (no leading "/"), must end
      # in the expected suffix for its kind, and -- "go" kind only -- "*" may
      # appear solely in the final path segment (the chain*_test.go glob shape),
      # never in a directory component. Ticket #916 Phase 6/7 fix #4.
      cov_valid_path() {
        local p="$1" kind="$2" dirpart
        [[ "${p}" =~ ^[A-Za-z0-9._/*-]+$ ]] || return 1
        [[ "${p}" != /* ]] || return 1
        case "/${p}/" in
          */../*) return 1 ;;
        esac
        case "${kind}" in
          test.sh)
            [[ "${p}" == *.test.sh ]] || return 1
            [[ "${p}" != *'*'* ]] || return 1
            ;;
          go)
            [[ "${p}" == *.go ]] || return 1
            if [[ "${p}" == */* ]]; then
              dirpart="${p%/*}"
            else
              dirpart=""
            fi
            [[ "${dirpart}" != *'*'* ]] || return 1
            ;;
          *)
            return 1
            ;;
        esac
        return 0
      }

      # --- Token extraction: every inline-code span in the map -----------------
      grep -oE '`[^`]+`' "${MAP}" > "${COV_TMPDIR}/raw-tokens"
      grc=$?
      if [[ "${grc}" -gt 1 ]]; then
        cov_fail "failed to scan docs/pipeline-coverage-map.md for inline-code tokens (grep exit ${grc})"
      fi
      sed -e 's/^`//' -e 's/`$//' "${COV_TMPDIR}/raw-tokens" > "${COV_TMPDIR}/tokens"

      TOTAL_TOKENS="$(wc -l < "${COV_TMPDIR}/tokens" | tr -d '[:space:]')"
      if [[ "${TOTAL_TOKENS}" -eq 0 ]]; then
        cov_fail "zero inline-code tokens extracted from docs/pipeline-coverage-map.md (rows=0) -- fail closed, the map may be empty or malformed"
      else
        GO_TOKENS_FILE="${COV_TMPDIR}/go-tokens"
        SUITE_TOKENS_FILE="${COV_TMPDIR}/suite-tokens"
        LITERAL_TOKENS_FILE="${COV_TMPDIR}/literal-tokens"
        GO_LITERAL_TOKENS_FILE="${COV_TMPDIR}/go-literal-tokens"
        : > "${GO_TOKENS_FILE}"
        : > "${SUITE_TOKENS_FILE}"
        : > "${LITERAL_TOKENS_FILE}"
        : > "${GO_LITERAL_TOKENS_FILE}"

        while IFS= read -r tok || [[ -n "${tok}" ]]; do
          [[ -n "${tok}" ]] || continue
          if [[ "${tok}" =~ ^Test[A-Za-z0-9_]+$ ]]; then
            printf '%s\n' "${tok}" >> "${GO_TOKENS_FILE}"
          elif [[ "${tok}" == *".test.sh::"* ]]; then
            printf '%s\n' "${tok}" >> "${LITERAL_TOKENS_FILE}"
          elif [[ "${tok}" == *.test.sh ]]; then
            printf '%s\n' "${tok}" >> "${SUITE_TOKENS_FILE}"
          elif [[ "${tok}" == *".go::"* ]]; then
            printf '%s\n' "${tok}" >> "${GO_LITERAL_TOKENS_FILE}"
          fi
        done < "${COV_TMPDIR}/tokens"

        GO_COUNT="$(wc -l < "${GO_TOKENS_FILE}" | tr -d '[:space:]')"
        SUITE_COUNT="$(wc -l < "${SUITE_TOKENS_FILE}" | tr -d '[:space:]')"
        LITERAL_COUNT="$(wc -l < "${LITERAL_TOKENS_FILE}" | tr -d '[:space:]')"
        FLOW_COUNT=$((SUITE_COUNT + LITERAL_COUNT))

        if [[ "${GO_COUNT}" -eq 0 ]]; then
          cov_fail "zero Go-test tokens extracted from docs/pipeline-coverage-map.md -- fail closed"
        fi
        if [[ "${FLOW_COUNT}" -eq 0 ]]; then
          cov_fail "zero flow-suite tokens extracted from docs/pipeline-coverage-map.md -- fail closed"
        fi

        # --- Forward check: every Go-test token resolves -------------------
        WATCH_DIR="${COV_REPO_ROOT}/watch"
        GO_FUNCS_FILE="${COV_TMPDIR}/go-funcs"
        : > "${GO_FUNCS_FILE}"
        if [[ ! -d "${WATCH_DIR}" ]]; then
          cov_fail "watch directory not found at ${WATCH_DIR} -- cannot resolve any Go-test token"
        else
          GO_TEST_FILES_LIST="${COV_TMPDIR}/go-test-files"
          if ! find "${WATCH_DIR}" -type f -name '*_test.go' -print0 | sort -z > "${GO_TEST_FILES_LIST}"; then
            cov_fail "failed to enumerate watch/**/*_test.go files"
          else
            while IFS= read -r -d '' gf; do
              grep -ohE '^func Test[A-Za-z0-9_]+\(' "${gf}" 2>/dev/null \
                | sed -E 's/^func (Test[A-Za-z0-9_]+)\(.*/\1/' >> "${GO_FUNCS_FILE}"
            done < "${GO_TEST_FILES_LIST}"
          fi
        fi

        if [[ -s "${GO_TOKENS_FILE}" ]]; then
          while IFS= read -r gt || [[ -n "${gt}" ]]; do
            [[ -n "${gt}" ]] || continue
            if ! grep -qxF -- "${gt}" "${GO_FUNCS_FILE}" 2>/dev/null; then
              cov_fail "unknown Go test referenced in the coverage map: ${gt} (no func ${gt}( found under watch/**/*_test.go)"
            fi
          done < "${GO_TOKENS_FILE}"
        fi

        # --- Forward check: every bare flow-suite-path token resolves ------
        if [[ -s "${SUITE_TOKENS_FILE}" ]]; then
          while IFS= read -r sp || [[ -n "${sp}" ]]; do
            [[ -n "${sp}" ]] || continue
            if ! cov_valid_path "${sp}" "test.sh"; then
              cov_fail "unsafe or malformed path in coverage map token: ${sp}"
              continue
            fi
            if [[ ! -f "${COV_REPO_ROOT}/${sp}" || -L "${COV_REPO_ROOT}/${sp}" ]]; then
              cov_fail "referenced flow suite path does not exist: ${sp}"
            fi
          done < "${SUITE_TOKENS_FILE}"
        fi

        # --- Forward check: every path::literal token resolves -------------
        if [[ -s "${LITERAL_TOKENS_FILE}" ]]; then
          while IFS= read -r lt || [[ -n "${lt}" ]]; do
            [[ -n "${lt}" ]] || continue
            lp="${lt%%::*}"
            ll="${lt#*::}"
            [[ -n "${ll}" ]] || { cov_fail "empty literal in coverage map token: ${lt}"; continue; }
            if ! cov_valid_path "${lp}" "test.sh"; then
              cov_fail "unsafe or malformed path in coverage map token: ${lt}"
              continue
            fi
            if [[ ! -f "${COV_REPO_ROOT}/${lp}" || -L "${COV_REPO_ROOT}/${lp}" ]]; then
              cov_fail "referenced flow suite path does not exist: ${lp} (from token ${lt})"
            elif ! grep -qF -- "${ll}" "${COV_REPO_ROOT}/${lp}" 2>/dev/null; then
              cov_fail "literal not found in ${lp}: ${ll}"
            fi
          done < "${LITERAL_TOKENS_FILE}"
        fi

        # --- Forward check: every <go-glob-or-path>::<literal> token resolves
        # A new token shape used by the "## Adversarial suite bounds" table
        # (e.g. watch/internal/dispatch/chain*_test.go::TestChainFake_...):
        # the part before "::" is a Go source path or glob, the part after
        # "::" is either a Go test function name (verified via `func <name>(`
        # across the glob's matching files) or a plain literal (verified via
        # grep, mirroring the .test.sh::literal path above). Ticket #916
        # Phase 6/7 fix #2.
        if [[ -s "${GO_LITERAL_TOKENS_FILE}" ]]; then
          while IFS= read -r glt || [[ -n "${glt}" ]]; do
            [[ -n "${glt}" ]] || continue
            glp="${glt%%::*}"
            gll="${glt#*::}"
            [[ -n "${gll}" ]] || { cov_fail "empty literal in coverage map token: ${glt}"; continue; }
            if ! cov_valid_path "${glp}" "go"; then
              cov_fail "unsafe or malformed path in coverage map token: ${glt}"
              continue
            fi
            if [[ "${glp}" == */* ]]; then
              gl_dir="${COV_REPO_ROOT}/${glp%/*}"
              gl_pattern="${glp##*/}"
            else
              gl_dir="${COV_REPO_ROOT}"
              gl_pattern="${glp}"
            fi
            GL_MATCH_FILES="${COV_TMPDIR}/go-literal-match-files"
            if [[ ! -d "${gl_dir}" ]] || ! find "${gl_dir}" -maxdepth 1 -type f -name "${gl_pattern}" -print0 2>/dev/null | sort -z > "${GL_MATCH_FILES}"; then
              cov_fail "referenced Go source path/glob does not exist: ${glp} (from token ${glt})"
              continue
            fi
            if [[ ! -s "${GL_MATCH_FILES}" ]]; then
              cov_fail "referenced Go source path/glob matched zero files: ${glp} (from token ${glt})"
              continue
            fi
            if [[ "${gll}" =~ ^Test[A-Za-z0-9_]+$ ]]; then
              GL_FUNCS_FILE="${COV_TMPDIR}/go-literal-funcs"
              : > "${GL_FUNCS_FILE}"
              while IFS= read -r -d '' glf; do
                grep -ohE '^func Test[A-Za-z0-9_]+\(' "${glf}" 2>/dev/null \
                  | sed -E 's/^func (Test[A-Za-z0-9_]+)\(.*/\1/' >> "${GL_FUNCS_FILE}"
              done < "${GL_MATCH_FILES}"
              if ! grep -qxF -- "${gll}" "${GL_FUNCS_FILE}" 2>/dev/null; then
                cov_fail "unknown Go test referenced in the coverage map: ${gll} (no func ${gll}( found under ${glp})"
              fi
            else
              GL_LITERAL_FOUND=1
              while IFS= read -r -d '' glf; do
                if grep -qF -- "${gll}" "${glf}" 2>/dev/null; then
                  GL_LITERAL_FOUND=0
                  break
                fi
              done < "${GL_MATCH_FILES}"
              if [[ "${GL_LITERAL_FOUND}" -ne 0 ]]; then
                cov_fail "literal not found under ${glp}: ${gll}"
              fi
            fi
          done < "${GO_LITERAL_TOKENS_FILE}"
        fi

        # --- Backward check 1: every adversarial Go test is mapped ---------
        if [[ -d "${WATCH_DIR}" ]]; then
          CHAIN_TEST_FILES_LIST="${COV_TMPDIR}/chain-test-files"
          if find "${WATCH_DIR}/internal/dispatch" -maxdepth 1 -type f -name 'chain*_test.go' -print0 2>/dev/null | sort -z > "${CHAIN_TEST_FILES_LIST}"; then
            CHAIN_FUNCS_FILE="${COV_TMPDIR}/chain-funcs"
            : > "${CHAIN_FUNCS_FILE}"
            while IFS= read -r -d '' cf; do
              grep -ohE '^func Test[A-Za-z0-9_]+\(' "${cf}" 2>/dev/null \
                | sed -E 's/^func (Test[A-Za-z0-9_]+)\(.*/\1/' >> "${CHAIN_FUNCS_FILE}"
            done < "${CHAIN_TEST_FILES_LIST}"
            if [[ -s "${CHAIN_FUNCS_FILE}" ]]; then
              while IFS= read -r cn || [[ -n "${cn}" ]]; do
                [[ -n "${cn}" ]] || continue
                if ! grep -qxF -- "${cn}" "${GO_TOKENS_FILE}" 2>/dev/null; then
                  cov_fail "adversarial Go test not referenced anywhere in the coverage map: ${cn} (watch/internal/dispatch/chain*_test.go)"
                fi
              done < "${CHAIN_FUNCS_FILE}"
            else
              cov_fail "zero adversarial Go tests found under watch/internal/dispatch -- chain*_test.go glob may have drifted"
            fi
          else
            cov_fail "failed to enumerate watch/internal/dispatch/chain*_test.go files"
          fi
        fi

        # --- Backward check 2: both flow adversarial suites are mapped -----
        REFERENCED_SUITE_PATHS_FILE="${COV_TMPDIR}/referenced-suite-paths"
        : > "${REFERENCED_SUITE_PATHS_FILE}"
        cat "${SUITE_TOKENS_FILE}" >> "${REFERENCED_SUITE_PATHS_FILE}"
        while IFS= read -r lt || [[ -n "${lt}" ]]; do
          [[ -n "${lt}" ]] || continue
          printf '%s\n' "${lt%%::*}" >> "${REFERENCED_SUITE_PATHS_FILE}"
        done < "${LITERAL_TOKENS_FILE}"
        for adv in "flow/tests/adversarial-chain.test.sh" "flow/tests/escalation-hardstop-matrix.test.sh"; do
          if ! grep -qxF -- "${adv}" "${REFERENCED_SUITE_PATHS_FILE}" 2>/dev/null; then
            cov_fail "adversarial suite not referenced anywhere in the coverage map: ${adv}"
          fi
        done

        # --- AC4: no suite in "## Adversarial suite bounds" is EXCLUDEd ----
        ADV_SECTION_FILE="${COV_TMPDIR}/adv-section"
        awk '
          BEGIN { infence = 0; insec = 0 }
          /^```/ { infence = !infence; if (insec) print; next }
          infence { if (insec) print; next }
          /^## Adversarial suite bounds[[:space:]]*$/ { insec = 1; next }
          insec && /^## / { insec = 0; next }
          insec { print }
        ' "${MAP}" > "${ADV_SECTION_FILE}"

        ADV_TOKENS_FILE="${COV_TMPDIR}/adv-tokens"
        grep -oE '`[^`]+`' "${ADV_SECTION_FILE}" 2>/dev/null | sed -e 's/^`//' -e 's/`$//' > "${ADV_TOKENS_FILE}"
        [[ -s "${ADV_TOKENS_FILE}" ]] || cov_fail "zero rows extracted from '## Adversarial suite bounds' -- section heading may be missing/renamed"
        # Anti-vacuity: a non-empty extraction alone isn't enough -- every
        # extracted token could still be non-suite-shaped (e.g. bare numbers
        # or prose in backticks), in which case the loop below's own
        # suite-shape filter would skip every token and this check would
        # silently perform zero comparisons. Require at least one
        # suite-shaped token to survive that same filter.
        ADV_SUITE_TOKEN_COUNT="$(grep -cE '\.test\.sh(::|$)' "${ADV_TOKENS_FILE}" 2>/dev/null)" || ADV_SUITE_TOKEN_COUNT=0
        [[ "${ADV_SUITE_TOKEN_COUNT}" -gt 0 ]] || cov_fail "zero suite-shaped (*.test.sh or *.test.sh::literal) tokens found in '## Adversarial suite bounds' -- rows may have been replaced with unrelated backtick spans"
        while IFS= read -r atok || [[ -n "${atok}" ]]; do
          [[ -n "${atok}" ]] || continue
          [[ "${atok}" == *.test.sh || "${atok}" == *".test.sh::"* ]] || continue
          apath="${atok%%::*}"
          astripped="${apath#flow/}"
          if is_excluded "${astripped}"; then
            cov_fail "adversarial suite listed in the coverage map's ## Adversarial suite bounds section is excluded via run-checks.sh's EXCLUDE allowlist: ${apath} (checked as ${astripped})"
          fi
        done < "${ADV_TOKENS_FILE}"

        # --- Registration check: flow-ci.yml's flow filter --------------------
        FLOW_CI="${COV_REPO_ROOT}/.github/workflows/flow-ci.yml"
        if [[ ! -r "${FLOW_CI}" ]]; then
          cov_fail "flow-ci.yml not found or unreadable: .github/workflows/flow-ci.yml"
        else
          FLOW_BLOCK_FILE="${COV_TMPDIR}/flow-ci-block"
          awk '
            BEGIN { inflow = 0; indent = -1 }
            inflow == 0 && /^[[:space:]]*flow:[[:space:]]*$/ {
              inflow = 1
              n = match($0, /[^ ]/); indent = n - 1
              next
            }
            inflow == 1 {
              if ($0 ~ /^[[:space:]]*$/) { next }
              m = match($0, /[^ ]/); cur = m - 1
              if (cur <= indent) { inflow = 0; next }
              print
            }
          ' "${FLOW_CI}" > "${FLOW_BLOCK_FILE}"

          if [[ ! -s "${FLOW_BLOCK_FILE}" ]]; then
            cov_fail "flow-ci.yml: could not locate a 'flow:' path-filter block to verify coverage-map registration"
          else
            if ! grep -qF -- "docs/pipeline-coverage-map.md" "${FLOW_BLOCK_FILE}"; then
              cov_fail "flow-ci.yml: the flow filter's block is missing the 'docs/pipeline-coverage-map.md' path registration"
            fi
            if ! grep -qF -- "watch/**/*_test.go" "${FLOW_BLOCK_FILE}"; then
              cov_fail "flow-ci.yml: the flow filter's block is missing a 'watch/**/*_test.go' test glob registration"
            fi
          fi
        fi
      fi
    fi
  fi

  if [[ "${cov_failed}" -gt 0 ]]; then
    failed=$((failed + 1))
    FAILED_SUITES+=("coverage-map sync check")
  fi
fi

# --- 2. Discovery -------------------------------------------------------------
# Discover into a real temp file with a checked status -- not process
# substitution, per AGENTS.md's unchecked-command-substitution rule and the
# flow-ci.yml:83-84 / root-safe-perms-contract.test.sh:216-226 precedent.
LIST="$(mktemp)" || {
  echo "run-checks.sh: failed to create discovery list temp file." >&2
  exit 2
}
trap 'rm -f "${LIST}"; rm -rf "${COV_TMPDIR:-}"' EXIT

if ! find "${FLOW_ROOT}" -type f -name '*.test.sh' -print0 | sort -z > "${LIST}"; then
  echo "run-checks.sh: suite discovery failed under ${FLOW_ROOT}." >&2
  exit 1
fi

[[ -r "${LIST}" ]] || {
  echo "run-checks.sh: discovery list is not readable: ${LIST}" >&2
  exit 1
}

# Materialize into an array before executing anything.
SUITES=()
while IFS= read -r -d '' f; do
  SUITES+=("${f}")
done < "${LIST}" || {
  echo "run-checks.sh: failed to read discovery list: ${LIST}" >&2
  exit 1
}

# --- 3. Aggregate execution ----------------------------------------------------
for f in "${SUITES[@]:-}"; do
  [[ -n "${f}" ]] || continue
  rel="${f#"${FLOW_ROOT}"/}"
  if is_excluded "${rel}"; then
    echo "=== ${rel} === (skipped: excluded)"
    skipped=$((skipped + 1))
    continue
  fi
  echo "=== ${rel} ==="
  if [[ ! -r "${f}" ]]; then
    echo "run-checks.sh: suite is not readable: ${rel}" >&2
    run=$((run + 1))
    failed=$((failed + 1))
    FAILED_SUITES+=("${rel}")
    continue
  fi
  if [[ ! -s "${f}" ]]; then
    echo "run-checks.sh: suite is empty: ${rel}" >&2
    run=$((run + 1))
    failed=$((failed + 1))
    FAILED_SUITES+=("${rel}")
    continue
  fi
  if bash "${f}" </dev/null; then
    run=$((run + 1))
  else
    run=$((run + 1))
    failed=$((failed + 1))
    FAILED_SUITES+=("${rel}")
  fi
done

if [[ "${run}" -eq 0 ]]; then
  echo "run-checks.sh: zero suites executed (false-green guard) -- discovered=${#SUITES[@]} skipped=${skipped}" >&2
  exit 1
fi

echo "summary: suites run=${run} failed=${failed} skipped=${skipped}"

if [[ "${failed}" -gt 0 ]]; then
  echo "failing suites:"
  for f in "${FAILED_SUITES[@]}"; do
    echo "  ${f}"
  done
  exit 1
fi

exit 0
