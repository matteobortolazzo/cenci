#!/usr/bin/env bash
# Behavioral suite for skills/implement/scripts/docs-policy.sh -- the read-only
# resolver for the `docs.policy` config key (#1301). It prints `minimal` or
# `default` on stdout and exits 0, or exits 2 with exactly one stderr line when
# the config cannot be trusted. Builds a throwaway repo directory per scenario;
# no git, no gh, no network.
#
# Scenarios:
#   P1  one named case per docs.policy value: minimal; docs absent; docs.policy
#       absent; "Minimal"; "none"; null; 42 (plus "default" and non-object docs)
#   P15 malformed JSON, several top-level documents, non-object top level,
#       unreadable config (directory, dangling symlink, a regular file or
#       dangling symlink where the .cenci / .claude directory belongs -- never
#       chmod), missing jq, bad argument -> exit 2, exactly one stderr line
#       carrying a substring that names its error class
#   P16 read-only fallback to .claude/config.json only when .cenci/config.json
#       is missing; the legacy file is left byte-unchanged
#   P17 an unrecognised value warns exactly once, names docs.policy, never
#       echoes the value, and never contains the bare token `minimal`
#
# Every fixture helper checks its own filesystem writes and reports a failure
# through fail(), so an expected-`default` case can never pass because its
# fixture was silently never written.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "docs-policy.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "docs-policy.test.sh: failed to resolve flow directory." >&2; exit 2; }
SCRIPT="${FLOW_DIR}/skills/implement/scripts/docs-policy.sh"
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_not_contains() { [[ -n "$2" ]] || { fail "$3: empty needle"; return; }; [[ "$1" != *"$2"* ]] || fail "$3: expected NOT to contain [$2], got [$1]"; }
assert_contains() { [[ -n "$2" ]] || { fail "$3: empty needle"; return; }; [[ "$1" == *"$2"* ]] || fail "$3: expected to contain [$2], got [$1]"; }

[[ -r "${SCRIPT}" ]] || { fail "script missing: ${SCRIPT}"; echo "docs-policy.test.sh: failures=${failures}"; exit 1; }
T="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 2; }
[[ -n "${T}" && -d "${T}" ]] || { echo "mktemp -d returned no directory" >&2; exit 2; }
trap 'rm -rf "${T}"' EXIT

# Distinguishing substrings, one per error class the resolver reports.
ERR_PARSE='failed to parse'
ERR_NOT_OBJECT='must contain a JSON object'
ERR_UNREADABLE='cannot read config'
ERR_NOT_DIR='exists but is not a directory'
ERR_USAGE='usage:'
ERR_ROOT='repo root is not a directory'
ERR_NO_JQ='jq is required'
WARN_UNKNOWN='has an unrecognized value; using the default policy'

# run_policy [args...] -- runs the resolver; sets OUT, ERR, ERR_LINES, RC.
run_policy() {
  OUT="$(bash "${SCRIPT}" "$@" 2>"${T}/err")"; RC=$?
  if ! ERR="$(cat "${T}/err")"; then
    fail "run_policy: could not read captured stderr"
    ERR=""
  fi
  if ! ERR_LINES="$(wc -l < "${T}/err" | tr -d '[:space:]')"; then
    fail "run_policy: could not count captured stderr lines"
    ERR_LINES="ERROR"
  fi
}

# new_repo <name> -- an empty repo root with a .cenci directory.
new_repo() { mkdir -p "${T}/$1/.cenci" || { fail "$1: could not create fixture repo"; return 1; }; }
# write_cenci <name> <json> -- write .cenci/config.json verbatim.
write_cenci() { printf '%s' "$2" > "${T}/$1/.cenci/config.json" || { fail "$1: could not write .cenci/config.json"; return 1; }; }
# cenci_repo <name> <json> -- new_repo + write_cenci.
cenci_repo() { new_repo "$1" && write_cenci "$1" "$2"; }
# write_legacy <name> <json> -- write .claude/config.json verbatim (creating .claude).
write_legacy() {
  mkdir -p "${T}/$1/.claude" || { fail "$1: could not create .claude"; return 1; }
  printf '%s' "$2" > "${T}/$1/.claude/config.json" || { fail "$1: could not write .claude/config.json"; return 1; }
}
# fixture_mkdir <path> / fixture_symlink <target> <link> / fixture_file <path> -- checked.
fixture_mkdir() { mkdir -p "$1" || { fail "could not create fixture directory $1"; return 1; }; }
fixture_symlink() { ln -s "$1" "$2" || { fail "could not create fixture symlink $2"; return 1; }; }
fixture_file() { printf 'not a directory\n' > "$1" || { fail "could not create fixture file $1"; return 1; }; }

