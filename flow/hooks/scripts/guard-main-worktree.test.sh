#!/usr/bin/env bash
# Tests for guard-main-worktree.sh. Follows the repo's shell-test precedent
# (flow/hooks/scripts/check-sensitive-files.test.sh, sandbox/tests/*.test.sh):
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

# ── Bash mode (#795): tool_input.command redirection/tee targets ────
# guard-main-worktree.sh must also inspect Bash tool_input.command for
# `>`/`>>`/`>|`/tee write targets, extracted via the shared
# flow/hooks/scripts/lib/bash-write-targets.sh lib, behind the same
# .cenci/config.json / .claude/config.json gate. The Bash arm is
# deliberately MORE permissive than the Write|Edit arm: an out-of-repo-root
# target (e.g. $HOME/.claude/settings.json) is allowed (Q1a), unlike the
# Write|Edit arm.
BASH_REPO="${TEST_ROOT}/bash-mode"
make_git_repo "${BASH_REPO}"
mkdir -p "${BASH_REPO}/.cenci"
touch "${BASH_REPO}/.cenci/config.json"

echo "case: Bash command with >> to a main-worktree source file is blocked"
JSON=$(jq -n --arg cmd "echo x >> ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash >> main-worktree source blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash >> main-worktree source blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command redirecting into .worktrees/ is allowed"
JSON=$(jq -n --arg cmd "echo x > ${BASH_REPO}/.worktrees/1-x/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash > .worktrees/ allowed" 0

echo "case: Bash command redirecting to \${TMPDIR:-/tmp}/cenci/... is allowed under a custom TMPDIR"
CUSTOM_TMP_BASH="${TEST_ROOT}/bash-custom-tmp"
mkdir -p "${CUSTOM_TMP_BASH}"
JSON=$(jq -n --arg cmd 'echo x > "${TMPDIR:-/tmp}/cenci/x.md"' '{tool_input:{command:$cmd}}')
run_guard_with_tmpdir "${BASH_REPO}" "${CUSTOM_TMP_BASH}" "${JSON}"
assert_exit "bash custom TMPDIR cenci/ allowed" 0

echo "case: Bash command redirecting stderr to /dev/null is allowed (exempt device)"
JSON=$(jq -n --arg cmd "cmd 2>/dev/null" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash 2>/dev/null allowed" 0

echo "case: Bash command redirecting through an unresolved \$OUT variable is blocked"
JSON=$(jq -n --arg cmd 'cmd > "$OUT"' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash > \"\$OUT\" blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash > \"\$OUT\" blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command redirecting through \$(mktemp) is blocked"
JSON=$(jq -n --arg cmd 'cmd > $(mktemp)' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash > \$(mktemp) blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash > \$(mktemp) blocked: stderr should contain BLOCKED"
fi

# Pins Q1a: the Bash arm's repo-root scope pre-check allows a target that
# resolves outside RESOLVED_ROOT, even though it is a redirect into a
# genuinely sensitive file -- this is the resolution that lets
# /cenci:configure's `jq ... ~/.claude/settings.json > ~/.claude/settings.json.tmp`
# pattern through. Deliberately more permissive than the Write|Edit arm.
echo "case: Bash command redirecting to \$HOME/.claude/settings.json (out-of-root) is allowed (Q1a)"
JSON=$(jq -n --arg cmd 'echo x > "$HOME/.claude/settings.json"' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash \$HOME/.claude/settings.json out-of-root allowed (Q1a)" 0

# The #749 descendant-of-$ROOT rejection must still apply in Bash mode: a
# TMPDIR pointed inside the repo must not allowlist writes under it.
echo "case: Bash command with TMPDIR=\${BASH_REPO}/tmp (descendant of ROOT) is still blocked"
mkdir -p "${BASH_REPO}/tmp"
JSON=$(jq -n --arg cmd "echo x > ${BASH_REPO}/tmp/x" '{tool_input:{command:$cmd}}')
run_guard_with_tmpdir "${BASH_REPO}" "${BASH_REPO}/tmp" "${JSON}"
assert_exit "bash TMPDIR descendant of ROOT still blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash TMPDIR descendant of ROOT still blocked: stderr should contain BLOCKED"
fi

echo "case: unconfigured repo is a no-op even for an in-root Bash redirect"
BASH_UNCONFIGURED_REPO="${TEST_ROOT}/bash-unconfigured"
make_git_repo "${BASH_UNCONFIGURED_REPO}"
JSON=$(jq -n --arg cmd "echo x > ${BASH_UNCONFIGURED_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_UNCONFIGURED_REPO}" "${JSON}"
assert_exit "bash unconfigured repo no-op" 0

echo "case: Bash command with no redirect and no tee is allowed"
JSON=$(jq -n --arg cmd "git status" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash no-redirect allowed" 0

# ── Fix F (silent-failure-hunter, #795 round 2): end-to-end integration
# coverage for the awk-missing fail-closed path. bash-write-targets.test.sh
# unit-tests bwt_extract_targets directly with awk removed from PATH, but
# nothing previously proved the caller-side block (exit 2, BLOCKED message)
# actually fires through this hook's real JSON-on-stdin entry point.
echo "case: Bash command inspection fails closed when awk is unavailable (Fix F integration)"
AWK_MISSING_BIN="${TEST_ROOT}/bin-no-awk"
make_curated_bin "${AWK_MISSING_BIN}" sh git cat jq mktemp rm realpath
JSON=$(jq -n --arg cmd "echo x >> ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard_with_path "${BASH_REPO}" "${AWK_MISSING_BIN}" "${JSON}"
assert_exit "bash command inspection blocked when awk missing" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash command inspection blocked when awk missing: stderr should contain BLOCKED"
fi

# ── Should-fix (#795 round 3): end-to-end integration coverage for the
# command-too-long fail-closed path. bash-write-targets.test.sh unit-tests
# bwt_extract_targets's length pre-check directly, but nothing previously
# proved the caller-side block (exit 2, the specific "too long to inspect"
# BLOCKED message) actually fires through this hook's real JSON-on-stdin
# entry point. Mirrors the awk-missing integration case above.
echo "case: Bash command inspection fails closed when the command is too long to inspect (length pre-check integration)"
LONG_BASH_CMD="echo $(printf '%*s' 100005 '' | tr ' ' 'a') >> ${BASH_REPO}/src/too-long.c"
JSON=$(jq -n --arg cmd "${LONG_BASH_CMD}" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash command inspection blocked when command too long" 2
if [[ "${GUARD_STDERR}" == *"too long to inspect"* ]]; then
    pass
else
    fail "bash command inspection blocked when command too long: stderr should contain the 'too long to inspect' message, got: ${GUARD_STDERR}"
fi

# ── Should-fix (#795 round 4): end-to-end integration coverage for the
# wc-missing fail-closed path. bash-write-targets.test.sh unit-tests
# bwt_extract_targets's length pre-check's wc dependency directly, but
# nothing previously proved the caller-side block (exit 2, the specific "wc
# was not found on PATH" BLOCKED message) actually fires through this hook's
# real JSON-on-stdin entry point. Mirrors the awk-missing integration case
# above; PATH is curated with awk present but wc absent, isolating this
# specific dependency.
echo "case: Bash command inspection fails closed when wc is unavailable (wc-missing integration)"
WC_MISSING_BIN="${TEST_ROOT}/bin-no-wc"
make_curated_bin "${WC_MISSING_BIN}" sh git cat jq mktemp rm realpath awk
JSON=$(jq -n --arg cmd "echo x >> ${BASH_REPO}/src/no-wc.c" '{tool_input:{command:$cmd}}')
run_guard_with_path "${BASH_REPO}" "${WC_MISSING_BIN}" "${JSON}"
assert_exit "bash command inspection blocked when wc missing" 2
if [[ "${GUARD_STDERR}" == *"wc was not found on PATH"* ]]; then
    pass
else
    fail "bash command inspection blocked when wc missing: stderr should contain the 'wc was not found on PATH' message, got: ${GUARD_STDERR}"
fi

# ── #795 final round: empty-parse backstop + tilde expansion ─────────
# Zero extracted targets from a command that still looks like it might
# write (any `>`, or a delimited tee token) triggers a raw-text scan for
# the repo root; a root mention blocks, no mention allows. Mentions of the
# always-allowlisted subtrees (.worktrees/, .plans/, .claude/plans) are
# neutralized first so the pipeline's own `git -C <root>/.worktrees/<id>
# commit -m "a -> b"` shape never trips the backstop.

# run_guard_with_home <cwd> <home_override> <json> — like run_guard, but
# sets HOME for a single invocation. Used by the tilde-expansion cases so
# the out-of-root verdict is deterministic regardless of the ambient HOME.
run_guard_with_home() {
    local cwd="$1" home_override="$2" json="$3"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | HOME="${home_override}" sh "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

echo "case: brace-expansion tee construct mentioning the root is blocked (empty-parse backstop)"
JSON=$(jq -n --arg cmd "{tee,cat} ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "backstop brace tee in-root blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "backstop brace tee in-root blocked: stderr should contain BLOCKED"
fi

# {tee,cat} /var/tmp/unrelated-elsewhere.txt brace-expands (in COMMAND
# position, which the exit-6 unsupported-construct detection surface does
# not model -- that surface is scoped to write-TARGET position only; #808
# owns the command-position per-verb decision) to the literal words `tee cat
# /var/tmp/unrelated-elsewhere.txt` -- a bash invocation of `tee` with
# operand "cat" (a RELATIVE, in-root write target -- a file literally named
# "cat"), not merely an out-of-root absolute write as the old rationale
# assumed. The tokenizer still sees a zero-target parse here (its one
# "word" is the whole "{tee,cat}" string, whose basename never equals
# "tee"), so under the new design (#810 Requirement A) this now blocks via
# the unconditional delimited-tee zero-parse branch ("a delimited tee token
# in the raw text blocks unconditionally; its target may be relative to the
# main worktree") -- not the old "no root mention -> allow" backstop path
# this case originally exercised (flipped expectation, was exit 0).
echo "case: {tee,cat} /var/tmp/unrelated-elsewhere.txt is a relative in-root write (of a file named \"cat\"), not an out-of-root target, and is blocked (#810, flipped from exit 0)"
JSON=$(jq -n --arg cmd "{tee,cat} /var/tmp/unrelated-elsewhere.txt" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "brace-tee relative 'cat' target blocked (flipped from exit 0)" 2

echo "case: quoted -> plus a feature-worktree path mention is allowed (allowlisted-subtree neutralization)"
JSON=$(jq -n --arg cmd "git -C ${BASH_REPO}/.worktrees/1-x commit -m \"a -> b\"" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "backstop worktree-path mention neutralized" 0

echo "case: quoted -> plus a .plans path mention is allowed (allowlisted-subtree neutralization)"
JSON=$(jq -n --arg cmd "ls ${BASH_REPO}/.plans \"a > b\"" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "backstop .plans-path mention neutralized" 0

echo "case: alnum-embedded tee with an absolute in-root path is allowed (delimited-tee trigger)"
JSON=$(jq -n --arg cmd "grep guarantee ${BASH_REPO}/docs/notes.md" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "embedded tee in-root grep allowed" 0

# Tilde expansion (#795 final round): a plain unquoted ~/... target expands
# via HOME (mirroring \$HOME) and lands out-of-root -> allowed (Q1a), fixing
# the over-block that broke /cenci:configure's documented
# `... > ~/.claude/settings.json.tmp` pattern. A QUOTED leading tilde is a
# literal in-root filename character in real shells and must stay blocked
# (the tokenizer neutralizes it to ./~ so it is never expanded).
echo "case: unquoted ~ redirect expands via HOME and is allowed out-of-root (Q1a tilde expansion)"
TILDE_HOME="${TEST_ROOT}/tilde-home"
mkdir -p "${TILDE_HOME}"
JSON=$(jq -n --arg cmd 'echo x > ~/.claude/settings.json.tmp' '{tool_input:{command:$cmd}}')
run_guard_with_home "${BASH_REPO}" "${TILDE_HOME}" "${JSON}"
assert_exit "unquoted tilde out-of-root allowed" 0

# Under the new design (#810), this quoted-tilde case still discriminates
# tilde NON-expansion (a genuinely-expanded ~ would be absolute and
# out-of-root -> allowed, Q1a) -- but it now blocks via the new
# non-allowlisted relative-target policy (the neutralized "./~/notes.txt"
# target lexically collapses to the relative form "~/notes.txt", which
# matches no allowlist arm) rather than the old cwd-trusting canonicalize
# + repo-root-scope resolution logic.
echo "case: quoted ~ redirect is a literal in-root path and stays blocked (no tilde expansion for quoted tilde)"
JSON=$(jq -n --arg cmd 'echo x > "~/notes.txt"' '{tool_input:{command:$cmd}}')
run_guard_with_home "${BASH_REPO}" "${TILDE_HOME}" "${JSON}"
assert_exit "quoted tilde literal in-root blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "quoted tilde literal in-root blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810: stabilize Bash write guards for shell expansion, cwd
# changes, and comparison contexts (closes three #795 regressions) ─────────

# AC 2 / the ticket's fourth pinned case: {tee,cat} AGENTS.md brace-expands
# its command word to `tee cat AGENTS.md` -- a RELATIVE target with no
# root-string anywhere in the raw command text for the OLD backstop to
# find. Requirement A: "absence of a literal repo-root string is not
# sufficient evidence a write is out-of-root" -- a zero-parse command
# containing a delimited tee token must block unconditionally instead.
echo "case: {tee,cat} AGENTS.md (relative brace-expansion tee target) blocks from the configured main worktree (#810 AC 2)"
JSON=$(jq -n --arg cmd '{tee,cat} AGENTS.md' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "brace-tee relative AGENTS.md target blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "brace-tee relative AGENTS.md target blocked: stderr should contain BLOCKED"
fi

# AC 3 / Requirement B: a relative write target must not be resolved solely
# against the hook process's cwd -- the command text itself can `cd` before
# the write executes. Repro: the hook cwd is a FEATURE worktree (created via
# a real `git worktree add`), but the Bash command first `cd`s to the MAIN
# worktree root and then writes a relative target. Today's guard resolves
# "flow/should-not-write" against the feature-worktree cwd (whose own path
# already contains "/.worktrees/") and wrongly allowlists it.
WORKTREE_REPRO_REPO="${TEST_ROOT}/worktree-cwd-repro"
make_git_repo "${WORKTREE_REPRO_REPO}"
mkdir -p "${WORKTREE_REPRO_REPO}/.cenci"
touch "${WORKTREE_REPRO_REPO}/.cenci/config.json"
git -C "${WORKTREE_REPRO_REPO}" config user.email "test@example.com"
git -C "${WORKTREE_REPRO_REPO}" config user.name "Test"
git -C "${WORKTREE_REPRO_REPO}" add -A
git -C "${WORKTREE_REPRO_REPO}" commit -q -m "init"
FEATURE_WORKTREE_REPRO="${WORKTREE_REPRO_REPO}/.worktrees/1-x"
git -C "${WORKTREE_REPRO_REPO}" worktree add -q -b feat-1-x "${FEATURE_WORKTREE_REPRO}" >/dev/null

echo "case: relative Bash write target after a cd to the main worktree is blocked from a feature-worktree cwd (stale-cwd regression, #810 AC 3)"
JSON=$(jq -n --arg cmd "cd ${WORKTREE_REPRO_REPO} && printf x > flow/should-not-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "relative target after cd to main worktree blocked" 2
if [[ "${GUARD_STDERR,,}" == *"cwd"* ]]; then
    pass
else
    fail "relative target after cd to main worktree blocked: stderr should name the cwd uncertainty, got: ${GUARD_STDERR}"
fi

echo "case: control -- same command shape with an absolute .worktrees/-scoped target is allowed"
JSON=$(jq -n --arg cmd "cd ${WORKTREE_REPRO_REPO} && printf x > ${FEATURE_WORKTREE_REPRO}/flow/should-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "control: absolute .worktrees-scoped target allowed" 0

# ── Ticket #810 stabilization review (second cycle), availability fix:
# the relative-target policy's per-target loop had no equivalent to the
# empty-parse backstop's narrowing 1 (RESOLVED_ROOT itself a feature
# worktree -- nothing to protect), so an ordinary, non-adversarial command
# with NO cd (e.g. `pytest 2> errors.log`) run with cwd already inside a
# feature worktree was wrongly blocked, since its relative target doesn't
# match any .plans/.worktrees/etc. allowlist shape. Reuses
# FEATURE_WORKTREE_REPRO (a real feature worktree created via `git worktree
# add` above) so RESOLVED_ROOT is genuinely feature-worktree-shaped.
echo "case: an ordinary relative-write command with no cd is allowed from a feature-worktree cwd (availability fix)"
JSON=$(jq -n --arg cmd 'pytest 2> errors.log' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "plain relative write, no cd, feature-worktree cwd allowed" 0

echo "case: another ordinary relative-write shape (git diff > diff.txt) with no cd is allowed from a feature-worktree cwd (availability fix)"
JSON=$(jq -n --arg cmd 'git diff > diff.txt' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "git diff > diff.txt, no cd, feature-worktree cwd allowed" 0

# Critical non-regression: the availability fix must NOT weaken the
# regression-2 case above -- re-run it explicitly here, right alongside the
# new availability cases, since both scenarios share the IDENTICAL
# feature-worktree-shaped RESOLVED_ROOT and are only distinguished by the
# presence of a `cd` in the raw command text.
echo "case: non-regression -- relative target after a cd to the main worktree is STILL blocked from a feature-worktree cwd (availability fix must not weaken this)"
JSON=$(jq -n --arg cmd "cd ${WORKTREE_REPRO_REPO} && printf x > flow/should-not-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "availability fix non-regression: relative target after cd to main worktree still blocked" 2
if [[ "${GUARD_STDERR,,}" == *"cwd"* ]]; then
    pass
else
    fail "availability fix non-regression: stderr should still name the cwd uncertainty, got: ${GUARD_STDERR}"
fi

# ── Ticket #810 round-3 stabilization review, Finding 3 [HIGH]: the
# availability narrowing above `continue`d (allowed) before checking whether
# the target actually escapes upward past the feature-worktree root, and
# bash_command_might_cd only detected a delimited "cd" token, missing
# "pushd" -- a different directory-changing builtin that reproduces
# regression-2 through a different command form.

# Finding 3A: with NO cd/pushd anywhere in the command, a "../.."-relative
# target still genuinely resolves (via ordinary shell cwd-relative
# semantics, no cd required) to somewhere above the feature-worktree cwd it
# started from -- here, the main worktree's own AGENTS.md. This must block,
# not be waved through by the availability narrowing.
echo "case: relative Bash write target traversing upward with .. is blocked even with no cd (Finding 3A regression)"
JSON=$(jq -n --arg cmd 'printf x > ../../AGENTS.md' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "relative .. traversal with no cd blocked (Finding 3A)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "relative .. traversal with no cd blocked: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi

# Finding 3A non-regression: an ordinary relative write that does NOT
# traverse upward (the availability fix's own target case) must still be
# allowed -- re-confirms the case above (lines 836-839) is not weakened by
# the added escape check.
echo "case: non-regression -- an ordinary non-traversing relative write with no cd is still allowed from a feature-worktree cwd (Finding 3A must not weaken the availability fix)"
JSON=$(jq -n --arg cmd 'pytest 2> errors.log' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "non-traversing relative write, no cd, feature-worktree cwd still allowed (Finding 3A non-regression)" 0

# Finding 3B: bash_command_might_cd previously only matched a delimited "cd"
# token; "pushd" (which contains no "cd" substring) evaded it entirely, so
# `pushd <main-root> && printf x > flow/should-not-write` -- a real
# directory-change command -- passed as "no cd detected" and the
# availability narrowing wrongly allowed the main-worktree write.
echo "case: relative Bash write target after a pushd to the main worktree is blocked from a feature-worktree cwd (Finding 3B regression)"
JSON=$(jq -n --arg cmd "pushd ${WORKTREE_REPRO_REPO} && printf x > flow/should-not-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "relative target after pushd to main worktree blocked (Finding 3B)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "relative target after pushd to main worktree blocked: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi

# Pins the pipeline's own documented plan-persist step
# (flow/skills/implement/phases/phase-1-plan.md): a relative,
# allowlist-shaped `.plans/`-rooted target must keep working under the new
# cwd-independent relative-target policy (lexical collapse + allowlist
# match, never real cwd resolution).
echo "case: relative .plans/-shaped Bash write target is allowed (pins phase-1-plan.md's documented plan-persist step, #810)"
JSON=$(jq -n --arg cmd 'cat /tmp/x >> .plans/1-foo.md' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "relative .plans/ write target allowed" 0

# AC 4 / Requirement C: `>` inside a CLOSED [[ ... ]] / (( ... )) comparison
# context is not a redirect at all -- must never be classified as a write
# target.
echo "case: [[ z > a ]] and (( z > a )) are comparison contexts, not redirects, and are allowed (#810 AC 4)"
JSON=$(jq -n --arg cmd '[[ z > a ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash [[ z > a ]] allowed" 0

JSON=$(jq -n --arg cmd '(( z > a ))' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash (( z > a )) allowed" 0

# AC 5: a real redirect immediately after a [[ ... ]] region must still be
# extracted and checked.
echo "case: a real redirect immediately after [[ ... ]] is still extracted and blocked (#810 AC 5)"
JSON=$(jq -n --arg cmd "[[ -f x ]] > ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash [[ -f x ]] > src/foo.c (adjacent real redirect) blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash [[ -f x ]] > src/foo.c (adjacent real redirect) blocked: stderr should contain BLOCKED"
fi

# AC 1: unquoted brace expansion in write-target position must fail closed
# (new bwt_extract_targets exit code 6), never emit a bogus literal target
# -- and the guard must name the unsupported construct.
echo "case: tee <root>/src/{a,b}.c (unquoted brace expansion in tee operand position) is blocked, naming the unsupported construct (#810 AC 1)"
JSON=$(jq -n --arg cmd "tee ${BASH_REPO}/src/{a,b}.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "brace expansion tee target blocked" 2
if [[ "${GUARD_STDERR,,}" == *"brace"* ]]; then
    pass
else
    fail "brace expansion tee target blocked: stderr should name the unsupported brace-expansion construct, got: ${GUARD_STDERR}"
fi

# ── Ticket #810 stabilization review, Bug 1 [CRITICAL] (e2e): a sticky
# `curregion` flag left over from a closed (( ... )) region disabled
# tee-ARMING for the very next word, silently permitting a real write.
# Confirmed against real bash: `(( 1 )) ; tee AGENTS.md > /dev/null`
# genuinely writes to AGENTS.md.
echo "case: Bash command with a closed (( )) region before tee still blocks the tee write (Bug 1 e2e regression)"
JSON=$(jq -n --arg cmd '(( 1 )) ; tee AGENTS.md > /dev/null' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "(( 1 )) ; tee AGENTS.md > /dev/null blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "(( 1 )) ; tee AGENTS.md > /dev/null blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 stabilization review, Bug 2 [HIGH] (e2e): mark_regions() had
# no token-boundary check, so a "[[" glued mid-word (never the reserved
# conditional token in real bash) could still suppress a real, adjacent
# redirect. Confirmed against real bash: `echo x[[ z > AGENTS.md ]] b`
# genuinely writes to AGENTS.md.
echo "case: Bash command with a glued [[ (not its own token) still blocks the real redirect (Bug 2 e2e regression)"
JSON=$(jq -n --arg cmd 'echo x[[ z > AGENTS.md ]] b' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo x[[ z > AGENTS.md ]] b blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo x[[ z > AGENTS.md ]] b blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 stabilization review (second cycle), Bug 3 [CRITICAL] (e2e):
# is_opener_boundary/find_close accepted spans bash does not actually treat
# as "[[ ]]"/"(( ))", allowing three fail-open bypass vectors.

# Vector 1: process substitution glued to (( is not a real (( opener.
# Confirmed against real bash: `echo x >(( : > /repo/.env ))` genuinely
# writes to /repo/.env (the subshell `( : > /repo/.env )`).
echo "case: Bash command with process substitution glued to (( still blocks the real write (Bug 3 Vector 1 e2e regression)"
JSON=$(jq -n --arg cmd "echo x >(( : > ${BASH_REPO}/src/bug3-procsub.env ))" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo x >(( : > src/bug3-procsub.env )) blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo x >(( : > src/bug3-procsub.env )) blocked: stderr should contain BLOCKED"
fi

# Vector 2: "[[" preceded by a plain word (not command position) is not a
# real conditional opener. Confirmed against real bash:
# `echo x [[ z > AGENTS.md ]] b` genuinely writes to AGENTS.md.
echo "case: Bash command with [[ preceded by a plain word (not command position) still blocks the real redirect (Bug 3 Vector 2 e2e regression)"
JSON=$(jq -n --arg cmd "echo x [[ z > ${BASH_REPO}/src/bug3-notcmdpos.md ]] b" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo x [[ z > src/bug3-notcmdpos.md ]] b blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo x [[ z > src/bug3-notcmdpos.md ]] b blocked: stderr should contain BLOCKED"
fi

# Vector 3: bash terminates "[[ ... ]]" at the FIRST standalone "]]", not
# the last one in the text. Confirmed against real bash:
# `[[ [[ ]] ; echo x > AGENTS.md ]]` genuinely writes to AGENTS.md.
echo "case: Bash command where the first standalone ]] closes [[ still blocks the real redirect in between (Bug 3 Vector 3 e2e regression)"
JSON=$(jq -n --arg cmd "[[ [[ ]] ; echo x > ${BASH_REPO}/src/bug3-firstclose.md ]]" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "[[ [[ ]] ; echo x > src/bug3-firstclose.md ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "[[ [[ ]] ; echo x > src/bug3-firstclose.md ]] blocked: stderr should contain BLOCKED"
fi

# Non-regression: a real redirect after a genuine [[ ... ]] region (with a
# glued POSIX character class inside it) is still extracted and blocked.
echo "case: [[ \$x =~ [[:alpha:]] ]] followed by a real redirect still blocks (Bug 3 Vector 3 e2e non-regression)"
JSON=$(jq -n --arg cmd "[[ \$x =~ [[:alpha:]] ]] > ${BASH_REPO}/src/bug3-posixclass.out" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "[[ \$x =~ [[:alpha:]] ]] > src/bug3-posixclass.out blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "[[ \$x =~ [[:alpha:]] ]] > src/bug3-posixclass.out blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 round-4 security review [CRITICAL] (e2e): is_opener_boundary()
# previously accepted the &/| TAIL of the compound redirect operators >&/<&/
# >| as if it were a bare separator/pipe, wrongly opening a suppressed [[ ]]
# region and hiding a real redirect inside it. Confirmed against real bash:
# `echo hi >| [[ x > AGENTS.md ]]` truncate-redirects to a file literally
# named "[[", and the `[[ x > AGENTS.md ]]` that follows is NOT the reserved
# conditional token in that (redirect-target) position -- `> AGENTS.md`
# genuinely writes to AGENTS.md.
echo "case: Bash command with >| immediately before [[ still blocks the real redirect inside (round-4 e2e regression)"
JSON=$(jq -n --arg cmd 'echo hi >| [[ x > AGENTS.md ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo hi >| [[ x > AGENTS.md ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo hi >| [[ x > AGENTS.md ]] blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command with >& immediately before [[ still blocks the real redirect inside (round-4 e2e regression)"
JSON=$(jq -n --arg cmd 'echo hi >& [[ x > AGENTS.md ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo hi >& [[ x > AGENTS.md ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo hi >& [[ x > AGENTS.md ]] blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 round-5 security review [CRITICAL] (e2e): a real write
# hidden inside command/process substitution nested in [[ ]] / (( )) was
# previously swallowed by mark_regions()'s region suppression -- the benign
# earlier target made the parse non-empty, so the zero-parse backstop never
# ran, and the real write inside the suppressed [[ ]] span was never
# extracted. Confirmed against real bash: both commands below genuinely
# write to AGENTS.md in the configured main worktree.
echo "case: Bash command with a real write hidden inside \$( ) nested in [[ ]] still blocks (round-5 e2e regression)"
JSON=$(jq -n --arg cmd 'echo ok > /tmp/ok ; [[ $(printf x > AGENTS.md) ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo ok > /tmp/ok ; [[ \$(printf x > AGENTS.md) ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo ok > /tmp/ok ; [[ \$(printf x > AGENTS.md) ]] blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command with a tee operand hidden inside \$( ) nested in [[ ]] still blocks (round-5 e2e regression)"
JSON=$(jq -n --arg cmd 'echo ok > /tmp/ok ; [[ $(echo p | tee AGENTS.md) ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo ok > /tmp/ok ; [[ \$(echo p | tee AGENTS.md) ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo ok > /tmp/ok ; [[ \$(echo p | tee AGENTS.md) ]] blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 round-6 stabilization [HIGH] (e2e): bash 5.3+'s function
# substitution forms `${ cmd; }` / `${| cmd; }` execute a real command from
# inside a [[ ]]/(( )) construct just like `$(...)`, but were not recognized
# as a substitution marker -- the region was suppressed as an ordinary
# comparison, so the real write nested inside was never extracted and never
# blocked. Confirmed against real bash: this command genuinely writes to
# AGENTS.md in the configured main worktree.
echo "case: Bash command with a real write hidden inside \${ cmd; } funsub nested in [[ ]] still blocks (round-6 e2e regression)"
JSON=$(jq -n --arg cmd 'echo ok > /tmp/ok ; [[ -n ${ printf x > AGENTS.md; } ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo ok > /tmp/ok ; [[ -n \${ printf x > AGENTS.md; } ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo ok > /tmp/ok ; [[ -n \${ printf x > AGENTS.md; } ]] blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 stabilization (e2e): a command/process/function substitution
# marker found INSIDE a double-quoted span within a [[ ]]/(( )) region's
# interior is a deliberately blunt, maximally conservative unsupported
# construct: this tokenizer has no safe way to resume precise parsing through
# such a marker's real syntax once wrapped in an outer double-quoted string,
# so the whole command fails closed unconditionally (bwt_extract_targets
# exit 7) rather than attempting to precisely resolve what write is inside
# it. Confirmed against real bash: this command genuinely writes to
# AGENTS.md in the configured main worktree, but this guard now blocks it
# purely because the construct is unsupported for extraction -- not because
# it precisely resolved AGENTS.md as the target.
echo "case: Bash command with a real write hidden inside a double-quoted \$( ) command substitution nested in [[ ]] fails closed (exit 7 e2e)"
JSON=$(jq -n --arg cmd '[[ -n "$(printf x > AGENTS.md)" ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"cannot be safely resolved"* ]]; then
    pass
else
    fail "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked: stderr should name the unsupported double-quoted-nested-substitution construct, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *'[[ -n "$(printf x > AGENTS.md)" ]]'* ]]; then
    pass
else
    fail "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked: stderr should name the offending command, got: ${GUARD_STDERR}"
fi

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
