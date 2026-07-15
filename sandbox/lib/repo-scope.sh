#!/bin/bash
# Pure repo-scoping helpers for cenci-sand.
#
# Sourced by cenci-sand (on the host) and by the test harness
# (sandbox/tests/repo-scope.test.sh) so the logic that namespaces
# containers/volumes/images per repo lives in exactly one place.
#
# Every function here is pure (no side effects beyond reading the
# filesystem/git) and safe to source without side effects of its own.

# slugify <input>: lowercase the input and replace each character outside
# [a-z0-9_.-] with a dash. Used to turn a repo directory name into a
# container/volume/image-safe suffix.
#
# LC_ALL=C.UTF-8 is forced so multi-byte characters (e.g. accented letters)
# are matched one Unicode character at a time: the C locale treats them as
# raw bytes (one dash per byte), while locales like en_US.UTF-8 can widen
# `a-z` via locale collation to include accented letters (leaving them
# un-slugified). C.UTF-8 keeps Unicode-aware character boundaries with plain
# codepoint-ordered ranges, giving one dash per non-matching character.
slugify() {
    local input="$1"
    echo "${input}" | LC_ALL=C.UTF-8 tr '[:upper:]' '[:lower:]' | LC_ALL=C.UTF-8 sed -E 's/[^a-z0-9_.-]/-/g'
}

# resolve_repo_root: print the absolute root of the git repo containing the
# current working directory, or fail (non-zero return, nothing useful on
# stdout) when the cwd isn't inside a git repo. Callers use the return code
# to pick between the per-repo scheme and the legacy fallback.
resolve_repo_root() {
    git -C "${PWD}" rev-parse --show-toplevel 2>/dev/null
}

# compute_workdir <repo-root> <cwd>: map a host cwd inside <repo-root> to the
# equivalent path under the container's /workspace mount. Returns
# "/workspace" when cwd is the repo root itself, or
# "/workspace/<relative-subpath>" when cwd is a subdirectory of the repo root.
compute_workdir() {
    local repo_root="$1" cwd="$2"
    if [[ "${cwd}" == "${repo_root}" ]]; then
        echo "/workspace"
    else
        echo "/workspace${cwd#"${repo_root}"}"
    fi
}

# compute_legacy_workdir <workspace-host> <cwd>: map a host cwd to the
# equivalent path under the container's /workspace mount for the non-git
# fallback scheme (whole ~/Repos mount). Returns
# "/workspace/<relative-subpath>" when cwd is under <workspace-host>, or
# "/workspace" otherwise (cwd outside the mounted host root).
compute_legacy_workdir() {
    local workspace_host="$1" cwd="$2"
    if [[ "${cwd}" == "${workspace_host}"* ]]; then
        echo "/workspace${cwd#"${workspace_host}"}"
    else
        echo "/workspace"
    fi
}

# has_repo_image <repo-root>: true (exit 0) when <repo-root> opts into its own
# image via a Dockerfile at <repo-root>/.cenci/Dockerfile.
has_repo_image() {
    local repo_root="$1"
    [[ -f "${repo_root}/.cenci/Dockerfile" ]]
}

# select_image <repo-root> <repo-slug>: print the image tag cenci-sand should
# use for this repo. A per-repo Dockerfile at <repo-root>/.cenci/Dockerfile
# opts the repo into its own image; otherwise fall back to the shared monolith
# image.
select_image() {
    local repo_root="$1" repo_slug="$2"
    if has_repo_image "${repo_root}"; then
        echo "cenci-sandbox-${repo_slug}:latest"
    else
        echo "cenci-sandbox:latest"
    fi
}
