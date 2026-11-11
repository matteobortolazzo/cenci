#!/usr/bin/env bash
# Tests for migrate-project-core.sh (#1448, #1509): AGENTS.md is the only guidance file
# the script writes, and a CLAUDE.md is removed only when it is exactly one of the three
# inert adapter shapes (#1509). Cases M1-M38 each name the behaviour they prove; every
# case builds its own fixture under one mktemp root.
set -euo pipefail
FLOW_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || { echo "failed to resolve flow directory" >&2; exit 2; }
SCRIPT="${FLOW_DIR}/scripts/migrate-project-core.sh"
ADAPTER_TEMPLATE="${FLOW_DIR}/templates/claude-md-adapter.md"
FALLBACK_MARKER='<!-- cenci:agents-md-fallback -->'
ROOT="$(mktemp -d)" || { echo "mktemp failed" >&2; exit 2; }
trap 'rm -rf "${ROOT}"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

# new_fixture <name> -- an empty fixture directory under the shared mktemp root.
new_fixture() {
  mkdir -p "${ROOT}/$1" || fail "could not create fixture $1"
  printf '%s' "${ROOT}/$1"
}

# tree_sha <dir> -- the sha of every regular file under <dir>, sorted by path.
tree_sha() {
  local dir="$1" file
  while IFS= read -r file; do
    (cd "${dir}" && sha256sum "${file}") || fail "sha256sum failed for ${file}"
  done < <(cd "${dir}" && find . -type f | LC_ALL=C sort)
}

# Legacy cenci-generated adapter, byte for byte as the pre-#1448 template wrote it.
write_legacy_adapter() {
  printf '@AGENTS.md\n\n<!-- cenci:claude-only:start -->\n<!-- Optional Claude Code-only guidance belongs inside this block. -->\n<!-- cenci:claude-only:end -->\n' > "$1"
}

case_legacy_config_migration() {
  local d
  d="$(new_fixture legacy)"
  mkdir -p "${d}/.claude"
  printf '{"unknown":{"nested":true},"branchPattern":"old"}\n' > "${d}/.claude/config.json"
  "${SCRIPT}" "${d}" --apply >/dev/null
  jq -e '.unknown.nested == true' "${d}/.cenci/config.json" >/dev/null || fail "legacy: unknown key not preserved"

  d="$(new_fixture both)"
  mkdir -p "${d}/.claude" "${d}/.cenci"
  printf '{"unknown":{"legacy":1},"value":"legacy"}\n' > "${d}/.claude/config.json"
  printf '{"unknown":{"canonical":2},"value":"canonical","projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  "${SCRIPT}" "${d}" --apply >/dev/null
  jq -e '.value == "canonical" and .unknown.legacy == 1 and .unknown.canonical == 2' "${d}/.cenci/config.json" >/dev/null || fail "both: config merge wrong"
  test -f "${d}/apps/api/AGENTS.md" || fail "both: project AGENTS.md not written"
}

# M1: a lone substantive root CLAUDE.md ends up byte-identical in AGENTS.md and
# CLAUDE.md is gone.
case_M1_lone_substantive_migrates() {
  local d
  d="$(new_fixture m1)"
  printf '# Existing rules\n\n- keep this rule\n- and this one\n' > "${d}/CLAUDE.md"
  cp "${d}/CLAUDE.md" "${ROOT}/m1.original"
  "${SCRIPT}" "${d}" --apply >/dev/null
  cmp -s "${ROOT}/m1.original" "${d}/AGENTS.md" || fail "M1: AGENTS.md is not byte-identical to the original CLAUDE.md"
  [[ ! -e "${d}/CLAUDE.md" ]] || fail "M1: CLAUDE.md was not removed after migration"
}

# M2: removal happens only after AGENTS.md is written. The PATH shim wraps `install`,
# the tool migrate-project-core.sh writes AGENTS.md with, and fails only when its
# destination is an AGENTS.md; every other install call (the config write) passes
# through. If the script removed CLAUDE.md before, or regardless of, the AGENTS.md
# write, the sha check below would differ. The shim leaves a marker so a script that
# never reaches the shimmed write cannot pass this case vacuously.
case_M2_removal_only_after_agents_write() {
  local d shim real_install before after rc=0
  d="$(new_fixture m2)"
  shim="$(new_fixture m2-shim)"
  real_install="$(command -v install)" || fail "M2: install not found on PATH"
  printf '# Rules that must survive a failed write\n' > "${d}/CLAUDE.md"
  before="$(sha256sum "${d}/CLAUDE.md")"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'dest=""\n'
    printf 'for arg in "$@"; do dest="$arg"; done\n'
    printf 'case "${dest}" in\n'
    printf '  */AGENTS.md) : > "%s/hit"; echo "install: simulated AGENTS.md write failure" >&2; exit 1 ;;\n' "${shim}"
    printf 'esac\n'
    printf 'exec "%s" "$@"\n' "${real_install}"
  } > "${shim}/install"
  chmod +x "${shim}/install"
  PATH="${shim}:${PATH}" "${SCRIPT}" "${d}" --apply >/dev/null 2>&1 || rc=$?
  [[ -e "${shim}/hit" ]] || fail "M2: the shimmed install was never called for an AGENTS.md destination"
  [[ "${rc}" -ne 0 ]] || fail "M2: apply exited 0 although the AGENTS.md write failed"
  after="$(sha256sum "${d}/CLAUDE.md")" || fail "M2: CLAUDE.md is gone after a failed AGENTS.md write"
  [[ "${before}" == "${after}" ]] || fail "M2: CLAUDE.md changed after a failed AGENTS.md write"
}

