#!/bin/bash
# Credential-free model of the login lifecycle behind sandbox credential seeding (#1520).
#
# MODEL of assumed provider semantics, not observed provider behavior.
#
# What is modeled: a fake authority that issues single-use rotating refresh
# tokens (fake-refresh-0001 -> fake-refresh-0002 -> ...), and treats the
# presentation of an already-used token as reuse, which revokes the whole token
# family. Whether Claude Code or Codex really behave this way is NOT established
# here; sandbox/docs/host-auth-lifecycle.md records, per provider, whether reuse
# detection is Observed, an Upstream guarantee or Unresolved. This suite only
# shows what the *sandbox's* seeding code does if the model holds.
#
# What is real: seed_credential from sandbox/lib/seed-auth.sh, run in a scrubbed
# subprocess. Everything else (authority, holders, reader) is an in-test fixture.
#
# Security posture (docs/test-harness.md #363, #490):
#   - no network, no account, no real CLI, no keyring
#   - every credential file lives under one mktemp -d root, removed on exit
#   - fixtures run under `env -i` with only the variables they need
#   - fixtures take credential FILE paths, never token values in argv
#   - $HOME of the caller is never used; fixtures get a decoy home instead
set -uo pipefail

# An ambient reseed flag (a launcher exports it container-wide for
# --reseed-creds) would flip the seed-once cases. seed_credential only ever runs
# under env -i below, and this makes the suite's own environment clean too.
unset CENCI_SANDBOX_RESEED_CREDS

# Probe mode is a child-run switch that case 8 alone sets, via env -i. A value
# inherited from a caller must never turn a real run into a probe that exits 0
# with zero cases, so the switch alone proves nothing: the child also needs the
# parent's root (CENCI_AUTH_LIFECYCLE_PARENT_ROOT) to hold marker files that only
# a live parent creates. Both variables are captured and cleared first thing.
PROBE_MODE_ENV="${CENCI_AUTH_LIFECYCLE_PROBE_MODE:-}"
PARENT_ROOT_ENV="${CENCI_AUTH_LIFECYCLE_PARENT_ROOT:-}"
unset CENCI_AUTH_LIFECYCLE_PROBE_MODE CENCI_AUTH_LIFECYCLE_PARENT_ROOT
NESTED=0
PROBE=0
if [[ -n "${PARENT_ROOT_ENV}" && -f "${PARENT_ROOT_ENV}/nested-ok" ]]; then
    NESTED=1
    if [[ "${PROBE_MODE_ENV}" == "1" && -f "${PARENT_ROOT_ENV}/probe-armed" ]]; then
        PROBE=1
    fi
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || {
    echo "FATAL: cannot resolve the suite directory" >&2
    exit 1
}
SEED_LIB="${SCRIPT_DIR}/../lib/seed-auth.sh"
if [[ ! -f "${SEED_LIB}" ]]; then
    echo "FATAL: seed-auth.sh not found at ${SEED_LIB}" >&2
    exit 1
fi

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# assert_eq <label> <expected> <actual>
assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [[ "${expected}" == "${actual}" ]]; then
        pass
    else
        fail "${label} (expected: ${expected@Q}, got: ${actual@Q})"
    fi
}

# assert_ne <label> <unexpected> <actual>
assert_ne() {
    local label="$1" unexpected="$2" actual="$3"
    if [[ "${unexpected}" != "${actual}" ]]; then
        pass
    else
        fail "${label} (value must differ from: ${unexpected@Q})"
    fi
}

# ── Setup: one checked temp root, cleaned on exit ───────────────────
ROOT="$(mktemp -d)" || {
    echo "FATAL: mktemp -d failed" >&2
    exit 1
}
if [[ -z "${ROOT}" || "${ROOT}" == "/" || ! -d "${ROOT}" ]]; then
    echo "FATAL: mktemp -d returned an unusable root: ${ROOT@Q}" >&2
    exit 1
fi
trap 'rm -rf "${ROOT}"' EXIT

# Probe mode: case 8 starts a child run of this suite that the parent has armed.
# The child creates its root, fills it with a nested file, prints the root path
# and leaves through the normal EXIT trap above, so the parent can check that
# the trap removed the root. The child never reaches a case, so it cannot
# recurse and no case runs twice.
if [[ "${PROBE}" -eq 1 ]]; then
    if ! mkdir -p "${ROOT}/probe/nested"; then
        echo "FATAL: probe: cannot create the nested directory" >&2
        exit 1
    fi
    if ! : > "${ROOT}/probe/nested/file"; then
        echo "FATAL: probe: cannot write the nested file" >&2
        exit 1
    fi
    printf '%s\n' "${ROOT}"
    exit 0
fi

BIN="${ROOT}/bin"
OUT_DIR="${ROOT}/out"
DECOY_HOME="${ROOT}/decoy-home"
mkdir -p "${BIN}" "${OUT_DIR}" "${DECOY_HOME}/.claude" "${DECOY_HOME}/.codex" || {
    echo "FATAL: cannot create the fixture directories under ${ROOT}" >&2
    exit 1
}
# Proof for a nested child run that this root belongs to a live parent suite.
: > "${ROOT}/nested-ok" || {
    echo "FATAL: cannot write the nested-run marker" >&2
    exit 1
}

