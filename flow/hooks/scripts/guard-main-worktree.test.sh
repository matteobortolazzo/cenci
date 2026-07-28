#!/usr/bin/env bash
# Tests for guard-main-worktree.sh. Follows the repo's shell-test precedent
# (flow/skills/babysit/scripts/tick.test.sh, sandbox/tests/*.test.sh):
# plain bash, no framework, PASS/FAIL counters, non-zero exit on failure.
# Each case runs in a fresh directory under one mktemp root and drives the
# hook script with PreToolUse-style JSON on stdin.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD_SH="${SCRIPT_DIR}/guard-main-worktree.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# run_guard <cwd> <json> — runs the hook from <cwd> with <json> on stdin.
# Sets GUARD_EXIT and GUARD_STDERR.
run_guard() {
    local cwd="$1" json="$2"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | sh "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${GUARD_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${GUARD_EXIT}, expected ${expected}"
    fi
}

# run_guard_with_path <cwd> <path_override> <json> — like run_guard, but
# overrides PATH with a curated bin dir before invoking the hook. Used to
# simulate a PATH missing specific external tools (fail-closed tests).
# Sets GUARD_EXIT and GUARD_STDERR.
run_guard_with_path() {
    local cwd="$1" path_override="$2" json="$3"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | PATH="${path_override}" sh "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

# run_guard_with_tmpdir <cwd> <tmpdir_override> <json> — like run_guard, but
# sets TMPDIR to <tmpdir_override> for a single invocation. Used to drive the
# #749 TMPDIR-widening cases below. Sets GUARD_EXIT and GUARD_STDERR.
run_guard_with_tmpdir() {
    local cwd="$1" tmpdir_override="$2" json="$3"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | TMPDIR="${tmpdir_override}" sh "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

# make_curated_bin <dir> <tool>... — creates <dir> containing symlinks to the
# real binaries for each named tool (resolved from the current PATH) and
# nothing else. Used with run_guard_with_path to simulate a PATH that is
# missing specific tools.
make_curated_bin() {
    local dir="$1"
    shift
    mkdir -p "${dir}"
    local tool real
    for tool in "$@"; do
        real="$(command -v "${tool}")" || {
            echo "make_curated_bin: '${tool}' not found on PATH" >&2
            exit 1
        }
        ln -s "${real}" "${dir}/${tool}"
    done
}

# make_mktemp_fail_bin <dir> <rm_log> <tool>... — like make_curated_bin, but
# additionally plants a fake `mktemp` that always fails (exit 1, no output,
# simulating e.g. TMPDIR exhaustion) and a fake `rm` that appends its argv to
# <rm_log> (one invocation per line) before delegating to the real `rm` (its
# path captured at shim-creation time via `command -v rm`, never hardcoded).
# Used to prove the mktemp-failure fallback (#550): the fallback must never
# assign /dev/null to a path later passed to rm, and must not drop jq's own
# diagnostic.
make_mktemp_fail_bin() {
    local dir="$1" rm_log="$2"
    shift 2
    mkdir -p "${dir}"
    cat > "${dir}/mktemp" <<'SCRIPT'
#!/bin/sh
exit 1
SCRIPT
    chmod +x "${dir}/mktemp"
    local real_rm
    real_rm="$(command -v rm)" || {
        echo "make_mktemp_fail_bin: 'rm' not found on PATH" >&2
        exit 1
    }
    cat > "${dir}/rm" <<SCRIPT
#!/bin/sh
printf '%s\n' "\$*" >> '${rm_log}'
exec '${real_rm}' "\$@"
SCRIPT
    chmod +x "${dir}/rm"
    local tool real
    for tool in "$@"; do
        real="$(command -v "${tool}")" || {
            echo "make_mktemp_fail_bin: '${tool}' not found on PATH" >&2
            exit 1
        }
        ln -s "${real}" "${dir}/${tool}"
    done
}

echo "guard-main-worktree.test.sh"

# Not under /tmp: the guard allowlists /tmp/* paths, which would make every
# case exit 0 regardless of the config gate. /var/tmp is not allowlisted.
TEST_ROOT="$(mktemp -d /var/tmp/guard-main-worktree-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# Normalize TMPDIR for the whole suite (#749): TEST_ROOT deliberately lives
# under /var/tmp specifically to dodge the /tmp/* arm, and run-checks.sh:137
# runs every suite with the ambient environment -- an inherited
# TMPDIR=/var/tmp (or any other custom value) would silently widen the new
# TMPDIR allowlist and turn every pre-existing "should still be blocked" case
# into a false pass. Each #749 case below sets TMPDIR explicitly for its own
# single invocation via run_guard_with_tmpdir; every other case must run with
# TMPDIR genuinely unset.
unset TMPDIR

make_git_repo() {
    local dir="$1"
    mkdir -p "${dir}"
    git -C "${dir}" init -q
}

# ── Case 1: unconfigured git repo → guard is a no-op (the bug fix) ──
echo "case: unconfigured git repo allows source writes"
REPO="${TEST_ROOT}/unconfigured"
make_git_repo "${REPO}"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "unconfigured repo" 0

# ── Case 2: non-git directory, no config → no-op ────────────────────
echo "case: non-git directory allows source writes"
DIR="${TEST_ROOT}/plain-dir"
mkdir -p "${DIR}"
run_guard "${DIR}" "{\"tool_input\":{\"file_path\":\"${DIR}/src/foo.txt\"}}"
assert_exit "non-git dir" 0

# ── Case 3: configured repo → source writes blocked ─────────────────
echo "case: configured repo blocks source writes"
REPO="${TEST_ROOT}/configured"
make_git_repo "${REPO}"
mkdir -p "${REPO}/.cenci"
touch "${REPO}/.cenci/config.json"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "configured repo" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "configured repo: stderr should contain BLOCKED"
fi

# ── Case 4: configured repo, .worktrees/ path → allowlisted ─────────
echo "case: configured repo allows .worktrees/ writes"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.worktrees/1-feat/src/foo.txt\"}}"
assert_exit "worktree path" 0

# ── Case 5: configured repo, cwd in a subdirectory → still blocked ──
echo "case: configured repo blocks from a subdirectory cwd"
mkdir -p "${REPO}/src/nested"
run_guard "${REPO}/src/nested" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "subdirectory cwd" 2

# ── Case 6: missing file_path → no-op (existing behavior) ───────────
echo "case: missing file_path is a no-op"
run_guard "${REPO}" "{\"tool_input\":{}}"
assert_exit "missing file_path" 0

# ── Case 7: configured repo, .cenci/ path → blocked (configure now ships
# its own writes through a feature worktree + PR, so no main-worktree
# carve-out is needed for it) ────────────────────────────────────────
echo "case: configured repo blocks .cenci/ writes outside a worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.cenci/Dockerfile\"}}"
assert_exit ".cenci path" 2

echo "case: configured repo blocks AGENTS.md writes outside a worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/AGENTS.md\"}}"
assert_exit "AGENTS.md path" 2

echo "case: configured repo allows .cenci/ writes inside a feature worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.worktrees/configure-init/.cenci/Dockerfile\"}}"
assert_exit ".cenci path inside worktree" 0

echo "case: legacy-only configured repo remains protected"
LEGACY_REPO="${TEST_ROOT}/legacy-configured"
make_git_repo "${LEGACY_REPO}"
mkdir -p "${LEGACY_REPO}/.claude"
touch "${LEGACY_REPO}/.claude/config.json"
run_guard "${LEGACY_REPO}" "{\"tool_input\":{\"file_path\":\"${LEGACY_REPO}/src/foo.txt\"}}"
assert_exit "legacy configured repo" 2

# ── Case 8: non-git dir with its own .claude/config.json → no-op ────
# Pre-fix, ROOT=$(pwd) fallback would let this directory's own config.json
# satisfy the gate and incorrectly enforce (#371).
echo "case: non-git directory with .claude/config.json is still a no-op"
DIR="${TEST_ROOT}/non-git-with-config"
mkdir -p "${DIR}/.claude"
touch "${DIR}/.claude/config.json"
run_guard "${DIR}" "{\"tool_input\":{\"file_path\":\"${DIR}/src/foo.txt\"}}"
assert_exit "non-git dir with config.json" 0

# ── Case 9: configured repo, native Plan Mode storage under the sandbox
# container's home (/home/dev/.claude/plans/) → allowlisted (#431). The
# hook only ever sees an absolute file_path, never a real $HOME, so this
# exercises the container-home shape of that path.
echo "case: configured repo allows .claude/plans/ writes under container home"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"/home/dev/.claude/plans/plan.md\"}}"
assert_exit "container home .claude/plans path" 0

# ── Case 10: configured repo, native Plan Mode storage under an arbitrary
# host $HOME (not /home/dev) → allowlisted (#431). Proves the match is
# portable across $HOME values, not hardcoded to the container's home.
echo "case: configured repo allows .claude/plans/ writes under an arbitrary host home"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/home-host/.claude/plans/plan.md\"}}"
assert_exit "arbitrary host home .claude/plans path" 0

# ── Case 11: configured repo, source path adjacent to .claude but not under
# plans/ → still blocked (#431). Guards against an over-broad glob that
# would allow any .claude*-prefixed path.
echo "case: configured repo blocks source paths that merely resemble .claude/plans/"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/.claude-notes.md\"}}"
assert_exit "adjacent-to-.claude source path" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "adjacent-to-.claude source path: stderr should contain BLOCKED"
fi

# ── Hardening cases (#440): normalize file_path and use real JSON parsing ──
# Run against a dedicated repo so these cases don't depend on directory
# state left behind by earlier cases (e.g. case 5 creates $REPO/src/nested).
HARDEN_REPO="${TEST_ROOT}/hardening"
make_git_repo "${HARDEN_REPO}"
mkdir -p "${HARDEN_REPO}/.cenci"
touch "${HARDEN_REPO}/.cenci/config.json"

# Case: a "../.." substring makes the raw path contain /.worktrees/ (an
# allowlisted substring) but it must be normalized (.. collapsed) before the
# allowlist check — it canonicalizes to $HARDEN_REPO/src/evil.txt, outside
# any worktree. $HARDEN_REPO/src must not exist so this exercises
# normalization of a path whose target does not exist yet.
echo "case: ..-substring path is normalized before the allowlist match and blocked"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/x/../../src/evil.txt\"}}"
assert_exit "dot-dot substring bypass" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "dot-dot substring bypass: stderr should contain BLOCKED"
fi

# Case: a symlink planted under an allowlisted directory (.worktrees/) that
# resolves outside the worktree must be blocked — the literal path string
# matching the allowlist substring is not enough; it must be resolved first.
echo "case: symlink under .worktrees/ resolving outside the worktree is blocked"
mkdir -p "${TEST_ROOT}/outside"
mkdir -p "${HARDEN_REPO}/.worktrees"
ln -s "${TEST_ROOT}/outside" "${HARDEN_REPO}/.worktrees/link"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/link/evil.txt\"}}"
assert_exit "symlink escape via .worktrees/" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink escape via .worktrees/: stderr should contain BLOCKED"
fi

# Case: a symlinked allowlisted directory whose escape target has an existing
# ancestor several levels below the symlink itself, but the immediate tail
# (newdir/) does not exist yet. This must still resolve the symlink by
# walking up to the nearest existing ancestor (existingsub/), not just the
# immediate parent — regression test for the ancestor-walk fix (#440).
echo "case: symlink escape with a multi-level missing tail is blocked"
mkdir -p "${TEST_ROOT}/outside/existingsub"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/link/existingsub/newdir/newfile.txt\"}}"
assert_exit "symlink escape with multi-level missing tail" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink escape with multi-level missing tail: stderr should contain BLOCKED"
fi

# Case: jq missing from PATH must fail closed (block), not silently fall
# through to an allow. The curated PATH still has git/cat, so the config
# gate passes through to the jq check rather than short-circuiting at exit 0.
echo "case: jq missing from PATH fails closed"
JQ_MISSING_BIN="${TEST_ROOT}/bin-no-jq"
make_curated_bin "${JQ_MISSING_BIN}" sh git cat realpath
run_guard_with_path "${HARDEN_REPO}" "${JQ_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/src/foo.txt\"}}"
assert_exit "jq missing fails closed" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "jq missing fails closed: stderr should contain BLOCKED"
fi

# Case: a relative file_path (no leading /) must fail closed rather than be
# silently normalized relative to some directory and potentially match an
# allowlisted pattern by coincidence.
echo "case: relative file_path fails closed"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"src/foo.txt\"}}"
assert_exit "relative file_path fails closed" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "relative file_path fails closed: stderr should contain BLOCKED"
fi

# Case: the true tool_input.file_path is a blocked source path, but an
# unrelated field (old_string, as in an Edit tool call) contains a lookalike
# '"file_path": "..."' substring pointing at an allowlisted path. The hook
# must key off the real tool_input.file_path field via jq, not an incidental
# substring found anywhere in the raw JSON. Built via jq -n --arg (never
# hand-escaped strings) to guarantee valid nesting/escaping.
echo "case: lookalike file_path substring elsewhere in the JSON is ignored"
LOOKALIKE_JSON=$(jq -n --arg fp "${HARDEN_REPO}/src/evil.txt" --arg os '"file_path": "'"${HARDEN_REPO}"'/.worktrees/safe/x.txt"' '{tool_input:{file_path:$fp,old_string:$os}}')
run_guard "${HARDEN_REPO}" "${LOOKALIKE_JSON}"
assert_exit "lookalike file_path substring ignored" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "lookalike file_path substring ignored: stderr should contain BLOCKED"
fi

# Case: both realpath and readlink missing from PATH must also fail closed
# (the resolver step, not just the jq step).
echo "case: path resolver (realpath/readlink) missing from PATH fails closed"
RESOLVER_MISSING_BIN="${TEST_ROOT}/bin-no-resolver"
make_curated_bin "${RESOLVER_MISSING_BIN}" sh git cat jq
run_guard_with_path "${HARDEN_REPO}" "${RESOLVER_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/src/foo.txt\"}}"
assert_exit "resolver missing fails closed" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "resolver missing fails closed: stderr should contain BLOCKED"
fi

# Case: a symlink loop leaf directly under the .worktrees/ allowlist must
# still be blocked -- an unresolvable ELOOP node is a fail-closed signal
# that must win over the allowlist substring match, not be walked past.
echo "case: symlink loop leaf under .worktrees/ allowlist is blocked"
mkdir -p "${HARDEN_REPO}/.worktrees/loop"
ln -s b "${HARDEN_REPO}/.worktrees/loop/a"
ln -s a "${HARDEN_REPO}/.worktrees/loop/b"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/loop/a\"}}"
assert_exit "symlink loop leaf under .worktrees/" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink loop leaf under .worktrees/: stderr should contain BLOCKED"
fi

# Case: a symlink loop as an intermediate ancestor (not the leaf) under the
# .worktrees/ allowlist, with a not-yet-existing tail below it, must also be
# blocked -- proves the fix isn't leaf-only.
echo "case: symlink loop as an intermediate ancestor under .worktrees/ allowlist is blocked"
mkdir -p "${HARDEN_REPO}/.worktrees/loop2"
ln -s b "${HARDEN_REPO}/.worktrees/loop2/a"
ln -s a "${HARDEN_REPO}/.worktrees/loop2/b"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/loop2/a/newsub/new.txt\"}}"
assert_exit "symlink loop intermediate under .worktrees/" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink loop intermediate under .worktrees/: stderr should contain BLOCKED"
fi

# Case: a dangling symlink leaf under the .worktrees/ allowlist (real
# symlink node, unresolvable target) must be blocked.
echo "case: dangling symlink leaf under .worktrees/ allowlist is blocked"
ln -s "${TEST_ROOT}/nonexistent-target-worktree" "${HARDEN_REPO}/.worktrees/dangling-link"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/dangling-link\"}}"
assert_exit "dangling symlink leaf under .worktrees/" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "dangling symlink leaf under .worktrees/: stderr should contain BLOCKED"
fi

# Case: a present-but-empty file_path must fall back to filePath (jq `//`
# bug). jq's `//` alternative operator only falls back on null/false, NOT on
# an empty string. A payload with BOTH keys present, file_path set to "" and
# filePath set to a genuine main-worktree source path, previously extracted
# "" (the `//` never tried filePath because "" is not null) and hit the
# empty-path early `exit 0` -- silently ALLOWING the write. The extraction
# must check emptiness explicitly so a present-but-empty file_path correctly
# falls through to filePath.
echo "case: empty file_path falls back to filePath (jq // does not treat \"\" as fallback trigger)"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"\",\"filePath\":\"${HARDEN_REPO}/src/foo.txt\"}}"
assert_exit "empty file_path falls back to filePath" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "empty file_path falls back to filePath: stderr should contain BLOCKED"
fi

# ── mktemp-failure fallback (#550) ──────────────────────────────────
# The mktemp-failure fallback (`JQ_ERR=$(mktemp 2>/dev/null) || JQ_ERR=/dev/null`)
# must never assign /dev/null to a path later handed to `rm` (as root that
# unlinks the /dev/null device node), and must not silently drop jq's own
# parse-error text. PATH is curated so mktemp always fails and rm is shimmed
# to log its argv (then delegate to the real rm), so the test can observe
# both what gets unlinked and what stderr actually says. Malformed JSON on
# stdin (rather than a config file, since this hook reads JSON from stdin)
# triggers the jq parse failure.
echo "case: mktemp failure preserves jq's diagnostic and never rm's /dev/null"
MKTEMP_FAIL_BIN="${TEST_ROOT}/bin-mktemp-fail-guard"
RM_LOG_GUARD="${TEST_ROOT}/rm-log-guard.txt"
: > "${RM_LOG_GUARD}"
make_mktemp_fail_bin "${MKTEMP_FAIL_BIN}" "${RM_LOG_GUARD}" sh git cat jq realpath
run_guard_with_path "${HARDEN_REPO}" "${MKTEMP_FAIL_BIN}" '{"tool_input":'
assert_exit "mktemp failure fallback (exit code unchanged)" 2
if [[ "${GUARD_STDERR}" == *"guard-main-worktree.sh: warning: mktemp failed; jq errors are written directly to stderr below"* ]]; then
    pass
else
    fail "mktemp failure fallback: stderr should contain the mktemp-failure warning line"
fi
if [[ "${GUARD_STDERR}" == *"parse error"* ]]; then
    pass
else
    fail "mktemp failure fallback: stderr should contain jq's own parse-error diagnostic, not an empty detail"
fi
RM_LOG_GUARD_CONTENT="$(cat "${RM_LOG_GUARD}" 2>/dev/null)"
if [[ "${RM_LOG_GUARD_CONTENT}" != *"/dev/null"* ]]; then
    pass
else
    fail "mktemp failure fallback: rm must never be invoked with /dev/null (log: ${RM_LOG_GUARD_CONTENT})"
fi

# ── TMPDIR widening (#749) ──────────────────────────────────────────
# guard-main-worktree.sh's TMPDIR_ALLOW block additionally admits paths under
# a canonicalized $TMPDIR, so the repo-wide `${TMPDIR:-/tmp}/cenci/` body-file
# convention (shell-rules) stops being blocked. TMPDIR_REPO is a dedicated
# repo (not reused from earlier cases) so these cases are order-independent.
# CUSTOM_TMP is a *sibling* of TMPDIR_REPO under TEST_ROOT (never nested
# inside it), so it is not an ancestor of TMPDIR_REPO -- proving the
# ancestor-of-$ROOT guard does not gut this legitimate use.
TMPDIR_REPO="${TEST_ROOT}/tmpdir-widening"
make_git_repo "${TMPDIR_REPO}"
mkdir -p "${TMPDIR_REPO}/.cenci"
touch "${TMPDIR_REPO}/.cenci/config.json"
CUSTOM_TMP="${TEST_ROOT}/custom-tmp"
mkdir -p "${CUSTOM_TMP}"

echo "case: TMPDIR widening allows the \${TMPDIR}/cenci/ body-file convention"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/design-comment-749.md\"}}"
assert_exit "TMPDIR widening: cenci/ body file" 0

echo "case: TMPDIR widening covers every skill's \${TMPDIR}/... convention, not just /cenci/"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/claude/cenci-749-diff.patch\"}}"
assert_exit "TMPDIR widening: claude/ convention coverage" 0

echo "case: TMPDIR widening canonicalizes a symlinked TMPDIR before matching"
SYMLINK_TMP="${TEST_ROOT}/custom-tmp-link"
ln -s "${CUSTOM_TMP}" "${SYMLINK_TMP}"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${SYMLINK_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/x.md\"}}"
assert_exit "TMPDIR widening: symlinked TMPDIR resolves to the real path" 0

echo "case: TMPDIR widening does not gut the guard for the repo it protects"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR widening: repo source write still blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "TMPDIR widening: repo source write should contain BLOCKED"
fi

# Negative controls: each of these must still block the main-worktree source
# write. The equals/ancestor pair are the discriminating ones -- without the
# ancestor-of-$ROOT check they would flip to exit 0.
echo "case: TMPDIR unset skips the widening entirely (source write blocked)"
run_guard "${TMPDIR_REPO}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR unset: source write blocked" 2

echo "case: TMPDIR unset skips the widening even for a would-be-allowed custom-tmp path"
run_guard "${TMPDIR_REPO}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/design-comment-749.md\"}}"
assert_exit "TMPDIR unset: custom-tmp path still blocked (proves unset genuinely skips widening)" 2

echo "case: relative TMPDIR skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "relative-tmp" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "relative TMPDIR: source write blocked" 2

echo "case: TMPDIR equal to \$ROOT skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${TMPDIR_REPO}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR == ROOT: source write blocked" 2

echo "case: TMPDIR an ancestor of \$ROOT skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${TEST_ROOT}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR ancestor of ROOT: source write blocked" 2

# TMPDIR nested INSIDE $ROOT (a descendant, e.g. $ROOT/tmp) is the reverse of
# the ancestor case above: without a symmetric rejection, TMPDIR_ALLOW would
# be set to $ROOT/tmp and the guard would allowlist every write under that
# main-worktree subtree -- defeating the guard's purpose. A write under
# $ROOT/tmp itself (not just some other $ROOT/src path) is the discriminating
# assertion here: it is the exact path the containment bug would wrongly let
# through.
echo "case: TMPDIR a descendant of \$ROOT (inside the repo) skips the widening"
INSIDE_TMP="${TMPDIR_REPO}/tmp"
mkdir -p "${INSIDE_TMP}"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${INSIDE_TMP}" "{\"tool_input\":{\"file_path\":\"${INSIDE_TMP}/x\"}}"
assert_exit "TMPDIR descendant of ROOT: write under \$ROOT/tmp blocked" 2

echo "case: TMPDIR pointing at a non-existent directory skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${TEST_ROOT}/no-such-dir" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR non-existent dir: source write blocked" 2

# ── Deferred item 4 cross-file pin (#749) ────────────────────────────
# design's two Write targets: <designPath>/DESIGN.md (main-worktree design
# artifact, allowlisted via the existing */DESIGN.md arm) and
# ${TMPDIR:-/tmp}/cenci/design-comment-<number>.md (the body-file convention,
# allowlisted by the TMPDIR widening above). Pinning both here means a
# future change to either target's shape fails in this suite rather than
# silently prompting at runtime.
echo "case: cross-file pin -- design's designs/DESIGN.md Write target"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/designs/DESIGN.md\"}}"
assert_exit "cross-file pin: designs/DESIGN.md" 0

echo "case: cross-file pin -- design's \${TMPDIR}/cenci/design-comment-<n>.md Write target"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/design-comment-749.md\"}}"
assert_exit "cross-file pin: cenci/design-comment-749.md" 0

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