# M3: an inert @AGENTS.md beside AGENTS.md is removed, and so is a project adapter
# whose block holds only the template comment line.
case_M3_inert_adapters_removed() {
  local d
  d="$(new_fixture m3)"
  mkdir -p "${d}/.cenci" "${d}/apps/api"
  printf '{"projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  printf '# Root guidance\n' > "${d}/AGENTS.md"
  printf '# Api guidance\n' > "${d}/apps/api/AGENTS.md"
  printf '@AGENTS.md\n' > "${d}/CLAUDE.md"
  write_legacy_adapter "${d}/apps/api/CLAUDE.md"
  "${SCRIPT}" "${d}" --apply >/dev/null
  [[ ! -e "${d}/CLAUDE.md" ]] || fail "M3: inert root @AGENTS.md adapter was not removed"
  [[ ! -e "${d}/apps/api/CLAUDE.md" ]] || fail "M3: project adapter with only the template comment was not removed"
  [[ "$(cat "${d}/AGENTS.md")" == '# Root guidance' ]] || fail "M3: root AGENTS.md changed"
  [[ "$(cat "${d}/apps/api/AGENTS.md")" == '# Api guidance' ]] || fail "M3: project AGENTS.md changed"
}

# M4: a substantive CLAUDE.md beside AGENTS.md stays byte-identical and its path is
# named in both the preview and the apply output.
case_M4_substantive_beside_agents_kept() {
  local d preview applied
  d="$(new_fixture m4)"
  mkdir -p "${d}/.cenci" "${d}/apps/api"
  printf '{"projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  printf '# Root guidance\n' > "${d}/AGENTS.md"
  printf '# Api guidance\n' > "${d}/apps/api/AGENTS.md"
  printf '@AGENTS.md\n\n- Claude-only rule that must not be lost\n' > "${d}/CLAUDE.md"
  printf '@AGENTS.md\n\n- Api-only rule that must not be lost\n' > "${d}/apps/api/CLAUDE.md"
  cp "${d}/CLAUDE.md" "${ROOT}/m4.root.original"
  cp "${d}/apps/api/CLAUDE.md" "${ROOT}/m4.api.original"
  preview="$("${SCRIPT}" "${d}")"
  applied="$("${SCRIPT}" "${d}" --apply)"
  cmp -s "${ROOT}/m4.root.original" "${d}/CLAUDE.md" || fail "M4: root CLAUDE.md changed"
  cmp -s "${ROOT}/m4.api.original" "${d}/apps/api/CLAUDE.md" || fail "M4: project CLAUDE.md changed"
  [[ "${preview}" == *"keep: ${d}/CLAUDE.md"* ]] || fail "M4: preview does not name the kept root CLAUDE.md"
  [[ "${preview}" == *"keep: ${d}/apps/api/CLAUDE.md"* ]] || fail "M4: preview does not name the kept project CLAUDE.md"
  [[ "${applied}" == *"keep: ${d}/CLAUDE.md"* ]] || fail "M4: apply output does not name the kept root CLAUDE.md"
  [[ "${applied}" == *"keep: ${d}/apps/api/CLAUDE.md"* ]] || fail "M4: apply output does not name the kept project CLAUDE.md"
}

# M5: copies of the shipped fallback template at the root and in a project stay
# byte-identical, even though the rest of their content is inert.
case_M5_fallback_adapters_kept() {
  local d
  grep -qxF "${FALLBACK_MARKER}" "${ADAPTER_TEMPLATE}" || fail "M5: the shipped template carries no fallback marker line"
  d="$(new_fixture m5)"
  mkdir -p "${d}/.cenci" "${d}/apps/api"
  printf '{"projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  printf '# Root guidance\n' > "${d}/AGENTS.md"
  printf '# Api guidance\n' > "${d}/apps/api/AGENTS.md"
  cp "${ADAPTER_TEMPLATE}" "${d}/CLAUDE.md"
  cp "${ADAPTER_TEMPLATE}" "${d}/apps/api/CLAUDE.md"
  "${SCRIPT}" "${d}" --apply >/dev/null
  cmp -s "${ADAPTER_TEMPLATE}" "${d}/CLAUDE.md" || fail "M5: root fallback adapter changed"
  cmp -s "${ADAPTER_TEMPLATE}" "${d}/apps/api/CLAUDE.md" || fail "M5: project fallback adapter changed"
}

# M6: a root CLAUDE.local.md is never modified or deleted.
case_M6_claude_local_untouched() {
  local d
  d="$(new_fixture m6)"
  printf '# Personal notes\n' > "${d}/AGENTS.md"
  printf '@AGENTS.md\n\n- my private claude setting\n' > "${d}/CLAUDE.local.md"
  cp "${d}/CLAUDE.local.md" "${ROOT}/m6.original"
  "${SCRIPT}" "${d}" --apply >/dev/null
  cmp -s "${ROOT}/m6.original" "${d}/CLAUDE.local.md" || fail "M6: CLAUDE.local.md changed or was removed"
}

# M7: a fixture with no CLAUDE.md gets none written at the root or in a project.
case_M7_no_claude_md_written() {
  local d
  d="$(new_fixture m7)"
  mkdir -p "${d}/.cenci"
  printf '{"projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  "${SCRIPT}" "${d}" --apply >/dev/null
  test -f "${d}/AGENTS.md" || fail "M7: root AGENTS.md not written"
  test -f "${d}/apps/api/AGENTS.md" || fail "M7: project AGENTS.md not written"
  [[ ! -e "${d}/CLAUDE.md" ]] || fail "M7: a root CLAUDE.md was written"
  [[ ! -e "${d}/apps/api/CLAUDE.md" ]] || fail "M7: a project CLAUDE.md was written"
}

# M8: preview mode leaves the tree unchanged, and a second --apply is a no-op (sha
# over every file, which covers the guidance files and the config).
case_M8_preview_pure_apply_idempotent() {
  local d before after
  d="$(new_fixture m8)"
  mkdir -p "${d}/.cenci" "${d}/apps/api"
  printf '{"projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  printf '# Root guidance\n' > "${d}/AGENTS.md"
  printf '@AGENTS.md\n' > "${d}/CLAUDE.md"
  printf '@AGENTS.md\n\n- Api-only rule\n' > "${d}/apps/api/CLAUDE.md"
  printf '# Personal\n' > "${d}/CLAUDE.local.md"
  before="$(tree_sha "${d}")"
  "${SCRIPT}" "${d}" >/dev/null
  after="$(tree_sha "${d}")"
  [[ "${before}" == "${after}" ]] || fail "M8: preview mode changed the tree"
  "${SCRIPT}" "${d}" --apply >/dev/null
  before="$(tree_sha "${d}")"
  "${SCRIPT}" "${d}" --apply >/dev/null
  after="$(tree_sha "${d}")"
  [[ "${before}" == "${after}" ]] || fail "M8: a second --apply changed the tree"
}

