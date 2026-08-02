#!/usr/bin/env bash
# ensure-issue.test.sh -- behavior suite for ticket #876 (2/12 of #661):
# refine's deterministic create/recover/repair/link helper,
# `ensure-issue.sh`, invoked identically by SKILL.md and codex.md so every
# confirmed manifest entry (split child or companion design issue) resolves
# to at most one GitHub issue across timeouts, retries, crashes, and
# resumed refinement sessions.
#
# RED-phase note: ensure-issue.sh does not exist yet -- every case below is
# expected to fail because `bash "${SCRIPT}"` cannot find the file (exit
# 127), not because of a bug in this test file. Phase 4 implements the
# script to make these pass.
#
# Fixture idiom: a stateful fake `gh` placed on PATH, following
# flow/tests/maintain.test.sh:1644-1654's `PATH="${ROOT}/bin:${PATH}"`
# pattern -- but unlike a stdout-stub fixture, this one holds real
# server-side state (a JSON issue store at $GH_FIXTURE_STORE) so every case
# can assert *exactly one issue exists* server-side, not just what
# ensure-issue.sh printed. Colocated next to ensure-issue.sh, mirroring
# flow/skills/configure/scripts/detect-project.test.sh's placement.
#
# `failures` counter, small assert_* helpers, mktemp -d fixture repos,
# self-contained, auto-discovered by scripts/run-checks.sh's `*.test.sh`
# glob -- no registration needed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "ensure-issue.test.sh: failed to resolve script directory." >&2; exit 2; }
SCRIPT="${SCRIPT_DIR}/ensure-issue.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_ne() { [[ "$1" != "$2" ]] || fail "$3: expected NOT [$2]"; }

# =====================================================================
# Fixture gh: real server-side state in $GH_FIXTURE_STORE, toggled failure
# modes via marker files under $GH_FIXTURE_CTL_DIR.
#
# Store shape: {"nextNumber": <n>, "issues": [
#   {number, title, body, labels: [<name>...], milestone: <n|null>,
#    login, parent: <n|null>, subIssues: [<n>...]}
# ]}
#
# Endpoints modelled (matching ensure-issue.sh's REST-only surface --
# decision #3, no search API):
#   gh api user --jq .login
#   gh api "repos/<o>/<r>/issues?...&creator=...&since=..." --paginate   (list; ignores query filters deliberately -- forces the SCRIPT to do its own creator/marker filtering, see the forged-author case below)
#   gh api repos/<o>/<r>/issues -X POST --input <f> --jq .number          (create)
#   gh api repos/<o>/<r>/issues/<n> -X PATCH --input <f>                 (repair)
#   gh api repos/<o>/<r>/issues/<n> [--jq <expr>]                        (fetch one)
#   gh issue edit <n> --repo <o>/<r> --parent <p>                        (link)
#   gh issue view <p> --repo <o>/<r> --json subIssues --jq <expr>        (verify link)
# =====================================================================
write_fixture_gh() {
  local bindir="$1"
  mkdir -p "${bindir}"
  cat > "${bindir}/gh" <<'GHEOF'
#!/usr/bin/env bash
set -uo pipefail

STORE="${GH_FIXTURE_STORE:?GH_FIXTURE_STORE not set}"
CTL="${GH_FIXTURE_CTL_DIR:?GH_FIXTURE_CTL_DIR not set}"
LOGIN="${GH_FIXTURE_LOGIN:-cenci-bot}"

write_store() {
  local tmp
  tmp="$(mktemp "${STORE}.XXXXXX")" || { echo "fixture gh: mktemp failed" >&2; exit 90; }
  cat > "${tmp}"
  mv "${tmp}" "${STORE}"
}

case "${1:-}" in
  api)
    shift
    ENDPOINT=""
    METHOD="GET"
    INPUT=""
    JQEXPR=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -X) METHOD="$2"; shift 2 ;;
        --input) INPUT="$2"; shift 2 ;;
        --jq) JQEXPR="$2"; shift 2 ;;
        --paginate) shift ;;
        *) [[ -z "${ENDPOINT}" ]] && ENDPOINT="$1"; shift ;;
      esac
    done

    if [[ "${ENDPOINT}" == "user" ]]; then
      [[ -f "${CTL}/fail-user" ]] && exit 1
      RESULT="$(jq -n --arg login "${LOGIN}" '{login:$login}')"

    elif [[ "${ENDPOINT}" == */issues\?* ]]; then
      [[ -f "${CTL}/fail-list" ]] && exit 1
      RESULT="$(jq -c '[.issues[] | {number, title, body, labels: [.labels[] | {name: .}], milestone: (if .milestone == null then null else {number: .milestone} end), user: {login: .login}}]' "${STORE}")"
      if [[ -f "${CTL}/simulate-multi-page" ]]; then
        # Two concatenated top-level JSON-array "pages" -- untested-until-now
        # shape a real `gh api ... --paginate` response is not known to
        # produce today, but defended against anyway (#876 review finding
        # 3): every downstream jq call must fail closed rather than
        # silently mis-parse this into a single-page array.
        printf '%s\n%s\n' "${RESULT}" "${RESULT}"
        exit 0
      fi

    elif [[ "${ENDPOINT}" == */issues && "${METHOD}" == "POST" ]]; then
      [[ -n "${INPUT}" ]] || { echo "fixture gh: POST requires --input" >&2; exit 2; }
      PAYLOAD="$(cat "${INPUT}")"
      NUM="$(jq -r '.nextNumber' "${STORE}")"
      jq --argjson n "${NUM}" --argjson p "${PAYLOAD}" --arg login "${LOGIN}" \
        '.nextNumber = (.nextNumber + 1) | .issues += [{number:$n, title:$p.title, body:$p.body, labels:($p.labels // []), milestone:($p.milestone // null), login:$login, parent:null, subIssues:[]}]' \
        "${STORE}" | write_store
      # Unconditional POST-call counter (#913 AC1): every accepted create
      # appends one line here, regardless of which branch below runs next,
      # so "no second POST happened" can be asserted directly rather than
      # inferred from store_issue_count (which a seeded/forged candidate can
      # also inflate).
      printf 'post\n' >> "${CTL}/post-call-count"
      if [[ -f "${CTL}/crash-after-post" ]]; then
        # Real mid-`ensure` crash-fidelity proof (#913 Decision 5): the
        # store write above already landed server-side; now SIGKILL
        # ensure-issue.sh's own PID before it can reach persist_entry_result
        # (ensure-issue.sh:630-663). Gated strictly on this control marker
        # -- via a PID recorded by run_ensure_issue_crashing before the
        # script is even exec'd -- so a stale/absent PID can never fire a
        # no-op kill that would masquerade as a crash.
        TARGET_PID="$(cat "${CTL}/target.pid" 2>/dev/null)" || { echo "fixture gh: crash-after-post set but target.pid could not be read" >&2; exit 92; }
        [[ -n "${TARGET_PID}" ]] || { echo "fixture gh: crash-after-post set but target.pid was empty" >&2; exit 92; }
        [[ "${TARGET_PID}" =~ ^[1-9][0-9]*$ ]] || { echo "fixture gh: crash-after-post set but target.pid was not a positive integer" >&2; exit 92; }
        kill -KILL "${TARGET_PID}" || { echo "fixture gh: crash-after-post kill -KILL ${TARGET_PID} failed" >&2; exit 92; }
        # Single-shot arming (#913 review finding, PID-reuse hazard): clear
        # both the marker and the recorded PID immediately after a
        # successful kill so a later fixture-gh invocation (a future
        # scenario, or any re-entry that happens to read a stale
        # target.pid) can never re-fire this kill against a since-recycled,
        # unrelated PID.
        rm -f "${CTL}/crash-after-post" "${CTL}/target.pid"
        exit 92
      fi
      if [[ -f "${CTL}/simulate-post-timeout" ]]; then
        exit 1
      fi
      if [[ -f "${CTL}/simulate-malformed-response" ]]; then
        echo "not-json-garbage"
        exit 0
      fi
      echo "${NUM}"
      exit 0

    elif [[ "${ENDPOINT}" =~ /issues/([0-9]+)$ && "${METHOD}" == "PATCH" ]]; then
      NUM="${BASH_REMATCH[1]}"
      [[ -f "${CTL}/fail-patch-${NUM}" ]] && exit 1
      [[ -n "${INPUT}" ]] || { echo "fixture gh: PATCH requires --input" >&2; exit 2; }
      PAYLOAD="$(cat "${INPUT}")"
      # simulate-patch-body-noop-<NUM> -- a partial-apply PATCH: title,
      # labels, and milestone land normally, but the body update is
      # deliberately dropped, modeling a PATCH that "succeeds" (exit 0)
      # while silently failing to apply one field (#876 review finding 6).
      if [[ -f "${CTL}/simulate-patch-body-noop-${NUM}" ]]; then
        jq --argjson n "${NUM}" --argjson p "${PAYLOAD}" \
          '(.issues[] | select(.number == $n)) |= (
            . + (if ($p | has("title")) then {title: $p.title} else {} end)
              + (if ($p | has("labels")) then {labels: $p.labels} else {} end)
              + (if ($p | has("milestone")) then {milestone: $p.milestone} else {} end)
          )' "${STORE}" | write_store
        exit 0
      fi
      jq --argjson n "${NUM}" --argjson p "${PAYLOAD}" \
        '(.issues[] | select(.number == $n)) |= (
          . + (if ($p | has("title")) then {title: $p.title} else {} end)
            + (if ($p | has("body")) then {body: $p.body} else {} end)
            + (if ($p | has("labels")) then {labels: $p.labels} else {} end)
            + (if ($p | has("milestone")) then {milestone: $p.milestone} else {} end)
        )' "${STORE}" | write_store
      exit 0

    elif [[ "${ENDPOINT}" =~ /issues/([0-9]+)$ && "${METHOD}" == "GET" ]]; then
      NUM="${BASH_REMATCH[1]}"
      RESULT="$(jq --argjson n "${NUM}" '.issues[] | select(.number == $n) | {number, title, body, labels: [.labels[] | {name: .}], milestone: (if .milestone == null then null else {number: .milestone} end), user: {login: .login}}' "${STORE}")"

    else
      echo "fixture gh: unhandled api endpoint [${ENDPOINT}] method=${METHOD}" >&2
      exit 2
    fi

    if [[ -n "${JQEXPR}" ]]; then
      jq -r "${JQEXPR}" <<<"${RESULT}"
    else
      printf '%s\n' "${RESULT}"
    fi
    ;;

  issue)
    shift
    case "${1:-}" in
      edit)
        shift
        NUM="$1"; shift
        PARENT=""
        while [[ $# -gt 0 ]]; do
          case "$1" in
            --repo) shift 2 ;;
            --parent) PARENT="$2"; shift 2 ;;
            *) shift ;;
          esac
        done
        [[ -f "${CTL}/fail-link" ]] && exit 1
        jq --argjson n "${NUM}" --argjson p "${PARENT}" \
          '(.issues[] | select(.number == $n) | .parent) = $p
           | (.issues[] |= (if .number == $p then .subIssues = ((.subIssues + [$n]) | unique) else . end))' \
          "${STORE}" | write_store
        exit 0
        ;;
      view)
        shift
        NUM="$1"; shift
        JQEXPR=""
        while [[ $# -gt 0 ]]; do
          case "$1" in
            --repo) shift 2 ;;
            --json) shift 2 ;;
            --jq) JQEXPR="$2"; shift 2 ;;
            *) shift ;;
          esac
        done
        RESULT="$(jq --argjson n "${NUM}" '.issues[] | select(.number == $n) | {number, subIssues: {nodes: [.subIssues[] | {number: .}]}}' "${STORE}")"
        if [[ -n "${JQEXPR}" ]]; then
          jq -r "${JQEXPR}" <<<"${RESULT}"
        else
          printf '%s\n' "${RESULT}"
        fi
        exit 0
        ;;
      *)
        echo "fixture gh: unhandled issue subcommand [${1:-}]" >&2
        exit 2
        ;;
    esac
    ;;

  *)
    echo "fixture gh: unhandled command [${1:-}]" >&2
    exit 2
    ;;
