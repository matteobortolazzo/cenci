#!/usr/bin/env bash
# Performs one plugin's version bump for plugin-version-bump.yml's "Bump
# version" step: stamps every manifest plus the marketplace entry, commits,
# pushes to main, tags, and optionally dispatches a downstream release
# workflow (ticket #743).
#
# Reads PLUGIN_NAME, PLUGIN_JSON_PATHS, MARKETPLACE_NAME, TAG_PREFIX,
# DISPATCH_WORKFLOW, ORIGINAL_SHA, GITHUB_REPOSITORY and the optional
# PUSH_RETRIES from the environment only. Nothing is `${{ }}`-interpolated
# into this script's body — the same rule resolve-release-version.sh (#626)
# established — so no workflow input can inject shell source here.
#
# WHY THIS LIVES IN A SCRIPT AND RETRIES (#743)
#
# The three caller workflows (flow, watch, sandbox) previously shared one
# `version-bump-main` concurrency group so that concurrent bumps could not
# race each other's push to main. That mechanism was lossy: GitHub keeps at
# most ONE pending run per concurrency group and cancels any previously
# pending run, so a commit touching three project paths fired three bumps,
# one ran, one queued, and the third silently displaced the queued one. A
# real release (the #742 installer-verification fix) was dropped that way,
# reported as `cancelled` rather than `failure`, so nothing alerted.
#
# Serialization-by-queue cannot be made lossless — no `cancel-in-progress`
# setting removes the single-pending-slot limit — so the race is handled here
# instead, and the concurrency group is now per-plugin. Every attempt
# re-derives the ENTIRE bump from the live tip of origin/main: refresh, re-read
# the current version, re-stamp, re-commit, push. A rejected push therefore
# retries against the new tip rather than replaying a stale diff, which is
# strictly stronger than the old queue — it also survives races against any
# other pusher to main, which the queue never covered.
#
# ORIGINAL_SHA (the commit that triggered the run) is read only for the skip
# guard and the bump-type classification. Both must inspect the triggering
# commit itself, never whatever else landed on main while this run was
# waiting or retrying, so both are resolved once, before the retry loop.
set -uo pipefail

: "${PLUGIN_NAME:?bump-plugin-version.sh: PLUGIN_NAME is required}"
: "${PLUGIN_JSON_PATHS:?bump-plugin-version.sh: PLUGIN_JSON_PATHS is required}"
: "${MARKETPLACE_NAME:?bump-plugin-version.sh: MARKETPLACE_NAME is required}"
: "${TAG_PREFIX:?bump-plugin-version.sh: TAG_PREFIX is required}"
: "${ORIGINAL_SHA:?bump-plugin-version.sh: ORIGINAL_SHA is required}"
: "${GITHUB_REPOSITORY:?bump-plugin-version.sh: GITHUB_REPOSITORY is required}"
DISPATCH_WORKFLOW="${DISPATCH_WORKFLOW:-}"
PUSH_RETRIES="${PUSH_RETRIES:-5}"

MARKETPLACE_JSON=".claude-plugin/marketplace.json"

die() {
  echo "bump-plugin-version.sh: $*" >&2
  exit 1
}

# --- Skip guard: is the triggering commit already this plugin's release? ---
# Matched as a literal prefix, so another plugin's release commit landing in
# the same push never suppresses this plugin's bump.
LAST_MSG="$(git log -1 --pretty=format:"%s" "$ORIGINAL_SHA")" \
  || die "could not read the triggering commit ${ORIGINAL_SHA}"
case "$LAST_MSG" in
  "chore(release): ${PLUGIN_NAME}/v"*)
    echo "Triggering commit is already a ${PLUGIN_NAME} release commit, skipping"
    exit 0
    ;;
esac

# --- Bump type, classified from the triggering commit only -----------------
COMMITS="$(git log -1 --pretty=format:"%s%n%b" "$ORIGINAL_SHA")" \
  || die "could not read the triggering commit ${ORIGINAL_SHA}"
BUMP="patch"
if echo "$COMMITS" | grep -qiE '^feat!:|^[a-z]+\(.*\)!:|^BREAKING[ -]CHANGE:'; then
  BUMP="major"
elif echo "$COMMITS" | grep -qiE '^feat(\(|:)'; then
  BUMP="minor"
fi

