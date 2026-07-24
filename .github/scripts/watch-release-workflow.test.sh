#!/usr/bin/env bash
# Static regression tests for watch-release.yml's supply-chain hardening
# (#626): the release's install.sh must be cosign-signed into a bundle, and
# both install.sh and install.sh.bundle must actually ship as release
# assets. Follows the maintenance-workflows.test.sh precedent: plain bash,
# no framework, grep -F/contains-based static checks directly against the
# real workflow YAML (no fixture tree — this asserts on the actual file
# that runs in CI, not a stand-in).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RELEASE="${ROOT}/.github/workflows/watch-release.yml"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }
contains() { grep -qF -- "$2" "$1" || fail "$3"; }
block_contains() { [[ "$1" == *"$2"* ]] || fail "$3"; }

echo "watch-release-workflow.test.sh"

# ── the "Sign install.sh with cosign" step must run cosign sign-blob
# against install.sh and produce install.sh.bundle (#626 AC1: the installer
# asset actually gets signed, not just checksums.txt) ──
SIGN_BLOCK="$(sed -n '/name: Sign install\.sh with cosign/,/^      - name:/p' "$RELEASE")"
if [[ -z "$SIGN_BLOCK" ]]; then
  fail "watch-release.yml missing a 'Sign install.sh with cosign' step"
else
  block_contains "$SIGN_BLOCK" "cosign sign-blob" "install.sh signing step must run cosign sign-blob"
  block_contains "$SIGN_BLOCK" "--bundle install.sh.bundle" "install.sh signing step must produce install.sh.bundle via --bundle"
  echo "$SIGN_BLOCK" | grep -Eq '^[[:space:]]+install\.sh$' ||
    fail "install.sh signing step must pass install.sh (bare, on its own line) as the cosign sign-blob target"
fi

# ── the release's assets=( ... ) array must ship both install.sh and
# install.sh.bundle (#626 AC2: the signed bundle must actually be uploaded
# alongside install.sh, or verification has nothing to check against) ──
ASSETS_BLOCK="$(sed -n '/assets=(/,/^[[:space:]]*)$/p' "$RELEASE")"
if [[ -z "$ASSETS_BLOCK" ]]; then
  fail "watch-release.yml missing an assets=( ... ) array"
else
  block_contains "$ASSETS_BLOCK" $'\n            install.sh\n' "release assets array missing install.sh"
  block_contains "$ASSETS_BLOCK" $'\n            install.sh.bundle\n' "release assets array missing install.sh.bundle"
fi

# Sanity-check the file is actually being read (a typo'd $RELEASE path
# would otherwise make every check above vacuously pass on an empty var).
contains "$RELEASE" "name: watch — Release" "unexpected watch-release.yml content — wrong file resolved?"

echo "watch-release-workflow.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
