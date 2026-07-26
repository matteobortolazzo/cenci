#!/usr/bin/env bash
# Seed a dedicated demo repo for the cenci README GIF (demo.tape) and the
# static screenshots — mirrors lazyboards' docs/demo-repo-seed.sh toolchain.
#
# Usage:
#   1. Create the repo first:  gh repo create <you>/lazyboards-demo --public --clone
#      (the same demo repo lazyboards' own README GIF records against)
#   2. cd lazyboards-demo
#   3. Run this script:  bash /path/to/cenci/docs/demo-repo-seed.sh
#
# Issue numbers matter: demo.tape and docs/demo-cenci-fake.py reference
# #12/#9 (New) and #6 (Refined) by number, so run this against a fresh repo
# where issues get numbered 1..12 in creation order. Re-running appends
# issues and breaks the numbering — clear the repo first for a clean slate.

set -euo pipefail

if ! gh repo view >/dev/null 2>&1; then
  echo "error: run this inside a cloned gh repo" >&2
  exit 1
fi

echo "==> Ensuring labels exist"
create_label() {
  local name="$1" color="$2" desc="${3:-}"
  gh label create "$name" --color "$color" --description "$desc" 2>/dev/null \
    || gh label edit   "$name" --color "$color" --description "$desc" >/dev/null
}

# Column labels — match the per-repo lazyboards board cenci generates
# (flow/skills/configure/SKILL.md step 5f / docs/orchestration.md), minus the
# optional Designed and Implemented columns the demo board doesn't pin.
create_label "New"       "c5def5" "Incoming, not yet triaged"
create_label "Refined"   "bfd4f2" "Requirements clarified, ready to plan"
create_label "Planned"   "d4c5f9" "Approved plan committed, ready to dispatch"
create_label "In Review" "0e8a16" "PR open, awaiting review"

# Extra labels for flavour
create_label "bug"          "d73a4a" "Something isn't working"
create_label "enhancement"  "a2eeef" "New feature or request"
create_label "docs"         "0075ca" "Documentation change"

echo "==> Creating issues (order matters — numbering is referenced by demo.tape)"

new_issue() {
  local title="$1" label="$2" body="$3"
  gh issue create --title "$title" --label "$label" --body "$body" >/dev/null
  echo "  + $title  [$label]"
}

# ---- #1-#3: New ----
new_issue "Dark mode toggle in settings" \
  "New,enhancement" \
  "Users want to switch between light and dark themes at runtime without restarting the app."

new_issue "Export notes as Markdown" \
  "New,enhancement" \
  "Add a bulk export action that writes every note to a folder of \`.md\` files with YAML frontmatter."

new_issue "Crash when opening empty workspace" \
  "New,bug" \
  "Steps to reproduce:
1. Launch the app with no workspace argument
2. Close the welcome modal
3. Nil pointer panic in \`workspace.Current()\`

Regression since 0.4.2."

# ---- #4-#6: Refined ----
new_issue "Support nested tags (tag/subtag)" \
  "Refined,enhancement" \
  "## Summary

Allow tags like \`project/alpha\` to group under a parent \`project\` node in the sidebar.

## Acceptance criteria

- Parser splits on \`/\` into a tree
- Sidebar renders expandable nodes
- Collapsing state persists across restarts
- Backwards compatible with flat tags"

new_issue "Keyboard shortcut for quick switcher" \
  "Refined,enhancement" \
  "Bind \`Ctrl+P\` to open a fuzzy-find palette across notes and tags. Reuse the existing command palette component."

new_issue "Replace deprecated encoding/json with v2" \
  "Refined" \
  "Swap \`encoding/json\` for \`encoding/json/v2\` where available. Benchmarks show a 30-40% decode speedup on our largest fixtures."

# ---- #7-#12: New ----
new_issue "Sync engine: conflict resolution UI" \
  "New,enhancement" \
  "When two devices edit the same note offline, the sync engine picks last-write-wins silently. Users have asked for a three-way merge UI."

new_issue "Fix flaky test in auth middleware" \
  "New,bug" \
  "\`TestAuth_RejectsExpiredToken\` fails ~1 in 20 runs on CI. Suspected race in the token cache eviction goroutine."

new_issue "Add JSON logging mode" \
  "New,enhancement" \
  "Structured logs for production environments. Enabled via \`LOG_FORMAT=json\`."

new_issue "Fix typo in onboarding email" \
  "New,docs" \
  "The welcome email says \"vist your dashboard\" — should be \"visit\"."

new_issue "Rate limit public API endpoints" \
  "New,enhancement" \
  "Add a token-bucket rate limiter to the public REST API. Return \`429\` with a \`Retry-After\` header when exceeded."

new_issue "Migrate CI from Travis to GitHub Actions" \
  "New" \
  "Travis is being sunset for open-source. Port the build matrix and caching to GitHub Actions."

echo "==> Done. Run 'lazyboards' in this repo to verify the board."
