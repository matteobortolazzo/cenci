#!/usr/bin/env bash
# validate-agent-roles.sh — schema validation for Codex agent-role TOML files
# (#1040). Earlier tests only grepped for the two fields that previously broke
# (description added by #409, name added by #422) — a regression pin, not
# real schema validation. It caught neither a duplicate `name` across files
# nor an unknown top-level key, and would not catch the next required field
# Codex adds either.
#
# Usage:
#   validate-agent-roles.sh [--plain] <dir> [<dir> ...]
#
# Every *.toml file directly under each given directory is parsed and
# checked. Pass every directory that must share one Codex "config layer" (the
# same call: this repo runs it against both .codex/agents/ and
# templates/codex/agent-roles/ together) so a duplicate `name` across
# directories is caught directly, not just guarded against by keeping the
# directories differently named.
#
# Checks enforced per file:
#   - valid TOML syntax
#   - required non-empty string keys: name, description, developer_instructions
#   - no unknown top-level keys (known set below)
#   - name equals the filename stem
#   - name unique across every file passed in, across all directories
#
# Modes:
#   (default)  human-readable: one "FAIL: <message>" line per finding to
#              stderr; "<n> agent role file(s) OK" to stdout when clean.
#   --plain    one finding per line on stdout, nothing when clean — for
#              scripted consumers (this script's own tests and the
#              check-agent-role-drift.sh hook/maintain check).
#
# Exit codes: 0 when every file is clean, 1 when any finding exists, 2 for
# usage errors or a missing/broken python3 tomllib (fails closed rather than
# silently skipping validation).
set -uo pipefail

MODE="human"
DIRS=()
for arg in "$@"; do
  case "$arg" in
    --plain) MODE="plain" ;;
    *) DIRS+=("$arg") ;;
  esac
done

if [[ "${#DIRS[@]}" -eq 0 ]]; then
  echo "usage: validate-agent-roles.sh [--plain] <dir> [<dir> ...]" >&2
  exit 2
fi

command -v python3 >/dev/null 2>&1 || {
  echo "validate-agent-roles.sh: python3 not found on PATH" >&2
  exit 2
}
python3 -c 'import tomllib' >/dev/null 2>&1 || {
  echo "validate-agent-roles.sh: python3's tomllib module is unavailable (needs Python 3.11+)" >&2
  exit 2
}

FILES=()
for dir in "${DIRS[@]}"; do
  [[ -d "$dir" ]] || { echo "validate-agent-roles.sh: not a directory: $dir" >&2; exit 2; }
  while IFS= read -r -d '' f; do
    FILES+=("$f")
  done < <(find "$dir" -maxdepth 1 -name '*.toml' -print0 | sort -z)
done

if [[ "${#FILES[@]}" -eq 0 ]]; then
  echo "validate-agent-roles.sh: no *.toml files found in: ${DIRS[*]}" >&2
  exit 2
fi

FINDINGS="$(python3 - "${FILES[@]}" <<'PYEOF'
import sys
import tomllib

KNOWN_KEYS = {"name", "description", "model", "model_reasoning_effort", "developer_instructions"}
REQUIRED_STRING_KEYS = ["name", "description", "developer_instructions"]

files = sys.argv[1:]
findings = []
names = {}  # name -> list of files declaring it

for path in files:
    stem = path.rsplit("/", 1)[-1].removesuffix(".toml")
    try:
        with open(path, "rb") as fh:
            data = tomllib.load(fh)
    except tomllib.TOMLDecodeError as exc:
        findings.append(f"invalid-toml {path}: {exc}")
        continue
    except OSError as exc:
        findings.append(f"unreadable {path}: {exc}")
        continue

    for key in REQUIRED_STRING_KEYS:
        value = data.get(key)
        if not isinstance(value, str) or value.strip() == "":
            findings.append(f"missing-field {path}: '{key}' must be a non-empty string")

    unknown = sorted(set(data.keys()) - KNOWN_KEYS)
    for key in unknown:
        findings.append(f"unknown-key {path}: '{key}' is not a recognized agent-role field")

    name = data.get("name")
    if isinstance(name, str) and name.strip() != "" and name != stem:
        findings.append(f"name-mismatch {path}: name '{name}' does not match filename stem '{stem}'")

    if isinstance(name, str) and name.strip() != "":
        names.setdefault(name, []).append(path)

for name, paths in names.items():
    if len(paths) > 1:
        findings.append(f"duplicate-name {name}: declared in {', '.join(paths)}")

for finding in findings:
    print(finding)
PYEOF
)"

if [[ -z "$FINDINGS" ]]; then
  if [[ "$MODE" == "human" ]]; then
    echo "${#FILES[@]} agent role file(s) OK"
  fi
  exit 0
fi

if [[ "$MODE" == "plain" ]]; then
  printf '%s\n' "$FINDINGS"
else
  while IFS= read -r line; do
    echo "FAIL: $line" >&2
  done <<< "$FINDINGS"
fi
exit 1