# Set only in this suite's own environment. It must never reach a fixture, a
# captured output or a file under ${ROOT}.
export CENCI_TEST_SENTINEL_SECRET=sentinel-must-not-leak-0001

FIXTURE_PATH="/usr/bin:/bin"
DECOY_MARKER="decoy-marker-must-stay-0001"
DECOY_CLAUDE="${DECOY_HOME}/.claude/.credentials.json"
DECOY_CODEX="${DECOY_HOME}/.codex/auth.json"
for decoy in "${DECOY_CLAUDE}" "${DECOY_CODEX}"; do
    printf '{"decoy":"%s"}\n' "${DECOY_MARKER}" > "${decoy}" || {
        echo "FATAL: cannot write the decoy credential ${decoy}" >&2
        exit 1
    }
done
DECOY_CLAUDE_BEFORE="$(<"${DECOY_CLAUDE}")"
DECOY_CODEX_BEFORE="$(<"${DECOY_CODEX}")"
DECOY_FILES_BEFORE="$(cd "${DECOY_HOME}" && find . -type f | sort)"
if [[ -z "${DECOY_CLAUDE_BEFORE}" || -z "${DECOY_CODEX_BEFORE}" || -z "${DECOY_FILES_BEFORE}" ]]; then
    echo "FATAL: the decoy snapshots are empty, so case 7 would compare nothing" >&2
    exit 1
fi

# ── Fixtures (written once, run only under env -i) ──────────────────

# write_fixture <name> — stdin becomes ${BIN}/<name>; a failed write is fatal, so
# no case can run against a missing or empty fixture.
write_fixture() {
    cat > "${BIN}/$1" || {
        echo "FATAL: cannot write the fixture ${BIN}/$1" >&2
        exit 1
    }
    if [[ ! -s "${BIN}/$1" ]]; then
        echo "FATAL: the fixture ${BIN}/$1 is empty" >&2
        exit 1
    fi
}

write_fixture fake-common.sh <<'FIXTURE'
# Shared by the fake authority, holder and reader. Credential file shape:
#   {"fakeRefreshToken":"fake-refresh-0001"}
TOKEN_RE='^\{"fakeRefreshToken":"(fake-refresh-[0-9]{4})"\}$'

# parse_token <file> — prints the token, fails on missing/empty/truncated files.
parse_token() {
    local content
    [ -f "$1" ] || return 1
    content="$(<"$1")"
    [[ "${content}" =~ ${TOKEN_RE} ]] || return 1
    printf '%s' "${BASH_REMATCH[1]}"
}

# make_cred <token> — the credential file body.
make_cred() {
    printf '{"fakeRefreshToken":"%s"}\n' "$1"
}
FIXTURE

write_fixture fake-authority <<'FIXTURE'
# fake-authority init   <authority-dir>
# fake-authority state  <authority-dir>
# fake-authority redeem <authority-dir> <cred-file> <out-file>
#
# MODEL: single-use rotating refresh tokens with family revocation on reuse.
# The valid token is always fake-refresh-<seq>. Redeeming it writes the next
# credential to <out-file> and bumps seq. Presenting any other token is reuse:
# it is refused and the whole family is revoked. Once revoked, everything
# fails. Redeem is atomic under a mkdir lock. Prints outcome labels only.
set -u
here="$(dirname "$0")"
# shellcheck disable=SC1091
. "${here}/fake-common.sh"

cmd="${1:?command}"
dir="${2:?authority-dir}"

# Disabling this single check (setting it to 0) turns the authority into one
# that accepts stale tokens, which the suite must then catch.
ENFORCE_SINGLE_USE=1

case "${cmd}" in
init)
    mkdir -p "${dir}" || { echo write-failed; exit 5; }
    printf '1' > "${dir}/seq" || { echo write-failed; exit 5; }
    rm -f "${dir}/revoked"
    ;;
state)
    if [ -e "${dir}/revoked" ]; then revoked=yes; else revoked=no; fi
    printf 'seq=%s revoked=%s\n' "$(<"${dir}/seq")" "${revoked}"
    ;;
redeem)
    cred="${3:?cred-file}"
    out="${4:?out-file}"
    lock="${dir}/lock"
    tries=0
    until mkdir "${lock}" 2>/dev/null; do
        tries=$((tries + 1))
        if [ "${tries}" -ge 200 ]; then
            echo lock-timeout
            exit 4
        fi
        sleep 0.05
    done
    trap 'rmdir "${lock}"' EXIT

    if ! token="$(parse_token "${cred}")"; then
        echo unreadable
        exit 2
    fi
    if [ -e "${dir}/revoked" ]; then
        echo invalid_grant
        exit 1
    fi
    seq="$(<"${dir}/seq")"
    current="$(printf 'fake-refresh-%04d' "${seq}")"
    if [ "${ENFORCE_SINGLE_USE}" = 1 ] && [ "${token}" != "${current}" ]; then
        : > "${dir}/revoked" || { echo write-failed; exit 5; }
        echo invalid_grant
        exit 1
    fi
    seq=$((seq + 1))
    printf '%s' "${seq}" > "${dir}/seq.tmp" || { echo write-failed; exit 5; }
    mv "${dir}/seq.tmp" "${dir}/seq" || { echo write-failed; exit 5; }
    make_cred "$(printf 'fake-refresh-%04d' "${seq}")" > "${out}" || { echo write-failed; exit 5; }
    echo ok
    ;;