# expect_clean <name> <expected stdout> -- exit 0, no stderr output at all.
expect_clean() {
  run_policy "${T}/$1"
  assert_eq "${RC}" "0" "$1 exit"
  assert_eq "${OUT}" "$2" "$1 stdout"
  [[ -z "${ERR}" ]] || fail "$1 no warning: expected empty stderr, got [${ERR}]"
  assert_eq "${ERR_LINES}" "0" "$1 no warning lines"
}
# expect_warned <name> -- exit 0, default, exactly one warning naming docs.policy.
expect_warned() {
  run_policy "${T}/$1"
  assert_eq "${RC}" "0" "$1 exit"
  assert_eq "${OUT}" "default" "$1 stdout"
  assert_eq "${ERR_LINES}" "1" "$1 exactly one warning line"
  assert_contains "${ERR}" "docs.policy" "$1 warning names the key"
  assert_contains "${ERR}" "${WARN_UNKNOWN}" "$1 warning wording"
  assert_not_contains "${ERR}" "minimal" "$1 warning never carries the bare token minimal"
}
# expect_error <name> <class-substring> -- exit 2, empty stdout, exactly one
# stderr line that names its error class.
expect_error() {
  run_policy "${T}/$1"
  assert_eq "${RC}" "2" "$1 exit"
  assert_eq "${OUT}" "" "$1 stdout"
  assert_eq "${ERR_LINES}" "1" "$1 exactly one stderr line"
  assert_contains "${ERR}" "$2" "$1 error class"
}
# expect_args_error <label> <class-substring> [args...] -- expect_error for a
# direct argument list rather than a fixture repo.
expect_args_error() {
  local label="$1" class="$2"
  shift 2
  run_policy "$@"
  assert_eq "${RC}" "2" "${label} exit"
  assert_eq "${OUT}" "" "${label} stdout"
  assert_eq "${ERR_LINES}" "1" "${label} exactly one stderr line"
  assert_contains "${ERR}" "${class}" "${label} error class"
}

# --- P1. one named case per docs.policy value ----------------------------------------
cenci_repo p1_minimal '{"docs":{"policy":"minimal"}}' && expect_clean p1_minimal minimal
cenci_repo p1_docs_absent '{"other":true}' && expect_clean p1_docs_absent default
cenci_repo p1_policy_absent '{"docs":{"other":1}}' && expect_clean p1_policy_absent default
cenci_repo p1_capital '{"docs":{"policy":"Minimal"}}' && expect_warned p1_capital
cenci_repo p1_none '{"docs":{"policy":"none"}}' && expect_warned p1_none
cenci_repo p1_null '{"docs":{"policy":null}}' && expect_warned p1_null
cenci_repo p1_number '{"docs":{"policy":42}}' && expect_warned p1_number
cenci_repo p1_default_str '{"docs":{"policy":"default"}}' && expect_warned p1_default_str
cenci_repo p1_docs_string '{"docs":"minimal"}' && expect_warned p1_docs_string
cenci_repo p1_docs_null '{"docs":null}' && expect_warned p1_docs_null

# --- P17. the warning never echoes the offending value -----------------------------
if cenci_repo p17_secret '{"docs":{"policy":"SEKRET-VALUE-9137"}}'; then
  expect_warned p17_secret
  assert_not_contains "${ERR}" "SEKRET-VALUE-9137" "p17 warning must not echo the value"
fi
if cenci_repo p17_number '{"docs":{"policy":424242}}'; then
  expect_warned p17_number
  assert_not_contains "${ERR}" "424242" "p17 warning must not echo a numeric value"
fi

