#!/usr/bin/env bash
set -euo pipefail
SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/migrate-project-core.sh"
ROOT="$(mktemp -d)"; trap 'rm -rf "${ROOT}"' EXIT

mkdir -p "${ROOT}/legacy/.claude"
printf '{"unknown":{"nested":true},"branchPattern":"old"}\n' > "${ROOT}/legacy/.claude/config.json"
printf '# Existing rules\n' > "${ROOT}/legacy/CLAUDE.md"
"${SCRIPT}" "${ROOT}/legacy" --apply >/dev/null
jq -e '.unknown.nested == true' "${ROOT}/legacy/.cenci/config.json" >/dev/null
grep -q 'Existing rules' "${ROOT}/legacy/AGENTS.md"
grep -q '^@AGENTS.md' "${ROOT}/legacy/CLAUDE.md"

mkdir -p "${ROOT}/both/.claude" "${ROOT}/both/.cenci"
printf '{"unknown":{"legacy":1},"value":"legacy"}\n' > "${ROOT}/both/.claude/config.json"
printf '{"unknown":{"canonical":2},"value":"canonical","projects":[{"path":"apps/api"}]}\n' > "${ROOT}/both/.cenci/config.json"
"${SCRIPT}" "${ROOT}/both" --apply >/dev/null
jq -e '.value == "canonical" and .unknown.legacy == 1 and .unknown.canonical == 2' "${ROOT}/both/.cenci/config.json" >/dev/null
test -f "${ROOT}/both/apps/api/AGENTS.md"
before="$(sha256sum "${ROOT}/both/.cenci/config.json" "${ROOT}/both/AGENTS.md")"
"${SCRIPT}" "${ROOT}/both" --apply >/dev/null
after="$(sha256sum "${ROOT}/both/.cenci/config.json" "${ROOT}/both/AGENTS.md")"
test "${before}" = "${after}"

echo "migrate-project-core.test.sh: passed"