esac
GHEOF
  chmod +x "${bindir}/gh"
}

# =====================================================================
# Fixture repo scaffolding
# =====================================================================

# make_env -- fresh mktemp -d ROOT with the fixture gh on a per-test PATH,
# an empty issue store, an empty control-marker directory, and a checkpoint
# path under .plans/ (mirroring the real .plans/.refine-<parent>.checkpoint.json
# location, gitignored, per-checkout).
make_env() {
  ROOT="$(mktemp -d)" || { echo "make_env: mktemp -d failed" >&2; exit 2; }
  mkdir -p "${ROOT}/bin" "${ROOT}/ctl" "${ROOT}/.plans" "${ROOT}/inputs"
  write_fixture_gh "${ROOT}/bin"
  printf '{"nextNumber": 500, "issues": []}' > "${ROOT}/store.json"
  CHECKPOINT="${ROOT}/.plans/.refine-100.checkpoint.json"
  export GH_FIXTURE_STORE="${ROOT}/store.json"
  export GH_FIXTURE_CTL_DIR="${ROOT}/ctl"
  export GH_FIXTURE_LOGIN="cenci-bot"
}

# run_ensure_issue <args...> -- invokes ensure-issue.sh with the fixture gh
# first on PATH; captures OUT and CODE. Runs with cwd=ROOT, mirroring every
# real caller (refine/SKILL.md, refine/codex.md), which always invokes this
# script from the repo/worktree root and passes --checkpoint relative to
# it (`.plans/.refine-<n>.checkpoint.json`) -- validate_checkpoint_path's
# .plans/ branch is anchored to the resolved cwd (#876 review finding 1),
# so this fixture's cwd must match that real invocation shape.
run_ensure_issue() {
  OUT="$(cd "${ROOT}" && PATH="${ROOT}/bin:${PATH}" bash "${SCRIPT}" "$@" 2>&1)"
  CODE=$?
}

