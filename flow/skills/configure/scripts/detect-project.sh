#!/usr/bin/env bash
# detect-project.sh — deterministic, LLM-free project detection for
# /cenci:configure. Run from the repository root; prints one JSON object on
# stdout with every mechanical detection the configure skill needs, so the
# agent consumes facts instead of re-deriving them ad hoc:
#
#   {
#     "pluginVersion":  "<flow plugin version>" | null,
#     "platform":       {"name":"github","owner":"…","repo":"…"} | null,
#     "inContainer":    true | false,
#     "packageManager": "pnpm" | "yarn" | "npm" | null,
#     "mcpServers":     ["angular", "primeng", …],   // detected catalog triggers
#     "lspServers":     ["typescript", "gopls", …],  // detected catalog triggers
#     "dindDetected":   true | false,                // Testcontainers/Docker-SDK triggers
#     "playwrightTest": true | false,                // @playwright/test in root devDependencies
#     "warnings":       ["…"]                        // non-fatal detection notes
#   }
#
# Detection only — no file is written, no question is asked; catalogs,
# question flow, and user choices stay in the configure skill. Per-detector
# failures (e.g. malformed package.json) degrade to empty results plus a
# `warnings` entry rather than aborting the run. Requires jq: without it the
# script fails closed (exit 2) and configure falls back to its manual
# detection procedures.
#
# Plugin version source: --plugin-root <dir> (or $CLAUDE_PLUGIN_ROOT), read
# from <dir>/.claude-plugin/plugin.json — configure stamps it into
# .cenci/config.json as configVersion.
set -uo pipefail

PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --plugin-root)
      PLUGIN_ROOT="${2:-}"
      shift 2 || { echo "detect-project.sh: --plugin-root requires a value" >&2; exit 2; }
      ;;
    *)
      echo "detect-project.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

command -v jq >/dev/null 2>&1 || { echo "detect-project.sh: jq is required but was not found on PATH." >&2; exit 2; }

WARNINGS=()

# --- plugin version ---------------------------------------------------------
PLUGIN_VERSION=""
if [[ -n "$PLUGIN_ROOT" && -f "${PLUGIN_ROOT}/.claude-plugin/plugin.json" ]]; then
  PLUGIN_VERSION="$(jq -r 'if (.version | type) == "string" then .version else "" end' \
    "${PLUGIN_ROOT}/.claude-plugin/plugin.json" 2>/dev/null)" || PLUGIN_VERSION=""
fi
if [[ -z "$PLUGIN_VERSION" ]]; then
  WARNINGS+=("plugin version could not be resolved — pass --plugin-root or set CLAUDE_PLUGIN_ROOT; configVersion cannot be stamped from this run")
fi

