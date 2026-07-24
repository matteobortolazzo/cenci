#!/usr/bin/env bash
# Resolves the release VERSION + TAG for watch-release.yml's "Resolve version
# and tag" step (ticket #626).
#
# Reads EVENT_NAME, VERSION_INPUT, GITHUB_REF_NAME, GITHUB_OUTPUT from the
# environment only. This script's body never interpolates any of these
# values into shell source (no `${{ }}` templating happens here — that is
# the whole point of extracting this step out of the workflow YAML) and
# never `eval`s or re-parses the resolved version as shell source, so a
# malicious workflow_dispatch input cannot execute injected shell content.
#
# Resolution:
#   - EVENT_NAME == "workflow_dispatch": VERSION comes from VERSION_INPUT
#     (the untrusted, manually-typed input surface).
#   - Any other EVENT_NAME: VERSION comes from GITHUB_REF_NAME with a
#     leading "watch/v" stripped (mirrors the previous inline resolution).
#   - A single stray leading "v" is stripped from VERSION before validation
#     (tolerates a manually-typed "v1.2.3" workflow_dispatch input).
#
# Validation: the complete resulting value must whole-string match
# ^[0-9]+\.[0-9]+\.[0-9]+$ via bash `[[ =~ ]]` — never line-oriented `grep`,
# which would let a malicious multi-line value with a valid-looking first
# line slip through.
#
# On success: writes "version=<version>" and "tag=watch/v<version>" to
# $GITHUB_OUTPUT and exits 0.
# On any non-semver result (including injection payloads that don't reduce
# to a clean semver string): exits non-zero and writes nothing to
# $GITHUB_OUTPUT.
set -uo pipefail

: "${EVENT_NAME:?resolve-release-version.sh: EVENT_NAME is required}"
: "${GITHUB_OUTPUT:?resolve-release-version.sh: GITHUB_OUTPUT is required}"
VERSION_INPUT="${VERSION_INPUT:-}"
GITHUB_REF_NAME="${GITHUB_REF_NAME:-}"

if [ "$EVENT_NAME" = "workflow_dispatch" ]; then
  VERSION="$VERSION_INPUT"
else
  VERSION="${GITHUB_REF_NAME#watch/v}"
fi

# Tolerate a single stray leading "v" (e.g. a manually-typed "v1.2.3").
VERSION="${VERSION#v}"

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "resolve-release-version.sh: rejected non-semver version (event=${EVENT_NAME})" >&2
  exit 1
fi

{
  echo "version=${VERSION}"
  echo "tag=watch/v${VERSION}"
} >>"$GITHUB_OUTPUT"
