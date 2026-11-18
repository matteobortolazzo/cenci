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
# the marker-delimited literal copy in
# flow/skills/configure/references/settings-template.md
# (<!-- cenci:settings-deny:start/end -->, moved there from SKILL.md by #1343)
# — a divergence between any two copies fails the suite. It also pins a
# non-vacuity floor, per-group coverage, a negative guard against
# boundary-unsafe legacy `--force`/`--hard` forms that would also block
# `git push --force-with-lease` (flow/skills/implement/phases/phase-9-pr.md:67),
# exact-sentence anchors for the reconfigure heal clause in
# flow/skills/configure/phases/guidance-settings.md, and (#1343) a guard that
# the deny markers live in no other configure markdown file, so the heal step
# cannot read a second, diverging copy.
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
# marker-delimited literal copy in
# flow/skills/configure/references/settings-template.md. A divergence between
# any two copies fails the suite.

TEMPLATE_SETTINGS="${REPO_ROOT}/flow/templates/settings.json"
CLAUDE_SETTINGS="${REPO_ROOT}/.claude/settings.json"
CONFIGURE_DIR="${REPO_ROOT}/flow/skills/configure"
SKILL_MD="${CONFIGURE_DIR}/SKILL.md"
SETTINGS_REF_REL="flow/skills/configure/references/settings-template.md"
SETTINGS_REF_MD="${REPO_ROOT}/${SETTINGS_REF_REL}"
GUIDANCE_REL="flow/skills/configure/phases/guidance-settings.md"
GUIDANCE_MD="${REPO_ROOT}/${GUIDANCE_REL}"

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

echo "case: settings-template.md deny start marker present exactly once"
if [[ -r "${SETTINGS_REF_MD}" ]]; then
    start_count="$(grep -Fc -- "${MARKER_START}" "${SETTINGS_REF_MD}" 2>&1)"
    start_status=$?
    # grep -c exits 1 on a legitimate zero-match count, so exit status alone
    # can't distinguish "no matches" from a real failure — validate the
    # captured output is a well-formed count instead.
    if [[ ${start_status} -gt 1 || ! "${start_count}" =~ ^[0-9]+$ ]]; then
        fail "${SETTINGS_REF_REL}: grep failed to count marker '${MARKER_START}' (exit ${start_status}): ${start_count}"
    elif [[ "${start_count}" -eq 1 ]]; then
        pass
    else
        fail "${SETTINGS_REF_REL}: expected marker '${MARKER_START}' exactly once, found ${start_count}"
    fi
else
    fail "${SETTINGS_REF_REL}: file not readable: ${SETTINGS_REF_MD}"
fi

echo "case: settings-template.md deny end marker present exactly once"
if [[ -r "${SETTINGS_REF_MD}" ]]; then
    end_count="$(grep -Fc -- "${MARKER_END}" "${SETTINGS_REF_MD}" 2>&1)"
    end_status=$?
    # Same grep -c caveat as the start-marker case above.
    if [[ ${end_status} -gt 1 || ! "${end_count}" =~ ^[0-9]+$ ]]; then
        fail "${SETTINGS_REF_REL}: grep failed to count marker '${MARKER_END}' (exit ${end_status}): ${end_count}"
    elif [[ "${end_count}" -eq 1 ]]; then
        pass
    else
        fail "${SETTINGS_REF_REL}: expected marker '${MARKER_END}' exactly once, found ${end_count}"
    fi
else
    fail "${SETTINGS_REF_REL}: file not readable: ${SETTINGS_REF_MD}"
fi

