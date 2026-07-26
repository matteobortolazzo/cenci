#!/usr/bin/env bash
# Repo-wide drift guard for ticket #642: flags permission-removing `chmod`
# calls in tracked *.test.sh files that lack a root-safe lever (an `id -u`/
# `EUID` guard within the next 10 lines, or a `# root-safe: <reason>` marker
# on one of the 3 preceding lines). uid 0 bypasses DAC permission bits, so a
# test that proves a script fails by removing permissions silently inverts
# under root. See flow/docs/shell-scripting-gotchas.md.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "root-safe-perms-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "root-safe-perms-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "root-safe-perms-contract.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

SELF_ABS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
SELF_REL="${SELF_ABS#"${REPO_ROOT}/"}"

# Permission-*removing* chmod: numeric modes whose owner digit lacks the write
# bit (0,1,4,5 — covers 000/400/444/500/555), with an optional leading 0 and an
# optional -R; or symbolic forms that remove r/w (a-w, u-w, -w, a-r, u-r,
# ug-rw, a-rwx). Additive/restorative modes (+x, 644, 700, 755, u+w) must not
# match. Verified against every tracked *.sh in the repo: 6 hits, all intended.
CHMOD_REMOVE_RE='(^|[^[:alnum:]_])chmod[[:space:]]+(-R[[:space:]]+)?(0?[0145][0-7][0-7]|[ugoa]*-[rwxXst]*[rw][rwxXst]*)([[:space:]]|$)'
ROOT_GUARD_RE='(id[[:space:]]+-u|EUID)'
ROOT_SAFE_MARKER_RE='#[[:space:]]*root-safe:[[:space:]]*[^[:space:]]'