*)
    echo "unknown command" >&2
    exit 64
    ;;
esac
FIXTURE

write_fixture fake-holder-refresh <<'FIXTURE'
# fake-holder-refresh <cred-file> <authority-dir> [<barrier-dir>|- [<expected>]]
#
# A holder of one copy of a lineage. Reads its token from the file (never from
# argv), redeems it at the authority and persists the result with temp + mv.
# With a barrier, it signals ready after reading and waits (bounded) until
# <expected> holders are ready before redeeming, so concurrent runs overlap
# deterministically. Prints one label: refreshed | invalid_grant | unreadable |
# barrier-timeout | lock-timeout | persist-failed. Never prints a token.
#
# Test-only hook: FAKE_HOLDER_CRASH_BEFORE_PERSIST=1 makes the holder exit
# cooperatively with status 7 after the authority redeemed (the token is spent
# and the authority has rotated) but before the persist mv, leaving its temp
# file behind. It models "token spent, nothing persisted"; it is not a kill and
# proves nothing about atomic persist or torn writes. It is only ever passed
# through env -i, for the one run that models this outcome.
set -u
here="$(dirname "$0")"
# shellcheck disable=SC1091
. "${here}/fake-common.sh"

cred="${1:?cred-file}"
auth="${2:?authority-dir}"
barrier="${3:--}"
expected="${4:-0}"

if ! parse_token "${cred}" > /dev/null; then
    echo unreadable
    exit 2
fi

if [ "${barrier}" != "-" ]; then
    mkdir -p "${barrier}" || { echo barrier-failed; exit 3; }
    : > "${barrier}/ready.$$" || { echo barrier-failed; exit 3; }
    tries=0
    while :; do
        set -- "${barrier}"/ready.*
        [ "$#" -ge "${expected}" ] && break
        tries=$((tries + 1))
        if [ "${tries}" -ge 200 ]; then
            echo barrier-timeout
            exit 3
        fi
        sleep 0.05
    done
fi

out="${cred}.new.$$"
label="$("${BASH}" "${here}/fake-authority" redeem "${auth}" "${cred}" "${out}")"
rc=$?
case "${label}" in
ok)
    if [ "${FAKE_HOLDER_CRASH_BEFORE_PERSIST:-0}" = 1 ]; then
        # Cooperative exit after redeem, before the persist: the authority has
        # rotated and the temp file exists, but nothing was persisted.
        echo crashed-before-persist
        exit 7
    fi
    if ! mv "${out}" "${cred}"; then
        echo persist-failed
        exit 5
    fi
    echo refreshed
    ;;
*)
    rm -f "${out}"
    echo "${label:-redeem-failed-${rc}}"
    exit 1
    ;;
esac
FIXTURE

write_fixture fake-reader <<'FIXTURE'
# fake-reader <cred-file> — how a modeled CLI treats the file it finds.
# Missing, empty or truncated JSON means the login is gone: needs-relogin. It
# never contacts the authority.
set -u
here="$(dirname "$0")"
# shellcheck disable=SC1091
. "${here}/fake-common.sh"
if parse_token "${1:?cred-file}" > /dev/null; then
    echo ok
else
    echo needs-relogin
fi
FIXTURE

write_fixture fake-env-dump <<'FIXTURE'
# fake-env-dump — prints the environment the fixture was started with.
env
FIXTURE

# ── Runners ─────────────────────────────────────────────────────────

FX_COUNT=0
FX_OUT=""
FX_ERR=""

# run_fx <fixture> [args...] — runs a fixture under env -i with only PATH and
# the decoy HOME. Stdout/stderr go to files under ${OUT_DIR} (scanned later for
# the sentinel) and are mirrored in FX_OUT / FX_ERR. Returns the fixture's rc.
FX_EXTRA=()
_run_fx() {
    local script="$1"
    shift
    FX_COUNT=$((FX_COUNT + 1))
    local base="${OUT_DIR}/fx-${FX_COUNT}"
    env -i "PATH=${FIXTURE_PATH}" "HOME=${DECOY_HOME}" ${FX_EXTRA[@]+"${FX_EXTRA[@]}"} \
        "${BASH}" "${BIN}/${script}" "$@" > "${base}.out" 2> "${base}.err"
    local rc=$?
    FX_OUT="$(<"${base}.out")"
    FX_ERR="$(<"${base}.err")"
    return "${rc}"
}

run_fx() {
    FX_EXTRA=()
    _run_fx "$@"
}

# run_fx_crash <fixture> [args...] — run_fx plus the holder's test-only crash hook,
# passed through env -i for this one run only.
run_fx_crash() {
    FX_EXTRA=("FAKE_HOLDER_CRASH_BEFORE_PERSIST=1")
    _run_fx "$@"
    local rc=$?
    FX_EXTRA=()
    return "${rc}"
}

