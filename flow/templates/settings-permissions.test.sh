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

# _extract_skill_deny_stripped <skill-md-file> <raw-out-file>
# <stripped-out-file> — shared first half of extract_skill_deny_json_sorted
# and extract_skill_deny_json_ordered: extracts the raw marker block,
# rejects an empty block, and strips the ```json/``` fence lines into
# <stripped-out-file>. Callers apply their own jq filter (sorted vs
# insertion-order) to <stripped-out-file>.
_extract_skill_deny_stripped() {
    local file="$1" raw="$2" stripped="$3"
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
    return 0
}

# extract_skill_deny_json_sorted <skill-md-file> <out-file> — extracts the
# fenced JSON array between the markers, strips the ```json/``` fence
# lines, and writes the sorted JSON array into <out-file>.
extract_skill_deny_json_sorted() {
    local file="$1" out="$2"
    local raw="${WORK_DIR}/skill-deny-raw.txt"
    local stripped="${WORK_DIR}/skill-deny-stripped.json"
    if ! _extract_skill_deny_stripped "${file}" "${raw}" "${stripped}"; then
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
#
# A fourth round (#794, security review MEDIUM finding) adds 32 more
# entries: `gh api` destructive-method denies for DELETE and PUT across all
# eight syntactic forms (method-first/path-first x -X M/-XM/--method M/
# --method=M) x {DELETE, PUT} x {upper, lower} case, mirroring the file's
# established lowercase-config-key mirror precedent — bringing the total to
# 186. The floor is raised to 186 to catch regressions; 154, 141, and 101
# remain valid floors under the pre-fourth-round counts. As with the second
# round, this is unverified empirically (no `claude` binary available in
# this environment to confirm Claude Code's Bash-matcher semantics against
# the new entries).
#
# A fifth round (#794 follow-up, security review MEDIUM finding) adds 8
# more entries covering the `-X=METHOD` syntactic form (method-first and
# path-first) x {DELETE, PUT} x {upper, lower} case, which `gh`'s
# Cobra/pflag argument parsing also accepts but which none of the fourth
# round's eight forms matched — bringing the gh api destructive-method
# coverage to 10 syntactic forms total (method-first `-X M`/`-XM`/`-X=M`/
# `--method M`/`--method=M` and path-first mirrors of the same, x
# {DELETE, PUT} x {upper, lower}) and the deny-array total to 194. The
# floor is raised to 194 to catch regressions; 186, 154, 141, and 101
# remain valid floors under the pre-fifth-round counts. As with the second
# and fourth rounds, this is unverified empirically (no `claude` binary
# available in this environment to confirm Claude Code's Bash-matcher
# semantics against the new entries).
MIN_DENY_LEN=194

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

    for group in "git config" "git -c " "git filter-branch" "git reset --hard" "git clean" "git push --force" "gh api"; do
        echo "case: ${label} permissions.deny has at least one entry matching group '${group}'"
        if [[ "${ok}" -eq 1 ]] && jq -e --arg g "${group}" 'any(.[]; contains($g))' "${file}" >/dev/null 2>&1; then
            pass
        else
            fail "${label}: permissions.deny has no entry matching group '${group}'"
        fi
    done
done

# ── gh api destructive-method coverage (#794) ────────────────────────────
# The generic `contains("gh api")` group check above is satisfied by a
# single surviving entry — it does not prove all ten syntactic forms x
# {DELETE, PUT} x {upper, lower} case are actually present. This array
# holds all 40 entries verbatim. The per-entry membership loop below
# (`any(.[]; . == $e)`) proves exact PRESENCE of each of the 40 entries in
# each of the three sources (root AGENTS.md: a claimed assertion must be
# exercised by an explicit case) — by itself it proves membership only, NOT
# order or uniqueness (#813). The "gh api deny contract: ordered-slice
# equality + uniqueness" section further below adds the ordered-slice-
# equality and whole-array-uniqueness assertions that close that gap.
GH_API_DENY_FORMS=(
    "Bash(gh api -X DELETE*)"
    "Bash(gh api -X delete*)"
    "Bash(gh api -XDELETE*)"
    "Bash(gh api -Xdelete*)"
    "Bash(gh api --method DELETE*)"
    "Bash(gh api --method delete*)"
    "Bash(gh api --method=DELETE*)"
    "Bash(gh api --method=delete*)"
    "Bash(gh api * -X DELETE*)"
    "Bash(gh api * -X delete*)"
    "Bash(gh api * -XDELETE*)"
    "Bash(gh api * -Xdelete*)"
    "Bash(gh api * --method DELETE*)"
    "Bash(gh api * --method delete*)"
    "Bash(gh api * --method=DELETE*)"
    "Bash(gh api * --method=delete*)"
    "Bash(gh api -X PUT*)"
    "Bash(gh api -X put*)"
    "Bash(gh api -XPUT*)"
    "Bash(gh api -Xput*)"
    "Bash(gh api --method PUT*)"
    "Bash(gh api --method put*)"
    "Bash(gh api --method=PUT*)"
    "Bash(gh api --method=put*)"
    "Bash(gh api * -X PUT*)"
    "Bash(gh api * -X put*)"
    "Bash(gh api * -XPUT*)"
    "Bash(gh api * -Xput*)"
    "Bash(gh api * --method PUT*)"
    "Bash(gh api * --method put*)"
    "Bash(gh api * --method=PUT*)"
    "Bash(gh api * --method=put*)"
    "Bash(gh api -X=DELETE*)"
    "Bash(gh api -X=delete*)"
    "Bash(gh api -X=PUT*)"
    "Bash(gh api -X=put*)"
    "Bash(gh api * -X=DELETE*)"
    "Bash(gh api * -X=delete*)"
    "Bash(gh api * -X=PUT*)"
    "Bash(gh api * -X=put*)"
)

for i in 0 1 2; do
    label="${DENY_LABELS[$i]}"
    file="${DENY_FILES[$i]}"
    ok="${DENY_OKS[$i]}"

    for form in "${GH_API_DENY_FORMS[@]}"; do
        echo "case: ${label} denies destructive gh api form '${form}'"
        if [[ "${ok}" -ne 1 ]]; then
            fail "${label}: cannot verify presence of '${form}' — extraction failed (see above)"
            continue
        fi
        if jq -e --arg e "${form}" 'any(.[]; . == $e)' "${file}" >/dev/null 2>&1; then
            pass
        else
            fail "${label}: permissions.deny missing gh api destructive-method entry '${form}'"
        fi
    done
done

# ── gh api deny contract: ordered-slice equality + uniqueness (#813, Q1) ──
# Confirmed decision (ticket #813 Q1, escalated + settled, not re-openable):
#   - Ordered equality applies ONLY to the GH_API_DENY_FORMS-matching slice
#     of each source's deny array (filter the source array down to the
#     entries that are members of the expected 40-element set, in the
#     source's own order, then compare that slice to the expected array for
#     exact ordered equality) — NOT to the full 194-entry array. Pinning the
#     position of unrelated entries (git config, git clean, ...) in three
#     files would be churn without security value.
#   - Uniqueness applies to each FULL deny array (all three sources are
#     verified duplicate-free today).
#   - MIN_DENY_LEN=194 and the 40 per-entry membership checks above stay
#     unchanged.
#   - A cross-source identity assertion (template == root == skill) is
#     deliberately OUT OF SCOPE — it would forbid .claude/settings.json from
#     ever carrying a repo-specific deny entry, a policy decision beyond
#     this ticket.

echo "case: GH_API_DENY_FORMS has exactly 40 elements"
if [[ "${#GH_API_DENY_FORMS[@]}" -eq 40 ]]; then
    pass
else
    fail "GH_API_DENY_FORMS: expected exactly 40 elements, found ${#GH_API_DENY_FORMS[@]}"
fi

echo "case: GH_API_DENY_FORMS has exactly 40 distinct elements (no self-duplicates)"
if ! GH_API_DENY_FORMS_DISTINCT_COUNT="$(printf '%s\n' "${GH_API_DENY_FORMS[@]}" | sort -u | wc -l | tr -d ' ')"; then
    fail "GH_API_DENY_FORMS: failed to compute distinct element count: ${GH_API_DENY_FORMS_DISTINCT_COUNT}"
elif [[ "${GH_API_DENY_FORMS_DISTINCT_COUNT}" -eq 40 ]]; then
    pass
else
    fail "GH_API_DENY_FORMS: expected 40 distinct elements, found ${GH_API_DENY_FORMS_DISTINCT_COUNT}"
fi

# Materialize GH_API_DENY_FORMS as a JSON array with an explicitly checked
# exit status (never unchecked command/process substitution for a
# security-critical extraction — root AGENTS.md).
GH_API_EXPECTED_JSON="${WORK_DIR}/gh-api-expected.json"
if ! printf '%s\n' "${GH_API_DENY_FORMS[@]}" | jq -R . | jq -s . > "${GH_API_EXPECTED_JSON}"; then
    echo "settings-permissions.test.sh: failed to materialize GH_API_DENY_FORMS as JSON at ${GH_API_EXPECTED_JSON}." >&2
    exit 2
fi

# extract_deny_ordered <settings-json-file> <out-file> — writes
# .permissions.deny AS-IS (its own insertion order, not sorted) into
# <out-file>. extract_sorted_deny's alphabetical sort would destroy the very
# insertion order the ordered-slice-equality predicate below needs to
# compare. Returns non-zero and sets EXTRACT_ERR on any failure.
extract_deny_ordered() {
    local file="$1" out="$2"
    EXTRACT_ERR=""
    rm -f "${out}"
    if [[ ! -r "${file}" ]]; then
        EXTRACT_ERR="file not readable: ${file}"
        return 1
    fi
    if ! jq -c '.permissions.deny' "${file}" > "${out}" 2>/dev/null; then
        rm -f "${out}"
        EXTRACT_ERR="jq failed to extract .permissions.deny from ${file}"
        return 1
    fi
    return 0
}

# extract_skill_deny_json_ordered <skill-md-file> <out-file> — same as
# extract_skill_deny_json_sorted above but preserves the fenced block's own
# insertion order (no `sort`).
extract_skill_deny_json_ordered() {
    local file="$1" out="$2"
    local raw="${WORK_DIR}/skill-deny-raw-ordered.txt"
    local stripped="${WORK_DIR}/skill-deny-stripped-ordered.json"
    if ! _extract_skill_deny_stripped "${file}" "${raw}" "${stripped}"; then
        return 1
    fi
    if ! jq -c '.' "${stripped}" > "${out}" 2>/dev/null; then
        EXTRACT_ERR="jq could not parse the extracted block as a JSON array"
        return 1
    fi
    return 0
}

TEMPLATE_DENY_ORDERED="${WORK_DIR}/template-deny-ordered.json"
CLAUDE_DENY_ORDERED="${WORK_DIR}/claude-deny-ordered.json"
SKILL_DENY_ORDERED="${WORK_DIR}/skill-deny-ordered.json"

if extract_deny_ordered "${TEMPLATE_SETTINGS}" "${TEMPLATE_DENY_ORDERED}"; then
    TEMPLATE_ORD_OK=1
else
    TEMPLATE_ORD_OK=0
    fail "flow/templates/settings.json: ${EXTRACT_ERR}"
fi

if extract_deny_ordered "${CLAUDE_SETTINGS}" "${CLAUDE_DENY_ORDERED}"; then
    CLAUDE_ORD_OK=1
else
    CLAUDE_ORD_OK=0
    fail ".claude/settings.json: ${EXTRACT_ERR}"
fi

if extract_skill_deny_json_ordered "${SKILL_MD}" "${SKILL_DENY_ORDERED}"; then
    SKILL_ORD_OK=1
else
    SKILL_ORD_OK=0
    fail "flow/skills/configure/SKILL.md: ${EXTRACT_ERR}"
fi

# gh_api_slice_equal_ok <full-deny-json-file> <expected-json-file> — pure
# predicate, never calls fail() (flow/docs/shell-scripting-gotchas.md's
# "never call fail() inside $(...)" rule; enforced by
# flow/tests/read-helper-purity-contract.test.sh). Filters
# <full-deny-json-file>'s array down to the entries that are members of
# <expected-json-file>'s set, preserving <full-deny-json-file>'s own order,
# then compares that filtered slice to <expected-json-file> for exact
# ordered equality. Returns 0 only on an exact ordered match; returns 1 on
# any mismatch OR any jq evaluation error, so a broken predicate can never
# silently report a pass.
gh_api_slice_equal_ok() {
    local full="$1" expected="$2" result
    result="$(jq -n --slurpfile full "${full}" --slurpfile expected "${expected}" \
        '($expected[0]) as $e
         | ($full[0] | map(select(. as $x | $e | index($x) != null))) as $slice
         | ($slice == $e)' 2>/dev/null)" || return 1
    [[ "${result}" == "true" ]]
}

# gh_api_array_unique_ok <full-deny-json-file> — pure predicate, same
# no-fail() contract as above. Returns 0 only when <full-deny-json-file>'s
# array is duplicate-free; returns 1 on any duplicate OR any jq evaluation
# error.
gh_api_array_unique_ok() {
    local full="$1" result
    result="$(jq -n --slurpfile full "${full}" \
        '($full[0] | length) == ($full[0] | unique | length)' 2>/dev/null)" || return 1
    [[ "${result}" == "true" ]]
}

DENY_ORDERED_FILES=("${TEMPLATE_DENY_ORDERED}" "${CLAUDE_DENY_ORDERED}" "${SKILL_DENY_ORDERED}")
DENY_ORD_OKS=("${TEMPLATE_ORD_OK}" "${CLAUDE_ORD_OK}" "${SKILL_ORD_OK}")

for i in 0 1 2; do
    label="${DENY_LABELS[$i]}"
    file="${DENY_ORDERED_FILES[$i]}"
    ok="${DENY_ORD_OKS[$i]}"

    echo "case: ${label} gh api destructive-method forms appear as an ordered slice matching GH_API_DENY_FORMS"
    if [[ "${ok}" -ne 1 ]]; then
        fail "${label}: cannot verify gh api ordered-slice equality — ordered extraction failed (see above)"
    elif gh_api_slice_equal_ok "${file}" "${GH_API_EXPECTED_JSON}"; then
        pass
    else
        if ! actual_slice="$(jq -c --slurpfile expected "${GH_API_EXPECTED_JSON}" '($expected[0]) as $e | map(select(. as $x | $e | index($x) != null))' "${file}" 2>&1)"; then
            actual_slice="jq failed to compute diagnostic slice: ${actual_slice}"
        fi
        fail "${label}: gh api destructive-method forms slice does not match GH_API_DENY_FORMS in order (actual slice: ${actual_slice})"
    fi

    echo "case: ${label} permissions.deny array is duplicate-free"
    if [[ "${ok}" -ne 1 ]]; then
        fail "${label}: cannot verify duplicate-free deny array — ordered extraction failed (see above)"
    elif gh_api_array_unique_ok "${file}"; then
        pass
    else
        fail "${label}: permissions.deny array contains at least one duplicate entry"
    fi
done

# ── Self-tests: gh api ordered-slice + uniqueness predicates are non-vacuous
# (#813 Implementation Order step 4) ─────────────────────────────────────
# Derives three deliberately-broken copies of the real extracted (ordered)
# template deny array, entirely inside WORK_DIR via jq — never touching any
# shipped configuration file — and asserts the predicates above reject each
# one. A positive control asserts the unmutated copy is accepted, so this
# self-test block cannot pass vacuously (e.g. via a predicate that always
# returns 1).
if [[ "${TEMPLATE_ORD_OK}" -eq 1 ]]; then
    echo "case: gh api predicate self-test — positive control (unmutated copy accepted)"
    if gh_api_slice_equal_ok "${TEMPLATE_DENY_ORDERED}" "${GH_API_EXPECTED_JSON}" \
        && gh_api_array_unique_ok "${TEMPLATE_DENY_ORDERED}"; then
        pass
    else
        fail "self-test: unmutated flow/templates/settings.json deny array was rejected by the gh api predicates (positive control failed)"
    fi

    GH_API_SELFTEST_MISSING="${WORK_DIR}/gh-api-selftest-missing.json"
    echo "case: gh api ordered-slice predicate self-test — missing entry is rejected"
    if ! jq --arg e "${GH_API_DENY_FORMS[0]}" 'map(select(. != $e))' \
        "${TEMPLATE_DENY_ORDERED}" > "${GH_API_SELFTEST_MISSING}"; then
        fail "self-test: failed to materialize missing-entry fixture"
    elif gh_api_slice_equal_ok "${GH_API_SELFTEST_MISSING}" "${GH_API_EXPECTED_JSON}"; then
        fail "self-test: gh_api_slice_equal_ok accepted a deny array missing '${GH_API_DENY_FORMS[0]}' (predicate is vacuous)"
    else
        pass
    fi

    GH_API_SELFTEST_SWAPPED="${WORK_DIR}/gh-api-selftest-swapped.json"
    echo "case: gh api ordered-slice predicate self-test — swapped adjacent pair (within the slice) is rejected"
    if ! jq --arg a "${GH_API_DENY_FORMS[0]}" --arg b "${GH_API_DENY_FORMS[1]}" \
        'map(if . == $a then $b elif . == $b then $a else . end)' \
        "${TEMPLATE_DENY_ORDERED}" > "${GH_API_SELFTEST_SWAPPED}"; then
        fail "self-test: failed to materialize swapped-pair fixture"
    elif gh_api_slice_equal_ok "${GH_API_SELFTEST_SWAPPED}" "${GH_API_EXPECTED_JSON}"; then
        fail "self-test: gh_api_slice_equal_ok accepted a deny array with '${GH_API_DENY_FORMS[0]}' and '${GH_API_DENY_FORMS[1]}' swapped (predicate is vacuous)"
    else
        pass
    fi

    GH_API_SELFTEST_DUPLICATE="${WORK_DIR}/gh-api-selftest-duplicate.json"
    echo "case: gh api uniqueness predicate self-test — injected duplicate is rejected"
    if ! jq '. + [.[0]]' "${TEMPLATE_DENY_ORDERED}" > "${GH_API_SELFTEST_DUPLICATE}"; then
        fail "self-test: failed to materialize duplicate-entry fixture"
    elif gh_api_array_unique_ok "${GH_API_SELFTEST_DUPLICATE}"; then
        fail "self-test: gh_api_array_unique_ok accepted a deny array with an injected duplicate entry (predicate is vacuous)"
    else
        pass
    fi
else
    fail "self-test: cannot run gh api predicate self-tests — flow/templates/settings.json ordered extraction failed (see above)"
fi

# ── Negative guard: boundary-unsafe legacy forms (#739, Q3) ──────────────
# Bash(git push --force:*) and Bash(git push --force*) also match
# `git push --force-with-lease`, which Phase 9's re-run push path in
# flow/skills/implement/phases/phase-9-pr.md requires. Pins the
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
