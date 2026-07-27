#!/usr/bin/env bash
# Network-free contract test guarding against cosign identity-regexp drift
# (#736): install.sh's verify_installer_asset() is the single source of
# truth for the --certificate-identity-regexp value that install.sh itself
# uses to verify its own downloaded bytes, but the same literal is also
# quoted verbatim in seven doc locations across six README files plus this
# repo's docs/ (watch/README.md quotes it twice: the installer quickstart
# and the checksums.txt verification section) so users can run the same
# verification by hand. #736 fixed a bug where that
# literal only matched a `watch/v*` tag-push identity, silently rejecting
# every automated release (dispatched with `--ref main`) — if the doc
# copies and the install.sh source are ever allowed to drift again, users
# following the docs would run a *different*, possibly stale-permissive or
# stale-narrow, check than the one install.sh actually enforces. This test
# extracts the literal from install.sh and asserts every documented copy is
# byte-identical to it, failing with the exact list of files that drifted.
#
# Follows the watch-release-workflow.test.sh precedent: plain bash, no
# framework, grep-based static checks directly against the real repo files
# (no fixture tree — this asserts on the actual files shipped, not a
# stand-in).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALL_SH="${ROOT}/install.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }

echo "installer-identity-drift.test.sh"

# extract_regexp_values <file> — prints "<lineno>:<value>" for every line in
# <file> that quotes a --certificate-identity-regexp literal (grep -E is
# appropriate here per docs/plan-fidelity.md's Architectural Context: this
# is matching file contents across many lines, not validating one captured
# variable). Uses `#`/`%%` parameter expansion (not a second regexp) to pull
# the single-quoted value out of each matching line, since the value itself
# contains regexp metacharacters that would need re-escaping to embed in a
# sed/grep extraction pattern.
extract_regexp_values() {
  local file="$1" lineno content value
  grep -nF -- "--certificate-identity-regexp '" "$file" | while IFS=: read -r lineno content; do
    value="${content#*--certificate-identity-regexp \'}"
    value="${value%%\'*}"
    printf '%s:%s\n' "$lineno" "$value"
  done
}

if [[ ! -f "$INSTALL_SH" ]]; then
  fail "install.sh not found at ${INSTALL_SH}"
  echo "installer-identity-drift.test.sh: failures=${failures}"
  exit 1
fi

CANONICAL_LINES="$(extract_regexp_values "$INSTALL_SH")"
CANONICAL_COUNT="$(printf '%s\n' "$CANONICAL_LINES" | grep -c . || true)"
if [[ "$CANONICAL_COUNT" -ne 1 ]]; then
  fail "install.sh must quote --certificate-identity-regexp exactly once (found ${CANONICAL_COUNT}) — verify_installer_asset() must stay the single source of truth"
  echo "installer-identity-drift.test.sh: failures=${failures}"
  exit 1
fi
CANONICAL="${CANONICAL_LINES#*:}"

if [[ -z "$CANONICAL" ]]; then
  fail "extracted an empty --certificate-identity-regexp value from install.sh — extraction is broken or the value itself is empty"
fi

# The seven documented copies (#736's plan step 2): six files, watch/README.md
# quoting the value twice (the installer quickstart and the checksums.txt
# verification section).
DOC_FILES=(
  "README.md"
  "flow/README.md"
  "sandbox/README.md"
  "watch/README.md"
  "docs/getting-started.md"
  "docs/migrating-to-cenci.md"
  "docs/release-hygiene.md"
)

total_copies=0
for rel in "${DOC_FILES[@]}"; do
  doc_file="${ROOT}/${rel}"
  if [[ ! -f "$doc_file" ]]; then
    fail "${rel}: file not found"
    continue
  fi
  lines="$(extract_regexp_values "$doc_file")"
  count="$(printf '%s\n' "$lines" | grep -c . || true)"
  if [[ "$count" -eq 0 ]]; then
    fail "${rel}: no --certificate-identity-regexp copy found (expected at least one)"
    continue
  fi
  while IFS= read -r entry; do
    [[ -n "$entry" ]] || continue
    lineno="${entry%%:*}"
    value="${entry#*:}"
    total_copies=$((total_copies + 1))
    if [[ "$value" != "$CANONICAL" ]]; then
      fail "${rel}:${lineno} drifted from install.sh's canonical --certificate-identity-regexp value
    install.sh:  ${CANONICAL}
    ${rel}:${lineno}:  ${value}"
    fi
  done <<< "$lines"
done

# Sanity-check the expected number of documented copies is actually present
# (a typo'd path or an accidentally-deleted doc mention would otherwise make
# the loop above vacuously pass with zero comparisons). 8 total: one each in
# README.md, flow/README.md, sandbox/README.md, docs/getting-started.md,
# docs/migrating-to-cenci.md, and docs/release-hygiene.md, plus two in
# watch/README.md (the installer quickstart and the checksums.txt
# verification section) — 7 documented locations, 8 quoted occurrences.
if [[ "$total_copies" -ne 8 ]]; then
  fail "expected exactly 8 documented --certificate-identity-regexp copies across ${#DOC_FILES[@]} files, found ${total_copies} — a doc copy was added, removed, or a file failed to resolve"
fi

echo "installer-identity-drift.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