# write_install_shim <shim-dir> <dest-glob> <action> -- a PATH shim that wraps `install`,
# the tool migrate-project-core.sh writes AGENTS.md with. When the last argument (the
# destination) matches <dest-glob> the shim leaves a `hit` marker in <shim-dir>, then
# acts: `fail` exits 1 without writing, `truncate` empties the destination and exits 0
# (a write that reports success but did not happen), `record` only leaves the marker and
# passes through. Every other call, including the config write, passes through to the
# real install. The marker lets a case prove the shimmed call fired, so a script that
# never reaches it cannot pass vacuously.
write_install_shim() {
  local shim="$1" glob="$2" action="$3" real_install
  real_install="$(command -v install)" || fail "install not found on PATH"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'dest=""\n'
    printf 'for arg in "$@"; do dest="$arg"; done\n'
    printf 'case "${dest}" in\n'
    printf '  %s)\n' "${glob}"
    printf '    : > "%s/hit"\n' "${shim}"
    case "${action}" in
      fail) printf '    echo "install: simulated AGENTS.md write failure" >&2; exit 1 ;;\n' ;;
      truncate) printf '    : > "${dest}"; exit 0 ;;\n' ;;
      record) printf '    ;;\n' ;;
      *) fail "unknown shim action ${action}" ;;
    esac
    printf 'esac\n'
    printf 'exec "%s" "$@"\n' "${real_install}"
  } > "${shim}/install"
  chmod +x "${shim}/install"
}

# M9: an unreadable CLAUDE.md is never classified, so it is never removed. The script
# must report the path, exit 3 in preview and apply, and leave the whole tree untouched.
# The unreadable file is a DAC condition root cannot create, so the case skips as root.
case_M9_unreadable_claude_md_fails_closed() {
  local d before after rc mode
  d="$(new_fixture m9)"
  printf '@AGENTS.md\n' > "${d}/CLAUDE.md"
  before="$(tree_sha "${d}")"
  chmod 000 "${d}/CLAUDE.md"
  if [[ "$(id -u)" -eq 0 || -r "${d}/CLAUDE.md" ]]; then
    echo "SKIP: M9 (CLAUDE.md is still readable after chmod 000, running as a user that bypasses file modes)"
    chmod 644 "${d}/CLAUDE.md"
    return 0
  fi
  for mode in "" "--apply"; do
    rc=0
    if [[ -n "${mode}" ]]; then
      "${SCRIPT}" "${d}" "${mode}" >"${ROOT}/m9.out" 2>"${ROOT}/m9.err" || rc=$?
    else
      "${SCRIPT}" "${d}" >"${ROOT}/m9.out" 2>"${ROOT}/m9.err" || rc=$?
    fi
    [[ "${rc}" -eq 3 ]] || { chmod 644 "${d}/CLAUDE.md"; fail "M9 [${mode:-preview}]: expected exit 3 for an unreadable CLAUDE.md, got ${rc}"; }
    grep -qF "${d}/CLAUDE.md" "${ROOT}/m9.err" || { chmod 644 "${d}/CLAUDE.md"; fail "M9 [${mode:-preview}]: stderr does not name the unreadable CLAUDE.md"; }
  done
  chmod 644 "${d}/CLAUDE.md"
  after="$(tree_sha "${d}")"
  [[ "${before}" == "${after}" ]] || fail "M9: the tree changed although CLAUDE.md could not be read"
  [[ ! -e "${d}/AGENTS.md" && ! -e "${d}/.cenci" ]] || fail "M9: something was written although CLAUDE.md could not be read"
}

# M10: a project path that leaves ROOT is skipped with a visible `skip:` line on stderr
# in preview and apply, and nothing outside ROOT is written or removed. Covers a project
# directory that is a symlink out of ROOT, a symlinked parent of a not-yet-existing
# project, a `..` path and an absolute path; a valid project still migrates.
case_M10_projects_outside_root_skipped() {
  local d outside before after mode err
  d="$(new_fixture m10)"
  outside="$(new_fixture m10-outside)"
  printf '@AGENTS.md\n' > "${outside}/CLAUDE.md"
  printf 'unrelated\n' > "${outside}/keep.txt"
  mkdir -p "${d}/.cenci"
  ln -s "${outside}" "${d}/evil" || fail "M10: could not create the escaping symlink"
  ln -s "${outside}" "${d}/link" || fail "M10: could not create the parent symlink"
  printf '{"projects":[{"path":"evil"},{"path":"link/sub"},{"path":"../x"},{"path":"/abs/elsewhere"},{"path":"."},{"path":"good"}]}\n' > "${d}/.cenci/config.json"
  before="$(tree_sha "${outside}")"
  for mode in "" "--apply"; do
    if [[ -n "${mode}" ]]; then
      err="$("${SCRIPT}" "${d}" "${mode}" 2>&1 >/dev/null)" || fail "M10 [apply]: the script failed instead of skipping"
    else
      err="$("${SCRIPT}" "${d}" 2>&1 >/dev/null)" || fail "M10 [preview]: the script failed instead of skipping"
    fi
    [[ "${err}" == *"skip: evil ("* ]] || fail "M10 [${mode:-preview}]: no skip line for the symlinked project"
    [[ "${err}" == *"skip: link/sub ("* ]] || fail "M10 [${mode:-preview}]: no skip line for the project under a symlinked parent"
    [[ "${err}" == *"skip: ../x ("* ]] || fail "M10 [${mode:-preview}]: no skip line for the .. project"
    [[ "${err}" == *"skip: /abs/elsewhere ("* ]] || fail "M10 [${mode:-preview}]: no skip line for the absolute project"
    [[ "${err}" == *"skip: . ("* ]] || fail "M10 [${mode:-preview}]: no skip line for the root project"
    after="$(tree_sha "${outside}")"
    [[ "${before}" == "${after}" ]] || fail "M10 [${mode:-preview}]: a file outside ROOT was written or removed"
  done
  [[ -f "${outside}/CLAUDE.md" && ! -e "${outside}/AGENTS.md" ]] || fail "M10: the outside directory was touched"
  [[ -f "${d}/good/AGENTS.md" ]] || fail "M10: the valid project was not migrated"
}

