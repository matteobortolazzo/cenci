#!/usr/bin/env bash
# validate-agent-roles.sh — classifier for Codex agent-role TOML files (#1040,
# reworked by #1191). Earlier versions checked a closed five-key list, so a valid
# role with `sandbox_mode`, `mcp_servers` or `skills.config` failed, a `name`
# differing from the filename failed, and identical names in different
# configuration layers failed. This version fails only on a proven type or
# required-field error and on a same-layer duplicate `name`.
#
# Usage:
#   validate-agent-roles.sh [--plain] <dir> [<dir> ...]
#
# Every *.toml file under each given directory is parsed and checked, recursively
# and in sorted full-path order; symlinked subdirectories are not followed.
# Each directory argument is one configuration layer (Codex treats
# ~/.codex/agents/ and .codex/agents/ as separate layers), and nested files belong
# to the layer of the directory argument they sit under, so a `name` repeated
# across directories is NOT a duplicate; a `name` repeated inside one directory
# (including its subdirectories) is.
#
# Documented key subset, version-stamped: Codex docs 2026-11, Codex CLI 0.144.1+.
# This is a documented subset, not a claim to be the full schema. Anything else is
# reported as unverified, never invalid.
#   - name, description, developer_instructions: required non-empty strings
#   - model, model_reasoning_effort: strings (model names are never checked
#     offline; an effort outside minimal/low/medium/high/xhigh is unverified
#     because supported efforts vary by model and version)
#   - sandbox_mode: string, one of read-only / workspace-write / danger-full-access
#   - mcp_servers, skills: tables; skills.config: an array of tables
#     ([[skills.config]]), entries checked only for being tables
#
# Finding kinds, one per output line, "<kind> <subject>: <detail>":
#   failing:      invalid-toml unreadable missing-field invalid-type invalid-value
#                 duplicate-name
#   not failing:  unverified-key unverified-value name-convention
#
# Modes:
#   (default)  human-readable: "FAIL: <finding>" to stderr per failing finding,
#              "NOTE: <finding>" to stdout per note, and "<n> agent role file(s)
#              OK" to stdout when nothing fails.
#   --plain    one finding per line on stdout, nothing when clean — for scripted
#              consumers (this script's tests and check-agent-role-drift.sh).
#
# Exit codes: 0 no failing finding (clean or notes only), 1 at least one failing
# finding, 2 usage error (no directory, not a directory, no *.toml), 3 validation
# unavailable (python3 missing or without tomllib; one `cenci: …` line on stderr
# naming Python 3.11+, mirroring flow/codex/require-runtime.sh).
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

unavailable() {  # unavailable <found> — exit 3 with the one-line prerequisite message
  printf 'cenci: Codex agent-role validation unavailable — Python 3.11+ with tomllib not found on PATH (found: %s). Install Python 3.11 or newer, then rerun; no agent-role file was changed.\n' "$1" >&2
  exit 3
}

# Report only a validated X.Y.Z, rebuilt from digits, so version text from a broken
# python3 is never echoed or evaluated.
python_version_or_marker() {
  local raw major minor patch
  raw="$(python3 -I -c 'import sys; print("%d.%d.%d" % sys.version_info[:3])' 2>/dev/null)" || raw=""
  major="${raw%%.*}"
  minor="${raw#*.}"; minor="${minor%%.*}"
  patch="${raw#*.*.}"
  case "$raw" in
    *.*.*) ;;
    *) printf '%s' "no tomllib"; return ;;
  esac
  case "$major$minor$patch" in
    '' | *[!0-9]*) printf '%s' "no tomllib"; return ;;
  esac
  if [[ -z "$major" || -z "$minor" || -z "$patch" || "${#major}" -gt 6 || "${#minor}" -gt 6 || "${#patch}" -gt 6 ]]; then
    printf '%s' "no tomllib"; return
  fi
  printf '%s.%s.%s' "$major" "$minor" "$patch"
}

if ! command -v python3 >/dev/null 2>&1; then
  unavailable "none"
fi
# python3 runs in isolated mode (-I) everywhere: hooks and maintain run from the repo
# root, and without -I the cwd leads sys.path so a repo-planted tomllib.py would run.
if ! python3 -I -c 'import tomllib' >/dev/null 2>&1; then
  unavailable "$(python_version_or_marker)"
fi

FILES=()
for i in "${!DIRS[@]}"; do
  dir="${DIRS[$i]}"
  [[ -d "$dir" ]] || { echo "validate-agent-roles.sh: not a directory: $dir" >&2; exit 2; }
  while IFS= read -r -d '' f; do
    FILES+=("${i}:${f}")
  done < <(find "$dir" -name '*.toml' -print0 | LC_ALL=C sort -z)
done

if [[ "${#FILES[@]}" -eq 0 ]]; then
  echo "validate-agent-roles.sh: no *.toml files found in: ${DIRS[*]}" >&2
  exit 2
fi

FINDINGS="$(python3 -I - "${FILES[@]}" 2>/dev/null <<'PYEOF'
import sys
import tomllib

sys.stdout.reconfigure(errors="backslashreplace")