# run_ensure_issue_crashing <args...> -- like run_ensure_issue, but the
# script runs backgrounded inside a subshell that first records its own
# (post-exec) PID under ${GH_FIXTURE_CTL_DIR}/target.pid, then execs
# ensure-issue.sh in place (#913 AC1). Deterministic: the PID is written
# before the exec'd process can be invoked at all, so there is no race
# between "PID recorded" and "fixture gh reads target.pid" -- no sleep is
# needed or used. The fixture gh's `crash-after-post` marker (see
# write_fixture_gh) SIGKILLs this recorded PID immediately after its create
# POST lands server-side and before persist_entry_result would run. Sets
# OUT/CODE exactly like run_ensure_issue; CODE is 137 (128+SIGKILL) when the
# kill actually fired, and whatever ensure-issue.sh itself would have
# returned otherwise (see the crash-harness-non-vacuity case below).
run_ensure_issue_crashing() {
  local outfile bgpid
  outfile="$(mktemp "${ROOT}/crash-run-out.XXXXXX")" \
    || { fail "run_ensure_issue_crashing: mktemp failed for the captured-output scratch file"; return 1; }
  (
    cd "${ROOT}" || exit 99
    export PATH="${ROOT}/bin:${PATH}"
    echo "${BASHPID}" > "${GH_FIXTURE_CTL_DIR}/target.pid"
    exec bash "${SCRIPT}" "$@"
  ) > "${outfile}" 2>&1 &
  bgpid=$!
  wait "${bgpid}"
  CODE=$?
  OUT="$(cat "${outfile}" 2>/dev/null)"
  rm -f -- "${outfile}"
}

# seed_issue <number> <title> <body> <labels-json> <milestone-json> <login> <parent-json> -- writes a
# server-side issue directly into the store, bypassing ensure-issue.sh
# entirely, so tests can construct pre-existing state (legacy issues,
# duplicate markers, forged-author issues, etc.).
seed_issue() {
  local number="$1" title="$2" body="$3" labels="$4" milestone="$5" login="$6" parent="${7:-null}"
  jq --argjson n "${number}" --arg t "${title}" --arg b "${body}" --argjson l "${labels}" --argjson m "${milestone}" --arg login "${login}" --argjson p "${parent}" \
    '.issues += [{number:$n, title:$t, body:$b, labels:$l, milestone:$m, login:$login, parent:$p, subIssues:[]}]
     | .nextNumber = ([.nextNumber, ($n + 1)] | max)' \
    "${GH_FIXTURE_STORE}" > "${GH_FIXTURE_STORE}.tmp" && mv "${GH_FIXTURE_STORE}.tmp" "${GH_FIXTURE_STORE}"
}

store_issue_count() { jq '.issues | length' "${GH_FIXTURE_STORE}"; }
store_issue_field() { local n="$1" filter="$2"; jq -c --argjson n "${n}" ".issues[] | select(.number == \$n) | ${filter}" "${GH_FIXTURE_STORE}"; }
checkpoint_field() { jq -r "$1" "${CHECKPOINT}" 2>/dev/null; }
# post_call_count -- number of create-POST calls accepted by the fixture gh
# so far (see write_fixture_gh's unconditional post-call-count counter),
# asserted directly rather than inferred from store_issue_count (#913 AC1).
post_call_count() {
  if [[ -f "${GH_FIXTURE_CTL_DIR}/post-call-count" ]]; then
    wc -l < "${GH_FIXTURE_CTL_DIR}/post-call-count" | tr -d ' '
  else
    echo 0
  fi
}

write_manifest() {
  # write_manifest <path> <json-array> -- manifest entries: [{slot,
  # titleFile, bodyFile, labels, milestone}, ...]
  printf '%s' "$2" > "$1"
}

write_text() { printf '%s' "$2" > "$1"; }

# =====================================================================
# init -- mint + validate nonces, atomic checkpoint write
# =====================================================================

make_env
write_text "${ROOT}/inputs/child1-title.txt" "Add API validation (1/2)"
write_text "${ROOT}/inputs/child1-body.md" "Related to #100

Child body one."
write_text "${ROOT}/inputs/child2-title.txt" "Wire up UI (2/2)"
write_text "${ROOT}/inputs/child2-body.md" "Related to #100

Child body two."
write_manifest "${ROOT}/manifest.json" '[
  {"slot":"child-1-of-2","titleFile":"'"${ROOT}"'/inputs/child1-title.txt","bodyFile":"'"${ROOT}"'/inputs/child1-body.md","labels":["Refined"],"milestone":null},
  {"slot":"child-2-of-2","titleFile":"'"${ROOT}"'/inputs/child2-title.txt","bodyFile":"'"${ROOT}"'/inputs/child2-body.md","labels":["Refined","Browser"],"milestone":5}
]'

run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${CHECKPOINT}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "0" "init: happy path exits 0 (got: ${OUT})"
[[ -f "${CHECKPOINT}" ]] || fail "init: checkpoint file was not written at ${CHECKPOINT}"
assert_eq "$(checkpoint_field '.schemaVersion')" "1" "init: schemaVersion == 1"
assert_eq "$(checkpoint_field '.repo')" "acme/widgets" "init: repo recorded"
assert_eq "$(checkpoint_field '.parent')" "100" "init: parent recorded"
assert_eq "$(checkpoint_field '.creatorLogin')" "cenci-bot" "init: creatorLogin resolved via gh api user --jq .login"
assert_eq "$(checkpoint_field '.entries | length')" "2" "init: two entries recorded from the manifest"
assert_eq "$(checkpoint_field '.entries[0].slot')" "child-1-of-2" "init: entry 0 slot"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "null" "init: entry 0 issueNumber starts null"
assert_eq "$(checkpoint_field '.entries[0].verification')" "pending" "init: entry 0 verification starts pending"
assert_eq "$(checkpoint_field '.entries[0].link')" "unlinked" "init: entry 0 link starts unlinked"
NONCE1="$(checkpoint_field '.entries[0].nonce')"
NONCE2="$(checkpoint_field '.entries[1].nonce')"
[[ "${NONCE1}" =~ ^[0-9a-f]{32}$ ]] || fail "init: entry 0 nonce must match ^[0-9a-f]{32}\$ (got: ${NONCE1})"
[[ "${NONCE2}" =~ ^[0-9a-f]{32}$ ]] || fail "init: entry 1 nonce must match ^[0-9a-f]{32}\$ (got: ${NONCE2})"
assert_ne "${NONCE1}" "${NONCE2}" "init: two entries must mint distinct nonces"
TITLEHASH1="$(checkpoint_field '.entries[0].titleHash')"
[[ "${TITLEHASH1}" =~ ^[0-9a-f]{64}$ ]] || fail "init: entry 0 titleHash must be a sha256 hex digest (got: ${TITLEHASH1})"
assert_eq "$(checkpoint_field '.entries[1].labels')" '["Refined","Browser"]' "init: entry 1 labels carried from manifest"
assert_eq "$(checkpoint_field '.entries[1].milestone')" "5" "init: entry 1 milestone carried from manifest"
rm -rf "${ROOT}"

# --- init: missing/unreadable manifest fails closed (exit 2), no checkpoint ---
make_env
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${CHECKPOINT}" --manifest "${ROOT}/does-not-exist.json"
assert_eq "${CODE}" "2" "init: missing manifest exits 2 (usage/preflight)"
[[ ! -f "${CHECKPOINT}" ]] || fail "init: missing manifest must not write a checkpoint"
rm -rf "${ROOT}"