# --- no config at all ---------------------------------------------------------------
fixture_mkdir "${T}/noconfig" && expect_clean noconfig default
# A .cenci directory with no config.json and no legacy file is still the default.
new_repo cenci_dir_only && expect_clean cenci_dir_only default

# --- P15. untrusted config -> exit 2, exactly one stderr line -----------------------
cenci_repo p15_malformed '{"docs": {"policy": "minimal"' && expect_error p15_malformed "${ERR_PARSE}"
cenci_repo p15_empty '' && expect_error p15_empty "${ERR_PARSE}"
# Several top-level JSON documents are malformed, even when one says minimal.
cenci_repo p15_multidoc '{} {"docs":{"policy":"minimal"}}' && expect_error p15_multidoc "${ERR_PARSE}"
cenci_repo p15_array '["docs"]' && expect_error p15_array "${ERR_NOT_OBJECT}"
cenci_repo p15_scalar '"minimal"' && expect_error p15_scalar "${ERR_NOT_OBJECT}"

# Unreadable via a directory at the config path (EISDIR) -- no chmod.
new_repo p15_dir && fixture_mkdir "${T}/p15_dir/.cenci/config.json" && expect_error p15_dir "${ERR_UNREADABLE}"

# Unreadable via a dangling symlink (the target does not exist).
new_repo p15_dangling && fixture_symlink "${T}/does-not-exist.json" "${T}/p15_dangling/.cenci/config.json" \
  && expect_error p15_dangling "${ERR_UNREADABLE}"

# An unreadable .cenci file must never fall through to a valid legacy file.
new_repo p15_nofall && fixture_symlink "${T}/does-not-exist.json" "${T}/p15_nofall/.cenci/config.json" \
  && write_legacy p15_nofall '{"docs":{"policy":"minimal"}}' && expect_error p15_nofall "${ERR_UNREADABLE}"

new_repo p15_nofall_dir && fixture_mkdir "${T}/p15_nofall_dir/.cenci/config.json" \
  && write_legacy p15_nofall_dir '{"docs":{"policy":"minimal"}}' && expect_error p15_nofall_dir "${ERR_UNREADABLE}"

# A malformed .cenci file must not fall through to a valid legacy file either.
cenci_repo p15_nofall_bad '{oops' && write_legacy p15_nofall_bad '{"docs":{"policy":"minimal"}}' \
  && expect_error p15_nofall_bad "${ERR_PARSE}"

# A regular file named .cenci (ENOTDIR on .cenci/config.json) is an error, never
# a silent fall-through to the legacy file or to `default`.
fixture_mkdir "${T}/p15_cenci_file" && fixture_file "${T}/p15_cenci_file/.cenci" \
  && expect_error p15_cenci_file "${ERR_NOT_DIR}"
fixture_mkdir "${T}/p15_cenci_file_legacy" && fixture_file "${T}/p15_cenci_file_legacy/.cenci" \
  && write_legacy p15_cenci_file_legacy '{"docs":{"policy":"minimal"}}' \
  && expect_error p15_cenci_file_legacy "${ERR_NOT_DIR}"
# A dangling symlink named .cenci is not a directory either.
fixture_mkdir "${T}/p15_cenci_dangling" && fixture_symlink "${T}/does-not-exist-dir" "${T}/p15_cenci_dangling/.cenci" \
  && expect_error p15_cenci_dangling "${ERR_NOT_DIR}"

# With no .cenci at all, a regular file named .claude (the fallback location)
# is an error too.
fixture_mkdir "${T}/p15_claude_file" && fixture_file "${T}/p15_claude_file/.claude" \
  && expect_error p15_claude_file "${ERR_NOT_DIR}"

# Missing jq -> exit 2 with one stderr line (PATH has no jq).
if fixture_mkdir "${T}/nojq-bin" && cenci_repo p15_nojq '{"docs":{"policy":"minimal"}}'; then
  BASH_BIN="$(command -v bash)" || { echo "docs-policy.test.sh: bash not on PATH." >&2; exit 2; }
  OUT="$(PATH="${T}/nojq-bin" "${BASH_BIN}" "${SCRIPT}" "${T}/p15_nojq" 2>"${T}/err")"; RC=$?
  ERR="$(cat "${T}/err")" || fail "p15 missing jq: could not read captured stderr"
  ERR_LINES="$(wc -l < "${T}/err" | tr -d '[:space:]')" || fail "p15 missing jq: could not count stderr lines"
  assert_eq "${RC}" "2" "p15 missing jq exit"
  assert_eq "${OUT}" "" "p15 missing jq stdout"
  assert_eq "${ERR_LINES}" "1" "p15 missing jq exactly one stderr line"
  assert_contains "${ERR}" "${ERR_NO_JQ}" "p15 missing jq error class"