# run_seed <reseed:0|1> <staged> <dest> — the REAL seed_credential, in a
# scrubbed bash. Only the reseed case carries CENCI_SANDBOX_RESEED_CREDS.
run_seed() {
    local reseed="$1" staged="$2" dest="$3"
    local extra=()
    if [[ "${reseed}" == "1" ]]; then
        extra=("CENCI_SANDBOX_RESEED_CREDS=1")
    fi
    FX_COUNT=$((FX_COUNT + 1))
    local base="${OUT_DIR}/fx-${FX_COUNT}"
    # The single-quoted script is the child bash's, so $1..$3 must not expand here.
    # shellcheck disable=SC2016
    env -i "PATH=${FIXTURE_PATH}" "HOME=${DECOY_HOME}" ${extra[@]+"${extra[@]}"} \
        "${BASH}" -c 'source "$1"; seed_credential "$2" "$3"' _ \
        "${SEED_LIB}" "${staged}" "${dest}" > "${base}.out" 2> "${base}.err"
    local rc=$?
    FX_OUT="$(<"${base}.out")"
    FX_ERR="$(<"${base}.err")"
    return "${rc}"
}

# write_cred <file> <n> — a credential file holding fake-refresh-<n>.
write_cred() {
    mkdir -p "$(dirname "$1")" || {
        echo "FATAL: cannot create the directory for $1" >&2
        exit 1
    }
    printf '{"fakeRefreshToken":"fake-refresh-%04d"}\n' "$2" > "$1" || {
        echo "FATAL: cannot write the credential fixture $1" >&2
        exit 1
    }
}

# new_lineage <case-dir> — a fresh authority at seq 1 (token fake-refresh-0001).
new_lineage() {
    mkdir -p "$1" || {
        echo "FATAL: cannot create the case directory $1" >&2
        exit 1
    }
    run_fx fake-authority init "$1/authority" || fail "authority init failed in $1: ${FX_OUT} ${FX_ERR}"
}

# authority_state <case-dir> — sets AUTH_STATE to "seq=N revoked=yes|no". It sets
# a global instead of printing: a $(...) caller would run fail() in a subshell,
# losing the failure count and reusing the fx-N capture file.
AUTH_STATE=""
authority_state() {
    AUTH_STATE=""
    run_fx fake-authority state "$1/authority" || fail "authority state failed in $1: ${FX_OUT} ${FX_ERR}"
    AUTH_STATE="${FX_OUT}"
}

# holder_bg <cred> <authority> <barrier> <expected> <outfile> — a background
# holder under env -i; sets HOLDER_PID.
HOLDER_PID=""
holder_bg() {
    env -i "PATH=${FIXTURE_PATH}" "HOME=${DECOY_HOME}" \
        "${BASH}" "${BIN}/fake-holder-refresh" "$1" "$2" "$3" "$4" > "$5" 2>&1 &
    HOLDER_PID=$!
}

echo "auth-lifecycle.test.sh"
echo "(modeled outcomes: assumed provider semantics, not observed provider behavior)"

# ── Case 1: two holders of one copied lineage refresh concurrently ──
echo "case: concurrent refresh by two holders of one copied lineage"
C1="${ROOT}/case1"
new_lineage "${C1}"
write_cred "${C1}/holder-a.json" 1
write_cred "${C1}/holder-b.json" 1
holder_bg "${C1}/holder-a.json" "${C1}/authority" "${C1}/barrier" 2 "${C1}/a.out"
PID_A="${HOLDER_PID}"
holder_bg "${C1}/holder-b.json" "${C1}/authority" "${C1}/barrier" 2 "${C1}/b.out"
PID_B="${HOLDER_PID}"
wait "${PID_A}"
RC_A=$?
wait "${PID_B}"
RC_B=$?
LABEL_A="$(<"${C1}/a.out")"
LABEL_B="$(<"${C1}/b.out")"
assert_eq "exactly one holder refreshes and the other gets invalid_grant" \
    "invalid_grant refreshed" \
    "$(printf '%s\n%s\n' "${LABEL_A}" "${LABEL_B}" | sort | tr '\n' ' ' | sed 's/ $//')"
assert_eq "exit codes are one success and one failure" "0 1" \
    "$(printf '%s\n%s\n' "${RC_A}" "${RC_B}" | sort | tr '\n' ' ' | sed 's/ $//')"
authority_state "${C1}"
assert_eq "the loser's reuse revokes the family (modeled)" "seq=2 revoked=yes" "${AUTH_STATE}"
for holder in a b; do
    label="${LABEL_A}"
    [[ "${holder}" == "b" ]] && label="${LABEL_B}"
    case "${label}" in
    refreshed)
        assert_eq "winner (${holder}) persisted the rotated credential" \
            '{"fakeRefreshToken":"fake-refresh-0002"}' "$(<"${C1}/holder-${holder}.json")"
        ;;
    invalid_grant)
        assert_eq "loser (${holder}) keeps its dead credential" \
            '{"fakeRefreshToken":"fake-refresh-0001"}' "$(<"${C1}/holder-${holder}.json")"
        ;;
    *)
        fail "holder ${holder} produced an unexpected outcome: ${label@Q}"
        ;;
    esac