# M11: a malformed `projects` entry stops the run with exit 3 and a message, in apply,
# instead of silently dropping the rest of the list and exiting 0. Nothing is written.
case_M11_malformed_projects_fail_closed() {
  local d rc config
  # Path-less entries ({"slug":"x"}, null, false) must stop the run exactly like an empty
  # path: check.sh fails the same entries, so silently skipping them hid the mismatch.
  for config in '{"projects":["apps/api"]}' '{"projects":[{"path":"apps/api"},"oops",{"path":"apps/web"}]}' '{"projects":"apps/api"}' '{"projects":[{"path":""}]}' '{"projects":[{"slug":"x"}]}' '{"projects":[{"path":null}]}' '{"projects":[{"path":false}]}'; do
    d="$(new_fixture "m11-${RANDOM}")"
    mkdir -p "${d}/.cenci"
    printf '%s\n' "${config}" > "${d}/.cenci/config.json"
    rc=0
    "${SCRIPT}" "${d}" --apply >"${ROOT}/m11.out" 2>"${ROOT}/m11.err" || rc=$?
    [[ "${rc}" -eq 3 ]] || fail "M11: expected exit 3 for ${config}, got ${rc}"
    [[ -s "${ROOT}/m11.err" ]] || fail "M11: no message on stderr for ${config}"
    [[ ! -e "${d}/AGENTS.md" ]] || fail "M11: AGENTS.md written despite ${config}"
  done
}

# M12: an unknown second argument is refused with exit 2 and changes nothing.
case_M12_unknown_mode_rejected() {
  local d rc before after
  d="$(new_fixture m12)"
  printf '@AGENTS.md\n' > "${d}/CLAUDE.md"
  before="$(tree_sha "${d}")"
  rc=0
  "${SCRIPT}" "${d}" --aply >"${ROOT}/m12.out" 2>"${ROOT}/m12.err" || rc=$?
  [[ "${rc}" -eq 2 ]] || fail "M12: expected exit 2 for --aply, got ${rc}"
  grep -qF -- '--aply' "${ROOT}/m12.err" || fail "M12: stderr does not name the rejected argument"
  after="$(tree_sha "${d}")"
  [[ "${before}" == "${after}" ]] || fail "M12: --aply changed the tree"
  [[ ! -e "${d}/AGENTS.md" && ! -e "${d}/.cenci" ]] || fail "M12: --aply wrote files"
}

# M13: the script refuses (exit 3, naming the path) to copy from, or write over, a
# symlink: an AGENTS.md symlink, a dangling AGENTS.md symlink, and a lone substantive
# CLAUDE.md that is a symlink. The link targets and the links themselves stay as they were.
case_M13_symlinked_sources_refused() {
  local d target rc
  target="${ROOT}/m13-target.md"
  printf '# Target guidance\n' > "${target}"

  d="$(new_fixture m13-agents)"
  ln -s "${target}" "${d}/AGENTS.md" || fail "M13: could not create the AGENTS.md symlink"
  rc=0
  "${SCRIPT}" "${d}" --apply >"${ROOT}/m13.out" 2>"${ROOT}/m13.err" || rc=$?
  [[ "${rc}" -eq 3 ]] || fail "M13 [AGENTS.md symlink]: expected exit 3, got ${rc}"
  grep -qF "${d}/AGENTS.md" "${ROOT}/m13.err" || fail "M13 [AGENTS.md symlink]: stderr does not name the path"
  [[ -L "${d}/AGENTS.md" ]] || fail "M13 [AGENTS.md symlink]: the symlink was replaced"
  [[ "$(cat "${target}")" == '# Target guidance' ]] || fail "M13 [AGENTS.md symlink]: the link target changed"
  [[ ! -e "${d}/.cenci" ]] || fail "M13 [AGENTS.md symlink]: config was written despite the refusal"

  d="$(new_fixture m13-dangling)"
  ln -s "${ROOT}/m13-missing.md" "${d}/AGENTS.md" || fail "M13: could not create the dangling symlink"
  rc=0
  "${SCRIPT}" "${d}" --apply >"${ROOT}/m13.out" 2>"${ROOT}/m13.err" || rc=$?
  [[ "${rc}" -eq 3 ]] || fail "M13 [dangling AGENTS.md]: expected exit 3, got ${rc}"
  [[ -L "${d}/AGENTS.md" && ! -e "${ROOT}/m13-missing.md" ]] || fail "M13 [dangling AGENTS.md]: the dangling symlink was replaced or followed"

  d="$(new_fixture m13-claude)"
  printf '# Substantive rules behind a link\n\n- rule\n' > "${ROOT}/m13-claude-target.md"
  ln -s "${ROOT}/m13-claude-target.md" "${d}/CLAUDE.md" || fail "M13: could not create the CLAUDE.md symlink"
  rc=0
  "${SCRIPT}" "${d}" --apply >"${ROOT}/m13.out" 2>"${ROOT}/m13.err" || rc=$?
  [[ "${rc}" -eq 3 ]] || fail "M13 [CLAUDE.md symlink]: expected exit 3, got ${rc}"
  grep -qF "${d}/CLAUDE.md" "${ROOT}/m13.err" || fail "M13 [CLAUDE.md symlink]: stderr does not name the path"
  [[ -L "${d}/CLAUDE.md" && ! -e "${d}/AGENTS.md" ]] || fail "M13 [CLAUDE.md symlink]: the symlink was removed or AGENTS.md written"
}

