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

# ── the GoReleaser *binary* must be pinned to an exact version, never a
# floating range (#1139). '~> v2' silently adopted goreleaser v2.18.1, whose
# stricter current-tag handling aborted the release for watch/v2.2.1 with no
# change on our side; because sandbox containers always resolve the newest
# plugin version, the resulting missing release artifact silently killed the
# watch attention layer in every container. Mirrors the cosign-release pin,
# which exists for the same class of upstream drift. ──
GORELEASER_LINE="$(grep -n 'uses: goreleaser/goreleaser-action@' "$RELEASE" | sed -n 1p | cut -d: -f1)"
if [[ -z "$GORELEASER_LINE" ]]; then
  fail "watch-release.yml missing a goreleaser/goreleaser-action step"
else
  GORELEASER_BLOCK="$(sed -n "${GORELEASER_LINE},/^      - /p" "$RELEASE")"
  GORELEASER_VERSION="$(echo "$GORELEASER_BLOCK" | sed -n "s/^[[:space:]]*version:[[:space:]]*'\{0,1\}\([^'#]*\)'\{0,1\}[[:space:]]*$/\1/p" | head -n1)"
  if [[ -z "$GORELEASER_VERSION" ]]; then
    fail "the goreleaser-action step must declare an explicit 'version:' input"
  elif [[ ! "$GORELEASER_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    fail "the goreleaser binary must be pinned to an exact vX.Y.Z (got '${GORELEASER_VERSION}') — a floating range adopts upstream breakage on the next release run (#1139)"
  fi
fi

# ── GORELEASER_CURRENT_TAG names a clean semver that is NOT a real tag in
# this repo (tags are the monorepo-prefixed watch/vX.Y.Z). goreleaser v2.18.1
# reads that tag's contents and aborts when the lookup returns empty, so the
# alias has to be materialised as a real annotated tag before goreleaser runs
# (#1139). Ordering is asserted by line number: an alias created after the
# build step would be useless. ──
ALIAS_LINE="$(grep -n 'name: Materialise the clean-semver alias tag for GoReleaser' "$RELEASE" | sed -n 1p | cut -d: -f1)"
if [[ -z "$ALIAS_LINE" ]]; then
  fail "watch-release.yml missing the step that materialises the clean-semver alias tag GORELEASER_CURRENT_TAG points at (#1139)"
else
  if [[ -n "$GORELEASER_LINE" ]]; then
    (( ALIAS_LINE < GORELEASER_LINE )) ||
      fail "the alias-tag step (line ${ALIAS_LINE}) must run before the goreleaser build step (line ${GORELEASER_LINE}) — goreleaser resolves GORELEASER_CURRENT_TAG at build time"
  fi
  if [[ -n "$TAGGED_CHECKOUT_LINE" ]]; then
    (( ALIAS_LINE > TAGGED_CHECKOUT_LINE )) ||
      fail "the alias-tag step (line ${ALIAS_LINE}) must run after the tagged checkout (line ${TAGGED_CHECKOUT_LINE}) — the watch/v<version> tag it aliases isn't on disk before it"
  fi

  ALIAS_BLOCK="$(sed -n "${ALIAS_LINE},/^      - /p" "$RELEASE")"
  # Annotated, so `git tag -l --format='%(contents)'` returns the tag message.
  # A lightweight alias would leave the contents lookup dependent on the
  # underlying commit message, which is exactly the fragility being fixed.
  block_contains "$ALIAS_BLOCK" "git tag -f -a" \
    "the alias tag must be annotated so goreleaser's tag-contents lookup returns a non-empty value"
  # The alias must be built from the real prefixed tag, not from HEAD, or it
  # can silently pin the release to the wrong commit.
  block_contains "$ALIAS_BLOCK" 'refs/tags/${TAG}' \
    "the alias tag must be created from refs/tags/\${TAG} (the real watch/v<version> tag), never from HEAD"
  # The alias must stay local. Pushing it would publish a second, competing
  # tag namespace for every release.
  [[ "$ALIAS_BLOCK" != *"git push"* ]] ||
    fail "the alias tag must never be pushed — it exists only for this runner's goreleaser invocation"
  # It must not land in the watch/v* namespace, which the publish step's
  # previous-tag lookup globs over for release notes.
  [[ "$ALIAS_BLOCK" != *'"watch/v${VERSION}"'* ]] ||
    fail "the alias tag must not be created inside the watch/v* namespace — the publish step's previous-tag glob would pick it up"

  # An *annotated* tag is a real git object, so git demands a committer
  # identity to write it. A GitHub runner's checkout has none, so `git tag -a`
  # dies with "fatal: empty ident name ... not allowed" (exit 128) — which is
  # exactly how the #1139 fix shipped, failing every release from watch/v2.2.1
  # through v2.4.2 and leaving sandbox containers with no downloadable binary
  # for the plugin version the marketplace serves them. The identity must be
  # configured in this same step (each `run:` block is its own shell, and no
  # earlier step sets one) and before the tag is written.
  ALIAS_NAME_LINE="$(echo "$ALIAS_BLOCK" | grep -n 'git config user\.name' | sed -n 1p | cut -d: -f1)"
  ALIAS_EMAIL_LINE="$(echo "$ALIAS_BLOCK" | grep -n 'git config user\.email' | sed -n 1p | cut -d: -f1)"
  ALIAS_TAG_LINE="$(echo "$ALIAS_BLOCK" | grep -n 'git tag -f -a' | sed -n 1p | cut -d: -f1)"
  if [[ -z "$ALIAS_NAME_LINE" || -z "$ALIAS_EMAIL_LINE" ]]; then
    fail "the alias-tag step must configure git user.name and user.email — an annotated tag without a committer identity fails with 'fatal: empty ident name' (exit 128)"
  elif [[ -n "$ALIAS_TAG_LINE" ]]; then
    (( ALIAS_NAME_LINE < ALIAS_TAG_LINE && ALIAS_EMAIL_LINE < ALIAS_TAG_LINE )) ||
      fail "the alias-tag step must configure the committer identity before it runs 'git tag -f -a'"
  fi
fi

# Sanity-check the file is actually being read (a typo'd $RELEASE path
# would otherwise make every check above vacuously pass on an empty var).
contains "$RELEASE" "name: watch — Release" "unexpected watch-release.yml content — wrong file resolved?"

echo "watch-release-workflow.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