done

# ── Case 2: a rotated refresh token is presented again ─────────────
echo "case: reuse of a rotated refresh token"
C2="${ROOT}/case2"
new_lineage "${C2}"
write_cred "${C2}/live.json" 1
write_cred "${C2}/stale-copy.json" 1
run_fx fake-holder-refresh "${C2}/live.json" "${C2}/authority"
assert_eq "first redeem of the current token rotates it" "refreshed" "${FX_OUT}"
authority_state "${C2}"
assert_eq "no revocation before any reuse" "seq=2 revoked=no" "${AUTH_STATE}"
run_fx fake-holder-refresh "${C2}/stale-copy.json" "${C2}/authority"
RC_REUSE=$?
assert_eq "presenting the rotated token returns invalid_grant" "invalid_grant" "${FX_OUT}"
assert_ne "reuse is a failure exit" "0" "${RC_REUSE}"
authority_state "${C2}"
assert_eq "reuse revokes the family (modeled)" "seq=2 revoked=yes" "${AUTH_STATE}"
run_fx fake-holder-refresh "${C2}/live.json" "${C2}/authority"
assert_eq "the successor token also fails after family revocation" "invalid_grant" "${FX_OUT}"
assert_eq "the successor's credential file is not rotated further" \
    '{"fakeRefreshToken":"fake-refresh-0002"}' "$(<"${C2}/live.json")"

# ── Case 3: seed-once leaves a stale volume copy after a host refresh ─
echo "case: seed-once leaves a stale volume copy after a host refresh"
C3="${ROOT}/case3"
new_lineage "${C3}"
write_cred "${C3}/host.json" 1
HOST0="$(<"${C3}/host.json")"
VOLUME3="${C3}/volume/home/.claude/.credentials.json"
run_seed 0 "${C3}/host.json" "${VOLUME3}"
assert_eq "first seed succeeds" "0" "$?"
assert_eq "first seed copies the host credential into the empty volume" \
    "${HOST0}" "$(<"${VOLUME3}")"
run_fx fake-holder-refresh "${C3}/host.json" "${C3}/authority"
assert_eq "the host refreshes its own copy" "refreshed" "${FX_OUT}"
assert_ne "host copy moved on from the seeded credential" "${HOST0}" "$(<"${C3}/host.json")"
run_seed 0 "${C3}/host.json" "${VOLUME3}"
assert_eq "second seed without the flag succeeds" "0" "$?"
assert_eq "second seed leaves the stale volume copy untouched" \
    "${HOST0}" "$(<"${VOLUME3}")"
assert_ne "volume copy now differs from the host copy" "$(<"${C3}/host.json")" "$(<"${VOLUME3}")"
# An ambient reseed flag must not reach seed_credential: run_seed scrubs it.
CENCI_SANDBOX_RESEED_CREDS=1 run_seed 0 "${C3}/host.json" "${VOLUME3}"
assert_eq "the seed run under an ambient reseed flag succeeds" "0" "$?"
# Proves only that the harness scrubs the flag; that the flag really flips
# seed_credential to a reseed is covered by case 4.
assert_eq "the harness scrubs an ambient reseed flag, so the volume stays stale" \
    "${HOST0}" "$(<"${VOLUME3}")"
run_fx fake-holder-refresh "${VOLUME3}" "${C3}/authority"
assert_eq "the stale volume holder gets invalid_grant" "invalid_grant" "${FX_OUT}"
authority_state "${C3}"
assert_eq "the stale redeem revokes the family (modeled)" "seq=2 revoked=yes" "${AUTH_STATE}"

# ── Case 4: CENCI_SANDBOX_RESEED_CREDS=1 recovery ──────────────────
echo "case: CENCI_SANDBOX_RESEED_CREDS=1 recovery through the real seed_credential"
C4="${ROOT}/case4"
new_lineage "${C4}"
write_cred "${C4}/host.json" 1
VOLUME4="${C4}/volume/home/.claude/.credentials.json"
run_seed 0 "${C4}/host.json" "${VOLUME4}"
assert_eq "initial seed succeeds" "0" "$?"
HOST4_INITIAL="$(<"${C4}/host.json")"
# seed_credential returns 0 even when its copy fails, so the read-back is the check.
assert_eq "the initial seed leaves the volume holding the host's initial credential" \
    "${HOST4_INITIAL}" "$(<"${VOLUME4}")"
run_fx fake-holder-refresh "${C4}/host.json" "${C4}/authority"
assert_eq "the host refreshes its own copy" "refreshed" "${FX_OUT}"
assert_ne "volume is stale before the reseed" "$(<"${C4}/host.json")" "$(<"${VOLUME4}")"
chmod 644 "${VOLUME4}" || {
    echo "FATAL: cannot loosen the mode of ${VOLUME4}" >&2
    exit 1
}
assert_eq "the stale volume copy is mode 644 before the reseed" "644" "$(stat -c '%a' "${VOLUME4}")"
run_seed 1 "${C4}/host.json" "${VOLUME4}"
assert_eq "reseed succeeds" "0" "$?"
assert_eq "reseed replaces the stale volume copy with the host copy" \
    "$(<"${C4}/host.json")" "$(<"${VOLUME4}")"