# The deny markers must live in exactly one configure markdown file (#1343):
# a second copy in SKILL.md or a phase file would be a fourth deny list the
# three-way sync below never compares. Paired with the exactly-once cases
# above (the reference file exists and holds both markers), so a zero count
# elsewhere cannot pass vacuously against a missing or emptied reference.
echo "case: deny markers appear in no configure markdown file other than settings-template.md"
other_hits=""
other_scanned=0
other_scan_failed=0
for other in "${SKILL_MD}" "${CONFIGURE_DIR}"/phases/*.md "${CONFIGURE_DIR}"/references/*.md; do
    [[ "${other}" != "${SETTINGS_REF_MD}" ]] || continue
    if [[ ! -r "${other}" ]]; then
        # An unmatched glob stays literal (unreadable): a missing directory
        # is a scan failure, never a silent zero.
        other_scan_failed=1
        continue
    fi
    other_scanned=$((other_scanned + 1))
    other_marker_hits="$(grep -Fc -e "${MARKER_START}" -e "${MARKER_END}" -- "${other}" 2>&1)"
    other_status=$?
    if [[ ${other_status} -gt 1 || ! "${other_marker_hits}" =~ ^[0-9]+$ ]]; then
        other_scan_failed=1
    elif [[ "${other_marker_hits}" -gt 0 ]]; then
        other_hits="${other_hits} ${other#"${REPO_ROOT}/"}"
    fi
done
if [[ "${other_scan_failed}" -eq 1 ]]; then
    fail "deny-marker scan of the other configure markdown files failed (unreadable file or grep error)"
elif [[ ! -r "${SETTINGS_REF_MD}" ]]; then
    fail "${SETTINGS_REF_REL}: cannot verify the markers appear nowhere else — the reference file is missing"
elif [[ "${other_scanned}" -lt 15 ]]; then
    fail "deny-marker scan is vacuous: only ${other_scanned} other configure markdown files scanned (expected SKILL.md, 11 phase files and 3 other reference files = 15)"
elif [[ -n "${other_hits}" ]]; then
    fail "deny markers must live only in ${SETTINGS_REF_REL}, also found in:${other_hits}"
else
    pass
fi

echo "case: settings-template.md extracted deny block is non-empty"
if extract_skill_deny_raw "${SETTINGS_REF_MD}" "${WORK_DIR}/skill-deny-raw-check.txt" && [[ -s "${WORK_DIR}/skill-deny-raw-check.txt" ]]; then
    pass
else
    fail "${SETTINGS_REF_REL}: extracted deny block between markers is empty or unreadable${EXTRACT_ERR:+ (${EXTRACT_ERR})}"
fi

echo "case: settings-template.md extracted deny block parses via jq as a JSON array"
REF_DENY="${WORK_DIR}/skill-deny.json"
if extract_skill_deny_json_sorted "${SETTINGS_REF_MD}" "${REF_DENY}"; then
    pass
    REF_OK=1
else
    fail "${SETTINGS_REF_REL}: ${EXTRACT_ERR}"
    REF_OK=0
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

echo "case: permissions.deny sync — flow/templates/settings.json vs settings-template.md marker block"
if [[ "${TEMPLATE_OK}" -eq 1 && "${REF_OK}" -eq 1 ]]; then
    assert_deny_sync "flow/templates/settings.json" "${TEMPLATE_DENY}" "${SETTINGS_REF_REL}" "${REF_DENY}"
else
    fail "permissions.deny sync flow/templates/settings.json vs ${SETTINGS_REF_REL}: skipped — extraction failed for one or both copies (see above)"
fi

echo "case: permissions.deny sync — .claude/settings.json vs settings-template.md marker block"
if [[ "${CLAUDE_OK}" -eq 1 && "${REF_OK}" -eq 1 ]]; then
    assert_deny_sync ".claude/settings.json" "${CLAUDE_DENY}" "${SETTINGS_REF_REL}" "${REF_DENY}"
else
    fail "permissions.deny sync .claude/settings.json vs ${SETTINGS_REF_REL}: skipped — extraction failed for one or both copies (see above)"
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
#
# A sixth round (#758) ran the matcher checks the earlier rounds could not: a
# one-time manual `claude -p` probe run, recorded in
# flow/docs/claude-bash-matcher-evidence.md (tested version, procedure, S1-S7
# semantics, final-template coverage and positive controls). It showed that
# Claude Code matches deny rules case-sensitively (probe S1, S2), lets `*` span
# spaces and several flags (S3), does not strip `-C <path>` or `--git-dir=`
# before matching (S4a, S4b), matches deny rules through an env-var prefix (S5)
# and through `&&` compounds (S7), but not past an unmirrored global flag
# (S6). It adds 82 entries (MATCHER_GAP_DENY_FORMS below): the four
# `--git-dir`/`--work-tree` rules, the two blanket `-c` rules (case-sensitive
# matching, so they are safe next to the allowed `git -C <path>`), and a
# `-C <path>` mirror of every base group that had none — bringing the total to
# 276. The floor is raised to 276 to catch regressions; the earlier floors
# remain valid under the pre-sixth-round counts.
MIN_DENY_LEN=276

DENY_LABELS=("flow/templates/settings.json" ".claude/settings.json" "${SETTINGS_REF_REL} marker block")
DENY_FILES=("${TEMPLATE_DENY}" "${CLAUDE_DENY}" "${REF_DENY}")
DENY_OKS=("${TEMPLATE_OK}" "${CLAUDE_OK}" "${REF_OK}")

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
#     exact ordered equality) — NOT to the full deny array (194 entries when
#     this decision was made, 276 since the sixth round). Pinning the
#     position of unrelated entries (git config, git clean, ...) in three
#     files would be churn without security value.
#   - Uniqueness applies to each FULL deny array (all three sources are
#     verified duplicate-free today).
#   - MIN_DENY_LEN (194 at the time of this decision, 276 since the sixth
#     round) and the 40 per-entry membership checks above stay unchanged.
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
REF_DENY_ORDERED="${WORK_DIR}/skill-deny-ordered.json"

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

if extract_skill_deny_json_ordered "${SETTINGS_REF_MD}" "${REF_DENY_ORDERED}"; then
    REF_ORD_OK=1
else
    REF_ORD_OK=0
    fail "${SETTINGS_REF_REL}: ${EXTRACT_ERR}"
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

DENY_ORDERED_FILES=("${TEMPLATE_DENY_ORDERED}" "${CLAUDE_DENY_ORDERED}" "${REF_DENY_ORDERED}")
DENY_ORD_OKS=("${TEMPLATE_ORD_OK}" "${CLAUDE_ORD_OK}" "${REF_ORD_OK}")

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

# ── Bash-matcher gap rules (#758) ────────────────────────────────────────
# Rules the one-time `claude -p` probe run (flow/docs/claude-bash-matcher-
# evidence.md) showed are needed. Each must be present, verbatim, in all three
# copies: per-entry membership (any(.[]; . == $e)) like the gh api loop above,
# so one surviving entry cannot satisfy the whole group. The array is in the
# order the entries land in the template (each next to its base group).
#   - four `--git-dir`/`--work-tree` rules: no earlier rule matched either flag
#     (probes N-GD*, N-WT*, S4b);
#   - blanket `-c` rules, added because probe S1 showed case-sensitive matching;
#   - `-C <path>` mirrors for every base group probe S4a showed is not
#     normalized (probes K-* in the record).
MATCHER_GAP_DENY_FORMS=(
    "Bash(git -C * push * +*)"
    "Bash(git -C * push --mirror*)"
    "Bash(git -C * push * --mirror*)"
    "Bash(git -C * push --delete *)"
    "Bash(git -C * push * --delete *)"
    "Bash(git -C * push --no-verify*)"
    "Bash(git -C * push * --no-verify*)"
    "Bash(git -C * commit --no-verify*)"
    "Bash(git -C * commit * --no-verify*)"
    "Bash(git -C * clean)"
    "Bash(git -C * clean *)"
    "Bash(git -C * checkout -- *)"
    "Bash(git -C * checkout * -- *)"
    "Bash(git -C * restore)"
    "Bash(git -C * restore *)"
    "Bash(git -C * reflog expire*)"
    "Bash(git -C * reflog delete*)"
    "Bash(git -C * gc --prune*)"
    "Bash(git -C * gc * --prune*)"
    "Bash(git -C * update-ref)"
    "Bash(git -C * update-ref *)"
    "Bash(git -C * replace)"
    "Bash(git -C * replace *)"
    "Bash(git -C * branch -f *)"
    "Bash(git -C * branch --force *)"
    "Bash(git -c *)"
    "Bash(git -C * -c *)"
    "Bash(git --git-dir*)"
    "Bash(git --work-tree*)"
    "Bash(git -C * --git-dir*)"
    "Bash(git -C * --work-tree*)"
    "Bash(git -C * --config-env*)"
    "Bash(git -C * --exec-path*)"
    "Bash(git -C * config --global*)"
    "Bash(git -C * config --system*)"
    "Bash(git -C * config --local*)"
    "Bash(git -C * config --file*)"
    "Bash(git -C * config -f *)"
    "Bash(git -C * config --unset*)"
    "Bash(git -C * config * --unset*)"
    "Bash(git -C * config --add *)"
    "Bash(git -C * config * --add *)"
    "Bash(git -C * config --replace-all*)"
    "Bash(git -C * config * --replace-all*)"
    "Bash(git -C * config --edit*)"
    "Bash(git -C * config -e*)"
    "Bash(git -C * config set *)"
    "Bash(git -C * config unset *)"
    "Bash(git -C * config core.hooksPath*)"
    "Bash(git -C * config core.hookspath*)"
    "Bash(git -C * config * core.hooksPath*)"
    "Bash(git -C * config * core.hookspath*)"
    "Bash(git -C * config core.sshCommand*)"
    "Bash(git -C * config core.sshcommand*)"
    "Bash(git -C * config * core.sshCommand*)"
    "Bash(git -C * config * core.sshcommand*)"
    "Bash(git -C * config core.pager*)"
    "Bash(git -C * config * core.pager*)"
    "Bash(git -C * config core.editor*)"
    "Bash(git -C * config * core.editor*)"
    "Bash(git -C * config core.fsmonitor*)"
    "Bash(git -C * config * core.fsmonitor*)"
    "Bash(git -C * config alias.*)"
    "Bash(git -C * config * alias.*)"
    "Bash(git -C * config credential.helper*)"
    "Bash(git -C * config * credential.helper*)"
    "Bash(git -C * config core.gitProxy*)"
    "Bash(git -C * config core.gitproxy*)"
    "Bash(git -C * config * core.gitProxy*)"
    "Bash(git -C * config * core.gitproxy*)"
    "Bash(git -C * config core.askPass*)"
    "Bash(git -C * config core.askpass*)"
    "Bash(git -C * config * core.askPass*)"
    "Bash(git -C * config * core.askpass*)"
    "Bash(git -C * config sequence.editor*)"
    "Bash(git -C * config * sequence.editor*)"
    "Bash(git -C * config gpg.program*)"
    "Bash(git -C * config * gpg.program*)"
    "Bash(git -C * config diff.external*)"
    "Bash(git -C * config * diff.external*)"
    "Bash(git -C * remote set-url*)"
    "Bash(git -C * config remote.*)"
)
MATCHER_GAP_COUNT=82

echo "case: MATCHER_GAP_DENY_FORMS has exactly ${MATCHER_GAP_COUNT} elements"
if [[ "${#MATCHER_GAP_DENY_FORMS[@]}" -eq "${MATCHER_GAP_COUNT}" ]]; then
    pass
else
    fail "MATCHER_GAP_DENY_FORMS: expected exactly ${MATCHER_GAP_COUNT} elements, found ${#MATCHER_GAP_DENY_FORMS[@]}"
fi

echo "case: MATCHER_GAP_DENY_FORMS has ${MATCHER_GAP_COUNT} distinct elements (no self-duplicates)"
if ! MATCHER_GAP_DISTINCT_COUNT="$(printf '%s\n' "${MATCHER_GAP_DENY_FORMS[@]}" | sort -u | wc -l | tr -d ' ')"; then
    fail "MATCHER_GAP_DENY_FORMS: failed to compute distinct element count: ${MATCHER_GAP_DISTINCT_COUNT}"
elif [[ "${MATCHER_GAP_DISTINCT_COUNT}" -eq "${MATCHER_GAP_COUNT}" ]]; then
    pass
else
    fail "MATCHER_GAP_DENY_FORMS: expected ${MATCHER_GAP_COUNT} distinct elements, found ${MATCHER_GAP_DISTINCT_COUNT}"
fi

for i in 0 1 2; do
    label="${DENY_LABELS[$i]}"
    file="${DENY_FILES[$i]}"
    ok="${DENY_OKS[$i]}"

    for form in "${MATCHER_GAP_DENY_FORMS[@]}"; do
        echo "case: ${label} denies matcher-gap form '${form}'"
        if [[ "${ok}" -ne 1 ]]; then
            fail "${label}: cannot verify presence of '${form}' — extraction failed (see above)"
            continue
        fi
        if jq -e --arg e "${form}" 'any(.[]; . == $e)' "${file}" >/dev/null 2>&1; then
            pass
        else
            fail "${label}: permissions.deny missing matcher-gap entry '${form}'"
        fi
    done
done

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

# ── Heal clause anchors (#739, Q5; moved to phases/guidance-settings.md by #1343)
# Exact-sentence anchors, not generic keywords (flow/docs/shell-scripting-
# gotchas.md rule 3) — the heal step is prose an agent executes, not code,
# so a generic keyword match would pass vacuously against unrelated text.
# The heal paragraphs read the deny list from references/settings-template.md,
# so the phase file must also tell the agent to read it (the pointer sentence
# the Step Map row and "read it now" wording provide).
HEAL_MARKER='All base deny rules from the template **MUST** be present in `permissions.deny`. Only **append** new entries — never remove or replace existing ones, including user-added entries.'
SUPERSESSION_MARKER='(a) **remove** the legacy `Bash(git push --force:*)` and `Bash(git reset --hard:*)` entries when adding the base list — the boundary-safe forms below supersede them'
HEAL_POINTER_SENTENCE='The boundary-safe base deny list itself, with the rationale for its word boundaries and its known residual gaps, lives in `references/settings-template.md` — read it now, before writing or healing `permissions.deny`.'
STEP4_MARKER='4. **Create or update'
ALLOW_APPEND_MARKER='**Append to `permissions.allow`:**'

# offset_of <haystack> <needle> — pure extractor (no fail(), safe in $(...)):
# prints the byte offset of the first occurrence of needle and returns 0, or
# prints nothing and returns 1 when the needle is empty or absent.
offset_of() {
    local haystack="$1" needle="$2" prefix
    [[ -n "${needle}" && "${haystack}" == *"${needle}"* ]] || return 1
    prefix="${haystack%%"${needle}"*}"
    printf '%s' "${#prefix}"
}

echo "case: guidance-settings.md base-deny heal clause (IMPORTANT paragraph) present"
if GUIDANCE_CONTENT="$(cat "${GUIDANCE_MD}" 2>&1)"; then
    if [[ "${GUIDANCE_CONTENT}" == *"${HEAL_MARKER}"* ]]; then
        pass
    else
        fail "${GUIDANCE_REL}: missing base-deny heal clause anchor: [${HEAL_MARKER}]"
    fi

    echo "case: guidance-settings.md legacy-supersession clause present"
    if [[ "${GUIDANCE_CONTENT}" == *"${SUPERSESSION_MARKER}"* ]]; then
        pass
    else
        fail "${GUIDANCE_REL}: missing legacy-supersession clause anchor: [${SUPERSESSION_MARKER}]"
    fi

    echo "case: guidance-settings.md points the heal step at settings-template.md"
    # One literal pin of the full pointer sentence, plus its placement inside
    # step 4: after the step-4 marker and before the permissions.allow append
    # marker. Each offset is existence-paired (offset_of fails when absent).
    if [[ "${GUIDANCE_CONTENT}" != *"${HEAL_POINTER_SENTENCE}"* ]]; then
        fail "${GUIDANCE_REL}: missing heal pointer sentence anchor: [${HEAL_POINTER_SENTENCE}]"
    elif ! pointer_off="$(offset_of "${GUIDANCE_CONTENT}" "${HEAL_POINTER_SENTENCE}")"; then
        fail "${GUIDANCE_REL}: cannot locate the heal pointer sentence"
    elif ! step4_off="$(offset_of "${GUIDANCE_CONTENT}" "${STEP4_MARKER}")"; then
        fail "${GUIDANCE_REL}: cannot verify pointer placement — step-4 marker missing: [${STEP4_MARKER}]"
    elif ! append_off="$(offset_of "${GUIDANCE_CONTENT}" "${ALLOW_APPEND_MARKER}")"; then
        fail "${GUIDANCE_REL}: cannot verify pointer placement — permissions.allow append marker missing: [${ALLOW_APPEND_MARKER}]"
    elif [[ "${step4_off}" -lt "${pointer_off}" && "${pointer_off}" -lt "${append_off}" ]]; then
        pass
    else
        fail "${GUIDANCE_REL}: the heal pointer sentence must sit after step 4 and before the permissions.allow append (offsets step4=${step4_off} pointer=${pointer_off} append=${append_off}), or it could heal without reading the deny list"
    fi
else
    fail "${GUIDANCE_REL}: could not read file for heal-clause check: ${GUIDANCE_CONTENT}"
fi

# Paired absence: the heal/supersession prose is not left behind in SKILL.md.
# The existence markers are the anchors just asserted in the phase file, so a
# zero count here cannot pass because SKILL.md was emptied.
echo "case: SKILL.md no longer carries the heal/supersession prose"
if SKILL_CONTENT="$(cat "${SKILL_MD}" 2>&1)"; then
    if [[ "${SKILL_CONTENT}" != *"## Task"* ]]; then
        fail "flow/skills/configure/SKILL.md: cannot verify absence — existence marker '## Task' missing"
    elif [[ "${SKILL_CONTENT}" == *"${HEAL_MARKER}"* || "${SKILL_CONTENT}" == *"${SUPERSESSION_MARKER}"* ]]; then
        fail "flow/skills/configure/SKILL.md: still carries the base-deny heal or legacy-supersession prose that moved to ${GUIDANCE_REL}"
    else
        pass
    fi
else
    fail "flow/skills/configure/SKILL.md: could not read file for the heal-clause absence check: ${SKILL_CONTENT}"
fi

# ── Deny-list rationale prose anchors (#1343, Q4) ─────────────────────
# The rationale prose (word boundaries, `-C` coverage, `gh api` coverage,
# residual gaps) moved with the cenci:settings-deny block from SKILL.md into
# references/settings-template.md. One distinctive sentence per topic, pinned
# from the pre-move SKILL.md (`git show main:flow/skills/configure/SKILL.md`),
# never read from the files under test. Each must occur in the reference file
# and in neither SKILL.md nor phases/guidance-settings.md. The existence
# check on the reference file pairs with the absence checks, so a zero count
# elsewhere cannot pass because a file was emptied.
# #758 replaced the pre-move `-C <path>` coverage sentence (it said coverage
# was only for the highest-value groups, pending a Bash-matcher check that
# could not run) with the verified wording below, so the `-C` anchor now pins
# the rewritten prose rather than the pre-move text.
RATIONALE_SENTENCES=(
    'Deny always overrides allow, so a permitted form must never be caught by a deny rule.'
    'Claude Code does not strip `-C <path>` or `--git-dir=<path>` before matching a deny rule, so every deny group carries its own `-C`-prefixed mirror (probe S4a, probe S4b).'
    '`gh api` destructive-method coverage denies `DELETE`/`PUT`'
    'this residual list is illustrative of known gaps under literal prefix matching, not exhaustive.'
)

echo "case: deny-list rationale prose lives in settings-template.md, not SKILL.md or guidance-settings.md"
if SETTINGS_REF_CONTENT="$(cat "${SETTINGS_REF_MD}" 2>&1)" \
    && SKILL_RATIONALE_CONTENT="$(cat "${SKILL_MD}" 2>&1)" \
    && GUIDANCE_RATIONALE_CONTENT="$(cat "${GUIDANCE_MD}" 2>&1)"; then
    for sentence in "${RATIONALE_SENTENCES[@]}"; do
        if [[ "${SETTINGS_REF_CONTENT}" != *"${sentence}"* ]]; then
            fail "${SETTINGS_REF_REL}: missing deny-list rationale sentence: [${sentence}]"
        elif [[ "${SKILL_RATIONALE_CONTENT}" == *"${sentence}"* ]]; then
            fail "flow/skills/configure/SKILL.md: still carries deny-list rationale sentence that moved to ${SETTINGS_REF_REL}: [${sentence}]"
        elif [[ "${GUIDANCE_RATIONALE_CONTENT}" == *"${sentence}"* ]]; then
            fail "${GUIDANCE_REL}: carries deny-list rationale sentence that belongs only in ${SETTINGS_REF_REL}: [${sentence}]"
        else
            pass
        fi
    done
else
    fail "could not read settings-template.md, SKILL.md or guidance-settings.md for the rationale-prose check"
fi

# ── Evidence-doc contract (#758) ─────────────────────────────────────
# The unverified-matcher history above was closed by a one-time manual
# `claude -p` probe run recorded in flow/docs/claude-bash-matcher-evidence.md.
# These cases keep the prose and the record honest: the retired phrases are
# gone (paired with an existence anchor so an emptied file cannot pass), every
# `probe <ID>` the prose cites resolves to a row in the record, and the
# committed record carries no host paths, accounts, tokens or session IDs.
EVIDENCE_REL="flow/docs/claude-bash-matcher-evidence.md"
EVIDENCE_MD="${REPO_ROOT}/${EVIDENCE_REL}"

RETIRED_PHRASES=(
    'pending empirical Claude Code Bash-matcher verification'
    'no `claude` CLI installed'
)
RETIRED_EXISTENCE_ANCHOR='Deny always overrides allow, so a permitted form must never be caught by a deny rule.'

echo "case: ${EVIDENCE_REL} exists and is non-empty"
if [[ -r "${EVIDENCE_MD}" && -s "${EVIDENCE_MD}" ]]; then
    pass
else
    fail "${EVIDENCE_REL}: evidence doc missing, unreadable or empty"
fi

echo "case: settings-template.md no longer carries the retired unverified-matcher phrases"
if RETIRED_REF_CONTENT="$(cat "${SETTINGS_REF_MD}" 2>&1)"; then
    if [[ "${RETIRED_REF_CONTENT}" != *"${RETIRED_EXISTENCE_ANCHOR}"* ]]; then
        fail "${SETTINGS_REF_REL}: cannot verify absence of the retired phrases — existence anchor missing: [${RETIRED_EXISTENCE_ANCHOR}]"
    else
        for phrase in "${RETIRED_PHRASES[@]}"; do
            if [[ "${RETIRED_REF_CONTENT}" == *"${phrase}"* ]]; then
                fail "${SETTINGS_REF_REL}: still carries retired phrase: [${phrase}]"
            else
                pass
            fi
        done
    fi
else
    fail "${SETTINGS_REF_REL}: could not read file for the retired-phrase check: ${RETIRED_REF_CONTENT}"
fi

# Probe IDs start with one capital letter (S semantics, K -C coverage, G gh api,
# N new forms, H hardening forms, P positive controls) and take one of two
# shapes: digits with an optional lowercase suffix of at most two letters (S4a,
# S4ac for the paired control of S4a, G01, H10), or a
# hyphenated capital tag with optional digits (K-FP1, K-PX, N-CU1). Each cited
# ID must head a table row in the evidence doc.
#
# Extraction captures the WHOLE token after `probe ` (every letter, digit and
# hyphen up to the next other character) and validates it against the shape
# afterwards, so a cited `probe S4ac` can never be truncated to `S4a` and then
# resolve against a different row, and a `probe <Token>` the shape does not
# accept fails instead of being silently skipped. Lowercase prose such as
# "probe run" or "probe rows" is not a citation and is not captured.
PROBE_ID_SHAPE='^[SKGNHP](-[A-Z]+[0-9]*|[0-9]+[a-z]{0,2})$'
# probe_id_shape_ok <id> — pure: returns 0 only for a well-formed probe ID.
probe_id_shape_ok() {
    [[ "$1" =~ ${PROBE_ID_SHAPE} ]]
}
# probe_tokens <file> — pure: prints every `probe <Token>` citation, one per line.
probe_tokens() {
    grep -oE 'probe [A-Z0-9][A-Za-z0-9-]*' "$1"
}
# evidence_row_exists <id> <file> — pure: 0 when a table row heads with the ID.
evidence_row_exists() {
    grep -Eq "^[|] $1 [|]" "$2"
}
# evidence_observed <id> <file> — pure (no fail()): prints the `observed`
# column (the 5th data column: ID, probe command, rule(s), expected, observed)
# of the row headed by the ID and returns 0. Returns non-zero when the row is
# missing, grep itself failed, the ID heads more than one row, or the row does
# not have exactly EVIDENCE_ROW_PIPES `|` characters (six cells): a cell that
# holds a literal pipe shifts every later column, so the value read as
# `observed` would come from the wrong cell.
EVIDENCE_ROW_PIPES=7
evidence_observed() {
    local id="$1" file="$2" rows rc pipes _lead _id _cmd _rules _expected obs _rest
    rows="$(grep -E "^[|] ${id} [|]" "${file}")"
    rc=$?
    [[ "${rc}" -eq 0 && -n "${rows}" ]] || return 1
    [[ "${rows}" != *$'\n'* ]] || return 1
    pipes="${rows//[^|]/}"
    [[ "${#pipes}" -eq "${EVIDENCE_ROW_PIPES}" ]] || return 1
    IFS='|' read -r _lead _id _cmd _rules _expected obs _rest <<< "${rows}"
    obs="${obs//[[:space:]]/}"
    printf '%s' "${obs}"
    [[ -n "${obs}" ]]
}

PROBE_FIXTURE="${WORK_DIR}/probe-token-fixture.md"
printf '%s\n' 'bogus probe Z99x and probe S4acd, then probe S4a, probe S4ac, probe S4bc, probe S4b, probe G01, probe H10, probe N-CU1, probe P1, probe K-CG01 and probe N-NP2; a probe run and probe rows are prose.' > "${PROBE_FIXTURE}"
PROBE_FIXTURE_EXPECTED=$'probe Z99x\nprobe S4acd\nprobe S4a\nprobe S4ac\nprobe S4bc\nprobe S4b\nprobe G01\nprobe H10\nprobe N-CU1\nprobe P1\nprobe K-CG01\nprobe N-NP2'
# S4ac and S4bc are real rows (the paired no-deny controls of S4a and S4b), so
# a citation of either is legitimate and must be accepted. Z99x has no valid
# prefix and S4acd has a third suffix letter: both must be rejected, and S4acd
# must be captured whole rather than truncated to the real row S4ac.
echo "case: probe citation extraction self-test — whole tokens captured, real suffixed IDs accepted, bogus shapes rejected"
if ! fixture_tokens="$(probe_tokens "${PROBE_FIXTURE}")"; then
    fail "self-test: probe_tokens failed on the citation fixture"
elif [[ "${fixture_tokens}" != "${PROBE_FIXTURE_EXPECTED}" ]]; then
    fail "self-test: probe_tokens did not capture whole tokens, got: ${fixture_tokens}"
else
    shape_failures=""
    for shape_id in Z99x S4acd; do
        if probe_id_shape_ok "${shape_id}"; then
            shape_failures="${shape_failures} ${shape_id}(accepted)"
        fi
    done
    for shape_id in S4a S4b S4ac S4bc S1c G01 H10 N-CU1 P1 K-CG01 N-NP2 N-EV1; do
        probe_id_shape_ok "${shape_id}" || shape_failures="${shape_failures} ${shape_id}(rejected)"
    done
    if [[ -n "${shape_failures}" ]]; then
        fail "self-test: probe_id_shape_ok misjudged IDs:${shape_failures}"
    else
        pass
    fi
fi

# A well-formed row has six cells, so exactly seven unescaped `|` characters.
# A cell with a literal pipe (escaped `\|` or not) shifts every later column,
# so the observed value would be read from the wrong cell; and a repeated row
# ID would silently resolve to whichever row came first.
OBSERVED_FIXTURE="${WORK_DIR}/observed-fixture.md"
printf '%s\n' \
    '| X1 | cmd | rule | denied (desired); executed is a known gap | executed | stub log |' \
    '| X2 | cmd | rule | executed | denied | tool result |' \
    '| X4 | a \| b | rule | executed | denied | tool result |' \
    '| X5 | cmd | rule | extra | executed | denied | tool result |' \
    '| X6 | cmd | rule | executed | denied | tool result |' \
    '| X6 | cmd | rule | executed | executed | tool result |' > "${OBSERVED_FIXTURE}"
echo "case: evidence_observed self-test — reads the observed column, rejects shifted columns and duplicate IDs"
if ! x1="$(evidence_observed X1 "${OBSERVED_FIXTURE}")" || ! x2="$(evidence_observed X2 "${OBSERVED_FIXTURE}")"; then
    fail "self-test: evidence_observed failed on a well-formed fixture row"
elif [[ "${x1}" != "executed" || "${x2}" != "denied" ]]; then
    fail "self-test: evidence_observed read the wrong column (X1=${x1}, X2=${x2})"
elif evidence_observed X3 "${OBSERVED_FIXTURE}" >/dev/null; then
    fail "self-test: evidence_observed returned success for a row that does not exist"
elif evidence_observed X4 "${OBSERVED_FIXTURE}" >/dev/null; then
    fail "self-test: evidence_observed accepted a row with an escaped pipe in a cell (columns shifted)"
elif evidence_observed X5 "${OBSERVED_FIXTURE}" >/dev/null; then
    fail "self-test: evidence_observed accepted a row with an extra column separator"
elif evidence_observed X6 "${OBSERVED_FIXTURE}" >/dev/null; then
    fail "self-test: evidence_observed accepted a duplicated row ID instead of failing"
else
    pass
fi

CITED_IDS_FILE="${WORK_DIR}/cited-probe-ids.txt"
echo "case: settings-template.md cites at least one 'probe <ID>' (non-vacuity)"
probe_tokens "${SETTINGS_REF_MD}" > "${CITED_IDS_FILE}"
cited_status=$?
if [[ "${cited_status}" -gt 1 ]]; then
    fail "${SETTINGS_REF_REL}: grep failed to extract probe citations (exit ${cited_status})"
elif [[ ! -s "${CITED_IDS_FILE}" ]]; then
    fail "${SETTINGS_REF_REL}: no 'probe <ID>' citation found — every remaining gap sentence must cite its evidence row"
else
    pass
fi

echo "case: every 'probe <Token>' in settings-template.md is a well-formed probe ID"
if [[ ! -s "${CITED_IDS_FILE}" ]]; then
    fail "${SETTINGS_REF_REL}: cannot check citation shapes — none extracted (see above)"
else
    malformed=""
    while IFS= read -r cited; do
        probe_id_shape_ok "${cited#probe }" || malformed="${malformed} ${cited#probe }"
    done < "${CITED_IDS_FILE}"
    if [[ -n "${malformed}" ]]; then
        fail "${SETTINGS_REF_REL}: 'probe <Token>' citations that are not a well-formed probe ID:${malformed}"
    else
        pass
    fi
fi

echo "case: every probe <ID> cited in settings-template.md resolves to a row in ${EVIDENCE_REL}"
if [[ ! -r "${EVIDENCE_MD}" ]]; then
    fail "${EVIDENCE_REL}: cannot resolve probe citations — evidence doc missing or unreadable"
elif [[ ! -s "${CITED_IDS_FILE}" ]]; then
    fail "${SETTINGS_REF_REL}: cannot resolve probe citations — none extracted (see above)"
else
    unresolved=""
    resolved_count=0
    while IFS= read -r cited; do
        cited_id="${cited#probe }"
        if evidence_row_exists "${cited_id}" "${EVIDENCE_MD}"; then
            resolved_count=$((resolved_count + 1))
        else
            unresolved="${unresolved} ${cited_id}"
        fi
    done < "${CITED_IDS_FILE}"
    if [[ -n "${unresolved}" ]]; then
        fail "${SETTINGS_REF_REL}: cites probe IDs with no row in ${EVIDENCE_REL}:${unresolved}"
    elif [[ "${resolved_count}" -eq 0 ]]; then
        fail "probe citation resolution is vacuous: no citation was checked"
    else
        pass
    fi
fi

# Direction check: a row existing is not enough, the prose cites some rows as
# denied coverage and others as known gaps (`executed`), and a row whose
# observed column says the opposite would turn the citation into a false claim.
# The three lists below pin the observed value of every row the prose leans on;
# they are set from the final-template table as recorded, and an ID in the
# wrong list (or a row edited to a different observed value) fails here.
# Rows judged `inconclusive` (S5c, H10, N-EV1) are cited only as "unjudged".
EVIDENCE_DENIED_IDS=(
    S3 S4p S5 S7
    N-CU1 N-CU2 N-BL3 N-GD1 N-WT1 N-FD1 N-FD2 N-FD3
    G01 G02 G03 G04 G05 G06 G07 G08 G09 G10 G13 G14 G15 G16
    K-CG01 H1 H2 H3 H4 H6 H9
)
EVIDENCE_EXECUTED_IDS=(
    S1 S2 S4a S4b S6
    P1 P2 P3 P4 P5
    N-CG1 N-CG2 N-NP1 N-NP2 N-NP3 N-NP4 N-FD4 N-FD5
    G11 H11 H12 H13 H14 H15 H16
)
EVIDENCE_INCONCLUSIVE_IDS=(S5c H10 N-EV1)

# Coverage of the lists themselves: the direction check below only looks at the
# IDs the lists name, so a cited probe ID that is in none of them would never
# have its observed value checked, and an ID in two lists would be demanded to
# be both denied and executed (or silently satisfied by whichever list agrees).
# direction_list_problems <cited-ids-file> — pure (no fail(), safe in $(...)):
# prints one problem per line and returns non-zero when there is any.
direction_list_problems() {
    local cited_file="$1" cited cited_id member other count hits problems="" seen=" " dup_seen=" "
    local -a all_ids=("${EVIDENCE_DENIED_IDS[@]}" "${EVIDENCE_EXECUTED_IDS[@]}" "${EVIDENCE_INCONCLUSIVE_IDS[@]}")
    while IFS= read -r cited; do
        cited_id="${cited#probe }"
        case "${seen}" in *" ${cited_id} "*) continue ;; esac
        seen="${seen}${cited_id} "
        hits=0
        for member in "${all_ids[@]}"; do
            if [[ "${member}" == "${cited_id}" ]]; then
                hits=$((hits + 1))
            fi
        done
        if [[ "${hits}" -eq 0 ]]; then
            problems="${problems}cited probe ${cited_id} is in none of the direction lists (its observed value is never checked)"$'\n'
        fi
    done < "${cited_file}"
    for member in "${all_ids[@]}"; do
        case "${dup_seen}" in *" ${member} "*) continue ;; esac
        count=0
        for other in "${all_ids[@]}"; do
            if [[ "${other}" == "${member}" ]]; then
                count=$((count + 1))
            fi
        done
        if [[ "${count}" -gt 1 ]]; then
            dup_seen="${dup_seen}${member} "
            problems="${problems}probe ${member} appears ${count} times across the direction lists (it must be in exactly one)"$'\n'
        fi
    done
    printf '%s' "${problems}"
    [[ -z "${problems}" ]]
}

LIST_FIXTURE_CITED="${WORK_DIR}/direction-list-cited.txt"
printf '%s\n' 'probe A1' 'probe A2' 'probe A2' 'probe B1' > "${LIST_FIXTURE_CITED}"
echo "case: direction-list coverage self-test — an unlisted citation and a duplicated ID are each reported"
list_clean_out="$(EVIDENCE_DENIED_IDS=(A1) EVIDENCE_EXECUTED_IDS=(A2) EVIDENCE_INCONCLUSIVE_IDS=(B1 C1); direction_list_problems "${LIST_FIXTURE_CITED}")"
list_clean_rc=$?
list_missing_out="$(EVIDENCE_DENIED_IDS=(A1) EVIDENCE_EXECUTED_IDS=(A2) EVIDENCE_INCONCLUSIVE_IDS=(C1); direction_list_problems "${LIST_FIXTURE_CITED}")"
list_missing_rc=$?
list_dup_out="$(EVIDENCE_DENIED_IDS=(A1 A2) EVIDENCE_EXECUTED_IDS=(A2) EVIDENCE_INCONCLUSIVE_IDS=(B1); direction_list_problems "${LIST_FIXTURE_CITED}")"
list_dup_rc=$?
if [[ "${list_clean_rc}" -ne 0 ]]; then
    fail "self-test: direction_list_problems rejected a consistent fixture: ${list_clean_out}"
elif [[ "${list_missing_rc}" -eq 0 || "${list_missing_out}" != *"cited probe B1 is in none"* ]]; then
    fail "self-test: direction_list_problems missed a cited ID that is in no list (rc=${list_missing_rc}, out=${list_missing_out})"
elif [[ "${list_dup_rc}" -eq 0 || "${list_dup_out}" != *"probe A2 appears 2 times"* ]]; then
    fail "self-test: direction_list_problems missed an ID present in two lists (rc=${list_dup_rc}, out=${list_dup_out})"
else
    pass
fi

echo "case: every cited probe ID is in exactly one direction list, and no ID is in two"
if [[ ! -s "${CITED_IDS_FILE}" ]]; then
    fail "${SETTINGS_REF_REL}: cannot check direction-list coverage — no citations extracted (see above)"
elif list_problems="$(direction_list_problems "${CITED_IDS_FILE}")"; then
    pass
else
    fail "direction-list coverage: ${list_problems//$'\n'/; }"
fi

# check_evidence_direction <observed> <id>... — one pass/fail per ID.
check_evidence_direction() {
    local want="$1" id got
    shift
    for id in "$@"; do
        if ! got="$(evidence_observed "${id}" "${EVIDENCE_MD}")"; then
            fail "${EVIDENCE_REL}: no readable row (or no observed column) for ${id}; expected ${want}"
        elif [[ "${got}" != "${want}" ]]; then
            fail "${EVIDENCE_REL}: row ${id} observed '${got}', but the direction list expects ${want} (the settings-template.md prose and the evidence-doc rows that lean on it cite it that way)"
        else
            pass
        fi
    done
}

echo "case: direction allowlists are non-empty (non-vacuity)"
if [[ "${#EVIDENCE_DENIED_IDS[@]}" -eq 0 || "${#EVIDENCE_EXECUTED_IDS[@]}" -eq 0 || "${#EVIDENCE_INCONCLUSIVE_IDS[@]}" -eq 0 ]]; then
    fail "evidence direction allowlists are empty: the direction check would be vacuous"
else
    pass
fi

echo "case: rows cited as denied coverage are observed denied"
check_evidence_direction denied "${EVIDENCE_DENIED_IDS[@]}"
echo "case: rows cited as known gaps, controls or scope statements are observed executed"
check_evidence_direction executed "${EVIDENCE_EXECUTED_IDS[@]}"
echo "case: rows cited as unjudged are observed inconclusive"
check_evidence_direction inconclusive "${EVIDENCE_INCONCLUSIVE_IDS[@]}"

# evidence_leak_hits <file> — pure (no fail(), safe in $(...)): prints a
# comma-separated list of leak patterns found and returns 0 only when none.
# A grep exit status above 1 (unreadable or missing file, bad pattern) is
# reported as a hit too: "could not scan" must never read as "no match".
EVIDENCE_LEAK_LITERALS=('/home/' '/Users/' '/workspace' '/root/' '/mnt/' '/var/' '/private/' '/tmp/claude' '/tmp/cenci/' 'ghp_' 'gho_' 'github_pat_' 'sk-ant' 'toolu_')
EVIDENCE_UUID_RE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
evidence_leak_hits() {
    local file="$1" pat hits="" rc
    for pat in "${EVIDENCE_LEAK_LITERALS[@]}"; do
        grep -Fq -- "${pat}" "${file}" 2>/dev/null
        rc=$?
        if [[ "${rc}" -eq 0 ]]; then
            hits="${hits}${hits:+, }${pat}"
        elif [[ "${rc}" -gt 1 ]]; then
            hits="${hits}${hits:+, }scan error (grep exit ${rc}) for ${pat}"
        fi
    done
    grep -Eq -- "${EVIDENCE_UUID_RE}" "${file}" 2>/dev/null
    rc=$?
    if [[ "${rc}" -eq 0 ]]; then
        hits="${hits}${hits:+, }session-id-shaped UUID"
    elif [[ "${rc}" -gt 1 ]]; then
        hits="${hits}${hits:+, }scan error (grep exit ${rc}) for the UUID pattern"
    fi
    printf '%s' "${hits}"
    [[ -z "${hits}" ]]
}

# Self-test: the detector is not vacuous (a leaking fixture is rejected, a
# clean placeholder-only fixture is accepted).
LEAK_FIXTURE="${WORK_DIR}/evidence-leak-fixture.md"
CLEAN_FIXTURE="${WORK_DIR}/evidence-clean-fixture.md"
printf '%s\n' '| S1 | git -C /home/someone/repo status |' > "${LEAK_FIXTURE}"
printf '%s\n' '| S1 | git -C <repo> status |' > "${CLEAN_FIXTURE}"
echo "case: evidence leak detector self-test — leaking and clean fixtures"
if evidence_leak_hits "${LEAK_FIXTURE}" >/dev/null; then
    fail "self-test: evidence_leak_hits accepted a fixture containing a home path (detector is vacuous)"
elif ! evidence_leak_hits "${CLEAN_FIXTURE}" >/dev/null; then
    fail "self-test: evidence_leak_hits rejected a placeholder-only fixture (positive control failed)"
else
    pass
fi

# Every branch of the detector gets its own fixture: one for the UUID pattern
# (its text is assembled at runtime from fragments so this test file never
# contains a real-looking UUID that a scan of the repository would flag) and one
# generated per entry of EVIDENCE_LEAK_LITERALS, so adding a literal to the
# detector automatically adds a fixture that must trip it. The three arrays are
# parallel (name, text, the pattern the report must name); the loop is driven by
# their length, and a length mismatch or an empty want pattern (`*""*` matches
# any report) fails the case instead of letting a fixture pass vacuously.
LEAK_FIXTURE_NAMES=(uuid)
LEAK_FIXTURE_WANTS=('UUID')
LEAK_FIXTURE_TEXTS=("$(printf '| S1 | session %s-%s-%s-%s-%s |' 12345678 1234 1234 1234 123456789012)")
for leak_literal in "${EVIDENCE_LEAK_LITERALS[@]}"; do
    LEAK_FIXTURE_NAMES+=("literal-${#LEAK_FIXTURE_NAMES[@]}")
    LEAK_FIXTURE_WANTS+=("${leak_literal}")
    LEAK_FIXTURE_TEXTS+=("$(printf '| S1 | x%sx |' "${leak_literal}")")
done
echo "case: evidence leak detector self-test — the UUID and every leak literal each get a fixture that is rejected"
leak_branch_failures=""
leak_fixture_count="${#LEAK_FIXTURE_TEXTS[@]}"
if [[ "${#LEAK_FIXTURE_NAMES[@]}" -ne "${leak_fixture_count}" || "${#LEAK_FIXTURE_WANTS[@]}" -ne "${leak_fixture_count}" ]]; then
    leak_branch_failures=" parallel fixture arrays differ in length (names=${#LEAK_FIXTURE_NAMES[@]}, texts=${leak_fixture_count}, wants=${#LEAK_FIXTURE_WANTS[@]})"
elif [[ "${leak_fixture_count}" -ne $(( ${#EVIDENCE_LEAK_LITERALS[@]} + 1 )) ]]; then
    leak_branch_failures=" expected one UUID fixture plus one per leak literal, got ${leak_fixture_count} fixtures for ${#EVIDENCE_LEAK_LITERALS[@]} literals"
else
    for ((idx = 0; idx < leak_fixture_count; idx++)); do
        if [[ -z "${LEAK_FIXTURE_WANTS[$idx]}" ]]; then
            leak_branch_failures="${leak_branch_failures} ${LEAK_FIXTURE_NAMES[$idx]}(empty want pattern)"
            continue
        fi
        leak_file="${WORK_DIR}/evidence-leak-${LEAK_FIXTURE_NAMES[$idx]}.md"
        printf '%s\n' "${LEAK_FIXTURE_TEXTS[$idx]}" > "${leak_file}"
        if leak_out="$(evidence_leak_hits "${leak_file}")"; then
            leak_branch_failures="${leak_branch_failures} ${LEAK_FIXTURE_NAMES[$idx]}(accepted)"
        elif [[ "${leak_out}" != *"${LEAK_FIXTURE_WANTS[$idx]}"* ]]; then
            leak_branch_failures="${leak_branch_failures} ${LEAK_FIXTURE_NAMES[$idx]}(reported: ${leak_out})"
        fi
    done
fi
if [[ -n "${leak_branch_failures}" ]]; then
    fail "self-test: evidence_leak_hits missed or mislabelled fixtures:${leak_branch_failures}"
else
    pass
fi

echo "case: evidence leak detector self-test — an unreadable file is a failure, not 'no match'"
if evidence_leak_hits "${WORK_DIR}/no-such-evidence-file.md" >/dev/null; then
    fail "self-test: evidence_leak_hits reported a nonexistent file as clean (grep exit status >1 was read as no match)"
else
    pass
fi

echo "case: ${EVIDENCE_REL} contains no absolute host paths, accounts, tokens or session IDs"
if [[ ! -r "${EVIDENCE_MD}" ]]; then
    fail "${EVIDENCE_REL}: cannot verify sanitization — evidence doc missing or unreadable"
elif ! grep -Fq -- '<repo>' "${EVIDENCE_MD}"; then
    fail "${EVIDENCE_REL}: sanitization check is vacuous — no '<repo>' placeholder found in the record"
elif leak_hits="$(evidence_leak_hits "${EVIDENCE_MD}")"; then
    pass
else
    fail "${EVIDENCE_REL}: contains host-specific or sensitive content: ${leak_hits}"
fi

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
