#!/usr/bin/env bash
# Resolve independent refinement controls from project-core's config snapshot.
# Output is JSON; malformed present controls fail closed with visible stderr notices.
set -euo pipefail
if [[ $# -ne 1 ]]; then
  echo 'Usage: resolve-policy.sh <resolved-config.json>' >&2
  exit 2
fi
resolved=$(jq -c -s '
  if length != 1 or (.[0] | type) != "object" then
    error("expected one configuration object")
  else .[0] end
  | (has("refinement") and (.refinement | type) != "object") as $invalidBlock
  | (if (.refinement | type) == "object" then .refinement else {} end) as $r
  | {
      confirmBeforeApply: (if $invalidBlock then true elif ($r | has("confirmBeforeApply")) then
        if ($r.confirmBeforeApply | type) == "boolean" then $r.confirmBeforeApply else true end
        else false end),
      automerge: (if $r.automerge == "eligible" then "eligible" else "off" end),
      notices: [
        (if $invalidBlock then "refinement invalid; using confirmBeforeApply=true, automerge=off" else empty end),
        (if ($r | has("confirmBeforeApply")) and ($r.confirmBeforeApply | type) != "boolean"
         then "refinement.confirmBeforeApply invalid; using true" else empty end),
        (if ($r | has("automerge")) and $r.automerge != "off" and $r.automerge != "eligible"
         then "refinement.automerge invalid; using off" else empty end)
      ]
    }
' "$1") || { echo 'Unable to resolve refinement policy; stop before writes.' >&2; exit 2; }
jq -r '.notices[]' <<< "$resolved" >&2
jq -c 'del(.notices)' <<< "$resolved"