# --- init: malformed manifest JSON fails closed (exit 2) ---
make_env
write_text "${ROOT}/manifest.json" "{ not json"
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${CHECKPOINT}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "2" "init: malformed manifest JSON exits 2"
[[ ! -f "${CHECKPOINT}" ]] || fail "init: malformed manifest must not write a checkpoint"
rm -rf "${ROOT}"

# =====================================================================
# --checkpoint path traversal is rejected outright (exit 2) -- a literal
# ".." path segment must never be accepted, even when the bare string
# starts with an otherwise-allowed prefix (#876 review finding 1:
# `/tmp/cenci/../../../home/user/.claude/settings.json` passed the old
# bare-prefix-match check even though it resolves well outside tmp_root).
# =====================================================================
make_env
TMP_ROOT="${TMPDIR:-/tmp}/cenci/"
# Checking `-f "${TMP_ROOT}../../../etc/passwd..."` literally would be
# vacuous: a non-root test process almost certainly can't write under /etc
# anyway, so that assertion could pass even if the traversal guard were
# broken (#876 review finding). Instead, escape into a location this test
# process CAN write to -- ROOT itself, which mktemp -d already guarantees
# shares the exact same parent as TMP_ROOT (both are created directly under
# "${TMPDIR:-/tmp}") -- so a real write there, if the guard failed to catch
# this traversal shape, would be observable, not masked by permissions.
ESCAPE_SUBDIR="traversal-escape-target"
mkdir -p "${ROOT}/${ESCAPE_SUBDIR}"
TRAVERSAL_TARGET="${TMP_ROOT}../$(basename -- "${ROOT}")/${ESCAPE_SUBDIR}/pwned-checkpoint.json"
RESOLVED_TRAVERSAL_TARGET="${ROOT}/${ESCAPE_SUBDIR}/pwned-checkpoint.json"
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${TRAVERSAL_TARGET}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "2" "traversal: --checkpoint containing .. (tmp_root bypass shape) is rejected (got ${CODE}: ${OUT})"
[[ ! -f "${RESOLVED_TRAVERSAL_TARGET}" ]] || fail "traversal: init must not have written to the resolved (writable) traversal target ${RESOLVED_TRAVERSAL_TARGET}"
rm -rf "${ROOT}"

# A .. segment inside an otherwise-valid .plans/ tail must also be
# rejected -- the .. segment itself is disqualifying, regardless of where
# it would resolve.
make_env
PLANS_TRAVERSAL="${ROOT}/.plans/../.plans/.refine-100.checkpoint.json"
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${PLANS_TRAVERSAL}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "2" "traversal: a .. segment inside an otherwise-valid .plans/ tail is still rejected (got ${CODE}: ${OUT})"
[[ ! -f "${ROOT}/.plans/.refine-100.checkpoint.json" ]] || fail "traversal: the resolved .plans/ target must not have been written"
rm -rf "${ROOT}"

# `clear` validates --checkpoint through the same helper -- a traversal
# attempt there must not `rm -f` an out-of-bounds path either.
make_env
CLEAR_TRAVERSAL_TARGET="${TMP_ROOT}../../../etc/passwd-ensure-issue-clear-traversal-test-$$"
run_ensure_issue clear --checkpoint "${CLEAR_TRAVERSAL_TARGET}"
assert_eq "${CODE}" "2" "traversal: clear also rejects a --checkpoint containing .. (got ${CODE}: ${OUT})"
rm -rf "${ROOT}"

# =====================================================================
# The .plans/ branch is anchored to the invocation's own cwd -- the original
# vulnerability was two-fold: (a) ..-based traversal, tested above, and (b)
# an unanchored .plans/ branch that accepted ANY directory named .plans/
# anywhere on the filesystem, not just the one under the invocation's own
# cwd. A --checkpoint path pointing at a .plans/ directory elsewhere is
# syntactically valid (no ".." anywhere) but must still be rejected.
# =====================================================================
make_env
OTHER_DIR="$(mktemp -d)"
mkdir -p "${OTHER_DIR}/.plans"
UNANCHORED_TARGET="${OTHER_DIR}/.plans/.refine-100.checkpoint.json"
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${UNANCHORED_TARGET}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "2" "cwd-anchor: a .plans/ directory outside the invocation's own cwd is rejected (got ${CODE}: ${OUT})"
[[ ! -f "${UNANCHORED_TARGET}" ]] || fail "cwd-anchor: init must not have written to a .plans/ directory outside its own cwd"
rm -rf "${ROOT}" "${OTHER_DIR}"

# A correctly cwd-anchored .plans/ location with the WRONG basename must
# also be rejected -- only .refine-<digits>.checkpoint.json is a valid
# checkpoint filename.
make_env
WRONG_BASENAME_1="${ROOT}/.plans/settings.json"
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${WRONG_BASENAME_1}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "2" "cwd-anchor: a .plans/ path with a non-checkpoint basename is rejected (got ${CODE}: ${OUT})"
[[ ! -f "${WRONG_BASENAME_1}" ]] || fail "cwd-anchor: init must not have written to .plans/settings.json"
rm -rf "${ROOT}"

make_env
WRONG_BASENAME_2="${ROOT}/.plans/.refine-100.txt"
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${WRONG_BASENAME_2}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "2" "cwd-anchor: a .plans/ path with the wrong extension is rejected (got ${CODE}: ${OUT})"
[[ ! -f "${WRONG_BASENAME_2}" ]] || fail "cwd-anchor: init must not have written to .plans/.refine-100.txt"
rm -rf "${ROOT}"

# =====================================================================
# Regression (#876 review follow-up, HIGH): canonicalize_path's
# not-yet-existing-ancestor walk only terminates on "" (an absolute-path
# property). A RELATIVE --checkpoint whose .plans/ segment does not yet
# exist previously spun forever, because "${ancestor%/*}" is a no-op once
# the walk reaches a slash-free first segment ("`.plans`") -- every real
# caller (refine/SKILL.md, refine/codex.md) passes exactly this shape
# (`.plans/.refine-<n>.checkpoint.json`, relative to its own cwd), and
# `.plans/` is gitignored so it does not exist on a fresh checkout. This
# case deliberately does NOT use make_env (which pre-creates
# "${ROOT}/.plans", masking the bug) and passes a relative --checkpoint,
# matching real-caller shape exactly. Wrapped in `timeout` so a regression
# here fails loudly instead of wedging the suite indefinitely.
# =====================================================================
ROOT="$(mktemp -d)"
mkdir -p "${ROOT}/bin" "${ROOT}/ctl" "${ROOT}/inputs"
write_fixture_gh "${ROOT}/bin"
printf '{"nextNumber": 500, "issues": []}' > "${ROOT}/store.json"
export GH_FIXTURE_STORE="${ROOT}/store.json"
export GH_FIXTURE_CTL_DIR="${ROOT}/ctl"
export GH_FIXTURE_LOGIN="cenci-bot"
[[ ! -d "${ROOT}/.plans" ]] || fail "relative-checkpoint: fixture must not pre-create .plans/ (that would mask the bug)"
write_text "${ROOT}/inputs/title.txt" "Add API validation (1/1)"
write_text "${ROOT}/inputs/body.md" "Body."
write_manifest "${ROOT}/manifest.json" '[
  {"slot":"child-1-of-1","titleFile":"'"${ROOT}"'/inputs/title.txt","bodyFile":"'"${ROOT}"'/inputs/body.md","labels":["Refined"],"milestone":null}
]'
OUT="$(cd "${ROOT}" && PATH="${ROOT}/bin:${PATH}" timeout 10 bash "${SCRIPT}" init --repo acme/widgets --parent 100 --checkpoint ".plans/.refine-100.checkpoint.json" --manifest "${ROOT}/manifest.json" 2>&1)"
CODE=$?
assert_ne "${CODE}" "124" "relative-checkpoint: init must not hang (timeout hit) on a relative --checkpoint into a not-yet-existing .plans/ (got: ${OUT})"
assert_eq "${CODE}" "0" "relative-checkpoint: init succeeds against a relative --checkpoint into a not-yet-existing .plans/ (got ${CODE}: ${OUT})"
[[ -f "${ROOT}/.plans/.refine-100.checkpoint.json" ]] || fail "relative-checkpoint: init did not write the checkpoint at ${ROOT}/.plans/.refine-100.checkpoint.json"
rm -rf "${ROOT}"

