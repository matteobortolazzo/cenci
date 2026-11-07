#!/usr/bin/env bash
# Contract test for ticket #1200 lane 2 — the doc acceptance criteria that no
# behavioral test can pin: the working-directory contract for buildCommand /
# testCommand / lintCommand, its pointers, the lint-command check prose in
# docs/health-gates.md, the worktree .gitignore false-pass bullet, and the
# Claude-only advisory note in docs/codex.md.
#
# Each assertion greps ONE exact phrase (grep -F, so it must sit on one source
# line, gotcha #555) in the real committed doc, then pins the ordering of the
# new health-gates sections and that the false-pass bullet names no tool beyond
# CSharpier, all against the real doc. The real docs are never edited.
#
# Follows the `failures=` counter style of flow/hooks/scripts/check-config-staleness.test.sh.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "lint-command-docs-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)" || { echo "lint-command-docs-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

CONFIG_SCHEMA="flow/skills/configure/references/config-schema.md"
PHASE_4="flow/skills/implement/phases/phase-4-implement-green.md"
PHASE_5="flow/skills/implement/phases/phase-5-refactor.md"
PHASE_9="flow/skills/implement/phases/phase-9-pr.md"
IMPLEMENTER="flow/agents/implementer.md"
HEALTH_GATES="docs/health-gates.md"
CODEX_DOC="flow/docs/codex.md"

# doc_has <path> <phrase> -- true when the phrase sits on one line of the file.
doc_has() { grep -qF -- "$2" "$1" 2>/dev/null; }

# line_of <path> <phrase> -- first matching line number, empty when absent.
line_of() { grep -nF -m1 -- "$2" "$1" 2>/dev/null | cut -d: -f1; }

# expect <repo-relative-path> <phrase> -- asserts the phrase is in the doc.
expect() {
  doc_has "${REPO_DIR}/$1" "$2" || fail "$1: missing phrase [$2]"
}

# --- config-schema.md: the contract, the script forms, the detection note ---
expect "${CONFIG_SCHEMA}" 'run with the working directory set to `<worktree>/<projects[].path>`'
expect "${CONFIG_SCHEMA}" '(the worktree root for a single-repo config)'
expect "${CONFIG_SCHEMA}" 'the same resolution `hooks/scripts/run-gate.sh` applies to `gateCommand`'
expect "${CONFIG_SCHEMA}" 'a static relative path from the project directory'
expect "${CONFIG_SCHEMA}" '`sh ../scripts/gate.sh api`'
expect "${CONFIG_SCHEMA}" 'root=$(git rev-parse --show-toplevel) && [ -n "$root" ] && sh "$root/scripts/gate.sh" api'
expect "${CONFIG_SCHEMA}" 'never an unchecked `$(git rev-parse --show-toplevel)` substitution'
expect "${CONFIG_SCHEMA}" "maintain's \`lint-command\` check and the SessionStart advisory (\`flow/hooks/scripts/check-lint-command.sh\`)"
expect "${CONFIG_SCHEMA}" 'detection is report-only: nothing writes `.cenci/config.json`'

# --- phase docs: pointer at each handover/run site ---
for f in "${PHASE_4}" "${PHASE_5}" "${PHASE_9}"; do
  expect "$f" 'working-directory contract in `skills/configure/references/config-schema.md`'
done

# --- implementer.md: the per-client form and the cd-compound precedence ---
expect "${IMPLEMENTER}" 'run from the project directory inside the worktree'
expect "${IMPLEMENTER}" "set the client's \`workdir\` to that directory"
expect "${IMPLEMENTER}" 'one `cd "<absolute project dir>" && <command>` call'
expect "${IMPLEMENTER}" 'any log redirect on it uses an absolute target'
expect "${IMPLEMENTER}" 'Claude Code may ask for approval on that combination'
expect "${IMPLEMENTER}" 'the contract in `skills/configure/references/config-schema.md`'
expect "${IMPLEMENTER}" '**Never prefix git with `cd`.**'