assert_eq "reseed restores mode 600" "600" "$(stat -c '%a' "${VOLUME4}")"
run_fx fake-reader "${VOLUME4}"
assert_eq "the reseeded credential is readable" "ok" "${FX_OUT}"
run_fx fake-holder-refresh "${VOLUME4}" "${C4}/authority"
assert_eq "the reseeded volume refreshes successfully" "refreshed" "${FX_OUT}"
assert_eq "the reseeded volume persisted the next rotated credential" \
    '{"fakeRefreshToken":"fake-refresh-0003"}' "$(<"${VOLUME4}")"
authority_state "${C4}"
assert_eq "recovery leaves the family intact" "seq=3 revoked=no" "${AUTH_STATE}"

# ── Case 5: token spent without a persisted credential; unreadable files ─
echo "case: token spent without a persisted credential, and truncated/empty credentials"
C5="${ROOT}/case5"
new_lineage "${C5}"
authority_state "${C5}"
STATE5_BEFORE="${AUTH_STATE}"
assert_eq "authority starts at seq 1, unrevoked" "seq=1 revoked=no" "${STATE5_BEFORE}"
printf '{"fakeRefreshToken":"fake-ref' > "${C5}/truncated.json" || {
    echo "FATAL: cannot write the truncated fixture" >&2
    exit 1
}
: > "${C5}/empty.json" || {
    echo "FATAL: cannot write the empty fixture" >&2
    exit 1
}
if [[ -s "${C5}/truncated.json" ]]; then
    pass
else
    fail "the truncated fixture must exist and be non-empty"
fi
if [[ -f "${C5}/empty.json" && ! -s "${C5}/empty.json" ]]; then
    pass
else
    fail "the empty fixture must exist and be zero-size"
fi
run_fx fake-reader "${C5}/truncated.json"
assert_eq "a truncated credential reads as needs-relogin" "needs-relogin" "${FX_OUT}"
run_fx fake-reader "${C5}/empty.json"
assert_eq "an empty credential reads as needs-relogin" "needs-relogin" "${FX_OUT}"
run_fx fake-reader "${C5}/absent.json"
assert_eq "a missing credential reads as needs-relogin" "needs-relogin" "${FX_OUT}"
run_fx fake-holder-refresh "${C5}/truncated.json" "${C5}/authority"
RC_TRUNC=$?
assert_eq "a holder refuses a truncated credential without redeeming" "unreadable" "${FX_OUT}"
assert_ne "an unreadable credential is a failure exit" "0" "${RC_TRUNC}"
assert_eq "a truncated file is left as found" '{"fakeRefreshToken":"fake-ref' \
    "$(<"${C5}/truncated.json")"
authority_state "${C5}"
assert_eq "the authority state is unchanged by the unreadable credentials" \
    "${STATE5_BEFORE}" "${AUTH_STATE}"
# Models "token spent, nothing persisted": the holder exits cooperatively after
# the authority redeemed (token spent, authority rotated) but before the persist
# mv. This is not a kill and says nothing about atomic persist or torn writes.
# The test hook is passed through env -i for this one run only.
C5X="${C5}/crash"
new_lineage "${C5X}"
write_cred "${C5X}/cred.json" 1
run_fx_crash fake-holder-refresh "${C5X}/cred.json" "${C5X}/authority"
RC_CRASH=$?
assert_eq "the modeled holder reports it stopped after redeem, before persist" \
    "crashed-before-persist" "${FX_OUT}"
assert_eq "the modeled holder exits with the hook's status 7" "7" "${RC_CRASH}"
assert_eq "nothing was persisted: the credential file still holds the old content" \
    '{"fakeRefreshToken":"fake-refresh-0001"}' "$(<"${C5X}/cred.json")"
run_fx fake-reader "${C5X}/cred.json"
assert_eq "the intact old credential still reads as ok" "ok" "${FX_OUT}"
TEMP_FILES=("${C5X}"/cred.json.new.*)
if [[ "${#TEMP_FILES[@]}" -eq 1 && -f "${TEMP_FILES[0]}" ]]; then
    pass
else
    fail "the modeled holder leaves exactly one unpersisted temp file behind (found: ${TEMP_FILES[*]@Q})"
fi
authority_state "${C5X}"
assert_eq "the authority rotated: the token was spent but never saved" \
    "seq=2 revoked=no" "${AUTH_STATE}"
run_fx fake-holder-refresh "${C5X}/cred.json" "${C5X}/authority"
assert_eq "the old credential is now a presented-stale token (invalid_grant)" \
    "invalid_grant" "${FX_OUT}"
authority_state "${C5X}"
assert_eq "presenting the spent token revokes the family (modeled)" \
    "seq=2 revoked=yes" "${AUTH_STATE}"

# ── Case 6: the sentinel secret never appears ──────────────────────
echo "case: sentinel secret never appears"
run_fx fake-env-dump
assert_eq "env dump fixture runs" "0" "$?"
SCRUBBED_ENV="${FX_OUT}"
if [[ "${SCRUBBED_ENV}" == *"${CENCI_TEST_SENTINEL_SECRET}"* ]]; then
    fail "the scrubbed fixture environment contains the sentinel"