# =====================================================================
# Portability: checkpoint-path validation must not hard-depend on GNU
# realpath's -m extension. BSD/macOS realpath rejects -m outright, which
# would previously break every --checkpoint-taking subcommand
# unconditionally on those hosts. A stub `realpath` on PATH errors out ONLY
# when passed -m (mirroring real BSD/macOS realpath's rejection of that
# GNU-only flag) and otherwise delegates to the real system realpath
# (captured via `command -v realpath` before PATH is overridden) --
# mirroring flow/hooks/scripts/run-gate.test.sh's Case 13/14 fixture-binary
# pattern and flow/tests/maintain.test.sh's case41c.
# =====================================================================
make_env
write_text "${ROOT}/inputs/title.txt" "Add API validation (1/1)"
write_text "${ROOT}/inputs/body.md" "Body."
write_manifest "${ROOT}/manifest.json" '[
  {"slot":"child-1-of-1","titleFile":"'"${ROOT}"'/inputs/title.txt","bodyFile":"'"${ROOT}"'/inputs/body.md","labels":["Refined"],"milestone":null}
]'
REAL_REALPATH="$(command -v realpath)"
cat > "${ROOT}/bin/realpath" <<EOF
#!/usr/bin/env bash
for arg in "\$@"; do
  case "\${arg}" in
    -m) echo "fixture realpath: -m is a GNU-only extension, rejected (mirroring BSD/macOS realpath)" >&2; exit 1 ;;
  esac
done
exec "${REAL_REALPATH}" "\$@"
EOF
chmod +x "${ROOT}/bin/realpath"
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${CHECKPOINT}" --manifest "${ROOT}/manifest.json"
assert_eq "${CODE}" "0" "portability: init succeeds against a realpath stub that rejects GNU -m (got ${CODE}: ${OUT})"
[[ -f "${CHECKPOINT}" ]] || fail "portability: init did not write the checkpoint when realpath rejects -m"
run_ensure_issue clear --checkpoint "${CHECKPOINT}"
assert_eq "${CODE}" "0" "portability: clear also succeeds against a realpath stub that rejects GNU -m (got ${CODE}: ${OUT})"
[[ ! -f "${CHECKPOINT}" ]] || fail "portability: clear did not remove the checkpoint when realpath rejects -m"
rm -rf "${ROOT}"

# =====================================================================
# init: --parent-meta outside ${TMPDIR:-/tmp}/cenci/ is never deleted --
# only a validated in-scope path is removed after being consumed (#876
# review finding 2: an unvalidated --parent-meta path let a single init
# call delete an arbitrary JSON-object file on disk).
# =====================================================================
make_env
write_text "${ROOT}/inputs/title.txt" "Add API validation (1/1)"
write_text "${ROOT}/inputs/body.md" "Body."
write_manifest "${ROOT}/manifest.json" '[
  {"slot":"child-1-of-1","titleFile":"'"${ROOT}"'/inputs/title.txt","bodyFile":"'"${ROOT}"'/inputs/body.md","labels":["Refined"],"milestone":null}
]'
write_text "${ROOT}/outside-parent-meta.json" '{"labels":[],"milestone":null}'
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${CHECKPOINT}" --manifest "${ROOT}/manifest.json" --parent-meta "${ROOT}/outside-parent-meta.json"
assert_eq "${CODE}" "0" "parent-meta-out-of-scope: init still succeeds even though --parent-meta is outside \${TMPDIR:-/tmp}/cenci/ (got: ${OUT})"
[[ -f "${ROOT}/outside-parent-meta.json" ]] || fail "parent-meta-out-of-scope: init must NOT delete a --parent-meta file outside \${TMPDIR:-/tmp}/cenci/"
rm -rf "${ROOT}"

# In-scope regression: a --parent-meta path actually under
# ${TMPDIR:-/tmp}/cenci/ (matching every real caller) is still consumed
# and removed exactly as before this fix.
make_env
write_text "${ROOT}/inputs/title.txt" "Add API validation (1/1)"
write_text "${ROOT}/inputs/body.md" "Body."
write_manifest "${ROOT}/manifest.json" '[
  {"slot":"child-1-of-1","titleFile":"'"${ROOT}"'/inputs/title.txt","bodyFile":"'"${ROOT}"'/inputs/body.md","labels":["Refined"],"milestone":null}
]'
IN_SCOPE_TMP_ROOT="${TMPDIR:-/tmp}/cenci"
mkdir -p "${IN_SCOPE_TMP_ROOT}"
IN_SCOPE_PARENT_META="${IN_SCOPE_TMP_ROOT}/ensure-issue-test-parent-meta-$$.json"
write_text "${IN_SCOPE_PARENT_META}" '{"labels":[],"milestone":null}'
run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${CHECKPOINT}" --manifest "${ROOT}/manifest.json" --parent-meta "${IN_SCOPE_PARENT_META}"
assert_eq "${CODE}" "0" "parent-meta-in-scope: init succeeds with a --parent-meta path under \${TMPDIR:-/tmp}/cenci/ (got: ${OUT})"
[[ ! -f "${IN_SCOPE_PARENT_META}" ]] || fail "parent-meta-in-scope: init must delete a --parent-meta path under \${TMPDIR:-/tmp}/cenci/ after consuming it"
rm -f "${IN_SCOPE_PARENT_META}"
rm -rf "${ROOT}"

# =====================================================================
# Shared single-entry fixture for `ensure`/`link`/`clear` cases below.
# =====================================================================
setup_single_entry_env() {
  make_env
  write_text "${ROOT}/inputs/title.txt" "Add API validation (1/1)"
  write_text "${ROOT}/inputs/body.md" "Related to #100

The canonical child body."
  write_manifest "${ROOT}/manifest.json" '[
    {"slot":"child-1-of-1","titleFile":"'"${ROOT}"'/inputs/title.txt","bodyFile":"'"${ROOT}"'/inputs/body.md","labels":["Refined"],"milestone":null}
  ]'
  run_ensure_issue init --repo acme/widgets --parent 100 --checkpoint "${CHECKPOINT}" --manifest "${ROOT}/manifest.json"
  [[ "${CODE}" -eq 0 ]] || fail "setup_single_entry_env: init did not exit 0 (got ${CODE}: ${OUT})"
  ENTRY_NONCE="$(checkpoint_field '.entries[0].nonce')"
}