# M14: the cmp guard. The PATH shim wraps `install` and, for an AGENTS.md destination,
# empties the file and exits 0, a write that reports success but did not happen. The
# script must notice (exit 1, "AGENTS.md write could not be verified") and must not
# remove CLAUDE.md. Delete the cmp check and this case fails.
case_M14_unverified_write_keeps_claude_md() {
  local d shim before after rc=0
  d="$(new_fixture m14)"
  shim="$(new_fixture m14-shim)"
  printf '# Rules that must survive a silently failed write\n' > "${d}/CLAUDE.md"
  before="$(sha256sum "${d}/CLAUDE.md")"
  write_install_shim "${shim}" '*/AGENTS.md' truncate
  PATH="${shim}:${PATH}" "${SCRIPT}" "${d}" --apply >"${ROOT}/m14.out" 2>"${ROOT}/m14.err" || rc=$?
  [[ -e "${shim}/hit" ]] || fail "M14: the shimmed install was never called for an AGENTS.md destination"
  [[ "${rc}" -eq 1 ]] || fail "M14: expected exit 1 for an unverifiable write, got ${rc}"
  grep -qF 'AGENTS.md write could not be verified' "${ROOT}/m14.err" || fail "M14: stderr lacks the verification message"
  after="$(sha256sum "${d}/CLAUDE.md")" || fail "M14: CLAUDE.md was removed after an unverified AGENTS.md write"
  [[ "${before}" == "${after}" ]] || fail "M14: CLAUDE.md changed after an unverified AGENTS.md write"
}

# M15: ordering across directories. The root CLAUDE.md is inert and its AGENTS.md write
# succeeds, but the project's AGENTS.md write fails. The PATH shim wraps `install` and
# fails only for the project's destination. The root CLAUDE.md must still exist, because
# no CLAUDE.md is removed until every AGENTS.md write is confirmed.
case_M15_removal_waits_for_every_agents_write() {
  local d shim rc=0
  d="$(new_fixture m15)"
  shim="$(new_fixture m15-shim)"
  mkdir -p "${d}/.cenci"
  printf '{"projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  printf '@AGENTS.md\n' > "${d}/CLAUDE.md"
  write_install_shim "${shim}" '*/apps/api/AGENTS.md' fail
  PATH="${shim}:${PATH}" "${SCRIPT}" "${d}" --apply >/dev/null 2>&1 || rc=$?
  [[ -e "${shim}/hit" ]] || fail "M15: the shimmed install was never called for the project's AGENTS.md"
  [[ "${rc}" -ne 0 ]] || fail "M15: apply exited 0 although the project's AGENTS.md write failed"
  [[ -f "${d}/AGENTS.md" ]] || fail "M15: the root AGENTS.md was not written before the failing project write"
  [[ -f "${d}/CLAUDE.md" ]] || fail "M15: the root CLAUDE.md was removed before every AGENTS.md write was confirmed"
}

# M16: an AGENTS.md that already equals the planned content is not rewritten. The PATH
# shim records every `install` call for an AGENTS.md destination and passes through: the
# first apply (a placeholder is planned) must hit it, the second must not.
case_M16_identical_agents_not_rewritten() {
  local d shim
  d="$(new_fixture m16)"
  shim="$(new_fixture m16-shim)"
  write_install_shim "${shim}" '*/AGENTS.md' record
  PATH="${shim}:${PATH}" "${SCRIPT}" "${d}" --apply >/dev/null 2>&1 || fail "M16: the first apply failed"
  [[ -e "${shim}/hit" ]] || fail "M16: the first apply never installed AGENTS.md, so the case proves nothing"
  rm -f "${shim}/hit"
  PATH="${shim}:${PATH}" "${SCRIPT}" "${d}" --apply >/dev/null 2>&1 || fail "M16: the second apply failed"
  [[ ! -e "${shim}/hit" ]] || fail "M16: the second apply rewrote an AGENTS.md that already held the planned content"
}

# M17: a fallback adapter with CRLF line endings is still a fallback adapter. The marker
# line ends in \r, so a scan that does not strip it would classify the file substantive
# and, with no AGENTS.md beside it, migrate and remove the opted-in adapter.
case_M17_crlf_fallback_kept() {
  local d out
  d="$(new_fixture m17)"
  printf '%s\r\n@AGENTS.md\r\n' "${FALLBACK_MARKER}" > "${d}/CLAUDE.md"
  cp "${d}/CLAUDE.md" "${ROOT}/m17.original"
  out="$("${SCRIPT}" "${d}" --apply 2>&1)" || fail "M17: apply failed"
  cmp -s "${ROOT}/m17.original" "${d}/CLAUDE.md" || fail "M17: the CRLF fallback adapter was changed or removed"
  [[ "${out}" == *"keep: ${d}/CLAUDE.md"* ]] || fail "M17: apply output does not name the kept CRLF fallback adapter"
  [[ "${out}" != *"remove: ${d}/CLAUDE.md"* ]] || fail "M17: the CRLF fallback adapter was planned for removal"
  [[ "$(cat "${d}/AGENTS.md")" == '# Project Guidance' ]] || fail "M17: the CRLF fallback adapter was migrated into AGENTS.md"
}

# M18: a project path that starts with a tab is rejected. The plan file is tab separated,
# so the tab used to vanish on re-read: the path was judged as <root>/<tab>link (absent, so
# accepted) but used as <root>/link, a committed symlink out of ROOT that was never
# checked. The project is skipped with a `skip:` line and nothing outside ROOT is
# previewed, read, written or removed.
case_M18_control_char_project_skipped() {
  local d outside before after mode out err
  d="$(new_fixture m18)"
  outside="$(new_fixture m18-outside)"
  printf '@AGENTS.md\n' > "${outside}/CLAUDE.md"
  printf 'unrelated\n' > "${outside}/keep.txt"
  mkdir -p "${d}/.cenci"
  ln -s "${outside}" "${d}/link" || fail "M18: could not create the escaping symlink"
  printf '{"projects":[{"path":"\\tlink"}]}\n' > "${d}/.cenci/config.json"
  before="$(tree_sha "${outside}")"
  for mode in --preview --apply; do
    "${SCRIPT}" "${d}" "${mode}" >"${ROOT}/m18.out" 2>"${ROOT}/m18.err" || fail "M18 [${mode}]: the script failed instead of skipping"
    out="$(cat "${ROOT}/m18.out")" || fail "M18: cannot read stdout"
    err="$(cat "${ROOT}/m18.err")" || fail "M18: cannot read stderr"
    [[ "${err}" == *"skip: "* ]] || fail "M18 [${mode}]: no skip line for the tab-prefixed project"
    [[ "${out}" != *"${outside}"* && "${err}" != *"${outside}"* ]] || fail "M18 [${mode}]: output names a path outside ROOT"
    [[ "${out}" != *"link/AGENTS.md"* ]] || fail "M18 [${mode}]: the symlinked project was planned"
    after="$(tree_sha "${outside}")"
    [[ "${before}" == "${after}" ]] || fail "M18 [${mode}]: a file outside ROOT was written or removed"
  done
  [[ -f "${outside}/CLAUDE.md" && ! -e "${outside}/AGENTS.md" ]] || fail "M18: the outside directory was touched"
}

