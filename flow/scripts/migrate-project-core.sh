#!/usr/bin/env bash
set -euo pipefail

ROOT="${1:-.}"
MODE="${2:---preview}"
CANONICAL="${ROOT}/.cenci/config.json"
LEGACY="${ROOT}/.claude/config.json"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/cenci-core.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

command -v jq >/dev/null || { echo "jq is required" >&2; exit 3; }
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

propose_guidance() {
  local dir="$1" relative="$2" source="" target
  target="${TMP}/${relative}AGENTS.md"
  mkdir -p "$(dirname "${target}")"
  if [[ -f "${dir}/AGENTS.md" ]]; then source="${dir}/AGENTS.md"
  elif [[ -f "${dir}/CLAUDE.md" ]]; then source="${dir}/CLAUDE.md"
  fi
  if [[ -n "${source}" ]]; then cp "${source}" "${target}"; else printf '# Project Guidance\n' > "${target}"; fi
  printf '@AGENTS.md\n\n<!-- cenci:claude-only:start -->\n<!-- cenci:claude-only:end -->\n' > "${TMP}/${relative}CLAUDE.md"
}

propose_guidance "${ROOT}" ""
if [[ -f "${TMP}/.cenci/config.json" ]]; then
  while IFS= read -r project; do
    [[ -n "${project}" && "${project}" != "." && "${project}" != *".."* ]] || continue
    propose_guidance "${ROOT}/${project}" "${project}/"
  done < <(jq -r '.projects[]?.path // empty' "${TMP}/.cenci/config.json")
fi

echo "Proposed neutral project-core migration:"
diff -ruN --exclude config.json "${ROOT}" "${TMP}" || true
if [[ "${MODE}" != "--apply" ]]; then exit 0; fi

mkdir -p "${ROOT}/.cenci"
install -m 600 "${TMP}/.cenci/config.json" "${CANONICAL}"
install -m 644 "${TMP}/AGENTS.md" "${ROOT}/AGENTS.md"
install -m 644 "${TMP}/CLAUDE.md" "${ROOT}/CLAUDE.md"
while IFS= read -r project; do
  [[ -n "${project}" && "${project}" != "." && "${project}" != *".."* ]] || continue
  install -d "${ROOT}/${project}"
  install -m 644 "${TMP}/${project}/AGENTS.md" "${ROOT}/${project}/AGENTS.md"
  install -m 644 "${TMP}/${project}/CLAUDE.md" "${ROOT}/${project}/CLAUDE.md"
done < <(jq -r '.projects[]?.path // empty' "${TMP}/.cenci/config.json")
