#!/usr/bin/env bash
# merge-sandbox-config.sh — deterministic, client-neutral merge of the
# `sandbox` config object for /cenci:configure's Q9 (per-repo Dockerfile),
# Q9b (nested Docker / dind) and Q9c (Azure CLI) answers (#632, #1080). Both
# Claude's SKILL.md and Codex's codex.md invoke this same script so
# equivalent inputs and answers produce byte-equivalent `sandbox` JSON — a
# shared executable, not duplicated prose, guarantees the client-neutral
# contract (ticket #632's acceptance criteria; "prose presence alone is not
# evidence that generated JSON is correct").
#
# Usage:
#   merge-sandbox-config.sh <existing-config-path|-> \
#     --dockerfile <true|false> \
#     --dind <true|false> --azure <true|false>
#
# <existing-config-path|-> is the current .cenci/config.json contents: a
# file path, or "-" to read from stdin. Absent, empty, or JSON `null`
# existing config is treated as {} so unknown top-level keys elsewhere in
# the file survive a merge unmodified. Prints the full merged config (not
# just the sandbox object) to stdout.
#
# Merge rules:
#   sandbox := .sandbox // {}
#   --dockerfile true  -> sandbox.enabled = true, and delete any legacy
#                          sandbox.baseVersion
#   --dockerfile false -> delete sandbox.enabled and legacy sandbox.baseVersion
#                          (leaves other sandbox keys, including dind, untouched)
#   --dind true         -> sandbox.dind = true
#   --dind false        -> delete sandbox.dind
#                          (leaves other sandbox keys, including enabled/
#                          baseVersion, untouched)
#   --azure true        -> sandbox.azure = true
#   --azure false       -> delete sandbox.azure
#                          (leaves every other sandbox key untouched)
#   an emptied sandbox object ({}) is omitted from the output entirely;
#   otherwise the merged sandbox object is written back in place of the
#   original.
#
# Requires jq on PATH; if absent, fails closed (exit 2) — matches
# detect-project.sh's jq-required convention. Configure's SKILL.md documents
# the four merge outcomes as a manual fallback for the jq-absent case.
set -uo pipefail

usage() {
  echo "usage: merge-sandbox-config.sh <existing-config-path|-> --dockerfile <true|false> --dind <true|false> --azure <true|false>" >&2
  exit 2
}

command -v jq >/dev/null 2>&1 || { echo "merge-sandbox-config.sh: jq is required but was not found on PATH." >&2; exit 2; }

[[ $# -ge 1 ]] || usage
CONFIG_SRC="$1"
shift

DOCKERFILE=""
DIND=""
AZURE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --azure)
      AZURE="${2:-}"
      shift 2 || usage
      ;;
    --dockerfile)
      DOCKERFILE="${2:-}"
      shift 2 || usage
      ;;
    --dind)
      DIND="${2:-}"
      shift 2 || usage
      ;;
    *)
      echo "merge-sandbox-config.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

[[ "$DOCKERFILE" == "true" || "$DOCKERFILE" == "false" ]] \
  || { echo "merge-sandbox-config.sh: --dockerfile must be true or false" >&2; exit 2; }
[[ "$DIND" == "true" || "$DIND" == "false" ]] \
  || { echo "merge-sandbox-config.sh: --dind must be true or false" >&2; exit 2; }
# Required, not defaulted: an omitted --azure defaulting to false would
# silently DELETE an existing sandbox.azure and quietly strip Azure support
# from a repo that had opted in. Fail closed instead, like every other flag.
[[ "$AZURE" == "true" || "$AZURE" == "false" ]] \
  || { echo "merge-sandbox-config.sh: --azure must be true or false" >&2; exit 2; }

if [[ "$CONFIG_SRC" == "-" ]]; then
  if ! EXISTING_RAW="$(cat)"; then
    echo "merge-sandbox-config.sh: unreadable stdin" >&2
    exit 2
  fi
elif [[ -f "$CONFIG_SRC" ]]; then
  if ! EXISTING_RAW="$(cat "$CONFIG_SRC")"; then
    echo "merge-sandbox-config.sh: unreadable existing config: $CONFIG_SRC" >&2
    exit 2
  fi
else
  EXISTING_RAW="{}"
fi
[[ -n "$EXISTING_RAW" ]] || EXISTING_RAW="{}"

EXISTING="$(jq -c '. // {}' <<<"$EXISTING_RAW" 2>/dev/null)" \
  || { echo "merge-sandbox-config.sh: existing config is not valid JSON" >&2; exit 2; }

jq -c \
  --argjson dockerfileOn "$DOCKERFILE" \
  --argjson dindOn "$DIND" \
  --argjson azureOn "$AZURE" \
  '
  (.sandbox // {}) as $sandbox
  | (
      if $dockerfileOn then
        (($sandbox + {enabled: true}) | del(.baseVersion))
      else
        ($sandbox | del(.enabled, .baseVersion))
      end
    ) as $sandbox2
  | (
      if $dindOn then
        $sandbox2 + {dind: true}
      else
        ($sandbox2 | del(.dind))
      end
    ) as $sandbox3
  | (
      if $azureOn then
        $sandbox3 + {azure: true}
      else
        ($sandbox3 | del(.azure))
      end
    ) as $sandbox4
  | if ($sandbox4 | length) == 0 then
      del(.sandbox)
    else
      .sandbox = $sandbox4
    end
  ' <<<"$EXISTING"