# M19: a project path holding a line terminator (LF, CR) or a NUL is one malformed
# entry, not two projects. jq -r prints "a\nb" on two lines and the read loop used to plan
# "a" and "b" as separate projects (both real directories here, their inert adapters
# removed on --apply). The jq program rejects the entry, so the run stops with exit 3 and
# a message, and nothing is planned, previewed or written. Other control characters (the
# tab in M18) keep their per-project `skip:` handling.
case_M19_line_terminator_project_rejected() {
  local d rc mode path
  for path in 'a\nb' 'a\rb' 'a\u0000b'; do
    for mode in --preview --apply; do
      d="$(new_fixture "m19-${RANDOM}")"
      mkdir -p "${d}/.cenci" "${d}/a" "${d}/b"
      printf '@AGENTS.md\n' > "${d}/a/CLAUDE.md"
      printf '@AGENTS.md\n' > "${d}/b/CLAUDE.md"
      printf '{"projects":[{"path":"%s"}]}\n' "${path}" > "${d}/.cenci/config.json"
      rc=0
      "${SCRIPT}" "${d}" "${mode}" >"${ROOT}/m19.out" 2>"${ROOT}/m19.err" || rc=$?
      [[ "${rc}" -eq 3 ]] || fail "M19 [${path} ${mode}]: expected exit 3, got ${rc}"
      [[ -s "${ROOT}/m19.err" ]] || fail "M19 [${path} ${mode}]: no message on stderr"
      grep -qE 'a/AGENTS\.md|b/AGENTS\.md' "${ROOT}/m19.out" && fail "M19 [${path} ${mode}]: a or b was planned"
      [[ ! -e "${d}/a/AGENTS.md" && ! -e "${d}/b/AGENTS.md" ]] || fail "M19 [${path} ${mode}]: AGENTS.md written for a or b"
      [[ -f "${d}/a/CLAUDE.md" && -f "${d}/b/CLAUDE.md" ]] || fail "M19 [${path} ${mode}]: a CLAUDE.md of a or b was removed"
      [[ ! -e "${d}/AGENTS.md" ]] || fail "M19 [${path} ${mode}]: the root AGENTS.md was written despite the rejection"
    done
  done
}

# --- #1509: a CLAUDE.md is inert only when it is exactly one of three shapes -----------
# After CR, trailing whitespace and blank lines are stripped, the lines must be exactly
#   (a) @AGENTS.md
#   (b) @AGENTS.md, the start marker, the end marker
#   (c) @AGENTS.md, the start marker, the template comment, the end marker
# Everything else is substantive, including an empty file. M20-M23 are the inert shapes,
# M24-M33 the near-misses the old per-line allow-list wrongly accepted, M34-M37 the edges.
IMPORT_LINE='@AGENTS.md'
START_MARKER='<!-- cenci:claude-only:start -->'
END_MARKER='<!-- cenci:claude-only:end -->'
TEMPLATE_COMMENT='<!-- Optional Claude Code-only guidance belongs inside this block. -->'

# fixture_beside_agents <name> -- a new fixture holding an AGENTS.md; its path is left in
# FIXTURE_DIR (not printed), so the helper can call fail outside any command substitution.
FIXTURE_DIR=""
fixture_beside_agents() {
  FIXTURE_DIR="$(new_fixture "$1")"
  printf '# Root guidance\n' > "${FIXTURE_DIR}/AGENTS.md" || fail "could not write AGENTS.md in fixture $1"
}

# expect_inert_removed <label> <dir> -- the preview names the CLAUDE.md as removed and
# leaves it in place; --apply deletes it and leaves AGENTS.md unchanged.
expect_inert_removed() {
  local label="$1" dir="$2" agents_before agents_after
  agents_before="$(sha256sum "${dir}/AGENTS.md")" || fail "${label}: cannot hash AGENTS.md"
  "${SCRIPT}" "${dir}" >"${ROOT}/${label}.preview" 2>"${ROOT}/${label}.preview.err" || fail "${label}: preview failed"
  grep -qxF "remove: ${dir}/CLAUDE.md" "${ROOT}/${label}.preview" || fail "${label}: preview does not plan to remove the CLAUDE.md"
  [[ -f "${dir}/CLAUDE.md" ]] || fail "${label}: preview removed the CLAUDE.md"
  "${SCRIPT}" "${dir}" --apply >/dev/null 2>&1 || fail "${label}: apply failed"
  [[ ! -e "${dir}/CLAUDE.md" ]] || fail "${label}: apply did not delete the inert CLAUDE.md"
  agents_after="$(sha256sum "${dir}/AGENTS.md")" || fail "${label}: AGENTS.md is gone after apply"
  [[ "${agents_before}" == "${agents_after}" ]] || fail "${label}: AGENTS.md changed"
}