# =====================================================================
# AC-7 (a): POST accepted then client timeout is recovered without a
# duplicate.
# =====================================================================
setup_single_entry_env
touch "${ROOT}/ctl/simulate-post-timeout"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_ne "${CODE}" "0" "AC7a: first ensure attempt (simulated timeout) reports non-zero (got: ${OUT})"
assert_eq "$(store_issue_count)" "1" "AC7a: the fixture server DID accept the create despite the client-side timeout"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "null" "AC7a: checkpoint must NOT record an issueNumber after only the failed attempt"
rm -f "${ROOT}/ctl/simulate-post-timeout"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "AC7a: retry after the timeout recovers cleanly (got: ${OUT})"
assert_eq "$(store_issue_count)" "1" "AC7a: retry must NOT create a duplicate issue"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "500" "AC7a: retry recovers the existing issue number via marker scan"
assert_eq "$(checkpoint_field '.entries[0].verification')" "verified" "AC7a: retry marks the entry verified"
rm -rf "${ROOT}"

# =====================================================================
# AC-7 (b): malformed/non-JSON POST response is recovered by marker, no
# duplicate.
# =====================================================================
setup_single_entry_env
touch "${ROOT}/ctl/simulate-malformed-response"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_ne "${CODE}" "0" "AC7b: first ensure attempt (malformed response) reports non-zero (got: ${OUT})"
assert_eq "$(store_issue_count)" "1" "AC7b: the fixture server DID create the issue despite the malformed response body"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "null" "AC7b: checkpoint must NOT record an issueNumber from an unverified numeric-ID response"
rm -f "${ROOT}/ctl/simulate-malformed-response"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "AC7b: retry after the malformed response recovers cleanly (got: ${OUT})"
assert_eq "$(store_issue_count)" "1" "AC7b: retry must NOT create a duplicate issue"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "500" "AC7b: retry recovers the existing issue number via marker scan"
rm -rf "${ROOT}"

# =====================================================================
# AC-7 (c): a known issue with the wrong title/body/labels/milestone is
# exactly repaired and reverified, never recreated. Body is included here
# (#876 review finding 6) -- previously only title/labels/milestone were
# mutated, so a body-repair regression was untested.
# =====================================================================
setup_single_entry_env
MARKER="<!-- cenci-refine-create:${ENTRY_NONCE} -->"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "AC7c: initial create exits 0 (got: ${OUT})"
CREATED_NUM="$(checkpoint_field '.entries[0].issueNumber')"
ORIGINAL_MARKER_BODY="$(store_issue_field "${CREATED_NUM}" '.body')"
# Mutate the issue server-side, as if it drifted after creation (wrong
# title/body/labels/milestone -- the marker itself is kept (in a wrong
# body) so the candidate scan can still find and repair it; a body with no
# marker at all would instead hit the "legacy issue" no-match path and
# mask this test's real intent).
jq --argjson n "${CREATED_NUM}" --arg marker "${MARKER}" \
  '(.issues[] | select(.number == $n) | .title) = "WRONG TITLE"
   | (.issues[] | select(.number == $n) | .body) = ("WRONG BODY, but the marker is still present.\n\n" + $marker)
   | (.issues[] | select(.number == $n) | .labels) = ["SomeOtherLabel"]
   | (.issues[] | select(.number == $n) | .milestone) = 99' \
  "${GH_FIXTURE_STORE}" > "${GH_FIXTURE_STORE}.tmp" && mv "${GH_FIXTURE_STORE}.tmp" "${GH_FIXTURE_STORE}"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "AC7c: repair-and-reverify exits 0 (got: ${OUT})"
assert_eq "$(store_issue_count)" "1" "AC7c: repair must not create a second issue"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "${CREATED_NUM}" "AC7c: repair reuses the same issue number"
assert_eq "$(checkpoint_field '.entries[0].verification')" "verified" "AC7c: repair reverifies to verified"
assert_eq "$(store_issue_field "${CREATED_NUM}" '.title')" '"Add API validation (1/1)"' "AC7c: repaired title matches the manifest exactly"
assert_eq "$(store_issue_field "${CREATED_NUM}" '.body')" "${ORIGINAL_MARKER_BODY}" "AC7c: repaired body matches the originally-created marker-bearing body exactly"
assert_eq "$(store_issue_field "${CREATED_NUM}" '.labels')" '["Refined"]' "AC7c: repaired labels match the manifest exactly"
assert_eq "$(store_issue_field "${CREATED_NUM}" '.milestone')" "null" "AC7c: repaired milestone matches the manifest exactly (null, not 99)"
rm -rf "${ROOT}"

# =====================================================================
# AC-7 (c-2): the repair PATCH "succeeds" (exit 0) but silently drops the
# body update -- the post-repair reverify must catch this and fail closed
# (exit 13, verification recorded as "mismatch"), never report "verified"
# with a still-wrong body (#876 review finding 6: the reverify previously
# only re-checked title/labels/milestone, never body).
# =====================================================================
setup_single_entry_env
MARKER="<!-- cenci-refine-create:${ENTRY_NONCE} -->"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "AC7c-2: initial create exits 0 (got: ${OUT})"
CREATED_NUM="$(checkpoint_field '.entries[0].issueNumber')"
# Marker kept (in a wrong body) so the candidate scan finds and repairs
# this issue rather than treating it as a legacy no-marker issue.
jq --argjson n "${CREATED_NUM}" --arg marker "${MARKER}" \
  '(.issues[] | select(.number == $n) | .title) = "WRONG TITLE"
   | (.issues[] | select(.number == $n) | .body) = ("WRONG BODY, but the marker is still present.\n\n" + $marker)' \
  "${GH_FIXTURE_STORE}" > "${GH_FIXTURE_STORE}.tmp" && mv "${GH_FIXTURE_STORE}.tmp" "${GH_FIXTURE_STORE}"
touch "${ROOT}/ctl/simulate-patch-body-noop-${CREATED_NUM}"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "13" "AC7c-2: a repair PATCH that silently drops the body must fail closed, not report verified (got ${CODE}: ${OUT})"
assert_eq "$(checkpoint_field '.entries[0].verification')" "mismatch" "AC7c-2: checkpoint records the mismatch verification state when the body reverify fails"
rm -rf "${ROOT}"

# =====================================================================
# AC-7 (d): two marker matches fail closed (exit 10), no checkpoint write.
# =====================================================================
setup_single_entry_env
MARKER="<!-- cenci-refine-create:${ENTRY_NONCE} -->"
seed_issue 700 "Add API validation (1/1)" "The canonical child body.

${MARKER}" '["Refined"]' 'null' "cenci-bot"
seed_issue 701 "Add API validation (1/1)" "The canonical child body.

