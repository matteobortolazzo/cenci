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

# ── every `run: bash .github/scripts/...` step must be preceded by an
# actions/checkout, or the script isn't on disk yet and the step dies with
# exit 127 (#690). #677 extracted the "Resolve version and tag" step's
# inline shell into .github/scripts/resolve-release-version.sh but left the
# step first in the job, ahead of the checkout at refs/tags/watch/v<version>
# — which silently broke every watch release from v0.28.0 on. Asserted
# positionally by line number rather than by step name so any future
# script-invoking step added above the checkout fails here too. ──
FIRST_CHECKOUT_LINE="$(grep -n 'uses: actions/checkout@' "$RELEASE" | sed -n 1p | cut -d: -f1)"
if [[ -z "$FIRST_CHECKOUT_LINE" ]]; then
  fail "watch-release.yml has no actions/checkout step"
else
  while IFS=: read -r line _; do
    [[ -n "$line" ]] || continue
    (( line > FIRST_CHECKOUT_LINE )) ||
      fail "watch-release.yml line ${line} runs a .github/scripts/ script before the first actions/checkout (line ${FIRST_CHECKOUT_LINE}) — the repo isn't on disk yet, so the step fails with exit 127"
  done < <(grep -n 'bash \.github/scripts/' "$RELEASE")
fi

# ── that first checkout must be the cheap script-only one, not a full
# clone: it exists solely to make .github/scripts available to the resolve
# step, and the authoritative build checkout is the later tagged one. ──
PRE_CHECKOUT_BLOCK="$(sed -n "${FIRST_CHECKOUT_LINE:-1},/^      - name:/p" "$RELEASE")"
block_contains "$PRE_CHECKOUT_BLOCK" "sparse-checkout: .github/scripts" \
  "the pre-resolve checkout must sparse-checkout .github/scripts"

# ── the release must still BUILD from the tag's commit: the resolve step's
# output has to drive a later checkout, so the signed/published bytes come
# from refs/tags/watch/v<version> and never from a mutable branch (#626). ──
contains "$RELEASE" 'ref: refs/tags/watch/v${{ steps.v.outputs.version }}' \
  "watch-release.yml must check out refs/tags/watch/v<version> for the build"
TAGGED_CHECKOUT_LINE="$(grep -n 'ref: refs/tags/watch/v' "$RELEASE" | sed -n 1p | cut -d: -f1)"
RESOLVE_LINE="$(grep -n 'bash \.github/scripts/resolve-release-version\.sh' "$RELEASE" | sed -n 1p | cut -d: -f1)"
if [[ -n "$TAGGED_CHECKOUT_LINE" && -n "$RESOLVE_LINE" ]]; then
  (( TAGGED_CHECKOUT_LINE > RESOLVE_LINE )) ||
    fail "the tagged checkout (line ${TAGGED_CHECKOUT_LINE}) must come after the resolve step (line ${RESOLVE_LINE}) that produces its version output"
else
  fail "watch-release.yml missing either the tagged checkout or the resolve step"
fi

# ── the release must verify its own published installer signature after
# publishing (#736): a regression to the wrong --certificate-identity-regexp
# (or any other drift) must fail the release job itself, not rely solely on
# the manual runbook step in docs/release-hygiene.md#8 — the step that
# evidently never actually ran against a real automated release. ──
VERIFY_LINE="$(grep -n 'name: Verify the published installer signature' "$RELEASE" | sed -n 1p | cut -d: -f1)"
PUBLISH_LINE="$(grep -n 'name: Publish release to the watch/v\* tag' "$RELEASE" | sed -n 1p | cut -d: -f1)"
if [[ -z "$VERIFY_LINE" ]]; then
  fail "watch-release.yml missing a 'Verify the published installer signature' step"
else
  if [[ -n "$PUBLISH_LINE" ]]; then
    (( VERIFY_LINE > PUBLISH_LINE )) ||
      fail "the installer-signature verification step (line ${VERIFY_LINE}) must run after the publish step (line ${PUBLISH_LINE}) — it verifies the just-published release asset"
  else
    fail "watch-release.yml missing the 'Publish release to the watch/v* tag' step to compare against"
  fi

  VERIFY_BLOCK="$(sed -n "${VERIFY_LINE},/^      - name:/p" "$RELEASE")"
  block_contains "$VERIFY_BLOCK" "gh release download" \
    "the verification step must re-download the published install.sh/install.sh.bundle via gh release download, not reuse the local checkout copy"
  block_contains "$VERIFY_BLOCK" "cosign verify-blob" \
    "the verification step must run cosign verify-blob against the re-downloaded assets"
  block_contains "$VERIFY_BLOCK" "grep" \
    "the verification step must derive --certificate-identity-regexp from install.sh via grep, not a hardcoded literal"
  block_contains "$VERIFY_BLOCK" "install.sh" \
    "the verification step must read the identity regexp out of install.sh"
  # Must NOT hardcode the actual regexp value in the workflow — only ever
  # extract it from the checked-out install.sh, so this step can't itself
  # drift from what install.sh enforces (the same drift #736 exists to fix).
  [[ "$VERIFY_BLOCK" != *'--certificate-identity-regexp '"'"'^https'* ]] ||
    fail "the verification step must not hardcode a --certificate-identity-regexp literal — it must grep the value out of install.sh"
fi

# Sanity-check the file is actually being read (a typo'd $RELEASE path
# would otherwise make every check above vacuously pass on an empty var).
contains "$RELEASE" "name: watch — Release" "unexpected watch-release.yml content — wrong file resolved?"

echo "watch-release-workflow.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