else
    pass
fi
if [[ "${SCRUBBED_ENV}" == *"CENCI_TEST_SENTINEL_SECRET"* ]]; then
    fail "the scrubbed fixture environment contains the sentinel variable name"
else
    pass
fi
assert_eq "a scrubbed fixture sees exactly PATH and HOME" "HOME PATH" \
    "$(printf '%s\n' "${SCRUBBED_ENV}" | cut -d= -f1 | grep -vxE '_|PWD|SHLVL|OLDPWD' | sort | tr '\n' ' ' | sed 's/ $//')"
# Positive control: an unscrubbed run does carry it, so the checks above can fail.
UNSCRUBBED_ENV="$("${BASH}" "${BIN}/fake-env-dump")"
if [[ "${UNSCRUBBED_ENV}" == *"${CENCI_TEST_SENTINEL_SECRET}"* ]]; then
    pass
else
    fail "positive control: an unscrubbed fixture should see the sentinel"
fi
# Positive control for the file scan: plant the sentinel in a scratch file under
# the scanned root and run the exact same scan, which must find it. The control
# file is removed before the real scan.
SCAN_CONTROL_DIR="${ROOT}/scan-control"
mkdir -p "${SCAN_CONTROL_DIR}" || {
    echo "FATAL: cannot create ${SCAN_CONTROL_DIR}" >&2
    exit 1
}
printf '%s\n' "${CENCI_TEST_SENTINEL_SECRET}" > "${SCAN_CONTROL_DIR}/planted" || {
    echo "FATAL: cannot plant the sentinel scan control" >&2
    exit 1
}
grep -rqF -- "${CENCI_TEST_SENTINEL_SECRET}" "${ROOT}"
GREP_RC=$?
assert_eq "positive control: the sentinel scan finds a planted sentinel" "0" "${GREP_RC}"
rm -rf "${SCAN_CONTROL_DIR}" || {
    echo "FATAL: cannot remove the sentinel scan control" >&2
    exit 1
}
grep -rqF -- "${CENCI_TEST_SENTINEL_SECRET}" "${ROOT}"
GREP_RC=$?
assert_eq "no file or captured output under the root contains the sentinel" "1" "${GREP_RC}"

# ── Case 7: the decoy home is untouched ────────────────────────────
echo "case: decoy home untouched"
assert_eq "decoy .claude credential is unchanged" "${DECOY_CLAUDE_BEFORE}" "$(<"${DECOY_CLAUDE}")"
assert_eq "decoy .codex credential is unchanged" "${DECOY_CODEX_BEFORE}" "$(<"${DECOY_CODEX}")"
assert_eq "no file was added to or removed from the decoy home" "${DECOY_FILES_BEFORE}" \
    "$(cd "${DECOY_HOME}" && find . -type f | sort)"
# Positive control, as in case 6: the same scan must find a planted marker, and
# the planted file is gone before the real scan.
DECOY_CONTROL_DIR="${OUT_DIR}/scan-control"
mkdir -p "${DECOY_CONTROL_DIR}" || {
    echo "FATAL: cannot create ${DECOY_CONTROL_DIR}" >&2
    exit 1
}
printf '%s\n' "${DECOY_MARKER}" > "${DECOY_CONTROL_DIR}/planted" || {
    echo "FATAL: cannot plant the decoy marker scan control" >&2
    exit 1
}
grep -rqF -- "${DECOY_MARKER}" "${OUT_DIR}"
GREP_RC=$?
assert_eq "positive control: the decoy scan finds a planted marker" "0" "${GREP_RC}"
rm -rf "${DECOY_CONTROL_DIR}" || {
    echo "FATAL: cannot remove the decoy marker scan control" >&2
    exit 1
}
grep -rqF -- "${DECOY_MARKER}" "${OUT_DIR}"
GREP_RC=$?
assert_eq "the decoy marker never appears in captured output" "1" "${GREP_RC}"
# No path in this suite may derive from the caller's home directory.
# The pattern is split so this line cannot match itself.
CALLER_HOME_RE='\$\{?HOM''E'
# Positive control: a planted line that references the variable must match the
# same regex, so the scan below can fail. The planted line is split like the
# pattern, and the scratch file is removed before the real scan.
CALLER_REF_CONTROL_DIR="${ROOT}/home-scan-control"
mkdir -p "${CALLER_REF_CONTROL_DIR}" || {
    echo "FATAL: cannot create ${CALLER_REF_CONTROL_DIR}" >&2
    exit 1
}
# The single quotes keep the planted text literal on purpose.
# shellcheck disable=SC2016
printf '%s\n' 'cache_dir="${HOM''E}/.cache"' > "${CALLER_REF_CONTROL_DIR}/planted" || {
    echo "FATAL: cannot plant the home-variable scan control" >&2
    exit 1
}
PLANTED_CODE="$(grep -vE '^[[:space:]]*#' "${CALLER_REF_CONTROL_DIR}/planted")"
grep -qE "${CALLER_HOME_RE}" <<< "${PLANTED_CODE}"
GREP_RC=$?
assert_eq "positive control: the home-variable scan finds a planted reference" "0" "${GREP_RC}"
rm -rf "${CALLER_REF_CONTROL_DIR}" || {
    echo "FATAL: cannot remove the home-variable scan control" >&2
    exit 1
}
if [[ -e "${CALLER_REF_CONTROL_DIR}" ]]; then
    echo "FATAL: the home-variable scan control is still present" >&2
    exit 1