# expect_substantive_kept <label> <dir> -- the preview names the CLAUDE.md as kept and
# never as removed; its sha256 is the same after --apply.
expect_substantive_kept() {
  local label="$1" dir="$2" claude_before claude_after
  claude_before="$(sha256sum "${dir}/CLAUDE.md")" || fail "${label}: cannot hash CLAUDE.md"
  "${SCRIPT}" "${dir}" >"${ROOT}/${label}.preview" 2>"${ROOT}/${label}.preview.err" || fail "${label}: preview failed"
  grep -qxF "keep: ${dir}/CLAUDE.md" "${ROOT}/${label}.preview" || fail "${label}: preview does not keep the CLAUDE.md"
  if grep -qxF "remove: ${dir}/CLAUDE.md" "${ROOT}/${label}.preview"; then
    fail "${label}: preview plans to remove a substantive CLAUDE.md"
  fi
  "${SCRIPT}" "${dir}" --apply >/dev/null 2>&1 || fail "${label}: apply failed"
  claude_after="$(sha256sum "${dir}/CLAUDE.md")" || fail "${label}: the substantive CLAUDE.md was deleted by apply"
  [[ "${claude_before}" == "${claude_after}" ]] || fail "${label}: the substantive CLAUDE.md changed"
}

# M20: shape (a), a bare import, is removed.
case_M20_bare_import_removed() {
  fixture_beside_agents m20
  printf '%s\n' "${IMPORT_LINE}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_inert_removed M20 "${FIXTURE_DIR}"
}

# M21: shape (b), the import plus an empty block, is removed.
case_M21_empty_block_removed() {
  fixture_beside_agents m21
  printf '%s\n%s\n%s\n' "${IMPORT_LINE}" "${START_MARKER}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_inert_removed M21 "${FIXTURE_DIR}"
}

# M22: shape (c), the import plus a block holding only the template comment, is removed.
case_M22_template_block_removed() {
  fixture_beside_agents m22
  printf '%s\n%s\n%s\n%s\n' "${IMPORT_LINE}" "${START_MARKER}" "${TEMPLATE_COMMENT}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_inert_removed M22 "${FIXTURE_DIR}"
}

# M23: blank lines between and around the lines, blank lines holding spaces or tabs, and
# trailing spaces and tabs after every line do not change the shape.
case_M23_blank_lines_and_trailing_whitespace_ignored() {
  fixture_beside_agents m23-c
  printf '\n\n%s  \n\n \t \n%s\t\n%s \t \n\n%s   \n\n\n' "${IMPORT_LINE}" "${START_MARKER}" "${TEMPLATE_COMMENT}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_inert_removed M23-c "${FIXTURE_DIR}"
  fixture_beside_agents m23-b
  printf '%s\t\n\n  \n%s \n\n%s\t \t\n' "${IMPORT_LINE}" "${START_MARKER}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_inert_removed M23-b "${FIXTURE_DIR}"
}

# M24: an empty (0-byte) file is substantive.
case_M24_empty_file_kept() {
  fixture_beside_agents m24
  : > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M24 "${FIXTURE_DIR}"
}

# M25: a file of only whitespace and blank lines is substantive.
case_M25_whitespace_only_kept() {
  fixture_beside_agents m25
  printf ' \t\n\n   \n\t\n' > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M25 "${FIXTURE_DIR}"
}

# M26: a lone start marker, with no import and no end marker, is substantive. This is the
# reproduction from #1509: the old per-line allow-list accepted it and planned its removal.
case_M26_start_marker_only_kept() {
  fixture_beside_agents m26
  printf '%s\n' "${START_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M26 "${FIXTURE_DIR}"
}

# M27: the import followed by a start marker with no end marker is substantive.
case_M27_start_without_end_kept() {
  fixture_beside_agents m27
  printf '%s\n%s\n' "${IMPORT_LINE}" "${START_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M27 "${FIXTURE_DIR}"
}

# M28: the import followed by an end marker with no start marker is substantive.
case_M28_end_without_start_kept() {
  fixture_beside_agents m28
  printf '%s\n%s\n' "${IMPORT_LINE}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M28 "${FIXTURE_DIR}"
}

# M29: the import followed by the end marker before the start marker is substantive.
case_M29_end_before_start_kept() {
  fixture_beside_agents m29
  printf '%s\n%s\n%s\n' "${IMPORT_LINE}" "${END_MARKER}" "${START_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M29 "${FIXTURE_DIR}"
}

# M30: the import line twice is substantive.
case_M30_import_twice_kept() {
  fixture_beside_agents m30
  printf '%s\n%s\n' "${IMPORT_LINE}" "${IMPORT_LINE}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M30 "${FIXTURE_DIR}"
}

# M31: the template comment directly after the import, with no block, is substantive.
case_M31_template_comment_outside_block_kept() {
  fixture_beside_agents m31
  printf '%s\n%s\n' "${IMPORT_LINE}" "${TEMPLATE_COMMENT}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M31 "${FIXTURE_DIR}"
}

# M32: two blocks after the import are substantive.
case_M32_two_blocks_kept() {
  fixture_beside_agents m32
  printf '%s\n%s\n%s\n%s\n%s\n' "${IMPORT_LINE}" "${START_MARKER}" "${END_MARKER}" "${START_MARKER}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M32 "${FIXTURE_DIR}"
}

# M33: the template comment twice inside one block is substantive (at most once).
case_M33_template_comment_twice_kept() {
  fixture_beside_agents m33
  printf '%s\n%s\n%s\n%s\n%s\n' "${IMPORT_LINE}" "${START_MARKER}" "${TEMPLATE_COMMENT}" "${TEMPLATE_COMMENT}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M33 "${FIXTURE_DIR}"
}

