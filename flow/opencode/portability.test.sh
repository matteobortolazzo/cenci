#!/usr/bin/env bash
# Drift guard: the README "Skill portability" table's OpenCode column and
# install-skills.sh's PORTABLE_SKILLS list must name exactly the same skills,
# mirroring the workflow/skill-file consistency checks in
# flow/codex/workflows.test.sh and flow/codex/acceptance.test.sh.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
README="$ROOT/README.md"
SCRIPT="$ROOT/opencode/install-skills.sh"
failures=0
fail(){ echo "FAIL: $1" >&2; failures=$((failures+1)); }

if [[ ! -f "$SCRIPT" ]]; then
  fail "install-skills.sh does not exist at $SCRIPT"
fi

if [[ ! -f "$README" ]]; then
  fail "README.md does not exist at $README"
fi

script_skills=""
if [[ -f "$SCRIPT" ]]; then
  script_skills="$(grep '^PORTABLE_SKILLS=' "$SCRIPT" 2>/dev/null \
    | sed -E 's/^PORTABLE_SKILLS="(.*)"$/\1/' \
    | tr ' ' '\n' | sed '/^$/d' | sort)"
  if [[ -z "$script_skills" ]]; then
    fail "install-skills.sh has no PORTABLE_SKILLS declaration to compare against the README"
  fi
fi

if [[ -f "$README" ]]; then
  header="$(awk '
    /^## Skill portability/ {insection=1}
    insection && /^\| *Skill *\|/ {print; exit}
  ' "$README")"

  if [[ -z "$header" ]]; then
    fail "README.md has no 'Skill portability' table"
  elif ! grep -q 'OpenCode' <<<"$header"; then
    fail "README.md Skill portability table has no OpenCode column"
  fi

  readme_yes_skills="$(awk -F'|' '
    /^## Skill portability/ {insection=1}
    insection && /^\| *Skill *\|/ {
      for (i=1;i<=NF;i++) {
        col=$i; gsub(/^[ \t]+|[ \t]+$/, "", col)
        if (col == "OpenCode") ocidx=i
      }
      next
    }
    insection && /^\|*-+/ {next}
    insection && ocidx && /^\|/{
      name=$2; gsub(/^[ \t]+|[ \t]+$/, "", name); gsub(/`/, "", name)
      val=$ocidx; gsub(/^[ \t]+|[ \t]+$/, "", val)
      if (val == "Yes") print name
    }
  ' "$README" | sort)"

  if [[ -n "$script_skills" ]]; then
    if ! diff <(printf '%s\n' "$script_skills") <(printf '%s\n' "$readme_yes_skills") >/dev/null; then
      fail "README OpenCode 'Yes' skills and install-skills.sh PORTABLE_SKILLS disagree"
      diff <(printf '%s\n' "$script_skills") <(printf '%s\n' "$readme_yes_skills") >&2 || true
    fi
  fi
fi

echo "portability.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