# --- platform from the git remote -------------------------------------------
# Read the raw configured URL (git config) rather than `git remote get-url`,
# which applies url.<base>.insteadOf rewrites and would hide the real host.
PLATFORM_JSON="null"
REMOTE_URL="$(git config --get remote.origin.url 2>/dev/null)" || REMOTE_URL=""
if [[ "$REMOTE_URL" =~ ^git@github\.com:([^/]+)/(.+)$ ]] \
  || [[ "$REMOTE_URL" =~ ^https://github\.com/([^/]+)/(.+)$ ]]; then
  OWNER="${BASH_REMATCH[1]}"
  REPO="${BASH_REMATCH[2]}"
  REPO="${REPO%/}"
  REPO="${REPO%.git}"
  PLATFORM_JSON="$(jq -n --arg o "$OWNER" --arg r "$REPO" '{name: "github", owner: $o, repo: $r}')" \
    || PLATFORM_JSON="null"
fi

# --- container detection (CENCI_SANDBOX, then /.dockerenv) -------------------
# CENCI_DETECT_DOCKERENV_PATH exists for tests only — production callers never
# set it, so the fallback stays the real /.dockerenv sentinel.
DOCKERENV_PATH="${CENCI_DETECT_DOCKERENV_PATH:-/.dockerenv}"
IN_CONTAINER=false
if [[ "${CENCI_SANDBOX:-}" == "1" ]]; then
  IN_CONTAINER=true
elif [[ -f "$DOCKERENV_PATH" ]]; then
  IN_CONTAINER=true
fi

# --- package manager from lockfiles ------------------------------------------
PACKAGE_MANAGER=""
if [[ -f pnpm-lock.yaml ]]; then
  PACKAGE_MANAGER="pnpm"
elif [[ -f yarn.lock ]]; then
  PACKAGE_MANAGER="yarn"
elif [[ -f package-lock.json ]]; then
  PACKAGE_MANAGER="npm"
fi

# --- bounded finds (prune VCS/vendor/build dirs) ------------------------------
PRUNE=(\( -name .git -o -name node_modules -o -name .worktrees -o -name bin -o -name obj -o -name dist -o -name .angular -o -name .next -o -name vendor \) -prune)

find_first() {  # find_first <name-pattern> → first match or nothing
  find . "${PRUNE[@]}" -o -type f -name "$1" -print -quit 2>/dev/null
}

find_all() {  # find_all <name-pattern> → all matches, newline-separated
  find . "${PRUNE[@]}" -o -type f -name "$1" -print 2>/dev/null
}

# --- npm dependency scan (root package.json) ---------------------------------
NPM_DEPS=""
NPM_DEV_DEPS=""
if [[ -f package.json ]]; then
  if ! NPM_DEPS="$(jq -r '((.dependencies // {}) + (.devDependencies // {})) | keys[]' package.json 2>/dev/null)"; then
    NPM_DEPS=""
    WARNINGS+=("package.json is not valid JSON — npm dependency detection skipped")
  fi
  NPM_DEV_DEPS="$(jq -r '(.devDependencies // {}) | keys[]' package.json 2>/dev/null)" || NPM_DEV_DEPS=""
fi

has_npm_dep() { grep -qxF -- "$1" <<< "$NPM_DEPS"; }

# --- .csproj PackageReference scan -------------------------------------------
CSPROJ_FILES="$(find_all '*.csproj')"
CSPROJ_REFS=""
if [[ -n "$CSPROJ_FILES" ]]; then
  CSPROJ_REFS="$(while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    grep -hoE '<PackageReference[^>]*Include="[^"]*"' "$f" 2>/dev/null
  done <<< "$CSPROJ_FILES" | sed -E 's/.*Include="([^"]*)".*/\1/' | sort -u)" || CSPROJ_REFS=""
fi

# --- MCP catalog triggers -----------------------------------------------------
MCP=()
has_npm_dep "@angular/core" && MCP+=("angular")
has_npm_dep "primeng" && MCP+=("primeng")

# --- LSP catalog triggers -----------------------------------------------------
LSP=()
if has_npm_dep typescript || has_npm_dep "@angular/core" || has_npm_dep react \
  || has_npm_dep next || has_npm_dep vue; then
  LSP+=("typescript")
fi
if [[ -f pyproject.toml || -f requirements.txt || -n "$(find_first '*.py')" ]]; then
  LSP+=("pyright")
fi
[[ -n "$(find_first 'Cargo.toml')" ]] && LSP+=("rust-analyzer")
[[ -n "$CSPROJ_FILES" ]] && LSP+=("csharp-ls")
GO_MOD="$(find_first 'go.mod')"
[[ -n "$GO_MOD" ]] && LSP+=("gopls")

# --- Testcontainers / Docker-SDK (dind) triggers ------------------------------
DIND=false
if has_npm_dep testcontainers || has_npm_dep dockerode || grep -q '^@testcontainers/' <<< "$NPM_DEPS"; then
  DIND=true
fi
if [[ "$DIND" == false && -n "$CSPROJ_REFS" ]]; then
  if grep -qE '^Testcontainers' <<< "$CSPROJ_REFS" || grep -qxF 'Docker.DotNet' <<< "$CSPROJ_REFS"; then
    DIND=true
  fi
fi
if [[ "$DIND" == false && -f requirements.txt ]]; then
  grep -qiE '^testcontainers([[(<>=~! ].*)?$' requirements.txt && DIND=true
fi
if [[ "$DIND" == false && -f pyproject.toml ]]; then
  grep -qiE '"?testcontainers[["<>=~! ]' pyproject.toml && DIND=true
fi
if [[ "$DIND" == false && -n "$GO_MOD" ]]; then
  grep -q 'github.com/testcontainers/testcontainers-go' "$GO_MOD" && DIND=true
fi

# --- Playwright Test (root devDependencies only) ------------------------------
PLAYWRIGHT_TEST=false
grep -qxF -- "@playwright/test" <<< "$NPM_DEV_DEPS" && PLAYWRIGHT_TEST=true

# --- assemble output ----------------------------------------------------------
to_json_array() {
  if [[ $# -eq 0 ]]; then
    echo "[]"
  else
    printf '%s\n' "$@" | jq -R . | jq -sc .
  fi
}

MCP_JSON="$(to_json_array ${MCP[@]+"${MCP[@]}"})" || { echo "detect-project.sh: failed to encode mcpServers" >&2; exit 2; }
LSP_JSON="$(to_json_array ${LSP[@]+"${LSP[@]}"})" || { echo "detect-project.sh: failed to encode lspServers" >&2; exit 2; }
WARN_JSON="$(to_json_array ${WARNINGS[@]+"${WARNINGS[@]}"})" || { echo "detect-project.sh: failed to encode warnings" >&2; exit 2; }

jq -n \
  --arg pluginVersion "$PLUGIN_VERSION" \
  --argjson platform "$PLATFORM_JSON" \
  --argjson inContainer "$IN_CONTAINER" \
  --arg packageManager "$PACKAGE_MANAGER" \
  --argjson mcpServers "$MCP_JSON" \
  --argjson lspServers "$LSP_JSON" \
  --argjson dindDetected "$DIND" \
  --argjson playwrightTest "$PLAYWRIGHT_TEST" \
  --argjson warnings "$WARN_JSON" \
  '{
    pluginVersion: (if $pluginVersion == "" then null else $pluginVersion end),
    platform: $platform,
    inContainer: $inContainer,
    packageManager: (if $packageManager == "" then null else $packageManager end),
    mcpServers: $mcpServers,
    lspServers: $lspServers,
    dindDetected: $dindDetected,
    playwrightTest: $playwrightTest,
    warnings: $warnings
  }'