# scan_file <abs-path> <display-path> — prints "<display>:<line>" per
# violation, or "<display>:UNREADABLE" (never both) if the file cannot be
# read — a silent empty result here would let a permission-mangled tracked
# test file slip through the scan as a false all-clear.
scan_file() {
  local abs="$1" display="$2"
  local -a lines=(); local line i j n cleared
  if [[ ! -r "${abs}" ]]; then
    printf '%s:UNREADABLE\n' "${display}"
    return
  fi
  while IFS= read -r line || [[ -n "${line}" ]]; do lines+=("${line}"); done < "${abs}"
  n=${#lines[@]}
  for ((i = 0; i < n; i++)); do
    [[ "${lines[i]}" =~ $CHMOD_REMOVE_RE ]] || continue
    cleared=0
    for ((j = i - 3; j < i; j++)); do            # marker on 3 preceding lines
      (( j >= 0 )) || continue
      [[ "${lines[j]}" =~ $ROOT_SAFE_MARKER_RE ]] && cleared=1
    done
    for ((j = i + 1; j <= i + 10 && j < n; j++)); do   # guard within next 10
      [[ "${lines[j]}" =~ $ROOT_GUARD_RE ]] && cleared=1
    done
    (( cleared )) || printf '%s:%d\n' "${display}" "$((i + 1))"
  done
}

# =====================================================================
# Fixture self-test — proves the detector itself before trusting it on the
# repo scan. Temp dir outside the repo so neither the flow gate's `find`
# nor this suite's own repo scan sees the fixtures.
# =====================================================================
FIXTURE_DIR="$(mktemp -d)" || { echo "root-safe-perms-contract.test.sh: failed to create fixture directory." >&2; exit 2; }
trap 'rm -rf "${FIXTURE_DIR}"' EXIT

assert_count() {
  local file="$1" expected="$2" label="$3"
  local -a violations=()
  local got_count
  mapfile -t violations < <(scan_file "${file}" "${label}")
  got_count=${#violations[@]}
  [[ "${got_count}" -eq "${expected}" ]] || fail "fixture ${label}: expected ${expected} violation(s), got ${got_count} (${violations[*]:-none})"
}

# unguarded.test.sh — chmod 000, no guard, no marker: exactly 1 violation
UNGUARDED="${FIXTURE_DIR}/unguarded.test.sh"
cat > "${UNGUARDED}" <<'EOF'
#!/usr/bin/env bash
f="$(mktemp)"
chmod 000 "$f"
run_something "$f"
rm -f "$f"
EOF
assert_count "${UNGUARDED}" 1 "unguarded"

# guarded.test.sh — chmod 000 then an id -u guard within 10 lines: 0
GUARDED="${FIXTURE_DIR}/guarded.test.sh"
cat > "${GUARDED}" <<'EOF'
#!/usr/bin/env bash
f="$(mktemp)"
chmod 000 "$f"
if [[ "$(id -u)" -eq 0 ]]; then
  echo "SKIP: requires a non-root user"
else
  run_something "$f"
fi
rm -f "$f"
EOF
assert_count "${GUARDED}" 0 "guarded"

# marker.test.sh — # root-safe: <reason> on the preceding line: 0
MARKER="${FIXTURE_DIR}/marker.test.sh"
cat > "${MARKER}" <<'EOF'
#!/usr/bin/env bash
f="$(mktemp)"
# root-safe: DAC-specific branch, root cannot reach it any other way
chmod 000 "$f"
run_something "$f"
rm -f "$f"
EOF
assert_count "${MARKER}" 0 "marker"

# additive.test.sh — only additive/restorative modes: 0
ADDITIVE="${FIXTURE_DIR}/additive.test.sh"
cat > "${ADDITIVE}" <<'EOF'
#!/usr/bin/env bash
f="$(mktemp)"
chmod +x "$f"
chmod 644 "$f"
chmod 700 "$f"
chmod 755 "$f"
chmod u+w "$f"
chmod -R 755 "$f"
chmod g+r "$f"
chmod 0644 "$f"
rm -f "$f"
EOF
assert_count "${ADDITIVE}" 0 "additive"

# modes.test.sh — one removing chmod per line, no guards: exactly 11
MODES="${FIXTURE_DIR}/modes.test.sh"
cat > "${MODES}" <<'EOF'
#!/usr/bin/env bash
f="$(mktemp)"
chmod 000 "$f"
chmod 400 "$f"
chmod 444 "$f"
chmod 500 "$f"
chmod 555 "$f"
chmod 0500 "$f"
chmod -R 500 "$f"
chmod a-w "$f"
chmod u-w "$f"
chmod -w "$f"
chmod a-r "$f"
chmod u-r "$f"
rm -f "$f"
EOF
# NOTE (#642 deviation): the plan's table declares "exactly 11" for this
# fixture's 6 numeric (000/400/444/500/555/0500) + 1 (-R 500) + 5 symbolic
# (a-w/u-w/-w/a-r/u-r) lines, but 6+1+5 = 12, and every one of those 12
# lines is independently a genuine permission-removing chmod per the AC's
# own list (000/400/444/500/555 explicitly named, 0500 is the same set with
# an optional leading 0 which the regex notes must still match, -R 500 is
# the -R form of 500, and all 5 named symbolic forms remove r or w). This is
# an arithmetic slip in the plan (6+1+5 summed wrong), not a design decision
# to reinterpret — the exact-count assertion here is corrected to the
# arithmetically consistent 12 matching this exact fixture content.
assert_count "${MODES}" 12 "modes"

# unreadable.test.sh — a regular file with no read permission (no chmod
# calls in its content), proving scan_file's readability check is load-
# bearing rather than a silent "0 violations" false-clear. DAC-specific like
# site 4: uid 0 bypasses read permission on any regular file, so this is
# guard + skip under root, not a root-proof lever.
if [[ "$(id -u)" -eq 0 ]]; then
  echo "SKIP: fixture 'unreadable' requires a non-root user (uid 0 can read any regular file)"
else
  UNREADABLE="${FIXTURE_DIR}/unreadable.test.sh"
  cat > "${UNREADABLE}" <<'EOF'
#!/usr/bin/env bash
echo "no chmod calls in this file"
EOF
  chmod 000 "${UNREADABLE}"
  assert_count "${UNREADABLE}" 1 "unreadable"
  chmod 644 "${UNREADABLE}"
fi

# window.test.sh — one chmod 000 with id -u exactly 10 lines later (cleared);
# a second chmod 000 with id -u 11 lines later (not cleared): exactly 1, at
# the second site's line.
WINDOW="${FIXTURE_DIR}/window.test.sh"
{
  echo '#!/usr/bin/env bash'
  # shellcheck disable=SC2016  # writing literal $f into the fixture file, not expanding here
  echo 'f="$(mktemp)"'
  # shellcheck disable=SC2016  # writing literal $f into the fixture file, not expanding here
  echo 'chmod 000 "$f"'          # line 3 — guard at line 13 (10 lines later)
  for _ in $(seq 1 9); do echo '# filler'; done
  # shellcheck disable=SC2016  # writing literal $(id -u) into the fixture file, not expanding here
  echo 'if [[ "$(id -u)" -eq 0 ]]; then'   # line 13
  echo 'echo "SKIP: within window"'
  echo 'fi'
  # shellcheck disable=SC2016  # writing literal $g into the fixture file, not expanding here
  echo 'g="$(mktemp)"'
  # shellcheck disable=SC2016  # writing literal $g into the fixture file, not expanding here
  echo 'chmod 000 "$g"'          # second chmod, guard 11 lines later (not cleared)
  for _ in $(seq 1 10); do echo '# filler'; done
  # shellcheck disable=SC2016  # writing literal $(id -u) into the fixture file, not expanding here
  echo 'if [[ "$(id -u)" -eq 0 ]]; then'   # 11 lines after second chmod
  echo 'echo "SKIP: outside window"'
  echo 'fi'
} > "${WINDOW}"
# shellcheck disable=SC2016  # matching the literal $g written into the fixture, not expanding here
SECOND_CHMOD_LINE="$(grep -n 'chmod 000 "\$g"' "${WINDOW}" | head -1 | cut -d: -f1)"
mapfile -t WINDOW_VIOLATIONS < <(scan_file "${WINDOW}" "window")
if [[ "${#WINDOW_VIOLATIONS[@]}" -eq 1 && "${WINDOW_VIOLATIONS[0]}" == "window:${SECOND_CHMOD_LINE}" ]]; then
  :
else
  fail "fixture window: expected exactly 1 violation at window:${SECOND_CHMOD_LINE}, got: ${WINDOW_VIOLATIONS[*]:-none}"
fi

# =====================================================================
# Repo-wide scan
# =====================================================================
declare -a FILES=()
if git -C "${REPO_ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  # Redirect into a real file (inside FIXTURE_DIR, already EXIT-trapped) so
  # git's own exit status is checked directly, rather than via a process
  # substitution subshell whose status is unavailable to the `if`/`while`
  # here — an unchecked partial failure could otherwise leave FILES
  # non-empty and silently skip the find fallback below.
  GIT_LS_OUT="${FIXTURE_DIR}/git-ls-files.out"
  if git -C "${REPO_ROOT}" ls-files -z -- '*.test.sh' > "${GIT_LS_OUT}"; then
    while IFS= read -r -d '' p; do
      FILES+=("${p}")
    done < "${GIT_LS_OUT}"
  fi
fi

if [[ "${#FILES[@]}" -eq 0 ]]; then
  while IFS= read -r -d '' p; do
    FILES+=("${p#"${REPO_ROOT}/"}")
  done < <(find "${REPO_ROOT}" -type f -name '*.test.sh' \
    -not -path '*/.worktrees/*' -not -path '*/.claude/worktrees/*' -not -path '*/node_modules/*' -print0)
fi

declare -a VIOLATIONS=()
scanned=0
for rel in "${FILES[@]}"; do
  [[ "${rel}" == "${SELF_REL}" ]] && continue
  abs="${REPO_ROOT}/${rel}"
  [[ -f "${abs}" ]] || continue
  scanned=$((scanned+1))
  while IFS= read -r v; do
    [[ -n "${v}" ]] && VIOLATIONS+=("${v}")
  done < <(scan_file "${abs}" "${rel}")
done

if [[ "${scanned}" -eq 0 ]]; then
  fail "repo scan found 0 *.test.sh files — enumeration is broken (vacuous pass guard)"
fi

for v in "${VIOLATIONS[@]}"; do
  if [[ "${v}" == *:UNREADABLE ]]; then
    fail "${v%:UNREADABLE}: file is not readable — cannot scan for root-safe chmod violations"
  else
    fail "${v}: permission-removing chmod without a root-safe lever — see flow/docs/shell-scripting-gotchas.md — use ENOTDIR/EISDIR/ELOOP, or add an id -u guard with a stdout SKIP, or a \"# root-safe: <reason>\" comment"
  fi
done

# =====================================================================
# Doc anchor guard — the rule this test enforces must be documented, and
# the doc's rule text must not silently drift out from under this test.
# =====================================================================
GOTCHAS_DOC="${FLOW_DIR}/docs/shell-scripting-gotchas.md"
if [[ ! -f "${GOTCHAS_DOC}" ]]; then
  fail "doc not found: ${GOTCHAS_DOC}"
elif ! grep -qF "chmod\`-based unwritability/unreadability is a no-op for root" "${GOTCHAS_DOC}"; then
  fail "doc missing anchor phrase: flow/docs/shell-scripting-gotchas.md must state that chmod-based unwritability/unreadability is a no-op for root"
fi

echo "root-safe-perms-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
