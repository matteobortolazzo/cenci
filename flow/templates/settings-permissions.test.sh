#!/usr/bin/env bash
# Tests for the shipped settings.json permission lists. Claude Code's file
# permission checker only matches scoped-path Edit(<path>) rules, not
# scoped-path Write(<path>) ones — a scoped Write(...) entry is dead weight
# that produces a startup warning, in permissions.allow and permissions.deny
# alike. Follows the repo's shell-test precedent
# (flow/hooks/scripts/guard-main-worktree.test.sh): plain bash, no
# framework, PASS/FAIL counters, non-zero exit on failure.
#
# Ticket #739 extends this file with a three-way `permissions.deny` sync
# contract across flow/templates/settings.json, .claude/settings.json, and
# the marker-delimited literal copy in flow/skills/configure/SKILL.md
# (<!-- cenci:settings-deny:start/end -->) — a divergence between any two
# copies fails the suite. It also pins a non-vacuity floor, per-group
# coverage, a negative guard against boundary-unsafe legacy
# `--force`/`--hard` forms that would also block `git push
# --force-with-lease` (flow/skills/implement/phases/phase-9-pr.md:67), and
# exact-sentence anchors for the SKILL.md reconfigure heal clause.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../" && pwd)"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# assert_no_scoped_write <file> <list> — fails if permissions.<list>
# (allow or deny) contains any scoped-path Write(...) entry
# (e.g. "Write(~/.ssh/**)"), since only bare "Write" is honored by
# Claude Code's file permission checker.
assert_no_scoped_write() {
    local file="$1" list="$2"
    if jq -e --arg list "${list}" \
        '[.permissions[$list][]? | select(test("^Write\\("))] | length == 0' \
        "${file}" >/dev/null 2>&1; then
        pass
    else
        local offenders
        if offenders="$(jq -r --arg list "${list}" \
            '[.permissions[$list][]? | select(test("^Write\\("))] | join(", ")' \
            "${file}" 2>&1)"; then
            fail "${file}: scoped Write(...) ${list} entries found: ${offenders}"
        else
            fail "${file}: could not evaluate permissions.${list} (jq error: ${offenders})"
        fi
    fi
}

# assert_edit_deny_present <file> <path> — fails if permissions.deny lacks
# the Edit(<path>) rule that actually blocks file writes to <path>.
assert_edit_deny_present() {
    local file="$1" path="$2"
    if jq -e --arg rule "Edit(${path})" \
        '.permissions.deny | index($rule) != null' "${file}" >/dev/null 2>&1; then
        pass
    else
        fail "${file}: missing deny rule Edit(${path})"
    fi
}

echo "settings-permissions.test.sh"

for file in "flow/templates/settings.json" ".claude/settings.json"; do
    echo "case: ${file} has no scoped Write(...) allow entries"
    assert_no_scoped_write "${REPO_ROOT}/${file}" allow

    echo "case: ${file} has no scoped Write(...) deny entries"
    assert_no_scoped_write "${REPO_ROOT}/${file}" deny

    echo "case: ${file} keeps Edit(...) deny coverage for sensitive paths"
    assert_edit_deny_present "${REPO_ROOT}/${file}" "~/.ssh/**"
    assert_edit_deny_present "${REPO_ROOT}/${file}" "~/.aws/**"
    assert_edit_deny_present "${REPO_ROOT}/${file}" ".env"
    assert_edit_deny_present "${REPO_ROOT}/${file}" ".env.*"
done

# ── Three-way permissions.deny sync contract (#739) ─────────────────────
# Compares flow/templates/settings.json, .claude/settings.json, and the
# marker-delimited literal copy in flow/skills/configure/SKILL.md. A
# divergence between any two copies fails the suite.

TEMPLATE_SETTINGS="${REPO_ROOT}/flow/templates/settings.json"
CLAUDE_SETTINGS="${REPO_ROOT}/.claude/settings.json"
SKILL_MD="${REPO_ROOT}/flow/skills/configure/SKILL.md"

MARKER_START='<!-- cenci:settings-deny:start -->'
MARKER_END='<!-- cenci:settings-deny:end -->'

WORK_DIR="$(mktemp -d)" || {
    echo "settings-permissions.test.sh: failed to create scratch temp directory." >&2
    exit 2
}
trap 'rm -rf "${WORK_DIR}"' EXIT

EXTRACT_ERR=""

# extract_sorted_deny <settings-json-file> <out-file> — writes
# .permissions.deny, sorted, as a JSON array into <out-file>. Returns
# non-zero and sets EXTRACT_ERR on any failure; never leaves <out-file>
# holding stale content from a previous call.
extract_sorted_deny() {
    local file="$1" out="$2"
    EXTRACT_ERR=""
    rm -f "${out}"
    if [[ ! -r "${file}" ]]; then
        EXTRACT_ERR="file not readable: ${file}"
        return 1
    fi
    if ! jq -c '.permissions.deny | sort' "${file}" > "${out}" 2>/dev/null; then
        rm -f "${out}"
        EXTRACT_ERR="jq failed to extract/sort .permissions.deny from ${file}"
        return 1
    fi
    return 0
}

# extract_skill_deny_raw <skill-md-file> <out-file> — writes the raw
# (still fenced) lines strictly between the cenci:settings-deny markers
# into <out-file>. Redirects awk output into a real temp file with a
# checked exit status rather than unchecked command/process substitution,
# per root AGENTS.md's security-critical-extraction rule and the
# run-checks.sh:66-75 / root-safe-perms-contract.test.sh:216-226 precedent.
extract_skill_deny_raw() {
    local file="$1" out="$2"
    EXTRACT_ERR=""
    rm -f "${out}"
    if [[ ! -r "${file}" ]]; then
        EXTRACT_ERR="file not readable: ${file}"
        return 1
    fi
    if ! awk -v start="${MARKER_START}" -v end="${MARKER_END}" \
        'index($0, start) { flag=1; next } index($0, end) { flag=0 } flag' \
        "${file}" > "${out}"; then
        EXTRACT_ERR="awk marker extraction failed reading ${file}"
        return 1
    fi
    return 0
}

# extract_skill_deny_json_sorted <skill-md-file> <out-file> — extracts the
# fenced JSON array between the markers, strips the ```json/``` fence
# lines, and writes the sorted JSON array into <out-file>.
extract_skill_deny_json_sorted() {
    local file="$1" out="$2"
    local raw="${WORK_DIR}/skill-deny-raw.txt"
    local stripped="${WORK_DIR}/skill-deny-stripped.json"
    if ! extract_skill_deny_raw "${file}" "${raw}"; then
        return 1
    fi
    if [[ ! -s "${raw}" ]]; then
        EXTRACT_ERR="extracted block between ${MARKER_START} and ${MARKER_END} is empty (markers missing, duplicated, or block vacuous)"
        return 1
    fi
    if ! grep -v '^```' "${raw}" > "${stripped}"; then
        EXTRACT_ERR="extracted block contains only fence lines, no JSON content"
        return 1
    fi
    if ! jq -c '. | sort' "${stripped}" > "${out}" 2>/dev/null; then
        EXTRACT_ERR="jq could not parse the extracted block as a JSON array"
        return 1
    fi
    return 0
}

# assert_deny_sync <label_a> <sorted-json-file-a> <label_b> <sorted-json-file-b>
# Fails naming both diverging copies and the differing entries.
assert_deny_sync() {
    local label_a="$1" json_a="$2" label_b="$3" json_b="$4"
    local equal
    if ! equal="$(jq -n --slurpfile a "${json_a}" --slurpfile b "${json_b}" '$a[0] == $b[0]' 2>&1)"; then
        fail "permissions.deny sync ${label_a} vs ${label_b}: jq comparison failed: ${equal}"
        return
    fi
    if [[ "${equal}" == "true" ]]; then
        pass
        return
    fi
    local only_a only_b
    only_a="$(jq -n --slurpfile a "${json_a}" --slurpfile b "${json_b}" '($a[0] - $b[0]) | join(", ")' 2>/dev/null)"
    only_b="$(jq -n --slurpfile a "${json_a}" --slurpfile b "${json_b}" '($b[0] - $a[0]) | join(", ")' 2>/dev/null)"
    fail "permissions.deny sync mismatch: ${label_a} vs ${label_b} — only in ${label_a}: [${only_a}]; only in ${label_b}: [${only_b}]"
}

echo "case: SKILL.md deny start marker present exactly once"
if [[ -r "${SKILL_MD}" ]]; then
    start_count="$(grep -Fc -- "${MARKER_START}" "${SKILL_MD}" 2>&1)"
    start_status=$?
    # grep -c exits 1 on a legitimate zero-match count, so exit status alone
    # can't distinguish "no matches" from a real failure — validate the
    # captured output is a well-formed count instead.
    if [[ ${start_status} -gt 1 || ! "${start_count}" =~ ^[0-9]+$ ]]; then
        fail "flow/skills/configure/SKILL.md: grep failed to count marker '${MARKER_START}' (exit ${start_status}): ${start_count}"
    elif [[ "${start_count}" -eq 1 ]]; then
        pass
    else
        fail "flow/skills/configure/SKILL.md: expected marker '${MARKER_START}' exactly once, found ${start_count}"
    fi
else
    fail "flow/skills/configure/SKILL.md: file not readable: ${SKILL_MD}"
fi

echo "case: SKILL.md deny end marker present exactly once"
if [[ -r "${SKILL_MD}" ]]; then
    end_count="$(grep -Fc -- "${MARKER_END}" "${SKILL_MD}" 2>&1)"
    end_status=$?
    # Same grep -c caveat as the start-marker case above.
    if [[ ${end_status} -gt 1 || ! "${end_count}" =~ ^[0-9]+$ ]]; then
        fail "flow/skills/configure/SKILL.md: grep failed to count marker '${MARKER_END}' (exit ${end_status}): ${end_count}"
    elif [[ "${end_count}" -eq 1 ]]; then
        pass
    else
        fail "flow/skills/configure/SKILL.md: expected marker '${MARKER_END}' exactly once, found ${end_count}"
    fi
else
    fail "flow/skills/configure/SKILL.md: file not readable: ${SKILL_MD}"
fi

echo "case: SKILL.md extracted deny block is non-empty"
if extract_skill_deny_raw "${SKILL_MD}" "${WORK_DIR}/skill-deny-raw-check.txt" && [[ -s "${WORK_DIR}/skill-deny-raw-check.txt" ]]; then
    pass
else
    fail "flow/skills/configure/SKILL.md: extracted deny block between markers is empty or unreadable${EXTRACT_ERR:+ (${EXTRACT_ERR})}"
fi

echo "case: SKILL.md extracted deny block parses via jq as a JSON array"
SKILL_DENY="${WORK_DIR}/skill-deny.json"
if extract_skill_deny_json_sorted "${SKILL_MD}" "${SKILL_DENY}"; then
    pass
    SKILL_OK=1
else
    fail "flow/skills/configure/SKILL.md: ${EXTRACT_ERR}"
    SKILL_OK=0
fi

TEMPLATE_DENY="${WORK_DIR}/template-deny.json"
CLAUDE_DENY="${WORK_DIR}/claude-deny.json"

if extract_sorted_deny "${TEMPLATE_SETTINGS}" "${TEMPLATE_DENY}"; then
    TEMPLATE_OK=1
else
    TEMPLATE_OK=0
    fail "flow/templates/settings.json: ${EXTRACT_ERR}"
fi

if extract_sorted_deny "${CLAUDE_SETTINGS}" "${CLAUDE_DENY}"; then
    CLAUDE_OK=1
else
    CLAUDE_OK=0
    fail ".claude/settings.json: ${EXTRACT_ERR}"
fi

echo "case: permissions.deny sync — flow/templates/settings.json vs .claude/settings.json"
if [[ "${TEMPLATE_OK}" -eq 1 && "${CLAUDE_OK}" -eq 1 ]]; then
    assert_deny_sync "flow/templates/settings.json" "${TEMPLATE_DENY}" ".claude/settings.json" "${CLAUDE_DENY}"
else
    fail "permissions.deny sync flow/templates/settings.json vs .claude/settings.json: skipped — extraction failed for one or both copies (see above)"
fi

echo "case: permissions.deny sync — flow/templates/settings.json vs SKILL.md marker block"
if [[ "${TEMPLATE_OK}" -eq 1 && "${SKILL_OK}" -eq 1 ]]; then
    assert_deny_sync "flow/templates/settings.json" "${TEMPLATE_DENY}" "flow/skills/configure/SKILL.md" "${SKILL_DENY}"
else
    fail "permissions.deny sync flow/templates/settings.json vs flow/skills/configure/SKILL.md: skipped — extraction failed for one or both copies (see above)"
fi

echo "case: permissions.deny sync — .claude/settings.json vs SKILL.md marker block"
if [[ "${CLAUDE_OK}" -eq 1 && "${SKILL_OK}" -eq 1 ]]; then
    assert_deny_sync ".claude/settings.json" "${CLAUDE_DENY}" "flow/skills/configure/SKILL.md" "${SKILL_DENY}"
else
    fail "permissions.deny sync .claude/settings.json vs flow/skills/configure/SKILL.md: skipped — extraction failed for one or both copies (see above)"
fi

# ── Non-vacuity floor + per-group coverage (#739) ────────────────────────
# The plan's reference deny list totals 70 entries with a single blanket
# "Bash(git -c *)" rule. Step 1's empirical Bash-matcher verification could
# not run in this environment (no `claude` binary), so per the plan's
# documented fallback the implementation substitutes 7 narrow per-key
# "git -c" forms for that one entry, bringing the total to 76.
#
# Phase 6+7 hardening (security review MEDIUM/LOW findings) adds 25 more
# entries: a `git --config-env*` bypass rule (1), 5 additional exec-capable
# config keys (core.gitProxy, core.askPass, sequence.editor, gpg.program,
# diff.external) each in bare/mid-position `-c` and `config` forms (20),
# and 4 mechanical LOW-finding gaps (`git config -f`, `git config -e`,
# refspec force-push `git push * +*`, and `git config remote.*`) — bringing
# the total to 101.
#
# A second Phase 6+7 round (security review HIGH/MEDIUM findings) adds 40
# more entries, unverified (no `claude` binary available to empirically
# confirm Claude Code's Bash-matcher semantics): 24 `-C <path>`-prefixed
# mirror entries covering the highest-value groups (force-push, reset
# --hard, filter-branch/filter-repo, and all 12 `-c` exec-keys), plus 16
# lowercase config-key case-variant entries (core.hooksPath/sshCommand/
# gitProxy/askPass, each mirrored bare and mid-position in both the `-c`
# and `config` groups, plus the `-C`-prefixed lowercase mirror) — bringing
# the total to 141. The floor is raised to 141 to catch regressions; 101
# remains a valid floor under the pre-second-round count.
#
# A third Phase 6+7 round (security review MEDIUM/LOW findings) adds 13
# more entries: 11 mid-position `git -c * <key>*` forms for the 7 original
# exec keys that lacked them (core.hooksPath/sshCommand/pager/editor/
# fsmonitor, alias.*, credential.helper) plus the 2 missing mid-position
# lowercase mirrors for core.gitProxy/core.askPass, and a net +2 from
# replacing the unverified `Bash(git -C * filter-branch:*)` /
# `Bash(git -C * filter-repo:*)` `:*`-plus-inner-glob pair with the
# glob-consistent explicit 4-entry form (`filter-branch`/`filter-branch *`/
# `filter-repo`/`filter-repo *`) — bringing the total to 154. The floor is
# raised to 154 to catch regressions; 141 and 101 remain valid floors under
# the pre-third-round counts.
MIN_DENY_LEN=154

DENY_LABELS=("flow/templates/settings.json" ".claude/settings.json" "flow/skills/configure/SKILL.md marker block")
DENY_FILES=("${TEMPLATE_DENY}" "${CLAUDE_DENY}" "${SKILL_DENY}")
DENY_OKS=("${TEMPLATE_OK}" "${CLAUDE_OK}" "${SKILL_OK}")

for i in 0 1 2; do
    label="${DENY_LABELS[$i]}"
    file="${DENY_FILES[$i]}"
    ok="${DENY_OKS[$i]}"

    echo "case: ${label} permissions.deny has at least ${MIN_DENY_LEN} entries (non-vacuity floor)"
    if [[ "${ok}" -eq 1 ]]; then
        if ! len="$(jq 'length' "${file}" 2>&1)"; then
            fail "${label}: jq failed to compute permissions.deny array length: ${len}"
        elif [[ "${len}" -ge "${MIN_DENY_LEN}" ]]; then
            pass
        else
            fail "${label}: permissions.deny has ${len} entries, expected >= ${MIN_DENY_LEN}"
        fi
    else
        fail "${label}: permissions.deny extraction failed (see above) — cannot verify >= ${MIN_DENY_LEN} entries"
    fi

    for group in "git config" "git -c " "git filter-branch" "git reset --hard" "git clean" "git push --force"; do
        echo "case: ${label} permissions.deny has at least one entry matching group '${group}'"
        if [[ "${ok}" -eq 1 ]] && jq -e --arg g "${group}" 'any(.[]; contains($g))' "${file}" >/dev/null 2>&1; then
            pass
        else
            fail "${label}: permissions.deny has no entry matching group '${group}'"
        fi
    done
done

# ── Negative guard: boundary-unsafe legacy forms (#739, Q3) ──────────────
# Bash(git push --force:*) and Bash(git push --force*) also match
# `git push --force-with-lease`, which the Goal-Autopilot resume path in
# flow/skills/implement/phases/phase-9-pr.md:67 requires. Pins the
# `--force-with-lease` carve-out so a future "simplification" cannot
# silently re-break it.
LEGACY_FORMS=("Bash(git push --force:*)" "Bash(git push --force*)")

for i in 0 1 2; do
    label="${DENY_LABELS[$i]}"
    file="${DENY_FILES[$i]}"
    ok="${DENY_OKS[$i]}"

    for legacy in "${LEGACY_FORMS[@]}"; do
        echo "case: ${label} does not carry boundary-unsafe legacy entry '${legacy}'"
        if [[ "${ok}" -ne 1 ]]; then
            fail "${label}: cannot verify absence of '${legacy}' — extraction failed (see above)"
            continue
        fi
        if jq -e --arg e "${legacy}" 'any(.[]; . == $e)' "${file}" >/dev/null 2>&1; then
            fail "${label}: carries boundary-unsafe legacy entry '${legacy}' which also blocks 'git push --force-with-lease', required by flow/skills/implement/phases/phase-9-pr.md:67"
        else
            pass
        fi
    done
done

# ── SKILL.md heal clause anchors (#739, Q5) ──────────────────────────────
# Exact-sentence anchors, not generic keywords (flow/docs/shell-scripting-
# gotchas.md rule 3) — the heal step is prose an agent executes, not code,
# so a generic keyword match would pass vacuously against unrelated text.
echo "case: SKILL.md base-deny heal clause (IMPORTANT paragraph) present"
if SKILL_CONTENT="$(cat "${SKILL_MD}" 2>&1)"; then
    HEAL_MARKER='All base deny rules from the template **MUST** be present in `permissions.deny`. Only **append** new entries — never remove or replace existing ones, including user-added entries.'
    if [[ "${SKILL_CONTENT}" == *"${HEAL_MARKER}"* ]]; then
        pass
    else
        fail "flow/skills/configure/SKILL.md: missing base-deny heal clause anchor: [${HEAL_MARKER}]"
    fi

    echo "case: SKILL.md legacy-supersession clause present"
    SUPERSESSION_MARKER='(a) **remove** the legacy `Bash(git push --force:*)` and `Bash(git reset --hard:*)` entries when adding the base list — the boundary-safe forms below supersede them'
    if [[ "${SKILL_CONTENT}" == *"${SUPERSESSION_MARKER}"* ]]; then
        pass
    else
        fail "flow/skills/configure/SKILL.md: missing legacy-supersession clause anchor: [${SUPERSESSION_MARKER}]"
    fi
else
    fail "flow/skills/configure/SKILL.md: could not read file for heal-clause check: ${SKILL_CONTENT}"
fi

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
