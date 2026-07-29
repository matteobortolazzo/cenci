#!/usr/bin/env bash
# Tests for check-sensitive-files.sh. Mirrors the harness style of
# guard-main-worktree.test.sh: plain bash, no framework, PASS/FAIL counters,
# non-zero exit on failure. Each case drives the hook script with
# PreToolUse-style JSON on stdin. Unlike guard-main-worktree.sh, this hook
# does not gate on cenci configuration or consult git state — it runs
# unconditionally in every repo. The Write|Edit arm only reads
# tool_input.file_path (or filePath) from stdin, so no per-case cwd/repo
# setup is needed there; the Bash arm (#795) resolves a relative redirect
# target against the hook process's cwd, so those cases use
# run_check_in_dir to set an explicit cwd.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check-sensitive-files.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# run_check <json> — runs the hook with <json> on stdin.
# Sets CHECK_EXIT and CHECK_STDERR.
run_check() {
    local json="$1"
    CHECK_STDERR="$(echo "${json}" | sh "${CHECK_SH}" 2>&1 >/dev/null)"
    CHECK_EXIT=$?
}

# run_check_in_dir <cwd> <json> — like run_check, but runs from <cwd>. Used
# for the Bash-mode relative-target case (#795), where a relative redirect
# target must be resolved against the hook process's cwd.
run_check_in_dir() {
    local cwd="$1" json="$2"
    CHECK_STDERR="$(cd "${cwd}" && echo "${json}" | sh "${CHECK_SH}" 2>&1 >/dev/null)"
    CHECK_EXIT=$?
}