# --- docs/health-gates.md: lint-command check paragraph + working directory ---
expect "${HEALTH_GATES}" 'Each target resolves to one of three outcomes: `pass`, `fail` or `skip`'
expect "${HEALTH_GATES}" 'a single-repo config checks `stack.framework`, `stack.backend` and `stack.frontend` against the top-level `lintCommand`'
expect "${HEALTH_GATES}" 'a monorepo reads only each `projects[].lintCommand` with no top-level fallback'
expect "${HEALTH_GATES}" 'an absent `lintCommand` still skips cleanly at runtime'
expect "${HEALTH_GATES}" 'The check is report-only'
expect "${HEALTH_GATES}" 'the contract is stated in `flow/skills/configure/references/config-schema.md`'

# --- docs/health-gates.md: the worktree .gitignore false-pass bullet ---
expect "${HEALTH_GATES}" "When a gate's tool resolves its own file set"
expect "${HEALTH_GATES}" 'applies `.gitignore` files from directories above the worktree root matches zero files inside `.worktrees/<id>-<desc>/` and exits `0`'
expect "${HEALTH_GATES}" "puts \`.worktrees/\` in the main checkout's \`.gitignore\`"
expect "${HEALTH_GATES}" 'CSharpier is the reported instance'
expect "${HEALTH_GATES}" '`Checked 0 files`'
expect "${HEALTH_GATES}" 'Verify a gate by its reported file count, not its exit code'
expect "${HEALTH_GATES}" 'pass an explicit file list or an ignore-path override'
expect "${HEALTH_GATES}" "Velka's changed-files workaround"
expect "${HEALTH_GATES}" 'reads only `./.gitignore` and `./.prettierignore` in the directory it runs from (prettier.io/docs/cli, `--ignore-path`)'
expect "${HEALTH_GATES}" "cannot be verified against this repo's own gates, because none of them uses a \`.gitignore\`-honoring tool"

# --- flow/docs/codex.md: the Claude-only advisory note ---
expect "${CODEX_DOC}" 'The `lint-command` advisory'
expect "${CODEX_DOC}" "is wired only into Claude Code's SessionStart, like the config-staleness and agent-role-drift advisories"

# --- Ordering and scope pins in docs/health-gates.md ---
hg="${REPO_DIR}/${HEALTH_GATES}"
scoped_line="$(line_of "${hg}" '#994')"
leadin_line="$(line_of "${hg}" "When a gate's tool resolves its own file set")"
if [[ -z "${scoped_line}" || -z "${leadin_line}" || "${leadin_line}" -le "${scoped_line}" ]]; then
  fail "${HEALTH_GATES}: the worktree false-pass lead-in (line ${leadin_line:-none}) must follow the #994 scoped-runs block (line ${scoped_line:-none})"
fi

gate_consumer_line="$(line_of "${hg}" 'The maintain checker'"'"'s `gate-command` check')"
lint_para_line="$(line_of "${hg}" 'Each target resolves to one of three outcomes')"
contract_line="$(line_of "${hg}" '## Exit-0-is-healthy contract')"
if [[ -z "${gate_consumer_line}" || -z "${lint_para_line}" || -z "${contract_line}" \
  || "${lint_para_line}" -le "${gate_consumer_line}" || "${lint_para_line}" -ge "${contract_line}" ]]; then
  fail "${HEALTH_GATES}: the lint-command paragraph (line ${lint_para_line:-none}) must sit after the gate-command consumer bullet (line ${gate_consumer_line:-none}) and before '## Exit-0-is-healthy contract' (line ${contract_line:-none})"
fi

# The bullet names CSharpier as the reported instance and no other tool as affected.
csharpier_line_text="$(grep -F -m1 -- 'CSharpier is the reported instance' "${hg}" 2>/dev/null || true)"
if [[ -z "${csharpier_line_text}" ]]; then
  fail "${HEALTH_GATES}: CSharpier bullet line not found for the no-other-tool check"
elif [[ "${csharpier_line_text}" == *ESLint* ]]; then
  fail "${HEALTH_GATES}: the false-pass bullet must not name ESLint (or any tool beyond CSharpier) as affected"
fi

echo "lint-command-docs-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