fi

# Bad / missing argument.
expect_args_error "no argument" "${ERR_USAGE}"
expect_args_error "nonexistent repo root" "${ERR_ROOT}" "${T}/does-not-exist-root"
expect_args_error "extra argument" "${ERR_USAGE}" "${T}/p1_minimal" extra
expect_args_error "empty argument" "${ERR_USAGE}" ""

# --- P16. legacy fallback only when .cenci/config.json is missing -------------------
if fixture_mkdir "${T}/p16_legacy/.claude" \
  && printf '%s\n' '{"docs":{"policy":"minimal"},"keep":"me"}' > "${T}/p16_legacy/.claude/config.json"; then
  LEGACY_BEFORE="$(cksum < "${T}/p16_legacy/.claude/config.json")" || { echo "cksum failed" >&2; exit 2; }
  expect_clean p16_legacy minimal
  LEGACY_AFTER="$(cksum < "${T}/p16_legacy/.claude/config.json")" || { echo "cksum failed" >&2; exit 2; }
  assert_eq "${LEGACY_AFTER}" "${LEGACY_BEFORE}" "p16 legacy file byte-unchanged"
  [[ ! -e "${T}/p16_legacy/.cenci" ]] || fail "p16 resolver must not create .cenci"
else
  fail "p16_legacy: could not write fixture"
fi

# A .cenci directory without config.json still falls back to the legacy file.
new_repo p16_cenci_dir_legacy && write_legacy p16_cenci_dir_legacy '{"docs":{"policy":"minimal"}}' \
  && expect_clean p16_cenci_dir_legacy minimal

# Legacy file with a bad value warns once and names the key.
write_legacy p16_legacy_bad '{"docs":{"policy":"nope"}}' && expect_warned p16_legacy_bad

# Malformed legacy file -> exit 2 (it is the file in use).
write_legacy p16_legacy_malformed '{oops' && expect_error p16_legacy_malformed "${ERR_PARSE}"

# .cenci takes precedence over the legacy file, in both directions.
cenci_repo prec_cenci_default '{"other":1}' && write_legacy prec_cenci_default '{"docs":{"policy":"minimal"}}' \
  && expect_clean prec_cenci_default default
cenci_repo prec_cenci_minimal '{"docs":{"policy":"minimal"}}' && write_legacy prec_cenci_minimal '{"other":1}' \
  && expect_clean prec_cenci_minimal minimal

# --- read-only: the resolver never writes either config file -----------------------
if cenci_repo ro '{"docs":{"policy":"minimal"}}' && write_legacy ro '{"docs":{"policy":"Minimal"}}'; then
  CENCI_BEFORE="$(cksum < "${T}/ro/.cenci/config.json")" || { echo "cksum failed" >&2; exit 2; }
  LEGACY_RO_BEFORE="$(cksum < "${T}/ro/.claude/config.json")" || { echo "cksum failed" >&2; exit 2; }
  expect_clean ro minimal
  CENCI_AFTER="$(cksum < "${T}/ro/.cenci/config.json")" || { echo "cksum failed" >&2; exit 2; }
  LEGACY_RO_AFTER="$(cksum < "${T}/ro/.claude/config.json")" || { echo "cksum failed" >&2; exit 2; }
  assert_eq "${CENCI_AFTER}" "${CENCI_BEFORE}" "read-only .cenci file byte-unchanged"
  assert_eq "${LEGACY_RO_AFTER}" "${LEGACY_RO_BEFORE}" "read-only legacy file byte-unchanged"
fi

if [[ "${failures}" -eq 0 ]]; then
  echo "docs-policy.test.sh: failures=0"
  exit 0
fi
echo "docs-policy.test.sh: failures=${failures}"
exit 1