# M34: with no AGENTS.md beside it, a lone marker-only CLAUDE.md, and a lone empty one,
# take the lone-substantive path: AGENTS.md is a byte-identical copy and CLAUDE.md is gone.
case_M34_lone_marker_only_and_empty_migrate_verbatim() {
  local d
  d="${ROOT}/m34-marker"
  mkdir -p "${d}" || fail "M34: could not create fixture m34-marker"
  printf '%s\n' "${START_MARKER}" > "${d}/CLAUDE.md"
  cp "${d}/CLAUDE.md" "${ROOT}/m34-marker.original"
  "${SCRIPT}" "${d}" --apply >/dev/null 2>&1 || fail "M34 [marker only]: apply failed"
  cmp -s "${ROOT}/m34-marker.original" "${d}/AGENTS.md" || fail "M34 [marker only]: AGENTS.md is not byte-identical to the original CLAUDE.md"
  [[ ! -e "${d}/CLAUDE.md" ]] || fail "M34 [marker only]: CLAUDE.md was not removed"

  d="${ROOT}/m34-empty"
  mkdir -p "${d}" || fail "M34: could not create fixture m34-empty"
  : > "${d}/CLAUDE.md"
  : > "${ROOT}/m34-empty.original"
  "${SCRIPT}" "${d}" --apply >/dev/null 2>&1 || fail "M34 [empty]: apply failed"
  [[ -f "${d}/AGENTS.md" ]] || fail "M34 [empty]: AGENTS.md was not written"
  cmp -s "${ROOT}/m34-empty.original" "${d}/AGENTS.md" || fail "M34 [empty]: AGENTS.md is not a 0-byte copy of the empty CLAUDE.md"
  [[ ! -e "${d}/CLAUDE.md" ]] || fail "M34 [empty]: CLAUDE.md was not removed"
}

# M35: @../AGENTS.md is the import of configure's .claude/CLAUDE.md only; in a root
# CLAUDE.md it is substantive.
case_M35_root_parent_import_kept() {
  fixture_beside_agents m35
  printf '@../AGENTS.md\n' > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M35 "${FIXTURE_DIR}"
}

# M36: an import with leading whitespace is substantive (only trailing whitespace is ignored).
case_M36_indented_import_kept() {
  fixture_beside_agents m36
  printf '  @AGENTS.md\n' > "${FIXTURE_DIR}/CLAUDE.md"
  expect_substantive_kept M36 "${FIXTURE_DIR}"
}

# M37: shape (c) with CRLF line endings is still inert and is removed.
case_M37_crlf_template_block_removed() {
  fixture_beside_agents m37
  printf '%s\r\n\r\n%s\r\n%s\r\n%s\r\n' "${IMPORT_LINE}" "${START_MARKER}" "${TEMPLATE_COMMENT}" "${END_MARKER}" > "${FIXTURE_DIR}/CLAUDE.md"
  expect_inert_removed M37 "${FIXTURE_DIR}"
}

# M38: a root CLAUDE.local.md is never listed. The fixture also holds an inert root
# CLAUDE.md (a remove: line) and a substantive project CLAUDE.md (a keep: line), so the
# output is non-trivial; neither the --preview nor the --apply output (stdout and stderr)
# mentions CLAUDE.local.md on any line, and its sha256 is unchanged after --apply.
case_M38_claude_local_never_listed() {
  local d mode before after
  d="$(new_fixture m38)"
  mkdir -p "${d}/.cenci" "${d}/apps/api"
  printf '{"projects":[{"path":"apps/api"}]}\n' > "${d}/.cenci/config.json"
  printf '# Root guidance\n' > "${d}/AGENTS.md"
  printf '# Api guidance\n' > "${d}/apps/api/AGENTS.md"
  printf '%s\n' "${IMPORT_LINE}" > "${d}/CLAUDE.md"
  printf '@AGENTS.md\n\n- Api-only rule that must not be lost\n' > "${d}/apps/api/CLAUDE.md"
  printf '@AGENTS.md\n\n- my private claude setting\n' > "${d}/CLAUDE.local.md"
  before="$(sha256sum "${d}/CLAUDE.local.md")" || fail "M38: cannot hash CLAUDE.local.md"
  for mode in --preview --apply; do
    "${SCRIPT}" "${d}" "${mode}" >"${ROOT}/m38${mode}.out" 2>&1 || fail "M38 [${mode}]: the script failed"
    grep -qxF "remove: ${d}/CLAUDE.md" "${ROOT}/m38${mode}.out" || fail "M38 [${mode}]: output has no remove: line, so the case proves nothing"
    grep -qxF "keep: ${d}/apps/api/CLAUDE.md" "${ROOT}/m38${mode}.out" || fail "M38 [${mode}]: output has no keep: line, so the case proves nothing"
    if grep -qF 'CLAUDE.local.md' "${ROOT}/m38${mode}.out"; then
      fail "M38 [${mode}]: output mentions CLAUDE.local.md"
    fi
  done
  after="$(sha256sum "${d}/CLAUDE.local.md")" || fail "M38: CLAUDE.local.md is gone after apply"
  [[ "${before}" == "${after}" ]] || fail "M38: CLAUDE.local.md changed after apply"
}

case_legacy_config_migration
case_M1_lone_substantive_migrates
case_M2_removal_only_after_agents_write
case_M3_inert_adapters_removed
case_M4_substantive_beside_agents_kept
case_M5_fallback_adapters_kept
case_M6_claude_local_untouched
case_M7_no_claude_md_written
case_M8_preview_pure_apply_idempotent
case_M9_unreadable_claude_md_fails_closed
case_M10_projects_outside_root_skipped
case_M11_malformed_projects_fail_closed
case_M12_unknown_mode_rejected
case_M13_symlinked_sources_refused
case_M14_unverified_write_keeps_claude_md
case_M15_removal_waits_for_every_agents_write
case_M16_identical_agents_not_rewritten
case_M17_crlf_fallback_kept
case_M18_control_char_project_skipped
case_M19_line_terminator_project_rejected
case_M20_bare_import_removed
case_M21_empty_block_removed
case_M22_template_block_removed
case_M23_blank_lines_and_trailing_whitespace_ignored
case_M24_empty_file_kept
case_M25_whitespace_only_kept
case_M26_start_marker_only_kept
case_M27_start_without_end_kept
case_M28_end_without_start_kept
case_M29_end_before_start_kept
case_M30_import_twice_kept
case_M31_template_comment_outside_block_kept
case_M32_two_blocks_kept
case_M33_template_comment_twice_kept
case_M34_lone_marker_only_and_empty_migrate_verbatim
case_M35_root_parent_import_kept
case_M36_indented_import_kept
case_M37_crlf_template_block_removed
case_M38_claude_local_never_listed

echo "migrate-project-core.test.sh: passed"