# --- Attempt loop: each pass re-derives the bump from the live tip ---------
NEW_VERSION=""
pushed=0
attempt=1
while [ "$attempt" -le "$PUSH_RETRIES" ]; do
  # An explicit refspec (rather than a bare `git fetch origin main`) so
  # refs/remotes/origin/main is guaranteed updated, making the reset below
  # deterministic regardless of the clone's fetch configuration.
  git fetch --quiet origin +refs/heads/main:refs/remotes/origin/main \
    || die "could not fetch origin/main (attempt ${attempt})"
  # Discards the previous attempt's rejected commit, if any, and re-bases
  # this attempt's work on the tip that actually won the race.
  git reset --quiet --hard origin/main \
    || die "could not reset to origin/main (attempt ${attempt})"

  # Read the current version from the first manifest path (source of truth).
  # Re-read on every attempt: a concurrent push may have changed it.
  # shellcheck disable=SC2086 # deliberate word splitting: space-separated paths
  set -- $PLUGIN_JSON_PATHS
  SOURCE_JSON="$1"
  VERSION="$(jq -r '.version' "$SOURCE_JSON")" \
    || die "could not read .version from ${SOURCE_JSON}"
  # Fail loudly on a malformed source version rather than letting the
  # arithmetic below silently produce a nonsense version and tag.
  if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "${SOURCE_JSON} has a non-semver .version: '${VERSION}'"
  fi
  MAJOR="${VERSION%%.*}"
  REST="${VERSION#*.}"
  MINOR="${REST%%.*}"
  PATCH="${REST#*.}"

  case "$BUMP" in
    major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0 ;;
    minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
    patch) PATCH=$((PATCH + 1)) ;;
  esac

  NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
  echo "Bumping ${BUMP} (attempt ${attempt}/${PUSH_RETRIES}): ${VERSION} -> ${NEW_VERSION}"

  # Stamp every manifest path (source of truth + Codex manifest + any
  # status-bar frontend manifests). Frontends keep the version under
  # different keys: KDE Plasma applets use .KPlugin.Version; GNOME
  # extensions must keep .version an integer (managed by
  # extensions.gnome.org), so the semver goes into ."version-name"
  # (a string field GNOME Shell 45+ displays); everything else uses
  # a top-level .version.
  for p in $PLUGIN_JSON_PATHS; do
    if jq -e 'has("KPlugin")' "$p" > /dev/null; then
      # shellcheck disable=SC2016 # $v is a jq --arg placeholder, not a shell variable
      FILTER='.KPlugin.Version = $v'
    elif jq -e 'has("shell-version")' "$p" > /dev/null; then
      # shellcheck disable=SC2016 # $v is a jq --arg placeholder, not a shell variable
      FILTER='."version-name" = $v'
    else
      # shellcheck disable=SC2016 # $v is a jq --arg placeholder, not a shell variable
      FILTER='.version = $v'
    fi
    jq --arg v "$NEW_VERSION" "$FILTER" "$p" > tmp.json \
      || die "could not stamp ${NEW_VERSION} into ${p}"
    mv tmp.json "$p" || die "could not write the stamped ${p}"
  done

  # Stamp the marketplace entry. Fail loudly if the name matches no entry
  # (e.g. a typo in a caller's marketplace-name input) — otherwise the jq
  # filter would silently no-op and ship a stale marketplace version. This is
  # a configuration error, not a race, so it exits instead of retrying.
  jq -e --arg n "$MARKETPLACE_NAME" '.plugins[] | select(.name == $n)' \
    "$MARKETPLACE_JSON" > /dev/null \
    || die "no marketplace entry named '${MARKETPLACE_NAME}' in ${MARKETPLACE_JSON}"
  jq --arg v "$NEW_VERSION" --arg n "$MARKETPLACE_NAME" \
    '(.plugins[] | select(.name == $n)).version = $v' \
    "$MARKETPLACE_JSON" > tmp.json \
    || die "could not stamp ${NEW_VERSION} into ${MARKETPLACE_JSON}"
  mv tmp.json "$MARKETPLACE_JSON" || die "could not write the stamped ${MARKETPLACE_JSON}"

  # shellcheck disable=SC2086 # deliberate word splitting: space-separated paths
  git add $PLUGIN_JSON_PATHS "$MARKETPLACE_JSON" \
    || die "could not stage the bumped manifests"
  git commit --quiet -m "chore(release): ${PLUGIN_NAME}/v${NEW_VERSION}" \
    || die "could not commit the ${NEW_VERSION} bump"

  if git push --quiet origin HEAD:main; then
    pushed=1
    break
  fi

  echo "Push rejected — origin/main moved; refreshing and retrying" >&2
  attempt=$((attempt + 1))
done

if [ "$pushed" -ne 1 ]; then
  die "could not push the ${PLUGIN_NAME} bump after ${PUSH_RETRIES} attempt(s) — origin/main kept moving ahead"
fi

# --- Tag the commit that actually landed -----------------------------------
gh api "repos/${GITHUB_REPOSITORY}/git/refs" \
  -f ref="refs/tags/${TAG_PREFIX}/v${NEW_VERSION}" \
  -f sha="$(git rev-parse HEAD)" \
  || die "could not create tag refs/tags/${TAG_PREFIX}/v${NEW_VERSION}"

# --- Optionally dispatch a downstream workflow (watch release) -------------
# A tag pushed with GITHUB_TOKEN does NOT fire an `on: push: tags` workflow
# (recursion guard), but dispatching one IS allowed. Run the workflow
# definition from main (latest); it checks out the tag to build.
if [ -n "$DISPATCH_WORKFLOW" ]; then
  gh workflow run "$DISPATCH_WORKFLOW" \
    --ref main \
    -f version="${NEW_VERSION}" \
    || die "could not dispatch ${DISPATCH_WORKFLOW} for ${NEW_VERSION}"
fi
