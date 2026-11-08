#!/usr/bin/env bash
set -euo pipefail
# An inherited CDPATH makes `cd` print the directory it picked, corrupting captured paths.
unset CDPATH

ROOT="${1:-.}"
MODE="${2:---preview}"
case "${MODE}" in
  --preview | --apply) ;;
  *)
    echo "unknown mode: ${MODE} (expected --preview or --apply)" >&2
    exit 2
    ;;
esac
CANONICAL="${ROOT}/.cenci/config.json"
LEGACY="${ROOT}/.claude/config.json"
FALLBACK_MARKER='<!-- cenci:agents-md-fallback -->'
TMP="$(mktemp -d "${TMPDIR:-/tmp}/cenci-core.XXXXXX")" || { echo "mktemp failed" >&2; exit 3; }
trap 'rm -rf "${TMP}"' EXIT
# One tab-separated line per planned action: "agents<TAB><rel>AGENTS.md" is a guidance
# file to write, "keep<TAB><rel>CLAUDE.md" and "remove<TAB><rel>CLAUDE.md" are the
# CLAUDE.md decisions. This script never writes a CLAUDE.md and never reads or touches
# CLAUDE.local.md.
PLAN="${TMP}/plan.tsv"
: > "${PLAN}"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 3; }
ROOT_PHYS="$(cd -P -- "${ROOT}" 2>/dev/null && pwd -P)" || { echo "cannot resolve root directory: ${ROOT}" >&2; exit 3; }
mkdir -p "${TMP}/.cenci"

if [[ -f "${LEGACY}" && -f "${CANONICAL}" ]]; then
  jq -s '.[0] * .[1]' "${LEGACY}" "${CANONICAL}" > "${TMP}/.cenci/config.json"
elif [[ -f "${CANONICAL}" ]]; then
  jq -S . "${CANONICAL}" > "${TMP}/.cenci/config.json"
elif [[ -f "${LEGACY}" ]]; then
  jq -S . "${LEGACY}" > "${TMP}/.cenci/config.json"
else
  printf '{}\n' > "${TMP}/.cenci/config.json"
fi

# classify_claude_md <file> -- prints none | fallback | inert | substantive. The same
# rules as step 1 of skills/configure/phases/guidance-settings.md, which configure
# applies to a CLAUDE.md it finds.
#   fallback:    a line equal to the fallback marker (never touched)
#   inert:       no line other than @AGENTS.md, the two claude-only markers and the
#                template comment line; blank lines are ignored
#   substantive: anything else
# A file that exists but cannot be read is reported on stderr and fails (exit 3): it must
# never be classified inert, because inert files are removed.
classify_claude_md() {
  local file="$1" line content marker_rc=0
  if [[ ! -f "${file}" ]]; then
    printf 'none\n'
    return 0
  fi
  content="$(cat -- "${file}")" || { echo "cannot read ${file}" >&2; return 3; }
  # Strip CR once, before both scans, so a CRLF-ended marker or import line matches.
  content="${content//$'\r'/}"
  grep -qxF -- "${FALLBACK_MARKER}" <<<"${content}" || marker_rc=$?
  case "${marker_rc}" in
    0)
      printf 'fallback\n'
      return 0
      ;;
    1) ;;
    *)
      echo "cannot scan ${file} for the fallback marker" >&2
      return 3
      ;;
  esac
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -n "${line//[[:space:]]/}" ]] || continue
    case "${line}" in
      '@AGENTS.md' | '<!-- cenci:claude-only:start -->' | '<!-- cenci:claude-only:end -->' | '<!-- Optional Claude Code-only guidance belongs inside this block. -->') ;;
      *)
        printf 'substantive\n'
        return 0
        ;;
    esac
  done <<<"${content}"
  printf 'inert\n'
}

# propose_guidance <dir> <relative-prefix> -- plans one directory. AGENTS.md comes from
# the existing AGENTS.md, else a lone substantive CLAUDE.md verbatim, else a placeholder.
# Any unreadable CLAUDE.md, and any symlink the plan would copy from or write over,
# aborts the whole run (exit 3) before anything is written.
propose_guidance() {
  local dir="$1" relative="$2" target class
  target="${TMP}/${relative}AGENTS.md"
  mkdir -p "$(dirname "${target}")"
  if [[ -L "${dir}/AGENTS.md" ]]; then
    echo "refusing to copy from or replace a symlink: ${dir}/AGENTS.md" >&2
    exit 3
  fi
  class="$(classify_claude_md "${dir}/CLAUDE.md")" || exit 3
  if [[ -f "${dir}/AGENTS.md" ]]; then
    cp "${dir}/AGENTS.md" "${target}"
  elif [[ "${class}" == "substantive" ]]; then
    if [[ -L "${dir}/CLAUDE.md" ]]; then
      echo "refusing to copy from a symlink: ${dir}/CLAUDE.md" >&2
      exit 3
    fi
    cp "${dir}/CLAUDE.md" "${target}"
  else
    printf '# Project Guidance\n' > "${target}"
  fi
  printf 'agents\t%sAGENTS.md\n' "${relative}" >> "${PLAN}"
  case "${class}" in
    fallback) printf 'keep\t%sCLAUDE.md\n' "${relative}" >> "${PLAN}" ;;
    inert) printf 'remove\t%sCLAUDE.md\n' "${relative}" >> "${PLAN}" ;;
    substantive)
      if [[ -f "${dir}/AGENTS.md" ]]; then
        printf 'keep\t%sCLAUDE.md\n' "${relative}" >> "${PLAN}"
      else
        printf 'remove\t%sCLAUDE.md\n' "${relative}" >> "${PLAN}"
      fi
      ;;
  esac
}