${MARKER}" '["Refined"]' 'null' "cenci-bot"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "10" "AC7d: two marker matches exits 10 (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "2" "AC7d: fail-closed must not create a third issue"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "null" "AC7d: fail-closed must not write an issueNumber into the checkpoint"
[[ "${OUT}" == *"700"* && "${OUT}" == *"701"* ]] || fail "AC7d: the partial-state report must name both candidate issue numbers (got: ${OUT})"
rm -rf "${ROOT}"

# =====================================================================
# AC-7 (e): --parent link fails after a successful create -- exit 14, no
# second child created on retry.
# =====================================================================
setup_single_entry_env
# Seed the parent issue #100 itself, so the fixture's `gh issue edit
# --parent` mutation (which only updates an EXISTING store entry matching
# `.number == $p`, never auto-vivifies one) has a real parent-side record
# to attach subIssues to -- without this, the final assertion below can
# never observe a populated subIssues list no matter what `link` does.
# `store_issue_count` below therefore counts 2 (this seeded parent plus
# the one created child), not 1.
seed_issue 100 "Parent" "The parent ticket." '[]' 'null' "cenci-bot"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "AC7e: create exits 0 (got: ${OUT})"
CREATED_NUM="$(checkpoint_field '.entries[0].issueNumber')"
touch "${ROOT}/ctl/fail-link"
run_ensure_issue link --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --parent 100
assert_eq "${CODE}" "14" "AC7e: link failure exits 14 (got ${CODE}: ${OUT})"
assert_eq "$(checkpoint_field '.entries[0].link')" "unlinked" "AC7e: checkpoint link state stays unlinked after a failed link"
# Retry `ensure` after the link failure must not create a second child.
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "AC7e: retrying ensure after a link failure still succeeds (got: ${OUT})"
assert_eq "$(store_issue_count)" "2" "AC7e: retrying ensure after a link failure must not create a second child (the seeded parent #100 plus the one child)"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "${CREATED_NUM}" "AC7e: retrying ensure reuses the same issue number, never a new one"
rm -f "${ROOT}/ctl/fail-link"
run_ensure_issue link --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --parent 100
assert_eq "${CODE}" "0" "AC7e: link succeeds once the transient failure clears (got: ${OUT})"
assert_eq "$(checkpoint_field '.entries[0].link')" "verified" "AC7e: successful link is recorded as verified"
assert_eq "$(store_issue_field 100 '.subIssues')" "[${CREATED_NUM}]" "AC7e: parent-side subIssues list contains exactly the one child"
rm -rf "${ROOT}"

# =====================================================================
# link: an empty (but successfully-fetched, exit-0) parent re-verify
# response is now treated as unverified and fails closed (exit 14) rather
# than being accepted as verified (#876 review finding 4). Deliberately do
# NOT seed parent #100 in the store -- the fixture's `gh issue view` still
# exits 0, but its filtered RESULT is empty since no issue in the store
# matches number 100, exactly modeling the ambiguous-empty-response case.
# =====================================================================
setup_single_entry_env
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "empty-reverify: create exits 0 (got: ${OUT})"
run_ensure_issue link --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --parent 100
assert_eq "${CODE}" "14" "empty-reverify: an empty parent-side subIssues re-fetch must fail closed, not be accepted as verified (got ${CODE}: ${OUT})"
assert_eq "$(checkpoint_field '.entries[0].link')" "unlinked" "empty-reverify: checkpoint link state stays unlinked when re-verify cannot confirm the link"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: zero-match create (the common path, exercised standalone).
# =====================================================================
setup_single_entry_env
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "zero-match: create exits 0 (got: ${OUT})"
assert_eq "$(store_issue_count)" "1" "zero-match: exactly one issue created"
CREATED_NUM="$(checkpoint_field '.entries[0].issueNumber')"
[[ "${CREATED_NUM}" =~ ^[0-9]+$ ]] || fail "zero-match: checkpoint issueNumber must be numeric (got: ${CREATED_NUM})"
BODY_STORED="$(store_issue_field "${CREATED_NUM}" '.body')"
[[ "${BODY_STORED}" == *"cenci-refine-create:${ENTRY_NONCE}"* ]] || fail "zero-match: created issue body must carry the exact marker <!-- cenci-refine-create:${ENTRY_NONCE} --> (got: ${BODY_STORED})"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: a legacy issue with no marker at all is NOT adopted -- a fresh
# issue is created alongside it.
# =====================================================================
setup_single_entry_env
seed_issue 800 "Add API validation (1/1)" "The canonical child body. No marker here at all." '["Refined"]' 'null' "cenci-bot"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "legacy-unmarked: create still succeeds despite the pre-existing unmarked issue (got: ${OUT})"
assert_eq "$(store_issue_count)" "2" "legacy-unmarked: the legacy issue is untouched and a new issue is created alongside it"
NEW_NUM="$(checkpoint_field '.entries[0].issueNumber')"
[[ "${NEW_NUM}" != "800" ]] || fail "legacy-unmarked: checkpoint adopted the unmarked legacy issue #800 -- it must mint a fresh issue instead"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: crash between POST and number persistence recovers via the
# marker (checkpoint hand-corrupted back to issueNumber=null after a
# genuine prior create, simulating "the process died after POST returned
# but before the checkpoint write landed").
# =====================================================================
setup_single_entry_env
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "crash-recovery: initial create exits 0 (got: ${OUT})"
CREATED_NUM="$(checkpoint_field '.entries[0].issueNumber')"
jq '(.entries[0].issueNumber) = null | (.entries[0].verification) = "pending"' "${CHECKPOINT}" > "${CHECKPOINT}.tmp" && mv "${CHECKPOINT}.tmp" "${CHECKPOINT}"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "crash-recovery: re-run after the simulated crash exits 0 (got: ${OUT})"
assert_eq "$(store_issue_count)" "1" "crash-recovery: no duplicate issue is created"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "${CREATED_NUM}" "crash-recovery: the same issue number is recovered via marker scan"
rm -rf "${ROOT}"

# =====================================================================
# AC1 (#913): real process kill mid-`ensure`, between the create POST
# landing server-side and persist_entry_result writing the checkpoint
# (ensure-issue.sh:630-663) -- a genuine crash/restart-fidelity proof
# against the real on-disk checkpoint, not a simulated failure mode (the
# simulated variant is AC-7(a) above; #913 Alternatives Considered rejected
# reusing simulate-post-timeout for exactly this reason). SIGKILL is gated
# strictly on the `crash-after-post` control marker, fired from inside the
# fixture gh via run_ensure_issue_crashing's deterministic
# $BASHPID-before-exec PID handoff (no race, no sleep).
# =====================================================================

# --- non-vacuity (#913 Test Strategy item 3): without the marker, the
# crashing harness itself must not report a wait status of 137 -- a
# silently-not-firing kill must never be able to masquerade as a crash
# test. ---
setup_single_entry_env
run_ensure_issue_crashing ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_ne "${CODE}" "137" "crash-harness-non-vacuity: without crash-after-post, run_ensure_issue_crashing must not report wait status 137 (got ${CODE}: ${OUT})"
assert_eq "${CODE}" "0" "crash-harness-non-vacuity: without crash-after-post, the crashing harness completes the create normally (got ${CODE}: ${OUT})"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "500" "crash-harness-non-vacuity: the un-crashed run still records the created issue number"
rm -rf "${ROOT}"

# --- Scenario A: crash after POST acceptance, re-entering `ensure`
# recovers exactly one child via marker scan, with no second POST. ---
setup_single_entry_env
touch "${ROOT}/ctl/crash-after-post"
run_ensure_issue_crashing ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "137" "crash-A: the crashed run reports wait status 137 (SIGKILL) (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "1" "crash-A: the create landed server-side despite the kill"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "null" "crash-A: the checkpoint must still show issueNumber == null immediately post-crash -- proves the kill landed before persist_entry_result, not after (a no-op kill would let this pass)"
assert_eq "$(post_call_count)" "1" "crash-A: exactly one POST landed before the crash"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "crash-A: re-entering ensure after the crash recovers cleanly (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "1" "crash-A: recovery must not create a duplicate issue"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "500" "crash-A: recovery finds the crashed create via marker scan"
assert_eq "$(checkpoint_field '.entries[0].verification')" "verified" "crash-A: recovery marks the entry verified"
assert_eq "$(post_call_count)" "1" "crash-A: recovery must not issue a second POST"
rm -rf "${ROOT}"

# --- Scenario B: same crash, then a second forged marker-bearing candidate
# is discovered on re-entry -- fail closed (exit 10), no further POST. ---
setup_single_entry_env
MARKER="<!-- cenci-refine-create:${ENTRY_NONCE} -->"
touch "${ROOT}/ctl/crash-after-post"
run_ensure_issue_crashing ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "137" "crash-B: the crashed run reports wait status 137 (SIGKILL) (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "1" "crash-B: the create landed server-side despite the kill"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "null" "crash-B: the checkpoint must still show issueNumber == null immediately post-crash"
assert_eq "$(post_call_count)" "1" "crash-B: exactly one POST landed before the crash"
seed_issue 900 "Add API validation (1/1)" "The canonical child body.

${MARKER}" '["Refined"]' 'null' "cenci-bot"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "10" "crash-B: two marker matches after re-entry exits 10 (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "2" "crash-B: the crash-created issue plus the seeded forged candidate, no third"
assert_eq "$(post_call_count)" "1" "crash-B: no second POST is attempted once two candidates are found"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "null" "crash-B: fail-closed must not write an issueNumber into the checkpoint"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: missing checkpoint -- exit 11, no POST at all.
# =====================================================================
make_env
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "11" "missing-checkpoint: exits 11 (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "0" "missing-checkpoint: no issue was created"
rm -rf "${ROOT}"

# --- corrupt checkpoint (unparseable JSON) also fails closed with exit 11 ---
make_env
write_text "${CHECKPOINT}" "{ not json"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "11" "corrupt-checkpoint: exits 11 (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "0" "corrupt-checkpoint: no issue was created"
rm -rf "${ROOT}"

# --- checkpoint with the wrong schemaVersion also fails closed with exit 11 ---
make_env
write_text "${CHECKPOINT}" '{"schemaVersion": 2, "repo": "acme/widgets", "parent": 100, "creatorLogin": "cenci-bot", "createdAt": "2026-01-01T00:00:00Z", "entries": []}'
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "11" "wrong-schema-version: exits 11 (got ${CODE}: ${OUT})"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: forged marker under another author is treated as no match --
# the fixture list endpoint deliberately does NOT filter by author server
# side, so this proves ensure-issue.sh performs its own author check
# against `gh api user --jq .login` rather than trusting the marker alone.
# =====================================================================
setup_single_entry_env
MARKER="<!-- cenci-refine-create:${ENTRY_NONCE} -->"
seed_issue 900 "Add API validation (1/1)" "The canonical child body.

${MARKER}" '["Refined"]' 'null' "someone-else"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "forged-marker: create still succeeds, treating the forged match as no match (got: ${OUT})"
assert_eq "$(store_issue_count)" "2" "forged-marker: the forged issue is untouched and a new, legitimately-authored issue is created"
NEW_NUM="$(checkpoint_field '.entries[0].issueNumber')"
[[ "${NEW_NUM}" != "900" ]] || fail "forged-marker: checkpoint adopted issue #900 authored by someone-else -- author verification did not run"
assert_eq "$(store_issue_field "${NEW_NUM}" '.login')" '"cenci-bot"' "forged-marker: the newly created issue is authored by the authenticated creator"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: candidate-list failure fails closed (exit 12), no POST.
# =====================================================================
setup_single_entry_env
touch "${ROOT}/ctl/fail-list"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "12" "candidate-list-failure: exits 12 (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "0" "candidate-list-failure: no issue was created when the candidate list itself could not be fetched"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: the candidate-list response comes back as two concatenated
# top-level JSON-array "pages" instead of one merged array -- untested-
# until-now shape a real `gh api ... --paginate` response is not known to
# produce, but defended against anyway (#876 review finding 3). Every
# downstream jq call computing match_count must fail closed (exit 12)
# rather than silently mis-evaluate both "-ge 2" and "-eq 1" as false and
# fall through to a duplicate create.
# =====================================================================
setup_single_entry_env
touch "${ROOT}/ctl/simulate-multi-page"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "12" "multi-page-candidate-list: a multi-document candidate-list response fails closed (got ${CODE}: ${OUT})"
assert_eq "$(store_issue_count)" "0" "multi-page-candidate-list: no issue was created when match_count could not be computed as a single integer"
rm -rf "${ROOT}"

# =====================================================================
# Edge case: unrepairable verification mismatch -- the repair PATCH itself
# fails, so the issue can never be reconciled to the manifest; exit 13.
# =====================================================================
setup_single_entry_env
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "0" "unrepairable: initial create exits 0 (got: ${OUT})"
CREATED_NUM="$(checkpoint_field '.entries[0].issueNumber')"
jq --argjson n "${CREATED_NUM}" '(.issues[] | select(.number == $n) | .title) = "WRONG TITLE"' \
  "${GH_FIXTURE_STORE}" > "${GH_FIXTURE_STORE}.tmp" && mv "${GH_FIXTURE_STORE}.tmp" "${GH_FIXTURE_STORE}"
touch "${ROOT}/ctl/fail-patch-${CREATED_NUM}"
run_ensure_issue ensure --checkpoint "${CHECKPOINT}" --repo acme/widgets --slot child-1-of-1 --title "${ROOT}/inputs/title.txt" --body "${ROOT}/inputs/body.md"
assert_eq "${CODE}" "13" "unrepairable: exits 13 when the repair PATCH itself fails (got ${CODE}: ${OUT})"
assert_eq "$(checkpoint_field '.entries[0].verification')" "mismatch" "unrepairable: checkpoint records the mismatch verification state"
assert_eq "$(checkpoint_field '.entries[0].issueNumber')" "${CREATED_NUM}" "unrepairable: the known issue number is still recorded so a human can reconcile manually"
rm -rf "${ROOT}"

# =====================================================================
# clear -- idempotent checkpoint removal.
# =====================================================================
setup_single_entry_env
run_ensure_issue clear --checkpoint "${CHECKPOINT}"
assert_eq "${CODE}" "0" "clear: removes an existing checkpoint, exits 0 (got: ${OUT})"
[[ ! -f "${CHECKPOINT}" ]] || fail "clear: checkpoint file must be gone after clear"
run_ensure_issue clear --checkpoint "${CHECKPOINT}"
assert_eq "${CODE}" "0" "clear: re-running against an already-absent checkpoint is still a no-op success (got: ${OUT})"
rm -rf "${ROOT}"

echo "ensure-issue.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