# run_check_with_path <path_override> <json> — like run_check, but overrides
# PATH with a curated bin dir before invoking the hook. Used to simulate a
# PATH missing specific external tools (fail-closed tests).
run_check_with_path() {
    local path_override="$1" json="$2"
    CHECK_STDERR="$(echo "${json}" | PATH="${path_override}" sh "${CHECK_SH}" 2>&1 >/dev/null)"
    CHECK_EXIT=$?
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${CHECK_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${CHECK_EXIT}, expected ${expected}"
    fi
}

assert_blocked_stderr() {
    local label="$1"
    if [[ "${CHECK_STDERR}" == *BLOCKED* ]]; then
        pass
    else
        fail "${label}: stderr should contain BLOCKED"
    fi
}

# make_curated_bin <dir> <tool>... — creates <dir> containing symlinks to the
# real binaries for each named tool (resolved from the current PATH) and
# nothing else. Used with run_check_with_path to simulate a PATH that is
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

echo "check-sensitive-files.test.sh"

TEST_ROOT="$(mktemp -d /var/tmp/check-sensitive-files-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# ── Regression: env files (category 1 of today's blocklist) ─────────
echo "case: .env-family paths are blocked (regression)"
for p in "${TEST_ROOT}/.env" "${TEST_ROOT}/config/.env" "${TEST_ROOT}/app.env.local"; do
    run_check "{\"tool_input\":{\"file_path\":\"${p}\"}}"
    assert_exit "env file (${p})" 2
    assert_blocked_stderr "env file (${p})"
done

# ── Regression: credentials/secrets/keys (category 2) ────────────────
echo "case: credentials/secrets/key paths are blocked (regression)"
for p in "${TEST_ROOT}/credentials.json" "${TEST_ROOT}/app-secrets.yaml" "${TEST_ROOT}/server.pem" "${TEST_ROOT}/app.key"; do
    run_check "{\"tool_input\":{\"file_path\":\"${p}\"}}"
    assert_exit "credential/key file (${p})" 2
    assert_blocked_stderr "credential/key file (${p})"
done

# ── Regression: SSH/keystore files (category 3) ──────────────────────
echo "case: SSH/keystore paths are blocked (regression)"
for p in "${TEST_ROOT}/.ssh/id_rsa" "${TEST_ROOT}/app.keystore" "${TEST_ROOT}/app.jks"; do
    run_check "{\"tool_input\":{\"file_path\":\"${p}\"}}"
    assert_exit "ssh/keystore file (${p})" 2
    assert_blocked_stderr "ssh/keystore file (${p})"
done

# ── Non-sensitive absolute path → allowed ────────────────────────────
echo "case: non-sensitive absolute path is allowed"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/src/foo.txt\"}}"
assert_exit "non-sensitive path" 0

# ── Empty/missing file_path → no-op ──────────────────────────────────
echo "case: missing file_path is a no-op"
run_check "{\"tool_input\":{}}"
assert_exit "missing file_path" 0

echo "case: empty file_path is a no-op"
run_check "{\"tool_input\":{\"file_path\":\"\"}}"
assert_exit "empty file_path" 0

# ── Symlink hardening: benign name → sensitive canonical target ─────
# notes.txt (benign raw name) symlinks to a real, existing .env file. Only
# a canonicalizing implementation resolves the symlink and blocks via the
# canonical target; today's raw-name grep/sed sees only the literal string
# "notes.txt" and allows it. The target must be a real existing file — a
# broken symlink would fall into an ancestor-walk branch instead of having
# its final component dereferenced.
echo "case: symlink with benign name resolving to a sensitive canonical target is blocked"
mkdir -p "${TEST_ROOT}/symlinks"
touch "${TEST_ROOT}/symlinks/real.env"
ln -s "${TEST_ROOT}/symlinks/real.env" "${TEST_ROOT}/symlinks/notes.txt"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/symlinks/notes.txt\"}}"
assert_exit "symlink benign-name -> sensitive target" 2
assert_blocked_stderr "symlink benign-name -> sensitive target"

# ── Symlink hardening: sensitive name → benign canonical target ─────
# .env (sensitive raw name) symlinks to a real, existing benign file. Today's
# raw-name match already blocks this via the literal ".env" name; a hardened
# canonicalizing implementation must preserve this via a union of raw-name
# and canonical-name checks (canonicalization must not replace raw matching).
echo "case: symlink with sensitive name resolving to a benign canonical target is still blocked"
touch "${TEST_ROOT}/symlinks/benign.txt"
ln -s "${TEST_ROOT}/symlinks/benign.txt" "${TEST_ROOT}/symlinks/.env"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/symlinks/.env\"}}"
assert_exit "symlink sensitive-name -> benign target" 2
assert_blocked_stderr "symlink sensitive-name -> benign target"

# ── ..-collapse hardening ────────────────────────────────────────────
# The raw path's final literal component is "..", which matches no blocklist
# pattern; only after lexically collapsing ".." does the path resolve to a
# real, existing ".env" directory. Today's script has no normalization step,
# so it allows this write; a hardened implementation must collapse ".."
# (and/or canonicalize) before matching. NOTE: a simpler "*/.env"-suffixed
# ..-substring like "/a/b/../.env" is NOT usable here — its raw string still
# ends in the literal characters ".env", so today's suffix-glob already
# matches it without any normalization (verified empirically); it would not
# exercise this requirement.
echo "case: a ..-collapsed path resolving to a sensitive filename is blocked"
mkdir -p "${TEST_ROOT}/dotdot/.env"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/dotdot/.env/sub/..\"}}"
assert_exit "dot-dot collapse to sensitive path" 2
assert_blocked_stderr "dot-dot collapse to sensitive path"

# ── jq missing from PATH must fail closed ────────────────────────────
# Today's script never calls jq (it uses grep/sed), so a curated PATH
# lacking jq still runs the raw-text extraction successfully and allows this
# non-sensitive path. A hardened implementation depends on jq for field-
# scoped extraction and must fail closed (block) when jq is unavailable
# rather than silently falling through to an allow.
echo "case: jq missing from PATH fails closed"
JQ_MISSING_BIN="${TEST_ROOT}/bin-no-jq"
make_curated_bin "${JQ_MISSING_BIN}" sh cat grep sed realpath readlink
run_check_with_path "${JQ_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/src/foo.txt\"}}"
assert_exit "jq missing fails closed" 2
assert_blocked_stderr "jq missing fails closed"

# ── Malformed JSON on stdin must fail closed ─────────────────────────
# Deliberately truncated/invalid JSON (unterminated string, no closing
# braces). Today's grep pattern requires a closing quote to match at all, so
# it simply fails to extract anything and allows the write (exit 0). A
# hardened implementation must treat a JSON parse failure as fail-closed.
echo "case: malformed JSON on stdin fails closed"
run_check '{"tool_input":{"file_path": "oops'
assert_exit "malformed JSON fails closed" 2
assert_blocked_stderr "malformed JSON fails closed"

# ── Relative file_path must fail closed ──────────────────────────────
echo "case: relative file_path fails closed"
run_check "{\"tool_input\":{\"file_path\":\"src/foo.txt\"}}"
assert_exit "relative file_path fails closed" 2
assert_blocked_stderr "relative file_path fails closed"

# ── Resolver (realpath/readlink) missing from PATH must fail closed ──
echo "case: path resolver (realpath/readlink) missing from PATH fails closed"
RESOLVER_MISSING_BIN="${TEST_ROOT}/bin-no-resolver"
make_curated_bin "${RESOLVER_MISSING_BIN}" sh cat grep sed jq
run_check_with_path "${RESOLVER_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/src/foo.txt\"}}"
assert_exit "resolver missing fails closed" 2
assert_blocked_stderr "resolver missing fails closed"

# ── file_path present-but-empty must fall back to filePath (jq `//` bug) ──
# jq's `//` alternative operator only falls back on null/false, NOT on an
# empty string. A payload with BOTH keys present, file_path set to "" and
# filePath set to a genuinely sensitive path, previously extracted "" (the
# `//` never tried filePath because "" is not null) and hit the empty-path
# early `exit 0` -- silently ALLOWING the write. The extraction now checks
# emptiness explicitly so a present-but-empty file_path correctly falls
# through to filePath.
echo "case: empty file_path falls back to filePath (jq // does not treat \"\" as fallback trigger)"
run_check "{\"tool_input\":{\"file_path\":\"\",\"filePath\":\"${TEST_ROOT}/fallback/secrets/prod.env\"}}"
assert_exit "empty file_path falls back to filePath" 2
assert_blocked_stderr "empty file_path falls back to filePath"

# ── Genuine symlink loop: unresolvable ancestor node must fail closed ──
# A two-node ELOOP cycle (a -> b, b -> a) makes BOTH `[ -e path ]` and
# `[ -d path ]` report false for any path that requires traversing the
# cycle -- the exact same signal this script uses to decide "does not exist
# yet, walk up to the nearest real ancestor." Without hardening, the
# parent-anchored ancestor walk backs off past the loop node(s) entirely and
# calls resolve_path only on the nearest REAL, non-looping ancestor,
# carrying the unresolved loop segment forward as a literal TAIL string --
# degrading to raw-name-only matching and silently ALLOWING the write. A
# hardened ancestor walk must recognize that a path component which is a
# real symlink node (`[ -L ]` true) but whose target cannot be resolved
# (`[ -e ]` false -- ELOOP or a dangling target) is itself a fail-closed
# signal, and BLOCK rather than walk past it. This is defense-in-depth (an
# actual Write/Edit through a genuine symlink loop independently fails at
# the OS level regardless of this hook's verdict), but the hook is meant to
# fail closed rather than silently degrade to a weaker check.
echo "case: a genuine symlink loop (benign literal name) is blocked (fail-closed ancestor walk)"
mkdir -p "${TEST_ROOT}/loop"
ln -s b "${TEST_ROOT}/loop/a"
ln -s a "${TEST_ROOT}/loop/b"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/loop/a\"}}"
assert_exit "symlink loop (benign literal name)" 2
assert_blocked_stderr "symlink loop (benign literal name)"

# ── Genuine symlink loop as an intermediate ancestor (not the leaf) ──
# The unresolvable loop node (loop-mid/a) is not the final path component --
# there is a not-yet-existing tail (newsub/new.txt) below it. Proves the fix
# isn't leaf-only: the ancestor walk must detect the unresolvable loop node
# even when it is an intermediate ancestor of a longer not-yet-existing
# path, not just the terminal component.
echo "case: an unresolvable symlink loop as an intermediate ancestor (not the leaf) is blocked"
mkdir -p "${TEST_ROOT}/loop-mid"
ln -s b "${TEST_ROOT}/loop-mid/a"
ln -s a "${TEST_ROOT}/loop-mid/b"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/loop-mid/a/newsub/new.txt\"}}"
assert_exit "symlink loop as intermediate ancestor" 2
assert_blocked_stderr "symlink loop as intermediate ancestor"

# ── Dangling symlink as the final path component ──
# dangling-link is a real symlink node (`[ -L ]` true) whose target does not
# exist (`[ -e ]` false). Uses a benign literal name so a raw-string
# sensitive-name match would not fire on its own -- only the fail-closed
# ancestor-walk guard should cause the block here.
echo "case: a dangling symlink as the final path component is blocked"
ln -s "${TEST_ROOT}/nonexistent-target" "${TEST_ROOT}/dangling-link"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/dangling-link\"}}"
assert_exit "dangling symlink final component" 2
assert_blocked_stderr "dangling symlink final component"

# ── Lookalike file_path substring elsewhere in the JSON must be ignored ──
# The true tool_input.file_path is genuine; a sibling field (old_string, as
# in an Edit tool call) contains a lookalike '"file_path": "..."' substring
# pointing at the opposite classification. Field-scoped extraction (jq) must
# key off the real tool_input.file_path, not any "file_path"-shaped text
# found anywhere in the raw JSON. Built via jq -n --arg (never hand-escaped)
# to guarantee valid nesting/escaping.
#
# NOTE (empirically verified): because valid JSON escapes every quote inside
# a string value as \", the decoy text embedded in old_string never forms an
# unescaped `"file_path"` substring — grep's exact quote-adjacent pattern
# does not match it. So today's raw grep/sed extraction already returns the
# correct (true-field) result for this specific construction; it is not
# fooled by an *escaped* decoy. This matches the same construction already
# present in guard-main-worktree.test.sh, and was confirmed by running this
# exact JSON through the pre-hardening guard-main-worktree.sh (git rev
# d45e6fc^), which also correctly keyed off the true field.
#
# To actually reproduce today's field-scoping gap, the decoy must be a
# second *unescaped*, syntactically real "file_path" JSON key elsewhere in
# the tree (not embedded inside an escaped string value) — grep's head -1
# then picks whichever "file_path" key occurs first in the raw text,
# regardless of nesting/schema. That sub-case is included below and does
# fail today.
echo "case: lookalike file_path substring in old_string is ignored (true field sensitive)"
LOOKALIKE_JSON=$(jq -n --arg fp "${TEST_ROOT}/lookalike/.env" --arg os '"file_path": "'"${TEST_ROOT}"'/lookalike/benign.txt"' '{tool_input:{old_string:$os,file_path:$fp}}')
run_check "${LOOKALIKE_JSON}"
assert_exit "lookalike substring ignored (true field sensitive)" 2
assert_blocked_stderr "lookalike substring ignored (true field sensitive)"

echo "case: lookalike file_path substring in old_string is ignored (true field benign)"
LOOKALIKE_JSON_BENIGN=$(jq -n --arg fp "${TEST_ROOT}/lookalike/benign2.txt" --arg os '"file_path": "'"${TEST_ROOT}"'/lookalike/.env"' '{tool_input:{old_string:$os,file_path:$fp}}')
run_check "${LOOKALIKE_JSON_BENIGN}"
assert_exit "lookalike substring ignored (true field benign)" 0

echo "case: a genuine (unescaped) second file_path key elsewhere in the tree is ignored"
NESTED_LOOKALIKE_JSON=$(jq -n --arg decoy "${TEST_ROOT}/lookalike/decoy-benign.txt" --arg fp "${TEST_ROOT}/lookalike/real-sensitive.env" '{tool_input:{decoy:{file_path:$decoy},file_path:$fp}}')
run_check "${NESTED_LOOKALIKE_JSON}"
assert_exit "nested second file_path key ignored" 2
assert_blocked_stderr "nested second file_path key ignored"

# ── mktemp-failure fallback (#550) ──────────────────────────────────
# Same requirement as guard-main-worktree.sh: the mktemp-failure fallback
# must never assign /dev/null to a path later handed to rm, and must not
# silently drop jq's own parse-error text.
echo "case: mktemp failure preserves jq's diagnostic and never rm's /dev/null"
MKTEMP_FAIL_BIN="${TEST_ROOT}/bin-mktemp-fail-check"
RM_LOG_CHECK="${TEST_ROOT}/rm-log-check.txt"
: > "${RM_LOG_CHECK}"
make_mktemp_fail_bin "${MKTEMP_FAIL_BIN}" "${RM_LOG_CHECK}" sh cat jq realpath
run_check_with_path "${MKTEMP_FAIL_BIN}" '{"tool_input":'
assert_exit "mktemp failure fallback (exit code unchanged)" 2
if [[ "${CHECK_STDERR}" == *"check-sensitive-files.sh: warning: mktemp failed; jq errors are written directly to stderr below"* ]]; then
    pass
else
    fail "mktemp failure fallback: stderr should contain the mktemp-failure warning line"
fi
if [[ "${CHECK_STDERR}" == *"parse error"* ]]; then
    pass
else
    fail "mktemp failure fallback: stderr should contain jq's own parse-error diagnostic, not an empty detail"
fi
RM_LOG_CHECK_CONTENT="$(cat "${RM_LOG_CHECK}" 2>/dev/null)"
if [[ "${RM_LOG_CHECK_CONTENT}" != *"/dev/null"* ]]; then
    pass
else
    fail "mktemp failure fallback: rm must never be invoked with /dev/null (log: ${RM_LOG_CHECK_CONTENT})"
fi

# ── Bash mode (#795): tool_input.command redirection/tee targets ────
# check-sensitive-files.sh must also inspect Bash tool_input.command for
# `>`/`>>`/`>|`/tee write targets, extracted via the shared
# flow/hooks/scripts/lib/bash-write-targets.sh lib, and run those targets
# through the same blocklist as file_path/filePath. Runs unconditionally in
# every repo (Q2), matching the Write|Edit arm's unconditional behavior.
# Normalize TMPDIR for this section so the "${TMPDIR:-/tmp}/cenci/..."
# allowed case below is deterministic regardless of the ambient environment
# (mirrors guard-main-worktree.test.sh's #749 precedent).
unset TMPDIR

BASH_TEST_ROOT="$(mktemp -d /var/tmp/check-sensitive-files-bash-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}" "${BASH_TEST_ROOT}"' EXIT

echo "case: Bash command with a sensitive > redirect target is blocked"
BASH_CMD="echo x > ${BASH_TEST_ROOT}/.env"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash > sensitive target" 2
assert_blocked_stderr "bash > sensitive target"

echo "case: Bash command with a sensitive tee -a target is blocked"
BASH_CMD="jq . ${BASH_TEST_ROOT}/in.json | tee -a ${BASH_TEST_ROOT}/deploy/credentials.json"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash tee -a sensitive target" 2
assert_blocked_stderr "bash tee -a sensitive target"

echo "case: Bash command redirecting stderr to /dev/null is allowed (exempt device)"
BASH_CMD="cmd 2>/dev/null"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash 2>/dev/null allowed" 0

echo "case: Bash command redirecting through an unresolved \$OUT variable is blocked and names the target"
BASH_CMD='cmd > "$OUT"'
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash > \"\$OUT\" blocked" 2
if [[ "${CHECK_STDERR}" == *'$OUT'* ]]; then
    pass
else
    fail "bash > \"\$OUT\" blocked: stderr should name the unresolved target (\$OUT): ${CHECK_STDERR}"
fi

echo "case: Bash command redirecting through \$(mktemp) is blocked and names the target"
BASH_CMD='cmd > $(mktemp)'
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash > \$(mktemp) blocked" 2
if [[ "${CHECK_STDERR}" == *'$(mktemp)'* ]]; then
    pass
else
    fail "bash > \$(mktemp) blocked: stderr should name the unresolved target (\$(mktemp)): ${CHECK_STDERR}"
fi

echo "case: Bash command with no redirect and no tee is allowed via early exit"
BASH_CMD="git status"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash no-redirect early exit" 0

echo "case: Bash command redirecting to \${TMPDIR:-/tmp}/cenci/... is allowed"
BASH_CMD='cmd > "${TMPDIR:-/tmp}/cenci/x.md"'
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash \${TMPDIR:-/tmp}/cenci/... allowed" 0

echo "case: Bash command with multiple redirects, only the second target sensitive, is blocked"
BASH_CMD="cmd > ${BASH_TEST_ROOT}/first.txt 2> ${BASH_TEST_ROOT}/.env"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash multi-redirect second sensitive" 2
assert_blocked_stderr "bash multi-redirect second sensitive"

echo "case: Bash command with multiple tee operands, only the last sensitive, is blocked"
BASH_CMD="foo | tee ${BASH_TEST_ROOT}/a.txt ${BASH_TEST_ROOT}/.env"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash multi-tee last sensitive" 2
assert_blocked_stderr "bash multi-tee last sensitive"

echo "case: Bash command with tee as the first token on a new line (no leading whitespace) is blocked (Fix 2 regression)"
BASH_CMD="$(printf 'cd /workspace\ntee -a %s/deploy/.env' "${BASH_TEST_ROOT}")"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash multi-line tee on new line blocked" 2
assert_blocked_stderr "bash multi-line tee on new line blocked"

echo "case: Bash command with a relative redirect target is resolved against cwd and blocked"
RELATIVE_TARGET_DIR="${BASH_TEST_ROOT}/relative-target-cwd"
mkdir -p "${RELATIVE_TARGET_DIR}"
BASH_CMD="echo x > .env"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check_in_dir "${RELATIVE_TARGET_DIR}" "${JSON}"
assert_exit "bash relative target resolved against cwd" 2
assert_blocked_stderr "bash relative target resolved against cwd"

# ── Fix F (silent-failure-hunter, #795 round 2): end-to-end integration
# coverage for the awk-missing fail-closed path. bash-write-targets.test.sh
# unit-tests bwt_extract_targets directly with awk removed from PATH, but
# nothing previously proved the caller-side block (exit 2, BLOCKED message)
# actually fires through this hook's real JSON-on-stdin entry point.
echo "case: Bash command inspection fails closed when awk is unavailable (Fix F integration)"
AWK_MISSING_BIN="${BASH_TEST_ROOT}/bin-no-awk"
make_curated_bin "${AWK_MISSING_BIN}" sh cat jq mktemp rm
BASH_CMD="echo x > ${BASH_TEST_ROOT}/no-awk-target.txt"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check_with_path "${AWK_MISSING_BIN}" "${JSON}"
assert_exit "bash command inspection blocked when awk missing" 2
assert_blocked_stderr "bash command inspection blocked when awk missing"

# ── Should-fix (#795 round 3): end-to-end integration coverage for the
# command-too-long fail-closed path. bash-write-targets.test.sh unit-tests
# bwt_extract_targets's length pre-check directly, but nothing previously
# proved the caller-side block (exit 2, the specific "too long to inspect"
# BLOCKED message) actually fires through this hook's real JSON-on-stdin
# entry point. Mirrors the awk-missing integration case above.
echo "case: Bash command inspection fails closed when the command is too long to inspect (length pre-check integration)"
LONG_BASH_CMD="echo $(printf '%*s' 100005 '' | tr ' ' 'a') > ${BASH_TEST_ROOT}/too-long-target.txt"
JSON=$(jq -n --arg cmd "${LONG_BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "bash command inspection blocked when command too long" 2
if [[ "${CHECK_STDERR}" == *"too long to inspect"* ]]; then
    pass
else
    fail "bash command inspection blocked when command too long: stderr should contain the 'too long to inspect' message, got: ${CHECK_STDERR}"
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
WC_MISSING_BIN="${BASH_TEST_ROOT}/bin-no-wc"
make_curated_bin "${WC_MISSING_BIN}" sh cat jq mktemp rm awk realpath
BASH_CMD="echo x > ${BASH_TEST_ROOT}/no-wc-target.txt"
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_check_with_path "${WC_MISSING_BIN}" "${JSON}"
assert_exit "bash command inspection blocked when wc missing" 2
if [[ "${CHECK_STDERR}" == *"wc was not found on PATH"* ]]; then
    pass
else
    fail "bash command inspection blocked when wc missing: stderr should contain the 'wc was not found on PATH' message, got: ${CHECK_STDERR}"
fi

# ── #795 final round: historical-bypass regression table ────────────
# Every construct found across the five adversarial review rounds, driven
# end-to-end through the hook's real JSON-on-stdin entry point, asserting a
# block. Constructs the tokenizer now parses ({backtick,sudo,if-then} tee,
# the mid-command redirect) block via extracted targets; constructs it still
# does not model (brace expansion) block via the empty-parse backstop
# (check_sensitive_raw). Either way: never a silent allow.
echo "case: historical bypass constructs from all five review rounds are blocked end-to-end"
HIST_DIR="${BASH_TEST_ROOT}/hist"
mkdir -p "${HIST_DIR}"
HIST_CMDS=(
    "{tee,cat} ${HIST_DIR}/.env"
    "\`tee ${HIST_DIR}/.env\`"
    "sudo tee ${HIST_DIR}/.env"
    "if true; then tee ${HIST_DIR}/.env; fi"
    "tee /tmp/decoy >/dev/null ${HIST_DIR}/.env"
    "echo x > /dev/fd/../..${HIST_DIR}/.env"
)
for HIST_CMD in "${HIST_CMDS[@]}"; do
    JSON=$(jq -n --arg cmd "${HIST_CMD}" '{tool_input:{command:$cmd}}')
    run_check "${JSON}"
    assert_exit "historical bypass blocked (${HIST_CMD})" 2
    assert_blocked_stderr "historical bypass blocked (${HIST_CMD})"
done

# ── #795 final round: Bash-arm symlink resolution ───────────────────
# The Bash arm was purely lexical -- `>> notes.txt` where notes.txt symlinks
# to a real .env was allowed. It now canonicalizes each target via the same
# parent-anchored canonicalize_path as the Write|Edit arm and checks the
# union (raw + collapsed + canonical).
echo "case: >> through a benign-named symlink resolving to .env is blocked (Bash-arm canonicalization)"
touch "${HIST_DIR}/real.env"
ln -s "${HIST_DIR}/real.env" "${HIST_DIR}/notes.txt"
JSON=$(jq -n --arg cmd ">> notes.txt" '{tool_input:{command:$cmd}}')
run_check_in_dir "${HIST_DIR}" "${JSON}"
assert_exit "bash symlink to .env blocked" 2
assert_blocked_stderr "bash symlink to .env blocked"

# ── #795 final round: empty-parse backstop (check_sensitive_raw) ────
# Zero extracted targets from a command that still looks like it might write
# (any `>`, or a delimited tee token) triggers a raw-text marker scan: a
# marker mention blocks, no marker allows. An alnum-embedded "tee"
# (esteemed, committee) is not a write candidate shape and never triggers
# the scan at all.
echo "case: empty-parse backstop blocks a marker mention (zero targets, quoted >)"
JSON=$(jq -n --arg cmd "echo \"a -> b\" ${BASH_TEST_ROOT}/secrets.yaml" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "backstop marker mention blocked" 2
assert_blocked_stderr "backstop marker mention blocked"

echo "case: empty-parse backstop stays quiet with no marker (quoted > false-positive check)"
JSON=$(jq -n --arg cmd 'echo "a > b"' '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "quoted > no marker allowed" 0

echo "case: alnum-embedded tee with a marker mention is not a write-candidate shape (allowed)"
JSON=$(jq -n --arg cmd "cat ${BASH_TEST_ROOT}/committee-credentials-review.md" '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "embedded tee + marker read allowed" 0

echo "case: grep -r credentials src/ | head is allowed (no write candidate at all)"
JSON=$(jq -n --arg cmd 'grep -r credentials src/ | head' '{tool_input:{command:$cmd}}')
run_check "${JSON}"
assert_exit "grep credentials pipeline allowed" 0

# ── #795 final round: marker-sync contract ───────────────────────────
# check_sensitive_raw's substring markers must cover every check_sensitive
# whole-path glob: for each glob pattern, its literal core (asterisks
# stripped) must CONTAIN at least one raw marker's core -- then any string
# matching the glob necessarily contains that marker, so the backstop can
# never lag behind a blocklist addition. Precedent: the three-way
# permissions.deny sync contract in
# flow/templates/settings-permissions.test.sh.
echo "case: every check_sensitive pattern has a covering check_sensitive_raw marker (marker-sync contract)"
CS_PATTERNS="$(awk '/^check_sensitive\(\) \{/,/^\}$/' "${CHECK_SH}" \
    | grep -E '^[[:space:]]+\*.*\)[[:space:]]*$' | sed 's/[[:space:]]//g; s/)$//' | tr '|' '\n' | grep -v '^$')"
RAW_MARKERS="$(awk '/^check_sensitive_raw\(\) \{/,/^\}$/' "${CHECK_SH}" \
    | grep -E '^[[:space:]]+\*.*\)[[:space:]]*$' | sed 's/[[:space:]]//g; s/)$//' | tr '|' '\n' | grep -v '^$')"
if [[ -n "${CS_PATTERNS}" ]]; then
    pass
else
    fail "marker-sync: extracted no check_sensitive patterns (extraction broken or function renamed)"
fi
if [[ -n "${RAW_MARKERS}" ]]; then
    pass
else
    fail "marker-sync: extracted no check_sensitive_raw markers (extraction broken or function renamed)"
fi
while IFS= read -r CS_PAT; do
    [[ -z "${CS_PAT}" ]] && continue
    CS_CORE="${CS_PAT//\*/}"
    CS_CORE="${CS_CORE#/}"
    if [[ -z "${CS_CORE}" ]]; then
        fail "marker-sync: check_sensitive pattern '${CS_PAT}' has an empty literal core"
        continue
    fi
    COVERED=0
    while IFS= read -r RAW_PAT; do
        RAW_CORE="${RAW_PAT//\*/}"
        [[ -z "${RAW_CORE}" ]] && continue
        if [[ "${CS_CORE}" == *"${RAW_CORE}"* ]]; then
            COVERED=1
            break
        fi
    done <<< "${RAW_MARKERS}"
    if [[ "${COVERED}" -eq 1 ]]; then
        pass
    else
        fail "marker-sync: check_sensitive pattern '${CS_PAT}' has no covering check_sensitive_raw marker -- add a substring marker for it"
    fi
done <<< "${CS_PATTERNS}"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