# project_rejection <project> -- prints why a configured project path may not be planned
# (nothing when it may). A project must be a relative path below ROOT that does not
# contain "..", and its physical location, resolved through symlinks, must stay inside
# the physically resolved ROOT. A project that does not exist yet is judged by its deepest
# existing ancestor, which may be ROOT itself.
project_rejection() {
  local project="$1" probe phys
  if [[ -z "${project}" ]]; then
    printf 'empty path\n'
  elif [[ "${project}" == *[[:cntrl:]]* ]]; then
    # The plan file is tab separated and read back word-split, so a leading or trailing
    # tab would vanish and the path used would differ from the path checked.
    printf 'path contains a control character\n'
  elif [[ "${project}" == "." ]]; then
    printf 'the repository root is not a project\n'
  elif [[ "${project}" == /* ]]; then
    printf 'absolute path\n'
  elif [[ "${project}" == *".."* ]]; then
    printf 'path contains ..\n'
  else
    probe="${ROOT}/${project}"
    while [[ ! -e "${probe}" && ! -L "${probe}" ]]; do probe="$(dirname "${probe}")"; done
    if ! phys="$(cd -P -- "${probe}" 2>/dev/null && pwd -P)"; then
      printf 'path cannot be resolved to a directory\n'
    elif [[ "${phys}" == "${ROOT_PHYS}/"* ]]; then
      :
    elif [[ "${phys}" == "${ROOT_PHYS}" && ! -e "${ROOT}/${project}" && ! -L "${ROOT}/${project}" ]]; then
      :
    else
      printf 'resolves outside the repository root\n'
    fi
  fi
}

propose_guidance "${ROOT}" ""
# jq -r prints one path per line, so a path holding a line terminator or NUL would be read
# back as several projects (or a different one). jq rejects those entries before they reach
# the loop; project_rejection still skips any other control character, per project.
paths="$(jq -r '
  (.projects // [])
  | if type == "array" then .[] else error("projects must be an array") end
  | if type == "object" then (.path // "") else error("a projects entry is not an object") end
  | if type == "string" and . != "" then . else error("a project path is empty or not a string") end
  | if contains("\n") or contains("\r") or contains("\u0000") then error("a project path contains a line terminator or NUL") else . end
' "${TMP}/.cenci/config.json")" || { echo "invalid projects in .cenci/config.json" >&2; exit 3; }
if [[ -n "${paths}" ]]; then
  while IFS= read -r project; do
    reason="$(project_rejection "${project}")"
    if [[ -n "${reason}" ]]; then
      printf 'skip: %s (%s)\n' "${project//[[:cntrl:]]/?}" "${reason}" >&2
      continue
    fi
    propose_guidance "${ROOT}/${project}" "${project}/"
  done <<<"${paths}"
fi

# The preview is built in full before anything is printed, so a failure never leaves a
# half-written preview on stdout.
PREVIEW="${TMP}/preview.txt"
echo "Proposed neutral project-core migration:" > "${PREVIEW}"
while IFS=$'\t' read -r action rel; do
  case "${action}" in
    agents)
      diff_rc=0
      diff -uN --label "a/${rel}" --label "b/${rel}" "${ROOT}/${rel}" "${TMP}/${rel}" >> "${PREVIEW}" || diff_rc=$?
      [[ "${diff_rc}" -le 1 ]] || { echo "diff failed for ${ROOT}/${rel}" >&2; exit 3; }
      ;;
    keep) echo "keep: ${ROOT}/${rel}" >> "${PREVIEW}" ;;
    remove) echo "remove: ${ROOT}/${rel}" >> "${PREVIEW}" ;;
  esac
done < "${PLAN}"
cat "${PREVIEW}"
if [[ "${MODE}" != "--apply" ]]; then exit 0; fi

mkdir -p "${ROOT}/.cenci"
install -m 600 "${TMP}/.cenci/config.json" "${CANONICAL}"

# AGENTS.md is written, and confirmed identical, for every directory before any
# CLAUDE.md is removed, so a failed write can never cost the only copy of the guidance.
# An AGENTS.md that already equals the plan is left alone.
while IFS=$'\t' read -r action rel; do
  [[ "${action}" == "agents" ]] || continue
  if [[ ! -f "${ROOT}/${rel}" ]] || ! cmp -s "${TMP}/${rel}" "${ROOT}/${rel}"; then
    install -d "$(dirname "${ROOT}/${rel}")"
    install -m 644 "${TMP}/${rel}" "${ROOT}/${rel}"
  fi
  cmp -s "${TMP}/${rel}" "${ROOT}/${rel}" || { echo "AGENTS.md write could not be verified: ${ROOT}/${rel}" >&2; exit 1; }
done < "${PLAN}"
while IFS=$'\t' read -r action rel; do
  [[ "${action}" == "remove" ]] || continue
  rm -f -- "${ROOT}/${rel}"
  echo "removed: ${ROOT}/${rel}"
done < "${PLAN}"