fi
SUITE_CODE="$(grep -vE '^[[:space:]]*#' "${BASH_SOURCE[0]}")"
assert_ne "the home-variable scan input (the suite's own code) is not empty" "" "${SUITE_CODE}"
grep -qE "${CALLER_HOME_RE}" <<< "${SUITE_CODE}"
GREP_RC=$?
assert_eq "the suite never expands the caller's home directory variable" "1" "${GREP_RC}"

# ── Case 8: the harness removes its own temp root on exit ──────────
echo "case: temp root removed on exit"
if [[ "${NESTED}" -eq 1 ]]; then
    # A nested child run (the ambient-flag check below) stops here, so the case
    # cannot recurse into more child runs.
    echo "  (skipped in a nested run)"
else
    # The children get only PATH, their own TMPDIR and the two variables a parent
    # suite arms. Each TMPDIR sits under this run's root, so a failing child
    # cannot leak a directory into the runner's temp area.
    AMBIENT_TMP="${ROOT}/ambient-tmp"
    PROBE_TMP="${ROOT}/probe-tmp"
    mkdir -p "${AMBIENT_TMP}" "${PROBE_TMP}" || {
        echo "FATAL: cannot create the child TMPDIR directories" >&2
        exit 1
    }

    # Ambient flag: a child that sees the probe switch but no probe-armed marker
    # must run the real suite, not exit 0 with zero cases.
    env -i "PATH=${FIXTURE_PATH}" "TMPDIR=${AMBIENT_TMP}" \
        CENCI_AUTH_LIFECYCLE_PROBE_MODE=1 "CENCI_AUTH_LIFECYCLE_PARENT_ROOT=${ROOT}" \
        "${BASH}" "${BASH_SOURCE[0]}" > "${OUT_DIR}/ambient.out" 2> "${OUT_DIR}/ambient.err"
    AMBIENT_RC=$?
    AMBIENT_OUT="$(<"${OUT_DIR}/ambient.out")"
    assert_eq "a run that inherits the probe switch alone still runs the real suite and exits 0" \
        "0" "${AMBIENT_RC}"
    if [[ "${AMBIENT_OUT}" == *"case: concurrent refresh by two holders of one copied lineage"* \
        && "${AMBIENT_OUT}" == *"case: decoy home untouched"* ]]; then
        pass
    else
        fail "the inherited-switch run did not execute the real cases"
    fi
    AMBIENT_PASSED_RE='Passed: ([0-9]+), Failed: 0$'
    if [[ "${AMBIENT_OUT}" =~ ${AMBIENT_PASSED_RE} && "${BASH_REMATCH[1]}" -gt 0 ]]; then
        pass
    else
        fail "the inherited-switch run reported no passing assertions"
    fi
    assert_eq "the inherited-switch run printed nothing on stderr" "" "$(<"${OUT_DIR}/ambient.err")"

    : > "${ROOT}/probe-armed" || {
        echo "FATAL: cannot arm the probe run" >&2
        exit 1
    }
    env -i "PATH=${FIXTURE_PATH}" "TMPDIR=${PROBE_TMP}" \
        CENCI_AUTH_LIFECYCLE_PROBE_MODE=1 "CENCI_AUTH_LIFECYCLE_PARENT_ROOT=${ROOT}" \
        "${BASH}" "${BASH_SOURCE[0]}" > "${OUT_DIR}/probe.out" 2> "${OUT_DIR}/probe.err"
    PROBE_RC=$?
    rm -f "${ROOT}/probe-armed" || {
        echo "FATAL: cannot disarm the probe run" >&2
        exit 1
    }
    PROBE_ROOT="$(<"${OUT_DIR}/probe.out")"
    assert_eq "the probe run exits 0 through its normal trap" "0" "${PROBE_RC}"
    assert_ne "the probe printed a root path" "" "${PROBE_ROOT}"
    if [[ "${PROBE_ROOT}" == "${PROBE_TMP}"/* && "${PROBE_ROOT}" != *$'\n'* ]]; then
        pass
    else
        fail "the probe root is one mktemp path under the temp area (got: ${PROBE_ROOT@Q})"
    fi
    assert_eq "the probe run printed nothing on stderr" "" "$(<"${OUT_DIR}/probe.err")"
    if [[ -e "${PROBE_ROOT}" ]]; then
        PROBE_ROOT_STATE="present"
    else
        PROBE_ROOT_STATE="absent"
    fi
    assert_eq "the probe root no longer exists after the run exits" "absent" "${PROBE_ROOT_STATE}"
fi

echo
echo "Passed: ${PASSES}, Failed: ${FAILURES}"
if [[ "${PASSES}" -eq 0 ]]; then
    echo "FAIL: no assertion ran, so a zero-case run cannot count as a pass" >&2
    exit 1
fi
[[ "${FAILURES}" -eq 0 ]]