REQUIRED_STRING_KEYS = ["name", "description", "developer_instructions"]
SANDBOX_MODES = ("read-only", "workspace-write", "danger-full-access")
EFFORTS = ("minimal", "low", "medium", "high", "xhigh")
KNOWN_KEYS = {
    "name", "description", "developer_instructions", "model",
    "model_reasoning_effort", "sandbox_mode", "mcp_servers", "skills",
}


def q(value):
    """Escape control characters so one finding is always exactly one line."""
    return "".join(
        c if not (ord(c) < 32 or 0x7F <= ord(c) <= 0x9F) else "\\x%02x" % ord(c)
        for c in str(value)
    )


findings = []
layers = {}  # layer index -> {name -> [paths]}

for arg in sys.argv[1:]:
    layer, path = arg.split(":", 1)
    shown = q(path)
    stem = path.rsplit("/", 1)[-1].removesuffix(".toml")
    try:
        with open(path, "rb") as fh:
            data = tomllib.load(fh)
    except OSError as exc:
        findings.append(f"unreadable {shown}: {q(exc)}")
        continue
    except ValueError as exc:  # TOMLDecodeError and UnicodeDecodeError
        findings.append(f"invalid-toml {shown}: {q(exc)}")
        continue
    except (RecursionError, MemoryError):  # pathological file: fail this file only
        findings.append(f"invalid-toml {shown}: file is nested too deeply or could not be parsed within limits")
        continue
    # Any other exception propagates: python exits non-zero and the caller reports
    # exit 3 (found: unusable) — a broken tomllib is an environment fault.

    for key in REQUIRED_STRING_KEYS:
        value = data.get(key)
        if not isinstance(value, str) or value.strip() == "":
            findings.append(f"missing-field {shown}: '{key}' must be a non-empty string")

    if "model" in data and not isinstance(data["model"], str):
        findings.append(f"invalid-type {shown}: 'model' must be a string")

    if "model_reasoning_effort" in data:
        effort = data["model_reasoning_effort"]
        if not isinstance(effort, str):
            findings.append(f"invalid-type {shown}: 'model_reasoning_effort' must be a string")
        elif effort not in EFFORTS:
            findings.append(
                f"unverified-value {shown}: 'model_reasoning_effort' value '{q(effort)}' "
                f"is not one of {'/'.join(EFFORTS)}"
            )

    if "sandbox_mode" in data:
        mode = data["sandbox_mode"]
        if not isinstance(mode, str):
            findings.append(f"invalid-type {shown}: 'sandbox_mode' must be a string")
        elif mode not in SANDBOX_MODES:
            findings.append(
                f"invalid-value {shown}: 'sandbox_mode' value '{q(mode)}' "
                f"must be one of {'/'.join(SANDBOX_MODES)}"
            )

    if "mcp_servers" in data and not isinstance(data["mcp_servers"], dict):
        findings.append(f"invalid-type {shown}: 'mcp_servers' must be a table")

    if "skills" in data:
        skills = data["skills"]
        if not isinstance(skills, dict):
            findings.append(
                f"invalid-type {shown}: 'skills' must be a table (skills.config cannot exist under it)"
            )
        else:
            for sub in sorted(skills):
                if sub == "config":
                    config = skills[sub]
                    if not isinstance(config, list):
                        findings.append(f"invalid-type {shown}: 'skills.config' must be an array of tables")
                    else:
                        for idx, entry in enumerate(config):
                            if not isinstance(entry, dict):
                                findings.append(f"invalid-type {shown}: 'skills.config[{idx}]' must be a table")
                else:
                    findings.append(
                        f"unverified-key {shown}: 'skills.{q(sub)}' is not in the documented key subset"
                    )

    for key in sorted(set(data) - KNOWN_KEYS):
        findings.append(f"unverified-key {shown}: '{q(key)}' is not in the documented key subset")

    name = data.get("name")
    if isinstance(name, str) and name.strip() != "":
        if name != stem:
            findings.append(
                f"name-convention {shown}: name '{q(name)}' differs from filename stem '{q(stem)}' (advisory)"
            )
        layers.setdefault(layer, {}).setdefault(name, []).append(shown)

for layer in layers:
    for name, paths in layers[layer].items():
        if len(paths) > 1:
            findings.append(f"duplicate-name {q(name)}: declared in {', '.join(sorted(paths))}")

for finding in findings:
    print(finding)
PYEOF
)"
PY_RC=$?
if [[ "$PY_RC" -ne 0 ]]; then
  unavailable "unusable"
fi

FAILING=0
NOTES=()
FAILS=()
if [[ -n "$FINDINGS" ]]; then
  while IFS= read -r line; do
    kind="${line%% *}"
    case "$kind" in
      unverified-key | unverified-value | name-convention) NOTES+=("$line") ;;
      *) FAILS+=("$line"); FAILING=1 ;;
    esac
  done <<< "$FINDINGS"
fi

if [[ "$MODE" == "plain" ]]; then
  [[ -z "$FINDINGS" ]] || printf '%s\n' "$FINDINGS"
else
  for line in "${FAILS[@]+"${FAILS[@]}"}"; do
    echo "FAIL: $line" >&2
  done
  for line in "${NOTES[@]+"${NOTES[@]}"}"; do
    echo "NOTE: $line"
  done
  if [[ "$FAILING" -eq 0 ]]; then
    echo "${#FILES[@]} agent role file(s) OK"
  fi
fi

exit "$FAILING"
